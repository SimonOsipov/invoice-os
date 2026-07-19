// M4-08-01 (task-170): HTTP acceptance tests for internal/importer's
// PreviewHandler -- written BEFORE the real handler logic exists (RED
// against handlers.go's not-implemented stub: PreviewHandler currently
// always answers 501 "not implemented" without checking identity, parsing
// the multipart body, enforcing the upload cap, or calling Decode, so every
// assertion below fails on its status/body value, not on a compile error).
// Mirrors handlers_test.go's httptest + auth.WithIdentity idiom and reuses
// its buildMultipartBody/csvBody/xlsxBody/xlsxContentType helpers verbatim
// -- no duplicate helpers here. Unlike CreateHandler's tests, every case
// below is a plain httptest unit test: PreviewHandler is stateless
// ([preview-stateless]), so none of these need dbTestPools/seedTenant/
// seedEntity, a TestRLS_* name, or a service container -- they all run under
// a bare `go test ./internal/importer/...` with no DSNs set.
//
// Spec-to-test map (Test Specs table, M4-08-01 story / task-170
// Implementation Plan §C):
//
//	PRV-01 TestPreviewHandler_NoIdentityOversizedBody401
//	PRV-02 TestPreviewHandler_CSV200
//	PRV-03 TestPreviewHandler_SemicolonDelimiter
//	PRV-04 TestPreviewHandler_XLSXNullDelimiterEncoding
//	PRV-05 TestPreviewHandler_SampleRowsCappedAtFive
//	PRV-06 TestPreviewHandler_SampleRowsBelowCap
//	PRV-07 TestPreviewHandler_HeaderOnlyCSV
//	PRV-08 TestPreviewHandler_EmptyCSVEmptyArraysNotNull
//	PRV-09 TestPreviewHandler_RaggedRowUnpadded
//	PRV-10 TestPreviewHandler_OversizedBodyWithIdentity413
//	PRV-11 TestPreviewHandler_MissingFilePart400
//	PRV-12 TestPreviewHandler_UnrecognizedFormat400
//	PRV-13 TestPreviewHandler_UndecodableXLSX400
//	PRV-14 TestPreviewHandler_InvalidMultipart400
//	PRV-15 TestPreviewHandler_DuplicateBlankHeaderPreservedVerbatim
//	PRV-16 TestPreviewHandler_ColumnsRowsTotalMatchDirectDecode
//
// Run: no DSNs required --
//
//	go test -count=1 ./internal/importer/...
package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// previewBody mirrors the POST /v1/imports/preview response wire shape
// (previewResponse) plus an Error field for the shared {"error":"..."}
// envelope, same convention as handlers_test.go's importBatchBody.
type previewBody struct {
	Format     string     `json:"format"`
	Delimiter  *string    `json:"delimiter"`
	Encoding   *string    `json:"encoding"`
	Columns    []string   `json:"columns"`
	SampleRows [][]string `json:"sample_rows"`
	RowsTotal  int        `json:"rows_total"`
	Error      string     `json:"error"`
}

// doPreviewRequest builds the POST /v1/imports/preview request, injects id
// into the context when non-nil (auth.WithIdentity, mirroring
// handlers_test.go's doImportCreate), runs it through PreviewHandler(), and
// decodes the JSON response body -- tolerating a completely empty body.
// Returns both the raw bytes (needed by PRV-04/PRV-08's raw-JSON assertions,
// which a struct round-trip cannot make -- encoding/json renders both a nil
// and an empty []string as [] once decoded back into a Go slice) and the
// decoded struct.
func doPreviewRequest(t *testing.T, id *auth.Identity, contentType string, body io.Reader) (*httptest.ResponseRecorder, []byte, previewBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports/preview", body)
	r.Header.Set("Content-Type", contentType)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	PreviewHandler().ServeHTTP(rec, r)

	raw := rec.Body.Bytes()
	var resp previewBody
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return rec, raw, resp
}

// --- PRV-01: ordering proof -- identity before the upload cap ---------------

// TestPreviewHandler_NoIdentityOversizedBody401 (PRV-01): no identity in the
// request context, combined with a body larger than maxUploadBytes, must
// still 401 -- not 413 -- proving the identity check runs before
// http.MaxBytesReader/ParseMultipartForm ever look at the body. The body
// must carry no "columns" key (Decode was never reached). RED against the
// 501 stub: status assertion fails (got 501, want 401).
func TestPreviewHandler_NoIdentityOversizedBody401(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maxUploadBytes+1024)
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", oversized)
	rec, raw, resp := doPreviewRequest(t, nil, contentType, body)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for no identity + an oversized body, NOT 413 (body=%s)", rec.Code, raw)
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
	if bytes.Contains(raw, []byte(`"columns"`)) {
		t.Errorf("body must carry no columns key when the identity check fails first, got %s", raw)
	}
}

// --- PRV-02: CSV success ------------------------------------------------------

// TestPreviewHandler_CSV200 (PRV-02): a valid CSV multipart upload with
// identity set must produce 200, columns equal to the header row in file
// order, and Content-Type: application/json. RED against the 501 stub:
// status assertion fails (got 501, want 200).
func TestPreviewHandler_CSV200(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Invoice No", "Issue Date", "Buyer TIN"}
	rows := [][]string{{"INV-2041", "2026-06-03", "TIN-1"}}
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, header, rows))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !reflect.DeepEqual(resp.Columns, header) {
		t.Errorf("columns = %v, want %v (file order)", resp.Columns, header)
	}
}

// --- PRV-03: semicolon delimiter sniffed -------------------------------------

// TestPreviewHandler_SemicolonDelimiter (PRV-03): a semicolon-delimited
// UTF-8 CSV (raw bytes built here -- csvBody only ever writes commas) must
// report format:"csv", delimiter:";", encoding:"utf-8". RED against the 501
// stub: status assertion fails (got 501, want 200).
func TestPreviewHandler_SemicolonDelimiter(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	raw := []byte("Invoice No;Issue Date;Buyer TIN\nINV-1;2026-06-03;TIN-1\n")
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", raw)
	rec, rawBody, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rawBody)
	}
	if resp.Format != "csv" {
		t.Errorf("format = %q, want %q", resp.Format, "csv")
	}
	if resp.Delimiter == nil || *resp.Delimiter != ";" {
		t.Errorf("delimiter = %v, want %q", resp.Delimiter, ";")
	}
	if resp.Encoding == nil || *resp.Encoding != "utf-8" {
		t.Errorf("encoding = %v, want %q", resp.Encoding, "utf-8")
	}
}

// --- PRV-04: xlsx -- delimiter/encoding both JSON null ----------------------

// TestPreviewHandler_XLSXNullDelimiterEncoding (PRV-04): a valid .xlsx
// upload must produce 200, format:"xlsx", and -- asserted against the RAW
// JSON body, not a struct round-trip -- delimiter and encoding both
// literally `null`, mirroring importResponse for the same reason (DecodeFacts
// leaves both "" for xlsx, and nilIfEmpty("") is nil). RED against the 501
// stub: status assertion fails (got 501, want 200).
func TestPreviewHandler_XLSXNullDelimiterEncoding(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Invoice No", "Issue Date"}
	rows := [][]string{{"INV-1", "2026-06-03"}}
	body, contentType := buildMultipartBody(t, "", "", "data.xlsx", xlsxContentType, xlsxBody(t, header, rows))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if resp.Format != "xlsx" {
		t.Errorf("format = %q, want %q", resp.Format, "xlsx")
	}
	if !bytes.Contains(raw, []byte(`"delimiter":null`)) {
		t.Errorf(`raw body must contain "delimiter":null for an xlsx upload, got %s`, raw)
	}
	if !bytes.Contains(raw, []byte(`"encoding":null`)) {
		t.Errorf(`raw body must contain "encoding":null for an xlsx upload, got %s`, raw)
	}
}

// --- PRV-05/06: sample_rows cap is a ceiling, not a floor --------------------

// TestPreviewHandler_SampleRowsCappedAtFive (PRV-05): a 9-data-row CSV must
// report rows_total:9 while sample_rows carries exactly maxSampleRows (5)
// entries. RED against the 501 stub: status assertion fails (got 501, want
// 200).
func TestPreviewHandler_SampleRowsCappedAtFive(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Invoice No"}
	var rows [][]string
	for i := 0; i < 9; i++ {
		rows = append(rows, []string{fmt.Sprintf("INV-%d", i)})
	}
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, header, rows))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if resp.RowsTotal != 9 {
		t.Errorf("rows_total = %d, want 9", resp.RowsTotal)
	}
	if len(resp.SampleRows) != maxSampleRows {
		t.Errorf("len(sample_rows) = %d, want %d", len(resp.SampleRows), maxSampleRows)
	}
}

// TestPreviewHandler_SampleRowsBelowCap (PRV-06): a 2-data-row CSV must
// report sample_rows with exactly 2 entries -- the cap is a ceiling, not a
// floor (no padding up to 5). RED against the 501 stub: status assertion
// fails (got 501, want 200).
func TestPreviewHandler_SampleRowsBelowCap(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Invoice No"}
	rows := [][]string{{"INV-1"}, {"INV-2"}}
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, header, rows))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if len(resp.SampleRows) != 2 {
		t.Errorf("len(sample_rows) = %d, want 2 (cap is a ceiling, not a floor)", len(resp.SampleRows))
	}
	if resp.RowsTotal != 2 {
		t.Errorf("rows_total = %d, want 2", resp.RowsTotal)
	}
}

// --- PRV-07: header-only CSV ---------------------------------------------------

// TestPreviewHandler_HeaderOnlyCSV (PRV-07): a CSV with a header row and no
// data rows must report a non-empty columns, an empty sample_rows, and
// rows_total:0. RED against the 501 stub: status assertion fails (got 501,
// want 200).
func TestPreviewHandler_HeaderOnlyCSV(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Invoice No", "Issue Date"}
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, header, nil))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if len(resp.Columns) == 0 {
		t.Error("columns must be non-empty for a header-only CSV")
	}
	if len(resp.SampleRows) != 0 {
		t.Errorf("sample_rows = %v, want empty", resp.SampleRows)
	}
	if resp.RowsTotal != 0 {
		t.Errorf("rows_total = %d, want 0", resp.RowsTotal)
	}
}

// --- PRV-08: empty CSV -- [] not null, plus the comma/utf-8 defaults --------

// TestPreviewHandler_EmptyCSVEmptyArraysNotNull (PRV-08): a zero-byte .csv
// file part must produce 200 whose RAW JSON body contains "columns":[] and
// "sample_rows":[], never null (a struct round-trip into []string cannot
// distinguish the two -- this must be a raw-body assertion). Decode's
// empty-input branch also defaults delimiter to "," and encoding to
// "utf-8" (sniffDelimiter's no-candidate-parses fallback, and
// utf8.Valid(nil) is true) -- NOT null, so those are asserted too. RED
// against the 501 stub: status assertion fails (got 501, want 200).
func TestPreviewHandler_EmptyCSVEmptyArraysNotNull(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", []byte{})
	rec, raw, _ := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"columns":[]`)) {
		t.Errorf(`raw body must contain "columns":[], not null, got %s`, raw)
	}
	if !bytes.Contains(raw, []byte(`"sample_rows":[]`)) {
		t.Errorf(`raw body must contain "sample_rows":[], not null, got %s`, raw)
	}
	if !bytes.Contains(raw, []byte(`"delimiter":","`)) {
		t.Errorf(`raw body must contain "delimiter":",", got %s`, raw)
	}
	if !bytes.Contains(raw, []byte(`"encoding":"utf-8"`)) {
		t.Errorf(`raw body must contain "encoding":"utf-8", got %s`, raw)
	}
}

// --- PRV-09: ragged row comes back unpadded ----------------------------------

// TestPreviewHandler_RaggedRowUnpadded (PRV-09): a data row shorter than the
// header (built as raw CSV bytes, not via csvBody's per-row writer, so the
// short row survives verbatim) must come back in sample_rows with its OWN
// length -- never padded out to the header's length. RED against the 501
// stub: status assertion fails (got 501, want 200).
func TestPreviewHandler_RaggedRowUnpadded(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	raw := []byte("Invoice No,Issue Date,Buyer TIN\nINV-1,2026-06-03\n")
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", raw)
	rec, rawBody, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rawBody)
	}
	if len(resp.SampleRows) != 1 {
		t.Fatalf("len(sample_rows) = %d, want 1", len(resp.SampleRows))
	}
	if got := len(resp.SampleRows[0]); got != 2 {
		t.Errorf("sample_rows[0] length = %d, want 2 (own length, unpadded, not the header's 3)", got)
	}
}

// --- PRV-10: oversized body WITH identity -> 413 -----------------------------

// TestPreviewHandler_OversizedBodyWithIdentity413 (PRV-10): a body over
// maxUploadBytes, with identity set this time, must 413 with the shared
// {"error":"..."} envelope carrying the exact message CreateHandler uses.
// RED against the 501 stub: status assertion fails (got 501, want 413).
func TestPreviewHandler_OversizedBodyWithIdentity413(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	oversized := bytes.Repeat([]byte("x"), maxUploadBytes+1024)
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", oversized)
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a body over the upload cap (body=%s)", rec.Code, raw)
	}
	if resp.Error != "request body exceeds the upload size limit" {
		t.Errorf("error = %q, want %q", resp.Error, "request body exceeds the upload size limit")
	}
}

// --- PRV-11: missing file part -----------------------------------------------

// TestPreviewHandler_MissingFilePart400 (PRV-11): a multipart body with no
// file part (buildMultipartBody with filename=="") must 400 "file is
// required". RED against the 501 stub: status assertion fails (got 501,
// want 400).
func TestPreviewHandler_MissingFilePart400(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	body, contentType := buildMultipartBody(t, "", "", "", "", nil)
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if resp.Error != "file is required" {
		t.Errorf("error = %q, want %q", resp.Error, "file is required")
	}
}

// --- PRV-12: unrecognized format ---------------------------------------------

// TestPreviewHandler_UnrecognizedFormat400 (PRV-12): a .txt filename with an
// explicit application/octet-stream Content-Type (set explicitly via
// buildMultipartBody's fileContentType arg, not left to CreateFormFile's
// default) must 400 "unrecognized file format". RED against the 501 stub:
// status assertion fails (got 501, want 400).
func TestPreviewHandler_UnrecognizedFormat400(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	body, contentType := buildMultipartBody(t, "", "", "data.txt", "application/octet-stream", []byte("whatever"))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if resp.Error != "unrecognized file format" {
		t.Errorf("error = %q, want %q", resp.Error, "unrecognized file format")
	}
}

// --- PRV-13: undecodable xlsx -------------------------------------------------

// TestPreviewHandler_UndecodableXLSX400 (PRV-13): an .xlsx filename whose
// content is not a valid zip archive must 400 "could not decode uploaded
// file". RED against the 501 stub: status assertion fails (got 501, want
// 400).
func TestPreviewHandler_UndecodableXLSX400(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	body, contentType := buildMultipartBody(t, "", "", "data.xlsx", xlsxContentType, []byte("not a zip file"))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if resp.Error != "could not decode uploaded file" {
		t.Errorf("error = %q, want %q", resp.Error, "could not decode uploaded file")
	}
}

// --- PRV-14: not multipart at all --------------------------------------------

// TestPreviewHandler_InvalidMultipart400 (PRV-14): a request declaring
// Content-Type: application/json with a JSON (non-multipart) body must 400
// "invalid multipart form". RED against the 501 stub: status assertion
// fails (got 501, want 400).
func TestPreviewHandler_InvalidMultipart400(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	rec, raw, resp := doPreviewRequest(t, &id, "application/json", bytes.NewReader([]byte(`{"not":"multipart"}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if resp.Error != "invalid multipart form" {
		t.Errorf("error = %q, want %q", resp.Error, "invalid multipart form")
	}
}

// --- PRV-15: duplicate + blank header names preserved verbatim --------------

// TestPreviewHandler_DuplicateBlankHeaderPreservedVerbatim (PRV-15): a
// header row with a duplicate name ("Total" twice) and a blank name must
// come back in columns verbatim, in file order, at its original length --
// no dedupe, no rename, no blank-name filtering. Guards M4-08-03/-04's
// mapping derivation against a preview that silently rewrites headers. RED
// against the 501 stub: status assertion fails (got 501, want 200).
func TestPreviewHandler_DuplicateBlankHeaderPreservedVerbatim(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Total", "Total", "", "Net"}
	rows := [][]string{{"10", "20", "x", "30"}}
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, header, rows))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !reflect.DeepEqual(resp.Columns, header) {
		t.Errorf("columns = %v, want %v verbatim (duplicate/blank names preserved, no dedupe/rename/filter)", resp.Columns, header)
	}
	if len(resp.Columns) != 4 {
		t.Errorf("len(columns) = %d, want 4", len(resp.Columns))
	}
}

// --- PRV-16: columns/rows_total must equal a direct Decode call -------------

// TestPreviewHandler_ColumnsRowsTotalMatchDirectDecode (PRV-16): for one CSV
// fixture, a direct in-test Decode(bytes.NewReader(fixture), "csv") call's
// header/rows must deep-equal the handler's columns and rows_total. This is
// the only spec that fails if someone forks a second parsing path instead
// of reusing Decode -- the exact divergence [column-source]/
// [preview-reuses-decode] exist to prevent, and it deliberately does NOT
// hardcode the expected header/count. RED against the 501 stub: status
// assertion fails (got 501, want 200).
func TestPreviewHandler_ColumnsRowsTotalMatchDirectDecode(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	fixture := csvBody(t, []string{"Invoice No", "Issue Date", "Buyer TIN"}, [][]string{
		{"INV-1", "2026-06-03", "TIN-1"},
		{"INV-2", "2026-06-04", "TIN-2"},
		{"INV-3", "2026-06-05", "TIN-3"},
	})
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", fixture)
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}

	wantHeader, wantRows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("direct Decode of the same fixture bytes: %v", err)
	}
	if !reflect.DeepEqual(resp.Columns, wantHeader) {
		t.Errorf("columns = %v, want %v (must equal a direct Decode call on the same bytes -- [preview-reuses-decode])", resp.Columns, wantHeader)
	}
	if resp.RowsTotal != len(wantRows) {
		t.Errorf("rows_total = %d, want %d (len of direct Decode's rows)", resp.RowsTotal, len(wantRows))
	}
}
