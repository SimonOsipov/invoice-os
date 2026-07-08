package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestIdentityMiddleware(t *testing.T) {
	var got auth.Identity
	var ok bool
	h := identityMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok = auth.IdentityFromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Tenant-ID", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	req.Header.Set("X-User-ID", "user-42")
	req.Header.Set("X-User-Role", "authenticated")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !ok {
		t.Fatal("expected an identity in context when the gateway headers are present")
	}
	want := auth.Identity{
		Subject:  "user-42",
		Role:     "authenticated",
		TenantID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
	}
	if got != want {
		t.Errorf("identity = %+v, want %+v", got, want)
	}
}

func TestIdentityMiddlewareAbsent(t *testing.T) {
	// No tenant header → no identity: the middleware must fail closed so an
	// un-fronted service never fabricates a caller (db.WithinRequestTenantTx then
	// refuses with ErrNoTenant).
	var present bool
	h := identityMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, present = auth.IdentityFromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	if present {
		t.Error("expected no identity in context when the tenant header is absent")
	}
}
