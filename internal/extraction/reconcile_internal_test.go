// reconcile_internal_test.go: EXTR-05-03's tolerance constant and EXTR-05-04's purity scans.
// Package extraction: the constant is unexported and the scans read files in this package.
package extraction

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"github.com/shopspring/decimal"
)

func TestReconcile_ToleranceIsOneMinorUnit(t *testing.T) {
	got, err := decimal.NewFromString(reconcileTolerance)
	if err != nil {
		t.Fatalf("decimal.NewFromString(reconcileTolerance) error: %v", err)
	}
	want, err := decimal.NewFromString("0.01")
	if err != nil {
		t.Fatalf("test setup: decimal.NewFromString(\"0.01\") error: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("reconcileTolerance parses to %s, want 0.01", got)
	}
}

// --- EXTR-05-04: purity scans ---------------------------------------------------------
//
// Named ri* (not rv*, resolve_internal_test.go's own prefix) to avoid colliding in this same
// package. Each scan carries a floor (the named file parses and declares something) and a
// needle/control pair (a planted source string with the banned construct must be caught, and a
// clean one must not) -- a scan matching nothing on a real file reads exactly like a scan that
// never ran.

// riImportScanFiles is D-9's import contract: reconcile.go and lineitems.go together.
var riImportScanFiles = []string{"reconcile.go", "lineitems.go"}

// riMapGoroutineFiles is reconcile.go alone (D-12): lineitems.go legitimately writes three map
// types building its header lexicon and row index, so scoping this scan to both files would be
// wrong.
var riMapGoroutineFiles = []string{"reconcile.go"}

// riAllowedImports is the decision stage's own allowlist: stdlib plus shopspring/decimal, never
// time, math/rand, net/*, database/sql or pgx.
var riAllowedImports = []string{"math", "regexp", "slices", "strconv", "strings", "unicode", "github.com/shopspring/decimal"}

func riParse(t *testing.T, name string, src any) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

func riImportPaths(f *ast.File) []string {
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

func riHasMapType(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.MapType); ok {
			found = true
		}
		return !found
	})
	return found
}

func riConcurrency(f *ast.File) []string {
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

func TestReconcile_ImportsOnlyPureStdlibAndDecimal(t *testing.T) {
	files := 0
	for _, name := range riImportScanFiles {
		f := riParse(t, name, nil)
		files++
		if len(f.Decls) == 0 {
			t.Fatalf("%s declares nothing; the scan below would report all-clear over an empty AST", name)
		}
		for _, p := range riImportPaths(f) {
			if !slices.Contains(riAllowedImports, p) {
				t.Errorf("%s imports %q; the decision stage stays on pure stdlib plus shopspring/decimal", name, p)
			}
		}
	}
	if files != len(riImportScanFiles) {
		t.Fatalf("parsed %d of %d files; the allowlist above ran over a subset", files, len(riImportScanFiles))
	}

	const needle = `package p

import "database/sql"

var _ = sql.ErrNoRows
`
	got := riImportPaths(riParse(t, "needle.go", needle))
	if len(got) == 0 {
		t.Fatal("the needle source imports database/sql and riImportPaths found no import at all")
	}
	banned := false
	for _, p := range got {
		if !slices.Contains(riAllowedImports, p) {
			banned = true
		}
	}
	if !banned {
		t.Errorf("the allowlist accepted the needle's imports %v; the clean result above proves nothing", got)
	}

	const control = `package p

import "github.com/shopspring/decimal"

var _ = decimal.Decimal{}
`
	for _, p := range riImportPaths(riParse(t, "control.go", control)) {
		if !slices.Contains(riAllowedImports, p) {
			t.Errorf("the control source only imports the allowed shopspring/decimal, and the scan flagged %q; the scan is not specific", p)
		}
	}
}

func TestReconcile_WritesNoMapTypeAndStartsNoGoroutine(t *testing.T) {
	decls := 0
	for _, name := range riMapGoroutineFiles {
		f := riParse(t, name, nil)
		decls += len(f.Decls)
		if riHasMapType(f) {
			t.Errorf("%s writes a map type; the decision stage stays off maps (resolve.go's own posture)", name)
		}
		if hits := riConcurrency(f); len(hits) != 0 {
			t.Errorf("%s carries %v; Reconcile is a pure function and starts nothing", name, hits)
		}
	}
	if decls == 0 {
		t.Fatalf("the %d scanned file(s) declare nothing; the scan above reported all-clear over an empty AST", len(riMapGoroutineFiles))
	}

	const mapNeedle = `package p

func f() {
	var m map[string]int
	for k := range m {
		_ = k
	}
}
`
	const mapControl = `package p

func f() {
	var s []int
	for i := range s {
		_ = i
	}
}
`
	if !riHasMapType(riParse(t, "mapneedle.go", mapNeedle)) {
		t.Error("the map needle source declares a map and the scan did not report it; the all-clear above proves nothing")
	}
	if riHasMapType(riParse(t, "mapcontrol.go", mapControl)) {
		t.Error("the map control source only declares a slice and the scan called it a map; the scan is not specific")
	}

	const goNeedle = `package p

func g() {}

func f() {
	go g()
}
`
	const goControl = `package p

func g() {}

func f() {
	g()
}
`
	if hits := riConcurrency(riParse(t, "goneedle.go", goNeedle)); len(hits) == 0 {
		t.Error("the goroutine needle source starts a goroutine and the scan did not report it; the all-clear above proves nothing")
	}
	if hits := riConcurrency(riParse(t, "gocontrol.go", goControl)); len(hits) != 0 {
		t.Errorf("the goroutine control source only calls a function and the scan reported %v; the scan is not specific", hits)
	}
}
