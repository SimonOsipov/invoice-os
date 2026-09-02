// reader_merge_internal_test.go: the correction merge is a pure function over the decided
// readings and the latest correction per field, so it has an oracle without a database.
package extraction

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func mgStr(s string) *string { return &s }

var (
	mgR0       = &ExtractionRegion{Page: 1, X0: 0.11, Y0: 0.12, X1: 0.13, Y1: 0.14}
	mgAlt1     = &ExtractionRegion{Page: 1, X0: 0.21, Y0: 0.22, X1: 0.33, Y1: 0.34}
	mgAlt2     = &ExtractionRegion{Page: 2, X0: 0.41, Y0: 0.52, X1: 0.63, Y1: 0.74}
	mgBox      = Region{Page: 3, X0: 0.15, Y0: 0.26, X1: 0.37, Y1: 0.48}
	mgPointed  = &ExtractionRegion{Page: 3, X0: 0.15, Y0: 0.26, X1: 0.37, Y1: 0.48}
	mgDecoyR0  = &ExtractionRegion{Page: 1, X0: 0.01, Y0: 0.02, X1: 0.03, Y1: 0.04}
	mgDecoyAlt = &ExtractionRegion{Page: 4, X0: 0.71, Y0: 0.72, X1: 0.83, Y1: 0.84}
)

// mgRead is the extractor's own answer for one ambiguous field, rebuilt per case so a merge
// that mutates its input in place cannot leak across cases.
func mgRead() []ExtractionFieldState {
	return []ExtractionFieldState{{
		Name:   "total",
		Value:  mgStr("READ-A"),
		Region: mgR0,
		Reason: "ambiguous",
		Alternatives: []ExtractionCandidate{
			{Value: mgStr("ALT-1"), Region: mgAlt1},
			{Value: mgStr("ALT-2"), Region: mgAlt2},
		},
	}}
}

func mgCorrection(method CorrectionMethod, value string, superseded *string) Correction {
	return Correction{FieldName: "total", Value: value, Method: method, Superseded: superseded, Actor: "operator"}
}

// Every arm of the precedence table, over crafted inputs: the DB cases prove the query feeds
// the merge, this proves the merge itself.
func TestExtractionMerge_ResolvesEachMethodWithoutADatabase(t *testing.T) {
	for _, c := range []struct {
		name        string
		corrections []Correction
		want        ExtractionFieldState
	}{
		{
			name:        "no correction leaves the reading exactly as it is",
			corrections: nil,
			want:        mgRead()[0],
		},
		{
			name:        "typed takes the value and leaves the region",
			corrections: []Correction{mgCorrection(MethodTyped, "HUMAN-B", mgStr("HUMAN-OLD"))},
			want: ExtractionFieldState{
				Name: "total", Value: mgStr("HUMAN-B"), Region: mgR0, Reason: "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: "typed", Was: mgStr("HUMAN-OLD")},
			},
		},
		{
			name:        "chosen takes the matched alternative's region",
			corrections: []Correction{mgCorrection(MethodChosen, "ALT-2", nil)},
			want: ExtractionFieldState{
				Name: "total", Value: mgStr("ALT-2"), Region: mgAlt2, Reason: "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: "chosen", Was: mgStr("READ-A")},
			},
		},
		{
			name:        "chosen matching no alternative keeps the reading's region",
			corrections: []Correction{mgCorrection(MethodChosen, "HUMAN-NOMATCH", nil)},
			want: ExtractionFieldState{
				Name: "total", Value: mgStr("HUMAN-NOMATCH"), Region: mgR0, Reason: "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: "chosen", Was: mgStr("READ-A")},
			},
		},
		{
			name: "pointed takes its own stored box and its anchor label",
			corrections: []Correction{func() Correction {
				c := mgCorrection(MethodPointed, "HUMAN-P", nil)
				box := mgBox
				c.Region = &box
				c.AnchorLabel = "Total due"
				return c
			}()},
			want: ExtractionFieldState{
				Name: "total", Value: mgStr("HUMAN-P"), Region: mgPointed, Reason: "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: "pointed", Was: mgStr("READ-A"), Where: mgStr("Total due")},
			},
		},
		{
			name:        "undone resets to the reading and ignores its own value",
			corrections: []Correction{mgCorrection(MethodUndone, "UNDO-Z", mgStr("HUMAN-B"))},
			want:        mgRead()[0],
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := mergeCorrections(mgRead(), c.corrections)
			if len(got) != 1 {
				t.Fatalf("the merge returned %d field(s), want 1", len(got))
			}
			if !reflect.DeepEqual(got[0], c.want) {
				t.Errorf("the merge returned\n  %s\nwant\n  %s", mgShow(got[0]), mgShow(c.want))
			}
		})
	}

	t.Run("a correction naming no read field is appended after the read ones", func(t *testing.T) {
		got := mergeCorrections(mgRead(), []Correction{
			{FieldName: "currency", Value: "NGN", Method: MethodTyped, Actor: "operator"},
		})
		if len(got) != 2 {
			t.Fatalf("the merge returned %d field(s), the first being\n  %s\nwant 2 — a correction with "+
				"no reading is a human's value that already reached the invoice", len(got), mgShow(got[0]))
		}
		want := ExtractionFieldState{
			Name: "currency", Value: mgStr("NGN"), Reason: "",
			Alternatives: []ExtractionCandidate{},
			Corrected:    &ExtractionCorrected{Method: "typed"},
		}
		if got[0].Name != "total" {
			t.Errorf("the merge put %q first, want the read field total", got[0].Name)
		}
		if !reflect.DeepEqual(got[1], want) {
			t.Errorf("the synthesized field is\n  %s\nwant\n  %s", mgShow(got[1]), mgShow(want))
		}
	})

	// The corrections read hands its rows back in field_name order, so a sort fed by it can
	// never be observed to do anything. Fed unsorted, it can: the merge owns the output order.
	t.Run("synthesized fields are name-ordered whatever order the corrections arrive in", func(t *testing.T) {
		got := mergeCorrections(mgRead(), []Correction{
			{FieldName: "supplier_name", Value: "S", Method: MethodTyped, Actor: "operator"},
			{FieldName: "buyer_name", Value: "B", Method: MethodTyped, Actor: "operator"},
			{FieldName: "currency", Value: "NGN", Method: MethodTyped, Actor: "operator"},
		})
		want := []string{"total", "buyer_name", "currency", "supplier_name"}
		if names := mgNames(got); !slices.Equal(names, want) {
			t.Errorf("the merge returned fields %v, want %v — the read field first, then the "+
				"synthesized ones in name order, not the order they arrived in", names, want)
		}
	})

	// Two alternatives may legitimately hold one string — that is a shape of ambiguous — and the
	// row records which was clicked nowhere, so the rule is first match. Pinned, not assumed.
	t.Run("chosen takes the FIRST alternative whose value matches", func(t *testing.T) {
		fields := mgRead()
		fields[0].Alternatives = []ExtractionCandidate{
			{Value: mgStr("ALT-2"), Region: mgAlt1},
			{Value: mgStr("ALT-2"), Region: mgAlt2},
		}
		got := mergeCorrections(fields, []Correction{mgCorrection(MethodChosen, "ALT-2", nil)})
		if got[0].Region != mgAlt1 {
			t.Errorf("the chosen total highlights %s, want the first matching alternative %+v",
				mgShow(got[0]), *mgAlt1)
		}
	})

	// A correction's value is matched against ITS OWN field's alternatives. mgDecoy holds the
	// same string at a different box, and is deliberately the first field.
	t.Run("chosen matches the corrected field's alternatives, not a neighbour's", func(t *testing.T) {
		fields := append(mgDecoy(), mgRead()...)
		got := mergeCorrections(fields, []Correction{mgCorrection(MethodChosen, "ALT-2", nil)})
		if len(got) != 2 || got[1].Name != "total" {
			t.Fatalf("the merge returned %d field(s) %v, want [invoice_number total]", len(got), mgNames(got))
		}
		if got[1].Region != mgAlt2 {
			t.Errorf("the chosen total highlights %s, want total's own second alternative %+v — "+
				"invoice_number spells the same value at a different box", mgShow(got[1]), *mgAlt2)
		}
		if !reflect.DeepEqual(got[0], mgDecoy()[0]) {
			t.Errorf("the untouched invoice_number came back\n  %s\nwant\n  %s — a correction on total "+
				"settles total", mgShow(got[0]), mgShow(mgDecoy()[0]))
		}
	})
}

// mgDecoy is a second ambiguous field whose own alternative spells total's chosen value.
func mgDecoy() []ExtractionFieldState {
	return []ExtractionFieldState{{
		Name:         "invoice_number",
		Value:        mgStr("READ-N"),
		Region:       mgDecoyR0,
		Reason:       "ambiguous",
		Alternatives: []ExtractionCandidate{{Value: mgStr("ALT-2"), Region: mgDecoyAlt}},
	}}
}

func mgNames(fields []ExtractionFieldState) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}

// mgShow renders a field state as the wire bytes, so a failure names values, not addresses.
func mgShow(f ExtractionFieldState) string {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Sprintf("%+v", f)
	}
	return string(b)
}

// --- EXTR-13-10: a "line_items" correction must overlay the per-cell readings ---------------
//
// The defect: LineFieldName(i, role) is the only name a per-cell reading ever carries, but the
// line-items POST appends its correction under the literal name "line_items" (the block row).
// mergeCorrections only overlays a correction whose FieldName matches a reading's Name exactly,
// so today that row never touches line_items[N].<role> at all -- the readings pass straight
// through untouched. The fix expands the block correction into per-cell fields before the loop
// that already handles "total" and every other header name.
//
// EXTR-14's accepted residual: after a REMOVAL, rows below shift up and inherit the reading at
// their new position -- right values, one wrong highlight. Nothing below removes a middle row,
// only a tail row or none at all, so that residual is never exercised here.

func lmgLine(index int, desc, qty, price, total string, region func(role string) *ExtractionRegion) []ExtractionFieldState {
	roles := []struct{ role, value string }{
		{LineRoleDescription, desc}, {LineRoleQuantity, qty}, {LineRoleUnitPrice, price}, {LineRoleLineTotal, total},
	}
	out := make([]ExtractionFieldState, 0, len(roles))
	for _, r := range roles {
		v := r.value
		out = append(out, ExtractionFieldState{
			Name: LineFieldName(index, r.role), Value: &v, Region: region(r.role), Alternatives: []ExtractionCandidate{},
		})
	}
	return out
}

// lmgUniformRegion gives every role of one line the same box, at the given page -- distinct
// enough to tell "the reading's own box" from "no box" without needing four different ones.
func lmgUniformRegion(page int) func(role string) *ExtractionRegion {
	return func(string) *ExtractionRegion {
		return &ExtractionRegion{Page: page, X0: 0.1, Y0: 0.1, X1: 0.2, Y1: 0.2}
	}
}

func lmgFind(fields []ExtractionFieldState, name string) (ExtractionFieldState, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return ExtractionFieldState{}, false
}

func lmgInput(desc, qty, price, total string) LineItemInput {
	return LineItemInput{Description: &desc, Quantity: &qty, UnitPrice: &price, LineTotal: &total}
}

// lmgCorrection is the ONE row the line-items POST ever appends: FieldName "line_items", value
// the canonical JSON of the whole set, method typed, region nil -- handlers_lineitems.go:169-181.
func lmgCorrection(lines []LineItemInput, superseded *string) Correction {
	return Correction{FieldName: "line_items", Value: canonicalLineJSON(lines), Method: MethodTyped, Superseded: superseded, Actor: "operator"}
}

// 1: a cell the correction overwrites.
func TestExtractionMerge_LineCorrectionOverwritesACell(t *testing.T) {
	readings := lmgLine(1, "OLD-DESC", "2", "10.00", "20.00", lmgUniformRegion(1))
	corr := lmgCorrection([]LineItemInput{lmgInput("NEW-DESC", "2", "10.00", "20.00")}, nil)

	got := mergeCorrections(readings, []Correction{corr})

	name := LineFieldName(1, LineRoleDescription)
	f, ok := lmgFind(got, name)
	if !ok {
		t.Fatalf("the merge dropped %q entirely, want it present", name)
	}
	if f.Value == nil || *f.Value != "NEW-DESC" {
		t.Errorf("%s = %s, want \"NEW-DESC\" -- the corrected value, not the OLD-DESC reading", name, mgShow(f))
	}
}

// 2: the tail readings a shorter correction drops.
func TestExtractionMerge_LineCorrectionDropsReadingsPastItsLength(t *testing.T) {
	readings := append(
		lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1)),
		lmgLine(2, "DESC-2", "2", "2.00", "2.00", lmgUniformRegion(1))...,
	)
	corr := lmgCorrection([]LineItemInput{lmgInput("DESC-1", "1", "1.00", "1.00")}, nil)

	got := mergeCorrections(readings, []Correction{corr})

	// Control needle: line 1 must still be there, or the absence checks below prove nothing.
	if _, ok := lmgFind(got, LineFieldName(1, LineRoleDescription)); !ok {
		t.Fatalf("line 1 is gone entirely, want it present -- the absence checks for line 2 below would be vacuous")
	}
	for _, role := range LineRoles {
		name := LineFieldName(2, role)
		if f, ok := lmgFind(got, name); ok {
			t.Errorf("%s came back as %s, want it dropped -- the correction only has one line", name, mgShow(f))
		}
	}
}

// 3: a row the correction adds beyond every reading.
func TestExtractionMerge_LineCorrectionAddsARowWithNoRegion(t *testing.T) {
	readings := lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1))
	corr := lmgCorrection([]LineItemInput{
		lmgInput("DESC-1", "1", "1.00", "1.00"),
		lmgInput("DESC-2", "2", "2.00", "2.00"),
	}, nil)

	got := mergeCorrections(readings, []Correction{corr})

	name := LineFieldName(2, LineRoleDescription)
	f, ok := lmgFind(got, name)
	if !ok {
		t.Fatalf("%s is absent, want it present -- the correction added a second line the extractor never read", name)
	}
	if f.Value == nil || *f.Value != "DESC-2" {
		t.Errorf("%s = %s, want \"DESC-2\"", name, mgShow(f))
	}
	if f.Region != nil {
		t.Errorf("%s highlights %+v, want nil -- no reading ever sat at this name", name, *f.Region)
	}
}

// 4: a plain edit keeps the reading's box and gains a corrected block.
func TestExtractionMerge_LineCorrectionPreservesRegionAndGainsCorrected(t *testing.T) {
	region := lmgUniformRegion(7)
	readings := lmgLine(1, "OLD-DESC", "2", "10.00", "20.00", region)
	corr := lmgCorrection([]LineItemInput{lmgInput("NEW-DESC", "2", "10.00", "20.00")}, nil)

	got := mergeCorrections(readings, []Correction{corr})

	name := LineFieldName(1, LineRoleDescription)
	f, ok := lmgFind(got, name)
	if !ok {
		t.Fatalf("%s is absent, want it present", name)
	}
	want := region(LineRoleDescription)
	if f.Region == nil || *f.Region != *want {
		t.Errorf("%s highlights %s, want the reading's own box %+v -- a typed edit carries no box of its own", name, mgShow(f), *want)
	}
	if f.Corrected == nil {
		t.Fatalf("%s carries a nil corrected block, want one -- a line edit is a correction like any other", name)
	}
	if f.Corrected.Method != "typed" {
		t.Errorf("%s corrected.method = %q, want \"typed\"", name, f.Corrected.Method)
	}
	if f.Corrected.Was == nil || *f.Corrected.Was != "OLD-DESC" {
		t.Errorf("%s corrected.was = %s, want \"OLD-DESC\" -- the reading, since this is the field's first correction", name, mgShow(f))
	}
}

// 9 (green fence, must stay green across the fix): a header correction merges exactly as
// before, alongside a line correction on the SAME call -- proving the expansion is additive to
// the existing per-field merge, not a replacement of it.
func TestExtractionMerge_HeaderCorrectionStillMergesAlongsideALineCorrection(t *testing.T) {
	fields := append(mgRead(), lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1))...)
	corrections := []Correction{
		mgCorrection(MethodTyped, "HUMAN-TOTAL", mgStr("READ-A")),
		lmgCorrection([]LineItemInput{lmgInput("DESC-1", "1", "1.00", "1.00")}, nil),
	}

	got := mergeCorrections(fields, corrections)

	total, ok := lmgFind(got, "total")
	if !ok {
		t.Fatalf("total is absent from the merge, want it present")
	}
	if total.Value == nil || *total.Value != "HUMAN-TOTAL" {
		t.Errorf("total = %s, want \"HUMAN-TOTAL\"", mgShow(total))
	}
	if total.Corrected == nil || total.Corrected.Method != "typed" {
		t.Errorf("total's corrected block is %+v, want method typed", total.Corrected)
	}
}
