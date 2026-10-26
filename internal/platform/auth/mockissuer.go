package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

// MockIssuer mints GoTrue-shaped, ES256-signed JWTs and serves its own JWKS, so a
// Verifier validates them with the exact code path it will use against Supabase
// GoTrue after M8. It stands in for GoTrue outside production, and is compiled into
// the gateway only under -tags mockissuer (AUTH-01).
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

// NewMockIssuerFromJWK creates an issuer that signs with the one-key ES256 private
// JWK array `prenv jwk-es256` prints, keeping its kid, so every process given the
// same key serves one JWKS. Errors carry a fixed reason, never the input.
func NewMockIssuerFromJWK(issuer, jwkJSON string) (*MockIssuer, error) {
	if issuer == "" {
		return nil, fmt.Errorf("auth: mock issuer requires an issuer URL")
	}
	// A map keeps member names exact (RFC 7517 names are case-sensitive).
	var keys []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(jwkJSON)), &keys); err != nil {
		return nil, fmt.Errorf("auth: mock issuer key: not a JSON array of JWK objects")
	}
	if len(keys) != 1 {
		return nil, fmt.Errorf("auth: mock issuer key: want exactly 1 key, got %d", len(keys))
	}
	member := func(name string) string {
		var v string
		_ = json.Unmarshal(keys[0][name], &v)
		return v
	}
	if member("kty") != "EC" || member("crv") != "P-256" || member("alg") != "ES256" {
		return nil, fmt.Errorf("auth: mock issuer key: not an ES256 P-256 key")
	}
	kid := member("kid")
	if kid == "" {
		return nil, fmt.Errorf("auth: mock issuer key: no kid")
	}
	dec := base64.RawURLEncoding.DecodeString
	d, errD := dec(member("d"))
	x, errX := dec(member("x"))
	y, errY := dec(member("y"))
	if errD != nil || errX != nil || errY != nil || len(d) == 0 {
		return nil, fmt.Errorf("auth: mock issuer key: a coordinate or the private scalar is missing or not unpadded base64url")
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return nil, fmt.Errorf("auth: mock issuer key: the private scalar is not a valid P-256 scalar")
	}
	if key.X.Cmp(new(big.Int).SetBytes(x)) != 0 || key.Y.Cmp(new(big.Int).SetBytes(y)) != 0 {
		return nil, fmt.Errorf("auth: mock issuer key: x/y are not the public point of the private scalar")
	}
	return &MockIssuer{issuer: issuer, kid: kid, key: key}, nil
}

// MintOptions describes a token to mint. Zero fields take GoTrue-shaped
// defaults: a random UUID subject, the "authenticated" role, and a one-hour TTL.
type MintOptions struct {
	Subject  string
	Role     string
	TenantID string
	Email    string // minted only when non-empty
	Staff    bool   // app_metadata.staff, minted only when set
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
		AppMetadata: appMetadata{TenantID: opts.TenantID, Staff: opts.Staff},
		Email:       opts.Email,
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
