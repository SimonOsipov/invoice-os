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
