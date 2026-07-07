package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMockIssuer_JWKSServesUsableECKey(t *testing.T) {
	iss := mustIssuer(t)
	rec := httptest.NewRecorder()
	iss.JWKSHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var set jwks
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("decode jwks: %v", err)
	}
	if len(set.Keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(set.Keys))
	}
	k := set.Keys[0]
	if k.Kty != "EC" || k.Crv != "P-256" || k.Alg != "ES256" || k.Use != "sig" || k.Kid == "" {
		t.Fatalf("jwk = %+v", k)
	}
	keys, err := set.publicKeys()
	if err != nil {
		t.Fatalf("publicKeys: %v", err)
	}
	if _, ok := keys[k.Kid]; !ok {
		t.Fatalf("kid %q not parsed into a public key", k.Kid)
	}
}

func TestMockIssuer_MintHeaderIsGoTrueShaped(t *testing.T) {
	iss := mustIssuer(t)
	tok := mustMint(t, iss, MintOptions{Subject: testSubject})

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3", len(parts))
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr map[string]any
	if err := json.Unmarshal(b, &hdr); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if hdr["alg"] != "ES256" || hdr["typ"] != "JWT" {
		t.Fatalf("header alg/typ = %v/%v", hdr["alg"], hdr["typ"])
	}
	if _, ok := hdr["kid"].(string); !ok {
		t.Fatalf("header kid missing/not string: %v", hdr["kid"])
	}
}

func TestMockIssuer_MintDefaultsAreVerifiable(t *testing.T) {
	// Defaults (random UUID subject, "authenticated" role) must pass Verify.
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	id, err := v.Verify(t.Context(), mustMint(t, iss, MintOptions{TenantID: "tenant-x"}))
	if err != nil {
		t.Fatalf("Verify default-minted token: %v", err)
	}
	if id.Role != "authenticated" || id.TenantID != "tenant-x" || id.Subject == "" {
		t.Fatalf("identity = %+v", id)
	}
}
