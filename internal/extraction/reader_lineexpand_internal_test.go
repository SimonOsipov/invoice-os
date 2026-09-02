// reader_lineexpand_internal_test.go: QA (Mode B) coverage for expandLineCorrection. The RED
// specs in reader_merge_internal_test.go pin the four value outcomes -- overwrite, tail drop,
// added row, region kept. These pin what those leave open: the head/expansion/tail wire order,
// canonicalLineJSON's inverse over null-versus-empty cells, which correction row is picked, the
// corrected block's was on both branches, and the reason/alternatives the expansion clears.
//
// Helpers use an lxe* prefix; mg and lmg (reader_merge_internal_test.go) are reused as-is.
package extraction

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func lxeStr(s string) *string { return &s }

// lxeBlockRead is the "line_items" block row Reconcile emits ahead of the cells -- a reading
// that shares the correction's field name exactly, so the per-field merge overlays it.
func lxeBlockRead() ExtractionFieldState {
	return ExtractionFieldState{Name: "line_items", Reason: "missing", Alternatives: []ExtractionCandidate{}}
}

// lxeUndone is the line-items row as an undo. Unreachable through the API today -- refuseField
// admits HeaderFields only, so no route can post a correction named "line_items" except the
// line-items POST, which is always typed -- but the merge's own rule is method-driven.
func lxeUndone(lines []LineItemInput) Correction {
	c := lmgCorrection(lines, nil)
	c.Method = MethodUndone
	return c
}

// --- gap 1: the head/expansion/tail splice ---------------------------------------------------
//
// In production the cells are the LAST rows Reconcile emits (reconcile.go Reconcile: headers,
// then the block, then composeLineRows), so the tail is empty and "splice where the first cell
// sat" and "append at the end" cannot be told apart. A non-empty tail is the only fixture that
// discriminates them, which is why this one is synthetic.
func TestExtractionMerge_LineExpansionKeepsTheWireOrder(t *testing.T) {
	fields := []ExtractionFieldState{mgRead()[0], lxeBlockRead()}
	fields = append(fields, lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1))...)
	fields = append(fields, lmgLine(2, "DESC-2", "2", "2.00", "2.00", lmgUniformRegion(1))...)
	// A read field placed AFTER the cells. Reconcile never emits one there; without it this
	// test passes on an expansion appended to the end.
	fields = append(fields, ExtractionFieldState{
		Name: "currency", Value: lxeStr("NGN"), Alternatives: []ExtractionCandidate{},
	})

	// Three lines out of two read ones: the expansion is longer than what it replaces, so a
	// splice that mis-measures the head or the tail cannot come out the right length by luck.
	corr := lmgCorrection([]LineItemInput{
		lmgInput("DESC-1", "1", "1.00", "1.00"),
		lmgInput("EDIT-2", "2", "2.00", "2.00"),
		lmgInput("ADD-3", "3", "3.00", "3.00"),
	}, nil)

	got := mergeCorrections(fields, []Correction{corr})

	want := []string{"total", "line_items"}
	for i := 1; i <= 3; i++ {
		for _, role := range LineRoles {
			want = append(want, LineFieldName(i, role))
		}
	}
	want = append(want, "currency")

	if names := mgNames(got); !slices.Equal(names, want) {
		t.Errorf("the merge returned fields\n  %v\nwant\n  %v\n-- the expansion belongs where the "+
			"first cell sat, with the head before it and the tail after", names, want)
	}
}

// The same splice with the correction's set SHORTER than the readings: the tail must not be
// swallowed by the rows the expansion drops.
func TestExtractionMerge_LineExpansionKeepsTheTailWhenTheSetShrinks(t *testing.T) {
	fields := []ExtractionFieldState{lxeBlockRead()}
	fields = append(fields, lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1))...)
	fields = append(fields, lmgLine(2, "DESC-2", "2", "2.00", "2.00", lmgUniformRegion(1))...)
	fields = append(fields, lmgLine(3, "DESC-3", "3", "3.00", "3.00", lmgUniformRegion(1))...)
	fields = append(fields, ExtractionFieldState{
		Name: "currency", Value: lxeStr("NGN"), Alternatives: []ExtractionCandidate{},
	})

	corr := lmgCorrection([]LineItemInput{lmgInput("DESC-1", "1", "1.00", "1.00")}, nil)

	got := mergeCorrections(fields, []Correction{corr})

	want := []string{"line_items"}
	for _, role := range LineRoles {
		want = append(want, LineFieldName(1, role))
	}
	want = append(want, "currency")
	if names := mgNames(got); !slices.Equal(names, want) {
		t.Errorf("the merge returned fields\n  %v\nwant\n  %v", names, want)
	}
}

// --- gap 2: canonicalLineJSON round-trips through parseLineItemsJSON -------------------------
//
// The two halves live in handlers_lineitems.go as inverses. A null cell is an absence and an
// empty string is a value the operator typed; the correction blob has to keep them apart or
// the expansion re-emits one as the other.
func TestLineItemsJSON_RoundTripsNullAndEmptyCells(t *testing.T) {
	lines := []LineItemInput{
		{Description: lxeStr("Widget"), Quantity: lxeStr("2"), UnitPrice: lxeStr("10.00"), LineTotal: lxeStr("20.00")},
		// Empty string beside null, in the same object, so a round trip that collapses one
		// into the other cannot pass.
		{Description: lxeStr(""), Quantity: nil, UnitPrice: lxeStr("0"), LineTotal: nil},
		{}, // every cell null
	}

	encoded := canonicalLineJSON(lines)
	for _, needle := range []string{`"description":""`, `"quantity":null`} {
		if !strings.Contains(encoded, needle) {
			t.Errorf("canonicalLineJSON = %s, want it to contain %s -- null and \"\" are different states", encoded, needle)
		}
	}

	got, ok := parseLineItemsJSON(encoded)
	if !ok {
		t.Fatalf("parseLineItemsJSON(%s) ok = false, want true", encoded)
	}
	if !reflect.DeepEqual(got, lines) {
		t.Fatalf("the round trip returned %+v, want the posted set back unchanged", got)
	}
	// Named again rather than left to DeepEqual: this is the distinction the expansion turns on.
	if got[1].Description == nil || *got[1].Description != "" {
		t.Errorf("line 2 description came back %v, want a pointer to \"\" -- an operator's empty cell", got[1].Description)
	}
	if got[1].Quantity != nil {
		t.Errorf("line 2 quantity came back %q, want nil -- a null cell is an absence", *got[1].Quantity)
	}

	// The empty set is the "remove every line" post, and "[]" is what clears the value CHECK.
	if empty := canonicalLineJSON(nil); empty != "[]" {
		t.Errorf("canonicalLineJSON(nil) = %q, want \"[]\"", empty)
	}
	back, ok := parseLineItemsJSON("[]")
	if !ok || len(back) != 0 {
		t.Errorf("parseLineItemsJSON(\"[]\") = (%+v, %v), want an empty set and true", back, ok)
	}
}

func TestLineItemsJSON_RefusesWhatIsNotALineArray(t *testing.T) {
	// Control first: the same call accepts the real thing, so the refusals below are not the
	// answer to every input.
	if _, ok := parseLineItemsJSON(canonicalLineJSON([]LineItemInput{lmgInput("D", "1", "1.00", "1.00")})); !ok {
		t.Fatalf("parseLineItemsJSON refused canonicalLineJSON's own output; every case below would be vacuous")
	}

	for _, value := range []string{
		"", "not json", "[", "{}", `{"lines":[]}`, `"a string"`, "42", "[1,2]",
		`[{"description":5}]`, `[[]]`,
	} {
		if lines, ok := parseLineItemsJSON(value); ok {
			t.Errorf("parseLineItemsJSON(%q) ok = true (%+v), want false", value, lines)
		}
	}

	// Documented divergence: JSON null unmarshals into a nil slice without an error, so it
	// reads as the empty set rather than as garbage. Nothing can write it -- canonicalLineJSON
	// never emits it, and refuseField admits HeaderFields only, so the correction route cannot
	// name "line_items" at all. Pinned so a future writer of that value knows what it means.
	if lines, ok := parseLineItemsJSON("null"); !ok || len(lines) != 0 {
		t.Errorf("parseLineItemsJSON(\"null\") = (%+v, %v), want an empty set and true", lines, ok)
	}
}

// --- which correction row the expansion picks ------------------------------------------------

// A value the parse refuses leaves every CELL exactly as the extractor read it: a stale grid is
// visible where a silently emptied one is not.
func TestExtractionMerge_UnparseableLineCorrectionLeavesTheCellsAlone(t *testing.T) {
	readings := lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1))

	broken := lmgCorrection(nil, nil)
	broken.Value = "not json"
	valid := lmgCorrection([]LineItemInput{lmgInput("EDITED", "1", "1.00", "1.00")}, nil)

	// Control: the same call on the same readings DOES expand a parseable value, so the
	// no-op below is a property of the value, not of the call.
	if got := expandLineCorrection(slices.Clone(readings), []Correction{valid}); len(got) == 0 ||
		got[0].Value == nil || *got[0].Value != "EDITED" {
		t.Fatalf("expandLineCorrection ignored a VALID correction (%v); the no-op case below would be vacuous", mgNames(got))
	}

	if got := expandLineCorrection(slices.Clone(readings), []Correction{broken}); !reflect.DeepEqual(got, readings) {
		t.Errorf("expandLineCorrection returned\n  %v\nwant the readings back untouched", mgNames(got))
	}

	// And through the whole merge: the block row takes the unparseable value like any other
	// field, but no cell moves.
	merged := mergeCorrections(slices.Clone(readings), []Correction{broken})
	for _, r := range readings {
		f, ok := lmgFind(merged, r.Name)
		if !ok {
			t.Fatalf("%s vanished from the merge, want it present", r.Name)
		}
		if !reflect.DeepEqual(f, r) {
			t.Errorf("%s came back\n  %s\nwant\n  %s", r.Name, mgShow(f), mgShow(r))
		}
	}
}

// The picked row is the last non-undone one. The corrections read already collapses to one row
// per field name (latestCorrectionsPerFieldTx, rn = 1), so this is the merge's own rule rather
// than a shape the query can produce -- and it is the same rule the per-field loop follows.
func TestExtractionMerge_LatestNonUndoneLineCorrectionWins(t *testing.T) {
	readings := lmgLine(1, "READ", "1", "1.00", "1.00", lmgUniformRegion(1))
	older := lmgCorrection([]LineItemInput{lmgInput("OLDER", "1", "1.00", "1.00")}, nil)
	newer := lmgCorrection([]LineItemInput{lmgInput("NEWER", "1", "1.00", "1.00")}, nil)
	name := LineFieldName(1, LineRoleDescription)

	t.Run("the later of two wins", func(t *testing.T) {
		got := mergeCorrections(slices.Clone(readings), []Correction{older, newer})
		f, ok := lmgFind(got, name)
		if !ok {
			t.Fatalf("%s is absent", name)
		}
		if f.Value == nil || *f.Value != "NEWER" {
			t.Errorf("%s = %s, want \"NEWER\" -- the older row loses", name, mgShow(f))
		}
	})

	t.Run("an undone row does not win over a typed one", func(t *testing.T) {
		got := mergeCorrections(slices.Clone(readings), []Correction{older, lxeUndone([]LineItemInput{lmgInput("UNDO", "9", "9.00", "9.00")})})
		f, ok := lmgFind(got, name)
		if !ok {
			t.Fatalf("%s is absent", name)
		}
		if f.Value == nil || *f.Value != "OLDER" {
			t.Errorf("%s = %s, want \"OLDER\" -- an undo carries no value of its own, exactly as "+
				"the per-field loop treats one", name, mgShow(f))
		}
	})

	t.Run("an undone row alone restores the readings", func(t *testing.T) {
		got := mergeCorrections(slices.Clone(readings), []Correction{lxeUndone([]LineItemInput{lmgInput("UNDO", "9", "9.00", "9.00")})})
		if !reflect.DeepEqual(got, readings) {
			t.Errorf("the merge returned\n  %v\nwant the readings back -- an undo is a full reset", mgNames(got))
		}
	})

	t.Run("a header correction is never mistaken for the line row", func(t *testing.T) {
		fields := append(mgRead(), readings...)
		got := mergeCorrections(fields, []Correction{mgCorrection(MethodTyped, "HUMAN-TOTAL", nil)})
		f, ok := lmgFind(got, name)
		if !ok {
			t.Fatalf("%s is absent", name)
		}
		if f.Corrected != nil {
			t.Errorf("%s gained %+v from a correction on total, want no corrected block", name, *f.Corrected)
		}
		if f.Value == nil || *f.Value != "READ" {
			t.Errorf("%s = %s, want the untouched reading \"READ\"", name, mgShow(f))
		}
	})
}

// --- the block row keeps its own overlay ------------------------------------------------------

// "line_items" is a reading in its own right (reconcile.go reconcileLines) and the correction
// names it exactly, so the per-field loop settles it. The expansion must neither duplicate nor
// rewrite it.
func TestExtractionMerge_LineBlockRowIsMergedLikeAnyOtherField(t *testing.T) {
	fields := append([]ExtractionFieldState{lxeBlockRead()},
		lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1))...)
	lines := []LineItemInput{lmgInput("EDITED", "1", "1.00", "1.00")}
	corr := lmgCorrection(lines, nil)

	got := mergeCorrections(fields, []Correction{corr})

	seen := 0
	for _, f := range got {
		if f.Name == "line_items" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("the merge returned %d field(s) named line_items in %v, want exactly 1", seen, mgNames(got))
	}

	block, _ := lmgFind(got, "line_items")
	if block.Value == nil || *block.Value != canonicalLineJSON(lines) {
		t.Errorf("line_items = %s, want the canonical JSON of the posted set", mgShow(block))
	}
	if block.Reason != "" {
		t.Errorf("line_items reason = %q, want \"\" -- a settled field is no longer flagged", block.Reason)
	}
	if block.Alternatives == nil {
		t.Error("line_items alternatives = nil, want an empty non-nil slice")
	}
	if block.Corrected == nil || block.Corrected.Method != "typed" {
		t.Errorf("line_items corrected = %+v, want method typed", block.Corrected)
	}
	if block.Region != nil {
		t.Errorf("line_items highlights %+v, want nil -- the block row carries no box", *block.Region)
	}
}

// --- the corrected block's was ----------------------------------------------------------------

// The per-field rule is: the superseded value, and the reading only when this is the field's
// first correction. Per cell that reads as the superseded BLOB's cell at the same position.
func TestExtractionMerge_LineCorrectedWasFollowsThePerFieldRule(t *testing.T) {
	readings := lmgLine(1, "READ-DESC", "9", "9.00", "9.00", lmgUniformRegion(1))
	name := LineFieldName(1, LineRoleDescription)
	prior := canonicalLineJSON([]LineItemInput{lmgInput("PRIOR-DESC", "5", "5.00", "5.00")})

	t.Run("the first correction supersedes the reading", func(t *testing.T) {
		got := mergeCorrections(slices.Clone(readings), []Correction{
			lmgCorrection([]LineItemInput{lmgInput("NEW-DESC", "1", "1.00", "1.00")}, nil),
		})
		f, _ := lmgFind(got, name)
		if f.Corrected == nil || f.Corrected.Was == nil || *f.Corrected.Was != "READ-DESC" {
			t.Errorf("%s corrected.was = %s, want \"READ-DESC\"", name, mgShow(f))
		}
	})

	t.Run("a later correction supersedes the blob's own cell, not the reading", func(t *testing.T) {
		got := mergeCorrections(slices.Clone(readings), []Correction{
			lmgCorrection([]LineItemInput{lmgInput("NEW-DESC", "1", "1.00", "1.00")}, &prior),
		})
		f, _ := lmgFind(got, name)
		if f.Corrected == nil || f.Corrected.Was == nil {
			t.Fatalf("%s corrected.was = %s, want \"PRIOR-DESC\"", name, mgShow(f))
		}
		if *f.Corrected.Was == "READ-DESC" {
			t.Errorf("%s corrected.was = \"READ-DESC\", want \"PRIOR-DESC\" -- the reading is only "+
				"the answer for a field's FIRST correction", name)
		}
		if *f.Corrected.Was != "PRIOR-DESC" {
			t.Errorf("%s corrected.was = %q, want \"PRIOR-DESC\"", name, *f.Corrected.Was)
		}
		// Every role, not just the one: a was read from the wrong role would pass above.
		for _, tc := range []struct{ role, want string }{
			{LineRoleQuantity, "5"}, {LineRoleUnitPrice, "5.00"}, {LineRoleLineTotal, "5.00"},
		} {
			cell, ok := lmgFind(got, LineFieldName(1, tc.role))
			if !ok || cell.Corrected == nil || cell.Corrected.Was == nil || *cell.Corrected.Was != tc.want {
				t.Errorf("line 1 %s corrected.was = %s, want %q", tc.role, mgShow(cell), tc.want)
			}
		}
	})

	t.Run("a row past the blob's length falls back to the reading, which for an added row is nil", func(t *testing.T) {
		got := mergeCorrections(slices.Clone(readings), []Correction{
			lmgCorrection([]LineItemInput{
				lmgInput("NEW-DESC", "1", "1.00", "1.00"),
				lmgInput("ADDED", "2", "2.00", "2.00"),
			}, &prior),
		})
		added, ok := lmgFind(got, LineFieldName(2, LineRoleDescription))
		if !ok {
			t.Fatalf("line 2 description is absent, want the added row present")
		}
		if added.Corrected == nil {
			t.Fatalf("line 2 description carries no corrected block, want one")
		}
		if added.Corrected.Was != nil {
			t.Errorf("line 2 description corrected.was = %q, want null -- nothing stood at that "+
				"position before, neither a reading nor a superseded cell", *added.Corrected.Was)
		}
	})

	t.Run("an unparseable superseded blob falls back to the reading", func(t *testing.T) {
		junk := "not json"
		got := mergeCorrections(slices.Clone(readings), []Correction{
			lmgCorrection([]LineItemInput{lmgInput("NEW-DESC", "1", "1.00", "1.00")}, &junk),
		})
		f, _ := lmgFind(got, name)
		if f.Corrected == nil || f.Corrected.Was == nil || *f.Corrected.Was != "READ-DESC" {
			t.Errorf("%s corrected.was = %s, want \"READ-DESC\"", name, mgShow(f))
		}
	})

	// The pointed route is the only writer of an anchor label and it cannot name line_items, so
	// an expanded cell never carries a where.
	t.Run("an expanded cell carries no anchor label", func(t *testing.T) {
		got := mergeCorrections(slices.Clone(readings), []Correction{
			lmgCorrection([]LineItemInput{lmgInput("NEW-DESC", "1", "1.00", "1.00")}, nil),
		})
		f, _ := lmgFind(got, name)
		if f.Corrected == nil {
			t.Fatalf("%s carries no corrected block", name)
		}
		if f.Corrected.Where != nil {
			t.Errorf("%s corrected.where = %q, want null", name, *f.Corrected.Where)
		}
	})
}

// --- what the expansion clears ----------------------------------------------------------------

// reconcileLines flags a line_total whose quantity x unit_price does not match. A correction
// settles the cell, so the flag and its alternatives go, exactly as they do for a header field.
func TestExtractionMerge_LineExpansionClearsReasonAndAlternatives(t *testing.T) {
	readings := lmgLine(1, "DESC-1", "2", "10.00", "99.00", lmgUniformRegion(1))
	flagged := LineFieldName(1, LineRoleLineTotal)
	for i := range readings {
		if readings[i].Name == flagged {
			readings[i].Reason = "inconsistent"
			readings[i].Alternatives = []ExtractionCandidate{{Value: lxeStr("20.00")}}
		}
	}

	// Control: with no line correction the flag survives, so the clearing below is the
	// correction's doing.
	uncorrected, _ := lmgFind(mergeCorrections(slices.Clone(readings), nil), flagged)
	if uncorrected.Reason != "inconsistent" {
		t.Fatalf("the flag did not survive an EMPTY correction set (%s); the assertions below would be vacuous",
			mgShow(uncorrected))
	}

	got := mergeCorrections(slices.Clone(readings), []Correction{
		lmgCorrection([]LineItemInput{lmgInput("DESC-1", "2", "10.00", "20.00")}, nil),
	})
	f, ok := lmgFind(got, flagged)
	if !ok {
		t.Fatalf("%s is absent", flagged)
	}
	if f.Reason != "" {
		t.Errorf("%s reason = %q, want \"\" -- a settled cell is no longer flagged", flagged, f.Reason)
	}
	if f.Alternatives == nil {
		t.Errorf("%s alternatives = nil, want an empty non-nil slice -- nil marshals to JSON null", flagged)
	}
	if len(f.Alternatives) != 0 {
		t.Errorf("%s kept %d alternative(s), want none", flagged, len(f.Alternatives))
	}
}

// The empty set is the "remove every line" post. Every cell goes; the block row stays.
func TestExtractionMerge_EmptyLineCorrectionDropsEveryCell(t *testing.T) {
	fields := append([]ExtractionFieldState{lxeBlockRead()},
		lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1))...)
	fields = append(fields, lmgLine(2, "DESC-2", "2", "2.00", "2.00", lmgUniformRegion(1))...)

	// Control: a NON-empty set on the same fixture keeps line 1, so the absences below are the
	// empty set's doing rather than the fixture's.
	kept := mergeCorrections(slices.Clone(fields), []Correction{
		lmgCorrection([]LineItemInput{lmgInput("DESC-1", "1", "1.00", "1.00")}, nil),
	})
	if _, ok := lmgFind(kept, LineFieldName(1, LineRoleDescription)); !ok {
		t.Fatalf("a one-line correction dropped line 1 as well; the assertions below would be vacuous")
	}

	got := mergeCorrections(slices.Clone(fields), []Correction{lmgCorrection([]LineItemInput{}, nil)})
	for _, f := range got {
		if _, _, isCell := ParseLineFieldName(f.Name); isCell {
			t.Errorf("%s came back as %s, want every cell dropped", f.Name, mgShow(f))
		}
	}
	if _, ok := lmgFind(got, "line_items"); !ok {
		t.Error("the line_items block row is gone, want it present carrying \"[]\"")
	}
}

// A null cell is an absence: it emits no field at all, never a field carrying an empty value.
// LineItemResults follows the same rule on the reading side.
func TestExtractionMerge_ANullCellInTheCorrectionEmitsNoField(t *testing.T) {
	readings := lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1))
	corr := lmgCorrection([]LineItemInput{{
		Description: lxeStr("DESC-1"), Quantity: nil, UnitPrice: lxeStr("1.00"), LineTotal: lxeStr(""),
	}}, nil)

	got := mergeCorrections(slices.Clone(readings), []Correction{corr})

	// Control: the siblings ARE emitted, so the quantity's absence is the null's doing.
	for _, role := range []string{LineRoleDescription, LineRoleUnitPrice} {
		if _, ok := lmgFind(got, LineFieldName(1, role)); !ok {
			t.Fatalf("line 1 %s is absent; the assertion below would be vacuous", role)
		}
	}
	if f, ok := lmgFind(got, LineFieldName(1, LineRoleQuantity)); ok {
		t.Errorf("line 1 quantity came back as %s, want no field at all -- the posted cell was null", mgShow(f))
	}
	// An empty string is a value the operator typed, and it survives where a null does not.
	f, ok := lmgFind(got, LineFieldName(1, LineRoleLineTotal))
	if !ok {
		t.Fatalf("line 1 line_total is absent, want it present carrying \"\"")
	}
	if f.Value == nil || *f.Value != "" {
		t.Errorf("line 1 line_total = %s, want a value of \"\"", mgShow(f))
	}
}

// EXTR-14's accepted residual, stated as what IS guaranteed today. The posted set is
// positional, so after a MIDDLE row is removed the rows below inherit the box of the reading at
// their new position. The VALUES are right, and that is what this pins; the region is
// deliberately not asserted either way (reader.go expandLineCorrection's own comment).
func TestExtractionMerge_MiddleRowRemovalKeepsTheRemainingValues(t *testing.T) {
	readings := append(
		lmgLine(1, "DESC-1", "1", "1.00", "1.00", lmgUniformRegion(1)),
		lmgLine(2, "DESC-2", "2", "2.00", "2.00", lmgUniformRegion(2))...,
	)
	readings = append(readings, lmgLine(3, "DESC-3", "3", "3.00", "3.00", lmgUniformRegion(3))...)

	got := mergeCorrections(readings, []Correction{lmgCorrection([]LineItemInput{
		lmgInput("DESC-1", "1", "1.00", "1.00"),
		lmgInput("DESC-3", "3", "3.00", "3.00"),
	}, nil)})

	for _, tc := range []struct {
		index int
		want  string
	}{{1, "DESC-1"}, {2, "DESC-3"}} {
		name := LineFieldName(tc.index, LineRoleDescription)
		f, ok := lmgFind(got, name)
		if !ok {
			t.Fatalf("%s is absent, want it present", name)
		}
		if f.Value == nil || *f.Value != tc.want {
			t.Errorf("%s = %s, want %q -- the surviving rows close up", name, mgShow(f), tc.want)
		}
	}
	if f, ok := lmgFind(got, LineFieldName(3, LineRoleDescription)); ok {
		t.Errorf("line 3 description came back as %s, want it dropped -- two rows were posted", mgShow(f))
	}
}
