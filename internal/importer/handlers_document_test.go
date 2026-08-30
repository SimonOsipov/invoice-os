// handlers_document_test.go: Mode A RED specs for EXTR-06-06 (task-766), POST
// /v1/imports/document over the not-implemented stub in handlers_document.go (always 501, imp
// never called) -- every assertion below fails on STATUS/BODY/imp-call-count, never on a
// compile error.
//
// Spec-to-test map (Test Specs table, EXTR-06-06 / task-766, H-02 extended per the architect's
// correction #5 -- a 5th malformed-entity_id row):
//
//	H-01 TestCreateDocumentHandler_NoIdentityReturns401ImpNeverCalled
//	H-02 TestCreateDocumentHandler_MalformedRequestIs400ImpNeverCalled (5 cases)
//	H-03 TestCreateDocumentHandler_ErrorMapping
//	H-04 TestCreateDocumentHandler_500BodyIsBareEnvelopeNeverEchoesRawError
//	H-05 TestCreateDocumentHandler_SuccessKeyOrderMatchesSpreadsheetImportResponse
//	H-06 TestCreateDocumentHandler_FormatIsDocumentDelimiterEncodingAreNull
//	H-07 TestCreateDocumentHandler_CorsAllowMethodsAlreadyContainsPOST (green from the start)
//	H-08 TestImportRoutes_DocumentAndSpreadsheetDoNotCollide
//	H-09 TestCreateDocumentHandler_EndToEndOverRealServiceWritesReadableInvoice
//
// Run:
//
//	.ralph/dbtest.sh ./internal/importer/... -run 'CreateDocumentHandler|ImportRoutes'
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// importDocumentBody mirrors importResponse's exact wire shape (H-05's byte-identical
// requirement) plus the shared error envelope.
type importDocumentBody struct {
	ID                  string     `json:"id"`
	Status              string     `json:"status"`
	Format              string     `json:"format"`
	Delimiter           *string    `json:"delimiter"`
	Encoding            *string    `json:"encoding"`
	RowsTotal           int        `json:"rows_total"`
	RowsValid           int        `json:"rows_valid"`
	RowsInvalid         int        `json:"rows_invalid"`
	ReadyInvoices       int        `json:"ready_invoices"`
	QuarantinedInvoices int        `json:"quarantined_invoices"`
	Errors              []RowError `json:"errors"`

	RuleSetVersion         *int                `json:"rule_set_version"`
	InvoicesClean          int                 `json:"invoices_clean"`
	InvoicesWithViolations int                 `json:"invoices_with_violations"`
	InvoiceViolations      []InvoiceViolations `json:"invoice_violations"`

	Error string `json:"error"`
}

// docImpSpy records every call CreateDocumentHandler's imp closure receives.
type docImpSpy struct {
	calls []struct{ entityID, documentID string }
	res   BatchResult
	err   error
}

func (s *docImpSpy) fn() func(ctx context.Context, entityID, documentID string) (BatchResult, error) {
	return func(ctx context.Context, entityID, documentID string) (BatchResult, error) {
		s.calls = append(s.calls, struct{ entityID, documentID string }{entityID, documentID})
		return s.res, s.err
	}
}

// docJSONBody renders the POST /v1/imports/document JSON body.
func docJSONBody(entityID, documentID string) string {
	return fmt.Sprintf(`{"entity_id":%q,"document_id":%q}`, entityID, documentID)
}

// doImportDocumentPost drives CreateDocumentHandler directly, mirroring doImportUpload's shape.
func doImportDocumentPost(t *testing.T, imp func(ctx context.Context, entityID, documentID string) (BatchResult, error), id *auth.Identity, rawBody string) (*httptest.ResponseRecorder, []byte, importDocumentBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports/document", strings.NewReader(rawBody))
	r.Header.Set("Content-Type", "application/json")
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CreateDocumentHandler(imp, nil).ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	var resp importDocumentBody
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec, raw, resp
}

// jsonKeyOrder walks raw's top-level object and returns its keys in wire order -- a
// map[string]json.RawMessage would lose that order, which is exactly what H-05 asserts on.
func jsonKeyOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("read opening token: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("response is not a JSON object: %q", raw)
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatalf("read key token: %v", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			t.Fatalf("expected a string key, got %v", keyTok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("skip value for %s: %v", key, err)
		}
	}
	return keys
}

// --- H-01 ------------------------------------------------------------------------------------

func TestCreateDocumentHandler_NoIdentityReturns401ImpNeverCalled(t *testing.T) {
	spy := &docImpSpy{}
	rec, _, _ := doImportDocumentPost(t, spy.fn(), nil, docJSONBody(uuid.NewString(), uuid.NewString()))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if len(spy.calls) != 0 {
		t.Errorf("imp called %d time(s), want 0 -- an unauthenticated caller must never reach the service", len(spy.calls))
	}
}

// --- H-02 (5 rows: the architect's 4 plus a malformed-entity_id case, correction #5) ---------

func TestCreateDocumentHandler_MalformedRequestIs400ImpNeverCalled(t *testing.T) {
	validEntity := uuid.NewString()
	validDoc := uuid.NewString()
	id := testIdentity()

	cases := []struct {
		name string
		body string
	}{
		{"blankEntityID", docJSONBody("", validDoc)},
		{"blankDocumentID", docJSONBody(validEntity, "")},
		{"nonUUIDDocumentID", docJSONBody(validEntity, "not-a-uuid")},
		{"malformedJSON", `{"entity_id":`},
		// architect correction #2/#5: entity_id needs its own uuid.Parse guard, mirroring
		// internal/invoice/handlers.go:667-670 -- a presence check alone does not satisfy AC #2.
		{"nonUUIDEntityID", docJSONBody("not-a-uuid", validDoc)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &docImpSpy{}
			rec, _, _ := doImportDocumentPost(t, spy.fn(), &id, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if len(spy.calls) != 0 {
				t.Errorf("imp called %d time(s), want 0", len(spy.calls))
			}
		})
	}
}

// --- H-03 --------------------------------------------------------------------------------------

func TestCreateDocumentHandler_ErrorMapping(t *testing.T) {
	id := testIdentity()
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"notFound", ErrNotFound, http.StatusNotFound},
		{"validation", ErrValidation, http.StatusBadRequest},
		{"notActiveMember", db.ErrNotActiveMember, http.StatusForbidden},
		{"operational", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &docImpSpy{err: tc.err}
			rec, _, _ := doImportDocumentPost(t, spy.fn(), &id, docJSONBody(uuid.NewString(), uuid.NewString()))
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

// --- H-04 --------------------------------------------------------------------------------------

func TestCreateDocumentHandler_500BodyIsBareEnvelopeNeverEchoesRawError(t *testing.T) {
	id := testIdentity()
	boom := errors.New(`pq: duplicate key value violates unique constraint "invoices_pkey"`)
	spy := &docImpSpy{err: boom}
	rec, raw, resp := doImportDocumentPost(t, spy.fn(), &id, docJSONBody(uuid.NewString(), uuid.NewString()))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if resp.Error != "internal server error" {
		t.Errorf("error = %q, want the bare \"internal server error\" -- raw body %s", resp.Error, raw)
	}
	if strings.Contains(string(raw), "duplicate key") {
		t.Errorf("response leaked the raw error: %s", raw)
	}
}

// --- H-05 --------------------------------------------------------------------------------------

func TestCreateDocumentHandler_SuccessKeyOrderMatchesSpreadsheetImportResponse(t *testing.T) {
	ruleVer := 3
	golden, err := json.Marshal(importResponse{
		ID: "batch-1", Status: "completed", Format: "csv",
		Delimiter: nilIfEmpty(","), Encoding: nilIfEmpty("utf-8"),
		RowsTotal: 2, RowsValid: 1, RowsInvalid: 1,
		ReadyInvoices: 1, QuarantinedInvoices: 1,
		Errors:                 []RowError{{Row: 2, Field: "invoice_number", Message: "missing"}},
		RuleSetVersion:         &ruleVer,
		InvoicesClean:          1,
		InvoicesWithViolations: 0,
		InvoiceViolations:      []InvoiceViolations{},
	})
	if err != nil {
		t.Fatalf("marshal golden importResponse: %v", err)
	}
	wantKeys := jsonKeyOrder(t, golden)

	id := testIdentity()
	spy := &docImpSpy{res: BatchResult{
		ID: "doc-batch-1", Status: "completed",
		RowsTotal: 1, RowsValid: 1, ReadyInvoices: 1,
		Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{},
	}}
	_, raw, _ := doImportDocumentPost(t, spy.fn(), &id, docJSONBody(uuid.NewString(), uuid.NewString()))
	if len(raw) == 0 {
		t.Fatal("document response body is empty; want the full importResponse shape")
	}
	gotKeys := jsonKeyOrder(t, raw)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("key order = %v, want %v", gotKeys, wantKeys)
	}
}

// --- H-06 --------------------------------------------------------------------------------------

func TestCreateDocumentHandler_FormatIsDocumentDelimiterEncodingAreNull(t *testing.T) {
	id := testIdentity()
	spy := &docImpSpy{res: BatchResult{
		ID: "doc-batch-2", Status: "completed",
		RowsTotal: 1, RowsValid: 1, ReadyInvoices: 1,
		Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{},
	}}
	_, raw, resp := doImportDocumentPost(t, spy.fn(), &id, docJSONBody(uuid.NewString(), uuid.NewString()))
	if resp.Format != "document" {
		t.Errorf("format = %q, want %q -- raw body %s", resp.Format, "document", raw)
	}
	if resp.Delimiter != nil {
		t.Errorf("delimiter = %q, want null", *resp.Delimiter)
	}
	if resp.Encoding != nil {
		t.Errorf("encoding = %q, want null", *resp.Encoding)
	}
}

// --- H-07 (green from the start: reads gateway source, untouched by the stub) ----------------

var handlersDocCorsAllowMethodsRE = regexp.MustCompile(`corsAllowMethods\s*=\s*"([^"]*)"`)

// TestCreateDocumentHandler_CorsAllowMethodsAlreadyContainsPOST mirrors
// internal/extraction/handlers_test.go:485's fence: a NEW http method would need a
// corsAllowMethods edit no other test can see; POST already existing is what this checks.
func TestCreateDocumentHandler_CorsAllowMethodsAlreadyContainsPOST(t *testing.T) {
	raw, err := os.ReadFile("../gateway/cors.go")
	if err != nil {
		t.Fatalf("read ../gateway/cors.go: %v", err)
	}
	m := handlersDocCorsAllowMethodsRE.FindSubmatch(raw)
	if m == nil {
		t.Fatal("no corsAllowMethods constant in ../gateway/cors.go; the extraction lost its anchor")
	}
	methods := string(m[1])
	if strings.TrimSpace(methods) == "" {
		t.Fatal("corsAllowMethods read as empty; the check below would pass vacuously")
	}
	if !strings.Contains(methods, "POST") {
		t.Errorf("corsAllowMethods = %q, want it to already contain POST", methods)
	}
}

// --- H-08 --------------------------------------------------------------------------------------

// TestImportRoutes_DocumentAndSpreadsheetDoNotCollide registers both routes on one in-process
// mux (mirroring cmd/invoice/main.go's registration) and proves neither swallows the other.
func TestImportRoutes_DocumentAndSpreadsheetDoNotCollide(t *testing.T) {
	docSpy := &docImpSpy{res: BatchResult{ID: "d1", Status: "completed", Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{}}}
	var sheetCalls int
	sheetImp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		sheetCalls++
		return BatchResult{ID: "s1", Status: "completed", Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{}}, nil
	}
	open := newFakeDocOpen("f.csv", "text/csv", []byte("invoice_number\nINV-1\n")).fn()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/imports", CreateHandler(sheetImp, open, nil))
	mux.HandleFunc("POST /v1/imports/document", CreateDocumentHandler(docSpy.fn(), nil))

	id := testIdentity()
	entityID, documentID := uuid.NewString(), uuid.NewString()

	r := httptest.NewRequest("POST", "/v1/imports/document", strings.NewReader(docJSONBody(entityID, documentID)))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if sheetCalls != 0 {
		t.Errorf("spreadsheet imp called %d time(s) for a /document request -- pattern collision", sheetCalls)
	}
	if len(docSpy.calls) != 1 {
		t.Errorf("document imp called %d time(s), want 1", len(docSpy.calls))
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}

	// AC #7: /v1/imports must keep its exact current behaviour untouched.
	body, ct := buildImportForm(t, entityID, mustMappingJSON(t, map[string]string{"invoice_number": "invoice_number"}), documentID)
	r2 := httptest.NewRequest("POST", "/v1/imports", body)
	r2.Header.Set("Content-Type", ct)
	r2 = r2.WithContext(auth.WithIdentity(r2.Context(), id))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusCreated {
		t.Errorf("spreadsheet path status = %d, want 201 -- AC #7 regressed", rec2.Code)
	}
	if sheetCalls != 1 {
		t.Errorf("spreadsheet imp called %d time(s), want 1", sheetCalls)
	}
	if len(docSpy.calls) != 1 {
		t.Errorf("document imp called %d time(s) after the /v1/imports request, want still 1 -- pattern collision the other way", len(docSpy.calls))
	}
}

// --- H-09 --------------------------------------------------------------------------------------

// TestCreateDocumentHandler_EndToEndOverRealServiceWritesReadableInvoice wires the REAL
// Service.ImportDocument (no fakes) through an in-process mux and reads the write back with
// the superuser pool -- the stub never calls imp, so both the status and the read-back fail.
func TestCreateDocumentHandler_EndToEndOverRealServiceWritesReadableInvoice(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "H-09 tenant")
	entityID := seedEntity(t, super, tenantID, "H-09 entity")
	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("H-09-INV"))

	svc := newTestService(app)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/imports/document", CreateDocumentHandler(svc.ImportDocument, nil))

	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	r := httptest.NewRequest("POST", "/v1/imports/document", strings.NewReader(docJSONBody(entityID, documentID)))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 -- body %s", rec.Code, rec.Body.String())
	}
	if got := countInvoicesByNumber(t, super, entityID, "H-09-INV"); got != 1 {
		t.Errorf("invoices by number = %d, want 1 -- the write never reached the DB", got)
	}
}
