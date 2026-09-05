// M4-03-05 (task-106): HTTP acceptance tests for internal/importer's
// CreateHandler -- written BEFORE the real handler logic exists (RED against
// handlers.go's not-implemented stub: CreateHandler currently always answers
// 501 "not implemented" without checking identity, parsing the multipart
// body, enforcing the upload cap, or calling the injected imp
// closure, so every assertion below fails on its status/body value, not on a
// compile error). Mirrors internal/invoice/handlers_test.go's httptest +
// auth.WithIdentity idiom; the non-DB cases (401/413/400) use a fake imp
// closure exactly like invoice's fake store closures, while the DB-backed
// cases (201/dry-run/404/xlsx) build the REAL handler over a REAL *Service
// over an INERT gate (&fakeGate{} -- see newTestService's doc in
// service_test.go for why inert and not nil), reusing store_test.go's
// dbTestPools/seedTenant/seedEntity harness.
//
// Spec-to-test map (Test Specs table, M4-03-05 story / task-106):
//
//	IMP-API-01 TestCreateHandler_NoIdentity401
//	IMP-API-02 TestCreateHandler_201
//	IMP-API-03 TestCreateHandler_DryRun200NothingPersisted
//	IMP-API-04 TestCreateHandler_OversizedBody413
//	IMP-API-05 TestCreateHandler_BadMapping400 (missing field + non-JSON garbage)
//	IMP-API-06 TestCreateHandler_EntityNotFound404
//	IMP-API-07 TestCreateHandler_XLSX201
//
// Run: `make test-rls` (or `make test-audit`) for the DB-backed cases, or
// directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/importer/...
//
// The non-DB cases (401/413/400) run with no DSNs set at all.
package importer

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// xlsxContentType is the canonical MIME type for an .xlsx upload -- set
// explicitly on the IMP-API-07 file part so format detection can key off
// either the filename extension or the Content-Type header ([mapping-transport]
// leaves both available to the handler).
const xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// importBatchBody mirrors the (future) POST /v1/imports response wire shape
// -- BatchResult's fields plus the DecodeFacts merged in (format/delimiter/
// encoding), plus an Error field for the shared {"error":"..."} envelope,
// same convention as invoice/handlers_test.go's invoiceBody. Delimiter/
// Encoding are pointers because the story's spec has them null for an xlsx
// upload (DecodeFacts leaves them "" there); RowError already carries its own
// json tags (row/rows/field/message) from store.go, so Errors reuses it
// directly.
type importBatchBody struct {
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
	Error               string     `json:"error"`
}

// importFunc is the exact signature CreateHandler's imp parameter expects
// ((*Service).Import's signature) -- named here purely to keep the test
// helpers below readable.
type importFunc = func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error)

// --- request-building helpers -------------------------------------------

// csvBody renders header+rows as a comma-delimited CSV byte slice, for use as
// a multipart "file" part's content.
func csvBody(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		t.Fatalf("write csv header: %v", err)
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			t.Fatalf("write csv row: %v", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatalf("flush csv writer: %v", err)
	}
	return buf.Bytes()
}

// xlsxBody renders header+rows as a tiny one-sheet .xlsx workbook (via
// excelize, the same library decode.go's decodeXLSX reads with), for use as a
// multipart "file" part's content (IMP-API-07).
func xlsxBody(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0)

	for col, h := range header {
		cell, err := excelize.CoordinatesToCellName(col+1, 1)
		if err != nil {
			t.Fatalf("cell name: %v", err)
		}
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			t.Fatalf("set header cell: %v", err)
		}
	}
	for r, row := range rows {
		for col, v := range row {
			cell, err := excelize.CoordinatesToCellName(col+1, r+2)
			if err != nil {
				t.Fatalf("cell name: %v", err)
			}
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				t.Fatalf("set data cell: %v", err)
			}
		}
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("write xlsx to buffer: %v", err)
	}
	return buf.Bytes()
}

// buildMultipartBody assembles a POST /v1/imports multipart body: entity_id
// (skipped if "") + mapping (skipped if ""), then a "file" part named
// filename with fileContent (skipped if filename == ""). fileContentType, if
// non-empty, is set explicitly on the file part's Content-Type header
// (otherwise CreateFormFile's default of application/octet-stream applies,
// leaving detection to the filename extension alone). Returns the encoded
// body and the multipart Content-Type header value (with boundary) the
// request must carry.
func buildMultipartBody(t *testing.T, entityID, mappingJSON, filename, fileContentType string, fileContent []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if entityID != "" {
		if err := w.WriteField("entity_id", entityID); err != nil {
			t.Fatalf("write entity_id field: %v", err)
		}
	}
	if mappingJSON != "" {
		if err := w.WriteField("mapping", mappingJSON); err != nil {
			t.Fatalf("write mapping field: %v", err)
		}
	}
	if filename != "" {
		var fw io.Writer
		var err error
		if fileContentType != "" {
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
			h.Set("Content-Type", fileContentType)
			fw, err = w.CreatePart(h)
		} else {
			fw, err = w.CreateFormFile("file", filename)
		}
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := fw.Write(fileContent); err != nil {
			t.Fatalf("write file content: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// storedUpload is buildMultipartBody's [upload-once] counterpart for the
// import path: the bytes that used to ride as a "file" part now sit in a
// stored document, and the request names it by id. Returns the request body,
// its Content-Type, and the open fake that serves those bytes back.
func storedUpload(t *testing.T, entityID, mappingJSON, filename, contentType string, content []byte) (io.Reader, string, *fakeDocOpen) {
	t.Helper()
	open := newFakeDocOpen(filename, contentType, content)
	body, ct := buildImportForm(t, entityID, mappingJSON, open.doc.ID)
	return body, ct, open
}

// dbStoredUpload is storedUpload for the specs that actually PERSIST:
// import_batches.document_id and invoices.source_document_id both carry a
// composite FK, so a made-up id is a 23503. Stores through the real
// document.Service over an in-memory object store, exactly as production does,
// and returns its Open as the handler's seam.
func dbStoredUpload(t *testing.T, app *pgxpool.Pool, tenantID, entityID, mappingJSON, filename, contentType string, content []byte) (io.Reader, string, openSpec) {
	t.Helper()
	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	doc := storeDocumentAs(t, docSvc, tenantID, filename, contentType, content)
	body, ct := buildImportForm(t, entityID, mappingJSON, doc.ID)
	return body, ct, docSvc.Open
}

// doImportCreate builds the POST /v1/imports request (query appended
// verbatim, e.g. "?dry_run=true"), injects id into the context when non-nil
// (auth.WithIdentity, mirroring invoice/handlers_test.go's doInvoiceCreate),
// runs it through CreateHandler(imp, open, nil), and decodes the JSON
// response body -- tolerating a completely empty body. Thin wrapper over
// doImportUpload (handlers_upload_once_test.go) so the specs below keep their
// two-value call shape.
func doImportCreate(t *testing.T, imp importFunc, open openSpec, id *auth.Identity, query, contentType string, body io.Reader) (*httptest.ResponseRecorder, importBatchBody) {
	t.Helper()
	rec, _, resp := doImportUpload(t, imp, open, id, query, contentType, body)
	return rec, resp
}

// --- IMP-API-01: no identity ------------------------------------------------

// TestCreateHandler_NoIdentity401 (IMP-API-01): no identity in the request
// context must 401 before any multipart parsing or import runs -- asserted by
// failing the test if imp is ever called (mirrors invoice's
// TestCreateHandler_NoIdentity401). RED against the 501 stub: the status
// assertion fails (got 501, want 401).
func TestCreateHandler_NoIdentity401(t *testing.T) {
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run without an identity")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType, open := storedUpload(t, uuid.NewString(), string(mappingJSON), "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	rec, resp := doImportCreate(t, imp, open.fn(), nil, "", contentType, body)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- IMP-API-02/03: real DB-backed import (real + dry-run) ------------------

// TestCreateHandler_201 (IMP-API-02): a valid CSV multipart upload (a seeded
// entity, mapping matching the CSV header, one data row, no ?dry_run) with
// identity set must produce 201 with a non-empty id, status "completed", and
// the row/invoice counts for the one ready invoice. RED against the 501
// stub: every field assertion fails (got status 501, empty body).
func TestCreateHandler_201(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})

	tenantID := seedTenant(t, super, "IMP-API-02 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-API-02 entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}
	rows := [][]string{{"IMP-API-02-1", "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"}}
	mapping := map[string]string{
		"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
		"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
	}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType, open := dbStoredUpload(t, app, tenantID, entityID, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, open, &id, "", contentType, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.ID == "" {
		t.Error("expected a non-empty id in the body")
	}
	if resp.Status != "completed" {
		t.Errorf("status = %q, want %q", resp.Status, "completed")
	}
	if resp.RowsTotal != 1 || resp.RowsValid != 1 || resp.RowsInvalid != 0 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (1,1,0)", resp.RowsTotal, resp.RowsValid, resp.RowsInvalid)
	}
	if resp.ReadyInvoices != 1 || resp.QuarantinedInvoices != 0 {
		t.Errorf("invoices = (ready=%d quarantined=%d), want (1,0)", resp.ReadyInvoices, resp.QuarantinedInvoices)
	}
}

// TestCreateHandler_DryRun200NothingPersisted (IMP-API-03): the SAME kind of
// request as IMP-API-02 but with ?dry_run=true must produce 200 with the same
// counts, AND leave zero import_batches/invoices rows behind for the entity
// (verified directly via the superuser pool, bypassing RLS) -- a dry run must
// never write. RED against the 501 stub: the status assertion fails (got
// 501).
func TestCreateHandler_DryRun200NothingPersisted(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})

	tenantID := seedTenant(t, super, "IMP-API-03 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-API-03 entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}
	rows := [][]string{{"IMP-API-03-1", "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"}}
	mapping := map[string]string{
		"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
		"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
	}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType, open := dbStoredUpload(t, app, tenantID, entityID, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, open, &id, "?dry_run=true", contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for ?dry_run=true (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.RowsTotal != 1 || resp.RowsValid != 1 || resp.RowsInvalid != 0 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (1,1,0)", resp.RowsTotal, resp.RowsValid, resp.RowsInvalid)
	}
	if resp.ReadyInvoices != 1 || resp.QuarantinedInvoices != 0 {
		t.Errorf("invoices = (ready=%d quarantined=%d), want (1,0)", resp.ReadyInvoices, resp.QuarantinedInvoices)
	}

	ctx := context.Background()
	var batchCount, invoiceCount int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, entityID).Scan(&batchCount); err != nil {
		t.Fatalf("count import_batches: %v", err)
	}
	if err := super.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE entity_id = $1`, entityID).Scan(&invoiceCount); err != nil {
		t.Fatalf("count invoices: %v", err)
	}
	if batchCount != 0 {
		t.Errorf("import_batches rows for entity = %d, want 0 (dry run must persist nothing)", batchCount)
	}
	if invoiceCount != 0 {
		t.Errorf("invoices rows for entity = %d, want 0 (dry run must persist nothing)", invoiceCount)
	}
}

// --- IMP-API-04: oversized body ---------------------------------------------

// TestCreateHandler_OversizedBody413 (IMP-API-04): a multipart body over the
// upload cap ([upload-cap]) must 413 with a non-empty error message, and imp
// must never run. No DB needed -- the cap must fire before Decode/Import are
// ever reached. Since [upload-once] the file itself no longer crosses this
// wire, so the padding rides in an unrelated part: the cap bounds the WHOLE
// request, not one part.
func TestCreateHandler_OversizedBody413(t *testing.T) {
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: uuid.NewString()}
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run when the request body exceeds the upload cap")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	open := newFakeDocOpen("data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	body, contentType := buildImportForm(t, uuid.NewString(), string(mappingJSON), open.doc.ID,
		importPart{field: "pad", filename: "pad.bin", content: bytes.Repeat([]byte("x"), maxUploadBytes+1024)})
	rec, resp := doImportCreate(t, imp, open.fn(), &id, "", contentType, body)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a body over the upload cap (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- IMP-API-05: missing/malformed mapping ----------------------------------

// TestCreateHandler_BadMapping400 (IMP-API-05): a multipart body whose
// mapping field is either absent entirely, or present but non-JSON garbage,
// must 400 before imp ever runs. No DB needed.
func TestCreateHandler_BadMapping400(t *testing.T) {
	tests := []struct {
		name       string
		mappingRaw string // "" means omit the mapping field entirely
	}{
		{"mapping field missing", ""},
		{"mapping is non-JSON garbage", "not-json{{{"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: uuid.NewString()}
			imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
				t.Fatal("imp must not run when mapping is missing or malformed")
				return BatchResult{}, nil
			}
			body, contentType, open := storedUpload(t, uuid.NewString(), tc.mappingRaw, "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
			rec, resp := doImportCreate(t, imp, open.fn(), &id, "", contentType, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			if resp.Error == "" {
				t.Error("expected a non-empty error message in the body")
			}
		})
	}
}

// --- IMP-API-06: entity not in caller's tenant -------------------------------

// TestCreateHandler_EntityNotFound404 (IMP-API-06): a valid multipart upload
// whose entity_id is a random uuid never seeded under the caller's tenant
// must 404 -- needs a real Service so EntitySupplier's zero-rows lookup
// actually surfaces ErrNotFound (a fake imp closure can't exercise this path
// meaningfully). RED against the 501 stub: the status assertion fails (got
// 501, want 404).
func TestCreateHandler_EntityNotFound404(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})

	tenantID := seedTenant(t, super, "IMP-API-06 tenant")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No"}
	rows := [][]string{{"IMP-API-06-1"}}
	mapping := map[string]string{"invoice_number": "Inv No"}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	unseededEntityID := uuid.NewString() // never seeded under tenantID
	body, contentType, open := dbStoredUpload(t, app, tenantID, unseededEntityID, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, open, &id, "", contentType, body)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an entity_id not seeded under the caller's tenant (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- IMP-API-07: xlsx upload -------------------------------------------------

// TestCreateHandler_XLSX201 (IMP-API-07): an .xlsx multipart upload (built
// via excelize, filename + Content-Type both signaling xlsx) with a mapping
// matching its header must be routed to the xlsx decode path and produce 201,
// same as a CSV upload. RED against the 501 stub: the status assertion fails
// (got 501, want 201).
func TestCreateHandler_XLSX201(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})

	tenantID := seedTenant(t, super, "IMP-API-07 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-API-07 entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}
	rows := [][]string{{"IMP-API-07-1", "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"}}
	mapping := map[string]string{
		"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
		"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
	}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType, open := dbStoredUpload(t, app, tenantID, entityID, string(mappingJSON), "data.xlsx", xlsxContentType, xlsxBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, open, &id, "", contentType, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for an xlsx upload (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.ID == "" {
		t.Error("expected a non-empty id in the body")
	}
	if resp.Status != "completed" {
		t.Errorf("status = %q, want %q", resp.Status, "completed")
	}
}

// --- AUDIT-12-01 AC-1: a caller the seam refuses gets 403, never 500 -------

// TestImport_SuspendedCallerIs403NotAServerError: db.ErrNotActiveMember from
// open must route through statusForErr, not the switch's default 500 arm.
// imp must never run -- the refusal happens in open(), before imp exists.
func TestImport_SuspendedCallerIs403NotAServerError(t *testing.T) {
	id := testIdentity()
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run for a caller the seam refuses")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType, open := storedUpload(t, uuid.NewString(), string(mappingJSON), "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	open.err = db.ErrNotActiveMember
	rec, resp := doImportCreate(t, imp, open.fn(), &id, "", contentType, body)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a caller the seam refuses (body=%v)", rec.Code, resp)
	}
	if resp.Error != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want %q", resp.Error, db.NotActiveMemberMessage)
	}
}

// --- AUDIT-12-01 AC-4: no second sentinel, one mapper -----------------------

// imhmAlias returns the name f binds importPath to, or defaultName when the
// import has no explicit alias, or "" when f does not import it at all.
func imhmAlias(f *ast.File, importPath, defaultName string) string {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return defaultName
	}
	return ""
}

// imhmIsSelector reports whether e is exactly x.sel.
func imhmIsSelector(e ast.Expr, x, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != sel {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == x
}

// imhmIsErrorsIsCall reports whether e is errorsAlias.Is(_, sentinelAlias.sentinelName).
func imhmIsErrorsIsCall(e ast.Expr, errorsAlias, sentinelAlias, sentinelName string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	return imhmIsSelector(call.Fun, errorsAlias, "Is") && imhmIsSelector(call.Args[1], sentinelAlias, sentinelName)
}

// imhmBodyHasArm reports whether body consults db.ErrNotActiveMember, either
// through a bare call to statusForErr or an inline errors.Is arm.
func imhmBodyHasArm(body ast.Node, errorsAlias, dbAlias string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "statusForErr" {
			found = true
			return false
		}
		if imhmIsErrorsIsCall(call, errorsAlias, dbAlias, "ErrNotActiveMember") {
			found = true
			return false
		}
		return true
	})
	return found
}

// imhmSwitchArmed finds the tagless switch inside fnBody that maps
// document.ErrNotFound (CreateHandler's and SheetHandler's open-error
// switch) and reports whether its default arm now also consults
// db.ErrNotActiveMember. siteFound is false when no such switch exists --
// a renamed or restructured site, distinct from an unarmed one.
func imhmSwitchArmed(fnBody *ast.BlockStmt, errorsAlias, dbAlias, docAlias string) (siteFound, armed bool) {
	ast.Inspect(fnBody, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		isDocSwitch := false
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				if imhmIsErrorsIsCall(expr, errorsAlias, docAlias, "ErrNotFound") {
					isDocSwitch = true
				}
			}
		}
		if !isDocSwitch {
			return true
		}
		siteFound = true
		if imhmBodyHasArm(sw.Body, errorsAlias, dbAlias) {
			armed = true
		}
		return true
	})
	return
}

// imhmStoreErrArmed finds `_, err := store(...)` inside fnBody (PreviewHandler)
// and reports whether the `if err != nil` block right after it now consults
// db.ErrNotActiveMember.
func imhmStoreErrArmed(fnBody *ast.BlockStmt, errorsAlias, dbAlias string) (siteFound, armed bool) {
	ast.Inspect(fnBody, func(n ast.Node) bool {
		blk, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range blk.List {
			assign, ok := stmt.(*ast.AssignStmt)
			if !ok || assign.Tok != token.DEFINE || len(assign.Rhs) != 1 || len(assign.Lhs) == 0 {
				continue
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				continue
			}
			fnIdent, ok := call.Fun.(*ast.Ident)
			if !ok || fnIdent.Name != "store" {
				continue
			}
			errIdent, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident)
			if !ok {
				continue
			}
			siteFound = true
			if i+1 >= len(blk.List) {
				continue
			}
			ifs, ok := blk.List[i+1].(*ast.IfStmt)
			if !ok || ifs.Init != nil {
				continue
			}
			bin, ok := ifs.Cond.(*ast.BinaryExpr)
			if !ok || bin.Op != token.NEQ {
				continue
			}
			x, xOK := bin.X.(*ast.Ident)
			y, yOK := bin.Y.(*ast.Ident)
			matches := (xOK && x.Name == errIdent.Name && yOK && y.Name == "nil") ||
				(yOK && y.Name == errIdent.Name && xOK && x.Name == "nil")
			if !matches {
				continue
			}
			if imhmBodyHasArm(ifs.Body, errorsAlias, dbAlias) {
				armed = true
			}
		}
		return true
	})
	return
}

// imhmIsErrorsNewCall reports whether e is errorsAlias.New(...).
func imhmIsErrorsNewCall(e ast.Expr, errorsAlias string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	return imhmIsSelector(call.Fun, errorsAlias, "New")
}

// TestImporterHandlers_NoSecondSentinel (AC-4): db.ErrNotActiveMember is
// reached at each of the three sites ONLY through statusForErr or an inline
// errors.Is arm, and no new top-level error var joins the three the package
// already declares (ErrBackfillPrivilegedRole, ErrValidation, ErrNotFound).
// AST, not text, mirroring internal/platform/db/handler_mapping_test.go's
// scan -- scoped to one package and the three named sites rather than a
// generic per-function population, because CreateHandler already calls
// statusForErr for Service.Import's own error (handlers.go:303): a
// whole-function-body scan would find that call and pass vacuously without
// ever touching the broken open()-error switch this test exists to catch.
func TestImporterHandlers_NoSecondSentinel(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	const dbImportPath = "github.com/SimonOsipov/invoice-os/internal/platform/db"
	const docImportPath = "github.com/SimonOsipov/invoice-os/internal/document"
	allowedSentinels := map[string]bool{
		"ErrBackfillPrivilegedRole": true,
		"ErrValidation":             true,
		"ErrNotFound":               true,
	}

	found := map[string]bool{"CreateHandler": false, "PreviewHandler": false, "SheetHandler": false}
	armed := map[string]bool{"CreateHandler": false, "PreviewHandler": false, "SheetHandler": false}
	var newSentinels []string

	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		errorsAlias := imhmAlias(f, "errors", "errors")
		dbAlias := imhmAlias(f, dbImportPath, "db")
		docAlias := imhmAlias(f, docImportPath, "document")

		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, vname := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						if imhmIsErrorsNewCall(vs.Values[i], errorsAlias) && !allowedSentinels[vname.Name] {
							newSentinels = append(newSentinels, name+":"+vname.Name)
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil {
					continue
				}
				if _, want := found[d.Name.Name]; !want {
					continue
				}
				found[d.Name.Name] = true
				var siteFound, isArmed bool
				if d.Name.Name == "PreviewHandler" {
					siteFound, isArmed = imhmStoreErrArmed(d.Body, errorsAlias, dbAlias)
				} else {
					siteFound, isArmed = imhmSwitchArmed(d.Body, errorsAlias, dbAlias, docAlias)
				}
				if !siteFound {
					t.Errorf("%s: expected error-handling site not found -- scan needs updating for a restructured handler", d.Name.Name)
				}
				armed[d.Name.Name] = isArmed
			}
		}
	}

	for _, name := range [...]string{"CreateHandler", "PreviewHandler", "SheetHandler"} {
		if !found[name] {
			t.Fatalf("%s: not found in internal/importer -- scan target renamed or moved", name)
		}
		if !armed[name] {
			t.Errorf("%s: the refused-caller path never consults statusForErr or errors.Is(_, db.ErrNotActiveMember) -- a caller the seam refuses still gets a 500 here", name)
		}
	}
	if len(newSentinels) > 0 {
		t.Errorf("unexpected new sentinel var(s) %v -- AC-4 forbids a second sentinel for db.ErrNotActiveMember", newSentinels)
	}
}

// --- GET /v1/imports/{id} (task-283 specs 4/5/6) ----------------------------

// batchResponseBody mirrors the (future) GET /v1/imports/{id} response wire
// shape (batchResponse, handlers.go), plus an Error field for the shared
// {"error":"..."} envelope -- same convention as importBatchBody.
type batchResponseBody struct {
	ID             string     `json:"id"`
	EntityID       string     `json:"entity_id"`
	Status         string     `json:"status"`
	RowsTotal      int        `json:"rows_total"`
	RowsValid      int        `json:"rows_valid"`
	RowsInvalid    int        `json:"rows_invalid"`
	Errors         []RowError `json:"errors"`
	RuleSetVersion *int       `json:"rule_set_version"`
	CreatedAt      time.Time  `json:"created_at"`
	// Filename (BULK-01-01, GAP 2, QA Stage 4): json.Unmarshal silently
	// ignores an unknown key, so without this field BULK-01-2/3 would decode
	// successfully and pass while asserting nothing -- this field must be
	// present for those specs to be meaningful. Production batchResponse
	// (handlers.go) does NOT gain a Filename field/json key yet (AC #6 is the
	// executor's job); until then no response ever carries "filename" at all,
	// so resp.Filename below always decodes to nil.
	Filename *string `json:"filename"`
	Error    string  `json:"error"`
}

// doImportGetBatch builds the GET /v1/imports/{id} request, injects id into
// the context when non-nil (mirrors doImportCreate), runs it through
// GetHandler(get, nil), and decodes the JSON response body -- tolerating a
// completely empty body (the 501 stub writes a body, but this mirrors
// doImportCreate's own tolerance for consistency).
func doImportGetBatch(t *testing.T, get func(ctx context.Context, id string) (Batch, error), id *auth.Identity, batchID string) (*httptest.ResponseRecorder, batchResponseBody) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/imports/"+batchID, nil)
	r.SetPathValue("id", batchID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	GetHandler(get, nil).ServeHTTP(rec, r)
	var resp batchResponseBody
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

// TestGetBatchHandler_ErrorsIsEmptyArrayNotNull (spec 4): a store returning
// Batch{Errors: nil} must still render "errors":[], never null -- marshalling
// a nil slice without omitempty would emit null. RED against the 501 stub:
// the status assertion fails (got 501, want 200).
func TestGetBatchHandler_ErrorsIsEmptyArrayNotNull(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	batchID := uuid.NewString()
	get := func(ctx context.Context, gotID string) (Batch, error) {
		return Batch{ID: batchID, Errors: nil}, nil
	}
	rec, _ := doImportGetBatch(t, get, &id, batchID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if !bytes.Contains(raw, []byte(`"errors":[]`)) {
		t.Errorf("body = %s, want raw JSON to contain \"errors\":[] (never null, even when the store returns a nil []RowError)", raw)
	}
}

// TestGetBatchHandler_MalformedID400StoreNeverCalled (spec 6, task-283 R5):
// a malformed (non-uuid) path id must 400 BEFORE store.GetBatch is ever
// called. The status alone is VACUOUS here: a malformed id that DID reach
// Store.GetBatch would ALSO surface as 400, via its own 22P02->ErrValidation
// mapping (store.go) -- so 400 cannot by itself prove a handler-level
// uuid.Parse pre-check exists. The spy (store.GetBatch must never run) is
// the ONLY half of this test that actually discriminates the two
// implementations.
func TestGetBatchHandler_MalformedID400StoreNeverCalled(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(ctx context.Context, gotID string) (Batch, error) {
		t.Fatal("store.GetBatch must not run when the path id is not a well-formed uuid")
		return Batch{}, nil
	}
	rec, resp := doImportGetBatch(t, get, &id, "not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestGetBatchHandler_NoIdentity401 (QA Stage 4, task-283 F2 / AC #3
// "identity-first 401"): no identity in the request context must 401
// BEFORE the handler-level uuid.Parse guard, and BEFORE store.GetBatch,
// ever run -- proven here with a MALFORMED path id ("not-a-uuid"), so a
// wrong ordering (uuid.Parse before the identity check) would surface as
// 400, not 401, discriminating the two orderings. The spy additionally
// proves the store itself never runs either way.
func TestGetBatchHandler_NoIdentity401(t *testing.T) {
	get := func(ctx context.Context, gotID string) (Batch, error) {
		t.Fatal("store.GetBatch must not run without an identity")
		return Batch{}, nil
	}
	rec, resp := doImportGetBatch(t, get, nil, "not-a-uuid")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- BULK-01-01 (task-305): filename threaded from the multipart part -----

// TestCreateHandler_PassesSanitizedPartFilenameToImp (BULK-01-9): the source
// document's OWN filename must reach imp -- not the entity_id form field, not
// left blank. Since [upload-once] it is read off the document row (Store
// already sanitised it) rather than off a multipart part. A spy imp records
// both so a transposition (filename where entity_id belongs) would also be
// caught.
func TestCreateHandler_PassesSanitizedPartFilenameToImp(t *testing.T) {
	var capturedEntityID, capturedFilename string
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		capturedEntityID = entityID
		capturedFilename = filename
		return BatchResult{}, nil
	}
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: uuid.NewString()}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	seededEntityID := uuid.NewString()
	body, contentType, open := storedUpload(t, seededEntityID, string(mappingJSON), "report.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	rec, _ := doImportCreate(t, imp, open.fn(), &id, "", contentType, body)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("unexpected 401 (body=%s)", rec.Body.String())
	}
	if capturedFilename != "report.csv" {
		t.Errorf("imp received filename = %q, want %q (the document row's own name)", capturedFilename, "report.csv")
	}
	if capturedEntityID != seededEntityID {
		t.Errorf("imp received entityID = %q, want %q", capturedEntityID, seededEntityID)
	}
	if capturedFilename == capturedEntityID {
		t.Fatalf("fixture assumption broken: filename and entityID must differ to prove no transposition (%q == %q)", capturedFilename, capturedEntityID)
	}
}

// TestCreateHandler_DryRunFilenameThreadedButNothingPersisted (BULK-01-10,
// AC #9): ?dry_run=true with a filename present must still thread the
// sanitised filename to imp (CreateHandler's imp call site is UNCONDITIONAL
// on dry_run -- a single call site serves both paths, so a careless
// implementation that only wires filename on the real path would be caught
// here) AND must create no batch (leg 2, DB-backed real Service). RED
// against the stubs: leg 1's capturedFilename never matches (CreateHandler
// always threads "").
func TestCreateHandler_DryRunFilenameThreadedButNothingPersisted(t *testing.T) {
	// Leg 1 (spy, no DB): filename threading survives the dry-run path.
	var capturedFilename string
	var capturedDryRun bool
	spy := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		capturedFilename = filename
		capturedDryRun = dryRun
		return BatchResult{RowsTotal: len(rows), RowsValid: len(rows)}, nil
	}
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: uuid.NewString()}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType, open := storedUpload(t, uuid.NewString(), string(mappingJSON), "dryrun-report.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	rec, resp := doImportCreate(t, spy, open.fn(), &id, "?dry_run=true", contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for ?dry_run=true (body=%s)", rec.Code, rec.Body.String())
	}
	if !capturedDryRun {
		t.Fatal("fixture assumption broken: imp was not called with dryRun=true")
	}
	if capturedFilename != "dryrun-report.csv" {
		t.Errorf("imp received filename = %q, want %q even on the dry-run path", capturedFilename, "dryrun-report.csv")
	}
	if resp.RowsTotal != 1 || resp.RowsValid != 1 {
		t.Errorf("counts = (total=%d valid=%d), want (1,1) (shipped body shape preserved)", resp.RowsTotal, resp.RowsValid)
	}

	// Leg 2 (DB-backed, real Service): the same shape through the REAL
	// Service/Store must persist zero import_batches rows.
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})

	tenantID := seedTenant(t, super, "BULK-01-10 tenant")
	entityID := seedEntity(t, super, tenantID, "BULK-01-10 entity")
	realID := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}
	rows := [][]string{{"BULK-01-10-1", "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"}}
	mapping := map[string]string{
		"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
		"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
	}
	realMappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	realBody, realContentType, realOpen := dbStoredUpload(t, app, tenantID, entityID, string(realMappingJSON), "dryrun-report.csv", "", csvBody(t, header, rows))
	realRec, _ := doImportCreate(t, svc.Import, realOpen, &realID, "?dry_run=true", realContentType, realBody)
	if realRec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for ?dry_run=true (body=%s)", realRec.Code, realRec.Body.String())
	}

	ctx := context.Background()
	var batchCount int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, entityID).Scan(&batchCount); err != nil {
		t.Fatalf("count import_batches: %v", err)
	}
	if batchCount != 0 {
		t.Errorf("import_batches rows for entity = %d, want 0 (dry run with a filename present must still persist nothing)", batchCount)
	}
}

// --- EXTR-15-10 (task-855, Mode A): document_id on the GET body ------------

// BD-2 (AC-2): a batchResponse with no stored document serialises an explicit
// "document_id":null. A `,omitempty` on the tag drops the key instead, and the
// SPA's ImportBatch.document_id would then read `undefined`, not null.
//
// DOC-01's TestGetBatch_BodyOmitsDocumentID (handlers_upload_once_test.go)
// pins the opposite and must be DELETED, not weakened: `,omitempty` satisfies
// both only until something populates the field. hasDocumentIDKey itself
// stays -- its other two call sites are the preview endpoint's.
func TestBatchResponse_DocumentIDSerialisesAsExplicitNull(t *testing.T) {
	b, err := json.Marshal(batchResponse{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// CONTROL, paired with the assertion below: rule_set_version is the shipped
	// pointer-with-no-omitempty this mirrors, so a miss below is the new field's
	// own absence, not a broken predicate.
	if !bytes.Contains(b, []byte(`"rule_set_version":null`)) {
		t.Fatalf("control needle: body %s no longer contains \"rule_set_version\":null", b)
	}
	if !bytes.Contains(b, []byte(`"document_id":null`)) {
		t.Errorf("body = %s, want it to contain \"document_id\":null (EXTR-15-10 AC-2)", b)
	}
}

// BD-2b (AC-2): the tag is exactly `document_id` and the field is *string. The
// marshal spec above passes on a plain `string` too, once anything populates
// it -- "" renders "document_id":"" and never null.
func TestBatchResponse_DocumentIDIsAPointerTaggedWithoutOmitempty(t *testing.T) {
	ty := reflect.TypeOf(batchResponse{})

	// CONTROL: Filename is the shipped *string-with-a-bare-tag this mirrors.
	fn, ok := ty.FieldByName("Filename")
	if !ok || fn.Tag.Get("json") != "filename" {
		t.Fatalf("control needle: batchResponse.Filename's json tag is no longer a bare `filename`")
	}

	f, ok := ty.FieldByName("DocumentID")
	if !ok {
		t.Fatalf("not implemented -- batchResponse must carry DocumentID *string `json:\"document_id\"` (EXTR-15-10 AC-2)")
	}
	if got := f.Tag.Get("json"); got != "document_id" {
		t.Errorf("json tag = %q, want exactly %q -- no omitempty", got, "document_id")
	}
	if f.Type.String() != "*string" {
		t.Errorf("DocumentID is %s, want *string", f.Type)
	}
}

// BD-8 (AC-1/AC-2 seam, not in the plan's table): the field can exist on BOTH
// Batch and batchResponse and still never be copied between them, leaving the
// SPA a permanent null. Asserts on the raw body, not on batchResponseBody --
// json.Unmarshal drops an unknown key silently.
func TestGetHandler_BodyCarriesTheBatchDocumentID(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	batchID := uuid.NewString()
	documentID := uuid.NewString()

	batch := batchWithDocumentID(t, Batch{
		ID:        batchID,
		EntityID:  uuid.NewString(),
		Status:    "completed",
		RowsTotal: 1,
		Errors:    []RowError{},
		CreatedAt: time.Now().UTC(),
	}, documentID)

	rec, _ := doImportGetBatch(t, func(context.Context, string) (Batch, error) { return batch, nil }, &id, batchID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	want := `"document_id":"` + documentID + `"`
	if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
		t.Errorf("body = %s, want it to contain %s -- GetHandler must copy Batch.DocumentID into batchResponse", rec.Body.String(), want)
	}
}
