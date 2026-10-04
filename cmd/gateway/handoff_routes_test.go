package main

import (
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/types"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// handoffRoutes maps each hand-off pattern to the handoff field main must wrap in withCORS.
var handoffRoutes = map[string]string{
	"POST /auth/sign-in":     "SignIn",
	"OPTIONS /auth/sign-in":  "SignIn",
	"POST /auth/exchange":    "Exchange",
	"OPTIONS /auth/exchange": "Exchange",
	"POST /auth/refresh":     "Refresh",
	"OPTIONS /auth/refresh":  "Refresh",
	"POST /auth/sign-out":    "SignOut",
	"OPTIONS /auth/sign-out": "SignOut",
}

const (
	handoffAllowedOrigin = "https://landing.example"
	handoffState         = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 base64url chars
)

func TestHandoffRoutesRegisteredWithPreflight(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if buildConstrained(src) {
		t.Fatal("main.go carries a build constraint; its routes are not in every build")
	}
	sites, stmts := mainRoutes(t, src)
	if len(sites) < 4 {
		t.Fatalf("found %d literal-pattern routes in main, want at least 4; the scan went blind: %+v", len(sites), sites)
	}
	// Control: a known guarded registration, so the scan can tell guarded from top-level.
	if s := sitesFor(sites, "OPTIONS /auth/login"); len(s) != 1 || s[0].topLevel {
		t.Errorf("control: OPTIONS /auth/login = %+v, want one registration under the mock-issuer if", s)
	}

	corsLocal, recv := false, ""
	for _, s := range stmts {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		switch types.ExprString(call.Fun) {
		case "gateway.CORS":
			if types.ExprString(as.Lhs[0]) == "withCORS" {
				corsLocal = true
			}
		case "handoffHandlers":
			if len(call.Args) == 0 || types.ExprString(call.Args[0]) != `probed["auth"]` {
				t.Errorf("handoffHandlers base = %v, want probed[\"auth\"]", call.Args)
			}
			recv = types.ExprString(as.Lhs[0])
		}
	}
	if !corsLocal {
		t.Fatal("main has no top-level `withCORS := gateway.CORS(...)`; the wrap below names nothing")
	}
	if recv == "" {
		t.Fatal("main has no top-level `x := handoffHandlers(probed[\"auth\"], ...)`")
	}

	for pattern, field := range handoffRoutes {
		s := sitesFor(sites, pattern)
		if len(s) != 1 {
			t.Errorf("%s is registered %d times, want exactly once", pattern, len(s))
			continue
		}
		if !s[0].topLevel {
			t.Errorf("%s is registered under a condition; it must be a top-level statement of main", pattern)
		}
		if want := "withCORS(" + recv + "." + field + ")"; s[0].handler != want {
			t.Errorf("%s handler = %s, want %s", pattern, s[0].handler, want)
		}
	}
}

// handoffMux mounts handoffHandlers on the hand-off patterns the way main does.
func handoffMux(t *testing.T, authURL *url.URL, withOptions bool) *http.ServeMux {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	h := handoffHandlers(authURL, gateway.NewSessionChecker(nil, nil, time.Now, log), log, nil)
	withCORS := gateway.CORS([]string{handoffAllowedOrigin})
	fields := map[string]http.Handler{"SignIn": h.SignIn, "Exchange": h.Exchange, "Refresh": h.Refresh, "SignOut": h.SignOut}
	mux := http.NewServeMux()
	for pattern, field := range handoffRoutes {
		if !withOptions && strings.HasPrefix(pattern, "OPTIONS ") {
			continue
		}
		// An unbuilt handler stays unmounted, so its path 404s rather than panicking; TestHandoffHandlersWiring names it.
		if fields[field] == nil {
			continue
		}
		mux.Handle(pattern, withCORS(fields[field]))
	}
	return mux
}

func preflight(mux http.Handler, path, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodOptions, path, nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func postJSON(mux http.Handler, path, origin, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func handoffPaths() []string {
	return []string{"/auth/sign-in", "/auth/exchange", "/auth/refresh", "/auth/sign-out"}
}

// handoffAccessToken is a GoTrue-shaped access token; sign-out reads its sub without verifying it.
func handoffAccessToken(sub string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc([]byte(`{"sub":"`+sub+`"}`)) + "." + enc([]byte("sig"))
}

func TestHandoffPreflightGrantsTraceHeaders(t *testing.T) {
	authURL, _ := url.Parse("http://127.0.0.1:1")
	mux := handoffMux(t, authURL, true)

	paths := handoffPaths()
	if len(paths) == 0 {
		t.Fatal("handoffPaths is empty; the loop below would be vacuous")
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", handoffAllowedOrigin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", "content-type, sentry-trace, baggage, traceparent")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s = %d, want 204", path, rec.Code)
		}
		got := allowHeaderSet(rec.Header())
		if !got["content-type"] {
			t.Errorf("control: OPTIONS %s granted %v, want content-type", path, got)
		}
		for _, tok := range []string{"sentry-trace", "baggage"} {
			if !got[tok] {
				t.Errorf("OPTIONS %s granted %v, missing %q", path, got, tok)
			}
		}
		if got["traceparent"] {
			t.Errorf("OPTIONS %s granted %v, must not grant traceparent", path, got)
		}
	}
}

func TestHandoffPreflightAnswersThroughCORS(t *testing.T) {
	tokB := handoffAccessToken("user-b")
	gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/logout" && r.URL.RawQuery == "scope=global":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path != "/token":
			http.NotFound(w, r)
		case r.URL.Query().Get("grant_type") == "password":
			_, _ = w.Write([]byte(`{"access_token":"tok-A","refresh_token":"ref-A"}`))
		case r.URL.Query().Get("grant_type") == "refresh_token":
			_, _ = w.Write([]byte(`{"access_token":"` + tokB + `","refresh_token":"ref-B"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gotrue.Close()
	authURL, _ := url.Parse(gotrue.URL)
	mux := handoffMux(t, authURL, true)
	control := handoffMux(t, authURL, false)

	for _, path := range handoffPaths() {
		rec := preflight(mux, path, handoffAllowedOrigin)
		if rec.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s from an allowed origin = %d, want 204", path, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != handoffAllowedOrigin {
			t.Errorf("OPTIONS %s Access-Control-Allow-Origin = %q, want %q", path, got, handoffAllowedOrigin)
		}
		if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
			t.Errorf("OPTIONS %s Access-Control-Allow-Methods = %q, want POST granted", path, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Content-Type") {
			t.Errorf("OPTIONS %s Access-Control-Allow-Headers = %q, want Content-Type granted", path, got)
		}

		// Control: without the OPTIONS route the method-scoped POST pattern 405s the preflight.
		if c := preflight(control, path, handoffAllowedOrigin); c.Code != http.StatusMethodNotAllowed {
			t.Errorf("control: OPTIONS %s on a POST-only mux = %d, want 405", path, c.Code)
		}
	}

	// The follow-up POST carries the grant and reaches the real handlers, which share one store.
	rec := postJSON(mux, "/auth/sign-in", handoffAllowedOrigin,
		`{"email":"a@example.com","password":"pw","state":"`+handoffState+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /auth/sign-in = %d %s, want 200 with a code", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != handoffAllowedOrigin {
		t.Errorf("POST /auth/sign-in Access-Control-Allow-Origin = %q, want %q", got, handoffAllowedOrigin)
	}
	var in struct{ Code string }
	if err := json.Unmarshal(rec.Body.Bytes(), &in); err != nil || in.Code == "" {
		t.Fatalf("sign-in body %s carries no code (err %v)", rec.Body, err)
	}
	body, _ := json.Marshal(map[string]string{"code": in.Code, "state": handoffState})
	rec = postJSON(mux, "/auth/exchange", handoffAllowedOrigin, string(body))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tok-A"`) {
		t.Errorf("POST /auth/exchange = %d %s, want 200 with tok-A", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != handoffAllowedOrigin {
		t.Errorf("POST /auth/exchange Access-Control-Allow-Origin = %q, want %q", got, handoffAllowedOrigin)
	}

	// The exchanged refresh token renews through the same mux and carries the grant.
	var ex struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ex); err != nil || ex.RefreshToken != "ref-A" {
		t.Fatalf("exchange body %s carries no ref-A (err %v)", rec.Body, err)
	}
	body, _ = json.Marshal(map[string]string{"refresh_token": ex.RefreshToken})
	rec = postJSON(mux, "/auth/refresh", handoffAllowedOrigin, string(body))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"`+tokB+`"`) || !strings.Contains(rec.Body.String(), `"ref-B"`) {
		t.Errorf("POST /auth/refresh = %d %s, want 200 with tokB and ref-B", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != handoffAllowedOrigin {
		t.Errorf("POST /auth/refresh Access-Control-Allow-Origin = %q, want %q", got, handoffAllowedOrigin)
	}

	// The renewed refresh token signs out through the same mux and carries the grant.
	rec = postJSON(mux, "/auth/sign-out", handoffAllowedOrigin, `{"refresh_token":"ref-B"}`)
	if rec.Code != http.StatusNoContent {
		t.Errorf("POST /auth/sign-out = %d %s, want 204", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != handoffAllowedOrigin {
		t.Errorf("POST /auth/sign-out Access-Control-Allow-Origin = %q, want %q", got, handoffAllowedOrigin)
	}
}

func TestHandoffPreflightDisallowedOriginGetsNoGrant(t *testing.T) {
	authURL, _ := url.Parse("http://127.0.0.1:1")
	mux := handoffMux(t, authURL, true)
	const origin = "https://evil.example"
	grants := []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"}

	for _, path := range handoffPaths() {
		rec := preflight(mux, path, origin)
		if rec.Code != http.StatusNoContent {
			t.Errorf("OPTIONS %s from a disallowed origin = %d, want 204", path, rec.Code)
		}
		for _, h := range grants {
			if got := rec.Header().Get(h); got != "" {
				t.Errorf("OPTIONS %s from a disallowed origin carries %s = %q, want none", path, h, got)
			}
		}
		// Positive pair: the allowed origin on the same mux does get the grant.
		if got := preflight(mux, path, handoffAllowedOrigin).Header().Get("Access-Control-Allow-Origin"); got != handoffAllowedOrigin {
			t.Errorf("OPTIONS %s from the allowed origin Access-Control-Allow-Origin = %q, want %q", path, got, handoffAllowedOrigin)
		}
	}

	// A disallowed POST still reaches the handler (400 on an unknown code) and carries no grant.
	rec := postJSON(mux, "/auth/exchange", origin, `{"code":"x","state":"`+handoffState+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /auth/exchange from a disallowed origin = %d %s, want 400 from the exchange handler", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("POST /auth/exchange from a disallowed origin Access-Control-Allow-Origin = %q, want none", got)
	}
	for _, path := range []string{"/auth/refresh", "/auth/sign-out"} {
		rec = postJSON(mux, path, origin, `{}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s from a disallowed origin = %d %s, want 400 from its handler", path, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("POST %s from a disallowed origin Access-Control-Allow-Origin = %q, want none", path, got)
		}
	}
}
