// doc_test.go is the doc-sync gate for docs/ai-client.md (AC 1, AC 2). Every
// name the doc must carry is derived from a Go symbol or parsed from the
// source that defines it, then compared against the doc — never retyped as a
// literal, which would pass silently after a rename. Does NOT cover whether
// the doc's PROSE is true (retry rules, per-environment claims, secrets
// rule): no derivation exists for those, only for names.
//
// Named TestAIDoc* so ci.yml's -run alternation reaches it, matching the
// convention in internal/platform/db's doc gate tests.
package ai

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const aiDoc = "docs/ai-client.md"

// aiRepoRoot locates the worktree root: go test runs with cwd set to this
// package's directory. internal/platform/db and internal/tools/fleetgate
// each keep their own copy; this is the third.
func aiRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		t.Fatal("git reported an empty worktree root; every scan below would read nothing")
	}
	return root
}

// readFile Fatals naming the missing file, so a test calling it before
// docs/ai-client.md exists fails for the right reason, not a nil dereference.
func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func readAIDoc(t *testing.T, root string) string {
	t.Helper()
	return readFile(t, root, aiDoc)
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// D1: outcome literals in client.go + fake.go. Fatal unless exactly 5
// distinct, so a sixth outcome or a collapsed rename goes unnoticed.
var outcomeLiteralRE = regexp.MustCompile(`outcome(?::|\s*=)\s*"([a-z]+)"`)

func derivedOutcomes(t *testing.T, root string) []string {
	t.Helper()
	src := readFile(t, root, "internal/platform/ai/client.go") + readFile(t, root, "internal/platform/ai/fake.go")
	var raw []string
	for _, m := range outcomeLiteralRE.FindAllStringSubmatch(src, -1) {
		raw = append(raw, m[1])
	}
	distinct := dedupeSorted(raw)
	if len(distinct) != 5 {
		t.Fatalf("client.go+fake.go's outcome literals parse to %d distinct value(s) %v, want exactly 5", len(distinct), distinct)
	}
	return distinct
}

// D3: log.go's slog attrs, in source (== emitted) order. Fatal unless
// exactly 9, so an added or removed key goes unnoticed.
var logKeyRE = regexp.MustCompile(`slog\.[A-Z][A-Za-z0-9]*\("([a-z_]+)"`)

func derivedLogKeys(t *testing.T, root string) []string {
	t.Helper()
	src := readFile(t, root, "internal/platform/ai/log.go")
	var keys []string
	for _, m := range logKeyRE.FindAllStringSubmatch(src, -1) {
		keys = append(keys, m[1])
	}
	if len(keys) != 9 {
		t.Fatalf("log.go's slog attrs parse to %d key(s) %v, want exactly 9", len(keys), keys)
	}
	return keys
}

// D5: the backoff base and cap client.go's retry loop actually uses.
var backoffRE = regexp.MustCompile(`min\((\d+)\*time\.Millisecond<<\(attempt-1\), (\d+)\*time\.Second\)`)

func derivedBackoff(t *testing.T, root string) (base, backoffCap string) {
	t.Helper()
	src := readFile(t, root, "internal/platform/ai/client.go")
	m := backoffRE.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("client.go has no backoff expression matching %s", backoffRE.String())
	}
	return m[1] + "ms", m[2] + "s"
}

// D6: the shell subcommand that dispatches to cmd_set_ai_fake.
var subcommandRE = regexp.MustCompile(`(?m)^\s*([a-z][a-z0-9-]+)\)\s+cmd_set_ai_fake\b`)

func derivedSubcommand(t *testing.T, root string) string {
	t.Helper()
	src := readFile(t, root, "scripts/ci/railway-env.sh")
	m := subcommandRE.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("railway-env.sh has no subcommand matching %s", subcommandRE.String())
	}
	return m[1]
}

// firstGroup collects every regex group-1 match. Fatal on zero: a table-shape
// change that reads no rows must not read as agreement.
func firstGroup(t *testing.T, re *regexp.Regexp, src, what string) []string {
	t.Helper()
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s lists no %s — the table shape changed and this scan reads nothing", aiDoc, what)
	}
	return out
}

// docSection returns doc's text between heading (exclusive) and the next
// occurrence of stop (exclusive). Both boundaries are mandatory: a missing
// one Fatals instead of silently reading past it into a neighboring table.
func docSection(t *testing.T, doc, heading, stop string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("%s has no %q heading", aiDoc, heading)
	}
	body := doc[i+1+len(heading):]
	j := strings.Index(body, "\n"+stop)
	if j < 0 {
		t.Fatalf("%s has no %q heading after %q", aiDoc, stop, heading)
	}
	return body[:j]
}

// backtickedRowRE matches a markdown table row whose first cell is a
// backticked lowercase/underscore token — the shape both the log-key table
// and the outcome table use.
var backtickedRowRE = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|")

var tableSeparatorRE = regexp.MustCompile(`(?m)^\|[-:| ]+\|\s*$`)

// tableBodyRows returns the cells of the first markdown table's body rows in
// sec (header and separator skipped), stopping at the first blank or
// non-table line.
func tableBodyRows(sec string) [][]string {
	loc := tableSeparatorRE.FindStringIndex(sec)
	if loc == nil {
		return nil
	}
	// loc[1] lands on the separator line's own trailing newline, since `$` in
	// multiline mode is zero-width: strip it or every split starts with "".
	body := strings.TrimPrefix(sec[loc[1]:], "\n")
	var rows [][]string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, cells)
	}
	return rows
}

// Pure fixture test for docSection's happy path: proves the slice is exactly
// the text between the two headings, neither swallowing nor dropping a
// boundary character.
func TestDocSection_SlicesBetweenTwoHeadings(t *testing.T) {
	doc := "# Title\n\n## A\nbody-of-a\n\n## B\nbody-of-b\n"
	if got, want := docSection(t, doc, "## A", "## B"), "\nbody-of-a\n"; got != want {
		t.Fatalf("docSection(doc, %q, %q) = %q, want %q", "## A", "## B", got, want)
	}
}

// Pure fixture test: proves tableBodyRows reads real rows, not just skips
// them all (the exact way a trailing-newline slip in the separator match
// once made it return nil unconditionally).
func TestTableBodyRows_ParsesBodyRowsAfterTheSeparator(t *testing.T) {
	fixture := "| Marker in the input | Fake answer |\n|---|---|\n| `foo` | one |\n| `bar` | two |\n"
	want := [][]string{{"`foo`", "one"}, {"`bar`", "two"}}
	if got := tableBodyRows(fixture); !reflect.DeepEqual(got, want) {
		t.Fatalf("tableBodyRows(fixture) = %v, want %v", got, want)
	}
	if got := tableBodyRows("no separator line here, just prose"); got != nil {
		t.Fatalf("tableBodyRows(no separator) = %v, want nil", got)
	}
}

// missingNames reports which of want are absent from doc as a backticked
// token. Pure and file-free: T06 is the direct proof it can fail at all.
func missingNames(doc string, want []string) []string {
	var missing []string
	for _, name := range want {
		if !strings.Contains(doc, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	return missing
}

func TestAIDoc_NamesEveryVariableMarkerAndOutcome(t *testing.T) {
	root := aiRepoRoot(t)
	doc := readAIDoc(t, root)
	if strings.TrimSpace(doc) == "" {
		t.Fatal("docs/ai-client.md is empty")
	}

	outcomes := derivedOutcomes(t, root)
	subcommand := derivedSubcommand(t, root)
	want := append([]string{EnvKey, EnvFake, Model, markerUnavailable, markerAnswerPrefix, subcommand}, outcomes...)

	if missing := missingNames(doc, want); len(missing) > 0 {
		t.Errorf("%s does not name %v as backticked tokens", aiDoc, missing)
	}

	markerSection := docSection(t, doc, "## Fake mode markers", "## ")
	rows := tableBodyRows(markerSection)
	if len(rows) != 3 {
		t.Fatalf("%s's fake-mode-marker table has %d body row(s) %v, want exactly 3", aiDoc, len(rows), rows)
	}
	blank := 0
	for _, r := range rows {
		if len(r) == 0 || strings.Contains(r[0], "AIFAKE-") {
			continue
		}
		blank++
		if !strings.Contains(strings.ToLower(strings.Join(r, " ")), "blank") {
			t.Errorf("%s's no-marker row does not say %q: %v", aiDoc, "blank", r)
		}
	}
	if blank != 1 {
		t.Errorf("%s's fake-mode-marker table has %d row(s) with no AIFAKE- in the first cell, want exactly 1", aiDoc, blank)
	}
}

func TestAIDoc_NoToolLocalReadme(t *testing.T) {
	root := aiRepoRoot(t)
	readAIDoc(t, root) // the doc belongs at docs/ai-client.md, not the package dir

	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash("internal/platform/ai")))
	if err != nil {
		t.Fatalf("read internal/platform/ai: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("internal/platform/ai has zero entries — os.ReadDir read nothing")
	}
	for _, e := range entries {
		if strings.ToLower(e.Name()) == "readme.md" {
			t.Errorf("internal/platform/ai contains %s — the doc belongs at %s, not a tool-local README", e.Name(), aiDoc)
		}
	}
}

func TestAIDoc_OutcomeTableMatchesTheCode(t *testing.T) {
	root := aiRepoRoot(t)
	doc := readAIDoc(t, root)

	wantOutcomes := derivedOutcomes(t, root)
	outcomeSection := docSection(t, doc, "### Outcomes", "## ")
	gotOutcomes := dedupeSorted(firstGroup(t, backtickedRowRE, outcomeSection, "outcome table"))

	if !reflect.DeepEqual(wantOutcomes, gotOutcomes) {
		t.Errorf("%s's Outcomes table lists %v, code emits %v", aiDoc, gotOutcomes, wantOutcomes)
	}

	// Slicing control: if the heading-based slice failed to separate the two
	// tables, both scans would read the same combined row set.
	keySection := docSection(t, doc, "## The log line", "### Outcomes")
	gotKeys := dedupeSorted(firstGroup(t, backtickedRowRE, keySection, "log key table"))
	if reflect.DeepEqual(gotOutcomes, gotKeys) {
		t.Fatal("the Outcomes and log-key tables parsed identically — the heading slice is not distinguishing the two tables")
	}
}

func TestAIDoc_LogKeyTableMatchesTheLogger(t *testing.T) {
	root := aiRepoRoot(t)
	doc := readAIDoc(t, root)

	wantKeys := derivedLogKeys(t, root)
	keySection := docSection(t, doc, "## The log line", "### Outcomes")
	gotKeys := firstGroup(t, backtickedRowRE, keySection, "log key table")

	if len(gotKeys) != 9 {
		t.Fatalf("%s's log-key table has %d row(s) %v, want exactly 9", aiDoc, len(gotKeys), gotKeys)
	}
	if !reflect.DeepEqual(wantKeys, gotKeys) {
		t.Errorf("%s's log-key table order is %v, log.go emits %v — order is emission order", aiDoc, gotKeys, wantKeys)
	}
}

func TestAIDoc_StatesTheBudgetAndBackoff(t *testing.T) {
	root := aiRepoRoot(t)
	doc := readAIDoc(t, root)

	base, backoffCap := derivedBackoff(t, root)
	for _, want := range []string{budget.String(), base, backoffCap} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s does not state %q", aiDoc, want)
		}
	}
}

func TestAIDocMissingNames_FindsAPlantedOmission(t *testing.T) {
	want := []string{"alpha", "beta", "gamma"}
	fixture := "the doc names `alpha` and `gamma` but not the third"

	if got := missingNames(fixture, want); !reflect.DeepEqual(got, []string{"beta"}) {
		t.Fatalf("missingNames(fixture, want) = %v, want [beta]", got)
	}

	complete := "the doc names `alpha`, `beta` and `gamma`"
	if got := missingNames(complete, want); len(got) != 0 {
		t.Fatalf("missingNames(complete, want) = %v, want empty — the control fixture has no omission", got)
	}
}
