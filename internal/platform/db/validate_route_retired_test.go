// RMV-01-03 — the single-document validate route is retired.
//
// POST /v1/validate/batch SURVIVES (Out of Scope). Two earlier passes on this
// story filed the batch registration as the offender, so the discrimination is
// asserted with a planted fixture, not left to the reader.
//
// Reuses Scan 3's AST walk (seam_coverage_test.go): same roots, same resolver.
// The TestRLS_ prefix is what makes it run in CI
// (TestCIRunFiltersReachEveryTestInThePackage).

package db_test

import (
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

const (
	vrRetiredRoute = "POST /v1/validate"
	vrBatchRoute   = "POST /v1/validate/batch"
	vrRulesRoute   = "PATCH /v1/rules/{key}"
)

// vrNeedleBothRoutes plants the retired route and its surviving longer sibling
// in one file. A prefix-shaped predicate reports two offenders here.
const vrNeedleBothRoutes = `package main

func main() {
	app.Mux.HandleFunc("POST /v1/validate", nil)
	app.Mux.Handle("POST /v1/validate/batch", nil)
}
`

// vrOffenders returns the registrations of the retired route. Exact match: the
// batch route is a different route, not a longer spelling of this one.
func vrOffenders(routes []scRoute) []scRoute {
	var out []scRoute
	for _, r := range routes {
		if r.route == vrRetiredRoute {
			out = append(out, r)
		}
	}
	return out
}

func TestRLS_SingleDocumentValidateRouteIsNotRegistered(t *testing.T) {
	t.Run("the scan tells the retired route from the surviving batch route", func(t *testing.T) {
		routes, unresolved := scFixtureRoutes(t, "vr.go", vrNeedleBothRoutes)
		if len(routes) != 2 || len(unresolved) != 0 {
			t.Fatalf("fixture resolved %v (%d unresolved), want both registrations — the control cannot discriminate what it never read", routes, len(unresolved))
		}
		var batch scRoute
		for _, r := range routes {
			if r.route == vrBatchRoute {
				batch = r
			}
		}
		if batch.route == "" {
			t.Fatalf("fixture routes %v carry no %q — the surviving sibling was never planted", routes, vrBatchRoute)
		}
		off := vrOffenders(routes)
		if len(off) != 1 || off[0].route != vrRetiredRoute {
			t.Fatalf("offenders = %v, want exactly the one %q registration", off, vrRetiredRoute)
		}
		if off[0].line == batch.line {
			t.Fatalf("the offender is the %q registration at line %d — a prefix match would retire a route this story fences Out of Scope", vrBatchRoute, batch.line)
		}
	})

	root := repoRootDir(t)
	roots := append(hmGoFiles(t, root, []string{"cmd"}), "internal/platform/server.go")
	sort.Strings(roots)

	fset := token.NewFileSet()
	consts := map[string]map[string]string{}
	var routes []scRoute
	var unresolved []string
	rootsWithRoutes := 0
	for _, rel := range roots {
		f := scParse(t, fset, root, rel)
		dir := filepath.Dir(rel)
		if consts[dir] == nil {
			consts[dir] = map[string]string{}
		}
		scStringConstsIn(f, consts[dir])
		r, u := scRoutesIn(fset, rel, f, consts[dir])
		routes = append(routes, r...)
		unresolved = append(unresolved, u...)
		if len(r) > 0 {
			rootsWithRoutes++
		}
	}
	if len(unresolved) > 0 {
		t.Fatalf("%d route argument(s) unresolved: %v — a route the scan cannot read is a route it cannot clear", len(unresolved), unresolved)
	}
	t.Logf("walk measured %d registration(s) across %d root file(s) with routes (%d roots walked)", len(routes), rootsWithRoutes, len(roots))

	// Floor. An absence assertion over an empty walk passes while examining
	// nothing. 69 across 9 measured at RMV-01-03; the margin absorbs this
	// story's own deletion.
	if rootsWithRoutes < 8 || len(routes) < 60 {
		t.Fatalf("found %d registration(s) across %d file(s), want at least 60 across at least 8 (68 across 9 measured at RMV-01-03) — a walk that reads nothing clears everything", len(routes), rootsWithRoutes)
	}

	registered := map[string]bool{}
	for _, r := range routes {
		registered[r.route] = true
	}
	// Control needles: two routes this story fences must still be found.
	for _, want := range []string{vrRulesRoute, vrBatchRoute} {
		if !registered[want] {
			t.Fatalf("the walk found no %q — that route survives this story, so its absence means the walk broke, not that the route went away", want)
		}
	}

	// The batch registration must never be reported as the offender.
	for _, r := range routes {
		if r.route == vrBatchRoute {
			if len(vrOffenders([]scRoute{r})) != 0 {
				t.Errorf("%s reported as a %q registration — the batch route is Out of Scope for RMV-01", r, vrRetiredRoute)
			}
		}
	}

	if off := vrOffenders(routes); len(off) > 0 {
		for _, r := range off {
			t.Errorf("%s:%d still registers %q — RMV-01-03 retires the single-document validate route; delete the registration (and its doc row in %s)", r.file, r.line, r.route, scDocPath)
		}
	}
}

// ---------------------------------------------------------------------------
// The other half of AC 3: "registers OR REFERENCES". The scan above reads
// registrations only, so a comment or symbol naming the retired route reads
// clean — which is how `evaluators.go`'s "for the M3-09 UI" and
// `handlers.go`'s "contrast ValidateHandler's identity-first-401 above"
// survived the retirement commit.
// ---------------------------------------------------------------------------

// vrGoFiles is every .go file in the repo bar this one, which carries the
// needles by necessity.
func vrGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == vrSelfPath {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

const vrSelfPath = "internal/platform/db/validate_route_retired_test.go"

// vrBareRouteHits reports each 1-based line mentioning /v1/validate that is
// neither /v1/validate/batch nor a longer path segment. Exact-segment, not
// prefix: the two surviving siblings must never register a hit.
func vrBareRouteHits(src string) []int {
	const needle = "/v1/validate"
	var lines []int
	for i, line := range strings.Split(src, "\n") {
		for at := 0; ; {
			j := strings.Index(line[at:], needle)
			if j < 0 {
				break
			}
			end := at + j + len(needle)
			at = end
			if end < len(line) {
				c := line[end]
				if c == '/' || c == '_' || c == '-' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') {
					continue
				}
			}
			lines = append(lines, i+1)
			break
		}
	}
	return lines
}

// vrSymbolHits reports each line naming the deleted validation.ValidateHandler.
// BatchValidateHandler and internal/invoice's own ValidateHandler both survive,
// so the match is boundary-anchored and the caller scopes it by directory.
func vrSymbolHits(src string) []int {
	var lines []int
	for i, line := range strings.Split(src, "\n") {
		if vrSymbolRe.MatchString(line) {
			lines = append(lines, i+1)
		}
	}
	return lines
}

var vrSymbolRe = regexp.MustCompile(`(^|[^A-Za-z0-9_])ValidateHandler([^A-Za-z0-9_]|$)`)

func TestRLS_SingleDocumentValidateRouteIsNotReferencedInGo(t *testing.T) {
	t.Run("the needles tell the retired route from its surviving siblings", func(t *testing.T) {
		fixture := strings.Join([]string{
			`app.Mux.Handle("POST /v1/validate/batch", nil)`,             // 1 survives
			`app.Mux.HandleFunc("POST /v1/invoices/{id}/validate", nil)`, // 2 survives
			`// see /v1/validated for nothing`,                           // 3 survives
			`// the retired POST /v1/validate route`,                     // 4 OFFENDS
			`return BatchValidateHandler(load, eng, nil)`,                // 5 survives
			`// contrast ValidateHandler's identity-first-401`,           // 6 OFFENDS
		}, "\n")
		if got := vrBareRouteHits(fixture); len(got) != 1 || got[0] != 4 {
			t.Errorf("vrBareRouteHits = %v, want [4] — the batch peer, the gate route and /v1/validated must never register a hit", got)
		}
		if got := vrSymbolHits(fixture); len(got) != 1 || got[0] != 6 {
			t.Errorf("vrSymbolHits = %v, want [6] — BatchValidateHandler is a different symbol that survives", got)
		}
	})

	root := repoRootDir(t)
	files := vrGoFiles(t, root)
	// Floor. An absence assertion over a truncated walk clears everything.
	// 707 after self-exclusion at RMV-01-03.
	if len(files) < 600 {
		t.Fatalf("walked %d .go file(s), want at least 600 (707 measured at RMV-01-03) — a truncated list reads clean", len(files))
	}
	if slices.Contains(files, vrSelfPath) {
		t.Fatalf("the walk must exclude %s — this file carries the needles by necessity", vrSelfPath)
	}

	// Non-vacuity: the surviving siblings must be findable by the same walk.
	survivors := map[string]bool{}
	for _, rel := range files {
		src := vrRead(t, root, rel)
		if strings.Contains(src, vrBatchRoute) {
			survivors[vrBatchRoute] = true
		}
		if strings.Contains(src, "BatchValidateHandler") {
			survivors["BatchValidateHandler"] = true
		}
	}
	for _, want := range []string{vrBatchRoute, "BatchValidateHandler"} {
		if !survivors[want] {
			t.Fatalf("the walk found no %q — it survives this story, so its absence means the walk broke, not that the tree is clean", want)
		}
	}

	for _, rel := range files {
		src := vrRead(t, root, rel)
		for _, line := range vrBareRouteHits(src) {
			t.Errorf("%s:%d names the bare /v1/validate route, retired by RMV-01-03 — reword it; %s is a different, surviving route", rel, line, vrBatchRoute)
		}
		// validation.ValidateHandler is deleted; internal/invoice's is not.
		if !strings.HasPrefix(rel, "internal/validation/") && !strings.HasPrefix(rel, "cmd/validation/") {
			continue
		}
		for _, line := range vrSymbolHits(src) {
			t.Errorf("%s:%d names validation.ValidateHandler, deleted by RMV-01-03 — reword it or name the symbol that actually handles the case", rel, line)
		}
	}
}

func vrRead(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}
