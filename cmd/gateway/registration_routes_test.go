package main

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

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
	recv, corsLocal := "", false
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
			corsLocal = corsLocal || types.ExprString(as.Lhs[0]) == "withCORS"
		case "registrationHandlers":
			if len(call.Args) == 0 || types.ExprString(call.Args[0]) != `probed["auth"]` {
				t.Errorf("registrationHandlers base = %v, want probed[\"auth\"]", call.Args)
			}
			recv = types.ExprString(as.Lhs[0])
		}
	}
	if recv == "" {
		t.Fatal("main has no top-level `x := registrationHandlers(probed[\"auth\"], ...)`")
	}
	if !corsLocal {
		t.Fatal("main has no top-level `withCORS := gateway.CORS(...)`; the register wrap names nothing")
	}

	// Register is browser-called (landing form): CORS-wrapped with a preflight. Verify is a mailed GET link.
	for pattern, want := range map[string]string{
		"POST /auth/register":    "withCORS(" + recv + ".Register)",
		"OPTIONS /auth/register": "withCORS(" + recv + ".Register)",
		"GET /auth/verify":       recv + ".Verify",
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

const registerAllowedOrigin = "https://landing.example"

// registerMux mounts the real register handler behind the CORS allow-list on both patterns, as main does.
func registerMux(t *testing.T) (*http.ServeMux, func() []string) {
	t.Helper()
	authURL, calls := fakeAuth(t)
	site, _ := url.Parse("https://site.example")
	reg := registrationHandlers(authURL, site, 0, slog.New(slog.DiscardHandler), nil)
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
