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

// A second Go caller of the SQL function could flip a rule with no staff_audit_log row:
// the function takes a caller-supplied actor and writes no audit row itself.
// It reads string literals only, so a comment naming the function is no caller.
func TestSource_SetRuleEnabledHasOneCaller(t *testing.T) {
	root := repoRoot(t)
	const owner = "internal/validation/staff_rules.go"
	var sawOwner bool
	for _, dir := range []string{"cmd", "internal", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			var found bool
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING &&
					strings.Contains(strings.ToLower(lit.Value), "set_rule_enabled") {
					found = true
				}
				return !found
			})
			if !found {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			if rel = filepath.ToSlash(rel); rel == owner {
				sawOwner = true
				return nil
			}
			t.Errorf("%s names set_rule_enabled in a string: the function takes a caller-supplied actor and writes no audit row, so only %s may call it", rel, owner)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !sawOwner {
		t.Fatalf("the scan did not find set_rule_enabled in %s: it reads nothing", owner)
	}
}
