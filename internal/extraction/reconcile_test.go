// reconcile_test.go: EXTR-05-03's row arithmetic and the line-sum check -- Reconcile's per-row
// and subtotal-vs-lines passes only. Totality over HeaderFields, ambiguity and the supplier
// check are EXTR-05-04/05; these fixtures never supply more than one candidate per field.
package extraction_test

import (
	"fmt"
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

// rcCandAt builds a candidate with explicit ordering fields. D-14 restricts "equal standing" to
// Tier and Distance alone, so these are the only two the AC-3/4/5 fixtures below vary on purpose.
func rcCandAt(field, value string, tier extraction.Tier, distance float64) extraction.Candidate {
	return extraction.Candidate{Field: field, Value: value, Reason: extraction.ReasonNone, Tier: tier, Distance: distance}
}

// rcAllHeaderCandidates returns one plausible candidate per HeaderFields member, each with a
// distinct filler value -- shared by every "every header field found" fixture so a totality gap
// in one test cannot hide behind another's coincidental field list.
func rcAllHeaderCandidates() []extraction.Candidate {
	out := make([]extraction.Candidate, 0, len(extraction.HeaderFields))
	for i, f := range extraction.HeaderFields {
		out = append(out, rcCandidate(f, fmt.Sprintf("v%d", i)))
	}
	return out
}

// rcExpectedNames is the totality order Core AC 1 pins: every HeaderFields member, in order,
// then line_items.
func rcExpectedNames() []string {
	names := make([]string, 0, len(extraction.HeaderFields)+1)
	names = append(names, extraction.HeaderFields...)
	return append(names, "line_items")
}

func rcNames(results []extraction.FieldResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Name
	}
	return out
}

// rcNamesEqual compares two name sequences positionally, without pulling in the "slices"
// package for a one-line loop.
func rcNamesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// rcValidReasons is the closed set every FieldResult.Reason must belong to (Core AC 3).
var rcValidReasons = []extraction.Reason{
	extraction.ReasonNone,
	extraction.ReasonUnreadable,
	extraction.ReasonAmbiguous,
	extraction.ReasonInconsistent,
	extraction.ReasonMissing,
}

func rcIsValidReason(r extraction.Reason) bool {
	for _, v := range rcValidReasons {
		if r == v {
			return true
		}
	}
	return false
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

// --- EXTR-05-04: totality, ambiguity and reason precedence --------------------------

func TestReconcile_IsTotalOverHeaderFieldsAndTheLineBlock(t *testing.T) {
	results := extraction.Reconcile(extraction.Input{})
	want := rcExpectedNames()
	if len(results) != len(want) {
		t.Fatalf("got %d results %v, want exactly %d: %v", len(results), rcNames(results), len(want), want)
	}
	if got := rcNames(results); !rcNamesEqual(got, want) {
		t.Errorf("result names = %v, want %v in exactly this order", got, want)
	}
}

func TestReconcile_NoCandidateIsMissing(t *testing.T) {
	in := extraction.Input{Candidates: []extraction.Candidate{rcCandidate("total", "1500.00")}}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "invoice_number")
	if !ok {
		t.Fatalf(`"invoice_number" not found in %+v -- a field with no candidate must still get a result`, results)
	}
	if got.Reason != extraction.ReasonMissing {
		t.Errorf("invoice_number reason = %q, want ReasonMissing", got.Reason)
	}
	if got.Value != nil {
		t.Errorf("invoice_number Value = %q, want nil", *got.Value)
	}
	if got.Region != nil {
		t.Errorf("invoice_number Region = %+v, want nil", got.Region)
	}
}

func TestReconcile_ZeroLinesMakesTheLineBlockMissing(t *testing.T) {
	in := extraction.Input{Candidates: rcAllHeaderCandidates()}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "line_items")
	if !ok {
		t.Fatalf(`"line_items" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonMissing {
		t.Errorf("line_items reason = %q, want ReasonMissing -- Lines is empty even though every header field was found", got.Reason)
	}
	if got.Value != nil {
		t.Errorf("line_items Value = %q, want nil", *got.Value)
	}
	if got.Region != nil {
		t.Errorf("line_items Region = %+v, want nil", got.Region)
	}
}

func TestReconcile_OneLineMakesTheLineBlockClean(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("2"), UnitPrice: rcStr("10.00"), LineTotal: rcStr("20.00")},
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "line_items")
	if !ok {
		t.Fatalf(`"line_items" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("line_items reason = %q, want ReasonNone -- one line was read and its row reconciles", got.Reason)
	}
}

func TestReconcile_ABadRowLeavesTheLineBlockClean(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("2"), UnitPrice: rcStr("10.00"), LineTotal: rcStr("25.00")},
		},
	}
	results := extraction.Reconcile(in)
	lineBlock, ok := rcFind(results, "line_items")
	if !ok {
		t.Fatalf(`"line_items" not found in %+v`, results)
	}
	if lineBlock.Reason != extraction.ReasonNone {
		t.Errorf("line_items reason = %q, want ReasonNone -- the block is present, only the row itself is flagged", lineBlock.Reason)
	}
	row, ok := rcFind(results, "line_items[1].line_total")
	if !ok {
		t.Fatalf(`"line_items[1].line_total" not found in %+v -- 2 x 10.00 = 20.00, printed 25.00`, results)
	}
	if row.Reason != extraction.ReasonInconsistent {
		t.Errorf("line_items[1].line_total reason = %q, want ReasonInconsistent", row.Reason)
	}
}

func TestReconcile_ALoneCandidateReconcilesCleanly(t *testing.T) {
	in := extraction.Input{Candidates: []extraction.Candidate{rcCandidate("total", "1500.00")}}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "total")
	if !ok {
		t.Fatalf(`"total" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("total reason = %q, want ReasonNone", got.Reason)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("total Alternatives = %+v, want empty", got.Alternatives)
	}
	if got.Value == nil || *got.Value != "1500.00" {
		t.Errorf("total Value = %v, want \"1500.00\"", got.Value)
	}
}

func TestReconcile_AStrictlyBetterHeadWins(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandAt("issue_date", "2026-01-01", extraction.TierLearned, 0.01),
			rcCandAt("issue_date", "2026-02-02", extraction.TierGeneric, 0.01),
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "issue_date")
	if !ok {
		t.Fatalf(`"issue_date" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("issue_date reason = %q, want ReasonNone -- TierLearned strictly outranks TierGeneric regardless of equal distance", got.Reason)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("issue_date Alternatives = %+v, want empty", got.Alternatives)
	}
	if got.Value == nil || *got.Value != "2026-01-01" {
		t.Errorf("issue_date Value = %v, want the TierLearned reading \"2026-01-01\"", got.Value)
	}
}

func TestReconcile_ACloserCandidateOutranksAFartherOne(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandAt("issue_date", "2026-01-01", extraction.TierGeneric, 0.01),
			rcCandAt("issue_date", "2026-02-02", extraction.TierGeneric, 0.20),
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "issue_date")
	if !ok {
		t.Fatalf(`"issue_date" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("issue_date reason = %q, want ReasonNone -- distance 0.01 strictly outranks 0.20 at the same tier", got.Reason)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("issue_date Alternatives = %+v, want empty", got.Alternatives)
	}
	if got.Value == nil || *got.Value != "2026-01-01" {
		t.Errorf("issue_date Value = %v, want the closer reading \"2026-01-01\"", got.Value)
	}
}

func TestReconcile_EqualStandingIsAmbiguous(t *testing.T) {
	const a, b = "2026-03-12", "2026-12-03"
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandAt("issue_date", a, extraction.TierGeneric, 0.01),
			rcCandAt("issue_date", b, extraction.TierGeneric, 0.01),
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "issue_date")
	if !ok {
		t.Fatalf(`"issue_date" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("issue_date reason = %q, want ReasonAmbiguous -- both candidates share Tier and Distance", got.Reason)
	}
	if got.Value == nil {
		t.Fatal("issue_date Value = nil, want one of the two equal-standing readings")
	}
	if *got.Value != a && *got.Value != b {
		t.Fatalf("issue_date Value = %q, want %q or %q", *got.Value, a, b)
	}
	if len(got.Alternatives) != 1 {
		t.Fatalf("issue_date Alternatives = %+v, want exactly 1", got.Alternatives)
	}
	alt := got.Alternatives[0]
	if alt.Value == nil || *alt.Value == *got.Value || (*alt.Value != a && *alt.Value != b) {
		t.Errorf("issue_date alternative Value = %v, want whichever of %q/%q is not the decided value %q", alt.Value, a, b, *got.Value)
	}
}

func TestReconcile_ThreeWayTieKeepsEveryPeer(t *testing.T) {
	values := []string{"75.00", "80.00", "90.00"}
	cands := make([]extraction.Candidate, 0, len(values))
	for _, v := range values {
		cands = append(cands, rcCandAt("vat", v, extraction.TierGeneric, 0.01))
	}
	results := extraction.Reconcile(extraction.Input{Candidates: cands})
	got, ok := rcFind(results, "vat")
	if !ok {
		t.Fatalf(`"vat" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("vat reason = %q, want ReasonAmbiguous -- all three candidates share Tier and Distance", got.Reason)
	}
	if len(got.Alternatives) != 2 {
		t.Fatalf("vat Alternatives = %+v, want exactly 2 -- a three-way tie keeps every peer but the decided one", got.Alternatives)
	}
	if got.Value == nil {
		t.Fatal("vat Value = nil, want one of the three tied readings")
	}
	seen := map[string]bool{*got.Value: true}
	altVals := make([]string, len(got.Alternatives))
	for i, alt := range got.Alternatives {
		if alt.Value == nil {
			t.Fatalf("vat alternative %d Value = nil", i)
		}
		altVals[i] = *alt.Value
		if seen[*alt.Value] {
			t.Errorf("vat alternative Value %q repeats a value already accounted for", *alt.Value)
		}
		seen[*alt.Value] = true
	}
	if len(altVals) == 2 && altVals[0] >= altVals[1] {
		t.Errorf("vat Alternatives = %v, want comparator (ascending value) order", altVals)
	}
	for _, v := range values {
		if !seen[v] {
			t.Errorf("value %q is missing from the decided field plus its alternatives %v", v, altVals)
		}
	}
}

func TestReconcile_TwoCandidatesWithOneValueIsNotAmbiguous(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandAt("total", "1500.00", extraction.TierGeneric, 0.01),
			rcCandAt("total", "1500.00", extraction.TierGeneric, 0.01),
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "total")
	if !ok {
		t.Fatalf(`"total" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("total reason = %q, want ReasonNone -- two equal-standing candidates sharing one value are one answer, not an ambiguity", got.Reason)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("total Alternatives = %+v, want empty", got.Alternatives)
	}
	if got.Value == nil || *got.Value != "1500.00" {
		t.Errorf("total Value = %v, want \"1500.00\"", got.Value)
	}
}

func TestReconcile_AlternativesAreNeverNil(t *testing.T) {
	in := extraction.Input{Candidates: rcAllHeaderCandidates()}
	results := extraction.Reconcile(in)
	if len(results) == 0 {
		t.Fatal("Reconcile returned zero results; the loop below would be vacuous")
	}
	for _, name := range extraction.HeaderFields {
		got, ok := rcFind(results, name)
		if !ok {
			t.Fatalf("%q not found in %+v", name, results)
		}
		if got.Alternatives == nil {
			t.Errorf("%s Alternatives is nil, want a non-nil (possibly empty) slice", name)
		}
	}
}

func TestReconcile_AnAlternativeCarriesNoReason(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandAt("total", "1000.00", extraction.TierGeneric, 0.01),
			rcCandAt("total", "2000.00", extraction.TierGeneric, 0.01),
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "total")
	if !ok {
		t.Fatalf(`"total" not found in %+v`, results)
	}
	if len(got.Alternatives) == 0 {
		t.Fatal("total Alternatives is empty; the ambiguous fixture above should have produced at least one")
	}
	for i, alt := range got.Alternatives {
		if alt.Reason != extraction.ReasonNone {
			t.Errorf("alternative %d reason = %q, want ReasonNone -- an alternative never carries a reason", i, alt.Reason)
		}
	}
}

func TestReconcile_EveryReasonIsDeclared(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandidate("invoice_number", "INV-001"),
			rcCandAt("vat", "75.00", extraction.TierGeneric, 0.01),
			rcCandAt("vat", "80.00", extraction.TierGeneric, 0.01),
			rcCandidate("subtotal", "1000.00"),
		},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("500.00"), LineTotal: rcStr("500.00")},
		},
	}
	results := extraction.Reconcile(in)
	if len(results) == 0 {
		t.Fatal("Reconcile returned zero results; the checks below would be vacuous")
	}

	clean, ok := rcFind(results, "invoice_number")
	if !ok {
		t.Fatalf(`"invoice_number" not found in %+v`, results)
	}
	if clean.Reason != extraction.ReasonNone {
		t.Errorf("invoice_number reason = %q, want ReasonNone", clean.Reason)
	}

	missing, ok := rcFind(results, "issue_date")
	if !ok {
		t.Fatalf(`"issue_date" not found in %+v`, results)
	}
	if missing.Reason != extraction.ReasonMissing {
		t.Errorf("issue_date reason = %q, want ReasonMissing", missing.Reason)
	}

	ambiguous, ok := rcFind(results, "vat")
	if !ok {
		t.Fatalf(`"vat" not found in %+v`, results)
	}
	if ambiguous.Reason != extraction.ReasonAmbiguous {
		t.Errorf("vat reason = %q, want ReasonAmbiguous", ambiguous.Reason)
	}

	inconsistent, ok := rcFind(results, "subtotal")
	if !ok {
		t.Fatalf(`"subtotal" not found in %+v`, results)
	}
	if inconsistent.Reason != extraction.ReasonInconsistent {
		t.Errorf("subtotal reason = %q, want ReasonInconsistent -- line sum 500.00 vs printed 1000.00", inconsistent.Reason)
	}

	for _, r := range results {
		if !rcIsValidReason(r.Reason) {
			t.Errorf("%s reason = %q, not a member of the closed set", r.Name, r.Reason)
		}
	}
}

func TestReconcile_AMissingSubtotalIsNotRewrittenToInconsistent(t *testing.T) {
	in := extraction.Input{
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1500.00"), LineTotal: rcStr("1500.00")},
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "subtotal")
	if !ok {
		t.Fatalf(`"subtotal" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonMissing {
		t.Errorf("subtotal reason = %q, want ReasonMissing -- zero subtotal candidates means missing is never rewritten to inconsistent", got.Reason)
	}
}

func TestReconcile_AnAmbiguousSubtotalIsNotRewrittenToInconsistent(t *testing.T) {
	in := extraction.Input{
		Candidates: []extraction.Candidate{
			rcCandAt("subtotal", "1000.00", extraction.TierGeneric, 0.01),
			rcCandAt("subtotal", "2000.00", extraction.TierGeneric, 0.01),
		},
		Lines: []extraction.DocLine{
			{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("1500.00"), LineTotal: rcStr("1500.00")},
		},
	}
	results := extraction.Reconcile(in)
	got, ok := rcFind(results, "subtotal")
	if !ok {
		t.Fatalf(`"subtotal" not found in %+v`, results)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("subtotal reason = %q, want ReasonAmbiguous -- neither candidate matches the line sum, but ambiguous must never be rewritten to inconsistent", got.Reason)
	}
}
