// fixtures_test.go: the deterministic PDF corpus. Real client documents cannot be committed,
// so the fixtures are generated here and the bytes they produce are committed beside them --
// TestFixtures_MatchTheirGenerator regenerates and byte-compares, so neither side can drift
// alone. Regenerate a deliberate change with -update and read the diff before committing.
//
// No third-party PDF writer -- a convention, not a fence: assertFenced allows the extraction
// import by name and ignores non-module deps entirely, so nothing here reds on one. The reason
// is that a third-party writer makes TestFixtures_GeneratorIsDeterministic someone else's
// property. The imports today are stdlib, the package under test and internal/platform/jev.
package extraction_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

var fxUpdate = flag.Bool("update", false, "rewrite the PDF fixtures under testdata/ from their generators instead of comparing against them")

const (
	fxDir      = "testdata"
	fxNative   = "native_invoice.pdf"
	fxNative3  = "native_3page.pdf"
	fxScanned  = "scanned_invoice.pdf"
	fxHybrid   = "hybrid_invoice.pdf"
	fxTable    = "table_invoice.pdf"
	fxDense    = "dense_invoice.pdf"
	fxRich     = "rich_invoice.pdf"
	fxMinBytes = 200 // floor: every fixture carries a catalog, a page tree, a page and a stream
)

// The golden corpus: the six anchor-rule layouts, flat in testdata/ under corpus_test.go's
// corpusPrefix. See docs/extraction-corpus.md.
const (
	fxCorpusInline    = "corpus_inline_labels.pdf"
	fxCorpusSplit     = "corpus_split_labels.pdf"
	fxCorpusStacked   = "corpus_stacked_labels.pdf"
	fxCorpusTwoColumn = "corpus_two_column.pdf"
	fxCorpusAmbigDate = "corpus_ambiguous_date.pdf"
	fxCorpusTotals    = "corpus_totals_block.pdf"
)

var fxCorpus = []struct {
	name  string
	build func() []byte
}{
	{fxNative, fxBuildNative},
	{fxNative3, fxBuildNative3Page},
	{fxScanned, fxBuildScanned},
	{fxHybrid, fxBuildHybrid},
	{fxTable, fxBuildTable},
	{fxDense, fxBuildDense},
	{fxCorpusInline, fxBuildCorpusInlineLabels},
	{fxCorpusSplit, fxBuildCorpusSplitLabels},
	{fxCorpusStacked, fxBuildCorpusStackedLabels},
	{fxCorpusTwoColumn, fxBuildCorpusTwoColumn},
	{fxCorpusAmbigDate, fxBuildCorpusAmbiguousDate},
	{fxCorpusTotals, fxBuildCorpusTotalsBlock},
	// Not corpus_-prefixed on purpose: EXTR-14-09's learned-rule fixture, regenerated and
	// byte-compared like the rest but outside every corpus_ ratchet.
	{fxLearnedTwoParty, fxBuildLearnedTwoParty},
	{fxLearnedTypedTotal, func() []byte {
		return fxBuildLearnedTypedTotal("INV-1009", "2026-05-02", "Adeyemi Trading Limited", "14,800,000.00")
	}},
	{fxLearnedTypedTotalTwin, func() []byte {
		return fxBuildLearnedTypedTotal("INV-1010", "2026-06-09", "Okafor Industries Limited", "9,250,000.00")
	}},
	// Not corpus_-prefixed on purpose: EXTR-18-01's rich fixture, outside every corpus_ ratchet.
	{fxRich, fxBuildRichInvoice},
	// Not corpus_-prefixed on purpose: EXTR-21-06's four production arrangements. Byte-compared
	// like the rest, outside every corpus_ ratchet.
	{fxWildTwoParty, func() []byte { return fxBuildWildTwoPartyBareTIN(fxWildTwoPartyFriendly) }},
	{fxWildRuled, func() []byte { return fxBuildWildRuledLinesTotals(fxWildRuledFriendly) }},
	{fxWildRCNaira, fxBuildWildRCDueNaira},
	{fxWildStacked, func() []byte { return fxBuildWildStackedBorderless(fxWildStackedFriendly) }},
	// EXTR-21-07's image-only arrangement: raster ink, no text layer at all.
	{fxWildScanned, fxBuildWildScannedNoNumber},
	// Each twin's geometry under its source's printed labels.
	{fxWildTwoPartyAsPrinted, func() []byte { return fxBuildWildTwoPartyBareTIN(fxWildTwoPartyPrinted) }},
	{fxWildRuledAsPrinted, func() []byte { return fxBuildWildRuledLinesTotals(fxWildRuledPrinted) }},
	{fxWildStackedAsPrinted, func() []byte { return fxBuildWildStackedBorderless(fxWildStackedPrinted) }},
	// EXTR-26-06: faithful transcriptions of two Nigerian invoice mock-ups, outside every corpus_
	// ratchet.
	{fxAdvisoryRegister, func() []byte { return fxBuildAdvisoryRegister(false) }},
	// R0 with the two letter-spaced header labels unspaced -- the one declared transformation.
	{fxAdvisoryRegisterUnspaced, func() []byte { return fxBuildAdvisoryRegister(true) }},
	{fxAdvisoryDense, fxBuildAdvisoryDense},
	// EXTR-36-01: R0 re-set one Tj per glyph, Chrome/Skia's own shape.
	{fxChromeRegister, fxBuildChromeRegister},
	{fxChromeRegisterTwin, fxBuildChromeRegisterTwin},
	// AIR-03-05's deployed-steering fixture: not corpus_-prefixed, outside every corpus_ ratchet.
	{fxAISteered, fxBuildAISteeredInvoice},
	// AIR-04-04's deployed-unavailable fixture: outside every corpus_ ratchet.
	{fxAIUnavailable, fxBuildAIUnavailableInvoice},
	// The value check's one-doubt fixture: outside every corpus_ ratchet.
	{fxJevDoubt, fxBuildJevDoubtInvoice},
	// AIR-08-13's deployed line-items fixture: outside every corpus_ ratchet.
	{fxAILines, fxBuildAILinesInvoice},
	// CHECK-01-03's seven non-invoice types: the document-type check's negative half.
	// Byte-compared like the rest, outside every corpus_ ratchet.
	{fxNonInvoiceReceipt, fxBuildNonInvoiceReceipt},
	{fxNonInvoiceProforma, fxBuildNonInvoiceProforma},
	{fxNonInvoiceQuotation, fxBuildNonInvoiceQuotation},
	{fxNonInvoiceCreditNote, fxBuildNonInvoiceCreditNote},
	{fxNonInvoiceDeliveryNote, fxBuildNonInvoiceDeliveryNote},
	{fxNonInvoiceStatement, fxBuildNonInvoiceStatement},
	{fxNonInvoicePurchaseOrder, fxBuildNonInvoicePurchaseOrder},
}

// --- the generator ----------------------------------------------------------

// US-Letter, in points.
const (
	fxPageWidthPt  = 612
	fxPageHeightPt = 792
)

const (
	fxHelvetica = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"

	// fxImageDraw scales the 4x4 XObject to fill the page.
	fxImageDraw = "q\n612 0 0 792 0 0 cm\n/Im0 Do\nQ\n"
)

// fxPixels is a 4x4 /DeviceGray checkerboard. Neither byte value is an ASCII letter, so a
// whole-file scan for /Font, BT or Tj cannot trip over the image data.
var fxPixels = []byte{
	0x00, 0xC0, 0x00, 0xC0,
	0xC0, 0x00, 0xC0, 0x00,
	0x00, 0xC0, 0x00, 0xC0,
	0xC0, 0x00, 0xC0, 0x00,
}

// fxObject is one indirect object's body: a dict, or a dict followed by its stream.
type fxObject []byte

// fxAssemble writes objects 1..N followed by the cross-reference table. The table is nothing
// but each object's byte offset from the file start, so the objects have to be laid down
// before it; every entry is padded to exactly 20 bytes, which is what lets a reader seek to
// one by number. startxref then carries the table's own offset.
func fxAssemble(objs []fxObject) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	buf.Write([]byte{'%', 0xE2, 0xE3, 0xCF, 0xD3, '\n'})

	offsets := make([]int, len(objs))
	for i, obj := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n", i+1)
		buf.Write(obj)
		buf.WriteString("\nendobj\n")
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	// No /ID: it is optional on an unencrypted file, and every conventional value for it is
	// derived from the clock.
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

func fxStream(body []byte) fxObject {
	var b bytes.Buffer
	fmt.Fprintf(&b, "<< /Length %d >>\nstream\n", len(body))
	b.Write(body)
	// The EOL before endstream is a delimiter, not part of the stream data.
	b.WriteString("\nendstream")
	return b.Bytes()
}

func fxImageObject(w, h int, body []byte) fxObject {
	var b bytes.Buffer
	fmt.Fprintf(&b, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Length %d >>\nstream\n", w, h, len(body))
	b.Write(body)
	b.WriteString("\nendstream")
	return b.Bytes()
}

func fxFontRes(obj int) string  { return fmt.Sprintf("<< /Font << /F1 %d 0 R >> >>", obj) }
func fxImageRes(obj int) string { return fmt.Sprintf("<< /XObject << /Im0 %d 0 R >> >>", obj) }

func fxPage(res string, contents int) fxObject {
	return fxObject(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources %s /Contents %d 0 R >>",
		fxPageWidthPt, fxPageHeightPt, res, contents))
}

// fxLine is one line of page text at a point size and a bottom-left PDF user-space origin.
type fxLine struct {
	size, x, y int
	text       string
}

func fxText(lines ...fxLine) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		fmt.Fprintf(&b, "BT\n/F1 %d Tf\n%d %d Td\n(%s) Tj\nET\n", l.size, l.x, l.y, l.text)
	}
	return b.Bytes()
}

// fxBuildNative is one US-Letter page of real text.
func fxBuildNative() []byte {
	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxFontRes(5), 4),
		fxStream(fxText(
			fxLine{24, 72, 720, "INVOICE"},
			fxLine{12, 72, 690, "Invoice No: INV-001"},
			fxLine{12, 72, 670, "Total: NGN 1,500.00"},
		)),
		fxObject(fxHelvetica),
	})
}

// fxBuildNative3Page is three pages of real text sharing one font.
func fxBuildNative3Page() []byte {
	const font = 9
	objs := []fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R 5 0 R 7 0 R] /Count 3 >>"),
	}
	for p := 1; p <= 3; p++ {
		objs = append(objs,
			fxPage(fxFontRes(font), 2+2*p),
			fxStream(fxText(
				fxLine{24, 72, 720, "INVOICE"},
				fxLine{12, 72, 690, fmt.Sprintf("Page %d of 3", p)},
				fxLine{12, 72, 670, fmt.Sprintf("Invoice No: INV-00%d", p)},
			)),
		)
	}
	return fxAssemble(append(objs, fxObject(fxHelvetica)))
}

// fxBuildScanned is image-only: no /Font, no BT/Tj, one 4x4 /DeviceGray XObject filling the
// page. AC-6's needs-OCR case.
func fxBuildScanned() []byte {
	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxImageRes(5), 4),
		fxStream([]byte(fxImageDraw)),
		fxImageObject(4, 4, fxPixels),
	})
}

// fxBuildHybrid is page 1 native text, page 2 image-only. It pins the unowned gap in D-9:
// today's verdict is document-level and does not flag it, and EXTR-02-07 asserts exactly that.
func fxBuildHybrid() []byte {
	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>"),
		fxPage(fxFontRes(7), 4),
		fxStream(fxText(
			fxLine{24, 72, 720, "INVOICE"},
			fxLine{12, 72, 690, "Invoice No: INV-002"},
			fxLine{12, 72, 670, "Total: NGN 2,750.00"},
		)),
		fxPage(fxImageRes(8), 6),
		fxStream([]byte(fxImageDraw)),
		fxObject(fxHelvetica),
		fxImageObject(4, 4, fxPixels),
	})
}

// fxTableColXs are the 4-column table's vertical rule positions (5 boundaries, 117pt wide
// columns). fxTableRowYs are its horizontal rule positions: top of header, header/row1,
// row1/row2, bottom. Header/body text matches build_docx.py's for cross-format parity.
var (
	fxTableColXs  = [5]int{72, 189, 306, 423, 540}
	fxTableRowYs  = [4]int{650, 626, 602, 578}
	fxTableHeader = []string{"Description", "Qty", "Unit Price", "Total"}
	fxTableBody   = [][]string{
		{"Widget", "2", "500.00", "1000.00"},
		{"Gadget", "1", "500.00", "500.00"},
	}
)

// fxRuleH is a stroked horizontal line at y from x0 to x1.
func fxRuleH(y, x0, x1 int) string {
	return fmt.Sprintf("%d %d m\n%d %d l\nS\n", x0, y, x1, y)
}

// fxRuleV is a stroked vertical line at x from y0 to y1.
func fxRuleV(x, y0, y1 int) string {
	return fmt.Sprintf("%d %d m\n%d %d l\nS\n", x, y0, x, y1)
}

// fxTableRowText lays one row of cell strings on a baseline, 4pt into each column.
func fxTableRowText(baseline int, cells []string) []fxLine {
	lines := make([]fxLine, len(cells))
	for i, text := range cells {
		lines[i] = fxLine{10, fxTableColXs[i] + 4, baseline, text}
	}
	return lines
}

// fxBuildTable is one US-Letter page: a title plus a ruled 4-column, 3-row table (a header
// row and two body rows). EXTR-03-04's table-mapping fixture -- TableFormer's own read of
// it is asserted only to a coarse floor (T-04-14), never pinned exactly, because an ML
// model's row/column verdict on a synthetic page is not a contract this story can hold.
func fxBuildTable() []byte {
	lines := []fxLine{{24, 72, 720, "INVOICE"}}
	lines = append(lines, fxTableRowText(638, fxTableHeader)...)
	lines = append(lines, fxTableRowText(614, fxTableBody[0])...)
	lines = append(lines, fxTableRowText(590, fxTableBody[1])...)

	var rules bytes.Buffer
	for _, y := range fxTableRowYs {
		rules.WriteString(fxRuleH(y, fxTableColXs[0], fxTableColXs[len(fxTableColXs)-1]))
	}
	for _, x := range fxTableColXs {
		rules.WriteString(fxRuleV(x, fxTableRowYs[len(fxTableRowYs)-1], fxTableRowYs[0]))
	}

	content := fxText(lines...)
	content = append(content, rules.Bytes()...)

	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxFontRes(5), 4),
		fxStream(content),
		fxObject(fxHelvetica),
	})
}

// fxRichTableRowYs are the rich fixture's own horizontal rule positions: header-top,
// header/row1, row1/row2, row2/row3, row3/row4, bottom (1 header + 4 data rows = 5 bands =
// 6 lines). A separate array from fxTableRowYs -- that one is fxBuildTable's own, and
// resizing it would change table_invoice.pdf's committed bytes.
var fxRichTableRowYs = [6]int{590, 566, 542, 518, 494, 470}

// fxBuildRichInvoice is one US-Letter page: header fields, a ruled 4-column/4-row table with a
// deliberate line-total error (Gadget: 3 x 250.00 prints as 900.00, not 750.00) and a row whose
// Qty and Unit Price are blank, and a split-label totals block whose Sub-total (1,500.00) does
// not match the line sum (2,080.00). Both mismatches exceed reconcileTolerance, so
// ReasonInconsistentTotal has something to catch.
func fxBuildRichInvoice() []byte {
	lines := []fxLine{
		{24, 72, 720, "INVOICE"},
		{12, 72, 690, "Invoice No: ASC-2026-0918"},
		{12, 72, 672, "Issue Date: 12/03/2026"},
		{12, 72, 654, "Supplier: Kaduna Supply Limited"},
		{12, 72, 636, "TIN: 30154829-0032"},
		{12, 72, 618, "Currency: NGN"},
	}
	lines = append(lines, fxTableRowText(578, fxTableHeader)...)
	lines = append(lines, fxTableRowText(554, []string{"Widget", "2", "500.00", "1000.00"})...)
	lines = append(lines, fxTableRowText(530, []string{"Gadget", "3", "250.00", "900.00"})...)
	lines = append(lines, fxTableRowText(506, []string{"Delivery", "1", "120.00", "120.00"})...)
	// Row 4 prints a description and a line total but no Qty or Unit Price. Both blanks fail
	// liNormalizeQuantity/liNormalizeAmount, so LineItemResults emits no row for them
	// (lineitems.go:95) and the wire OMITS the two cells -- the only subject EXTR13-E2E-01's
	// empty-cell arm has once the real extractor replaces the mock's hand-authored reading.
	lines = append(lines, fxTableRowText(482, []string{"Handling", "", "", "60.00"})...)
	// Split-label shape (label Tj, then value Tj, same baseline) -- fxBuildCorpusTotalsBlock's
	// precedent. The inline single-Tj form would not resolve ReasonNone for Reconcile to check.
	lines = append(lines,
		fxLine{12, 380, 440, "Sub-total"}, fxLine{12, 500, 440, "1,500.00"},
		fxLine{12, 380, 422, "VAT"}, fxLine{12, 500, 422, "112.50"},
		fxLine{12, 380, 404, "Total"}, fxLine{12, 500, 404, "1,612.50"},
	)

	// H before V, matching fxBuildTable's own loop shape.
	var rules bytes.Buffer
	for _, y := range fxRichTableRowYs {
		rules.WriteString(fxRuleH(y, fxTableColXs[0], fxTableColXs[len(fxTableColXs)-1]))
	}
	for _, x := range fxTableColXs {
		rules.WriteString(fxRuleV(x, fxRichTableRowYs[len(fxRichTableRowYs)-1], fxRichTableRowYs[0]))
	}

	content := fxText(lines...)
	content = append(content, rules.Bytes()...)

	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxFontRes(5), 4),
		fxStream(content),
		fxObject(fxHelvetica),
	})
}

// --- the golden corpus ------------------------------------------------------

// The six layouts below are the anchor-rule corpus. One fxLine is one Tj is one pdfium token,
// so the number of fxLine values per field IS the token granularity a layout exercises.
// Every TIN sits in the free part of the reserved 99999999- block, never -0001..-0009
// (internal/submission/mock_script.go). corpus_test.go holds what Tier-1 must resolve from
// each; docs/extraction-corpus.md holds the rest.

// fxTextPage is one US-Letter page of text over a single Helvetica.
func fxTextPage(lines ...fxLine) []byte {
	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxFontRes(5), 4),
		fxStream(fxText(lines...)),
		fxObject(fxHelvetica),
	})
}

// fxNaira is the naira in a PDF string literal: octal for byte 0xA4, the code both
// /Differences and the /ToUnicode CMap key on. An octal escape ends at three digits, so a
// digit may follow it unseparated.
const fxNaira = `\244`

// fxAmount is the amount every naira spec reads back, without its symbol.
const fxAmount = "1,075.00"

// fxNairaFont is fxHelvetica plus the two halves the sign needs: /Differences names the glyph
// so it draws, /ToUnicode carries the Unicode so pdfium extracts U+20A6. toUnicode <= 0 omits
// the CMap reference, which TestFixtures_WithoutTheToUnicodeCMapTheGlyphReadsAsCurrencySign
// reads back as U+00A4.
func fxNairaFont(toUnicode int) string {
	dict := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica" +
		" /Encoding << /Type /Encoding /Differences [164 /naira] >>"
	if toUnicode > 0 {
		dict += fmt.Sprintf(" /ToUnicode %d 0 R", toUnicode)
	}
	return dict + " >>"
}

// fxUCS2CMap is a /ToUnicode CMap over single-byte codes: each pair is a hex code and the hex
// UTF-16 it reads back as. Constant body, so fxStream's /Length and the assembled bytes stay
// deterministic.
func fxUCS2CMap(name string, codes [][2]string) fxObject {
	var chars bytes.Buffer
	for _, c := range codes {
		fmt.Fprintf(&chars, "<%s> <%s>\n", c[0], c[1])
	}
	return fxStream(fmt.Appendf(nil, `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CMapName /%s-UCS2 def
/CMapType 2 def
/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def
1 begincodespacerange
<00> <FF>
endcodespacerange
%d beginbfchar
%sendbfchar
endcmap
CMapName currentdict /CMap defineresource pop
end
end`, name, len(codes), chars.String()))
}

// fxNairaCMap maps the single code the naira font redefines.
func fxNairaCMap() fxObject {
	return fxUCS2CMap("Naira", [][2]string{{"A4", "20A6"}})
}

// fxNairaTextPage is fxTextPage over a naira-capable font. withCMap is the control knob for
// TestFixtures_WithoutTheToUnicodeCMapTheGlyphReadsAsCurrencySign; fxAssemble numbers by slice
// index, so the appended CMap is object 6.
func fxNairaTextPage(withCMap bool, lines ...fxLine) []byte {
	const cmapObj = 6

	toUnicode := 0
	if withCMap {
		toUnicode = cmapObj
	}
	objs := []fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxFontRes(5), 4),
		fxStream(fxText(lines...)),
		fxObject(fxNairaFont(toUnicode)),
	}
	if withCMap {
		objs = append(objs, fxNairaCMap())
	}
	return fxAssemble(objs)
}

// fxNairaLines is the shared content the naira specs read back: "Total" and the amount as two
// Tj on one baseline, the fxBuildCorpusSplitLabels shape. One Tj would give pdfium a single
// "Total: N1,075.00" token, which reAmount's anchors reject for the label, not the symbol.
func fxNairaLines() []fxLine {
	return []fxLine{
		{24, 72, 720, "INVOICE"},
		{12, 72, 690, "Total"},
		{12, 220, 690, fxNaira + fxAmount},
	}
}

// fxBuildCorpusInlineLabels puts every "Label: value" in one Tj, so all ten fields resolve by
// same_token. No token is a bare TIN, so the format-only sweeps cannot fire here.
func fxBuildCorpusInlineLabels() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No: INV-1001"},
		fxLine{12, 72, 672, "Invoice Date: 2026-03-04"},
		fxLine{12, 72, 654, "Supplier TIN: 99999999-0101"},
		fxLine{12, 72, 636, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 618, "Buyer TIN: 99999999-0102"},
		fxLine{12, 72, 600, "Buyer: Honeywell Group"},
		fxLine{12, 72, 582, "Currency: NGN"},
		fxLine{12, 72, 240, "Sub-total: 1,000.00"},
		fxLine{12, 72, 222, "VAT: 75.00"},
		fxLine{12, 72, 204, "Total: 1,075.00"},
	)
}

// fxBuildCorpusSplitLabels puts label and value on one baseline as two Tj, so the same fields
// resolve by right instead. 15/04/2026 has a day > 12 and is deliberately UNambiguous --
// ambiguity is fxCorpusAmbigDate's job. The buyer TIN reads at Y0 0.53, in the lower page
// half the buyer sweep needs.
func fxBuildCorpusSplitLabels() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No"}, fxLine{12, 220, 690, "INV-1002"},
		fxLine{12, 72, 672, "Invoice Date"}, fxLine{12, 220, 672, "15/04/2026"},
		fxLine{12, 72, 654, "Supplier TIN"}, fxLine{12, 220, 654, "99999999-0201"},
		fxLine{12, 72, 636, "Supplier"}, fxLine{12, 220, 636, "Adeyemi Trading Limited"},
		fxLine{12, 72, 618, "Currency"}, fxLine{12, 220, 618, "NGN"},
		fxLine{12, 72, 360, "Buyer TIN"}, fxLine{12, 220, 360, "99999999-0202"},
		fxLine{12, 72, 342, "Buyer"}, fxLine{12, 220, 342, "Honeywell Group"},
		fxLine{12, 72, 240, "Sub-total"}, fxLine{12, 220, 240, "2,000.00"},
		fxLine{12, 72, 222, "VAT"}, fxLine{12, 220, 222, "150.00"},
		fxLine{12, 72, 204, "Total"}, fxLine{12, 220, 204, "2,150.00"},
	)
}

// fxBuildCorpusStackedLabels stacks each value 16pt under its label at the same x -- the only
// layout with no inline field at all. A label clears its own group's values by at most 0.027
// normalised and the next group's label by at least 0.087, so below cannot span two groups:
// TestCorpus_StackedValuesSitBelowTheirLabels.
func fxBuildCorpusStackedLabels() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No"},
		fxLine{12, 72, 674, "INV-1003"},
		fxLine{12, 72, 610, "Supplier"},
		fxLine{12, 72, 594, "Adeyemi Trading Limited"},
		fxLine{12, 72, 578, "99999999-0301"},
		fxLine{12, 72, 530, "Invoice Date"},
		fxLine{12, 72, 514, "22 Apr 2026"},
		fxLine{12, 72, 360, "Buyer"},
		fxLine{12, 72, 344, "Honeywell Group"},
		fxLine{12, 72, 328, "99999999-0302"},
		fxLine{12, 72, 240, "Total"},
		fxLine{12, 72, 224, "NGN 3,225.00"},
	)
}

// fxBuildCorpusTwoColumn puts the supplier and buyer blocks in different column bands, the only
// corpus layout whose labels reach the right-hand third:
// TestCorpus_TwoColumnPartiesLandInTheOuterBands. x=400 and not 340, which centres the buyer
// labels at 0.58/0.65, inside the MIDDLE band. Both TINs sit inside a longer token, so the
// buyer/supplier split here is decided by label and not by page half.
func fxBuildCorpusTwoColumn() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No: INV-1004"},
		fxLine{12, 72, 672, "Invoice Date: 2026-05-06"},
		fxLine{12, 72, 630, "Supplier"},
		fxLine{12, 72, 614, "Adeyemi Trading Limited"},
		fxLine{12, 72, 598, "TIN: 99999999-0401"},
		fxLine{12, 400, 630, "Buyer"},
		fxLine{12, 400, 614, "Honeywell Group"},
		fxLine{12, 400, 598, "TIN: 99999999-0402"},
		fxLine{12, 72, 240, "Total: NGN 6,450.00"},
	)
}

// fxBuildCorpusAmbiguousDate carries 12/03/2026: both components <= 12 and no month name, so
// ShapeDate returns both readings and issue_date keeps two candidates.
func fxBuildCorpusAmbiguousDate() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No: INV-1005"},
		fxLine{12, 72, 672, "Invoice Date: 12/03/2026"},
		fxLine{12, 72, 654, "Supplier TIN: 99999999-0501"},
		fxLine{12, 72, 636, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 240, "Total: NGN 4,300.00"},
	)
}

// fxBuildCorpusTotalsBlock is a right-aligned split totals block. It exercises the lexicon
// overlap: "Sub-total" matches subtotal AND \btotal\b, because - is a non-word character, so
// one label mints a candidate for two fields. The VAT label carries no percentage -- a
// remainder like "7.5%" would mint a spurious amount candidate.
func fxBuildCorpusTotalsBlock() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No: INV-1006"},
		fxLine{12, 72, 672, "Supplier TIN: 99999999-0601"},
		fxLine{12, 380, 240, "Sub-total"}, fxLine{12, 500, 240, "5,000.00"},
		fxLine{12, 380, 222, "VAT"}, fxLine{12, 500, 222, "375.00"},
		fxLine{12, 380, 204, "Total"}, fxLine{12, 500, 204, "5,375.00"},
	)
}

// --- the learned-rule fixture (NOT a corpus layout) -------------------------

// fxLearnedTwoParty is deliberately named outside corpusPrefix: it is EXTR-14-09's before/after
// document, not a seventh Tier-1 layout, so it stays out of corpusExpect, corpusLayouts,
// corpusTokenFloor and both accuracy rates. docs/extraction-corpus.md, "## Learned rules".
const fxLearnedTwoParty = "learned_two_party.pdf"

// fxBuildLearnedTwoParty stacks both party blocks (label / name / BARE TIN) in page 1's top
// half. The page half does not scope the sweep, so each bare TIN follows its own heading and
// Tier-1 alone decides both supplier_tin and buyer_tin here.
//
// The chain the fixture exists for is unaffected and stronger: a pointed correction's learned
// rule must now BEAT a real generic candidate rather than fill a void
// (TestLearnedTwoParty_Tier1BindsTheBuyerTINAndTheLearnedRuleStillOutranksIt).
func fxBuildLearnedTwoParty() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No: INV-1007"},
		fxLine{12, 72, 672, "Invoice Date: 2026-04-22"},
		fxLine{12, 72, 630, "Supplier"},
		fxLine{12, 72, 614, "Adeyemi Trading Limited"},
		fxLine{12, 72, 598, "99999999-0701"},
		fxLine{12, 72, 540, "Buyer"},
		fxLine{12, 72, 524, "Honeywell Group"},
		fxLine{12, 72, 508, "99999999-0702"},
		fxLine{12, 72, 240, "Total: NGN 3,225.00"},
	)
}

// --- the wild arrangements (NOT corpus layouts) -----------------------------

// Five production layouts reproduced by arrangement only -- which labels appear, where, and at
// what token granularity. No bytes are copied. Scrubbed per docs/extraction-corpus.md
// "Scrubbing an anonymised real document" by Claude Opus 5 on 2026-09-06 (the four text layers)
// and 2026-09-07 (the image-only one); step 7's second-person confirmation is recorded on the
// pull request, not here.
//
// Deliberately outside corpusPrefix: they gain no corpusExpect, corpusLayouts, corpusTokenFloor
// or t1aGaps entry, so no Tier-1 number moves. internal/extraction/endtoend scores them.
const (
	fxWildTwoParty = "wild_two_party_bare_tin.pdf"
	fxWildRuled    = "wild_ruled_lines_totals.pdf"
	fxWildRCNaira  = "wild_rc_due_naira.pdf"
	fxWildStacked  = "wild_stacked_borderless.pdf"
	fxWildScanned  = "wild_scanned_no_number.pdf"

	// Each twin's geometry under its source document's printed labels.
	fxWildTwoPartyAsPrinted = "wild_two_party_bare_tin_asprinted.pdf"
	fxWildRuledAsPrinted    = "wild_ruled_lines_totals_asprinted.pdf"
	fxWildStackedAsPrinted  = "wild_stacked_borderless_asprinted.pdf"
)

// The pinned synthetic identifier table. Every literal is freshly minted, never observed; the
// TINs continue the free reserved block past 99999999-0702 and avoid -0001..-0009. The same
// table is declared in endtoend/goldens_test.go, which holds the two copies together.
const (
	fxWildTINSupplierTwoParty = "99999999-0801"
	fxWildTINBuyerTwoParty    = "99999999-0802"
	fxWildTINSupplierRuled    = "99999999-0901"
	fxWildTINBuyerRuled       = "99999999-0902"
	fxWildTINSupplierRCNaira  = "99999999-1001"
	fxWildTINBuyerRCNaira     = "99999999-1002"
	fxWildTINSupplierStacked  = "99999999-1101"
	fxWildTINBuyerStacked     = "99999999-1102"
	fxWildTINSupplierScanned  = "99999999-1201"
	fxWildTINBuyerScanned     = "99999999-1202"

	fxWildInvTwoParty = "INV-2101"
	fxWildInvRuled    = "INV-2102"
	fxWildInvRCNaira  = "INV-2103"
	fxWildInvStacked  = "INV-2104"

	// A sibling's number is its twin's plus 100.
	fxWildInvTwoPartyAsPrinted = "INV-2201"
	fxWildInvRuledAsPrinted    = "INV-2202"
	fxWildInvStackedAsPrinted  = "INV-2204"

	// An RC (Corporate Affairs Commission registration) number is an "other identifier" under
	// step 3 of the scrubbing procedure and is replaced like a TIN.
	fxWildRCNumber = "RC-000142"

	fxWildSupplier = "Adeyemi Trading Limited"
	fxWildBuyer    = "Honeywell Group"

	// The raster font is uppercase-only (fxGlyphs), and normalizeName preserves case, so the
	// scanned arrangement's cast is scored in these forms.
	fxWildSupplierUpper = "ADEYEMI TRADING LIMITED"
	fxWildBuyerUpper    = "HONEYWELL GROUP"
)

// fxQuoteFont is fxHelvetica plus a /ToUnicode CMap for byte 0x27. Under StandardEncoding
// pdfium reads quoteright (U+2019) there and docling reads the ASCII apostrophe; the CMap
// settles both readers on U+0027, which TestWildGoldens_DescribeTheirPDF holds them to.
//
// ceiling: docling 1.10.0 normalises U+2018/U+2019 to ASCII whatever the PDF says -- measured
// over five encodings, including /ToUnicode <27> <2019>. Revisit on a sidecar bump.
func fxQuoteFont(toUnicode int) string {
	return fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode %d 0 R >>", toUnicode)
}

// fxQuoteTextPage is fxTextPage over that font. fxAssemble numbers by slice index, so the
// appended CMap is object 6.
func fxQuoteTextPage(lines ...fxLine) []byte {
	const cmapObj = 6
	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxFontRes(5), 4),
		fxStream(fxText(lines...)),
		fxObject(fxQuoteFont(cmapObj)),
		fxUCS2CMap("Quote", [][2]string{{"27", "0027"}}),
	})
}

// fxBuildWildTwoPartyBareTIN puts the supplier and buyer blocks side by side, each ending in a
// TIN label whose value is a separate Tj. The friendly set labels both "TIN:", heads the buyer
// block "Invoice to" and puts "Customer No." above and "Buyer's Signature" below its name. The
// apostrophe is why the page needs fxQuoteFont rather than fxTextPage.
func fxBuildWildTwoPartyBareTIN(l fxWildTwoPartyLabels) []byte {
	return fxQuoteTextPage(fxWildTwoPartyLines(l)...)
}

// fxWildTwoPartyLabels is every label the two-party page prints, plus its invoice number.
type fxWildTwoPartyLabels struct {
	title, invoiceNo, invNum, invoiceDate, supplierTIN, invoiceTo, customerNo, buyerTIN, signature, currency, subtotal, vat, total string
}

var (
	fxWildTwoPartyFriendly = fxWildTwoPartyLabels{
		title: "INVOICE", invoiceNo: "Invoice No: ", invNum: fxWildInvTwoParty, invoiceDate: "Invoice Date: ",
		supplierTIN: "TIN:", invoiceTo: "Invoice to", customerNo: "Customer No.", buyerTIN: "TIN:",
		signature: "Buyer's Signature", currency: "Currency: NGN", subtotal: "Sub-total", vat: "VAT", total: "Total",
	}
	// NG-2's printed labels. The signature is NG-5's, the source the twin's paraphrase came from.
	fxWildTwoPartyPrinted = fxWildTwoPartyLabels{
		title: "Sales Invoice", invoiceNo: "Invoice No: ", invNum: fxWildInvTwoPartyAsPrinted, invoiceDate: "Invoice Date: ",
		supplierTIN: "VAT Reg. No:", invoiceTo: "INVOICE TO:", customerNo: "Customer No.", buyerTIN: "TIN:",
		signature: "Customer's Signature", currency: "Currency: NGN", subtotal: "Net Amount", vat: "VAT @ 7.5%", total: "Total NGN",
	}
)

func fxWildTwoPartyLines(l fxWildTwoPartyLabels) []fxLine {
	return []fxLine{
		{24, 72, 720, l.title},
		{12, 72, 690, l.invoiceNo + l.invNum},
		{12, 72, 672, l.invoiceDate + "2026-06-11"},
		{12, 72, 630, fxWildSupplier},
		{12, 72, 614, l.supplierTIN}, {12, 160, 614, fxWildTINSupplierTwoParty},
		{12, 360, 646, l.invoiceTo},
		{12, 360, 630, l.customerNo},
		{12, 360, 614, fxWildBuyer},
		{12, 360, 598, l.buyerTIN}, {12, 448, 598, fxWildTINBuyerTwoParty},
		{12, 360, 560, l.signature},
		{12, 72, 582, l.currency},
		{12, 72, 240, l.subtotal}, {12, 220, 240, "1,200.00"},
		{12, 72, 222, l.vat}, {12, 220, 222, "90.00"},
		{12, 72, 204, l.total}, {12, 220, 204, "1,290.00"},
	}
}

// fxWildRuledColXs are the five-column table's vertical rule positions (6 boundaries).
// fxWildRuledRowYs are its horizontal rules: header top, header/row1, row1/row2, row2/row3,
// bottom. Separate arrays from fxTableColXs/fxRichTableRowYs -- resizing those would change
// table_invoice.pdf's and rich_invoice.pdf's committed bytes.
var (
	fxWildRuledColXs = [6]int{72, 116, 290, 340, 430, 540}
	fxWildRuledRowYs = [5]int{512, 488, 464, 440, 416}

	// The decoration the Nigerian source mock-ups print. "RATE (N)" is escaped so the balanced
	// parens in the PDF string literal are explicit; "Amount " + fxNaira must stay one Tj,
	// because a lone \244 Tj emits no token at all.
	fxWildRuledHeader = []string{"S/N", "DESCRIPTION OF GOODS", "QTY", `RATE \(N\)`, "Amount " + fxNaira}
	fxWildRuledBody   = [][]string{
		{"1", "Steel Rods", "4", "1,000.00", "4,000.00"},
		{"2", "Cement Bags", "6", "500.00", "3,000.00"},
		{"3", "Roofing Sheets", "2", "500.00", "1,000.00"},
	}
)

// The measured production header. Unescaped, unlike its ruled neighbour: nothing draws this
// into a PDF, so it carries no builder and no committed bytes.
var fxDenseIndexHeader = []string{"Item", "Service description", "Reference", "Qty", "Unit rate ₦", "Amount ₦", "VAT ₦"}

// fxWildRuledRowText lays one five-column row on a baseline, 4pt into each column.
func fxWildRuledRowText(baseline int, cells []string) []fxLine {
	lines := make([]fxLine, len(cells))
	for i, text := range cells {
		lines[i] = fxLine{10, fxWildRuledColXs[i] + 4, baseline, text}
	}
	return lines
}

// fxBuildWildRuledLinesTotals is a ruled five-column line-item table whose Total label continues
// on the last data row's own baseline, the way a ruled invoice prints a continuing totals row.
// t1.total.right therefore reaches that row's 1,000.00 and not the printed 8,600.00. The totals
// corroborate (8,000.00 + 600.00 = 8,600.00) and the line amount does not, so the fixture can
// tell a corroborated pick from a positional one.
func fxBuildWildRuledLinesTotals(l fxWildRuledLabels) []byte {
	return fxWildRuledPage(fxWildRuledLines(l)...)
}

// fxWildRuledLabels is every label the ruled page prints, plus its invoice number.
type fxWildRuledLabels struct {
	title, invoiceNo, invNum, invoiceDate, supplierTIN, supplier, buyerTIN, buyer, currency string
	header                                                                                  []string
	subtotal, vat, total                                                                    string
}

var (
	fxWildRuledFriendly = fxWildRuledLabels{
		title: "INVOICE", invoiceNo: "Invoice No: ", invNum: fxWildInvRuled, invoiceDate: "Invoice Date: ",
		supplierTIN: "Supplier TIN: ", supplier: "Supplier: ", buyerTIN: "Buyer TIN: ", buyer: "Buyer: ", currency: "Currency: NGN",
		header: fxWildRuledHeader, subtotal: "Sub-total", vat: "VAT", total: "Total",
	}
	// NG-4's printed labels. Total stays: TOTAL DUE (NGN) merges with row 3's amount here.
	fxWildRuledPrinted = fxWildRuledLabels{
		title: "MONTHLY SERVICE INVOICE", invoiceNo: "Invoice No: ", invNum: fxWildInvRuledAsPrinted, invoiceDate: "Invoice Date: ",
		supplierTIN: "Supplier TIN: ", supplier: "Supplier: ", buyerTIN: "Buyer TIN: ", buyer: "Buyer: ", currency: "Currency: NGN",
		header:   []string{"Item", "Service description", "Qty", "Unit rate " + fxNaira, "Amount " + fxNaira},
		subtotal: "Taxable amount", vat: "VAT @ 7.5%", total: "Total",
	}
)

func fxWildRuledLines(l fxWildRuledLabels) []fxLine {
	lines := []fxLine{
		{24, 72, 720, l.title},
		{12, 72, 690, l.invoiceNo + l.invNum},
		{12, 72, 672, l.invoiceDate + "2026-06-24"},
		{12, 72, 654, l.supplierTIN + fxWildTINSupplierRuled},
		{12, 72, 636, l.supplier + fxWildSupplier},
		{12, 72, 618, l.buyerTIN + fxWildTINBuyerRuled},
		{12, 72, 600, l.buyer + fxWildBuyer},
		{12, 72, 582, l.currency},
	}
	lines = append(lines, fxWildRuledRowText(500, l.header)...)
	lines = append(lines, fxWildRuledRowText(476, fxWildRuledBody[0])...)
	lines = append(lines, fxWildRuledRowText(452, fxWildRuledBody[1])...)
	lines = append(lines, fxWildRuledRowText(428, fxWildRuledBody[2])...)
	return append(lines,
		fxLine{12, 380, 404, l.subtotal}, fxLine{12, 500, 404, "8,000.00"},
		fxLine{12, 380, 386, l.vat}, fxLine{12, 500, 386, "600.00"},
		// The Total label continues on the last data row's own baseline, so t1.total.right
		// reaches the line amount beside it and the printed 8,600.00 falls outside every
		// total relation. TestWildLayouts_TheRuledTableTotalStillTakesTheLineAmount.
		fxLine{12, 380, 428, l.total}, fxLine{12, 500, 368, "8,600.00"},
	)
}

// fxWildRuledPage draws lines over the ruled table's rules; neither fxTextPage nor
// fxNairaTextPage builds a naira font AND rules, so the objects are assembled directly (CMap is 6).
func fxWildRuledPage(lines ...fxLine) []byte {
	// H before V, matching fxBuildTable's own loop shape.
	var rules bytes.Buffer
	for _, y := range fxWildRuledRowYs {
		rules.WriteString(fxRuleH(y, fxWildRuledColXs[0], fxWildRuledColXs[len(fxWildRuledColXs)-1]))
	}
	for _, x := range fxWildRuledColXs {
		rules.WriteString(fxRuleV(x, fxWildRuledRowYs[len(fxWildRuledRowYs)-1], fxWildRuledRowYs[0]))
	}

	content := fxText(lines...)
	content = append(content, rules.Bytes()...)

	const cmapObj = 6
	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxFontRes(5), 4),
		fxStream(content),
		fxObject(fxNairaFont(cmapObj)),
		fxNairaCMap(),
	})
}

// fxBuildWildRCDueNaira prints an RC number between the VAT label and its amount, "Due Date"
// above "Issue Date", and every amount prefixed with a naira. There is no "Currency:" label at
// all: the naira is the only currency marker here, which is what keeps the two naira roles
// apart from fxBuildWildRuledLinesTotals'. Symbol and amount stay in ONE Tj -- a lone \244 Tj
// emits no token, and a split pair drops the symbol.
func fxBuildWildRCDueNaira() []byte {
	return fxNairaTextPage(true,
		fxLine{24, 72, 720, "INVOICE"},
		fxLine{12, 72, 690, "Invoice No: " + fxWildInvRCNaira},
		fxLine{12, 72, 666, "Due Date"}, fxLine{12, 220, 666, "2026-08-07"},
		fxLine{12, 72, 648, "Issue Date"}, fxLine{12, 220, 648, "2026-07-08"},
		fxLine{12, 72, 624, "Supplier TIN: " + fxWildTINSupplierRCNaira},
		fxLine{12, 72, 606, "Supplier: " + fxWildSupplier},
		fxLine{12, 72, 588, "Buyer TIN: " + fxWildTINBuyerRCNaira},
		fxLine{12, 72, 570, "Buyer: " + fxWildBuyer},
		fxLine{12, 72, 258, "Sub-total"}, fxLine{12, 220, 258, fxNaira + " 2,500.00"},
		fxLine{12, 72, 240, "RC NUMBER: " + fxWildRCNumber},
		fxLine{12, 72, 222, "VAT"}, fxLine{12, 220, 222, fxNaira + " 187.50"},
		fxLine{12, 72, 204, "Total"}, fxLine{12, 220, 204, fxNaira + " 2,687.50"},
	)
}

// fxBuildWildStackedBorderless stacks labels and values with no rules and no "Label:"
// punctuation, and offsets every value 8pt below its label in a second column; only right's
// drop band reaches the six offset values (TestTier1_TheDropBandStaysInsideItsMeasuredWindow).
//
// The friendly set keeps "Invoice No" readable on purpose, over its value at the same x
// (fxBuildCorpusStackedLabels); the as-printed set letter-spaces it as NG-3 prints it.
func fxBuildWildStackedBorderless(l fxWildStackedLabels) []byte {
	return fxTextPage(fxWildStackedLines(l)...)
}

// fxWildStackedLabels is every label the stacked page prints, plus its invoice number.
type fxWildStackedLabels struct {
	title, invoiceNo, invNum, issueDate, buyer, buyerTIN, supplier, supplierTIN, currency, subtotal, vat, total string
}

var (
	fxWildStackedFriendly = fxWildStackedLabels{
		title: "INVOICE", invoiceNo: "Invoice No", invNum: fxWildInvStacked, issueDate: "Issue Date",
		buyer: "Buyer", buyerTIN: "Buyer TIN", supplier: "Supplier", supplierTIN: "Supplier TIN", currency: "Currency",
		subtotal: "Sub total", vat: "VAT", total: "Total",
	}
	// NG-3's printed labels, letter-spaced where fxBuildAdvisoryRegister's R0 spaces them.
	fxWildStackedPrinted = fxWildStackedLabels{
		title: "Invoice", invoiceNo: `I N V O I C E N U M B E R`, invNum: fxWildInvStackedAsPrinted, issueDate: `I S S U E D`,
		buyer: "BILLED TO", buyerTIN: "Buyer TIN", supplier: "FROM", supplierTIN: "Supplier TIN", currency: "CURRENCY",
		subtotal: "Subtotal", vat: "VAT 7.5%", total: "Amount payable",
	}
)

func fxWildStackedLines(l fxWildStackedLabels) []fxLine {
	return []fxLine{
		{24, 72, 720, l.title},
		{12, 72, 690, l.invoiceNo},
		{12, 72, 674, l.invNum},
		{12, 72, 620, l.issueDate}, {12, 300, 612, "2026-07-30"},
		{12, 72, 596, l.buyer}, {12, 300, 588, fxWildBuyer},
		{12, 72, 572, l.buyerTIN}, {12, 300, 564, fxWildTINBuyerStacked},
		{12, 72, 548, l.supplier}, {12, 300, 540, fxWildSupplier},
		{12, 72, 524, l.supplierTIN}, {12, 300, 516, fxWildTINSupplierStacked},
		{12, 72, 500, l.currency}, {12, 300, 492, "NGN"},
		{12, 72, 260, l.subtotal}, {12, 300, 252, "1,500.00"},
		{12, 72, 236, l.vat}, {12, 300, 228, "112.50"},
		{12, 72, 212, l.total}, {12, 300, 204, "1,612.50"},
	}
}

// fxWildSibling is an as-printed sibling: its builder, and the page both members draw on.
type fxWildSibling struct {
	name, twin string
	build      func() []byte
	page       func(lines ...fxLine) []byte
}

// fxWildSiblings are the three as-printed siblings. TestFixtures_EachAsPrintedSiblingKeepsItsTwinsGeometry.
var fxWildSiblings = []fxWildSibling{
	{fxWildTwoPartyAsPrinted, fxWildTwoParty, func() []byte { return fxBuildWildTwoPartyBareTIN(fxWildTwoPartyPrinted) }, fxQuoteTextPage},
	{fxWildRuledAsPrinted, fxWildRuled, func() []byte { return fxBuildWildRuledLinesTotals(fxWildRuledPrinted) }, fxWildRuledPage},
	{fxWildStackedAsPrinted, fxWildStacked, func() []byte { return fxBuildWildStackedBorderless(fxWildStackedPrinted) }, fxTextPage},
}

// --- the raster half --------------------------------------------------------

// The dense fixture is drawn as pixels, not as text operators: OCR only has work to do if the
// glyphs are ink. 1275x1651 is exactly pdfium's 150-DPI grid for US-Letter
// (pdfium_render_test.go's prLetterWidthPx/prLetterHeightPx), so its render resamples nothing.
const (
	fxRasterW = 1275
	fxRasterH = 1651
)

// The bitmap font cell. fxGlyphAdvance leaves one blank column between glyphs.
const (
	fxGlyphW       = 5
	fxGlyphH       = 7
	fxGlyphAdvance = 6
)

// fxGlyphs is a 5x7 dot-matrix font: the set bits draw the glyph, so a wrong pixel is visible in
// the source. Uppercase only -- 5x7 lowercase with descenders reads far worse under OCR than the
// all-caps an invoice prints anyway. Indexed, never ranged, so it cannot reorder any output.
var fxGlyphs = map[byte][fxGlyphH]uint8{
	' ': {0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00000},
	'.': {0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b01100, 0b01100},
	',': {0b00000, 0b00000, 0b00000, 0b00000, 0b01100, 0b00100, 0b01000},
	'-': {0b00000, 0b00000, 0b00000, 0b11111, 0b00000, 0b00000, 0b00000},
	':': {0b00000, 0b01100, 0b01100, 0b00000, 0b01100, 0b01100, 0b00000},
	'(': {0b00010, 0b00100, 0b01000, 0b01000, 0b01000, 0b00100, 0b00010},
	')': {0b01000, 0b00100, 0b00010, 0b00010, 0b00010, 0b00100, 0b01000},
	'%': {0b11001, 0b11010, 0b00010, 0b00100, 0b01000, 0b01011, 0b10011},
	'0': {0b01110, 0b10001, 0b10011, 0b10101, 0b11001, 0b10001, 0b01110},
	'1': {0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'2': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b01000, 0b11111},
	'3': {0b11111, 0b00010, 0b00100, 0b00010, 0b00001, 0b10001, 0b01110},
	'4': {0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010},
	'5': {0b11111, 0b10000, 0b11110, 0b00001, 0b00001, 0b10001, 0b01110},
	'6': {0b00110, 0b01000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110},
	'7': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000},
	'8': {0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110},
	'9': {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100},
	'A': {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'B': {0b11110, 0b10001, 0b10001, 0b11110, 0b10001, 0b10001, 0b11110},
	'C': {0b01110, 0b10001, 0b10000, 0b10000, 0b10000, 0b10001, 0b01110},
	'D': {0b11110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b11110},
	'E': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b11111},
	'F': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b10000},
	'G': {0b01110, 0b10001, 0b10000, 0b10111, 0b10001, 0b10001, 0b01111},
	'H': {0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'I': {0b01110, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'J': {0b00111, 0b00010, 0b00010, 0b00010, 0b00010, 0b10010, 0b01100},
	'K': {0b10001, 0b10010, 0b10100, 0b11000, 0b10100, 0b10010, 0b10001},
	'L': {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b11111},
	'M': {0b10001, 0b11011, 0b10101, 0b10101, 0b10001, 0b10001, 0b10001},
	'N': {0b10001, 0b10001, 0b11001, 0b10101, 0b10011, 0b10001, 0b10001},
	'O': {0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'P': {0b11110, 0b10001, 0b10001, 0b11110, 0b10000, 0b10000, 0b10000},
	'Q': {0b01110, 0b10001, 0b10001, 0b10001, 0b10101, 0b10010, 0b01101},
	'R': {0b11110, 0b10001, 0b10001, 0b11110, 0b10100, 0b10010, 0b10001},
	'S': {0b01111, 0b10000, 0b10000, 0b01110, 0b00001, 0b00001, 0b11110},
	'T': {0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100},
	'U': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'V': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01010, 0b00100},
	'W': {0b10001, 0b10001, 0b10001, 0b10101, 0b10101, 0b10101, 0b01010},
	'X': {0b10001, 0b10001, 0b01010, 0b00100, 0b01010, 0b10001, 0b10001},
	'Y': {0b10001, 0b10001, 0b01010, 0b00100, 0b00100, 0b00100, 0b00100},
	'Z': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b10000, 0b11111},
}

// fxCanvas is a one-byte-per-pixel ink mask; pack turns it into the 1-bit image once, at the end.
type fxCanvas struct {
	w, h int
	ink  []byte
}

func fxNewCanvas(w, h int) *fxCanvas { return &fxCanvas{w: w, h: h, ink: make([]byte, w*h)} }

// fill inks a half-open rectangle, clipped to the canvas.
func (c *fxCanvas) fill(x0, y0, x1, y1 int) {
	for y := max(y0, 0); y < min(y1, c.h); y++ {
		for x := max(x0, 0); x < min(x1, c.w); x++ {
			c.ink[y*c.w+x] = 1
		}
	}
}

// draw lays a string with each glyph cell scaled by scale; x,y is the first cell's top-left. An
// unmapped byte panics rather than drawing nothing: a typo must not ship as a silent hole.
func (c *fxCanvas) draw(s string, x, y, scale int) {
	for i := range len(s) {
		rows, ok := fxGlyphs[s[i]]
		if !ok {
			panic(fmt.Sprintf("fixtures: no glyph for %q in %q", s[i], s))
		}
		gx := x + i*fxGlyphAdvance*scale
		for r, bits := range rows {
			for col := range fxGlyphW {
				if bits&(1<<(fxGlyphW-1-col)) != 0 {
					c.fill(gx+col*scale, y+r*scale, gx+(col+1)*scale, y+(r+1)*scale)
				}
			}
		}
	}
}

// fxTextW is a drawn string's ink width: the trailing inter-glyph column is not part of it.
func fxTextW(s string, scale int) int { return len(s)*fxGlyphAdvance*scale - scale }

// drawRight lays a string ending at x.
func (c *fxCanvas) drawRight(s string, x, y, scale int) { c.draw(s, x-fxTextW(s, scale), y, scale) }

// pack emits 1-bit /DeviceGray rows: bit 1 is white, bit 0 is ink. The row padding bits stay
// white, so no black sliver appears down the right edge.
func (c *fxCanvas) pack() []byte {
	stride := (c.w + 7) / 8
	out := bytes.Repeat([]byte{0xFF}, stride*c.h)
	for y := range c.h {
		for x := range c.w {
			if c.ink[y*c.w+x] != 0 {
				out[y*stride+x/8] &^= 0x80 >> (x % 8)
			}
		}
	}
	return out
}

// fxRunLength encodes src with the PDF /RunLengthDecode filter: a length byte 0..127 means the
// next n+1 bytes are literal, 129..255 repeats the next byte 257-n times, 128 is EOD. Hand-rolled
// rather than compress/flate because these bytes are byte-compared and flate's output is not
// promised stable across Go releases.
func fxRunLength(src []byte) []byte {
	out := make([]byte, 0, len(src)/8)
	for i := 0; i < len(src); {
		j := i + 1
		for j < len(src) && src[j] == src[i] && j-i < 128 {
			j++
		}
		if j-i >= 2 {
			out = append(out, byte(257-(j-i)), src[i])
			i = j
			continue
		}
		k := i
		for k < len(src) && k-i < 128 {
			if k+2 < len(src) && src[k] == src[k+1] && src[k+1] == src[k+2] {
				break
			}
			k++
		}
		out = append(out, byte(k-i-1))
		out = append(out, src[i:k]...)
		i = k
	}
	return append(out, 128)
}

// fxImageObjectRLE is a 1-bit /DeviceGray XObject under /RunLengthDecode. Uncompressed 8-bit
// would commit ~2 MB for one page of line art.
func fxImageObjectRLE(w, h int, packed []byte) fxObject {
	body := fxRunLength(packed)
	var b bytes.Buffer
	fmt.Fprintf(&b, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /RunLengthDecode /Length %d >>\nstream\n", w, h, len(body))
	b.Write(body)
	b.WriteString("\nendstream")
	return b.Bytes()
}

// fxDenseRows are the line items. Every amount is qty x unit price and stays under 1,000,000 so
// it fits its column at this glyph scale; the grand total sits on its own row and need not.
var fxDenseRows = [][4]string{
	{"CEMENT 50KG BAG", "120", "7,850.00", "942,000.00"},
	{"STEEL ROD 12MM", "40", "18,250.00", "730,000.00"},
	{"ROOFING SHEET 0.5MM", "85", "9,400.00", "799,000.00"},
	{"PVC PIPE 110MM", "150", "3,275.00", "491,250.00"},
	{"PAINT 20L EMULSION", "60", "12,500.00", "750,000.00"},
	{"HARDWOOD PLANK 4M", "95", "6,300.00", "598,500.00"},
}

// fxDenseCols are the table's five vertical rules; fxDenseRights are the anchors the three
// numeric columns are right-aligned to, 10 px inside their cell.
var (
	fxDenseCols   = [5]int{45, 560, 700, 970, 1235}
	fxDenseRights = [3]int{690, 960, 1225}
)

const (
	fxDenseTableTop = 606 // the rule above the header row
	fxDenseHeadRule = 656 // the rule under it, and row 0's top
	fxDenseRowPitch = 44
	fxDenseRule     = 3 // grid line thickness
	fxDenseCellPad  = 8 // a row's top to its glyph cell's top
	fxDenseBody     = 4 // body glyph scale: 28 px tall, ~13 pt at 150 DPI
)

// fxBuildDense is the p95 fixture: one US-Letter page of Nigerian-shaped invoice content drawn
// entirely as raster glyphs, so it reads as a scan and OCR has to do the work. Its TINs are the
// NNNNNNNN-NNNN FIRS shape (internal/portfolio/tin.go) and pass that package's Luhn check;
// neither the TINs nor the company names appear in db/seed.dev.sql, so a seed edit cannot
// silently change what this document means, and neither is in the 99999999-* block the mock APP
// adapter reserves as submission triggers.
func fxBuildDense() []byte {
	c := fxNewCanvas(fxRasterW, fxRasterH)

	c.draw("INVOICE", 45, 55, 8)
	c.fill(45, 130, 1235, 136)

	c.draw("KADUNA SUPPLY LTD", 45, 165, 5)
	c.draw("27 ALI AKILU ROAD", 45, 215, fxDenseBody)
	c.draw("KADUNA, KADUNA STATE", 45, 253, fxDenseBody)
	c.draw("TIN: 30154829-0032", 45, 291, fxDenseBody)
	c.draw("RC NUMBER: RC-441209", 45, 329, fxDenseBody)

	c.draw("INVOICE NO: ASC-2026-0417", 635, 165, fxDenseBody)
	c.draw("ISSUE DATE: 12 AUG 2026", 635, 203, fxDenseBody)
	c.draw("DUE DATE: 11 SEP 2026", 635, 241, fxDenseBody)
	c.draw("CURRENCY: NGN", 635, 279, fxDenseBody)
	c.draw("PO NUMBER: PO-88213", 635, 317, fxDenseBody)

	c.draw("BILL TO:", 45, 390, fxDenseBody)
	c.draw("ENUGU CERAMICS LIMITED", 45, 428, 5)
	c.draw("5 OGUI ROAD, ENUGU", 45, 478, fxDenseBody)
	c.draw("ENUGU STATE, NIGERIA", 45, 516, fxDenseBody)
	c.draw("TIN: 40287316-0012", 45, 554, fxDenseBody)

	// A fully ruled grid, so a table detector sees a table and not a block of text.
	bottom := fxDenseHeadRule + fxDenseRowPitch*len(fxDenseRows)
	c.fill(fxDenseCols[0], fxDenseTableTop, fxDenseCols[4], fxDenseTableTop+fxDenseRule)
	for i := range len(fxDenseRows) + 1 {
		y := fxDenseHeadRule + fxDenseRowPitch*i
		c.fill(fxDenseCols[0], y, fxDenseCols[4], y+fxDenseRule)
	}
	for _, x := range fxDenseCols {
		c.fill(x, fxDenseTableTop, x+fxDenseRule, bottom+fxDenseRule)
	}

	head := fxDenseTableTop + fxDenseRule + fxDenseCellPad
	c.draw("DESCRIPTION", fxDenseCols[0]+10, head, fxDenseBody)
	for i, label := range [3]string{"QTY", "UNIT PRICE", "AMOUNT"} {
		c.drawRight(label, fxDenseRights[i], head, fxDenseBody)
	}
	for i, row := range fxDenseRows {
		y := fxDenseHeadRule + fxDenseRowPitch*i + fxDenseRule + fxDenseCellPad
		c.draw(row[0], fxDenseCols[0]+10, y, fxDenseBody)
		for j := range fxDenseRights {
			c.drawRight(row[j+1], fxDenseRights[j], y, fxDenseBody)
		}
	}

	tot := bottom + fxDenseRule
	c.drawRight("SUBTOTAL", 900, tot+30, fxDenseBody)
	c.drawRight("4,310,750.00", 1225, tot+30, fxDenseBody)
	c.drawRight("VAT 7.5%", 900, tot+72, fxDenseBody)
	c.drawRight("323,306.25", 1225, tot+72, fxDenseBody)
	c.fill(700, tot+112, 1235, tot+114)
	c.drawRight("TOTAL DUE (NGN)", 900, tot+128, fxDenseBody)
	c.drawRight("4,634,056.25", 1225, tot+128, fxDenseBody)
	c.fill(700, tot+170, 1235, tot+176)

	c.draw("REMIT TO ACCOUNT 3081447726", 45, 1160, fxDenseBody)
	c.draw("SORT CODE 011152303", 45, 1198, fxDenseBody)
	c.draw("PAYMENT DUE WITHIN 30 DAYS", 45, 1236, fxDenseBody)
	c.draw("ISSUED UNDER THE FIRS", 45, 1300, fxDenseBody)
	c.draw("E-INVOICING REGULATIONS 2026", 45, 1338, fxDenseBody)

	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxImageRes(5), 4),
		fxStream([]byte(fxImageDraw)),
		fxImageObjectRLE(fxRasterW, fxRasterH, c.pack()),
	})
}

// --- the image-only wild arrangement ----------------------------------------

// The values wild_scanned_no_number.pdf draws as ink. Declared here so the endtoend expectation
// table can be checked against what was DRAWN as well as against what OCR returned; a table
// copied out of the golden and a golden regenerated from a corrupt page agree with each other.
const (
	fxWildScannedIssueDate = "2026-08-14"
	fxWildScannedCurrency  = "NGN"
	fxWildScannedSubtotal  = "1,800.00"
	fxWildScannedVAT       = "135.00"
	fxWildScannedTotal     = "1,935.00"
)

// fxBuildWildScannedNoNumber is the scanned arrangement whose import quarantines for a missing
// invoice number: real raster ink OCR can read, and no INVOICE NO line anywhere. It reuses
// fxBuildDense's canvas so the page lands on pdfium's exact 150-DPI US-Letter grid.
//
// Three geometry rules, each measured against a probe that broke it: no two drawn strings share
// a y (two columns on one baseline merge into one token), one glyph scale for all body text
// (mixing scales split a name across two tokens), and no % character (VAT 7.5% OCR'd as %S2).
func fxBuildWildScannedNoNumber() []byte {
	c := fxNewCanvas(fxRasterW, fxRasterH)

	c.draw("INVOICE", 45, 55, 8)
	c.fill(45, 130, 1235, 136)

	c.draw(fxWildSupplierUpper, 45, 165, fxDenseBody)
	c.draw("14 MARINA STREET", 45, 203, fxDenseBody)
	c.draw("LAGOS ISLAND, LAGOS STATE", 45, 241, fxDenseBody)
	c.draw("TIN: "+fxWildTINSupplierScanned, 45, 279, fxDenseBody)

	// The right column is staggered between the left column's rows, never level with one.
	c.draw("ISSUE DATE: "+fxWildScannedIssueDate, 635, 317, fxDenseBody)
	c.draw("CURRENCY: "+fxWildScannedCurrency, 635, 355, fxDenseBody)

	c.draw("BILL TO:", 45, 430, fxDenseBody)
	c.draw(fxWildBuyerUpper, 45, 468, fxDenseBody)
	c.draw("7 AWOLOWO ROAD, IKOYI", 45, 506, fxDenseBody)
	c.draw("TIN: "+fxWildTINBuyerScanned, 45, 544, fxDenseBody)

	c.drawRight("SUBTOTAL", 900, 700, fxDenseBody)
	c.drawRight(fxWildScannedSubtotal, 1225, 700, fxDenseBody)
	c.drawRight("VAT", 900, 742, fxDenseBody)
	c.drawRight(fxWildScannedVAT, 1225, 742, fxDenseBody)
	c.fill(700, 782, 1235, 784)
	c.drawRight("TOTAL DUE", 900, 798, fxDenseBody)
	c.drawRight(fxWildScannedTotal, 1225, 798, fxDenseBody)

	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxImageRes(5), 4),
		fxStream([]byte(fxImageDraw)),
		fxImageObjectRLE(fxRasterW, fxRasterH, c.pack()),
	})
}

// --- the advisory arrangements (NOT corpus layouts) -------------------------

// Faithful transcriptions of two Nigerian invoice mock-ups (arch-26-06 Appendix C), TINs swapped
// into the free reserved block. Neither is registered in requiredPDFs, expectByLayout,
// corpusExpect, corpusLayouts or corpusTokenFloor -- no score constant and no doc table moves.
const (
	fxAdvisoryRegister         = "advisory_register.pdf"
	fxAdvisoryRegisterUnspaced = "advisory_register_unspaced.pdf"
	fxAdvisoryDense            = "advisory_dense.pdf"
	fxChromeRegister           = "chrome_register.pdf"
	fxChromeRegisterTwin       = "chrome_register_twin.pdf"
)

// fxNairaTextPages is fxNairaTextPage generalised to N pages sharing one naira font -- the
// register's second page. Object numbering follows fxBuildNative3Page: page p's content sits at
// object 2+2p, the shared font at 2+2n+1, the CMap (if any) right after.
func fxNairaTextPages(withCMap bool, pages ...[]fxLine) []byte {
	fontObj := 2 + 2*len(pages) + 1
	toUnicode := 0
	if withCMap {
		toUnicode = fontObj + 1
	}

	objs := make([]fxObject, 2, 2*len(pages)+4)
	kids := make([]string, len(pages))
	for p, lines := range pages {
		pageObj := 3 + 2*p
		kids[p] = fmt.Sprintf("%d 0 R", pageObj)
		objs = append(objs, fxPage(fxFontRes(fontObj), pageObj+1), fxStream(fxText(lines...)))
	}
	objs[0] = fxObject("<< /Type /Catalog /Pages 2 0 R >>")
	objs[1] = fxObject(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	objs = append(objs, fxObject(fxNairaFont(toUnicode)))
	if withCMap {
		objs = append(objs, fxNairaCMap())
	}
	return fxAssemble(objs)
}

// fxBuildAdvisoryRegister is R0 (arch-26-06 Appendix C), the NG-3 advisory register mock-up
// transcribed faithful. unspaced=true builds R1: the ONLY difference is the two letter-spaced
// header labels.
func fxBuildAdvisoryRegister(unspaced bool) []byte {
	invoiceNumberLabel := `I N V O I C E   N U M B E R`
	issuedLabel := `I S S U E D`
	if unspaced {
		invoiceNumberLabel = "Invoice number"
		issuedLabel = "Issued"
	}

	page1 := []fxLine{
		{10, 65, 721, `OKONKWO ADVISORY PARTNERS`},
		{26, 65, 657, `Invoice`},
		{6, 65, 610, invoiceNumberLabel},
		{9, 65, 592, `OAP/2026/0088`},
		{6, 224, 610, issuedLabel},
		{9, 224, 592, `2026-09-01`},
		{6, 383, 610, `DUE`},
		{9, 383, 592, `2026-09-15`},
		{6, 65, 553, `CURRENCY`},
		{9, 65, 536, `NGN`},
		{6, 65, 497, `FROM`},
		{9, 65, 479, `Okonkwo Advisory Partners`},
		{7, 65, 461, `4th Floor, Alfred Rewane Road`},
		{7, 65, 443, `Ikoyi, Lagos`},
		{7, 65, 425, `TIN 99999999-1311`},
		{7, 65, 407, `RC 1667402`},
		{6, 262, 497, `BILLED TO`},
		{9, 262, 479, `Honeywell Group Nigeria Plc`},
		{7, 262, 461, `Finance Department`},
		{7, 262, 443, `2 Adeyemo Alakija Street, Victoria Island`},
		{7, 262, 425, `Lagos`},
		{7, 262, 407, `TIN 99999999-1312`},
		{8, 65, 358, `Transfer pricing documentation review`},
		{7, 65, 344, `Engagement TP-2026-14 - 62 hours`},
		{8, 473, 358, `\2447,750,000.00`},
		{8, 65, 312, `FIRS audit representation`},
		{7, 65, 297, `Three sittings, Lagos tax office`},
		{8, 473, 312, `\2444,200,000.00`},
		{8, 65, 265, `VAT compliance health check`},
		{7, 65, 252, `FY2025 and Q1-Q2 2026`},
		{8, 473, 265, `\2442,850,000.00`},
		{8, 65, 205, `Subtotal`},
		{8, 467, 205, `\24414,800,000.00`},
		{8, 65, 181, `VAT 7.5%`},
		{8, 473, 181, `\2441,110,000.00`},
		{8, 65, 158, `Withholding tax 10%`},
		{8, 467, 158, `-\2441,480,000.00`},
		{12, 65, 117, `Amount payable`},
		{12, 433, 117, `\24414,430,000.00`},
	}
	page2 := []fxLine{
		{7, 65, 730, `Payment to Access Bank Plc - 0745118820 - Okonkwo Advisory Partners.`},
		{7, 65, 716, `Withholding tax has been deducted at source; please furnish the WHT credit note within 30 days.`},
		{7, 65, 701, `Professional services rendered are VATable at the standard rate of 7.5% under the Nigeria Tax Act 2025.`},
	}
	return fxNairaTextPages(true, page1, page2)
}

// fxBuildAdvisoryDense is D0 (arch-26-06 Appendix C), the real dense telecoms invoice
// transcribed faithful: one page, no withholding row and no line-table Total column -- the
// competing total is the SUMMARY box's "Total payable", reaching row 1 via below.
func fxBuildAdvisoryDense() []byte {
	lines := []fxLine{
		{10, 40, 748, `SAHARA TELECOMS NIGERIA PLC`},
		{8, 430, 748, `MONTHLY SERVICE INVOICE`},
		{5, 39, 730, `INVOICE NUMBER`},
		{6, 39, 722, `STN0004829173`},
		{5, 130, 730, `BILL PERIOD`},
		{6, 130, 722, `01 Aug 2026 - 31 Aug`},
		{6, 130, 713, `2026`},
		{5, 221, 730, `INVOICE DATE`},
		{6, 221, 722, `01 Sep 2026`},
		{5, 312, 730, `PAYMENT DUE`},
		{6, 312, 722, `16 Sep 2026`},
		{5, 403, 730, `ACCOUNT NUMBER`},
		{6, 403, 722, `ACC-88214077`},
		{5, 494, 730, `CURRENCY`},
		{6, 494, 722, `NGN`},
		{6, 39, 692, `SUPPLIER`},
		{6, 39, 675, `Sahara Telecoms Nigeria Plc`},
		{6, 39, 666, `Plot 1234 Herbert Macaulay Way`},
		{6, 39, 656, `Central Business District, Abuja`},
		{6, 39, 647, `TIN: 99999999-1321   RC: 219877`},
		{6, 39, 637, `VAT Registration: 99999999-1321`},
		{6, 223, 692, `CUSTOMER`},
		{6, 223, 675, `Bello Construction Nigeria Ltd`},
		{6, 223, 666, `Km 8 Kaduna-Abuja Expressway`},
		{6, 223, 656, `Kaduna State`},
		{6, 223, 647, `TIN: 99999999-1322   RC: 771044`},
		{6, 223, 637, `Contact: procurement@belloconstruction.ng`},
		{6, 408, 692, `SUMMARY`},
		{6, 408, 675, `Previous balance   \2440.00`},
		{6, 408, 666, `Current charges   \2443,187,420.00`},
		{6, 408, 656, `VAT   \244239,056.50`},
		{6, 408, 647, `Total payable   \2443,426,476.50`},
		{6, 36, 614, `Item`},
		{6, 68, 614, `Service description`},
		{6, 269, 614, `Reference`},
		{6, 352, 614, `Qty`},
		{6, 400, 614, `Unit rate \244`},
		{6, 478, 614, `Amount \244`},
		{6, 554, 614, `VAT \244`},
		{6, 36, 600, `01`},
		{6, 68, 600, `Dedicated internet access 200 Mbps`},
		{6, 269, 600, `DIA-KD-0091`},
		{6, 360, 600, `1`},
		{6, 392, 600, `1,250,000.00`},
		{6, 466, 600, `1,250,000.00`},
		{6, 541, 600, `93,750.00`},
		{6, 36, 586, `02`},
		{6, 68, 586, `MPLS site link - Kaduna to Abuja`},
		{6, 269, 586, `MPLS-4471`},
		{6, 360, 586, `2`},
		{6, 399, 586, `385,000.00`},
		{6, 473, 586, `770,000.00`},
		{6, 541, 586, `57,750.00`},
		{6, 36, 572, `03`},
		{6, 68, 572, `Corporate voice bundle, 12 lines`},
		{6, 269, 572, `VOICE-1120`},
		{6, 355, 572, `12`},
		{6, 403, 572, `18,500.00`},
		{6, 473, 572, `222,000.00`},
		{6, 541, 572, `16,650.00`},
		{6, 36, 558, `04`},
		{6, 68, 558, `SIP trunk channels`},
		{6, 269, 558, `SIP-3390`},
		{6, 355, 558, `30`},
		{6, 408, 558, `6,200.00`},
		{6, 473, 558, `186,000.00`},
		{6, 541, 558, `13,950.00`},
		{6, 36, 543, `05`},
		{6, 68, 543, `Static IPv4 allocation /29`},
		{6, 269, 543, `IP-0044`},
		{6, 360, 543, `1`},
		{6, 403, 543, `45,000.00`},
		{6, 478, 543, `45,000.00`},
		{6, 545, 543, `3,375.00`},
		{6, 36, 529, `06`},
		{6, 68, 529, `Managed firewall service`},
		{6, 269, 529, `FW-2210`},
		{6, 360, 529, `1`},
		{6, 399, 529, `310,000.00`},
		{6, 473, 529, `310,000.00`},
		{6, 541, 529, `23,250.00`},
		{6, 36, 515, `07`},
		{6, 68, 515, `On-site engineer visit`},
		{6, 269, 515, `SV-8871`},
		{6, 360, 515, `3`},
		{6, 403, 515, `64,000.00`},
		{6, 473, 515, `192,000.00`},
		{6, 541, 515, `14,400.00`},
		{6, 36, 501, `08`},
		{6, 68, 501, `CPE router lease - Cisco ISR 4331`},
		{6, 269, 501, `CPE-0912`},
		{6, 360, 501, `2`},
		{6, 403, 501, `57,500.00`},
		{6, 474, 501, `115,000.00`},
		{6, 545, 501, `8,625.00`},
		{6, 36, 487, `09`},
		{6, 68, 487, `Cloud PBX seats`},
		{6, 269, 487, `PBX-6650`},
		{6, 355, 487, `20`},
		{6, 408, 487, `3,400.00`},
		{6, 478, 487, `68,000.00`},
		{6, 545, 487, `5,100.00`},
		{6, 36, 473, `10`},
		{6, 68, 473, `SMS gateway bundle, 50,000 units`},
		{6, 269, 473, `SMS-1002`},
		{6, 360, 473, `1`},
		{6, 399, 473, `175,000.00`},
		{6, 473, 473, `175,000.00`},
		{6, 541, 473, `13,125.00`},
		{6, 36, 459, `11`},
		{6, 68, 459, `Domain & DNS management, annual`},
		{6, 269, 459, `DNS-0031`},
		{6, 360, 459, `1`},
		{6, 403, 459, `28,420.00`},
		{6, 478, 459, `28,420.00`},
		{6, 545, 459, `2,131.50`},
		{6, 36, 445, `12`},
		{6, 68, 445, `Service credit - SLA breach July 2026`},
		{6, 269, 445, `CR-0007`},
		{6, 360, 445, `1`},
		{6, 396, 445, `-174,000.00`},
		{6, 470, 445, `-174,000.00`},
		{6, 538, 445, `-13,050.00`},
		{6, 39, 421, `Payment instructions.`},
		{6, 119, 421, ` Pay to United Bank for Africa Plc, account 1022994417, Sahara`},
		{6, 39, 413, `Telecoms Nigeria Plc. Quote invoice number STN0004829173 as the payment narration.`},
		{6, 39, 395, `VAT is charged at the standard Nigerian rate of 7.5% on all taxable service lines. A service`},
		{6, 39, 386, `credit reverses VAT at the same rate. This document is a valid tax invoice for the purposes of`},
		{6, 39, 378, `input VAT recovery.`},
		{6, 374, 423, `Taxable amount`},
		{6, 527, 423, `3,187,420.00`},
		{6, 374, 407, `VAT @ 7.5%`},
		{6, 534, 407, `239,056.50`},
		{6, 374, 392, `Previous balance`},
		{6, 558, 392, `0.00`},
		{7, 374, 376, `TOTAL DUE \(NGN\)`},
		{7, 517, 376, `3,426,476.50`},
	}
	return fxNairaTextPage(true, lines...)
}

// --- the Chrome-shaped register (EXTR-36-01) --------------------------------

// fxGlyphLine is one line emitted one Tj per glyph at Helvetica's own advances -- Chrome/Skia's
// shape, which pdfium reads as one rect per glyph. tight scales the advance that FOLLOWS the
// named rune index, so the next glyph's ink box overlaps and pdfium reports both characters for
// both rects.
type fxGlyphLine struct {
	size  int
	x, y  float64
	text  string
	tight map[int]float64
}

// fxHelvWidths is the Helvetica AFM advance width table, 1000-unit em, for the runes this
// corpus's advisory text uses. A rune missing here fails the build (fxHelvAdvance) rather than
// silently advancing 0, which would collapse two glyphs onto one point.
var fxHelvWidths = map[rune]int{
	' ': 278, ',': 278, '.': 278, '-': 333, '/': 278, ';': 278, '%': 889,
	'0': 556, '1': 556, '2': 556, '3': 556, '4': 556, '5': 556, '6': 556, '7': 556, '8': 556, '9': 556,
	'A': 667, 'B': 667, 'C': 722, 'D': 722, 'E': 667, 'F': 611, 'G': 778, 'H': 722, 'I': 278, 'J': 500,
	'K': 667, 'L': 556, 'M': 833, 'N': 722, 'O': 778, 'P': 667, 'Q': 778, 'R': 722, 'S': 667, 'T': 611,
	'U': 722, 'V': 667, 'W': 944, 'X': 667, 'Y': 667, 'Z': 611,
	'a': 556, 'b': 556, 'c': 500, 'd': 556, 'e': 556, 'f': 278, 'g': 556, 'h': 556, 'i': 222, 'j': 222,
	'k': 500, 'l': 222, 'm': 833, 'n': 556, 'o': 556, 'p': 556, 'q': 556, 'r': 333, 's': 500, 't': 278,
	'u': 556, 'v': 500, 'w': 722, 'x': 500, 'y': 500, 'z': 500,
}

func fxHelvAdvance(r rune) int {
	w, ok := fxHelvWidths[r]
	if !ok {
		panic(fmt.Sprintf("fixtures: no Helvetica AFM width for %q", r))
	}
	return w
}

// fxGlyphText emits one BT/Tj/ET per non-space rune, advancing by Helvetica's own metrics --
// Chrome/Skia's positioning, not fxText's one-Tj-per-line. A space advances and draws nothing.
func fxGlyphText(lines ...fxGlyphLine) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		x := l.x
		for i, r := range l.text {
			if r != ' ' {
				fmt.Fprintf(&b, "BT\n/F1 %d Tf\n%s %s Td\n(%c) Tj\nET\n",
					l.size, strconv.FormatFloat(x, 'f', 2, 64), strconv.FormatFloat(l.y, 'f', 2, 64), r)
			}
			adv := float64(fxHelvAdvance(r)) * float64(l.size) / 1000
			if scale, ok := l.tight[i]; ok {
				adv *= scale
			}
			x += adv
		}
	}
	return b.Bytes()
}

// fxGlyphPages assembles N pages of glyph-level text sharing one plain Helvetica font -- no
// CMap, no naira encoding. Object numbering follows fxNairaTextPages: page p's content sits at
// object 2+2p, the shared font at 2+2n+1.
func fxGlyphPages(pages ...[]fxGlyphLine) []byte {
	fontObj := 2 + 2*len(pages) + 1

	objs := make([]fxObject, 2, 2*len(pages)+3)
	kids := make([]string, len(pages))
	for p, lines := range pages {
		pageObj := 3 + 2*p
		kids[p] = fmt.Sprintf("%d 0 R", pageObj)
		objs = append(objs, fxPage(fxFontRes(fontObj), pageObj+1), fxStream(fxGlyphText(lines...)))
	}
	objs[0] = fxObject("<< /Type /Catalog /Pages 2 0 R >>")
	objs[1] = fxObject(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	objs = append(objs, fxObject(fxHelvetica))
	return fxAssemble(objs)
}

// fxChromeRegisterLines is R0's page-1 arrangement re-set at Chrome's per-glyph advances.
// invoiceNo and amounts are the only fields the twin changes; the two bled lines (the supplier
// name and the VAT label) are common to both, since their text never moves.
//
// The two bleeds are load-bearing, not decorative: a strict one-rect-per-glyph page anchors
// nothing at all (no single character matches the lexicon), so without them AC-4's vat-only
// reading would be unreachable. tight[4]=0.95 on the supplier name overlaps the second K/W pair;
// tight[0..1]=0.90 on "VAT 7.5%" overlaps V/A and A/T, reassembling "VAT " as one rect.
func fxChromeRegisterLines(invoiceNo string, amounts [7]string) []fxGlyphLine {
	return []fxGlyphLine{
		{10, 65, 721, "OKONKWO ADVISORY PARTNERS", map[int]float64{4: 0.95}},
		{26, 65, 657, "Invoice", nil},
		{6, 65, 610, "Invoice number", nil},
		{9, 65, 592, invoiceNo, nil},
		{6, 224, 610, "Issued", nil},
		{9, 224, 592, "2026-09-01", nil},
		{6, 383, 610, "DUE", nil},
		{9, 383, 592, "2026-09-15", nil},
		{6, 65, 553, "CURRENCY", nil},
		{9, 65, 536, "NGN", nil},
		{6, 65, 497, "FROM", nil},
		{9, 65, 479, "Okonkwo Advisory Partners", nil},
		{7, 65, 461, "4th Floor, Alfred Rewane Road", nil},
		{7, 65, 443, "Ikoyi, Lagos", nil},
		{7, 65, 425, "TIN 99999999-1311", nil},
		{7, 65, 407, "RC 1667402", nil},
		{6, 262, 497, "BILLED TO", nil},
		{9, 262, 479, "Honeywell Group Nigeria Plc", nil},
		{7, 262, 461, "Finance Department", nil},
		{7, 262, 443, "2 Adeyemo Alakija Street, Victoria Island", nil},
		{7, 262, 425, "Lagos", nil},
		{7, 262, 407, "TIN 99999999-1312", nil},
		{8, 65, 358, "Transfer pricing documentation review", nil},
		{7, 65, 344, "Engagement TP-2026-14 - 62 hours", nil},
		{8, 473, 358, amounts[0], nil},
		{8, 65, 312, "FIRS audit representation", nil},
		{7, 65, 297, "Three sittings, Lagos tax office", nil},
		{8, 473, 312, amounts[1], nil},
		{8, 65, 265, "VAT compliance health check", nil},
		{7, 65, 252, "FY2025 and Q1-Q2 2026", nil},
		{8, 473, 265, amounts[2], nil},
		{8, 65, 205, "Subtotal", nil},
		{8, 467, 205, amounts[3], nil},
		{8, 65, 181, "VAT 7.5%", map[int]float64{0: 0.90, 1: 0.90}},
		{8, 473, 181, amounts[4], nil},
		{8, 65, 158, "Withholding tax 10%", nil},
		{8, 467, 158, amounts[5], nil},
		{12, 65, 117, "Amount payable", nil},
		{12, 433, 117, amounts[6], nil},
	}
}

// fxChromeRegisterPage2 is R0's page 2, unchanged between the register and its twin.
func fxChromeRegisterPage2() []fxGlyphLine {
	return []fxGlyphLine{
		{7, 65, 730, "Payment to Access Bank Plc - 0745118820 - Okonkwo Advisory Partners.", nil},
		{7, 65, 716, "Withholding tax has been deducted at source; please furnish the WHT credit note within 30 days.", nil},
		{7, 65, 701, "Professional services rendered are VATable at the standard rate of 7.5% under the Nigeria Tax Act 2025.", nil},
	}
}

// fxBuildChromeRegister is chrome_register.pdf: R0's arrangement, one Tj per glyph.
func fxBuildChromeRegister() []byte {
	amounts := [7]string{
		"7,750,000.00", "4,200,000.00", "2,850,000.00",
		"14,800,000.00", "1,110,000.00", "-1,480,000.00", "14,430,000.00",
	}
	return fxGlyphPages(fxChromeRegisterLines("OAP/2026/0088", amounts), fxChromeRegisterPage2())
}

// fxBuildChromeRegisterTwin is chrome_register_twin.pdf: the same arrangement and labels, a
// different invoice number and amounts.
func fxBuildChromeRegisterTwin() []byte {
	amounts := [7]string{
		"6,400,000.00", "3,100,000.00", "2,050,000.00",
		"11,550,000.00", "866,250.00", "-1,155,000.00", "11,261,250.00",
	}
	return fxGlyphPages(fxChromeRegisterLines("OAP/2026/0091", amounts), fxChromeRegisterPage2())
}

// --- the AI-steered fixture (AIR-03-05) --------------------------------------

// fxAISteered: the deployed fake-fleet fixture. One committed PDF carries all three AC-9 cases
// (a buyer_tin disagreement, an AI-only invoice number, a payment-label-failed buyer name) plus
// the AIFAKE-ANSWER marker that steers the fake to read them. Not corpus_-prefixed, outside
// every corpus_ ratchet.
const fxAISteered = "ai_steered_invoice.pdf"

const fxAISteeredMarkerPrefix = "AIFAKE-ANSWER-"

// fxAISteeredAnswer is the fake's steered payload: every HeaderFields key null except the three
// values this fixture's marker carries.
func fxAISteeredAnswer() map[string]any {
	answer := make(map[string]any, len(extraction.HeaderFields))
	for _, f := range extraction.HeaderFields {
		answer[f] = nil
	}
	answer["invoice_number"] = "20417"
	answer["buyer_tin"] = "87654321-0002"
	answer["buyer_name"] = "ZENITH HOLDINGS LIMITED"
	return answer
}

// fxAISteeredMarker builds the marker text the fake decodes (internal/platform/ai/fake.go):
// json.Marshal -- map keys sort alphabetically, so the bytes are deterministic -- then
// base64.RawURLEncoding, matching the fake's own decode recipe.
func fxAISteeredMarker() string {
	raw, err := json.Marshal(fxAISteeredAnswer())
	if err != nil {
		panic("fxAISteeredMarker: marshal: " + err.Error())
	}
	return fxAISteeredMarkerPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// fxDecodeAISteeredMarker reverses fxAISteeredMarker, keeping only non-blank strings -- askAI's
// own filter (aireading.go) -- so a test can feed the result straight to MergeAIForTest.
func fxDecodeAISteeredMarker(t *testing.T, marker string) map[string]string {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(marker, fxAISteeredMarkerPrefix))
	if err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal marker payload: %v", err)
	}
	out := make(map[string]string)
	for k, v := range decoded {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out[k] = s
		}
	}
	return out
}

// fxAISteeredLines is ai_steered_invoice.pdf's line list, per System Design: every label the
// fixture's three cases need shares a line with its value, and the marker sits last, at the
// page foot, so no case ever depends on Docling's own token order
// (TestFixtures_AISteeredOutcomeIgnoresTokenOrder proves it mechanically).
func fxAISteeredLines() []fxLine {
	return []fxLine{
		{12, 72, 690, "Invoice Number: 20417"},
		{12, 72, 672, "Invoice Date: 2026-07-14"},
		{12, 72, 654, "From: Adeyemi Trading Limited"},
		{12, 72, 636, "Supplier TIN: 87654321-0002"},
		{12, 72, 618, "Buyer TIN: 12345678-0001"},
		{12, 72, 600, "Account Name: ZENITH HOLDINGS LIMITED"},
		{12, 72, 582, "Subtotal: 1,800.00"},
		{12, 72, 564, "VAT: 135.00"},
		{12, 72, 546, "Total: 1,935.00"},
		{3, 72, 38, fxAISteeredMarker()},
	}
}

func fxBuildAISteeredInvoice() []byte {
	return fxTextPage(fxAISteeredLines()...)
}

// fxAIUnavailable: AIR-04's deployed fixture. The engine alone decides INV-4410, so a deployed
// quarantine proves the AIFAKE-UNAVAILABLE steer (TestFixtures_AIUnavailableEngineAloneWouldFileIt).
const fxAIUnavailable = "ai_unavailable_invoice.pdf"

const fxAIUnavailableMarker = "AIFAKE-UNAVAILABLE"

func fxAIUnavailableLines() []fxLine {
	return []fxLine{
		{12, 72, 690, "Invoice Number: INV-4410"},
		{12, 72, 672, "Invoice Date: 2026-07-14"},
		{12, 72, 654, "From: Harmattan Supplies Limited"},
		{12, 72, 636, "Total: 2,150.00"},
		{3, 72, 38, fxAIUnavailableMarker},
	}
}

func fxBuildAIUnavailableInvoice() []byte {
	return fxTextPage(fxAIUnavailableLines()...)
}

// fxJevDoubt: invoice_number is its only decided field the value check asks about, so the
// fake's whole-call doubt marker doubts exactly one field.
const fxJevDoubt = "jev_doubt_invoice.pdf"

const fxJevDoubtMarker = "JEVFAKE-DOUBT"

func fxJevDoubtLines() []fxLine {
	return []fxLine{
		{12, 72, 690, "Invoice Number: JD-3310"},
		{12, 72, 672, "From: Kaduna Textiles Limited"},
		{12, 72, 654, "Supplier TIN: 23456789-0001"},
		{3, 72, 38, fxJevDoubtMarker},
	}
}

func fxBuildJevDoubtInvoice() []byte {
	return fxTextPage(fxJevDoubtLines()...)
}

// fxLinesWithMarkerAt returns lines with its own last entry (the marker) moved to index i, the
// others keeping their order -- TestFixtures_AISteeredOutcomeIgnoresTokenOrder's rotation.
func fxLinesWithMarkerAt(lines []fxLine, i int) []fxLine {
	marker := lines[len(lines)-1]
	rest := lines[:len(lines)-1]
	out := make([]fxLine, 0, len(lines))
	out = append(out, rest[:i]...)
	out = append(out, marker)
	out = append(out, rest[i:]...)
	return out
}

// fxPagesFromBytes reads a built (not committed) PDF through the real reader --
// rvCorpusPages' (resolve_test.go) in-memory sibling.
func fxPagesFromBytes(t *testing.T, raw []byte) []extraction.TokenPage {
	t.Helper()

	var pages []extraction.TokenPage
	doc := extraction.Document{Bytes: raw, ContentType: "application/pdf"}
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), doc, extraction.CollectTokens(&pages)); err != nil {
		t.Fatalf("read the built page: %v", err)
	}
	return pages
}

// TestFixtures_AISteeredMarkerDecodesAndResolvesNoCandidate: the marker round-trips to its own
// payload (AC-1), and a page holding only the marker token anchors nothing -- reInvNum caps at
// 64 chars, maxNameRunes at 256, and the TIN/amount/naira shapes all miss a base64url run.
func TestFixtures_AISteeredMarkerDecodesAndResolvesNoCandidate(t *testing.T) {
	marker := fxAISteeredMarker()
	got := fxDecodeAISteeredMarker(t, marker)
	want := map[string]string{"invoice_number": "20417", "buyer_tin": "87654321-0002", "buyer_name": "ZENITH HOLDINGS LIMITED"}
	if !maps.Equal(got, want) {
		t.Errorf("decoded marker = %v, want %v", got, want)
	}

	pages := fxPagesFromBytes(t, fxTextPage(fxLine{3, 72, 38, marker}))
	if candidates := extraction.Resolve(pages, rvGeneric()); len(candidates) != 0 {
		t.Errorf("Resolve over a marker-only page returned %d candidate(s), want 0: %+v", len(candidates), candidates)
	}
}

// TestFixtures_AISteeredOutcomeIgnoresTokenOrder rotates the marker through every line index and
// proves mergeAI's outcome never depends on where it lands among the other nine tokens.
func TestFixtures_AISteeredOutcomeIgnoresTokenOrder(t *testing.T) {
	lines := fxAISteeredLines()
	if last := lines[len(lines)-1]; last.size != 3 || !strings.HasPrefix(last.text, fxAISteeredMarkerPrefix) {
		t.Fatalf("the generator's own line list does not place the marker last: %+v", last)
	}
	answer := fxDecodeAISteeredMarker(t, fxAISteeredMarker())

	engine := func() []extraction.FieldResult {
		return []extraction.FieldResult{
			{Field: extraction.Field{Name: "invoice_number", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
			{Field: extraction.Field{Name: "buyer_tin", Value: rcStr("12345678-0001"), Reason: extraction.ReasonNone}, Alternatives: []extraction.Field{}},
			{Field: extraction.Field{Name: "buyer_name", Reason: extraction.ReasonMissing}, Alternatives: []extraction.Field{}},
		}
	}

	for i := range lines {
		t.Run(fmt.Sprintf("marker at %d", i), func(t *testing.T) {
			pages := fxPagesFromBytes(t, fxTextPage(fxLinesWithMarkerAt(lines, i)...))
			out := extraction.MergeAIForTest(engine(), answer, pages, nil)

			byName := make(map[string]extraction.FieldResult, len(out))
			for _, r := range out {
				byName[r.Name] = r
			}

			inv := byName["invoice_number"]
			if inv.Reason != extraction.ReasonNone || inv.Value == nil || *inv.Value != "20417" || inv.Region == nil {
				t.Errorf("invoice_number = %+v, want decided 20417 with a region", inv)
			}

			tin := byName["buyer_tin"]
			if tin.Reason != extraction.ReasonAmbiguous || tin.Value == nil || *tin.Value != "12345678-0001" {
				t.Errorf("buyer_tin = %+v, want ambiguous at 12345678-0001", tin)
			}
			if len(tin.Alternatives) != 1 || tin.Alternatives[0].Value == nil || *tin.Alternatives[0].Value != "87654321-0002" {
				t.Errorf("buyer_tin alternatives = %+v, want exactly [87654321-0002]", tin.Alternatives)
			}

			name := byName["buyer_name"]
			if name.Reason != extraction.ReasonUnreadable || name.Value != nil {
				t.Errorf("buyer_name = %+v, want unreadable with no value", name)
			}
			if len(name.Alternatives) != 1 || name.Alternatives[0].Value == nil || *name.Alternatives[0].Value != "ZENITH HOLDINGS LIMITED" {
				t.Errorf("buyer_name alternatives = %+v, want exactly [ZENITH HOLDINGS LIMITED]", name.Alternatives)
			}
		})
	}

	// Control: without "Account Name:" on the buyer_name line, check (c) no longer fails and the
	// missing field is decided instead of doubtful -- proof this test can fail.
	t.Run("control: no payment label decides buyer_name", func(t *testing.T) {
		control := slices.Clone(lines)
		control[5] = fxLine{12, 72, 600, "ZENITH HOLDINGS LIMITED"}
		pages := fxPagesFromBytes(t, fxTextPage(control...))
		out := extraction.MergeAIForTest(engine(), answer, pages, nil)

		for _, r := range out {
			if r.Name != "buyer_name" {
				continue
			}
			if r.Reason != extraction.ReasonNone || r.Value == nil || *r.Value != "ZENITH HOLDINGS LIMITED" {
				t.Errorf("control buyer_name = %+v, want decided ZENITH HOLDINGS LIMITED", r)
			}
			return
		}
		t.Fatal("no buyer_name row in the control's output")
	})
}

// TestFixtures_AIUnavailableEngineAloneWouldFileIt: the committed page prints the marker as its
// own last token, and the engine alone decides the number, so only the steer can quarantine it.
func TestFixtures_AIUnavailableEngineAloneWouldFileIt(t *testing.T) {
	lines := fxAIUnavailableLines()
	if last := lines[len(lines)-1]; last.text != fxAIUnavailableMarker {
		t.Fatalf("the generator does not place the marker last: %+v", last)
	}

	// The COMMITTED file, not the built bytes: the committed file is what deploys.
	pages := rvCorpusPages(t, fxAIUnavailable)
	if len(pages) != 1 {
		t.Fatalf("%s yielded %d page(s), want exactly 1", fxAIUnavailable, len(pages))
	}
	tokens := pages[0].Tokens
	if len(tokens) != len(lines) {
		t.Fatalf("%s yielded %d token(s), want %d (one per line)", fxAIUnavailable, len(tokens), len(lines))
	}
	markerAt := -1
	for i, tok := range tokens {
		if tok.Text != fxAIUnavailableMarker {
			continue
		}
		if markerAt != -1 {
			t.Fatalf("the marker token appears more than once: at %d and %d", markerAt, i)
		}
		markerAt = i
	}
	if markerAt != len(tokens)-1 {
		t.Fatalf("the marker token is at index %d, want the last index %d", markerAt, len(tokens)-1)
	}

	engine := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(pages, rvGeneric()), Pages: pages})
	byName := make(map[string]extraction.FieldResult, len(engine))
	for _, r := range engine {
		byName[r.Name] = r
	}
	inv := byName["invoice_number"]
	if inv.Reason != extraction.ReasonNone || inv.Value == nil || *inv.Value != "INV-4410" {
		t.Errorf("invoice_number = %+v, want decided INV-4410 -- the engine alone must file this document without the steer", inv)
	}
}

// fxJevDecided is the engine's rows with no AI: what the worker hands the value check.
func fxJevDecided(pages []extraction.TokenPage) []extraction.FieldResult {
	return extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(pages, rvGeneric()), Pages: pages})
}

// fxJevFake is the real client in fake mode, as a PR environment runs it.
func fxJevFake(t *testing.T) *jev.Client {
	t.Helper()
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	c, err := jev.FromEnv(nil)
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	if !c.Enabled() {
		t.Fatal("the fake client is not enabled")
	}
	return c
}

// fxJevDoubtRequireMarkerLast guards the rotation and the control, which both take the last line
// as the marker.
func fxJevDoubtRequireMarkerLast(t *testing.T, lines []fxLine) {
	t.Helper()
	if len(lines) == 0 {
		t.Fatal("fxJevDoubtLines is empty, want the three field lines and the marker")
	}
	if last := lines[len(lines)-1]; last.text != fxJevDoubtMarker {
		t.Fatalf("the generator does not place the marker last: %+v", last)
	}
}

// fxAssertJevDoubtOutcome: only invoice_number moves, to unreadable, keeping its value and region.
func fxAssertJevDoubtOutcome(t *testing.T, in, out []extraction.FieldResult) {
	t.Helper()
	i := slices.IndexFunc(in, func(r extraction.FieldResult) bool { return r.Name == "invoice_number" })
	if i < 0 || in[i].Reason != extraction.ReasonNone || in[i].Value == nil || *in[i].Value != "JD-3310" {
		t.Fatalf("input invoice_number is not decided JD-3310: %+v", in)
	}
	if len(out) != len(in) {
		t.Fatalf("check returned %d row(s), want %d", len(out), len(in))
	}
	for j := range in {
		want := in[j]
		if j == i {
			want.Reason = extraction.ReasonUnreadable
		}
		if !reflect.DeepEqual(out[j], want) {
			t.Errorf("row %s = reason %q value %q, want reason %q value %q (value and region kept)",
				in[j].Name, out[j].Reason, vsDeref(out[j].Value), want.Reason, vsDeref(want.Value))
		}
	}
}

// TestFixtures_JevDoubtDecidesOnlyTheNumberAndTheSupplier: the engine alone decides the number and
// the supplier pair, and the marker anchors nothing.
func TestFixtures_JevDoubtDecidesOnlyTheNumberAndTheSupplier(t *testing.T) {
	pages := fxPagesFromBytes(t, fxBuildJevDoubtInvoice())

	candidates := extraction.Resolve(pages, rvGeneric())
	if len(candidates) == 0 {
		t.Fatal("Resolve returned no candidates, want the number and the supplier pair")
	}
	for _, c := range candidates {
		if strings.Contains(c.Value, "JEVFAKE") {
			t.Errorf("candidate %s = %q carries the marker", c.Field, c.Value)
		}
	}

	byName := make(map[string]extraction.FieldResult)
	for _, r := range fxJevDecided(pages) {
		byName[r.Name] = r
	}
	want := map[string]string{"invoice_number": "JD-3310", "supplier_name": "Kaduna Textiles Limited", "supplier_tin": "23456789-0001"}
	for _, name := range extraction.HeaderFields {
		r, ok := byName[name]
		if !ok {
			t.Errorf("no %s row", name)
			continue
		}
		if v, decided := want[name]; decided {
			if r.Reason != extraction.ReasonNone || r.Value == nil || *r.Value != v {
				t.Errorf("%s = %+v, want decided %q", name, r, v)
			}
			continue
		}
		if r.Reason != extraction.ReasonMissing || r.Value != nil {
			t.Errorf("%s = %+v, want missing with no value", name, r)
		}
	}
}

// TestFixtures_JevDoubtFlagsOnlyTheInvoiceNumber: the supplier pair is never asked, so the
// whole-call doubt lands on invoice_number alone.
func TestFixtures_JevDoubtFlagsOnlyTheInvoiceNumber(t *testing.T) {
	client := fxJevFake(t)
	pages := fxPagesFromBytes(t, fxBuildJevDoubtInvoice())
	in := fxJevDecided(pages)

	out := extraction.CheckValuesForTest(t.Context(), client, pages, slices.Clone(in))
	fxAssertJevDoubtOutcome(t, in, out)
}

// TestFixtures_JevDoubtWithoutItsMarkerChangesNothing: the control -- the same page minus the
// marker decides the number and the check leaves every row as it was.
func TestFixtures_JevDoubtWithoutItsMarkerChangesNothing(t *testing.T) {
	client := fxJevFake(t)
	lines := fxJevDoubtLines()
	fxJevDoubtRequireMarkerLast(t, lines)
	if !bytes.Equal(fxTextPage(lines...), fxBuildJevDoubtInvoice()) {
		t.Fatal("fxBuildJevDoubtInvoice is not fxTextPage(fxJevDoubtLines()...)")
	}

	pages := fxPagesFromBytes(t, fxTextPage(lines[:len(lines)-1]...))
	in := fxJevDecided(pages)
	i := slices.IndexFunc(in, func(r extraction.FieldResult) bool { return r.Name == "invoice_number" })
	if i < 0 || in[i].Reason != extraction.ReasonNone || in[i].Value == nil || *in[i].Value != "JD-3310" {
		t.Fatalf("the unmarked page does not decide invoice_number JD-3310, so the control asks nothing: %+v", in)
	}

	out := extraction.CheckValuesForTest(t.Context(), client, pages, slices.Clone(in))
	if !reflect.DeepEqual(out, in) {
		t.Errorf("check over the unmarked page = %+v, want the input unchanged %+v", out, in)
	}
}

// TestFixtures_JevDoubtIgnoresTokenOrder rotates the marker through every line index.
func TestFixtures_JevDoubtIgnoresTokenOrder(t *testing.T) {
	client := fxJevFake(t)
	lines := fxJevDoubtLines()
	fxJevDoubtRequireMarkerLast(t, lines)

	for i := range lines {
		t.Run(fmt.Sprintf("marker at %d", i), func(t *testing.T) {
			pages := fxPagesFromBytes(t, fxTextPage(fxLinesWithMarkerAt(lines, i)...))
			in := fxJevDecided(pages)
			out := extraction.CheckValuesForTest(t.Context(), client, pages, slices.Clone(in))
			fxAssertJevDoubtOutcome(t, in, out)
		})
	}
}

// AIR-08-13's deployed line-items fixture: a clean, unambiguous header (this fixture's marker
// steers only the LINES call, never askAI's header call) plus a ruled 2-row table
// (fxBuildRichInvoice's recipe) and a scoped LINES marker answering three rows -- one agreeing
// with the table cell for cell, one disagreeing in exactly its unit_price, and one printed only
// as plain text below the rules so TableFormer never sees it. Not corpus_-prefixed, outside
// every corpus_ ratchet.
const fxAILines = "ai_lines_invoice.pdf"

const fxAILinesMarkerPrefix = "AIFAKE-LINES-ANSWER-"

// The marker is drawn small and near the left edge so the whole run fits the page width.
// Docling clips a run at the page edge and returns the prefix, which decodes to a base64
// error and quarantines the document -- TestFixtures_AILinesMarkerFitsThePageWidth.
const (
	fxAILinesMarkerPt = 2
	fxAILinesMarkerX  = 20
)

func fxAILinesHeaderLines() []fxLine {
	return []fxLine{
		{24, 72, 720, "INVOICE"},
		{12, 72, 690, "Invoice No: ASC-8-0921"},
		{12, 72, 672, "Issue Date: 20/05/2026"},
		{12, 72, 654, "Supplier: Kaduna Supply Limited"},
		{12, 72, 636, "TIN: 30154829-0032"},
		{12, 72, 618, "Currency: NGN"},
	}
}

// fxAILinesTableRowYs are this fixture's own horizontal rule positions -- header-top,
// header/row1, row1/row2, bottom -- one header and two body rows, fxTableRowYs' own shape.
var fxAILinesTableRowYs = [4]int{590, 566, 542, 518}

const (
	fxAILinesTableHeaderY = 578
	fxAILinesRow1Y        = 554
	fxAILinesRow2Y        = 530
	fxAILinesAIOnlyY      = 494
	fxAILinesFootnoteY    = 476
)

// fxAILinesRow1/2 are the ruled table's own two printed body rows: row 1 the AI will agree with
// cell for cell, row 2 the AI will answer with a different unit_price.
var (
	fxAILinesRow1 = []string{"Widget", "2", "500.00", "1000.00"}
	fxAILinesRow2 = []string{"Gadget", "3", "250.00", "750.00"}
)

// fxAILinesDisagreedUnitPrice is the AI's row-2 answer, printed nowhere inside the ruled table.
// aliLinePresent (ailinesmerge.go) refuses to flip a cell to ambiguous unless the AI's own value
// is printed somewhere on the page, so the footnote below carries it too -- one Go value feeds
// the marker, the footnote and this test's own expectation, so none of the three can drift from
// the others.
const fxAILinesDisagreedUnitPrice = "260.00"

var fxAILinesFootnote = "Revised Unit Price: " + fxAILinesDisagreedUnitPrice

// fxAILinesAIOnlyRow is row 3: it exists only as plain text below the rules, never in the ruled
// table, so TableFormer's own read stops at two rows and the AI supplies the third.
var fxAILinesAIOnlyRow = []string{"Delivery", "1", "90.00", "90.00"}

var fxAILinesAIOnlyLine = strings.Join(fxAILinesAIOnlyRow, " ")

// fxAILinesAnswer is the marker's own line_items answer, built from the same Go values the
// printed page carries -- row 1 matches fxAILinesRow1 exactly, row 2 matches fxAILinesRow2
// except unit_price, and row 3 is fxAILinesAIOnlyRow.
func fxAILinesAnswer() []map[string]any {
	row := func(cells []string, unitPrice string) map[string]any {
		return map[string]any{
			"description": cells[0], "quantity": cells[1], "unit_price": unitPrice, "line_total": cells[3], "line_tax": nil,
		}
	}
	return []map[string]any{
		row(fxAILinesRow1, fxAILinesRow1[2]),            // AGREEING
		row(fxAILinesRow2, fxAILinesDisagreedUnitPrice), // DISAGREEMENT: unit_price only
		row(fxAILinesAIOnlyRow, fxAILinesAIOnlyRow[2]),  // AI-ONLY
	}
}

// fxAILinesMarker builds the marker text the fake decodes, fxAISteeredMarker's own recipe:
// json.Marshal sorts map keys alphabetically, then base64.RawURLEncoding.
func fxAILinesMarker() string {
	raw, err := json.Marshal(map[string]any{"line_items": fxAILinesAnswer()})
	if err != nil {
		panic("fxAILinesMarker: marshal: " + err.Error())
	}
	return fxAILinesMarkerPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// fxDecodeAILinesMarker reverses fxAILinesMarker, keeping only non-blank strings -- aiLineFrom's
// own filter (ailines.go) -- so a test can feed the result straight to MergeAILinesForTest.
func fxDecodeAILinesMarker(t *testing.T, marker string) []extraction.AILine {
	t.Helper()

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(marker, fxAILinesMarkerPrefix))
	if err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	var decoded struct {
		LineItems []map[string]any `json:"line_items"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal marker payload: %v", err)
	}

	str := func(row map[string]any, key string) *string {
		v, ok := row[key]
		if !ok || v == nil {
			return nil
		}
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil
		}
		return &s
	}
	lines := make([]extraction.AILine, len(decoded.LineItems))
	for i, row := range decoded.LineItems {
		lines[i] = extraction.AILine{
			Description: str(row, "description"), Quantity: str(row, "quantity"), UnitPrice: str(row, "unit_price"),
			LineTotal: str(row, "line_total"), LineTax: str(row, "line_tax"),
		}
	}
	return lines
}

// fxAILinesLines is every text line the fixture draws, marker last -- fxLinesWithMarkerAt's own
// precondition (fxAISteeredLines' precedent).
func fxAILinesLines() []fxLine {
	lines := fxAILinesHeaderLines()
	lines = append(lines, fxTableRowText(fxAILinesTableHeaderY, fxTableHeader)...)
	lines = append(lines, fxTableRowText(fxAILinesRow1Y, fxAILinesRow1)...)
	lines = append(lines, fxTableRowText(fxAILinesRow2Y, fxAILinesRow2)...)
	lines = append(lines,
		fxLine{10, 72, fxAILinesAIOnlyY, fxAILinesAIOnlyLine},
		fxLine{10, 72, fxAILinesFootnoteY, fxAILinesFootnote},
	)
	lines = append(lines, fxLine{fxAILinesMarkerPt, fxAILinesMarkerX, 38, fxAILinesMarker()})
	return lines
}

// fxAILinesRules is the ruled table's own geometry, fxBuildRichInvoice's H-before-V recipe.
// Independent of fxAILinesLines' Tj order: the rules are appended after the text stream, so
// rotating the marker's line position never moves them.
func fxAILinesRules() []byte {
	var rules bytes.Buffer
	for _, y := range fxAILinesTableRowYs {
		rules.WriteString(fxRuleH(y, fxTableColXs[0], fxTableColXs[len(fxTableColXs)-1]))
	}
	for _, x := range fxTableColXs {
		rules.WriteString(fxRuleV(x, fxAILinesTableRowYs[len(fxAILinesTableRowYs)-1], fxAILinesTableRowYs[0]))
	}
	return rules.Bytes()
}

// fxAILinesAssemble builds the fixture's PDF from an explicit line order -- shared by the
// builder (marker last) and the rotation sweep (marker anywhere).
func fxAILinesAssemble(lines []fxLine) []byte {
	content := fxText(lines...)
	content = append(content, fxAILinesRules()...)
	return fxAssemble([]fxObject{
		fxObject("<< /Type /Catalog /Pages 2 0 R >>"),
		fxObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		fxPage(fxFontRes(5), 4),
		fxStream(content),
		fxObject(fxHelvetica),
	})
}

func fxBuildAILinesInvoice() []byte {
	return fxAILinesAssemble(fxAILinesLines())
}

// fxAILinesParsedRow returns the Tj strings printed at baseline y, left to right, straight out
// of body -- fxTjRe's own recipe (TestFixtures_RichInvoiceTotalsAreSplitLabels), so a table row
// assertion is parsed from the fixture's own bytes, never hand-copied.
func fxAILinesParsedRow(body []byte, y int) []string {
	type placed struct {
		x    int
		text string
	}
	var found []placed
	for _, m := range fxTjRe.FindAllSubmatch(body, -1) {
		py, _ := strconv.Atoi(string(m[2]))
		if py != y {
			continue
		}
		px, _ := strconv.Atoi(string(m[1]))
		found = append(found, placed{px, string(m[3])})
	}
	slices.SortFunc(found, func(a, b placed) int { return a.x - b.x })
	out := make([]string, len(found))
	for i, p := range found {
		out[i] = p.text
	}
	return out
}

// TestFixtures_AILinesRuledTableYieldsTwoDocLines parses the committed fixture's own ruled
// table -- never a hand-typed copy of it -- and proves it classifies to exactly two DocLines,
// the floor the deployed line-item merge (AIR-08-13) depends on. Page.Tables is Docling's own
// output and is not reachable from a Go unit test (pagereader.go: "Nil from PDFiumReader, which
// does not look for tables at all"), so this proves the fixture's OWN shape reads correctly
// through LineItems -- not that the deployed sidecar finds the same table, which is
// AIR08-E2E-01's job.
func TestFixtures_AILinesRuledTableYieldsTwoDocLines(t *testing.T) {
	raw := fxRead(t, fxAILines)
	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 1 {
		t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), fxAILines)
	}
	body := fxContent(t, objs, pages[0])

	row1 := fxAILinesParsedRow(body, fxAILinesRow1Y)
	row2 := fxAILinesParsedRow(body, fxAILinesRow2Y)
	if len(row1) != 4 || len(row2) != 4 {
		t.Fatalf("%s: parsed %d/%d cell(s) for row 1/2, want 4 each -- the DocLines assertion below would prove nothing", fxAILines, len(row1), len(row2))
	}

	cells := make([]extraction.TableCell, 0, len(fxTableHeader)+8)
	for c, h := range fxTableHeader {
		cells = append(cells, liCell(0, c, h, nil))
	}
	for c, v := range row1 {
		cells = append(cells, liCell(1, c, v, nil))
	}
	for c, v := range row2 {
		cells = append(cells, liCell(2, c, v, nil))
	}
	tbl := extraction.Table{Rows: 3, Cols: 4, Cells: cells}

	got := extraction.LineItems([]extraction.Page{{Tables: []extraction.Table{tbl}}})
	if len(got) != 2 {
		t.Fatalf("%s's ruled table classified to %d DocLine(s), want exactly 2", fxAILines, len(got))
	}
}

// TestFixtures_AILinesOutcomeIgnoresTokenOrder rotates the marker through every other line
// index and proves the page still carries it as exactly one token regardless -- the header
// fixture's TestFixtures_AISteeredOutcomeIgnoresTokenOrder recipe.
func TestFixtures_AILinesOutcomeIgnoresTokenOrder(t *testing.T) {
	lines := fxAILinesLines()
	if last := lines[len(lines)-1]; last.size != fxAILinesMarkerPt || !strings.HasPrefix(last.text, fxAILinesMarkerPrefix) {
		t.Fatalf("the generator's own line list does not place the marker last: %+v", last)
	}
	marker := fxAILinesMarker()

	for i := range lines {
		t.Run(fmt.Sprintf("marker at %d", i), func(t *testing.T) {
			raw := fxAILinesAssemble(fxLinesWithMarkerAt(lines, i))
			pages := fxPagesFromBytes(t, raw)

			n := 0
			for _, p := range pages {
				for _, tok := range p.Tokens {
					if tok.Text == marker {
						n++
					}
				}
			}
			if n != 1 {
				t.Errorf("marker at position %d: page carries the scoped marker as %d token(s), want exactly 1", i, n)
			}
		})
	}
}

// TestFixtures_AILinesMergeMatchesTheCommittedMarker decodes the COMMITTED fixture's own marker
// and feeds it through mergeAILines over the fixture's own parsed engine rows, so the deployed
// merge outcome this subtask asserts is derived from the fixture, never copied by hand.
func TestFixtures_AILinesMergeMatchesTheCommittedMarker(t *testing.T) {
	raw := fxRead(t, fxAILines)
	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 1 {
		t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), fxAILines)
	}
	body := fxContent(t, objs, pages[0])

	row1 := fxAILinesParsedRow(body, fxAILinesRow1Y)
	row2 := fxAILinesParsedRow(body, fxAILinesRow2Y)
	if len(row1) != 4 || len(row2) != 4 {
		t.Fatalf("%s: parsed %d/%d cell(s) for row 1/2, want 4 each -- the merge assertion below would prove nothing", fxAILines, len(row1), len(row2))
	}

	engine := extraction.LineItemResults([]extraction.DocLine{
		{Index: 1, Description: &row1[0], Quantity: &row1[1], UnitPrice: &row1[2], LineTotal: &row1[3]},
		{Index: 2, Description: &row2[0], Quantity: &row2[1], UnitPrice: &row2[2], LineTotal: &row2[3]},
	})

	tokenPages := rvCorpusPages(t, fxAILines) // the COMMITTED file, not the built bytes: the committed file is what deploys.
	ai := fxDecodeAILinesMarker(t, fxAILinesMarker())

	got := extraction.MergeAILinesForTest(engine, ai, tokenPages)

	byIndex := map[int]map[string]extraction.FieldResult{}
	var indexes []int
	for _, r := range got {
		idx, role, ok := extraction.ParseLineFieldName(r.Name)
		if !ok {
			t.Fatalf("mergeAILines returned a non-line row %q -- ai != nil so every row must be a line cell", r.Name)
		}
		if byIndex[idx] == nil {
			byIndex[idx] = map[string]extraction.FieldResult{}
		}
		byIndex[idx][role] = r
		indexes = append(indexes, idx)
	}
	slices.Sort(indexes)
	indexes = slices.Compact(indexes)
	if !slices.Equal(indexes, []int{1, 2, 3}) {
		t.Fatalf("mergeAILines line indices = %v, want contiguous [1 2 3]", indexes)
	}

	// Row 1: untouched -- every cell the engine's own reading, no alternatives.
	row1Want := map[string]string{"description": row1[0], "quantity": row1[1], "unit_price": row1[2], "line_total": row1[3]}
	for role, want := range row1Want {
		got := byIndex[1][role]
		if got.Value == nil || *got.Value != want || got.Reason != extraction.ReasonNone || len(got.Alternatives) != 0 {
			t.Errorf("row 1 %s = %+v, want decided %q with no alternatives", role, got, want)
		}
	}

	// Row 2: ambiguous on unit_price only, the engine's value standing with the AI's as the one
	// alternative; every other role untouched by the disagreement.
	up := byIndex[2]["unit_price"]
	if up.Value == nil || *up.Value != row2[2] || up.Reason != extraction.ReasonAmbiguous ||
		len(up.Alternatives) != 1 || up.Alternatives[0].Value == nil || *up.Alternatives[0].Value != fxAILinesDisagreedUnitPrice {
		t.Errorf("row 2 unit_price = %+v, want ambiguous at %q with one alternative %q", up, row2[2], fxAILinesDisagreedUnitPrice)
	}
	row2Want := map[string]string{"description": row2[0], "quantity": row2[1], "line_total": row2[3]}
	for role, want := range row2Want {
		got := byIndex[2][role]
		if got.Value == nil || *got.Value != want || got.Reason != extraction.ReasonNone {
			t.Errorf("row 2 %s = %+v, want decided %q, untouched by the unit_price disagreement", role, got, want)
		}
	}

	// Row 3: AI-only, unmarked (Q12) -- every cell reason none, no alternatives.
	row3Want := map[string]string{
		"description": fxAILinesAIOnlyRow[0], "quantity": fxAILinesAIOnlyRow[1],
		"unit_price": fxAILinesAIOnlyRow[2], "line_total": fxAILinesAIOnlyRow[3],
	}
	for role, want := range row3Want {
		got := byIndex[3][role]
		if got.Value == nil || *got.Value != want || got.Reason != extraction.ReasonNone || len(got.Alternatives) != 0 {
			t.Errorf("row 3 %s = %+v, want unmarked %q", role, got, want)
		}
	}
}

// --- reading a fixture back -------------------------------------------------

var (
	fxKidsRe     = regexp.MustCompile(`/Kids\s*\[([^\]]*)\]`)
	fxRefRe      = regexp.MustCompile(`(\d+)\s+0\s+R`)
	fxContentsRe = regexp.MustCompile(`/Contents\s+(\d+)\s+0\s+R`)
)

// fxObjects splits a PDF into its indirect object bodies by number. Index-based rather than
// regexp: a stream body is binary and may hold any byte sequence.
func fxObjects(raw []byte) map[int][]byte {
	out := map[int][]byte{}
	marker := []byte(" 0 obj")
	for i := 0; i < len(raw); {
		j := bytes.Index(raw[i:], marker)
		if j < 0 {
			break
		}
		at := i + j
		k := at
		for k > 0 && raw[k-1] >= '0' && raw[k-1] <= '9' {
			k--
		}
		start := at + len(marker)
		i = start
		num, err := strconv.Atoi(string(raw[k:at]))
		if err != nil {
			continue
		}
		end := bytes.Index(raw[start:], []byte("endobj"))
		if end < 0 {
			break
		}
		out[num] = bytes.TrimSpace(raw[start : start+end])
		i = start + end
	}
	return out
}

// fxPages returns the page objects in /Kids order, so a test reads page 1 as the document
// declares it rather than as the file happens to be laid out.
func fxPages(t *testing.T, objs map[int][]byte) [][]byte {
	t.Helper()

	var tree []byte
	for _, body := range objs {
		if bytes.Contains(body, []byte("/Type /Pages")) {
			tree = body
			break
		}
	}
	if tree == nil {
		t.Fatalf("no /Type /Pages object among %d parsed object(s); the page assertions below would read nothing", len(objs))
	}
	kids := fxKidsRe.FindSubmatch(tree)
	if kids == nil {
		t.Fatalf("the /Type /Pages object carries no /Kids array: %q", tree)
	}

	var pages [][]byte
	for _, ref := range fxRefRe.FindAllSubmatch(kids[1], -1) {
		num, err := strconv.Atoi(string(ref[1]))
		if err != nil {
			continue
		}
		body, ok := objs[num]
		if !ok {
			t.Fatalf("/Kids names object %d, which the parse did not find", num)
		}
		pages = append(pages, body)
	}
	return pages
}

// fxContent returns one page's content stream body.
func fxContent(t *testing.T, objs map[int][]byte, page []byte) []byte {
	t.Helper()

	m := fxContentsRe.FindSubmatch(page)
	if m == nil {
		t.Fatalf("page object carries no /Contents reference: %q", page)
	}
	num, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("page object names a non-numeric /Contents object %q", m[1])
	}
	obj, ok := objs[num]
	if !ok {
		t.Fatalf("/Contents names object %d, which the parse did not find", num)
	}

	i := bytes.Index(obj, []byte("stream"))
	if i < 0 {
		t.Fatalf("content object %d is not a stream: %q", num, obj)
	}
	j := i + len("stream")
	if j < len(obj) && obj[j] == '\r' {
		j++
	}
	if j < len(obj) && obj[j] == '\n' {
		j++
	}
	end := bytes.LastIndex(obj, []byte("endstream"))
	if end < j {
		t.Fatalf("content object %d has no endstream after its stream keyword", num)
	}
	body := bytes.TrimRight(obj[j:end], "\r\n")
	if len(body) == 0 {
		t.Fatalf("content object %d has an empty stream; the operator assertions below would be vacuous", num)
	}
	return body
}

func fxRead(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(fxDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v -- the corpus is committed; regenerate it with `go test ./internal/extraction/ -run TestFixtures_MatchTheirGenerator -update`", name, err)
	}
	return raw
}

// fxAssertWellFormed is the floor under every comparison below: two empty slices are equal,
// and two runs of a generator that emits nothing prove nothing.
func fxAssertWellFormed(t *testing.T, name string, b []byte) {
	t.Helper()

	if len(b) < fxMinBytes {
		t.Fatalf("%s generated %d byte(s), want at least %d", name, len(b), fxMinBytes)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatalf("%s does not start with %%PDF-: % x", name, b[:min(16, len(b))])
	}
	if !bytes.HasSuffix(bytes.TrimRight(b, "\r\n"), []byte("%%EOF")) {
		t.Fatalf("%s does not end with %%%%EOF", name)
	}
}

// --- the tests --------------------------------------------------------------

func TestFixtures_MatchTheirGenerator(t *testing.T) {
	if *fxUpdate {
		if err := os.MkdirAll(fxDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", fxDir, err)
		}
		for _, f := range fxCorpus {
			if err := os.WriteFile(filepath.Join(fxDir, f.name), f.build(), 0o644); err != nil {
				t.Fatalf("write %s: %v", f.name, err)
			}
		}
	}

	entries, err := os.ReadDir(fxDir)
	if err != nil {
		t.Fatalf("read %s: %v -- the corpus is committed, so an absent directory is the failure and not a skip", fxDir, err)
	}
	pdfs := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pdf") {
			pdfs++
		}
	}
	if pdfs < len(fxCorpus) {
		t.Fatalf("%s holds %d .pdf file(s), want at least %d -- a byte-compare over a corpus that is not there reports nothing", fxDir, pdfs, len(fxCorpus))
	}

	for _, f := range fxCorpus {
		t.Run(f.name, func(t *testing.T) {
			want := f.build()
			fxAssertWellFormed(t, f.name, want)

			got := fxRead(t, f.name)
			if !bytes.Equal(got, want) {
				t.Errorf("committed %s does not match its generator: %d byte(s) on disk, %d regenerated -- it was hand-edited, or the generator changed and the bytes were not regenerated with -update", f.name, len(got), len(want))
			}
		})
	}
}

func TestFixtures_GeneratorIsDeterministic(t *testing.T) {
	for _, f := range fxCorpus {
		t.Run(f.name, func(t *testing.T) {
			first := f.build()
			fxAssertWellFormed(t, f.name, first)

			second := f.build()
			if !bytes.Equal(first, second) {
				t.Errorf("%s generated %d byte(s) then %d byte(s) in one process -- AC-3 needs the same PDF twice to be byte-identical, and a corpus built on a timestamp or a map walk is worthless", f.name, len(first), len(second))
			}
		})
	}
}

// fxUngenerated is every name in committed that no fxCorpus entry builds.
func fxUngenerated(committed []string) []string {
	built := map[string]bool{}
	for _, f := range fxCorpus {
		built[f.name] = true
	}
	var out []string
	for _, n := range committed {
		if !built[n] {
			out = append(out, n)
		}
	}
	return out
}

// A committed wild_ PDF with no fxCorpus entry is never byte-compared by TestFixtures_MatchTheirGenerator.
func TestFixtures_EveryCommittedWildArrangementHasAGenerator(t *testing.T) {
	entries, err := os.ReadDir(fxDir)
	if err != nil {
		t.Fatalf("read %s: %v", fxDir, err)
	}
	var wild []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasPrefix(n, "wild_") && strings.HasSuffix(n, ".pdf") {
			wild = append(wild, n)
		}
	}
	if len(wild) < 8 {
		t.Fatalf("%s holds %d wild_*.pdf file(s), want at least 8", fxDir, len(wild))
	}
	if missing := fxUngenerated(wild); len(missing) != 0 {
		t.Errorf("%v committed with no fxCorpus entry", missing)
	}
	if got := fxUngenerated(append(slices.Clone(wild), "wild_planted.pdf")); !slices.Equal(got, []string{"wild_planted.pdf"}) {
		t.Errorf("a planted wild_planted.pdf reports %v, want exactly [wild_planted.pdf]", got)
	}
}

// A committed PDF with no fxCorpus entry is never byte-compared by TestFixtures_MatchTheirGenerator.
func TestFixtures_EveryCommittedFixtureHasAGenerator(t *testing.T) {
	entries, err := os.ReadDir(fxDir)
	if err != nil {
		t.Fatalf("read %s: %v", fxDir, err)
	}
	var committed []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".pdf") {
			committed = append(committed, n)
		}
	}
	if len(committed) < fgGoldenFloor {
		t.Fatalf("%s holds %d .pdf file(s), want at least %d", fxDir, len(committed), fgGoldenFloor)
	}
	if missing := fxUngenerated(committed); len(missing) != 0 {
		t.Errorf("%v committed with no fxCorpus entry", missing)
	}
}

// fxImageResRe resolves a page's /Im0 through /Resources. A whole-file scan for
// "/Subtype /Image" would pass on an image object no page references, and on a page whose ink
// is drawn by a stream the page never names.
var fxImageResRe = regexp.MustCompile(`/XObject\s*<<\s*/Im0\s+(\d+)\s+0\s+R`)

func fxImageObjFor(t *testing.T, objs map[int][]byte, page []byte) []byte {
	t.Helper()

	m := fxImageResRe.FindSubmatch(page)
	if m == nil {
		t.Fatalf("page object carries no /XObject << /Im0 n 0 R >> resource: %q", page)
	}
	num, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("page names a non-numeric /Im0 object %q", m[1])
	}
	obj, ok := objs[num]
	if !ok {
		t.Fatalf("/Im0 names object %d, which the parse did not find", num)
	}
	return obj
}

// TestFixtures_TheScannedWildFixtureHasNoTextLayer covers both image-only fixtures: the 4x4
// checkerboard AC-6 needs and the raster-ink page OCR can actually read. The image control runs
// first and fatals -- every absence below is equally true of a blank page.
func TestFixtures_TheScannedWildFixtureHasNoTextLayer(t *testing.T) {
	for _, name := range []string{fxScanned, fxWildScanned} {
		t.Run(name, func(t *testing.T) {
			raw := fxRead(t, name)
			objs := fxObjects(raw)
			pages := fxPages(t, objs)
			if len(pages) < 1 {
				t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), name)
			}

			for i, page := range pages {
				if !bytes.Contains(fxImageObjFor(t, objs, page), []byte("/Subtype /Image")) {
					t.Fatalf("%s page %d's /Im0 is not an image XObject; it is not an image-only document, so the absences below prove nothing", name, i+1)
				}
				if fxFontResRe.Match(page) {
					t.Errorf("%s page %d declares a /Font resource; a scan carries no text layer at all", name, i+1)
				}

				body := fxContent(t, objs, page)
				if !bytes.Contains(body, []byte("Do")) {
					t.Errorf("%s page %d draws no XObject; an empty page is not a scan", name, i+1)
				}
				// Two assertions, not one: a combined check cannot say which absence failed.
				if bytes.Contains(body, []byte("BT")) {
					t.Errorf("%s page %d content stream carries the BT operator; pdfium would report a text layer", name, i+1)
				}
				if bytes.Contains(body, []byte("Tj")) {
					t.Errorf("%s page %d content stream carries the Tj operator; pdfium would report a text layer", name, i+1)
				}
			}
		})
	}
}

func TestFixtures_HybridHasTextOnPageOneOnly(t *testing.T) {
	raw := fxRead(t, fxHybrid)

	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 2 {
		t.Fatalf("found %d page object(s) in %s, want at least 2 -- the whole point of this fixture is that its two pages differ", len(pages), fxHybrid)
	}

	native := fxContent(t, objs, pages[0])
	for _, op := range []string{"BT", "Tj"} {
		if !bytes.Contains(native, []byte(op)) {
			t.Errorf("%s page 1 content stream lacks the %s operator; page 1 is the native half", fxHybrid, op)
		}
	}
	if !bytes.Contains(pages[0], []byte("/Font")) {
		t.Errorf("%s page 1 declares no /Font resource", fxHybrid)
	}

	scanned := fxContent(t, objs, pages[1])
	if !bytes.Contains(pages[1], []byte("/XObject")) {
		t.Errorf("%s page 2 declares no /XObject resource; page 2 is the scanned half", fxHybrid)
	}
	if !bytes.Contains(scanned, []byte("Do")) {
		t.Errorf("%s page 2 draws no XObject", fxHybrid)
	}
	for _, op := range []string{"BT", "Tj"} {
		if bytes.Contains(scanned, []byte(op)) {
			t.Errorf("%s page 2 content stream carries the %s operator; page 2 has no text layer", fxHybrid, op)
		}
	}
}

func TestFixtures_RichInvoicePrintsItsOwnNumber(t *testing.T) {
	raw := fxRead(t, fxRich)

	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 1 {
		t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), fxRich)
	}
	body := fxContent(t, objs, pages[0])

	if !bytes.Contains(body, []byte("ASC-2026-0918")) {
		t.Errorf("%s page 1 content stream does not carry ASC-2026-0918, its own invoice number", fxRich)
	}
	if bytes.Contains(body, []byte("INV-001")) {
		t.Errorf("%s page 1 content stream carries INV-001, another fixture's number", fxRich)
	}
}

// fxRuleOpRe matches one fxRuleH/fxRuleV emission ("%d %d m\n%d %d l\nS\n"): a horizontal
// rule's pair share the Y operand (2nd/4th group), a vertical rule's share the X operand
// (1st/3rd group). The trailing \n is optional: fxContent trims trailing "\r\n" off the
// stream, so the last rule in emission order has no trailing newline to match.
var fxRuleOpRe = regexp.MustCompile(`(\d+) (\d+) m\n(\d+) (\d+) l\nS\n?`)

func TestFixtures_RichInvoiceCarriesARuledTable(t *testing.T) {
	raw := fxRead(t, fxRich)

	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 1 {
		t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), fxRich)
	}
	body := fxContent(t, objs, pages[0])

	horiz, vert := 0, 0
	for _, m := range fxRuleOpRe.FindAllSubmatch(body, -1) {
		a, b, c, d := string(m[1]), string(m[2]), string(m[3]), string(m[4])
		switch {
		case b == d && a != c:
			horiz++
		case a == c && b != d:
			vert++
		}
	}
	if horiz != 6 {
		t.Errorf("%s page 1 carries %d horizontal rule(s), want exactly 6", fxRich, horiz)
	}
	if vert != 5 {
		t.Errorf("%s page 1 carries %d vertical rule(s), want exactly 5", fxRich, vert)
	}

	for _, row := range [][]string{
		{"Widget", "2", "500.00", "1000.00"},
		{"Gadget", "3", "250.00", "900.00"},
		{"Delivery", "1", "120.00", "120.00"},
	} {
		for _, cell := range row {
			if !bytes.Contains(body, []byte(cell)) {
				t.Errorf("%s page 1 content stream does not carry data row cell %q", fxRich, cell)
			}
		}
	}
}

// fxTjRe matches one fxText Tj emission ("%d %d Td\n(%s) Tj"): the Td x/y and the string drawn.
var fxTjRe = regexp.MustCompile(`(\d+) (\d+) Td\n\(([^)]*)\) Tj`)

// AC-6: the totals block is a label Tj followed by a value Tj on the same baseline, not one
// inline "Label: value" string -- Reconcile's sum-check (reconcile.go:172) only tightens a
// subtotal that already resolved ReasonNone, which an inline string would not.
func TestFixtures_RichInvoiceTotalsAreSplitLabels(t *testing.T) {
	raw := fxRead(t, fxRich)

	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 1 {
		t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), fxRich)
	}
	body := fxContent(t, objs, pages[0])

	type placed struct{ x, y int }
	toks := map[string]placed{}
	matches := fxTjRe.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatalf("%s page 1 content stream carries no Td/Tj pair; the assertions below would prove nothing", fxRich)
	}
	for _, m := range matches {
		x, _ := strconv.Atoi(string(m[1]))
		y, _ := strconv.Atoi(string(m[2]))
		text := string(m[3])
		toks[text] = placed{x, y}
		for _, pair := range [][2]string{{"Sub-total", "1,500.00"}, {"VAT", "112.50"}, {"Total", "1,612.50"}} {
			if strings.Contains(text, pair[0]) && strings.Contains(text, pair[1]) {
				t.Errorf("%s: one Tj %q carries both %q and %q -- want a split label/value pair", fxRich, text, pair[0], pair[1])
			}
		}
	}

	for _, pair := range []struct{ label, value string }{
		{"Sub-total", "1,500.00"},
		{"VAT", "112.50"},
		{"Total", "1,612.50"},
	} {
		l, ok := toks[pair.label]
		if !ok {
			t.Fatalf("%s: no Tj carries the bare label %q", fxRich, pair.label)
		}
		v, ok := toks[pair.value]
		if !ok {
			t.Fatalf("%s: no Tj carries the bare value %q", fxRich, pair.value)
		}
		if l.y != v.y {
			t.Errorf("%s: label %q sits at y=%d, value %q at y=%d -- want the same baseline", fxRich, pair.label, l.y, pair.value, v.y)
		}
		if l.x >= v.x {
			t.Errorf("%s: label %q at x=%d is not left of value %q at x=%d", fxRich, pair.label, l.x, pair.value, v.x)
		}
	}
}

// TestFixtures_RichInvoiceCarriesItsIssueDate pins the ambiguous day/month reading (D-10) that
// story AC 2's later 'ambiguous' verdict depends on -- nothing local pins it otherwise.
func TestFixtures_RichInvoiceCarriesItsIssueDate(t *testing.T) {
	raw := fxRead(t, fxRich)

	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 1 {
		t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), fxRich)
	}
	body := fxContent(t, objs, pages[0])

	if !bytes.Contains(body, []byte("12/03/2026")) {
		t.Errorf("%s page 1 content stream does not carry 12/03/2026, its ambiguous issue date", fxRich)
	}
}

// TestFixtures_RichInvoiceBytesAreUnique guards against fxBuildRichInvoice silently returning
// another fixture's bytes -- a bug several presence-only assertions elsewhere would not catch.
func TestFixtures_RichInvoiceBytesAreUnique(t *testing.T) {
	rich := fxRead(t, fxRich)
	for _, f := range fxCorpus {
		if f.name == fxRich {
			continue
		}
		other := fxRead(t, f.name)
		if bytes.Equal(rich, other) {
			t.Errorf("%s is byte-identical to %s -- the builder returned the wrong fixture's bytes", fxRich, f.name)
		}
	}
}

// TestFixtures_RichInvoiceDoesNotResizeTableRowYs pins fxBuildTable's own row geometry: the
// rich fixture's plan called for a separate fxRichTableRowYs precisely so this array stays
// untouched and table_invoice.pdf's committed bytes do not silently drift.
func TestFixtures_RichInvoiceDoesNotResizeTableRowYs(t *testing.T) {
	want := [4]int{650, 626, 602, 578}
	if fxTableRowYs != want {
		t.Fatalf("fxTableRowYs is %v, want %v -- EXTR-18-01 must not resize fxBuildTable's own geometry", fxTableRowYs, want)
	}
}

// fxBuildNPage is n blank US-Letter pages, for the page-cap boundary. MediaBox is inherited
// from the page tree and a page carries no content stream, so one page costs about 75 bytes
// and an 801-page document is ~60 KiB -- generated in-test, never committed.
func fxBuildNPage(n int) []byte {
	objs := make([]fxObject, 2, n+2)
	objs[0] = fxObject("<< /Type /Catalog /Pages 2 0 R >>")

	kids := make([]string, 0, n)
	for i := range n {
		kids = append(kids, fmt.Sprintf("%d 0 R", i+3))
		objs = append(objs, fxObject("<< /Type /Page /Parent 2 0 R >>"))
	}
	objs[1] = fxObject(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /MediaBox [0 0 %d %d] >>",
		strings.Join(kids, " "), n, fxPageWidthPt, fxPageHeightPt))

	return fxAssemble(objs)
}

// fxE2EDir holds the Playwright-side copies of a subset of this package's PDF corpus.
const fxE2EDir = "../../e2e/fixtures/documents"

// fxE2ECopies is the explicit table AC-2 requires: each name here must be byte-identical between
// fxE2EDir and testdata/. Table-driven, not a directory walk, because fxE2EDir also holds
// native_invoice_2p.pdf, which has no Go-side original of that name.
var fxE2ECopies = []string{fxNative, fxScanned, fxDense, fxRich, fxAdvisoryRegister, fxChromeRegister, fxChromeRegisterTwin, fxAISteered, fxAIUnavailable, fxAILines, fxJevDoubt}

// fxE2EExempt: native_invoice_2p.pdf has no Go-side original -- its closest analog, native_3page.pdf, is a different file.
var fxE2EExempt = map[string]bool{"native_invoice_2p.pdf": true}

// TestFixtures_E2ECopiesMatchTheirGoInvoiceOriginals closes a real gap: nothing held
// e2e/fixtures/documents/native_invoice.pdf in step with its testdata/ original, so the two
// could silently diverge. The completeness scan below guards fxE2ECopies itself: a new copy
// dropped into fxE2EDir without a table entry (and without an fxE2EExempt reason) fails here.
func TestFixtures_E2ECopiesMatchTheirGoInvoiceOriginals(t *testing.T) {
	for _, name := range fxE2ECopies {
		t.Run(name, func(t *testing.T) {
			want := fxRead(t, name)
			got, err := os.ReadFile(filepath.Join(fxE2EDir, name))
			if err != nil {
				t.Fatalf("read %s: %v", filepath.Join(fxE2EDir, name), err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s/%s does not match testdata/%s: %d byte(s) vs %d", fxE2EDir, name, name, len(got), len(want))
			}
		})
	}

	entries, err := os.ReadDir(fxE2EDir)
	if err != nil {
		t.Fatalf("read %s: %v", fxE2EDir, err)
	}
	covered := make(map[string]bool, len(fxE2ECopies))
	for _, name := range fxE2ECopies {
		covered[name] = true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pdf") || fxE2EExempt[e.Name()] {
			continue
		}
		if !covered[e.Name()] {
			t.Errorf("%s/%s is committed but fxE2ECopies does not guard it -- add it to the table or fxE2EExempt", fxE2EDir, e.Name())
		}
	}
}

// fxE2EExempt is a hole in the completeness scan above by design. Pinned at exactly one entry
// so a future unguarded copy cannot be waved through by silently appending to it.
func TestFixtures_E2EExemptionListStaysSingular(t *testing.T) {
	if len(fxE2EExempt) != 1 {
		t.Errorf("fxE2EExempt has %d entries, want exactly 1: %v -- each new exemption defeats the completeness scan for one more file", len(fxE2EExempt), fxE2EExempt)
	}
}

// --- the naira builder variant ----------------------------------------------

var (
	fxFontResRe   = regexp.MustCompile(`/Font\s*<<\s*/F1\s+(\d+)\s+0\s+R`)
	fxToUnicodeRe = regexp.MustCompile(`/ToUnicode\s+(\d+)\s+0\s+R`)
)

// fxFontObj resolves the page's /F1 through /Resources rather than scanning the whole file:
// a whole-file match would pass on a font object no page references.
func fxFontObj(t *testing.T, objs map[int][]byte, page []byte) []byte {
	t.Helper()

	m := fxFontResRe.FindSubmatch(page)
	if m == nil {
		t.Fatalf("page object carries no /Font << /F1 n 0 R >> resource: %q", page)
	}
	num, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("page names a non-numeric /F1 object %q", m[1])
	}
	obj, ok := objs[num]
	if !ok {
		t.Fatalf("/F1 names object %d, which the parse did not find", num)
	}
	return obj
}

// fxTokens reads an in-memory PDF through the real reader. Built bytes, not a committed
// fixture: this variant commits none.
func fxTokens(t *testing.T, raw []byte) []extraction.Token {
	t.Helper()

	var pages []extraction.TokenPage
	if _, err := extraction.NewPDFiumReader().Read(t.Context(),
		extraction.Document{Bytes: raw, ContentType: "application/pdf"},
		extraction.CollectTokens(&pages)); err != nil {
		t.Fatalf("Read the built page: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("read %d page(s) from the built PDF, want 1", len(pages))
	}
	return pages[0].Tokens
}

func fxTexts(toks []extraction.Token) []string {
	out := make([]string, 0, len(toks))
	for _, tok := range toks {
		out = append(out, tok.Text)
	}
	return out
}

// fxAmountToken finds the token carrying the amount, having first proved the read worked at
// all: without the "Total" needle a reader that returned one garbage token would look like a
// reader that returned the amount wrongly encoded.
func fxAmountToken(t *testing.T, toks []extraction.Token) extraction.Token {
	t.Helper()

	if len(toks) < 3 {
		t.Fatalf("read %d token(s) from the built page, want at least 3: %q", len(toks), fxTexts(toks))
	}
	seenLabel := false
	for _, tok := range toks {
		if strings.TrimSpace(tok.Text) == "Total" {
			seenLabel = true
			break
		}
	}
	if !seenLabel {
		t.Fatalf("no token reads %q; the read did not find the page's text at all: %q", "Total", fxTexts(toks))
	}
	for _, tok := range toks {
		if strings.Contains(tok.Text, fxAmount) {
			return tok
		}
	}
	t.Fatalf("no token carries %q: %q", fxAmount, fxTexts(toks))
	return extraction.Token{}
}

// AC-1.
func TestFixtures_TheNairaBuilderEmitsBothObjects(t *testing.T) {
	raw := fxNairaTextPage(true, fxNairaLines()...)
	fxAssertWellFormed(t, "naira variant", raw)

	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) != 1 {
		t.Fatalf("the variant emitted %d page(s), want 1", len(pages))
	}

	// The control needle: fxContent fatals on an unresolvable or empty stream, and the escape
	// proves the assertions below are about a page that actually draws a naira.
	body := fxContent(t, objs, pages[0])
	if !bytes.Contains(body, []byte(fxNaira)) {
		t.Fatalf("the content stream carries no %s escape, so the font assertions below would be about a page with no naira on it: %q", fxNaira, body)
	}

	font := fxFontObj(t, objs, pages[0])

	if !bytes.Contains(font, []byte("/Differences [164 /naira]")) {
		t.Errorf("the font dict carries no /Differences [164 /naira]; without it the drawn glyph is a currency sign: %q", font)
	}

	m := fxToUnicodeRe.FindSubmatch(font)
	if m == nil {
		t.Fatalf("the font dict carries no /ToUnicode reference: %q", font)
	}
	num, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("the font dict names a non-numeric /ToUnicode object %q", m[1])
	}
	cmap, ok := objs[num]
	if !ok {
		t.Fatalf("/ToUnicode names object %d, which the parse did not find -- the CMap is orphaned", num)
	}
	for _, want := range []string{"beginbfchar", "<A4> <20A6>", "endbfchar"} {
		if !bytes.Contains(cmap, []byte(want)) {
			t.Errorf("the /ToUnicode stream (object %d) carries no %q: %q", num, want, cmap)
		}
	}
}

// AC-2.
func TestFixtures_TheNairaBuilderPrintsARealNairaSign(t *testing.T) {
	tok := fxAmountToken(t, fxTokens(t, fxNairaTextPage(true, fxNairaLines()...)))

	want := append([]byte{0xE2, 0x82, 0xA6}, fxAmount...)
	if !bytes.Equal([]byte(tok.Text), want) {
		t.Errorf("the amount token reads %q (% x), want %q (% x)", tok.Text, tok.Text, want, want)
	}

	got := extraction.ShapeAmount.Normalize(tok.Text)
	if len(got) != 1 || got[0] != "1075.00" {
		t.Errorf("ShapeAmount.Normalize(%q) = %v, want [1075.00]", tok.Text, got)
	}
}

// AC-3: the control that stops AC-2 holding for some reason other than the CMap.
func TestFixtures_WithoutTheToUnicodeCMapTheGlyphReadsAsCurrencySign(t *testing.T) {
	raw := fxNairaTextPage(false, fxNairaLines()...)
	if bytes.Contains(raw, []byte("/ToUnicode")) {
		t.Fatalf("the withCMap=false build still carries a /ToUnicode; this spec would not be measuring its removal")
	}

	tok := fxAmountToken(t, fxTokens(t, raw))

	if !strings.ContainsRune(tok.Text, '¤') {
		t.Errorf("without the CMap the amount token reads %q (% x), want a U+00A4 currency sign -- the /Differences glyph name alone does not carry Unicode", tok.Text, tok.Text)
	}
	if strings.ContainsRune(tok.Text, '₦') {
		t.Errorf("the amount token reads U+20A6 with no /ToUnicode object: %q -- the CMap is not what makes the naira extractable, so AC-2 proves nothing", tok.Text)
	}
}

// AC-5: a second flag.Bool("update", ...) panics the test binary at registration, before any
// test runs. The needle is assembled from fragments so this scan does not match itself, and
// comment lines are skipped so a prose mention of the flag is not counted as one.
func TestFixtures_TheNairaVariantAddsNoSecondUpdateFlag(t *testing.T) {
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob *_test.go: %v", err)
	}
	// The floor first: this scan asserts an ABSENCE, and zero files read reports clean.
	if len(names) < 50 {
		t.Fatalf("read %d test file(s) in internal/extraction, want at least 50 (93 measured)", len(names))
	}

	registration := regexp.MustCompile(`flag\.Bo` + `ol\("upd` + `ate"`)

	sites := map[string][]int{}
	total := 0
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if registration.MatchString(line) {
				sites[name] = append(sites[name], i+1)
				total++
			}
		}
	}

	// The control: a needle that matches nothing reports a clean repo forever.
	if total == 0 {
		t.Fatalf("found zero flag registrations across %d test file(s); the needle no longer matches the real one in fixtures_test.go", len(names))
	}
	if total != 1 {
		t.Errorf("found %d -update registrations, want exactly 1: %v", total, sites)
	}
	if len(sites["fixtures_test.go"]) != 1 {
		t.Errorf("the -update flag is registered at %v, want exactly one site in fixtures_test.go", sites)
	}
}

// The variant commits no fixture, so TestFixtures_GeneratorIsDeterministic -- which iterates
// fxCorpus -- never reaches it. Two in-process builds close that gap without committing one.
func TestFixtures_TheNairaVariantIsByteDeterministic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withCMap bool
	}{
		{"with the CMap", true},
		{"without the CMap", false},
	} {
		first := fxNairaTextPage(tc.withCMap, fxNairaLines()...)
		fxAssertWellFormed(t, "naira variant "+tc.name, first)
		if second := fxNairaTextPage(tc.withCMap, fxNairaLines()...); !bytes.Equal(first, second) {
			t.Errorf("the naira variant %s built %d and %d byte(s) on two calls; the builder is not deterministic", tc.name, len(first), len(second))
		}
	}

	// The control: equal-to-itself holds just as well on a builder that ignores withCMap.
	if bytes.Equal(fxNairaTextPage(true, fxNairaLines()...), fxNairaTextPage(false, fxNairaLines()...)) {
		t.Errorf("withCMap true and false built identical bytes; the determinism clauses above hold over a knob that does nothing")
	}
}

// The control build drops an object from the slice, and fxAssemble numbers by slice index, so
// a shrunken slice is where an off-by-one xref or a dangling reference would land.
func TestFixtures_TheNairaVariantNumbersObjectsByItsSliceLength(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withCMap bool
		objects  int
	}{
		{"with the CMap", true, 6},
		{"without the CMap", false, 5},
	} {
		raw := fxNairaTextPage(tc.withCMap, fxNairaLines()...)
		fxAssertWellFormed(t, "naira variant "+tc.name, raw)

		objs := fxObjects(raw)
		if len(objs) != tc.objects {
			t.Errorf("the naira variant %s emitted %d object(s), want %d", tc.name, len(objs), tc.objects)
		}
		if !bytes.Contains(raw, []byte(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>", tc.objects+1))) {
			t.Errorf("the naira variant %s emitted %d object(s) but its trailer does not declare /Size %d", tc.name, tc.objects, tc.objects+1)
		}

		// Every n 0 R in the file has to name a parsed object, in both builds: withCMap=false
		// removes the CMap, and a font dict still naming it would dangle.
		for _, ref := range fxRefRe.FindAllSubmatch(raw, -1) {
			num, err := strconv.Atoi(string(ref[1]))
			if err != nil {
				continue
			}
			if _, ok := objs[num]; !ok {
				t.Errorf("the naira variant %s references object %d, which it never wrote", tc.name, num)
			}
		}

		pages := fxPages(t, objs)
		if len(pages) != 1 {
			t.Fatalf("the naira variant %s emitted %d page(s), want 1", tc.name, len(pages))
		}
		// Both builds share fxPage(fxFontRes(5), 4), so /F1 resolves to object 5 either way.
		font := fxFontObj(t, objs, pages[0])
		if !bytes.Contains(font, []byte("/BaseFont /Helvetica")) {
			t.Errorf("the naira variant %s resolved /F1 to a non-font object: %q", tc.name, font)
		}
		if !bytes.Contains(fxContent(t, objs, pages[0]), []byte(fxNaira)) {
			t.Errorf("the naira variant %s draws no %s; its render and token clauses would be about a page with no naira on it", tc.name, fxNaira)
		}

		m := fxToUnicodeRe.FindSubmatch(font)
		if !tc.withCMap {
			if m != nil {
				t.Errorf("the naira variant %s still names a /ToUnicode object: %q", tc.name, font)
			}
			continue
		}
		if m == nil {
			t.Fatalf("the naira variant %s names no /ToUnicode object: %q", tc.name, font)
		}
		if got := string(m[1]); got != strconv.Itoa(tc.objects) {
			t.Errorf("the font dict names /ToUnicode %s 0 R, want the last object %d -- fxAssemble numbers by slice index, so the appended CMap is object %d", got, tc.objects, tc.objects)
		}
	}
}

// TestFixtures_DenseIndexHeaderIsTheMeasuredShape pins AC-7: the measured production header is a
// named fixture, and stays distinguishable from its ruled neighbour's S/N-led arrangement.
func TestFixtures_DenseIndexHeaderIsTheMeasuredShape(t *testing.T) {
	if len(fxDenseIndexHeader) != 7 {
		t.Fatalf("len(fxDenseIndexHeader) = %d, want 7", len(fxDenseIndexHeader))
	}
	if fxDenseIndexHeader[0] != "Item" {
		t.Errorf("fxDenseIndexHeader[0] = %q, want %q", fxDenseIndexHeader[0], "Item")
	}
	if fxDenseIndexHeader[1] != "Service description" {
		t.Errorf("fxDenseIndexHeader[1] = %q, want %q", fxDenseIndexHeader[1], "Service description")
	}
	if fxDenseIndexHeader[4] != "Unit rate ₦" {
		t.Errorf("fxDenseIndexHeader[4] = %q, want %q", fxDenseIndexHeader[4], "Unit rate ₦")
	}
	if fxWildRuledHeader[0] != "S/N" {
		t.Errorf("fxWildRuledHeader[0] = %q, want %q -- the two arrangements must stay distinguishable", fxWildRuledHeader[0], "S/N")
	}
}

// --- the as-printed siblings ------------------------------------------------

var (
	// The last block's newline is optional: fxContent trims the stream's trailing EOL.
	fxTextBlockRe = regexp.MustCompile(`BT\n/F1 (\d+) Tf\n(\d+) (\d+) Td\n\((.*)\) Tj\nET\n?`)
	fxAmountRe    = regexp.MustCompile(`[0-9][0-9,]*\.[0-9]{2}`)
)

// fxDrawnLines parses a built one-page PDF back into the lines it draws, each text the literal
// the builder wrote. outside is every object with the content stream's text blocks removed.
func fxDrawnLines(t *testing.T, raw []byte) (lines []fxLine, outside map[int]string) {
	t.Helper()

	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) != 1 {
		t.Fatalf("parsed %d page(s), want 1", len(pages))
	}
	content := string(fxContent(t, objs, pages[0]))
	for _, m := range fxTextBlockRe.FindAllStringSubmatch(content, -1) {
		size, _ := strconv.Atoi(m[1])
		x, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		lines = append(lines, fxLine{size, x, y, m[4]})
	}
	if n := strings.Count(content, "BT\n"); n != len(lines) || n == 0 {
		t.Fatalf("parsed %d of %d text block(s)", len(lines), n)
	}

	contents := fxContentsRe.FindSubmatch(pages[0])
	num, _ := strconv.Atoi(string(contents[1]))
	outside = map[int]string{}
	for n, body := range objs {
		outside[n] = string(body)
	}
	outside[num] = fxTextBlockRe.ReplaceAllString(content, "")
	return lines, outside
}

// fxGeometryProblems reports each line whose size or origin differs between twin and sibling,
// and each text that differs where changed does not declare it, or matches where it does.
func fxGeometryProblems(twin, sibling []fxLine, changed []int) []string {
	if len(twin) != len(sibling) {
		return []string{fmt.Sprintf("the sibling draws %d line(s), its twin %d", len(sibling), len(twin))}
	}
	var out []string
	for i, a := range twin {
		b := sibling[i]
		if a.size != b.size || a.x != b.x || a.y != b.y {
			out = append(out, fmt.Sprintf("line %d moved: %dpt (%d,%d) -> %dpt (%d,%d)", i, a.size, a.x, a.y, b.size, b.x, b.y))
		}
		if declared := slices.Contains(changed, i); declared != (a.text != b.text) {
			out = append(out, fmt.Sprintf("line %d: %q -> %q, declared changed=%v", i, a.text, b.text, declared))
		}
	}
	return out
}

// fxLabelProblems reports a token count other than want, a label that is not the trimmed text
// of exactly one token, and a token carrying two labels or a label beside an amount.
func fxLabelProblems(texts []string, want int, labels []string) []string {
	var out []string
	if len(texts) != want {
		out = append(out, fmt.Sprintf("read %d token(s), want %d", len(texts), want))
	}
	for _, label := range labels {
		n := 0
		for _, text := range texts {
			if strings.TrimSpace(text) == label {
				n++
			}
		}
		if n != 1 {
			out = append(out, fmt.Sprintf("%q is the whole text of %d token(s), want 1", label, n))
		}
	}
	for _, text := range texts {
		var carried []string
		for _, label := range labels {
			if strings.Contains(text, label) {
				carried = append(carried, label)
			}
		}
		if len(carried) > 1 {
			out = append(out, fmt.Sprintf("token %q carries %q", text, carried))
		}
		if len(carried) > 0 && fxAmountRe.MatchString(text) {
			out = append(out, fmt.Sprintf("token %q glues %q to an amount", text, carried))
		}
	}
	return out
}

// fxWildSiblingNamed is a runtime lookup, so an unregistered sibling fails its own subtest.
func fxWildSiblingNamed(t *testing.T, name string) fxWildSibling {
	t.Helper()
	for _, s := range fxWildSiblings {
		if s.name == name {
			return s
		}
	}
	t.Fatalf("fxWildSiblings registers no %s", name)
	return fxWildSibling{}
}

func fxCorpusBuilder(t *testing.T, name string) func() []byte {
	t.Helper()
	for _, f := range fxCorpus {
		if f.name == name {
			return f.build
		}
	}
	t.Fatalf("fxCorpus has no %s", name)
	return nil
}

func TestFixtures_EachAsPrintedSiblingKeepsItsTwinsGeometry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed []int // label lines and the invoice-number line
	}{
		{"wild_two_party_bare_tin_asprinted.pdf", []int{0, 1, 4, 6, 11, 13, 15, 17}},
		{"wild_ruled_lines_totals_asprinted.pdf", []int{0, 1, 8, 9, 10, 11, 28, 30}},
		{"wild_stacked_borderless_asprinted.pdf", []int{0, 1, 2, 3, 5, 9, 13, 15, 17, 19}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fxWildSiblingNamed(t, tc.name)
			twin, twinOutside := fxDrawnLines(t, fxCorpusBuilder(t, s.twin)())
			lines, outside := fxDrawnLines(t, s.build())

			if p := fxGeometryProblems(twin, lines, tc.changed); len(p) > 0 {
				t.Errorf("%s drifts from %s:\n%s", tc.name, s.twin, strings.Join(p, "\n"))
			}
			if !maps.Equal(twinOutside, outside) {
				t.Errorf("%s differs from %s outside its text: rules, fonts or CMap", tc.name, s.twin)
			}

			moved := slices.Clone(lines)
			moved[5].y++
			if p := fxGeometryProblems(lines, moved, nil); len(p) != 1 || !strings.HasPrefix(p[0], "line 5 moved") {
				t.Errorf("a 1pt move at line 5 reported %q, want exactly that line", p)
			}
		})
	}
}

func TestFixtures_EachAsPrintedSiblingReadsItsDeclaredLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens int
		labels []string // the label tables' "pdfium reads" values
	}{
		{"wild_two_party_bare_tin_asprinted.pdf", 19, []string{"Sales Invoice", "VAT Reg. No:", "INVOICE TO:", "Customer No.", "TIN:", "Customer's Signature", "Currency: NGN", "Net Amount", "VAT @ 7.5%", "Total NGN"}},
		{"wild_ruled_lines_totals_asprinted.pdf", 34, []string{"MONTHLY SERVICE INVOICE", "Item", "Service description", "Qty", "Unit rate ₦", "Amount ₦", "Taxable amount", "VAT @ 7.5%"}},
		{"wild_stacked_borderless_asprinted.pdf", 21, []string{"Invoice", "I N V O I C E N U M B E R", "I S S U E D", "BILLED TO", "FROM", "CURRENCY", "Subtotal", "VAT 7.5%", "Amount payable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fxWildSiblingNamed(t, tc.name)
			if p := fxLabelProblems(fxTexts(fxTokens(t, s.build())), tc.tokens, tc.labels); len(p) > 0 {
				t.Errorf("%s:\n%s", tc.name, strings.Join(p, "\n"))
			}
		})
	}

	t.Run("control: TOTAL DUE (NGN) on the Total line", func(t *testing.T) {
		s := fxWildSiblingNamed(t, "wild_ruled_lines_totals_asprinted.pdf")
		lines, _ := fxDrawnLines(t, s.build())
		if !bytes.Equal(s.page(lines...), s.build()) {
			t.Fatal("the parsed lines do not rebuild the sibling's bytes; the control would read another page")
		}
		i := slices.IndexFunc(lines, func(l fxLine) bool { return l.text == "Total" })
		if i < 0 {
			t.Fatal("the ruled sibling draws no Total line")
		}
		lines[i].text = `TOTAL DUE \(NGN\)`
		p := fxLabelProblems(fxTexts(fxTokens(t, s.page(lines...))), 34, []string{"TOTAL DUE (NGN)"})
		if !slices.ContainsFunc(p, func(msg string) bool { return strings.Contains(msg, "glues") }) {
			t.Errorf("the merged label reported %q, want a label glued to an amount", p)
		}
	})
}

// A sibling's number is its twin's plus 100 behind the twin's own label, so the pair stays distinct.
func TestFixtures_EachAsPrintedSiblingDrawsItsTwinsNumberPlus100(t *testing.T) {
	for _, tc := range []struct{ name, twinText, text string }{
		{"wild_two_party_bare_tin_asprinted.pdf", "Invoice No: INV-2101", "Invoice No: INV-2201"},
		{"wild_ruled_lines_totals_asprinted.pdf", "Invoice No: INV-2102", "Invoice No: INV-2202"},
		{"wild_stacked_borderless_asprinted.pdf", "INV-2104", "INV-2204"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fxWildSiblingNamed(t, tc.name)
			twin, _ := fxDrawnLines(t, fxCorpusBuilder(t, s.twin)())
			lines, _ := fxDrawnLines(t, s.build())
			i := slices.IndexFunc(twin, func(l fxLine) bool { return l.text == tc.twinText })
			if i < 0 || i >= len(lines) {
				t.Fatalf("%s draws no %q", s.twin, tc.twinText)
			}
			if lines[i].text != tc.text {
				t.Errorf("line %d draws %q, want %q", i, lines[i].text, tc.text)
			}
		})
	}
}

// --- AC-7: a comment must not cite a test that does not exist -------------------------------

// fxCiteRE matches one cited name inside a comment: the literal four-letter prefix this package's
// test funcs all share, plus four or more further word runes. Every helper below is named to
// avoid carrying a match of its own pattern in its own name.
var fxCiteRE = regexp.MustCompile(`Test[A-Za-z0-9_]{4,}`)

// fxCitedNames is every distinct name fxCiteRE matches in path's comments (AST comment groups
// only, so a t.Errorf format string naming a test is not scanned as a citation).
func fxCitedNames(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	seen := map[string]bool{}
	for _, cg := range f.Comments {
		for _, m := range fxCiteRE.FindAllString(cg.Text(), -1) {
			seen[m] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// fxDeclaredFuncs is every func Test... declared under internal/extraction and its endtoend
// package -- a source scan, since a test binary cannot import another package's _test.go files.
func fxDeclaredFuncs(t *testing.T) map[string]bool {
	t.Helper()
	declRE := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(`)
	var paths []string
	for _, glob := range []string{"*_test.go", "endtoend/*_test.go"} {
		found, err := filepath.Glob(glob)
		if err != nil {
			t.Fatalf("glob %s: %v", glob, err)
		}
		paths = append(paths, found...)
	}
	if len(paths) < 50 {
		t.Fatalf("found %d _test.go file(s) under internal/extraction and endtoend, want at least 50 -- a missing declared name would read as undeclared for the wrong reason", len(paths))
	}
	declared := map[string]bool{}
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		for _, m := range declRE.FindAllStringSubmatch(string(src), -1) {
			declared[m[1]] = true
		}
	}
	if len(declared) < 1000 {
		t.Fatalf("found %d declared Test... func(s), want at least 1000 -- the lookup below would report every name undeclared", len(declared))
	}
	return declared
}

// fxUndeclaredNames reports every name declared holds false for.
func fxUndeclaredNames(names []string, declared map[string]bool) []string {
	var out []string
	for _, n := range names {
		if !declared[n] {
			out = append(out, n)
		}
	}
	return out
}

// AC-7. Scoped to this one file: a package-wide version reds on four pre-existing comments
// elsewhere -- three deliberate "Replaces X" citations and one line-wrap artefact.
func TestFixtures_EveryTestNamedInACommentIsDeclared(t *testing.T) {
	names := fxCitedNames(t, "fixtures_test.go")
	if len(names) == 0 {
		t.Fatalf("fixtures_test.go carries no Test... name in a comment; the loop below would check nothing")
	}
	declared := fxDeclaredFuncs(t)

	if got := fxUndeclaredNames(names, declared); len(got) != 0 {
		t.Errorf("fixtures_test.go cites %v in a comment, declared by no func Test... under internal/extraction or its endtoend package", got)
	}

	// Controls: a name nothing declares is caught; a name everything real declares is not.
	if got := fxUndeclaredNames([]string{"TestNoSuchNameAnywhereInTheRepository"}, declared); len(got) != 1 {
		t.Errorf("a name no source declares reports %q, want exactly one undeclared name", got)
	}
	const known = "TestFixtures_MatchTheirGenerator"
	if !declared[known] {
		t.Fatalf("declared[%s] is false; the positive control below is invalid", known)
	}
	if got := fxUndeclaredNames([]string{known}, declared); len(got) != 0 {
		t.Errorf("a genuinely declared name reports %q, want none", got)
	}
}

// fxHelveticaWidths is Helvetica's own advance width, in 1/1000 em, for every character a
// scoped marker can contain: the base64url alphabet plus the prefix's letters and hyphen.
var fxHelveticaWidths = map[rune]int{
	'0': 556, '1': 556, '2': 556, '3': 556, '4': 556, '5': 556, '6': 556, '7': 556, '8': 556, '9': 556,
	'A': 667, 'B': 667, 'C': 722, 'D': 722, 'E': 667, 'F': 611, 'G': 778, 'H': 722, 'I': 278,
	'J': 500, 'K': 667, 'L': 556, 'M': 833, 'N': 722, 'O': 778, 'P': 667, 'Q': 778, 'R': 722,
	'S': 667, 'T': 611, 'U': 722, 'V': 667, 'W': 944, 'X': 667, 'Y': 667, 'Z': 611,
	'a': 556, 'b': 556, 'c': 500, 'd': 556, 'e': 556, 'f': 278, 'g': 556, 'h': 556, 'i': 222,
	'j': 222, 'k': 500, 'l': 222, 'm': 833, 'n': 556, 'o': 556, 'p': 556, 'q': 556, 'r': 333,
	's': 500, 't': 278, 'u': 556, 'v': 500, 'w': 722, 'x': 500, 'y': 500, 'z': 500,
	'-': 333, '_': 556,
}

// TestFixtures_AILinesMarkerFitsThePageWidth is the local guard on a deployed-only failure:
// the sidecar clips a text run at the page edge and returns its prefix, whose payload is not
// valid base64, so the line call fails and the document quarantines. PDFium parses the content
// stream and never clips, so every other local test stays green on a marker that cannot survive
// the deployed read.
func TestFixtures_AILinesMarkerFitsThePageWidth(t *testing.T) {
	marker := fxAILinesMarker()
	if len(marker) == 0 {
		t.Fatal("the marker is empty, so the width assertion below proves nothing")
	}

	milli := 0
	for _, r := range marker {
		w, ok := fxHelveticaWidths[r]
		if !ok {
			t.Fatalf("no Helvetica width for %q; the marker alphabet grew and this guard no longer measures it", r)
		}
		milli += w
	}
	right := float64(fxAILinesMarkerX) + float64(milli)*float64(fxAILinesMarkerPt)/1000

	// Headroom, not just fit: the payload grows whenever the steered answer does, and a run
	// that ends exactly at the edge re-truncates silently on the next change.
	limit := float64(fxPageWidthPt) * 0.95
	if right > limit {
		t.Errorf("marker of %d chars at %dpt from x=%d ends at %.1fpt, past the %.1fpt limit on a %dpt page: the deployed reader would clip it mid-payload",
			len(marker), fxAILinesMarkerPt, fxAILinesMarkerX, right, limit, fxPageWidthPt)
	}
}

// --- CHECK-01-03: the seven non-invoice builders -----------------------------

func fxBuildNonInvoiceReceipt() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "RECEIPT"},
		fxLine{12, 72, 690, "Receipt No: RCP-3101"},
		fxLine{12, 72, 672, "Date: 2026-02-11"},
		fxLine{12, 72, 654, "Received from: Honeywell Group"},
		fxLine{12, 72, 636, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 618, "Supplier TIN: 99999999-1401"},
		fxLine{12, 72, 600, "Payment method: Bank transfer"},
		fxLine{12, 72, 582, "Amount received: NGN 1,075.00"},
		fxLine{12, 72, 564, "PAID IN FULL"},
		fxLine{12, 72, 546, "This receipt acknowledges payment. It is not a tax invoice."},
	)
}

func fxBuildNonInvoiceProforma() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "PROFORMA INVOICE"},
		fxLine{12, 72, 690, "Proforma No: PF-3201"},
		fxLine{12, 72, 672, "Date: 2026-02-18"},
		fxLine{12, 72, 654, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 636, "Supplier TIN: 99999999-1402"},
		fxLine{12, 72, 618, "Customer: Honeywell Group"},
		fxLine{12, 72, 600, "Currency: NGN"},
		fxLine{12, 72, 582, "Estimated total: NGN 2,150.00"},
		fxLine{12, 72, 564, "This is not a tax invoice. No payment is due on this document."},
		fxLine{12, 72, 546, "Prices are indicative and valid for 14 days."},
	)
}

func fxBuildNonInvoiceQuotation() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "QUOTATION"},
		fxLine{12, 72, 690, "Quotation No: QT-3301"},
		fxLine{12, 72, 672, "Date: 2026-03-03"},
		fxLine{12, 72, 654, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 636, "Supplier TIN: 99999999-1403"},
		fxLine{12, 72, 618, "Customer: Honeywell Group"},
		fxLine{12, 72, 600, "Quoted total: NGN 4,300.00"},
		fxLine{12, 72, 582, "Valid for 30 days from the date above."},
		fxLine{12, 72, 564, "Acceptance of this quotation is required before supply."},
	)
}

func fxBuildNonInvoiceCreditNote() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "CREDIT NOTE"},
		fxLine{12, 72, 690, "Credit Note No: CN-3401"},
		fxLine{12, 72, 672, "Date: 2026-03-19"},
		fxLine{12, 72, 654, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 636, "Supplier TIN: 99999999-1404"},
		fxLine{12, 72, 618, "Customer: Honeywell Group"},
		fxLine{12, 72, 600, "Reason: goods returned"},
		fxLine{12, 72, 582, "Credit amount: NGN -750.00"},
		fxLine{12, 72, 564, "This document reduces the amount owed. It requests no payment."},
	)
}

func fxBuildNonInvoiceDeliveryNote() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "DELIVERY NOTE"},
		fxLine{12, 72, 690, "Delivery Note No: DN-3501"},
		fxLine{12, 72, 672, "Date: 2026-04-08"},
		fxLine{12, 72, 654, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 636, "Supplier TIN: 99999999-1405"},
		fxLine{12, 72, 618, "Deliver to: Honeywell Group"},
		fxLine{12, 72, 600, "Address: 14 Kofo Abayomi Street, Victoria Island, Lagos"},
		fxLine{12, 72, 582, "Item: 40 cartons of bottled water"},
		fxLine{12, 72, 564, "Quantity dispatched: 40"},
		fxLine{12, 72, 546, "Goods received by: ____________"},
		fxLine{12, 72, 528, "No charges are shown on this document."},
	)
}

func fxBuildNonInvoiceStatement() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "STATEMENT OF ACCOUNT"},
		fxLine{12, 72, 690, "Statement No: ST-3601"},
		fxLine{12, 72, 672, "Period: 01/04/2026 to 30/04/2026"},
		fxLine{12, 72, 654, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 636, "Supplier TIN: 99999999-1406"},
		fxLine{12, 72, 618, "Customer: Honeywell Group"},
		fxLine{12, 72, 600, "Opening balance: NGN 12,000.00"},
		fxLine{12, 72, 582, "Payments received: NGN 5,000.00"},
		fxLine{12, 72, 564, "Closing balance: NGN 7,000.00"},
		fxLine{12, 72, 546, "This statement summarises several documents. It is not a tax invoice."},
	)
}

func fxBuildNonInvoicePurchaseOrder() []byte {
	return fxTextPage(
		fxLine{24, 72, 720, "PURCHASE ORDER"},
		fxLine{12, 72, 690, "PO No: PO-3701"},
		fxLine{12, 72, 672, "Date: 2026-05-12"},
		fxLine{12, 72, 654, "Ordered by: Honeywell Group"},
		fxLine{12, 72, 636, "Buyer TIN: 99999999-1407"},
		fxLine{12, 72, 618, "Supplier: Adeyemi Trading Limited"},
		fxLine{12, 72, 600, "Item: 200 reams of A4 paper"},
		fxLine{12, 72, 582, "Order value: NGN 3,600.00"},
		fxLine{12, 72, 564, "Please supply the goods above and invoice on delivery."},
	)
}
