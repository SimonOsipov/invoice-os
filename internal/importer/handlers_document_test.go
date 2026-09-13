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
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
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

// --- ReadingHandler / SupplyNumberHandler ---------------------------------------------------

// readingSpy records every call ReadingHandler's read closure receives.
type readingSpy struct {
	calls   []string // document ids
	reading *CarriedReading
	err     error
}

func (s *readingSpy) fn() func(ctx context.Context, documentID string) (*CarriedReading, error) {
	return func(ctx context.Context, documentID string) (*CarriedReading, error) {
		s.calls = append(s.calls, documentID)
		return s.reading, s.err
	}
}

// supplySpy records every call SupplyNumberHandler's supply closure receives.
type supplySpy struct {
	calls []struct{ entityID, documentID, invoiceNumber string }
	inv   invoice.Invoice
	err   error
}

func (s *supplySpy) fn() func(ctx context.Context, entityID, documentID, invoiceNumber string) (invoice.Invoice, error) {
	return func(ctx context.Context, entityID, documentID, invoiceNumber string) (invoice.Invoice, error) {
		s.calls = append(s.calls, struct{ entityID, documentID, invoiceNumber string }{entityID, documentID, invoiceNumber})
		return s.inv, s.err
	}
}

// doReadingGet drives ReadingHandler directly with an optional identity and raw query string.
func doReadingGet(t *testing.T, read func(ctx context.Context, documentID string) (*CarriedReading, error), id *auth.Identity, rawQuery string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/imports/document/reading?"+rawQuery, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ReadingHandler(read, nil).ServeHTTP(rec, r)
	return rec, rec.Body.Bytes()
}

// supplyJSONBody renders the POST /v1/imports/document/invoice JSON body.
func supplyJSONBody(entityID, documentID, number string) string {
	b, _ := json.Marshal(supplyRequest{EntityID: entityID, DocumentID: documentID, InvoiceNumber: number})
	return string(b)
}

// doSupplyPost drives SupplyNumberHandler directly with an optional identity and raw JSON body.
func doSupplyPost(t *testing.T, supply func(ctx context.Context, entityID, documentID, invoiceNumber string) (invoice.Invoice, error), id *auth.Identity, rawBody string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports/document/invoice", strings.NewReader(rawBody))
	r.Header.Set("Content-Type", "application/json")
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	SupplyNumberHandler(supply, nil).ServeHTTP(rec, r)
	return rec, rec.Body.Bytes()
}

// --- H-10 ------------------------------------------------------------------------------

func TestReadingHandler_AnswersNullNeverNotFound(t *testing.T) {
	id := testIdentity()
	validDoc := uuid.NewString()

	t.Run("nilReading", func(t *testing.T) {
		spy := &readingSpy{reading: nil}
		rec, raw := doReadingGet(t, spy.fn(), &id, "document_id="+validDoc)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 -- a nothing-to-carry document is not a 404", rec.Code)
		}
		if string(raw) != `{"reading":null}`+"\n" {
			t.Errorf("body = %q, want %q", raw, `{"reading":null}`+"\n")
		}
	})

	t.Run("realReading", func(t *testing.T) {
		reading := &CarriedReading{
			DocumentID: validDoc, ExtractionJobID: "job-1",
			IssueDate: nilIfEmpty("2026-03-01"), BuyerTIN: nilIfEmpty("TIN"), BuyerName: nilIfEmpty("Buyer"),
			Currency: nilIfEmpty("NGN"), Subtotal: nilIfEmpty("10.00"), VAT: nilIfEmpty("1.00"), Total: nilIfEmpty("11.00"),
			LineItems: []CarriedLine{},
		}
		spy := &readingSpy{reading: reading}
		rec, raw := doReadingGet(t, spy.fn(), &id, "document_id="+validDoc)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		gotOuter := jsonKeyOrder(t, raw)
		if !reflect.DeepEqual(gotOuter, []string{"reading"}) {
			t.Fatalf("outer keys = %v, want [reading]", gotOuter)
		}
		var wrapper struct {
			Reading json.RawMessage `json:"reading"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			t.Fatalf("decode wrapper: %v", err)
		}
		wantKeys := []string{"document_id", "extraction_job_id", "issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total", "line_items"}
		gotKeys := jsonKeyOrder(t, wrapper.Reading)
		if !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Errorf("reading keys = %v, want %v", gotKeys, wantKeys)
		}
	})

	t.Run("missingDocumentID", func(t *testing.T) {
		spy := &readingSpy{}
		rec, _ := doReadingGet(t, spy.fn(), &id, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if len(spy.calls) != 0 {
			t.Errorf("read called %d time(s), want 0", len(spy.calls))
		}
	})

	t.Run("malformedDocumentID", func(t *testing.T) {
		spy := &readingSpy{}
		rec, _ := doReadingGet(t, spy.fn(), &id, "document_id=not-a-uuid")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if len(spy.calls) != 0 {
			t.Errorf("read called %d time(s), want 0", len(spy.calls))
		}
	})

	t.Run("noIdentity", func(t *testing.T) {
		spy := &readingSpy{}
		rec, _ := doReadingGet(t, spy.fn(), nil, "document_id="+validDoc)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
		if len(spy.calls) != 0 {
			t.Errorf("read called %d time(s), want 0", len(spy.calls))
		}
	})

	t.Run("notActiveMember", func(t *testing.T) {
		spy := &readingSpy{err: db.ErrNotActiveMember}
		rec, raw := doReadingGet(t, spy.fn(), &id, "document_id="+validDoc)
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 -- body %s", rec.Code, raw)
		}
		var resp struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Error != db.NotActiveMemberMessage {
			t.Errorf("error = %q, want %q", resp.Error, db.NotActiveMemberMessage)
		}
	})

	t.Run("urnUUIDCanonicalized", func(t *testing.T) {
		spy := &readingSpy{}
		parsed, err := uuid.Parse(validDoc)
		if err != nil {
			t.Fatalf("parse validDoc: %v", err)
		}
		rec, _ := doReadingGet(t, spy.fn(), &id, "document_id=urn:uuid:"+validDoc)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		if len(spy.calls) != 1 || spy.calls[0] != parsed.String() {
			t.Errorf("read called with %v, want exactly [%q]", spy.calls, parsed.String())
		}
	})
}

// --- H-11 ------------------------------------------------------------------------------

func TestSupplyNumberHandler_MapsEveryOutcome(t *testing.T) {
	id := testIdentity()
	entityID, documentID := uuid.NewString(), uuid.NewString()

	errCases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"duplicateNumber", invoice.ErrDuplicateNumber, http.StatusConflict, invoice.NumberTakenReason},
		{"readingNotCarried", ErrReadingNotCarried, http.StatusConflict, readingNotCarriedReason},
		{"documentAlreadyFiled", ErrDocumentAlreadyFiled, http.StatusConflict, documentAlreadyFiledReason},
		{"notFound", ErrNotFound, http.StatusNotFound, ""},
		{"invoiceValidation", invoice.ErrValidation, http.StatusBadRequest, "one or more fields failed validation"},
		{"importerValidation", ErrValidation, http.StatusBadRequest, ""},
		{"notActiveMember", db.ErrNotActiveMember, http.StatusForbidden, db.NotActiveMemberMessage},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &supplySpy{err: tc.err}
			rec, raw := doSupplyPost(t, spy.fn(), &id, supplyJSONBody(entityID, documentID, "N1"))
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d -- body %s", rec.Code, tc.wantStatus, raw)
			}
			if tc.wantMsg != "" {
				var resp struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(raw, &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if resp.Error != tc.wantMsg {
					t.Errorf("error = %q, want %q", resp.Error, tc.wantMsg)
				}
			}
		})
	}

	t.Run("success", func(t *testing.T) {
		spy := &supplySpy{inv: invoice.Invoice{ID: "inv-1", InvoiceNumber: "N1", Status: invoice.StatusDraft}}
		rec, raw := doSupplyPost(t, spy.fn(), &id, supplyJSONBody(entityID, documentID, "N1"))
		if rec.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201 -- body %s", rec.Code, raw)
		}
		var resp invoice.Invoice
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.InvoiceNumber != "N1" {
			t.Errorf("invoice_number = %q, want %q", resp.InvoiceNumber, "N1")
		}
	})

	t.Run("blankNumber", func(t *testing.T) {
		spy := &supplySpy{}
		rec, raw := doSupplyPost(t, spy.fn(), &id, supplyJSONBody(entityID, documentID, ""))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 -- body %s", rec.Code, raw)
		}
		if len(spy.calls) != 0 {
			t.Errorf("supply called %d time(s), want 0", len(spy.calls))
		}
	})

	t.Run("whitespaceOnlyNumber", func(t *testing.T) {
		spy := &supplySpy{}
		rec, raw := doSupplyPost(t, spy.fn(), &id, supplyJSONBody(entityID, documentID, "   "))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 -- body %s", rec.Code, raw)
		}
		if len(spy.calls) != 0 {
			t.Errorf("supply called %d time(s), want 0", len(spy.calls))
		}
	})

	t.Run("numberIsTrimmedBeforeCall", func(t *testing.T) {
		spy := &supplySpy{inv: invoice.Invoice{InvoiceNumber: "N2"}}
		doSupplyPost(t, spy.fn(), &id, supplyJSONBody(entityID, documentID, "  N2  "))
		if len(spy.calls) != 1 || spy.calls[0].invoiceNumber != "N2" {
			t.Errorf("supply called with %+v, want invoiceNumber %q", spy.calls, "N2")
		}
	})

	t.Run("bodyTooLarge", func(t *testing.T) {
		spy := &supplySpy{}
		bigNumber := strings.Repeat("x", 5*1024)
		rec, _ := doSupplyPost(t, spy.fn(), &id, supplyJSONBody(entityID, documentID, bigNumber))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", rec.Code)
		}
	})

	t.Run("noIdentity", func(t *testing.T) {
		spy := &supplySpy{}
		rec, _ := doSupplyPost(t, spy.fn(), nil, supplyJSONBody(entityID, documentID, "N1"))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
		if len(spy.calls) != 0 {
			t.Errorf("supply called %d time(s), want 0", len(spy.calls))
		}
	})
}

// --- H-12 ------------------------------------------------------------------------------

// importRoutePatterns reads the pattern cmd/invoice/main.go registers for each named importer
// handler, so the route test below drives the deployed registrations, not copies of them.
func importRoutePatterns(t *testing.T, handlers ...string) map[string]string {
	t.Helper()
	path := filepath.Join(repoRootForImporter(t), "cmd", "invoice", "main.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		h, hOK := call.Args[1].(*ast.CallExpr)
		if !ok || lit.Kind != token.STRING || !hOK {
			return true
		}
		for _, name := range handlers {
			if selectorNameForImporter(h.Fun) != "importer."+name {
				continue
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", lit.Value, err)
			}
			if prev, dup := out[name]; dup {
				t.Errorf("cmd/invoice/main.go registers importer.%s twice (%q and %q)", name, prev, pattern)
			}
			out[name] = pattern
		}
		return true
	})
	for _, name := range handlers {
		if out[name] == "" {
			t.Fatalf("cmd/invoice/main.go registers no route for importer.%s", name)
		}
	}
	return out
}

// TestImportRoutes_ReadingAndSupplyDoNotCollide mounts main.go's four /v1/imports patterns on
// one mux and sends each wire path the clients call. Every request must reach its own handler
// and no other one.
func TestImportRoutes_ReadingAndSupplyDoNotCollide(t *testing.T) {
	patterns := importRoutePatterns(t, "CreateDocumentHandler", "GetHandler", "ReadingHandler", "SupplyNumberHandler")

	var getCalls int
	getFn := func(ctx context.Context, id string) (Batch, error) {
		getCalls++
		return Batch{}, ErrNotFound
	}
	docSpy := &docImpSpy{res: BatchResult{ID: "d1", Status: "completed", Errors: []RowError{}, InvoiceViolations: []InvoiceViolations{}}}
	readSpy := &readingSpy{}
	invoiceSpy := &supplySpy{inv: invoice.Invoice{ID: "inv-1", InvoiceNumber: "N1"}}

	mux := http.NewServeMux()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("main.go's import patterns %v conflict on one mux: %v", patterns, r)
			}
		}()
		mux.HandleFunc(patterns["CreateDocumentHandler"], CreateDocumentHandler(docSpy.fn(), nil))
		mux.HandleFunc(patterns["GetHandler"], GetHandler(getFn, nil))
		mux.HandleFunc(patterns["ReadingHandler"], ReadingHandler(readSpy.fn(), nil))
		mux.HandleFunc(patterns["SupplyNumberHandler"], SupplyNumberHandler(invoiceSpy.fn(), nil))
	}()

	id := testIdentity()
	documentID := uuid.NewString()
	counts := func() [4]int { return [4]int{len(docSpy.calls), getCalls, len(readSpy.calls), len(invoiceSpy.calls)} }
	names := [4]string{"document import", "batch get", "reading", "supply"}

	probes := []struct {
		method, target, body string
		handler              int // index into counts
		wantStatus           int
	}{
		{"GET", "/v1/imports/document/reading?document_id=" + documentID, "", 2, http.StatusOK},
		{"POST", "/v1/imports/document/invoice", supplyJSONBody(uuid.NewString(), documentID, "N1"), 3, http.StatusCreated},
		{"POST", "/v1/imports/document", docJSONBody(uuid.NewString(), documentID), 0, http.StatusCreated},
		{"GET", "/v1/imports/" + uuid.NewString(), "", 1, http.StatusNotFound},
	}
	for _, p := range probes {
		r := httptest.NewRequest(p.method, p.target, strings.NewReader(p.body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		before := counts()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		after := counts()

		if rec.Code != p.wantStatus {
			t.Errorf("%s %s: status = %d, want %d -- body %s", p.method, p.target, rec.Code, p.wantStatus, rec.Body.String())
		}
		for i := range after {
			want := 0
			if i == p.handler {
				want = 1
			}
			if got := after[i] - before[i]; got != want {
				t.Errorf("%s %s reached the %s handler %d time(s), want %d", p.method, p.target, names[i], got, want)
			}
		}
	}
}

// --- H-13 (handler over the real service) ----------------------------------------------

// TestSupplyNumberHandler_AGateOutageStillAnswers201 wires the REAL Service (a fake gate that
// errors) through SupplyNumberHandler on a mux -- the outage must not turn into a 500.
func TestSupplyNumberHandler_AGateOutageStillAnswers201(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "SNH-01 tenant")
	entityID := seedEntity(t, super, tenantID, "SNH-01 entity")
	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docNoNumberValues())

	svc := newTestServiceWithGate(app, &fakeGate{validateBatchErr: invoice.ErrUpstream})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/imports/document/invoice", SupplyNumberHandler(svc.SupplyInvoiceNumber, nil))

	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	r := httptest.NewRequest("POST", "/v1/imports/document/invoice", strings.NewReader(supplyJSONBody(entityID, documentID, "SNH-01-INV")))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 -- body %s", rec.Code, rec.Body.String())
	}
	var resp invoice.Invoice
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.InvoiceNumber != "SNH-01-INV" {
		t.Errorf("invoice_number = %q, want %q", resp.InvoiceNumber, "SNH-01-INV")
	}
	// Never validated: the operator's Re-validate is what stamps a verdict later.
	if resp.Status != invoice.StatusDraft || resp.RuleSetVersionID != nil {
		t.Errorf("status = %q, rule_set_version_id = %v, want draft and null", resp.Status, resp.RuleSetVersionID)
	}
	if got := countInvoicesCitingDocument(t, super, documentID); got != 1 {
		t.Errorf("invoices citing document = %d, want 1", got)
	}
}
