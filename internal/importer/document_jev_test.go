package importer

import "testing"

// A Jev doubt keeps its value at rank 0; only reasons never reach the draft.
func TestDocumentCreateInput_ADoubtedValueReachesTheDraft(t *testing.T) {
	ex := SettledExtraction{JobID: "job-jev-1", Fields: []extractedField{
		{Name: "invoice_number", Value: mpPtr("JD-3310"), Reason: mpPtr("unreadable")},
		{Name: "vat", Value: mpPtr("135.00"), Reason: mpPtr("unreadable")},
		{Name: "total", Value: mpPtr("1935.00")},
	}}

	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.InvoiceNumber != "JD-3310" {
		t.Errorf("InvoiceNumber = %q, want %q", got.InvoiceNumber, "JD-3310")
	}
	if got.VAT == nil || *got.VAT != "135.00" {
		t.Errorf("VAT = %v, want %q", got.VAT, "135.00")
	}
	if got.Total == nil || *got.Total != "1935.00" {
		t.Errorf("Total = %v, want %q", got.Total, "1935.00")
	}
}

// An AI value withheld by its own check is rank 0 with value NULL; a Jev doubt is rank 0 with its value.
func TestDocumentCreateInput_AnAIWithheldValueNeverReachesTheDraftButADoubtedOneDoes(t *testing.T) {
	ex := SettledExtraction{JobID: "job-jev-2", Fields: []extractedField{
		{Name: "invoice_number", Value: mpPtr("JD-3310"), Reason: mpPtr("unreadable")},
		{Name: "vat", Value: nil, Reason: mpPtr("unreadable")},
		{Name: "total", Value: mpPtr("1935.00")},
	}}

	got, rowErr := documentCreateInput("entity-1", "doc-1", ex)
	if rowErr != nil {
		t.Fatalf("rowErr = %+v, want nil", rowErr)
	}
	if got.VAT != nil {
		t.Errorf("VAT = %q, want nil: a withheld value must not reach the draft", *got.VAT)
	}
	if got.InvoiceNumber != "JD-3310" {
		t.Errorf("InvoiceNumber = %q, want %q: the doubted number still files", got.InvoiceNumber, "JD-3310")
	}
	if got.Total == nil || *got.Total != "1935.00" {
		t.Errorf("Total = %v, want %q", got.Total, "1935.00")
	}
}
