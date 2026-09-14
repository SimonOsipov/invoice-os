// CreateHandler's save branching, fake-driven: imp, open and save are all doubles.
// Also SavedMappingHandler's GET /v1/imports/saved-mapping specs (SM-GET/SM-MUX).
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// saveCall records one save invocation for the assertions below.
type saveCall struct {
	entityID string
	header   []string
	mapping  map[string]string
}

// saveSpy is a save double: records every call, answers a fixed error.
type saveSpy struct {
	calls []saveCall
	err   error
}

func (s *saveSpy) fn() saveFunc {
	return func(ctx context.Context, entityID string, header []string, mapping map[string]string) error {
		s.calls = append(s.calls, saveCall{
			entityID: entityID,
			header:   append([]string(nil), header...),
			mapping:  mapping,
		})
		return s.err
	}
}

// doImportSave is doImportUpload with an injected save double and logger.
func doImportSave(t *testing.T, imp importFunc, open openSpec, save saveFunc, log *slog.Logger, id *auth.Identity, query, contentType string, body io.Reader) (*httptest.ResponseRecorder, []byte, importBatchBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports"+query, body)
	r.Header.Set("Content-Type", contentType)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CreateHandler(imp, open, save, log).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	var resp importBatchBody
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec, raw, resp
}

// completedImp is an imp double that always reports a completed batch.
func completedImp() importFunc {
	return func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		return BatchResult{ID: "b1", Status: "completed", Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{}}, nil
	}
}

func TestCreateHandler_SavesMappingAfterCompletedImport(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	mapping := map[string]string{"invoice_number": "Invoice No", "total": "Total"}
	mappingJSON := mustMappingJSON(t, mapping)
	header := []string{"Invoice No", "Total"}

	var impHeader []string
	imp := func(ctx context.Context, gotEntityID, filename, documentID string, gotMapping map[string]string, gotHeader []string, rows [][]string, dryRun bool) (BatchResult, error) {
		impHeader = gotHeader
		return BatchResult{ID: "b1", Status: "completed", Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{}}, nil
	}
	body, ct, open := storedUpload(t, entityID, mappingJSON, "data.csv", "", csvBody(t, header, [][]string{{"INV-1", "119.00"}}))
	spy := &saveSpy{}

	rec, raw, _ := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("save calls = %d, want 1", len(spy.calls))
	}
	call := spy.calls[0]
	if call.entityID != entityID {
		t.Errorf("save entity_id = %q, want the form value %q", call.entityID, entityID)
	}
	if !reflect.DeepEqual(call.header, impHeader) {
		t.Errorf("save header = %v, want the header imp received %v", call.header, impHeader)
	}
	if !reflect.DeepEqual(call.header, header) {
		t.Errorf("save header = %v, want %v", call.header, header)
	}
	if !reflect.DeepEqual(call.mapping, mapping) {
		t.Errorf("save mapping = %v, want the posted mapping %v", call.mapping, mapping)
	}
}

// TestCreateHandler_DryRunDoesNotSave: the imp double answers "completed" on the
// dry run too, so only the dry-run clause can refuse the save.
func TestCreateHandler_DryRunDoesNotSave(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantStatus int
		wantCalls  int
	}{
		{"dry run", "?dry_run=true", http.StatusOK, 0},
		{"control: real import", "", http.StatusCreated, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
			open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
			body, ct := buildImportForm(t, uuid.NewString(), mappingJSON, open.doc.ID, importPart{field: "remember_mapping", content: []byte("true")})
			spy := &saveSpy{}

			rec, raw, _ := doImportSave(t, completedImp(), open.fn(), spy.fn(), nil, &id, tc.query, ct, body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, raw)
			}
			if len(spy.calls) != tc.wantCalls {
				t.Fatalf("save calls = %d, want %d", len(spy.calls), tc.wantCalls)
			}
		})
	}
}

func TestCreateHandler_RefusedRequestDoesNotSave(t *testing.T) {
	// The double answers "completed" alongside the error, so only the error check refuses the save.
	impCases := []struct {
		name       string
		err        error
		wantStatus int
		wantCalls  int
	}{
		{"imp returns ErrValidation", ErrValidation, http.StatusBadRequest, 0},
		{"control: imp returns nil", nil, http.StatusCreated, 1},
	}
	for _, tc := range impCases {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
			imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
				return BatchResult{Status: "completed"}, tc.err
			}
			body, ct, open := storedUpload(t, uuid.NewString(), mappingJSON, "data.csv", "", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
			spy := &saveSpy{}

			rec, raw, _ := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, raw)
			}
			if len(spy.calls) != tc.wantCalls {
				t.Fatalf("save calls = %d, want %d", len(spy.calls), tc.wantCalls)
			}
		})
	}

	t.Run("missing mapping", func(t *testing.T) {
		id := testIdentity()
		imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
			t.Fatal("imp must not run when mapping is missing")
			return BatchResult{}, nil
		}
		body, ct, open := storedUpload(t, uuid.NewString(), "", "data.csv", "", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
		spy := &saveSpy{}

		rec, raw, _ := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a missing mapping (body=%s)", rec.Code, raw)
		}
		if len(spy.calls) != 0 {
			t.Fatalf("save calls = %d, want 0", len(spy.calls))
		}
	})

	t.Run("unrecognized format", func(t *testing.T) {
		id := testIdentity()
		imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
			t.Fatal("imp must not run for an unrecognized format")
			return BatchResult{}, nil
		}
		mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
		open := newFakeDocOpen("scan.pdf", "application/pdf", []byte("%PDF-1.7\n"))
		body, ct := buildImportForm(t, uuid.NewString(), mappingJSON, open.doc.ID)
		spy := &saveSpy{}

		rec, raw, _ := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for an unrecognized format (body=%s)", rec.Code, raw)
		}
		if len(spy.calls) != 0 {
			t.Fatalf("save calls = %d, want 0", len(spy.calls))
		}
	})

	t.Run("undecodable file", func(t *testing.T) {
		id := testIdentity()
		imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
			t.Fatal("imp must not run for an undecodable file")
			return BatchResult{}, nil
		}
		mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
		content := bytes.Repeat([]byte{0x00, 0x01, 0x02}, 64)
		open := newFakeDocOpen("corrupt.csv", "text/csv", content)
		body, ct := buildImportForm(t, uuid.NewString(), mappingJSON, open.doc.ID)
		spy := &saveSpy{}

		rec, raw, _ := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for an undecodable file (body=%s)", rec.Code, raw)
		}
		if len(spy.calls) != 0 {
			t.Fatalf("save calls = %d, want 0", len(spy.calls))
		}
	})
}

func TestCreateHandler_FailedBatchDoesNotSave(t *testing.T) {
	cases := []struct {
		status    string
		wantCalls int
	}{
		{"failed", 0},
		{"completed", 1}, // control: the same request and spy record a save
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			id := testIdentity()
			mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
			imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
				return BatchResult{ID: "b1", Status: tc.status, Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{}}, nil
			}
			body, ct, open := storedUpload(t, uuid.NewString(), mappingJSON, "data.csv", "", csvBody(t, []string{"Invoice No"}, nil))
			spy := &saveSpy{}

			rec, raw, resp := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
			}
			if resp.Status != tc.status {
				t.Errorf("status = %q, want %q", resp.Status, tc.status)
			}
			if len(spy.calls) != tc.wantCalls {
				t.Fatalf("save calls = %d, want %d for a %s batch", len(spy.calls), tc.wantCalls, tc.status)
			}
		})
	}
}

func TestCreateHandler_SaveErrorLogsAndKeeps201Body(t *testing.T) {
	entityID := uuid.NewString()
	mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No", "total": "Total"})
	csv := csvBody(t, []string{"Invoice No", "Total"}, [][]string{{"INV-1", "119.00"}})

	id1 := testIdentity()
	body1, ct1, open1 := storedUpload(t, entityID, mappingJSON, "data.csv", "", csv)
	okSpy := &saveSpy{}
	recOK, rawOK, _ := doImportSave(t, completedImp(), open1.fn(), okSpy.fn(), nil, &id1, "", ct1, body1)
	if recOK.Code != http.StatusCreated {
		t.Fatalf("baseline status = %d, want 201 (body=%s)", recOK.Code, rawOK)
	}

	id2 := testIdentity()
	body2, ct2, open2 := storedUpload(t, entityID, mappingJSON, "data.csv", "", csv)
	failSpy := &saveSpy{err: errors.New("boom")}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	recFail, rawFail, _ := doImportSave(t, completedImp(), open2.fn(), failSpy.fn(), logger, &id2, "", ct2, body2)

	if recFail.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 even when save fails (body=%s)", recFail.Code, rawFail)
	}
	if !bytes.Equal(rawOK, rawFail) {
		t.Errorf("body with a failing save = %s, want the same body as a nil-error save %s", rawFail, rawOK)
	}
	logged := buf.String()
	if logged == "" {
		t.Fatal("expected one log record when save fails, got none")
	}
	if n := strings.Count(logged, `"importer: save mapping"`); n != 1 {
		t.Errorf("log holds %d %q record(s), want exactly 1: %s", n, "importer: save mapping", logged)
	}
}

func TestCreateHandler_RememberMappingFalseSkipsSave(t *testing.T) {
	entityID := uuid.NewString()
	mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})

	t.Run("false", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
		body, ct := buildImportForm(t, entityID, mappingJSON, open.doc.ID, importPart{field: "remember_mapping", content: []byte("false")})
		spy := &saveSpy{}

		rec, raw, _ := doImportSave(t, completedImp(), open.fn(), spy.fn(), nil, &id, "", ct, body)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
		}
		if len(spy.calls) != 0 {
			t.Fatalf("save calls = %d, want 0 when remember_mapping=false", len(spy.calls))
		}
	})

	t.Run("true", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
		body, ct := buildImportForm(t, entityID, mappingJSON, open.doc.ID, importPart{field: "remember_mapping", content: []byte("true")})
		spy := &saveSpy{}

		rec, raw, _ := doImportSave(t, completedImp(), open.fn(), spy.fn(), nil, &id, "", ct, body)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
		}
		if len(spy.calls) != 1 {
			t.Fatalf("save calls = %d, want 1 when remember_mapping=true", len(spy.calls))
		}
	})
}

func TestCreateHandler_MalformedRememberMapping400BeforeOpen(t *testing.T) {
	cases := []struct{ name, value string }{
		{"yes", "yes"},
		{"TRUE", "TRUE"}, // the parse is case-sensitive, like dry_run.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
			open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
			body, ct := buildImportForm(t, uuid.NewString(), mappingJSON, open.doc.ID, importPart{field: "remember_mapping", content: []byte(tc.value)})
			spy := &saveSpy{}

			rec, raw, resp := doImportSave(t, completedImp(), open.fn(), spy.fn(), nil, &id, "", ct, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
			}
			if resp.Error != "remember_mapping must be true or false" {
				t.Errorf("error = %q, want %q", resp.Error, "remember_mapping must be true or false")
			}
			if len(open.ids) != 0 {
				t.Errorf("open calls = %d, want 0 -- the 400 must land before open", len(open.ids))
			}
			if len(spy.calls) != 0 {
				t.Errorf("save calls = %d, want 0", len(spy.calls))
			}
		})
	}

	t.Run("control: true reaches open and saves", func(t *testing.T) {
		id := testIdentity()
		mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
		body, ct := buildImportForm(t, uuid.NewString(), mappingJSON, open.doc.ID, importPart{field: "remember_mapping", content: []byte("true")})
		spy := &saveSpy{}

		rec, raw, _ := doImportSave(t, completedImp(), open.fn(), spy.fn(), nil, &id, "", ct, body)

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
		}
		if len(open.ids) != 1 {
			t.Errorf("open calls = %d, want 1", len(open.ids))
		}
		if len(spy.calls) != 1 {
			t.Errorf("save calls = %d, want 1", len(spy.calls))
		}
	})
}

// --- SavedMappingHandler (SM-GET / SM-MUX) ----------------------------------

// lookupCall records one Store.SavedMapping-shaped call for the assertions below.
type lookupCall struct {
	entityID string
	header   []string
}

// lookupSpy is a lookup double: records every call, answers a fixed result/error.
type lookupSpy struct {
	calls  []lookupCall
	result *SavedMapping
	err    error
}

func (s *lookupSpy) fn() func(ctx context.Context, entityID string, header []string) (*SavedMapping, error) {
	return func(ctx context.Context, entityID string, header []string) (*SavedMapping, error) {
		s.calls = append(s.calls, lookupCall{entityID: entityID, header: append([]string(nil), header...)})
		return s.result, s.err
	}
}

// savedMappingErrorBody decodes the shared {"error":"..."} envelope.
type savedMappingErrorBody struct {
	Error string `json:"error"`
}

func doSavedMappingRequest(t *testing.T, open openSpec, lookup func(ctx context.Context, entityID string, header []string) (*SavedMapping, error), log *slog.Logger, id *auth.Identity, query string) (*httptest.ResponseRecorder, []byte, map[string]json.RawMessage) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/imports/saved-mapping"+query, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	SavedMappingHandler(open, lookup, log).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	var resp map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec, raw, resp
}

// SM-GET-01: no identity 401s before open or lookup ever runs.
func TestSavedMappingHandler_NoIdentityIs401BeforeOpenAndLookup(t *testing.T) {
	entityID := uuid.NewString()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
	lookup := &lookupSpy{}

	rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, nil, "?entity_id="+entityID+"&document_id="+open.doc.ID)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, raw)
	}
	var errBody savedMappingErrorBody
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if errBody.Error != "unauthorized" {
		t.Errorf("error = %q, want %q", errBody.Error, "unauthorized")
	}
	if len(open.ids) != 0 {
		t.Errorf("open calls = %d, want 0", len(open.ids))
	}
	if len(lookup.calls) != 0 {
		t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
	}
}

// SM-GET-02: entity_id absent or malformed 400s before open or lookup ever runs.
func TestSavedMappingHandler_EntityIDRequiredAndWellFormed(t *testing.T) {
	documentID := uuid.NewString()

	cases := []struct {
		name    string
		query   string
		wantErr string
	}{
		{"absent", "?document_id=" + documentID, "entity_id is required"},
		{"malformed", "?entity_id=nope&document_id=" + documentID, "entity_id must be a well-formed uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
			lookup := &lookupSpy{}

			rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, tc.query)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
			}
			var errBody savedMappingErrorBody
			if err := json.Unmarshal(raw, &errBody); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			if errBody.Error != tc.wantErr {
				t.Errorf("error = %q, want %q", errBody.Error, tc.wantErr)
			}
			if len(open.ids) != 0 {
				t.Errorf("open calls = %d, want 0", len(open.ids))
			}
			if len(lookup.calls) != 0 {
				t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
			}
		})
	}

	// Control: a well-formed entity_id is not itself refused.
	t.Run("control: well-formed entity_id reaches open", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
		lookup := &lookupSpy{}

		rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+uuid.NewString()+"&document_id="+open.doc.ID)

		if rec.Code == http.StatusBadRequest {
			t.Fatalf("status = 400 for a well-formed entity_id, want the request to reach open (body=%s)", raw)
		}
		if len(open.ids) != 1 {
			t.Errorf("open calls = %d, want 1", len(open.ids))
		}
	})
}

// SM-GET-03: document_id absent or malformed 400s before open or lookup ever runs.
func TestSavedMappingHandler_DocumentIDRequiredAndWellFormed(t *testing.T) {
	entityID := uuid.NewString()

	cases := []struct {
		name    string
		query   string
		wantErr string
	}{
		{"absent", "?entity_id=" + entityID, "document_id is required"},
		{"malformed", "?entity_id=" + entityID + "&document_id=nope", "document_id must be a well-formed uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
			lookup := &lookupSpy{}

			rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, tc.query)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
			}
			var errBody savedMappingErrorBody
			if err := json.Unmarshal(raw, &errBody); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			if errBody.Error != tc.wantErr {
				t.Errorf("error = %q, want %q", errBody.Error, tc.wantErr)
			}
			if len(open.ids) != 0 {
				t.Errorf("open calls = %d, want 0", len(open.ids))
			}
			if len(lookup.calls) != 0 {
				t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
			}
		})
	}

	// Control: a well-formed document_id is not itself refused.
	t.Run("control: well-formed document_id reaches open", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
		lookup := &lookupSpy{}

		rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+entityID+"&document_id="+open.doc.ID)

		if rec.Code == http.StatusBadRequest {
			t.Fatalf("status = 400 for a well-formed document_id, want the request to reach open (body=%s)", raw)
		}
		if len(open.ids) != 1 {
			t.Errorf("open calls = %d, want 1", len(open.ids))
		}
	})
}

// SM-GET-04: open's two named errors map exactly like CreateHandler's; lookup never runs.
func TestSavedMappingHandler_OpenRefusalsMapLikeCreate(t *testing.T) {
	entityID := uuid.NewString()
	documentID := uuid.NewString()

	t.Run("not found", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
		open.err = document.ErrNotFound
		lookup := &lookupSpy{}

		rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+entityID+"&document_id="+documentID)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, raw)
		}
		var errBody savedMappingErrorBody
		if err := json.Unmarshal(raw, &errBody); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if errBody.Error != "not found" {
			t.Errorf("error = %q, want %q", errBody.Error, "not found")
		}
		if len(lookup.calls) != 0 {
			t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
		}
	})

	t.Run("validation", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
		open.err = document.ErrValidation
		lookup := &lookupSpy{}

		rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+entityID+"&document_id="+documentID)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
		}
		var errBody savedMappingErrorBody
		if err := json.Unmarshal(raw, &errBody); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if errBody.Error != "document_id must be a well-formed uuid" {
			t.Errorf("error = %q, want %q", errBody.Error, "document_id must be a well-formed uuid")
		}
		if len(lookup.calls) != 0 {
			t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
		}
	})
}

// SM-GET-05: lookup receives the DECODED header, never a query-param substitute -- a
// quoted comma in one column name means a naive split would disagree with Decode.
func TestSavedMappingHandler_LookupReceivesTheDecodedHeader(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	header := []string{"Ref", "Total, NGN"}
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, header, [][]string{{"INV-1", "119.00"}}))
	lookup := &lookupSpy{}

	query := "?entity_id=" + entityID + "&document_id=" + open.doc.ID + "&columns=Evil&header=Evil"
	rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, query)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if len(lookup.calls) != 1 {
		t.Fatalf("lookup calls = %d, want 1", len(lookup.calls))
	}
	call := lookup.calls[0]
	if call.entityID != entityID {
		t.Errorf("lookup entity_id = %q, want the query value %q", call.entityID, entityID)
	}
	if !reflect.DeepEqual(call.header, header) {
		t.Errorf("lookup header = %v, want the decoded header %v -- the columns/header query params must never supply it", call.header, header)
	}
	if len(open.ids) != 1 || open.ids[0] != open.doc.ID {
		t.Fatalf("open ids = %v, want exactly [%s]", open.ids, open.doc.ID)
	}
	if len(open.ranges) != 1 || open.ranges[0] != "" {
		t.Errorf("open ranges = %v, want exactly [\"\"] -- the full document, no Range header", open.ranges)
	}
}

// SM-GET-06: a miss renders an explicit JSON null, never an absent key or a 404.
func TestSavedMappingHandler_MissIsExplicitNull(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
	lookup := &lookupSpy{} // zero value: nil result, nil error -- a miss

	rec, raw, resp := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+entityID+"&document_id="+open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if len(resp) != 1 {
		t.Fatalf("body has %d key(s), want exactly 1 (body=%s)", len(resp), raw)
	}
	sm, ok := resp["saved_mapping"]
	if !ok {
		t.Fatalf("body has no saved_mapping key (body=%s)", raw)
	}
	if string(sm) != "null" {
		t.Errorf("saved_mapping = %s, want the JSON literal null", sm)
	}
}

// SM-GET-07: a hit carries exactly mapping and saved_at, the latter as RFC 3339.
func TestSavedMappingHandler_HitCarriesMappingAndSavedAt(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
	loc := time.FixedZone("SAST", 2*60*60)
	savedAt := time.Date(2026, 3, 14, 9, 26, 53, 123456789, loc)
	mapping := map[string]string{"invoice_number": "Ref"}
	lookup := &lookupSpy{result: &SavedMapping{Mapping: mapping, SavedAt: savedAt}}

	rec, raw, resp := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+entityID+"&document_id="+open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	sm, ok := resp["saved_mapping"]
	if !ok || string(sm) == "null" {
		t.Fatalf("saved_mapping is missing or null, want a hit body (body=%s)", raw)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(sm, &fields); err != nil {
		t.Fatalf("decode saved_mapping %s: %v", sm, err)
	}
	if len(fields) != 2 {
		t.Fatalf("saved_mapping has %d key(s), want exactly 2 (mapping, saved_at): %s", len(fields), sm)
	}
	mRaw, ok := fields["mapping"]
	if !ok {
		t.Fatal("saved_mapping carries no mapping key")
	}
	var gotMapping map[string]string
	if err := json.Unmarshal(mRaw, &gotMapping); err != nil {
		t.Fatalf("decode mapping: %v", err)
	}
	if !reflect.DeepEqual(gotMapping, mapping) {
		t.Errorf("mapping = %v, want %v", gotMapping, mapping)
	}
	saRaw, ok := fields["saved_at"]
	if !ok {
		t.Fatal("saved_mapping carries no saved_at key")
	}
	var savedAtStr string
	if err := json.Unmarshal(saRaw, &savedAtStr); err != nil {
		t.Fatalf("decode saved_at: %v", err)
	}
	gotTime, err := time.Parse(time.RFC3339, savedAtStr)
	if err != nil {
		t.Fatalf("saved_at %q does not parse as RFC 3339: %v", savedAtStr, err)
	}
	if !gotTime.Equal(savedAt) {
		t.Errorf("saved_at = %s, want %s", gotTime, savedAt)
	}
}

// SM-GET-08: lookup's error maps through statusForErr -- 403 with no log record,
// or 500 with exactly one logged record.
func TestSavedMappingHandler_LookupErrorMapping(t *testing.T) {
	entityID := uuid.NewString()

	t.Run("suspended member", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		lookup := &lookupSpy{err: fmt.Errorf("wrap: %w", db.ErrNotActiveMember)}

		rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), logger, &id, "?entity_id="+entityID+"&document_id="+open.doc.ID)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, raw)
		}
		var errBody savedMappingErrorBody
		if err := json.Unmarshal(raw, &errBody); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if errBody.Error != db.NotActiveMemberMessage {
			t.Errorf("error = %q, want %q", errBody.Error, db.NotActiveMemberMessage)
		}
		if logged := buf.String(); logged != "" {
			t.Errorf("expected no log record for a 403, got: %s", logged)
		}
	})

	t.Run("other error", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		lookup := &lookupSpy{err: errors.New("boom")}

		rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), logger, &id, "?entity_id="+entityID+"&document_id="+open.doc.ID)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, raw)
		}
		logged := buf.String()
		if n := strings.Count(logged, `"importer: lookup saved mapping"`); n != 1 {
			t.Errorf("log holds %d record(s) of %q, want exactly 1: %s", n, "importer: lookup saved mapping", logged)
		}
	})
}

// SM-GET-09: an unrecognized document format 400s before lookup ever runs.
func TestSavedMappingHandler_UnrecognizedFormatIs400(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	open := newFakeDocOpen("scan.pdf", "application/pdf", []byte("%PDF-1.7\n"))
	lookup := &lookupSpy{}

	rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+entityID+"&document_id="+open.doc.ID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	var errBody savedMappingErrorBody
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if errBody.Error != "unrecognized file format" {
		t.Errorf("error = %q, want %q", errBody.Error, "unrecognized file format")
	}
	if len(lookup.calls) != 0 {
		t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
	}
}

// SM-GET-10: a suspended caller at open() is 403 through statusForErr, not the
// switch's default 500 arm; any other open error is a logged 500.
func TestSavedMappingHandler_SuspendedCallerAtOpenIs403(t *testing.T) {
	entityID := uuid.NewString()
	documentID := uuid.NewString()

	t.Run("suspended member", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
		open.err = fmt.Errorf("wrap: %w", db.ErrNotActiveMember)
		lookup := &lookupSpy{}

		rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+entityID+"&document_id="+documentID)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, raw)
		}
		var errBody savedMappingErrorBody
		if err := json.Unmarshal(raw, &errBody); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if errBody.Error != db.NotActiveMemberMessage {
			t.Errorf("error = %q, want %q", errBody.Error, db.NotActiveMemberMessage)
		}
		if len(lookup.calls) != 0 {
			t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
		}
	})

	t.Run("other error", func(t *testing.T) {
		id := testIdentity()
		open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
		open.err = errors.New("object storage unreachable: get")
		lookup := &lookupSpy{}
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))

		rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), logger, &id, "?entity_id="+entityID+"&document_id="+documentID)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, raw)
		}
		logged := buf.String()
		if n := strings.Count(logged, `"importer: open source document"`); n != 1 {
			t.Errorf("log holds %d record(s) of %q, want exactly 1: %s", n, "importer: open source document", logged)
		}
		if len(lookup.calls) != 0 {
			t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
		}
	})
}

// SM-GET-11: a stored file that fails Decode 400s before lookup ever runs.
func TestSavedMappingHandler_UndecodableFileIs400(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	open := newFakeDocOpen("data.xlsx", xlsxContentType, []byte("not a zip file"))
	lookup := &lookupSpy{}

	rec, raw, _ := doSavedMappingRequest(t, open.fn(), lookup.fn(), nil, &id, "?entity_id="+entityID+"&document_id="+open.doc.ID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	var errBody savedMappingErrorBody
	if err := json.Unmarshal(raw, &errBody); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if errBody.Error != "could not decode uploaded file" {
		t.Errorf("error = %q, want %q", errBody.Error, "could not decode uploaded file")
	}
	if len(lookup.calls) != 0 {
		t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
	}
}

// SM-GET-12: a non-conformant open() handing back a nil body 500s, guarded like
// Decode's own nil-dereference guard elsewhere in the package.
func TestSavedMappingHandler_NilObjectBodyIs500(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	docID := uuid.NewString()
	filename, contentType := "data.csv", "text/csv"
	open := func(ctx context.Context, _, rangeHeader string) (document.Document, document.Object, error) {
		return document.Document{ID: docID, Filename: &filename, DeclaredContentType: &contentType},
			document.Object{}, nil
	}
	lookup := &lookupSpy{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	rec, raw, _ := doSavedMappingRequest(t, open, lookup.fn(), logger, &id, "?entity_id="+entityID+"&document_id="+docID)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, raw)
	}
	logged := buf.String()
	if n := strings.Count(logged, `"importer: source document opened with no body"`); n != 1 {
		t.Errorf("log holds %d record(s) of %q, want exactly 1: %s", n, "importer: source document opened with no body", logged)
	}
	if len(lookup.calls) != 0 {
		t.Errorf("lookup calls = %d, want 0", len(lookup.calls))
	}
}

// SM-MUX-01: the literal /saved-mapping route must win over the {id} pattern
// registered beside it, exactly as main.go registers both.
func TestImportRoutes_SavedMappingIsNotSwallowedByBatchID(t *testing.T) {
	id := testIdentity()

	var getCalls int
	get := func(ctx context.Context, batchID string) (Batch, error) {
		getCalls++
		return Batch{ID: batchID, Errors: []RowError{}}, nil
	}
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Ref"}, nil))
	lookup := &lookupSpy{}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/imports/saved-mapping", SavedMappingHandler(open.fn(), lookup.fn(), nil))
	mux.HandleFunc("GET /v1/imports/{id}", GetHandler(get, nil))

	r := httptest.NewRequest("GET", "/v1/imports/saved-mapping", nil)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var errBody savedMappingErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.Bytes(), err)
	}
	if errBody.Error != "entity_id is required" {
		t.Errorf("error = %q, want %q -- the literal route must win over {id}, not fall through to GetHandler's uuid guard", errBody.Error, "entity_id is required")
	}
	if getCalls != 0 {
		t.Errorf("GetHandler's get ran %d time(s), want 0", getCalls)
	}

	// Control: the {id} pattern still serves a real batch id on the same mux.
	t.Run("control: GET /v1/imports/<uuid> still calls get", func(t *testing.T) {
		batchID := uuid.NewString()
		r2 := httptest.NewRequest("GET", "/v1/imports/"+batchID, nil)
		r2 = r2.WithContext(auth.WithIdentity(r2.Context(), id))
		rec2 := httptest.NewRecorder()
		mux.ServeHTTP(rec2, r2)

		if getCalls != 1 {
			t.Fatalf("get ran %d time(s), want 1", getCalls)
		}
		if rec2.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
		}
	})
}
