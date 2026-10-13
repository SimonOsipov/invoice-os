package db_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// A staff_read_ function checks only the actor it is handed; WithinStaffTx is what refuses a non-staff caller.
// It reads string literals only, so a comment naming the function is no caller.
func TestRLS_StaffReadFunctionsAreCalledOnlyInsideWithinStaffTx(t *testing.T) {
	root := repoRootDir(t)
	const tenancyFile = "internal/tenancy/staff_tenants.go"
	files, found, tenancyInside := 0, 0, false
	for _, dir := range []string{"cmd", "internal", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			files++
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)

			type span struct{ pos, end token.Pos }
			var closures []span
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isWithinStaffTx(call.Fun, f.Name.Name) {
					return true
				}
				for _, a := range call.Args {
					if fl, ok := a.(*ast.FuncLit); ok {
						closures = append(closures, span{fl.Pos(), fl.End()})
					}
				}
				return true
			})
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || !strings.Contains(strings.ToLower(lit.Value), "staff_read_") {
					return true
				}
				found++
				inside := false
				for _, c := range closures {
					if lit.Pos() >= c.pos && lit.End() <= c.end {
						inside = true
					}
				}
				switch {
				case !inside:
					t.Errorf("%s:%d names a staff_read_ function outside a WithinStaffTx closure: the function trusts the actor it is passed, so only the staff transaction may call it", rel, fset.Position(lit.Pos()).Line)
				case rel == tenancyFile:
					tenancyInside = true
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 100 {
		t.Errorf("the scan parsed %d non-test Go files, want at least 100: it walks the wrong root", files)
	}
	if found < 1 {
		t.Error("the scan found no staff_read_ literal: it reads nothing")
	}
	if !tenancyInside {
		t.Errorf("the scan did not find a staff_read_ literal inside a WithinStaffTx closure in %s", tenancyFile)
	}
	t.Logf("files=%d literals=%d", files, found)
}

func isWithinStaffTx(fun ast.Expr, pkg string) bool {
	switch fn := fun.(type) {
	case *ast.SelectorExpr:
		x, ok := fn.X.(*ast.Ident)
		return ok && x.Name == "db" && fn.Sel.Name == "WithinStaffTx"
	case *ast.Ident:
		return pkg == "db" && fn.Name == "WithinStaffTx"
	}
	return false
}
