// M4-03-02 (task-103): red AC tests for Decode's CSV/XLSX parsing and
// encoding/delimiter sniffing, authored BEFORE Decode is implemented
// (Stage 3 — decode.go currently a STUB returning zero values per
// [csv-sniff]/[xlsx-lib]). Pure Go, no DB, no t.Skip gate.
//
// Spec-to-test map (Test Specs table, M4-03-02 story / task-103):
//
//	IMP-DECODE-01 TestDecode_CommaCSVSplitsColumns
//	IMP-DECODE-02 TestDecode_SemicolonCSVSplitsColumns
//	IMP-DECODE-03 TestDecode_UTF8BOMStripped
//	IMP-DECODE-04 TestDecode_UTF16LEBOMDecodesSameAsUTF8Twin
//	IMP-DECODE-05 TestDecode_Windows1252NonUTF8Decodes
//	IMP-DECODE-06 TestDecode_RaggedRowNoError
//	IMP-DECODE-07 TestDecode_XLSXTwinMatchesCSVTwin
//	IMP-DECODE-08 TestDecode_XLSXDateCellFormattedNotSerial
//	IMP-DECODE-09 TestDecode_XLSXAccountingNumericCellKeepsSeparators
//
// RED (Stage 2, decode.go stub): Decode always returns (nil, nil,
// DecodeFacts{}, nil), so every content assertion below fails on VALUE
// (got nil/"" want the fixture's real content) -- the correct behavioral
// RED for this spec, not a compile or panic error. `go build ./...` still
// succeeds because the stub compiles.
//
// Run: go test ./internal/importer/ -run Decode -v
package importer

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// --- shared fixture content (IMP-DECODE-01 / -07 twins) -------------------

var csvTwinHeader = []string{"invoice_number", "total", "buyer_name"}

var csvTwinRows = [][]string{
	{"INV-1", "100.00", "Acme"},
	{"INV-2", "200.00", "Globex"},
}

// --- helpers ---------------------------------------------------------------

func assertHeader(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		// t.Fatalf, not t.Errorf: several callers pass a data ROW (e.g.
		// IMP-DECODE-08/09) and index straight into it (rows[0][1]) on the
		// very next line -- a soft Errorf would let a too-short mismatched
		// row fall through to that index and panic, masking the real
		// mismatch this assertion already caught.
		t.Fatalf("header = %#v, want %#v", got, want)
	}
}

func assertRows(t *testing.T, got, want [][]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %#v, want %#v", got, want)
	}
}

// buildXLSX creates an in-memory workbook via excelize, lets build populate
// "Sheet1" (excelize's default first-sheet name), and returns the encoded
// .xlsx bytes via WriteToBuffer -- the same in-test fixture-building idiom
// the story's Test Specs call for.
func buildXLSX(t *testing.T, build func(f *excelize.File, sheet string)) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("close excelize file: %v", err)
		}
	}()
	sheet := "Sheet1"
	build(f, sheet)
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer: %v", err)
	}
	return buf.Bytes()
}

// --- IMP-DECODE-01: comma CSV -----------------------------------------------

// IMP-DECODE-01: a comma CSV (header + 2 data rows) decodes with rows split
// on ",", facts.Delimiter=="," and facts.Format=="csv".
func TestDecode_CommaCSVSplitsColumns(t *testing.T) {
	fixture := []byte("invoice_number,total,buyer_name\nINV-1,100.00,Acme\nINV-2,200.00,Globex\n")

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertHeader(t, header, csvTwinHeader)
	assertRows(t, rows, csvTwinRows)
	if facts.Format != "csv" {
		t.Errorf("facts.Format = %q, want %q", facts.Format, "csv")
	}
	if facts.Delimiter != "," {
		t.Errorf("facts.Delimiter = %q, want %q", facts.Delimiter, ",")
	}
}

// --- IMP-DECODE-02: semicolon CSV -------------------------------------------

// IMP-DECODE-02: a ";"-delimited CSV decodes with rows split on ";" and
// facts.Delimiter==";".
func TestDecode_SemicolonCSVSplitsColumns(t *testing.T) {
	fixture := []byte("invoice_number;total;buyer_name\nINV-1;100.00;Acme\nINV-2;200.00;Globex\n")

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertHeader(t, header, csvTwinHeader)
	assertRows(t, rows, csvTwinRows)
	if facts.Delimiter != ";" {
		t.Errorf("facts.Delimiter = %q, want %q", facts.Delimiter, ";")
	}
}

// --- IMP-DECODE-03: UTF-8 BOM ------------------------------------------------

// IMP-DECODE-03: a UTF-8-BOM CSV (prefix bytes EF BB BF) has the BOM
// stripped from the first header cell and reports Encoding=="utf-8".
func TestDecode_UTF8BOMStripped(t *testing.T) {
	body := []byte("invoice_number,total\nINV-1,100.00\n")
	fixture := append([]byte{0xEF, 0xBB, 0xBF}, body...)

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantHeader := []string{"invoice_number", "total"}
	wantRows := [][]string{{"INV-1", "100.00"}}
	assertHeader(t, header, wantHeader)
	assertRows(t, rows, wantRows)

	if len(header) == 0 {
		t.Fatalf("header is empty, want at least 1 cell")
	}
	if strings.HasPrefix(header[0], "\ufeff") {
		t.Errorf("header[0] = %q, still carries a U+FEFF BOM prefix", header[0])
	}
	if facts.Encoding != "utf-8" {
		t.Errorf("facts.Encoding = %q, want %q", facts.Encoding, "utf-8")
	}
}

// --- IMP-DECODE-04: UTF-16 LE BOM --------------------------------------------

// IMP-DECODE-04: a UTF-16-LE-BOM CSV (prefix FF FE, body UTF-16LE-encoded)
// decodes to the same runes as its UTF-8 twin, with Encoding=="utf-16le".
// The fixture is built here via golang.org/x/text/encoding/unicode, encoding
// the UTF-8 twin's bytes as UTF-16LE with a leading BOM
// (unicode.UTF16(unicode.LittleEndian, unicode.UseBOM) writes the BOM).
func TestDecode_UTF16LEBOMDecodesSameAsUTF8Twin(t *testing.T) {
	utf8Twin := []byte("invoice_number,total\nINV-1,100.00\n")

	enc := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)
	utf16Fixture, err := enc.NewEncoder().Bytes(utf8Twin)
	if err != nil {
		t.Fatalf("encode UTF-16LE-BOM fixture: %v", err)
	}
	if !bytes.HasPrefix(utf16Fixture, []byte{0xFF, 0xFE}) {
		t.Fatalf("test fixture setup: encoded bytes do not start with a FF FE BOM, got % X", utf16Fixture[:2])
	}

	wantHeader := []string{"invoice_number", "total"}
	wantRows := [][]string{{"INV-1", "100.00"}}

	// The UTF-8 twin, decoded directly, must match the ground-truth literal
	// (guards against a vacuous nil==nil compare between the two decodes).
	twinHeader, twinRows, _, err := Decode(bytes.NewReader(utf8Twin), "csv")
	if err != nil {
		t.Fatalf("Decode(utf8Twin): %v", err)
	}
	assertHeader(t, twinHeader, wantHeader)
	assertRows(t, twinRows, wantRows)

	// The UTF-16LE-BOM fixture must decode to the identical content.
	header, rows, facts, err := Decode(bytes.NewReader(utf16Fixture), "csv")
	if err != nil {
		t.Fatalf("Decode(utf16Fixture): %v", err)
	}
	assertHeader(t, header, wantHeader)
	assertRows(t, rows, wantRows)
	if facts.Encoding != "utf-16le" {
		t.Errorf("facts.Encoding = %q, want %q", facts.Encoding, "utf-16le")
	}
}

// --- IMP-DECODE-05: Windows-1252 --------------------------------------------

// IMP-DECODE-05: a Windows-1252 (non-UTF-8) CSV containing byte 0xE9 (é) in
// a value decodes without error, that value decodes to "é", and
// facts.Encoding=="windows-1252".
func TestDecode_Windows1252NonUTF8Decodes(t *testing.T) {
	utf8Text := "invoice_number,buyer_name\nINV-1,Café\n"
	fixture, err := charmap.Windows1252.NewEncoder().Bytes([]byte(utf8Text))
	if err != nil {
		t.Fatalf("encode Windows-1252 fixture: %v", err)
	}
	// Confirm the fixture actually carries the raw 0xE9 byte the spec names
	// (Windows-1252 encodes U+00E9 'é' as the single byte 0xE9).
	if !bytes.Contains(fixture, []byte{0xE9}) {
		t.Fatalf("test fixture setup: encoded bytes do not contain 0xE9, got % X", fixture)
	}
	if bytes.Contains(fixture, []byte("é")) {
		t.Fatalf("test fixture setup: encoded bytes still contain UTF-8 'é' (2 bytes) -- fixture is not really Windows-1252")
	}

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v, want no error (Windows-1252 must never be rejected)", err)
	}
	wantHeader := []string{"invoice_number", "buyer_name"}
	wantRows := [][]string{{"INV-1", "Café"}}
	assertHeader(t, header, wantHeader)
	assertRows(t, rows, wantRows)
	if facts.Encoding != "windows-1252" {
		t.Errorf("facts.Encoding = %q, want %q", facts.Encoding, "windows-1252")
	}
}

// --- IMP-DECODE-06: ragged row -----------------------------------------------

// IMP-DECODE-06: a data row with fewer columns than the header decodes with
// NO error; the short row is returned as-is (not padded), so the service can
// quarantine it later.
func TestDecode_RaggedRowNoError(t *testing.T) {
	fixture := []byte("invoice_number,total,buyer_name\nINV-1,100.00,Acme\nINV-2,200.00\n")

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v, want no error (ragged rows are the service's problem, not decode's)", err)
	}
	if len(header) != 3 {
		t.Fatalf("len(header) = %d, want 3", len(header))
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}

	// Positive assertion on the full row, so this isn't just an absence check.
	assertHeader(t, rows[0], []string{"INV-1", "100.00", "Acme"})

	// The short row: fewer columns than the header, returned as-is (2 cells,
	// not padded to 3).
	wantShortRow := []string{"INV-2", "200.00"}
	assertHeader(t, rows[1], wantShortRow)
	if len(rows[1]) >= len(header) {
		t.Errorf("len(rows[1]) = %d, want < len(header) = %d (short row not padded)", len(rows[1]), len(header))
	}
}

// --- IMP-DECODE-07: XLSX twin of IMP-DECODE-01 -------------------------------

// IMP-DECODE-07: an XLSX twin of the IMP-DECODE-01 fixture (same logical
// cells, built via excelize) decodes to the same [][]string header+rows
// content; facts.Format=="xlsx", facts.Delimiter=="", facts.Encoding=="".
func TestDecode_XLSXTwinMatchesCSVTwin(t *testing.T) {
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		must := func(err error) {
			if err != nil {
				t.Fatalf("SetCellValue: %v", err)
			}
		}
		must(f.SetCellValue(sheet, "A1", csvTwinHeader[0]))
		must(f.SetCellValue(sheet, "B1", csvTwinHeader[1]))
		must(f.SetCellValue(sheet, "C1", csvTwinHeader[2]))
		must(f.SetCellValue(sheet, "A2", csvTwinRows[0][0]))
		must(f.SetCellValue(sheet, "B2", csvTwinRows[0][1]))
		must(f.SetCellValue(sheet, "C2", csvTwinRows[0][2]))
		must(f.SetCellValue(sheet, "A3", csvTwinRows[1][0]))
		must(f.SetCellValue(sheet, "B3", csvTwinRows[1][1]))
		must(f.SetCellValue(sheet, "C3", csvTwinRows[1][2]))
	})

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "xlsx")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertHeader(t, header, csvTwinHeader)
	assertRows(t, rows, csvTwinRows)
	if facts.Format != "xlsx" {
		t.Errorf("facts.Format = %q, want %q", facts.Format, "xlsx")
	}
	if facts.Delimiter != "" {
		t.Errorf("facts.Delimiter = %q, want \"\" (xlsx has no delimiter)", facts.Delimiter)
	}
	if facts.Encoding != "" {
		t.Errorf("facts.Encoding = %q, want \"\" (xlsx has no CSV encoding)", facts.Encoding)
	}
}

// --- IMP-DECODE-08: XLSX date cell -------------------------------------------

// IMP-DECODE-08: an XLSX with a date-formatted cell (a time.Time value with
// a date CustomNumFmt) decodes to the FORMATTED display string, not the
// numeric serial.
//
// Expected value verified empirically against excelize v2.11.0: a cell set
// via SetCellValue(sheet, cell, time.Date(2026, 1, 15, ...)) styled with
// CustomNumFmt "yyyy-mm-dd" round-trips (WriteToBuffer -> OpenReader ->
// Rows().Columns(), the same streaming API Decode's xlsx path uses) as
// exactly "2026-01-15" -- confirmed via a throwaway excelize probe, not
// guessed.
func TestDecode_XLSXDateCellFormattedNotSerial(t *testing.T) {
	dateFmt := "yyyy-mm-dd"
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		if err := f.SetCellValue(sheet, "A1", "invoice_number"); err != nil {
			t.Fatalf("SetCellValue header: %v", err)
		}
		if err := f.SetCellValue(sheet, "B1", "issue_date"); err != nil {
			t.Fatalf("SetCellValue header: %v", err)
		}
		if err := f.SetCellValue(sheet, "A2", "INV-1"); err != nil {
			t.Fatalf("SetCellValue: %v", err)
		}
		if err := f.SetCellValue(sheet, "B2", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("SetCellValue date: %v", err)
		}
		style, err := f.NewStyle(&excelize.Style{CustomNumFmt: &dateFmt})
		if err != nil {
			t.Fatalf("NewStyle: %v", err)
		}
		if err := f.SetCellStyle(sheet, "B2", "B2", style); err != nil {
			t.Fatalf("SetCellStyle: %v", err)
		}
	})

	header, rows, _, err := Decode(bytes.NewReader(fixture), "xlsx")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertHeader(t, header, []string{"invoice_number", "issue_date"})
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	wantRow := []string{"INV-1", "2026-01-15"}
	assertHeader(t, rows[0], wantRow)

	if rows[0][1] == "46022" || (len(rows[0]) > 1 && strings.TrimSpace(rows[0][1]) != "" && !strings.Contains(rows[0][1], "-")) {
		t.Errorf("issue_date cell = %q, looks like a numeric serial, not a formatted date string", rows[0][1])
	}
}

// --- IMP-DECODE-09: XLSX accounting-formatted numeric cell -------------------

// IMP-DECODE-09: an XLSX numeric cell 1058875.5 styled "#,##0.00" decodes to
// the FORMATTED display string "1,058,875.50" -- separators PRESENT.
// Normalization (stripping grouping commas) is deferred to the service
// ([numeric-normalization]); decode must not strip them.
//
// Expected value verified empirically against excelize v2.11.0: a float64
// cell styled with CustomNumFmt "#,##0.00" round-trips (WriteToBuffer ->
// OpenReader -> Rows().Columns()) as exactly "1,058,875.50".
func TestDecode_XLSXAccountingNumericCellKeepsSeparators(t *testing.T) {
	numFmt := "#,##0.00"
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		if err := f.SetCellValue(sheet, "A1", "invoice_number"); err != nil {
			t.Fatalf("SetCellValue header: %v", err)
		}
		if err := f.SetCellValue(sheet, "B1", "subtotal"); err != nil {
			t.Fatalf("SetCellValue header: %v", err)
		}
		if err := f.SetCellValue(sheet, "A2", "INV-1"); err != nil {
			t.Fatalf("SetCellValue: %v", err)
		}
		if err := f.SetCellValue(sheet, "B2", 1058875.5); err != nil {
			t.Fatalf("SetCellValue numeric: %v", err)
		}
		style, err := f.NewStyle(&excelize.Style{CustomNumFmt: &numFmt})
		if err != nil {
			t.Fatalf("NewStyle: %v", err)
		}
		if err := f.SetCellStyle(sheet, "B2", "B2", style); err != nil {
			t.Fatalf("SetCellStyle: %v", err)
		}
	})

	header, rows, _, err := Decode(bytes.NewReader(fixture), "xlsx")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertHeader(t, header, []string{"invoice_number", "subtotal"})
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	wantRow := []string{"INV-1", "1,058,875.50"}
	assertHeader(t, rows[0], wantRow)

	if !strings.Contains(rows[0][1], ",") {
		t.Errorf("subtotal cell = %q, wants grouping separators present (normalization is the service's job, not decode's)", rows[0][1])
	}
}
