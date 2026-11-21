// Package iaptest signs IAP-shaped assertions for tests and serves their public key.
package iaptest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v4"
)

const kid = "iaptest-key-1"

// Signer holds one P-256 key served by NewSigner's JWKS.
type Signer struct{ key *ecdsa.PrivateKey }

// Option adjusts a token before it is signed.
type Option func(*jwt.Token)

// WithKid overrides the kid header.
func WithKid(k string) Option { return func(t *jwt.Token) { t.Header["kid"] = k } }

// NewSigner returns a signer and the URL of an httptest JWKS serving its public key.
func NewSigner(t *testing.T) (*Signer, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	set := map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid,
		"x": enc(k.X.FillBytes(make([]byte, 32))), "y": enc(k.Y.FillBytes(make([]byte, 32))),
	}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(srv.Close)
	return &Signer{key: k}, srv.URL
}

// Sign returns an ES256 assertion over claims.
func (s *Signer) Sign(claims map[string]any, opts ...Option) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims(claims))
	tok.Header["kid"] = kid
	for _, o := range opts {
		o(tok)
	}
	out, err := tok.SignedString(s.key)
	if err != nil {
		panic(err)
	}
	return out
}
