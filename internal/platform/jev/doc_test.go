// doc_test.go is the doc-sync gate for docs/typesafe-jev.md. Names come from a
// Go symbol, an AST read of the declaring function, or railway-env.sh's
// dispatch block, never a retyped literal. The prose is human-reviewed.
// Go and shell names are case-sensitive, so every scan matches exact case.
package jev

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const jevDoc = "docs/typesafe-jev.md"

// jevDocHeadings is the spec's section list, exact text.
var jevDocHeadings = []string{
	"## What it is",
	"## Env knobs",
	"## Per environment",
	"## Fake mode markers",
	"## Retries and the budget",
	"## The log line",
	"### Outcomes",
	"## A skipped check changes nothing on screen",
	"## Data terms",
	"## Known limitations",
	"## See also",
}

func jevRepoRoot(t *testing.T) string {
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

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func readJevDoc(t *testing.T, root string) string {
	t.Helper()
	doc := readRepoFile(t, root, jevDoc)
	if strings.TrimSpace(doc) == "" {
		t.Fatalf("%s is empty", jevDoc)
	}
	return doc
}

// parseFunc parses rel without comments and returns its one func named name.
func parseFunc(t *testing.T, root, rel, name string) (*token.FileSet, *ast.File, *ast.FuncDecl) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var found []*ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Body != nil {
			found = append(found, fn)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s declares %d func(s) named %s, want exactly 1", rel, len(found), name)
	}
	return fset, f, found[0]
}

// stringLit fails on a computed value: a derivation that skipped it would
// read as agreement.
func stringLit(t *testing.T, fset *token.FileSet, e ast.Expr) string {
	t.Helper()
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		t.Fatalf("%s: %T is not a string literal; the derivation reads literals only", fset.Position(e.Pos()), e)
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatalf("%s: unquote %s: %v", fset.Position(e.Pos()), lit.Value, err)
	}
	return s
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func sortedDistinct(in []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(in)))
}

// derivedOutcomes reads every outcome that call and fakeCall set: a
// result{outcome: ...} field, an x.outcome = ... assignment, or x.fail(err, ...).
func derivedOutcomes(t *testing.T, root string) []string {
	t.Helper()
	var raw []string
	for _, src := range []struct{ rel, fn string }{
		{"internal/platform/jev/client.go", "call"},
		{"internal/platform/jev/fake.go", "fakeCall"},
	} {
		fset, _, fn := parseFunc(t, root, src.rel, src.fn)
		before := len(raw)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				if isIdent(x.Type, "result") {
					for _, el := range x.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok && isIdent(kv.Key, "outcome") {
							raw = append(raw, stringLit(t, fset, kv.Value))
						}
					}
				}
			case *ast.AssignStmt:
				for i, l := range x.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "outcome" {
						if len(x.Rhs) != len(x.Lhs) {
							t.Fatalf("%s: outcome is set from a multi-value expression", fset.Position(x.Pos()))
						}
						raw = append(raw, stringLit(t, fset, x.Rhs[i]))
					}
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "fail" && len(x.Args) == 2 {
					raw = append(raw, stringLit(t, fset, x.Args[1]))
				}
			}
			return true
		})
		if len(raw) == before {
			t.Fatalf("%s's %s sets no outcome; the scan read the wrong declaration", src.rel, src.fn)
		}
	}
	distinct := sortedDistinct(raw)
	if len(distinct) != 5 {
		t.Fatalf("client.go's call and fake.go's fakeCall set %d distinct outcome(s) %v, want exactly 5", len(distinct), distinct)
	}
	return distinct
}

var slogAttrFuncs = map[string]bool{
	"Any": true, "Bool": true, "Duration": true, "Float64": true, "Group": true,
	"Int": true, "Int64": true, "String": true, "Time": true, "Uint64": true,
}

// derivedLogKeys reads logCall's slog attr keys in source order, which is
// emission order.
func derivedLogKeys(t *testing.T, root string) []string {
	t.Helper()
	const rel = "internal/platform/jev/log.go"
	fset, f, fn := parseFunc(t, root, rel, "logCall")
	slogName := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "log/slog" {
			slogName = "slog"
			if imp.Name != nil {
				slogName = imp.Name.Name
			}
		}
	}
	if slogName == "" {
		t.Fatalf("%s does not import log/slog", rel)
	}
	var keys []string
	logAttrs := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "LogAttrs" {
			logAttrs = true
		}
		if isIdent(sel.X, slogName) && slogAttrFuncs[sel.Sel.Name] && len(call.Args) > 0 {
			keys = append(keys, stringLit(t, fset, call.Args[0]))
		}
		return true
	})
	if !logAttrs {
		t.Fatalf("%s's logCall has no LogAttrs call; the scan read the wrong declaration", rel)
	}
	if len(keys) != 7 {
		t.Fatalf("%s's logCall builds %d slog attr(s) %v, want exactly 7", rel, len(keys), keys)
	}
	return keys
}

var (
	dispatchStartRE = regexp.MustCompile(`^case "\$\{1:-\}" in$`)
	dispatchArmRE   = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\)\s+(.*)$`)
	setAIFakeCallRE = regexp.MustCompile(`\bcmd_set_ai_fake\b`)
	setAIFakeDefRE  = regexp.MustCompile(`^cmd_set_ai_fake\(\) \{$`)
	shellCommentRE  = regexp.MustCompile(`(^|\s)#.*$`)
)

// derivedSubcommand returns the one label in railway-env.sh's top-level
// dispatch block whose arm calls cmd_set_ai_fake.
func derivedSubcommand(t *testing.T, root string) string {
	t.Helper()
	const rel = "scripts/ci/railway-env.sh"
	lines := strings.Split(readRepoFile(t, root, rel), "\n")
	start, defined := -1, false
	for i := range lines {
		lines[i] = strings.TrimRight(shellCommentRE.ReplaceAllString(lines[i], ""), " \t")
		if setAIFakeDefRE.MatchString(lines[i]) {
			defined = true
		}
		if dispatchStartRE.MatchString(lines[i]) {
			if start >= 0 {
				t.Fatalf("%s has two top-level dispatch blocks", rel)
			}
			start = i
		}
	}
	if !defined {
		t.Fatalf("%s defines no cmd_set_ai_fake(); the dispatch target is gone", rel)
	}
	if start < 0 {
		t.Fatalf("%s has no top-level %s", rel, dispatchStartRE)
	}
	var arms, hits []string
	closed := false
	for _, l := range lines[start+1:] {
		if l == "esac" {
			closed = true
			break
		}
		if m := dispatchArmRE.FindStringSubmatch(l); m != nil {
			arms = append(arms, m[1])
			if setAIFakeCallRE.MatchString(m[2]) {
				hits = append(hits, m[1])
			}
		}
	}
	if !closed {
		t.Fatalf("%s's dispatch block has no closing esac", rel)
	}
	if len(arms) < 10 {
		t.Fatalf("%s's dispatch block has %d arm(s) %v, want at least 10; the scan read the wrong block", rel, len(arms), arms)
	}
	if len(hits) != 1 {
		t.Fatalf("%s's dispatch block has %d arm(s) %v calling cmd_set_ai_fake, want exactly 1", rel, len(hits), hits)
	}
	return hits[0]
}

// sectionBetween returns doc's text between heading and the next stop, both
// exclusive. A missing boundary is an error, never a read into the next table.
func sectionBetween(doc, heading, stop string) (string, error) {
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		return "", fmt.Errorf("%s has no %q heading", jevDoc, heading)
	}
	body := doc[i+1+len(heading):]
	j := strings.Index(body, "\n"+stop)
	if j < 0 {
		return "", fmt.Errorf("%s has no %q heading after %q", jevDoc, stop, heading)
	}
	return body[:j], nil
}

func docSection(t *testing.T, doc, heading, stop string) string {
	t.Helper()
	sec, err := sectionBetween(doc, heading, stop)
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

var tableSeparatorRE = regexp.MustCompile(`(?m)^\|[-:| ]+\|\s*$`)

// tableBodyRows returns the cells of the first markdown table's body rows in
// sec, stopping at the first blank or non-table line.
func tableBodyRows(sec string) [][]string {
	loc := tableSeparatorRE.FindStringIndex(sec)
	if loc == nil {
		return nil
	}
	// The multiline `$` stops before the separator's newline; drop it.
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

var backtickedCellRE = regexp.MustCompile("^`([^`]+)`$")

// tableTokens returns each body row's backticked first cell. A row with any
// other first cell fails, so an extra prose row cannot hide.
func tableTokens(t *testing.T, sec, what string) []string {
	t.Helper()
	rows := tableBodyRows(sec)
	if len(rows) == 0 {
		t.Fatalf("%s's %s has no body rows; the table shape changed and this scan reads nothing", jevDoc, what)
	}
	tokens := make([]string, 0, len(rows))
	for _, r := range rows {
		m := backtickedCellRE.FindStringSubmatch(r[0])
		if m == nil {
			t.Fatalf("%s's %s row %v has no backticked token as its first cell", jevDoc, what, r)
		}
		tokens = append(tokens, m[1])
	}
	return tokens
}

// missingNames reports which of want are absent from doc as a backticked token.
func missingNames(doc string, want []string) []string {
	var missing []string
	for _, name := range want {
		if !strings.Contains(doc, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	return missing
}

// durationNeedleRE anchors a duration on both sides so 3s is not found inside 13s.
func durationNeedleRE(want string) *regexp.Regexp {
	return regexp.MustCompile(`(?:\A|[^0-9.])` + regexp.QuoteMeta(want) + `(?:[^0-9A-Za-z]|\z)`)
}

func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// markerTableErrors checks sec's first table: one row per marker in slice
// order, plus exactly one row with no marker that says "no doubt".
func markerTableErrors(sec string, markers []string) []string {
	var errs []string
	rows := tableBodyRows(sec)
	if len(rows) != len(markers)+1 {
		errs = append(errs, fmt.Sprintf("marker table has %d body row(s), want %d: one per fakeMarkers entry plus the no-marker row", len(rows), len(markers)+1))
	}
	prefix := commonPrefix(markers)
	var markerCells []string
	plain := 0
	for _, r := range rows {
		if strings.Contains(r[0], prefix) {
			markerCells = append(markerCells, r[0])
			continue
		}
		plain++
		if !strings.Contains(strings.ToLower(strings.Join(r, " ")), "no doubt") {
			errs = append(errs, fmt.Sprintf("no-marker row %v does not say %q", r, "no doubt"))
		}
	}
	if plain != 1 {
		errs = append(errs, fmt.Sprintf("marker table has %d row(s) with no %s in the first cell, want exactly 1", plain, prefix))
	}
	for i := range max(len(markers), len(markerCells)) {
		var want, cell string
		if i < len(markers) {
			want = markers[i]
		}
		if i < len(markerCells) {
			cell = markerCells[i]
		}
		var carried []string
		for _, m := range markers {
			if strings.Contains(cell, m) {
				carried = append(carried, m)
			}
		}
		if want == "" || len(carried) != 1 || carried[0] != want {
			errs = append(errs, fmt.Sprintf("marker row %d's first cell %q carries %v, want exactly %q in fakeMarkers order", i+1, cell, carried, want))
		}
	}
	return errs
}

func requireFourMarkers(t *testing.T) {
	t.Helper()
	if len(fakeMarkers) != 4 {
		t.Fatalf("fake.go's fakeMarkers holds %d marker(s) %v, want exactly 4", len(fakeMarkers), fakeMarkers)
	}
}

func TestJevDoc_HasEverySectionInOrder(t *testing.T) {
	root := jevRepoRoot(t)
	lines := strings.Split(readJevDoc(t, root), "\n")
	prev, prevHeading := -1, ""
	for _, h := range jevDocHeadings {
		var at []int
		for i, l := range lines {
			if l == h {
				at = append(at, i)
			}
		}
		if len(at) == 0 {
			t.Errorf("%s has no %q heading", jevDoc, h)
			continue
		}
		if len(at) > 1 {
			t.Errorf("%s has the %q heading %d times, want once", jevDoc, h, len(at))
		}
		if at[0] < prev {
			t.Errorf("%s puts %q before %q, want the spec order", jevDoc, h, prevHeading)
		}
		prev, prevHeading = at[0], h
	}
}

func TestJevDoc_NamesEveryVariableMarkerAndOutcome(t *testing.T) {
	root := jevRepoRoot(t)
	requireFourMarkers(t)
	want := []string{EnvKey, EnvFake, Model, derivedSubcommand(t, root)}
	want = append(want, fakeMarkers...)
	want = append(want, derivedOutcomes(t, root)...)
	if slices.Contains(want, "") {
		t.Fatalf("a derived name is empty: %q", want)
	}
	doc := readJevDoc(t, root)
	if missing := missingNames(doc, want); len(missing) > 0 {
		t.Errorf("%s does not name %v as backticked tokens", jevDoc, missing)
	}
}

func TestJevDoc_MarkerTableMatchesTheFake(t *testing.T) {
	root := jevRepoRoot(t)
	requireFourMarkers(t)
	doc := readJevDoc(t, root)
	for _, e := range markerTableErrors(docSection(t, doc, "## Fake mode markers", "## "), fakeMarkers) {
		t.Errorf("%s: %s", jevDoc, e)
	}
}

func TestJevDocMarkerRows_ADroppedOrReorderedRowFails(t *testing.T) {
	requireFourMarkers(t)
	const noDoubt = "| *(none)* | No doubt: every answer is the default |"
	build := func(plain string, markers []string, extra ...string) string {
		lines := []string{"# T", "", "## Fake mode markers", "", "| Marker in `State` | Fake result |", "|---|---|", plain}
		for _, m := range markers {
			lines = append(lines, "| `"+m+"` | steered |")
		}
		lines = append(append(lines, extra...), "", "## Next", "")
		return strings.Join(lines, "\n")
	}
	check := func(t *testing.T, doc string) []string {
		t.Helper()
		sec, err := sectionBetween(doc, "## Fake mode markers", "## ")
		if err != nil {
			t.Fatal(err)
		}
		return markerTableErrors(sec, fakeMarkers)
	}

	if errs := check(t, build(noDoubt, fakeMarkers)); len(errs) != 0 {
		t.Fatalf("the unmodified fixture reports %v, want no error", errs)
	}

	dropped := slices.DeleteFunc(slices.Clone(fakeMarkers), func(m string) bool { return m == markerRefused })
	swapped := slices.Clone(fakeMarkers)
	swapped[0], swapped[1] = swapped[1], swapped[0]
	duplicated := slices.Insert(slices.Clone(fakeMarkers), 1, fakeMarkers[0])

	// clause names the one check each fixture must trip.
	cases := []struct{ name, doc, clause string }{
		{"REFUSED row dropped", build(noDoubt, dropped), "body row"},
		{"two rows swapped", build(noDoubt, swapped), "in fakeMarkers order"},
		{"a marker row duplicated", build(noDoubt, duplicated), "body row"},
		{"an extra no-marker row", build(noDoubt, fakeMarkers, "| *(other)* | no doubt either |"), "with no"},
		{"the no-marker row lacks no doubt", build("| *(none)* | defaults |", fakeMarkers), "does not say"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := check(t, tc.doc)
			if len(errs) == 0 {
				t.Fatal("markerTableErrors reports nothing, want an error")
			}
			if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, tc.clause) }) {
				t.Errorf("markerTableErrors = %v, want one naming %q", errs, tc.clause)
			}
		})
	}
}

func TestJevDoc_LogKeyTableMatchesTheLogger(t *testing.T) {
	root := jevRepoRoot(t)
	want := derivedLogKeys(t, root)
	doc := readJevDoc(t, root)
	got := tableTokens(t, docSection(t, doc, "## The log line", "### Outcomes"), "log-key table")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s's log-key table lists %v, log.go's logCall emits %v in that order", jevDoc, got, want)
	}
}

func TestJevDoc_OutcomeTableMatchesTheCode(t *testing.T) {
	root := jevRepoRoot(t)
	want := derivedOutcomes(t, root)
	doc := readJevDoc(t, root)
	raw := tableTokens(t, docSection(t, doc, "### Outcomes", "## "), "Outcomes table")
	// Counted before the dedupe, so a duplicated row cannot collapse into agreement.
	if len(raw) != len(want) {
		t.Fatalf("%s's Outcomes table has %d row(s) %v, want exactly %d", jevDoc, len(raw), raw, len(want))
	}
	if got := sortedDistinct(raw); !reflect.DeepEqual(got, want) {
		t.Errorf("%s's Outcomes table lists %v, the code sets %v", jevDoc, got, want)
	}
	keys := tableTokens(t, docSection(t, doc, "## The log line", "### Outcomes"), "log-key table")
	if reflect.DeepEqual(raw, keys) {
		t.Fatal("the Outcomes and log-key tables parsed identically; the heading slice does not separate them")
	}
}

func TestJevDoc_StatesTheBudgetAndTheRetryWait(t *testing.T) {
	root := jevRepoRoot(t)
	doc := readJevDoc(t, root)
	sec := docSection(t, doc, "## Retries and the budget", "## ")
	for _, want := range []string{budget.String(), retryWait.String()} {
		if !durationNeedleRE(want).MatchString(sec) {
			t.Errorf("%s's %q section does not state %q as a value of its own", jevDoc, "## Retries and the budget", want)
		}
	}
}

func TestJevDocDurationNeedle_RejectsASuffixOfALongerNumber(t *testing.T) {
	rejects := []struct{ want, prose string }{
		{"3s", "a 13s cap"},
		{"250ms", "a 1250ms pause"},
		{"3s", "a 3sec nap"},
	}
	for _, tc := range rejects {
		if durationNeedleRE(tc.want).MatchString(tc.prose) {
			t.Errorf("durationNeedleRE(%q) matched %q, want no match", tc.want, tc.prose)
		}
	}
	const stated = "The budget is `3s`; the wait is 250ms."
	for _, want := range []string{"3s", "250ms"} {
		if !durationNeedleRE(want).MatchString(stated) {
			t.Errorf("durationNeedleRE(%q) did not match %q", want, stated)
		}
	}
}

func TestJevDoc_NoToolLocalReadme(t *testing.T) {
	root := jevRepoRoot(t)
	const dir = "internal/platform/jev"
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("%s has zero entries; os.ReadDir read nothing", dir)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	// Control: the scanned directory is the one holding this file.
	_, self, _, ok := runtime.Caller(0)
	if !ok || !strings.HasSuffix(filepath.ToSlash(filepath.Dir(self)), dir) || !slices.Contains(names, filepath.Base(self)) {
		t.Fatalf("%s does not list this test file %s; the scan read the wrong directory: %v", dir, self, names)
	}
	for _, n := range names {
		if strings.HasPrefix(strings.ToLower(n), "readme") {
			t.Errorf("%s holds %s; the doc belongs at %s", dir, n, jevDoc)
		}
	}
}

func TestJevDocSectionBetween_AMissingBoundaryIsAnError(t *testing.T) {
	doc := "# Title\n\n## A\nbody-of-a\n\n## B\nbody-of-b\n"
	if sec, err := sectionBetween(doc, "## A", "## B"); err != nil || sec != "\nbody-of-a\n" {
		t.Fatalf("sectionBetween(doc, %q, %q) = %q, %v; want %q, nil", "## A", "## B", sec, err, "\nbody-of-a\n")
	}
	cases := map[string]struct{ heading, stop string }{
		"heading absent": {"## Z", "## B"},
		"stop absent":    {"## A", "## Z"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sec, err := sectionBetween(doc, tc.heading, tc.stop)
			if err == nil {
				t.Fatalf("sectionBetween(doc, %q, %q) = %q, want an error", tc.heading, tc.stop, sec)
			}
			if sec != "" {
				t.Errorf("sectionBetween(doc, %q, %q) returned %q alongside its error, want empty", tc.heading, tc.stop, sec)
			}
		})
	}
}
