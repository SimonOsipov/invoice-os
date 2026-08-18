// The doc-side half of AC-2: docs/demo-reset.md's two tables must list exactly
// what demopurge.go purges and spares, in the same order. Nothing else compared
// them, so a table added to purgeTables left the operator page silently wrong.
//
// Named TestPurge* so ci.yml's -run alternation reaches it
// (TestCIRunFiltersReachEveryTestInThePackage). No database.
package db_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const demoResetDoc = "docs/demo-reset.md"

var (
	purgedRowRE   = regexp.MustCompile("(?m)^\\| [0-9]+ \\| `([a-z_]+)` \\|")
	sparedRowRE   = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|")
	goListEntryRE = regexp.MustCompile("(?m)^\\t(?:\"([a-z_]+)\"|([A-Za-z][A-Za-z0-9_]*)),$")
)

// goStringList returns the entries of a `var <name> = []string{...}` block,
// resolving a bare identifier through consts.
func goStringList(t *testing.T, src, name string, consts map[string]string) []string {
	t.Helper()
	head := "var " + name + " = []string{"
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("demopurge.go declares no %s — this test can no longer read the list it compares against", name)
	}
	body := src[i+len(head):]
	end := strings.Index(body, "\n}")
	if end < 0 {
		t.Fatalf("%s has no closing brace", name)
	}
	var out []string
	for _, m := range goListEntryRE.FindAllStringSubmatch(body[:end], -1) {
		if m[1] != "" {
			out = append(out, m[1])
			continue
		}
		v, ok := consts[m[2]]
		if !ok {
			t.Fatalf("%s names %s, which this test cannot resolve to a table name", name, m[2])
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		t.Fatalf("%s parsed to zero entries — every comparison below would pass having read nothing", name)
	}
	return out
}

func firstGroup(t *testing.T, re *regexp.Regexp, src, what string) []string {
	t.Helper()
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s lists no %s — the doc's table shape changed and this scan reads nothing", demoResetDoc, what)
	}
	return out
}

func TestPurgeDemoResetDocTablesMatchTheCode(t *testing.T) {
	root := repoRootDir(t)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}

	code := read("internal/platform/db/demopurge.go")
	consts := map[string]string{}
	for _, m := range regexp.MustCompile("(?m)^\\s*(?:const\\s+)?([A-Za-z][A-Za-z0-9_]*)\\s*=\\s*\"([a-z_]+)\"").FindAllStringSubmatch(code, -1) {
		consts[m[1]] = m[2]
	}

	doc := read(demoResetDoc)
	spared := doc[strings.Index(doc, "## What it leaves standing"):]

	// Control: the scan must be able to report a mismatch, not only agreement.
	if reflect.DeepEqual(firstGroup(t, purgedRowRE, doc, "purged table"), firstGroup(t, sparedRowRE, spared, "spared table")) {
		t.Fatal("the purged and spared tables parsed identically — the row patterns are not distinguishing the two tables")
	}

	for _, c := range []struct {
		list, what string
		re         *regexp.Regexp
		src        string
	}{
		{"purgeTables", "purged table", purgedRowRE, doc},
		{"purgeExcludedTables", "spared table", sparedRowRE, spared},
	} {
		want := goStringList(t, code, c.list, consts)
		got := firstGroup(t, c.re, c.src, c.what)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s's %s list is %v, want %v (demopurge.go's %s, same order) — the operator page is what a human reads before a deploy",
				demoResetDoc, c.what, got, want, c.list)
		}
	}
}
