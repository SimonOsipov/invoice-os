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
	fxMinBytes = 200 // floor: every fixture carries a catalog, a page tree, a page and a stream
)

var fxCorpus = []struct {
	name  string
	build func() []byte
}{
	{fxNative, fxBuildNative},
	{fxNative3, fxBuildNative3Page},
	{fxScanned, fxBuildScanned},
	{fxHybrid, fxBuildHybrid},
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
