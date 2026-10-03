package platform

import (
	"fmt"
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
			req.Header.Set("X-User-ID", testSubject)
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
			want := auth.Identity{Subject: testSubject, Role: "authenticated", Email: "ada@example.test"}
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

// The wire hop must not change the verdict: a non-uuid or empty subject builds nothing, and the
// first of two X-User-ID lines decides.
func TestIdentityMiddleware_SubjectVerdictOverTheWire(t *testing.T) {
	cases := []struct {
		name string
		user []string // nil: header absent
		want string   // "" = no identity and no caller
	}{
		{"non-uuid", []string{"user-42"}, ""},
		{"empty", []string{""}, ""},
		{"absent", nil, ""},
		{"uuid", []string{testSubject}, testSubject},
		{"uuid then junk", []string{testSubject, "user-42"}, testSubject},
		{"junk then uuid", []string{"user-42", testSubject}, ""},
	}
	for _, tc := range cases {
		for _, withTenant := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/tenant=%v", tc.name, withTenant), func(t *testing.T) {
				var (
					id, caller     auth.Identity
					idOK, callerOK bool
				)
				srv := httptest.NewServer(chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					id, idOK = auth.IdentityFromContext(r.Context())
					caller, callerOK = auth.TenantlessCallerFromContext(r.Context())
				}), tenantIDMiddleware, identityMiddleware))
				t.Cleanup(srv.Close)

				req, err := http.NewRequest("GET", srv.URL+"/", nil)
				if err != nil {
					t.Fatal(err)
				}
				if tc.user != nil {
					req.Header["X-User-Id"] = tc.user
				}
				if withTenant {
					req.Header.Set("X-Tenant-ID", testTenant)
				}
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()

				if tc.want == "" {
					if idOK || callerOK {
						t.Errorf("identity %+v (%v) / caller %+v (%v), want neither", id, idOK, caller, callerOK)
					}
					return
				}
				got, gotOK, other := caller, callerOK, idOK
				if withTenant {
					got, gotOK, other = id, idOK, callerOK
				}
				if !gotOK || got.Subject != tc.want {
					t.Errorf("got %+v (ok %v), want Subject %q", got, gotOK, tc.want)
				}
				if other {
					t.Error("both an identity and a tenant-less caller were built")
				}
			})
		}
	}
}
