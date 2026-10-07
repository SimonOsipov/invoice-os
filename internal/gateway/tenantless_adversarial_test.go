package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

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

const (
	joinEmail      = "ada@corp.example"
	joinInviteID   = "3f2b8c1e-7d4a-4b6f-9a1c-5e8d2f0a7b3c"
	joinMinePath   = "/api/tenancy/v1/invitations/mine"
	joinAcceptPath = "/api/tenancy/v1/invitations/" + joinInviteID + "/accept"
)

// tenantlessClaims shapes a GoTrue-style token with no tenant; an empty email or sid omits the claim.
type tenantlessClaims struct {
	email, sid string
	meta       map[string]any
}

func (s *sidSigner) tenantlessToken(t *testing.T, sub string, c tenantlessClaims) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":  sidIssuer,
		"sub":  sub,
		"aud":  "authenticated",
		"iat":  now.Unix(),
		"exp":  now.Add(time.Hour).Unix(),
		"role": testRole,
	}
	if c.email != "" {
		claims["email"] = c.email
	}
	if c.sid != "" {
		claims["session_id"] = c.sid
	}
	if c.meta != nil {
		claims["user_metadata"] = c.meta
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = s.kid
	out, err := tok.SignedString(s.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return out
}

// confirmedUser is the measured GoTrue GET /user body, edited by edit.
func confirmedUser(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/gotrue_user_v2.197.0.json")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode testdata: %v", err)
	}
	if m["email"] != joinEmail || m["email_confirmed_at"] == nil {
		t.Fatalf("testdata must answer a confirmed %s, got email=%v email_confirmed_at=%v", joinEmail, m["email"], m["email_confirmed_at"])
	}
	if edit != nil {
		edit(m)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(out)
}

// confirmedUserSized pads the confirmed body with one JSON field to exactly size bytes.
func confirmedUserSized(t *testing.T, size int) string {
	t.Helper()
	base := len(confirmedUser(t, func(m map[string]any) { m["pad"] = "" }))
	if size < base {
		t.Fatalf("size %d is below the unpadded body (%d)", size, base)
	}
	body := confirmedUser(t, func(m map[string]any) { m["pad"] = strings.Repeat("a", size-base) })
	if len(body) != size {
		t.Fatalf("padded body = %d bytes, want %d", len(body), size)
	}
	return body
}

func joinRig(t *testing.T, userBody string) (*sessionRig, *userFake) {
	t.Helper()
	fake := newUserFake(t, http.StatusOK, userBody)
	return newSessionRig(t, fake.URL, nil, nil), fake
}

func (rg *sessionRig) do(method, path, bearer string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rg.handler.ServeHTTP(rec, request(method, path, bearer))
	return rec
}

func assertForbiddenNoUpstream(t *testing.T, rg *sessionRig, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Errorf("%s: status = %d, want 403", what, rec.Code)
	} else if got := errorBody(t, rec); got != "forbidden" {
		t.Errorf("%s: error = %q, want %q", what, got, "forbidden")
	}
	if n, m := rg.upstream.Hits(), rg.other.Hits(); n != 0 || m != 0 {
		t.Errorf("%s: upstream hits = %d tenancy, %d portfolio, want 0", what, n, m)
	}
}

func TestJoinRoutesAdmitAConfirmedTenantlessSession(t *testing.T) {
	cases := []struct{ name, method, path, upstreamPath string }{
		{"list", http.MethodGet, joinMinePath, "/v1/invitations/mine"},
		{"accept", http.MethodPost, joinAcceptPath, "/v1/invitations/" + joinInviteID + "/accept"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg, fake := joinRig(t, confirmedUser(t, nil))
			tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1})

			rec := rg.do(tc.method, tc.path, tok)

			if rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
				t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1 (body %q)", rec.Code, rg.upstream.Hits(), rec.Body.String())
			}
			if n := fake.Hits(); n != 1 {
				t.Errorf("GoTrue /user calls = %d, want 1", n)
			}
			if method, path := rg.upstream.Last(); method != tc.method || path != tc.upstreamPath {
				t.Errorf("upstream got %s %s, want %s %s", method, path, tc.method, tc.upstreamPath)
			}
			h := rg.upstream.Header()
			for key, want := range map[string]string{"X-Tenant-ID": "", "X-User-ID": subjectS1, "X-User-Email": joinEmail} {
				if got := h.Values(key); !slices.Equal(got, []string{want}) {
					t.Errorf("upstream %s = %q, want [%q]", key, got, want)
				}
			}
		})
	}
}

// user_metadata.email_verified is user-writable (PUT /user), so no route may read it.
func TestJoinRoutesIgnoreAWrittenMetadataClaim(t *testing.T) {
	rg, _ := joinRig(t, confirmedUser(t, func(m map[string]any) { m["email_confirmed_at"] = nil }))
	tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{
		email: joinEmail, sid: sid1, meta: map[string]any{"email_verified": true},
	})

	assertForbiddenNoUpstream(t, rg, rg.do(http.MethodGet, joinMinePath, tok), "GET mine")
	assertForbiddenNoUpstream(t, rg, rg.do(http.MethodPost, joinAcceptPath, tok), "POST accept")

	// Control: the token-accept route stays open to the same unconfirmed session.
	rec := rg.do(http.MethodPost, acceptPath, tok)
	if rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
		t.Errorf("control POST %s = %d, tenancy hits = %d, want 200 and 1", acceptPath, rec.Code, rg.upstream.Hits())
	}
}

func TestJoinRoutesRefuseUnprovenConfirmation(t *testing.T) {
	meta := map[string]any{"email_verified": true}
	sidTok := func(rg *sessionRig, t *testing.T) string {
		return rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1, meta: meta})
	}
	cases := []struct {
		name string
		body string
		tok  func(*sessionRig, *testing.T) string
	}{
		{"another email", confirmedUser(t, func(m map[string]any) { m["email"] = "bob@corp.example" }), sidTok},
		{"null confirmation", confirmedUser(t, func(m map[string]any) { m["email_confirmed_at"] = nil }), sidTok},
		{"absent confirmation", confirmedUser(t, func(m map[string]any) { delete(m, "email_confirmed_at") }), sidTok},
		{"undecodable body", "not json", sidTok},
		// The decoder fills email and email_confirmed_at, then fails on the repeated email.
		{"type error after a valid email", `{"email":"ada@corp.example","email_confirmed_at":"2026-10-07T17:04:47Z","email":5}`, sidTok},
		{"null body", "null", sidTok},
		{"empty body", "", sidTok},
		{"array body", "[]", sidTok},
		{"empty object", "{}", sidTok},
		{"empty-string confirmation", confirmedUser(t, func(m map[string]any) { m["email_confirmed_at"] = "" }), sidTok},
		{"boolean confirmation", confirmedUser(t, func(m map[string]any) { m["email_confirmed_at"] = true }), sidTok},
		{"non-string email", confirmedUser(t, func(m map[string]any) { m["email"] = 5 }), sidTok},
		{"top-level email absent", confirmedUser(t, func(m map[string]any) { delete(m, "email") }), sidTok},
		{"only user_metadata.email matches", confirmedUser(t, func(m map[string]any) {
			m["email"] = "bob@corp.example"
			m["user_metadata"] = map[string]any{"email": joinEmail, "email_verified": true}
		}), sidTok},
		{"17 KiB body", confirmedUserSized(t, 17<<10), sidTok},
		{"no session_id", confirmedUser(t, nil), func(rg *sessionRig, t *testing.T) string {
			return rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, meta: meta})
		}},
		{"mock issuer token", confirmedUser(t, nil), func(rg *sessionRig, t *testing.T) string {
			tok, err := rg.mock.Mint(auth.MintOptions{Subject: subjectS1, Role: testRole, Email: joinEmail})
			if err != nil {
				t.Fatalf("mint: %v", err)
			}
			return tok
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg, _ := joinRig(t, tc.body)
			tok := tc.tok(rg, t)

			assertForbiddenNoUpstream(t, rg, rg.do(http.MethodGet, joinMinePath, tok), "GET mine")
			assertForbiddenNoUpstream(t, rg, rg.do(http.MethodPost, joinAcceptPath, tok), "POST accept")

			// Control: a tenant-bearing route answers 200 with the same /user answer.
			rec := rg.get(rg.signer.token(t, subjectS1, sid2))
			if rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
				t.Errorf("control GET /me = %d, tenancy hits = %d, want 200 and 1", rec.Code, rg.upstream.Hits())
			}
		})
	}
}

func TestJoinRoutesRefuseEmptyEmails(t *testing.T) {
	cases := []struct {
		name, tokenEmail, userEmail string
		admitted                    bool
	}{
		{"both empty", "", "", false},
		{"token email empty", "", joinEmail, false},
		{"/user email empty", joinEmail, "", false},
		{"control: both set and equal", joinEmail, joinEmail, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg, _ := joinRig(t, confirmedUser(t, func(m map[string]any) { m["email"] = tc.userEmail }))
			tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: tc.tokenEmail, sid: sid1})

			if tc.admitted {
				if rec := rg.do(http.MethodGet, joinMinePath, tok); rec.Code != http.StatusOK {
					t.Errorf("GET mine = %d, want 200", rec.Code)
				}
				if rec := rg.do(http.MethodPost, joinAcceptPath, tok); rec.Code != http.StatusOK {
					t.Errorf("POST accept = %d, want 200", rec.Code)
				}
				return
			}
			assertForbiddenNoUpstream(t, rg, rg.do(http.MethodGet, joinMinePath, tok), "GET mine")
			assertForbiddenNoUpstream(t, rg, rg.do(http.MethodPost, joinAcceptPath, tok), "POST accept")
		})
	}
}

func TestJoinRoutesConfirmationMatchesCaseAndSpace(t *testing.T) {
	rg, _ := joinRig(t, confirmedUser(t, func(m map[string]any) { m["email"] = " ada@corp.example" }))
	tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: "Ada@Corp.Example", sid: sid1})

	if rec := rg.do(http.MethodGet, joinMinePath, tok); rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
		t.Fatalf("GET mine = %d, tenancy hits = %d, want 200 and 1", rec.Code, rg.upstream.Hits())
	}
	// The identity tenancy filters on is the token's own address, byte for byte.
	assertHeader(t, rg.upstream.Header(), "X-User-Email", "Ada@Corp.Example")
}

func TestJoinRoutesNearMissesForbidden(t *testing.T) {
	rg, _ := joinRig(t, confirmedUser(t, nil))
	tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1})
	const base = "/api/tenancy/v1/invitations/"
	if rec := rg.do(http.MethodGet, joinMinePath, tok); rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
		t.Errorf("control: GET mine = %d, tenancy hits = %d, want 200 and 1", rec.Code, rg.upstream.Hits())
	}
	ctl := rg.upstream.Hits()

	cases := []struct{ name, method, path string }{
		{"POST mine", "POST", joinMinePath},
		{"PUT mine", "PUT", joinMinePath},
		{"DELETE mine", "DELETE", joinMinePath},
		{"HEAD mine", "HEAD", joinMinePath},
		{"OPTIONS mine", "OPTIONS", joinMinePath},
		{"PATCH accept", "PATCH", joinAcceptPath},
		{"HEAD accept", "HEAD", joinAcceptPath},
		{"GET accept", "GET", joinAcceptPath},
		{"PUT accept", "PUT", joinAcceptPath},
		{"upper-case uuid", "POST", base + strings.ToUpper(joinInviteID) + "/accept"},
		{"braced uuid", "POST", base + "{" + joinInviteID + "}/accept"},
		{"uuid without dashes", "POST", base + strings.ReplaceAll(joinInviteID, "-", "") + "/accept"},
		{"not a uuid", "POST", base + "x/accept"},
		{"mine trailing slash", "GET", joinMinePath + "/"},
		{"mine extra segment", "GET", joinMinePath + "/x"},
		{"accept trailing slash", "POST", joinAcceptPath + "/"},
		{"resend", "POST", base + joinInviteID + "/resend"},
		{"bare id", "GET", base + joinInviteID},
		{"encoded slash", "GET", "/api/tenancy/v1%2Finvitations%2Fmine"},
		{"encoded slash before accept", "POST", base + joinInviteID + "%2Faccept"},
		{"encoded letter", "POST", base + joinInviteID + "/%61ccept"},
		{"double slash", "GET", "/api/tenancy/v1//invitations/mine"},
		{"double slash before id", "POST", base + "/" + joinInviteID + "/accept"},
		{"encoded slash after mine", "GET", joinMinePath + "%2F"},
		{"encoded letter in mine", "GET", "/api/tenancy/v1/invitations/%6dine"},
		{"encoded digit in id", "POST", base + "%33" + joinInviteID[1:] + "/accept"},
		{"space after id", "POST", base + joinInviteID + "%20/accept"},
		{"dot segment", "GET", "/api/tenancy/v1/./invitations/mine"},
		{"path parameter", "GET", joinMinePath + ";x=1"},
		{"other service", "GET", "/api/portfolio/v1/invitations/mine"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := rg.do(tc.method, tc.path, tok)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if got := errorBody(t, rec); got != "forbidden" {
				t.Errorf("error = %q, want %q", got, "forbidden")
			}
			if n, m := rg.upstream.Hits(), rg.other.Hits(); n != ctl || m != 0 {
				t.Errorf("upstream hits = %d tenancy (control left %d), %d portfolio, want no new hit", n, ctl, m)
			}
		})
	}
}

func TestJoinRoutesLeaveTenantBearingTokensAlone(t *testing.T) {
	rg, _ := joinRig(t, confirmedUser(t, func(m map[string]any) { m["email_confirmed_at"] = nil }))
	tok := rg.signer.token(t, subjectS1, sid1)

	rec := rg.do(http.MethodGet, joinMinePath, tok)

	if rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
		t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, rg.upstream.Hits())
	}
	if _, path := rg.upstream.Last(); path != "/v1/invitations/mine" {
		t.Errorf("upstream path = %q, want /v1/invitations/mine", path)
	}
	assertHeader(t, rg.upstream.Header(), "X-Tenant-ID", testTenant)
}

func TestJoinRoutesIgnoreMethodOverrideAndKeepQueryStrings(t *testing.T) {
	rg, _ := joinRig(t, confirmedUser(t, nil))
	tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1})
	do := func(method, path string, hdr map[string]string) *httptest.ResponseRecorder {
		r := request(method, path, tok)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		rg.handler.ServeHTTP(rec, r)
		return rec
	}

	if rec := do(http.MethodGet, joinMinePath+"?x=1", nil); rec.Code != http.StatusOK {
		t.Fatalf("control: GET mine with a query = %d, want 200", rec.Code)
	}
	if rec := do(http.MethodPost, joinAcceptPath+"?x=1", nil); rec.Code != http.StatusOK {
		t.Fatalf("control: POST accept with a query = %d, want 200", rec.Code)
	}
	ctl := rg.upstream.Hits()
	if ctl != 2 {
		t.Fatalf("control tenancy hits = %d, want 2", ctl)
	}

	for _, h := range []string{"X-HTTP-Method-Override", "X-HTTP-Method", "X-Method-Override"} {
		for _, tc := range []struct{ name, method, path, override string }{
			{"POST mine as GET", http.MethodPost, joinMinePath, http.MethodGet},
			{"GET accept as POST", http.MethodGet, joinAcceptPath, http.MethodPost},
		} {
			rec := do(tc.method, tc.path, map[string]string{h: tc.override})
			if rec.Code != http.StatusForbidden || rg.upstream.Hits() != ctl {
				t.Errorf("%s via %s: status = %d, tenancy hits = %d (control %d), want 403 and no new hit", tc.name, h, rec.Code, rg.upstream.Hits(), ctl)
			}
		}
	}
}

// Confirmation lives in the request context; no client header can stand in for it.
func TestJoinRoutesIgnoreClientConfirmationHeaders(t *testing.T) {
	spoof := map[string]string{
		"X-User-Email": joinEmail, "X-Confirmed-Email": joinEmail, "X-Email-Confirmed": "true",
		"X-Verified-Email": joinEmail, "X-Email-Verified": "true",
	}
	do := func(rg *sessionRig, method, path, tok string) *httptest.ResponseRecorder {
		r := request(method, path, tok)
		for k, v := range spoof {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		rg.handler.ServeHTTP(rec, r)
		return rec
	}

	t.Run("unconfirmed session", func(t *testing.T) {
		rg, _ := joinRig(t, confirmedUser(t, func(m map[string]any) { m["email_confirmed_at"] = nil }))
		tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1})
		assertForbiddenNoUpstream(t, rg, do(rg, http.MethodGet, joinMinePath, tok), "GET mine")
		assertForbiddenNoUpstream(t, rg, do(rg, http.MethodPost, joinAcceptPath, tok), "POST accept")
	})
	t.Run("mock-issuer token", func(t *testing.T) {
		rg, _ := joinRig(t, confirmedUser(t, nil))
		tok, err := rg.mock.Mint(auth.MintOptions{Subject: subjectS1, Role: testRole, Email: joinEmail})
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		assertForbiddenNoUpstream(t, rg, do(rg, http.MethodGet, joinMinePath, tok), "GET mine")
	})
	t.Run("another address in the client header", func(t *testing.T) {
		rg, _ := joinRig(t, confirmedUser(t, nil))
		tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1})
		r := request(http.MethodGet, joinMinePath, tok)
		r.Header.Set("X-User-Email", "evil@x")
		rec := httptest.NewRecorder()
		rg.handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK || rg.upstream.Hits() != 1 {
			t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, rg.upstream.Hits())
		}
		if got := rg.upstream.Header().Values("X-User-Email"); !slices.Equal(got, []string{joinEmail}) {
			t.Errorf("upstream X-User-Email = %q, want [%q]", got, joinEmail)
		}
	})
}

// A join route adds no verdict: revoked is 401, unavailable is 503 and uncached, as on every route.
func TestJoinRoutesKeepRevokedAndUnavailableVerdicts(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantRevoked bool
		uncached    bool
	}{
		{"session gone", http.StatusForbidden, gtError(403, "session_not_found"), true, false},
		{"user banned", http.StatusUnauthorized, gtError(401, "user_banned"), true, false},
		{"bad_jwt", http.StatusUnauthorized, gtError(401, "bad_jwt"), false, true},
		{"500 with a confirming body", http.StatusInternalServerError, confirmedUser(t, nil), false, true},
		{"404 with a confirming body", http.StatusNotFound, confirmedUser(t, nil), false, true},
	}
	routes := []struct{ method, path string }{{http.MethodGet, joinMinePath}, {http.MethodPost, joinAcceptPath}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newUserFake(t, tc.status, tc.body)
			rg := newSessionRig(t, fake.URL, nil, nil)
			refusal := rg.refusal(t)
			tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1})

			for _, rt := range routes {
				rec := rg.do(rt.method, rt.path, tok)
				if tc.wantRevoked {
					assertRevoked(t, rec, refusal)
				} else {
					assertUnavailable(t, rec)
				}
				if n := rg.upstream.Hits(); n != 0 {
					t.Errorf("%s %s: tenancy hits = %d, want 0", rt.method, rt.path, n)
				}
			}
			if tc.uncached {
				if n := fake.Hits(); n != len(routes) {
					t.Errorf("GoTrue /user calls = %d, want %d (a 503 is not cached)", n, len(routes))
				}
				fake.set(http.StatusOK, confirmedUser(t, nil))
				if rec := rg.do(http.MethodGet, joinMinePath, tok); rec.Code != http.StatusOK {
					t.Errorf("after GoTrue recovers: GET mine = %d, want 200 with no clock advance", rec.Code)
				}
			}
		})
	}
}

// A session confirmed once is revoked when GoTrue says so after the TTL.
func TestJoinRoutesConfirmedSessionThenRevoked(t *testing.T) {
	rg, fake := joinRig(t, confirmedUser(t, nil))
	refusal := rg.refusal(t)
	tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1})
	if rec := rg.do(http.MethodGet, joinMinePath, tok); rec.Code != http.StatusOK {
		t.Fatalf("control: GET mine = %d, want 200", rec.Code)
	}

	fake.set(http.StatusForbidden, gtError(403, "session_not_found"))
	rg.clock.Advance(SessionCheckTTL)
	assertRevoked(t, rg.do(http.MethodPost, joinAcceptPath, tok), refusal)
	if n := rg.upstream.Hits(); n != 1 {
		t.Errorf("tenancy hits = %d, want 1 (only the control)", n)
	}
}

// The provisioning and token-accept exemptions need no confirmation, whatever /user answers.
func TestTenantlessOpenRoutesNeedNoConfirmation(t *testing.T) {
	for _, body := range []string{"not json", confirmedUser(t, func(m map[string]any) { m["email_confirmed_at"] = nil })} {
		rg, _ := joinRig(t, body)
		tok := rg.signer.tenantlessToken(t, subjectS1, tenantlessClaims{email: joinEmail, sid: sid1})
		for _, path := range []string{provisioningPath, acceptPath} {
			if rec := rg.do(http.MethodPost, path, tok); rec.Code != http.StatusOK {
				t.Errorf("POST %s = %d, want 200", path, rec.Code)
			}
		}
		if n := rg.upstream.Hits(); n != 2 {
			t.Errorf("tenancy hits = %d, want 2", n)
		}
	}
}
