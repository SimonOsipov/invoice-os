package ubl_test

import (
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func noteCanonical(t *testing.T, ref *submission.BillingReference) submission.Canonical {
	t.Helper()
	c := completeCanonical(t)
	c.DocumentType, c.BillingReference = submission.CreditNote, ref
	return c
}

func TestRender_NoteWritesBillingReference(t *testing.T) {
	d := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	nodes := walkDocument(t, mustRender(t, noteCanonical(t, &submission.BillingReference{IRN: ublStr("IRN-ORIG-1"), IssueDate: &d})))
	base := "Invoice/cac:BillingReference/cac:InvoiceDocumentReference/"
	wantTextAt(t, nodes, base+"cbc:ID", "IRN-ORIG-1", "original IRN")
	wantTextAt(t, nodes, base+"cbc:IssueDate", "2026-09-10", "original issue date")
	wantChildOrder(t, nodes, "Invoice", []string{
		"cbc:CustomizationID", "cbc:ProfileID", "cbc:ID", "cbc:IssueDate", "cbc:InvoiceTypeCode",
		"cbc:DocumentCurrencyCode", "cac:BillingReference", "cac:AccountingSupplierParty", "cac:AccountingCustomerParty",
		"cac:TaxTotal", "cac:LegalMonetaryTotal", "cac:InvoiceLine", "cac:InvoiceLine",
	})
}

func TestRender_BillingReferenceOmittedWithoutAnIRN(t *testing.T) {
	d := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for name, ref := range map[string]*submission.BillingReference{
		"nil IRN":   {IssueDate: &d},
		"blank IRN": {IRN: ublStr(" "), IssueDate: &d},
		"empty":     {},
	} {
		out := mustRender(t, noteCanonical(t, ref))
		wantAbsent(t, out, "cac:BillingReference", name)
		wantAbsent(t, out, "cac:InvoiceDocumentReference", name)
	}
}

func TestRender_CommercialInvoiceWritesNoBillingReference(t *testing.T) {
	wantAbsent(t, mustRender(t, completeCanonical(t)), "cac:BillingReference", "commercial invoice")
}
