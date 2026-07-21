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
	"unicode/utf8"

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

// --- XLSX unzip-size bound (CodeRabbit, M4-03 PR review: zip-bomb -> DoS) ---

// TestDecode_XLSXUnzipSizeLimitEnforced proves decodeXLSX actually threads
// maxXLSXUnzipBytes through to excelize.Options.UnzipSizeLimit, rather than
// fabricating a real 100+ MiB zip-bomb fixture (unnecessary to prove the
// wiring, and cataloguing exhaustive oversized/malicious uploads is M4-15's
// job, not this story's): a trivial one-cell workbook's uncompressed total
// (several KB across [Content_Types].xml/_rels/workbook.xml/styles.xml/
// sheet1.xml) comfortably exceeds a near-zero limit, so temporarily lowering
// maxXLSXUnzipBytes must make Decode error where it otherwise wouldn't.
func TestDecode_XLSXUnzipSizeLimitEnforced(t *testing.T) {
	fixture := buildXLSX(t, func(f *excelize.File, sheet string) {
		if err := f.SetCellValue(sheet, "A1", "invoice_number"); err != nil {
			t.Fatalf("SetCellValue: %v", err)
		}
	})

	original := maxXLSXUnzipBytes
	maxXLSXUnzipBytes = 16 // far below even a minimal workbook's real uncompressed size
	defer func() { maxXLSXUnzipBytes = original }()

	if _, _, _, err := Decode(bytes.NewReader(fixture), "xlsx"); err == nil {
		t.Fatal("Decode: err = nil, want an unzip-size-limit error once maxXLSXUnzipBytes is set below the workbook's real uncompressed size")
	}
}

// --- quote-aware delimiter sniff (CodeRabbit, M4-03 PR review) -------------

// TestDecode_DelimiterSniffPicksSemicolonDespiteMoreCommasInsideQuotedFields:
// a ";"-delimited header whose QUOTED fields contain MORE commas (4) than
// there are real semicolon separators (2) must still sniff ";", not ",".
// Raw byte-frequency counting (the old sniffDelimiter) would tally the 4
// embedded commas against the 2 real semicolons and wrongly pick ",",
// garbling the whole file; sniffDelimiter now parses the header LINE through
// encoding/csv per candidate and picks whichever yields the most
// successfully-parsed fields, which is quote-aware by construction --
// parsing with the WRONG candidate as the delimiter fails outright (a
// quoted field must be immediately followed by ITS delimiter or the line
// end, and here it's followed by ';', not the candidate's ','), so only ';'
// parses cleanly.
func TestDecode_DelimiterSniffPicksSemicolonDespiteMoreCommasInsideQuotedFields(t *testing.T) {
	fixture := []byte(`"Buyer, Inc, Ltd";"Address, Line, Two";total` + "\n" +
		`"Acme, Global, Corp";"1 Main, St, Suite 2";100.00` + "\n")

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if facts.Delimiter != ";" {
		t.Fatalf("facts.Delimiter = %q, want %q -- sniff must not be misled by more commas inside quoted fields than real semicolons", facts.Delimiter, ";")
	}
	assertHeader(t, header, []string{"Buyer, Inc, Ltd", "Address, Line, Two", "total"})
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	assertHeader(t, rows[0], []string{"Acme, Global, Corp", "1 Main, St, Suite 2", "100.00"})
}

// --- M4-15-01: control-byte encoding gate (RED, gate not yet implemented) --
//
// decodeCSV will gain a guardrail, placed after the BOM/charset sniff switch
// and before sniffDelimiter, that rejects the resolved `decoded []byte` if
// it contains a NUL (0x00) or any other C0 control byte < 0x20 EXCEPT the
// three whitelisted ones a real CSV legitimately carries: \t (0x09, a
// delimiter candidate), \n (0x0A) and \r (0x0D) (line endings). The three
// tests below are RED today (decodeCSV has no such gate, so it silently
// decodes garbage-encoded uploads); the fourth pins the whitelist so the
// future gate doesn't regress tab/CRLF-delimited CSVs once it lands.

// A clean ASCII header with a raw C0 control byte (0x01, not on the
// whitelist) inside a DATA cell. 0x01 is valid single-byte UTF-8, so this
// takes the utf8.Valid fast path in decodeCSV and, with no gate, decodes
// without error today.
func TestDecode_CleanHeaderGarbageDataRejected(t *testing.T) {
	fixture := []byte("a,b\nfoo,\x01bar\n")

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err == nil {
		t.Fatalf("Decode: err = nil, want a decode error for a data cell containing raw control byte 0x01 (header=%#v rows=%#v)", header, rows)
	}
	if rows != nil {
		t.Errorf("rows = %#v, want nil when Decode rejects the input", rows)
	}
}

// A NUL byte (0x00) embedded in a data cell. Like 0x01, NUL is valid
// single-byte UTF-8, so decodeCSV's utf8.Valid branch accepts it and
// encoding/csv treats it as an ordinary field byte -- no error today.
func TestDecode_NULByteRejected(t *testing.T) {
	fixture := []byte("a,b\nfoo,\x00bar\n")

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err == nil {
		t.Fatalf("Decode: err = nil, want a decode error for a data cell containing a NUL byte (header=%#v rows=%#v)", header, rows)
	}
}

// Pure-ASCII content encoded as UTF-16LE WITHOUT a BOM (each ASCII byte
// followed by 0x00, built explicitly -- no golang.org/x/text encoder,
// since that always emits a BOM). Every individual byte is < 0x80, so the
// whole thing is trivially valid UTF-8 (a sequence of single-byte runes) --
// decodeCSV's utf8.Valid branch takes it as-is, embedded 0x00 bytes and
// all, with no error today. Only the post-switch control-byte gate can
// catch this: there is no BOM to sniff, and it never reaches the
// Windows-1252 fallback because it IS valid UTF-8.
func TestDecode_UTF16LENoBOMRejectedViaGate(t *testing.T) {
	content := []byte("a,b\n1,2\n")
	raw := make([]byte, 0, len(content)*2)
	for _, b := range content {
		raw = append(raw, b, 0x00)
	}

	header, rows, _, err := Decode(bytes.NewReader(raw), "csv")
	if err == nil {
		t.Fatalf("Decode: err = nil, want a decode error for BOM-less UTF-16LE content (embedded NUL bytes) (header=%#v rows=%#v)", header, rows)
	}
}

// Whitelist regression guard: a tab-delimited, CRLF-terminated, otherwise
// clean-ASCII CSV must NOT be rejected once the control-byte gate lands --
// \t, \r and \n are all on the whitelist. Already GREEN today (no gate
// exists yet to regress).
func TestDecode_TabDelimitedCRLFNotRejected(t *testing.T) {
	fixture := []byte("a\tb\r\n1\t2\r\n")

	header, rows, facts, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v, want no error -- tab delimiter and CRLF line endings must never be rejected by the control-byte gate", err)
	}
	if facts.Delimiter != "\t" {
		t.Fatalf("facts.Delimiter = %q, want %q (test fixture setup)", facts.Delimiter, "\t")
	}
	assertHeader(t, header, []string{"a", "b"})
	assertRows(t, rows, [][]string{{"1", "2"}})
}

// --- M4-15-01 QA (Mode B): adversarial C0-boundary + per-branch gate --------
//
// The three tests above (TestDecode_CleanHeaderGarbageDataRejected,
// TestDecode_NULByteRejected, TestDecode_UTF16LENoBOMRejectedViaGate) prove
// the gate exists; the tests below pin its EXACT scope decision
// ([reject-byte-scope]: NUL + C0 controls < 0x20 except \t/\n/\r) and prove
// it actually runs on every branch that produces a `decoded []byte` --
// UTF-8-BOM-stripped and Windows-1252-fallback, not just the utf8.Valid fast
// path the three tests above happen to exercise.

// TestDecode_C0BoundaryPrecise pins the gate's byte-set boundary exactly:
// 0x0B/0x0C (C0 controls adjacent to the whitelist but NOT on it) and 0x1F
// (the last byte below the 0x20 cutoff) are rejected; 0x20 (the first byte
// NOT covered by "< 0x20") and the three whitelisted controls are accepted.
func TestDecode_C0BoundaryPrecise(t *testing.T) {
	tests := []struct {
		name    string
		fixture []byte
		wantErr bool
		wantRow []string // only checked when wantErr is false
	}{
		{
			name:    "0x0B vertical tab rejected",
			fixture: []byte("a,b\nfoo,\x0bbar\n"),
			wantErr: true,
		},
		{
			name:    "0x0C form feed rejected",
			fixture: []byte("a,b\nfoo,\x0cbar\n"),
			wantErr: true,
		},
		{
			name:    "0x1F unit separator rejected",
			fixture: []byte("a,b\nfoo,\x1fbar\n"),
			wantErr: true,
		},
		{
			name:    "0x20 space accepted",
			fixture: []byte("a,b\nfoo, bar\n"),
			wantErr: false,
			wantRow: []string{"foo", " bar"},
		},
		{
			name:    "0x09 tab accepted (embedded in a comma-delimited data cell)",
			fixture: []byte("a,b\nfoo,x\x09y\n"),
			wantErr: false,
			wantRow: []string{"foo", "x\ty"},
		},
		{
			name:    "0x0A LF accepted (the row separator itself)",
			fixture: []byte("a,b\nfoo,bar\n"),
			wantErr: false,
			wantRow: []string{"foo", "bar"},
		},
		{
			name:    "0x0D CR accepted (bare, embedded mid-field)",
			fixture: []byte("a,b\nfoo,x\rY\n"),
			wantErr: false,
			wantRow: []string{"foo", "x\rY"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header, rows, _, err := Decode(bytes.NewReader(tt.fixture), "csv")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Decode: err = nil, want a decode error (header=%#v rows=%#v)", header, rows)
				}
				if rows != nil {
					t.Errorf("rows = %#v, want nil when Decode rejects the input", rows)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode: %v, want no error", err)
			}
			if len(rows) != 1 {
				t.Fatalf("len(rows) = %d, want 1", len(rows))
			}
			assertHeader(t, rows[0], tt.wantRow)
		})
	}
}

// TestDecode_DELByteNotRejected proves the gate's scope stops at 0x20: DEL
// (0x7F) is a control character by the wider ASCII-control definition, but
// [reject-byte-scope] only covers "NUL or < 0x20", so a data cell containing
// a raw 0x7F must decode WITHOUT error. This guards against someone
// "helpfully" widening firstDisallowedControlByte's condition to `b < 0x20 ||
// b == 0x7F` later without revisiting the decision.
func TestDecode_DELByteNotRejected(t *testing.T) {
	fixture := []byte("a,b\nfoo,\x7fbar\n")

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v, want no error -- DEL (0x7F) is out of [reject-byte-scope] (below 0x20 only)", err)
	}
	assertHeader(t, header, []string{"a", "b"})
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	assertHeader(t, rows[0], []string{"foo", "\x7fbar"})
}

// TestDecode_GateFiresOnUTF8BOMBranch proves the gate runs on the
// BOM-stripped `decoded` bytes, not just the plain utf8.Valid fast path the
// three RED-authored gate tests happen to exercise: a UTF-8-BOM-prefixed
// input whose body carries a disallowed NUL byte must still be rejected.
func TestDecode_GateFiresOnUTF8BOMBranch(t *testing.T) {
	fixture := append([]byte{0xEF, 0xBB, 0xBF}, []byte("a,b\nfoo,\x00bar\n")...)

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err == nil {
		t.Fatalf("Decode: err = nil, want a decode error -- a NUL byte after a stripped UTF-8 BOM must still be rejected (header=%#v rows=%#v)", header, rows)
	}
	if rows != nil {
		t.Errorf("rows = %#v, want nil when Decode rejects the input", rows)
	}
}

// TestDecode_GateFiresOnWindows1252FallbackBranch proves the gate also runs
// on the Windows-1252-decoded `decoded` bytes, i.e. AFTER the fallback
// decode, not only before it. The fixture is invalid UTF-8 (confirmed below
// via utf8.Valid, matching decodeCSV's own routing condition) and carries no
// BOM, so it takes the `default:` charmap.Windows1252 branch; it also
// carries a disallowed control byte (0x01) that charmap.Windows1252 passes
// through unchanged (Windows-1252's C0 range is identical to Latin-1/ASCII),
// so the gate must still catch it post-fallback.
func TestDecode_GateFiresOnWindows1252FallbackBranch(t *testing.T) {
	fixture := []byte("a,b\nfoo,\x81\x01bar\n")
	if utf8.Valid(fixture) {
		t.Fatalf("test fixture setup: fixture is valid UTF-8, want invalid so Decode routes to the Windows-1252 fallback branch")
	}

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err == nil {
		t.Fatalf("Decode: err = nil, want a decode error -- 0x01 survives the Windows-1252 fallback decode unchanged and must still be rejected (header=%#v rows=%#v)", header, rows)
	}
	if rows != nil {
		t.Errorf("rows = %#v, want nil when Decode rejects the input", rows)
	}
}

// TestDecode_EmptyInputNotRejectedByGate guards specifically that the gate
// itself doesn't break the empty-file path (TestDecode_EmptyInputNoPanic
// above already pins the same return shape for a different reason --
// panic-safety in general; this one pins it as a regression guard for
// firstDisallowedControlByte specifically, since a zero-length scan finding
// nothing is the one input shape most likely to be broken by an off-by-one
// in a hand-rolled byte-scan loop).
func TestDecode_EmptyInputNotRejectedByGate(t *testing.T) {
	header, rows, facts, err := Decode(bytes.NewReader(nil), "csv")
	if err != nil {
		t.Fatalf("Decode(empty input): %v, want no error -- the gate must not fire on an empty byte slice", err)
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

// TestDecode_AllWhitelistedControlsAccepted covers a case
// TestDecode_TabDelimitedCRLFNotRejected doesn't: a QUOTED field with an
// embedded raw LF (RFC4180-legal multiline CSV cell). encoding/csv only
// permits an unquoted \n to terminate a row; a quoted cell can carry one
// literally. The gate scans raw bytes with no notion of quoting, so this
// also proves it does not mistake a legitimately-embedded 0x0A for a
// disallowed byte just because it sits inside a cell rather than at a row
// boundary.
func TestDecode_AllWhitelistedControlsAccepted(t *testing.T) {
	fixture := []byte("a,b\n\"foo\nbar\",baz\n")

	header, rows, _, err := Decode(bytes.NewReader(fixture), "csv")
	if err != nil {
		t.Fatalf("Decode: %v, want no error -- an embedded LF inside a quoted field must not be rejected", err)
	}
	assertHeader(t, header, []string{"a", "b"})
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	assertHeader(t, rows[0], []string{"foo\nbar", "baz"})
}
