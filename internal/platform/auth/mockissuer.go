package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

// MockIssuer mints GoTrue-shaped, ES256-signed JWTs for local development and
// CI and serves its own JWKS, so a Verifier can validate them end to end with
// the exact code path it will use against Supabase GoTrue after M8. It is the
// dev/CI stand-in for GoTrue and must never be wired into a production build;
// production refusal of mock-issued tokens is enforced separately in M8-07.
type MockIssuer struct {
	issuer string
	kid    string
	key    *ecdsa.PrivateKey
}

// NewMockIssuer creates an issuer with a freshly generated P-256 key. issuer is
// the "iss" it stamps and must match the Verifier's configured issuer.
func NewMockIssuer(issuer string) (*MockIssuer, error) {
	if issuer == "" {
		return nil, fmt.Errorf("auth: mock issuer requires an issuer URL")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("auth: generate mock signing key: %w", err)
	}
	return &MockIssuer{issuer: issuer, kid: uuid.NewString(), key: key}, nil
}

// MintOptions describes a token to mint. Zero fields take GoTrue-shaped
// defaults: a random UUID subject, the "authenticated" role, and a one-hour TTL.
type MintOptions struct {
	Subject  string
	Role     string
	TenantID string
	TTL      time.Duration
}

// Mint returns a signed ES256 JWT carrying the GoTrue claim contract.
func (m *MockIssuer) Mint(opts MintOptions) (string, error) {
	if opts.Subject == "" {
		opts.Subject = uuid.NewString()
	}
	if opts.Role == "" {
		opts.Role = defaultAudience // "authenticated"
	}
	if opts.TTL == 0 {
		opts.TTL = time.Hour
	}
	now := time.Now()
	claims := gotrueClaims{
		Issuer:      m.issuer,
		Subject:     opts.Subject,
		Audience:    audience(defaultAudience),
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(opts.TTL).Unix(),
		Role:        opts.Role,
		AppMetadata: appMetadata{TenantID: opts.TenantID},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = m.kid
	return tok.SignedString(m.key)
}

// JWKSHandler serves the issuer's public key set, mirroring GoTrue's
// /.well-known/jwks.json so a Verifier can fetch it by URL.
func (m *MockIssuer) JWKSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.jwks())
	})
}

func (m *MockIssuer) jwks() jwks {
	pub := m.key.PublicKey
	size := (pub.Curve.Params().BitSize + 7) / 8
	return jwks{Keys: []jwk{{
		Kty: "EC",
		Crv: "P-256",
		Alg: "ES256",
		Use: "sig",
		Kid: m.kid,
		X:   base64.RawURLEncoding.EncodeToString(leftPad(pub.X.Bytes(), size)),
		Y:   base64.RawURLEncoding.EncodeToString(leftPad(pub.Y.Bytes(), size)),
	}}}
}

// leftPad zero-pads a big-endian coordinate to the curve's field size, as JWK
// requires fixed-width EC coordinates.
func leftPad(b []byte, size int) []byte {
	if len(b) >= size {
		return b
	}
	out := make([]byte, size)
	copy(out[size-len(b):], b)
	return out
}
