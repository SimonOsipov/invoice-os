package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	testIssuer  = "https://mock.fiscalbridge.test"
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

// TestMockLoginRoundTrip proves the dev/CI login path end to end: mint via
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
