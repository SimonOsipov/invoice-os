// handlers_header_row_test.go: RED specs for header_row on preview, import
// and the sheet endpoint (T01-T12). Written against the parseHeaderRow stub;
// the handlers still ignore header_row.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const titleCSV = "Sales Register - March 2026\n\nInv No,Total\nINV-1,100\nINV-2,200\n"
const pastEndCSV = "a,b\n1,2\n"

// Local to this file: the two wire messages parseHeaderRow's real
// implementation will carry. Not the production consts (they do not exist
// yet at the stub stage) so this file will not collide once they land.
const (
	wantHeaderRowMalformedMsg = "header_row must be a whole number of 1 or more"
	wantHeaderRowPastEndMsg   = "header_row is past the last row of the file"
)

// previewForm builds a POST /v1/imports/preview multipart body: a "file"
// part plus, when headerRow != "", a header_row field.
func previewForm(t *testing.T, filename string, content []byte, headerRow string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if headerRow != "" {
		if err := w.WriteField("header_row", headerRow); err != nil {
			t.Fatalf("write header_row field: %v", err)
		}
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// mustUnmarshalSheetError decodes the shared {"error":"..."} envelope
// SheetHandler writes on a 400.
func mustUnmarshalSheetError(t *testing.T, raw []byte) string {
	t.Helper()
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	return resp.Error
}

func TestParseHeaderRow(t *testing.T) {
	okCases := []struct {
		raw  string
		want int
	}{
		{"", 1},
		{"1", 1},
		{"3", 3},
	}
	for _, c := range okCases {
		t.Run("ok_"+c.raw, func(t *testing.T) {
			got, err := parseHeaderRow(c.raw)
			if err != nil {
				t.Fatalf("parseHeaderRow(%q) error = %v, want nil", c.raw, err)
			}
			if got != c.want {
				t.Errorf("parseHeaderRow(%q) = %d, want %d", c.raw, got, c.want)
			}
		})
	}

	badCases := []string{"0", "-1", "abc", "1.5", " 3", "3 ", "99999999999999999999"}
	for _, raw := range badCases {
		t.Run("bad_"+raw, func(t *testing.T) {
			got, err := parseHeaderRow(raw)
			if err == nil {
				t.Fatalf("parseHeaderRow(%q) error = nil, want non-nil", raw)
			}
			if err.Error() != wantHeaderRowMalformedMsg {
				t.Errorf("parseHeaderRow(%q) error = %q, want %q", raw, err.Error(), wantHeaderRowMalformedMsg)
			}
			if got != 0 {
				t.Errorf("parseHeaderRow(%q) = %d, want 0", raw, got)
			}
		})
	}
}

func TestPreviewHandler_ReadsFromTheHeaderRow(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	body, ct := previewForm(t, "r.csv", []byte(titleCSV), "3")
	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !reflect.DeepEqual(resp.Columns, []string{"Inv No", "Total"}) {
		t.Errorf("columns = %v, want [Inv No Total]", resp.Columns)
	}
	wantRows := [][]string{{"INV-1", "100"}, {"INV-2", "200"}}
	if !reflect.DeepEqual(resp.SampleRows, wantRows) {
		t.Errorf("sample_rows = %v, want %v", resp.SampleRows, wantRows)
	}
	if resp.RowsTotal != 2 {
		t.Errorf("rows_total = %d, want 2", resp.RowsTotal)
	}
	if resp.Delimiter == nil || *resp.Delimiter != "," {
		t.Errorf("delimiter = %v, want \",\"", resp.Delimiter)
	}
}

func TestPreviewHandler_OmittedHeaderRowIsRowOne(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()

	body1, ct1 := previewForm(t, "r.csv", []byte(titleCSV), "")
	_, raw1, resp1 := doPreviewUpload(t, store.fn(), &id, ct1, body1)

	body2, ct2 := previewForm(t, "r.csv", []byte(titleCSV), "1")
	_, raw2, _ := doPreviewUpload(t, store.fn(), &id, ct2, body2)

	if !bytes.Equal(raw1, raw2) {
		t.Errorf("omitted body %s != header_row=1 body %s", raw1, raw2)
	}
	if !reflect.DeepEqual(resp1.Columns, []string{"Sales Register - March 2026"}) {
		t.Errorf("columns = %v, want [Sales Register - March 2026]", resp1.Columns)
	}
}

func TestPreviewHandler_SniffsTheHeaderRowsDelimiter(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	content := "Sales Register - March 2026\n\nInv No;Total;Currency\nINV-1;100;NGN\n"
	body, ct := previewForm(t, "r.csv", []byte(content), "3")
	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if resp.Delimiter == nil || *resp.Delimiter != ";" {
		t.Errorf("delimiter = %v, want \";\"", resp.Delimiter)
	}
	if !reflect.DeepEqual(resp.Columns, []string{"Inv No", "Total", "Currency"}) {
		t.Errorf("columns = %v, want [Inv No Total Currency]", resp.Columns)
	}
	wantRows := [][]string{{"INV-1", "100", "NGN"}}
	if !reflect.DeepEqual(resp.SampleRows, wantRows) {
		t.Errorf("sample_rows = %v, want %v", resp.SampleRows, wantRows)
	}
}

func TestPreviewHandler_MalformedHeaderRowIsRefusedBeforeStore(t *testing.T) {
	id := testIdentity()
	for _, v := range []string{"0", "-1", "abc"} {
		t.Run(v, func(t *testing.T) {
			store := newFakeDocStore()
			body, ct := previewForm(t, "r.csv", []byte(titleCSV), v)
			rec, raw, _ := doPreviewUpload(t, store.fn(), &id, ct, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
			}
			want := `{"error":"header_row must be a whole number of 1 or more"}` + "\n"
			if string(raw) != want {
				t.Errorf("raw body = %s, want %s", raw, want)
			}
			if len(store.calls) != 0 {
				t.Errorf("store calls = %d, want 0", len(store.calls))
			}
			if hasDocumentIDKey(raw) {
				t.Errorf("raw body = %s, must not carry a document_id key", raw)
			}
		})
	}
}

func TestPreviewHandler_PastTheEndNamesTheStoredDocument(t *testing.T) {
	id := testIdentity()

	t.Run("past_end", func(t *testing.T) {
		store := newFakeDocStore()
		body, ct := previewForm(t, "r.csv", []byte(pastEndCSV), "9")
		rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
		}
		if resp.Error != wantHeaderRowPastEndMsg {
			t.Errorf("error = %q, want %q", resp.Error, wantHeaderRowPastEndMsg)
		}
		if resp.DocumentID != store.doc.ID {
			t.Errorf("document_id = %q, want %q", resp.DocumentID, store.doc.ID)
		}
		if len(store.calls) != 1 {
			t.Errorf("store calls = %d, want 1", len(store.calls))
		}
	})

	t.Run("control_row_2", func(t *testing.T) {
		store := newFakeDocStore()
		body, ct := previewForm(t, "r.csv", []byte(pastEndCSV), "2")
		rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
		}
		if !reflect.DeepEqual(resp.Columns, []string{"1", "2"}) {
			t.Errorf("columns = %v, want [1 2]", resp.Columns)
		}
		if len(resp.SampleRows) != 0 {
			t.Errorf("sample_rows = %v, want []", resp.SampleRows)
		}
		if resp.RowsTotal != 0 {
			t.Errorf("rows_total = %d, want 0", resp.RowsTotal)
		}
	})
}

func TestPreviewHandler_XLSXGapRowIsAnEmptyArrayNotNull(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		mustSetCellValue(t, f, sheet, "A1", "Header")
		mustSetCellValue(t, f, sheet, "A2", "Row2")
		if err := f.SetRowHeight(sheet, 3, 15); err != nil {
			t.Fatalf("set row height: %v", err)
		}
		mustSetCellValue(t, f, sheet, "A4", "Row4")
	})

	body, ct := previewForm(t, "g.xlsx", fixture, "")
	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"sample_rows":[["Row2"],[],["Row4"]]`)) {
		t.Errorf("raw body = %s, want the gap row to serialize as [], never null", raw)
	}
	if bytes.Contains(raw, []byte("null]")) || bytes.Contains(raw, []byte(",null")) {
		t.Errorf("raw body = %s, must not contain a null element inside sample_rows", raw)
	}
	if resp.RowsTotal != 3 {
		t.Errorf("rows_total = %d, want 3", resp.RowsTotal)
	}
}

func TestPreviewHandler_XLSXTitleGapRowsAboveTheHeader(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		mustSetCellValue(t, f, sheet, "A1", "Title")
		if err := f.SetRowHeight(sheet, 2, 15); err != nil {
			t.Fatalf("set row height: %v", err)
		}
		mustSetCellValue(t, f, sheet, "A3", "Inv No")
		mustSetCellValue(t, f, sheet, "B3", "Total")
		mustSetCellValue(t, f, sheet, "A4", "INV-1")
		mustSetCellValue(t, f, sheet, "B4", "100")
		if err := f.SetRowHeight(sheet, 5, 15); err != nil {
			t.Fatalf("set row height: %v", err)
		}
		mustSetCellValue(t, f, sheet, "A6", "INV-2")
	})

	body, ct := previewForm(t, "g.xlsx", fixture, "3")
	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !reflect.DeepEqual(resp.Columns, []string{"Inv No", "Total"}) {
		t.Errorf("columns = %v, want [Inv No Total]", resp.Columns)
	}
	if !bytes.Contains(raw, []byte(`"sample_rows":[["INV-1","100"],[],["INV-2"]]`)) {
		t.Errorf("raw body = %s, want sample_rows [[INV-1 100] [] [INV-2]]", raw)
	}
	if resp.RowsTotal != 3 {
		t.Errorf("rows_total = %d, want 3", resp.RowsTotal)
	}
}

func TestImport_PassesTheHeaderRowAndItsColumns(t *testing.T) {
	id := testIdentity()
	mapping := mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"})

	type call struct {
		headerRow int
		header    []string
		rows      [][]string
	}
	var calls []call
	imp := func(ctx context.Context, entityID, filename, documentID string, headerRow int, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		calls = append(calls, call{headerRow, header, rows})
		return BatchResult{}, nil
	}

	open1 := newFakeDocOpen("r.csv", "", []byte(titleCSV))
	body1, ct1 := buildImportForm(t, uuid.NewString(), mapping, open1.doc.ID, importPart{field: "header_row", content: []byte("3")})
	rec1, raw1, _ := doImportUpload(t, imp, open1.fn(), &id, "", ct1, body1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("import 1 status = %d, want 201 (body=%s)", rec1.Code, raw1)
	}

	open2 := newFakeDocOpen("r.csv", "", []byte(titleCSV))
	body2, ct2 := buildImportForm(t, uuid.NewString(), mapping, open2.doc.ID)
	rec2, raw2, _ := doImportUpload(t, imp, open2.fn(), &id, "", ct2, body2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("import 2 status = %d, want 201 (body=%s)", rec2.Code, raw2)
	}

	if len(calls) != 2 {
		t.Fatalf("imp calls = %d, want 2", len(calls))
	}
	if calls[0].headerRow != 3 {
		t.Errorf("call 1 headerRow = %d, want 3", calls[0].headerRow)
	}
	wantHeader1 := []string{"Inv No", "Total"}
	if !reflect.DeepEqual(calls[0].header, wantHeader1) {
		t.Errorf("call 1 header = %v, want %v", calls[0].header, wantHeader1)
	}
	wantRows1 := [][]string{{"INV-1", "100"}, {"INV-2", "200"}}
	if !reflect.DeepEqual(calls[0].rows, wantRows1) {
		t.Errorf("call 1 rows = %v, want %v", calls[0].rows, wantRows1)
	}

	if calls[1].headerRow != 1 {
		t.Errorf("call 2 headerRow = %d, want 1", calls[1].headerRow)
	}
	wantHeader2 := []string{"Sales Register - March 2026"}
	if !reflect.DeepEqual(calls[1].header, wantHeader2) {
		t.Errorf("call 2 header = %v, want %v", calls[1].header, wantHeader2)
	}
}

func TestImport_BadHeaderRowNeverReachesImport(t *testing.T) {
	id := testIdentity()
	mapping := mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"})

	for _, v := range []string{"0", "-1", "abc"} {
		t.Run(v, func(t *testing.T) {
			open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			var impCalls int
			imp := func(ctx context.Context, entityID, filename, documentID string, headerRow int, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
				impCalls++
				return BatchResult{}, nil
			}
			body, ct := buildImportForm(t, uuid.NewString(), mapping, open.doc.ID, importPart{field: "header_row", content: []byte(v)})
			rec, raw, resp := doImportUpload(t, imp, open.fn(), &id, "", ct, body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
			}
			if resp.Error != wantHeaderRowMalformedMsg {
				t.Errorf("error = %q, want %q", resp.Error, wantHeaderRowMalformedMsg)
			}
			if len(open.ids) != 0 {
				t.Errorf("open calls = %d, want 0", len(open.ids))
			}
			if impCalls != 0 {
				t.Errorf("imp calls = %d, want 0", impCalls)
			}
		})
	}

	t.Run("9", func(t *testing.T) {
		open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
		var impCalls int
		imp := func(ctx context.Context, entityID, filename, documentID string, headerRow int, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
			impCalls++
			return BatchResult{}, nil
		}
		body, ct := buildImportForm(t, uuid.NewString(), mapping, open.doc.ID, importPart{field: "header_row", content: []byte("9")})
		rec, raw, resp := doImportUpload(t, imp, open.fn(), &id, "", ct, body)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
		}
		if resp.Error != wantHeaderRowPastEndMsg {
			t.Errorf("error = %q, want %q", resp.Error, wantHeaderRowPastEndMsg)
		}
		if len(open.ids) != 1 {
			t.Errorf("open calls = %d, want 1", len(open.ids))
		}
		if impCalls != 0 {
			t.Errorf("imp calls = %d, want 0", impCalls)
		}
	})
}

func TestSheetHandler_ReadsFromTheHeaderRowQuery(t *testing.T) {
	id := testIdentity()

	doRequest := func(t *testing.T, query string) (*httptest.ResponseRecorder, []byte) {
		t.Helper()
		open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
		r := httptest.NewRequest(http.MethodGet, "/v1/documents/"+open.doc.ID+"/sheet"+query, nil)
		r.SetPathValue("id", open.doc.ID)
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		rec := httptest.NewRecorder()
		SheetHandler(open.fn(), nil).ServeHTTP(rec, r)
		return rec, rec.Body.Bytes()
	}

	rec, raw := doRequest(t, "?header_row=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if !reflect.DeepEqual(resp.Columns, []string{"Inv No", "Total"}) {
		t.Errorf("columns = %v, want [Inv No Total]", resp.Columns)
	}
	wantRows := [][]string{{"INV-1", "100"}, {"INV-2", "200"}}
	if !reflect.DeepEqual(resp.Rows, wantRows) {
		t.Errorf("rows = %v, want %v", resp.Rows, wantRows)
	}
	if resp.RowsTotal != 2 {
		t.Errorf("rows_total = %d, want 2", resp.RowsTotal)
	}
	if resp.RowsReturned != 2 {
		t.Errorf("rows_returned = %d, want 2", resp.RowsReturned)
	}

	rec2, raw2 := doRequest(t, "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec2.Code, raw2)
	}
	resp2 := mustUnmarshalSheet(t, raw2)
	if !reflect.DeepEqual(resp2.Columns, []string{"Sales Register - March 2026"}) {
		t.Errorf("columns = %v, want [Sales Register - March 2026]", resp2.Columns)
	}
	if resp2.RowsTotal != 3 {
		t.Errorf("rows_total = %d, want 3", resp2.RowsTotal)
	}
}

func TestSheetHandler_BadHeaderRowIs400(t *testing.T) {
	id := testIdentity()

	for _, v := range []string{"0", "-1", "abc"} {
		t.Run(v, func(t *testing.T) {
			open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			r := httptest.NewRequest(http.MethodGet, "/v1/documents/"+open.doc.ID+"/sheet?header_row="+v, nil)
			r.SetPathValue("id", open.doc.ID)
			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			rec := httptest.NewRecorder()
			SheetHandler(open.fn(), nil).ServeHTTP(rec, r)

			raw := rec.Body.Bytes()
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
			}
			resp := mustUnmarshalSheetError(t, raw)
			if resp != wantHeaderRowMalformedMsg {
				t.Errorf("error = %q, want %q", resp, wantHeaderRowMalformedMsg)
			}
			if len(open.ids) != 0 {
				t.Errorf("open calls = %d, want 0", len(open.ids))
			}
		})
	}

	t.Run("9", func(t *testing.T) {
		open := newFakeDocOpen("r.csv", "", []byte(pastEndCSV))
		r := httptest.NewRequest(http.MethodGet, "/v1/documents/"+open.doc.ID+"/sheet?header_row=9", nil)
		r.SetPathValue("id", open.doc.ID)
		r = r.WithContext(auth.WithIdentity(r.Context(), id))
		rec := httptest.NewRecorder()
		SheetHandler(open.fn(), nil).ServeHTTP(rec, r)

		raw := rec.Body.Bytes()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
		}
		resp := mustUnmarshalSheetError(t, raw)
		if resp != wantHeaderRowPastEndMsg {
			t.Errorf("error = %q, want %q", resp, wantHeaderRowPastEndMsg)
		}
	})
}
