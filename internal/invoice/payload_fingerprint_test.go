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
//     line items, ...) must leave the fingerprint UNCHANGED. This is not
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
	baseFP := contentFingerprint(base)

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
			mutatedFP := contentFingerprint(mutated)
			if mutatedFP == baseFP {
				t.Errorf("contentFingerprint unchanged after mutating %s: both %q -- this "+
					"content column must be part of the fingerprint [AC#9]", field, baseFP)
			}
		})
	}
}

// TestContentFingerprint_NonContentFieldsAreIgnored (AC #9, "only if"
// direction): mutating a field that is NOT one of the ten MBS-content
// columns must leave the fingerprint UNCHANGED. A false-positive change
// here would make [toctou-staleness]'s re-check spuriously fire
// ErrStaleValidation on an invoice whose CONTENT never changed -- e.g. a
// concurrent status transition or an audit-only write between the
// fingerprint-taken and fingerprint-rechecked reads.
func TestContentFingerprint_NonContentFieldsAreIgnored(t *testing.T) {
	base := fullFingerprintFixture()
	baseFP := contentFingerprint(base)

	mutations := map[string]func(*Invoice){
		"ID":               func(i *Invoice) { i.ID = "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz" },
		"EntityID":         func(i *Invoice) { i.EntityID = "yyyyyyyy-yyyy-yyyy-yyyy-yyyyyyyyyyyy" },
		"ImportBatchID":    func(i *Invoice) { i.ImportBatchID = strPtr("xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx") },
		"Status":           func(i *Invoice) { i.Status = StatusValidated },
		"CreatedAt":        func(i *Invoice) { i.CreatedAt = time.Now() },
		"RuleSetVersionID": func(i *Invoice) { i.RuleSetVersionID = strPtr("11111111-2222-3333-4444-555555555555") },
		"LineItems": func(i *Invoice) {
			i.LineItems = []LineItem{{ID: "different-line", LineNo: 7, UnitPrice: strPtr("9999.00")}}
		},
		"LineItemsEmptied": func(i *Invoice) { i.LineItems = nil },
	}

	for field, mutate := range mutations {
		field, mutate := field, mutate
		t.Run(field, func(t *testing.T) {
			mutated := fullFingerprintFixture()
			mutate(&mutated)
			mutatedFP := contentFingerprint(mutated)
			if mutatedFP != baseFP {
				t.Errorf("contentFingerprint changed after mutating non-content field %s: "+
					"%q -> %q -- only the ten MBS-content columns may affect the fingerprint; "+
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

	fpNull := contentFingerprint(nullCurrency)
	fpEmpty := contentFingerprint(emptyCurrency)
	if fpNull == fpEmpty {
		t.Errorf("contentFingerprint(Currency=nil) == contentFingerprint(Currency=\"\") "+
			"(%q) -- a NULL column must fingerprint differently from an empty-string column",
			fpNull)
	}
}
