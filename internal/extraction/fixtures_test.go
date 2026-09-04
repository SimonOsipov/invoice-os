// fixtures_test.go: the deterministic PDF corpus. Real client documents cannot be committed,
// so the fixtures are generated here and the bytes they produce are committed beside them --
// TestFixtures_MatchTheirGenerator regenerates and byte-compares, so neither side can drift
// alone. Regenerate a deliberate change with -update and read the diff before committing.
//
// Stdlib only. deps_test.go scan B walks test imports, and any in-module import outside
// internal/platform/* fails it; a third-party writer would also make AC-3's determinism
// someone else's property.
package extraction_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
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
	// Not corpus_-prefixed on purpose: EXTR-18-01's rich fixture, outside every corpus_ ratchet.
	{fxRich, fxBuildRichInvoice},
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
// header/row1, row1/row2, row2/row3, bottom (1 header + 3 data rows = 4 bands = 5 lines).
// A separate array from fxTableRowYs -- that one is fxBuildTable's own, and resizing it would
// change table_invoice.pdf's committed bytes.
var fxRichTableRowYs = [5]int{590, 566, 542, 518, 494}

// fxBuildRichInvoice is one US-Letter page: header fields, a ruled 4-column/3-row table with a
// deliberate line-total error (Gadget: 3 x 250.00 prints as 900.00, not 750.00), and a
// split-label totals block whose Sub-total (1,500.00) does not match the line sum (2,020.00).
// Both mismatches exceed reconcileTolerance, so ReasonInconsistentTotal has something to catch.
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
	// Split-label shape (label Tj, then value Tj, same baseline) -- fxBuildCorpusTotalsBlock's
	// precedent. The inline single-Tj form would not resolve ReasonNone for Reconcile to check.
	lines = append(lines,
		fxLine{12, 380, 440, "Sub-total"}, fxLine{12, 500, 440, "1,500.00"},
		fxLine{12, 380, 422, "VAT"}, fxLine{12, 500, 422, "112.50"},
		fxLine{12, 380, 404, "Total"}, fxLine{12, 500, 404, "1,612.50"},
	)

	// V before H: fxContent's TrimRight(body, "\r\n") swallows the trailing "\n" of whichever
	// rule is emitted last, dropping it from a reader's rule count. Verticals first keeps the
	// swallowed rule a horizontal one, which still clears its lower (>=4 vs >=5) floor.
	var rules bytes.Buffer
	for _, x := range fxTableColXs {
		rules.WriteString(fxRuleV(x, fxRichTableRowYs[len(fxRichTableRowYs)-1], fxRichTableRowYs[0]))
	}
	for _, y := range fxRichTableRowYs {
		rules.WriteString(fxRuleH(y, fxTableColXs[0], fxTableColXs[len(fxTableColXs)-1]))
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
// half. That is the whole trick: t1.buyer_tin.sweep is banded BandPage1Bottom so it cannot
// reach the buyer's TIN, and the buyer_tin lexicon needs a party word beside a TIN word, which
// a bare number does not carry -- so Tier-1 alone returns ZERO buyer_tin candidates.
//
// supplier_tin therefore reads "ambiguous" here: t1.supplier_tin.sweep is top-banded and claims
// both bare TINs. That is the cost of the arrangement, not a defect to fix -- it is invisible to
// every accuracy number because this is not a corpus layout.
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

func TestFixtures_ScannedHasNoTextLayer(t *testing.T) {
	raw := fxRead(t, fxScanned)

	// Positive control: without an image, "carries no text" is true of a blank page too.
	if !bytes.Contains(raw, []byte("/Subtype /Image")) {
		t.Fatalf("%s carries no image XObject; it is not the image-only document AC-6 needs, so the absence checks below prove nothing", fxScanned)
	}
	if bytes.Contains(raw, []byte("/Font")) {
		t.Errorf("%s declares a /Font resource; a scan carries no text layer at all", fxScanned)
	}

	objs := fxObjects(raw)
	pages := fxPages(t, objs)
	if len(pages) < 1 {
		t.Fatalf("found %d page object(s) in %s, want at least 1", len(pages), fxScanned)
	}
	for i, page := range pages {
		body := fxContent(t, objs, page)
		if !bytes.Contains(body, []byte("Do")) {
			t.Errorf("%s page %d draws no XObject; an empty page is not a scan", fxScanned, i+1)
		}
		for _, op := range []string{"BT", "Tj"} {
			if bytes.Contains(body, []byte(op)) {
				t.Errorf("%s page %d content stream carries the %s operator; pdfium would report a text layer and AC-6 would not fire", fxScanned, i+1, op)
			}
		}
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
// (1st/3rd group).
var fxRuleOpRe = regexp.MustCompile(`(\d+) (\d+) m\n(\d+) (\d+) l\nS\n`)

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
	if horiz < 4 {
		t.Errorf("%s page 1 carries %d horizontal rule(s), want at least 4", fxRich, horiz)
	}
	if vert < 5 {
		t.Errorf("%s page 1 carries %d vertical rule(s), want at least 5", fxRich, vert)
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
