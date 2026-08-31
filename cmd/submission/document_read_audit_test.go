// document_read_audit_test.go: where the review screen's audit row is SPELLED, where its
// recorder is WIRED, and where the new route is DECLARED. All three are static: main() is not
// unit-testable (main_test.go:1-4), so nothing here serves a mux or opens a database.
//
// Helpers use a dr* prefix; ea wt ds are taken.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	drReadEvent   = "document.read"
	drAdapterFn   = "newDocumentReadAuditor"
	drExtractionD = "internal/extraction/"

	// ls internal/extraction/*.go | grep -v _test | wc -l -> 26 at 9ed1c501. A floor, not an
	// equality: the point is that the walk resolved a real directory, not that the package
	// stopped growing.
	drMinExtractionFiles = 26

	// A production string literal that IS under internal/extraction today (worker.go:28). The
	// absence proof below is worthless without it: a matcher that finds nothing anywhere
	// reports a clean extraction package too.
	drNeedleLiteral = "extraction-worker"

	drDocPath  = "docs/read-path-suspension.md"
	drDocRoute = "GET /v1/extractions/{id}"

	// The §8 table carries 65 registrations across 59 routes at 9ed1c501, so a parse that finds
	// fewer than 55 rows has lost the table, not found a shrinking one.
	drMinDocRows = 55

	// docs/read-path-suspension.md:320 reads "59 distinct routes, 65 registrations" before this
	// route lands. Floors, so EXTR-11-03's second route raises them rather than breaking this.
	drMinDocRoutes        = 60
	drMinDocRegistrations = 66
)

// drExtractionFiles narrows eaProdFiles' walk to internal/extraction and floors it.
func drExtractionFiles(t *testing.T, files []string) []string {
	t.Helper()
	var out []string
	for _, rel := range files {
		if strings.HasPrefix(rel, drExtractionD) {
			out = append(out, rel)
		}
	}
	if len(out) < drMinExtractionFiles {
		t.Fatalf("the walk found %d non-test .go file(s) under %s, want at least %d (26 measured) -- a scan over a path that stopped resolving reports all-clear",
			len(out), drExtractionD, drMinExtractionFiles)
	}
	return out
}

// AC 8's placement half. The event literal belongs in cmd/, never in internal/extraction: a
// const identifier inside the package reads as a non-literal to internal/platform/db's repo-wide
// audit.Record scan and lands the call site in no bucket (newExtractionAuditor's own rule,
// main.go:274-277).
func TestNewDocumentReadAuditor_SpellsTheEventInCmd(t *testing.T) {
	root, files := eaProdFiles(t)
	extractionFiles := drExtractionFiles(t, files)

	// Control needle: the literal matcher must find a literal that IS in internal/extraction,
	// or the zero-hit clearance below is a broken walk reporting an empty set.
	if got := eaLiteralSites(t, root, extractionFiles, drNeedleLiteral); len(got) != 1 {
		t.Fatalf("the literal matcher found %q at %v under %s, want exactly 1 site -- the matcher is broken, so the counts below answer a different question",
			drNeedleLiteral, got, drExtractionD)
	}

	sites := eaLiteralSites(t, root, files, drReadEvent)

	// Second control: internal/document/document.go:139 spells this exact string today. Without
	// this the whole test could pass on a matcher that never matches anything.
	var sawDocumentPkg bool
	for _, s := range sites {
		if strings.HasPrefix(s, "internal/document/") {
			sawDocumentPkg = true
		}
	}
	if !sawDocumentPkg {
		t.Fatalf("no %q literal found under internal/document (document.go:139 spells it) -- the matcher is broken; sites=%v", drReadEvent, sites)
	}

	var inCmd []string
	for _, s := range sites {
		if strings.HasPrefix(s, "cmd/submission/main.go:") {
			inCmd = append(inCmd, s)
		}
	}
	if len(inCmd) == 0 {
		t.Errorf("%q is spelled as a production literal at %v, none of them in cmd/submission/main.go -- %s is the composition root's job", drReadEvent, sites, drAdapterFn)
	}

	if got := eaLiteralSites(t, root, extractionFiles, drReadEvent); len(got) != 0 {
		t.Errorf("%q is spelled as a production literal under %s at %v -- a literal there drops the call site out of the repo-wide audit.Record partition",
			drReadEvent, drExtractionD, got)
	}
}

// AC 8's wiring half. Every audit assertion in internal/extraction injects its own recorder, so
// all of them stay green on a fleet that registered the route over a Reader carrying none. This
// is the only thing that says production audits anything.
//
// Field-name agnostic on purpose: it asks that SOME extraction.Reader literal is built with the
// adapter's return value, which survives the reader being hoisted into a local.
func TestSubmissionMain_WiresTheDocumentReadAuditorOntoAReader(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/submission/main.go: %v", err)
	}

	var readers, wired int
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Reader" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "extraction" {
			return true
		}
		readers++
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if call, ok := kv.Value.(*ast.CallExpr); ok && wtCallName(call.Fun) == drAdapterFn {
				wired++
			}
		}
		return true
	})

	// Control needle: an extraction.Reader literal already exists at main.go:163, so zero here
	// means the scan is broken rather than the wiring absent.
	if readers == 0 {
		t.Fatal("no extraction.Reader composite literal found in cmd/submission/main.go -- the scan is broken, so the assertion below is vacuous")
	}
	if wired == 0 {
		t.Errorf("none of the %d extraction.Reader literal(s) in cmd/submission/main.go carries a %s() call -- the detail route would then serve 200s and audit nothing, with every injected-recorder test still green",
			readers, drAdapterFn)
	}
}

// drDocSection returns the lines of docs/read-path-suspension.md's §8 endpoint table, bounded
// by its own heading the way internal/platform/db/seam_coverage_test.go:1034 bounds it.
func drDocSection(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(wtRepoRoot(t), filepath.FromSlash(drDocPath)))
	if err != nil {
		t.Fatalf("read %s: %v", drDocPath, err)
	}
	var (
		out []string
		in  bool
	)
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if in {
				break
			}
			in = strings.Contains(strings.ToLower(trimmed), "the endpoint table")
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s carries no section headed \"the endpoint table\" -- the parse below reads nothing", drDocPath)
	}
	return out
}

var drRowCell = regexp.MustCompile("^`([A-Z]+ /[^`]+)`$")

// AC 2. TestRLS_ReadPathSuspensionDocEnumeratesEveryRoute already errors for a registered route
// with no row -- but only once the route is registered, so it cannot say the doc is owed until
// the moment the fleet would ship it unclassified. This says it now.
func TestReadPathSuspensionDoc_DeclaresTheExtractionDetailRoute(t *testing.T) {
	lines := drDocSection(t)

	declared := map[string]string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		m := drRowCell.FindStringSubmatch(strings.TrimSpace(cells[0]))
		if m == nil {
			continue
		}
		declared[m[1]] = strings.TrimSpace(cells[2])
	}
	if len(declared) < drMinDocRows {
		t.Fatalf("%s's endpoint table parsed to %d row(s), want at least %d (59 at 9ed1c501) -- a parse that lost the table finds no missing row either",
			drDocPath, len(declared), drMinDocRows)
	}

	// Control needle: the collection route this one sits beside is declared today.
	if got, ok := declared["GET /v1/extractions"]; !ok || got != "covered" {
		t.Fatalf("the parse read `GET /v1/extractions` as verdict %q (present=%v), want covered -- the row parser is broken", got, ok)
	}

	verdict, ok := declared[drDocRoute]
	switch {
	case !ok:
		t.Errorf("%s declares no row for `%s` -- TestRLS_ReadPathSuspensionDocEnumeratesEveryRoute goes red the moment the route is registered without it", drDocPath, drDocRoute)
	case verdict != "covered":
		t.Errorf("%s declares `%s` with verdict %q, want exactly covered", drDocPath, drDocRoute, verdict)
	}

	drAssertCountLine(t, lines)
}

var drCountLine = regexp.MustCompile(`(\d+) distinct routes, (\d+) registrations`)

// The prose count is corrected for honesty, not for CI -- but an honest doc is the deliverable,
// so the floor moves with the table.
func drAssertCountLine(t *testing.T, lines []string) {
	t.Helper()

	var m []string
	for _, line := range lines {
		if got := drCountLine.FindStringSubmatch(line); got != nil {
			if m != nil {
				t.Fatalf("%s carries two route-count sentences; they can disagree", drDocPath)
			}
			m = got
		}
	}
	if m == nil {
		t.Fatalf("%s's endpoint section carries no \"N distinct routes, M registrations\" sentence -- the assertion below has nothing to read", drDocPath)
	}

	routes, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("route count %q is not a number: %v", m[1], err)
	}
	registrations, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatalf("registration count %q is not a number: %v", m[2], err)
	}
	if routes < drMinDocRoutes {
		t.Errorf("%s claims %d distinct routes, want at least %d -- the detail route raises it by one", drDocPath, routes, drMinDocRoutes)
	}
	if registrations < drMinDocRegistrations {
		t.Errorf("%s claims %d registrations, want at least %d -- the detail route raises it by one", drDocPath, registrations, drMinDocRegistrations)
	}
}
