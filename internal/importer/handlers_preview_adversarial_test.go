// M4-08-01 (task-170) QA pass: adversarial/edge/negative coverage added on
// top of the executor's PRV-01..PRV-16 suite (handlers_preview_test.go).
// These pin PreviewHandler contract behavior the Test Specs table didn't
// enumerate as its own row, but that a hostile or careless caller can hit in
// practice. Mirrors handlers_adversarial_test.go's idiom for CreateHandler:
// one targeted case per behavior, reusing handlers_preview_test.go's
// doPreviewRequest/previewBody and handlers_test.go's buildMultipartBody/
// csvBody/xlsxBody/xlsxContentType verbatim -- no duplicate helpers.
//
// PreviewHandler is stateless ([preview-stateless]), so -- like
// handlers_preview_test.go -- every test here is a plain httptest unit test:
// no dbTestPools, no TestRLS_* naming, no service container, no DSN.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- baseline: no identity + an ordinary (not oversized) body must also 401 -

// TestPreviewHandler_NoIdentityOrdinaryBody401: PRV-01 only proves ordering
// (no identity + an OVERSIZED body is 401, not 413). This confirms the
// simpler claim PRV-01 doesn't directly cover: a completely ordinary,
// well-formed, small CSV upload with no identity in context is ALSO 401 --
// the endpoint is genuinely auth-gated, not an unauthenticated door that
// merely happens to reject huge bodies first.
func TestPreviewHandler_NoIdentityOrdinaryBody401(t *testing.T) {
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, []string{"Invoice No"}, [][]string{{"INV-1"}}))
	rec, raw, resp := doPreviewRequest(t, nil, contentType, body)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an ordinary well-formed request with no identity (body=%s)", rec.Code, raw)
	}
	if resp.Error != "unauthorized" {
		t.Errorf("error = %q, want %q", resp.Error, "unauthorized")
	}
}

// --- method routing: only POST is registered -------------------------------

// TestPreviewHandler_WrongMethod405: the mux pattern is "POST
// /v1/imports/preview" (main.go). Go 1.22+'s enhanced ServeMux answers any
// other method against the same path with 405 Method Not Allowed
// automatically -- this must be exercised through an actual mux, since
// PreviewHandler() itself (an http.HandlerFunc) has no method check of its
// own; calling it directly bypasses the routing layer entirely.
func TestPreviewHandler_WrongMethod405(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/imports/preview", PreviewHandler())

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/v1/imports/preview", nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405 for %s /v1/imports/preview (body=%s)", rec.Code, method, rec.Body.String())
			}
		})
	}
}

// --- two "file" parts: FormFile takes the first, second is silently
//     ignored ----------------------------------------------------------------

// twoFilePartsBody builds a multipart body carrying TWO parts both named
// "file", for pinning net/http's documented FormFile behavior ("FormFile
// returns the first file for the provided form key") against a hostile or
// buggy client that duplicates the part name.
func twoFilePartsBody(t *testing.T, filename1 string, content1 []byte, filename2 string, content2 []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fw1, err := w.CreateFormFile("file", filename1)
	if err != nil {
		t.Fatalf("create first file part: %v", err)
	}
	if _, err := fw1.Write(content1); err != nil {
		t.Fatalf("write first file content: %v", err)
	}

	fw2, err := w.CreateFormFile("file", filename2)
	if err != nil {
		t.Fatalf("create second file part: %v", err)
	}
	if _, err := fw2.Write(content2); err != nil {
		t.Fatalf("write second file content: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// TestPreviewHandler_TwoFilePartsFirstWins: a multipart body with two parts
// both named "file" must preview the FIRST one -- r.FormFile("file")'s
// documented behavior -- not error, not merge, not silently prefer the last.
// Pinned so a client bug (accidentally appending "file" twice) has a known,
// tested outcome rather than an assumed one.
func TestPreviewHandler_TwoFilePartsFirstWins(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	first := csvBody(t, []string{"First Col"}, [][]string{{"F1"}})
	second := csvBody(t, []string{"Second Col"}, [][]string{{"S1"}})
	body, contentType := twoFilePartsBody(t, "first.csv", first, "second.csv", second)
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !reflect.DeepEqual(resp.Columns, []string{"First Col"}) {
		t.Errorf("columns = %v, want the FIRST file part's header %v (FormFile takes the first match)", resp.Columns, []string{"First Col"})
	}
}

// --- file part with an empty filename attribute -----------------------------

// emptyFilenamePartBody builds a multipart body whose "file" part carries a
// literal empty filename="" in its Content-Disposition header -- distinct
// from buildMultipartBody(t,...,"",...) (which omits the file part
// entirely, PRV-11's case). This exercises Go's own multipart/formdata.go
// readForm: a part is only added to Form.File when p.FileName() != "" --
// an explicit empty filename makes Go treat the part as a plain VALUE, not
// a file, so it never lands in Form.File["file"] at all.
func emptyFilenamePartBody(t *testing.T, content []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename=""`)
	h.Set("Content-Type", "text/csv")
	fw, err := w.CreatePart(h)
	if err != nil {
		t.Fatalf("create part with empty filename: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// TestPreviewHandler_EmptyFilenamePart400: a "file" part with an explicit
// empty filename="" attribute must 400 "file is required" -- Go's
// mime/multipart never surfaces such a part via r.FormFile (it is filed
// under Form.Value, not Form.File, since FileName() == ""), so this
// resolves identically to PRV-11's missing-part case, NOT a 200 with an
// empty-string filename echoed anywhere. Pinned as observed net/http
// behavior, load-bearing for any client that might send filename="" for a
// pasted/clipboard file.
func TestPreviewHandler_EmptyFilenamePart400(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	body, contentType := emptyFilenamePartBody(t, []byte("Invoice No\nINV-1\n"))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a file part with an empty filename attribute (body=%s)", rec.Code, raw)
	}
	if resp.Error != "file is required" {
		t.Errorf("error = %q, want %q", resp.Error, "file is required")
	}
}

// --- path traversal in the filename: name only, never touches the
//     filesystem -------------------------------------------------------------

// TestPreviewHandler_PathTraversalFilenameNeverTouchesFilesystem: a filename
// carrying a path traversal component ("../../etc/passwd.csv") must preview
// normally -- detectFormat only calls filepath.Ext on it (".csv" still
// resolves to the csv format) and neither PreviewHandler nor Decode ever
// calls os.Open/os.ReadFile/filepath.Join on the client-declared filename
// anywhere in this package -- it is metadata, never a filesystem path. The
// uploaded BYTES (not any file on disk) are what gets previewed.
func TestPreviewHandler_PathTraversalFilenameNeverTouchesFilesystem(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Invoice No"}
	rows := [][]string{{"INV-1"}}
	body, contentType := buildMultipartBody(t, "", "", "../../etc/passwd.csv", "", csvBody(t, header, rows))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a path-traversal filename with valid CSV bytes (body=%s)", rec.Code, raw)
	}
	if !reflect.DeepEqual(resp.Columns, header) {
		t.Errorf("columns = %v, want %v (uploaded bytes, not any path on disk)", resp.Columns, header)
	}
}

// --- header row present, every cell blank -----------------------------------

// TestPreviewHandler_AllBlankHeaderCells: a CSV whose header row parses to
// four fields that are all the empty string ("" x4, comma-separated) must
// still report columns of length 4 (all ""), not collapse/dedupe/filter them
// away -- consistent with PRV-15's "no filtering" guarantee, just pushed to
// the extreme where EVERY cell is blank rather than just one.
func TestPreviewHandler_AllBlankHeaderCells(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"", "", "", ""}
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, header, nil))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !reflect.DeepEqual(resp.Columns, header) {
		t.Errorf("columns = %v, want %v (all-blank header preserved, length 4)", resp.Columns, header)
	}
	if resp.RowsTotal != 0 {
		t.Errorf("rows_total = %d, want 0", resp.RowsTotal)
	}
}

// --- UTF-8 BOM: stripped before the header is read ---------------------------

// TestPreviewHandler_UTF8BOMStrippedFromFirstColumn: a CSV prefixed with a
// UTF-8 byte-order-mark must come back with columns[0] EXACTLY "Invoice No"
// -- no BOM bytes attached -- because decode.go's decodeCSV strips the BOM
// (bytes.TrimPrefix(raw, utf8BOM)) before ever handing bytes to csv.Reader.
// Pinned because M4-08-03/-04's mapping derivation will string-match against
// these column names, and a stray BOM on columns[0] would silently break
// every header match on that first column for any BOM-prefixed upload.
func TestPreviewHandler_UTF8BOMStrippedFromFirstColumn(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	bom := []byte{0xEF, 0xBB, 0xBF}
	raw := append(append([]byte{}, bom...), []byte("Invoice No,Issue Date\nINV-1,2026-06-03\n")...)
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", raw)
	rec, rawBody, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rawBody)
	}
	want := []string{"Invoice No", "Issue Date"}
	if !reflect.DeepEqual(resp.Columns, want) {
		t.Errorf("columns = %v, want %v (BOM must be stripped, not attached to columns[0])", resp.Columns, want)
	}
	if len(resp.Columns) > 0 && resp.Columns[0] != "Invoice No" {
		t.Errorf("columns[0] = %q, want exactly %q with no leading BOM bytes", resp.Columns[0], "Invoice No")
	}
	if resp.Encoding == nil || *resp.Encoding != "utf-8" {
		t.Errorf("encoding = %v, want %q", resp.Encoding, "utf-8")
	}
}

// --- CRLF line endings -------------------------------------------------------

// TestPreviewHandler_CRLFLineEndings: a CSV using \r\n line endings must
// parse identically to \n -- columns carry no stray trailing \r, and the one
// data row comes back with its own two verbatim fields.
func TestPreviewHandler_CRLFLineEndings(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	raw := []byte("Invoice No,Issue Date\r\nINV-1,2026-06-03\r\n")
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", raw)
	rec, rawBody, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rawBody)
	}
	want := []string{"Invoice No", "Issue Date"}
	if !reflect.DeepEqual(resp.Columns, want) {
		t.Errorf("columns = %v, want %v (no stray \\r on the last header cell)", resp.Columns, want)
	}
	if len(resp.SampleRows) != 1 {
		t.Fatalf("len(sample_rows) = %d, want 1", len(resp.SampleRows))
	}
	wantRow := []string{"INV-1", "2026-06-03"}
	if !reflect.DeepEqual(resp.SampleRows[0], wantRow) {
		t.Errorf("sample_rows[0] = %v, want %v (no stray \\r on the last data cell)", resp.SampleRows[0], wantRow)
	}
}

// --- very wide header (hundreds of columns) ----------------------------------

// TestPreviewHandler_VeryWideHeader: a 300-column CSV must preview
// successfully, with columns and the one sample row both carrying exactly
// 300 entries -- no silent truncation of the column axis (only sample_rows'
// ROW count is capped at maxSampleRows, never a row's own field count).
func TestPreviewHandler_VeryWideHeader(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	const width = 300
	header := make([]string, width)
	row := make([]string, width)
	for i := 0; i < width; i++ {
		header[i] = fmt.Sprintf("Col%d", i)
		row[i] = fmt.Sprintf("v%d", i)
	}
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, header, [][]string{row}))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if len(resp.Columns) != width {
		t.Errorf("len(columns) = %d, want %d", len(resp.Columns), width)
	}
	if len(resp.SampleRows) != 1 {
		t.Fatalf("len(sample_rows) = %d, want 1", len(resp.SampleRows))
	}
	if len(resp.SampleRows[0]) != width {
		t.Errorf("len(sample_rows[0]) = %d, want %d (no truncation of a row's own field count)", len(resp.SampleRows[0]), width)
	}
}

// --- extension vs. Content-Type disagreement: detectFormat prefers the
//     extension, in both directions ------------------------------------------

// TestPreviewHandler_ExtensionWinsOverContentType: detectFormat resolves
// format from the FILENAME extension first, falling back to Content-Type
// only when the extension is missing/unrecognized (handlers.go:116-137).
// Both directions of a mismatch must resolve by extension, not
// Content-Type: a ".csv" name carrying an xlsx Content-Type but real CSV
// bytes previews as csv; a ".xlsx" name carrying a "text/csv" Content-Type
// but real xlsx bytes previews as xlsx. Pinned because a browser or proxy
// can send an inaccurate Content-Type header while the filename stays
// truthful.
func TestPreviewHandler_ExtensionWinsOverContentType(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}

	t.Run(".csv name, xlsx content-type, real csv bytes", func(t *testing.T) {
		header := []string{"Invoice No"}
		body, contentType := buildMultipartBody(t, "", "", "data.csv", xlsxContentType, csvBody(t, header, [][]string{{"INV-1"}}))
		rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
		}
		if resp.Format != "csv" {
			t.Errorf("format = %q, want %q (extension wins over Content-Type)", resp.Format, "csv")
		}
		if !reflect.DeepEqual(resp.Columns, header) {
			t.Errorf("columns = %v, want %v", resp.Columns, header)
		}
	})

	t.Run(".xlsx name, text/csv content-type, real xlsx bytes", func(t *testing.T) {
		header := []string{"Invoice No"}
		body, contentType := buildMultipartBody(t, "", "", "data.xlsx", "text/csv", xlsxBody(t, header, [][]string{{"INV-1"}}))
		rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
		}
		if resp.Format != "xlsx" {
			t.Errorf("format = %q, want %q (extension wins over Content-Type)", resp.Format, "xlsx")
		}
		if resp.Delimiter != nil {
			t.Errorf("delimiter = %v, want nil for xlsx", resp.Delimiter)
		}
		if resp.Encoding != nil {
			t.Errorf("encoding = %v, want nil for xlsx", resp.Encoding)
		}
	})
}

// --- extra, unexpected multipart part alongside file (e.g. entity_id) -------

// TestPreviewHandler_ExtraUnexpectedPartIgnored: a well-formed request that
// also carries an entity_id field (a field CreateHandler reads but
// PreviewHandler never does) must still preview successfully -- the extra
// part is silently ignored, not rejected -- since M4-08-02's client may
// evolve to send it defensively/consistently with the create-import call.
func TestPreviewHandler_ExtraUnexpectedPartIgnored(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Invoice No"}
	body, contentType := buildMultipartBody(t, uuid.NewString(), "", "data.csv", "", csvBody(t, header, [][]string{{"INV-1"}}))
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a request carrying an extra entity_id part (body=%s)", rec.Code, raw)
	}
	if !reflect.DeepEqual(resp.Columns, header) {
		t.Errorf("columns = %v, want %v", resp.Columns, header)
	}
}

// --- anti-fork, strengthened: a header a naive split cannot replicate -------

// TestPreviewHandler_ColumnsMatchDirectDecode_QuotedEmbeddedComma
// (QA-hardened companion to PRV-16): QA mutation-tested PRV-16 by swapping
// PreviewHandler's columns derivation for a hand-rolled
// strings.Split(firstLine, ",") and found PRV-16 stayed GREEN -- every
// existing spec that asserts on `columns` uses a header with no characters
// that need CSV quoting, so a naive comma-split coincidentally reproduces
// csv.Reader's real output on those specific fixtures. [preview-reuses-decode]
// was, as a result, unenforced by the suite for exactly the class of
// second-parser bug it exists to catch.
//
// This test closes that gap: "Buyer, Ltd" is a header field containing a
// comma, which encoding/csv (and therefore Decode) quotes on write and
// un-quotes on read, but a naive split(",")-based reimplementation cannot
// tell from an ordinary field boundary -- it would produce 3 tokens
// (`"Buyer`, ` Ltd"`, `Total`) instead of the correct 2 (`Buyer, Ltd`,
// `Total`). Asserted BOTH against a hardcoded expected value (proves the
// real value is right, not just "equal to some other function's output")
// AND against a direct Decode call on the same bytes (mirrors PRV-16's own
// mechanism). Confirmed by mutation: this test goes red against the same
// hand-rolled-split mutant PRV-16 missed; reverted before commit.
func TestPreviewHandler_ColumnsMatchDirectDecode_QuotedEmbeddedComma(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	header := []string{"Buyer, Ltd", "Total"}
	rows := [][]string{{"Acme, Inc", "100"}}
	fixture := csvBody(t, header, rows)
	body, contentType := buildMultipartBody(t, "", "", "data.csv", "", fixture)
	rec, raw, resp := doPreviewRequest(t, &id, contentType, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !reflect.DeepEqual(resp.Columns, header) {
		t.Fatalf("columns = %v, want %v (a naive comma-split would wrongly produce 3 fields from the quoted \"Buyer, Ltd\" cell)", resp.Columns, header)
	}

	wantHeader, _, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("direct Decode of the same fixture bytes: %v", err)
	}
	if !reflect.DeepEqual(resp.Columns, wantHeader) {
		t.Errorf("columns = %v, want %v (must equal a direct Decode call on the same bytes -- [preview-reuses-decode])", resp.Columns, wantHeader)
	}
}

// --- concurrency: no shared mutable state -----------------------------------

// TestPreviewHandler_ConcurrentRequestsIsolated: PreviewHandler() returns a
// plain closure over no mutable state (no injected struct, no package-level
// var it writes to) -- N concurrent requests, each with distinct content,
// must never see another goroutine's columns/rows. Cheap insurance against a
// future edit accidentally introducing shared state (e.g. a reused buffer).
func TestPreviewHandler_ConcurrentRequestsIsolated(t *testing.T) {
	handler := PreviewHandler()
	const n = 20

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
			col := fmt.Sprintf("Col-%d", i)
			val := fmt.Sprintf("Val-%d", i)
			body, contentType := buildMultipartBody(t, "", "", "data.csv", "", csvBody(t, []string{col}, [][]string{{val}}))

			r := httptest.NewRequest("POST", "/v1/imports/preview", body)
			r.Header.Set("Content-Type", contentType)
			r = r.WithContext(auth.WithIdentity(context.Background(), id))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)

			if rec.Code != http.StatusOK {
				errs[i] = fmt.Errorf("goroutine %d: status = %d, want 200 (body=%s)", i, rec.Code, rec.Body.String())
				return
			}
			var resp previewBody
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				errs[i] = fmt.Errorf("goroutine %d: decode response: %v", i, err)
				return
			}
			if len(resp.Columns) != 1 || resp.Columns[0] != col {
				errs[i] = fmt.Errorf("goroutine %d: columns = %v, want [%s] (cross-goroutine data leak)", i, resp.Columns, col)
				return
			}
			if len(resp.SampleRows) != 1 || len(resp.SampleRows[0]) != 1 || resp.SampleRows[0][0] != val {
				errs[i] = fmt.Errorf("goroutine %d: sample_rows = %v, want [[%s]] (cross-goroutine data leak)", i, resp.SampleRows, val)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Error(err)
			_ = i
		}
	}
}
