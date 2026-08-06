package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuleKeyHasExactlyOneConstructionSite (task-404 AC-1) is a regression
// guard, not a RED spec: RuleKey: has exactly one non-test construction site
// today, inside storeDuplicateRowError. The browser classifies an errors[]
// entry as already-imported purely by RuleKey's presence -- a second
// construction site would silently mislabel some other row as
// already-imported. Scan technique copied from filename_removed_test.go.
func TestRuleKeyHasExactlyOneConstructionSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/importer: %v", err)
	}

	const needle = "RuleKey:"
	var sites []string
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, needle) {
				sites = append(sites, fmt.Sprintf("%s:%d", name, i+1))
			}
		}
	}

	if scanned == 0 {
		t.Fatal("scanned 0 non-test .go files in internal/importer -- the walk is broken, so the count below passed vacuously")
	}
	if len(sites) != 1 {
		t.Errorf("found %d RuleKey: construction site(s) %v, want exactly 1 -- the browser classifies an errors[] entry as already-imported purely by RuleKey's presence, so a second site would silently mislabel a row", len(sites), sites)
	}
}
