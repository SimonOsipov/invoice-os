// handlers_document_adversarial_test.go: QA-added adversarial coverage for POST
// /v1/imports/document (EXTR-06-06, task-766), extending handlers_document_test.go's H-01..H-09
// with three claims those specs did not independently exercise: identity-first ordering under a
// malformed/absent body, empty/null body handling, and a quarantine result still returning 201
// (no dry_run branch exists on this route).
//
// NOTE: a fourth candidate -- a decode error firing AFTER entity_id/document_id were already
// parsed valid, to independently prove the json.Decode error check matters -- turned out to be
// unconstructible. Verified empirically: Go's Decoder never partially commits struct fields on a
// syntax error (`{"entity_id":"E1","document_id":"D1","note":}` decodes to a FULLY zero-valued
// struct, not one with entity_id/document_id already set), and a type-mismatch on one field always
// leaves the OTHER field's presence guard to catch it. Given this handler's two-required-string-
// field shape, the decode-error check at handlers_document.go's json.Decode call is structurally
// subsumed by the entity_id/document_id blank guards for every reachable malformed input -- not a
// defect, just an unfalsifiable-by-black-box-input branch.
package importer

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// --- Identity-first ordering (AC #1: "before the body is read") -------------------------------

// TestCreateDocumentHandler_NoIdentityWithMalformedBodyStill401 proves the 401 fires before the
// body is ever decoded: H-01 only exercises this with a WELL-FORMED body, which cannot tell
// identity-first apart from decode-first (either order yields 401 there).
func TestCreateDocumentHandler_NoIdentityWithMalformedBodyStill401(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformedJSON", `{"entity_id":`},
		{"emptyBody", ""},
		{"nullBody", "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &docImpSpy{}
			rec, raw, _ := doImportDocumentPost(t, spy.fn(), nil, tc.body)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 -- a stranger must not learn the body was malformed; raw body %s", rec.Code, raw)
			}
			if len(spy.calls) != 0 {
				t.Errorf("imp called %d time(s), want 0", len(spy.calls))
			}
		})
	}
}

// --- Empty / null body, authenticated -----------------------------------------------------------

// TestCreateDocumentHandler_EmptyOrNullBodyIs400ImpNeverCalled covers two DIFFERENT guard paths:
// an empty body fails json.Decode itself (io.EOF); a literal "null" body decodes successfully
// into a zero-valued struct (encoding/json's documented no-op for null into a non-pointer field)
// and is caught by the entity_id/document_id presence guards instead.
func TestCreateDocumentHandler_EmptyOrNullBodyIs400ImpNeverCalled(t *testing.T) {
	id := testIdentity()
	cases := []struct {
		name string
		body string
	}{
		{"emptyBody", ""},
		{"nullBody", "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &docImpSpy{}
			rec, raw, _ := doImportDocumentPost(t, spy.fn(), &id, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 -- raw body %s", rec.Code, raw)
			}
			if len(spy.calls) != 0 {
				t.Errorf("imp called %d time(s), want 0", len(spy.calls))
			}
		})
	}
}

// --- Quarantine is still 201: no dry_run branch exists on this route (D-4) ----------------------

// TestCreateDocumentHandler_QuarantineResultStillReturns201 proves a quarantine outcome (imp
// returns err == nil with ReadyInvoices 0, QuarantinedInvoices > 0) is not mistaken for a
// failure: none of H-01..H-09 exercise the handler with a quarantine-shaped BatchResult.
func TestCreateDocumentHandler_QuarantineResultStillReturns201(t *testing.T) {
	id := testIdentity()
	spy := &docImpSpy{res: BatchResult{
		ID: "doc-batch-q", Status: "completed",
		RowsTotal: 1, RowsValid: 0, RowsInvalid: 1,
		ReadyInvoices: 0, QuarantinedInvoices: 1,
		Errors:             []RowError{{Row: 1, Field: "invoice_number", Message: "blank invoice number: row cannot be grouped"}},
		InvoiceViolations:  []InvoiceViolations{},
	}}
	rec, raw, resp := doImportDocumentPost(t, spy.fn(), &id, docJSONBody(uuid.NewString(), uuid.NewString()))
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 -- a quarantine is still a completed run, not an error; raw body %s", rec.Code, raw)
	}
	if resp.QuarantinedInvoices != 1 || resp.ReadyInvoices != 0 {
		t.Errorf("quarantined_invoices=%d ready_invoices=%d, want 1 and 0", resp.QuarantinedInvoices, resp.ReadyInvoices)
	}
	if !strings.Contains(string(raw), "blank invoice number") {
		t.Errorf("quarantine error message missing from body: %s", raw)
	}
}
