package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddleware_InjectsIdentity(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)
	tok := mustMint(t, iss, MintOptions{Subject: testSubject, Role: "authenticated", TenantID: "tenant-x"})

	var got Identity
	var ok bool
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = IdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !ok || got.Subject != testSubject || got.TenantID != "tenant-x" {
		t.Fatalf("identity = %+v ok=%v", got, ok)
	}
}

func TestMiddleware_Rejects(t *testing.T) {
	iss := mustIssuer(t)
	v, _ := jwksServer(t, iss)

	nextCalled := false
	h := v.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true }))

	cases := []struct{ name, authz string }{
		{"missing header", ""},
		{"not bearer scheme", "Basic Zm9vOmJhcg=="},
		{"empty bearer", "Bearer "},
		{"malformed token", "Bearer not.a.jwt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled = false
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (must never be 500)", rec.Code)
			}
			if nextCalled {
				t.Fatal("next handler must not run for a rejected request")
			}
			// The body must be opaque — no reason leaked.
			if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"unauthorized"}` {
				t.Fatalf("leaky response body: %q", body)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
			}
		})
	}
}
