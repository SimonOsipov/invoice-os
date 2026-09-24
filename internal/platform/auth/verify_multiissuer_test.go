package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const (
	additionalIssuer = "urn:ascomply:auth:pr-1"
	unknownIssuer    = "https://unknown.example/auth/v1"
)

// countedIssuer is a mock issuer whose JWKS is served by a counting handler.
type countedIssuer struct {
	iss  *MockIssuer
	hits *swapHandler
	url  string
}

func newCountedIssuer(t *testing.T, issuer string) countedIssuer {
	t.Helper()
	iss, err := NewMockIssuer(issuer)
	if err != nil {
		t.Fatalf("NewMockIssuer(%q): %v", issuer, err)
	}
	h := &swapHandler{h: iss.JWKSHandler()}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return countedIssuer{iss: iss, hits: h, url: srv.URL}
}

// multiVerifier trusts primary plus every additional issuer.
func multiVerifier(t *testing.T, primary countedIssuer, additional ...countedIssuer) *Verifier {
	t.Helper()
	cfg := Config{
		Issuer:   primary.iss.issuer,
		JWKSURL:  primary.url,
		CacheTTL: time.Hour,
		Logger:   discardLogger(),
	}
	for _, a := range additional {
		cfg.Additional = append(cfg.Additional, TrustedIssuer{Issuer: a.iss.issuer, JWKSURL: a.url})
	}
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

// assertVerifies fails the test unless tok verifies to the minted identity.
func assertVerifies(t *testing.T, v *Verifier, tok, tenant, label string) {
	t.Helper()
	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("%s: Verify: %v", label, err)
	}
	if id.Subject != testSubject || id.TenantID != tenant {
		t.Fatalf("%s: identity = %+v, want subject %s tenant %s", label, id, testSubject, tenant)
	}
}

func TestVerify_PrimaryIssuerUnchangedWithAdditionalSet(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)

	assertVerifies(t, v, mustMint(t, a.iss, MintOptions{Subject: testSubject, TenantID: "tenant-a"}), "tenant-a", "primary token")
	if n := a.hits.count(); n != 1 {
		t.Fatalf("primary JWKS hits = %d, want 1", n)
	}
	if n := b.hits.count(); n != 0 {
		t.Fatalf("additional JWKS hits = %d after a primary token, want 0", n)
	}

	// The zero above only means something if B is live in the same verifier.
	assertVerifies(t, v, mustMint(t, b.iss, MintOptions{Subject: testSubject, TenantID: "tenant-b"}), "tenant-b", "additional token")
	if n := b.hits.count(); n != 1 {
		t.Fatalf("additional JWKS hits = %d after an additional token, want 1", n)
	}
}

func TestVerify_AdditionalIssuerVerifiesAgainstItsOwnJWKS(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)

	assertVerifies(t, v, mustMint(t, b.iss, MintOptions{Subject: testSubject, TenantID: "tenant-b"}), "tenant-b", "additional token")
	if n := b.hits.count(); n != 1 {
		t.Fatalf("additional JWKS hits = %d, want 1", n)
	}
	if n := a.hits.count(); n != 0 {
		t.Fatalf("primary JWKS hits = %d for an additional token, want 0", n)
	}
}

func TestVerify_IssuerClaimCannotBorrowAnotherIssuersKey(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)

	// Prime both caches, so a merged key map would hold B's kid when iss=A arrives.
	assertVerifies(t, v, mustMint(t, a.iss, MintOptions{Subject: testSubject, TenantID: "tenant-a"}), "tenant-a", "primary token")
	assertVerifies(t, v, mustMint(t, b.iss, MintOptions{Subject: testSubject, TenantID: "tenant-b"}), "tenant-b", "additional token")

	borrowed := validClaims(b.iss)
	borrowed.Issuer = a.iss.issuer
	if _, err := v.Verify(context.Background(), signClaims(t, b.iss, borrowed)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("iss=A signed with B's key: err = %v, want ErrUnauthorized", err)
	}

	reverse := validClaims(a.iss)
	reverse.Issuer = b.iss.issuer
	if _, err := v.Verify(context.Background(), signClaims(t, a.iss, reverse)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("iss=B signed with A's key: err = %v, want ErrUnauthorized", err)
	}
}

func TestVerify_UnknownIssuerRefusedWithoutFetch(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)

	// Signed with A's real key and kid: only the iss gate can refuse it.
	c := validClaims(a.iss)
	c.Issuer = unknownIssuer
	if _, err := v.Verify(context.Background(), signClaims(t, a.iss, c)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown iss: err = %v, want ErrUnauthorized", err)
	}
	if na, nb := a.hits.count(), b.hits.count(); na != 0 || nb != 0 {
		t.Fatalf("JWKS hits after an unknown iss: primary=%d additional=%d, want 0 and 0", na, nb)
	}

	// The counter observes fetches: a known issuer does register one.
	assertVerifies(t, v, mustMint(t, a.iss, MintOptions{Subject: testSubject, TenantID: "tenant-a"}), "tenant-a", "primary token")
	if n := a.hits.count(); n != 1 {
		t.Fatalf("primary JWKS hits = %d after a primary token, want 1", n)
	}
}

func TestNewVerifier_RefusesDuplicateOrEmptyIssuer(t *testing.T) {
	const primaryURL = "https://primary.example/.well-known/jwks.json"
	const otherURL = "https://other.example/.well-known/jwks.json"
	base := func(additional ...TrustedIssuer) Config {
		return Config{Issuer: testIssuer, JWKSURL: primaryURL, Additional: additional, Logger: discardLogger()}
	}

	if _, err := NewVerifier(base(
		TrustedIssuer{Issuer: additionalIssuer, JWKSURL: otherURL},
		TrustedIssuer{Issuer: unknownIssuer, JWKSURL: otherURL},
	)); err != nil {
		t.Fatalf("two distinct additional issuers: NewVerifier err = %v, want nil", err)
	}

	tests := []struct {
		name string
		cfg  Config
	}{
		{"repeats the primary", base(TrustedIssuer{Issuer: testIssuer, JWKSURL: otherURL})},
		{"repeats another entry", base(
			TrustedIssuer{Issuer: additionalIssuer, JWKSURL: otherURL},
			TrustedIssuer{Issuer: additionalIssuer, JWKSURL: primaryURL},
		)},
		{"empty issuer", base(TrustedIssuer{Issuer: "", JWKSURL: otherURL})},
		{"empty jwks url", base(TrustedIssuer{Issuer: additionalIssuer, JWKSURL: ""})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewVerifier(tc.cfg); err == nil {
				t.Fatalf("NewVerifier(Additional=%+v) err = nil, want an error", tc.cfg.Additional)
			}
		})
	}
}

func TestVerify_RejectionsHoldForAnAdditionalIssuer(t *testing.T) {
	a := newCountedIssuer(t, testIssuer)
	b := newCountedIssuer(t, additionalIssuer)
	v := multiVerifier(t, a, b)
	iss := b.iss

	// Without this, every case below passes because B is not trusted at all.
	assertVerifies(t, v, mustMint(t, iss, MintOptions{Subject: testSubject, TenantID: "tenant-b"}), "tenant-b", "valid additional token")

	tests := []struct {
		name  string
		token func() string
	}{
		{"wrong issuer", func() string {
			c := validClaims(iss)
			c.Issuer = unknownIssuer
			return signClaims(t, iss, c)
		}},
		{"wrong audience", func() string {
			c := validClaims(iss)
			c.Audience = "anon"
			return signClaims(t, iss, c)
		}},
		{"expired", func() string {
			return mustMint(t, iss, MintOptions{Subject: testSubject, TTL: -time.Minute})
		}},
		{"non-uuid subject", func() string {
			return mustMint(t, iss, MintOptions{Subject: "not-a-uuid"})
		}},
		{"missing role", func() string {
			c := validClaims(iss)
			c.Role = ""
			return signClaims(t, iss, c)
		}},
		{"tampered signature", func() string {
			tok := mustMint(t, iss, MintOptions{Subject: testSubject})
			return tok[:len(tok)-2] + flip(tok[len(tok)-2:])
		}},
		{"wrong signing method (alg confusion)", func() string {
			hs := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims(iss))
			hs.Header["kid"] = iss.kid
			s, err := hs.SignedString([]byte("shared-secret"))
			if err != nil {
				t.Fatalf("sign hs256: %v", err)
			}
			return s
		}},
		{"garbage", func() string { return "not.a.jwt" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), tc.token()); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("expected ErrUnauthorized, got %v", err)
			}
		})
	}
}

// signAudience signs otherwise-valid claims whose aud is the given raw value.
func signAudience(t *testing.T, iss *MockIssuer, aud any) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss":          iss.issuer,
		"sub":          testSubject,
		"aud":          aud,
		"iat":          now.Unix(),
		"exp":          now.Add(time.Hour).Unix(),
		"role":         "authenticated",
		"app_metadata": map[string]any{"tenant_id": "tenant-x"},
	})
	tok.Header["kid"] = iss.kid
	s, err := tok.SignedString(iss.key)
	if err != nil {
		t.Fatalf("signAudience: %v", err)
	}
	return s
}

func TestVerify_AudienceArrayAccepted(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)

	assertVerifies(t, v, signAudience(t, iss, []string{"authenticated"}), "tenant-x", `aud ["authenticated"]`)

	for name, aud := range map[string]any{
		`["other"]`: []string{"other"},
		`[]`:        []string{},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), signAudience(t, iss, aud)); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("aud %s: err = %v, want ErrUnauthorized", name, err)
			}
		})
	}
}
