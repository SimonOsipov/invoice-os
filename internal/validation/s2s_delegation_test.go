package validation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// A hand-copied body behaves identically to the delegate, so no request can tell them apart; only the source can.
func TestS2SMiddleware_BodyIsOneCallToPlatformRequireToken(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "s2s.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "S2SMiddleware" && f.Recv == nil {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("s2s.go declares no func S2SMiddleware")
	}
	if len(fn.Body.List) != 1 {
		t.Fatalf("S2SMiddleware body has %d statements, want 1 (a return of platform.RequireToken)", len(fn.Body.List))
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		t.Fatalf("S2SMiddleware body is %T, want a single-value return", fn.Body.List[0])
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		t.Fatalf("S2SMiddleware returns %T, want a call to platform.RequireToken", ret.Results[0])
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("S2SMiddleware calls %T, want platform.RequireToken", call.Fun)
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "platform" || sel.Sel.Name != "RequireToken" {
		t.Fatalf("S2SMiddleware calls %v.%s, want platform.RequireToken", sel.X, sel.Sel.Name)
	}
	if len(call.Args) != 2 {
		t.Fatalf("RequireToken call has %d arguments, want 2", len(call.Args))
	}
	if id, ok := call.Args[0].(*ast.Ident); !ok || id.Name != "headerS2SToken" {
		t.Errorf("first argument = %v, want headerS2SToken", call.Args[0])
	}
	if id, ok := call.Args[1].(*ast.Ident); !ok || id.Name != "token" {
		t.Errorf("second argument = %v, want token", call.Args[1])
	}
}
