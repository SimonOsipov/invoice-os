package auth

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

// ErrUnauthorized is the single error every rejection wraps. Middleware maps it
// to a 401; callers must not surface the wrapped reason to clients.
var ErrUnauthorized = errors.New("auth: unauthorized")

// errStaleKey is an internal sentinel: the signing key was missing from — or did
// not verify against — the cached JWKS. It drives exactly one refetch-and-retry
// (the key-rotation path) and is never returned to callers.
var errStaleKey = errors.New("auth: signing key not in cached jwks")

const (
	defaultCacheTTL     = time.Hour
	defaultHTTPTimeout  = 10 * time.Second
	defaultAudience     = "authenticated"
	maxJWKSResponseSize = 1 << 20 // 1 MiB
)

// Config configures a Verifier. Issuer and JWKSURL are the only values that
// differ between the mock issuer (dev/CI) and Supabase GoTrue (M8), so the M8
// cutover is a config change rather than a code change.
type Config struct {
	Issuer     string        // required: expected "iss"
	JWKSURL    string        // required: where the signing public keys are served
	Audience   string        // expected "aud"; defaults to "authenticated"
	CacheTTL   time.Duration // JWKS cache lifetime; defaults to 1h
	HTTPClient *http.Client  // JWKS fetch client; defaults to a 10s-timeout client
	Logger     *slog.Logger  // defaults to slog.Default()
}

// Verifier validates GoTrue-shaped JWTs against a configured issuer and its
// JWKS. It caches the key set with a TTL and, on a signature/kid failure while
// using cached keys, refetches once to ride out key rotation.
type Verifier struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger

	mu        sync.RWMutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time

	now func() time.Time // overridable in tests
}

// NewVerifier validates the config and returns a ready Verifier.
func NewVerifier(cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("auth: Config.Issuer is required")
	}
	if cfg.JWKSURL == "" {
		return nil, fmt.Errorf("auth: Config.JWKSURL is required")
	}
	if cfg.Audience == "" {
		cfg.Audience = defaultAudience
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = defaultCacheTTL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Verifier{cfg: cfg, http: cfg.HTTPClient, log: cfg.Logger, now: time.Now}, nil
}

// Verify checks a token's signature and claims and returns the caller identity.
// Every failure returns an error wrapping ErrUnauthorized with no distinguishing
// detail, so middleware can answer 401 without leaking why.
func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	id, err := v.verifyWith(ctx, token, false)
	if errors.Is(err, errStaleKey) {
		// Cached keys were stale (rotation): refetch once and retry.
		id, err = v.verifyWith(ctx, token, true)
	}
	if err != nil {
		if errors.Is(err, errStaleKey) {
			// Retry also failed to find the key: report as a plain rejection.
			return Identity{}, fmt.Errorf("%w: unknown signing key", ErrUnauthorized)
		}
		return Identity{}, err
	}
	return id, nil
}

func (v *Verifier) verifyWith(ctx context.Context, token string, forceRefresh bool) (Identity, error) {
	keys, usedCache, err := v.jwksKeys(ctx, forceRefresh)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: jwks: %v", ErrUnauthorized, err)
	}

	keyfunc := func(t *jwt.Token) (interface{}, error) {
		switch t.Method.(type) {
		case *jwt.SigningMethodECDSA, *jwt.SigningMethodRSA:
		default:
			return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
		}
		kid, _ := t.Header["kid"].(string)
		key, ok := keys[kid]
		if !ok {
			return nil, errStaleKey
		}
		return key, nil
	}

	var claims gotrueClaims
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"ES256", "RS256"}))
	if _, err := parser.ParseWithClaims(token, &claims, keyfunc); err != nil {
		// A missing kid or a bad signature while using cached keys is the
		// rotation signal; surface it so Verify refetches and retries once.
		if usedCache && (errors.Is(err, errStaleKey) || isSignatureError(err)) {
			return Identity{}, errStaleKey
		}
		return Identity{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}

	return v.validate(&claims)
}

// validate enforces the claim contract beyond signature and expiry (expiry is
// checked by gotrueClaims.Valid during parsing).
func (v *Verifier) validate(c *gotrueClaims) (Identity, error) {
	if c.Issuer != v.cfg.Issuer {
		return Identity{}, fmt.Errorf("%w: bad issuer", ErrUnauthorized)
	}
	if string(c.Audience) != v.cfg.Audience {
		return Identity{}, fmt.Errorf("%w: bad audience", ErrUnauthorized)
	}
	if _, err := uuid.Parse(c.Subject); err != nil {
		return Identity{}, fmt.Errorf("%w: subject is not a uuid", ErrUnauthorized)
	}
	if c.Role == "" {
		return Identity{}, fmt.Errorf("%w: missing role", ErrUnauthorized)
	}
	// tenant_id is extracted but not required here; the tenant-context layer
	// (M2-06) decides what to do when it is absent.
	return Identity{Subject: c.Subject, Role: c.Role, TenantID: c.AppMetadata.TenantID}, nil
}

// jwksKeys returns the current key set. Unless forceRefresh is set it serves a
// fresh cache; usedCache reports whether the returned keys came from the cache
// (only then is a rotation retry meaningful).
func (v *Verifier) jwksKeys(ctx context.Context, forceRefresh bool) (map[string]crypto.PublicKey, bool, error) {
	if !forceRefresh {
		v.mu.RLock()
		keys, fresh := v.keys, v.keys != nil && v.now().Sub(v.fetchedAt) < v.cfg.CacheTTL
		v.mu.RUnlock()
		if fresh {
			return keys, true, nil
		}
	}
	keys, err := v.fetchJWKS(ctx)
	if err != nil {
		return nil, false, err
	}
	v.mu.Lock()
	v.keys, v.fetchedAt = keys, v.now()
	v.mu.Unlock()
	return keys, false, nil
}

func (v *Verifier) fetchJWKS(ctx context.Context) (map[string]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var set jwks
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSResponseSize)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	keys, err := set.publicKeys()
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no usable keys")
	}
	return keys, nil
}

// isSignatureError reports whether a parse error was a signature failure,
// independent of the concrete signing-method error (ECDSA/RSA differ).
func isSignatureError(err error) bool {
	var ve *jwt.ValidationError
	return errors.As(err, &ve) && ve.Errors&jwt.ValidationErrorSignatureInvalid != 0
}
