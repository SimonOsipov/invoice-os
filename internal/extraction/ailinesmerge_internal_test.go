// ailinesmerge_internal_test.go: acceptance specs for mergeAILines' per-cell table and the
// AC-13/14 page-presence check. Reuses aliStr (ailines_internal_test.go), mgStr
// (reader_merge_internal_test.go) and tok/onePage (aitext_rules_internal_test.go).
package extraction

import (
	"reflect"
	"sort"
	"testing"
)

func findRow(t *testing.T, rows []FieldResult, name string) FieldResult {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row named %q in output", name)
	return FieldResult{}
}

func findLineRow(t *testing.T, rows []FieldResult, index int, role string) FieldResult {
	t.Helper()
	return findRow(t, rows, LineFieldName(index, role))
}

func TestMergeAILines_NilAIReturnsRowsUnchanged(t *testing.T) {
	rows := []FieldResult{
		{Field: Field{Name: "invoice_number", Value: mgStr("INV-1"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "line_items", Reason: ReasonMissing}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(5, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
	}

	got := mergeAILines(rows, nil, nil)

	if !reflect.DeepEqual(got, rows) {
		t.Errorf("mergeAILines(rows, nil, nil) = %+v, want rows unchanged %+v", got, rows)
	}
}

func TestMergeAILines_EmptyAIReturnsRowsUnchanged(t *testing.T) {
	rows := []FieldResult{
		{Field: Field{Name: "invoice_number", Value: mgStr("INV-1"), Reason: ReasonNone}, Alternatives: []Field{}},
	}

	got := mergeAILines(rows, []AILine{}, nil)

	if !reflect.DeepEqual(got, rows) {
		t.Errorf("mergeAILines(rows, []AILine{}, nil) = %+v, want rows unchanged %+v", got, rows)
	}
}

func TestMergeAILines_NonLineRowsSurviveByteIdenticalInPosition(t *testing.T) {
	header1 := FieldResult{Field: Field{Name: "invoice_number", Value: mgStr("INV-1"), Reason: ReasonNone}, Alternatives: []Field{}}
	header2 := FieldResult{Field: Field{Name: "total", Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}}
	block := FieldResult{Field: Field{Name: "line_items", Reason: ReasonNone}, Alternatives: []Field{}}
	rows := []FieldResult{
		header1,
		header2,
		block,
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00")}}
	pages := onePage(1, tok("Widget 30.00", 1, 0.10, 0.10, 0.90, 0.12))

	got := mergeAILines(rows, ai, pages)

	if len(got) < 3 {
		t.Fatalf("len(got) = %d, want at least 3 non-line rows", len(got))
	}
	if !reflect.DeepEqual(got[0], header1) {
		t.Errorf("got[0] = %+v, want header1 unchanged %+v", got[0], header1)
	}
	if !reflect.DeepEqual(got[1], header2) {
		t.Errorf("got[1] = %+v, want header2 unchanged %+v", got[1], header2)
	}
	if !reflect.DeepEqual(got[2], block) {
		t.Errorf("got[2] = %+v, want block row unchanged %+v", got[2], block)
	}
}

func TestMergeAILines_AnAgreeingCellKeepsTheEngineRowUnchanged(t *testing.T) {
	region := &Region{Page: 1, X0: 0.10, Y0: 0.50, X1: 0.90, Y1: 0.52}
	engineTotal := FieldResult{
		Field:        Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Region: region, Reason: ReasonInconsistent},
		Alternatives: []Field{},
	}
	rows := []FieldResult{
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		engineTotal,
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00")}}
	// real pages: the agreement must be why the row survives, not the page check
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.40, 0.12), tok("30.00", 1, 0.60, 0.10, 0.80, 0.12))

	got := mergeAILines(rows, ai, pages)

	gotTotal := findLineRow(t, got, 1, LineRoleLineTotal)
	if !reflect.DeepEqual(gotTotal, engineTotal) {
		t.Errorf("line_total = %+v, want unchanged %+v (ReasonInconsistent must survive)", gotTotal, engineTotal)
	}
}

func TestMergeAILines_AnEngineAbsentCellTheAIAnsweredIsWrittenUnmarked(t *testing.T) {
	rows := []FieldResult{
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00"), UnitPrice: aliStr("30.00")}}
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.40, 0.12), tok("30.00", 1, 0.60, 0.10, 0.80, 0.12))

	got := mergeAILines(rows, ai, pages)

	unitPrice := findLineRow(t, got, 1, LineRoleUnitPrice)
	if unitPrice.Value == nil || *unitPrice.Value != "30.00" {
		t.Errorf("unit_price value = %v, want 30.00", unitPrice.Value)
	}
	if unitPrice.Region != nil {
		t.Errorf("unit_price region = %+v, want nil", unitPrice.Region)
	}
	if unitPrice.Reason != ReasonNone {
		t.Errorf("unit_price reason = %q, want ReasonNone specifically", unitPrice.Reason)
	}
	if len(unitPrice.Alternatives) != 0 {
		t.Errorf("unit_price alternatives = %+v, want empty", unitPrice.Alternatives)
	}
}

func TestMergeAILines_ADisagreeingCellKeepsTheEngineValueAsAnAmbiguousAlternative(t *testing.T) {
	region := &Region{Page: 1, X0: 0.60, Y0: 0.10, X1: 0.80, Y1: 0.12}
	rows := []FieldResult{
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleUnitPrice), Value: mgStr("30.00"), Region: region, Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00"), UnitPrice: aliStr("29.00")}}
	pages := onePage(1,
		tok("Widget", 1, 0.10, 0.10, 0.40, 0.12),
		tok("30.00", 1, 0.60, 0.10, 0.80, 0.12),
		tok("29.00", 1, 0.60, 0.20, 0.80, 0.22),
	)

	got := mergeAILines(rows, ai, pages)

	unitPrice := findLineRow(t, got, 1, LineRoleUnitPrice)
	if unitPrice.Value == nil || *unitPrice.Value != "30.00" {
		t.Errorf("unit_price value = %v, want the engine's 30.00 to stand at rank 0", unitPrice.Value)
	}
	if unitPrice.Region != region {
		t.Errorf("unit_price region = %v, want the engine's own region", unitPrice.Region)
	}
	if unitPrice.Reason != ReasonAmbiguous {
		t.Errorf("unit_price reason = %q, want ReasonAmbiguous", unitPrice.Reason)
	}
	if len(unitPrice.Alternatives) != 1 {
		t.Fatalf("unit_price alternatives = %+v, want exactly one", unitPrice.Alternatives)
	}
	alt := unitPrice.Alternatives[0]
	if alt.Value == nil || *alt.Value != "29.00" {
		t.Errorf("alternative value = %v, want the AI's 29.00", alt.Value)
	}
	if alt.Region != nil {
		t.Errorf("alternative region = %+v, want nil", alt.Region)
	}
}

func TestMergeAILines_AHallucinatedDisagreementLeavesTheEngineCellUnflagged(t *testing.T) {
	region := &Region{Page: 1, X0: 0.60, Y0: 0.10, X1: 0.80, Y1: 0.12}
	rows := []FieldResult{
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleUnitPrice), Value: mgStr("30.00"), Region: region, Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00"), UnitPrice: aliStr("29.00")}}
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.40, 0.12), tok("30.00", 1, 0.60, 0.10, 0.80, 0.12))

	got := mergeAILines(rows, ai, pages)

	unitPrice := findLineRow(t, got, 1, LineRoleUnitPrice)
	if unitPrice.Reason != ReasonNone {
		t.Errorf("unit_price reason = %q, want ReasonNone: an off-page AI value must not flag a good engine cell", unitPrice.Reason)
	}
	if len(unitPrice.Alternatives) != 0 {
		t.Errorf("unit_price alternatives = %+v, want none: a hallucinated candidate must not reach the user", unitPrice.Alternatives)
	}
}

func TestMergeAILines_AnInventedAIRowEmitsEveryAnsweredRoleUnmarked(t *testing.T) {
	rows := []FieldResult{
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{
		{Description: aliStr("Widget"), LineTotal: aliStr("30.00")},
		{Description: aliStr("Gadget"), Quantity: aliStr("2"), UnitPrice: aliStr("5.00"), LineTotal: aliStr("10.00")}, // LineTax left null
	}
	pages := onePage(1,
		tok("Widget", 1, 0.10, 0.10, 0.40, 0.12),
		tok("30.00", 1, 0.60, 0.10, 0.80, 0.12),
		tok("Gadget", 1, 0.10, 0.20, 0.40, 0.22),
		tok("2", 1, 0.45, 0.20, 0.50, 0.22),
		tok("5.00", 1, 0.55, 0.20, 0.70, 0.22),
		tok("10.00", 1, 0.75, 0.20, 0.90, 0.22),
	)

	got := mergeAILines(rows, ai, pages)

	var invented []FieldResult
	for _, r := range got {
		if idx, _, ok := ParseLineFieldName(r.Name); ok && idx == 2 {
			invented = append(invented, r)
		}
	}
	if len(invented) != 4 {
		t.Fatalf("invented row cell count = %d, want 4 (description, quantity, unit_price, line_total; line_tax left null)", len(invented))
	}
	for _, r := range invented {
		if r.Reason != ReasonNone {
			t.Errorf("%s reason = %q, want ReasonNone", r.Name, r.Reason)
		}
		if r.Region != nil {
			t.Errorf("%s region = %+v, want nil", r.Name, r.Region)
		}
		if _, role, _ := ParseLineFieldName(r.Name); role == LineRoleLineTax {
			t.Errorf("line_tax row present for a cell the AI left null")
		}
	}
}

func TestMergeAILines_ADroppedEngineRowIsUnchanged(t *testing.T) {
	serviceFeeDesc := FieldResult{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Service fee"), Reason: ReasonNone}, Alternatives: []Field{}}
	serviceFeeTotal := FieldResult{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("99.00"), Reason: ReasonNone}, Alternatives: []Field{}}
	rows := []FieldResult{
		serviceFeeDesc,
		serviceFeeTotal,
		{Field: Field{Name: LineFieldName(2, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(2, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00")}} // never mentions the service fee row

	got := mergeAILines(rows, ai, nil)

	// The service fee is printed first, so it keeps index 1 after renumbering.
	gotDesc := findLineRow(t, got, 1, LineRoleDescription)
	gotTotal := findLineRow(t, got, 1, LineRoleLineTotal)
	if !reflect.DeepEqual(gotDesc, serviceFeeDesc) {
		t.Errorf("dropped description = %+v, want unchanged %+v", gotDesc, serviceFeeDesc)
	}
	if !reflect.DeepEqual(gotTotal, serviceFeeTotal) {
		t.Errorf("dropped line_total = %+v, want unchanged %+v", gotTotal, serviceFeeTotal)
	}
}

func TestMergeAILines_RenumberingIsContiguousAndPreservesEngineCellContent(t *testing.T) {
	serviceFeeDesc := FieldResult{Field: Field{Name: LineFieldName(3, LineRoleDescription), Value: mgStr("Service fee"), Reason: ReasonNone}, Alternatives: []Field{}}
	serviceFeeTotal := FieldResult{Field: Field{Name: LineFieldName(3, LineRoleLineTotal), Value: mgStr("99.00"), Reason: ReasonNone}, Alternatives: []Field{}}
	widgetDesc := FieldResult{Field: Field{Name: LineFieldName(7, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}}
	widgetTotal := FieldResult{Field: Field{Name: LineFieldName(7, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}}
	rows := []FieldResult{serviceFeeDesc, serviceFeeTotal, widgetDesc, widgetTotal}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00")}}

	got := mergeAILines(rows, ai, nil)

	seen := map[int]bool{}
	var indexes []int
	for _, r := range got {
		idx, _, ok := ParseLineFieldName(r.Name)
		if !ok || seen[idx] {
			continue
		}
		seen[idx] = true
		indexes = append(indexes, idx)
	}
	if len(indexes) != 2 {
		t.Fatalf("distinct line indexes = %v, want exactly 2", indexes)
	}
	sort.Ints(indexes)
	for i, idx := range indexes {
		if idx != i+1 {
			t.Fatalf("indexes = %v, want contiguous from 1", indexes)
		}
	}

	type triple struct {
		value  string
		region *Region
		reason Reason
	}
	wantSet := map[triple]int{}
	for _, r := range []FieldResult{serviceFeeDesc, serviceFeeTotal, widgetDesc, widgetTotal} {
		wantSet[triple{*r.Value, r.Region, r.Reason}]++
	}
	gotSet := map[triple]int{}
	for _, r := range got {
		if _, _, ok := ParseLineFieldName(r.Name); !ok {
			continue
		}
		gotSet[triple{*r.Value, r.Region, r.Reason}]++
	}
	if !reflect.DeepEqual(gotSet, wantSet) {
		t.Errorf("(value,region,reason) multiset = %v, want %v (renumbering must not touch cell content)", gotSet, wantSet)
	}
}

func TestMergeAILines_ABlockRowSurvivesExactlyAsReconcileSet(t *testing.T) {
	block := FieldResult{Field: Field{Name: "line_items", Reason: ReasonMissing}, Alternatives: []Field{}}
	rows := []FieldResult{
		block,
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00"), UnitPrice: aliStr("30.00")}}
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.40, 0.12), tok("30.00", 1, 0.60, 0.10, 0.80, 0.12))

	got := mergeAILines(rows, ai, pages)

	// the fixture must actually fill a cell in, or AC-11's "over AI-filled cells" is untested
	_ = findLineRow(t, got, 1, LineRoleUnitPrice)
	gotBlock := findRow(t, got, "line_items")
	if !reflect.DeepEqual(gotBlock, block) {
		t.Errorf("line_items block = %+v, want unchanged %+v", gotBlock, block)
	}
}

func TestMergeAILines_TrailingZeroAmountsAgreeNotAmbiguous(t *testing.T) {
	region := &Region{Page: 1, X0: 0.60, Y0: 0.10, X1: 0.80, Y1: 0.12}
	engineTotal := FieldResult{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("700.00"), Region: region, Reason: ReasonNone}, Alternatives: []Field{}}
	rows := []FieldResult{
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		engineTotal,
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("700")}}
	// real pages: the trailing-zero reading must be why the row survives, not the page check
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.40, 0.12), tok("700", 1, 0.60, 0.10, 0.80, 0.12))

	got := mergeAILines(rows, ai, pages)

	gotTotal := findLineRow(t, got, 1, LineRoleLineTotal)
	if gotTotal.Reason == ReasonAmbiguous {
		t.Errorf("line_total reason = ReasonAmbiguous, want unchanged: 700 and 700.00 must agree")
	}
	if !reflect.DeepEqual(gotTotal, engineTotal) {
		t.Errorf("line_total = %+v, want unchanged %+v", gotTotal, engineTotal)
	}
}

func TestAliLinePresent_AFormattingDifferenceStillCounts(t *testing.T) {
	pages := onePage(1, tok("1,250.00", 1, 0.10, 0.10, 0.40, 0.12))
	if !aliLinePresent(LineRoleUnitPrice, "1250.00", pages) {
		t.Error("1250.00 not found against a comma-grouped 1,250.00 on the page")
	}
}

func TestAliLinePresent_ADifferentAmountIsNotPresent(t *testing.T) {
	pages := onePage(1, tok("1,250.00", 1, 0.10, 0.10, 0.40, 0.12))
	if aliLinePresent(LineRoleUnitPrice, "1251.00", pages) {
		t.Error("1251.00 reported present against an unrelated 1,250.00 on the page")
	}
}

func TestAliLinePresent_ADescriptionMatchesCaseAndSpacingLoosely(t *testing.T) {
	pages := onePage(1, tok("premium  WIDGET", 1, 0.10, 0.10, 0.60, 0.12))
	if !aliLinePresent(LineRoleDescription, "Premium Widget", pages) {
		t.Error("Premium Widget not found against premium  WIDGET on the page")
	}
}

func TestAliLinePresent_ABlankPageRefusesEveryRole(t *testing.T) {
	if aliLinePresent(LineRoleLineTotal, "30.00", nil) {
		t.Error("30.00 reported present with no pages at all")
	}
}

func TestMergeAILines_AHallucinatedUnitPriceIsRefusedAndNotWritten(t *testing.T) {
	rows := []FieldResult{
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{{Description: aliStr("Widget"), LineTotal: aliStr("30.00"), UnitPrice: aliStr("999.99")}}
	pages := onePage(1, tok("Widget", 1, 0.10, 0.10, 0.40, 0.12), tok("30.00", 1, 0.60, 0.10, 0.80, 0.12))

	got := mergeAILines(rows, ai, pages)

	for _, r := range got {
		if r.Name == LineFieldName(1, LineRoleUnitPrice) {
			t.Fatalf("unit_price row written from a hallucinated value not present on the page: %+v", r)
		}
	}
}

func TestMergeAILines_AnInventedRowFullyRefusedByThePageCheckConsumesNoIndex(t *testing.T) {
	rows := []FieldResult{
		{Field: Field{Name: LineFieldName(1, LineRoleDescription), Value: mgStr("Widget"), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("30.00"), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	ai := []AILine{
		{Description: aliStr("Widget"), LineTotal: aliStr("30.00")},      // pairs with the engine row
		{Description: aliStr("Ghost item"), LineTotal: aliStr("777.00")}, // nothing on the page names this row
		{Description: aliStr("Gadget"), LineTotal: aliStr("10.00")},      // a real invented row, on the page
	}
	pages := onePage(1,
		tok("Widget", 1, 0.10, 0.10, 0.40, 0.12),
		tok("30.00", 1, 0.60, 0.10, 0.80, 0.12),
		tok("Gadget", 1, 0.10, 0.20, 0.40, 0.22),
		tok("10.00", 1, 0.60, 0.20, 0.80, 0.22),
	)

	got := mergeAILines(rows, ai, pages)

	indexes := map[int]bool{}
	for _, r := range got {
		if idx, _, ok := ParseLineFieldName(r.Name); ok {
			indexes[idx] = true
		}
	}
	if len(indexes) != 2 || !indexes[1] || !indexes[2] {
		t.Fatalf("line indexes = %v, want exactly {1,2}: the fully refused ghost row must not open a gap", indexes)
	}
	gadget := findLineRow(t, got, 2, LineRoleDescription)
	if gadget.Value == nil || *gadget.Value != "Gadget" {
		t.Errorf("index 2 description = %v, want Gadget renumbered past the refused ghost row", gadget.Value)
	}
}
