package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

const (
	testIssuer  = "https://mock.local/auth/v1"
	testSubject = "11111111-1111-1111-1111-111111111111"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustIssuer(t *testing.T) *MockIssuer {
	t.Helper()
	iss, err := NewMockIssuer(testIssuer)
	if err != nil {
		t.Fatalf("NewMockIssuer: %v", err)
	}
	return iss
}

// verifierFor returns a Verifier whose JWKS is served by srv and that expects
// the given issuer.
func verifierFor(t *testing.T, issuer, jwksURL string) *Verifier {
	t.Helper()
	v, err := NewVerifier(Config{
		Issuer:   issuer,
		JWKSURL:  jwksURL,
		CacheTTL: time.Hour,
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

// jwksServer serves iss's JWKS and returns the verifier wired to it.
func jwksServer(t *testing.T, iss *MockIssuer) (*Verifier, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(iss.JWKSHandler())
	t.Cleanup(srv.Close)
	return verifierFor(t, iss.issuer, srv.URL), srv
}

func mustMint(t *testing.T, iss *MockIssuer, opts MintOptions) string {
	t.Helper()
	tok, err := iss.Mint(opts)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	return tok
}

// signClaims signs arbitrary claims with the issuer's key and kid, for crafting
// tokens the public Mint API would never produce (wrong aud, missing role, ...).
func signClaims(t *testing.T, iss *MockIssuer, c gotrueClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = iss.kid
	s, err := tok.SignedString(iss.key)
	if err != nil {
		t.Fatalf("signClaims: %v", err)
	}
	return s
}

func validClaims(iss *MockIssuer) gotrueClaims {
	now := time.Now()
	return gotrueClaims{
		Issuer:      iss.issuer,
		Subject:     testSubject,
		Audience:    "authenticated",
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(time.Hour).Unix(),
		Role:        "authenticated",
		AppMetadata: appMetadata{TenantID: "tenant-x"},
	}
}

func TestVerify_HappyPath(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	tok := mustMint(t, iss, MintOptions{Subject: testSubject, Role: "authenticated", TenantID: "tenant-x"})

	id, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != testSubject || id.Role != "authenticated" || id.TenantID != "tenant-x" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestVerify_Rejections(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	ctx := context.Background()

	tests := []struct {
		name  string
		token func() string
	}{
		{"wrong issuer", func() string {
			c := validClaims(iss)
			c.Issuer = "https://evil.example/auth/v1"
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
			_, err := v.Verify(ctx, tc.token())
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("expected ErrUnauthorized, got %v", err)
			}
		})
	}
}

func TestVerify_JWKSFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	iss := mustIssuer(t)
	v := verifierFor(t, iss.issuer, srv.URL)

	_, err := v.Verify(context.Background(), mustMint(t, iss, MintOptions{Subject: testSubject}))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized on jwks failure, got %v", err)
	}
}

func TestVerify_CachesJWKS(t *testing.T) {
	iss := mustIssuer(t)
	swap := &swapHandler{h: iss.JWKSHandler()}
	srv := httptest.NewServer(swap)
	t.Cleanup(srv.Close)
	v := verifierFor(t, iss.issuer, srv.URL)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := v.Verify(ctx, mustMint(t, iss, MintOptions{Subject: testSubject})); err != nil {
			t.Fatalf("Verify #%d: %v", i, err)
		}
	}
	if n := swap.count(); n != 1 {
		t.Fatalf("expected 1 JWKS fetch (cached), got %d", n)
	}
}

// TestVerify_KeyRotation_NewKid: the signing key rotates to a new kid the cache
// hasn't seen; the verifier must refetch once and accept the token.
func TestVerify_KeyRotation_NewKid(t *testing.T) {
	iss1 := mustIssuer(t)
	swap := &swapHandler{h: iss1.JWKSHandler()}
	srv := httptest.NewServer(swap)
	t.Cleanup(srv.Close)
	v := verifierFor(t, iss1.issuer, srv.URL)
	ctx := context.Background()

	// Prime the cache with iss1's key.
	if _, err := v.Verify(ctx, mustMint(t, iss1, MintOptions{Subject: testSubject})); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// Rotate to a fresh key (new kid) and present a token signed by it.
	iss2 := mustIssuer(t)
	swap.set(iss2.JWKSHandler())
	id, err := v.Verify(ctx, mustMint(t, iss2, MintOptions{Subject: testSubject, TenantID: "t2"}))
	if err != nil {
		t.Fatalf("post-rotation Verify: %v", err)
	}
	if id.TenantID != "t2" {
		t.Fatalf("identity = %+v", id)
	}
	if n := swap.count(); n != 2 {
		t.Fatalf("expected 2 JWKS fetches (prime + rotation refetch), got %d", n)
	}
}

// TestVerify_KeyRotation_SameKid: the key rotates but reuses the kid, so the
// cached key yields a signature failure; the verifier must refetch and recover.
func TestVerify_KeyRotation_SameKid(t *testing.T) {
	iss1 := mustIssuer(t)
	swap := &swapHandler{h: iss1.JWKSHandler()}
	srv := httptest.NewServer(swap)
	t.Cleanup(srv.Close)
	v := verifierFor(t, iss1.issuer, srv.URL)
	ctx := context.Background()

	if _, err := v.Verify(ctx, mustMint(t, iss1, MintOptions{Subject: testSubject})); err != nil {
		t.Fatalf("prime: %v", err)
	}

	iss2 := mustIssuer(t)
	iss2.kid = iss1.kid // same kid, different key
	swap.set(iss2.JWKSHandler())
	if _, err := v.Verify(ctx, mustMint(t, iss2, MintOptions{Subject: testSubject})); err != nil {
		t.Fatalf("post-rotation Verify (same kid): %v", err)
	}
}

// swapHandler is an http.Handler whose delegate can be swapped at runtime, used
// to simulate JWKS key rotation, and which counts requests to assert caching.
type swapHandler struct {
	mu sync.Mutex
	h  http.Handler
	n  int
}

func (s *swapHandler) set(h http.Handler) {
	s.mu.Lock()
	s.h = h
	s.mu.Unlock()
}

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.n++
	h := s.h
	s.mu.Unlock()
	h.ServeHTTP(w, r)
}

func (s *swapHandler) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func flip(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
	}
	return string(b)
}
