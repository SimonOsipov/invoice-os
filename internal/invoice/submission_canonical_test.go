package invoice

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
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

// canonicalTarget finds the Canonical field an Invoice or LineItem field name maps to:
// Supplier*/Buyer* land on the party, every other name matches one to one.
func canonicalTarget(c reflect.Value, name string) reflect.Value {
	for _, p := range []string{"Supplier", "Buyer"} {
		if rest, ok := strings.CutPrefix(name, p); ok && rest != "" {
			if f := c.FieldByName(p).FieldByName(rest); f.IsValid() {
				return f
			}
		}
	}
	return c.FieldByName(name)
}

func TestSubmissionCanonical_MapsEveryNRSField(t *testing.T) {
	headerIdx, headerTags := contentFields(reflect.TypeOf(Invoice{}))
	for n, i := range headerIdx {
		tag := headerTags[n]
		t.Run(tag, func(t *testing.T) {
			var inv Invoice
			setSample(t, reflect.ValueOf(&inv).Elem().Field(i), tag)
			name := reflect.TypeOf(inv).Field(i).Name
			got := canonicalTarget(reflect.ValueOf(SubmissionCanonical(inv)), name)
			if !got.IsValid() {
				t.Fatalf("Canonical has no field for Invoice.%s", name)
			}
			if want := reflect.ValueOf(inv).Field(i).Interface(); !reflect.DeepEqual(got.Interface(), want) {
				t.Errorf("Canonical for Invoice.%s = %v, want %v", name, got.Interface(), want)
			}
		})
	}
	if len(headerIdx) < 28 {
		t.Fatalf("walked %d header fields, want at least 28", len(headerIdx))
	}

	lineIdx, lineTags := contentFields(reflect.TypeOf(LineItem{}))
	for n, i := range lineIdx {
		tag := lineTags[n]
		t.Run("line_"+tag, func(t *testing.T) {
			var li LineItem
			setSample(t, reflect.ValueOf(&li).Elem().Field(i), tag)
			name := reflect.TypeOf(li).Field(i).Name
			got := reflect.ValueOf(SubmissionCanonical(Invoice{LineItems: []LineItem{li}}).Lines[0]).FieldByName(name)
			if !got.IsValid() {
				t.Fatalf("CanonicalLine has no field for LineItem.%s", name)
			}
			if want := reflect.ValueOf(li).Field(i).Interface(); !reflect.DeepEqual(got.Interface(), want) {
				t.Errorf("CanonicalLine.%s = %v, want %v", name, got.Interface(), want)
			}
		})
	}
	if len(lineIdx) < 15 {
		t.Fatalf("walked %d line fields, want at least 15", len(lineIdx))
	}
}

// assertNoZeroField fails for every zero field reachable from v, descending into
// structs and the first element of each slice.
func assertNoZeroField(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			assertNoZeroField(t, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	case reflect.Slice:
		if v.Len() == 0 {
			t.Errorf("%s is empty", path)
			return
		}
		assertNoZeroField(t, v.Index(0), path+"[0]")
	case reflect.Ptr:
		if v.IsNil() {
			t.Errorf("%s is nil", path)
		}
	default:
		if v.IsZero() {
			t.Errorf("%s is zero", path)
		}
	}
}

func TestSubmissionCanonical_NoCanonicalFieldIsLeftZero(t *testing.T) {
	inv := fullyPopulatedInvoice()
	idx, tags := contentFields(reflect.TypeOf(Invoice{}))
	for n, i := range idx {
		setSample(t, reflect.ValueOf(&inv).Elem().Field(i), tags[n])
	}
	lIdx, lTags := contentFields(reflect.TypeOf(LineItem{}))
	li := &inv.LineItems[0]
	for n, i := range lIdx {
		setSample(t, reflect.ValueOf(li).Elem().Field(i), lTags[n])
	}
	li.LineNo = 1
	li.LineTotal, li.LineTax, li.TaxPercent = strPtr("10.00"), strPtr("0.75"), strPtr("7.50")

	assertNoZeroField(t, reflect.ValueOf(SubmissionCanonical(inv)), "Canonical")
}

func TestSubmissionCanonical_TaxSubtotalsFromLines(t *testing.T) {
	lines := []LineItem{
		{LineNo: 1, TaxCategory: strPtr("STANDARD_VAT"), TaxPercent: strPtr("7.50"), LineTotal: strPtr("100.00"), LineTax: strPtr("7.50")},
		{LineNo: 2, TaxCategory: strPtr("ZERO_RATED"), TaxPercent: strPtr("0"), LineTotal: strPtr("50.00"), LineTax: strPtr("0.00")},
		{LineNo: 3, TaxCategory: strPtr("STANDARD_VAT"), TaxPercent: strPtr("7.50"), LineTotal: strPtr("20.00"), LineTax: strPtr("1.50")},
	}
	got := SubmissionCanonical(Invoice{LineItems: lines}).TaxSubtotals
	want := taxSubtotals(lines)
	if len(want) != 2 || len(got) != len(want) {
		t.Fatalf("got %d entries, want %d (2 expected)", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Category != w.TaxCategory || !reflect.DeepEqual(g.Percent, w.TaxPercent) ||
			!reflect.DeepEqual(g.TaxableAmount, w.TaxableAmount) || !reflect.DeepEqual(g.TaxAmount, w.TaxAmount) {
			t.Errorf("TaxSubtotals[%d] = %+v, want %+v", i, g, w)
		}
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

	// NRS fields: the only non-nil values allowed are the line-less LineID/LineNo/InvoiceID zeros.
	nilLine := SubmissionCanonical(Invoice{LineItems: []LineItem{{}}})
	for _, v := range []reflect.Value{reflect.ValueOf(got), reflect.ValueOf(nilLine.Lines[0])} {
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.Kind() == reflect.Ptr && !f.IsNil() {
				t.Errorf("%s = %v, want nil", v.Type().Field(i).Name, f.Elem())
			}
		}
	}
	for _, p := range []submission.Party{got.Supplier, got.Buyer} {
		v := reflect.ValueOf(p)
		for i := 0; i < v.NumField(); i++ {
			if !v.Field(i).IsNil() {
				t.Errorf("Party.%s = %v, want nil", v.Type().Field(i).Name, v.Field(i).Elem())
			}
		}
	}
	if got.TaxSubtotals != nil {
		t.Errorf("TaxSubtotals = %v, want nil", got.TaxSubtotals)
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
