package invoice

import "testing"

// TestSubmissionCanonical_EmptyLineItemsSliceVsNil (QA, AC-5 edge): both a
// nil LineItems and a non-nil-but-empty []LineItem{} must map to Lines of
// length 0 -- the plan's `var lines []submission.CanonicalLine` never gets
// appended to when the source loop has zero iterations either way, so this
// asserts the ACTUAL observable result (including nil-ness of the output
// slice) for both inputs, rather than assuming they behave identically.
func TestSubmissionCanonical_EmptyLineItemsSliceVsNil(t *testing.T) {
	nilInv := Invoice{LineItems: nil}
	emptyInv := Invoice{LineItems: []LineItem{}}

	gotNil := SubmissionCanonical(nilInv)
	gotEmpty := SubmissionCanonical(emptyInv)

	if len(gotNil.Lines) != 0 {
		t.Errorf("nil LineItems: len(Lines) = %d, want 0", len(gotNil.Lines))
	}
	if len(gotEmpty.Lines) != 0 {
		t.Errorf("empty LineItems: len(Lines) = %d, want 0", len(gotEmpty.Lines))
	}
	// Document the actual nil-ness rather than assume it: the mapper's `var
	// lines []submission.CanonicalLine` + append-in-loop never appends when
	// the loop has zero iterations, so both a nil and an empty source slice
	// currently produce a NIL Lines slice (not an empty non-nil one).
	if gotNil.Lines != nil {
		t.Errorf("nil LineItems: Lines = %#v, want nil", gotNil.Lines)
	}
	if gotEmpty.Lines != nil {
		t.Errorf("empty (non-nil) LineItems: Lines = %#v, want nil (observed mapper behavior: an empty source slice does not force a non-nil empty result)", gotEmpty.Lines)
	}
}

// TestSubmissionCanonical_DuplicateAndNonSequentialLineNos (QA, AC-2 edge):
// the mapper must pass LineNo through unchanged -- never renumber, dedupe,
// or sort -- even when the source data is not the well-formed 1..N sequence
// Store.Get normally produces.
func TestSubmissionCanonical_DuplicateAndNonSequentialLineNos(t *testing.T) {
	inv := Invoice{
		LineItems: []LineItem{
			{ID: "x", LineNo: 5},
			{ID: "y", LineNo: 5}, // duplicate LineNo
			{ID: "z", LineNo: 0}, // zero LineNo
			{ID: "w", LineNo: 2}, // out of order / non-sequential
		},
	}

	got := SubmissionCanonical(inv)

	if len(got.Lines) != 4 {
		t.Fatalf("len(Lines) = %d, want 4", len(got.Lines))
	}
	wantIDs := []string{"x", "y", "z", "w"}
	wantNos := []int{5, 5, 0, 2}
	for i, line := range got.Lines {
		if line.LineID != wantIDs[i] {
			t.Errorf("Lines[%d].LineID = %q, want %q", i, line.LineID, wantIDs[i])
		}
		if line.LineNo != wantNos[i] {
			t.Errorf("Lines[%d].LineNo = %d, want %d (mapper must not renumber/sort)", i, line.LineNo, wantNos[i])
		}
	}
}

// TestSubmissionCanonical_UnstoredLineHasEmptyLineID (AC-2 edge, per the
// task's own wording -- LineID is empty for an unstored line): a LineItem
// with ID == "" must map to LineID == "", never a fabricated non-empty id.
func TestSubmissionCanonical_UnstoredLineHasEmptyLineID(t *testing.T) {
	inv := Invoice{
		LineItems: []LineItem{
			{ID: "", LineNo: 1, Description: strPtr("unsaved line")},
		},
	}

	got := SubmissionCanonical(inv)

	if len(got.Lines) != 1 {
		t.Fatalf("len(Lines) = %d, want 1", len(got.Lines))
	}
	if got.Lines[0].LineID != "" {
		t.Errorf("Lines[0].LineID = %q, want \"\" (unstored line must not get a fabricated id)", got.Lines[0].LineID)
	}
}

// TestSubmissionCanonical_MoneyStringsPassThroughByteIdentical (AC-1/AC-3
// edge): the mapper is a pure projection, never a validator -- malformed,
// oddly-formatted, empty, or very long money strings must cross byte-for-
// byte unchanged, proving the mapper does no parsing/reformatting/validation.
func TestSubmissionCanonical_MoneyStringsPassThroughByteIdentical(t *testing.T) {
	longDigits := ""
	for i := 0; i < 200; i++ {
		longDigits += "9"
	}
	cases := []string{"NaN", "1,000.00", "", longDigits, "-0.00", "1e10"}

	for _, money := range cases {
		inv := Invoice{
			Currency: strPtr(money),
			Subtotal: strPtr(money),
			VAT:      strPtr(money),
			Total:    strPtr(money),
			LineItems: []LineItem{
				{ID: "l1", LineNo: 1, Quantity: strPtr(money), UnitPrice: strPtr(money), LineTotal: strPtr(money), LineTax: strPtr(money)},
			},
		}

		got := SubmissionCanonical(inv)

		checks := map[string]*string{
			"Currency": got.Currency,
			"Subtotal": got.Subtotal,
			"VAT":      got.VAT,
			"Total":    got.Total,
		}
		for name, ptr := range checks {
			if ptr == nil || *ptr != money {
				t.Errorf("money=%q: %s = %s, want %q", money, name, strOrNil(ptr), money)
			}
		}
		line := got.Lines[0]
		lineChecks := map[string]*string{
			"Lines[0].Quantity":  line.Quantity,
			"Lines[0].UnitPrice": line.UnitPrice,
			"Lines[0].LineTotal": line.LineTotal,
			"Lines[0].LineTax":   line.LineTax,
		}
		for name, ptr := range lineChecks {
			if ptr == nil || *ptr != money {
				t.Errorf("money=%q: %s = %s, want %q", money, name, strOrNil(ptr), money)
			}
		}
	}
}

// TestSubmissionCanonical_MultiByteAndControlCharsInSupplierName (AC-1
// edge): multi-byte, RTL, and control characters in SupplierName must pass
// through unchanged -- the mapper does no sanitization/normalization.
func TestSubmissionCanonical_MultiByteAndControlCharsInSupplierName(t *testing.T) {
	cases := []string{
		"艾克森美孚公司",             // multi-byte CJK
		"شركة النفط الوطنية",  // Arabic RTL
		"‮evil‬ Co",           // RTL override control chars
		"Tab\tNewline\nCo",    // control characters
		"Zürich Ünïcode Ñame", // accented Latin
	}

	for _, name := range cases {
		inv := Invoice{SupplierName: strPtr(name)}

		got := SubmissionCanonical(inv)

		if got.Supplier.Name == nil || *got.Supplier.Name != name {
			t.Errorf("SupplierName = %q: Supplier.Name = %s, want %q", name, strOrNil(got.Supplier.Name), name)
		}
	}
}

// TestSubmissionCanonical_SharesDescriptionPointerAcrossLines (AC-3
// clarification): the mapper is a projection, not a deep-copier -- when two
// source LineItems share the same *string Description pointer, the mapped
// CanonicalLines must share that SAME pointer too (not each get an
// independently-allocated copy). This is intentional: SubmissionCanonical's
// own contract is "no I/O, pure projection", and copying every string
// pointer would be unnecessary allocation this task never asked for. It is
// the CALLER's job (not this mapper's) to avoid mutating shared backing
// storage after projection, exactly like DoesNotMutateInput never claims the
// mapper defends against a mutation performed by someone else after it
// returns.
func TestSubmissionCanonical_SharesDescriptionPointerAcrossLines(t *testing.T) {
	shared := strPtr("Shared Description")
	inv := Invoice{
		LineItems: []LineItem{
			{ID: "a", LineNo: 1, Description: shared},
			{ID: "b", LineNo: 2, Description: shared},
		},
	}

	got := SubmissionCanonical(inv)

	if len(got.Lines) != 2 {
		t.Fatalf("len(Lines) = %d, want 2", len(got.Lines))
	}
	if got.Lines[0].Description != shared {
		t.Errorf("Lines[0].Description = %p, want the same pointer as shared (%p)", got.Lines[0].Description, shared)
	}
	if got.Lines[1].Description != shared {
		t.Errorf("Lines[1].Description = %p, want the same pointer as shared (%p)", got.Lines[1].Description, shared)
	}
	if got.Lines[0].Description != got.Lines[1].Description {
		t.Errorf("Lines[0].Description (%p) and Lines[1].Description (%p) should be the SAME shared pointer", got.Lines[0].Description, got.Lines[1].Description)
	}
}
