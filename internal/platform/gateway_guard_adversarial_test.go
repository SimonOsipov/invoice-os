package platform

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func TestRequireGateway_UncleanPathsNeverReachAGuardedHandlerWithoutAToken(t *testing.T) {
	// Control: the canonical path is live with the token, so the 401s below are the guard's doing.
	app, calls := guardedApp(t)
	ok := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	ok.Header.Set(HeaderGatewayToken, guardToken)
	if rec := serve(app, ok); rec.Code != http.StatusOK || calls.count() != 1 {
		t.Fatalf("control: status = %d, handler calls = %d, want 200 and 1", rec.Code, calls.count())
	}

	for _, path := range []string{
		"//v1/thing", "/v1/./thing", "/v1/x/../thing", "/healthz/../v1/thing",
		"/%76%31/thing", "/v1%2Fthing", "/v1/thing/", "/v1/thing/.",
		"/healthz/", "/readyz/", "/Healthz", "/healthz/x",
	} {
		t.Run(path, func(t *testing.T) {
			app, calls := guardedApp(t)
			r := httptest.NewRequest(http.MethodGet, path, nil)
			newForged().apply(r)
			assertRefused(t, serve(app, r), path)
			if n := calls.count(); n != 0 {
				t.Errorf("handler ran %d times, want 0", n)
			}
		})
	}
}

func TestRequireGateway_UncleanHealthPathsAnswerAsAtHead(t *testing.T) {
	for _, path := range []string{"//healthz", "/v1/../healthz", "/./readyz", "//readyz"} {
		t.Run(path, func(t *testing.T) {
			guarded, calls := guardedApp(t)
			plain := newGuardApp(t)
			assertRefused(t, serve(guarded, httptest.NewRequest(http.MethodGet, "/v1/thing", nil)), "guard engaged")

			got := serve(guarded, httptest.NewRequest(http.MethodGet, path, nil))
			want := serve(plain, httptest.NewRequest(http.MethodGet, path, nil))
			if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Location") != want.Header().Get("Location") {
				t.Errorf("got %d %q %q, want %d %q %q", got.Code, got.Body.String(), got.Header().Get("Location"),
					want.Code, want.Body.String(), want.Header().Get("Location"))
			}
			if calls.count() != 0 {
				t.Errorf("a route handler ran %d times", calls.count())
			}
		})
	}
}

func TestRequireGateway_HeadFollowsGetPatterns(t *testing.T) {
	app, calls := guardedApp(t)

	assertRefused(t, serve(app, httptest.NewRequest(http.MethodHead, "/v1/thing", nil)), "HEAD without token")
	if n := calls.count(); n != 0 {
		t.Errorf("handler ran %d times for a refused HEAD, want 0", n)
	}
	r := httptest.NewRequest(http.MethodHead, "/v1/thing", nil)
	r.Header.Set(HeaderGatewayToken, guardToken)
	if rec := serve(app, r); rec.Code != http.StatusOK {
		t.Errorf("HEAD with token status = %d, want 200", rec.Code)
	}
}

func TestRequireGateway_OnlyTheFirstTokenValueCounts(t *testing.T) {
	app, calls := guardedApp(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	r.Header[HeaderGatewayToken] = []string{"wrong", guardToken}

	assertRefused(t, serve(app, r), "wrong value first, right value second")
	if n := calls.count(); n != 0 {
		t.Errorf("handler ran %d times, want 0", n)
	}
}

func TestRequireGateway_OpenPatternShapes(t *testing.T) {
	const (
		wild     = "POST /v1/peer/{id}"
		hosted   = "POST example.test/v1/hosted"
		catchAll = "POST /v1/open/{rest...}"
		specific = "POST /v1/open/secret"
	)
	for _, tc := range []struct {
		name, method, target string
		open                 bool
	}{
		{"wildcard segment", http.MethodPost, "/v1/peer/abc", true},
		{"wildcard segment, wrong method", http.MethodGet, "/v1/peer/abc", false},
		{"host pattern, right host", http.MethodPost, "http://example.test/v1/hosted", true},
		{"host pattern, other host", http.MethodPost, "http://other.test/v1/hosted", false},
		{"catch-all", http.MethodPost, "/v1/open/a/b", true},
		{"specific route under an open catch-all", http.MethodPost, "/v1/open/secret", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newGuardApp(t)
			calls := &guardCalls{}
			for _, p := range []string{wild, "GET /v1/peer/{id}", hosted, catchAll, specific} {
				app.Mux.HandleFunc(p, calls.handler)
			}
			app.RequireGateway(guardToken, wild, hosted, catchAll)
			r := httptest.NewRequest(tc.method, tc.target, nil)
			newForged().apply(r)

			rec := serve(app, r)
			if !tc.open {
				assertRefused(t, rec, tc.name)
				if n := calls.count(); n != 0 {
					t.Errorf("handler ran %d times, want 0", n)
				}
				return
			}
			if rec.Code != http.StatusOK || calls.count() != 1 {
				t.Fatalf("status = %d, handler calls = %d, want 200 and 1", rec.Code, calls.count())
			}
			if calls.hasID || calls.tenantl || calls.tenantID != "" {
				t.Errorf("open handler saw identity %+v, tenant-less %v, tenant %q; want none", calls.identity, calls.tenantl, calls.tenantID)
			}
		})
	}
}

func TestRequireGateway_ConfiguredTokenAndQueryNeverReachTheLog(t *testing.T) {
	const (
		secret = "cfg-secret-6b1d94e0"
		query  = "q-marker-4417"
	)
	var buf bytes.Buffer
	app := newGuardApp(t)
	app.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	calls := &guardCalls{}
	app.Mux.HandleFunc("GET /v1/thing", calls.handler)
	app.Mux.HandleFunc("POST /v1/peer", calls.handler)
	app.RequireGateway(secret, "POST /v1/peer")

	refused := []struct{ method, target, token string }{
		{http.MethodGet, "/v1/thing?q=" + query, ""},
		{http.MethodGet, "/v1/thing", secret[:len(secret)-1]},
		{http.MethodGet, "/v1/thing", secret + "x"},
		{http.MethodGet, "/v1/nothing", ""},
		{http.MethodHead, "/v1/thing", ""},
	}
	for _, tc := range refused {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		if tc.token != "" {
			r.Header.Set(HeaderGatewayToken, tc.token)
		}
		rec := serve(app, r)
		assertRefused(t, rec, tc.method+" "+tc.target)
		if strings.Contains(rec.Body.String()+fmt.Sprint(rec.Header()), secret) {
			t.Errorf("response to %s %s carries the configured token", tc.method, tc.target)
		}
	}

	admitted := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	admitted.Header.Set(HeaderGatewayToken, secret)
	if rec := serve(app, admitted); rec.Code != http.StatusOK {
		t.Fatalf("admitted status = %d, want 200", rec.Code)
	}
	open := httptest.NewRequest(http.MethodPost, "/v1/peer", nil)
	if rec := serve(app, open); rec.Code != http.StatusOK {
		t.Fatalf("open route status = %d, want 200", rec.Code)
	}

	var lines []map[string]any
	for _, rec := range logRecords(t, &buf) {
		if rec["msg"] == refusalLogMsg {
			lines = append(lines, rec)
		}
	}
	if len(lines) != len(refused) {
		t.Fatalf("refusal lines = %d, want %d (one per refusal, none for the admitted or open request): %s", len(lines), len(refused), buf.String())
	}
	if lines[len(lines)-1]["method"] != http.MethodHead {
		t.Errorf("last refusal method = %v, want HEAD", lines[len(lines)-1]["method"])
	}
	for _, leak := range []string{secret, secret[:len(secret)-1], query} {
		if strings.Contains(buf.String(), leak) {
			t.Errorf("log holds %q: %s", leak, buf.String())
		}
	}
}

func TestApp_WithoutRequireGatewayIgnoresAGatewayTokenHeader(t *testing.T) {
	app := newGuardApp(t)
	calls := &guardCalls{}
	app.Mux.HandleFunc("GET /v1/thing", calls.handler)
	f := newForged()
	r := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	r.Header.Set(HeaderGatewayToken, "anything")
	f.apply(r)

	if rec := serve(app, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := auth.Identity{Subject: f.user, Role: f.role, TenantID: f.tenant, Email: f.email}
	if !calls.hasID || calls.identity != want {
		t.Errorf("identity = %+v (present %v), want %+v", calls.identity, calls.hasID, want)
	}
	if rec := serve(app, httptest.NewRequest(http.MethodGet, "/v1/nothing", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("unmatched path status = %d, want 404 (no guard)", rec.Code)
	}
}

func TestRequireToken_EmptyConfiguredTokenAdmitsOnlyAMissingOrEmptyHeader(t *testing.T) {
	// Pins the documented hazard: callers must pass a non-empty token (RequireGateway panics on one).
	h := RequireToken("X-Test-Token", "")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		name  string
		set   bool
		value string
		want  int
	}{
		{"header absent", false, "", http.StatusOK},
		{"header empty", true, "", http.StatusOK},
		{"header guessed", true, "guess", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			if tc.set {
				r.Header.Set("X-Test-Token", tc.value)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// The marker is the guard's own refusal and nothing else: an admitted request, an open route and a
// client-sent copy never carry it, and the refusal body stays the plain 401.
func TestRequireGateway_MarkerOnlyOnARefusal(t *testing.T) {
	app, _ := guardedApp(t)

	ok := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	ok.Header.Set(HeaderGatewayToken, guardToken)
	ok.Header.Set(HeaderGatewayGuard, GatewayGuardRefused)
	rec := serve(app, ok)
	if rec.Code != http.StatusOK {
		t.Fatalf("admitted request: status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Values(HeaderGatewayGuard); len(got) != 0 {
		t.Errorf("admitted request: %s = %q, want none", HeaderGatewayGuard, got)
	}

	open := serve(app, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if open.Code != http.StatusOK {
		t.Fatalf("open route: status = %d, want 200", open.Code)
	}
	if got := open.Header().Values(HeaderGatewayGuard); len(got) != 0 {
		t.Errorf("open route: %s = %q, want none", HeaderGatewayGuard, got)
	}

	refused := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	refused.Header.Set(HeaderGatewayGuard, "something-else")
	rr := serve(app, refused)
	assertRefused(t, rr, "no token, client-sent marker")
	if got := rr.Header().Values(HeaderGatewayGuard); len(got) != 1 {
		t.Errorf("refusal: %s = %q, want exactly one value", HeaderGatewayGuard, got)
	}
}
