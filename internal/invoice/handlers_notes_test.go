package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

const validNoteBody = `{"document_type":"credit_note","invoice_number":"CN-1","issue_date":"2026-10-30T00:00:00Z",` +
	`"lines":[{"line_id":"L1","quantity":"2.000","unit_price":"10.00"},{"line_id":"L2","quantity":"1","unit_price":"5.50"}]}`

func doIssueNote(issue func(context.Context, string, NoteInput) (Invoice, error), withID bool, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/invoices/"+id+"/notes", strings.NewReader(body))
	r.SetPathValue("id", id)
	if withID {
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: "u", Role: "authenticated", TenantID: uuid.NewString()}))
	}
	rec := httptest.NewRecorder()
	IssueNoteHandler(issue, nil).ServeHTTP(rec, r)
	return rec
}

func TestIssueNoteHandler_201PassesPathIdAndBody(t *testing.T) {
	pathID := uuid.NewString()
	var gotID string
	var got NoteInput
	issue := func(_ context.Context, id string, in NoteInput) (Invoice, error) {
		gotID, got = id, in
		return Invoice{ID: "new", Status: StatusDraft}, nil
	}
	rec := doIssueNote(issue, true, pathID, validNoteBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"new"`) {
		t.Errorf("body = %s, want the Invoice JSON", rec.Body.String())
	}
	if gotID != pathID {
		t.Errorf("store id = %q, want %q", gotID, pathID)
	}
	if got.DocumentType != submission.CreditNote || got.InvoiceNumber != "CN-1" || got.IssueDate == nil || got.IssueDate.Format("2006-01-02") != "2026-10-30" {
		t.Errorf("store input = %+v", got)
	}
	want := []NoteLineInput{{"L1", "2.000", "10.00"}, {"L2", "1", "5.50"}}
	if !reflect.DeepEqual(got.Lines, want) {
		t.Errorf("lines = %+v, want %+v", got.Lines, want)
	}

	rec = doIssueNote(issue, true, pathID, strings.Replace(validNoteBody, "credit_note", "debit_note", 1))
	if rec.Code != http.StatusCreated || got.DocumentType != submission.DebitNote {
		t.Errorf("debit: status = %d, type = %q, want 201 and debit_note", rec.Code, got.DocumentType)
	}
}

func TestIssueNoteHandler_NoIdentityIs401(t *testing.T) {
	called := false
	issue := func(context.Context, string, NoteInput) (Invoice, error) { called = true; return Invoice{}, nil }
	rec := doIssueNote(issue, false, uuid.NewString(), validNoteBody)
	if rec.Code != http.StatusUnauthorized || called {
		t.Errorf("status = %d, called = %v, want 401 and not called", rec.Code, called)
	}
}

func TestIssueNoteHandler_MalformedBodyIs400(t *testing.T) {
	for name, body := range map[string]string{
		"bad json":   `{"document_type":`,
		"bare date":  `{"document_type":"credit_note","invoice_number":"CN-1","issue_date":"2026-10-30","lines":[]}`,
		"empty body": ``,
	} {
		called := false
		issue := func(context.Context, string, NoteInput) (Invoice, error) { called = true; return Invoice{}, nil }
		rec := doIssueNote(issue, true, uuid.NewString(), body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid request body") || called {
			t.Errorf("%s: status = %d body = %s called = %v, want 400 invalid request body, not called", name, rec.Code, rec.Body.String(), called)
		}
	}
}

func TestIssueNoteHandler_StoreErrorsMapToStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		msg    string
	}{
		{ErrOriginalNotFound, 404, "not found"},
		{ErrOriginalIsANote, 409, "a note cannot be the original of a note"},
		{ErrOriginalOtherEntity, 409, "the original belongs to another client"},
		{ErrOriginalNotIssued, 409, "a note needs an accepted invoice with an IRN"},
		{fmt.Errorf("%w: a line is repeated", ErrValidation), 400, "invoice: validation: a line is repeated"},
		{ErrUnknownLineID, 400, "line_items id must name a line of this invoice"},
		{ErrNotFound, 404, "not found"},
		{ErrDuplicateNumber, 409, "duplicate invoice number"},
		{db.ErrWorkspaceLocked, 403, db.WorkspaceLockedMessage},
		{db.ErrNotActiveMember, 403, db.NotActiveMemberMessage},
		{db.ErrNoTenant, 401, "unauthorized"},
	} {
		issue := func(context.Context, string, NoteInput) (Invoice, error) { return Invoice{}, tc.err }
		rec := doIssueNote(issue, true, uuid.NewString(), validNoteBody)
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: decode %q: %v", tc.err, rec.Body.String(), err)
		}
		if rec.Code != tc.status || body["error"] != tc.msg || len(body) != 1 {
			t.Errorf("%v: status = %d body = %v, want %d {error: %q}", tc.err, rec.Code, body, tc.status, tc.msg)
		}
	}
	// An unmapped error is a 500 that leaks nothing.
	issue := func(context.Context, string, NoteInput) (Invoice, error) { return Invoice{}, errors.New("boom") }
	if rec := doIssueNote(issue, true, uuid.NewString(), validNoteBody); rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("unmapped: %d %s", rec.Code, rec.Body.String())
	}
}

func TestIssueNoteGate_Table(t *testing.T) {
	irn := "IRN-1"
	for _, tc := range []struct {
		name       string
		inv        Invoice
		want       bool
		wantReason string
	}{
		{"commercial accepted irn", Invoice{DocumentType: submission.CommercialInvoice, Status: StatusAccepted, IRN: &irn}, true, ""},
		{"empty type accepted irn", Invoice{Status: StatusAccepted, IRN: &irn}, true, ""},
		{"accepted null irn", Invoice{Status: StatusAccepted}, false, "Only an accepted invoice with an IRN can carry a note."},
		{"credit note accepted irn", Invoice{DocumentType: submission.CreditNote, Status: StatusAccepted, IRN: &irn}, false, "A note cannot have its own note. Open the original invoice to issue another."},
		{"debit note accepted irn", Invoice{DocumentType: submission.DebitNote, Status: StatusAccepted, IRN: &irn}, false, "A note cannot have its own note. Open the original invoice to issue another."},
		{"draft credit note, no irn", Invoice{DocumentType: submission.CreditNote, Status: StatusDraft}, false, "A note cannot have its own note. Open the original invoice to issue another."},
	} {
		ok, reason := issueNoteGate(tc.inv)
		if ok != tc.want || (tc.wantReason == "") != (reason == nil) || (reason != nil && *reason != tc.wantReason) {
			t.Errorf("%s: gate = %v, %v; want %v, %q", tc.name, ok, reason, tc.want, tc.wantReason)
		}
	}
	for _, st := range []Status{StatusDraft, StatusValidated, StatusRejected, StatusQueued, StatusSubmitted, StatusFailed} {
		ok, reason := issueNoteGate(Invoice{Status: st, IRN: &irn})
		if ok || reason == nil || *reason != "Only an accepted invoice with an IRN can carry a note." {
			t.Errorf("%s: gate = %v, %v; want false, the not-issued sentence", st, ok, reason)
		}
	}
}

func doNotes(notes func(context.Context, string) (NotesOnInvoice, error), withID bool, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/v1/invoices/"+id+"/notes", nil)
	r.SetPathValue("id", id)
	if withID {
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: "u", Role: "authenticated", TenantID: uuid.NewString()}))
	}
	rec := httptest.NewRecorder()
	NotesHandler(notes, nil).ServeHTTP(rec, r)
	return rec
}

func TestNotes_NoNotesIsAnEmptyArray(t *testing.T) {
	pathID := uuid.NewString()
	total := "500.00"
	var gotID string
	// A nil slice from the store must still reach the wire as [].
	notes := func(_ context.Context, id string) (NotesOnInvoice, error) {
		gotID = id
		return NotesOnInvoice{CreditedSoFar: "0.00", StillCreditable: &total}, nil
	}
	rec := doNotes(notes, true, pathID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotID != pathID {
		t.Errorf("store id = %q, want %q", gotID, pathID)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if len(body) != 3 || string(body["notes"]) != "[]" || string(body["credited_so_far"]) != `"0.00"` || string(body["still_creditable"]) != `"500.00"` {
		t.Errorf("body = %s, want keys notes [], credited_so_far \"0.00\", still_creditable \"500.00\"", rec.Body.String())
	}
}

func TestNotes_ListWireKeysAndNullStillCreditable(t *testing.T) {
	notes := func(context.Context, string) (NotesOnInvoice, error) {
		t100 := "100.00"
		return NotesOnInvoice{
			Notes:         []NoteSummary{{ID: "n1", DocumentType: submission.DebitNote, InvoiceNumber: "DN-1", Status: StatusDraft, Total: &t100}, {ID: "n2", DocumentType: submission.CreditNote, InvoiceNumber: "CN-1", Status: StatusAccepted}},
			CreditedSoFar: "7.00",
		}, nil
	}
	rec := doNotes(notes, true, uuid.NewString())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Notes           []map[string]json.RawMessage `json:"notes"`
		StillCreditable json.RawMessage              `json:"still_creditable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(body.StillCreditable) != "null" {
		t.Errorf("still_creditable = %s, want literal null, present", body.StillCreditable)
	}
	if len(body.Notes) != 2 {
		t.Fatalf("notes = %d rows, want 2", len(body.Notes))
	}
	first := body.Notes[0]
	if len(first) != 5 || string(first["id"]) != `"n1"` || string(first["document_type"]) != `"debit_note"` || string(first["invoice_number"]) != `"DN-1"` || string(first["status"]) != `"draft"` || string(first["total"]) != `"100.00"` {
		t.Errorf("first note = %v, want id, document_type, invoice_number, status, total", first)
	}
	if string(body.Notes[1]["total"]) != "null" {
		t.Errorf("a NULL stored total = %s, want literal null", body.Notes[1]["total"])
	}
}

func TestNotesHandler_MapsErrors(t *testing.T) {
	ok := func(context.Context, string) (NotesOnInvoice, error) {
		return NotesOnInvoice{CreditedSoFar: "0.00"}, nil
	}
	if rec := doNotes(ok, true, uuid.NewString()); rec.Code != http.StatusOK {
		t.Fatalf("control: success answered %d %s, want 200", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		err    error
		status int
		msg    string
	}{
		{ErrNotFound, 404, "not found"},
		{fmt.Errorf("%w: bad id", ErrValidation), 400, "invoice: validation: bad id"},
		{db.ErrWorkspaceLocked, 403, db.WorkspaceLockedMessage},
	} {
		rec := doNotes(func(context.Context, string) (NotesOnInvoice, error) { return NotesOnInvoice{}, tc.err }, true, uuid.NewString())
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: decode %q: %v", tc.err, rec.Body.String(), err)
		}
		if rec.Code != tc.status || body["error"] != tc.msg || len(body) != 1 {
			t.Errorf("%v: status = %d body = %v, want %d {error: %q}", tc.err, rec.Code, body, tc.status, tc.msg)
		}
	}

	called := false
	spy := func(context.Context, string) (NotesOnInvoice, error) { called = true; return NotesOnInvoice{}, nil }
	if rec := doNotes(spy, false, uuid.NewString()); rec.Code != http.StatusUnauthorized || called {
		t.Errorf("no identity: status = %d, called = %v, want 401 and not called", rec.Code, called)
	}

	boom := func(context.Context, string) (NotesOnInvoice, error) { return NotesOnInvoice{}, errors.New("boom") }
	if rec := doNotes(boom, true, uuid.NewString()); rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("unmapped: %d %s, want 500 without the cause", rec.Code, rec.Body.String())
	}
}
