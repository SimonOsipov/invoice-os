// pdfium_chars_adversarial_test.go: EXTR-36-02 QA. Specs the AC set leaves unpinned -- the
// predicate's own choice, the stream-contiguity property 03 inherits, and charsIn's answers on
// degenerate input. External package, for the reason pdfium_chars_test.go's header records.
package extraction_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/klippa-app/go-pdfium/responses"
)

// pcaChar builds one synthetic char. charsIn is a pure function of chars and box, so a hand-built
// slice is as valid an oracle as a fixture -- and it is the only one for a rotated glyph.
func pcaChar(text string, left, bottom, right, top, angle float64) *responses.GetPageTextStructuredChar {
	return &responses.GetPageTextStructuredChar{
		Text:          text,
		Angle:         angle,
		PointPosition: responses.CharPosition{Left: left, Bottom: bottom, Right: right, Top: top},
	}
}

// --- the predicate's own choice -------------------------------------------------------------

// TestCharsIn_CentreAssignsABledGlyphToExactlyOneRect kills the any-overlap alternative, which
// every AC-3/AC-4 spec passes unchanged: the bled pair's two rects overlap by 0.13 pt, so overlap
// reads "KW" from each and double-counts, while centre partitions them into "K" and "W".
func TestCharsIn_CentreAssignsABledGlyphToExactlyOneRect(t *testing.T) {
	page1 := pdcPage1(t, pdcStructured(t, chrRegister))
	runs := pdcOverlapRuns(page1.Rects)

	found := false
	for _, run := range runs {
		if len(run) != 2 || pdcNaiveText(page1.Rects, run) != "KWKW" {
			continue
		}
		found = true
		a := extraction.CharsInForTest(page1.Chars, page1.Rects[run[0]].PointPosition)
		b := extraction.CharsInForTest(page1.Chars, page1.Rects[run[1]].PointPosition)
		union := extraction.CharsInForTest(page1.Chars, pdcUnionBox(page1.Rects, run))

		if a+b != union {
			t.Errorf("rects %v read %q + %q = %q, want the union's %q -- the two reads must partition the union", run, a, b, a+b, union)
		}
		if a == union || b == union {
			t.Errorf("rects %v read %q and %q; neither may equal the union %q, or the glyphs are being double-counted (the any-overlap predicate)", run, a, b, union)
		}
		if len(a) == 0 || len(b) == 0 {
			t.Errorf("rects %v read %q and %q; each overlapping rect must claim its own glyph", run, a, b)
		}
	}
	if !found {
		t.Fatalf("no 2-rect overlap run on %s page 1 naively reads %q", chrRegister, "KWKW")
	}
}

// TestCharsIn_TakesAGlyphWhoseBoxOverrunsTheBox kills the full-containment alternative, which no
// committed fixture discriminates (measured: identical on all 2144 rects and on both union boxes).
// Synthetic, because the corpus carries no glyph whose ink box overruns its rect.
func TestCharsIn_TakesAGlyphWhoseBoxOverrunsTheBox(t *testing.T) {
	box := responses.CharPosition{Left: 0, Bottom: 0, Right: 10, Top: 10}
	chars := []*responses.GetPageTextStructuredChar{
		pcaChar("A", -1, -1, 5, 11, 0), // centre (2,5) inside; box overruns on three sides
		pcaChar("B", 5, 0, 9, 10, 0),
	}
	if got := extraction.CharsInForTest(chars, box); got != "AB" {
		t.Errorf("charsIn = %q, want %q -- a glyph whose centre is inside is taken however far its own box overruns", got, "AB")
	}
	// The mirror: a glyph whose box overlaps but whose centre is outside is refused.
	out := []*responses.GetPageTextStructuredChar{pcaChar("C", 9, 0, 19, 10, 0)} // centre 14, outside
	if got := extraction.CharsInForTest(out, box); got != "" {
		t.Errorf("charsIn = %q, want empty -- an overlapping glyph whose centre is outside must be refused", got)
	}
}

// TestCharsIn_BoundsAreClosed pins the >=/<= clause. Undiscriminated by AC-3 once both sides are
// trimmed (measured: 3 rects differ under open bounds, all of them a trailing-space glyph), and
// load-bearing at 03 for an interior space sitting on a union box's bottom edge.
func TestCharsIn_BoundsAreClosed(t *testing.T) {
	box := responses.CharPosition{Left: 0, Bottom: 0, Right: 10, Top: 10}
	onEdge := []*responses.GetPageTextStructuredChar{
		pcaChar("L", -2, 4, 2, 6, 0), // centre x == box.Left
		pcaChar("R", 8, 4, 12, 6, 0), // centre x == box.Right
		pcaChar("B", 4, -1, 6, 1, 0), // centre y == box.Bottom
		pcaChar("T", 4, 9, 6, 11, 0), // centre y == box.Top
	}
	if got := extraction.CharsInForTest(onEdge, box); got != "LRBT" {
		t.Errorf("charsIn = %q, want %q -- a centre exactly on an edge is inside", got, "LRBT")
	}
}

// TestCharsIn_IgnoresCharAngle pins [charsin-rotated-pages] as it behaves today rather than
// asserting it is right: the predicate reads PointPosition alone, so a rotated glyph is taken on
// its axis-aligned box. No committed fixture carries Angle != 0, asserted below so the residual
// closes the day one does.
func TestCharsIn_IgnoresCharAngle(t *testing.T) {
	box := responses.CharPosition{Left: 0, Bottom: 0, Right: 10, Top: 10}
	chars := []*responses.GetPageTextStructuredChar{
		pcaChar("U", 0, 0, 4, 4, 0),
		pcaChar("R", 5, 5, 9, 9, 1.5708), // a quarter turn: taken all the same
	}
	if got := extraction.CharsInForTest(chars, box); got != "UR" {
		t.Errorf("charsIn = %q, want %q -- Angle is not read, so a rotated glyph is taken on its box", got, "UR")
	}

	rotated, scanned := 0, 0
	for _, name := range slices.Sorted(maps.Keys(fingerprintGoldens)) {
		pages, err := extraction.PDFiumStructuredTextForTest(t.Context(), ptDoc(t, name), "both")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, p := range pages {
			for _, c := range p.Chars {
				scanned++
				if c != nil && c.Angle != 0 {
					rotated++
				}
			}
		}
	}
	if scanned < pdcMinTotalChars {
		t.Fatalf("scanned %d char(s), want at least %d -- the count below would mean nothing", scanned, pdcMinTotalChars)
	}
	if rotated != 0 {
		t.Errorf("%d of %d committed char(s) carry Angle != 0; the corpus now has a rotated-page oracle and [charsin-rotated-pages] must be decided, not pinned", rotated, scanned)
	}
	t.Logf("[charsin-rotated-pages] still undecided: 0 of %d committed chars are rotated", scanned)
}

// pcaMinTotalChars floors the rotation sweep: 10195 chars measured across the 29 fixtures.
const pdcMinTotalChars = 9000

// --- the property 03 inherits ----------------------------------------------------------------

// TestCharsIn_CollectsAContiguousStreamRun pins [charsin-stream-contiguity]: on every committed
// rect the collected chars are one strictly unbroken run of the char stream -- not one \r\n
// interloper, measured. 03 merges rects geometrically, so this is the guard that reds if a merged
// box ever spans stream-distant material. The gap detector is itself pinned below.
func TestCharsIn_CollectsAContiguousStreamRun(t *testing.T) {
	names := slices.Sorted(maps.Keys(fingerprintGoldens))
	if len(names) < fgGoldenFloor {
		t.Fatalf("fingerprintGoldens holds %d row(s), want at least %d", len(names), fgGoldenFloor)
	}

	checked := 0
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			pages, err := extraction.PDFiumStructuredTextForTest(t.Context(), ptDoc(t, name), "both")
			if err != nil {
				t.Fatalf("PDFiumStructuredTextForTest(both): %v", err)
			}
			for _, p := range pages {
				for i, r := range p.Rects {
					idx := pcaCollectedIndices(p.Chars, r.PointPosition)
					if len(idx) < 2 {
						continue
					}
					checked++
					if gap := pcaFirstGap(idx); gap >= 0 {
						t.Errorf("page %d rect %d (%q): collected char indices %v break at %d -- the run is not contiguous in the char stream",
							p.Number, i, r.Text, idx, gap)
					}
				}
			}
		})
	}
	if checked < pdcMinNonChromeRects {
		t.Fatalf("checked %d multi-char rect(s), want at least %d", checked, pdcMinNonChromeRects)
	}
}

// pcaCollectedIndices mirrors charsIn's predicate and returns stream indices rather than text.
func pcaCollectedIndices(chars []*responses.GetPageTextStructuredChar, box responses.CharPosition) []int {
	var idx []int
	for i, c := range chars {
		if c == nil || c.Text == "\r" || c.Text == "\n" {
			continue
		}
		cx := (c.PointPosition.Left + c.PointPosition.Right) / 2
		cy := (c.PointPosition.Top + c.PointPosition.Bottom) / 2
		if cx >= box.Left && cx <= box.Right && cy >= box.Bottom && cy <= box.Top {
			idx = append(idx, i)
		}
	}
	return idx
}

// pcaFirstGap returns the stream index of the first char sitting between two collected chars, or
// -1. Strict: a skipped \r or \n counts, because none occurs mid-run on any committed rect.
func pcaFirstGap(idx []int) int {
	for k := 1; k < len(idx); k++ {
		if idx[k] != idx[k-1]+1 {
			return idx[k-1] + 1
		}
	}
	return -1
}

// TestCharsIn_TheStreamGapDetectorFires pins pcaFirstGap itself: without this the sweep above
// reports a clean corpus whether or not it can see a break.
func TestCharsIn_TheStreamGapDetectorFires(t *testing.T) {
	if got := pcaFirstGap([]int{3, 4, 5}); got != -1 {
		t.Errorf("pcaFirstGap(contiguous) = %d, want -1", got)
	}
	if got := pcaFirstGap([]int{3, 4, 7}); got != 5 {
		t.Errorf("pcaFirstGap(broken at 5) = %d, want 5", got)
	}

	// End to end: a box that skips a glyph in the middle of the stream must be caught.
	chars := []*responses.GetPageTextStructuredChar{
		pcaChar("A", 0, 0, 2, 2, 0),
		pcaChar("B", 50, 50, 52, 52, 0), // far outside the box below
		pcaChar("C", 4, 0, 6, 2, 0),
	}
	box := responses.CharPosition{Left: 0, Bottom: 0, Right: 10, Top: 10}
	idx := pcaCollectedIndices(chars, box)
	if extraction.CharsInForTest(chars, box) != "AC" {
		t.Fatalf("the synthetic case must collect A and C only")
	}
	if got := pcaFirstGap(idx); got != 1 {
		t.Errorf("pcaFirstGap(%v) = %d, want 1 -- a stream-distant box must be reported", idx, got)
	}
}

// TestCharsIn_IdenticalBoxesReadTheSameText: two rects sharing one box must read one text each,
// not one between them. Synthetic -- asserted below, no committed page carries a duplicate box.
func TestCharsIn_IdenticalBoxesReadTheSameText(t *testing.T) {
	box := responses.CharPosition{Left: 0, Bottom: 0, Right: 10, Top: 10}
	chars := []*responses.GetPageTextStructuredChar{pcaChar("X", 1, 1, 3, 3, 0), pcaChar("Y", 5, 5, 7, 7, 0)}
	first, second := extraction.CharsInForTest(chars, box), extraction.CharsInForTest(chars, box)
	if first != "XY" || second != first {
		t.Errorf("two reads of one box gave %q and %q, want %q twice -- charsIn holds no state", first, second, "XY")
	}

	dupes := 0
	for _, name := range slices.Sorted(maps.Keys(fingerprintGoldens)) {
		pages, err := extraction.PDFiumStructuredTextForTest(t.Context(), ptDoc(t, name), "both")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, p := range pages {
			seen := map[responses.CharPosition]bool{}
			for _, r := range p.Rects {
				if seen[r.PointPosition] {
					dupes++
					t.Logf("%s page %d carries a duplicate rect box %+v (%q)", name, p.Number, r.PointPosition, r.Text)
				}
				seen[r.PointPosition] = true
			}
		}
	}
	if dupes != 0 {
		t.Errorf("%d duplicate rect box(es) across the corpus; the synthetic case above is no longer the only oracle", dupes)
	}
}

// --- degenerate input ------------------------------------------------------------------------

// TestCharsIn_DegenerateInputsAllReadEmpty records the finding, not a wish: five distinct
// conditions collapse to "". A caller cannot tell "the box holds no glyph" from "every glyph in it
// was a separator" from "the box is inverted". 03 must not read "" as "this token has no text" --
// pdfiumTokens drops an empty-text rect (pdfium.go), so a char-sourced token that reads "" would
// vanish rather than fail.
func TestCharsIn_DegenerateInputsAllReadEmpty(t *testing.T) {
	box := responses.CharPosition{Left: 0, Bottom: 0, Right: 10, Top: 10}
	real := []*responses.GetPageTextStructuredChar{pcaChar("A", 1, 1, 3, 3, 0)}

	cases := []struct {
		name  string
		chars []*responses.GetPageTextStructuredChar
		box   responses.CharPosition
	}{
		{"nil char slice", nil, box},
		{"empty char slice", []*responses.GetPageTextStructuredChar{}, box},
		{"box holds no centre", real, responses.CharPosition{Left: 900, Bottom: 900, Right: 910, Top: 910}},
		{"zero-area box away from any centre", real, responses.CharPosition{}},
		{"inverted box (Top < Bottom)", real, responses.CharPosition{Left: 0, Bottom: 10, Right: 10, Top: 0}},
		{"only separators inside", []*responses.GetPageTextStructuredChar{pcaChar("\r", 1, 1, 2, 2, 0), pcaChar("\n", 1, 1, 2, 2, 0)}, box},
		{"only an empty-text glyph inside", []*responses.GetPageTextStructuredChar{pcaChar("", 1, 1, 2, 2, 0)}, box},
	}
	for _, c := range cases {
		if got := extraction.CharsInForTest(c.chars, c.box); got != "" {
			t.Errorf("%s: charsIn = %q, want empty", c.name, got)
		}
	}

	// Not degenerate: a zero-area box exactly on a centre reads that glyph, because the bounds
	// are closed.
	pt := responses.CharPosition{Left: 2, Bottom: 2, Right: 2, Top: 2}
	if got := extraction.CharsInForTest(real, pt); got != "A" {
		t.Errorf("zero-area box on a glyph centre: charsIn = %q, want %q", got, "A")
	}

	// A nil element in the slice is skipped, not a panic.
	withNil := []*responses.GetPageTextStructuredChar{nil, pcaChar("Z", 1, 1, 3, 3, 0), nil}
	if got := extraction.CharsInForTest(withNil, box); got != "Z" {
		t.Errorf("nil element: charsIn = %q, want %q", got, "Z")
	}
}

// TestCharsIn_TrailingSpaceIsLostOnEveryFixtureThatHasOne pins [charsin-trailing-space] as a
// count, so 03 cannot char-source a single-rect token's text without this number moving. The AC-3
// specs trim both sides and therefore see none of these.
func TestCharsIn_TrailingSpaceIsLostOnEveryFixtureThatHasOne(t *testing.T) {
	names := pdcNonChromeFixtures(t)
	untrimmedMismatch, trimmedMismatch, rects := 0, 0, 0

	for _, name := range names {
		pages, err := extraction.PDFiumStructuredTextForTest(t.Context(), ptDoc(t, name), "both")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, p := range pages {
			for _, r := range p.Rects {
				rects++
				got := extraction.CharsInForTest(p.Chars, r.PointPosition)
				if got != r.Text {
					untrimmedMismatch++
					if strings.TrimRight(got, " ") != strings.TrimRight(r.Text, " ") {
						t.Errorf("%s page %d rect %q: charsIn = %q -- differs by more than a trailing space, so the whole rule is unsound, not just its trailing-space carve-out", name, p.Number, r.Text, got)
					}
				} else if strings.TrimRight(got, " ") != strings.TrimRight(r.Text, " ") {
					trimmedMismatch++
				}
			}
		}
	}

	if rects < pdcMinNonChromeRects {
		t.Fatalf("scanned %d rect(s), want at least %d", rects, pdcMinNonChromeRects)
	}
	if trimmedMismatch != 0 {
		t.Errorf("%d rect(s) differ after trimming but not before -- impossible; the comparison is wrong", trimmedMismatch)
	}
	if untrimmedMismatch == 0 {
		t.Fatalf("no rect on the 27 word-level fixtures differs before trimming; AC-3's TrimRight is then unexercised and this spec proves nothing")
	}
	t.Logf("[charsin-trailing-space]: %d of %d word-level rect(s) match only after trimming", untrimmedMismatch, rects)
}
