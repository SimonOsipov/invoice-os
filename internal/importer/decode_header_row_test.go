package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
)

// xlsxOf writes one row per entry into column A onward; a nil entry leaves
// that row absent (an excelize gap row).
func xlsxOf(t *testing.T, rows [][]string) []byte {
	t.Helper()
	return buildXLSX(t, func(f *excelize.File, sheet string) {
		for r, row := range rows {
			if row == nil {
				continue
			}
			for c, val := range row {
				cell, err := excelize.CoordinatesToCellName(c+1, r+1)
				if err != nil {
					t.Fatalf("cell name: %v", err)
				}
				mustSetCellValue(t, f, sheet, cell, val)
			}
		}
	})
}

func TestDecodeFrom_RowOneEqualsDecode(t *testing.T) {
	raw := func(b string) func(t *testing.T) []byte {
		return func(t *testing.T) []byte { return []byte(b) }
	}

	cases := []struct {
		name       string
		format     string
		build      func(t *testing.T) []byte
		wantHeader []string
		wantRows   [][]string
		wantFormat string
		wantDelim  string
		wantEnc    string
	}{
		{
			name: "csv comma", format: "csv", build: raw("a,b\n1,2\n"),
			wantHeader: []string{"a", "b"}, wantRows: [][]string{{"1", "2"}},
			wantFormat: "csv", wantDelim: ",", wantEnc: "utf-8",
		},
		{
			name: "csv semicolon", format: "csv", build: raw("a;b\n1;2\n"),
			wantHeader: []string{"a", "b"}, wantRows: [][]string{{"1", "2"}},
			wantFormat: "csv", wantDelim: ";", wantEnc: "utf-8",
		},
		{
			name: "csv leading blank line", format: "csv", build: raw("\nh1,h2\nA,B\n"),
			wantHeader: []string{"h1", "h2"}, wantRows: [][]string{{"A", "B"}},
			wantFormat: "csv", wantDelim: ",", wantEnc: "utf-8",
		},
		{
			name: "csv blank line between data rows", format: "csv", build: raw("h1,h2\nA,A2\n\nB,B2\n"),
			wantHeader: []string{"h1", "h2"}, wantRows: [][]string{{"A", "A2"}, {"B", "B2"}},
			wantFormat: "csv", wantDelim: ",", wantEnc: "utf-8",
		},
		{
			name: "csv empty file", format: "csv", build: raw(""),
			wantHeader: nil, wantRows: nil,
			wantFormat: "csv", wantDelim: ",", wantEnc: "utf-8",
		},
		{
			name: "csv header-only file", format: "csv", build: raw("h1,h2\n"),
			wantHeader: []string{"h1", "h2"}, wantRows: [][]string{},
			wantFormat: "csv", wantDelim: ",", wantEnc: "utf-8",
		},
		{
			name: "xlsx gap row at start", format: "xlsx",
			build: func(t *testing.T) []byte {
				return buildXLSX(t, func(f *excelize.File, sheet string) {
					mustSetCellValue(t, f, sheet, "A1", "Header")
					if err := f.SetRowHeight(sheet, 2, 15); err != nil {
						t.Fatalf("set row height: %v", err)
					}
					mustSetCellValue(t, f, sheet, "A3", "Row3")
				})
			},
			wantHeader: []string{"Header"}, wantRows: [][]string{nil, {"Row3"}},
			wantFormat: "xlsx",
		},
		{
			name: "xlsx gap row at end", format: "xlsx",
			build: func(t *testing.T) []byte {
				return buildXLSX(t, func(f *excelize.File, sheet string) {
					mustSetCellValue(t, f, sheet, "A1", "Header")
					mustSetCellValue(t, f, sheet, "A2", "Row2")
					if err := f.SetRowHeight(sheet, 3, 15); err != nil {
						t.Fatalf("set row height: %v", err)
					}
				})
			},
			wantHeader: []string{"Header"}, wantRows: [][]string{{"Row2"}, nil},
			wantFormat: "xlsx",
		},
		{
			name: "xlsx header only, no data rows", format: "xlsx",
			build: func(t *testing.T) []byte {
				return buildXLSX(t, func(f *excelize.File, sheet string) {
					mustSetCellValue(t, f, sheet, "A1", "Header")
				})
			},
			wantHeader: []string{"Header"}, wantRows: nil,
			wantFormat: "xlsx",
		},
		{
			name: "xlsx empty workbook", format: "xlsx",
			build: func(t *testing.T) []byte {
				return buildXLSX(t, func(f *excelize.File, sheet string) {})
			},
			wantHeader: nil, wantRows: nil,
			wantFormat: "xlsx",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := c.build(t)
			header, rows, facts, err := DecodeFrom(bytes.NewReader(input), c.format, 1)
			if err != nil {
				t.Fatalf("DecodeFrom: %v", err)
			}
			assertHeader(t, header, c.wantHeader)
			assertRows(t, rows, c.wantRows)
			if c.wantRows == nil && rows != nil {
				t.Errorf("rows = %#v, want nil, not an empty non-nil slice", rows)
			}
			if facts.Format != c.wantFormat {
				t.Errorf("facts.Format = %q, want %q", facts.Format, c.wantFormat)
			}
			if facts.Delimiter != c.wantDelim {
				t.Errorf("facts.Delimiter = %q, want %q", facts.Delimiter, c.wantDelim)
			}
			if facts.Encoding != c.wantEnc {
				t.Errorf("facts.Encoding = %q, want %q", facts.Encoding, c.wantEnc)
			}
		})
	}
}

func TestDecodeFrom_CSVTitleRowsAboveTheHeader(t *testing.T) {
	fixture := []byte("Sales Register - March 2026\n\nInv No,Total\nINV-1,100\nINV-2,200\n")

	header, rows, facts, err := DecodeFrom(bytes.NewReader(fixture), "csv", 3)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	assertHeader(t, header, []string{"Inv No", "Total"})
	assertRows(t, rows, [][]string{{"INV-1", "100"}, {"INV-2", "200"}})
	if facts.Delimiter != "," {
		t.Errorf("facts.Delimiter = %q, want %q", facts.Delimiter, ",")
	}
	if facts.Encoding != "utf-8" {
		t.Errorf("facts.Encoding = %q, want %q", facts.Encoding, "utf-8")
	}
}

func TestDecodeFrom_SniffsTheDelimiterFromTheHeaderRow(t *testing.T) {
	fixture := []byte("Sales Register - March 2026\n\nInv No;Total;Currency\nINV-1;100;NGN\n")

	t.Run("N=3 sniffs the header row's delimiter", func(t *testing.T) {
		header, rows, facts, err := DecodeFrom(bytes.NewReader(fixture), "csv", 3)
		if err != nil {
			t.Fatalf("DecodeFrom: %v", err)
		}
		if facts.Delimiter != ";" {
			t.Errorf("facts.Delimiter = %q, want %q", facts.Delimiter, ";")
		}
		assertHeader(t, header, []string{"Inv No", "Total", "Currency"})
		assertRows(t, rows, [][]string{{"INV-1", "100", "NGN"}})
	})

	t.Run("N=1 control sniffs from the title line instead", func(t *testing.T) {
		header, rows, facts, err := DecodeFrom(bytes.NewReader(fixture), "csv", 1)
		if err != nil {
			t.Fatalf("DecodeFrom: %v", err)
		}
		if facts.Delimiter != "," {
			t.Errorf("facts.Delimiter = %q, want %q", facts.Delimiter, ",")
		}
		assertHeader(t, header, []string{"Sales Register - March 2026"})
		assertRows(t, rows, [][]string{{"Inv No;Total;Currency"}, {"INV-1;100;NGN"}})
	})
}

func TestDecodeFrom_BlankRowAboveTheHeaderIsTheSameRowInCSVAndXLSX(t *testing.T) {
	csvFixture := []byte("Title\n\nInv No,Total\nINV-1,100\n")
	xlsxFixture := xlsxOf(t, [][]string{
		{"Title"},
		nil,
		{"Inv No", "Total"},
		{"INV-1", "100"},
	})

	t.Run("N=3", func(t *testing.T) {
		csvHeader, csvRows, _, csvErr := DecodeFrom(bytes.NewReader(csvFixture), "csv", 3)
		xlsxHeader, xlsxRows, _, xlsxErr := DecodeFrom(bytes.NewReader(xlsxFixture), "xlsx", 3)
		if csvErr != nil {
			t.Fatalf("csv DecodeFrom: %v", csvErr)
		}
		if xlsxErr != nil {
			t.Fatalf("xlsx DecodeFrom: %v", xlsxErr)
		}
		assertHeader(t, csvHeader, []string{"Inv No", "Total"})
		assertRows(t, csvRows, [][]string{{"INV-1", "100"}})
		if !reflect.DeepEqual(csvHeader, xlsxHeader) {
			t.Errorf("xlsx header = %#v, want equal to csv header %#v", xlsxHeader, csvHeader)
		}
		if !reflect.DeepEqual(csvRows, xlsxRows) {
			t.Errorf("xlsx rows = %#v, want equal to csv rows %#v", xlsxRows, csvRows)
		}
	})

	t.Run("N=2 the blank row itself is the header", func(t *testing.T) {
		csvHeader, csvRows, _, csvErr := DecodeFrom(bytes.NewReader(csvFixture), "csv", 2)
		xlsxHeader, xlsxRows, _, xlsxErr := DecodeFrom(bytes.NewReader(xlsxFixture), "xlsx", 2)
		if csvErr != nil {
			t.Fatalf("csv DecodeFrom: %v", csvErr)
		}
		if xlsxErr != nil {
			t.Fatalf("xlsx DecodeFrom: %v", xlsxErr)
		}
		if csvHeader != nil {
			t.Errorf("csv header = %#v, want nil (blank row 2)", csvHeader)
		}
		assertRows(t, csvRows, [][]string{{"Inv No", "Total"}, {"INV-1", "100"}})
		if !reflect.DeepEqual(csvHeader, xlsxHeader) {
			t.Errorf("xlsx header = %#v, want equal to csv header %#v", xlsxHeader, csvHeader)
		}
		if !reflect.DeepEqual(csvRows, xlsxRows) {
			t.Errorf("xlsx rows = %#v, want equal to csv rows %#v", xlsxRows, csvRows)
		}
	})

	t.Run("CRLF blank row above the header", func(t *testing.T) {
		fixture := []byte("Title\r\n\r\nInv No,Total\r\n")
		header, rows, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 2)
		if err != nil {
			t.Fatalf("DecodeFrom: %v", err)
		}
		if header != nil {
			t.Errorf("header = %#v, want nil", header)
		}
		assertRows(t, rows, [][]string{{"Inv No", "Total"}})
	})
}

func TestDecodeFrom_TitleLinesAreNeverParsed(t *testing.T) {
	fixture := []byte("Sales \"Q1\" register\nInv No,Total\nINV-1,100\n")

	t.Run("N=1 the bare quote still fails today", func(t *testing.T) {
		_, _, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 1)
		if err == nil {
			t.Fatal("err = nil, want a bare-quote parse error")
		}
		if !errors.Is(err, csv.ErrBareQuote) {
			t.Errorf("err = %v, want errors.Is(err, csv.ErrBareQuote)", err)
		}
	})

	t.Run("N=2 the bad title line is never parsed", func(t *testing.T) {
		header, rows, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 2)
		if err != nil {
			t.Fatalf("DecodeFrom: %v", err)
		}
		assertHeader(t, header, []string{"Inv No", "Total"})
		assertRows(t, rows, [][]string{{"INV-1", "100"}})
	})
}

func TestDecodeFrom_HeaderRowPastTheEndIsRefused(t *testing.T) {
	t.Run("csv with a trailing newline", func(t *testing.T) {
		fixture := []byte("a,b\n1,2\n")
		_, _, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 3)
		if !errors.Is(err, ErrHeaderRowPastEnd) {
			t.Errorf("err = %v, want errors.Is(err, ErrHeaderRowPastEnd)", err)
		}

		header, rows, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 2)
		if err != nil {
			t.Fatalf("DecodeFrom: %v", err)
		}
		assertHeader(t, header, []string{"1", "2"})
		if len(rows) != 0 {
			t.Errorf("len(rows) = %d, want 0", len(rows))
		}
	})

	t.Run("csv with no trailing newline", func(t *testing.T) {
		fixture := []byte("a,b\n1,2")
		_, _, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 3)
		if !errors.Is(err, ErrHeaderRowPastEnd) {
			t.Errorf("err = %v, want errors.Is(err, ErrHeaderRowPastEnd)", err)
		}
	})

	t.Run("xlsx", func(t *testing.T) {
		fixture := xlsxOf(t, [][]string{{"a", "b"}, {"1", "2"}})
		_, _, _, err := DecodeFrom(bytes.NewReader(fixture), "xlsx", 3)
		if !errors.Is(err, ErrHeaderRowPastEnd) {
			t.Errorf("err = %v, want errors.Is(err, ErrHeaderRowPastEnd)", err)
		}

		header, rows, _, err := DecodeFrom(bytes.NewReader(fixture), "xlsx", 2)
		if err != nil {
			t.Fatalf("DecodeFrom: %v", err)
		}
		assertHeader(t, header, []string{"1", "2"})
		if rows != nil {
			t.Errorf("rows = %#v, want nil", rows)
		}
	})

	t.Run("empty csv", func(t *testing.T) {
		header, _, _, err := DecodeFrom(bytes.NewReader([]byte("")), "csv", 1)
		if err != nil {
			t.Fatalf("N=1 DecodeFrom: %v", err)
		}
		if header != nil {
			t.Errorf("N=1 header = %#v, want nil", header)
		}

		_, _, _, err = DecodeFrom(bytes.NewReader([]byte("")), "csv", 2)
		if !errors.Is(err, ErrHeaderRowPastEnd) {
			t.Errorf("N=2 err = %v, want errors.Is(err, ErrHeaderRowPastEnd)", err)
		}
	})

	t.Run("headerRow below 1 is refused, not ErrHeaderRowPastEnd, no panic", func(t *testing.T) {
		fixture := []byte("a\n")
		for _, n := range []int{0, -1} {
			_, _, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", n)
			if err == nil {
				t.Errorf("csv N=%d: err = nil, want a below-1 error", n)
			}
			if errors.Is(err, ErrHeaderRowPastEnd) {
				t.Errorf("csv N=%d: err = %v, want anything but ErrHeaderRowPastEnd", n, err)
			}
		}

		xlsxFixture := xlsxOf(t, [][]string{{"a"}})
		_, _, _, err := DecodeFrom(bytes.NewReader(xlsxFixture), "xlsx", 0)
		if err == nil {
			t.Error("xlsx N=0: err = nil, want a below-1 error")
		}
		if errors.Is(err, ErrHeaderRowPastEnd) {
			t.Errorf("xlsx N=0: err = %v, want anything but ErrHeaderRowPastEnd", err)
		}
	})
}

func TestDecodeFrom_CRLFTitleRows(t *testing.T) {
	fixture := []byte("Title\r\n\r\nInv No;Total\r\nINV-1;5\r\n")

	header, rows, facts, err := DecodeFrom(bytes.NewReader(fixture), "csv", 3)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	if facts.Delimiter != ";" {
		t.Errorf("facts.Delimiter = %q, want %q", facts.Delimiter, ";")
	}
	assertHeader(t, header, []string{"Inv No", "Total"})
	if header[1] != "Total" {
		t.Errorf("header[1] = %q, want %q with no trailing CR", header[1], "Total")
	}
	assertRows(t, rows, [][]string{{"INV-1", "5"}})
}

func TestDecodeFrom_RowsBelowTheHeaderKeepTodaysReading(t *testing.T) {
	t.Run("csv", func(t *testing.T) {
		fixture := []byte("T\n\nh1\nA\n\nB\n")
		header, rows, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 3)
		if err != nil {
			t.Fatalf("DecodeFrom: %v", err)
		}
		assertHeader(t, header, []string{"h1"})
		assertRows(t, rows, [][]string{{"A"}, {"B"}})
	})

	t.Run("xlsx", func(t *testing.T) {
		fixture := xlsxOf(t, [][]string{{"T"}, nil, {"h1"}, {"A"}, nil, {"B"}})
		header, rows, _, err := DecodeFrom(bytes.NewReader(fixture), "xlsx", 3)
		if err != nil {
			t.Fatalf("DecodeFrom: %v", err)
		}
		assertHeader(t, header, []string{"h1"})
		assertRows(t, rows, [][]string{{"A"}, nil, {"B"}})
	})
}

func TestDecodeFrom_KeepsCellsVerbatim(t *testing.T) {
	fixture := []byte("Title\n\nAmount,Date\n\" 1,000.00 \",07/03/2026\n")

	_, rows, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 3)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	assertRows(t, rows, [][]string{{" 1,000.00 ", "07/03/2026"}})
}

func TestDecodeFrom_WhitespaceOnlyRowIsAHeaderNotBlank(t *testing.T) {
	fixture := []byte("Title\n   \nA\n")

	header, rows, _, err := DecodeFrom(bytes.NewReader(fixture), "csv", 2)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	assertHeader(t, header, []string{"   "})
	assertRows(t, rows, [][]string{{"A"}})
}
