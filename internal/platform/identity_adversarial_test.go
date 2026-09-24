package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// The gateway sends X-Tenant-ID present and empty (TestTenantlessProvisioningOverwritesClientIdentityHeaders);
// both that shape and an absent header must take the tenant-less branch after a real HTTP hop.
func TestIdentityMiddleware_EmptyTenantHeaderOverTheWire(t *testing.T) {
	cases := []struct {
		name       string
		tenant     []string // nil: header absent
		wantHeader bool
	}{
		{"present and empty", []string{""}, true},
		{"absent", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				sawHeader, idOK, callerOK bool
				caller                    auth.Identity
				tenantCtx                 string
			)
			srv := httptest.NewServer(chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, sawHeader = r.Header["X-Tenant-Id"]
				_, idOK = auth.IdentityFromContext(r.Context())
				caller, callerOK = auth.TenantlessCallerFromContext(r.Context())
				tenantCtx = TenantIDFromContext(r.Context())
			}), tenantIDMiddleware, identityMiddleware))
			t.Cleanup(srv.Close)

			req, err := http.NewRequest("POST", srv.URL+"/v1/workspaces", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.tenant != nil {
				req.Header["X-Tenant-Id"] = tc.tenant
			}
			req.Header.Set("X-User-ID", "user-42")
			req.Header.Set("X-User-Role", "authenticated")
			req.Header.Set("X-User-Email", "ada@example.test")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()

			if sawHeader != tc.wantHeader {
				t.Fatalf("X-Tenant-ID present on the server = %v, want %v", sawHeader, tc.wantHeader)
			}
			want := auth.Identity{Subject: "user-42", Role: "authenticated", Email: "ada@example.test"}
			if !callerOK || caller != want {
				t.Errorf("tenant-less caller = %+v (ok %v), want %+v", caller, callerOK, want)
			}
			if idOK {
				t.Error("IdentityFromContext reports an identity for a tenant-less caller")
			}
			if tenantCtx != "" {
				t.Errorf("tenant in context = %q, want none", tenantCtx)
			}
		})
	}
}
