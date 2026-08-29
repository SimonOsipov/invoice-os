// reconcile_adversarial_test.go: EXTR-05-03's edge and negative cases -- the withholding-gap
// non-failure, and the AST scan proving the money path never touches float64.
package extraction_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// TestReconcile_RowMissingAnyOfTheThreeNumbersIsNotChecked covers the two combinations
// reconcile_test.go's own missing-UnitPrice case does not: missing Quantity and missing
// LineTotal. Each printed total, where present, disagrees with what the other two numbers would
// multiply to -- a bug that checked the row anyway would flag it here.
func TestReconcile_RowMissingAnyOfTheThreeNumbersIsNotChecked(t *testing.T) {
	cases := []struct {
		name string
		line extraction.DocLine
	}{
		{"missing quantity", extraction.DocLine{Index: 1, Quantity: nil, UnitPrice: rcStr("500.00"), LineTotal: rcStr("900.00")}},
		{"missing line total", extraction.DocLine{Index: 1, Quantity: rcStr("2"), UnitPrice: rcStr("500.00"), LineTotal: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := extraction.Reconcile(extraction.Input{Lines: []extraction.DocLine{tc.line}})
			if flags := rcLineFlags(results); len(flags) != 0 {
				t.Errorf("line_items[1].line_total flagged as %+v, want no per-row result -- %s", flags, tc.name)
			}
		})
	}
}

// TestReconcile_FieldWithTwoCandidatesIsSilentlyAbsent pins today's generalisation of the
// 0-or-1-candidate rule from subtotal to every field: a field with 2+ candidates gets no
// FieldResult at all rather than a wrong reason code. Ambiguity resolution is EXTR-05-04's; this
// is the contract 04 inherits and must not read as "vat decided, clean."
func TestReconcile_FieldWithTwoCandidatesIsSilentlyAbsent(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandidate("vat", "75.00"),
			rcCandidate("vat", "80.00"),
			rcCandidate("total", "1200.00"),
		},
	}
	results := extraction.Reconcile(in)
	if _, ok := rcFind(results, "vat"); ok {
		t.Error(`"vat" found in Reconcile's output; a field with 2 candidates should be absent (ambiguity resolution is EXTR-05-04's), never decided as though it had exactly one`)
	}
	got, ok := rcFind(results, "total")
	if !ok {
		t.Fatalf(`"total" result not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("total reason = %q, want ReasonNone -- vat's ambiguity must not leak into total's own decision", got.Reason)
	}
}

// TestReconcile_AllLineTotalsUnparseableLeavesTheBlockMissing pins D-21: the line_items block
// reports ReasonNone only when the sum check could actually run, i.e. at least one line carries
// a parseable total. A row that exists but contributes nothing parseable leaves the sum check
// unable to run, which must read as ReasonMissing -- not the ReasonNone a genuinely reconciled
// block gets, and not indistinguishable from having zero lines either.
func TestReconcile_AllLineTotalsUnparseableLeavesTheBlockMissing(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{rcCandidate("subtotal", "1500.00")},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1000.00"), LineTotal: rcStr("garbled")},
		},
	}
	results := extraction.Reconcile(in)
	subtotal, ok := rcFind(results, "subtotal")
	if !ok {
		t.Fatalf(`"subtotal" result not found in %+v`, results)
	}
	if subtotal.Reason != extraction.ReasonNone {
		t.Errorf("subtotal reason = %q, want ReasonNone -- no line carries a parseable total, so the sum check never ran (D-19)", subtotal.Reason)
	}
	if flags := rcLineFlags(results); len(flags) != 0 {
		t.Errorf("line_items[1].line_total flagged as %+v, want no per-row result -- an unparseable printed total is not checked either", flags)
	}
	lineBlock, ok := rcFind(results, "line_items")
	if !ok {
		t.Fatalf(`"line_items" result not found in %+v`, results)
	}
	if lineBlock.Reason != extraction.ReasonMissing {
		t.Errorf("line_items reason = %q, want ReasonMissing (D-21) -- a row present with no usable total means the sum check could not run", lineBlock.Reason)
	}
}

// TestReconcile_LargeAmountsStayExactUnderDecimal proves the row check is genuinely decimal, not
// float64 wearing a decimal.Decimal wrapper. 99999999.999 * 9999999999.99 is exactly
// 999999999989000000.00001 under shopspring/decimal; the same multiplication in float64 rounds
// to 999999999988999936, off by about 107 -- two orders of magnitude past reconcileTolerance. A
// float64-backed implementation would wrongly flag this exact match as inconsistent.
func TestReconcile_LargeAmountsStayExactUnderDecimal(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("99999999.999"), UnitPrice: rcStr("9999999999.99"), LineTotal: rcStr("999999999989000000.00001")},
		},
	}
	results := extraction.Reconcile(in)
	if flags := rcLineFlags(results); len(flags) != 0 {
		t.Errorf("line_items[1].line_total flagged as %+v, want no per-row result -- the product is exact under decimal.Decimal; float64 would diverge by ~107", flags)
	}
}

func TestReconcile_WithholdingGapIsNotAFailure(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandidate("subtotal", "1000.00"),
			rcCandidate("total", "950.00"),
		},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1000.00"), LineTotal: rcStr("1000.00")},
		},
	}
	results := extraction.Reconcile(in)
	for _, name := range []string{"subtotal", "total"} {
		got, ok := rcFind(results, name)
		if !ok {
			t.Fatalf("%q result not found in %+v", name, results)
		}
		if got.Reason != extraction.ReasonNone {
			t.Errorf("%s reason = %q, want ReasonNone -- total below subtotal is a withholding gap, never a failure", name, got.Reason)
		}
	}
}

// --- purity scan: no float on the money path -----------------------------------

// rcParse parses one source. src nil reads the named file; a string is a needle/control.
func rcParse(t *testing.T, name string, src any) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

// rcFloatHits reports every float64 conversion and strconv.ParseFloat call in f.
func rcFloatHits(f *ast.File) []string {
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "float64" {
				hits = append(hits, "float64(...) conversion")
			}
		case *ast.SelectorExpr:
			if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "strconv" && fn.Sel.Name == "ParseFloat" {
				hits = append(hits, "strconv.ParseFloat")
			}
		}
		return true
	})
	return hits
}

func TestReconcile_NamesNoFloatOnTheMoneyPath(t *testing.T) {
	f := rcParse(t, "reconcile.go", nil)
	if len(f.Decls) == 0 {
		t.Fatal("reconcile.go declares nothing; the scan below reported all-clear over an empty AST")
	}
	if hits := rcFloatHits(f); len(hits) != 0 {
		t.Errorf("reconcile.go carries %v; the money path must stay on shopspring/decimal", hits)
	}

	// Needle/control: a scan matching nothing on a clean file reads exactly like a scan that
	// never fires at all.
	const floatNeedle = `package p

import "strconv"

func f(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return float64(v)
}
`
	const floatControl = `package p

import "github.com/shopspring/decimal"

func f(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}
`
	needleHits := rcFloatHits(rcParse(t, "needle.go", floatNeedle))
	if len(needleHits) == 0 {
		t.Fatal("the needle source calls strconv.ParseFloat and converts to float64, and the scan reported nothing; the all-clear above proves nothing")
	}
	if controlHits := rcFloatHits(rcParse(t, "control.go", floatControl)); len(controlHits) != 0 {
		t.Errorf("the control source only calls decimal.NewFromString, an allowed idiom, and the scan flagged %v; the scan is not specific", controlHits)
	}
}

// --- EXTR-05-04: totality, ambiguity and reason precedence --------------------------

func TestReconcile_IgnoresACandidateOutsideTheVocabulary(t *testing.T) {
	in := extraction.Input{Candidates: []extraction.Candidate{rcCandidate("not_a_field", "x")}}
	results := extraction.Reconcile(in)
	want := rcExpectedNames()
	if len(results) != len(want) {
		t.Fatalf("got %d results %v, want exactly %d: %v -- a candidate outside HeaderFields must not change the total", len(results), rcNames(results), len(want), want)
	}
	if got := rcNames(results); !rcNamesEqual(got, want) {
		t.Errorf("result names = %v, want %v in exactly this order", got, want)
	}
	if _, ok := rcFind(results, "not_a_field"); ok {
		t.Error(`"not_a_field" found in Reconcile's output; a candidate outside HeaderFields must never surface as its own result`)
	}
}

func TestReconcile_ANativePDFWithNoTablesIsMissingNotClean(t *testing.T) {
	missing := extraction.Reconcile(extraction.Input{Lines: nil})
	clean := extraction.Reconcile(extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("2"), UnitPrice: rcStr("10.00"), LineTotal: rcStr("20.00")},
		},
	})

	missingBlock, ok := rcFind(missing, "line_items")
	if !ok {
		t.Fatalf(`"line_items" not found in %+v`, missing)
	}
	cleanBlock, ok := rcFind(clean, "line_items")
	if !ok {
		t.Fatalf(`"line_items" not found in %+v`, clean)
	}
	if missingBlock.Reason != extraction.ReasonMissing {
		t.Errorf("line_items reason = %q for a Tables==nil document (Lines empty), want ReasonMissing", missingBlock.Reason)
	}
	if missingBlock.Reason == cleanBlock.Reason {
		t.Errorf("a document with no tables and a document with one clean line both report line_items reason %q; the two states must differ", missingBlock.Reason)
	}
}

// TestReconcile_IsPermutationInvariantOnEqualInput asserts the output against a literal expected
// result, not merely self-consistency across shuffles -- a permutation check that only compares
// shuffles to each other cannot catch a non-total comparator (D-9's own oracle note). It also
// runs 13 candidates for one field, past the n=12 insertion-sort cutover in Go's sort, so both
// sort paths are exercised.
func TestReconcile_IsPermutationInvariantOnEqualInput(t *testing.T) {
	const n = 13
	base := make([]extraction.Candidate, 0, n)
	for i := 1; i <= n; i++ {
		base = append(base, rcCandAt("vat", fmt.Sprintf("V%02d", i), extraction.TierGeneric, float64(i)/100))
	}

	want := []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "issue_date", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "supplier_tin", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "supplier_name", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "buyer_tin", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "buyer_name", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "currency", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "subtotal", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "vat", Value: rcStr("V01"), Reason: extraction.ReasonNone}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "total", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		{Field: extraction.Field{Name: "line_items", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
	}

	for i := 0; i < 100; i++ {
		shuffled := make([]extraction.Candidate, len(base))
		copy(shuffled, base)
		rand.New(rand.NewSource(int64(i))).Shuffle(len(shuffled), func(a, b int) {
			shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
		})
		got := extraction.Reconcile(extraction.Input{Candidates: shuffled})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("permutation %d: Reconcile(shuffled) = %+v, want the literal %+v -- output must not depend on candidate order", i, got, want)
		}
	}
}

func TestReconcile_MutatesNeitherItsInputSliceNorItsCandidates(t *testing.T) {
	region := &extraction.Region{Page: 1, X0: 0.1, Y0: 0.1, X1: 0.2, Y1: 0.2}
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandAt("total", "1500.00", extraction.TierGeneric, 0.01),
			{Field: "total", Value: "1600.00", Region: region, Tier: extraction.TierGeneric, Distance: 0.01, RuleID: "r1"},
			rcCandidate("subtotal", "1000.00"),
		},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1000.00"), LineTotal: rcStr("1000.00")},
		},
		Entity: extraction.Entity{TIN: "12345678-0001", Name: "Acme Ltd"},
	}

	before, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input before Reconcile: %v", err)
	}
	beforeSum := sha256.Sum256(before)

	_ = extraction.Reconcile(in)

	after, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input after Reconcile: %v", err)
	}
	afterSum := sha256.Sum256(after)

	if beforeSum != afterSum {
		t.Errorf("Input changed after Reconcile: before %x, after %x -- Reconcile must not mutate its input", beforeSum, afterSum)
	}
}
