// resolve_internal_test.go: V-16, V-18, V-19, V-20. Package extraction, not extraction_test:
// V-20 calls the unexported comparator, and the three source scans read files in this package.
//
// The three scans are green from the start -- an absence scan over clean source reports
// all-clear, which reads exactly like a scan that reached nothing. Each therefore carries two
// guards: a floor (every named file parses and yields declarations) and a needle (a source
// STRING carrying the banned construct is reported, and a near-miss is not). The needle is
// never a committed file: proving the map scan works must not commit a map.
package extraction

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"testing"
)

// rvMapScanFiles are the files that declare or read every type on the resolution path. A map
// FIELD in any of them puts map iteration one dereference from Resolve. anchor_store.go is out:
// it runs before Resolve, not on its path.
var rvMapScanFiles = []string{
	"resolve.go", "tier1.go", "vocabulary.go", "anchor.go",
	"shapes.go", "fingerprint.go", "extractor.go", "pagereader.go",
}

// rvPureFiles are the two files Resolve's purity is asserted over. shapes.go is out: it
// legitimately imports time for a fixed-layout time.Parse, which reads no clock.
var rvPureFiles = []string{"resolve.go", "tier1.go"}

// rvAllowedImports excludes time, math/rand, net/*, database/sql and pgx by omission.
var rvAllowedImports = []string{"math", "regexp", "slices", "strings", "unicode"}

// rvParse parses one source. src nil reads the named file; a string is a needle.
func rvParse(t *testing.T, name string, src any) *ast.File {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

// rvHasMapType reports whether a map type is written anywhere in f. go/ast alone cannot type a
// range expression, so "no map type is written here" is the syntactic superset.
func rvHasMapType(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.MapType); ok {
			found = true
		}
		return !found
	})
	return found
}

func rvImportPaths(f *ast.File) []string {
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			p = imp.Path.Value
		}
		out = append(out, p)
	}
	return out
}

// rvConcurrency names every concurrency construct in f.
func rvConcurrency(f *ast.File) []string {
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.GoStmt:
			hits = append(hits, "go statement")
		case *ast.SelectStmt:
			hits = append(hits, "select statement")
		case *ast.ChanType:
			hits = append(hits, "channel type")
		}
		return true
	})
	return hits
}

// V-16
func TestResolve_TouchesNoMap(t *testing.T) {
	decls := 0
	for _, name := range rvMapScanFiles {
		// A mistyped name Fatalfs here rather than silently scanning nothing.
		f := rvParse(t, name, nil)
		decls += len(f.Decls)
		if rvHasMapType(f) {
			t.Errorf("%s writes a map type; map iteration order is not deterministic and this file is on the resolution path", name)
		}
	}
	if decls == 0 {
		t.Fatalf("the %d scanned file(s) declare nothing; the scan above reported all-clear over an empty AST", len(rvMapScanFiles))
	}

	const needle = `package p

func f() {
	var m map[string]int
	for k := range m {
		_ = k
	}
}
`
	const control = `package p

func f() {
	var s []int
	for i := range s {
		_ = i
	}
}
`
	if !rvHasMapType(rvParse(t, "needle.go", needle)) {
		t.Error("the needle source ranges a map and the scan did not report it; the all-clear above proves nothing")
	}
	if rvHasMapType(rvParse(t, "control.go", control)) {
		t.Error("the control source ranges a slice and the scan called it a map; the scan is not specific")
	}
}

// V-18
func TestResolve_ImportsOnlyPureStdlib(t *testing.T) {
	files := 0
	for _, name := range rvPureFiles {
		f := rvParse(t, name, nil)
		files++
		for _, p := range rvImportPaths(f) {
			if !slices.Contains(rvAllowedImports, p) {
				t.Errorf("%s imports %q; the resolution path takes no clock, no network and no database", name, p)
			}
		}
	}
	if files != len(rvPureFiles) {
		t.Fatalf("parsed %d of %d files; the allowlist above ran over a subset", files, len(rvPureFiles))
	}

	const needle = `package p

import "database/sql"

var _ = sql.ErrNoRows
`
	got := rvImportPaths(rvParse(t, "needle.go", needle))
	if len(got) == 0 {
		t.Fatal("the needle source imports database/sql and rvImportPaths found no import at all")
	}
	banned := false
	for _, p := range got {
		if !slices.Contains(rvAllowedImports, p) {
			banned = true
		}
	}
	if !banned {
		t.Errorf("the allowlist accepted the needle's imports %v; the clean result above proves nothing", got)
	}
}

// V-19
func TestResolve_StartsNoGoroutine(t *testing.T) {
	files := 0
	for _, name := range rvPureFiles {
		files++
		if hits := rvConcurrency(rvParse(t, name, nil)); len(hits) != 0 {
			t.Errorf("%s carries %v; Resolve is a pure function and starts nothing", name, hits)
		}
	}
	if files != len(rvPureFiles) {
		t.Fatalf("parsed %d of %d files; the scan above ran over a subset", files, len(rvPureFiles))
	}

	const needle = `package p

func g() {}

func f() {
	go g()
}
`
	const control = `package p

func g() {}

func f() {
	g()
}
`
	if hits := rvConcurrency(rvParse(t, "needle.go", needle)); len(hits) == 0 {
		t.Error("the needle source starts a goroutine and the scan did not report it; the all-clear above proves nothing")
	}
	if hits := rvConcurrency(rvParse(t, "control.go", control)); len(hits) != 0 {
		t.Errorf("the control source only calls a function and the scan reported %v; the scan is not specific", hits)
	}
}

// V-20
func TestResolve_ComparatorIsTotal(t *testing.T) {
	box := func(page int, x0, y0, x1, y1 float64) *Region {
		return &Region{Page: page, X0: x0, Y0: y0, X1: x1, Y1: y1}
	}
	base := Candidate{
		Field:    "total",
		Value:    "A",
		Region:   box(1, 0.10, 0.10, 0.20, 0.20),
		RuleID:   "r1",
		Tier:     TierLearned,
		Distance: 0.10,
	}
	with := func(mut func(*Candidate)) Candidate {
		c := base
		mut(&c)
		return c
	}

	set := []struct {
		name string
		c    Candidate
	}{
		{"base", base},
		{"tier", with(func(c *Candidate) { c.Tier = TierGeneric })},
		{"distance", with(func(c *Candidate) { c.Distance = 0.20 })},
		{"nil region", with(func(c *Candidate) { c.Region = nil })},
		{"page", with(func(c *Candidate) { c.Region = box(2, 0.10, 0.10, 0.20, 0.20) })},
		{"y0", with(func(c *Candidate) { c.Region = box(1, 0.10, 0.15, 0.20, 0.20) })},
		{"x0", with(func(c *Candidate) { c.Region = box(1, 0.15, 0.10, 0.20, 0.20) })},
		{"y1 only", with(func(c *Candidate) { c.Region = box(1, 0.10, 0.10, 0.20, 0.30) })},
		{"x1 only", with(func(c *Candidate) { c.Region = box(1, 0.10, 0.10, 0.30, 0.20) })},
		{"value", with(func(c *Candidate) { c.Value = "B" })},
		{"rule id", with(func(c *Candidate) { c.RuleID = "r2" })},
	}

	// Floor: two identical entries would make the totality assertion below unsatisfiable and
	// the failure unreadable.
	for i := range set {
		for j := i + 1; j < len(set); j++ {
			if reflect.DeepEqual(set[i].c, set[j].c) {
				t.Fatalf("the adversarial set repeats itself: %s and %s are the same candidate", set[i].name, set[j].name)
			}
		}
	}

	for i := range set {
		if got := compareCandidates(set[i].c, set[i].c); got != 0 {
			t.Errorf("compareCandidates is not reflexive on %s: got %d, want 0", set[i].name, got)
		}
	}

	for i := range set {
		for j := i + 1; j < len(set); j++ {
			ij := compareCandidates(set[i].c, set[j].c)
			ji := compareCandidates(set[j].c, set[i].c)
			if ij == 0 {
				t.Errorf("compareCandidates(%s, %s) == 0 but the two differ; the order is not total and an unstable sort may reorder them", set[i].name, set[j].name)
			}
			if (ij > 0) != (ji < 0) || (ij < 0) != (ji > 0) {
				t.Errorf("compareCandidates is not antisymmetric on (%s, %s): got %d and %d", set[i].name, set[j].name, ij, ji)
			}
		}
	}
}
