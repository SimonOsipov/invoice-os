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

// handoffRoutes maps each hand-off pattern to the handoff field main must wrap in withCORS,
// or in withSessionCORS for the fields in handoffSessionFields.
var handoffRoutes = map[string]string{
	"POST /auth/sign-in":     "SignIn",
	"OPTIONS /auth/sign-in":  "SignIn",
	"POST /auth/exchange":    "Exchange",
	"OPTIONS /auth/exchange": "Exchange",
	"POST /auth/refresh":     "Refresh",
	"OPTIONS /auth/refresh":  "Refresh",
	"POST /auth/sign-out":    "SignOut",
	"OPTIONS /auth/sign-out": "SignOut",

	"POST /auth/console-handoff":    "ConsoleHandoff",
	"OPTIONS /auth/console-handoff": "ConsoleHandoff",
}

// handoffSessionFields are the three routes that read or write the refresh-token cookie.
var handoffSessionFields = map[string]bool{"Exchange": true, "Refresh": true, "SignOut": true}

const (
	handoffAllowedOrigin = "https://landing.example"
	handoffSessionOrigin = "https://app.example"
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

	corsLocal, sessionLocal, recv := false, false, ""
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
		case "gateway.SessionCORS":
			if types.ExprString(as.Lhs[0]) == "withSessionCORS" && len(call.Args) == 2 {
				sessionLocal = true
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
	if !sessionLocal {
		t.Error("main has no top-level `withSessionCORS := gateway.SessionCORS(origins, cookies)`; the session routes would grant no credentials")
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
		wrap := "withCORS"
		if handoffSessionFields[field] {
			wrap = "withSessionCORS"
		}
		if want := wrap + "(" + recv + "." + field + ")"; s[0].handler != want {
			t.Errorf("%s handler = %s, want %s", pattern, s[0].handler, want)
		}
	}
}

// One handoffHandlers call builds the invitee set-password and exchange handlers over one store.
func TestHandoffHandlers_InvitationPasswordCodeRedeemsThroughExchange(t *testing.T) {
	const (
		prefix  = "https://site.example/?verified=1&handoff="
		verify  = `{"access_token":"verify-at","refresh_token":"verify-rt","user":{"id":"u-1","email":"invitee@corp.example"}}`
		session = `{"access_token":"grant-at","refresh_token":"grant-rt"}`
	)
	site, _ := url.Parse("https://site.example")
	gotrue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/verify"):
			_, _ = w.Write([]byte(verify))
		case strings.HasSuffix(r.URL.Path, "/token"):
			_, _ = w.Write([]byte(session))
		case strings.HasSuffix(r.URL.Path, "/logout"):
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(gotrue.Close)
	authURL, _ := url.Parse(gotrue.URL)
	log := slog.New(slog.DiscardHandler)
	h := handoffHandlers(authURL, site, gateway.NewSessionChecker(nil, nil, time.Now, log), log, nil, nil, nil, nil, nil)

	rec := serveForm(h.InvitationPassword, "/auth/invitation/password", "token=T&type=signup&password=n3w-Passw0rd&state="+handoffState)

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, prefix) {
		t.Fatalf("invitation password = %d Location %q, want 303 %s<code>", rec.Code, loc, prefix)
	}
	code := strings.TrimPrefix(loc, prefix)
	exchange := func(state string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"code": code, "state": state})
		return postNoOrigin(h.Exchange, "/auth/exchange", string(body))
	}
	rec = exchange(handoffState)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 2 || got["access_token"] != "grant-at" || got["refresh_token"] != "grant-rt" {
		t.Errorf("exchange body = %s, want exactly the grant's access_token grant-at and refresh_token grant-rt", rec.Body.String())
	}
	if rec = exchange(handoffState); rec.Code != http.StatusBadRequest {
		t.Errorf("second exchange = %d, want 400", rec.Code)
	}
}

// handoffMux mounts handoffHandlers on the hand-off patterns the way main does.
func handoffMux(t *testing.T, authURL *url.URL, withOptions bool) *http.ServeMux {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	cookies, err := gateway.ParseSessionOrigins("app=" + handoffSessionOrigin)
	if err != nil {
		t.Fatalf("ParseSessionOrigins: %v", err)
	}
	h := handoffHandlers(authURL, nil, gateway.NewSessionChecker(nil, nil, time.Now, log), log, nil, nil, nil, cookies, nil)
	origins := []string{handoffAllowedOrigin, handoffSessionOrigin}
	withCORS, withSessionCORS := gateway.CORS(origins), gateway.SessionCORS(origins, cookies)
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
		wrap := withCORS
		if handoffSessionFields[field] {
			wrap = withSessionCORS
		}
		mux.Handle(pattern, wrap(fields[field]))
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

// postNoOrigin is a body-mode caller (prodcheck, e2e/api, ctl): no Origin, so /auth/exchange answers it.
func postNoOrigin(mux http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// postSession is a cookie-mode POST from origin, carrying the refresh-token cookie when cookie is set.
func postSession(mux http.Handler, path, origin, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Auth-Session", "cookie")
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// requireGrant asserts the exact origin and credentials grant on a session-route answer.
func requireGrant(t *testing.T, what string, rec *httptest.ResponseRecorder, origin string) {
	t.Helper()
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Errorf("%s Access-Control-Allow-Origin = %q, want %q", what, got, origin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("%s Access-Control-Allow-Credentials = %q, want true", what, got)
	}
}

// requireCookie asserts exactly one Set-Cookie of the given name, with the value (or cleared when value is "").
func requireCookie(t *testing.T, what string, rec *httptest.ResponseRecorder, name, value string) *http.Cookie {
	t.Helper()
	got := rec.Result().Cookies()
	if len(got) != 1 || got[0].Name != name || got[0].Value != value {
		t.Fatalf("%s Set-Cookie = %q, want one %s=%q", what, rec.Header().Values("Set-Cookie"), name, value)
	}
	if value == "" && !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("%s clear = %q, want Max-Age=0", what, rec.Header().Get("Set-Cookie"))
	}
	return got[0]
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

	// The sign-in comes from the landing and answers a code; the app origin redeems it in cookie mode.
	rec := postJSON(mux, "/auth/sign-in", handoffAllowedOrigin,
		`{"email":"a@example.com","password":"pw","state":"`+handoffState+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /auth/sign-in = %d %s, want 200 with a code", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != handoffAllowedOrigin {
		t.Errorf("POST /auth/sign-in Access-Control-Allow-Origin = %q, want %q", got, handoffAllowedOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("POST /auth/sign-in Access-Control-Allow-Credentials = %q, want none", got)
	}
	var in struct{ Code string }
	if err := json.Unmarshal(rec.Body.Bytes(), &in); err != nil || in.Code == "" {
		t.Fatalf("sign-in body %s carries no code (err %v)", rec.Body, err)
	}

	body, _ := json.Marshal(map[string]string{"code": in.Code, "state": handoffState})
	rec = postSession(mux, "/auth/exchange", handoffSessionOrigin, string(body), nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"access_token":"tok-A","renewable":true}` {
		t.Fatalf("POST /auth/exchange = %d %s, want 200 {access_token tok-A, renewable true}", rec.Code, rec.Body)
	}
	requireGrant(t, "POST /auth/exchange", rec, handoffSessionOrigin)
	requireCookie(t, "POST /auth/exchange", rec, "__Host-rt-app", "ref-A")

	// The cookie renews through the same mux: the body holds the access token only, the cookie rotates.
	rec = postSession(mux, "/auth/refresh", handoffSessionOrigin, ``, &http.Cookie{Name: "__Host-rt-app", Value: "ref-A"})
	var renewed map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &renewed); rec.Code != http.StatusOK || err != nil || len(renewed) != 1 || renewed["access_token"] != tokB {
		t.Fatalf("POST /auth/refresh = %d %s, want 200 with only access_token tokB", rec.Code, rec.Body)
	}
	requireGrant(t, "POST /auth/refresh", rec, handoffSessionOrigin)
	requireCookie(t, "POST /auth/refresh", rec, "__Host-rt-app", "ref-B")

	// The rotated cookie signs out through the same mux, and the answer clears it.
	rec = postSession(mux, "/auth/sign-out", handoffSessionOrigin, ``, &http.Cookie{Name: "__Host-rt-app", Value: "ref-B"})
	if rec.Code != http.StatusNoContent {
		t.Errorf("POST /auth/sign-out = %d %s, want 204", rec.Code, rec.Body)
	}
	requireGrant(t, "POST /auth/sign-out", rec, handoffSessionOrigin)
	requireCookie(t, "POST /auth/sign-out", rec, "__Host-rt-app", "")

	// The preflight from the session origin asks for the header and gets credentials on the session routes only.
	for _, path := range handoffPaths() {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", handoffSessionOrigin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", "x-auth-session, content-type")
		pre := httptest.NewRecorder()
		mux.ServeHTTP(pre, req)
		wantSession := path != "/auth/sign-in"
		if got := pre.Header().Get("Access-Control-Allow-Credentials") == "true"; got != wantSession {
			t.Errorf("OPTIONS %s credentials = %v, want %v", path, got, wantSession)
		}
		if got := allowHeaderSet(pre.Header())["x-auth-session"]; got != wantSession {
			t.Errorf("OPTIONS %s grants X-Auth-Session = %v, want %v", path, got, wantSession)
		}
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

	// A browser origin without the session header is refused at the exchange (E4) and carries no grant.
	rec := postJSON(mux, "/auth/exchange", origin, `{"code":"x","state":"`+handoffState+`"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "origin not allowed") {
		t.Errorf("POST /auth/exchange from a disallowed origin = %d %s, want 403 origin not allowed", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("POST /auth/exchange from a disallowed origin Access-Control-Allow-Origin = %q, want none", got)
	}
	// Refresh and sign-out keep body mode for a browser origin (the ceiling): 400 from the handler.
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
