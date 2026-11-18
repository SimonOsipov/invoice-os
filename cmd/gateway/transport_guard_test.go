package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// BUG-150: every gateway client transport rides ResetRetryTransport, so a reset upstream
// connection on a bodyless read is retried instead of surfacing as a 502.
func TestGatewayTransportsAllCarryResetRetry(t *testing.T) {
	var files []string
	for _, dir := range []string{".", "../../internal/gateway"} {
		m, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	sites := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "IDTokenTransport" && sel.Sel.Name != "TraceTransport") {
				return true
			}
			sites++
			if !isResetRetryNil(call) {
				t.Errorf("%s: %s must wrap platform.ResetRetryTransport(nil)", fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if sites == 0 {
		t.Fatal("found no IDTokenTransport/TraceTransport calls; the guard scans nothing")
	}
}

func isResetRetryNil(call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}
	inner, ok := call.Args[0].(*ast.CallExpr)
	if !ok || len(inner.Args) != 1 {
		return false
	}
	sel, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "ResetRetryTransport" {
		return false
	}
	id, ok := inner.Args[0].(*ast.Ident)
	return ok && id.Name == "nil"
}
