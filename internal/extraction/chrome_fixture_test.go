// chrome_fixture_test.go pins what the shipped reader makes of a Chrome-shaped PDF: one Tj per
// glyph, the way Chrome/Skia prints, rather than fixtures_test.go's one Tj per line. The fixture
// itself (fxBuildChromeRegister, the per-glyph seam) is EXTR-36-01's own implementation step;
// every test below reads the committed bytes only, so its first red is the file being absent.
package extraction_test

import (
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	chrRegister     = "chrome_register.pdf"
	chrRegisterTwin = "chrome_register_twin.pdf"
)

// AC-1 (re-pointed from _FragmentsIntoGlyphs; EXPECTED RED, see task-1063's Implementation
// Notes): the merge reduces the per-glyph page back to real words. Measured: page 1 emits
// exactly 39 tokens, page 2 exactly 3; the shortest is "DUE" at 3 runes, so 0 of 42 are <=2.
func TestChromeRegister_MergesIntoWords(t *testing.T) {
	pages := pdcStructured(t, chrRegister)
	if len(pages) != 2 {
		t.Fatalf("%s carries %d page(s), want 2", chrRegister, len(pages))
	}

	var all []extraction.Token
	counts := map[int]int{}
	for _, p := range pages {
		tokens, _, _ := extraction.PDFiumWordsForTest(p.Rects, p.Chars, p.Number, p.WidthPt, p.HeightPt, extraction.PdfiumSplitGapForTest)
		counts[p.Number] = len(tokens)
		all = append(all, tokens...)
	}
	if counts[1] != 39 {
		t.Errorf("page 1 emits %d token(s), want exactly 39", counts[1])
	}
	if counts[2] != 3 {
		t.Errorf("page 2 emits %d token(s), want exactly 3", counts[2])
	}

	shortRunes, shortest, shortCount := 100, "", 0
	for _, tok := range all {
		if n := utf8.RuneCountInString(tok.Text); n <= 2 {
			shortCount++
		} else if n < shortRunes {
			shortRunes, shortest = n, tok.Text
		}
	}
	if shortCount != 0 {
		t.Errorf("%d of %d merged token(s) are <= 2 runes, want 0", shortCount, len(all))
	}
	if shortest != "DUE" || shortRunes != 3 {
		t.Errorf("shortest merged token (over 2 runes) = %q (%d rune(s)), want %q (3 runes)", shortest, shortRunes, "DUE")
	}
}

// AC-4 (re-pointed): a bled overlapping pair is a raw-rect fact of the generator, independent of
// any later merge. Exactly one such consecutive pair exists on page 1 (the KW/KW bleed), and the
// merged token holding it reads the whole name once.
func TestChromeRegister_BleedsAnOverlappingPair(t *testing.T) {
	page1 := pdcPage1(t, pdcStructured(t, chrRegister))
	if len(page1.Rects) < 2 {
		t.Fatalf("page 1 carries %d rect(s), want at least 2 to scan consecutive pairs", len(page1.Rects))
	}

	found := 0
	for i := 1; i < len(page1.Rects); i++ {
		prev, cur := page1.Rects[i-1], page1.Rects[i]
		if prev == nil || cur == nil {
			continue
		}
		if prev.Text == cur.Text && cur.PointPosition.Left < prev.PointPosition.Right {
			found++
		}
	}
	if found != 1 {
		t.Errorf("found %d consecutive equal-text overlapping rect pair(s) on page 1, want exactly 1 (the KW/KW bleed)", found)
	}

	tokens, _, _ := extraction.PDFiumWordsForTest(page1.Rects, page1.Chars, page1.Number, page1.WidthPt, page1.HeightPt, extraction.PdfiumSplitGapForTest)
	want := "OKONKWO ADVISORY PARTNERS"
	occurrences := 0
	for _, tok := range tokens {
		if tok.Text == want {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Errorf("merged tokens contain %q %d time(s), want exactly 1", want, occurrences)
	}
}

// AC-8 (re-pointed from _TodayAnchorsOnlyVAT): once merged, chrome_register.pdf's anchor labels
// equal advisory_register.pdf's own multiset -- the per-glyph print reads no differently from
// its word-level twin.
func TestChromeRegister_AnchorsMatchItsGeneratedTwin(t *testing.T) {
	chromeObs := extraction.AnchorObservations(pdwMergedTokenPages(t, chrRegister))
	advisoryObs := extraction.AnchorObservations(rvCorpusPages(t, fxAdvisoryRegister))

	chromeLabels := make([]string, len(chromeObs))
	for i, o := range chromeObs {
		chromeLabels[i] = o.Label
	}
	advisoryLabels := make([]string, len(advisoryObs))
	for i, o := range advisoryObs {
		advisoryLabels[i] = o.Label
	}
	sort.Strings(chromeLabels)
	sort.Strings(advisoryLabels)

	if len(advisoryLabels) == 0 {
		t.Fatalf("%s yields no anchor observation; the comparison below would be vacuous", fxAdvisoryRegister)
	}
	if !slices.Equal(chromeLabels, advisoryLabels) {
		t.Errorf("chrome_register.pdf anchor labels = %v, want %s's own %v", chromeLabels, fxAdvisoryRegister, advisoryLabels)
	}
}

// AC-5 (re-pointed): the RAW RECT ratio, not the token ratio the merge is about to collapse.
// Page 1: 577 rects vs advisory_register.pdf's 39 = 14.8x. Whole doc: 802 vs 42 = 19.1x.
func TestChromeRegister_IsNotAWordLevelBuild(t *testing.T) {
	chromePages := pdcStructured(t, chrRegister)
	advisoryPages := pdcStructured(t, fxAdvisoryRegister)

	chromePage1 := pdcPage1(t, chromePages)
	advisoryPage1 := pdcPage1(t, advisoryPages)
	if len(advisoryPage1.Rects) == 0 {
		t.Fatalf("%s page 1 carries no rect; the ratio below would divide by zero", fxAdvisoryRegister)
	}
	if ratio := float64(len(chromePage1.Rects)) / float64(len(advisoryPage1.Rects)); ratio < 10 {
		t.Errorf("chrome_register.pdf reads %d page-1 rect(s) against %s's %d (%.1fx) -- want at least an order of magnitude (10x)",
			len(chromePage1.Rects), fxAdvisoryRegister, len(advisoryPage1.Rects), ratio)
	}

	chromeTotal, advisoryTotal := 0, 0
	for _, p := range chromePages {
		chromeTotal += len(p.Rects)
	}
	for _, p := range advisoryPages {
		advisoryTotal += len(p.Rects)
	}
	if advisoryTotal == 0 {
		t.Fatalf("%s carries no rect; the whole-doc ratio would divide by zero", fxAdvisoryRegister)
	}
	if ratio := float64(chromeTotal) / float64(advisoryTotal); ratio < 10 {
		t.Errorf("chrome_register.pdf reads %d rect(s) against %s's %d (%.1fx) -- want at least an order of magnitude (10x)",
			chromeTotal, fxAdvisoryRegister, advisoryTotal, ratio)
	}
}

// chrRect is one page-1 token in points, not normalised units, so a gap and a rect height are
// both directly comparable to the story's line-height ratios.
type chrRect struct {
	x0, y0, x1, y1 float64
}

func chrRectsForPage(page extraction.TokenPage) []chrRect {
	out := make([]chrRect, len(page.Tokens))
	for i, tok := range page.Tokens {
		out[i] = chrRect{
			x0: tok.Region.X0 * page.WidthPt,
			x1: tok.Region.X1 * page.WidthPt,
			y0: tok.Region.Y0 * page.HeightPt,
			y1: tok.Region.Y1 * page.HeightPt,
		}
	}
	return out
}

// chrOverlap1D mirrors resolve.go's overlap1D: the length two intervals share, negative when
// disjoint.
func chrOverlap1D(a0, a1, b0, b1 float64) float64 { return min(a1, b1) - max(a0, b0) }

// chrGroupLines unions rects whose Y ranges overlap into one visual line -- transitively, so a
// row spanning several separately-placed columns still lands in one group. Union-Find over a
// small page (hundreds of rects) rather than a sweep: simplicity over asymptotic cost here.
func chrGroupLines(rects []chrRect) [][]chrRect {
	parent := make([]int, len(rects))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := range rects {
		for j := i + 1; j < len(rects); j++ {
			if chrOverlap1D(rects[i].y0, rects[i].y1, rects[j].y0, rects[j].y1) > 0 {
				if ri, rj := find(i), find(j); ri != rj {
					parent[ri] = rj
				}
			}
		}
	}

	groups := map[int][]chrRect{}
	for i, r := range rects {
		root := find(i)
		groups[root] = append(groups[root], r)
	}
	out := make([][]chrRect, 0, len(groups))
	for _, g := range groups {
		out = append(out, g)
	}
	return out
}

// chrFragment is a maximal run of X-overlapping rects within one line, collapsed to its union
// box -- Chrome's bled glyphs read as one fragment, not one gap per glyph.
type chrFragment struct {
	x0, x1  float64
	tallest float64 // the line's tallest rect, not the fragment's own
}

// chrFragmentsForLine sorts a line's rects by X and merges strict box overlaps into fragments,
// left to right.
func chrFragmentsForLine(line []chrRect) []chrFragment {
	sorted := make([]chrRect, len(line))
	copy(sorted, line)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].x0 < sorted[j].x0 })

	tallest := 0.0
	for _, r := range sorted {
		if h := r.y1 - r.y0; h > tallest {
			tallest = h
		}
	}

	var frags []chrFragment
	for _, r := range sorted {
		if n := len(frags); n > 0 && r.x0 < frags[n-1].x1 {
			if r.x1 > frags[n-1].x1 {
				frags[n-1].x1 = r.x1
			}
			continue
		}
		frags = append(frags, chrFragment{x0: r.x0, x1: r.x1})
	}
	for i := range frags {
		frags[i].tallest = tallest
	}
	return frags
}

// chrJoinCeiling separates a within-line gap (<=~0.6 line heights on real Chrome advances) from
// a cross-column gap (>=~10): any value strictly between the two clears the split described in
// the story's Section F.
const chrJoinCeiling = 5.0

// AC-1 (re-pointed to raw rects, per architecture Section E: reading tokens made this spec
// merge-owned; on the RAW rects it is merge-proof): the fixture's advances have to come from
// real Chrome/Skia metrics, not from a generator choosing its own comfortable grid -- the widest
// gap a word or a run of prose ever needs to stay joined, and the narrowest gap that actually
// crosses into a different placed run. Reading raw rects keeps every asserted number where it was
// before the merge existed.
func TestChromeRegister_GapsMatchARealChromePrint(t *testing.T) {
	page1 := pdcPage1(t, pdcStructured(t, chrRegister))
	if len(page1.Rects) == 0 {
		t.Fatalf("page 1 carries no rect")
	}
	rects := make([]chrRect, 0, len(page1.Rects))
	for _, r := range page1.Rects {
		if r == nil {
			continue
		}
		p := r.PointPosition
		rects = append(rects, chrRect{x0: p.Left, x1: p.Right, y0: p.Bottom, y1: p.Top})
	}
	if len(rects) == 0 {
		t.Fatalf("page 1 carries no non-nil rect")
	}

	lines := chrGroupLines(rects)
	if len(lines) == 0 {
		t.Fatalf("no line group formed from %d rect(s)", len(rects))
	}

	var within, cross []float64
	for _, line := range lines {
		frags := chrFragmentsForLine(line)
		for i := 1; i < len(frags); i++ {
			gap := frags[i].x0 - frags[i-1].x1
			if frags[i].tallest <= 0 {
				continue
			}
			ratio := gap / frags[i].tallest
			if ratio < chrJoinCeiling {
				within = append(within, ratio)
			} else {
				cross = append(cross, ratio)
			}
		}
	}
	if len(within) == 0 {
		t.Fatalf("no within-line gap found across %d line group(s)", len(lines))
	}
	if len(cross) == 0 {
		t.Fatalf("no cross-column gap found across %d line group(s)", len(lines))
	}

	widest, narrowest := within[0], cross[0]
	for _, r := range within {
		if r > widest {
			widest = r
		}
	}
	for _, r := range cross {
		if r < narrowest {
			narrowest = r
		}
	}

	if widest < 0.55 || widest >= 0.60 {
		t.Errorf("widest within-line gap ratio = %.4f, want [0.55, 0.60)", widest)
	}
	if narrowest < 10.0 {
		t.Errorf("narrowest cross-column gap ratio = %.4f, want >= 10.0", narrowest)
	}
}

// --- byte-level oracles: durable across reader changes ----------------------

// chrGlyphRe matches one emitted glyph: its Td origin and its Tj operand. No fixture text
// carries a parenthesis, so the operand needs no escape handling.
var chrGlyphRe = regexp.MustCompile(`([-0-9.]+) ([-0-9.]+) Td\n\(([^)]*)\) Tj`)

// chrPage1Stream is the committed page-1 content stream, read from the file rather than from the
// generator, so a hand-edited fixture is visible here.
func chrPage1Stream(t *testing.T, name string) []byte {
	t.Helper()
	objs := fxObjects(fxRead(t, name))
	pages := fxPages(t, objs)
	if len(pages) == 0 {
		t.Fatalf("%s declares no page", name)
	}
	return fxContent(t, objs, pages[0])
}

// AC-1/AC-5 at the byte layer. TestChromeRegister_MergesIntoWords and ..._IsNotAWordLevelBuild
// both measure the READER, so subtask 03's merge turns the former red on a fixture that never
// moved. This one reads the emitted operators, so only the generator can move it.
func TestChromeRegister_TheContentStreamEmitsOneTjPerGlyph(t *testing.T) {
	for _, name := range []string{chrRegister, chrRegisterTwin} {
		t.Run(name, func(t *testing.T) {
			ops := chrGlyphRe.FindAllStringSubmatch(string(chrPage1Stream(t, name)), -1)
			if len(ops) < 500 {
				t.Fatalf("page 1 emits %d glyph Tj operator(s), want at least 500 -- a word-level stream wearing a Chrome name", len(ops))
			}
			for _, m := range ops {
				if n := utf8.RuneCountInString(m[3]); n != 1 {
					t.Errorf("Tj operand %q carries %d rune(s), want 1 -- the stream is not one Tj per glyph", m[3], n)
				}
			}
		})
	}
}

// chrRowsByY reconstructs page-1 text per Td ordinate, in emission order.
func chrRowsByY(t *testing.T, name string) map[string]string {
	t.Helper()
	ops := chrGlyphRe.FindAllStringSubmatch(string(chrPage1Stream(t, name)), -1)
	if len(ops) == 0 {
		t.Fatalf("%s page 1 emits no glyph", name)
	}
	rows := map[string]string{}
	for _, m := range ops {
		rows[m[2]] += m[3]
	}
	return rows
}

// chrStripNumerals removes the characters an amount or an invoice number is free to change.
var chrStripNumerals = strings.NewReplacer(
	"0", "", "1", "", "2", "", "3", "", "4", "", "5", "", "6", "", "7", "", "8", "", "9", "", ",", "")

// AC-2. TestFixtures_MatchTheirGenerator only pins the twin's bytes against the generator that
// wrote them, so it cannot see a twin whose LABELS drift. Strip the digits and the two files
// must be the same document.
func TestChromeRegisterTwin_ChangesOnlyTheNumberAndTheAmounts(t *testing.T) {
	primary, twin := chrRowsByY(t, chrRegister), chrRowsByY(t, chrRegisterTwin)
	if len(primary) < 20 {
		t.Fatalf("%s page 1 carries %d text row(s), want at least 20", chrRegister, len(primary))
	}
	if !slices.Equal(slices.Sorted(maps.Keys(primary)), slices.Sorted(maps.Keys(twin))) {
		t.Fatalf("the two fixtures place text on different ordinates -- the arrangement is not shared")
	}

	differing := 0
	for y, want := range primary {
		if got := twin[y]; got != want {
			differing++
		}
		if got, want := chrStripNumerals.Replace(twin[y]), chrStripNumerals.Replace(want); got != want {
			t.Errorf("at y=%s the twin reads %q and the register %q once digits are stripped -- the labels must be identical", y, got, want)
		}
	}
	if differing == 0 {
		t.Errorf("no row differs between %s and %s -- the twin is a copy, not a second invoice", chrRegister, chrRegisterTwin)
	}
}

// The width table must cover every rune the committed fixtures actually emit, and must refuse an
// unknown one rather than advancing 0 and stacking two glyphs on one point.
func TestChromeRegister_TheWidthTableCoversEveryEmittedGlyph(t *testing.T) {
	seen := map[rune]bool{}
	for _, name := range []string{chrRegister, chrRegisterTwin} {
		for _, m := range chrGlyphRe.FindAllStringSubmatch(string(chrPage1Stream(t, name)), -1) {
			for _, r := range m[3] {
				seen[r] = true
			}
		}
	}
	if len(seen) < 30 {
		t.Fatalf("the two fixtures emit %d distinct rune(s), want at least 30", len(seen))
	}
	for r := range seen {
		if _, ok := fxHelvWidths[r]; !ok {
			t.Errorf("fxHelvWidths has no advance for %q, which the committed stream emits", r)
		}
	}

	defer func() {
		if recover() == nil {
			t.Errorf("fxHelvAdvance('₦') returned instead of panicking -- an unknown rune would advance 0")
		}
	}()
	fxHelvAdvance('₦')
}

// The gap oracle's own machinery: overlapping rects collapse to one fragment, disjoint ones keep
// their gap. Without this, TestChromeRegister_GapsMatchARealChromePrint could be measuring a
// helper that merges everything and reports one fragment.
func TestChromeGeometry_FragmentsMergeOverlapsAndKeepGaps(t *testing.T) {
	overlapping := []chrRect{{0, 0, 10, 8}, {9, 0, 19, 8}, {18, 0, 28, 8}}
	if got := chrFragmentsForLine(overlapping); len(got) != 1 {
		t.Errorf("three overlapping rects collapse to %d fragment(s), want 1", len(got))
	}

	spaced := []chrRect{{0, 0, 10, 8}, {14, 0, 24, 8}}
	frags := chrFragmentsForLine(spaced)
	if len(frags) != 2 {
		t.Fatalf("two disjoint rects collapse to %d fragment(s), want 2", len(frags))
	}
	if got := (frags[1].x0 - frags[0].x1) / frags[1].tallest; got != 0.5 {
		t.Errorf("gap ratio = %v, want 0.5", got)
	}

	if got := len(chrGroupLines(append(slices.Clone(overlapping), chrRect{0, 100, 10, 108}))); got != 2 {
		t.Errorf("rects on two vertical bands group into %d line(s), want 2", got)
	}
}

// AC-9. The golden table already carries all three literals, but nothing asserts the RELATION
// between them: a future edit that moved both Chrome rows in lockstep would keep every golden
// green while the twin stopped sharing its layout. These two read Fingerprint live.
func TestChromeRegister_TheTwinSharesItsFingerprint(t *testing.T) {
	primary := extraction.Fingerprint(pdwMergedTokenPages(t, chrRegister))
	twin := extraction.Fingerprint(pdwMergedTokenPages(t, chrRegisterTwin))
	if primary == extraction.Fingerprint(nil) {
		t.Fatalf("%s fingerprints to the empty-observation hash -- it anchors nothing and the comparison below is vacuous", chrRegister)
	}
	if primary != twin {
		t.Errorf("%s = %s, %s = %s -- the twin must share one layout identity", chrRegister, primary, chrRegisterTwin, twin)
	}
	if advisory := extraction.Fingerprint(rvCorpusPages(t, fxAdvisoryRegister)); primary != advisory {
		t.Errorf("%s = %s, %s = %s -- the merged Chrome print must read as its word-level twin's layout", chrRegister, primary, fxAdvisoryRegister, advisory)
	}
}

func TestChromeRegister_ADifferentLayoutDoesNotShareIt(t *testing.T) {
	chrome := extraction.Fingerprint(pdwMergedTokenPages(t, chrRegister))
	dense := extraction.Fingerprint(rvCorpusPages(t, fxAdvisoryDense))
	if dense == extraction.Fingerprint(nil) {
		t.Fatalf("%s fingerprints to the empty-observation hash -- any two layouts would differ from it", fxAdvisoryDense)
	}
	if chrome == dense {
		t.Errorf("%s and %s share fingerprint %s -- the merge has flattened two different arrangements", chrRegister, fxAdvisoryDense, chrome)
	}
}

// --- AC-1, AC-2, AC-4, AC-5 (doc half) ---------------------------------------------------------

const chrDocHeading = "## The Chrome-shaped arrangement"

// chrRectCount sums a fixture's raw rects across every page.
func chrRectCount(t *testing.T, name string) int {
	t.Helper()
	n := 0
	for _, p := range pdcStructured(t, name) {
		n += len(p.Rects)
	}
	return n
}

// chrTokenCount sums a fixture's merged tokens across every page.
func chrTokenCount(t *testing.T, name string) int {
	t.Helper()
	n := 0
	for _, p := range pdwMergedTokenPages(t, name) {
		n += len(p.Tokens)
	}
	return n
}

// AC-1, AC-2, AC-4, AC-5 (doc half). docs/extraction-corpus.md gains a Chrome-shaped
// arrangements section; every fact below that the shipped reader can answer is re-derived here
// rather than trusted, so the doc cannot drift from what the code actually does.
func TestCorpusDoc_RecordsTheChromeArrangement(t *testing.T) {
	doc := acRepoFile(t, acDoc)
	if n := strings.Count(doc, "\n"+chrDocHeading); n != 1 {
		t.Fatalf("%s carries %d %q heading(s), want exactly 1", acDoc, n, chrDocHeading)
	}
	section := acDocSectionText(t, doc, chrDocHeading)
	if strings.TrimSpace(section) == "" {
		t.Fatalf("%s's %q section is empty", acDoc, chrDocHeading)
	}

	registerRects, twinRects := chrRectCount(t, chrRegister), chrRectCount(t, chrRegisterTwin)
	registerTokens, twinTokens := chrTokenCount(t, chrRegister), chrTokenCount(t, chrRegisterTwin)
	if registerRects == 0 || twinRects == 0 || registerTokens == 0 || twinTokens == 0 {
		t.Fatalf("register/twin rects = %d/%d, tokens = %d/%d -- every count must be positive or the presence checks below hold vacuously", registerRects, twinRects, registerTokens, twinTokens)
	}

	for _, needle := range []string{
		chrRegister,
		chrRegisterTwin,
		strconv.Itoa(registerRects),
		strconv.Itoa(twinRects),
		strconv.Itoa(registerTokens),
		strconv.Itoa(twinTokens),
		"NG-3",
		"bytes do not cross into the repository",
		extraction.FingerprintVersion,
		"fingerprintGoldens",
	} {
		if !strings.Contains(section, needle) {
			t.Errorf("%s's %q section never says %q", acDoc, chrDocHeading, needle)
		}
	}

	const addingHeading = "## Adding a layout"
	adding := acDocSectionText(t, doc, addingHeading)
	for _, needle := range []string{chrRegister, chrRegisterTwin} {
		if !strings.Contains(adding, needle) {
			t.Errorf("%s's %q section never names %s", acDoc, addingHeading, needle)
		}
	}
}

// chrPage1Gaps is every consecutive, vertically-overlapping rect pair on chrome_register.pdf
// page 1, as the signed horizontal distance between them: negative is a bleed. Located by
// predicate, never by index -- a regeneration renumbers rects.
func chrPage1Gaps(t *testing.T) (overlaps map[int]float64, narrowest float64, narrowestLeft int) {
	t.Helper()
	page1 := pdcPage1(t, pdcStructured(t, chrRegister))
	overlaps, narrowest, narrowestLeft = map[int]float64{}, math.Inf(1), -1
	for i := 1; i < len(page1.Rects); i++ {
		prev, cur := page1.Rects[i-1], page1.Rects[i]
		if prev == nil || cur == nil {
			continue
		}
		if min(prev.PointPosition.Top, cur.PointPosition.Top)-max(prev.PointPosition.Bottom, cur.PointPosition.Bottom) <= 0 {
			continue
		}
		switch gap := cur.PointPosition.Left - prev.PointPosition.Right; {
		case gap < 0:
			overlaps[i-1] = -gap
		case gap < narrowest:
			narrowest, narrowestLeft = gap, i-1
		}
	}
	if len(overlaps) == 0 || narrowestLeft < 0 {
		t.Fatalf("page 1 yields %d overlap(s) and narrowest-gap rect %d -- the doc checks below would hold vacuously", len(overlaps), narrowestLeft)
	}
	return overlaps, narrowest, narrowestLeft
}

// The bleed-boundary figures the doc's "### The bleed boundary" prints: every overlap depth, the
// narrowest non-bleeding gap, and both rects named by index. Re-derived, so a regeneration that
// moves any of them reds rather than leaving the page stale.
func TestCorpusDoc_RecordsTheBleedBoundary(t *testing.T) {
	overlaps, narrowest, narrowestLeft := chrPage1Gaps(t)
	if len(overlaps) != 3 {
		t.Errorf("page 1 carries %d overlapping pair(s), want 3", len(overlaps))
	}

	section := acDocSectionText(t, acRepoFile(t, acDoc), chrDocHeading)
	shallowestAt, shallowest := -1, math.Inf(1)
	for left, depth := range overlaps {
		if needle := strconv.FormatFloat(depth, 'f', 4, 64); !strings.Contains(section, needle) {
			t.Errorf("%s's %q section never says %q, the overlap at rect %d", acDoc, chrDocHeading, needle, left)
		}
		if depth < shallowest {
			shallowestAt, shallowest = left, depth
		}
	}
	for _, needle := range []string{
		strconv.FormatFloat(narrowest, 'f', 4, 64),
		"(rect " + strconv.Itoa(narrowestLeft) + ")",
		"(rect " + strconv.Itoa(shallowestAt) + ")",
	} {
		if !strings.Contains(section, needle) {
			t.Errorf("%s's %q section never says %q", acDoc, chrDocHeading, needle)
		}
	}

	// The page calls the KW bleed the SHALLOWEST of the three; that only reads true while the
	// VAT line's two engineered overlaps stay deeper.
	if !strings.Contains(section, "The shallowest is "+strconv.FormatFloat(shallowest, 'f', 4, 64)+" pt") {
		t.Errorf("%s's %q section does not name %.4f pt as the shallowest overlap", acDoc, chrDocHeading, shallowest)
	}
}
