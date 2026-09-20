package importer

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"golang.org/x/text/encoding/unicode"
)

func TestDecodeFrom_BOMThenTitleRows(t *testing.T) {
	fixture := append([]byte{0xEF, 0xBB, 0xBF}, "Title\nInv No;Total\nINV-1;5\n"...)

	header, rows, facts, err := DecodeFrom(bytes.NewReader(fixture), "csv", 2)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	assertHeader(t, header, []string{"Inv No", "Total"})
	assertRows(t, rows, [][]string{{"INV-1", "5"}})
	if facts.Delimiter != ";" || facts.Encoding != "utf-8" {
		t.Errorf("facts = %+v, want Delimiter ; Encoding utf-8", facts)
	}
}

func TestDecodeFrom_UTF16TitleRowsCountDecodedLines(t *testing.T) {
	enc := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)
	fixture, err := enc.NewEncoder().Bytes([]byte("Title\r\n\r\nInv No\tTotal\r\nINV-1\t5\r\n"))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	header, rows, facts, err := DecodeFrom(bytes.NewReader(fixture), "csv", 3)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	assertHeader(t, header, []string{"Inv No", "Total"})
	assertRows(t, rows, [][]string{{"INV-1", "5"}})
	if facts.Delimiter != "\t" || facts.Encoding != "utf-16le" {
		t.Errorf("facts = %+v, want Delimiter \\t Encoding utf-16le", facts)
	}
}

func TestDecodeFrom_Windows1252TitleRow(t *testing.T) {
	fixture := []byte("Caf\xe9 register\nInv No,Caf\xe9\nINV-1,5\n")

	header, rows, facts, err := DecodeFrom(bytes.NewReader(fixture), "csv", 2)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	assertHeader(t, header, []string{"Inv No", "Café"})
	assertRows(t, rows, [][]string{{"INV-1", "5"}})
	if facts.Encoding != "windows-1252" {
		t.Errorf("facts.Encoding = %q, want windows-1252", facts.Encoding)
	}
}

func TestDecodeFrom_HeaderOnTheLastLine(t *testing.T) {
	for _, fixture := range []string{"Title\nh1,h2\n", "Title\nh1,h2"} {
		header, rows, _, err := DecodeFrom(strings.NewReader(fixture), "csv", 2)
		if err != nil {
			t.Fatalf("%q N=2: %v", fixture, err)
		}
		assertHeader(t, header, []string{"h1", "h2"})
		if len(rows) != 0 {
			t.Errorf("%q N=2: rows = %#v, want none", fixture, rows)
		}

		_, _, _, err = DecodeFrom(strings.NewReader(fixture), "csv", 3)
		if err != ErrHeaderRowPastEnd {
			t.Errorf("%q N=3: err = %v, want the unwrapped ErrHeaderRowPastEnd", fixture, err)
		}
	}
}

func TestDecodeFrom_BlankLastLineIsAnEmptyHeader(t *testing.T) {
	header, rows, _, err := DecodeFrom(strings.NewReader("Title\n\n"), "csv", 2)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	if header != nil || len(rows) != 0 {
		t.Errorf("header = %#v rows = %#v, want nil header and no rows", header, rows)
	}
}

// encoding/csv does not split on a lone \r either, so a classic-Mac file is one line.
func TestDecodeFrom_CROnlyFileIsOneLine(t *testing.T) {
	fixture := "Title\rInv No,Total\rINV-1,5\r"

	if _, _, _, err := DecodeFrom(strings.NewReader(fixture), "csv", 1); err != nil {
		t.Errorf("N=1: err = %v, want nil (today's reading)", err)
	}
	if _, _, _, err := DecodeFrom(strings.NewReader(fixture), "csv", 2); err != ErrHeaderRowPastEnd {
		t.Errorf("N=2: err = %v, want ErrHeaderRowPastEnd", err)
	}
}

func TestDecodeFrom_ExtremeHeaderRows(t *testing.T) {
	csvFixture := []byte("a\nb\n")
	xlsxFixture := xlsxOf(t, [][]string{{"a"}, {"b"}})

	for _, tc := range []struct {
		format  string
		fixture []byte
	}{{"csv", csvFixture}, {"xlsx", xlsxFixture}} {
		if _, _, _, err := DecodeFrom(bytes.NewReader(tc.fixture), tc.format, math.MaxInt); err != ErrHeaderRowPastEnd {
			t.Errorf("%s MaxInt: err = %v, want ErrHeaderRowPastEnd", tc.format, err)
		}
		_, _, _, err := DecodeFrom(bytes.NewReader(tc.fixture), tc.format, math.MinInt)
		if err == nil || err == ErrHeaderRowPastEnd {
			t.Errorf("%s MinInt: err = %v, want the below-1 error", tc.format, err)
		}
	}
}

func TestDecodeFrom_BelowOneIsCheckedBeforeTheFormat(t *testing.T) {
	_, _, _, err := DecodeFrom(strings.NewReader("a\n"), "tsv", 0)
	if err == nil || !strings.Contains(err.Error(), "below 1") {
		t.Errorf("err = %v, want the below-1 error", err)
	}
	_, _, _, err = DecodeFrom(strings.NewReader("a\n"), "tsv", 2)
	if err == nil || !strings.Contains(err.Error(), `unsupported format "tsv"`) {
		t.Errorf("err = %v, want the unsupported-format error", err)
	}
}

func TestDecodeFrom_LeadingGapRowsMatchLeadingBlankLines(t *testing.T) {
	csvHeader, csvRows, _, err := DecodeFrom(strings.NewReader("\n\nInv No,Total\nINV-1,5\n"), "csv", 3)
	if err != nil {
		t.Fatalf("csv: %v", err)
	}
	xlsxHeader, xlsxRows, _, err := DecodeFrom(bytes.NewReader(xlsxOf(t, [][]string{nil, nil, {"Inv No", "Total"}, {"INV-1", "5"}})), "xlsx", 3)
	if err != nil {
		t.Fatalf("xlsx: %v", err)
	}
	assertHeader(t, csvHeader, []string{"Inv No", "Total"})
	assertHeader(t, xlsxHeader, csvHeader)
	assertRows(t, csvRows, [][]string{{"INV-1", "5"}})
	assertRows(t, xlsxRows, csvRows)
}

func TestDecodeFrom_TitleWithAnotherDelimiterDoesNotSteerTheSniff(t *testing.T) {
	fixture := "Report; March, 2026 | FY;Q1;Q2\nInv No\tTotal\nINV-1\t5\n"

	header, rows, facts, err := DecodeFrom(strings.NewReader(fixture), "csv", 2)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	if facts.Delimiter != "\t" {
		t.Errorf("facts.Delimiter = %q, want tab", facts.Delimiter)
	}
	assertHeader(t, header, []string{"Inv No", "Total"})
	assertRows(t, rows, [][]string{{"INV-1", "5"}})
}

func TestDecodeFrom_UnclosedQuoteInATitleIsNeverParsed(t *testing.T) {
	fixture := "\"Sales register\nInv No,Total\nINV-1,5\n"

	if _, _, _, err := DecodeFrom(strings.NewReader(fixture), "csv", 1); err == nil {
		t.Error("N=1: err = nil, want today's parse error")
	}
	header, rows, _, err := DecodeFrom(strings.NewReader(fixture), "csv", 2)
	if err != nil {
		t.Fatalf("N=2: %v", err)
	}
	assertHeader(t, header, []string{"Inv No", "Total"})
	assertRows(t, rows, [][]string{{"INV-1", "5"}})
}

// Pins the ceiling: a quoted cell spanning lines above the header counts once per line.
func TestDecodeFrom_MultiLineTitleCellCountsEachLine(t *testing.T) {
	fixture := "\"Sales\nregister\"\nInv No,Total\nINV-1,5\n"

	header, _, _, err := DecodeFrom(strings.NewReader(fixture), "csv", 3)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	assertHeader(t, header, []string{"Inv No", "Total"})
}

// Charset and control-byte checks cover the whole file, title lines included.
func TestDecodeFrom_ControlByteInATitleRefusesTheFile(t *testing.T) {
	fixture := "Title\x01\nInv No,Total\nINV-1,5\n"

	_, _, _, err := DecodeFrom(strings.NewReader(fixture), "csv", 2)
	if err == nil || !strings.Contains(err.Error(), "disallowed control byte 0x01 at offset 5") {
		t.Errorf("err = %v, want the control-byte refusal at offset 5", err)
	}
}

func TestDecodeFrom_BadDataBelowTheHeaderStillFails(t *testing.T) {
	fixture := "Title\nInv No,Total\nINV-1,5 \"x\"\n"

	if _, _, _, err := DecodeFrom(strings.NewReader(fixture), "csv", 2); err == nil {
		t.Error("err = nil, want the bare-quote parse error from the data row")
	}
}

func TestDecodeFrom_XLSXHeaderOnTheLastRow(t *testing.T) {
	fixture := xlsxOf(t, [][]string{{"Title"}, nil, {"Inv No", "Total"}})

	header, rows, facts, err := DecodeFrom(bytes.NewReader(fixture), "xlsx", 3)
	if err != nil {
		t.Fatalf("DecodeFrom: %v", err)
	}
	assertHeader(t, header, []string{"Inv No", "Total"})
	if rows != nil {
		t.Errorf("rows = %#v, want nil", rows)
	}
	if facts != (DecodeFacts{Format: "xlsx"}) {
		t.Errorf("facts = %+v, want {Format: xlsx}", facts)
	}
}
