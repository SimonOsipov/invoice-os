package validation

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A second Go caller of the SQL function could flip a rule with no staff_audit_log row:
// the function takes a caller-supplied actor and writes no audit row itself.
func TestSource_SetRuleEnabledHasOneCaller(t *testing.T) {
	root := repoRoot(t)
	const owner = "internal/validation/staff_rules.go"
	var sawOwner bool
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if !strings.Contains(string(b), "set_rule_enabled") {
				return nil
			}
			if rel == owner {
				sawOwner = true
				return nil
			}
			t.Errorf("%s mentions set_rule_enabled: the function takes a caller-supplied actor and writes no audit row, so only %s may call it", rel, owner)
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
