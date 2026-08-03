// sheet_handler_adversarial_test.go: QA coverage beyond the 16 red specs in
// sheet_handler_test.go -- boundary/edge fixtures, row numbering re-verified
// independently of S03, the nil-body-guard-position divergence's actual
// differentiating input, the Range-header seam, and the production route mount.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// nRowCSVFixture builds a CSV with exactly n data rows, for boundary tests
// either side of maxSheetRows.
func nRowCSVFixture(t *testing.T, n int) []byte {
	t.Helper()
	header := []string{"h1"}
	rows := make([][]string, n)
	for i := range rows {
		rows[i] = []string{fmt.Sprintf("r%d", i)}
	}
	return csvBody(t, header, rows)
}

// --- CSV edge shapes ----------------------------------------------------

func TestSheetHandler_TrailingBlankLineDropsNoRealRow(t *testing.T) {
	id := testIdentity()
	content := []byte("h1\nA\nB\n\n")
	open := newFakeDocOpen("x.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if resp.RowsTotal != 2 {
		t.Fatalf("rows_total = %d, want 2 -- the trailing blank line must not count as a row", resp.RowsTotal)
	}
	if len(resp.Rows) != 2 || resp.Rows[1][0] != "B" {
		t.Errorf("rows = %#v, want last real row to be B", resp.Rows)
	}
}

func TestSheetHandler_HeaderOnlyCSVHasZeroDataRows(t *testing.T) {
	id := testIdentity()
	content := []byte("h1,h2,h3\n")
	open := newFakeDocOpen("x.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"rows":[]`)) {
		t.Errorf("raw body = %s, want \"rows\":[] for a header with zero data rows", raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if resp.RowsTotal != 0 || resp.RowsReturned != 0 {
		t.Errorf("rows_total = %d, rows_returned = %d, want both 0", resp.RowsTotal, resp.RowsReturned)
	}
	if len(resp.Columns) != 3 {
		t.Errorf("columns = %v, want 3 header entries even with no data rows", resp.Columns)
	}
}

func TestSheetHandler_SingleColumnCSV(t *testing.T) {
	id := testIdentity()
	content := []byte("only\nA\nB\nC\n")
	open := newFakeDocOpen("x.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if len(resp.Columns) != 1 || resp.Columns[0] != "only" {
		t.Fatalf("columns = %v, want a single \"only\" entry", resp.Columns)
	}
	for i, row := range resp.Rows {
		if len(row) != 1 {
			t.Errorf("rows[%d] = %v, want exactly 1 cell", i, row)
		}
	}
}

// TestSheetHandler_RaggedRowHasMoreCellsThanHeader: Decode's CSV reader sets
// FieldsPerRecord = -1 (decode.go:98) precisely so a row like this is not an
// error; the story's "rows are verbatim" constraint requires every cell to
// survive, not just the ones the header has names for.
func TestSheetHandler_RaggedRowHasMoreCellsThanHeader(t *testing.T) {
	id := testIdentity()
	content := []byte("h1,h2\nA,B,C,D\n")
	open := newFakeDocOpen("x.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if len(resp.Rows) != 1 || len(resp.Rows[0]) != 4 {
		t.Fatalf("rows = %#v, want one row of 4 cells -- a ragged row must not lose cells", resp.Rows)
	}
	if !reflect.DeepEqual(resp.Rows[0], []string{"A", "B", "C", "D"}) {
		t.Errorf("rows[0] = %#v, want [A B C D] verbatim", resp.Rows[0])
	}
}

// --- the maxSheetRows boundary, both sides -------------------------------

func TestSheetHandler_ExactlyMaxSheetRowsIsNotTruncated(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("x.csv", "text/csv", nRowCSVFixture(t, maxSheetRows))

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if resp.Truncated {
		t.Error("truncated = true at exactly maxSheetRows, want false -- the cap is a ceiling, not a floor")
	}
	if resp.RowsTotal != maxSheetRows || resp.RowsReturned != maxSheetRows || len(resp.Rows) != maxSheetRows {
		t.Errorf("rows_total=%d rows_returned=%d len(rows)=%d, want all %d", resp.RowsTotal, resp.RowsReturned, len(resp.Rows), maxSheetRows)
	}
}

func TestSheetHandler_ExactlyMaxSheetRowsPlusOneIsTruncated(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("x.csv", "text/csv", nRowCSVFixture(t, maxSheetRows+1))

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if !resp.Truncated {
		t.Error("truncated = false at maxSheetRows+1, want true")
	}
	if resp.RowsTotal != maxSheetRows+1 {
		t.Errorf("rows_total = %d, want %d -- the true count survives truncation", resp.RowsTotal, maxSheetRows+1)
	}
	if resp.RowsReturned != maxSheetRows || len(resp.Rows) != maxSheetRows {
		t.Errorf("rows_returned=%d len(rows)=%d, want both %d", resp.RowsReturned, len(resp.Rows), maxSheetRows)
	}
}

// --- XLSX gap rows at both edges ------------------------------------------

// TestSheetHandler_XLSXGapRowAtStart: SetRowHeight with no cell value pulls an
// untouched row into excelize's dimension as a nil-row gap, at data index 0
// (immediately after the header) rather than the middle, which S05 covers.
func TestSheetHandler_XLSXGapRowAtStart(t *testing.T) {
	id := testIdentity()
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		mustSetCellValue(t, f, sheet, "A1", "Header")
		if err := f.SetRowHeight(sheet, 2, 15); err != nil {
			t.Fatalf("set row height: %v", err)
		}
		mustSetCellValue(t, f, sheet, "A3", "Row3")
	})
	open := newFakeDocOpen("gap-start.xlsx", xlsxContentType, fixture)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"rows":[[],["Row3"]]`)) {
		t.Errorf("raw body = %s, want the leading gap to serialize as [] at index 0, never null", raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if got := resp.Rows[1][0]; got != "Row3" {
		t.Errorf("rows[1][0] = %q, want %q at sheet row %d", got, "Row3", sheetRow(1))
	}
}

// TestSheetHandler_XLSXGapRowAtEnd: the gap is the LAST row the iterator
// returns -- the nil-coercion must hold there too, not just mid-sheet.
func TestSheetHandler_XLSXGapRowAtEnd(t *testing.T) {
	id := testIdentity()
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		mustSetCellValue(t, f, sheet, "A1", "Header")
		mustSetCellValue(t, f, sheet, "A2", "Row2")
		if err := f.SetRowHeight(sheet, 3, 15); err != nil {
			t.Fatalf("set row height: %v", err)
		}
	})
	open := newFakeDocOpen("gap-end.xlsx", xlsxContentType, fixture)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"rows":[["Row2"],[]]`)) {
		t.Errorf("raw body = %s, want the trailing gap to serialize as [] at the last index, never null", raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if resp.RowsTotal != 2 {
		t.Errorf("rows_total = %d, want 2", resp.RowsTotal)
	}
}

// --- NULL filename / NULL content type, both directions -------------------

func TestSheetHandler_NullFilenameFallsBackToDeclaredContentType(t *testing.T) {
	id := testIdentity()
	// newFakeDocOpen leaves Filename nil when filename == "".
	open := newFakeDocOpen("", "text/csv", csvBody(t, []string{"h"}, [][]string{{"v"}}))
	if open.doc.Filename != nil {
		t.Fatalf("test setup: doc.Filename = %v, want nil", open.doc.Filename)
	}

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a NULL filename resolved via content type (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if resp.Format != "csv" {
		t.Errorf("format = %q, want csv", resp.Format)
	}
}

func TestSheetHandler_NullDeclaredContentTypeFallsBackToFilename(t *testing.T) {
	id := testIdentity()
	// newFakeDocOpen leaves DeclaredContentType nil when contentType == "".
	open := newFakeDocOpen("x.csv", "", csvBody(t, []string{"h"}, [][]string{{"v"}}))
	if open.doc.DeclaredContentType != nil {
		t.Fatalf("test setup: doc.DeclaredContentType = %v, want nil", open.doc.DeclaredContentType)
	}

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a NULL content type resolved via extension (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if resp.Format != "csv" {
		t.Errorf("format = %q, want csv", resp.Format)
	}
}

// --- Range cannot reach the object store from this endpoint ---------------

// TestSheetHandler_RangeHeaderNeverReachesObjectStore: the previewer always
// wants the whole decodable file, never a byte slice of it, so open must
// always be called with "" regardless of what the inbound request carries.
func TestSheetHandler_RangeHeaderNeverReachesObjectStore(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("x.csv", "text/csv", csvBody(t, []string{"h"}, [][]string{{"v"}}))

	r := httptest.NewRequest(http.MethodGet, "/v1/documents/"+open.doc.ID+"/sheet", nil)
	r.SetPathValue("id", open.doc.ID)
	r.Header.Set("Range", "bytes=0-10")
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	SheetHandler(open.fn(), nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.Bytes())
	}
	if len(open.ranges) != 1 || open.ranges[0] != "" {
		t.Errorf("open received ranges %v, want exactly one call with \"\" -- a Range header must never reach the object store here", open.ranges)
	}
}

// --- row numbering, verified independently of S03 --------------------------

// TestSheetHandler_RowNumberingIndependentlyMatchesImporterSheetRows: a fixture
// different from S03's, combining a blank line AND a quoted multi-line cell,
// cross-checked against a direct Decode() and against sheetRows() -- the exact
// function service.go:415 calls for invoices.source_rows -- so the previewer
// and the stored evidence trail can't drift apart on the same bytes.
func TestSheetHandler_RowNumberingIndependentlyMatchesImporterSheetRows(t *testing.T) {
	id := testIdentity()
	// Row 1 header. Row 2 data (idx0). Row 3 blank (dropped by encoding/csv).
	// Rows 4-5 one quoted record spanning a physical newline (idx1). Row 6 data (idx2).
	content := []byte("h1,h2\nfirst,F2\n\n\"multi\nline\",X\nlast,L2\n")
	open := newFakeDocOpen("x.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)

	wantHeader, wantRows, _, err := Decode(bytes.NewReader(content), "csv")
	if err != nil {
		t.Fatalf("direct Decode: %v", err)
	}
	if len(wantRows) != 3 {
		t.Fatalf("test setup: direct Decode produced %d rows, want 3 -- the fixture doesn't exercise what this test claims", len(wantRows))
	}
	if !reflect.DeepEqual(resp.Columns, wantHeader) || !reflect.DeepEqual(resp.Rows, wantRows) {
		t.Fatalf("handler rows %#v != direct Decode rows %#v", resp.Rows, wantRows)
	}

	// Emulate the service's own row-group -> source_rows mapping (service.go:415)
	// for a group spanning all three data rows, over the SAME Decode() output.
	gotSourceRows := sheetRows([]int{0, 1, 2})
	wantSourceRows := []int{2, 3, 4}
	if !reflect.DeepEqual(gotSourceRows, wantSourceRows) {
		t.Fatalf("sheetRows([0,1,2]) = %v, want %v", gotSourceRows, wantSourceRows)
	}
	for i, want := range wantSourceRows {
		if got := sheetRow(i); got != want {
			t.Errorf("sheetRow(%d) = %d, want %d to match what the importer would write to invoices.source_rows", i, got, want)
		}
	}
	if resp.Rows[1][0] != "multi\nline" {
		t.Errorf("rows[1][0] = %q, want the quoted multi-line cell verbatim", resp.Rows[1][0])
	}
}

// --- the doubly-broken input the nil-body-guard-position divergence is about --

// TestSheetHandler_UnrecognizedFormatAndNilBodyIs500: the nil-body guard runs
// BEFORE detectFormat so a doubly-broken document (unrecognized format AND no
// bytes) answers 500, not 400. None of the 16 red specs exercise this: S12's
// filename IS recognized, so it 500s the same regardless of guard order
// (confirmed by reordering the guard and re-running S12 -- still green).
// This input is the guard-position divergence's actual differentiator.
func TestSheetHandler_UnrecognizedFormatAndNilBodyIs500(t *testing.T) {
	id := testIdentity()
	docID := "11111111-1111-1111-1111-111111111111"
	filename := "x.bin" // unrecognized: no matching extension or content type
	open := func(ctx context.Context, _, rangeHeader string) (document.Document, document.Object, error) {
		return document.Document{ID: docID, Filename: &filename}, document.Object{}, nil
	}

	rec, raw := doSheetRequest(t, open, &id, docID)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a doubly-broken input (unrecognized format AND nil body) (body=%s)", rec.Code, raw)
	}
	var resp sheetErrorBody
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode error response %s: %v", raw, err)
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

// --- the route survives in production -------------------------------------

// repoRootForImporter mirrors internal/document/handlers_adversarial_test.go's
// repoRoot -- duplicated rather than imported (it's an unexported test
// helper in a different package).
func repoRootForImporter(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func selectorNameForImporter(e ast.Expr) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name + "." + sel.Sel.Name
}

// TestSheetRoute_MountedInCmdInvoiceWithSheetHandler: nothing else pins that
// GET /v1/documents/{id}/sheet is wired in cmd/invoice/main.go -- the
// document package's own mainRoutePattern deliberately excludes it (4-segment
// class only), and the red specs call SheetHandler directly, never main.go.
func TestSheetRoute_MountedInCmdInvoiceWithSheetHandler(t *testing.T) {
	path := filepath.Join(repoRootForImporter(t), "cmd", "invoice", "main.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var pattern string
	var handler *ast.CallExpr
	var seen int
	ast.Inspect(f, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		seen++
		p := strings.Trim(lit.Value, `"`)
		if !strings.HasSuffix(p, "/v1/documents/{id}/sheet") {
			return true
		}
		if pattern != "" {
			t.Errorf("cmd/invoice/main.go registers more than one .../sheet route (%q and %q)", pattern, p)
		}
		pattern = p
		handler, _ = call.Args[1].(*ast.CallExpr)
		return true
	})

	if seen == 0 {
		t.Fatal("no HandleFunc call found in cmd/invoice/main.go -- the scan matched nothing, so every assertion below is vacuous")
	}
	if pattern == "" {
		t.Fatal("cmd/invoice/main.go registers no .../sheet route -- SheetHandler is unreachable in production")
	}
	if want := "GET /v1/documents/{id}/sheet"; pattern != want {
		t.Errorf("mounted pattern = %q, want %q", pattern, want)
	}
	if handler == nil {
		t.Fatalf("the %q handler argument is not a call expression", pattern)
	}
	if got := selectorNameForImporter(handler.Fun); got != "importer.SheetHandler" {
		t.Errorf("%q is served by %q, want importer.SheetHandler", pattern, got)
	}
	if len(handler.Args) == 0 || selectorNameForImporter(handler.Args[0]) == "" ||
		!strings.HasSuffix(selectorNameForImporter(handler.Args[0]), ".Open") {
		t.Errorf("SheetHandler's open argument is not an .Open method value; the route would not resolve a stored key")
	}
}
