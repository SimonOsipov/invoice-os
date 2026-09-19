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
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// hrCall is what a recording imp saw.
type hrCall struct {
	headerRow int
	header    []string
	rows      [][]string
	dryRun    bool
}

func hrRecordingImp(calls *[]hrCall) importFunc {
	return func(ctx context.Context, entityID, filename, documentID string, headerRow int, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		*calls = append(*calls, hrCall{headerRow, header, rows, dryRun})
		return BatchResult{}, nil
	}
}

// hrSheet serves GET /v1/documents/{id}/sheet with a raw query suffix.
func hrSheet(t *testing.T, open openSpec, docID, query string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/documents/"+docID+"/sheet"+query, nil)
	r.SetPathValue("id", docID)
	r = r.WithContext(auth.WithIdentity(r.Context(), testIdentity()))
	rec := httptest.NewRecorder()
	SheetHandler(open, nil).ServeHTTP(rec, r)
	return rec, rec.Body.Bytes()
}

// hrPreviewParts builds a preview body from raw parts, in order.
func hrPreviewParts(t *testing.T, parts ...importPart) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		var fw io.Writer
		var err error
		if p.filename != "" {
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.field, p.filename))
			h.Set("Content-Type", "text/csv")
			fw, err = w.CreatePart(h)
		} else {
			fw, err = w.CreateFormField(p.field)
		}
		if err != nil {
			t.Fatalf("create %s part: %v", p.field, err)
		}
		if _, err := fw.Write(p.content); err != nil {
			t.Fatalf("write %s: %v", p.field, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

func TestParseHeaderRow_EdgeInputs(t *testing.T) {
	accepted := map[string]int{
		"+3":                  3,
		"03":                  3,
		"0003":                3,
		"9223372036854775807": 9223372036854775807,
	}
	for raw, want := range accepted {
		got, err := parseHeaderRow(raw)
		if err != nil || got != want {
			t.Errorf("parseHeaderRow(%q) = %d, %v; want %d, nil", raw, got, err, want)
		}
	}

	refused := []string{" ", "\t", "\t3", "3\n", "３", "٣", "-0", "+0", "00", "0x3", "1e2", "3,", "9223372036854775808", "-9223372036854775809"}
	for _, raw := range refused {
		got, err := parseHeaderRow(raw)
		if err == nil || err.Error() != headerRowMalformed || got != 0 {
			t.Errorf("parseHeaderRow(%q) = %d, %v; want 0, %q", raw, got, err, headerRowMalformed)
		}
	}
}

// The same raw value must resolve to the same row on all three endpoints, or
// the preview and the import disagree about where the header is.
func TestHeaderRow_SignedAndZeroPaddedAgreeAcrossEndpoints(t *testing.T) {
	id := testIdentity()
	for _, v := range []string{"+3", "03"} {
		t.Run(v, func(t *testing.T) {
			store := newFakeDocStore()
			body, ct := previewForm(t, "r.csv", []byte(titleCSV), v)
			rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)
			if rec.Code != http.StatusOK || !reflect.DeepEqual(resp.Columns, []string{"Inv No", "Total"}) || resp.RowsTotal != 2 {
				t.Errorf("preview %q: %d %s, want 200 with row 3's columns", v, rec.Code, raw)
			}

			var calls []hrCall
			open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			ib, ict := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID,
				importPart{field: "header_row", content: []byte(v)})
			irec, iraw, _ := doImportUpload(t, hrRecordingImp(&calls), open.fn(), &id, "", ict, ib)
			if irec.Code != http.StatusCreated || len(calls) != 1 || calls[0].headerRow != 3 || !reflect.DeepEqual(calls[0].header, []string{"Inv No", "Total"}) {
				t.Errorf("import %q: %d %s calls=%+v, want 201 with imp at row 3", v, irec.Code, iraw, calls)
			}

			sopen := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			srec, sraw := hrSheet(t, sopen.fn(), sopen.doc.ID, "?header_row="+url.QueryEscape(v))
			if srec.Code != http.StatusOK {
				t.Fatalf("sheet %q: %d %s", v, srec.Code, sraw)
			}
			if s := mustUnmarshalSheet(t, sraw); !reflect.DeepEqual(s.Columns, []string{"Inv No", "Total"}) || s.RowsTotal != 2 {
				t.Errorf("sheet %q: %s, want row 3's columns", v, sraw)
			}
		})
	}
}

// No value below 1 or outside int reaches store, open, DecodeFrom or imp.
func TestHeaderRow_MalformedNeverReachesStoreOpenOrImport(t *testing.T) {
	id := testIdentity()
	want := `{"error":"header_row must be a whole number of 1 or more"}` + "\n"
	for _, v := range []string{"0", "-1", "-0", " ", "  3", "3 ", "３", "1.5", "99999999999999999999"} {
		t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
			store := newFakeDocStore()
			body, ct := previewForm(t, "r.csv", []byte(titleCSV), v)
			rec, raw, _ := doPreviewUpload(t, store.fn(), &id, ct, body)
			if rec.Code != http.StatusBadRequest || string(raw) != want || len(store.calls) != 0 {
				t.Errorf("preview: %d %s store=%d; want 400 %s, store 0", rec.Code, raw, len(store.calls), want)
			}

			var calls []hrCall
			open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			ib, ict := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID,
				importPart{field: "header_row", content: []byte(v)})
			irec, iraw, _ := doImportUpload(t, hrRecordingImp(&calls), open.fn(), &id, "?dry_run=true", ict, ib)
			if irec.Code != http.StatusBadRequest || string(iraw) != want || len(open.ids) != 0 || len(calls) != 0 {
				t.Errorf("import: %d %s open=%d imp=%d; want 400 %s, open 0, imp 0", irec.Code, iraw, len(open.ids), len(calls), want)
			}

			sopen := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			srec, sraw := hrSheet(t, sopen.fn(), sopen.doc.ID, "?header_row="+url.QueryEscape(v))
			if srec.Code != http.StatusBadRequest || string(sraw) != want || len(sopen.ids) != 0 {
				t.Errorf("sheet: %d %s open=%d; want 400 %s, open 0", srec.Code, sraw, len(sopen.ids), want)
			}
		})
	}
}

// A huge value that still parses is past-end, not a panic, on all three.
func TestHeaderRow_MaxIntIsPastEndOnEveryEndpoint(t *testing.T) {
	id := testIdentity()
	const v = "9223372036854775807"
	for _, fx := range []struct {
		name    string
		content []byte
	}{{"r.csv", []byte(titleCSV)}, {"r.xlsx", buildXLSX(t, func(f *excelize.File, sheet string) {
		mustSetCellValue(t, f, sheet, "A1", "H")
		mustSetCellValue(t, f, sheet, "A2", "v")
	})}} {
		t.Run(fx.name, func(t *testing.T) {
			store := newFakeDocStore()
			body, ct := previewForm(t, fx.name, fx.content, v)
			rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)
			if rec.Code != http.StatusBadRequest || resp.Error != headerRowPastEnd || resp.DocumentID != store.doc.ID {
				t.Errorf("preview: %d %s; want 400 past-end naming %s", rec.Code, raw, store.doc.ID)
			}

			var calls []hrCall
			open := newFakeDocOpen(fx.name, "", fx.content)
			ib, ict := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "H"}), open.doc.ID,
				importPart{field: "header_row", content: []byte(v)})
			irec, iraw, iresp := doImportUpload(t, hrRecordingImp(&calls), open.fn(), &id, "", ict, ib)
			if irec.Code != http.StatusBadRequest || iresp.Error != headerRowPastEnd || len(calls) != 0 {
				t.Errorf("import: %d %s imp=%d; want 400 past-end, imp 0", irec.Code, iraw, len(calls))
			}

			sopen := newFakeDocOpen(fx.name, "", fx.content)
			srec, sraw := hrSheet(t, sopen.fn(), sopen.doc.ID, "?header_row="+url.QueryEscape(v))
			if srec.Code != http.StatusBadRequest || mustUnmarshalSheetError(t, sraw) != headerRowPastEnd {
				t.Errorf("sheet: %d %s; want 400 past-end", srec.Code, sraw)
			}
		})
	}
}

// The past-end body is exactly the two keys, and the stored document is the
// one a later import with a valid header_row reads.
func TestPreviewHandler_PastEndDocumentIsImportableAtAValidRow(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	body, ct := previewForm(t, "r.csv", []byte(titleCSV), "6")
	rec, raw, _ := doPreviewUpload(t, store.fn(), &id, ct, body)
	want := `{"error":"header_row is past the last row of the file","document_id":"` + store.doc.ID + `"}` + "\n"
	if rec.Code != http.StatusBadRequest || string(raw) != want {
		t.Fatalf("preview = %d %s, want 400 %s", rec.Code, raw, want)
	}
	if len(store.calls) != 1 || !bytes.Equal(store.calls[0].body, []byte(titleCSV)) {
		t.Fatalf("store calls = %d, want 1 holding the uploaded bytes", len(store.calls))
	}

	open := newFakeDocOpen("r.csv", "", store.calls[0].body)
	open.doc.ID = store.doc.ID
	var calls []hrCall
	ib, ict := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), store.doc.ID,
		importPart{field: "header_row", content: []byte("3")})
	irec, iraw, _ := doImportUpload(t, hrRecordingImp(&calls), open.fn(), &id, "", ict, ib)
	if irec.Code != http.StatusCreated || len(open.ids) != 1 || open.ids[0] != store.doc.ID {
		t.Fatalf("import = %d %s open=%v, want 201 reading %s", irec.Code, iraw, open.ids, store.doc.ID)
	}
	if len(calls) != 1 || calls[0].headerRow != 3 || !reflect.DeepEqual(calls[0].rows, [][]string{{"INV-1", "100"}, {"INV-2", "200"}}) {
		t.Errorf("imp calls = %+v, want row 3 and its two data rows", calls)
	}
}

// Past-end is one past the last row; the last row itself is a header with no data.
func TestHeaderRow_LastRowIsAHeaderWithNoData(t *testing.T) {
	id := testIdentity()
	xlsx := buildXLSX(t, func(f *excelize.File, sheet string) {
		mustSetCellValue(t, f, sheet, "A1", "Title")
		mustSetCellValue(t, f, sheet, "A3", "Inv No")
	})
	for _, fx := range []struct {
		name    string
		content []byte
		last    string
		past    string
	}{{"r.csv", []byte(titleCSV), "5", "6"}, {"r.xlsx", xlsx, "3", "4"}} {
		t.Run(fx.name, func(t *testing.T) {
			var calls []hrCall
			open := newFakeDocOpen(fx.name, "", fx.content)
			ib, ict := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "x"}), open.doc.ID,
				importPart{field: "header_row", content: []byte(fx.last)})
			irec, iraw, _ := doImportUpload(t, hrRecordingImp(&calls), open.fn(), &id, "?dry_run=true", ict, ib)
			if irec.Code != http.StatusOK || len(calls) != 1 || len(calls[0].rows) != 0 || fmt.Sprint(calls[0].headerRow) != fx.last {
				t.Errorf("import at last row: %d %s calls=%+v, want 200 with no data rows", irec.Code, iraw, calls)
			}

			store := newFakeDocStore()
			body, ct := previewForm(t, fx.name, fx.content, fx.past)
			rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)
			if rec.Code != http.StatusBadRequest || resp.Error != headerRowPastEnd {
				t.Errorf("preview one past last: %d %s, want 400 past-end", rec.Code, raw)
			}
		})
	}
}

// rows_total counts every data row after N; sample_rows stays capped.
func TestPreviewHandler_HeaderRowRowsTotalCountsPastTheSampleCap(t *testing.T) {
	id := testIdentity()
	var b strings.Builder
	b.WriteString("Title\nSubtitle\nInv No\n")
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&b, "INV-%d\n", i)
	}
	store := newFakeDocStore()
	body, ct := previewForm(t, "r.csv", []byte(b.String()), "3")
	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, raw)
	}
	if resp.RowsTotal != 7 || len(resp.SampleRows) != maxSampleRows || resp.SampleRows[0][0] != "INV-1" {
		t.Errorf("rows_total=%d sample=%v, want 7 and the first %d data rows", resp.RowsTotal, resp.SampleRows, maxSampleRows)
	}
}

// Preview and import read header_row the same way, so a duplicated field or
// one sent in the query resolves to the same row on both.
func TestHeaderRow_TransportResolvesAlikeOnPreviewAndImport(t *testing.T) {
	id := testIdentity()
	mapping := mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"})
	cases := []struct {
		name  string
		query string
		parts []importPart
		want  int
	}{
		{"sent_twice_first_wins", "", []importPart{{field: "header_row", content: []byte("3")}, {field: "header_row", content: []byte("1")}}, 3},
		{"query_only", "?header_row=3", nil, 3},
		// net/http appends multipart values after the query's, so the query wins.
		{"query_beats_body", "?header_row=3", []importPart{{field: "header_row", content: []byte("1")}}, 3},
		{"empty_value_is_row_one", "", []importPart{{field: "header_row", content: []byte("")}}, 1},
		{"file_part_is_not_a_value", "", []importPart{{field: "header_row", filename: "h.txt", content: []byte("3")}}, 1},
	}
	wantCols := map[int][]string{1: {"Sales Register - March 2026"}, 3: {"Inv No", "Total"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFakeDocStore()
			parts := append([]importPart{{field: "file", filename: "r.csv", content: []byte(titleCSV)}}, c.parts...)
			body, ct := hrPreviewParts(t, parts...)
			r := httptest.NewRequest(http.MethodPost, "/v1/imports/preview"+c.query, body)
			r.Header.Set("Content-Type", ct)
			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			rec := httptest.NewRecorder()
			PreviewHandler(store.fn(), nil).ServeHTTP(rec, r)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"columns":%s`, mustJSON(t, wantCols[c.want]))) {
				t.Errorf("preview: %d %s, want row %d's columns", rec.Code, rec.Body.String(), c.want)
			}

			var calls []hrCall
			open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			ib, ict := buildImportForm(t, uuid.NewString(), mapping, open.doc.ID, c.parts...)
			irec, iraw, _ := doImportUpload(t, hrRecordingImp(&calls), open.fn(), &id, c.query, ict, ib)
			if irec.Code != http.StatusCreated || len(calls) != 1 || calls[0].headerRow != c.want || !reflect.DeepEqual(calls[0].header, wantCols[c.want]) {
				t.Errorf("import: %d %s calls=%+v, want imp at row %d", irec.Code, iraw, calls, c.want)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestImport_HeaderRowWithDryRunReachesImpAsADryRun(t *testing.T) {
	id := testIdentity()
	var calls []hrCall
	open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
	ib, ict := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID,
		importPart{field: "header_row", content: []byte("3")})
	rec, raw, _ := doImportUpload(t, hrRecordingImp(&calls), open.fn(), &id, "?dry_run=true", ict, ib)
	if rec.Code != http.StatusOK || len(calls) != 1 || !calls[0].dryRun || calls[0].headerRow != 3 || len(calls[0].rows) != 2 {
		t.Errorf("dry run: %d %s calls=%+v, want 200 with a dry-run imp at row 3", rec.Code, raw, calls)
	}
}

// Earlier refusals keep their messages when header_row is also bad; header_row
// is checked before document_id.
func TestImport_HeaderRowRefusalOrder(t *testing.T) {
	id := testIdentity()
	mapping := mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"})
	bad := importPart{field: "header_row", content: []byte("0")}
	cases := []struct {
		name  string
		docID string
		extra []importPart
		want  string
	}{
		{"retired_file_part_first", uuid.NewString(), []importPart{{field: "file", filename: "d.csv", content: []byte("a\n1\n")}, bad},
			"file is no longer accepted here: upload it to /v1/imports/preview and send the document_id it returns"},
		{"bad_remember_mapping_first", uuid.NewString(), []importPart{{field: "remember_mapping", content: []byte("yes")}, bad},
			"remember_mapping must be true or false"},
		{"before_missing_document_id", "", []importPart{bad}, headerRowMalformed},
		{"before_malformed_document_id", "not-a-uuid", []importPart{bad}, headerRowMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []hrCall
			open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			ib, ict := buildImportForm(t, uuid.NewString(), mapping, c.docID, c.extra...)
			rec, raw, resp := doImportUpload(t, hrRecordingImp(&calls), open.fn(), &id, "", ict, ib)
			if rec.Code != http.StatusBadRequest || resp.Error != c.want || len(open.ids) != 0 || len(calls) != 0 {
				t.Errorf("%d %s open=%d imp=%d, want 400 %q", rec.Code, raw, len(open.ids), len(calls), c.want)
			}
		})
	}
}

// "file is required" still wins over a bad header_row on preview.
func TestPreviewHandler_MissingFileBeatsABadHeaderRow(t *testing.T) {
	id := testIdentity()
	store := newFakeDocStore()
	body, ct := hrPreviewParts(t, importPart{field: "header_row", content: []byte("0")})
	rec, raw, resp := doPreviewUpload(t, store.fn(), &id, ct, body)
	if rec.Code != http.StatusBadRequest || resp.Error != "file is required" || len(store.calls) != 0 {
		t.Errorf("%d %s, want 400 file is required", rec.Code, raw)
	}
}

func TestSheetHandler_HeaderRowQueryEdges(t *testing.T) {
	t.Run("repeated_first_wins", func(t *testing.T) {
		open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
		rec, raw := hrSheet(t, open.fn(), open.doc.ID, "?header_row=3&header_row=1")
		if rec.Code != http.StatusOK || !reflect.DeepEqual(mustUnmarshalSheet(t, raw).Columns, []string{"Inv No", "Total"}) {
			t.Errorf("%d %s, want row 3's columns", rec.Code, raw)
		}
	})
	t.Run("empty_is_row_one", func(t *testing.T) {
		open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
		rec, raw := hrSheet(t, open.fn(), open.doc.ID, "?header_row=")
		if rec.Code != http.StatusOK || !reflect.DeepEqual(mustUnmarshalSheet(t, raw).Columns, []string{"Sales Register - March 2026"}) {
			t.Errorf("%d %s, want row 1's columns", rec.Code, raw)
		}
	})
	t.Run("pdf_keeps_its_refusal", func(t *testing.T) {
		for _, q := range []string{"", "?header_row=3", "?header_row=99"} {
			open := newFakeDocOpen("scan.pdf", "application/pdf", []byte("%PDF-1.4\n"))
			rec, raw := hrSheet(t, open.fn(), open.doc.ID, q)
			if rec.Code != http.StatusBadRequest || mustUnmarshalSheetError(t, raw) != "unrecognized file format" {
				t.Errorf("%q: %d %s, want 400 unrecognized file format", q, rec.Code, raw)
			}
		}
	})
	t.Run("cross_tenant_stays_404", func(t *testing.T) {
		for _, q := range []string{"", "?header_row=3", "?header_row=99"} {
			open := newFakeDocOpen("r.csv", "", []byte(titleCSV))
			open.err = document.ErrNotFound
			rec, raw := hrSheet(t, open.fn(), open.doc.ID, q)
			if rec.Code != http.StatusNotFound || mustUnmarshalSheetError(t, raw) != "not found" {
				t.Errorf("%q: %d %s, want 404 not found", q, rec.Code, raw)
			}
		}
	})
	t.Run("xlsx_gap_rows_are_arrays", func(t *testing.T) {
		xlsx := buildXLSX(t, func(f *excelize.File, sheet string) {
			mustSetCellValue(t, f, sheet, "A1", "Title")
			mustSetCellValue(t, f, sheet, "A3", "Inv No")
			mustSetCellValue(t, f, sheet, "A4", "INV-1")
			mustSetCellValue(t, f, sheet, "A6", "INV-2")
		})
		open := newFakeDocOpen("r.xlsx", "", xlsx)
		rec, raw := hrSheet(t, open.fn(), open.doc.ID, "?header_row=3")
		if rec.Code != http.StatusOK || !bytes.Contains(raw, []byte(`"columns":["Inv No"],"rows":[["INV-1"],[],["INV-2"]],"rows_total":3`)) {
			t.Errorf("%d %s, want row 3's columns and a [] gap row", rec.Code, raw)
		}
	})
}
