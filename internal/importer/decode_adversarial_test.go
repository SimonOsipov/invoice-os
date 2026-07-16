// M4-03-02 (task-103) Stage 4 QA: adversarial decode-robustness coverage
// added post-implementation. The 9 IMP-DECODE-01..09 acceptance-criteria
// tests in decode_test.go cover the story's ACs; this file pins a focused
// set of edge cases they don't exercise: empty/header-only input, quoted
// delimiters, CRLF, UTF-16 BE (only LE is AC-tested), a delimiter-sniff
// misfire probe, and multi-sheet XLSX. Exhaustive malformed-row/oversized
// -input cataloguing is out of scope here -- that is M4-15's job. This
// suite only pins encoding correctness and "never panics".
package importer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/unicode"
)

// Header-only CSV (no data rows): header populated, rows empty, no error.
func TestDecode_HeaderOnlyCSVNoDataRows(t *testing.T) {
	fixture := []byte("invoice_number,total,buyer_name\n")

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v, want no error for a header-only CSV", err)
	}
	assertHeader(t, header, []string{"invoice_number", "total", "buyer_name"})
	if len(rows) != 0 {
		t.Errorf("len(rows) = %d, want 0 for a header-only CSV", len(rows))
	}
}

// Empty input (0 bytes) must not panic. Pins the actual current behavior:
// encoding/csv.ReadAll on a fully empty reader returns (nil, nil), so Decode
// returns an empty header, empty rows, and no error.
func TestDecode_EmptyInputNoPanic(t *testing.T) {
	header, rows, facts, err := Decode(bytes.NewReader(nil), "csv")
	if err != nil {
		t.Fatalf("Decode(empty input): %v, want no error", err)
	}
	if len(header) != 0 {
		t.Errorf("header = %#v, want empty for 0-byte input", header)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %#v, want empty for 0-byte input", rows)
	}
	if facts.Format != "csv" {
		t.Errorf("facts.Format = %q, want %q", facts.Format, "csv")
	}
}

// A quoted CSV field containing the delimiter: once "," is sniffed,
// encoding/csv's own quote handling must keep the embedded comma inside a
// single field rather than splitting it.
func TestDecode_QuotedFieldContainingDelimiterRespected(t *testing.T) {
	fixture := []byte("buyer_name,total,invoice_number\n" + `"Shoprite, Inc.",100.00,INV-1` + "\n")

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if facts.Delimiter != "," {
		t.Fatalf("facts.Delimiter = %q, want %q (test fixture setup)", facts.Delimiter, ",")
	}
	assertHeader(t, header, []string{"buyer_name", "total", "invoice_number"})
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	assertHeader(t, rows[0], []string{"Shoprite, Inc.", "100.00", "INV-1"})
}

// CRLF line endings: no stray \r left in any column of any row.
func TestDecode_CRLFLineEndingsNoStrayCR(t *testing.T) {
	fixture := []byte("invoice_number,total\r\nINV-1,100.00\r\nINV-2,200.00\r\n")

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertHeader(t, header, []string{"invoice_number", "total"})
	assertRows(t, rows, [][]string{{"INV-1", "100.00"}, {"INV-2", "200.00"}})
	for i, row := range rows {
		for j, cell := range row {
			if strings.ContainsRune(cell, '\r') {
				t.Errorf("rows[%d][%d] = %q, contains a stray \\r", i, j, cell)
			}
		}
	}
}

// UTF-16 BE BOM (FE FF): decode.go has a dedicated big-endian branch
// (utf16BEBOM case) but only the LE path is covered by IMP-DECODE-04. Pin
// BE explicitly since decode.go claims to handle it.
func TestDecode_UTF16BEBOMDecodesSameAsUTF8Twin(t *testing.T) {
	utf8Twin := []byte("invoice_number,total\nINV-1,100.00\n")

	enc := unicode.UTF16(unicode.BigEndian, unicode.UseBOM)
	utf16Fixture, err := enc.NewEncoder().Bytes(utf8Twin)
	if err != nil {
		t.Fatalf("encode UTF-16BE-BOM fixture: %v", err)
	}
	if !bytes.HasPrefix(utf16Fixture, []byte{0xFE, 0xFF}) {
		t.Fatalf("test fixture setup: encoded bytes do not start with a FE FF BOM, got % X", utf16Fixture[:2])
	}

	wantHeader := []string{"invoice_number", "total"}
	wantRows := [][]string{{"INV-1", "100.00"}}

	header, rows, facts, err := Decode(bytes.NewReader(utf16Fixture), "csv")
	if err != nil {
		t.Fatalf("Decode(utf16BEFixture): %v", err)
	}
	assertHeader(t, header, wantHeader)
	assertRows(t, rows, wantRows)
	if facts.Encoding != "utf-16be" {
		t.Errorf("facts.Encoding = %q, want %q", facts.Encoding, "utf-16be")
	}
}

// Delimiter-sniff misfire probe: a ";"-delimited file whose header ALSO
// contains a comma inside one column name must still sniff ";" (higher
// frequency: 2 semicolons vs. 1 embedded comma), not split on the comma.
func TestDecode_DelimiterSniffPicksSemicolonDespiteEmbeddedComma(t *testing.T) {
	fixture := []byte("Buyer, Ltd;total;invoice_number\nAcme Inc;100.00;INV-1\n")

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if facts.Delimiter != ";" {
		t.Fatalf("facts.Delimiter = %q, want %q -- sniff misfired on the embedded header comma", facts.Delimiter, ";")
	}
	assertHeader(t, header, []string{"Buyer, Ltd", "total", "invoice_number"})
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	assertHeader(t, rows[0], []string{"Acme Inc", "100.00", "INV-1"})
}

// XLSX with a second sheet: only the FIRST sheet is decoded. A firm's
// workbook may carry extra sheets (notes, a pivot); Decode must not merge or
// wander into them.
func TestDecode_XLSXOnlyFirstSheetDecoded(t *testing.T) {
	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatalf("close excelize file: %v", err)
		}
	}()
	sheet1 := "Sheet1"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("SetCellValue: %v", err)
		}
	}
	must(f.SetCellValue(sheet1, "A1", "invoice_number"))
	must(f.SetCellValue(sheet1, "A2", "INV-1"))

	if _, err := f.NewSheet("Sheet2"); err != nil {
		t.Fatalf("NewSheet: %v", err)
	}
	must(f.SetCellValue("Sheet2", "A1", "should_not_appear"))
	must(f.SetCellValue("Sheet2", "A2", "DECOY"))

	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer: %v", err)
	}

	header, rows, _, err := Decode(bytes.NewReader(buf.Bytes()), "xlsx")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	assertHeader(t, header, []string{"invoice_number"})
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	assertHeader(t, rows[0], []string{"INV-1"})
	for _, row := range rows {
		for _, cell := range row {
			if cell == "DECOY" {
				t.Errorf("row = %#v contains %q from Sheet2 -- Decode must only read the first sheet", row, "DECOY")
			}
		}
	}
}

// An unknown/unsupported format string (including empty) must return a
// clear error, never panic or silently succeed empty. The handler is
// expected to detect format upstream, but Decode must still be defensive.
func TestDecode_UnknownFormatReturnsError(t *testing.T) {
	for _, format := range []string{"", "pdf"} {
		header, rows, facts, err := Decode(bytes.NewReader([]byte("a,b\n1,2\n")), format)
		if err == nil {
			t.Errorf("Decode(format=%q): err = nil, want a clear error for an unsupported format", format)
		}
		if header != nil || rows != nil {
			t.Errorf("Decode(format=%q): header=%#v rows=%#v, want nil on error", format, header, rows)
		}
		if facts != (DecodeFacts{}) {
			t.Errorf("Decode(format=%q): facts = %#v, want the zero value on error", format, facts)
		}
	}
}
