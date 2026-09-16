// pdfium.go: PDFiumReader, the native-PDF PageReader.
package extraction

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

// Stamped into extraction_jobs.extractor / .extractor_version. Pinned by
// TestPDFiumReader_PinsNameAndVersion.
const (
	pdfiumReaderName    = "pdfium"
	pdfiumReaderVersion = "v1"
)

// pdfiumRenderDPI puts US-Letter on a 1275x1651 grid: legible at full review width, and about
// 113 KiB of grayscale PNG per invoice page. RGBA is 10x that and JPEG q85 8x, on line art.
const pdfiumRenderDPI = 150

// maxPages refuses a document rather than letting River dead-letter it as a timeout: 800 x the
// 300 ms per-page cost is 240 s, half the 480 s of render budget inside worker.go's 600 s.
const maxPages = 800

// Render-bitmap releases, read by TestPDFiumReader_HoldsOnePageAtATime.
var pdfiumCleanups atomic.Int64

// PDFiumReader holds no mutable state: a read borrows a pool instance for its own duration and
// returns it, so two reads share nothing.
type PDFiumReader struct {
	// dpi overrides pdfiumRenderDPI. Only AC-4's alignment sweep sets it, through
	// NewPDFiumReaderAtDPIForTest.
	dpi int
}

var _ PageReader = (*PDFiumReader)(nil)

func NewPDFiumReader() *PDFiumReader { return &PDFiumReader{} }

func (r *PDFiumReader) Name() string { return pdfiumReaderName }

func (r *PDFiumReader) Version() string { return pdfiumReaderVersion }

func (r *PDFiumReader) renderDPI() int {
	if r.dpi == 0 {
		return pdfiumRenderDPI
	}
	return r.dpi
}

// Read hands doc.Bytes to pdfium by reference and calls onPage once per page in ascending
// order. onPage must not be nil.
//
// ctx.Err() is tested before the pool is touched, so a cancelled call never pays the wasm
// compile (law E12). The totals are assigned only once the whole document is through, so any
// failure returns a zero PageResult.
func (r *PDFiumReader) Read(ctx context.Context, doc Document, onPage func(Page) error) (PageResult, error) {
	if err := ctx.Err(); err != nil {
		return PageResult{}, err
	}

	var result PageResult
	err := withPDFiumInstance(ctx, func(inst pdfium.Pdfium) error {
		opened, err := inst.OpenDocument(&requests.OpenDocument{File: &doc.Bytes})
		if err != nil {
			return fmt.Errorf("pdfium: open document: %w", err)
		}
		defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: opened.Document})

		count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: opened.Document})
		if err != nil {
			return fmt.Errorf("pdfium: page count: %w", err)
		}
		if count.PageCount > maxPages {
			return fmt.Errorf("pdfium: document has %d pages, over the %d page limit", count.PageCount, maxPages)
		}

		totals := PageResult{Pages: count.PageCount}
		for i := range count.PageCount {
			if err := ctx.Err(); err != nil {
				return err
			}

			size, err := inst.FPDF_GetPageSizeByIndex(&requests.FPDF_GetPageSizeByIndex{
				Document: opened.Document,
				Index:    i,
			})
			if err != nil {
				return fmt.Errorf("pdfium: page %d size: %w", i+1, err)
			}

			ref := requests.Page{ByIndex: &requests.PageByIndex{Document: opened.Document, Index: i}}
			text, err := inst.GetPageTextStructured(&requests.GetPageTextStructured{
				Page: ref,
				Mode: requests.GetPageTextStructuredModeBoth,
			})
			if err != nil {
				return fmt.Errorf("pdfium: page %d text: %w", i+1, err)
			}

			tokens, chars := pdfiumTokens(text.Rects, text.Chars, i+1, size.Width, size.Height)
			totals.TextChars += chars
			if chars > 0 {
				totals.PagesWithText++
			}

			if err := r.renderPage(inst, ref, Page{
				Number:   i + 1,
				WidthPt:  size.Width,
				HeightPt: size.Height,
				Tokens:   tokens,
			}, onPage); err != nil {
				return err
			}
		}

		result = totals
		return nil
	})
	if err != nil {
		return PageResult{}, err
	}
	return result, nil
}

// renderPage renders one page, hands it to onPage, then releases the render bitmap. Cleanup is
// mandatory under the wasm backend and runs on every path, including onPage's error.
func (r *PDFiumReader) renderPage(inst pdfium.Pdfium, ref requests.Page, page Page, onPage func(Page) error) error {
	rendered, err := inst.RenderPageInDPI(&requests.RenderPageInDPI{
		Page:        ref,
		DPI:         r.renderDPI(),
		ImageFormat: requests.RenderImageFormatGrayscale,
	})
	if err != nil {
		return fmt.Errorf("pdfium: page %d render: %w", page.Number, err)
	}
	defer func() {
		rendered.Cleanup()
		pdfiumCleanups.Add(1)
	}()

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, rendered.Result.RenderedImage); err != nil {
		return fmt.Errorf("pdfium: page %d encode: %w", page.Number, err)
	}

	// The render's own grid, not pageWidthPt * dpi / 72: go-pdfium ceils that product, so
	// US-Letter at DPI 150 is 1651 rows and not 1650.
	grid := rendered.Result.RenderedImage.Bounds()
	page.ImagePNG = encoded.Bytes()
	page.ImageWidth = grid.Dx()
	page.ImageHeight = grid.Dy()

	return onPage(page)
}

// pdfiumTokens converts one page's text rects into word-level tokens (lines -> fragments ->
// words, pdfiumWords) and counts the non-whitespace characters the raw rects carry -- unchanged
// by the merge, so TestPDFiumReader_TextCharsUnchanged stays green. An empty rect is dropped
// before either count or merge: law E08 and Field.Value both refuse an empty string.
func pdfiumTokens(rects []*responses.GetPageTextStructuredRect, chars []*responses.GetPageTextStructuredChar, page int, widthPt, heightPt float64) ([]Token, int) {
	charCount := 0
	for _, rect := range rects {
		if rect == nil || rect.Text == "" {
			continue
		}
		for _, r := range rect.Text {
			if !unicode.IsSpace(r) {
				charCount++
			}
		}
	}

	words := pdfiumWords(rects, chars, pdfiumSplitGap)
	tokens := make([]Token, 0, len(words))
	for _, w := range words {
		// pdfium measures from the page bottom and a Region from the top, hence heightPt - y.
		tokens = append(tokens, Token{
			Text: w.text,
			Region: Region{
				Page: page,
				X0:   w.box.Left / widthPt,
				Y0:   (heightPt - w.box.Top) / heightPt,
				X1:   w.box.Right / widthPt,
				Y1:   (heightPt - w.box.Bottom) / heightPt,
			},
		})
	}
	return tokens, charCount
}

// splitGap: adjacent fragments join below this many line heights and split at or above it. Its
// window is recomputed from the fixtures by TestPDFiumWords_SplitGapStaysInsideItsMeasuredWindow
// -- never move it by hand.
const pdfiumSplitGap = 0.60

// pdfiumWord is one group after Stage 3 (words), before conversion to a Token.
type pdfiumWord struct {
	box     responses.CharPosition
	members int
	first   int // lowest original rect index among its members (stream order)
	text    string
}

// pdfiumRectRef pairs a rect with its position in the page's own rect slice, so a group can
// report which original rect it started from once lines and fragments reorder by geometry.
type pdfiumRectRef struct {
	idx  int
	rect *responses.GetPageTextStructuredRect
}

// pdfiumFragment is Stage 2's output: a maximal run of pairwise box-overlapping rects on one
// line, walked in (Left, Right, stream-index) order.
type pdfiumFragment struct {
	box          responses.CharPosition
	first        int // lowest original rect index among members
	members      int
	leftmostText string // the fragment's own visually-leftmost rect text, for Stage 4's fallback
}

// pdfiumGap is one same-line, adjacent-fragment gap -- the value Stage 3 divides against
// splitGap. Exposed via PDFiumGapsForTest so the window specs never reimplement the grouping.
type pdfiumGap struct {
	line        int
	ratio       float64
	left, right responses.CharPosition
}

// pdfiumLineFragments is one line's Stage 2 output plus the height Stage 3 divides by.
type pdfiumLineFragments struct {
	frags   []pdfiumFragment
	tallest float64
}

// pdfiumWords runs Stages 1-4 over one page: rects group into lines, lines into fragments,
// fragments into words at splitGap, and each word's text resolves per Stage 4.
func pdfiumWords(rects []*responses.GetPageTextStructuredRect, chars []*responses.GetPageTextStructuredChar, splitGap float64) []pdfiumWord {
	var words []pdfiumWord
	for _, line := range pdfiumLinesWithFragments(rects) {
		frags, tallest := line.frags, line.tallest
		for i := 0; i < len(frags); {
			j := i
			for j+1 < len(frags) && tallest > 0 && (frags[j+1].box.Left-frags[j].box.Right)/tallest < splitGap {
				j++
			}
			words = append(words, pdfiumMergeRun(rects, chars, frags[i:j+1]))
			i = j + 1
		}
	}
	pdfiumSortWordsByFirst(words) // Section A: NOT line-then-X -- this is what keeps the 27 byte-identical
	return words
}

// pdfiumFragmentGaps exposes Stage 1+2's own consecutive-fragment gaps, ratio-scaled by each
// line's tallest rect -- the same values pdfiumWords compares against splitGap.
func pdfiumFragmentGaps(rects []*responses.GetPageTextStructuredRect) []pdfiumGap {
	var gaps []pdfiumGap
	for li, line := range pdfiumLinesWithFragments(rects) {
		if line.tallest <= 0 {
			continue
		}
		for k := 0; k+1 < len(line.frags); k++ {
			gaps = append(gaps, pdfiumGap{
				line:  li,
				ratio: (line.frags[k+1].box.Left - line.frags[k].box.Right) / line.tallest,
				left:  line.frags[k].box,
				right: line.frags[k+1].box,
			})
		}
	}
	return gaps
}

// pdfiumLinesWithFragments runs Stages 0-2: drop empty rects, group by line, then fragment each
// line. Shared by pdfiumWords and pdfiumFragmentGaps so both see identical gaps.
func pdfiumLinesWithFragments(rects []*responses.GetPageTextStructuredRect) []pdfiumLineFragments {
	valid := make([]pdfiumRectRef, 0, len(rects))
	for i, r := range rects {
		if r == nil || r.Text == "" {
			continue
		}
		valid = append(valid, pdfiumRectRef{idx: i, rect: r})
	}

	lines := pdfiumLineGroups(valid)
	out := make([]pdfiumLineFragments, len(lines))
	for i, line := range lines {
		out[i] = pdfiumLineFragments{frags: pdfiumFragmentsForLine(line), tallest: pdfiumLineTallest(line)}
	}
	return out
}

// pdfiumLineGroups partitions rects into lines by strict vertical overlap -- tolerance zero, the
// measured window (-0.6300, +0.1620] pt holds it with headroom on both sides.
// ceiling: O(n^2) union-find, index it above ~5k rects per page
func pdfiumLineGroups(valid []pdfiumRectRef) [][]pdfiumRectRef {
	n := len(valid)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := 0; i < n; i++ {
		pi := valid[i].rect.PointPosition
		for j := i + 1; j < n; j++ {
			pj := valid[j].rect.PointPosition
			if min(pi.Top, pj.Top)-max(pi.Bottom, pj.Bottom) > 0 {
				if ri, rj := find(i), find(j); ri != rj {
					parent[ri] = rj
				}
			}
		}
	}

	var order []int
	groups := make(map[int][]pdfiumRectRef, n)
	for i := 0; i < n; i++ {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], valid[i])
	}
	out := make([][]pdfiumRectRef, len(order))
	for k, r := range order {
		out[k] = groups[r]
	}
	return out
}

// pdfiumFragmentsForLine walks one line's rects sorted by (Left, Right, stream index) -- a total
// order, so the walk is deterministic -- joining while Left < the running fragment's Right
// (strict overlap, no tuned constant).
func pdfiumFragmentsForLine(line []pdfiumRectRef) []pdfiumFragment {
	sorted := append([]pdfiumRectRef(nil), line...)
	pdfiumSortRects(sorted)

	var frags []pdfiumFragment
	for _, rr := range sorted {
		box := rr.rect.PointPosition
		if n := len(frags); n > 0 && box.Left < frags[n-1].box.Right {
			cur := &frags[n-1]
			cur.box = pdfiumUnion(cur.box, box)
			cur.members++
			if rr.idx < cur.first {
				cur.first = rr.idx
			}
			continue
		}
		frags = append(frags, pdfiumFragment{box: box, first: rr.idx, members: 1, leftmostText: rr.rect.Text})
	}
	return frags
}

// pdfiumSortRects insertion-sorts by (Left, Right, stream index). pdfium.go may not import sort
// or slices (TestPDFiumSourceHasNoAmbientDependency); a line carries few rects, so this is plenty.
func pdfiumSortRects(g []pdfiumRectRef) {
	for i := 1; i < len(g); i++ {
		for j := i; j > 0 && pdfiumRectLess(g[j], g[j-1]); j-- {
			g[j], g[j-1] = g[j-1], g[j]
		}
	}
}

func pdfiumRectLess(a, b pdfiumRectRef) bool {
	pa, pb := a.rect.PointPosition, b.rect.PointPosition
	if pa.Left != pb.Left {
		return pa.Left < pb.Left
	}
	if pa.Right != pb.Right {
		return pa.Right < pb.Right
	}
	return a.idx < b.idx
}

// pdfiumSortWordsByFirst insertion-sorts words for emission -- see pdfiumSortRects on why not
// sort/slices.
func pdfiumSortWordsByFirst(words []pdfiumWord) {
	for i := 1; i < len(words); i++ {
		for j := i; j > 0 && words[j].first < words[j-1].first; j-- {
			words[j], words[j-1] = words[j-1], words[j]
		}
	}
}

// pdfiumLineTallest is the max rect height (Top-Bottom) over a line's own rects -- Stage 3's
// divisor, computed once per line rather than per gap.
func pdfiumLineTallest(line []pdfiumRectRef) float64 {
	tallest := 0.0
	for _, rr := range line {
		if h := rr.rect.PointPosition.Top - rr.rect.PointPosition.Bottom; h > tallest {
			tallest = h
		}
	}
	return tallest
}

func pdfiumUnion(a, b responses.CharPosition) responses.CharPosition {
	return responses.CharPosition{
		Left:   min(a.Left, b.Left),
		Right:  max(a.Right, b.Right),
		Top:    max(a.Top, b.Top),
		Bottom: min(a.Bottom, b.Bottom),
	}
}

// pdfiumMergeRun folds a run of Stage-3-joined fragments into one word (Stage 4). members==1
// keeps the single rect's own text byte-for-byte; an empty charsIn result falls back to the
// run's own leftmost rect text so a group can never vanish (Section D).
func pdfiumMergeRun(rects []*responses.GetPageTextStructuredRect, chars []*responses.GetPageTextStructuredChar, run []pdfiumFragment) pdfiumWord {
	w := pdfiumWord{box: run[0].box, first: run[0].first, members: run[0].members}
	leftmostText := run[0].leftmostText
	for _, f := range run[1:] {
		w.box = pdfiumUnion(w.box, f.box)
		w.members += f.members
		if f.first < w.first {
			w.first = f.first
		}
	}
	if w.members == 1 {
		w.text = rects[w.first].Text
		return w
	}
	w.text = charsIn(chars, w.box)
	if w.text == "" {
		w.text = leftmostText
	}
	return w
}

// charsIn returns the text of the chars whose boxes centre inside box, in char-stream order
// (rects and chars share one stream, so no sort is needed). \r and \n are skipped: pdfium's line
// breaks carry a degenerate box landing inside a neighbouring rect. Centre assigns a bled glyph to
// exactly one box (TestCharsIn_CentreAssignsABledGlyphToExactlyOneRect); no committed fixture
// separates it from full containment.
func charsIn(chars []*responses.GetPageTextStructuredChar, box responses.CharPosition) string {
	var b strings.Builder
	for _, c := range chars {
		if c == nil || c.Text == "\r" || c.Text == "\n" {
			continue
		}
		cx := (c.PointPosition.Left + c.PointPosition.Right) / 2
		cy := (c.PointPosition.Top + c.PointPosition.Bottom) / 2
		if cx >= box.Left && cx <= box.Right && cy >= box.Bottom && cy <= box.Top {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
