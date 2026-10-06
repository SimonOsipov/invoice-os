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

const acceptPath = "/api/tenancy/v1/invitations/accept"

// The second tenant-less route: a new invitee's first token carries no tenant.
func TestTenantlessAcceptRouteIsTheSecondExemption(t *testing.T) {
	for _, tc := range []struct{ name, path, upstreamPath string }{
		{"accept", acceptPath, "/v1/invitations/accept"},
		{"provisioning", provisioningPath, "/v1/workspaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg := setupGateway(t)
			tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})

			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("POST", tc.path, tok))

			cap := tg.caps["tenancy"]
			if rec.Code != http.StatusOK || cap.hits != 1 {
				t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1 (body %q)", rec.Code, cap.hits, rec.Body.String())
			}
			if cap.path != tc.upstreamPath {
				t.Errorf("upstream path = %q, want %q", cap.path, tc.upstreamPath)
			}
			assertHeader(t, cap.header, "X-Tenant-ID", "")
			assertHeader(t, cap.header, "X-User-ID", testSubject)
			assertHeader(t, cap.header, wireUserEmail, testEmail)
		})
	}
}

func TestTenantlessAcceptNearMissesForbidden(t *testing.T) {
	// Positive pair: the exact route is exempt, so no near-miss below can pass by the exemption being absent.
	tg := setupGateway(t)
	tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, request("POST", acceptPath, tok))
	if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
		t.Fatalf("control: POST %s = %d, tenancy hits = %d, want 200 and 1", acceptPath, rec.Code, tg.caps["tenancy"].hits)
	}

	cases := []struct{ name, method, path, service string }{
		{"GET accept", "GET", acceptPath, "tenancy"},
		{"HEAD accept", "HEAD", acceptPath, "tenancy"},
		{"PUT accept", "PUT", acceptPath, "tenancy"},
		{"invitations collection", "POST", "/api/tenancy/v1/invitations", "tenancy"},
		{"extra segment", "POST", acceptPath + "/x", "tenancy"},
		{"trailing slash", "POST", acceptPath + "/", "tenancy"},
		{"encoded slash", "POST", "/api/tenancy/v1%2Finvitations%2Faccept", "tenancy"},
		{"encoded letter", "POST", "/api/tenancy/v1/invitations/%61ccept", "tenancy"},
		{"other service", "POST", "/api/portfolio/v1/invitations/accept", "portfolio"},
		{"DELETE accept", "DELETE", acceptPath, "tenancy"},
		{"PATCH accept", "PATCH", acceptPath, "tenancy"},
		{"OPTIONS accept", "OPTIONS", acceptPath, "tenancy"},
		{"case of the resource", "POST", "/api/tenancy/v1/Invitations/accept", "tenancy"},
		{"case of the verb segment", "POST", "/api/tenancy/v1/invitations/Accept", "tenancy"},
		{"double slash", "POST", "/api/tenancy/v1//invitations/accept", "tenancy"},
		{"dot segment", "POST", "/api/tenancy/v1/./invitations/accept", "tenancy"},
		{"path parameter", "POST", acceptPath + ";x=1", "tenancy"},
		{"encoded trailing slash", "POST", acceptPath + "%2F", "tenancy"},
		{"encoded dot before the verb", "POST", "/api/tenancy/v1/invitations/%2e/accept", "tenancy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := setupGateway(t)
			tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})

			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request(tc.method, tc.path, tok))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("tenant-less: status = %d, want 403", rec.Code)
			}
			if got := errorBody(t, rec); got != "forbidden" {
				t.Errorf("error = %q, want %q", got, "forbidden")
			}
			for svc, c := range tg.caps {
				if c.hits != 0 {
					t.Fatalf("tenant-less: %s upstream hit %d time(s), want 0", svc, c.hits)
				}
			}

			// With a tenant the same request is proxied, so the 403 above is authorization.
			rec = httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request(tc.method, tc.path, tg.validToken(t)))
			if rec.Code != http.StatusOK || tg.caps[tc.service].hits != 1 {
				t.Fatalf("with tenant: status = %d, %s hits = %d, want 200 and 1", rec.Code, tc.service, tg.caps[tc.service].hits)
			}
		})
	}
}

// The accept route keeps provisioning's guarantees: a query string stays exempt, a client cannot choose the identity, a tenant is never stripped.
func TestTenantlessAcceptRouteKeepsTheIdentityContract(t *testing.T) {
	t.Run("query string still exempt", func(t *testing.T) {
		tg := setupGateway(t)
		tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})

		rec := httptest.NewRecorder()
		tg.handler.ServeHTTP(rec, request("POST", acceptPath+"?x=1", tok))

		if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
			t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, tg.caps["tenancy"].hits)
		}
	})

	t.Run("client identity headers are replaced by the token's", func(t *testing.T) {
		tg := setupGateway(t)
		tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})
		r := request("POST", acceptPath, tok)
		r.Header.Set("X-Tenant-ID", "tenant-evil")
		r.Header.Set("X-User-ID", "attacker")
		r.Header.Set("X-User-Role", "operator")
		r.Header.Set("X-User-Email", "evil@x")

		rec := httptest.NewRecorder()
		tg.handler.ServeHTTP(rec, r)

		if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
			t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, tg.caps["tenancy"].hits)
		}
		for key, want := range map[string]string{"X-Tenant-ID": "", "X-User-ID": testSubject, "X-User-Role": testRole, "X-User-Email": testEmail} {
			if got := tg.caps["tenancy"].header.Values(key); !slices.Equal(got, []string{want}) {
				t.Errorf("upstream %s = %q, want [%q]", key, got, want)
			}
		}
	})

	t.Run("a tenant-bearing token is proxied with its tenant", func(t *testing.T) {
		tg := setupGateway(t)
		tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant, Email: testEmail})

		rec := httptest.NewRecorder()
		tg.handler.ServeHTTP(rec, request("POST", acceptPath, tok))

		if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
			t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, tg.caps["tenancy"].hits)
		}
		assertHeader(t, tg.caps["tenancy"].header, "X-Tenant-ID", testTenant)
	})
}
