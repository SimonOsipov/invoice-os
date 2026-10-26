package auth

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jwkArray returns a one-key private JWK array as `prenv jwk-es256` prints it; set
// edits the member map before encoding.
func jwkArray(t *testing.T, kid string, set func(m map[string]any)) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	e, err := k.ECDH()
	if err != nil {
		t.Fatalf("ecdh: %v", err)
	}
	return jwkArrayOf(t, kid, e, set)
}

func jwkArrayOf(t *testing.T, kid string, e *ecdh.PrivateKey, set func(m map[string]any)) string {
	t.Helper()
	pub := e.PublicKey().Bytes()
	enc := base64.RawURLEncoding.EncodeToString
	m := map[string]any{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "kid": kid,
		"key_ops": []string{"sign", "verify"},
		"x":       enc(pub[1:33]), "y": enc(pub[33:]), "d": enc(e.Bytes()),
	}
	if set != nil {
		set(m)
	}
	raw, err := json.Marshal([]map[string]any{m})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func jwksKid(t *testing.T, iss *MockIssuer) string {
	t.Helper()
	set := iss.jwks()
	if len(set.Keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(set.Keys))
	}
	return set.Keys[0].Kid
}

func TestMockIssuerFromJWK_SameKeySameKidAndCrossVerifies(t *testing.T) {
	raw := jwkArray(t, "kid-shared", nil)
	a, err := NewMockIssuerFromJWK(testIssuer, raw)
	if err != nil {
		t.Fatalf("issuer A: %v", err)
	}
	b, err := NewMockIssuerFromJWK(testIssuer, raw)
	if err != nil {
		t.Fatalf("issuer B: %v", err)
	}
	if got := jwksKid(t, a); got != "kid-shared" {
		t.Errorf("A kid = %q, want kid-shared", got)
	}
	if got := jwksKid(t, b); got != "kid-shared" {
		t.Errorf("B kid = %q, want kid-shared", got)
	}
	v, _ := jwksServer(t, a)
	id, err := v.Verify(t.Context(), mustMint(t, b, MintOptions{Subject: testSubject, TenantID: "t1"}))
	if err != nil {
		t.Fatalf("a token minted by B does not verify through A's JWKS: %v", err)
	}
	if id.TenantID != "t1" {
		t.Errorf("tenant = %q, want t1", id.TenantID)
	}
}

// The BUG-53 case: a restarted instance must accept a token minted before.
func TestMockIssuerFromJWK_TokenSurvivesRestart(t *testing.T) {
	raw := jwkArray(t, "kid-restart", nil)
	a, err := NewMockIssuerFromJWK(testIssuer, raw)
	if err != nil {
		t.Fatalf("issuer A: %v", err)
	}
	tok := mustMint(t, a, MintOptions{Subject: testSubject})
	restarted, err := NewMockIssuerFromJWK(testIssuer, raw)
	if err != nil {
		t.Fatalf("restarted issuer: %v", err)
	}
	v, _ := jwksServer(t, restarted)
	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatalf("a token minted before the restart does not verify: %v", err)
	}
}

func TestMockIssuerFromJWK_RejectsBadKeys(t *testing.T) {
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherECDH, err := other.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	otherPub := otherECDH.PublicKey().Bytes()
	enc := base64.RawURLEncoding.EncodeToString
	one := jwkArray(t, "k", nil)
	cases := map[string]string{
		"empty":        "",
		"not JSON":     "not json",
		"not an array": strings.Trim(one, "[]"),
		"two keys":     "[" + strings.Trim(one, "[]") + "," + strings.Trim(one, "[]") + "]",
		"no keys":      "[]",
		"crv P-384":    jwkArray(t, "k", func(m map[string]any) { m["crv"] = "P-384" }),
		"alg RS256":    jwkArray(t, "k", func(m map[string]any) { m["alg"] = "RS256" }),
		"no kid":       jwkArray(t, "", nil),
		"no d":         jwkArray(t, "k", func(m map[string]any) { delete(m, "d") }),
		"x/y of another key": jwkArray(t, "k", func(m map[string]any) {
			m["x"], m["y"] = enc(otherPub[1:33]), enc(otherPub[33:])
		}),
		"bad base64 d": jwkArray(t, "k", func(m map[string]any) { m["d"] = "!!!" }),
		"bad base64 x": jwkArray(t, "k", func(m map[string]any) { m["x"] = "!!!" }),
		"zero scalar":  jwkArray(t, "k", func(m map[string]any) { m["d"] = enc(make([]byte, 32)) }),
		"member type":  jwkArray(t, "k", func(m map[string]any) { m["kid"] = 7 }),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if iss, err := NewMockIssuerFromJWK(testIssuer, raw); err == nil {
				t.Fatalf("accepted the key, kid %q", iss.kid)
			}
		})
	}
	t.Run("no issuer URL", func(t *testing.T) {
		if _, err := NewMockIssuerFromJWK("", one); err == nil {
			t.Fatal("accepted an empty issuer URL")
		}
	})
}

func TestMockIssuerFromJWK_ErrorNeverQuotesTheKey(t *testing.T) {
	const marker = "SECRET-MARKER-9f3a"
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e, err := k.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	d := base64.RawURLEncoding.EncodeToString(e.Bytes())
	for name, raw := range map[string]string{
		"wrong alg":      jwkArrayOf(t, marker, e, func(m map[string]any) { m["alg"] = "RS256"; m["note"] = marker }),
		"truncated JSON": `[{"kty":"EC","d":"` + d + `","kid":"` + marker + `"`,
		"wrong type":     `[{"kty":"EC","crv":"P-256","alg":"ES256","kid":"` + marker + `","d":["` + d + `"]}]`,
		"bad x":          jwkArrayOf(t, marker, e, func(m map[string]any) { m["x"] = marker }),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewMockIssuerFromJWK(testIssuer, raw)
			if err == nil {
				t.Fatal("accepted the key")
			}
			if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), d) {
				t.Errorf("error quotes the key: %q", err.Error())
			}
		})
	}
}

func TestMockIssuerFromJWK_TrimsSurroundingSpace(t *testing.T) {
	raw := "\n\n" + jwkArray(t, "kid-pad", nil) + "\n \t\n"
	iss, err := NewMockIssuerFromJWK(testIssuer, raw)
	if err != nil {
		t.Fatalf("padded key: %v", err)
	}
	if got := jwksKid(t, iss); got != "kid-pad" {
		t.Errorf("kid = %q, want kid-pad", got)
	}
}

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

func TestMockMint_EmailOmittedWhenEmpty(t *testing.T) {
	iss := mustIssuer(t)

	// omitempty keeps email-less mock tokens byte-identical to before the claim existed.
	without := decodeSegment(t, mustMint(t, iss, MintOptions{Subject: testSubject}), 1)
	if without["sub"] != testSubject {
		t.Fatalf("decoded claims sub = %v, want %q", without["sub"], testSubject)
	}
	if v, ok := without["email"]; ok {
		t.Errorf("claims minted without Email carry an email key: %v", v)
	}

	with := decodeSegment(t, mustMint(t, iss, MintOptions{Subject: testSubject, Email: "ada@example.test"}), 1)
	if got := with["email"]; got != "ada@example.test" {
		t.Errorf("claims minted with Email: email = %v, want %q", got, "ada@example.test")
	}
}
