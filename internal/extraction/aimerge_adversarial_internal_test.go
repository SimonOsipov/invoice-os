// aimerge_adversarial_internal_test.go: edge, negative and property coverage for mergeAI beyond
// the AC-3 table specs in aimerge_internal_test.go.
package extraction

import (
	"reflect"
	"slices"
	"testing"
)

func advRegion(y float64) *Region {
	return &Region{Page: 1, X0: 0.10, Y0: y, X1: 0.30, Y1: y + 0.02}
}

func TestMergeAIAdv_TheInputRowsAreNeverWrittenThrough(t *testing.T) {
	tieAlts := make([]Field, 1, 8)
	tieAlts[0] = Field{Name: "supplier_tin", Value: mgStr("87654321-0002"), Region: advRegion(0.12), Reason: ReasonNone}
	engine := []FieldResult{
		{Field: Field{Name: "buyer_tin", Value: mgStr("12345678-0001"), Region: advRegion(0.10), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "supplier_tin", Value: mgStr("12345678-0009"), Region: advRegion(0.11), Reason: ReasonAmbiguous}, Alternatives: tieAlts},
		{
			Field:        Field{Name: "issue_date", Value: mgStr("2026-03-12"), Region: advRegion(0.20), Reason: ReasonAmbiguous},
			Alternatives: []Field{{Name: "issue_date", Value: mgStr("2026-12-03"), Region: advRegion(0.22), Reason: ReasonNone}},
		},
		{Field: Field{Name: "invoice_number", Reason: ReasonMissing}, Alternatives: []Field{}},
		{Field: Field{Name: "buyer_name", Reason: ReasonMissing}, Alternatives: []Field{}},
		{Field: Field{Name: "line_items", Reason: ReasonNone}, Alternatives: []Field{}},
	}
	before := cloneResults(engine)
	pages := onePage(1,
		tok("TIN 22223333-4444", 1, 0.5, 0.10, 0.9, 0.12),
		tok("Date 2026-12-03", 1, 0.5, 0.20, 0.9, 0.22),
		tok("Invoice Number: 20417", 1, 0.5, 0.30, 0.9, 0.32),
	)
	ans := map[string]any{
		"buyer_tin": "22223333-4444", "supplier_tin": "22223333-4444", "issue_date": "2026-12-03",
		"invoice_number": "20417", "buyer_name": "NOT ON THE PAGE LTD",
	}

	got := mergeWith(engine, ans, pages, nil)

	if !reflect.DeepEqual(engine, before) {
		t.Fatalf("engine mutated:\n got %+v\nwant %+v", engine, before)
	}
	if spare := tieAlts[:2][1]; spare.Value != nil {
		t.Errorf("row 4 appended into the engine's spare capacity: %+v", spare)
	}
	for i := range got {
		if got[i].Reason == engine[i].Reason && reflect.DeepEqual(got[i], engine[i]) {
			continue
		}
		if len(got[i].Alternatives) > 0 && len(engine[i].Alternatives) > 0 && &got[i].Alternatives[0] == &engine[i].Alternatives[0] {
			t.Errorf("row %d (%s) shares its Alternatives backing array with the input", i, got[i].Name)
		}
	}
	// every row above changed, so a no-op merge cannot pass
	for i := 0; i < 5; i++ {
		if reflect.DeepEqual(got[i], before[i]) {
			t.Errorf("row %d (%s) unchanged; the fixture must drive a table row", i, got[i].Name)
		}
	}
}

func TestMergeAIAdv_Row4TwoMergesOnOneEngineDoNotOverwriteEachOther(t *testing.T) {
	alts := make([]Field, 1, 8)
	alts[0] = Field{Name: "buyer_tin", Value: mgStr("87654321-0002"), Region: advRegion(0.12), Reason: ReasonNone}
	engine := []FieldResult{
		{Field: Field{Name: "buyer_tin", Value: mgStr("12345678-0001"), Region: advRegion(0.10), Reason: ReasonAmbiguous}, Alternatives: alts},
	}
	pages := onePage(1,
		tok("TIN 22223333-4444", 1, 0.5, 0.10, 0.9, 0.12),
		tok("TIN 55556666-7777", 1, 0.5, 0.20, 0.9, 0.22),
	)

	first := mergeWith(engine, map[string]any{"buyer_tin": "22223333-4444"}, pages, nil)
	_ = mergeWith(engine, map[string]any{"buyer_tin": "55556666-7777"}, pages, nil)

	if n := len(first[0].Alternatives); n != 2 {
		t.Fatalf("first merge Alternatives len = %d, want 2", n)
	}
	if v := first[0].Alternatives[1].Value; v == nil || *v != "22223333-4444" {
		t.Errorf("first merge's appended reading = %v, want 22223333-4444 (a later merge wrote through a shared array)", v)
	}
}

func TestMergeAIAdv_WritingTheOutputDoesNotReachTheInput(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("VAT: 135.00", 1, 0.1, 0.2, 0.4, 0.22))

	got := mergeWith(engine, map[string]any{"vat": "135.00"}, pages, nil)
	if got[0].Reason != ReasonNone {
		t.Fatalf("setup: vat reason = %q, want decided", got[0].Reason)
	}
	if engine[0].Reason != ReasonMissing || engine[0].Value != nil {
		t.Errorf("engine row now %+v; mergeAI must write a copy", engine[0].Field)
	}
}

func TestMergeAIAdv_NilAndEmptyInputs(t *testing.T) {
	pages := onePage(1, tok("VAT: 135.00", 1, 0.1, 0.2, 0.4, 0.22))
	ans := map[string]string{"vat": "135.00"}

	if got := mergeAI(nil, ans, pages, nil); len(got) != 0 {
		t.Errorf("nil engine: len = %d, want 0", len(got))
	}
	if got := mergeAI([]FieldResult{}, ans, pages, nil); len(got) != 0 {
		t.Errorf("empty engine: len = %d, want 0", len(got))
	}

	engine := []FieldResult{{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}}}
	want := cloneResults(engine)
	if got := mergeAI(engine, nil, pages, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("nil answer: got %+v, want unchanged", got)
	}
	if got := mergeAI(engine, ans, nil, nil); got[0].Reason != ReasonUnreadable {
		t.Errorf("no pages: vat reason = %q, want unreadable (nothing printed, so the page check fails)", got[0].Reason)
	}
}

func TestMergeAIAdv_ABlankAnswerOnAMissingFieldOffersNothing(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "buyer_name", Reason: ReasonMissing}, Alternatives: []Field{}},
		{Field: Field{Name: "buyer_tin", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)

	got := mergeAI(engine, map[string]string{"buyer_name": "  ", "buyer_tin": "\t\n"}, onePage(1), nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blank answers on missing fields: got %+v, want unchanged (D4, not row 8)", got)
	}
}

func TestMergeAIAdv_RowsOutsideHeaderFieldsIgnoreAnAnswer(t *testing.T) {
	lineName := LineFieldName(1, LineRoleLineTotal)
	engine := []FieldResult{
		{Field: Field{Name: "line_items", Reason: ReasonMissing}, Alternatives: []Field{}},
		{Field: Field{Name: lineName, Reason: ReasonMissing}, Alternatives: []Field{}},
		{Field: Field{Name: "not_a_field", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("1000.00", 1, 0.1, 0.1, 0.3, 0.12))
	ans := map[string]string{"line_items": "1000.00", lineName: "1000.00", "not_a_field": "1000.00", "vat": "1000.00"}

	got := mergeAI(engine, ans, pages, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("non-header rows changed: got %+v, want %+v", got, want)
	}
}

func TestMergeAIAdv_EngineUnreadableRowsStand(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "vat", Reason: ReasonUnreadable}, Alternatives: []Field{}},
		{Field: Field{Name: "total", Region: advRegion(0.3), Reason: ReasonUnreadable}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("VAT: 135.00", 1, 0.1, 0.2, 0.4, 0.22))

	got := mergeWith(engine, map[string]any{"vat": "135.00", "total": "9999.00"}, pages, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unreadable engine rows: got %+v, want unchanged (a row the table does not name)", got)
	}
}

func TestMergeAIAdv_Row1KeepsTheEngineSpacingWhenTheAIsDiffers(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "buyer_name", Value: mgStr("ACME LTD"), Region: advRegion(0.1), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("Bill to ACME   LTD", 1, 0.5, 0.1, 0.9, 0.12))
	if a, ok := checkAI("buyer_name", "ACME   LTD", pages); !ok || a.Value != "ACME   LTD" {
		t.Fatalf("setup: checkAI = %+v, %v; want checked with the AI's own spacing", a, ok)
	}

	got := mergeWith(engine, map[string]any{"buyer_name": "ACME   LTD"}, pages, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want the engine row verbatim", got)
	}
}

func TestMergeAIAdv_Row3MatchesAReadingUnderCollapse(t *testing.T) {
	engine := []FieldResult{
		{
			Field:        Field{Name: "buyer_name", Value: mgStr("ACME  LTD"), Region: advRegion(0.10), Reason: ReasonAmbiguous},
			Alternatives: []Field{{Name: "buyer_name", Value: mgStr("OTHER CO"), Region: advRegion(0.20), Reason: ReasonNone}},
		},
	}
	pages := onePage(1, tok("Bill to ACME  LTD", 1, 0.5, 0.4, 0.9, 0.42))

	got := mergeWith(engine, map[string]any{"buyer_name": "ACME LTD"}, pages, nil)

	want := FieldResult{Field: Field{Name: "buyer_name", Value: mgStr("ACME LTD"), Region: advRegion(0.10), Reason: ReasonNone}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %+v, want %+v (decided as the AI's form, head reading's region)", got[0], want)
	}
}

func TestMergeAIAdv_Row3RegionComesFromTheMatchedReading(t *testing.T) {
	head, alt1, alt2 := advRegion(0.10), advRegion(0.20), advRegion(0.30)
	mk := func() []FieldResult {
		return []FieldResult{{
			Field: Field{Name: "buyer_tin", Value: mgStr("11111111-0001"), Region: head, Reason: ReasonAmbiguous},
			Alternatives: []Field{
				{Name: "buyer_tin", Value: mgStr("22222222-0002"), Region: alt1, Reason: ReasonNone},
				{Name: "buyer_tin", Value: mgStr("33333333-0003"), Region: alt2, Reason: ReasonNone},
			},
		}}
	}
	pages := onePage(1,
		tok("TIN 11111111-0001", 1, 0.6, 0.6, 0.9, 0.62),
		tok("TIN 33333333-0003", 1, 0.6, 0.7, 0.9, 0.72),
	)
	cases := []struct {
		name, ai string
		region   *Region
	}{
		{"matches the head", "11111111-0001", head},
		{"matches the last alternative", "33333333-0003", alt2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mergeWith(mk(), map[string]any{"buyer_tin": c.ai}, pages, nil)
			want := FieldResult{Field: Field{Name: "buyer_tin", Value: mgStr(c.ai), Region: c.region, Reason: ReasonNone}, Alternatives: []Field{}}
			if !reflect.DeepEqual(got[0], want) {
				t.Errorf("got %+v, want %+v", got[0], want)
			}
		})
	}
}

func TestMergeAIAdv_Row4AnOverfullTieTakesNoChoice(t *testing.T) {
	alts := make([]Field, 8)
	for i := range alts {
		alts[i] = Field{Name: "buyer_tin", Value: mgStr("1000000" + string(rune('0'+i)) + "-0000"), Region: advRegion(float64(i) * 0.05), Reason: ReasonNone}
	}
	engine := []FieldResult{
		{Field: Field{Name: "buyer_tin", Value: mgStr("19999999-0009"), Region: advRegion(0.9), Reason: ReasonAmbiguous}, Alternatives: alts},
	}
	want := cloneResults(engine)
	pages := onePage(1, tok("TIN 20000000-0001", 1, 0.6, 0.1, 0.9, 0.12))

	got := mergeWith(engine, map[string]any{"buyer_tin": "20000000-0001"}, pages, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("9-reading tie changed: got %d alternatives", len(got[0].Alternatives))
	}
}

func TestMergeAIAdv_Row8OffersTheTrimmedTextWithNoRegion(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "buyer_name", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Account Name: ZENITH  HOLDINGS", 1, 0.1, 0.1, 0.5, 0.12))

	got := mergeWith(engine, map[string]any{"buyer_name": "  ZENITH  HOLDINGS \n"}, pages, nil)

	want := FieldResult{
		Field:        Field{Name: "buyer_name", Reason: ReasonUnreadable},
		Alternatives: []Field{{Name: "buyer_name", Value: mgStr("ZENITH  HOLDINGS"), Reason: ReasonNone}},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %+v, want %+v (trimmed, interior spacing kept, no region)", got[0], want)
	}
}

func TestMergeAIAdv_ArithmeticToleranceBoundary(t *testing.T) {
	lines := []DocLine{{Index: 1, LineTotal: mgStr("1000.00")}, {Index: 2, LineTotal: mgStr("800.00")}}
	subtotal := []struct {
		ai   string
		want Reason
	}{
		{"1800.01", ReasonNone},
		{"1799.99", ReasonNone},
		{"1800.02", ReasonInconsistent},
		{"1799.98", ReasonInconsistent},
	}
	for _, c := range subtotal {
		t.Run("subtotal "+c.ai, func(t *testing.T) {
			engine := []FieldResult{{Field: Field{Name: "subtotal", Reason: ReasonMissing}, Alternatives: []Field{}}}
			pages := onePage(1, tok("Subtotal: "+c.ai, 1, 0.1, 0.1, 0.4, 0.12))
			got := mergeWith(engine, map[string]any{"subtotal": c.ai}, pages, lines)
			if got[0].Reason != c.want || got[0].Value == nil || *got[0].Value != c.ai {
				t.Errorf("subtotal = %+v, want %s reason %q", got[0].Field, c.ai, c.want)
			}
		})
	}

	total := []struct {
		ai   string
		want Reason
	}{
		{"1935.01", ReasonNone},
		{"1934.99", ReasonNone},
		{"1935.02", ReasonInconsistent},
		{"1934.98", ReasonInconsistent},
	}
	for _, c := range total {
		t.Run("total "+c.ai, func(t *testing.T) {
			engine := []FieldResult{
				{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: advRegion(0.1), Reason: ReasonNone}, Alternatives: []Field{}},
				{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: advRegion(0.2), Reason: ReasonNone}, Alternatives: []Field{}},
				{Field: Field{Name: "total", Reason: ReasonMissing}, Alternatives: []Field{}},
			}
			pages := onePage(1, tok("Total: "+c.ai, 1, 0.1, 0.3, 0.4, 0.32))
			got := mergeWith(engine, map[string]any{"total": c.ai}, pages, nil)
			if got[2].Reason != c.want || got[2].Value == nil || *got[2].Value != c.ai {
				t.Errorf("total = %+v, want %s reason %q", got[2].Field, c.ai, c.want)
			}
		})
	}
}

func TestMergeAIAdv_ATotalIsCheckedAgainstASubtotalTheSameMergeDecided(t *testing.T) {
	lines := []DocLine{{Index: 1, LineTotal: mgStr("1000.00")}, {Index: 2, LineTotal: mgStr("800.00")}}
	cases := []struct {
		total string
		want  Reason
	}{
		{"2000.00", ReasonInconsistent},
		{"1935.00", ReasonNone},
	}
	for _, c := range cases {
		t.Run(c.total, func(t *testing.T) {
			engine := []FieldResult{
				{Field: Field{Name: "subtotal", Reason: ReasonMissing}, Alternatives: []Field{}},
				{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: advRegion(0.2), Reason: ReasonNone}, Alternatives: []Field{}},
				{Field: Field{Name: "total", Reason: ReasonMissing}, Alternatives: []Field{}},
			}
			pages := onePage(1,
				tok("Subtotal: 1800.00", 1, 0.1, 0.1, 0.4, 0.12),
				tok("Total: "+c.total, 1, 0.1, 0.3, 0.4, 0.32),
			)
			got := mergeWith(engine, map[string]any{"subtotal": "1800.00", "total": c.total}, pages, lines)
			if got[0].Reason != ReasonNone {
				t.Fatalf("setup: subtotal reason = %q, want decided", got[0].Reason)
			}
			if got[2].Reason != c.want {
				t.Errorf("total reason = %q, want %q", got[2].Reason, c.want)
			}
		})
	}
}

func TestMergeAIAdv_AMergeDecidedVATIsNeverChecked(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: advRegion(0.1), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
		{Field: Field{Name: "total", Value: mgStr("1935.00"), Region: advRegion(0.3), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	lines := []DocLine{{Index: 1, LineTotal: mgStr("1800.00")}}
	pages := onePage(1, tok("VAT: 999.00", 1, 0.1, 0.2, 0.4, 0.22))

	got := mergeWith(engine, map[string]any{"vat": "999.00"}, pages, lines)
	if got[1].Reason != ReasonNone || got[1].Value == nil || *got[1].Value != "999.00" {
		t.Errorf("vat = %+v, want decided 999.00 (vat is outside the arithmetic pass)", got[1].Field)
	}
	if got[2].Reason != ReasonNone {
		t.Errorf("engine total reason = %q, want untouched", got[2].Reason)
	}
}

func TestMergeAIAdv_TheLineSumCheckSkipsAnEngineSubtotal(t *testing.T) {
	engine := []FieldResult{
		{
			Field:        Field{Name: "subtotal", Value: mgStr("1800.00"), Region: advRegion(0.1), Reason: ReasonAmbiguous},
			Alternatives: []Field{{Name: "subtotal", Value: mgStr("1700.00"), Region: advRegion(0.15), Reason: ReasonNone}},
		},
		{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	want := cloneResults(engine)
	lines := []DocLine{{Index: 1, LineTotal: mgStr("1900.00")}}
	pages := onePage(1, tok("VAT: 135.00", 1, 0.1, 0.2, 0.4, 0.22))

	got := mergeWith(engine, map[string]any{"vat": "135.00"}, pages, lines)
	if !reflect.DeepEqual(got[0], want[0]) {
		t.Errorf("subtotal = %+v, want unchanged (not merge-decided)", got[0])
	}
	if got[1].Reason != ReasonNone {
		t.Fatalf("setup: vat reason = %q, want decided", got[1].Reason)
	}
}

func TestMergeAIAdv_AnInconsistentAmountGetsNoAlternative(t *testing.T) {
	engine := []FieldResult{
		{
			Field:        Field{Name: "total", Value: mgStr("2000.00"), Region: advRegion(0.3), Reason: ReasonAmbiguous},
			Alternatives: []Field{{Name: "total", Value: mgStr("1935.00"), Region: advRegion(0.35), Reason: ReasonNone}},
		},
		{Field: Field{Name: "subtotal", Value: mgStr("1800.00"), Region: advRegion(0.1), Reason: ReasonNone}, Alternatives: []Field{}},
		{Field: Field{Name: "vat", Value: mgStr("135.00"), Region: advRegion(0.2), Reason: ReasonNone}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Total: 2000.00", 1, 0.6, 0.3, 0.9, 0.32))

	got := mergeWith(engine, map[string]any{"total": "2000.00"}, pages, nil)

	want := FieldResult{Field: Field{Name: "total", Value: mgStr("2000.00"), Region: advRegion(0.3), Reason: ReasonInconsistent}, Alternatives: []Field{}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("total = %+v, want %+v (the balancing 1935.00 is not offered back: Q6)", got[0], want)
	}
}

// Every engine reason x every AI outcome: a closed reason set, same length and order, never-nil
// Alternatives, and a failed AI value never at rank 0 (AC-5).
func TestMergeAIAdv_PropertySweepOverReasonsAndOutcomes(t *testing.T) {
	allowed := []Reason{ReasonNone, ReasonAmbiguous, ReasonUnreadable, ReasonMissing, ReasonInconsistent}
	const printed, other, failed = "22223333-4444", "12345678-0001", "9999999-1202"
	pages := onePage(1,
		tok("TIN 22223333-4444", 1, 0.5, 0.1, 0.9, 0.12),
		tok("Ref 9999999-1202", 1, 0.5, 0.2, 0.9, 0.22),
		tok("TIN 12345678-0001", 1, 0.5, 0.3, 0.9, 0.32),
	)
	engineRows := map[string]FieldResult{
		"decided":      {Field: Field{Name: "buyer_tin", Value: mgStr(other), Region: advRegion(0.1), Reason: ReasonNone}, Alternatives: []Field{}},
		"ambiguous":    {Field: Field{Name: "buyer_tin", Value: mgStr(other), Region: advRegion(0.1), Reason: ReasonAmbiguous}, Alternatives: []Field{{Name: "buyer_tin", Value: mgStr(printed), Region: advRegion(0.2), Reason: ReasonNone}}},
		"ambiguousNew": {Field: Field{Name: "buyer_tin", Value: mgStr(other), Region: advRegion(0.1), Reason: ReasonAmbiguous}, Alternatives: []Field{{Name: "buyer_tin", Value: mgStr("33334444-5555"), Region: advRegion(0.2), Reason: ReasonNone}}},
		"missing":      {Field: Field{Name: "buyer_tin", Reason: ReasonMissing}, Alternatives: []Field{}},
		"inconsistent": {Field: Field{Name: "buyer_tin", Value: mgStr(other), Region: advRegion(0.1), Reason: ReasonInconsistent}, Alternatives: []Field{}},
		"unreadable":   {Field: Field{Name: "buyer_tin", Reason: ReasonUnreadable}, Alternatives: []Field{}},
	}
	answers := map[string]map[string]string{
		"absent":  {},
		"blank":   {"buyer_tin": "   "},
		"equal":   {"buyer_tin": other},
		"checked": {"buyer_tin": printed},
		"failed":  {"buyer_tin": failed},
	}
	lineRow := FieldResult{Field: Field{Name: LineFieldName(1, LineRoleLineTotal), Value: mgStr("5.00"), Reason: ReasonNone}, Alternatives: []Field{}}
	vatRow := FieldResult{Field: Field{Name: "vat", Reason: ReasonMissing}, Alternatives: []Field{}}

	changedSomewhere := false
	for en, row := range engineRows {
		for an, ans := range answers {
			engine := cloneResults([]FieldResult{vatRow, row, lineRow})
			before := cloneResults(engine)
			got := mergeAI(engine, ans, pages, nil)

			if len(got) != len(engine) {
				t.Fatalf("%s/%s: len %d, want %d", en, an, len(got), len(engine))
			}
			for i := range got {
				if got[i].Name != engine[i].Name {
					t.Errorf("%s/%s: row %d name %q, want %q", en, an, i, got[i].Name, engine[i].Name)
				}
				if !slices.Contains(allowed, got[i].Reason) {
					t.Errorf("%s/%s: row %d reason %q outside the pinned set", en, an, i, got[i].Reason)
				}
				if got[i].Alternatives == nil {
					t.Errorf("%s/%s: row %d Alternatives is nil", en, an, i)
				}
			}
			if !reflect.DeepEqual(got[0], before[0]) || !reflect.DeepEqual(got[2], before[2]) {
				t.Errorf("%s/%s: an unanswered row changed", en, an)
			}
			if an == "failed" && got[1].Value != nil && *got[1].Value == failed {
				t.Errorf("%s/%s: the failed AI text reached rank 0", en, an)
			}
			if an == "absent" || an == "blank" {
				if !reflect.DeepEqual(got[1], before[1]) {
					t.Errorf("%s/%s: row changed on no answer (D4)", en, an)
				}
			}
			if !reflect.DeepEqual(got[1], before[1]) {
				changedSomewhere = true
			}
		}
	}
	if !changedSomewhere {
		t.Fatal("no combination changed a row; the sweep cannot discriminate")
	}
}
