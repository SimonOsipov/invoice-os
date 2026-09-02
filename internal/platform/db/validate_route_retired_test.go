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
	"path/filepath"
	"sort"
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
		t.Fatalf("found %d registration(s) across %d file(s), want at least 60 across at least 8 (69 across 9 measured at RMV-01-03) — a walk that reads nothing clears everything", len(routes), rootsWithRoutes)
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
