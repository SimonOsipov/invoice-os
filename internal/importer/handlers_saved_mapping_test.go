// CreateHandler's save branching (SM-SAVE-01..07), fake-driven -- imp/open/save
// are all doubles, mirroring handlers_upload_once_test.go's idiom.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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

// doImportSave is doImportUpload's (handlers_upload_once_test.go) twin that
// injects a save double instead of hardcoding noSave -- doImportUpload's own
// callers do not care what save received, these specs do.
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

// --- SM-SAVE-01 --------------------------------------------------------

// TestCreateHandler_SavesMappingAfterCompletedImport (SM-SAVE-01, AC #3).
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

// --- SM-SAVE-02 (green by construction against the Stage 2.5 stub) -----

// TestCreateHandler_DryRunDoesNotSave (SM-SAVE-02, AC #3): the imp double
// still answers "completed" on the dry-run call, matching a real dry run's
// shape closely enough to red a save gated on Status alone but not on dryRun.
func TestCreateHandler_DryRunDoesNotSave(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
	open := newFakeDocOpen("data.csv", "text/csv", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
	body, ct := buildImportForm(t, entityID, mappingJSON, open.doc.ID, importPart{field: "remember_mapping", content: []byte("true")})
	spy := &saveSpy{}

	rec, raw, _ := doImportSave(t, completedImp(), open.fn(), spy.fn(), nil, &id, "?dry_run=true", ct, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a dry run (body=%s)", rec.Code, raw)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("save calls = %d, want 0 for a dry run", len(spy.calls))
	}
}

// --- SM-SAVE-03 (green by construction against the Stage 2.5 stub) -----

// TestCreateHandler_RefusedRequestDoesNotSave (SM-SAVE-03, AC #3).
func TestCreateHandler_RefusedRequestDoesNotSave(t *testing.T) {
	t.Run("imp returns ErrValidation", func(t *testing.T) {
		id := testIdentity()
		entityID := uuid.NewString()
		mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
		imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
			return BatchResult{Status: "completed"}, ErrValidation
		}
		body, ct, open := storedUpload(t, entityID, mappingJSON, "data.csv", "", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
		spy := &saveSpy{}

		rec, raw, _ := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
		}
		if len(spy.calls) != 0 {
			t.Fatalf("save calls = %d, want 0 when imp fails", len(spy.calls))
		}
	})

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

// --- SM-SAVE-04 (green by construction against the Stage 2.5 stub) -----

// TestCreateHandler_FailedBatchDoesNotSave (SM-SAVE-04, AC #3, [completed-means-status-completed]).
func TestCreateHandler_FailedBatchDoesNotSave(t *testing.T) {
	id := testIdentity()
	entityID := uuid.NewString()
	mappingJSON := mustMappingJSON(t, map[string]string{"invoice_number": "Invoice No"})
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		return BatchResult{ID: "b1", Status: "failed", Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{}}, nil
	}
	body, ct, open := storedUpload(t, entityID, mappingJSON, "data.csv", "", csvBody(t, []string{"Invoice No"}, nil))
	spy := &saveSpy{}

	rec, raw, resp := doImportSave(t, imp, open.fn(), spy.fn(), nil, &id, "", ct, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
	}
	if resp.Status != "failed" {
		t.Errorf("status = %q, want %q", resp.Status, "failed")
	}
	if len(spy.calls) != 0 {
		t.Fatalf("save calls = %d, want 0 for a failed batch", len(spy.calls))
	}
}

// --- SM-SAVE-05 ----------------------------------------------------------

// TestCreateHandler_SaveErrorLogsAndKeeps201Body (SM-SAVE-05, AC #5).
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

// --- SM-SAVE-06 ------------------------------------------------------------

// TestCreateHandler_RememberMappingFalseSkipsSave (SM-SAVE-06, AC #3, [untouched-restore-does-not-save]).
// The "false" subtest is green by construction against the Stage 2.5 stub.
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

// --- SM-SAVE-07 ------------------------------------------------------------

// TestCreateHandler_MalformedRememberMapping400BeforeOpen (SM-SAVE-07, AC #4).
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
}
