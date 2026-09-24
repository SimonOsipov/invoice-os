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

// contextKeys runs identityMiddleware on req and reports what each context key holds.
func contextKeys(req *http.Request) (id auth.Identity, idOK bool, caller auth.Identity, callerOK bool) {
	identityMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		id, idOK = auth.IdentityFromContext(r.Context())
		caller, callerOK = auth.TenantlessCallerFromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), req)
	return
}

func TestIdentityMiddleware_TenantlessCaller(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/workspaces", nil)
	req.Header.Set("X-User-ID", "user-42")
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if !callerOK {
		t.Fatal("expected a tenant-less caller when X-User-ID is set and X-Tenant-ID is not")
	}
	want := auth.Identity{Subject: "user-42", Role: "authenticated", Email: "ada@example.test"}
	if caller != want {
		t.Errorf("tenant-less caller = %+v, want %+v", caller, want)
	}
	if idOK {
		t.Errorf("IdentityFromContext = %+v, want none for a tenant-less caller", id)
	}
}

func TestIdentityMiddleware_TenantHeaderStillBuildsIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Tenant-ID", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	req.Header.Set("X-User-ID", "user-42")
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if !idOK {
		t.Fatal("expected an identity when X-Tenant-ID is set")
	}
	want := auth.Identity{
		Subject:  "user-42",
		Role:     "authenticated",
		TenantID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Email:    "ada@example.test",
	}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
	if callerOK {
		t.Errorf("TenantlessCallerFromContext = %+v, want none when a tenant is present", caller)
	}
}

func TestIdentityMiddleware_NoUserNoIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if idOK {
		t.Errorf("IdentityFromContext = %+v, want none without X-Tenant-ID and X-User-ID", id)
	}
	if callerOK {
		t.Errorf("TenantlessCallerFromContext = %+v, want none without X-Tenant-ID and X-User-ID", caller)
	}

	// Control: the same request plus X-User-ID does build a caller.
	req.Header.Set("X-User-ID", "user-42")
	if _, _, _, ok := contextKeys(req); !ok {
		t.Error("control: adding X-User-ID built no tenant-less caller")
	}
}
