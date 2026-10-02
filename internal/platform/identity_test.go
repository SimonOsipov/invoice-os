package platform

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const testSubject = "c0000000-0000-0000-0000-000000000001"

func TestIdentityMiddleware(t *testing.T) {
	var got auth.Identity
	var ok bool
	h := identityMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok = auth.IdentityFromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Tenant-ID", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	req.Header.Set("X-User-ID", testSubject)
	req.Header.Set("X-User-Role", "authenticated")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !ok {
		t.Fatal("expected an identity in context when the gateway headers are present")
	}
	want := auth.Identity{
		Subject:  testSubject,
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
	req.Header.Set("X-User-ID", testSubject)
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if !callerOK {
		t.Fatal("expected a tenant-less caller when X-User-ID is set and X-Tenant-ID is not")
	}
	want := auth.Identity{Subject: testSubject, Role: "authenticated", Email: "ada@example.test"}
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
	req.Header.Set("X-User-ID", testSubject)
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if !idOK {
		t.Fatal("expected an identity when X-Tenant-ID is set")
	}
	want := auth.Identity{
		Subject:  testSubject,
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
	req.Header.Set("X-User-ID", testSubject)
	if _, _, _, ok := contextKeys(req); !ok {
		t.Error("control: adding X-User-ID built no tenant-less caller")
	}
}

const testTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestIdentityMiddleware_NonUUIDSubjectBuildsNoIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Tenant-ID", testTenant)
	req.Header.Set("X-User-ID", "user-42")
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if idOK {
		t.Errorf("IdentityFromContext = %+v, want none for a non-uuid subject", id)
	}
	if callerOK {
		t.Errorf("TenantlessCallerFromContext = %+v, want none when a tenant header is present", caller)
	}
}

func TestIdentityMiddleware_EmptySubjectWithTenantBuildsNoIdentity(t *testing.T) {
	cases := []struct {
		name string
		user []string // nil: header absent
	}{
		{"absent", nil},
		{"present and empty", []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-Tenant-ID", testTenant)
			req.Header.Set("X-User-Role", "authenticated")
			if tc.user != nil {
				req.Header["X-User-Id"] = tc.user
			}
			id, idOK, caller, callerOK := contextKeys(req)
			if idOK {
				t.Errorf("IdentityFromContext = %+v, want none for an empty subject", id)
			}
			if callerOK {
				t.Errorf("TenantlessCallerFromContext = %+v, want none", caller)
			}
		})
	}
}

func TestIdentityMiddleware_NonUUIDTenantlessSubjectBuildsNoCaller(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/workspaces", nil)
	req.Header.Set("X-User-ID", "user-42")
	req.Header.Set("X-User-Role", "authenticated")

	id, idOK, caller, callerOK := contextKeys(req)
	if callerOK {
		t.Errorf("TenantlessCallerFromContext = %+v, want none for a non-uuid subject", caller)
	}
	if idOK {
		t.Errorf("IdentityFromContext = %+v, want none", id)
	}
}

func TestIdentityMiddleware_NonUUIDSubjectShapes(t *testing.T) {
	subjects := []string{
		"user-42",
		"c0000000-0000-0000-0000-00000000000", // 35 chars
		" ",
		"c0000000-0000-0000-0000-00000000000g", // non-hex digit
	}
	for _, subj := range subjects {
		for _, tenant := range []string{testTenant, ""} {
			name := fmt.Sprintf("%q/tenant=%v", subj, tenant != "")
			t.Run(name, func(t *testing.T) {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("X-User-ID", subj)
				req.Header.Set("X-User-Role", "authenticated")
				if tenant != "" {
					req.Header.Set("X-Tenant-ID", tenant)
				}
				id, idOK, caller, callerOK := contextKeys(req)
				if idOK {
					t.Errorf("IdentityFromContext = %+v, want none", id)
				}
				if callerOK {
					t.Errorf("TenantlessCallerFromContext = %+v, want none", caller)
				}
			})
		}
	}
}

// uuid.Parse is the verifier's check (verify.go), so the middleware accepts the same forms.
func TestIdentityMiddleware_VerifierSubjectFormsBuildIdentity(t *testing.T) {
	forms := []struct{ name, subject string }{
		{"canonical", "c0000000-0000-0000-0000-000000000001"},
		{"upper-case", "C0000000-0000-0000-0000-000000000001"},
		{"braced", "{c0000000-0000-0000-0000-000000000001}"},
		{"urn", "urn:uuid:c0000000-0000-0000-0000-000000000001"},
		{"32-hex", "c0000000000000000000000000000001"},
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-Tenant-ID", testTenant)
			req.Header.Set("X-User-ID", f.subject)
			req.Header.Set("X-User-Role", "authenticated")

			id, idOK, _, callerOK := contextKeys(req)
			if !idOK {
				t.Fatalf("no identity for subject form %q", f.subject)
			}
			if id.Subject != f.subject || id.TenantID != testTenant {
				t.Errorf("identity = %+v, want Subject %q TenantID %q", id, f.subject, testTenant)
			}
			if callerOK {
				t.Error("tenant-less caller set beside an identity")
			}

			bare := httptest.NewRequest("POST", "/v1/workspaces", nil)
			bare.Header.Set("X-User-ID", f.subject)
			if _, _, caller, ok := contextKeys(bare); !ok || caller.Subject != f.subject {
				t.Errorf("tenant-less caller = %+v (ok %v), want Subject %q", caller, ok, f.subject)
			}
		})
	}
}
