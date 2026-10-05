package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

func TestGatewayTokenSignsEveryRouteKind(t *testing.T) {
	cases := []struct {
		name, svc, method, path, wantPath string
		tenantless                        bool
	}{
		{"tenant GET, nested path", "portfolio", "GET", "/api/portfolio/v1/clients/42/contacts", "/v1/clients/42/contacts", false},
		{"query string", "dashboard", "GET", "/api/dashboard/v1/summary?from=a&to=b", "/v1/summary", false},
		{"POST", "invoice", "POST", "/api/invoice/v1/invoices", "/v1/invoices", false},
		{"PUT", "submission", "PUT", "/api/submission/v1/runs/7", "/v1/runs/7", false},
		{"PATCH", "notifications", "PATCH", "/api/notifications/v1/prefs", "/v1/prefs", false},
		{"DELETE", "validation", "DELETE", "/api/validation/v1/rules/3", "/v1/rules/3", false},
		{"bare service prefix", "invoice", "GET", "/api/invoice", "/", false},
		{"prefix with trailing slash", "tenancy", "GET", "/api/tenancy/", "/", false},
		{"tenant-less provisioning", "tenancy", "POST", "/api/tenancy/v1/workspaces", "/v1/workspaces", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tg := setupGateway(t)
			opts := auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant}
			if c.tenantless {
				opts.TenantID = ""
			}
			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request(c.method, c.path, tg.mint(t, opts)))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			cap := tg.caps[c.svc]
			if cap.hits != 1 {
				t.Fatalf("%s hits = %d, want 1", c.svc, cap.hits)
			}
			if cap.path != c.wantPath {
				t.Errorf("upstream path = %q, want %q (prefix stripping)", cap.path, c.wantPath)
			}
			if got := cap.header.Values(platform.HeaderGatewayToken); !slices.Equal(got, []string{testGatewayToken}) {
				t.Errorf("upstream X-Gateway-Token = %q, want [%q]", got, testGatewayToken)
			}
			for svc, other := range tg.caps {
				if svc != c.svc && other.hits != 0 {
					t.Errorf("%s was hit by a request for %s", svc, c.svc)
				}
			}
		})
	}
}

// Header names reach the gateway as a real server parses them, so the client's casing, duplicates
// and Connection-listed names go through net/http, not a hand-built request.
func TestGatewayTokenSurvivesClientHeaderTricks(t *testing.T) {
	tricks := []struct {
		name string
		set  func(h http.Header)
	}{
		{"lower-case name", func(h http.Header) { h["x-gateway-token"] = []string{"forged"} }},
		{"upper-case name", func(h http.Header) { h["X-GATEWAY-TOKEN"] = []string{"forged"} }},
		{"duplicate names in three cases", func(h http.Header) {
			h["X-Gateway-Token"] = []string{"forged-a", "forged-b"}
			h["x-gateway-token"] = []string{"forged-c"}
			h["X-GATEWAY-TOKEN"] = []string{"forged-d"}
		}},
		{"empty value", func(h http.Header) { h["X-Gateway-Token"] = []string{""} }},
		{"listed in Connection", func(h http.Header) {
			h.Set("Connection", "X-Gateway-Token")
			h.Set("X-Gateway-Token", "forged")
		}},
		{"listed in a lower-case Connection among others", func(h http.Header) {
			h["connection"] = []string{"keep-alive, x-gateway-token, x-s2s-token"}
			h.Set("X-Gateway-Token", "forged")
		}},
		{"S2S token in a lower-case name", func(h http.Header) { h["x-s2s-token"] = []string{"sneaky"} }},
		{"S2S token listed in Connection", func(h http.Header) {
			h.Set("Connection", "X-S2S-Token")
			h.Set("X-S2S-Token", "sneaky")
		}},
		{"identity header listed in Connection", func(h http.Header) {
			h.Set("Connection", "X-Tenant-ID, X-User-ID")
			h.Set("X-Tenant-ID", "tenant-forged")
			h.Set("X-User-ID", "forged-user")
		}},
	}
	for _, c := range tricks {
		t.Run(c.name, func(t *testing.T) {
			tg := setupGateway(t)
			srv := httptest.NewServer(tg.handler)
			t.Cleanup(srv.Close)

			req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/validation/v1/validate/batch", nil)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+tg.validToken(t))
			c.set(req.Header)
			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			cap := tg.caps["validation"]
			if cap.hits != 1 {
				t.Fatalf("validation hits = %d, want 1", cap.hits)
			}
			if got := cap.header.Values("X-Gateway-Token"); !slices.Equal(got, []string{testGatewayToken}) {
				t.Errorf("upstream X-Gateway-Token = %q, want only [%q]", got, testGatewayToken)
			}
			if got := cap.header.Values("X-S2S-Token"); len(got) != 0 {
				t.Errorf("upstream X-S2S-Token = %q, want none", got)
			}
			assertHeader(t, cap.header, "X-Tenant-ID", testTenant)
			assertHeader(t, cap.header, "X-User-ID", testSubject)
		})
	}
}

// outputs is everything a caller or an operator can read back from one gateway run.
type outputs struct {
	status  int
	headers http.Header
	body    string
}

func (o outputs) leaks() string {
	var b strings.Builder
	for k, vs := range o.headers {
		b.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	b.WriteString(o.body)
	return b.String()
}

// The refusal paths (401, 403, 404, 502) and the success path all answer without the credential.
func TestGatewayTokenNeverLeavesTheGateway(t *testing.T) {
	scenario := func(t *testing.T, h http.Handler, tg *testGateway, bearer string) {
		t.Helper()
		tenantless := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole})
		for _, c := range []struct {
			name, path, bearer string
			want               int
		}{
			{"proxied", "/api/tenancy/v1/ping", bearer, http.StatusOK},
			{"unauthenticated", "/api/tenancy/v1/ping", "", http.StatusUnauthorized},
			{"tenant-less", "/api/tenancy/v1/ping", tenantless, http.StatusForbidden},
			{"unknown service", "/api/nosuch/v1/ping", bearer, http.StatusNotFound},
			{"unreachable upstream", "/api/invoice/v1/ping", bearer, http.StatusBadGateway},
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, request("GET", c.path, c.bearer))
			if rec.Code != c.want {
				t.Fatalf("%s: status = %d, want %d", c.name, rec.Code, c.want)
			}
			o := outputs{rec.Code, rec.Header(), rec.Body.String()}
			if strings.Contains(o.leaks(), testGatewayToken) {
				t.Errorf("%s: the response carries the gateway token:\n%s", c.name, o.leaks())
			}
		}
	}

	t.Run("responses and log lines", func(t *testing.T) {
		tg := setupGateway(t)
		log, buf := captureLog()
		h := Handler(Options{
			Verifier:     tg.verifier,
			Sessions:     liveSessions(t),
			Upstreams:    map[string]*url.URL{"tenancy": statusUpstream(t), "invoice": closedURL(t)},
			Logger:       log,
			GatewayToken: testGatewayToken,
		})
		scenario(t, h, tg, tg.validToken(t))

		if !strings.Contains(buf.String(), "gateway upstream unreachable") || !strings.Contains(buf.String(), "gateway authz denied") {
			t.Fatalf("the scenario logged no refusal line, so the check below is vacuous:\n%s", buf.String())
		}
		if strings.Contains(buf.String(), testGatewayToken) {
			t.Errorf("a log line carries the gateway token:\n%s", buf.String())
		}
	})

	t.Run("sentry events and logs", func(t *testing.T) {
		app, rec, _ := sentrytest.Boot(t, "gateway")
		tg := setupGateway(t)
		app.Mux.Handle(routePrefix, Handler(Options{
			Verifier:     tg.verifier,
			Sessions:     liveSessions(t),
			Upstreams:    map[string]*url.URL{"tenancy": statusUpstream(t), "invoice": closedURL(t)},
			Logger:       app.Logger,
			GatewayToken: testGatewayToken,
		}))
		scenario(t, app.Handler(), tg, tg.validToken(t))

		var wire []string
		for _, set := range [][]*sentry.Event{rec.Events(), rec.Transactions(), rec.LogEvents()} {
			for _, e := range set {
				raw, err := json.Marshal(e)
				if err != nil {
					t.Fatalf("marshal event: %v", err)
				}
				wire = append(wire, string(raw))
			}
		}
		if len(rec.Events()) == 0 || len(rec.Logs()) == 0 {
			t.Fatalf("recorded %d events and %d logs, want some of each -- the check below is vacuous", len(rec.Events()), len(rec.Logs()))
		}
		for _, w := range wire {
			if strings.Contains(w, testGatewayToken) {
				t.Errorf("a Sentry payload carries the gateway token: %s", w)
			}
		}
	})
}

const upstreamSecretBody = "UPSTREAM-BODY-SECRET"

// markedUpstream answers status with the guard marker value and a body the client must never see.
func markedUpstream(t *testing.T, status int, marker string) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if marker != "" {
			w.Header().Set(platform.HeaderGatewayGuard, marker)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(upstreamSecretBody))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return u
}

// The marker alone decides: any status, even a 2xx, answers 502. Only the exact guard value counts.
func TestGuardMarkerAnswers502OnAnyStatusAndNeverLeaks(t *testing.T) {
	for _, status := range []int{200, 204, 401, 403, 404, 500, 503} {
		t.Run("status "+http.StatusText(status), func(t *testing.T) {
			tg := setupGateway(t)
			h := Handler(Options{Verifier: tg.verifier, Sessions: liveSessions(t), Upstreams: map[string]*url.URL{"tenancy": markedUpstream(t, status, platform.GatewayGuardRefused)}, GatewayToken: testGatewayToken})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tg.validToken(t)))

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", rec.Code)
			}
			if got := rec.Body.String(); got != `{"error":"bad gateway"}`+"\n" {
				t.Errorf("body = %q, want the gateway's bad-gateway body", got)
			}
			for k, vs := range rec.Header() {
				if strings.EqualFold(k, platform.HeaderGatewayGuard) {
					t.Errorf("header %s = %q reached the client", k, vs)
				}
			}
			if strings.Contains(rec.Body.String(), upstreamSecretBody) {
				t.Errorf("the upstream's body reached the client: %q", rec.Body.String())
			}
		})
	}

	for _, v := range []string{"Refused", "refused;x", "REFUSED", "1", ""} {
		t.Run("not the marker "+v, func(t *testing.T) {
			tg := setupGateway(t)
			h := Handler(Options{Verifier: tg.verifier, Sessions: liveSessions(t), Upstreams: map[string]*url.URL{"tenancy": markedUpstream(t, http.StatusUnauthorized, v)}, GatewayToken: testGatewayToken})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, request("GET", "/api/tenancy/v1/ping", tg.validToken(t)))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want the upstream's 401 passed through", rec.Code)
			}
		})
	}
}

// A client-sent marker is request data: it must not turn a pass-through into a 502, and an
// upstream answer without the marker stays unreported by the gateway.
func TestGuardMarkerSentByAClientHasNoEffect(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
	}{{"upstream 200", 200}, {"upstream 401", 401}, {"upstream 500", 500}} {
		t.Run(c.name, func(t *testing.T) {
			app, rec, want := sentrytest.Boot(t, "gateway")
			h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": markedUpstream(t, c.status, ""), "marked": markedUpstream(t, 401, platform.GatewayGuardRefused)})
			for _, name := range []string{"x-gateway-guard", platform.HeaderGatewayGuard} {
				req := request(http.MethodGet, "/api/tenancy/v1/ping", tok)
				req.Header[name] = []string{platform.GatewayGuardRefused}
				out := httptest.NewRecorder()
				h.ServeHTTP(out, req)
				if out.Code != c.status {
					t.Errorf("client sent %s: status = %d, want the upstream's %d", name, out.Code, c.status)
				}
			}
			rec.None(t)

			// Positive control: a marked upstream response on the same recorder is counted.
			serveAPI(h, "/api/marked/v1/ping", tok)
			rec.One(t, want)
		})
	}
}

// A refusal opens exactly one Sentry issue, as the gateway's own 5xx, and carries no secret.
func TestGuardMarkerRefusalIsReportedOnce(t *testing.T) {
	app, rec, want := sentrytest.Boot(t, "gateway")
	h, tok := mountAPI(t, app, map[string]*url.URL{"tenancy": markedUpstream(t, http.StatusUnauthorized, platform.GatewayGuardRefused)})

	if got := serveAPI(h, "/api/tenancy/v1/ping", tok).Code; got != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", got)
	}
	e := rec.One(t, want)
	if wantFP := []string{"http-5xx", routePrefix, "502"}; !slices.Equal(e.Fingerprint, wantFP) {
		t.Errorf("fingerprint = %q, want %q", e.Fingerprint, wantFP)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	for _, secret := range []string{testGatewayToken, tok, upstreamSecretBody} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the Sentry event carries %q: %s", secret, raw)
		}
	}
}

// The one ERROR line names the upstream and carries neither a credential nor a client-forged identity.
func TestGuardMarkerErrorLogHasNoSecrets(t *testing.T) {
	tg := setupGateway(t)
	log, buf := captureLog()
	tok := tg.validToken(t)
	h := Handler(Options{Verifier: tg.verifier, Sessions: liveSessions(t), Upstreams: map[string]*url.URL{"tenancy": markedUpstream(t, http.StatusUnauthorized, platform.GatewayGuardRefused)}, Logger: log, GatewayToken: testGatewayToken})

	req := request("GET", "/api/tenancy/v1/ping", tok)
	forged := map[string]string{"X-Tenant-ID": "tenant-FORGED", "X-User-ID": "user-FORGED", "X-User-Role": "role-FORGED", "X-User-Email": "forged@FORGED.test", platform.HeaderGatewayToken: "token-FORGED"}
	for k, v := range forged {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	var errLines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if rec["level"] == "ERROR" {
			errLines = append(errLines, rec)
		}
	}
	if len(errLines) != 1 {
		t.Fatalf("%d ERROR lines, want 1:\n%s", len(errLines), buf.String())
	}
	if errLines[0]["msg"] != "gateway token refused by upstream" || errLines[0]["upstream"] != "tenancy" {
		t.Errorf("ERROR line = %v, want msg and upstream=tenancy", errLines[0])
	}
	logged := buf.String()
	secrets := []string{testGatewayToken, tok, upstreamSecretBody, testSubject, testTenant}
	for _, v := range forged {
		secrets = append(secrets, v)
	}
	for _, s := range secrets {
		if strings.Contains(logged, s) {
			t.Errorf("the log carries %q:\n%s", s, logged)
		}
	}
}
