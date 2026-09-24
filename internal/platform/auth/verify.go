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
// not verify against — the cached JWKS. It drives exactly one retry
// (the key-rotation path) and is never returned to callers.
var errStaleKey = errors.New("auth: signing key not in cached jwks")

const (
	defaultCacheTTL     = time.Hour
	defaultHTTPTimeout  = 10 * time.Second
	defaultAudience     = "authenticated"
	maxJWKSResponseSize = 1 << 20 // 1 MiB

	// Tokens live 1h, so 6h rides out an outage while bounding how long a withdrawn key stays trusted.
	// ceiling: fixed constants, make them Config fields if an environment needs other values.
	staleGrace         = 6 * time.Hour
	minRefetchInterval = 30 * time.Second
)

var errRefetchThrottled = errors.New("refetch throttled")

// Config configures a Verifier. The M8 cutover is a config change rather than
// a code change.
type Config struct {
	Issuer     string          // required: expected "iss"
	JWKSURL    string          // required: where the signing public keys are served
	Additional []TrustedIssuer // further issuers, each verified against its own JWKS
	Audience   string          // expected "aud"; defaults to "authenticated"
	CacheTTL   time.Duration   // JWKS cache lifetime; defaults to 1h
	HTTPClient *http.Client    // JWKS fetch client; defaults to a 10s-timeout client
	Logger     *slog.Logger    // defaults to slog.Default()
}

// Verifier validates GoTrue-shaped JWTs against a set of trusted issuers, each
// with its own JWKS. It caches each key set with a TTL and, on a signature/kid
// failure while using cached keys, refetches once to ride out key rotation.
type Verifier struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger

	sets map[string]*keySet // keyed by issuer

	now func() time.Time // overridable in tests
}

// keySet is one issuer's cached JWKS.
type keySet struct {
	issuer  string
	jwksURL string

	mu        sync.Mutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
	// lastForced anchors the refetch window: the start of the last forced or
	// failed fetch. A successful unforced fetch leaves it alone.
	lastForced time.Time
	failed     bool       // the last fetch failed
	inflight   *fetchCall // coalesces concurrent fetches
}

type fetchCall struct {
	done chan struct{}
	keys map[string]crypto.PublicKey
	err  error
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
	sets := map[string]*keySet{cfg.Issuer: {issuer: cfg.Issuer, jwksURL: cfg.JWKSURL}}
	for i, ti := range cfg.Additional {
		if ti.Issuer == "" || ti.JWKSURL == "" {
			return nil, fmt.Errorf("auth: Config.Additional[%d] needs Issuer and JWKSURL", i)
		}
		if _, dup := sets[ti.Issuer]; dup {
			return nil, fmt.Errorf("auth: Config.Additional[%d] repeats issuer %q", i, ti.Issuer)
		}
		sets[ti.Issuer] = &keySet{issuer: ti.Issuer, jwksURL: ti.JWKSURL}
	}
	return &Verifier{cfg: cfg, http: cfg.HTTPClient, log: cfg.Logger, sets: sets, now: time.Now}, nil
}

// Verify checks a token's signature and claims and returns the caller identity.
// Every failure returns an error wrapping ErrUnauthorized with no distinguishing
// detail, so middleware can answer 401 without leaking why.
func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	ks, err := v.selectKeySet(token)
	if err != nil {
		return Identity{}, err
	}
	id, err := v.verifyWith(ctx, ks, token, false)
	if errors.Is(err, errStaleKey) {
		// Cached keys were stale (rotation): retry.
		id, err = v.verifyWith(ctx, ks, token, true)
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

// selectKeySet picks the key set by the token's unverified iss. An unknown iss is
// refused before any fetch, so a forged iss cannot drive outbound requests.
func (v *Verifier) selectKeySet(token string) (*keySet, error) {
	var claims jwt.MapClaims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &claims); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	iss, _ := claims["iss"].(string)
	ks, ok := v.sets[iss]
	if !ok {
		return nil, fmt.Errorf("%w: untrusted issuer", ErrUnauthorized)
	}
	return ks, nil
}

func (v *Verifier) verifyWith(ctx context.Context, ks *keySet, token string, forceRefresh bool) (Identity, error) {
	keys, usedCache, err := v.jwksKeys(ctx, ks, forceRefresh)
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

	return v.validate(&claims, ks.issuer)
}

// validate enforces the claim contract beyond signature and expiry (expiry is
// checked by gotrueClaims.Valid during parsing).
func (v *Verifier) validate(c *gotrueClaims, issuer string) (Identity, error) {
	if c.Issuer != issuer {
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

// jwksKeys returns the issuer's current key set. Unless forceRefresh is set it
// serves a fresh cache; usedCache reports whether the returned keys came from
// the cache (only then is a rotation retry meaningful). Forced refetches and
// refetches after a failure run at most once per minRefetchInterval.
func (v *Verifier) jwksKeys(ctx context.Context, ks *keySet, forceRefresh bool) (map[string]crypto.PublicKey, bool, error) {
	ks.mu.Lock()
	now := v.now()
	if !forceRefresh && ks.keys != nil && now.Sub(ks.fetchedAt) < v.cfg.CacheTTL {
		keys := ks.keys
		ks.mu.Unlock()
		return keys, true, nil
	}
	call := ks.inflight
	if call == nil {
		throttled := (forceRefresh || ks.failed) && !ks.lastForced.IsZero() && now.Sub(ks.lastForced) < minRefetchInterval
		if throttled {
			defer ks.mu.Unlock()
			return v.cachedOrStale(ks, now, errRefetchThrottled)
		}
		call = &fetchCall{done: make(chan struct{})}
		ks.inflight = call
		if forceRefresh {
			ks.lastForced = now
		}
		ks.mu.Unlock()

		// The fetch is shared, so one caller's cancellation must not fail it for the rest.
		call.keys, call.err = v.fetchJWKS(context.WithoutCancel(ctx), ks.jwksURL)
		ks.mu.Lock()
		if call.err != nil {
			ks.failed, ks.lastForced = true, now
		} else {
			ks.keys, ks.fetchedAt, ks.failed = call.keys, v.now(), false
		}
		ks.inflight = nil
		ks.mu.Unlock()
		close(call.done)
	} else {
		ks.mu.Unlock()
		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	if call.err == nil {
		return call.keys, false, nil
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return v.cachedOrStale(ks, v.now(), call.err)
}

// cachedOrStale answers when no fetch result is usable: the cached keys while
// they are within CacheTTL + staleGrace, else err. Caller holds ks.mu.
func (v *Verifier) cachedOrStale(ks *keySet, now time.Time, err error) (map[string]crypto.PublicKey, bool, error) {
	if ks.keys == nil {
		return nil, false, err
	}
	age := now.Sub(ks.fetchedAt)
	if age < v.cfg.CacheTTL {
		return ks.keys, true, nil
	}
	if age < v.cfg.CacheTTL+staleGrace {
		v.log.Warn("auth: serving stale JWKS", "issuer", ks.issuer, "age", age, "err", err)
		return ks.keys, true, nil
	}
	return nil, false, err
}

func (v *Verifier) fetchJWKS(ctx context.Context, jwksURL string) (map[string]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
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
