package main

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strconv"
	"strings"
	"testing"
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

	// The seam: reg := registrationHandlers(probed["auth"], ...) as a top-level statement.
	recv := ""
	for _, s := range stmts {
		as, ok := s.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || types.ExprString(call.Fun) != "registrationHandlers" {
			continue
		}
		if len(call.Args) == 0 || types.ExprString(call.Args[0]) != `probed["auth"]` {
			t.Errorf("registrationHandlers base = %v, want probed[\"auth\"]", call.Args)
		}
		recv = types.ExprString(as.Lhs[0])
	}
	if recv == "" {
		t.Fatal("main has no top-level `x := registrationHandlers(probed[\"auth\"], ...)`")
	}

	for pattern, field := range map[string]string{"POST /auth/register": "Register", "GET /auth/verify": "Verify"} {
		s := sitesFor(sites, pattern)
		if len(s) != 1 {
			t.Errorf("%s is registered %d times, want exactly once", pattern, len(s))
			continue
		}
		if !s[0].topLevel {
			t.Errorf("%s is registered under a condition; it must be a top-level statement of main", pattern)
		}
		if want := recv + "." + field; s[0].handler != want {
			t.Errorf("%s handler = %s, want %s", pattern, s[0].handler, want)
		}
	}
}
