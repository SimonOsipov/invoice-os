// reconcile_test.go: EXTR-05-03's row arithmetic and the line-sum check -- Reconcile's per-row
// and subtotal-vs-lines passes only. Totality over HeaderFields, ambiguity and the supplier
// check are EXTR-05-04/05; these fixtures never supply more than one candidate per field.
package extraction_test

import (
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

func rcStr(s string) *string { return &s }

// rcCandidate builds a single-candidate reading for field with value v -- EXTR-05-03 restricts
// subtotal (the only field these fixtures decide) to 0-or-1 candidates; ambiguity is EXTR-05-04.
func rcCandidate(field, v string) extraction.Candidate {
	return extraction.Candidate{Field: field, Value: v, Reason: extraction.ReasonNone}
}

// rcFind returns the result named name and whether it was present.
func rcFind(results []extraction.FieldResult, name string) (extraction.FieldResult, bool) {
	for _, r := range results {
		if r.Name == name {
			return r, true
		}
	}
	return extraction.FieldResult{}, false
}

// rcLineFlags returns every per-row arithmetic flag (line_items[N].line_total) in results.
func rcLineFlags(results []extraction.FieldResult) []extraction.FieldResult {
	var out []extraction.FieldResult
	for _, r := range results {
		if strings.HasPrefix(r.Name, "line_items[") {
			out = append(out, r)
		}
	}
	return out
}

func TestReconcile_RowProductMatchesItsLineTotal(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("2"), UnitPrice: rcStr("500.00"), LineTotal: rcStr("1000.00")},
		},
	}
	results := extraction.Reconcile(in)
	if len(results) == 0 {
		t.Fatal("Reconcile returned zero results for a one-line input; the row check below would be vacuous")
	}
	if flags := rcLineFlags(results); len(flags) != 0 {
		t.Errorf("line_items[1].line_total flagged as %+v, want no per-row result -- 2 x 500.00 = 1000.00 exactly", flags)
	}
	if lb, ok := rcFind(results, "line_items"); !ok {
		t.Error(`"line_items" result not found among Reconcile's output`)
	} else if lb.Reason != extraction.ReasonNone {
		t.Errorf("line_items reason = %q, want ReasonNone -- one line was read", lb.Reason)
	}
}

func TestReconcile_RowProductMissesItsLineTotal(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("2"), UnitPrice: rcStr("500.00"), LineTotal: rcStr("900.00")},
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "line_items[1].line_total")
	if !ok {
		t.Fatalf(`"line_items[1].line_total" not found in %+v -- 2 x 500.00 = 1000.00, printed 900.00`, results)
	}
	if got.Reason != extraction.ReasonInconsistent {
		t.Errorf("line_items[1].line_total reason = %q, want ReasonInconsistent", got.Reason)
	}
	gotVal := "<nil>"
	if got.Value != nil {
		gotVal = *got.Value
	}
	if gotVal != "900.00" {
		t.Errorf("line_items[1].line_total Value = %s, want the line's own printed total 900.00", gotVal)
	}
}

func TestReconcile_RowOffByExactlyOneMinorUnitPasses(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("3"), UnitPrice: rcStr("3.33"), LineTotal: rcStr("10.00")},
		},
	}
	results := extraction.Reconcile(in)
	if len(results) == 0 {
		t.Fatal("Reconcile returned zero results; the row check below would be vacuous")
	}
	if flags := rcLineFlags(results); len(flags) != 0 {
		t.Errorf("line_items[1].line_total flagged as %+v, want no per-row result -- |9.99-10.00| = 0.01 = reconcileTolerance, not greater than it", flags)
	}
}

func TestReconcile_RowOffByTwoMinorUnitsFails(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("3"), UnitPrice: rcStr("3.33"), LineTotal: rcStr("10.01")},
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "line_items[1].line_total")
	if !ok {
		t.Fatalf(`"line_items[1].line_total" not found in %+v -- |9.99-10.01| = 0.02 exceeds reconcileTolerance`, results)
	}
	if got.Reason != extraction.ReasonInconsistent {
		t.Errorf("line_items[1].line_total reason = %q, want ReasonInconsistent", got.Reason)
	}
}

func TestReconcile_ThreeDecimalQuantityMultipliesExactly(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("2.505"), UnitPrice: rcStr("100.00"), LineTotal: rcStr("250.50")},
		},
	}
	results := extraction.Reconcile(in)
	if len(results) == 0 {
		t.Fatal("Reconcile returned zero results; the row check below would be vacuous")
	}
	if flags := rcLineFlags(results); len(flags) != 0 {
		t.Errorf("line_items[1].line_total flagged as %+v, want no per-row result -- 2.505 x 100.00 = 250.5000 exactly", flags)
	}
}

func TestReconcile_RowMissingItsUnitPriceIsNotChecked(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("2"), UnitPrice: nil, LineTotal: rcStr("900.00")},
		},
	}
	results := extraction.Reconcile(in)
	if len(results) == 0 {
		t.Fatal("Reconcile returned zero results; the row check below would be vacuous")
	}
	if flags := rcLineFlags(results); len(flags) != 0 {
		t.Errorf("line_items[1].line_total flagged as %+v, want no per-row result -- a row missing UnitPrice is not checked", flags)
	}
	if lb, ok := rcFind(results, "line_items"); !ok {
		t.Error(`"line_items" result not found among Reconcile's output`)
	} else if lb.Reason != extraction.ReasonNone {
		t.Errorf("line_items reason = %q, want ReasonNone -- one line was read even though it is not arithmetic-checked", lb.Reason)
	}
}

func TestReconcile_OneBadRowDoesNotCondemnTheTable(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("10.00"), LineTotal: rcStr("10.00")},
			{Index: 2, Quantity: rcStr("1"), UnitPrice: rcStr("10.00"), LineTotal: rcStr("20.00")},
			{Index: 3, Quantity: rcStr("1"), UnitPrice: rcStr("10.00"), LineTotal: rcStr("10.00")},
		},
	}
	results := extraction.Reconcile(in)
	flags := rcLineFlags(results)
	if len(flags) != 1 {
		t.Fatalf("got %d per-row flag(s) %+v, want exactly 1 -- only row 2 is wrong", len(flags), flags)
	}
	if flags[0].Name != "line_items[2].line_total" {
		t.Errorf("flagged row = %q, want %q", flags[0].Name, "line_items[2].line_total")
	}
	if flags[0].Reason != extraction.ReasonInconsistent {
		t.Errorf("reason = %q, want ReasonInconsistent", flags[0].Reason)
	}
}

func TestReconcile_LineSumMatchesTheSubtotal(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{rcCandidate("subtotal", "1500.00")},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1000.00"), LineTotal: rcStr("1000.00")},
			{Index: 2, Quantity: rcStr("1"), UnitPrice: rcStr("500.00"), LineTotal: rcStr("500.00")},
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "subtotal")
	if !ok {
		t.Fatalf(`"subtotal" result not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("subtotal reason = %q, want ReasonNone -- 1000.00 + 500.00 = 1500.00", got.Reason)
	}
}

func TestReconcile_LineSumMissesTheSubtotal(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{rcCandidate("subtotal", "1400.00")},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1000.00"), LineTotal: rcStr("1000.00")},
			{Index: 2, Quantity: rcStr("1"), UnitPrice: rcStr("500.00"), LineTotal: rcStr("500.00")},
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "subtotal")
	if !ok {
		t.Fatalf(`"subtotal" result not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonInconsistent {
		t.Errorf("subtotal reason = %q, want ReasonInconsistent -- line sum 1500.00 vs printed 1400.00", got.Reason)
	}
}

// The pair is the oracle (D-19): asserting either half alone could pass on a Reconcile that
// always reports the subtotal clean, or one that always reports the block missing, regardless
// of whether the sum check actually ran.
func TestReconcile_NoLinesLeavesTheSubtotalCleanAndTheBlockMissing(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{rcCandidate("subtotal", "1500.00")},
	}
	results := extraction.Reconcile(in)
	if len(results) == 0 {
		t.Fatal("Reconcile returned zero results; the pair check below would be vacuous")
	}
	subtotal, subOK := rcFind(results, "subtotal")
	if !subOK {
		t.Fatalf(`"subtotal" result not found in %+v`, results)
	}
	lineBlock, lineOK := rcFind(results, "line_items")
	if !lineOK {
		t.Fatalf(`"line_items" result not found in %+v`, results)
	}
	if subtotal.Reason != extraction.ReasonNone || lineBlock.Reason != extraction.ReasonMissing {
		t.Errorf("subtotal reason = %q (want ReasonNone) and line_items reason = %q (want ReasonMissing) -- no lines means the sum check never ran, so subtotal stays clean while the block itself is missing",
			subtotal.Reason, lineBlock.Reason)
	}
}

func TestReconcile_NoSubtotalCandidateRunsNoSumCheck(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1000.00"), LineTotal: rcStr("1000.00")},
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "subtotal")
	if !ok {
		t.Fatalf(`"subtotal" result not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonMissing {
		t.Errorf("subtotal reason = %q, want ReasonMissing -- zero subtotal candidates means the sum check never ran, and missing is never rewritten to inconsistent", got.Reason)
	}
}

func TestReconcile_SubtotalPlusVATNeedNotEqualTotal(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandidate("subtotal", "1000.00"),
			rcCandidate("vat", "75.00"),
			rcCandidate("total", "1200.00"),
		},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1000.00"), LineTotal: rcStr("1000.00")},
		},
	}
	results := extraction.Reconcile(in)
	for _, name := range []string{"subtotal", "vat", "total"} {
		got, ok := rcFind(results, name)
		if !ok {
			t.Fatalf("%q result not found in %+v", name, results)
		}
		if got.Reason != extraction.ReasonNone {
			t.Errorf("%s reason = %q, want ReasonNone -- subtotal + vat need not equal total", name, got.Reason)
		}
	}
}
