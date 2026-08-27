// sheet_handler_test.go: RED specs for GET /v1/documents/{id}/sheet, authored
// before SheetHandler exists. Every test fails to build (undefined
// SheetHandler / sheetResponse / maxSheetRows) until the handler lands.
//
// Reuses openSpec / fakeDocOpen / countingCloser (handlers_upload_once_test.go)
// and csvBody / xlsxBody / buildXLSX (handlers_test.go, decode_test.go) --
// no second parsing path, no new on-disk fixtures.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// sheetErrorBody is the shared {"error":"..."} envelope, decoded separately
// from sheetResponse (which carries no such field).
type sheetErrorBody struct {
	Error string `json:"error"`
}

func doSheetRequest(t *testing.T, open openSpec, id *auth.Identity, pathID string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/documents/"+pathID+"/sheet", nil)
	r.SetPathValue("id", pathID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	SheetHandler(open, nil).ServeHTTP(rec, r)
	return rec, rec.Body.Bytes()
}

func mustUnmarshalSheet(t *testing.T, raw []byte) sheetResponse {
	t.Helper()
	var resp sheetResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	return resp
}

func mustSetCellValue(t *testing.T, f *excelize.File, sheet, cell string, value any) {
	t.Helper()
	if err := f.SetCellValue(sheet, cell, value); err != nil {
		t.Fatalf("set cell %s: %v", cell, err)
	}
}

// oversizedCSVFixture builds a CSV with maxSheetRows+1 data rows, shared by
// the cap tests below.
func oversizedCSVFixture(t *testing.T) []byte {
	t.Helper()
	header := []string{"h1"}
	rows := make([][]string, maxSheetRows+1)
	for i := range rows {
		rows[i] = []string{fmt.Sprintf("r%d", i)}
	}
	return csvBody(t, header, rows)
}

// --- AC-1: stored CSV/XLSX decode server-side --------------------------------

func TestSheetHandler_DecodesStoredCSV(t *testing.T) {
	id := testIdentity()
	header := []string{"Inv No", "Date", "Buyer"}
	rows := make([][]string, 10)
	for i := range rows {
		rows[i] = []string{fmt.Sprintf("INV-%d", i), "2026-06-01", "Acme"}
	}
	open := newFakeDocOpen("x.csv", "text/csv", csvBody(t, header, rows))

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)

	if resp.Format != "csv" {
		t.Errorf("format = %q, want %q", resp.Format, "csv")
	}
	if resp.Delimiter == nil || *resp.Delimiter != "," {
		t.Errorf("delimiter = %v, want \",\"", resp.Delimiter)
	}
	if resp.Encoding == nil || *resp.Encoding != "utf-8" {
		t.Errorf("encoding = %v, want \"utf-8\"", resp.Encoding)
	}
	if resp.RowsTotal != 10 {
		t.Errorf("rows_total = %d, want 10", resp.RowsTotal)
	}
	if resp.RowsReturned != 10 {
		t.Errorf("rows_returned = %d, want 10", resp.RowsReturned)
	}
	if len(resp.Columns) != len(header) {
		t.Fatalf("columns = %v, want %d entries", resp.Columns, len(header))
	}
	for i, c := range resp.Columns {
		if c == "" {
			t.Errorf("columns[%d] is empty, want a non-empty column name", i)
		}
	}
	if len(resp.Rows) != 10 {
		t.Errorf("len(rows) = %d, want 10", len(resp.Rows))
	}
	if open.body.closes != 1 {
		t.Errorf("object body closed %d times, want 1", open.body.closes)
	}
}

// TestSheetHandler_MatchesDirectDecode: CSV on purpose -- xlsx would fail
// DeepEqual on the nil-row coercion, which the gap-row test covers instead.
func TestSheetHandler_MatchesDirectDecode(t *testing.T) {
	id := testIdentity()
	header := []string{"Inv No", "Total"}
	rows := [][]string{{"INV-1", "119.00"}, {"INV-2", "220.00"}, {"INV-3", "330.00"}}
	content := csvBody(t, header, rows)
	open := newFakeDocOpen("x.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if len(resp.Columns) == 0 || len(resp.Rows) == 0 {
		t.Fatalf("columns/rows empty, want non-empty before comparing against a direct Decode")
	}

	wantHeader, wantRows, _, err := Decode(bytes.NewReader(content), "csv")
	if err != nil {
		t.Fatalf("direct Decode: %v", err)
	}
	if !reflect.DeepEqual(resp.Columns, wantHeader) {
		t.Errorf("columns = %#v, want %#v", resp.Columns, wantHeader)
	}
	if !reflect.DeepEqual(resp.Rows, wantRows) {
		t.Errorf("rows = %#v, want %#v", resp.Rows, wantRows)
	}
}

// TestSheetHandler_RowNumberingMatchesImporterSheetRow: encoding/csv drops
// blank lines, so "B" (physical line 4) lands at data index 1 and "C"
// (physical line 7) at data index 2 -- skewed against physical line numbers
// in both cases. Asserted via sheetRow(i) itself, not a re-derived literal,
// so this tracks service.go's mapping if it ever changes.
func TestSheetHandler_RowNumberingMatchesImporterSheetRow(t *testing.T) {
	id := testIdentity()
	content := []byte("h1,h2\nA,A2\n\nB,B2\n\n\nC,C2\n")
	open := newFakeDocOpen("x.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)

	if len(resp.Rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3 -- the blank lines produced no records", len(resp.Rows))
	}
	if resp.Rows[1][0] != "B" {
		t.Errorf("rows[1][0] = %q, want %q", resp.Rows[1][0], "B")
	}
	if got := sheetRow(1); got != 3 {
		t.Errorf("sheetRow(1) = %d, want 3", got)
	}
	if resp.Rows[2][0] != "C" {
		t.Errorf("rows[2][0] = %q, want %q", resp.Rows[2][0], "C")
	}
	if got := sheetRow(2); got != 4 {
		t.Errorf("sheetRow(2) = %d, want 4", got)
	}
}

func TestSheetHandler_DecodesStoredXLSX(t *testing.T) {
	id := testIdentity()
	header := []string{"Inv No", "Total"}
	rows := [][]string{{"INV-1", "119.00"}, {"INV-2", "220.00"}}
	open := newFakeDocOpen("x.xlsx", xlsxContentType, xlsxBody(t, header, rows))

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"format":"xlsx"`)) {
		t.Errorf("raw body = %s, want format xlsx", raw)
	}
	if !bytes.Contains(raw, []byte(`"delimiter":null`)) {
		t.Errorf("raw body = %s, want delimiter:null for xlsx", raw)
	}
	if !bytes.Contains(raw, []byte(`"encoding":null`)) {
		t.Errorf("raw body = %s, want encoding:null for xlsx", raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if len(resp.Rows) == 0 {
		t.Fatalf("rows is empty, want the 2 data rows from the fixture")
	}
}

// TestSheetHandler_XLSXGapRowIsEmptyArrayNotNull: excelize materializes an
// untouched row as a nil []string; a nil []string inside [][]string marshals
// to a `null` element, which crashes any client doing row.map(...).
func TestSheetHandler_XLSXGapRowIsEmptyArrayNotNull(t *testing.T) {
	id := testIdentity()
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		mustSetCellValue(t, f, sheet, "A1", "Header")
		mustSetCellValue(t, f, sheet, "A2", "Row2")
		// Row 3 deliberately untouched -- excelize materializes it as a gap.
		mustSetCellValue(t, f, sheet, "A4", "Row4")
	})
	open := newFakeDocOpen("gap.xlsx", xlsxContentType, fixture)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"rows":[["Row2"],[],["Row4"]]`)) {
		t.Errorf("raw body = %s, want the gap row to serialize as [] inside rows, never null", raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if resp.RowsTotal != 3 {
		t.Fatalf("rows_total = %d, want 3 (header + 2 real rows + 1 gap)", resp.RowsTotal)
	}
	if got := resp.Rows[2][0]; got != "Row4" {
		t.Errorf("rows[2][0] = %q, want %q at sheet row %d", got, "Row4", sheetRow(2))
	}
}

// --- AC-3: the truncation window ---------------------------------------------

func TestSheetHandler_CapsAtMaxSheetRows(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("x.csv", "text/csv", oversizedCSVFixture(t))

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if !resp.Truncated {
		t.Error("truncated = false, want true")
	}
	if resp.RowsTotal != maxSheetRows+1 {
		t.Errorf("rows_total = %d, want %d", resp.RowsTotal, maxSheetRows+1)
	}
	if resp.RowsReturned != maxSheetRows {
		t.Errorf("rows_returned = %d, want %d", resp.RowsReturned, maxSheetRows)
	}
	if resp.RowsReturned != len(resp.Rows) {
		t.Errorf("rows_returned = %d, len(rows) = %d -- a handler must not report a count it never sent", resp.RowsReturned, len(resp.Rows))
	}
}

func TestSheetHandler_TruncationWindowIsTheFirstRows(t *testing.T) {
	id := testIdentity()
	content := oversizedCSVFixture(t)
	open := newFakeDocOpen("x.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)

	_, wantRows, _, err := Decode(bytes.NewReader(content), "csv")
	if err != nil {
		t.Fatalf("direct Decode: %v", err)
	}
	want := wantRows[:maxSheetRows]
	if !reflect.DeepEqual(resp.Rows, want) {
		t.Fatalf("rows != the first %d rows of a direct Decode", maxSheetRows)
	}
	if resp.Rows[0][0] != want[0][0] {
		t.Errorf("rows[0] = %v, want the first data row %v", resp.Rows[0], want[0])
	}
}

func TestSheetHandler_UnderCapIsNotTruncated(t *testing.T) {
	id := testIdentity()
	header := []string{"h1", "h2"}
	rows := make([][]string, 10)
	for i := range rows {
		rows[i] = []string{fmt.Sprintf("r%d-1", i), fmt.Sprintf("r%d-2", i)}
	}
	open := newFakeDocOpen("x.csv", "text/csv", csvBody(t, header, rows))

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	resp := mustUnmarshalSheet(t, raw)
	if resp.Truncated {
		t.Error("truncated = true, want false for 10 rows under the cap")
	}
	if resp.RowsTotal != 10 || resp.RowsReturned != 10 {
		t.Errorf("rows_total = %d, rows_returned = %d, want both 10", resp.RowsTotal, resp.RowsReturned)
	}
	if resp.RowsReturned != len(resp.Rows) {
		t.Errorf("rows_returned = %d, len(rows) = %d", resp.RowsReturned, len(resp.Rows))
	}
}

// --- AC-4: columns/rows always arrays, never null ----------------------------

// TestSheetHandler_EmptyFileReturnsArraysNotNull: raw bytes, not an
// unmarshalled struct -- json.Unmarshal collapses both null and [] into an
// empty slice, so a struct assertion here would be vacuous.
func TestSheetHandler_EmptyFileReturnsArraysNotNull(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("empty.csv", "text/csv", nil)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an empty csv (body=%s)", rec.Code, raw)
	}
	if !bytes.Contains(raw, []byte(`"columns":[]`)) {
		t.Errorf("raw body = %s, want \"columns\":[]", raw)
	}
	if !bytes.Contains(raw, []byte(`"rows":[]`)) {
		t.Errorf("raw body = %s, want \"rows\":[]", raw)
	}
}

// --- AC-5: auth / id validation / RLS / nil body -----------------------------

func TestSheetHandler_UnauthenticatedIs401(t *testing.T) {
	open := newFakeDocOpen("x.csv", "text/csv", csvBody(t, []string{"h"}, [][]string{{"v"}}))

	rec, raw := doSheetRequest(t, open.fn(), nil, uuid.NewString())

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, raw)
	}
	if len(open.ids) != 0 {
		t.Errorf("open calls = %d, want 0 -- unauthenticated must never reach open", len(open.ids))
	}
}

func TestSheetHandler_MalformedIDIs400(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("x.csv", "text/csv", csvBody(t, []string{"h"}, [][]string{{"v"}}))

	rec, raw := doSheetRequest(t, open.fn(), &id, "not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if len(open.ids) != 0 {
		t.Errorf("open calls = %d, want 0 -- a malformed id must never reach open", len(open.ids))
	}
}

// TestSheetHandler_NilObjectBodyIs500: a local closure, not the shared
// fakeDocOpen (which never returns a nil body). Filename is a recognized
// ".csv" on purpose, so this pins the nil-body guard, not the format branch.
func TestSheetHandler_NilObjectBodyIs500(t *testing.T) {
	id := testIdentity()
	docID := uuid.NewString()
	filename := "x.csv"
	open := func(ctx context.Context, _, rangeHeader string) (document.Document, document.Object, error) {
		return document.Document{ID: docID, Filename: &filename}, document.Object{}, nil
	}

	rec, raw := doSheetRequest(t, open, &id, docID)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, raw)
	}
	var resp sheetErrorBody
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode error response %s: %v", raw, err)
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

// TestSheet_SuspendedCallerIs403NotAServerError: db.ErrNotActiveMember from
// open must route through statusForErr, not the switch's default 500 arm.
func TestSheet_SuspendedCallerIs403NotAServerError(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", []byte("Inv No\nINV-1\n"))
	open.err = db.ErrNotActiveMember

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a caller the seam refuses (body=%s)", rec.Code, raw)
	}
	var resp sheetErrorBody
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode error response %s: %v", raw, err)
	}
	if resp.Error != db.NotActiveMemberMessage {
		t.Errorf("error = %q, want %q (body=%s)", resp.Error, db.NotActiveMemberMessage, raw)
	}
}

// TestRLS_SheetCrossTenantIs404AndIdenticalToUnknown: at the handler layer
// the only observable seam is `open` -- the object-store-never-fetched
// guarantee is already proven DB-backed at
// internal/document/store_adversarial_test.go:529
// (TestServiceOpen_CrossTenantRefusedBeforeAnyObjectFetch), cited here, not
// restaged against a stub this test wrote itself. The TestRLS_ prefix is
// kept for that reason despite this being a stub-level handler test, not a
// per-role-pool DB test.
func TestRLS_SheetCrossTenantIs404AndIdenticalToUnknown(t *testing.T) {
	id := testIdentity()
	newRefusingOpen := func(calls *[]string) openSpec {
		return func(ctx context.Context, docID, rangeHeader string) (document.Document, document.Object, error) {
			*calls = append(*calls, docID)
			return document.Document{}, document.Object{}, document.ErrNotFound
		}
	}

	crossTenantID := uuid.NewString()
	var crossCalls []string
	recCross, rawCross := doSheetRequest(t, newRefusingOpen(&crossCalls), &id, crossTenantID)

	unknownID := uuid.NewString()
	var unknownCalls []string
	recUnknown, rawUnknown := doSheetRequest(t, newRefusingOpen(&unknownCalls), &id, unknownID)

	if recCross.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant status = %d, want 404 (body=%s)", recCross.Code, rawCross)
	}
	if recUnknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404 (body=%s)", recUnknown.Code, rawUnknown)
	}
	if !bytes.Equal(rawCross, rawUnknown) {
		t.Errorf("cross-tenant body %s != unknown body %s, want byte-equal -- no existence oracle", rawCross, rawUnknown)
	}
	if len(crossCalls) != 1 || crossCalls[0] != crossTenantID {
		t.Errorf("open called with %v, want exactly one call carrying %q", crossCalls, crossTenantID)
	}
	if len(unknownCalls) != 1 || unknownCalls[0] != unknownID {
		t.Errorf("open called with %v, want exactly one call carrying %q", unknownCalls, unknownID)
	}
}

// --- AC-6: unrecognized format / undecodable bytes ---------------------------

func TestSheetHandler_UnrecognizedFormatIs400(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("x.bin", "", []byte("whatever bytes"))

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	var resp sheetErrorBody
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode error response %s: %v", raw, err)
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
}

func TestSheetHandler_UndecodableIs400(t *testing.T) {
	id := testIdentity()
	content := bytes.Repeat([]byte{0x00, 0x01}, 32)
	open := newFakeDocOpen("corrupt.csv", "text/csv", content)

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a control-byte-gated file (body=%s)", rec.Code, raw)
	}
}

func TestSheetHandler_CorruptXLSXIs400(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("corrupt.xlsx", xlsxContentType, []byte("not a zip file at all"))

	rec, raw := doSheetRequest(t, open.fn(), &id, open.doc.ID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-zip xlsx (body=%s)", rec.Code, raw)
	}
}
