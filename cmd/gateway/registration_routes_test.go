package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// routeSite is one Handle/HandleFunc call in func main with a string-literal pattern.
type routeSite struct {
	pattern, handler string
	topLevel         bool // the call is a statement directly in main's body
}

// mainRoutes parses src and returns main's literal-pattern routes and its top-level statements.
func mainRoutes(t *testing.T, src []byte) ([]routeSite, []ast.Stmt) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("no func main")
	}
	top := map[*ast.CallExpr]bool{}
	for _, s := range body.List {
		if es, ok := s.(*ast.ExprStmt); ok {
			if call, ok := es.X.(*ast.CallExpr); ok {
				top[call] = true
			}
		}
	}
	var sites []routeSite
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		p, _ := strconv.Unquote(lit.Value)
		sites = append(sites, routeSite{p, types.ExprString(call.Args[1]), top[call]})
		return true
	})
	return sites, body.List
}

// buildConstrained reports a //go:build or // +build line in the file header.
func buildConstrained(src []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if strings.HasPrefix(line, "//go:build") || strings.HasPrefix(line, "// +build") {
			return true
		}
	}
	return false
}

func sitesFor(sites []routeSite, pattern string) []routeSite {
	var out []routeSite
	for _, s := range sites {
		if s.pattern == pattern {
			out = append(out, s)
		}
	}
	return out
}

// The scanner must tell a top-level registration from a guarded one, or its verdict on main.go is blind.
func TestRegistrationRouteScanClassifies(t *testing.T) {
	const src = `package main
func main() {
	app.Mux.Handle("GET /a", a)
	if cond {
		app.Mux.Handle("GET /b", b)
	}
	switch x {
	case 1:
		app.Mux.HandleFunc("GET /c", c)
	}
	for range xs {
		app.Mux.Handle("GET /d", d)
	}
	func() { app.Mux.Handle("GET /e", e) }()
	app.Mux.Handle(prefix, f)
}`
	sites, _ := mainRoutes(t, []byte(src))
	want := map[string]bool{"GET /a": true, "GET /b": false, "GET /c": false, "GET /d": false, "GET /e": false}
	if len(sites) != len(want) {
		t.Fatalf("found %d literal routes %+v, want %d", len(sites), sites, len(want))
	}
	for _, s := range sites {
		if s.topLevel != want[s.pattern] {
			t.Errorf("%s topLevel = %v, want %v", s.pattern, s.topLevel, want[s.pattern])
		}
	}
	if !buildConstrained([]byte("//go:build mockissuer\n\npackage main\n")) {
		t.Error("a //go:build header is not detected")
	}
	if buildConstrained([]byte("// Command x.\npackage main\n// //go:build later\n")) {
		t.Error("a comment after the package clause counts as a build constraint")
	}
}

func TestRegistrationRoutesRegisteredUnconditionally(t *testing.T) {
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

	// Control needles: one known unconditional, one known guarded registration.
	if s := sitesFor(sites, "GET /healthz/fleet"); len(s) != 1 || !s[0].topLevel {
		t.Errorf("control: GET /healthz/fleet = %+v, want one top-level registration", s)
	}
	if s := sitesFor(sites, "POST /auth/login"); len(s) != 1 || s[0].topLevel {
		t.Errorf("control: POST /auth/login = %+v, want one registration under the mock-issuer if", s)
	}

	// The seam: reg := registrationHandlers(probed["auth"], ...) and withCORS := gateway.CORS(...), both top-level.
	recv, hand, corsLocal := "", "", false
	invPreview, invRegister := "", ""
	for _, s := range stmts {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		if types.ExprString(call.Fun) == "invitationHandlers" && len(as.Lhs) == 2 {
			invPreview, invRegister = types.ExprString(as.Lhs[0]), types.ExprString(as.Lhs[1])
			continue
		}
		if len(as.Lhs) != 1 {
			continue
		}
		switch types.ExprString(call.Fun) {
		case "gateway.CORS":
			corsLocal = corsLocal || types.ExprString(as.Lhs[0]) == "withCORS"
		case "registrationHandlers":
			if len(call.Args) == 0 || types.ExprString(call.Args[0]) != `probed["auth"]` {
				t.Errorf("registrationHandlers base = %v, want probed[\"auth\"]", call.Args)
			}
			recv = types.ExprString(as.Lhs[0])
		case "handoffHandlers":
			hand = types.ExprString(as.Lhs[0])
		}
	}
	if hand == "" {
		t.Fatal("main has no top-level `x := handoffHandlers(...)`; the reset form has no sign-in throttle to clear")
	}
	if recv == "" {
		t.Fatal("main has no top-level `x := registrationHandlers(probed[\"auth\"], ...)`")
	}
	if !corsLocal {
		t.Fatal("main has no top-level `withCORS := gateway.CORS(...)`; the register wrap names nothing")
	}
	if invPreview == "" || invRegister == "" {
		t.Fatal("main has no top-level `preview, register := invitationHandlers(...)`; the invitation routes name nothing")
	}

	// The confirm page comes from the top-level `x, err := gateway.VerifyPageHandler(siteURL)`; an error stops boot.
	page, fatalOnErr := "", false
	for i, s := range stmts {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || types.ExprString(call.Fun) != "gateway.VerifyPageHandler" {
			continue
		}
		if len(call.Args) != 1 || types.ExprString(call.Args[0]) != "siteURL" {
			t.Errorf("gateway.VerifyPageHandler args = %v, want siteURL", call.Args)
		}
		page = types.ExprString(as.Lhs[0])
		if i+1 < len(stmts) {
			if is, ok := stmts[i+1].(*ast.IfStmt); ok && types.ExprString(is.Cond) == "err != nil" {
				ast.Inspect(is.Body, func(n ast.Node) bool {
					if c, ok := n.(*ast.CallExpr); ok && types.ExprString(c.Fun) == "platform.Fatal" {
						fatalOnErr = true
					}
					return true
				})
			}
		}
	}
	if page != "verifyPage" {
		t.Fatalf("main has no top-level `verifyPage, err := gateway.VerifyPageHandler(siteURL)` (found %q)", page)
	}
	if !fatalOnErr {
		t.Error("a VerifyPageHandler error does not reach platform.Fatal in the statement after it")
	}

	// Register is browser-called (landing form): CORS-wrapped with a preflight. Verify is a mailed link
	// whose page and same-origin form POST take no CORS wrap.
	for pattern, want := range map[string]string{
		"POST /auth/register":    "withCORS(" + recv + ".Register)",
		"OPTIONS /auth/register": "withCORS(" + recv + ".Register)",
		"GET /auth/verify":       page,
		"POST /auth/verify":      recv + ".Verify",

		"POST /auth/resend-verification":    "withCORS(" + recv + ".ResendVerification)",
		"OPTIONS /auth/resend-verification": "withCORS(" + recv + ".ResendVerification)",

		"POST /contacts/demo-request":    "withCORS(" + recv + ".DemoRequest)",
		"OPTIONS /contacts/demo-request": "withCORS(" + recv + ".DemoRequest)",

		// The request is landing-page JavaScript; the page and its same-origin form POST take no CORS wrap.
		"POST /auth/request-password-reset":    "withCORS(" + recv + ".RequestPasswordReset)",
		"OPTIONS /auth/request-password-reset": "withCORS(" + recv + ".RequestPasswordReset)",
		"GET /auth/reset-password":             "gateway.ResetPasswordPageHandler(siteURL)",
		"POST /auth/reset-password":            `resetPasswordHandler(probed["auth"], siteURL, sessions, ` + hand + `.SignInThrottle, app.Logger)`,

		// Browser-called from the landing accept page, like register: CORS-wrapped with a preflight.
		"POST /auth/invitation":             "withCORS(" + invPreview + ")",
		"OPTIONS /auth/invitation":          "withCORS(" + invPreview + ")",
		"POST /auth/invitation/register":    "withCORS(" + invRegister + ")",
		"OPTIONS /auth/invitation/register": "withCORS(" + invRegister + ")",
	} {
		s := sitesFor(sites, pattern)
		if len(s) != 1 {
			t.Errorf("%s is registered %d times, want exactly once", pattern, len(s))
			continue
		}
		if !s[0].topLevel {
			t.Errorf("%s is registered under a condition; it must be a top-level statement of main", pattern)
		}
		if s[0].handler != want {
			t.Errorf("%s handler = %s, want %s", pattern, s[0].handler, want)
		}
	}
}

// The account-mail routes are public GETs read by GoTrue and mail clients: top-level, once, no CORS wrap.
func TestAccountMailRoutesRegisteredUnconditionally(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	sites, stmts := mainRoutes(t, src)
	if len(sites) < 4 {
		t.Fatalf("found %d literal-pattern routes in main, want at least 4; the scan went blind: %+v", len(sites), sites)
	}

	// Each template handler is the first result of a top-level `x, err := gateway.MailTemplate("<name>")`,
	// and the statement after it must stop boot through platform.Fatal.
	tpl, fatalOnErr := map[string]string{}, map[string]bool{}
	for i, s := range stmts {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 2 || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || types.ExprString(call.Fun) != "gateway.MailTemplate" {
			continue
		}
		if len(call.Args) != 1 {
			t.Errorf("gateway.MailTemplate args = %v, want one template name", call.Args)
			continue
		}
		name, _ := strconv.Unquote(types.ExprString(call.Args[0]))
		tpl[name] = types.ExprString(as.Lhs[0])
		if i+1 < len(stmts) {
			if is, ok := stmts[i+1].(*ast.IfStmt); ok && types.ExprString(is.Cond) == "err != nil" {
				ast.Inspect(is.Body, func(n ast.Node) bool {
					if c, ok := n.(*ast.CallExpr); ok && types.ExprString(c.Fun) == "platform.Fatal" {
						fatalOnErr[name] = true
					}
					return true
				})
			}
		}
	}
	for _, name := range []string{"confirmation", "recovery"} {
		if tpl[name] == "" {
			t.Fatalf("main has no top-level `x, err := gateway.MailTemplate(%q)` (found %v)", name, tpl)
		}
		if !fatalOnErr[name] {
			t.Errorf("a MailTemplate(%q) error does not reach platform.Fatal in the statement after it; the gateway would boot without its template", name)
		}
	}
	if len(tpl) != 2 {
		t.Errorf("main loads templates %v, want exactly confirmation and recovery", tpl)
	}

	for pattern, want := range map[string]string{
		"GET /emails/confirmation.html": tpl["confirmation"],
		"GET /emails/recovery.html":     tpl["recovery"],
		"GET /emails/mark.png":          "gateway.MailLogo()",
	} {
		s := sitesFor(sites, pattern)
		if len(s) != 1 {
			t.Errorf("%s is registered %d times, want exactly once", pattern, len(s))
			continue
		}
		if !s[0].topLevel {
			t.Errorf("%s is registered under a condition; it must be a top-level statement of main", pattern)
		}
		// Exact handler: a CORS wrap under any name, or the other route's handler, is a different string.
		if s[0].handler != want {
			t.Errorf("%s handler = %s, want %s (no CORS wrap)", pattern, s[0].handler, want)
		}
	}

	// No catch-all or sibling under /emails: an unknown path must stay a 404.
	n := 0
	for _, s := range sites {
		if _, path, ok := strings.Cut(s.pattern, " "); ok && strings.HasPrefix(path, "/emails") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("main registers %d routes under /emails, want exactly the 3 account-mail routes", n)
	}
}

// The invitee set-password POST is mounted at the top level of main, in every build, over its own builder.
func TestRegistrationRoutes_InvitationPasswordIsMounted(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if buildConstrained(src) {
		t.Fatal("main.go carries a build constraint; its routes are not in every build")
	}
	sites, _ := mainRoutes(t, src)
	if len(sites) < 4 {
		t.Fatalf("found %d literal-pattern routes in main, want at least 4; the scan went blind: %+v", len(sites), sites)
	}
	if s := sitesFor(sites, "POST /auth/invitation/password"); len(s) != 1 {
		t.Errorf("POST /auth/invitation/password is registered %d times, want exactly once", len(s))
	} else {
		if !s[0].topLevel {
			t.Error("POST /auth/invitation/password is registered under a condition; it must be a top-level statement of main")
		}
		if !strings.HasPrefix(s[0].handler, `invitationPasswordHandler(probed["auth"], siteURL, `) {
			t.Errorf("POST /auth/invitation/password handler = %s, want invitationPasswordHandler(probed[\"auth\"], siteURL, ...) with no CORS wrap", s[0].handler)
		}
	}

	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	log := slog.New(slog.DiscardHandler)
	signIn := gateway.NewSignInThrottle("sign-in", gateway.SignInMaxFailures, gateway.SignInMaxKeys, gateway.SignInWindow, time.Now)
	mux := http.NewServeMux()
	mux.Handle("POST /auth/invitation/password", invitationPasswordHandler(authURL, site, gateway.NewSessionChecker(nil, nil, time.Now, log), signIn, nil, log))

	rec := serveForm(mux, "/auth/invitation/password", "type=signup&password=new-password-1")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://site.example/?verify=failed" {
		t.Errorf("POST with a bad form = %d Location %q, want 303 https://site.example/?verify=failed", rec.Code, rec.Header().Get("Location"))
	}
	if rec := serveRegistration(mux, http.MethodGet, "/auth/invitation/password", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /auth/invitation/password = %d, want 405", rec.Code)
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("a bad form or a wrong method reached GoTrue: %v", got)
	}
}

const registerAllowedOrigin = "https://landing.example"

// registerMux mounts the real register handler behind the CORS allow-list on both patterns, as main does.
func registerMux(t *testing.T) (*http.ServeMux, func() []string) {
	t.Helper()
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil, noPendingInvite)
	withCORS := gateway.CORS([]string{registerAllowedOrigin})
	mux := http.NewServeMux()
	mux.Handle("POST /auth/register", withCORS(reg.Register))
	mux.Handle("OPTIONS /auth/register", withCORS(reg.Register))
	return mux, calls
}

func TestRegisterPreflightGrantsTheAllowedOrigin(t *testing.T) {
	mux, calls := registerMux(t)

	rec := preflight(mux, "/auth/register", registerAllowedOrigin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight from the allowed origin = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("preflight Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}
	if !allowHeaderSet(rec.Header())["content-type"] {
		t.Errorf("preflight grants %v, want content-type for the JSON body", allowHeaderSet(rec.Header()))
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Errorf("preflight Access-Control-Allow-Methods = %q, want POST granted", got)
	}

	rec = preflight(mux, "/auth/register", "https://evil.example")
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight from a disallowed origin = %d, want 204", rec.Code)
	}
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("preflight from a disallowed origin carries %s = %q, want none", h, got)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("a preflight reached GoTrue: %v", got)
	}

	const body = `{"email":"new@corp.example","password":"Corr3ct-Horse","workspace_name":"Acme","display_name":"Ada","kind":"firm"}`
	rec = postJSON(mux, "/auth/register", registerAllowedOrigin, body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST from the allowed origin = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("POST Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}

	// The landing form reads the 400 message, so a refusal carries the grant too.
	const badAnswers = `{"email":"new@corp.example","password":"Corr3ct-Horse","workspace_name":"","display_name":"Ada"}`
	rec = postJSON(mux, "/auth/register", registerAllowedOrigin, badAnswers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST with a blank workspace_name = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("400 Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}
	if got := calls(); len(got) != 1 {
		t.Errorf("GoTrue saw %v after one good POST and one refused one, want one call", got)
	}

	rec = postJSON(mux, "/auth/register", "https://evil.example", body)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("POST from a disallowed origin got grant %q, want none", got)
	}
}

// Only a preflight (an OPTIONS carrying an Origin) is answered by CORS. Any other OPTIONS reaches the
// handler, which must refuse it as sign-in does (TestHandoffPreflightIsAnsweredByCORS), not register.
func TestRegisterOptionsWithoutOriginIsNotARegistration(t *testing.T) {
	mux, calls := registerMux(t)

	const body = `{"email":"new@corp.example","password":"Corr3ct-Horse","workspace_name":"Acme","display_name":"Ada","kind":"firm"}`
	req := httptest.NewRequest(http.MethodOptions, "/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("OPTIONS with no Origin = %d Allow %q, want 405 Allow POST: %s", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("an OPTIONS request reached GoTrue: %v", got)
	}

	// Positive pair: the same body as a POST does register.
	if rec := postJSON(mux, "/auth/register", registerAllowedOrigin, body); rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := calls(); len(got) != 1 {
		t.Errorf("GoTrue saw %v after one POST, want one call", got)
	}
}

// mailLinkMux mounts one real mail-link handler behind the CORS allow-list on both patterns, as main does.
func mailLinkMux(t *testing.T, path string, pick func(registration) http.Handler) (*http.ServeMux, func() []string) {
	t.Helper()
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil, noPendingInvite)
	withCORS := gateway.CORS([]string{registerAllowedOrigin})
	mux := http.NewServeMux()
	mux.Handle("POST "+path, withCORS(pick(reg)))
	mux.Handle("OPTIONS "+path, withCORS(pick(reg)))
	return mux, calls
}

func TestResendVerificationPreflightGrantsTheAllowedOrigin(t *testing.T) {
	const path = "/auth/resend-verification"
	mux, calls := mailLinkMux(t, path, func(r registration) http.Handler { return r.ResendVerification })

	rec := preflight(mux, path, registerAllowedOrigin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight from the allowed origin = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("preflight Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Errorf("preflight Access-Control-Allow-Methods = %q, want POST granted", got)
	}

	for _, origin := range []string{"https://evil.example", "http://landing.example", registerAllowedOrigin + ".evil.example", "null"} {
		rec = preflight(mux, path, origin)
		for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
			if got := rec.Header().Get(h); got != "" {
				t.Errorf("preflight from %q carries %s = %q, want none", origin, h, got)
			}
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("a preflight reached GoTrue: %v", got)
	}

	// Positive pair: the POST itself is answered, with the grant, and reaches GoTrue once.
	rec = postJSON(mux, path, registerAllowedOrigin, `{"email":"new@corp.example"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST from the allowed origin = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("POST Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}
	if got := calls(); !slices.Equal(got, []string{"POST /resend"}) {
		t.Errorf("GoTrue saw %v after one POST, want one /resend", got)
	}
}

// An OPTIONS without Origin is no preflight: it reaches the handler, which refuses it.
func TestResendVerificationOptionsWithoutOriginIsNotAResend(t *testing.T) {
	mux, calls := mailLinkMux(t, "/auth/resend-verification", func(r registration) http.Handler { return r.ResendVerification })
	const body = `{"email":"new@corp.example"}`

	req := httptest.NewRequest(http.MethodOptions, "/auth/resend-verification", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("OPTIONS with no Origin = %d Allow %q, want 405 Allow POST: %s", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("an OPTIONS request reached GoTrue: %v", got)
	}

	// Positive pair: the same body as a POST does reach GoTrue.
	if rec := postJSON(mux, "/auth/resend-verification", registerAllowedOrigin, body); rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := calls(); len(got) != 1 {
		t.Errorf("GoTrue saw %v after one POST, want one call", got)
	}
}

func TestRequestPasswordResetPreflightGrantsTheAllowedOrigin(t *testing.T) {
	const path = "/auth/request-password-reset"
	mux, calls := mailLinkMux(t, path, func(r registration) http.Handler { return r.RequestPasswordReset })

	rec := preflight(mux, path, registerAllowedOrigin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight from the allowed origin = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("preflight Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Errorf("preflight Access-Control-Allow-Methods = %q, want POST granted", got)
	}

	for _, origin := range []string{"https://evil.example", "http://landing.example", registerAllowedOrigin + ".evil.example", "null"} {
		rec = preflight(mux, path, origin)
		for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
			if got := rec.Header().Get(h); got != "" {
				t.Errorf("preflight from %q carries %s = %q, want none", origin, h, got)
			}
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("a preflight reached GoTrue: %v", got)
	}

	// Positive pair: the POST itself is answered, with the grant, and reaches GoTrue once.
	rec = postJSON(mux, path, registerAllowedOrigin, `{"email":"ada@corp.example"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST from the allowed origin = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("POST Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}
	if got := calls(); !slices.Equal(got, []string{"POST /recover"}) {
		t.Errorf("GoTrue saw %v after one POST, want one /recover", got)
	}
}

// An OPTIONS without Origin is no preflight: it reaches the handler, which refuses it.
func TestRequestPasswordResetOptionsWithoutOriginIsNotARequest(t *testing.T) {
	const path = "/auth/request-password-reset"
	mux, calls := mailLinkMux(t, path, func(r registration) http.Handler { return r.RequestPasswordReset })
	const body = `{"email":"ada@corp.example"}`

	req := httptest.NewRequest(http.MethodOptions, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("OPTIONS with no Origin = %d Allow %q, want 405 Allow POST: %s", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("an OPTIONS request reached GoTrue: %v", got)
	}

	// Positive pair: the same body as a POST does reach GoTrue.
	if rec := postJSON(mux, path, registerAllowedOrigin, body); rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := calls(); !slices.Equal(got, []string{"POST /recover"}) {
		t.Errorf("GoTrue saw %v after one POST, want one /recover", got)
	}
}

// demoRecSink records every demo request it is handed.
type demoRecSink struct {
	mu  sync.Mutex
	got []gateway.DemoRequest
}

func (s *demoRecSink) Registrant(context.Context, gateway.RegistrantContact) error { return nil }

func (s *demoRecSink) DemoRequest(_ context.Context, d gateway.DemoRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, d)
	return nil
}

func (s *demoRecSink) calls() []gateway.DemoRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gateway.DemoRequest(nil), s.got...)
}

// demoMux mounts the real demo handler behind the CORS allow-list on both patterns, as main does.
func demoMux(t *testing.T) (*http.ServeMux, *demoRecSink) {
	t.Helper()
	authURL, _ := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	sink := &demoRecSink{}
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), sink, noPendingInvite)
	withCORS := gateway.CORS([]string{registerAllowedOrigin})
	mux := http.NewServeMux()
	mux.Handle("POST /contacts/demo-request", withCORS(reg.DemoRequest))
	mux.Handle("OPTIONS /contacts/demo-request", withCORS(reg.DemoRequest))
	return mux, sink
}

func TestDemoRequest_PreflightFromLanding(t *testing.T) {
	mux, sink := demoMux(t)
	const path = "/contacts/demo-request"

	rec := preflight(mux, path, registerAllowedOrigin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight from the allowed origin = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("preflight Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}
	if !allowHeaderSet(rec.Header())["content-type"] {
		t.Errorf("preflight grants %v, want content-type for the JSON body", allowHeaderSet(rec.Header()))
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Errorf("preflight Access-Control-Allow-Methods = %q, want POST granted", got)
	}

	rec = preflight(mux, path, "https://evil.example")
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight from a disallowed origin = %d, want 204", rec.Code)
	}
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers"} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("preflight from a disallowed origin carries %s = %q, want none", h, got)
		}
	}
	if got := sink.calls(); len(got) != 0 {
		t.Fatalf("a preflight reached the sink: %+v", got)
	}

	// The behind-CORS handler answers the real request, and the landing form reads a refusal too.
	const good = `{"email":" ada@corp.example ","name":"Ada Lovelace","company":"Analytical Engines Ltd","marketing_consent_text":"I agree."}`
	rec = postJSON(mux, path, registerAllowedOrigin, good)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST from the allowed origin = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("202 Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}
	want := []gateway.DemoRequest{{Email: "ada@corp.example", Name: "Ada Lovelace", Company: "Analytical Engines Ltd", MarketingConsentText: "I agree."}}
	if got := sink.calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sink got %+v, want %+v", got, want)
	}

	rec = postJSON(mux, path, registerAllowedOrigin, `{"email":"ada@corp.example","name":"","company":"Analytical Engines Ltd"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST with a blank name = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != registerAllowedOrigin {
		t.Errorf("400 Access-Control-Allow-Origin = %q, want %q", got, registerAllowedOrigin)
	}

	rec = postJSON(mux, path, "https://evil.example", good)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("POST from a disallowed origin got grant %q, want none", got)
	}
}

// The landing's wire is read from its source: a key, path or method the handler would not take fails here.
func TestDemoRequest_AcceptsTheLandingWire(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(b)
	}
	client := read("../../frontend/landing/src/demoRequest.ts")
	consent := read("../../frontend/landing/src/components/MarketingConsent.tsx")

	sentence := regexp.MustCompile(`(?m)^export const MARKETING_CONSENT_TEXT = '([^'\\\n]+)'`).FindStringSubmatch(consent)
	if sentence == nil {
		t.Fatal("MarketingConsent.tsx: no exported single-quoted MARKETING_CONSENT_TEXT literal")
	}
	path := regexp.MustCompile("`\\$\\{base\\}(/[^`]+)`").FindStringSubmatch(client)
	method := regexp.MustCompile(`method: '([A-Z]+)'`).FindStringSubmatch(client)
	block := regexp.MustCompile(`(?s)\n    body: \{(.*?)\n    \},`).FindStringSubmatch(client)
	if path == nil || method == nil || block == nil {
		t.Fatalf("demoRequest.ts: cannot read the url, method and body literal (url %v, method %v, body %v)", path != nil, method != nil, block != nil)
	}
	if method[1] != http.MethodPost {
		t.Fatalf("the landing sends %s, the route takes POST", method[1])
	}

	values := map[string]string{"email": " ada@corp.example ", "name": " Ada Lovelace ", "company": "Analytical Engines Ltd", "marketing_consent_text": sentence[1]}
	body := map[string]string{}
	for _, m := range regexp.MustCompile(`\b([a-z_]+):`).FindAllStringSubmatch(block[1], -1) {
		v, known := values[m[1]]
		if !known {
			t.Fatalf("the landing sends key %q, which the handler does not read", m[1])
		}
		body[m[1]] = v
	}
	if len(body) != len(values) {
		t.Fatalf("the landing body carries %d keys, want the handler's %d: %v", len(body), len(values), body)
	}
	raw, _ := json.Marshal(body)

	mux, sink := demoMux(t)
	rec := postJSON(mux, path[1], registerAllowedOrigin, string(raw))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST %s with the landing body = %d, want 202: %s", path[1], rec.Code, rec.Body.String())
	}
	want := []gateway.DemoRequest{{Email: "ada@corp.example", Name: "Ada Lovelace", Company: "Analytical Engines Ltd", MarketingConsentText: sentence[1]}}
	if got := sink.calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sink got %+v, want %+v", got, want)
	}
}

// A field cap the landing sets above the handler's limit fails here: a value at the cap is taken, one past it is refused.
func TestDemoRequest_LandingFieldCapsMatchTheHandler(t *testing.T) {
	b, err := os.ReadFile("../../frontend/landing/src/components/demoForm.ts")
	if err != nil {
		t.Fatalf("read demoForm.ts: %v", err)
	}
	capOf := func(name string) int {
		m := regexp.MustCompile(`(?m)^export const ` + name + ` = (\d+)$`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("demoForm.ts: no exported %s numeric literal", name)
		}
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	nameCap, companyCap, emailCap := capOf("DEMO_NAME_MAX"), capOf("DEMO_COMPANY_MAX"), capOf("DEMO_EMAIL_MAX")

	mux, sink := demoMux(t)
	send := func(email, name, company string) int {
		raw, _ := json.Marshal(map[string]string{"email": email, "name": name, "company": company})
		return postJSON(mux, "/contacts/demo-request", registerAllowedOrigin, string(raw)).Code
	}
	const domain = "@corp.example"
	emailOf := func(n int) string { return strings.Repeat("a", n-len(domain)) + domain }

	for _, c := range []struct {
		field string
		with  func(n int) int
		cap   int
	}{
		{"name", func(n int) int { return send("ada@corp.example", strings.Repeat("Ω", n), "Acme") }, nameCap},
		{"company", func(n int) int { return send("ada@corp.example", "Ada", strings.Repeat("Ω", n)) }, companyCap},
		{"email", func(n int) int { return send(emailOf(n), "Ada", "Acme") }, emailCap},
	} {
		if got := c.with(c.cap); got != http.StatusAccepted {
			t.Errorf("%s at the landing cap (%d) = %d, want 202", c.field, c.cap, got)
		}
		if got := c.with(c.cap + 1); got != http.StatusBadRequest {
			t.Errorf("%s one past the landing cap (%d) = %d, want 400: the cap is not the handler's limit", c.field, c.cap+1, got)
		}
	}
	if got := len(sink.calls()); got != 3 {
		t.Errorf("sink saw %d requests, want the three at-cap ones", got)
	}
}

// An OPTIONS with no Origin is not a preflight: it reaches the handler, which must not take it for a demo request.
func TestDemoRequest_OptionsWithoutOriginIsNotADemoRequest(t *testing.T) {
	mux, sink := demoMux(t)
	const body = `{"email":"ada@corp.example","name":"Ada Lovelace","company":"Analytical Engines Ltd"}`

	req := httptest.NewRequest(http.MethodOptions, "/contacts/demo-request", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code < 400 {
		t.Errorf("OPTIONS with no Origin = %d, want a refusal: %s", rec.Code, rec.Body.String())
	}
	if got := sink.calls(); len(got) != 0 {
		t.Errorf("an OPTIONS request reached the sink: %+v", got)
	}

	// No other method reaches the handler either; the mux answers 405 for them.
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(m, "/contacts/demo-request", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /contacts/demo-request = %d, want 405", m, rec.Code)
		}
	}
	if got := sink.calls(); len(got) != 0 {
		t.Errorf("a non-POST request reached the sink: %+v", got)
	}

	// Positive pair: the same body as a POST is forwarded.
	if rec := postJSON(mux, "/contacts/demo-request", registerAllowedOrigin, body); rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if got := sink.calls(); len(got) != 1 {
		t.Errorf("sink saw %+v after one POST, want one call", got)
	}
}
