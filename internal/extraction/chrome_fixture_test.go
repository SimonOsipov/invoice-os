// chrome_fixture_test.go pins what the shipped reader makes of a Chrome-shaped PDF: one Tj per
// glyph, the way Chrome/Skia prints, rather than fixtures_test.go's one Tj per line. The fixture
// itself (fxBuildChromeRegister, the per-glyph seam) is EXTR-36-01's own implementation step;
// every test below reads the committed bytes only, so its first red is the file being absent.
package extraction_test

import (
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	chrRegister     = "chrome_register.pdf"
	chrRegisterTwin = "chrome_register_twin.pdf"
)

// chrPage1 selects the page numbered 1, not slice position 0, matching AnchorObservations' own
// selection rule.
func chrPage1(t *testing.T, pages []extraction.TokenPage) extraction.TokenPage {
	t.Helper()
	for _, p := range pages {
		if p.Number == 1 {
			return p
		}
	}
	t.Fatalf("no page numbered 1 among %d page(s)", len(pages))
	return extraction.TokenPage{}
}

// AC-4: today's reader fragments a per-glyph page into mostly 1-2 rune tokens.
func TestChromeRegister_FragmentsIntoGlyphs(t *testing.T) {
	page1 := chrPage1(t, rvCorpusPages(t, chrRegister))
	if len(page1.Tokens) == 0 {
		t.Fatalf("page 1 carries no tokens")
	}

	short := 0
	for _, tok := range page1.Tokens {
		if utf8.RuneCountInString(tok.Text) <= 2 {
			short++
		}
	}
	if ratio := float64(short) / float64(len(page1.Tokens)); ratio < 0.90 {
		t.Errorf("%d/%d (%.4f) page-1 tokens are <=2 runes, want >= 0.90", short, len(page1.Tokens), ratio)
	}
}

// AC-4: a negatively-advanced glyph pair reads back as one bleeding rect per glyph -- equal
// text, overlapping boxes -- scanned in token order the way pdfium emits them.
func TestChromeRegister_BleedsAnOverlappingPair(t *testing.T) {
	page1 := chrPage1(t, rvCorpusPages(t, chrRegister))
	if len(page1.Tokens) < 2 {
		t.Fatalf("page 1 carries %d token(s), want at least 2 to scan consecutive pairs", len(page1.Tokens))
	}

	found := 0
	for i := 1; i < len(page1.Tokens); i++ {
		prev, cur := page1.Tokens[i-1], page1.Tokens[i]
		if prev.Text == cur.Text && cur.Region.X0 < prev.Region.X1 {
			found++
		}
	}
	if found == 0 {
		t.Errorf("no consecutive pair carries equal text with an overlapping box -- the Chrome-shaped bleed never appears")
	}
}

// AC-4: this is AC-1's pre-fix pin. A one-rect-per-glyph page yields zero anchors (no single
// character matches the lexicon), so the fixture must also carry a bleed that reassembles at
// least "VAT " -- read against the word-level twin's own list for context on failure.
func TestChromeRegister_TodayAnchorsOnlyVAT(t *testing.T) {
	obs := extraction.AnchorObservations(rvCorpusPages(t, chrRegister))

	if len(obs) != 1 || obs[0].Label != "vat" {
		got := make([]string, len(obs))
		for i, o := range obs {
			got[i] = o.Label
		}
		twinObs := extraction.AnchorObservations(rvCorpusPages(t, fxAdvisoryRegister))
		twinLabels := make([]string, len(twinObs))
		for i, o := range twinObs {
			twinLabels[i] = o.Label
		}
		t.Errorf("AnchorObservations = %v, want exactly [vat] -- the word-level twin %s yields %v", got, fxAdvisoryRegister, twinLabels)
	}
}

// AC-5: the fixture must not be a word-level build wearing a new name -- its page-1 token count
// has to clear the word-level twin's own by an order of magnitude.
func TestChromeRegister_IsNotAWordLevelBuild(t *testing.T) {
	chromeTokens := len(chrPage1(t, rvCorpusPages(t, chrRegister)).Tokens)
	wordTokens := len(chrPage1(t, rvCorpusPages(t, fxAdvisoryRegister)).Tokens)
	if wordTokens == 0 {
		t.Fatalf("%s page 1 carries no tokens; the ratio below would divide by zero", fxAdvisoryRegister)
	}

	if ratio := float64(chromeTokens) / float64(wordTokens); ratio < 10 {
		t.Errorf("chrome_register.pdf reads %d page-1 token(s) against %s's %d (%.1fx) -- want at least an order of magnitude (10x)",
			chromeTokens, fxAdvisoryRegister, wordTokens, ratio)
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

// AC-1: the fixture's advances have to come from real Chrome/Skia metrics, not from a generator
// choosing its own comfortable grid -- the widest gap a word or a run of prose ever needs to
// stay joined, and the narrowest gap that actually crosses into a different placed run.
func TestChromeRegister_GapsMatchARealChromePrint(t *testing.T) {
	page1 := chrPage1(t, rvCorpusPages(t, chrRegister))
	rects := chrRectsForPage(page1)
	if len(rects) == 0 {
		t.Fatalf("page 1 carries no tokens")
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

// AC-1/AC-5 at the byte layer. TestChromeRegister_FragmentsIntoGlyphs and
// ..._IsNotAWordLevelBuild both measure the READER, so a later subtask that merges glyphs into
// words turns them red on a fixture that never moved. This one reads the emitted operators, so
// only the generator can move it.
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
