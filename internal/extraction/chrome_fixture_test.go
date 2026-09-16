// chrome_fixture_test.go pins what the shipped reader makes of a Chrome-shaped PDF: one Tj per
// glyph, the way Chrome/Skia prints, rather than fixtures_test.go's one Tj per line. The fixture
// itself (fxBuildChromeRegister, the per-glyph seam) is EXTR-36-01's own implementation step;
// every test below reads the committed bytes only, so its first red is the file being absent.
package extraction_test

import (
	"sort"
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
