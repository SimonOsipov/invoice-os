// QA Mode-B adversarial coverage for task-108 / M4-04-02, added AFTER
// implementation. AC #9 states contentFingerprint "changes IFF any of the
// ten content columns changes" -- PAY-20 (payload_test.go) exercises
// exactly one of those ten (VAT) plus the identical-invoice case. This file
// extends that to an "iff" a single-field spec cannot establish on its own:
//
//   - the "if" direction for the OTHER nine content columns (a bug that
//     dropped, say, SupplierName from writeFingerprintField's call list
//     would go undetected by PAY-20 alone);
//   - the "only if" direction: mutating a NON-content column (id, status,
//     import_batch_id, ...) must leave the fingerprint UNCHANGED. Line items
//     were on that list until INVED-01-02, which made them CONTENT: a line
//     added, removed, reordered or edited now moves the fingerprint, exactly
//     like a header change. Only the line `id` stays excluded
//     ([fingerprint-excludes-line-ids]). This is not
//     cosmetic -- [toctou-staleness] compares a fingerprint taken before
//     the 04 round trip against one recomputed from the locked row inside
//     the write tx. If a non-content field's mutation spuriously changed
//     the fingerprint, an ordinary status transition or audit write
//     happening between those two reads would falsely trip
//     ErrStaleValidation on a perfectly valid, unmodified-content invoice.
//   - the NULL-vs-empty-string distinction the doc comment claims
//     ("a NULL is distinct from \"\"") -- untested by PAY-20, which never
//     sets a field to "".
package invoice

import (
	"fmt"
	"testing"
	"time"
)

// fullFingerprintFixture is a base Invoice with all ten content columns set
// to distinct, non-empty values, plus every non-content field also
// populated (so mutating them away from a real value is a meaningful
// change, not nil->nil).
func fullFingerprintFixture() Invoice {
	d := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return Invoice{
		ID:            "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		EntityID:      "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		ImportBatchID: strPtr("cccccccc-cccc-cccc-cccc-cccccccccccc"),
		InvoiceNumber: "INV-100",
		Status:        StatusDraft,
		IssueDate:     &d,
		SupplierTIN:   strPtr("12345678-0001"),
		SupplierName:  strPtr("Acme"),
		BuyerTIN:      strPtr("87654321-0002"),
		BuyerName:     strPtr("Beta"),
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("1000.00"),
		VAT:           strPtr("75.00"),
		Total:         strPtr("1075.00"),
		LineItems: []LineItem{
			{ID: "line-a", LineNo: 1, UnitPrice: strPtr("1000.00")},
		},
	}
}

// TestContentFingerprint_EachOfTenContentColumnsIsSignificant (AC #9, "if"
// direction, all ten): mutating any ONE of the ten MBS-content columns away
// from the base fixture must change the fingerprint. PAY-20 only proves
// this for VAT; a regression that dropped a field from
// writeFingerprintField's call list (e.g. forgot BuyerName after a merge)
// would still pass PAY-20 but fail here.
func TestContentFingerprint_EachOfTenContentColumnsIsSignificant(t *testing.T) {
	base := fullFingerprintFixture()
	baseFP := contentFingerprint(base, base.LineItems)

	mutations := map[string]func(*Invoice){
		"InvoiceNumber": func(i *Invoice) { i.InvoiceNumber = "INV-999" },
		"IssueDate":     func(i *Invoice) { d := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC); i.IssueDate = &d },
		"SupplierTIN":   func(i *Invoice) { i.SupplierTIN = strPtr("99999999-0009") },
		"SupplierName":  func(i *Invoice) { i.SupplierName = strPtr("Different Supplier") },
		"BuyerTIN":      func(i *Invoice) { i.BuyerTIN = strPtr("11111111-0001") },
		"BuyerName":     func(i *Invoice) { i.BuyerName = strPtr("Different Buyer") },
		"Currency":      func(i *Invoice) { i.Currency = strPtr("USD") },
		"Subtotal":      func(i *Invoice) { i.Subtotal = strPtr("2000.00") },
		"VAT":           func(i *Invoice) { i.VAT = strPtr("150.00") },
		"Total":         func(i *Invoice) { i.Total = strPtr("2150.00") },
	}

	if len(mutations) != 10 {
		t.Fatalf("test fixture bug: %d mutations defined, want exactly 10 (the ten MBS-content columns)", len(mutations))
	}

	for field, mutate := range mutations {
		field, mutate := field, mutate
		t.Run(field, func(t *testing.T) {
			mutated := fullFingerprintFixture()
			mutate(&mutated)
			mutatedFP := contentFingerprint(mutated, mutated.LineItems)
			if mutatedFP == baseFP {
				t.Errorf("contentFingerprint unchanged after mutating %s: both %q -- this "+
					"content column must be part of the fingerprint [AC#9]", field, baseFP)
			}
		})
	}
}

// TestContentFingerprint_NonContentFieldsAreIgnored (AC #9, "only if"
// direction): mutating a field that is not part of the invoice's MBS content
// -- i.e. neither one of the ten content columns nor a line item -- must
// leave the fingerprint UNCHANGED. A false-positive change here would make
// [toctou-staleness]'s re-check spuriously fire ErrStaleValidation on an
// invoice whose CONTENT never changed -- e.g. a concurrent status transition
// or an audit-only write between the fingerprint-taken and
// fingerprint-rechecked reads.
//
// The LineItems/LineItemsEmptied entries were REMOVED here by INVED-01-02:
// they asserted the exact inverse of that subtask's AC #3 (lines are now
// content). The "only if" direction for line ids survives as INV-02-T5.
func TestContentFingerprint_NonContentFieldsAreIgnored(t *testing.T) {
	base := fullFingerprintFixture()
	baseFP := contentFingerprint(base, base.LineItems)

	mutations := map[string]func(*Invoice){
		"ID":               func(i *Invoice) { i.ID = "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz" },
		"EntityID":         func(i *Invoice) { i.EntityID = "yyyyyyyy-yyyy-yyyy-yyyy-yyyyyyyyyyyy" },
		"ImportBatchID":    func(i *Invoice) { i.ImportBatchID = strPtr("xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx") },
		"Status":           func(i *Invoice) { i.Status = StatusValidated },
		"CreatedAt":        func(i *Invoice) { i.CreatedAt = time.Now() },
		"RuleSetVersionID": func(i *Invoice) { i.RuleSetVersionID = strPtr("11111111-2222-3333-4444-555555555555") },
	}

	for field, mutate := range mutations {
		field, mutate := field, mutate
		t.Run(field, func(t *testing.T) {
			mutated := fullFingerprintFixture()
			mutate(&mutated)
			mutatedFP := contentFingerprint(mutated, mutated.LineItems)
			if mutatedFP != baseFP {
				t.Errorf("contentFingerprint changed after mutating non-content field %s: "+
					"%q -> %q -- only the ten MBS-content columns and the line items may "+
					"affect the fingerprint; "+
					"a spurious change here would falsely trip [toctou-staleness]'s "+
					"ErrStaleValidation on an invoice whose content never changed [AC#9]",
					field, baseFP, mutatedFP)
			}
		})
	}
}

// TestContentFingerprint_NullDistinctFromEmptyString (doc comment claim: "a
// NULL is distinct from \"\""). A column that is SQL NULL (*string nil)
// must fingerprint differently from the same column holding the empty
// string -- the mapper's own [payload-absence] rule treats them
// differently on the wire (omitted vs a blank value violating `required`),
// so the fingerprint must not conflate them either.
func TestContentFingerprint_NullDistinctFromEmptyString(t *testing.T) {
	nullCurrency := fullFingerprintFixture()
	nullCurrency.Currency = nil

	emptyCurrency := fullFingerprintFixture()
	emptyCurrency.Currency = strPtr("")

	fpNull := contentFingerprint(nullCurrency, nullCurrency.LineItems)
	fpEmpty := contentFingerprint(emptyCurrency, emptyCurrency.LineItems)
	if fpNull == fpEmpty {
		t.Errorf("contentFingerprint(Currency=nil) == contentFingerprint(Currency=\"\") "+
			"(%q) -- a NULL column must fingerprint differently from an empty-string column",
			fpNull)
	}
}

// --- INVED-01-02: line items as content (INV-02-T1..T9, T13) --------------
//
// RED (Stage 2.5, Mode A): contentFingerprint's signature already takes
// `lines []LineItem` (R0, commit 939aef2) but the body still hashes only the
// ten header fields -- `lines` is accepted and ignored. T1/T2/T3/T4/T6/T7
// fail on that gap; T5/T8/T9/T13 pass already, as regression/behaviour
// guards for the GREEN step, not RED-provers -- see each doc comment.

// twoLineFingerprintFixture is fullFingerprintFixture's header with exactly
// two distinct, fully-populated line items (all five MBS fields set on
// each), for INV-02-T1/T4/T7/T9's "invoice with 2 lines" specs.
func twoLineFingerprintFixture() Invoice {
	inv := fullFingerprintFixture()
	inv.LineItems = []LineItem{
		{ID: "line-a", LineNo: 1, Description: strPtr("Widget"), Quantity: strPtr("2"),
			UnitPrice: strPtr("100.00"), LineTotal: strPtr("200.00"), LineTax: strPtr("15.00")},
		{ID: "line-b", LineNo: 2, Description: strPtr("Gadget"), Quantity: strPtr("1"),
			UnitPrice: strPtr("50.00"), LineTotal: strPtr("50.00"), LineTax: strPtr("3.75")},
	}
	return inv
}

// threeLineFingerprintFixture is fullFingerprintFixture's header with three
// distinct, fully-populated line items, for INV-02-T3/T8's specs.
func threeLineFingerprintFixture() Invoice {
	inv := fullFingerprintFixture()
	inv.LineItems = []LineItem{
		{ID: "line-a", LineNo: 1, Description: strPtr("First"), Quantity: strPtr("1"),
			UnitPrice: strPtr("10.00"), LineTotal: strPtr("10.00"), LineTax: strPtr("0.75")},
		{ID: "line-b", LineNo: 2, Description: strPtr("Second"), Quantity: strPtr("2"),
			UnitPrice: strPtr("20.00"), LineTotal: strPtr("40.00"), LineTax: strPtr("3.00")},
		{ID: "line-c", LineNo: 3, Description: strPtr("Third"), Quantity: strPtr("3"),
			UnitPrice: strPtr("30.00"), LineTotal: strPtr("90.00"), LineTax: strPtr("6.75")},
	}
	return inv
}

// TestContentFingerprint_EachLineFieldIsSignificant (INV-02-T1): mutating
// exactly one of a line's five MBS fields (description/quantity/unit_price/
// line_total/line_tax) must move the fingerprint -- mirrors
// TestContentFingerprint_EachOfTenContentColumnsIsSignificant's "if" proof,
// replayed across the line fields §B's Field-set decision adds.
func TestContentFingerprint_EachLineFieldIsSignificant(t *testing.T) {
	base := twoLineFingerprintFixture()
	baseFP := contentFingerprint(base, base.LineItems)

	mutations := map[string]func(*LineItem){
		"Description": func(li *LineItem) { li.Description = strPtr("Different") },
		"Quantity":    func(li *LineItem) { li.Quantity = strPtr("99") },
		"UnitPrice":   func(li *LineItem) { li.UnitPrice = strPtr("999.00") },
		"LineTotal":   func(li *LineItem) { li.LineTotal = strPtr("999.00") },
		"LineTax":     func(li *LineItem) { li.LineTax = strPtr("99.00") },
	}
	if len(mutations) != 5 {
		t.Fatalf("test fixture bug: %d mutations defined, want exactly 5 (the five MBS line columns)", len(mutations))
	}

	for field, mutate := range mutations {
		field, mutate := field, mutate
		t.Run(field, func(t *testing.T) {
			mutated := twoLineFingerprintFixture()
			mutate(&mutated.LineItems[0])
			mutatedFP := contentFingerprint(mutated, mutated.LineItems)
			if mutatedFP == baseFP {
				t.Errorf("contentFingerprint unchanged after mutating line field %s: both %q -- "+
					"this line column must be part of the fingerprint [INV-02-T1]", field, baseFP)
			}
		})
	}
}

// TestContentFingerprint_AppendedLineChangesFingerprint (INV-02-T2):
// appending a 3rd line to a 2-line invoice must change the fingerprint.
func TestContentFingerprint_AppendedLineChangesFingerprint(t *testing.T) {
	base := twoLineFingerprintFixture()
	baseFP := contentFingerprint(base, base.LineItems)

	appended := twoLineFingerprintFixture()
	appended.LineItems = append(appended.LineItems, LineItem{
		ID: "line-c", LineNo: 3, Description: strPtr("Extra"), Quantity: strPtr("1"),
		UnitPrice: strPtr("10.00"), LineTotal: strPtr("10.00"), LineTax: strPtr("0.75"),
	})
	appendedFP := contentFingerprint(appended, appended.LineItems)

	if appendedFP == baseFP {
		t.Errorf("contentFingerprint unchanged after appending a 3rd line: both %q [INV-02-T2]", baseFP)
	}
}

// TestContentFingerprint_RemovedAndRenumberedLineChangesFingerprint
// (INV-02-T3): removing the middle line of a 3-line invoice and renumbering
// the survivor to 1..2 -- the shape a real replace-all-lines save produces --
// must change the fingerprint.
func TestContentFingerprint_RemovedAndRenumberedLineChangesFingerprint(t *testing.T) {
	base := threeLineFingerprintFixture()
	baseFP := contentFingerprint(base, base.LineItems)

	shortened := threeLineFingerprintFixture()
	kept := []LineItem{shortened.LineItems[0], shortened.LineItems[2]}
	kept[0].LineNo = 1
	kept[1].LineNo = 2
	shortened.LineItems = kept
	shortenedFP := contentFingerprint(shortened, shortened.LineItems)

	if shortenedFP == baseFP {
		t.Errorf("contentFingerprint unchanged after removing the middle line and renumbering the survivor: "+
			"both %q [INV-02-T3]", baseFP)
	}
}

// TestContentFingerprint_LineNoIsContent (INV-02-T4): reassigning which
// line_no carries which content -- the SAME two content tuples, attached to
// the OTHER line_no -- must change the fingerprint. This is what makes the
// spec meaningful with §C's sort in place: a plain reordering of the
// argument SLICE (line_no values held fixed) would sort back to the same
// canonical sequence and correctly show NO change; only an actual
// renumbering -- content reassigned to a different line_no -- proves
// line_no itself is hashed, not merely used to cancel out caller order.
func TestContentFingerprint_LineNoIsContent(t *testing.T) {
	base := twoLineFingerprintFixture()
	baseFP := contentFingerprint(base, base.LineItems)

	swapped := twoLineFingerprintFixture()
	swapped.LineItems[0].LineNo, swapped.LineItems[1].LineNo = swapped.LineItems[1].LineNo, swapped.LineItems[0].LineNo
	swappedFP := contentFingerprint(swapped, swapped.LineItems)

	if swappedFP == baseFP {
		t.Errorf("contentFingerprint unchanged after swapping which line_no carries which content: "+
			"both %q -- line_no must be part of the fingerprint, not just an ordering key [INV-02-T4]", baseFP)
	}
}

// TestContentFingerprint_LineIDsAreNotContent (INV-02-T5, guard): two
// invoices whose lines carry identical content but DIFFERENT ids must
// fingerprint EQUAL -- [fingerprint-excludes-line-ids]. This is the "only
// if" half of the LineItems/LineItemsEmptied entries removed from
// TestContentFingerprint_NonContentFieldsAreIgnored by INVED-01-02 (§F):
// those asserted the whole line was non-content, which INV-02-T1..T4 now
// disprove; only the line id survives as excluded.
func TestContentFingerprint_LineIDsAreNotContent(t *testing.T) {
	a := twoLineFingerprintFixture()
	b := twoLineFingerprintFixture()
	// Same content, deliberately different ids on both lines -- exactly what
	// a replace-all save produces every time (fresh uuids minted per write).
	b.LineItems[0].ID = "totally-different-id-a"
	b.LineItems[1].ID = "totally-different-id-b"

	fpA := contentFingerprint(a, a.LineItems)
	fpB := contentFingerprint(b, b.LineItems)
	if fpA != fpB {
		t.Errorf("contentFingerprint(id=%q) = %q, contentFingerprint(id=%q) = %q -- line ids must NOT affect "+
			"the fingerprint [fingerprint-excludes-line-ids] [INV-02-T5]",
			a.LineItems[0].ID, fpA, b.LineItems[0].ID, fpB)
	}
}

// TestContentFingerprint_ZeroLinesDiffersFromOneAllNullLine (INV-02-T6):
// zero lines and exactly one line whose five MBS fields are all NULL must
// fingerprint DIFFERENTLY -- the line-count marker (len(lines)) is itself
// part of the hash, so "no lines" and "one line of no content" cannot
// collide.
func TestContentFingerprint_ZeroLinesDiffersFromOneAllNullLine(t *testing.T) {
	zero := fullFingerprintFixture()
	zero.LineItems = nil
	zeroFP := contentFingerprint(zero, zero.LineItems)

	oneNull := fullFingerprintFixture()
	oneNull.LineItems = []LineItem{{ID: "line-null", LineNo: 1}}
	oneNullFP := contentFingerprint(oneNull, oneNull.LineItems)

	if zeroFP == oneNullFP {
		t.Errorf("contentFingerprint(0 lines) == contentFingerprint(1 all-NULL line) (%q) -- "+
			"the line count marker must distinguish them [INV-02-T6]", zeroFP)
	}
}

// TestContentFingerprint_LineEncodingStaysInjective (INV-02-T7): the same
// concatenation-collision shape the header encoding must resist
// (("ab","c") vs ("a","bc")), replayed across a LINE boundary --
// [{description:"ab"},{description:"c"}] must not collide with
// [{description:"a"},{description:"bc"}].
func TestContentFingerprint_LineEncodingStaysInjective(t *testing.T) {
	base := fullFingerprintFixture()

	abC := base
	abC.LineItems = []LineItem{
		{ID: "l1", LineNo: 1, Description: strPtr("ab")},
		{ID: "l2", LineNo: 2, Description: strPtr("c")},
	}
	aBc := base
	aBc.LineItems = []LineItem{
		{ID: "l1", LineNo: 1, Description: strPtr("a")},
		{ID: "l2", LineNo: 2, Description: strPtr("bc")},
	}

	fpABC := contentFingerprint(abC, abC.LineItems)
	fpABc := contentFingerprint(aBc, aBc.LineItems)
	if fpABC == fpABc {
		t.Errorf("contentFingerprint([{%q},{%q}]) == contentFingerprint([{%q},{%q}]) (%q) -- the "+
			"length-prefixed encoding must stay injective across a line boundary [INV-02-T7]",
			"ab", "c", "a", "bc", fpABC)
	}
}

// TestContentFingerprint_PureAndDoesNotMutateCallerSlice (INV-02-T8, guard):
// two calls with the SAME arguments return identical fingerprints, and --
// the load-bearing half -- the caller's line slice is not reordered or
// otherwise mutated by the call. gate.go:169 passes inv.LineItems, the SAME
// slice MBSPayload was built from; an in-place sort there would silently
// corrupt the payload the fingerprint is supposed to describe ([toctou-
// staleness] compares against exactly that slice).
func TestContentFingerprint_PureAndDoesNotMutateCallerSlice(t *testing.T) {
	inv := threeLineFingerprintFixture()
	// Deliberately out of line_no order, so an in-place sort would be
	// detectable.
	inv.LineItems = []LineItem{inv.LineItems[2], inv.LineItems[0], inv.LineItems[1]}
	original := append([]LineItem(nil), inv.LineItems...)

	fp1 := contentFingerprint(inv, inv.LineItems)
	fp2 := contentFingerprint(inv, inv.LineItems)
	if fp1 != fp2 {
		t.Errorf("contentFingerprint(same args) = %q then %q, want identical (deterministic) [INV-02-T8]", fp1, fp2)
	}

	if len(inv.LineItems) != len(original) {
		t.Fatalf("caller's LineItems length changed: %d -> %d, want unchanged [INV-02-T8]", len(original), len(inv.LineItems))
	}
	for i := range original {
		if inv.LineItems[i] != original[i] {
			t.Errorf("caller's LineItems[%d] = %+v after the call, want unchanged %+v -- contentFingerprint "+
				"must sort a defensive COPY, never the caller's own slice [INV-02-T8]",
				i, inv.LineItems[i], original[i])
		}
	}
}

// TestContentFingerprint_HeaderChangeStillSignificantWithLines (INV-02-T9,
// guard): a header-field change on an invoice that ALSO carries line items
// must still change the fingerprint -- the pre-existing ten-field behaviour
// (TestContentFingerprint_EachOfTenContentColumnsIsSignificant) must not
// regress now that lines are hashed too.
func TestContentFingerprint_HeaderChangeStillSignificantWithLines(t *testing.T) {
	base := twoLineFingerprintFixture()
	baseFP := contentFingerprint(base, base.LineItems)

	mutated := twoLineFingerprintFixture()
	mutated.VAT = strPtr("999.99")
	mutatedFP := contentFingerprint(mutated, mutated.LineItems)

	if mutatedFP == baseFP {
		t.Errorf("contentFingerprint unchanged after a header (VAT) mutation on a lined invoice: both %q -- "+
			"the ten header fields must remain significant now lines are hashed too [INV-02-T9]", baseFP)
	}
}

// TestContentFingerprint_NilAndEmptyLinesAreIdentical (INV-02-T13, guard):
// lines=nil and lines=[]LineItem{} must fingerprint EQUAL. Concrete
// consequence across INVED-01-02/04: hydrateLinesTx returns nil for a
// lineless invoice, while INVED-01-04's replaceLinesTx may return
// []LineItem{} for the same invoice -- a divergence here would make the
// no-op check and the staleness guard disagree on whether an edit happened.
func TestContentFingerprint_NilAndEmptyLinesAreIdentical(t *testing.T) {
	base := fullFingerprintFixture()

	nilLines := base
	nilLines.LineItems = nil
	emptyLines := base
	emptyLines.LineItems = []LineItem{}

	fpNil := contentFingerprint(nilLines, nilLines.LineItems)
	fpEmpty := contentFingerprint(emptyLines, emptyLines.LineItems)
	if fpNil != fpEmpty {
		t.Errorf("contentFingerprint(lines=nil) = %q != contentFingerprint(lines=[]LineItem{}) = %q, want "+
			"equal [INV-02-T13]", fpNil, fpEmpty)
	}
}

// --- QA adversarial coverage (Mode B), added AFTER implementation ---------

// TestContentFingerprint_LineNumericNullVsEmptyVsZeroAreDistinct (QA
// adversarial): a line's numeric field must fingerprint DIFFERENTLY across
// three genuinely different wire values -- NULL (omitted, [payload-absence]),
// the empty string (present, blank) and the literal "0" (present, a real
// zero) -- pairwise. TestContentFingerprint_NullDistinctFromEmptyString only
// proves the NULL-vs-"" half, on a HEADER field, and never covers "0" at
// all; a regression that, say, treated an empty numeric string as
// equivalent to zero somewhere upstream of the hash would pass every
// existing spec but silently conflate two distinct DB states here.
func TestContentFingerprint_LineNumericNullVsEmptyVsZeroAreDistinct(t *testing.T) {
	build := func(qty *string) Invoice {
		inv := fullFingerprintFixture()
		inv.LineItems = []LineItem{{ID: "l1", LineNo: 1, Quantity: qty}}
		return inv
	}

	nullInv, emptyInv, zeroInv := build(nil), build(strPtr("")), build(strPtr("0"))
	fpNull := contentFingerprint(nullInv, nullInv.LineItems)
	fpEmpty := contentFingerprint(emptyInv, emptyInv.LineItems)
	fpZero := contentFingerprint(zeroInv, zeroInv.LineItems)

	if fpNull == fpEmpty {
		t.Errorf("contentFingerprint(Quantity=nil) == contentFingerprint(Quantity=\"\") (%q)", fpNull)
	}
	if fpNull == fpZero {
		t.Errorf("contentFingerprint(Quantity=nil) == contentFingerprint(Quantity=\"0\") (%q)", fpNull)
	}
	if fpEmpty == fpZero {
		t.Errorf("contentFingerprint(Quantity=\"\") == contentFingerprint(Quantity=\"0\") (%q)", fpEmpty)
	}
}

// TestContentFingerprint_LineFieldDelimiterCharsStayInjective (QA
// adversarial): INV-02-T7 proves injectivity across a LINE boundary using
// plain alphabetic content ("ab"/"c" vs "a"/"bc"). This replays the same
// boundary-shift attack but embeds the encoder's OWN delimiter syntax
// ("S<len>:", ";") and a bare digit run inside the field VALUES themselves
// -- content an attacker (or a legitimately weird invoice description)
// could supply -- to prove the length-prefix, not any character pattern in
// the content, is what the parser (conceptually) relies on. Moving the 'a'
// character across the Description/Quantity boundary while keeping
// delimiter-lookalike substrings in both fields must still change the hash.
func TestContentFingerprint_LineFieldDelimiterCharsStayInjective(t *testing.T) {
	base := fullFingerprintFixture()

	variantA := base
	variantA.LineItems = []LineItem{
		{ID: "l1", LineNo: 1, Description: strPtr("5:x"), Quantity: strPtr("a;b")},
	}
	variantB := base
	variantB.LineItems = []LineItem{
		{ID: "l1", LineNo: 1, Description: strPtr("5:xa"), Quantity: strPtr(";b")},
	}

	fpA := contentFingerprint(variantA, variantA.LineItems)
	fpB := contentFingerprint(variantB, variantB.LineItems)
	if fpA == fpB {
		t.Errorf("contentFingerprint([{desc:%q,qty:%q}]) == contentFingerprint([{desc:%q,qty:%q}]) (%q) -- "+
			"the length-prefixed encoding must stay injective even when field VALUES embed delimiter-lookalike "+
			"characters (\"S5:\", \";\")",
			"5:x", "a;b", "5:xa", ";b", fpA)
	}
}

// TestContentFingerprint_FiftyLinesRemainsSensitive (QA adversarial, scale):
// contentFingerprint must not panic, silently truncate, or lose sensitivity
// on an invoice with many lines. Mutating a SINGLE line (the 37th of 50)
// must still move the fingerprint -- confirming the per-field/per-line
// hashing discipline holds at a size no other spec exercises (the largest
// elsewhere is threeLineFingerprintFixture's 3).
func TestContentFingerprint_FiftyLinesRemainsSensitive(t *testing.T) {
	const n = 50
	base := fullFingerprintFixture()
	lines := make([]LineItem, n)
	for i := range lines {
		desc := fmt.Sprintf("Line %d", i+1)
		price := fmt.Sprintf("%d.00", i+1)
		lines[i] = LineItem{ID: fmt.Sprintf("line-%d", i+1), LineNo: i + 1, Description: &desc, UnitPrice: &price}
	}
	base.LineItems = lines
	baseFP := contentFingerprint(base, base.LineItems)
	if baseFP == "" {
		t.Fatal("contentFingerprint(50 lines) is empty -- want a real hash")
	}

	mutated := base
	mutatedLines := append([]LineItem(nil), lines...)
	mutatedDesc := "MUTATED"
	mutatedLines[36].Description = &mutatedDesc // the 37th line, 0-indexed
	mutated.LineItems = mutatedLines
	mutatedFP := contentFingerprint(mutated, mutated.LineItems)

	if mutatedFP == baseFP {
		t.Errorf("contentFingerprint unchanged after mutating the 37th of 50 lines: both %q", baseFP)
	}
}

// TestContentFingerprint_LineNoValueIsHashedNotJustASortKey (QA adversarial
// -- closes a coverage gap found by mutation-testing INV-02-T4): T4
// (TestContentFingerprint_LineNoIsContent) swaps line_no between two lines
// of DIFFERENT content and asserts the fingerprint changes -- but that swap
// ALSO flips which content sorts first (the sort key is line_no), so a
// fingerprint change there is explained just as well by the reordering as
// by line_no's own bytes being hashed. Confirmed experimentally: a mutant
// that stops writing line_no into the hash at all (leaving it as a
// SORT-ONLY key) still passes T4 and every other existing spec undetected.
// This test controls for the confound: renumbering 1,2 -> 10,20 preserves
// the RELATIVE order (10 < 20, same as 1 < 2), so the sorted content
// sequence is byte-identical before and after -- the ONLY thing that can
// move the fingerprint is line_no's own encoded value.
func TestContentFingerprint_LineNoValueIsHashedNotJustASortKey(t *testing.T) {
	base := twoLineFingerprintFixture()
	baseFP := contentFingerprint(base, base.LineItems)

	renumbered := twoLineFingerprintFixture()
	renumbered.LineItems[0].LineNo = 10
	renumbered.LineItems[1].LineNo = 20
	renumberedFP := contentFingerprint(renumbered, renumbered.LineItems)

	if renumberedFP == baseFP {
		t.Errorf("contentFingerprint unchanged after renumbering line_no 1,2 -> 10,20 (order-preserving, so "+
			"the sorted CONTENT sequence is unchanged): both %q -- line_no's own bytes must be hashed, not "+
			"merely used to decide sort order", baseFP)
	}
}

// TestContentFingerprint_GoldenDigestPinsTheEncoding (QA adversarial --
// closes a second coverage gap found by mutation-testing): removing the
// explicit line-COUNT marker entirely (`count := strconv.Itoa(len(lines));
// writeFingerprintField(h, &count)`) passes EVERY spec in this file
// undetected, INCLUDING TestContentFingerprint_ZeroLinesDiffersFromOneAllNullLine
// (INV-02-T6), which the implementation's own doc comment claims that
// marker exists to guarantee: a lineless invoice and a one-all-NULL-line
// invoice still differ without it, because line_no alone ("1") is never
// empty. No behavioral assertion phrased as "X != Y" can catch a mutation
// that changes what the STRING fed to sha256 is while every pairwise
// inequality this file checks happens to survive by coincidence. Pinning
// the actual literal digest is the one check that cannot be fooled that
// way: ANY byte-level change to the encoding (marker removed, field
// reordered, delimiter changed, ...) changes this literal.
//
// Deliberately narrow (one lineless fixture, one 2-line fixture) --this is
// a change-detector, not a spec of what the "correct" digest should be.
// Updating the literal is fine after a DELIBERATE, reviewed encoding
// change; it must never be updated to silently paper over an
// undiscussed one.
func TestContentFingerprint_GoldenDigestPinsTheEncoding(t *testing.T) {
	lineless := fullFingerprintFixture()
	lineless.LineItems = nil
	const wantLineless = "0777fab27f14e3c71e3dcde4e250fb9b33cdde5e63c2d44a9f328098e17d7050"
	if got := contentFingerprint(lineless, lineless.LineItems); got != wantLineless {
		t.Errorf("contentFingerprint(lineless fixture) = %s, want %s -- the encoding changed; if that was "+
			"deliberate and reviewed, update this literal, never silently", got, wantLineless)
	}

	lined := twoLineFingerprintFixture()
	const wantLined = "2eae3f13152795c4ddd01a60a77a4cc924a450661e0618971f63b5cc677923fe"
	if got := contentFingerprint(lined, lined.LineItems); got != wantLined {
		t.Errorf("contentFingerprint(2-line fixture) = %s, want %s -- the encoding changed; if that was "+
			"deliberate and reviewed, update this literal, never silently", got, wantLined)
	}
}

// TestContentFingerprint_DuplicateLineNoIsOrderDependent (QA adversarial,
// documents architect-flagged behaviour -- NOT asserted as correct):
// contentFingerprint sorts with sort.SliceStable, whose defining property is
// that TIED keys preserve the CALLER's original relative order. When two
// lines share the same line_no (impossible in the DB --
// line_items_invoice_line_no_uq forbids it -- but not impossible in an
// in-memory []LineItem, e.g. a dry-run CreateInput built from malformed
// import data before any DB constraint runs), the fingerprint depends on
// which one the caller put first. This test exists to make that dependency
// VISIBLE and regression-checkable, not to bless it as desired behaviour.
func TestContentFingerprint_DuplicateLineNoIsOrderDependent(t *testing.T) {
	base := fullFingerprintFixture()
	a := LineItem{ID: "a", LineNo: 1, Description: strPtr("A-content")}
	b := LineItem{ID: "b", LineNo: 1, Description: strPtr("B-content")}

	orderAB := base
	orderAB.LineItems = []LineItem{a, b}
	orderBA := base
	orderBA.LineItems = []LineItem{b, a}

	fpAB := contentFingerprint(orderAB, orderAB.LineItems)
	fpBA := contentFingerprint(orderBA, orderBA.LineItems)
	if fpAB == fpBA {
		t.Errorf("contentFingerprint([a,b]) == contentFingerprint([b,a]) for tied line_no=1 (%q) -- expected "+
			"these to DIFFER: sort.SliceStable preserves caller order for tied keys, so a duplicate line_no "+
			"is order-dependent by construction. If this now fails, the sort or its comparator changed and "+
			"this documented behaviour needs re-verifying, not silently accepting", fpAB)
	}
}
