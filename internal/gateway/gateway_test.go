package gateway

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	testIssuer  = "https://mock.ascomply.test"
	testSubject = "11111111-1111-1111-1111-111111111111"
	testTenant  = "tenant-a"
	testRole    = "authenticated"
)

// capture records what an upstream service received, so tests can assert on
// routing (path) and injection (headers).
type capture struct {
	hits   int
	path   string
	header http.Header
}

// testGateway is a fully in-process gateway: an in-memory mock issuer, a Verifier
// that fetches that issuer's JWKS over httptest, and one recording upstream per
// routed service. No Railway, no Postgres.
type testGateway struct {
	handler http.Handler
	issuer  *auth.MockIssuer
	caps    map[string]*capture
}

func setupGateway(t *testing.T) *testGateway {
	t.Helper()
	issuer, err := auth.NewMockIssuer(testIssuer)
	if err != nil {
		t.Fatalf("mock issuer: %v", err)
	}
	jwks := httptest.NewServer(issuer.JWKSHandler())
	t.Cleanup(jwks.Close)
	verifier, err := auth.NewVerifier(auth.Config{Issuer: testIssuer, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	services := []string{"tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications"}
	caps := make(map[string]*capture, len(services))
	upstreams := make(map[string]*url.URL, len(services))
	for _, svc := range services {
		c := &capture{}
		caps[svc] = c
		srv := httptest.NewServer(recordingUpstream(c))
		t.Cleanup(srv.Close)
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatalf("parse upstream url: %v", err)
		}
		upstreams[svc] = u
	}

	return &testGateway{
		handler: Handler(Options{Verifier: verifier, Upstreams: upstreams}),
		issuer:  issuer,
		caps:    caps,
	}
}

func recordingUpstream(c *capture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits++
		c.path = r.URL.Path
		c.header = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func (tg *testGateway) mint(t *testing.T, opts auth.MintOptions) string {
	t.Helper()
	tok, err := tg.issuer.Mint(opts)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

// validToken mints a token for the standard test tenant/user/role.
func (tg *testGateway) validToken(t *testing.T) string {
	return tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant})
}

func request(method, path, bearer string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

func TestUnauthenticated(t *testing.T) {
	tg := setupGateway(t)
	cases := map[string]string{
		"no token":        "",
		"malformed token": "not.a.jwt",
		"expired token":   tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant, TTL: -time.Hour}),
	}
	for name, bearer := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", bearer))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want %q", got, "Bearer")
			}
			if tg.caps["tenancy"].hits != 0 {
				t.Errorf("upstream was hit on a rejected request")
			}
		})
	}
}

func TestValidTokenRoutesAndInjects(t *testing.T) {
	tg := setupGateway(t)
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tg.validToken(t)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	cap := tg.caps["tenancy"]
	if cap.hits != 1 {
		t.Fatalf("tenancy hits = %d, want 1", cap.hits)
	}
	if cap.path != "/v1/ping" {
		t.Errorf("upstream path = %q, want %q (prefix must be stripped)", cap.path, "/v1/ping")
	}
	assertHeader(t, cap.header, headerTenantID, testTenant)
	assertHeader(t, cap.header, headerUserID, testSubject)
	assertHeader(t, cap.header, headerUserRole, testRole)
}

func TestClientSuppliedIdentityHeadersStripped(t *testing.T) {
	tg := setupGateway(t)
	r := request("GET", "/api/tenancy/v1/ping", tg.validToken(t))
	// A hostile client tries to impersonate another tenant and escalate role.
	r.Header.Set(headerTenantID, "tenant-evil")
	r.Header.Set(headerUserID, "attacker")
	r.Header.Set(headerUserRole, "operator")

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cap := tg.caps["tenancy"]
	assertHeader(t, cap.header, headerTenantID, testTenant)
	assertHeader(t, cap.header, headerUserID, testSubject)
	assertHeader(t, cap.header, headerUserRole, testRole)
}

func TestRequestIDPropagated(t *testing.T) {
	tg := setupGateway(t)
	// The platform kit's requestIDMiddleware runs upstream of this handler and
	// puts the id in the context; simulate that here.
	r := request("GET", "/api/tenancy/v1/ping", tg.validToken(t))
	r = r.WithContext(platform.WithRequestID(r.Context(), "req-xyz"))

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertHeader(t, tg.caps["tenancy"].header, headerRequestID, "req-xyz")
}

func TestEmptyTenantForbidden(t *testing.T) {
	tg := setupGateway(t)
	// Valid, authenticated token — but no tenant claim: authenticated, not authorized.
	tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole})

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tok))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if tg.caps["tenancy"].hits != 0 {
		t.Errorf("upstream was hit on a forbidden request")
	}
}

func TestUnknownPrefixNotFound(t *testing.T) {
	tg := setupGateway(t)
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, request("GET", "/api/nope/x", tg.validToken(t)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRoutesEverySevenService(t *testing.T) {
	tg := setupGateway(t)
	tok := tg.validToken(t)
	if len(tg.caps) != 7 {
		t.Fatalf("gateway routes %d services, want 7 -- the loop below would assert nothing", len(tg.caps))
	}
	for svc, cap := range tg.caps {
		t.Run(svc, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("GET", "/api/"+svc+"/ping", tok))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if cap.hits != 1 {
				t.Errorf("%s hits = %d, want 1 (routed to wrong service?)", svc, cap.hits)
			}
			if cap.path != "/ping" {
				t.Errorf("%s upstream path = %q, want %q", svc, cap.path, "/ping")
			}
		})
	}
}

func TestUnreachableUpstreamBadGateway(t *testing.T) {
	issuer, err := auth.NewMockIssuer(testIssuer)
	if err != nil {
		t.Fatalf("mock issuer: %v", err)
	}
	jwks := httptest.NewServer(issuer.JWKSHandler())
	t.Cleanup(jwks.Close)
	verifier, err := auth.NewVerifier(auth.Config{Issuer: testIssuer, JWKSURL: jwks.URL})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	// Point tenancy at a server we immediately close: dials will be refused.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL, _ := url.Parse(dead.URL)
	dead.Close()

	h := Handler(Options{Verifier: verifier, Upstreams: map[string]*url.URL{"tenancy": deadURL}})
	tok, err := issuer.Mint(auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tok))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

// TestHealthCoexistsUnauthenticated mirrors main's mux wiring: /healthz is public
// while everything under /api/ requires a token.
func TestHealthCoexistsUnauthenticated(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/api/", tg.handler)

	health := httptest.NewRecorder()
	mux.ServeHTTP(health, request("GET", "/healthz", ""))
	if health.Code != http.StatusOK {
		t.Errorf("GET /healthz (no token) = %d, want 200", health.Code)
	}

	api := httptest.NewRecorder()
	mux.ServeHTTP(api, request("GET", "/api/tenancy/v1/ping", ""))
	if api.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/... (no token) = %d, want 401", api.Code)
	}
}

// TestMockLoginRoundTrip proves the mock login path end to end: mint via
// /auth/login, then use the token through a proxied route.
func TestMockLoginRoundTrip(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, platform.PostureLocal))
	mux.Handle("/api/", tg.handler)

	login := httptest.NewRecorder()
	body := strings.NewReader(`{"tenant_id":"tenant-a"}`)
	mux.ServeHTTP(login, httptest.NewRequest("POST", "/auth/login", body))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", login.Code)
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(login.Body).Decode(&resp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if resp.TokenType != "bearer" || resp.AccessToken == "" {
		t.Fatalf("login response = %+v, want a bearer access_token", resp)
	}

	api := httptest.NewRecorder()
	mux.ServeHTTP(api, request("GET", "/api/tenancy/v1/ping", resp.AccessToken))
	if api.Code != http.StatusOK {
		t.Fatalf("proxied request with minted token = %d, want 200", api.Code)
	}
	assertHeader(t, tg.caps["tenancy"].header, headerTenantID, "tenant-a")
}

// Hosted-allowlist personas mirror db/seed.dev.sql's tenants and memberships rows:
// subject and tenant are seeded; role is the GoTrue JWT role every client sends,
// not the seed's memberships.role (admin/preparer/reviewer).
const (
	firmSubject               = "c0000000-0000-0000-0000-000000000001"
	firmTenant                = "11111111-1111-1111-1111-111111111111"
	inhouseSubject            = "c0000000-0000-0000-0000-000000000002"
	inhouseTenant             = "22222222-2222-2222-2222-222222222222"
	preparerSubject           = "c0000000-0000-0000-0000-000000000003" // firm-tenant preparer; allowlisted so a blocked submit is demonstrable on the hosted build
	finApproverSubject        = "c0000000-0000-0000-0000-000000000004" // firm-tenant reviewer staffed fin_mgr + fin_dir
	complianceApproverSubject = "c0000000-0000-0000-0000-000000000005" // firm-tenant reviewer staffed compliance
	seededNotAllowlisted      = "c0000000-0000-0000-0000-000000000006" // seed-only preparer, never a login identity
	unlistedTenant            = "99999999-9999-9999-9999-999999999999"
	unlistedSubject           = "88888888-8888-8888-8888-888888888888"
	personaRole               = "authenticated"
)

func TestMockLoginHostedAllowlist(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, platform.PostureHosted))

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"firm persona", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, firmSubject, firmTenant, personaRole), http.StatusOK},
		{"in-house persona", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, inhouseSubject, inhouseTenant, personaRole), http.StatusOK},
		{"preparer persona", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, preparerSubject, firmTenant, personaRole), http.StatusOK},
		{"fin approver persona", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, finApproverSubject, firmTenant, personaRole), http.StatusOK},
		{"compliance approver persona", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, complianceApproverSubject, firmTenant, personaRole), http.StatusOK},
		{"fin approver on the wrong tenant", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, finApproverSubject, inhouseTenant, personaRole), http.StatusForbidden},
		{"compliance approver on the wrong tenant", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, complianceApproverSubject, inhouseTenant, personaRole), http.StatusForbidden},
		// the seed's own memberships.role for both -- the substitution the persona table warns about.
		{"fin approver with the seed's domain role", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":"reviewer"}`, finApproverSubject, firmTenant), http.StatusForbidden},
		{"compliance approver with the seed's domain role", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":"reviewer"}`, complianceApproverSubject, firmTenant), http.StatusForbidden},
		{"fin approver with tenant and role transposed", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, finApproverSubject, personaRole, firmTenant), http.StatusForbidden},
		// the comparison is a plain Go struct == on strings: case and whitespace variants must not
		// match. firmTenant/inhouseTenant are all-digit UUIDs (no hex letters), so the case
		// variant has to hit the subject, which does carry one ("c0000000-...").
		{"fin approver with uppercased subject", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, strings.ToUpper(finApproverSubject), firmTenant, personaRole), http.StatusForbidden},
		{"fin approver with trailing-whitespace tenant", fmt.Sprintf(`{"subject":%q,"tenant_id":"%s ","role":%q}`, finApproverSubject, firmTenant, personaRole), http.StatusForbidden},
		{"unknown tenant", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, firmSubject, unlistedTenant, personaRole), http.StatusForbidden},
		{"mismatched pairing", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, inhouseSubject, firmTenant, personaRole), http.StatusForbidden},
		{"unknown subject", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, unlistedSubject, firmTenant, personaRole), http.StatusForbidden},
		// seeded but never allowlisted: distinguishes "allowlist" from "any seeded membership".
		{"seeded, not allowlisted subject", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, seededNotAllowlisted, firmTenant, personaRole), http.StatusForbidden},
		{"escalated role", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":"admin"}`, firmSubject, firmTenant), http.StatusForbidden},
		{"preparer with an escalated role", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":"admin"}`, preparerSubject, firmTenant), http.StatusForbidden},
		// the seed's own memberships.role — the substitution the persona table warns about.
		{"preparer with the seed's domain role", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":"preparer"}`, preparerSubject, firmTenant), http.StatusForbidden},
		{"preparer on the wrong tenant", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, preparerSubject, inhouseTenant, personaRole), http.StatusForbidden},
		// tenant and role swapped: loginPersona's literals are unkeyed, so an entry
		// written (subject, role, tenant) compiles and would match this body instead.
		{"preparer with tenant and role transposed", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, preparerSubject, personaRole, firmTenant), http.StatusForbidden},
		// role omitted: a defaults-then-match implementation would fill "authenticated" and pass this.
		{"role omitted", fmt.Sprintf(`{"subject":%q,"tenant_id":%q}`, firmSubject, firmTenant), http.StatusForbidden},
		{"empty body", `{}`, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", strings.NewReader(tc.body)))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if tc.wantStatus == http.StatusOK {
				if resp["token_type"] != "bearer" || resp["access_token"] == "" {
					t.Fatalf("mint response = %+v, want bearer access_token", resp)
				}
				return
			}
			if _, minted := resp["access_token"]; minted {
				t.Errorf("refusal minted a token: %+v", resp)
			}
			if resp["error"] != "forbidden" {
				t.Errorf(`refusal body = %+v, want error:"forbidden"`, resp)
			}
		})
	}
}

// TestMockLoginHostedApproverRoundTrip proves the two new firm-approver personas
// mint tokens whose claims survive injectIdentity end to end — a 200 alone does
// not show the token carries the right identity.
func TestMockLoginHostedApproverRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		subject string
	}{
		{"fin approver", finApproverSubject},
		{"compliance approver", complianceApproverSubject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := setupGateway(t)
			mux := http.NewServeMux()
			mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, platform.PostureHosted))
			mux.Handle("/api/", tg.handler)

			login := httptest.NewRecorder()
			body := fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, tc.subject, firmTenant, personaRole)
			mux.ServeHTTP(login, httptest.NewRequest("POST", "/auth/login", strings.NewReader(body)))
			if login.Code != http.StatusOK {
				t.Fatalf("login status = %d, want 200", login.Code)
			}
			var resp struct {
				AccessToken string `json:"access_token"`
				TokenType   string `json:"token_type"`
			}
			if err := json.NewDecoder(login.Body).Decode(&resp); err != nil {
				t.Fatalf("decode login response: %v", err)
			}
			if resp.TokenType != "bearer" || resp.AccessToken == "" {
				t.Fatalf("login response = %+v, want a bearer access_token", resp)
			}

			api := httptest.NewRecorder()
			mux.ServeHTTP(api, request("GET", "/api/tenancy/v1/ping", resp.AccessToken))
			if api.Code != http.StatusOK {
				t.Fatalf("proxied request with minted token = %d, want 200", api.Code)
			}
			assertHeader(t, tg.caps["tenancy"].header, headerTenantID, firmTenant)
			assertHeader(t, tg.caps["tenancy"].header, headerUserID, tc.subject)
			assertHeader(t, tg.caps["tenancy"].header, headerUserRole, personaRole)
		})
	}
}

// TestLoginPersonas_AllSeeded checks the table itself, not the wire: every entry
// must be a real seeded membership and carry the GoTrue role. loginPersona's
// literals are unkeyed, so an entry written (subject, role, tenant) compiles —
// it fails both halves here.
func TestLoginPersonas_AllSeeded(t *testing.T) {
	if len(loginPersonas) != 5 {
		t.Errorf("len(loginPersonas) = %d, want 5 (firm admin, in-house admin, firm preparer, fin approver, compliance approver)", len(loginPersonas))
	}

	rows := seedMembershipRows(t)
	for _, p := range loginPersonas {
		if p.role != personaRole {
			t.Errorf("persona %s role = %q, want %q", p.subject, p.role, personaRole)
		}
		seeded := false
		for _, row := range rows {
			if strings.Contains(row, "'"+p.subject+"'") && strings.Contains(row, "'"+p.tenantID+"'") {
				seeded = true
				break
			}
		}
		if !seeded {
			t.Errorf("persona (%s, %s) has no memberships row in db/seed.dev.sql", p.subject, p.tenantID)
		}
	}
}

// seedMembershipRows returns the lines of seed.dev.sql's memberships INSERT, read
// from the embedded copy the binary ships, never the on-disk file.
func seedMembershipRows(t *testing.T) []string {
	t.Helper()
	b, err := fs.ReadFile(dbsql.FS, "seed.dev.sql")
	if err != nil {
		t.Fatalf("read embedded seed.dev.sql: %v", err)
	}
	var rows []string
	inBlock := false
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "INSERT INTO memberships"):
			inBlock = true
		case inBlock && strings.HasPrefix(line, "ON CONFLICT"):
			inBlock = false
		case inBlock:
			rows = append(rows, line)
		}
	}
	if len(rows) == 0 {
		t.Fatalf("no memberships rows in embedded seed.dev.sql — the block markers moved")
	}
	return rows
}

// seedRowFor returns the memberships row seeding (subject, tenant).
func seedRowFor(t *testing.T, rows []string, subject, tenant string) string {
	t.Helper()
	for _, row := range rows {
		if strings.Contains(row, "'"+subject+"'") && strings.Contains(row, "'"+tenant+"'") {
			return row
		}
	}
	t.Fatalf("no memberships row for (%s, %s)", subject, tenant)
	return ""
}

// tenant, user, role, then display_name/email skipped, then status. The tail is
// anchored so a column reorder fails to match instead of capturing the wrong field.
var seedMembershipRowRe = regexp.MustCompile(
	`'([0-9a-f-]{36})',\s*'([0-9a-f-]{36})',\s*'([a-z_]+)',.*'([a-z]+)'\),?$`)

type seedMembership struct{ tenantID, userID, role, status string }

// seedMemberships parses the memberships INSERT into fields. seedMembershipRows
// hands back the block's comment lines too, so non-matching lines are skipped.
func seedMemberships(t *testing.T) []seedMembership {
	t.Helper()
	var out []seedMembership
	for _, row := range seedMembershipRows(t) {
		m := seedMembershipRowRe.FindStringSubmatch(row)
		if m == nil {
			continue
		}
		out = append(out, seedMembership{tenantID: m[1], userID: m[2], role: m[3], status: m[4]})
	}
	if len(out) == 0 {
		t.Fatal("extracted 0 memberships rows from db/seed.dev.sql — the extractor stopped matching, which reads exactly like an empty seed")
	}
	return out
}

// roleMemberSeedRowRe matches one role_member_seed VALUES tuple: tenant, role key, user.
var roleMemberSeedRowRe = regexp.MustCompile(`'([0-9a-f-]{36})'::uuid,\s+'([a-z_]+)',\s+'([0-9a-f-]{36})'::uuid`)

// seedRoleMemberRows returns the VALUES rows of the role_member_seed CTE
// (db/seed.dev.sql). seedMembershipRows cannot see this staffing: it is a
// separate INSERT (workflow_role_members), not the memberships table.
func seedRoleMemberRows(t *testing.T) []string {
	t.Helper()
	b, err := fs.ReadFile(dbsql.FS, "seed.dev.sql")
	if err != nil {
		t.Fatalf("read embedded seed.dev.sql: %v", err)
	}
	var rows []string
	inBlock := false
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "WITH role_member_seed"):
			inBlock = true
		case inBlock && strings.HasPrefix(line, "INSERT INTO workflow_role_members"):
			inBlock = false
		case inBlock && roleMemberSeedRowRe.MatchString(line):
			rows = append(rows, line)
		}
	}
	if len(rows) == 0 {
		t.Fatalf("no role_member_seed rows in embedded seed.dev.sql — the block markers moved")
	}
	return rows
}

// TestApproverPersonasHoldTheirWorkflowRoles pins the workflow-role staffing that
// justifies admitting the two new personas. If a seed edit moves a role off one of
// them, the login tests above stay green while the justification evaporates.
func TestApproverPersonasHoldTheirWorkflowRoles(t *testing.T) {
	rows := seedRoleMemberRows(t)
	rolesFor := func(subject string) []string {
		seen := map[string]bool{}
		for _, row := range rows {
			m := roleMemberSeedRowRe.FindStringSubmatch(row)
			if m != nil && m[3] == subject {
				seen[m[2]] = true
			}
		}
		keys := make([]string, 0, len(seen))
		for k := range seen {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return keys
	}

	cases := []struct {
		subject string
		want    []string
	}{
		{finApproverSubject, []string{"fin_dir", "fin_mgr"}},
		{complianceApproverSubject, []string{"compliance"}},
	}
	for _, tc := range cases {
		if got := rolesFor(tc.subject); !slices.Equal(got, tc.want) {
			t.Errorf("workflow roles for %s = %v, want %v", tc.subject, got, tc.want)
		}
	}
}

// A suspended member resolves to no role at all (callerRoleTx filters
// status = 'active', internal/invoice/store.go), so allowlisting one mints a token
// whose every role-gated call then refuses. The seeded pairing alone cannot see it:
// TestLoginPersonas_AllSeeded passes just as happily on a suspended row.
func TestLoginPersonas_SeededActive(t *testing.T) {
	rows := seedMemberships(t)
	statusOf := map[string]string{}
	var nonActive []string
	for _, r := range rows {
		key := r.tenantID + "/" + r.userID
		statusOf[key] = r.status
		if r.status != "active" {
			nonActive = append(nonActive, key)
		}
	}

	for _, p := range loginPersonas {
		key := p.tenantID + "/" + p.subject
		status, ok := statusOf[key]
		if !ok {
			t.Errorf("persona %s has no memberships row in db/seed.dev.sql", key)
			continue
		}
		if status != "active" {
			t.Errorf("persona %s is seeded %q, want \"active\"", key, status)
		}
	}

	// Naming the excluded pair keeps the exclusion deliberate: reactivating one in
	// the seed goes red here instead of silently widening the allowlist elsewhere.
	wantNonActive := []string{
		firmTenant + "/c0000000-0000-0000-0000-000000000007",
		inhouseTenant + "/c0000000-0000-0000-0000-000000000012",
	}
	slices.Sort(nonActive)
	slices.Sort(wantNonActive)
	if !slices.Equal(nonActive, wantNonActive) {
		t.Errorf("seed's non-active memberships = %v, want %v", nonActive, wantNonActive)
	}
}

// TestSeedMembershipParserExtractsThirteenRows is the population floor under the
// parity test: a regex that quietly stops matching rows would otherwise let a
// shrinking seed read as agreement.
func TestSeedMembershipParserExtractsThirteenRows(t *testing.T) {
	rows := seedMemberships(t)
	if len(rows) != 13 {
		t.Fatalf("parsed %d memberships rows from db/seed.dev.sql, want 13", len(rows))
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.status]++
	}
	if counts["active"] != 11 || counts["suspended"] != 2 {
		t.Errorf("parsed statuses = %v, want 11 active and 2 suspended", counts)
	}
}

// TestLoginPersonasMatchEverySeededActiveMembership: loginPersonas is a literal, so
// nothing keeps it in step with db/seed.dev.sql except this test. It compares as
// SETS, failing in both directions.
func TestLoginPersonasMatchEverySeededActiveMembership(t *testing.T) {
	var seeded []string
	for _, r := range seedMemberships(t) {
		if r.status == "active" {
			seeded = append(seeded, r.tenantID+"/"+r.userID)
		}
	}
	var allowed []string
	for _, p := range loginPersonas {
		allowed = append(allowed, p.tenantID+"/"+p.subject)
	}
	slices.Sort(seeded)
	slices.Sort(allowed)
	if !slices.Equal(seeded, allowed) {
		t.Fatalf("loginPersonas and db/seed.dev.sql's active memberships disagree — a member the seed creates and the allowlist omits is an unexplained 403 during a demo, and an allowlist entry the seed never creates is a subject nothing seeds\nseeded but not allowlisted: %v\nallowlisted but not seeded: %v\nseed.dev.sql active: %v\nloginPersonas:       %v",
			missingFrom(seeded, allowed), missingFrom(allowed, seeded), seeded, allowed)
	}
}

// missingFrom returns the members of a that b does not hold.
func missingFrom(a, b []string) []string {
	out := []string{}
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

// The preparer is allowlisted so a refused submit is demonstrable on the hosted
// build, which holds only while the seed still makes them a preparer. A seed edit to
// an approver role retires that demonstration with every login case above still
// green; seed_test.go's pin skips whenever no database is configured.
func TestPreparerPersonaSeededAsPreparer(t *testing.T) {
	row := seedRowFor(t, seedMembershipRows(t), preparerSubject, firmTenant)
	if !strings.Contains(row, "'preparer'") {
		t.Errorf("preparer persona is no longer seeded as a preparer:%s", row)
	}
}

// TestMockLoginHostedRefusalOpaque pins AC-3: every refusal reason produces an
// identical, byte-for-byte body that never names which field failed.
func TestMockLoginHostedRefusalOpaque(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, platform.PostureHosted))

	unknownTenant := httptest.NewRecorder()
	mux.ServeHTTP(unknownTenant, httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, firmSubject, unlistedTenant, personaRole))))

	unknownSubject := httptest.NewRecorder()
	mux.ServeHTTP(unknownSubject, httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, unlistedSubject, firmTenant, personaRole))))

	// A listed subject on the wrong tenant must be as opaque as a wholly unknown
	// subject -- the two refusal reasons cannot be told apart from the body.
	admittedWrongTenant := httptest.NewRecorder()
	mux.ServeHTTP(admittedWrongTenant, httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, finApproverSubject, inhouseTenant, personaRole))))

	if unknownTenant.Code != http.StatusForbidden || unknownSubject.Code != http.StatusForbidden || admittedWrongTenant.Code != http.StatusForbidden {
		t.Fatalf("status = %d/%d/%d, want 403/403/403", unknownTenant.Code, unknownSubject.Code, admittedWrongTenant.Code)
	}
	if unknownTenant.Body.String() != unknownSubject.Body.String() {
		t.Fatalf("refusal bodies differ: %q vs %q", unknownTenant.Body.String(), unknownSubject.Body.String())
	}
	if unknownTenant.Body.String() != admittedWrongTenant.Body.String() {
		t.Fatalf("admitted-subject-wrong-tenant refusal body differs: %q vs %q", admittedWrongTenant.Body.String(), unknownTenant.Body.String())
	}
	for _, leak := range []string{"subject", "tenant_id", `"role"`} {
		if strings.Contains(unknownTenant.Body.String(), leak) {
			t.Errorf("refusal body leaks %q: %s", leak, unknownTenant.Body.String())
		}
	}
}

// TestMockLoginPostureAsymmetry pins [login-allowlist-hosted-only]: the SAME
// non-allowlisted triple is refused only under Hosted. Preview stays permissive
// because two contract-tenancy.spec.ts negative-path tests need the mint to
// succeed so /me can then reject it (subject B vs tenant A; subject A vs a
// fresh crypto.randomUUID() tenant no allowlist could ever contain).
func TestMockLoginPostureAsymmetry(t *testing.T) {
	tg := setupGateway(t)
	body := fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, firmSubject, unlistedTenant, personaRole)

	cases := []struct {
		posture    platform.PostureKind
		wantStatus int
	}{
		{platform.PostureHosted, http.StatusForbidden},
		{platform.PosturePreview, http.StatusOK},
		{platform.PostureLocal, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(string(tc.posture), func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, tc.posture))

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", strings.NewReader(body)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestLoginAllowlistOnlyGatesHostedPosture proves the widened allowlist cannot
// leak into a non-Hosted posture: the check at gateway.go's login handler is
// gated by "posture == PostureHosted &&", so outside Hosted the two new
// personas mint even for a pairing the allowlist would never contain.
func TestLoginAllowlistOnlyGatesHostedPosture(t *testing.T) {
	tg := setupGateway(t)
	// finApproverSubject paired with unlistedTenant: no loginPersonas row can match this.
	body := fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, finApproverSubject, unlistedTenant, personaRole)

	for _, posture := range []platform.PostureKind{platform.PosturePreview, platform.PostureLocal} {
		t.Run(string(posture), func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, posture))

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", strings.NewReader(body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s) -- the allowlist must not be consulted here", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestMockLoginPreviewNoMembershipCase mints for a triple /me later rejects
// (contract-tenancy.spec.ts:104) -- Preview must still mint it.
func TestMockLoginPreviewNoMembershipCase(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, platform.PosturePreview))

	body := fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, inhouseSubject, firmTenant, personaRole)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestMockLoginLocalEmptyBody pins the ignored-decode-error path: an empty body
// yields io.EOF, which stays ignored so GoTrue-shaped defaults still mint.
func TestMockLoginLocalEmptyBody(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, platform.PostureLocal))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TokenType != "bearer" || resp.AccessToken == "" {
		t.Fatalf("login response = %+v, want a bearer access_token", resp)
	}
}

// TestMockLoginPreflightBypassesHostedRefusal pins the exact composition main.go
// uses for /auth/login: a browser preflight (OPTIONS + Origin) must get CORS's 204
// even though the same body would 403 if it reached the handler under Hosted.
func TestMockLoginPreflightBypassesHostedRefusal(t *testing.T) {
	tg := setupGateway(t)
	login := CORS([]string{allowedOrigin})(MockLoginHandler(tg.issuer, platform.PostureHosted))

	r := httptest.NewRequest("OPTIONS", "/auth/login",
		strings.NewReader(fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, unlistedSubject, unlistedTenant, personaRole)))
	r.Header.Set("Origin", allowedOrigin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204 (body %s) -- a real browser preflight must never see the Hosted refusal", rec.Code, rec.Body.String())
	}
}

// TestMockLoginOptionsNoOriginFallsThroughUnderHosted pins the documented residual:
// an OPTIONS with no Origin is not a browser preflight, so CORS lets it fall through
// to MockLoginHandler. Nothing in the tree or any browser sends this, but under
// Hosted it now 403s where the pre-allowlist handler minted unconditionally.
func TestMockLoginOptionsNoOriginFallsThroughUnderHosted(t *testing.T) {
	tg := setupGateway(t)
	login := CORS([]string{allowedOrigin})(MockLoginHandler(tg.issuer, platform.PostureHosted))

	r := httptest.NewRequest("OPTIONS", "/auth/login", strings.NewReader(``))
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s) -- an Origin-less OPTIONS reaches the handler and the empty body is not an allowlisted triple", rec.Code, rec.Body.String())
	}
}

// TestMockLoginHostedMalformedBodies exercises decode edge cases the allowlist must
// survive without ever minting: whichever way the body fails to decode into a clean
// seeded triple, the zero/partial struct must not match, and the response must not
// carry an access_token key at all (not merely an empty one).
func TestMockLoginHostedMalformedBodies(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, platform.PostureHosted))

	firmTriple := fmt.Sprintf(`"subject":%q,"tenant_id":%q,"role":%q`, firmSubject, firmTenant, personaRole)

	cases := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
	}{
		{"top-level JSON array", `["not","an","object"]`, "", http.StatusForbidden},
		{"top-level JSON null", `null`, "", http.StatusForbidden},
		{"unknown extra field alongside a valid triple", fmt.Sprintf(`{%s,"admin_override":true}`, firmTriple), "", http.StatusOK},
		{
			"duplicate subject key, last value wins and is unlisted",
			fmt.Sprintf(`{"subject":%q,"subject":%q,"tenant_id":%q,"role":%q}`, firmSubject, unlistedSubject, firmTenant, personaRole),
			"", http.StatusForbidden,
		},
		{
			"duplicate subject key, last value wins and is the seeded one",
			fmt.Sprintf(`{"subject":%q,"subject":%q,"tenant_id":%q,"role":%q}`, unlistedSubject, firmSubject, firmTenant, personaRole),
			"", http.StatusOK,
		},
		{"non-JSON Content-Type header, unlisted triple, JSON body", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, unlistedSubject, unlistedTenant, personaRole), "text/plain", http.StatusForbidden},
		{"non-JSON Content-Type header, valid persona triple, JSON body", fmt.Sprintf(`{%s}`, firmTriple), "text/plain", http.StatusOK},
		{"oversized body, unlisted triple padded past 1MB", fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q,"pad":%q}`, unlistedSubject, unlistedTenant, personaRole, strings.Repeat("x", 1<<20)), "", http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(tc.body))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, r)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if tc.wantStatus == http.StatusForbidden {
				if _, minted := resp["access_token"]; minted {
					t.Errorf("refusal minted a token: %+v", resp)
				}
			}
		})
	}
}

// TestMockLoginHostedRefusalHasNoAccessTokenKey isolates AC-3's strongest form: the
// key itself must be absent, not merely empty -- a caller checking `"access_token" in
// resp` must see the same false a caller checking truthiness would.
func TestMockLoginHostedRefusalHasNoAccessTokenKey(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer, platform.PostureHosted))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login",
		strings.NewReader(fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, unlistedSubject, unlistedTenant, personaRole))))

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, present := resp["access_token"]; present {
		t.Fatalf("refusal body has an access_token key at all: %+v", resp)
	}
	if len(resp) != 1 || resp["error"] != "forbidden" {
		t.Fatalf("refusal body = %+v, want exactly {\"error\":\"forbidden\"}", resp)
	}
}

func TestMockIssuerEnabled(t *testing.T) {
	cases := []struct {
		env, flag string
		want      bool
	}{
		{"development", "true", true},
		{"development", "", false},
		{"development", "1", false},   // only the exact string "true" enables it
		{"production", "true", false}, // refused in production regardless
		{"production", "", false},

		// --- normalization added for M8-07's demo-side half: trim + lowercase
		// before comparing to "production" (mirrors submission.IsProduction) ---
		{"", "true", true},              // unset env stays permissive, same as "development"
		{"Production", "true", false},   // AC-3: casing bypass closed
		{"PRODUCTION", "true", false},   // casing bypass closed
		{" production", "true", false},  // AC-3: leading-whitespace bypass closed
		{" production ", "true", false}, // leading+trailing, distinct from AC-3's leading-only case
		{"production ", "true", false},  // trailing-whitespace bypass closed
	}
	for _, c := range cases {
		if got := MockIssuerEnabled(c.env, c.flag); got != c.want {
			t.Errorf("MockIssuerEnabled(%q, %q) = %v, want %v", c.env, c.flag, got, c.want)
		}
	}
}

// TestS2STokenNeverReachesUpstream (VB-16, task-109/M4-04-03,
// [s2s-gateway-strip], Stage-1 addendum G4): the gateway proxies
// /api/validation/* to 04 (routedServices in cmd/gateway/main.go includes
// "validation"), and injectIdentity today Sets/Dels X-Tenant-ID/X-User-*/
// X-Request-ID but never touches X-S2S-Token (gateway.go:118-132) -- so a
// client-supplied X-S2S-Token currently rides through to the upstream
// unchanged. A leaked peer token smuggled this way would let a caller
// impersonate a fleet peer at 04's batch route through the one public
// backend surface.
//
// The smuggler must first clear authorize(), which 403s on an empty
// TenantID (gateway.go:90-95) -- so this test uses a TENANT-BEARING
// identity (validToken, testTenant), per Stage-1 addendum G4: a request
// with no tenant never reaches the proxy and would assert nothing.
func TestS2STokenNeverReachesUpstream(t *testing.T) {
	tg := setupGateway(t)
	r := request("POST", "/api/validation/v1/validate/batch", tg.validToken(t))
	r.Header.Set("X-S2S-Token", "sneaky")

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	cap := tg.caps["validation"]
	if cap.hits != 1 {
		t.Fatalf("validation hits = %d, want 1", cap.hits)
	}
	if got := cap.header.Get("X-S2S-Token"); got != "" {
		t.Errorf("upstream saw X-S2S-Token = %q, want empty -- a client-supplied peer token must never "+
			"reach the upstream [s2s-gateway-strip] (injectIdentity, gateway.go:118-132, does not yet Del "+
			"this header)", got)
	}
}

func assertHeader(t *testing.T, h http.Header, key, want string) {
	t.Helper()
	if got := h.Get(key); got != want {
		t.Errorf("upstream header %s = %q, want %q", key, got, want)
	}
}
