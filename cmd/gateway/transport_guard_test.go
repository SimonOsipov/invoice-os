package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Every gateway client transport rides ResetRetryTransport, so a reset upstream
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
	sites, clients := 0, 0
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
			// A client literal without Transport falls back to http.DefaultTransport, no retry.
			if lit, ok := n.(*ast.CompositeLit); ok && isHTTPClient(lit.Type) {
				clients++
				if !hasTransport(lit) {
					t.Errorf("%s: http.Client has no Transport; wire platform.ResetRetryTransport(nil)", fset.Position(lit.Pos()))
				}
				return true
			}
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
	// Today's counts; lower means the scan stopped finding the gateway's clients.
	if sites < minTransportSites || clients < minClientLiterals {
		t.Fatalf("scan found %d transport calls and %d http.Client literals, want at least %d and %d",
			sites, clients, minTransportSites, minClientLiterals)
	}
}

const (
	minTransportSites = 17
	minClientLiterals = 16
)

func isHTTPClient(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Client" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "http"
}

func hasTransport(lit *ast.CompositeLit) bool {
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Transport" {
				return true
			}
		}
	}
	return false
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
