// The doc-side oracles for docs/demo-reset.md. Its two tables must list exactly
// what demopurge.go purges and spares, in the same order (AC-2), and it must
// still carry the operator facts an operator acts on (AC-1, AC-3, AC-4).
// Nothing else compared them, so a table added to purgeTables left the operator
// page silently wrong and a deleted paragraph left no trace at all.
//
// Named TestPurge* so ci.yml's -run alternation reaches it
// (TestCIRunFiltersReachEveryTestInThePackage). No database.
package db_test

import (
	"fmt"
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

// AC-1, AC-3 and AC-4: the operator facts the page exists to carry. Presence of
// the load-bearing claim, not its prose — every needle below is a short phrase
// the fact cannot be stated without. mustNot is a keyword filter: it catches the
// known ways of writing a residual up as fixed, not every way. The positive
// needles are what carry the assertion.
var demoResetDocFacts = []struct {
	ac      string
	heading string
	must    []string
	mustRE  []*regexp.Regexp
	mustNot []string
}{
	{
		ac:      "AC-1 operator checklist",
		heading: "## Operator checklist after a deploy",
		must:    []string{"gateway first", "invoice service"},
		mustRE:  []*regexp.Regexp{regexp.MustCompile(`(?i)\b(one|1)\s+minute\b`)},
	},
	{
		ac:      "AC-4 gateway-only residuals",
		heading: "### Why a gateway-only restart is not enough",
		must:    []string{"source_document_id", "backlog is unarmed", "unbounded", "until a human"},
		mustNot: []string{"automatically repaired", "repairs itself", "self-heal", "automatically recovers"},
	},
	{
		ac:      "AC-4 regenerated ids",
		heading: "## Demo ids are not stable across a deploy",
		must:    []string{"new uuid on every deploy"},
	},
	{
		ac:      "AC-3 audit_log purge count",
		heading: "## Reading the `audit_log` purge count",
		must:    []string{"no seeded baseline", "since the last purge"},
	},
}

// docBody returns one heading's own paragraphs, stopping at the next heading of
// any level so a subsection cannot satisfy its parent's assertions.
func docBody(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("%s has no %q section — the operator facts it carries would go unasserted", demoResetDoc, heading)
	}
	body := doc[i+1+len(heading):]
	if j := strings.Index(body, "\n#"); j >= 0 {
		body = body[:j]
	}
	return body
}

// Needles are matched on the FLOWED section — markdown wraps mid-phrase, so a
// raw match reds on a rewrap rather than on a missing fact.
func docFactFaults(sec string, must []string, mustRE []*regexp.Regexp, mustNot []string) []string {
	flow := strings.Join(strings.Fields(sec), " ")
	low := strings.ToLower(flow)
	var faults []string
	for _, m := range must {
		if !strings.Contains(low, strings.ToLower(m)) {
			faults = append(faults, fmt.Sprintf("states no %q", m))
		}
	}
	for _, re := range mustRE {
		if !re.MatchString(flow) {
			faults = append(faults, "matches no "+re.String())
		}
	}
	for _, m := range mustNot {
		if strings.Contains(low, strings.ToLower(m)) {
			faults = append(faults, fmt.Sprintf("claims %q — the residual is recorded as fixed", m))
		}
	}
	return faults
}

func TestPurgeDemoResetDocStatesTheOperatorFacts(t *testing.T) {
	root := repoRootDir(t)
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(demoResetDoc)))
	if err != nil {
		t.Fatalf("read %s: %v", demoResetDoc, err)
	}
	doc := string(b)

	// Control: the checker must be able to report a fault, not only agree.
	for _, f := range demoResetDocFacts {
		if faults := docFactFaults("", f.must, f.mustRE, nil); len(faults) == 0 {
			t.Fatalf("%s reports an empty section clean — it asserts nothing", f.ac)
		}
		if len(f.mustNot) == 0 {
			continue
		}
		if faults := docFactFaults(f.mustNot[0], nil, nil, f.mustNot); len(faults) == 0 {
			t.Fatalf("%s reports %q clean — its fixed-residual check asserts nothing", f.ac, f.mustNot[0])
		}
	}

	for _, f := range demoResetDocFacts {
		if faults := docFactFaults(docBody(t, doc, f.heading), f.must, f.mustRE, f.mustNot); len(faults) > 0 {
			t.Errorf("%s (%s %q): %s", f.ac, demoResetDoc, f.heading, strings.Join(faults, "; "))
		}
	}

	// The checklist by ORDINAL, not by first mention: the gateway purges and
	// re-seeds, then the invoice service's seeders write on top of that seed.
	// Comparing positions alone would pass on a step inserted between them.
	want := []string{"gateway", "invoice service"}
	steps := numberedSteps(docBody(t, doc, "## Operator checklist after a deploy"))
	if len(steps) != len(want) {
		t.Fatalf("%s's checklist has %d numbered steps, want %d — a restart is one step per service",
			demoResetDoc, len(steps), len(want))
	}
	for i, w := range want {
		if got := firstNamed(steps[i], want); got != w {
			t.Errorf("%s's checklist step %d names %q first, want %q — the gateway is restarted before the invoice service",
				demoResetDoc, i+1, got, w)
		}
	}
}

var stepMarkerRE = regexp.MustCompile(`(?m)^[0-9]+\. `)

// numberedSteps splits a section into its top-level numbered list items.
func numberedSteps(sec string) []string {
	marks := stepMarkerRE.FindAllStringIndex(sec, -1)
	out := make([]string, 0, len(marks))
	for i, m := range marks {
		end := len(sec)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		out = append(out, sec[m[1]:end])
	}
	return out
}

// firstNamed reports which of names appears earliest in step, "" for none.
func firstNamed(step string, names []string) string {
	low := strings.ToLower(step)
	first, at := "", -1
	for _, n := range names {
		if j := strings.Index(low, n); j >= 0 && (at < 0 || j < at) {
			first, at = n, j
		}
	}
	return first
}
