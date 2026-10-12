// aimerge_internal_test.go: acceptance specs for AIR-03-02's mergeAI table (AC-3) and its
// arithmetic pass on a merge-decided subtotal/total. Reuses recordingAI, tok, onePage
// (aireading_internal_test.go, aitext_rules_internal_test.go) and mgStr (reader_merge_internal_test.go).
package extraction

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
)

// mergeWith runs the merge through askAI, as the worker will (AIR-03-03): the fake AIReader
// answers ans, and checkAI reads pages for every non-blank answer.
func mergeWith(engine []FieldResult, ans map[string]any, pages []TokenPage, lines []DocLine) []FieldResult {
	stub := &recordingAI{enabled: true, answer: ans}
	answer, _ := askAI(context.Background(), stub, pages)
	return mergeAI(engine, answer, pages, lines)
}

// cloneResults deep-copies rows so a later comparison is against the state before the call, not
// a value mergeAI may have mutated in place.
func cloneResults(rows []FieldResult) []FieldResult {
	out := make([]FieldResult, len(rows))
	for i, r := range rows {
		out[i] = r
		if r.Value != nil {
			v := *r.Value
			out[i].Value = &v
		}
		if r.Region != nil {
			rg := *r.Region
			out[i].Region = &rg
		}
		alts := make([]Field, len(r.Alternatives))
		for j, a := range r.Alternatives {
			alts[j] = a
			if a.Value != nil {
				v := *a.Value
				alts[j].Value = &v
			}
			if a.Region != nil {
				rg := *a.Region
				alts[j].Region = &rg
			}
		}
		out[i].Alternatives = alts
	}
	return out
}

func TestMergeAI_Row1_AnAgreeingValueKeepsTheEngineResult(t *testing.T) {
	regionE := &Region{Page: 1, X0: 0.60, Y0: 0.80, X1: 0.80, Y1: 0.82}
	engine := []FieldResult{
		{Field: Field{Name: "total", Value: mgStr("1935.00"), Region: regionE, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}}, // control: must still decide on a real answer
	}
	pages := onePage(1,
		tok("Total: 1,935.00", 1, 0.10, 0.10, 0.40, 0.12),
		tok("VAT: 135.00", 1, 0.10, 0.20, 0.40, 0.22),
	)
	want := cloneResults(engine)

	got := mergeWith(engine, map[string]any{"total": "1,935.00", "vat": "135.00"}, pages, nil)

	if !reflect.DeepEqual(got[0], want[0]) {
		t.Errorf("total = %+v, want unchanged %+v", got[0], want[0])
	}
	wantVATRegion := Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.40, Y1: 0.22}
	wantVAT := FieldResult{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: &wantVATRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[1], wantVAT) {
		t.Errorf("control vat = %+v, want decided %+v", got[1], wantVAT)
	}
}

func TestMergeAI_Row1_AgreementIgnoresRepeatedSpaces(t *testing.T) {
	region := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.12}
	engine := []FieldResult{
		{Field: Field{Name: "buyer_name", Value: mgStr("ACME  LTD"), Region: region, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1,
		tok("ACME  LTD", 1, 0.50, 0.10, 0.80, 0.12),
		tok("VAT: 135.00", 1, 0.10, 0.20, 0.40, 0.22),
	)
	want := cloneResults(engine)

	got := mergeWith(engine, map[string]any{"buyer_name": "ACME LTD", "vat": "135.00"}, pages, nil)

	if !reflect.DeepEqual(got[0], want[0]) {
		t.Errorf("buyer_name = %+v, want unchanged %+v (collapse must ignore the repeated space)", got[0], want[0])
	}
	wantVATRegion := Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.40, Y1: 0.22}
	wantVAT := FieldResult{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: &wantVATRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[1], wantVAT) {
		t.Errorf("control vat = %+v, want decided %+v", got[1], wantVAT)
	}
}

func TestMergeAI_Row2_ADisagreementIsAmbiguousWithBothChoices(t *testing.T) {
	region := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.12}
	engine := []FieldResult{
		{Field: Field{Name: "buyer_tin", Value: mgStr("12345678-0001"), Region: region, Reason: ReasonNone}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Supplier TIN: 87654321-0002", 1, 0.50, 0.10, 0.90, 0.12))

	got := mergeWith(engine, map[string]any{"buyer_tin": "87654321-0002"}, pages, nil)

	wantAltRegion := Region{Page: 1, X0: 0.50, Y0: 0.10, X1: 0.90, Y1: 0.12}
	want := FieldResult{
		Field:        Field{Name: "buyer_tin", Value: mgStr("12345678-0001"), Region: region, Reason: ReasonAmbiguous},
		Alternatives: []Field{{Name: "buyer_tin", Value: mgStr("87654321-0002"), Region: &wantAltRegion, Reason: ReasonNone}},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("buyer_tin = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_Row3_TheAISettlesAnEngineTieItMatches(t *testing.T) {
	r1 := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	r2 := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
	engine := []FieldResult{
		{
			Field:        Field{Name: "issue_date", Value: mgStr("2026-03-12"), Region: r1, Reason: ReasonAmbiguous},
			Alternatives: []Field{{Name: "issue_date", Value: mgStr("2026-12-03"), Region: r2, Reason: ReasonNone}},
		},
	}
	// the token's own box differs from r2, so a Region == r2 assertion proves the reading's own
	// region was kept, not the page's.
	pages := onePage(1, tok("Date 2026-12-03", 1, 0.60, 0.40, 0.90, 0.42))

	got := mergeWith(engine, map[string]any{"issue_date": "2026-12-03"}, pages, nil)

	want := FieldResult{Field: Field{Name: "issue_date", Value: mgStr("2026-12-03"), Region: r2, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("issue_date = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_Row4_ANewValueJoinsAnEngineTie(t *testing.T) {
	ra := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	rb := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
	alts := make([]Field, 1, 8)
	alts[0] = Field{Name: "buyer_tin", Value: mgStr("87654321-0002"), Region: rb, Reason: ReasonNone}
	engine := []FieldResult{
		{Field: Field{Name: "buyer_tin", Value: mgStr("12345678-0001"), Region: ra, Reason: ReasonAmbiguous}, Alternatives: alts},
	}
	pages := onePage(1, tok("TIN 22223333-4444", 1, 0.50, 0.10, 0.80, 0.12))

	got := mergeWith(engine, map[string]any{"buyer_tin": "22223333-4444"}, pages, nil)

	if len(alts) != 1 {
		t.Fatalf("input Alternatives grew to %d in place, want 1 (mergeAI must clone before appending)", len(alts))
	}
	if got[0].Value == nil || *got[0].Value != "12345678-0001" || got[0].Reason != ReasonAmbiguous {
		t.Errorf("head = %+v, want unchanged 12345678-0001/ambiguous", got[0].Field)
	}
	wantRc := Region{Page: 1, X0: 0.50, Y0: 0.10, X1: 0.80, Y1: 0.12}
	wantAlts := []Field{
		{Name: "buyer_tin", Value: mgStr("87654321-0002"), Region: rb, Reason: ReasonNone},
		{Name: "buyer_tin", Value: mgStr("22223333-4444"), Region: &wantRc, Reason: ReasonNone},
	}
	if !reflect.DeepEqual(got[0].Alternatives, wantAlts) {
		t.Errorf("Alternatives = %+v, want %+v", got[0].Alternatives, wantAlts)
	}
}

func TestMergeAI_Row4_AFullTieTakesNoNinthChoice(t *testing.T) {
	tinAt := func(i int) *Field {
		return &Field{
			Name:   "supplier_tin",
			Value:  mgStr(fmt.Sprintf("1000000%d-000%d", i, i)),
			Region: &Region{Page: 1, X0: 0.1, Y0: 0.1 + float64(i)*0.02, X1: 0.3, Y1: 0.12 + float64(i)*0.02},
			Reason: ReasonNone,
		}
	}
	head := &Region{Page: 1, X0: 0.1, Y0: 0.5, X1: 0.3, Y1: 0.52}
	pages := onePage(1, tok("TIN 20000000-0001", 1, 0.6, 0.1, 0.9, 0.12))

	// main: 8 readings already -- no room for a ninth.
	full := make([]Field, 7, 8)
	for i := range full {
		full[i] = *tinAt(i)
	}
	engineFull := []FieldResult{
		{Field: Field{Name: "supplier_tin", Value: mgStr("19999999-0009"), Region: head, Reason: ReasonAmbiguous}, Alternatives: full},
	}
	wantFull := cloneResults(engineFull)

	gotFull := mergeWith(engineFull, map[string]any{"supplier_tin": "20000000-0001"}, pages, nil)
	if !reflect.DeepEqual(gotFull, wantFull) {
		t.Errorf("full 8-reading tie changed: got %+v, want unchanged %+v", gotFull, wantFull)
	}

	// control: with room for one more (7 readings), the same kind of new value IS appended.
	room := make([]Field, 6, 8)
	for i := range room {
		room[i] = *tinAt(i)
	}
	engineRoom := []FieldResult{
		{Field: Field{Name: "supplier_tin", Value: mgStr("19999999-0009"), Region: head, Reason: ReasonAmbiguous}, Alternatives: room},
	}

	gotRoom := mergeWith(engineRoom, map[string]any{"supplier_tin": "20000000-0001"}, pages, nil)
	if len(room) != 6 {
		t.Fatalf("input Alternatives grew to %d in place, want 6", len(room))
	}
	if len(gotRoom[0].Alternatives) != 7 {
		t.Fatalf("Alternatives length = %d, want 7 (appended the 8th reading)", len(gotRoom[0].Alternatives))
	}
	last := gotRoom[0].Alternatives[6]
	if last.Value == nil || *last.Value != "20000000-0001" {
		t.Errorf("appended alternative = %+v, want value 20000000-0001", last)
	}
}

func TestMergeAI_Row5_AValueOnlyTheAIReadIsDecided(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "invoice_number", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Invoice Number: 20417", 1, 0.10, 0.10, 0.40, 0.12))

	got := mergeWith(engine, map[string]any{"invoice_number": "20417"}, pages, nil)

	wantRegion := Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.12}
	want := FieldResult{Field: Field{Name: "invoice_number", Value: mgStr("20417"), Region: &wantRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("invoice_number = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_Row6_ABlankAnswerChangesNothing(t *testing.T) {
	regionD := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	regionA := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
	regionB := &Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.30, Y1: 0.32}
	regionC := &Region{Page: 1, X0: 0.10, Y0: 0.40, X1: 0.30, Y1: 0.42}
	engine := []FieldResult{
		{Field: Field{Name: "total", Value: mgStr("1935.00"), Region: regionD, Reason: ReasonNone}, Alternatives: []Field{}},
		{
			Field:        Field{Name: "issue_date", Value: mgStr("2026-03-12"), Region: regionA, Reason: ReasonAmbiguous},
			Alternatives: []Field{{Name: "issue_date", Value: mgStr("2026-12-03"), Region: regionB, Reason: ReasonNone}},
		},
		{Field: Field{Name: "buyer_tin", Reason: ReasonMissing}, Alternatives: []Field{}},
		{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: regionC, Reason: ReasonInconsistent}, Alternatives: []Field{}},
	}
	pages := onePage(1,
		tok("Total: 1,935.00", 1, 0.50, 0.10, 0.80, 0.12),
		tok("Date 2026-12-03", 1, 0.50, 0.20, 0.80, 0.22),
		tok("TIN 99999999-1202", 1, 0.50, 0.30, 0.80, 0.32),
		tok("Subtotal: 1900.00", 1, 0.50, 0.40, 0.80, 0.42),
	)
	want := cloneResults(engine)

	blank := []map[string]any{
		{"total": nil, "issue_date": nil, "buyer_tin": nil, "subtotal": nil},
		{"total": "  ", "issue_date": "  ", "buyer_tin": "  ", "subtotal": "  "},
		{},
	}
	for i, ans := range blank {
		got := mergeWith(engine, ans, pages, nil)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("blank case %d: got %+v, want unchanged %+v", i, got, want)
		}
	}

	// askAI returns no answer on any error, and flags this one.
	errStub := &recordingAI{enabled: true, answer: map[string]any{"total": "1935.00"}, err: ai.ErrUnavailable}
	errAnswer, failed := askAI(context.Background(), errStub, pages)
	if !failed {
		t.Errorf("askAI(ErrUnavailable) flag = false, want true")
	}
	gotErr := mergeAI(engine, errAnswer, pages, nil)
	if !reflect.DeepEqual(gotErr, want) {
		t.Errorf("stub error case: got %+v, want unchanged %+v", gotErr, want)
	}

	// mergeAI's own blank guard, bypassing askAI's filtering entirely.
	gotDirect := mergeAI(engine, map[string]string{"total": "  "}, pages, nil)
	if !reflect.DeepEqual(gotDirect, want) {
		t.Errorf("direct blank map: got %+v, want unchanged %+v", gotDirect, want)
	}

	// control: a real answer on the same engine DOES change a row, proving this test can fail.
	gotControl := mergeWith(engine, map[string]any{"buyer_tin": "99999999-1202"}, pages, nil)
	wantControlRegion := Region{Page: 1, X0: 0.50, Y0: 0.30, X1: 0.80, Y1: 0.32}
	wantControl := FieldResult{Field: Field{Name: "buyer_tin", Value: mgStr("99999999-1202"), Region: &wantControlRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(gotControl[2], wantControl) {
		t.Errorf("control buyer_tin = %+v, want decided %+v", gotControl[2], wantControl)
	}
}

func TestMergeAI_Row7_AFailedValueLeavesADecidedFieldAlone(t *testing.T) {
	region := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.12}
	engine := []FieldResult{
		{Field: Field{Name: "buyer_name", Value: mgStr("ACME TRADING LIMITED"), Region: region, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("Account Name: ZENITH HOLDINGS LIMITED", 1, 0.50, 0.10, 0.90, 0.12))

	cases := []struct {
		name string
		raw  string
	}{
		{"format check: 257-rune name", strings.Repeat("A", 257)},
		{"page check: not printed anywhere", "INVENTED HOLDINGS LIMITED"},
		{"payment label: after Account Name", "ZENITH HOLDINGS LIMITED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mergeWith(engine, map[string]any{"buyer_name": c.raw}, pages, nil)
			if !reflect.DeepEqual(got[0], want[0]) {
				t.Errorf("buyer_name = %+v, want unchanged %+v", got[0], want[0])
			}
		})
	}

	// control: a real vat answer on the same engine DOES decide the field.
	vatPages := onePage(1, tok("VAT: 135.00", 1, 0.10, 0.20, 0.40, 0.22))
	gotVAT := mergeWith(engine, map[string]any{"vat": "135.00"}, vatPages, nil)
	wantVATRegion := Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.40, Y1: 0.22}
	wantVAT := FieldResult{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: &wantVATRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(gotVAT[1], wantVAT) {
		t.Errorf("control vat = %+v, want decided %+v", gotVAT[1], wantVAT)
	}
}

func TestMergeAI_Row8_AFailedValueMakesAMissingFieldDoubtful(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "buyer_name", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1,
		tok("ACCOUNT NAME", 1, 0.10, 0.10, 0.30, 0.12),
		tok("ZENITH HOLDINGS LIMITED", 1, 0.10, 0.20, 0.50, 0.22),
	)

	got := mergeWith(engine, map[string]any{"buyer_name": "ZENITH HOLDINGS LIMITED"}, pages, nil)

	want := FieldResult{
		Field:        Field{Name: "buyer_name", Value: nil, Region: nil, Reason: ReasonUnreadable},
		Alternatives: []Field{{Name: "buyer_name", Value: mgStr("ZENITH HOLDINGS LIMITED"), Region: nil, Reason: ReasonNone}},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("buyer_name = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_Row8_AnyFailedCheckOffersTheText(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "buyer_tin", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Ref 9999999-1202", 1, 0.10, 0.10, 0.40, 0.12))

	got := mergeWith(engine, map[string]any{"buyer_tin": "9999999-1202"}, pages, nil)

	want := FieldResult{
		Field:        Field{Name: "buyer_tin", Value: nil, Region: nil, Reason: ReasonUnreadable},
		Alternatives: []Field{{Name: "buyer_tin", Value: mgStr("9999999-1202"), Region: nil, Reason: ReasonNone}},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("buyer_tin = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_AnAmbiguousFieldWithAFailedValueStands(t *testing.T) {
	ra := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	rb := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
	engine := []FieldResult{
		{
			Field:        Field{Name: "issue_date", Value: mgStr("2026-03-12"), Region: ra, Reason: ReasonAmbiguous},
			Alternatives: []Field{{Name: "issue_date", Value: mgStr("2026-12-03"), Region: rb, Reason: ReasonNone}},
		},
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("something else entirely", 1, 0.50, 0.10, 0.80, 0.12))

	got := mergeWith(engine, map[string]any{"issue_date": "2026-06-30"}, pages, nil)
	if !reflect.DeepEqual(got[0], want[0]) {
		t.Errorf("issue_date = %+v, want unchanged %+v", got[0], want[0])
	}

	vatPages := onePage(1, tok("VAT: 135.00", 1, 0.10, 0.20, 0.40, 0.22))
	gotVAT := mergeWith(engine, map[string]any{"vat": "135.00"}, vatPages, nil)
	wantVATRegion := Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.40, Y1: 0.22}
	wantVAT := FieldResult{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: &wantVATRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(gotVAT[1], wantVAT) {
		t.Errorf("control vat = %+v, want decided %+v", gotVAT[1], wantVAT)
	}
}

func TestMergeAI_AnInconsistentFieldStands(t *testing.T) {
	region := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	engine := []FieldResult{
		{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: region, Reason: ReasonInconsistent}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("Subtotal: 1900.00", 1, 0.50, 0.10, 0.80, 0.12))

	got := mergeWith(engine, map[string]any{"subtotal": "1900.00"}, pages, nil)
	if !reflect.DeepEqual(got[0], want[0]) {
		t.Errorf("subtotal = %+v, want unchanged %+v", got[0], want[0])
	}

	vatPages := onePage(1, tok("VAT: 135.00", 1, 0.10, 0.20, 0.40, 0.22))
	gotVAT := mergeWith(engine, map[string]any{"vat": "135.00"}, vatPages, nil)
	wantVATRegion := Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.40, Y1: 0.22}
	wantVAT := FieldResult{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: &wantVATRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(gotVAT[1], wantVAT) {
		t.Errorf("control vat = %+v, want decided %+v", gotVAT[1], wantVAT)
	}
}

func TestMergeAI_AllDigitNumberBesideAnotherEngineValueIsAmbiguous(t *testing.T) {
	region := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	engine := []FieldResult{
		{Field: Field{Name: "invoice_number", Value: mgStr("INV-0001"), Region: region, Reason: ReasonNone}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Invoice Number: 20417", 1, 0.50, 0.10, 0.80, 0.12))

	got := mergeWith(engine, map[string]any{"invoice_number": "20417"}, pages, nil)

	wantAltRegion := Region{Page: 1, X0: 0.50, Y0: 0.10, X1: 0.80, Y1: 0.12}
	want := FieldResult{
		Field:        Field{Name: "invoice_number", Value: mgStr("INV-0001"), Region: region, Reason: ReasonAmbiguous},
		Alternatives: []Field{{Name: "invoice_number", Value: mgStr("20417"), Region: &wantAltRegion, Reason: ReasonNone}},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("invoice_number = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_TheSameDigitsWithoutTheLabelStayRefused(t *testing.T) {
	region := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	engine := []FieldResult{
		{Field: Field{Name: "invoice_number", Value: mgStr("INV-0001"), Region: region, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("Ref: 20417", 1, 0.50, 0.10, 0.80, 0.12))

	got := mergeWith(engine, map[string]any{"invoice_number": "20417"}, pages, nil)
	if !reflect.DeepEqual(got[0], want[0]) {
		t.Errorf("invoice_number = %+v, want unchanged %+v", got[0], want[0])
	}

	vatPages := onePage(1, tok("VAT: 135.00", 1, 0.10, 0.20, 0.40, 0.22))
	gotVAT := mergeWith(engine, map[string]any{"vat": "135.00"}, vatPages, nil)
	wantVATRegion := Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.40, Y1: 0.22}
	wantVAT := FieldResult{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: &wantVATRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(gotVAT[1], wantVAT) {
		t.Errorf("control vat = %+v, want decided %+v", gotVAT[1], wantVAT)
	}
}

func TestMergeAI_LineRowsAndOrderAreUntouched(t *testing.T) {
	lines := []DocLine{
		{Index: 1, LineTotal: mgStr("1000.00")},
		{Index: 2, LineTotal: mgStr("800.00")},
	}
	engine := Reconcile(Input{Lines: lines})
	if len(engine) != len(HeaderFields)+3 {
		t.Fatalf("Reconcile emitted %d rows, want %d (every header field + line_items + 2 line totals)", len(engine), len(HeaderFields)+3)
	}
	wantTail := cloneResults(engine[len(HeaderFields):])

	pages := onePage(1,
		tok("2026-06-11", 1, 0.10, 0.10, 0.30, 0.12),
		tok("NGN", 1, 0.10, 0.20, 0.20, 0.22),
		tok("11112222-3333", 1, 0.10, 0.30, 0.40, 0.32),
	)
	ans := map[string]any{"issue_date": "2026-06-11", "currency": "NGN", "supplier_tin": "11112222-3333"}

	got := mergeWith(engine, ans, pages, lines)

	if len(got) != len(engine) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(engine))
	}
	for i := range got {
		if got[i].Name != engine[i].Name {
			t.Errorf("row %d name = %q, want %q (order must match)", i, got[i].Name, engine[i].Name)
		}
	}
	if !reflect.DeepEqual(got[len(HeaderFields):], wantTail) {
		t.Errorf("line rows = %+v, want untouched %+v", got[len(HeaderFields):], wantTail)
	}

	// the three answered header fields really did change -- proves this test can discriminate.
	changed := 0
	for i, name := range HeaderFields {
		if name == "issue_date" || name == "currency" || name == "supplier_tin" {
			if got[i].Reason != ReasonNone || got[i].Value == nil {
				t.Errorf("%s = %+v, want decided", name, got[i])
			}
			changed++
		}
	}
	if changed != 3 {
		t.Fatalf("test setup error: expected 3 AI-answered header fields, found %d", changed)
	}
}

func TestMergeAI_AnAISubtotalThatMissesTheLineSumIsInconsistent(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "subtotal", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	lines := []DocLine{
		{Index: 1, LineTotal: mgStr("1000.00")},
		{Index: 2, LineTotal: mgStr("800.00")},
	}
	pages := onePage(1, tok("Subtotal: 1900.00", 1, 0.10, 0.10, 0.40, 0.12))

	got := mergeWith(engine, map[string]any{"subtotal": "1900.00"}, pages, lines)

	wantRegion := Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.12}
	want := FieldResult{Field: Field{Name: "subtotal", Value: mgStr("1900.00"), Region: &wantRegion, Reason: ReasonInconsistent}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("subtotal = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_AnAISubtotalThatMatchesTheLineSumIsDecided(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "subtotal", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	lines := []DocLine{
		{Index: 1, LineTotal: mgStr("1000.00")},
		{Index: 2, LineTotal: mgStr("800.00")},
	}
	pages := onePage(1, tok("Subtotal: 1800.00", 1, 0.10, 0.10, 0.40, 0.12))

	got := mergeWith(engine, map[string]any{"subtotal": "1800.00"}, pages, lines)

	wantRegion := Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.40, Y1: 0.12}
	want := FieldResult{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: &wantRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("subtotal = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_AnAISubtotalWithNoLineTotalIsNotChecked(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "subtotal", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Subtotal: 1900.00", 1, 0.10, 0.10, 0.40, 0.12))

	got := mergeWith(engine, map[string]any{"subtotal": "1900.00"}, pages, nil)

	if got[0].Reason != ReasonNone {
		t.Errorf("Reason = %q, want none (no line total to check against)", got[0].Reason)
	}
	if got[0].Value == nil || *got[0].Value != "1900.00" {
		t.Errorf("Value = %v, want 1900.00", got[0].Value)
	}
}

func TestMergeAI_AnAITotalThatMissesSubtotalPlusVATIsInconsistent(t *testing.T) {
	subRegion := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	vatRegion := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
	engine := []FieldResult{
		{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: subRegion, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: vatRegion, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "total", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Total: 2000.00", 1, 0.10, 0.30, 0.30, 0.32))

	got := mergeWith(engine, map[string]any{"total": "2000.00"}, pages, nil)

	wantTotalRegion := Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.30, Y1: 0.32}
	want := FieldResult{Field: Field{Name: "total", Value: mgStr("2000.00"), Region: &wantTotalRegion, Reason: ReasonInconsistent}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[2], want) {
		t.Errorf("total = %+v, want %+v", got[2], want)
	}
}

func TestMergeAI_AnAITotalThatBalancesIsDecided(t *testing.T) {
	subRegion := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	vatRegion := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
	engine := []FieldResult{
		{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: subRegion, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: vatRegion, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "total", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Total: 1935.00", 1, 0.10, 0.30, 0.30, 0.32))

	got := mergeWith(engine, map[string]any{"total": "1935.00"}, pages, nil)

	wantTotalRegion := Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.30, Y1: 0.32}
	want := FieldResult{Field: Field{Name: "total", Value: mgStr("1935.00"), Region: &wantTotalRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[2], want) {
		t.Errorf("total = %+v, want %+v", got[2], want)
	}
}

func TestMergeAI_AnAITotalIsNotCheckedAgainstAnUndecidedAddend(t *testing.T) {
	t.Run("vat missing", func(t *testing.T) {
		subRegion := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
		engine := []FieldResult{
			{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: subRegion, Reason: ReasonNone}, Alternatives: []Field{}},
			{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
			{Field: Field{Name: "total", Reason: ReasonMissing}, Alternatives: []Field{}},
		}
		pages := onePage(1, tok("Total: 2000.00", 1, 0.10, 0.30, 0.30, 0.32))

		got := mergeWith(engine, map[string]any{"total": "2000.00"}, pages, nil)
		if got[2].Reason != ReasonNone {
			t.Errorf("Reason = %q, want none (vat undecided is not evidence)", got[2].Reason)
		}
	})

	t.Run("subtotal ambiguous", func(t *testing.T) {
		ra := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
		rb := &Region{Page: 1, X0: 0.10, Y0: 0.15, X1: 0.30, Y1: 0.17}
		vatRegion := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
		engine := []FieldResult{
			{
				Field:        Field{Name: "subtotal", Value: mgStr("1800.00"), Region: ra, Reason: ReasonAmbiguous},
				Alternatives: []Field{{Name: "subtotal", Value: mgStr("1700.00"), Region: rb, Reason: ReasonNone}},
			},
			{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: vatRegion, Reason: ReasonNone}, Alternatives: []Field{}},
			{Field: Field{Name: "total", Reason: ReasonMissing}, Alternatives: []Field{}},
		}
		pages := onePage(1, tok("Total: 2000.00", 1, 0.10, 0.30, 0.30, 0.32))

		got := mergeWith(engine, map[string]any{"total": "2000.00"}, pages, nil)
		if got[2].Reason != ReasonNone {
			t.Errorf("Reason = %q, want none (subtotal undecided is not evidence)", got[2].Reason)
		}
	})

	t.Run("subtotal flagged by this same merge", func(t *testing.T) {
		vatRegion := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
		engine := []FieldResult{
			{Field: Field{Name: "subtotal", Reason: ReasonMissing}, Alternatives: []Field{}},
			{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: vatRegion, Reason: ReasonNone}, Alternatives: []Field{}},
			{Field: Field{Name: "total", Reason: ReasonMissing}, Alternatives: []Field{}},
		}
		lines := []DocLine{
			{Index: 1, LineTotal: mgStr("1000.00")},
			{Index: 2, LineTotal: mgStr("800.00")},
		}
		pages := onePage(1,
			tok("Subtotal: 1900.00", 1, 0.10, 0.10, 0.30, 0.12),
			tok("Total: 2000.00", 1, 0.10, 0.30, 0.30, 0.32),
		)

		got := mergeWith(engine, map[string]any{"subtotal": "1900.00", "total": "2000.00"}, pages, lines)
		if got[0].Reason != ReasonInconsistent {
			t.Fatalf("test setup: subtotal Reason = %q, want inconsistent (this is what makes it not-evidence)", got[0].Reason)
		}
		if got[2].Reason != ReasonNone {
			t.Errorf("total Reason = %q, want none -- an already-flagged subtotal must not be read as evidence", got[2].Reason)
		}
	})
}

func TestMergeAI_TheCheckAppliesToAnEngineTieTheAISettles(t *testing.T) {
	ra := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	rb := &Region{Page: 1, X0: 0.10, Y0: 0.15, X1: 0.30, Y1: 0.17}
	engine := []FieldResult{
		{
			Field:        Field{Name: "subtotal", Value: mgStr("1800.00"), Region: ra, Reason: ReasonAmbiguous},
			Alternatives: []Field{{Name: "subtotal", Value: mgStr("1900.00"), Region: rb, Reason: ReasonNone}},
		},
	}
	lines := []DocLine{{Index: 1, LineTotal: mgStr("1800.00")}}
	pages := onePage(1, tok("Subtotal: 1900.00", 1, 0.50, 0.10, 0.80, 0.12))

	got := mergeWith(engine, map[string]any{"subtotal": "1900.00"}, pages, lines)

	want := FieldResult{Field: Field{Name: "subtotal", Value: mgStr("1900.00"), Region: rb, Reason: ReasonInconsistent}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("subtotal = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_TheCheckLeavesEngineDecidedAmountsAlone(t *testing.T) {
	totalRegion := &Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.30, Y1: 0.32}
	subRegion := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.12}
	vatRegion := &Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.30, Y1: 0.22}
	engine := []FieldResult{
		{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: subRegion, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: vatRegion, Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "total", Value: mgStr("2000.00"), Region: totalRegion, Reason: ReasonNone}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("Total: 2000.00", 1, 0.60, 0.30, 0.90, 0.32))

	got := mergeWith(engine, map[string]any{"total": "2000.00"}, pages, nil)
	if !reflect.DeepEqual(got[2], want[2]) {
		t.Errorf("engine-decided total = %+v, want untouched %+v (Q6: a Row1 agreement is never re-checked)", got[2], want[2])
	}

	// control: the same arithmetic check DOES fire when the merge itself decides the total --
	// proves this test can tell a real merge from an identity stub.
	t.Run("control merge-decided total is checked", func(t *testing.T) {
		engine := []FieldResult{
			{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: subRegion, Reason: ReasonNone}, Alternatives: []Field{}},
			{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: vatRegion, Reason: ReasonNone}, Alternatives: []Field{}},
			{Field: Field{Name: "total", Reason: ReasonMissing}, Alternatives: []Field{}},
		}
		gotC := mergeWith(engine, map[string]any{"total": "2000.00"}, pages, nil)
		if gotC[2].Reason != ReasonInconsistent {
			t.Errorf("Reason = %q, want inconsistent", gotC[2].Reason)
		}
	})
}

// --- the nine AI-only header fields ---

func mgMissing(name string) FieldResult {
	return FieldResult{Field: Field{Name: name, Reason: ReasonMissing}, Alternatives: []Field{}}
}

// mgRow returns the merged row called name.
func mgRow(t *testing.T, rows []FieldResult, name string) FieldResult {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %q row in %+v", name, rows)
	return FieldResult{}
}

func mgUnreadable(t *testing.T, r FieldResult, wantAlt string) {
	t.Helper()
	if r.Value != nil || r.Reason != ReasonUnreadable {
		t.Errorf("%s = value %v reason %q, want nil / unreadable", r.Name, r.Value, r.Reason)
	}
	if len(r.Alternatives) != 1 || r.Alternatives[0].Value == nil || *r.Alternatives[0].Value != wantAlt {
		t.Errorf("%s alternatives = %+v, want one %q", r.Name, r.Alternatives, wantAlt)
	}
}

func TestMergeAI_FillsANewHeaderFieldPrintedOnThePage(t *testing.T) {
	pages := onePage(1, tok("Email: ap@honeywell.example", 1, 0.10, 0.30, 0.50, 0.32))

	got := mergeWith([]FieldResult{mgMissing("buyer_email")}, map[string]any{"buyer_email": "ap@honeywell.example"}, pages, nil)

	wantRegion := Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.50, Y1: 0.32}
	want := FieldResult{Field: Field{Name: "buyer_email", Value: mgStr("ap@honeywell.example"), Region: &wantRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("buyer_email = %+v, want %+v", got[0], want)
	}
}

func TestMergeAI_ANewHeaderFieldNotOnThePageStaysUnfilled(t *testing.T) {
	pages := onePage(1, tok("Email: ap@honeywell.example", 1, 0.10, 0.30, 0.50, 0.32))

	got := mergeWith([]FieldResult{mgMissing("buyer_state")}, map[string]any{"buyer_state": "Lagos"}, pages, nil)

	mgUnreadable(t, got[0], "Lagos")
}

func TestMergeAI_ANewHeaderValuePrintedElsewhereOnThePageIsAccepted(t *testing.T) {
	pages := onePage(1,
		tok("Supplier address: Nigeria", 1, 0.10, 0.10, 0.50, 0.12),
		tok("Bill To: Honeywell Group", 1, 0.10, 0.30, 0.50, 0.32),
	)

	got := mergeWith([]FieldResult{mgMissing("buyer_country")}, map[string]any{"buyer_country": "Nigeria"}, pages, nil)

	if got[0].Value == nil || *got[0].Value != "Nigeria" || got[0].Reason != ReasonNone {
		t.Errorf("buyer_country = %+v, want Nigeria / none (the page check is page-wide, P31)", got[0])
	}
}

func TestMergeAI_AMalformedEmailIsNotARead(t *testing.T) {
	pages := onePage(1, tok("Email: ap at honeywell", 1, 0.10, 0.30, 0.50, 0.32))

	got := mergeWith([]FieldResult{mgMissing("buyer_email")}, map[string]any{"buyer_email": "ap at honeywell"}, pages, nil)

	mgUnreadable(t, got[0], "ap at honeywell")
}

func mgDueDateEngine() []FieldResult {
	issueRegion := &Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.50, Y1: 0.12}
	return []FieldResult{
		{Field: Field{Name: "issue_date", Value: mgStr("2026-07-14"), Region: issueRegion, Reason: ReasonNone}, Alternatives: []Field{}},
		mgMissing("due_date"),
	}
}

func TestMergeAI_APrintedDueDateFillsDueDateNeverIssueDate(t *testing.T) {
	pages := onePage(1,
		tok("Invoice Date: 2026-07-14", 1, 0.10, 0.10, 0.50, 0.12),
		tok("Due Date: 2026-08-13", 1, 0.10, 0.20, 0.50, 0.22),
	)
	engine := mgDueDateEngine()
	issueBefore := cloneResults(engine)[0]

	got := mergeWith(engine, map[string]any{"due_date": "2026-08-13"}, pages, nil)

	dueRegion := Region{Page: 1, X0: 0.10, Y0: 0.20, X1: 0.50, Y1: 0.22}
	wantDue := FieldResult{Field: Field{Name: "due_date", Value: mgStr("2026-08-13"), Region: &dueRegion, Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(mgRow(t, got, "due_date"), wantDue) {
		t.Errorf("due_date = %+v, want %+v", mgRow(t, got, "due_date"), wantDue)
	}
	if !reflect.DeepEqual(mgRow(t, got, "issue_date"), issueBefore) {
		t.Errorf("issue_date = %+v, want unchanged %+v", mgRow(t, got, "issue_date"), issueBefore)
	}
}

func TestMergeAI_AnAmbiguousDueDateIsRefused(t *testing.T) {
	pages := onePage(1, tok("Due Date: 03/04/2026", 1, 0.10, 0.20, 0.50, 0.22))

	got := mergeWith([]FieldResult{mgMissing("due_date")}, map[string]any{"due_date": "03/04/2026"}, pages, nil)

	mgUnreadable(t, got[0], "03/04/2026")
}

func TestCheckAI_ADueDateNeedsADueDateLabel(t *testing.T) {
	issueOnly := onePage(1, tok("Invoice Date: 2026-07-14", 1, 0.10, 0.10, 0.50, 0.12))
	if got, ok := checkAI("due_date", "2026-07-14", issueOnly); ok {
		t.Errorf("checkAI accepted the issue date as a due date: %+v", got)
	}

	labelled := onePage(1, tok("Due Date: 2026-08-13", 1, 0.10, 0.20, 0.50, 0.22))
	if got, ok := checkAI("due_date", "2026-08-13", labelled); !ok || got.Value != "2026-08-13" {
		t.Errorf("control: checkAI(due_date) = %+v ok=%v, want 2026-08-13 ok", got, ok)
	}
}

func TestMergeAI_ADueDateNeedsTheLabelDirectlyBeforeIt(t *testing.T) {
	pages := onePage(1, tok("Due Date: 14/02/2026  Invoice Date: 15/01/2026", 1, 0.10, 0.20, 0.90, 0.22))
	if got, ok := checkAI("due_date", "2026-01-15", pages); ok {
		t.Errorf("checkAI filed the issue date behind an earlier due-date label: %+v", got)
	}

	got := mergeWith([]FieldResult{mgMissing("due_date")}, map[string]any{"due_date": "2026-01-15"}, pages, nil)
	if got[0].Value != nil && *got[0].Value == "2026-01-15" {
		t.Errorf("due_date = %+v, want the issue date refused", got[0])
	}
}

func TestMergeAI_ADueDateEchoingTheIssueDateIsRefused(t *testing.T) {
	pages := onePage(1, tok("Invoice Date: 2026-07-14", 1, 0.10, 0.10, 0.50, 0.12))
	engine := mgDueDateEngine()
	issueBefore := cloneResults(engine)[0]

	got := mergeWith(engine, map[string]any{"due_date": "2026-07-14"}, pages, nil)

	mgUnreadable(t, mgRow(t, got, "due_date"), "2026-07-14")
	if !reflect.DeepEqual(mgRow(t, got, "issue_date"), issueBefore) {
		t.Errorf("issue_date = %+v, want unchanged %+v", mgRow(t, got, "issue_date"), issueBefore)
	}
}

// --- adversarial cover for the nine AI-only fields ---

var mgNewFieldPrints = []struct{ field, value, text string }{
	{"due_date", "2026-08-13", "Due Date: 2026-08-13"},
	{"buyer_email", "ap@honeywell.example", "Email: ap@honeywell.example"},
	{"buyer_telephone", "+234 801 234 5678", "Tel: +234 801 234 5678"},
	{"buyer_street", "12 Marina Road", "Address: 12 Marina Road"},
	{"buyer_city", "Lagos", "City: Lagos"},
	{"buyer_postal_zone", "101233", "Postal Code: 101233"},
	{"buyer_country", "Nigeria", "Country: Nigeria"},
	{"buyer_state", "Lagos State", "State: Lagos State"},
	{"buyer_lga", "Eti-Osa", "LGA: Eti-Osa"},
}

func TestMergeAI_EveryNewHeaderFieldFillsWhenPrinted(t *testing.T) {
	if len(mgNewFieldPrints) != 9 {
		t.Fatalf("table holds %d fields, want the 9 AI-only fields", len(mgNewFieldPrints))
	}
	for _, c := range mgNewFieldPrints {
		pages := onePage(1, tok(c.text, 1, 0.10, 0.30, 0.50, 0.32))
		got := mergeWith([]FieldResult{mgMissing(c.field)}, map[string]any{c.field: c.value}, pages, nil)
		if got[0].Value == nil || *got[0].Value != c.value || got[0].Reason != ReasonNone || got[0].Region == nil {
			t.Errorf("%s = %+v, want %q / none with the token's region", c.field, got[0], c.value)
		}
	}
}

func TestMergeAI_NewHeaderFieldsAbsentOrBlankStayMissingNeverGuessed(t *testing.T) {
	var tokens []Token
	var rows []FieldResult
	for _, c := range mgNewFieldPrints {
		tokens = append(tokens, tok(c.text, 1, 0.10, 0.30, 0.50, 0.32))
		rows = append(rows, mgMissing(c.field))
	}
	if len(rows) != 9 {
		t.Fatalf("rows = %d, want 9", len(rows))
	}
	blank := map[string]any{}
	for _, c := range mgNewFieldPrints {
		blank[c.field] = "  "
	}
	for name, ans := range map[string]map[string]any{"absent": {}, "blank": blank, "null": {"buyer_city": nil}} {
		got := mergeWith(cloneResults(rows), ans, onePage(1, tokens...), nil)
		for _, r := range got {
			if r.Value != nil || r.Reason != ReasonMissing || len(r.Alternatives) != 0 {
				t.Errorf("%s answer: %s = %+v, want nil / missing with no alternative although the page prints it", name, r.Name, r)
			}
		}
	}
}

func TestMergeAI_AMalformedPhoneIsNotARead(t *testing.T) {
	for _, bad := range []string{"call us", "12", "+234 801 234 5678 ext 9x"} {
		pages := onePage(1, tok("Tel: "+bad, 1, 0.10, 0.30, 0.50, 0.32))
		got := mergeWith([]FieldResult{mgMissing("buyer_telephone")}, map[string]any{"buyer_telephone": bad}, pages, nil)
		mgUnreadable(t, got[0], bad)
	}
}

func TestMergeAI_ADueDateAnswerNeverFillsAMissingIssueDate(t *testing.T) {
	pages := onePage(1, tok("Due Date: 2026-08-13", 1, 0.10, 0.20, 0.50, 0.22))
	engine := []FieldResult{mgMissing("issue_date"), mgMissing("due_date")}

	got := mergeWith(engine, map[string]any{"due_date": "2026-08-13"}, pages, nil)

	if r := mgRow(t, got, "due_date"); r.Value == nil || *r.Value != "2026-08-13" {
		t.Errorf("due_date = %+v, want 2026-08-13", r)
	}
	if r := mgRow(t, got, "issue_date"); r.Value != nil || r.Reason != ReasonMissing || len(r.Alternatives) != 0 {
		t.Errorf("issue_date = %+v, want nil / missing: a due date never fills it", r)
	}
}

func TestCheckAI_ADueDateLabelOnThePreviousTokenCounts(t *testing.T) {
	label := onePage(1, tok("Due Date", 1, 0.10, 0.20, 0.25, 0.22), tok("2026-08-13", 1, 0.30, 0.20, 0.45, 0.22))
	if got, ok := checkAI("due_date", "2026-08-13", label); !ok || got.Value != "2026-08-13" {
		t.Errorf("checkAI(due_date) under a Due Date label token = %+v ok=%v, want ok", got, ok)
	}
	issue := onePage(1, tok("Invoice Date", 1, 0.10, 0.20, 0.25, 0.22), tok("2026-08-13", 1, 0.30, 0.20, 0.45, 0.22))
	if got, ok := checkAI("due_date", "2026-08-13", issue); ok {
		t.Errorf("checkAI(due_date) under an Invoice Date label = %+v, want refused", got)
	}
}

func TestAIPrompt_AsksNoSupplierContactField(t *testing.T) {
	if len(HeaderFields) != 19 {
		t.Fatalf("HeaderFields holds %d, want 19", len(HeaderFields))
	}
	for _, f := range []string{"supplier_email", "supplier_telephone", "supplier_street", "supplier_city",
		"supplier_postal_zone", "supplier_country", "supplier_state", "supplier_lga"} {
		if slices.Contains(HeaderFields, f) || strings.Contains(aiSystem, f) {
			t.Errorf("%s is asked of the AI (HeaderFields or aiSystem); the supplier is read from the entity (D31)", f)
		}
	}
	if !strings.Contains(aiSystem, "- buyer_email:") {
		t.Error("control: aiSystem lacks the buyer_email line, so the absence scan above reads the wrong string")
	}
}

func TestMergeAI_ABlankNewFieldAnswerIsNotARead(t *testing.T) {
	rows := []FieldResult{mgMissing("buyer_city"), mgMissing("due_date")}
	pages := onePage(1, tok("City: Lagos", 1, 0.10, 0.30, 0.50, 0.32))

	got := mergeAI(cloneResults(rows), map[string]string{"buyer_city": "  ", "due_date": ""}, pages, nil)

	if !reflect.DeepEqual(got, rows) {
		t.Errorf("a blank answer moved the rows: %+v, want %+v", got, rows)
	}
}

func TestCallAI_DropsABlankNewFieldAnswer(t *testing.T) {
	stub := &recordingAI{enabled: true, answer: map[string]any{"buyer_city": "  ", "buyer_email": "a@b.example"}}

	got, failed := askAI(context.Background(), stub, onePage(1, tok("x", 1, 0, 0, 1, 1)))

	if failed || got["buyer_email"] != "a@b.example" {
		t.Fatalf("control: askAI = %v failed=%v, want buyer_email kept", got, failed)
	}
	if _, ok := got["buyer_city"]; ok {
		t.Errorf("askAI kept the blank buyer_city answer: %v", got)
	}
}
