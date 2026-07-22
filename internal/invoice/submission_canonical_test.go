package invoice

import (
	"reflect"
	"testing"
	"time"
)

// strOrNil renders a *string for a t.Errorf message -- "nil" or the pointee
// value, never a raw pointer address (the zero-value default for %v on a
// non-nil *string).
func strOrNil(s *string) string {
	if s == nil {
		return "nil"
	}
	return *s
}

// deepCopyInvoice allocates NEW backing values for every pointer/slice
// element of inv (not just copies of the pointers) so
// TestSubmissionCanonical_DoesNotMutateInput can detect a mutation through a
// shared pointer -- a shallow `cp := inv` would leave cp and inv pointing at
// the same backing storage and the test would pass even if the mapper wrote
// through a field.
func deepCopyInvoice(inv Invoice) Invoice {
	cp := inv
	if inv.IssueDate != nil {
		t := *inv.IssueDate
		cp.IssueDate = &t
	}
	if inv.SupplierTIN != nil {
		cp.SupplierTIN = strPtr(*inv.SupplierTIN)
	}
	if inv.SupplierName != nil {
		cp.SupplierName = strPtr(*inv.SupplierName)
	}
	if inv.BuyerTIN != nil {
		cp.BuyerTIN = strPtr(*inv.BuyerTIN)
	}
	if inv.BuyerName != nil {
		cp.BuyerName = strPtr(*inv.BuyerName)
	}
	if inv.Currency != nil {
		cp.Currency = strPtr(*inv.Currency)
	}
	if inv.Subtotal != nil {
		cp.Subtotal = strPtr(*inv.Subtotal)
	}
	if inv.VAT != nil {
		cp.VAT = strPtr(*inv.VAT)
	}
	if inv.Total != nil {
		cp.Total = strPtr(*inv.Total)
	}
	if inv.LineItems != nil {
		cp.LineItems = make([]LineItem, len(inv.LineItems))
		for i, li := range inv.LineItems {
			cp.LineItems[i] = deepCopyLineItem(li)
		}
	}
	return cp
}

// deepCopyLineItem is deepCopyInvoice's per-line counterpart -- see that
// function's comment for why a shallow copy would defeat the mutation test.
func deepCopyLineItem(li LineItem) LineItem {
	cp := li
	if li.Description != nil {
		cp.Description = strPtr(*li.Description)
	}
	if li.Quantity != nil {
		cp.Quantity = strPtr(*li.Quantity)
	}
	if li.UnitPrice != nil {
		cp.UnitPrice = strPtr(*li.UnitPrice)
	}
	if li.LineTotal != nil {
		cp.LineTotal = strPtr(*li.LineTotal)
	}
	if li.LineTax != nil {
		cp.LineTax = strPtr(*li.LineTax)
	}
	return cp
}

// fullyPopulatedInvoice builds an Invoice with every field set to a
// non-zero value, including the fields Canonical must NOT carry
// (EntityID/Status/Violations/RuleSetVersionID/CreatedAt/RuleSetVersion) --
// TestSubmissionCanonical_MapsEveryField only asserts on the fields
// Canonical DOES have; Canonical having no field for the rest is enforced
// at compile time, not by this test.
func fullyPopulatedInvoice() Invoice {
	issueDate := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	ruleSetVersionID := "rsv-1"
	ruleSetVersion := 3
	return Invoice{
		ID:               "inv-1",
		EntityID:         "entity-1",
		ImportBatchID:    strPtr("batch-1"),
		InvoiceNumber:    "INV-0001",
		Status:           StatusValidated,
		IssueDate:        &issueDate,
		SupplierTIN:      strPtr("TIN-SUP"),
		SupplierName:     strPtr("Supplier Co"),
		BuyerTIN:         strPtr("TIN-BUY"),
		BuyerName:        strPtr("Buyer Co"),
		Currency:         strPtr("NGN"),
		Subtotal:         strPtr("1000.00"),
		VAT:              strPtr("75.00"),
		Total:            strPtr("1075.00"),
		Violations:       []byte(`[]`),
		RuleSetVersionID: &ruleSetVersionID,
		CreatedAt:        time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC),
		LineItems: []LineItem{
			{
				ID:          "line-1",
				LineNo:      1,
				Description: strPtr("Widget"),
				Quantity:    strPtr("2"),
				UnitPrice:   strPtr("500.00"),
				LineTotal:   strPtr("1000.00"),
				LineTax:     strPtr("75.00"),
			},
		},
		RuleSetVersion: &ruleSetVersion,
	}
}

// TestSubmissionCanonical_MapsEveryField (AC-1): a fully-populated Invoice
// maps to a Canonical whose every field equals its source field, including
// all four money strings verbatim.
func TestSubmissionCanonical_MapsEveryField(t *testing.T) {
	inv := fullyPopulatedInvoice()

	got := SubmissionCanonical(inv)

	if got.InvoiceID != inv.ID {
		t.Errorf("InvoiceID = %q, want %q", got.InvoiceID, inv.ID)
	}
	if got.InvoiceNumber != inv.InvoiceNumber {
		t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, inv.InvoiceNumber)
	}
	if got.IssueDate == nil || inv.IssueDate == nil || !got.IssueDate.Equal(*inv.IssueDate) {
		t.Errorf("IssueDate = %v, want %v", got.IssueDate, inv.IssueDate)
	}
	if got.Supplier.TIN == nil || inv.SupplierTIN == nil || *got.Supplier.TIN != *inv.SupplierTIN {
		t.Errorf("Supplier.TIN = %s, want %s", strOrNil(got.Supplier.TIN), strOrNil(inv.SupplierTIN))
	}
	if got.Supplier.Name == nil || inv.SupplierName == nil || *got.Supplier.Name != *inv.SupplierName {
		t.Errorf("Supplier.Name = %s, want %s", strOrNil(got.Supplier.Name), strOrNil(inv.SupplierName))
	}
	if got.Buyer.TIN == nil || inv.BuyerTIN == nil || *got.Buyer.TIN != *inv.BuyerTIN {
		t.Errorf("Buyer.TIN = %s, want %s", strOrNil(got.Buyer.TIN), strOrNil(inv.BuyerTIN))
	}
	if got.Buyer.Name == nil || inv.BuyerName == nil || *got.Buyer.Name != *inv.BuyerName {
		t.Errorf("Buyer.Name = %s, want %s", strOrNil(got.Buyer.Name), strOrNil(inv.BuyerName))
	}
	if got.Currency == nil || inv.Currency == nil || *got.Currency != *inv.Currency {
		t.Errorf("Currency = %s, want %s", strOrNil(got.Currency), strOrNil(inv.Currency))
	}
	if got.Subtotal == nil || inv.Subtotal == nil || *got.Subtotal != *inv.Subtotal {
		t.Errorf("Subtotal = %s, want %s", strOrNil(got.Subtotal), strOrNil(inv.Subtotal))
	}
	if got.VAT == nil || inv.VAT == nil || *got.VAT != *inv.VAT {
		t.Errorf("VAT = %s, want %s", strOrNil(got.VAT), strOrNil(inv.VAT))
	}
	if got.Total == nil || inv.Total == nil || *got.Total != *inv.Total {
		t.Errorf("Total = %s, want %s", strOrNil(got.Total), strOrNil(inv.Total))
	}
	if len(got.Lines) != len(inv.LineItems) {
		t.Fatalf("len(Lines) = %d, want %d", len(got.Lines), len(inv.LineItems))
	}
	gotLine := got.Lines[0]
	srcLine := inv.LineItems[0]
	if gotLine.LineID != srcLine.ID {
		t.Errorf("Lines[0].LineID = %q, want %q", gotLine.LineID, srcLine.ID)
	}
	if gotLine.LineNo != srcLine.LineNo {
		t.Errorf("Lines[0].LineNo = %d, want %d", gotLine.LineNo, srcLine.LineNo)
	}
	if gotLine.Description == nil || srcLine.Description == nil || *gotLine.Description != *srcLine.Description {
		t.Errorf("Lines[0].Description = %s, want %s", strOrNil(gotLine.Description), strOrNil(srcLine.Description))
	}
	if gotLine.Quantity == nil || srcLine.Quantity == nil || *gotLine.Quantity != *srcLine.Quantity {
		t.Errorf("Lines[0].Quantity = %s, want %s", strOrNil(gotLine.Quantity), strOrNil(srcLine.Quantity))
	}
	if gotLine.UnitPrice == nil || srcLine.UnitPrice == nil || *gotLine.UnitPrice != *srcLine.UnitPrice {
		t.Errorf("Lines[0].UnitPrice = %s, want %s", strOrNil(gotLine.UnitPrice), strOrNil(srcLine.UnitPrice))
	}
	if gotLine.LineTotal == nil || srcLine.LineTotal == nil || *gotLine.LineTotal != *srcLine.LineTotal {
		t.Errorf("Lines[0].LineTotal = %s, want %s", strOrNil(gotLine.LineTotal), strOrNil(srcLine.LineTotal))
	}
	if gotLine.LineTax == nil || srcLine.LineTax == nil || *gotLine.LineTax != *srcLine.LineTax {
		t.Errorf("Lines[0].LineTax = %s, want %s", strOrNil(gotLine.LineTax), strOrNil(srcLine.LineTax))
	}
}

// TestSubmissionCanonical_NilStaysNil (AC-1): an Invoice with all nullable
// fields nil maps to a Canonical whose nullable fields are nil, never
// coerced to "".
func TestSubmissionCanonical_NilStaysNil(t *testing.T) {
	inv := Invoice{}

	got := SubmissionCanonical(inv)

	if got.Currency != nil {
		t.Errorf("Currency = %v, want nil", got.Currency)
	}
	if got.Subtotal != nil {
		t.Errorf("Subtotal = %v, want nil", got.Subtotal)
	}
	if got.VAT != nil {
		t.Errorf("VAT = %v, want nil", got.VAT)
	}
	if got.Total != nil {
		t.Errorf("Total = %v, want nil", got.Total)
	}
	if got.IssueDate != nil {
		t.Errorf("IssueDate = %v, want nil", got.IssueDate)
	}
	if got.Supplier.TIN != nil {
		t.Errorf("Supplier.TIN = %v, want nil", got.Supplier.TIN)
	}
	if got.Supplier.Name != nil {
		t.Errorf("Supplier.Name = %v, want nil", got.Supplier.Name)
	}
	if got.Buyer.TIN != nil {
		t.Errorf("Buyer.TIN = %v, want nil", got.Buyer.TIN)
	}
	if got.Buyer.Name != nil {
		t.Errorf("Buyer.Name = %v, want nil", got.Buyer.Name)
	}
}

// TestSubmissionCanonical_PreservesLineOrderAndID (AC-2): a three-line
// invoice with LineNo 1,2,3 and distinct ids maps to Lines in the same
// order with matching LineID/LineNo.
func TestSubmissionCanonical_PreservesLineOrderAndID(t *testing.T) {
	inv := Invoice{
		LineItems: []LineItem{
			{ID: "a", LineNo: 1},
			{ID: "b", LineNo: 2},
			{ID: "c", LineNo: 3},
		},
	}

	got := SubmissionCanonical(inv)

	if len(got.Lines) != 3 {
		t.Fatalf("len(Lines) = %d, want 3", len(got.Lines))
	}
	wantIDs := []string{"a", "b", "c"}
	wantNos := []int{1, 2, 3}
	for i, line := range got.Lines {
		if line.LineID != wantIDs[i] {
			t.Errorf("Lines[%d].LineID = %q, want %q", i, line.LineID, wantIDs[i])
		}
		if line.LineNo != wantNos[i] {
			t.Errorf("Lines[%d].LineNo = %d, want %d", i, line.LineNo, wantNos[i])
		}
	}
}

// TestSubmissionCanonical_DoesNotMutateInput (AC-3): the mapper does not
// mutate its argument -- inv must compare deep-equal to a copy taken before
// the call.
func TestSubmissionCanonical_DoesNotMutateInput(t *testing.T) {
	inv := fullyPopulatedInvoice()
	cp := deepCopyInvoice(inv)

	_ = SubmissionCanonical(inv)

	if !reflect.DeepEqual(inv, cp) {
		t.Errorf("SubmissionCanonical mutated its argument:\n got: %+v\nwant: %+v", inv, cp)
	}
}

// TestSubmissionCanonical_NoLines (AC-5): an Invoice with LineItems nil
// maps to Lines of length 0 -- never a fabricated line.
func TestSubmissionCanonical_NoLines(t *testing.T) {
	inv := Invoice{LineItems: nil}

	got := SubmissionCanonical(inv)

	if len(got.Lines) != 0 {
		t.Errorf("len(Lines) = %d, want 0", len(got.Lines))
	}
}
