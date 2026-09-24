package gateway

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestTenantlessProvisioningNearMissesForbidden(t *testing.T) {
	cases := []struct {
		name, method, path string
		want               int // 404 when the service segment itself does not route
	}{
		{"PUT", "PUT", provisioningPath, http.StatusForbidden},
		{"PATCH", "PATCH", provisioningPath, http.StatusForbidden},
		{"DELETE", "DELETE", provisioningPath, http.StatusForbidden},
		{"HEAD", "HEAD", provisioningPath, http.StatusForbidden},
		{"OPTIONS", "OPTIONS", provisioningPath, http.StatusForbidden},
		{"encoded slash", "POST", "/api/tenancy/v1%2Fworkspaces", http.StatusForbidden},
		{"encoded letter", "POST", "/api/tenancy/v1/%77orkspaces", http.StatusForbidden},
		{"dot segment", "POST", "/api/tenancy/v1/./workspaces", http.StatusForbidden},
		{"double slash", "POST", "/api/tenancy/v1//workspaces", http.StatusForbidden},
		{"case of resource", "POST", "/api/tenancy/v1/Workspaces", http.StatusForbidden},
		{"case of service", "POST", "/api/Tenancy/v1/workspaces", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := setupGateway(t)
			tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})

			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request(tc.method, tc.path, tok))
			if rec.Code != tc.want {
				t.Fatalf("tenant-less: status = %d, want %d", rec.Code, tc.want)
			}
			for svc, c := range tg.caps {
				if c.hits != 0 {
					t.Fatalf("tenant-less: %s upstream hit %d time(s), want 0", svc, c.hits)
				}
			}
			if tc.want == http.StatusNotFound {
				return
			}
			// With a tenant the same request is proxied, so the 403 is authorization.
			rec = httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request(tc.method, tc.path, tg.validToken(t)))
			if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
				t.Fatalf("with tenant: status = %d, tenancy hits = %d, want 200 and 1", rec.Code, tg.caps["tenancy"].hits)
			}
		})
	}
}

func TestTenantlessProvisioningQueryStringStillExempt(t *testing.T) {
	tg := setupGateway(t)
	tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, request("POST", provisioningPath+"?x=1", tok))

	if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
		t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, tg.caps["tenancy"].hits)
	}
	assertHeader(t, tg.caps["tenancy"].header, "X-Tenant-ID", "")
	assertHeader(t, tg.caps["tenancy"].header, "X-User-ID", testSubject)
}

// Every client copy must be replaced by the token's value, not dropped or appended to.
func TestTenantlessProvisioningOverwritesClientIdentityHeaders(t *testing.T) {
	cases := []struct {
		name, tokenEmail string
	}{
		{"token with email", testEmail},
		{"token without email", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := setupGateway(t)
			tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: tc.tokenEmail})
			r := request("POST", provisioningPath, tok)
			r.Header.Set("X-Tenant-ID", "tenant-evil")
			r.Header.Set("X-User-ID", "attacker")
			r.Header.Set("X-User-Role", "operator")
			r.Header.Set("X-User-Email", "evil@x")

			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
				t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, tg.caps["tenancy"].hits)
			}
			h := tg.caps["tenancy"].header
			// The gateway sends X-Tenant-ID present and empty; identityMiddleware reads that as no tenant.
			for key, want := range map[string]string{
				"X-Tenant-ID":  "",
				"X-User-ID":    testSubject,
				"X-User-Role":  testRole,
				"X-User-Email": tc.tokenEmail,
			} {
				if got := h.Values(key); !slices.Equal(got, []string{want}) {
					t.Errorf("upstream %s = %q, want [%q]", key, got, want)
				}
			}
		})
	}
}

func TestTenantBearingTokenOnProvisioningKeepsTenant(t *testing.T) {
	tg := setupGateway(t)
	tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant, Email: testEmail})

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, request("POST", provisioningPath, tok))

	if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
		t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, tg.caps["tenancy"].hits)
	}
	assertHeader(t, tg.caps["tenancy"].header, "X-Tenant-ID", testTenant)
	assertHeader(t, tg.caps["tenancy"].header, "X-User-Email", testEmail)
}
