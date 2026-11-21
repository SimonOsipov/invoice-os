package auth

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const (
	IAPJWKSURL = "https://www.gstatic.com/iap/verify/public_key-jwk"
	IAPIssuer  = "https://cloud.google.com/iap"

	iapMaxEmail  = 254
	iapIATLeeway = 30 * time.Second
)

// iapAudience is the production shape: /projects/<number>/global/backendServices/<id>.
var iapAudience = regexp.MustCompile(`^/projects/[0-9]+/global/backendServices/[0-9]+$`)

// IAPIdentity is the Google account an IAP assertion proves.
type IAPIdentity struct {
	Email   string
	Subject string
}

// IAPConfig configures an IAPVerifier. Audiences is the only source of accepted aud values.
type IAPConfig struct {
	Audiences []string
	JWKSURL   string // defaults to IAPJWKSURL
	Client    *http.Client
	Now       func() time.Time // defaults to time.Now
}

// IAPVerifier verifies IAP signed-header assertions (ES256, Google's key set).
type IAPVerifier struct {
	keys      *Verifier // reused only for its JWKS cache, fetch budget and stale-key handling
	set       *keySet
	audiences map[string]struct{}
	now       func() time.Time
}

// ParseIAPAudiences parses IAP_AUDIENCES, a comma-separated list of backend-service audiences.
// Empty means IAP sign-in is off (nil, nil); any other shape is an error.
func ParseIAPAudiences(v string) ([]string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if !iapAudience.MatchString(item) {
			return nil, fmt.Errorf("auth: IAP audience %q is not /projects/<n>/global/backendServices/<n>", item)
		}
		if seen[item] {
			return nil, fmt.Errorf("auth: IAP audience %q repeated", item)
		}
		seen[item] = true
		out = append(out, item)
	}
	return out, nil
}

// NewIAPVerifier returns a verifier for cfg.
func NewIAPVerifier(cfg IAPConfig) (*IAPVerifier, error) {
	if len(cfg.Audiences) == 0 {
		return nil, fmt.Errorf("auth: IAPConfig.Audiences is required")
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = IAPJWKSURL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	keys, err := NewVerifier(Config{Issuer: IAPIssuer, JWKSURL: cfg.JWKSURL, HTTPClient: cfg.Client})
	if err != nil {
		return nil, err
	}
	keys.now = cfg.Now
	auds := make(map[string]struct{}, len(cfg.Audiences))
	for _, a := range cfg.Audiences {
		if a == "" {
			return nil, fmt.Errorf("auth: IAPConfig.Audiences has an empty value")
		}
		auds[a] = struct{}{}
	}
	return &IAPVerifier{keys: keys, set: keys.sets[IAPIssuer], audiences: auds, now: cfg.Now}, nil
}

// Verify checks an assertion and returns the identity it proves. Every failure wraps ErrUnauthorized.
func (v *IAPVerifier) Verify(ctx context.Context, assertion string) (IAPIdentity, error) {
	fail := func(format string, a ...any) (IAPIdentity, error) {
		return IAPIdentity{}, fmt.Errorf("%w: iap: %s", ErrUnauthorized, fmt.Sprintf(format, a...))
	}
	// Read kid before verifying: the key set depends on it. Nothing from this parse is trusted.
	hdr, _, err := jwt.NewParser().ParseUnverified(assertion, jwt.MapClaims{})
	if err != nil {
		return fail("malformed: %v", err)
	}
	kid, _ := hdr.Header["kid"].(string)
	key, err := v.key(ctx, kid)
	if err != nil {
		return fail("%v", err)
	}

	claims := jwt.MapClaims{}
	// v4 accepts a missing exp/iat, so claims are checked by hand below.
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"ES256"}), jwt.WithoutClaimsValidation())
	if _, err := parser.ParseWithClaims(assertion, claims, func(*jwt.Token) (any, error) { return key, nil }); err != nil {
		return fail("%v", err)
	}

	now := v.now()
	if iss, ok := claims["iss"].(string); !ok || iss != IAPIssuer {
		return fail("bad issuer")
	}
	// A string only: an array aud is refused rather than matched by any element.
	aud, _ := claims["aud"].(string)
	if _, ok := v.audiences[aud]; !ok {
		return fail("bad audience")
	}
	exp, ok := claims["exp"].(float64)
	if !ok || !now.Before(time.Unix(int64(exp), 0)) {
		return fail("missing or past exp")
	}
	iat, ok := claims["iat"].(float64)
	if !ok || time.Unix(int64(iat), 0).After(now.Add(iapIATLeeway)) {
		return fail("missing or future iat")
	}
	email, _ := claims["email"].(string)
	if len(email) == 0 || len(email) > iapMaxEmail || !strings.Contains(email, "@") {
		return fail("unusable email")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return fail("missing sub")
	}
	return IAPIdentity{Email: email, Subject: sub}, nil
}

// key returns the ECDSA key for kid. An unknown kid forces one refetch, which the key set
// throttles to one per minRefetchInterval and answers from cache if it fails.
func (v *IAPVerifier) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	for _, force := range []bool{false, true} {
		keys, _, err := v.keys.jwksKeys(ctx, v.set, force)
		if err != nil {
			return nil, fmt.Errorf("jwks: %w", err)
		}
		if k, ok := keys[kid].(*ecdsa.PublicKey); ok {
			return k, nil
		}
	}
	return nil, fmt.Errorf("unknown signing key")
}
