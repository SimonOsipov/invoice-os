package archive

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// writeThroughXLSXRows feeds records to xlsxRows on a fresh workbook. It returns the
// adapter, the workbook read back from its bytes, and the first write error.
func writeThroughXLSXRows(t *testing.T, records ...[]string) (*xlsxRows, *excelize.File, error) {
	t.Helper()
	f := excelize.NewFile()
	t.Cleanup(func() { _ = f.Close() })
	sw, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		t.Fatalf("NewStreamWriter: %v", err)
	}
	x := &xlsxRows{sw: sw}
	var werr error
	for _, rec := range records {
		if werr = x.Write(rec); werr != nil {
			break
		}
	}
	if err := sw.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := excelize.OpenReader(&buf)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = back.Close() })
	return x, back, werr
}

// rec39 is a 39-cell record (the width of invoicesCSVHeader) with number and one cell set.
func rec39(number string, col int, val string) []string {
	r := make([]string, len(invoicesCSVHeader))
	r[1] = number
	r[col] = val
	return r
}

func TestXLSXRows_WritesEveryCellAsAString(t *testing.T) {
	_, back, err := writeThroughXLSXRows(t, []string{"=1+1", "00123", ""})
	if err != nil {
		t.Fatalf("xlsxRows.Write: %v", err)
	}
	for cell, want := range map[string]string{"A1": "=1+1", "B1": "00123", "C1": ""} {
		got, err := back.GetCellValue("Sheet1", cell)
		if err != nil || got != want {
			t.Errorf("%s = %q (err %v), want %q", cell, got, err, want)
		}
		typ, err := back.GetCellType("Sheet1", cell)
		if err != nil {
			t.Fatalf("GetCellType %s: %v", cell, err)
		}
		if typ != excelize.CellTypeInlineString && typ != excelize.CellTypeSharedString {
			t.Errorf("%s cell type = %v, want a string type", cell, typ)
		}
	}
}

func TestXLSXRows_RefusesACellOverTheXLSXLimit(t *testing.T) {
	col := headerIndex(t, "rejection_reasons")
	// 32,766 ASCII units plus one astral rune: 32,767 runes but 32,768 UTF-16 units.
	long := strings.Repeat("a", excelize.TotalCellChars-1) + "\U0001F600"
	x, back, err := writeThroughXLSXRows(t, rec39("INV-LONG-01", col, long))

	var tooLong *CellTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("Write error = %v, want *CellTooLongError", err)
	}
	if tooLong.InvoiceNumber != "INV-LONG-01" || tooLong.Column != "rejection_reasons" {
		t.Errorf("CellTooLongError = %+v, want INV-LONG-01 / rejection_reasons", tooLong)
	}
	if x.row != 0 {
		t.Errorf("rows set = %d, want 0", x.row)
	}
	if v, _ := back.GetCellValue("Sheet1", "A1"); v != "" {
		t.Errorf("A1 = %q, want nothing written", v)
	}
}

func TestXLSXRows_WritesACellAtExactlyTheLimitWhole(t *testing.T) {
	col := headerIndex(t, "supplier_street")
	exact := strings.Repeat("a", excelize.TotalCellChars-2) + "\U0001F600"
	_, back, err := writeThroughXLSXRows(t, rec39("INV-EXACT-01", col, exact))
	if err != nil {
		t.Fatalf("Write(32,767 units): %v", err)
	}
	cell, _ := excelize.CoordinatesToCellName(col+1, 1)
	if got, _ := back.GetCellValue("Sheet1", cell); got != exact {
		t.Errorf("cell round-trip differs: len %d, want %d", len(got), len(exact))
	}
}

func TestXLSXRows_MarksAnXMLInvalidControlCharacter(t *testing.T) {
	_, back, err := writeThroughXLSXRows(t, []string{"a\x01b", "a\tb"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	for cell, want := range map[string]string{"A1": "a�b", "B1": "a\tb"} {
		if got, _ := back.GetCellValue("Sheet1", cell); got != want {
			t.Errorf("%s = %q, want %q", cell, got, want)
		}
	}
}

func registerRequest(from, to string) Request {
	f, _ := time.Parse(time.DateOnly, from)
	e, _ := time.Parse(time.DateOnly, to)
	return Request{EntityID: "0d1a1e5e-6b0c-4a53-9d0f-2f4e0f3c9a11", From: f, To: e}
}

func TestRegisterFilename_SlugsTheNameAndFormatsTheDates(t *testing.T) {
	got := registerFilename("Okafor & Partners Ltd.", registerRequest("2026-09-01", "2026-09-30"))
	want := "ASComply_invoice-register_Okafor-Partners-Ltd_20260901_20260930.xlsx"
	if got != want {
		t.Errorf("registerFilename = %q, want %q", got, want)
	}
}

func TestRegisterFilename_FallsBackToTheEntityID(t *testing.T) {
	r := registerRequest("2026-09-01", "2026-09-30")
	got := registerFilename("&&&", r)
	want := "ASComply_invoice-register_" + r.EntityID + "_20260901_20260930.xlsx"
	if got != want {
		t.Errorf("registerFilename = %q, want %q", got, want)
	}
}

func TestRegisterFilename_TruncatesALongSlug(t *testing.T) {
	got := registerFilename(strings.Repeat("a", 100), registerRequest("2026-09-01", "2026-09-30"))
	want := "ASComply_invoice-register_" + strings.Repeat("a", maxSlugBytes) + "_20260901_20260930.xlsx"
	if got != want {
		t.Errorf("registerFilename = %q, want %q", got, want)
	}
}
