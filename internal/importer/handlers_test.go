// M4-03-05 (task-106): HTTP acceptance tests for internal/importer's
// CreateHandler -- written BEFORE the real handler logic exists (RED against
// handlers.go's not-implemented stub: CreateHandler currently always answers
// 501 "not implemented" without checking identity, parsing the multipart
// body, enforcing the 10 MiB upload cap, or calling the injected imp
// closure, so every assertion below fails on its status/body value, not on a
// compile error). Mirrors internal/invoice/handlers_test.go's httptest +
// auth.WithIdentity idiom; the non-DB cases (401/413/400) use a fake imp
// closure exactly like invoice's fake store closures, while the DB-backed
// cases (201/dry-run/404/xlsx) build the REAL handler over a REAL *Service
// (NewService(NewStore(app), invoice.NewStore(app), nil)), reusing
// store_test.go's dbTestPools/seedTenant/seedEntity harness.
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
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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
type importFunc = func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error)

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

// doImportCreate builds the POST /v1/imports request (query appended
// verbatim, e.g. "?dry_run=true"), injects id into the context when non-nil
// (auth.WithIdentity, mirroring invoice/handlers_test.go's doInvoiceCreate),
// runs it through CreateHandler(imp, nil), and decodes the JSON response body
// -- tolerating a completely empty body (the 501 stub writes no body at all,
// so json.Unmarshal on 0 bytes would otherwise fail the test on a decode
// error rather than on the real status/field assertions).
func doImportCreate(t *testing.T, imp importFunc, id *auth.Identity, query, contentType string, body io.Reader) (*httptest.ResponseRecorder, importBatchBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports"+query, body)
	r.Header.Set("Content-Type", contentType)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CreateHandler(imp, nil).ServeHTTP(rec, r)

	var resp importBatchBody
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

// --- IMP-API-01: no identity ------------------------------------------------

// TestCreateHandler_NoIdentity401 (IMP-API-01): no identity in the request
// context must 401 before any multipart parsing or import runs -- asserted by
// failing the test if imp is ever called (mirrors invoice's
// TestCreateHandler_NoIdentity401). RED against the 501 stub: the status
// assertion fails (got 501, want 401).
func TestCreateHandler_NoIdentity401(t *testing.T) {
	imp := func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run without an identity")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType := buildMultipartBody(t, uuid.NewString(), string(mappingJSON), "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	rec, resp := doImportCreate(t, imp, nil, "", contentType, body)

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
	svc := NewService(NewStore(app), invoice.NewStore(app), nil)

	tenantID := seedTenant(t, super, "IMP-API-02 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-API-02 entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

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
	body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, &id, "", contentType, body)

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
	svc := NewService(NewStore(app), invoice.NewStore(app), nil)

	tenantID := seedTenant(t, super, "IMP-API-03 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-API-03 entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

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
	body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, &id, "?dry_run=true", contentType, body)

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

// TestCreateHandler_OversizedBody413 (IMP-API-04): a multipart body whose
// file part alone exceeds the 10 MiB upload cap ([upload-cap]) must 413 with
// a non-empty error message, and imp must never run. No DB needed -- the cap
// must fire before Decode/Import are ever reached. RED against the 501
// stub: the status assertion fails (got 501, want 413).
func TestCreateHandler_OversizedBody413(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	imp := func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("imp must not run when the request body exceeds the 10 MiB cap")
		return BatchResult{}, nil
	}
	mappingJSON, err := json.Marshal(map[string]string{"invoice_number": "Inv No"})
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	oversized := bytes.Repeat([]byte("x"), 10<<20+1024) // > 10 MiB ([upload-cap])
	body, contentType := buildMultipartBody(t, uuid.NewString(), string(mappingJSON), "data.csv", "", oversized)
	rec, resp := doImportCreate(t, imp, &id, "", contentType, body)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a body over the 10 MiB cap (body=%s)", rec.Code, rec.Body.String())
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
			id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
			imp := func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
				t.Fatal("imp must not run when mapping is missing or malformed")
				return BatchResult{}, nil
			}
			body, contentType := buildMultipartBody(t, uuid.NewString(), tc.mappingRaw, "data.csv", "", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
			rec, resp := doImportCreate(t, imp, &id, "", contentType, body)

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
	svc := NewService(NewStore(app), invoice.NewStore(app), nil)

	tenantID := seedTenant(t, super, "IMP-API-06 tenant")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	header := []string{"Inv No"}
	rows := [][]string{{"IMP-API-06-1"}}
	mapping := map[string]string{"invoice_number": "Inv No"}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	unseededEntityID := uuid.NewString() // never seeded under tenantID
	body, contentType := buildMultipartBody(t, unseededEntityID, string(mappingJSON), "data.csv", "", csvBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, &id, "", contentType, body)

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
	svc := NewService(NewStore(app), invoice.NewStore(app), nil)

	tenantID := seedTenant(t, super, "IMP-API-07 tenant")
	entityID := seedEntity(t, super, tenantID, "IMP-API-07 entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

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
	body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), "data.xlsx", xlsxContentType, xlsxBody(t, header, rows))
	rec, resp := doImportCreate(t, svc.Import, &id, "", contentType, body)

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
