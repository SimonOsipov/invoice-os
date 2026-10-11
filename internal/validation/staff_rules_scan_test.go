package validation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// A second Go caller of a staff SQL function could write rules with no staff_audit_log row:
// each function takes a caller-supplied actor and writes no audit row itself.
// It reads string literals only, so a comment naming the function is no caller.
func TestSource_StaffSQLFunctionsHaveOneCaller(t *testing.T) {
	root := repoRoot(t)
	owners := map[string]string{
		"set_rule_enabled":       "internal/validation/staff_rules.go",
		"rule_draft_open":        "internal/validation/staff_drafts.go",
		"rule_draft_put_rule":    "internal/validation/staff_drafts.go",
		"rule_draft_remove_rule": "internal/validation/staff_drafts.go",
		"rule_draft_publish":     "internal/validation/staff_drafts.go",
	}
	sawOwner := map[string]bool{}
	files := 0
	for _, dir := range []string{"cmd", "internal", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			files++
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				for fn, owner := range owners {
					if !strings.Contains(strings.ToLower(lit.Value), fn) {
						continue
					}
					if rel == owner {
						sawOwner[fn] = true
					} else {
						t.Errorf("%s names %s in a string: the function takes a caller-supplied actor and writes no audit row, so only %s may call it", rel, fn, owner)
					}
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
	for fn, owner := range owners {
		if !sawOwner[fn] {
			t.Errorf("the scan did not find %s in %s: it reads nothing", fn, owner)
		}
	}
}
