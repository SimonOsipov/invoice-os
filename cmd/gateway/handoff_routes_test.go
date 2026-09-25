package main

import (
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

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// handoffRoutes maps each hand-off pattern to the handoff field main must wrap in withCORS.
var handoffRoutes = map[string]string{
	"POST /auth/sign-in":     "SignIn",
	"OPTIONS /auth/sign-in":  "SignIn",
	"POST /auth/exchange":    "Exchange",
	"OPTIONS /auth/exchange": "Exchange",
}

const (
	handoffAllowedOrigin = "https://landing.example"
	handoffState         = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 base64url chars (D25)
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

// handoffMux mounts handoffHandlers on the four patterns the way main does.
func handoffMux(t *testing.T, authURL *url.URL, withOptions bool) *http.ServeMux {
	t.Helper()
	h := handoffHandlers(authURL, slog.New(slog.DiscardHandler))
	withCORS := gateway.CORS([]string{handoffAllowedOrigin})
	fields := map[string]http.Handler{"SignIn": h.SignIn, "Exchange": h.Exchange}
	mux := http.NewServeMux()
	for pattern, field := range handoffRoutes {
		if !withOptions && strings.HasPrefix(pattern, "OPTIONS ") {
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

func handoffPaths() []string { return []string{"/auth/sign-in", "/auth/exchange"} }

func TestHandoffPreflightAnswersThroughCORS(t *testing.T) {
	gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.URL.Query().Get("grant_type") != "password" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-A","refresh_token":"ref-A"}`))
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
}
