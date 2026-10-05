package gateway

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	testIssuer  = "https://mock.ascomply.test"
	testSubject = "11111111-1111-1111-1111-111111111111"
	testTenant  = "tenant-a"
	testRole    = "authenticated"

	testGatewayToken = "gw-test-token"
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
	handler  http.Handler
	issuer   *auth.MockIssuer
	verifier *auth.Verifier
	caps     map[string]*capture
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
		handler:  Handler(Options{Verifier: verifier, Sessions: liveSessions(t), Upstreams: upstreams, GatewayToken: testGatewayToken}),
		issuer:   issuer,
		verifier: verifier,
		caps:     caps,
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

	h := Handler(Options{Verifier: verifier, Sessions: liveSessions(t), Upstreams: map[string]*url.URL{"tenancy": deadURL}, GatewayToken: testGatewayToken})
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

// A forgotten Sessions wire fails at construction, not on the first token with a session_id.
func TestHandlerRequiresSessions(t *testing.T) {
	verifier, err := auth.NewVerifier(auth.Config{Issuer: testIssuer, JWKSURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	build := func(sessions *SessionChecker) (panicked bool) {
		defer func() { panicked = recover() != nil }()
		Handler(Options{Verifier: verifier, Sessions: sessions, Upstreams: map[string]*url.URL{}, GatewayToken: "t"})
		return false
	}
	if build(liveSessions(t)) {
		t.Fatal("Handler panicked with Sessions set")
	}
	if !build(nil) {
		t.Error("Handler built without Sessions, want a panic")
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
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer))
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

// Seeded subjects and tenant from db/seed.dev.sql; personaRole is the GoTrue JWT role.
const (
	firmTenant                = "11111111-1111-1111-1111-111111111111"
	preparerSubject           = "c0000000-0000-0000-0000-000000000003" // firm-tenant preparer
	finApproverSubject        = "c0000000-0000-0000-0000-000000000004" // firm-tenant reviewer staffed fin_mgr + fin_dir
	complianceApproverSubject = "c0000000-0000-0000-0000-000000000005" // firm-tenant reviewer staffed compliance
	personaRole               = "authenticated"
)

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

// TestApproverPersonasHoldTheirWorkflowRoles pins the seeded fin_mgr/fin_dir and
// compliance staffing the demo's firm approval run needs.
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

// A refused submit is demonstrable only while the seed makes this member a preparer;
// seed_test.go's pin skips whenever no database is configured.
func TestPreparerPersonaSeededAsPreparer(t *testing.T) {
	row := seedRowFor(t, seedMembershipRows(t), preparerSubject, firmTenant)
	if !strings.Contains(row, "'preparer'") {
		t.Errorf("preparer persona is no longer seeded as a preparer:%s", row)
	}
}

// TestMockLoginLocalEmptyBody pins the ignored-decode-error path: an empty body
// yields io.EOF, which stays ignored so GoTrue-shaped defaults still mint.
func TestMockLoginLocalEmptyBody(t *testing.T) {
	tg := setupGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", MockLoginHandler(tg.issuer))

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

// The mock login is a mint for any identity: no seed holds this subject or tenant.
func TestMockLoginMintsAnyIdentity(t *testing.T) {
	tg := setupGateway(t)
	h := MockLoginHandler(tg.issuer)
	subject, tenant := uuid.NewString(), uuid.NewString()

	rec := httptest.NewRecorder()
	body := fmt.Sprintf(`{"subject":%q,"tenant_id":%q,"role":%q}`, subject, tenant, personaRole)
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp.AccessToken == "" {
		t.Fatalf("login body carries no access_token (decode err %v)", err)
	}
	id, err := tg.verifier.Verify(t.Context(), resp.AccessToken)
	if err != nil {
		t.Fatalf("minted token does not verify: %v", err)
	}
	if id.Subject != subject || id.TenantID != tenant {
		t.Errorf("token identity = (sub %q, tenant %q), want (%q, %q)", id.Subject, id.TenantID, subject, tenant)
	}
}

// A body that does not decode into the request shape mints GoTrue-shaped defaults, as an empty body does.
func TestMockLoginMalformedBodyMintsDefaults(t *testing.T) {
	tg := setupGateway(t)
	h := MockLoginHandler(tg.issuer)

	for _, body := range []string{`{`, `[]`, `{"subject":1}`} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Errorf("body %q: status = %d, want 200 (body %s)", body, rec.Code, rec.Body.String())
			continue
		}
		var resp struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp.TokenType != "bearer" || resp.AccessToken == "" {
			t.Errorf("body %q: response = %+v (decode err %v), want a bearer access_token", body, resp, err)
			continue
		}
		id, err := tg.verifier.Verify(t.Context(), resp.AccessToken)
		if err != nil {
			t.Errorf("body %q: minted token does not verify: %v", body, err)
			continue
		}
		if _, err := uuid.Parse(id.Subject); err != nil {
			t.Errorf("body %q: default subject %q is not a UUID: %v", body, id.Subject, err)
		}
		if id.Role != personaRole {
			t.Errorf("body %q: default role = %q, want %q", body, id.Role, personaRole)
		}
	}
}

// The mint reflects whatever the body decodes into and validates nothing: role and tenant are
// free strings, and a body the decoder half-reads keeps the fields it reached.
func TestMockLoginMintsWhatTheBodyDecodesInto(t *testing.T) {
	tg := setupGateway(t)
	h := MockLoginHandler(tg.issuer)
	const s1, s2 = "c0000000-0000-0000-0000-0000000000a1", "c0000000-0000-0000-0000-0000000000a2"
	pad := strings.Repeat("x", 1<<20)

	cases := []struct {
		name, body, contentType string
		wantSubject             string // empty: any UUID
		wantTenant, wantRole    string
	}{
		{"a role other than authenticated", `{"subject":"` + s1 + `","role":"service_role"}`, "", s1, "", "service_role"},
		{"a tenant that is not a UUID", `{"tenant_id":"not a uuid/../"}`, "", "", "not a uuid/../", personaRole},
		{"an empty role defaults", `{"subject":"` + s1 + `","role":""}`, "", s1, "", personaRole},
		{"an unknown extra field is ignored", `{"subject":"` + s1 + `","tenant_id":"t-1","admin_override":true}`, "", s1, "t-1", personaRole},
		{"a duplicate key keeps the last value", `{"subject":"` + s1 + `","subject":"` + s2 + `","tenant_id":"t-1"}`, "", s2, "t-1", personaRole},
		{"a wrongly typed field keeps the fields decoded after it", `{"subject":1,"tenant_id":"t-1"}`, "", "", "t-1", personaRole},
		{"a truncated body keeps nothing", `{"subject":"` + s1 + `","tenant_id":"t-1"`, "", "", "", personaRole},
		{"null mints defaults", `null`, "", "", "", personaRole},
		{"a non-JSON Content-Type is ignored", `{"subject":"` + s1 + `","tenant_id":"t-1"}`, "text/plain", s1, "t-1", personaRole},
		{"a body padded past 1 MiB still mints", `{"subject":"` + s1 + `","tenant_id":"t-1","pad":"` + pad + `"}`, "", s1, "t-1", personaRole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(tc.body))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			var resp struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp.AccessToken == "" {
				t.Fatalf("login body carries no access_token (decode err %v)", err)
			}
			id, err := tg.verifier.Verify(t.Context(), resp.AccessToken)
			if err != nil {
				t.Fatalf("minted token does not verify: %v", err)
			}
			if tc.wantSubject == "" {
				if _, err := uuid.Parse(id.Subject); err != nil {
					t.Errorf("subject %q, want a default UUID", id.Subject)
				}
			} else if id.Subject != tc.wantSubject {
				t.Errorf("subject = %q, want %q", id.Subject, tc.wantSubject)
			}
			if id.TenantID != tc.wantTenant || id.Role != tc.wantRole {
				t.Errorf("(tenant, role) = (%q, %q), want (%q, %q)", id.TenantID, id.Role, tc.wantTenant, tc.wantRole)
			}
		})
	}
}

// The mint takes a subject that is not a UUID, but the verifier refuses the token downstream.
func TestMockLoginNonUUIDSubjectMintsATokenTheVerifierRefuses(t *testing.T) {
	tg := setupGateway(t)
	rec := httptest.NewRecorder()
	MockLoginHandler(tg.issuer).ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"subject":"../etc"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp.AccessToken == "" {
		t.Fatalf("login body carries no access_token (decode err %v)", err)
	}
	if _, err := tg.verifier.Verify(t.Context(), resp.AccessToken); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("Verify = %v, want auth.ErrUnauthorized", err)
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

		// --- normalization added for the demo-side half: trim + lowercase
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
// "validation"), and injectIdentity Dels X-S2S-Token (with the identity headers it
// Sets/Dels), so a client-supplied X-S2S-Token never rides through to the upstream.
// A leaked peer token smuggled this way would let a caller impersonate a fleet peer
// at 04's batch route through the one public backend surface.
//
// The smuggler must first clear authorize(), which 403s on an empty
// TenantID -- so this test uses a TENANT-BEARING
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
			"reach the upstream [s2s-gateway-strip] (injectIdentity must Del this header)", got)
	}
}

func TestGatewayTokenReachesEveryUpstream(t *testing.T) {
	tg := setupGateway(t)
	tok := tg.validToken(t)
	if len(tg.caps) != 7 {
		t.Fatalf("gateway routes %d services, want 7 -- the loop below would assert nothing", len(tg.caps))
	}
	for svc, cap := range tg.caps {
		t.Run(svc, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("GET", "/api/"+svc+"/v1/ping", tok))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if cap.hits != 1 {
				t.Fatalf("%s hits = %d, want 1", svc, cap.hits)
			}
			if got := cap.header.Values(platform.HeaderGatewayToken); !slices.Equal(got, []string{testGatewayToken}) {
				t.Errorf("%s upstream %s = %q, want [%q]", svc, platform.HeaderGatewayToken, got, testGatewayToken)
			}
		})
	}
}

func TestClientSuppliedGatewayTokenIsOverwritten(t *testing.T) {
	tg := setupGateway(t)
	r := request("GET", "/api/tenancy/v1/ping", tg.validToken(t))
	r.Header.Add(platform.HeaderGatewayToken, "forged")
	r.Header.Add(platform.HeaderGatewayToken, "forged-2")

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cap := tg.caps["tenancy"]
	if cap.hits != 1 {
		t.Fatalf("tenancy hits = %d, want 1", cap.hits)
	}
	if got := cap.header.Values(platform.HeaderGatewayToken); !slices.Equal(got, []string{testGatewayToken}) {
		t.Errorf("upstream %s = %q, want only [%q]", platform.HeaderGatewayToken, got, testGatewayToken)
	}
}

// The peer credential is dropped while the gateway credential is set, in the same request.
func TestGatewayTokenIsNotTheS2SHeader(t *testing.T) {
	tg := setupGateway(t)
	r := request("POST", "/api/validation/v1/validate/batch", tg.validToken(t))
	r.Header.Set("X-S2S-Token", "sneaky")

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cap := tg.caps["validation"]
	if cap.hits != 1 {
		t.Fatalf("validation hits = %d, want 1", cap.hits)
	}
	assertHeader(t, cap.header, platform.HeaderGatewayToken, testGatewayToken)
	assertHeader(t, cap.header, "X-S2S-Token", "")
}

func TestHandlerPanicsWithoutGatewayToken(t *testing.T) {
	verifier, err := auth.NewVerifier(auth.Config{Issuer: testIssuer, JWKSURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	sessions := liveSessions(t)
	build := func(token string) (panicked bool) {
		defer func() { panicked = recover() != nil }()
		Handler(Options{Verifier: verifier, Sessions: sessions, Upstreams: map[string]*url.URL{}, GatewayToken: token})
		return false
	}
	if build("t") {
		t.Fatal("Handler panicked with GatewayToken set")
	}
	if !build("") {
		t.Error("Handler built with an empty GatewayToken, want a panic")
	}
}

// The gateway composed with a platform.App that called RequireGateway: the shared token gets
// through with the token's identity; a different one is refused by the guarded service itself
// and answered to the client as 502, never as the user's 401.
func TestGatewayGetsThroughAGuardedService(t *testing.T) {
	tg := setupGateway(t)
	tok := tg.validToken(t)

	guarded := func(t *testing.T, gatewayToken string) (rec *httptest.ResponseRecorder, reached, handled *atomic.Int32) {
		t.Helper()
		t.Setenv("SENTRY_DSN", "")
		prev := slog.Default()
		t.Cleanup(func() { slog.SetDefault(prev) })
		app, err := platform.New("tenancy")
		if err != nil {
			t.Fatalf("platform.New: %v", err)
		}
		handled = new(atomic.Int32)
		app.Mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
			handled.Add(1)
			id, _ := auth.IdentityFromContext(r.Context())
			_, _ = w.Write([]byte(id.Subject))
		})
		app.RequireGateway(testGatewayToken)

		reached = new(atomic.Int32)
		inner := app.Handler()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached.Add(1)
			inner.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatalf("parse upstream url: %v", err)
		}

		h := Handler(Options{Verifier: tg.verifier, Sessions: liveSessions(t), Upstreams: map[string]*url.URL{"tenancy": u}, GatewayToken: gatewayToken})
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tok))
		return rec, reached, handled
	}

	t.Run("same token", func(t *testing.T) {
		rec, reached, handled := guarded(t, testGatewayToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		if reached.Load() != 1 || handled.Load() != 1 {
			t.Errorf("service saw %d request(s), ran its handler %d time(s), want 1 and 1", reached.Load(), handled.Load())
		}
		if got := rec.Body.String(); got != testSubject {
			t.Errorf("subject at the service = %q, want %q", got, testSubject)
		}
	})

	t.Run("different token", func(t *testing.T) {
		rec, reached, handled := guarded(t, "other")
		if reached.Load() != 1 {
			t.Fatalf("service saw %d request(s), want 1 -- the refusal must come from the service, not the gateway", reached.Load())
		}
		if rec.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", rec.Code)
		}
		if got := rec.Header().Get(platform.HeaderGatewayGuard); got != "" {
			t.Errorf("%s = %q reached the client, want it stripped", platform.HeaderGatewayGuard, got)
		}
		if handled.Load() != 0 {
			t.Errorf("service handler ran %d time(s) on a mismatched token, want 0", handled.Load())
		}
	})
}

const (
	provisioningPath = "/api/tenancy/v1/workspaces"
	testEmail        = "ada@example.test"
	// The wire name identityMiddleware reads; a literal so renaming headerUserEmail fails a test.
	wireUserEmail = "X-User-Email"
)

func TestTenantlessTokenAllowedOnlyOnProvisioning(t *testing.T) {
	tg := setupGateway(t)
	tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, request("POST", provisioningPath, tok))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	cap := tg.caps["tenancy"]
	if cap.hits != 1 {
		t.Fatalf("tenancy hits = %d, want 1", cap.hits)
	}
	if cap.path != "/v1/workspaces" {
		t.Errorf("upstream path = %q, want %q", cap.path, "/v1/workspaces")
	}
	assertHeader(t, cap.header, headerTenantID, "")
	assertHeader(t, cap.header, headerUserID, testSubject)
	assertHeader(t, cap.header, wireUserEmail, testEmail)
}

func TestTenantlessTokenForbiddenElsewhere(t *testing.T) {
	cases := []struct{ method, path, service string }{
		{"GET", provisioningPath, "tenancy"},
		{"POST", provisioningPath + "/", "tenancy"},
		{"POST", "/api/tenancy/v1/me", "tenancy"},
		{"POST", "/api/portfolio/v1/workspaces", "portfolio"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			tg := setupGateway(t)
			tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Email: testEmail})

			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request(tc.method, tc.path, tok))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("tenant-less: status = %d, want 403", rec.Code)
			}
			for svc, c := range tg.caps {
				if c.hits != 0 {
					t.Fatalf("tenant-less: %s upstream hit %d time(s), want 0", svc, c.hits)
				}
			}

			// Same route with a tenant is proxied, so the 403 above is authorization, not routing.
			rec = httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request(tc.method, tc.path, tg.validToken(t)))
			if rec.Code != http.StatusOK || tg.caps[tc.service].hits != 1 {
				t.Fatalf("with tenant: status = %d, %s hits = %d, want 200 and 1", rec.Code, tc.service, tg.caps[tc.service].hits)
			}
		})
	}
}

func TestUserEmailHeaderInjectedAndClientCopyStripped(t *testing.T) {
	tg := setupGateway(t)
	tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant, Email: testEmail})
	r := request("GET", "/api/tenancy/v1/ping", tok)
	r.Header.Set(wireUserEmail, "evil@x")

	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cap := tg.caps["tenancy"]
	if cap.hits != 1 {
		t.Fatalf("tenancy hits = %d, want 1", cap.hits)
	}
	assertHeader(t, cap.header, wireUserEmail, testEmail)
}

func assertHeader(t *testing.T, h http.Header, key, want string) {
	t.Helper()
	if got := h.Get(key); got != want {
		t.Errorf("upstream header %s = %q, want %q", key, got, want)
	}
}

// A service's own 401 (no guard marker) is the user's auth refusal and passes through unchanged.
func TestProxyPassesAServiceOwn401Through(t *testing.T) {
	tg := setupGateway(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	h := Handler(Options{Verifier: tg.verifier, Sessions: liveSessions(t), Upstreams: map[string]*url.URL{"tenancy": u}, GatewayToken: testGatewayToken})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tg.validToken(t)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// upstreamCount counts what one upstream received: every request, hits on an /internal route, hits on the self-read.
type upstreamCount struct {
	any, internal, selfRead atomic.Int32
}

func (c *upstreamCount) reset() { c.any.Store(0); c.internal.Store(0); c.selfRead.Store(0) }

// countingUpstream serves an /internal route and the self-read from a case-sensitive Go mux, as a context service does.
func countingUpstream(t *testing.T) (*url.URL, *upstreamCount) {
	t.Helper()
	c := &upstreamCount{}
	mux := http.NewServeMux()
	internal := func(w http.ResponseWriter, _ *http.Request) { c.internal.Add(1); w.WriteHeader(http.StatusAccepted) }
	mux.HandleFunc("/internal", internal)
	mux.HandleFunc("/internal/", internal)
	mux.HandleFunc("GET /v1/contacts/me", func(w http.ResponseWriter, _ *http.Request) {
		c.selfRead.Add(1)
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.any.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u, c
}

// internalRig is the gateway mounted on a ServeMux at routePrefix behind a real listener, as cmd/gateway/main.go mounts it.
type internalRig struct {
	tg       *testGateway
	srv      *httptest.Server
	counts   map[string]*upstreamCount
	services []string
}

func newInternalRig(t *testing.T) *internalRig {
	t.Helper()
	tg := setupGateway(t)
	services := []string{"tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications"}
	ups := map[string]*url.URL{}
	counts := map[string]*upstreamCount{}
	for _, svc := range services {
		ups[svc], counts[svc] = countingUpstream(t)
	}
	api := Handler(Options{Verifier: tg.verifier, Sessions: liveSessions(t), Upstreams: ups, GatewayToken: testGatewayToken})
	mux := http.NewServeMux()
	mux.Handle(routePrefix, CORS(nil)(api))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &internalRig{tg: tg, srv: srv, counts: counts, services: services}
}

// do sends one request and follows every redirect; it returns the final status and how many redirects it followed.
func (r *internalRig) do(t *testing.T, method, rawPath, bearer string) (status, redirects int) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		redirects++
		if len(via) >= 10 {
			return fmt.Errorf("stopped after %d redirects", len(via))
		}
		return nil
	}}
	req, err := http.NewRequest(method, r.srv.URL+rawPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawPath, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, redirects
}

// {svc} is replaced by each service name.
func (r *internalRig) path(tmpl, svc string) string { return strings.ReplaceAll(tmpl, "{svc}", svc) }

func (r *internalRig) tenantless(t *testing.T) string {
	return r.tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole})
}

func TestRouter_InternalPathNeverReachesUpstream(t *testing.T) {
	rig := newInternalRig(t)

	// Control: the upstream does serve /internal, and its mux is case-sensitive, so /Internal is no internal route.
	direct, c := countingUpstream(t)
	for _, tc := range []struct {
		path string
		want int32
	}{{"/internal/contacts/registrants", 1}, {"/Internal/contacts/registrants", 1}} {
		path, want := tc.path, tc.want
		resp, err := http.Post(direct.String()+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := c.internal.Load(); got != want {
			t.Fatalf("control: after POST %s the upstream's /internal route has %d hits, want %d", path, got, want)
		}
	}

	// The first segment after the service is exactly internal once the mux cleans the path (301) or the router decodes it.
	blocked := []struct {
		name, tmpl string
		redirects  bool
	}{
		{"bare", "/api/{svc}/internal", false},
		{"trailing slash", "/api/{svc}/internal/", false},
		{"registrants", "/api/{svc}/internal/contacts/registrants", false},
		{"demo-requests", "/api/{svc}/internal/contacts/demo-requests", false},
		{"dot segment", "/api/{svc}/./internal/contacts/registrants", true},
		{"double slash", "/api/{svc}//internal/contacts/registrants", true},
		{"dot-dot segment", "/api/{svc}/v1/../internal/contacts/registrants", true},
		{"double slash before the service", "/api//{svc}/internal/contacts/registrants", true},
		{"dot segment before the service", "/api/./{svc}/internal/contacts/registrants", true},
		{"dot-dot into the service", "/api/x/../{svc}/internal/contacts/registrants", true},
		{"percent-encoded letter", "/api/{svc}/%69nternal/contacts/registrants", false},
	}
	// Go's mux does not clean an encoded dot segment and matches case-sensitively, and the upstream mux does the same,
	// so these may be proxied or refused but must reach no /internal route.
	noInternalRoute := []struct{ name, tmpl string }{
		{"encoded dot segment", "/api/{svc}/%2e/internal/contacts/registrants"},
		{"encoded dot-dot segment", "/api/{svc}/v1/%2e%2e/internal/contacts/registrants"},
		{"percent-encoded slash after internal", "/api/{svc}/internal%2Fcontacts%2Fregistrants"},
		{"percent-encoded slash after the service", "/api/{svc}%2Finternal/contacts/registrants"},
		{"Internal", "/api/{svc}/Internal/contacts/registrants"},
		{"INTERNAL", "/api/{svc}/INTERNAL/contacts/registrants"},
		{"encoded I", "/api/{svc}/%49nternal/contacts/registrants"},
	}
	tokens := map[string]string{"tenant-scoped": rig.tg.validToken(t), "tenantless": rig.tenantless(t)}

	for _, svc := range rig.services {
		for tokName, token := range tokens {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				for _, b := range blocked {
					t.Run(svc+"/"+tokName+"/"+method+"/"+b.name, func(t *testing.T) {
						rig.counts[svc].reset()
						path := rig.path(b.tmpl, svc)
						status, redirects := rig.do(t, method, path, token)
						if status != http.StatusNotFound {
							t.Errorf("%s %s answered %d, want 404", method, path, status)
						}
						if (redirects > 0) != b.redirects {
							t.Errorf("%s %s followed %d redirects, want some=%v: the case no longer meets the mux's own cleaning", method, path, redirects, b.redirects)
						}
						if n := rig.counts[svc].any.Load(); n != 0 {
							t.Errorf("%s %s reached the %s upstream %d times, want 0", method, path, svc, n)
						}
					})
				}
				for _, v := range noInternalRoute {
					t.Run(svc+"/"+tokName+"/"+method+"/"+v.name, func(t *testing.T) {
						rig.counts[svc].reset()
						path := rig.path(v.tmpl, svc)
						status, _ := rig.do(t, method, path, token)
						// A tenantless token meets authorize's 403 on a path the router does not refuse.
						if status != http.StatusNotFound && (tokName != "tenantless" || status != http.StatusForbidden) {
							t.Errorf("%s %s answered %d, want 404", method, path, status)
						}
						if n := rig.counts[svc].internal.Load(); n != 0 {
							t.Errorf("%s %s reached the %s upstream's /internal route %d times, want 0", method, path, svc, n)
						}
					})
				}
			}
		}
	}

	// A tenantless token gets 404, not authorize's 403, and no token still gets the verifier's 401.
	t.Run("no token is 401", func(t *testing.T) {
		rig.counts["notifications"].reset()
		status, _ := rig.do(t, http.MethodGet, "/api/notifications/internal/contacts/registrants", "")
		if status != http.StatusUnauthorized {
			t.Errorf("answered %d, want 401", status)
		}
		if n := rig.counts["notifications"].any.Load(); n != 0 {
			t.Errorf("upstream hit %d times, want 0", n)
		}
	})
}

func TestRouter_SelfReadIsProxied(t *testing.T) {
	rig := newInternalRig(t)
	token := rig.tg.validToken(t)

	t.Run("self-read", func(t *testing.T) {
		c := rig.counts["notifications"]
		status, redirects := rig.do(t, http.MethodGet, "/api/notifications/v1/contacts/me", token)
		if status != http.StatusOK || redirects != 0 {
			t.Fatalf("answered %d after %d redirects, want 200 with none", status, redirects)
		}
		if c.any.Load() != 1 || c.selfRead.Load() != 1 {
			t.Errorf("upstream saw %d requests and %d self-reads, want 1 and 1", c.any.Load(), c.selfRead.Load())
		}
	})

	// The refusal is for the first segment after the service, exactly: near misses still proxy.
	for _, path := range []string{
		"/api/notifications/v1/internal/contacts/registrants",
		"/api/notifications/internals/contacts/registrants",
		"/api/notifications/internal-status",
		"/api/notifications/x/internal",
	} {
		t.Run(path, func(t *testing.T) {
			c := rig.counts["notifications"]
			c.reset()
			rig.do(t, http.MethodGet, path, token)
			if c.any.Load() != 1 {
				t.Errorf("GET %s reached the upstream %d times, want 1 (it is not an internal first segment)", path, c.any.Load())
			}
			if c.internal.Load() != 0 {
				t.Errorf("GET %s reached the upstream's /internal route", path)
			}
		})
	}
}

// rawDo writes request line target verbatim over a socket, which a Go client would clean or refuse,
// and follows redirects by hand; it returns the final status and the number of redirects followed.
func (r *internalRig) rawDo(t *testing.T, method, target, bearer string) (status, redirects int) {
	t.Helper()
	for range 6 {
		conn, err := net.Dial("tcp", r.srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: gw.test\r\nAuthorization: Bearer %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", method, target, bearer)
		resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
		if err != nil {
			_ = conn.Close()
			t.Logf("%s %q: %v", method, target, err)
			return 0, redirects
		}
		_ = resp.Body.Close()
		_ = conn.Close()
		if resp.StatusCode/100 != 3 {
			return resp.StatusCode, redirects
		}
		loc, err := resp.Location()
		if err != nil {
			t.Fatalf("%s %q: %v", method, target, err)
		}
		target = loc.RequestURI()
		redirects++
	}
	t.Fatalf("%s %q: redirect loop", method, target)
	return 0, 0
}

// Request lines a Go client cleans or refuses, written to the socket as is. Whatever the method,
// no variant may reach the upstream's /internal route; refused ones must not reach the upstream at all.
func TestRouter_InternalPathRawRequestLinesNeverReachUpstream(t *testing.T) {
	rig := newInternalRig(t)
	token := rig.tg.validToken(t)
	c := rig.counts["notifications"]
	const reg = "/contacts/registrants"

	// Control: the raw helper reaches the upstream when it should, and the refusal is a 404 with no upstream hit.
	c.reset()
	if status, _ := rig.rawDo(t, http.MethodGet, "/api/notifications/v1/contacts/me", token); status != http.StatusOK || c.selfRead.Load() != 1 {
		t.Fatalf("control: raw self-read answered %d with %d upstream self-reads, want 200 and 1", status, c.selfRead.Load())
	}

	refused := []string{
		"/api/notifications/%69%6e%74%65%72%6e%61%6c" + reg,
		"/api/notifications/%69nternal%2Fcontacts/registrants",
		"/api/notifications/internal%2F..%2Finternal" + reg,
		"http://gw.test/api/notifications/internal" + reg,
		"/api/notifications/internal",
		"/api/notifications/internal/",
		"/api/notifications/v1/../internal" + reg,
		"/api/notifications//internal" + reg,
	}
	// Reach the upstream, which is a case-sensitive Go mux, but no /internal route there.
	proxiedHarmless := []string{
		"/api/notifications/internal;x=1" + reg,
		"/api/notifications/internal%00" + reg,
		"/api/notifications/internal." + reg,
		"/api/notifications/internal%20" + reg,
		"/api/notifications/%c0%af..%c0%afinternal" + reg,
		"/api/notifications/%e2%80%8binternal" + reg,
		"/api/notifications/%ef%bd%89nternal" + reg,
		"/api/notifications/v1/..%2finternal" + reg,
		"/api/notifications/v1/%2e%2e/internal" + reg,
	}
	// A non-CONNECT request is cleaned by the mux (301) before the router; CONNECT is not, so the router sees these unclean.
	cleanedOrUnclean := []string{
		"/api/notifications/INTERNAL/../internal" + reg,
		"/api/notifications/./internal" + reg,
		"/api/notifications/%2Finternal" + reg,
		"/api/notifications/\xff/../internal" + reg,
	}
	for _, set := range [][]string{refused, proxiedHarmless, cleanedOrUnclean} {
		if len(set) == 0 {
			t.Fatal("empty case set")
		}
	}

	// Only CONNECT keeps the raw path; other methods are cleaned to /v1/contacts/me by the mux and proxy legitimately.
	connectRefused := "/api/notifications/internal/../v1/contacts/me"
	t.Run("CONNECT "+connectRefused, func(t *testing.T) {
		c.reset()
		status, _ := rig.rawDo(t, http.MethodConnect, connectRefused, token)
		if status != http.StatusNotFound || c.any.Load() != 0 {
			t.Errorf("answered %d, upstream hits %d, want 404 and 0", status, c.any.Load())
		}
	})
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodConnect} {
		for _, target := range refused {
			t.Run(method+" "+target, func(t *testing.T) {
				c.reset()
				status, _ := rig.rawDo(t, method, target, token)
				if status != http.StatusNotFound || c.any.Load() != 0 {
					t.Errorf("answered %d, upstream hits %d, want 404 and 0", status, c.any.Load())
				}
			})
		}
		for _, target := range append(slices.Clone(proxiedHarmless), cleanedOrUnclean...) {
			t.Run(method+" "+target, func(t *testing.T) {
				c.reset()
				status, _ := rig.rawDo(t, method, target, token)
				if c.internal.Load() != 0 {
					t.Errorf("reached the upstream's /internal route %d times (answered %d)", c.internal.Load(), status)
				}
				if status/100 == 2 {
					t.Errorf("answered %d, want a refusal", status)
				}
			})
		}
	}
}

// Near misses of the internal segment still proxy: only a path that resolves to "internal" first is refused.
// Each request must reach the upstream once and never its /internal route, with and without the mux's cleaning (CONNECT).
func TestRouter_InternalNearMissesStillProxy(t *testing.T) {
	rig := newInternalRig(t)
	token := rig.tg.validToken(t)
	c := rig.counts["notifications"]
	targets := []string{
		"/api/notifications/v1/internal/x",
		"/api/notifications/internals/x",
		"/api/notifications/internal-status",
		"/api/notifications/x/internal",
		"/api/notifications/v1/../v1/contacts/me",
		"/api/notifications/v1/./contacts/me",
		"/api/notifications//v1/contacts/me",
	}
	for _, method := range []string{http.MethodGet, http.MethodConnect} {
		for _, target := range targets {
			t.Run(method+" "+target, func(t *testing.T) {
				c.reset()
				rig.rawDo(t, method, target, token)
				if c.any.Load() != 1 || c.internal.Load() != 0 {
					t.Errorf("upstream saw %d requests and %d /internal hits, want 1 and 0", c.any.Load(), c.internal.Load())
				}
			})
		}
	}
}
