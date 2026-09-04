// pdfium_render_test.go: AC-3's image half and AC-4's coordinate identity. Every clause runs
// the real reader over the committed corpus, so a box is measured against rendered ink rather
// than described.
//
// Every ink clause carries a positive floor and a white-ground guard. Without
// FPDFBitmap_FillRect pdfium yields an all-black bitmap, on which every dark-pixel clause
// passes vacuously; the guard fires before any of them.
//
// Stdlib only. deps_test.go scan B walks test imports too, and any in-module import outside
// internal/platform/* fails it.
package extraction_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	// Grayscale ink threshold. The corpus draws pure black glyphs on a white ground, so any
	// mid value separates them and antialiased edges land above it.
	prDarkBelow = 128

	// Positive floor under every ink clause: a blank white render finds none.
	prMinInk = 20

	// Measured max inset is 2 px at each swept DPI, on a box floored and ceiled to cover the
	// ink rather than clip it.
	prMaxInset = 3

	// The band the no-ink clause sweeps above and below a token's box.
	prBandRows = 6

	// Mirrors pdfium.go's render DPI. TestPDFiumReader_ImageDimensionsAreTheRendersOwn pins
	// the pixel grid it produces, so the two cannot drift apart silently.
	prDefaultDPI = 150

	// US-Letter at prDefaultDPI. 792 * (150/72.0) is 1650.0000000000002 in float64 and
	// go-pdfium ceils it, so the page is 1651 rows tall and not 1650.
	prLetterWidthPx  = 1275
	prLetterHeightPx = 1651

	// Floor under a digest: a body this small is not a rendered page. Measured 11 KiB.
	prMinPNGBytes = 1 << 10

	// The page cap's two boundary documents.
	prOverCap = 801
	prAtCap   = 800
)

// prDPIs is AC-4's sweep: the mapping is the render's own grid, not the DPI.
var prDPIs = []int{100, prDefaultDPI, 200}

// --- harness ----------------------------------------------------------------

// prRead runs the reader at one DPI and collects every page. ImagePNG is borrowed for the
// duration of its onPage call, so the copy is the contract and not caution.
func prRead(t *testing.T, name string, dpi int) []extraction.Page {
	t.Helper()

	var pages []extraction.Page
	res, err := extraction.NewPDFiumReaderAtDPIForTest(dpi).Read(t.Context(), ptDoc(t, name), func(p extraction.Page) error {
		p.ImagePNG = bytes.Clone(p.ImagePNG)
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		t.Fatalf("Read(%s) at DPI %d: %v", name, dpi, err)
	}
	if res.Pages == 0 || len(pages) != res.Pages {
		t.Fatalf("Read(%s) at DPI %d called onPage %d time(s) and reported %d page(s); the clauses below would examine nothing", name, dpi, len(pages), res.Pages)
	}
	return pages
}

// prGray decodes a page's render. Decoding is itself an assertion: ImagePNG is a PNG, it is
// grayscale, and it is the grid the page reports.
func prGray(t *testing.T, name string, p extraction.Page) *image.Gray {
	t.Helper()

	if len(p.ImagePNG) == 0 {
		t.Fatalf("%s page %d carries no ImagePNG; every ink clause below would examine nothing", name, p.Number)
	}
	img, err := png.Decode(bytes.NewReader(p.ImagePNG))
	if err != nil {
		t.Fatalf("%s page %d: decode ImagePNG (%d byte(s)): %v", name, p.Number, len(p.ImagePNG), err)
	}
	g, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("%s page %d decoded to %T, want *image.Gray: the render asks for RenderImageFormatGrayscale, and RGBA is 10x the bytes", name, p.Number, img)
	}
	if b := g.Bounds(); b.Dx() != p.ImageWidth || b.Dy() != p.ImageHeight {
		t.Fatalf("%s page %d reports %dx%d but its PNG decodes to %dx%d", name, p.Number, p.ImageWidth, p.ImageHeight, b.Dx(), b.Dy())
	}
	return g
}

// prAssertWhiteGround is the vacuity guard: an all-black bitmap satisfies every dark-pixel
// clause in this file, so the corners are checked before any of them run.
func prAssertWhiteGround(t *testing.T, name string, p extraction.Page, g *image.Gray) {
	t.Helper()

	b := g.Bounds()
	for _, c := range []image.Point{{X: 0, Y: 0}, {X: b.Dx() - 1, Y: 0}, {X: 0, Y: b.Dy() - 1}, {X: b.Dx() - 1, Y: b.Dy() - 1}} {
		if v := g.GrayAt(c.X, c.Y).Y; v < prDarkBelow {
			t.Fatalf("%s page %d: corner pixel (%d,%d) reads %d, want a light ground -- an unfilled bitmap is all black, and every dark-pixel clause below passes on one", name, p.Number, c.X, c.Y, v)
		}
	}
}

// prRect is a pixel box, inclusive on every edge.
type prRect struct{ x0, y0, x1, y1 int }

func (r prRect) String() string { return fmt.Sprintf("(%d,%d)-(%d,%d)", r.x0, r.y0, r.x1, r.y1) }

// prBox is the box a canvas draws for a normalised region: the near edge floored and the far
// edge ceiled, so the box covers the ink rather than clipping it. AC-4 is exactly the claim
// that this arithmetic needs nothing but the page's own ImageWidth and ImageHeight.
func prBox(r extraction.Region, w, h int) prRect {
	return prRect{
		x0: int(math.Floor(r.X0 * float64(w))),
		y0: int(math.Floor(r.Y0 * float64(h))),
		x1: int(math.Ceil(r.X1 * float64(w))),
		y1: int(math.Ceil(r.Y1 * float64(h))),
	}
}

// prInk counts the dark pixels inside a box and returns their own bounding box.
func prInk(g *image.Gray, box prRect) (int, prRect) {
	b := g.Bounds()
	ink := prRect{x0: b.Dx(), y0: b.Dy(), x1: -1, y1: -1}
	count := 0

	for y := max(box.y0, 0); y <= min(box.y1, b.Dy()-1); y++ {
		for x := max(box.x0, 0); x <= min(box.x1, b.Dx()-1); x++ {
			if g.GrayAt(x, y).Y >= prDarkBelow {
				continue
			}
			count++
			ink.x0, ink.y0 = min(ink.x0, x), min(ink.y0, y)
			ink.x1, ink.y1 = max(ink.x1, x), max(ink.y1, y)
		}
	}
	return count, ink
}

// prRowInk counts the dark pixels in a row range, split by whether they fall inside the box's
// columns.
func prRowInk(g *image.Gray, box prRect, fromRow, toRow int) (inside, outside int) {
	b := g.Bounds()

	for y := max(fromRow, 0); y <= min(toRow, b.Dy()-1); y++ {
		for x := range b.Dx() {
			if g.GrayAt(x, y).Y >= prDarkBelow {
				continue
			}
			if x < box.x0 || x > box.x1 {
				outside++
			} else {
				inside++
			}
		}
	}
	return inside, outside
}

// prToken finds one token by its exact text on one page.
func prToken(t *testing.T, name string, p extraction.Page, text string) extraction.Token {
	t.Helper()

	for _, tok := range p.Tokens {
		if tok.Text == text {
			return tok
		}
	}
	t.Fatalf("%s page %d carries no %q token among %d token(s); the box below would be the zero region, which covers the whole page", name, p.Number, text, len(p.Tokens))
	return extraction.Token{}
}

// prDigests reads one fixture and returns a SHA-256 per page, taken inside onPage so the
// borrowed buffer is never retained.
func prDigests(t *testing.T, name string) ([][32]byte, []int) {
	t.Helper()

	var digests [][32]byte
	var sizes []int
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), ptDoc(t, name), func(p extraction.Page) error {
		digests = append(digests, sha256.Sum256(p.ImagePNG))
		sizes = append(sizes, len(p.ImagePNG))
		return nil
	}); err != nil {
		t.Fatalf("Read(%s): %v", name, err)
	}
	return digests, sizes
}

// --- AC-4: the box space and the pixel space are one space -------------------

// TestPDFiumReader_KnownTokenLandsOnTheRenderedInk: AC-4. A known token's normalised region,
// scaled by the page's own ImageWidth and ImageHeight, lands on that token's ink at every
// swept DPI. The floor is what stops a blank render from satisfying it.
func TestPDFiumReader_KnownTokenLandsOnTheRenderedInk(t *testing.T) {
	for _, dpi := range prDPIs {
		t.Run(fmt.Sprintf("dpi%d", dpi), func(t *testing.T) {
			pages := prRead(t, fxNative, dpi)
			p := pages[0]
			g := prGray(t, fxNative, p)
			prAssertWhiteGround(t, fxNative, p, g)

			tok := prToken(t, fxNative, p, ptTopLine)
			box := prBox(tok.Region, p.ImageWidth, p.ImageHeight)

			inside, ink := prInk(g, box)
			if inside < prMinInk {
				t.Fatalf("%q at DPI %d covers %d dark pixel(s) in %v of a %dx%d render, want at least %d -- an empty box satisfies every clause below", ptTopLine, dpi, inside, box, p.ImageWidth, p.ImageHeight, prMinInk)
			}

			// Every dark pixel on the token's rows must fall inside its columns: those rows
			// carry this token alone in the fixture.
			if _, outside := prRowInk(g, box, box.y0, box.y1); outside != 0 {
				t.Errorf("%q at DPI %d: %d dark pixel(s) on rows %d..%d fall outside the box columns %d..%d", ptTopLine, dpi, outside, box.y0, box.y1, box.x0, box.x1)
			}

			t.Logf("DPI %d: %q box %v, ink %v, %d dark pixel(s)", dpi, ptTopLine, box, ink, inside)
			for _, e := range []struct {
				edge  string
				inset int
			}{
				{"left", ink.x0 - box.x0},
				{"top", ink.y0 - box.y0},
				{"right", box.x1 - ink.x1},
				{"bottom", box.y1 - ink.y1},
			} {
				if e.inset > prMaxInset {
					t.Errorf("%q at DPI %d: the box %v sits %d px %s of the ink %v, want at most %d -- the box no longer tracks the glyph run", ptTopLine, dpi, box, e.inset, e.edge, ink, prMaxInset)
				}
			}
		})
	}
}

// TestPDFiumReader_NoInkOutsideTheTokenBox: AC-4's other half. A band above and below the
// token's box carries ink, and none of it is outside the box columns.
func TestPDFiumReader_NoInkOutsideTheTokenBox(t *testing.T) {
	for _, dpi := range prDPIs {
		t.Run(fmt.Sprintf("dpi%d", dpi), func(t *testing.T) {
			pages := prRead(t, fxNative, dpi)
			p := pages[0]
			g := prGray(t, fxNative, p)
			prAssertWhiteGround(t, fxNative, p, g)

			box := prBox(prToken(t, fxNative, p, ptTopLine).Region, p.ImageWidth, p.ImageHeight)
			from, to := box.y0-prBandRows, box.y1+prBandRows

			inside, outside := prRowInk(g, box, from, to)
			if inside+outside < prMinInk {
				t.Fatalf("rows %d..%d around %q at DPI %d hold %d dark pixel(s), want at least %d -- a blank band has nothing outside the box either", from, to, ptTopLine, dpi, inside+outside, prMinInk)
			}
			if outside != 0 {
				t.Errorf("rows %d..%d around %q at DPI %d hold %d dark pixel(s) outside the box columns %d..%d, want 0", from, to, ptTopLine, dpi, outside, box.x0, box.x1)
			}
		})
	}
}

// TestPDFiumReader_ImageDimensionsAreTheRendersOwn: AC-4. The dimensions are the render's own
// grid. go-pdfium ceils pageWidthPt * dpi / 72, and 792 * (150/72.0) is 1650.0000000000002 in
// float64, so US-Letter is 1651 rows tall -- a canvas scaling by the naive product is a row
// short and drifts down the page.
func TestPDFiumReader_ImageDimensionsAreTheRendersOwn(t *testing.T) {
	pages, _ := ptRead(t, fxNative)
	p := pages[0]

	if p.WidthPt != fxPageWidthPt || p.HeightPt != fxPageHeightPt {
		t.Fatalf("%s page 1 is %vx%v pt, want %dx%d -- the pixel expectations below are US-Letter's", fxNative, p.WidthPt, p.HeightPt, fxPageWidthPt, fxPageHeightPt)
	}
	if p.ImageWidth != prLetterWidthPx || p.ImageHeight != prLetterHeightPx {
		t.Errorf("%s page 1 renders %dx%d px, want %dx%d at DPI %d", fxNative, p.ImageWidth, p.ImageHeight, prLetterWidthPx, prLetterHeightPx, prDefaultDPI)
	}

	naive := int(p.HeightPt * prDefaultDPI / 72.0)
	if naive != prLetterHeightPx-1 {
		t.Fatalf("the naive height is %d and the render is %d; they no longer differ, so this needle proves nothing", naive, prLetterHeightPx)
	}
	if p.ImageHeight == naive {
		t.Errorf("%s page 1 reports ImageHeight %d, which is pageHeightPt * dpi / 72 truncated and not the render's own row count", fxNative, naive)
	}
}

// --- AC-3: the image half of byte-identical output ---------------------------

// TestPDFiumReader_RendersByteIdenticalPNGsAcrossTwoReads: AC-3. Two reads of one document in
// one process render the same bytes. No golden PNG is committed -- compress/flate output has
// changed between Go releases and the toolchain is pinned only to a minor version, so a
// committed golden would turn a patch bump into a red suite for no defect.
func TestPDFiumReader_RendersByteIdenticalPNGsAcrossTwoReads(t *testing.T) {
	first, sizes := prDigests(t, fxNative3)

	if len(first) != 3 {
		t.Fatalf("the first read of %s produced %d digest(s), want 3 -- two empty lists are equal", fxNative3, len(first))
	}
	for i, n := range sizes {
		if n < prMinPNGBytes {
			t.Fatalf("%s page %d encoded to %d byte(s), want more than %d -- that is not a rendered page", fxNative3, i+1, n, prMinPNGBytes)
		}
	}
	for i := range first {
		for j := i + 1; j < len(first); j++ {
			if first[i] == first[j] {
				t.Fatalf("%s pages %d and %d rendered to the same bytes; the three pages differ, so one image is being handed out for all of them and the comparison below is vacuous", fxNative3, i+1, j+1)
			}
		}
	}

	second, _ := prDigests(t, fxNative3)
	if len(second) != len(first) {
		t.Fatalf("two reads of %s produced %d then %d page image(s)", fxNative3, len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("two reads of %s in one process rendered page %d differently: %x then %x", fxNative3, i+1, first[i], second[i])
		}
	}
}

// --- the memory posture ------------------------------------------------------

// TestPDFiumReader_HoldsOnePageAtATime: AC-3's memory posture. Each page carries its own
// buffer, and the wasm bitmap behind it is released after its onPage call and not before.
// An interior pointer keeps its whole allocation alive, so two pages cannot alias by the
// allocator reusing a freed address.
func TestPDFiumReader_HoldsOnePageAtATime(t *testing.T) {
	base := extraction.PDFiumCleanupsForTest()

	var firsts []*byte
	var seenCleanups []int64
	res, err := extraction.NewPDFiumReader().Read(t.Context(), ptDoc(t, fxNative3), func(p extraction.Page) error {
		if len(p.ImagePNG) < prMinPNGBytes {
			t.Fatalf("%s page %d carries %d byte(s) of ImagePNG, want more than %d", fxNative3, p.Number, len(p.ImagePNG), prMinPNGBytes)
		}
		firsts = append(firsts, &p.ImagePNG[0])
		seenCleanups = append(seenCleanups, extraction.PDFiumCleanupsForTest()-base)
		return nil
	})
	if err != nil {
		t.Fatalf("Read(%s): %v", fxNative3, err)
	}
	if res.Pages != 3 || len(firsts) != 3 {
		t.Fatalf("Read(%s) reported %d page(s) over %d onPage call(s), want 3 -- the clauses below need more than one page to compare", fxNative3, res.Pages, len(firsts))
	}

	for i := range firsts {
		for j := i + 1; j < len(firsts); j++ {
			if firsts[i] == firsts[j] {
				t.Errorf("%s pages %d and %d share one ImagePNG backing array; a reused buffer means the caller cannot hold a page while the next one renders", fxNative3, i+1, j+1)
			}
		}
	}

	for i, got := range seenCleanups {
		if want := int64(i); got != want {
			t.Errorf("%s page %d entered onPage after %d render cleanup(s), want %d -- the bitmap is released after its onPage call, not before it", fxNative3, i+1, got, want)
		}
	}
	if got := extraction.PDFiumCleanupsForTest() - base; got != 3 {
		t.Errorf("Read(%s) ran %d render cleanup(s) over 3 page(s); Cleanup is mandatory in wasm mode and one skipped call leaks a whole page bitmap", fxNative3, got)
	}
}

// TestPDFiumReader_CleansUpOnAnOnPageError: an onPage error aborts the read, comes back
// unwrapped, and every page that rendered is still released.
func TestPDFiumReader_CleansUpOnAnOnPageError(t *testing.T) {
	base := extraction.PDFiumCleanupsForTest()
	refused := errors.New("onPage refused page 2")

	calls := 0
	res, err := extraction.NewPDFiumReader().Read(t.Context(), ptDoc(t, fxNative3), func(p extraction.Page) error {
		calls++
		if p.Number == 2 {
			return refused
		}
		return nil
	})

	// Identity, not errors.Is: the contract is the onPage error itself, unwrapped.
	if err != refused {
		t.Errorf("Read(%s) returned %v, want the onPage error itself: a caller distinguishes its own refusal from a pdfium failure by identity", fxNative3, err)
	}
	if calls != 2 {
		t.Errorf("Read(%s) called onPage %d time(s), want 2 -- the read stops at the refusing page", fxNative3, calls)
	}
	if res != (extraction.PageResult{}) {
		t.Errorf("Read(%s) failed but returned %+v, want a zero PageResult", fxNative3, res)
	}
	if got := extraction.PDFiumCleanupsForTest() - base; got != 2 {
		t.Errorf("Read(%s) ran %d render cleanup(s) over %d page(s) entered; the refusing page's bitmap is released too", fxNative3, got, calls)
	}
}

// --- the page cap ------------------------------------------------------------

// TestPDFiumReader_RefusesOverThePageCap: the cap is stated, not a timeout. River retries the
// job three times and each attempt re-renders from scratch, so a document that cannot finish
// dead-letters after ~24 minutes with "context deadline exceeded" unless it is refused here.
func TestPDFiumReader_RefusesOverThePageCap(t *testing.T) {
	raw := fxBuildNPage(prOverCap)
	fxAssertWellFormed(t, fmt.Sprintf("%d-page document", prOverCap), raw)

	calls := 0
	res, err := extraction.NewPDFiumReader().Read(t.Context(), extraction.Document{Bytes: raw, ContentType: "application/pdf"}, func(extraction.Page) error {
		calls++
		return nil
	})

	if err == nil {
		t.Fatalf("Read on a %d-page document returned no error", prOverCap)
	}
	for _, want := range []string{fmt.Sprint(prOverCap), fmt.Sprint(prAtCap)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %s; an operator needs both the count and the cap to know what to do about it", err.Error(), want)
		}
	}
	if calls != 0 {
		t.Errorf("Read on a %d-page document called onPage %d time(s); a refused document is never partly rendered", prOverCap, calls)
	}
	if res != (extraction.PageResult{}) {
		t.Errorf("Read on a %d-page document returned %+v, want a zero PageResult", prOverCap, res)
	}
}

// TestPDFiumReader_AcceptsAtThePageCap: the boundary, proved without rendering 800 pages --
// onPage refuses page 1 and the read stops there. This is also the control needle under
// TestPDFiumReader_RefusesOverThePageCap: it proves pdfium opens a generated document of this
// shape at all, so the refusal above is the cap firing and not a broken fixture.
func TestPDFiumReader_AcceptsAtThePageCap(t *testing.T) {
	raw := fxBuildNPage(prAtCap)
	fxAssertWellFormed(t, fmt.Sprintf("%d-page document", prAtCap), raw)

	enough := errors.New("one page is enough")
	calls := 0
	_, err := extraction.NewPDFiumReader().Read(t.Context(), extraction.Document{Bytes: raw, ContentType: "application/pdf"}, func(p extraction.Page) error {
		calls++
		if p.Number != 1 {
			t.Errorf("onPage was reached with page %d; the abort below stops the read at page 1", p.Number)
		}
		return enough
	})

	// Identity: the cap must let a document at exactly maxPages through to onPage at all.
	if err != enough {
		t.Fatalf("Read on a %d-page document returned %v, want the onPage refusal: a document at exactly the cap is accepted, and an off-by-one cap refuses it here", prAtCap, err)
	}
	if calls != 1 {
		t.Errorf("Read on a %d-page document called onPage %d time(s), want 1", prAtCap, calls)
	}
}

// --- EXTR-18-03: the no-recoverable-text fixture's ink contrast --------------

// TestPDFiumReader_ScannedAndDenseDifferInInk: scanned_invoice.pdf and dense_invoice.pdf are
// both image-only (fxBuildScanned, fxBuildDense) -- no text layer for either to carry -- so ink
// is what separates the checkerboard "no recoverable text" scan from a document with real
// content. Uses prRead, never ptRead: pdfium reuses ImagePNG's buffer across renders, and only
// prRead clones it (line 71 above), so comparing two ptRead reads would compare a buffer to
// itself.
func TestPDFiumReader_ScannedAndDenseDifferInInk(t *testing.T) {
	scanned := prRead(t, fxScanned, prDefaultDPI)
	dense := prRead(t, fxDense, prDefaultDPI)

	if len(scanned) != 1 || len(dense) != 1 {
		t.Fatalf("%s renders %d page(s), %s renders %d; want exactly 1 each", fxScanned, len(scanned), fxDense, len(dense))
	}
	sBytes, dBytes := len(scanned[0].ImagePNG), len(dense[0].ImagePNG)
	if sBytes < prMinPNGBytes || dBytes < prMinPNGBytes {
		t.Fatalf("%s ImagePNG is %d byte(s), %s is %d; want more than %d each -- one of these is not a rendered page", fxScanned, sBytes, fxDense, dBytes, prMinPNGBytes)
	}
	if dBytes < 2*sBytes {
		t.Errorf("%s ImagePNG is %d byte(s), %s is %d (ratio %.2fx); want dense at least 2x scanned's size -- ink is what tells a real document from a blank scan", fxDense, dBytes, fxScanned, sBytes, float64(dBytes)/float64(sBytes))
	}
}
