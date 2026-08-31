// reconcile_internal_test.go: EXTR-05-03's tolerance constant and EXTR-05-04's purity scans.
// Package extraction: the constant is unexported and the scans read files in this package.
package extraction

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
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

// --- EXTR-05-05: flagIfInconsistent's ReasonNone gate, isolated -----------------------
//
// TestReconcile_AMissingSupplierNameKeepsItsReason (reconcile_test.go) exercises this same
// guard only through Reconcile, where a Missing field always carries a nil Value -- so removing
// the `Reason != ReasonNone` half of flagIfInconsistent's gate is invisible there: the nil-Value
// check alone still saves it. This test builds the FieldResult directly, with a Value no real
// Missing field can carry, so only the Reason check can save it -- QA's fix for that decorative
// gap (mutation: drop `|| out[i].Reason != ReasonNone` from flagIfInconsistent).
func TestFlagIfInconsistent_ReasonGateAloneProtectsAFieldItDidNotDecide(t *testing.T) {
	leaked := "leaked-value"
	out := []FieldResult{{Field: Field{Name: "supplier_name", Reason: ReasonMissing, Value: &leaked}}}
	flagIfInconsistent(out, "supplier_name", func(string) bool { return false })
	if out[0].Reason != ReasonMissing {
		t.Errorf("Reason = %q, want ReasonMissing -- flagIfInconsistent must never touch a field whose Reason is not ReasonNone, value or no value", out[0].Reason)
	}
}

// --- EXTR-16-03: the printed-value oracle's lexicon input ------------------------------

// riLabelSep is resolve.go's isLabelSep as a cutset. Restated, not called: an oracle that reads
// the code it checks moves with it.
const riLabelSep = ": -–—\t\n\v\f\r "

// RcAnchorLabelResiduesForTest returns what a same-token reading can leave behind on text: the
// remainder after each anchor-lexicon match no other entry's match strictly contains.
// reconcile_corpus_test.go's printed-value oracle reads it, and it re-derives anchorOutranked's
// widest-span rule rather than calling it, so a mutation there does not move the oracle too.
func RcAnchorLabelResiduesForTest(text string) []string {
	type span struct{ lo, hi int }
	var spans []span
	for _, m := range anchorLabelMatchers {
		if loc := m.RE.FindStringIndex(text); loc != nil {
			spans = append(spans, span{loc[0], loc[1]})
		}
	}

	var out []string
	for _, s := range spans {
		if s.lo == 0 && s.hi == len(text) {
			continue // the label IS the value; the caller's whole-token branch covers it
		}
		outranked := false
		for _, o := range spans {
			if o.lo <= s.lo && o.hi >= s.hi && o.hi-o.lo > s.hi-s.lo {
				outranked = true
				break
			}
		}
		if !outranked {
			out = append(out, strings.TrimLeft(text[s.hi:], riLabelSep))
		}
	}
	return out
}

// --- EXTR-16-03: D-4's equal-standing group, pinned -------------------------------------

func riFieldResult(t *testing.T, out []FieldResult, name string) FieldResult {
	t.Helper()
	for _, r := range out {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %s result among %d field(s)", name, len(out))
	return FieldResult{}
}

func riAltValues(alts []Field) []string {
	out := make([]string, 0, len(alts))
	for _, a := range alts {
		if a.Value != nil {
			out = append(out, *a.Value)
		}
	}
	return out
}

// The user decided at EXTR-16's critical-fork gate (D-4) that decideField's equal-standing group
// stays keyed on Tier AND Distance, because widening it turns 5 of 44 corpus pairs spuriously
// ambiguous and offers cross-party junk as alternatives. A failure here is someone reversing that
// decision, not a bug.
func TestReconcile_TheEqualStandingGroupStillKeysOnTierAndDistance(t *testing.T) {
	near := Candidate{Field: "supplier_name", Value: "Adeyemi Trading Limited", Tier: TierGeneric, Distance: 0.01, RuleID: "t1.supplier_name.right"}
	far := Candidate{Field: "supplier_name", Value: "Honeywell Group", Tier: TierGeneric, Distance: 0.02, RuleID: "t1.supplier_name.below"}

	got := riFieldResult(t, Reconcile(Input{Candidates: []Candidate{far, near}}), "supplier_name")
	if got.Value == nil || *got.Value != near.Value {
		t.Fatalf("value = %v, want %q -- the nearer candidate decides", got.Value, near.Value)
	}
	if got.Reason != ReasonNone {
		t.Errorf("reason = %q, want %q -- a Distance loser is not a second answer", got.Reason, ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("alternatives = %v, want none -- admitting a Distance loser reverses D-4", riAltValues(got.Alternatives))
	}

	// Positive control: the same pair at one Distance does go ambiguous, so the assertions above
	// are not passing over a pair decideField could never have grouped.
	far.Distance = near.Distance
	tied := riFieldResult(t, Reconcile(Input{Candidates: []Candidate{far, near}}), "supplier_name")
	if tied.Reason != ReasonAmbiguous || !slices.Equal(riAltValues(tied.Alternatives), []string{far.Value}) {
		t.Fatalf("at one Distance reason = %q with alternatives %v, want %q with [%q]", tied.Reason, riAltValues(tied.Alternatives), ReasonAmbiguous, far.Value)
	}
}
