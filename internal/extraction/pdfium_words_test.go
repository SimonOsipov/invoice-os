// pdfium_words_test.go: EXTR-36-03's word stage -- lines -> fragments -> words -- and its
// measured splitGap window. pdfiumWords/pdfiumFragmentGaps do not exist yet, so every
// PDFiumWordsForTest/PDFiumGapsForTest call is compile-red until pdfium.go adds them (subtask
// 03's own merge). Same convention as pdfium_chars_test.go's charsIn note.
package extraction_test

import (
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/klippa-app/go-pdfium/responses"
)

// --- harness ------------------------------------------------------------------------------

// pdwMerged runs the merge over every page of a committed fixture at splitGap, tokens
// concatenated in page order.
func pdwMerged(t *testing.T, name string, splitGap float64) (tokens []extraction.Token, textChars, groups int) {
	t.Helper()
	for _, p := range pdcStructured(t, name) {
		tk, tc, g := extraction.PDFiumWordsForTest(p.Rects, p.Chars, p.Number, p.WidthPt, p.HeightPt, splitGap)
		tokens = append(tokens, tk...)
		textChars += tc
		groups += g
	}
	return tokens, textChars, groups
}

// pdwMergedTokenPages merges every page of a fixture and wraps the result as TokenPage, so
// callers can feed Fingerprint/AnchorObservations the post-merge output directly.
func pdwMergedTokenPages(t *testing.T, name string) []extraction.TokenPage {
	t.Helper()
	var pages []extraction.TokenPage
	for _, p := range pdcStructured(t, name) {
		tokens, _, _ := extraction.PDFiumWordsForTest(p.Rects, p.Chars, p.Number, p.WidthPt, p.HeightPt, extraction.PdfiumSplitGapForTest)
		pages = append(pages, extraction.TokenPage{Number: p.Number, WidthPt: p.WidthPt, HeightPt: p.HeightPt, Tokens: tokens})
	}
	return pages
}

// pdwPreMergeTokens rebuilds the PRE-merge reader output -- one token per non-empty rect, in
// stream order, with pdfium.go's own bottom-to-top flip -- straight from the raw rects. It never
// calls Read(), which now merges, so a comparison against it measures invariance and not
// determinism. Repaired here by QA: sourcing "before" from ptRead made every such comparison a
// post-merge value against itself.
func pdwPreMergeTokens(t *testing.T, name string) []extraction.Token {
	t.Helper()
	var out []extraction.Token
	for _, p := range pdcStructured(t, name) {
		for _, r := range p.Rects {
			if r == nil || r.Text == "" {
				continue
			}
			pos := r.PointPosition
			out = append(out, extraction.Token{Text: r.Text, Region: extraction.Region{
				Page: p.Number,
				X0:   pos.Left / p.WidthPt,
				Y0:   (p.HeightPt - pos.Top) / p.HeightPt,
				X1:   pos.Right / p.WidthPt,
				Y1:   (p.HeightPt - pos.Bottom) / p.HeightPt,
			}})
		}
	}
	return out
}

func pdwTexts(tokens []extraction.Token) []string {
	out := make([]string, len(tokens))
	for i, tok := range tokens {
		out[i] = tok.Text
	}
	return out
}

// pdwStrip removes the whitespace a text-matching comparison here needs to ignore -- pdfium
// tokens carry spaces but never tabs or newlines.
func pdwStrip(s string) string { return strings.ReplaceAll(s, " ", "") }

// pdwTokenTexts is one fixture's CURRENT (one-rect-per-token) reader output, stripped -- ground
// truth for "this concatenated span makes a real word."
func pdwTokenTexts(t *testing.T, name string) []string {
	t.Helper()
	pages, _ := ptRead(t, name)
	out := make([]string, 0, len(ptTokens(pages)))
	for _, tok := range ptTokens(pages) {
		out = append(out, pdwStrip(tok.Text))
	}
	return out
}

// pdwFloor recomputes AC-5's floor at test time: over chrome_register.pdf page 1, walk each
// line's fragment gaps in order, growing a span while it stays a PREFIX of some
// advisory_register.pdf token; every time the span completes a WHOLE token (>=2 fragments), the
// widest gap inside that span is a floor candidate. The floor is the max candidate.
func pdwFloor(t *testing.T) float64 {
	t.Helper()
	targets := pdwTokenTexts(t, fxAdvisoryRegister)
	if len(targets) == 0 {
		t.Fatalf("%s carries no token to match spans against", fxAdvisoryRegister)
	}
	isPrefix := func(s string) bool {
		for _, tgt := range targets {
			if strings.HasPrefix(tgt, s) {
				return true
			}
		}
		return false
	}
	isWhole := func(s string) bool { return slices.Contains(targets, s) }

	page1 := pdcPage1(t, pdcStructured(t, chrRegister))
	gaps := extraction.PDFiumGapsForTest(page1.Rects, page1.Chars)
	if len(gaps) == 0 {
		t.Fatalf("%s page 1 carries no same-line fragment gap", chrRegister)
	}

	floor := math.Inf(-1)
	line := gaps[0].Line - 1 // force a reset on the first gap
	var span string
	var widest float64
	members := 0
	for _, g := range gaps {
		if g.Line != line {
			line, span, widest, members = g.Line, pdwStrip(g.Left), math.Inf(-1), 1
		}
		candidate := span + pdwStrip(g.Right)
		if !isPrefix(candidate) {
			span, widest, members = pdwStrip(g.Right), math.Inf(-1), 1
			continue
		}
		widest, members, span = max(widest, g.Ratio), members+1, candidate
		if members >= 2 && isWhole(span) {
			floor = max(floor, widest)
		}
	}
	if math.IsInf(floor, -1) {
		t.Fatalf("no >=2-fragment span on %s page 1 matched a whole %s token -- the floor recipe found nothing", chrRegister, fxAdvisoryRegister)
	}
	return floor
}

// pdwCeiling recomputes AC-5's ceiling at test time: the narrowest same-line fragment gap
// across the 29 generator-built fixtures.
func pdwCeiling(t *testing.T) (ceiling float64, fixture, left, right string) {
	t.Helper()
	names := pdcNonChromeFixtures(t)
	ceiling = math.Inf(1)
	for _, name := range names {
		for _, p := range pdcStructured(t, name) {
			for _, g := range extraction.PDFiumGapsForTest(p.Rects, p.Chars) {
				if g.Ratio < ceiling {
					ceiling, fixture, left, right = g.Ratio, name, g.Left, g.Right
				}
			}
		}
	}
	if math.IsInf(ceiling, 1) {
		t.Fatalf("no same-line fragment gap found across %d non-chrome fixture(s)", len(names))
	}
	return ceiling, fixture, left, right
}

// --- AC-1: the line-grouping tolerance -----------------------------------------------------

// pdwMustJoinFloor: the smallest positive Y-overlap between a mid-word punctuation glyph and
// its immediate stream neighbour, on the Chrome per-glyph fixtures. A hyphen or comma inside a
// token can only sit, in stream order, next to the characters either side of it -- independent
// of any Y-overlap grouping decision, so this is not circular with the predicate it bounds.
func pdwMustJoinFloor(t *testing.T) float64 {
	t.Helper()
	punct := map[string]bool{"-": true, ",": true, ".": true}
	floor := math.Inf(1)
	for _, name := range []string{chrRegister, chrRegisterTwin} {
		for _, p := range pdcStructured(t, name) {
			for i, r := range p.Rects {
				if r == nil || !punct[r.Text] {
					continue
				}
				for _, j := range [2]int{i - 1, i + 1} {
					if j < 0 || j >= len(p.Rects) || p.Rects[j] == nil || p.Rects[j].Text == "" {
						continue
					}
					n := p.Rects[j]
					overlap := min(r.PointPosition.Top, n.PointPosition.Top) - max(r.PointPosition.Bottom, n.PointPosition.Bottom)
					if overlap > 0 && overlap < floor {
						floor = overlap
					}
				}
			}
		}
	}
	return floor
}

// pdwMustNotMergeCeiling: the smallest positive Y-gap between any two rects on one page, across
// the non-word-level, non-Chrome fixtures. Two rects can only report a positive separation if
// their Y-ranges do not overlap at all, so this needs no line-grouping decision either: a
// same-line pair (word-level fixtures, or two columns on one row) shows up as <= 0 and is
// excluded by the sign check alone. All pairs, not just stream-consecutive ones -- the closest
// cross-line pair is not always adjacent in stream order (a row's other columns sit between).
func pdwMustNotMergeCeiling(t *testing.T) float64 {
	t.Helper()
	ceiling := math.Inf(1)
	for _, name := range pdcNonChromeFixtures(t) {
		if name == fxAdvisoryRegister || name == "advisory_register_unspaced.pdf" {
			continue // word-level: many same-row rects are routinely same-line by design
		}
		for _, p := range pdcStructured(t, name) {
			for i := 0; i < len(p.Rects); i++ {
				a := p.Rects[i]
				if a == nil || a.Text == "" {
					continue
				}
				for j := i + 1; j < len(p.Rects); j++ {
					b := p.Rects[j]
					if b == nil || b.Text == "" {
						continue
					}
					sep := max(a.PointPosition.Bottom, b.PointPosition.Bottom) - min(a.PointPosition.Top, b.PointPosition.Top)
					if sep > 0 && sep < ceiling {
						ceiling = sep
					}
				}
			}
		}
	}
	return ceiling
}

// AC-1: strict overlap (tolerance zero) must sit inside the window between the weakest
// confirmed must-join case and the weakest confirmed must-not-merge case.
func TestPDFiumWords_LineGroupingToleranceStaysInsideItsMeasuredWindow(t *testing.T) {
	mustJoin := pdwMustJoinFloor(t)
	mustNotMerge := pdwMustNotMergeCeiling(t)
	if math.IsInf(mustJoin, 1) {
		t.Fatalf("no mid-word punctuation pair found on the Chrome fixtures")
	}
	if math.IsInf(mustNotMerge, 1) {
		t.Fatalf("no positive cross-line separation found across the scanned fixtures")
	}
	if mustNotMerge >= mustJoin {
		t.Fatalf("must-not-merge bound %.4f is not below must-join bound %.4f -- no tolerance window exists", mustNotMerge, mustJoin)
	}
	t.Logf("tolerance window = (-%.4f, +%.4f] pt", mustJoin, mustNotMerge)
	if !(0 > -mustJoin && 0 <= mustNotMerge) {
		t.Errorf("tolerance 0 does not sit inside (-%.4f, %.4f]", mustJoin, mustNotMerge)
	}
}

// AC-1: a comma below an amount's digits and a hyphen inside a TIN both stay on their own line.
func TestPDFiumWords_ACommaStaysOnItsAmountLine(t *testing.T) {
	tokens, _, _ := pdwMerged(t, chrRegister, extraction.PdfiumSplitGapForTest)
	if len(tokens) == 0 {
		t.Fatalf("%s merged to no token", chrRegister)
	}

	found := false
	for _, tok := range tokens {
		if tok.Text == "," {
			t.Errorf("a bare comma token survived the merge at region %+v", tok.Region)
		}
		if tok.Text == "14,800,000.00" {
			found = true
		}
	}
	if !found {
		t.Errorf("tokens = %v, want one reading %q (see EXTR-36-01's currency-glyph note: no naira mark)", pdwTexts(tokens), "14,800,000.00")
	}
}

func TestPDFiumWords_AHyphenStaysInsideItsTIN(t *testing.T) {
	tokens, _, _ := pdwMerged(t, chrRegister, extraction.PdfiumSplitGapForTest)
	want := "TIN 99999999-1311"
	if !slices.Contains(pdwTexts(tokens), want) {
		t.Errorf("tokens = %v, want one reading %q", pdwTexts(tokens), want)
	}
}

// --- AC-2: fragments ------------------------------------------------------------------------

// AC-2: a maximal run of pairwise box-overlapping rects on one line is one fragment; a
// non-overlapping repeated glyph must not be swallowed into it.
//
// The token assertions alone cannot see stage 2: an overlapping pair stage 2 fails to fragment
// still has a NEGATIVE gap, so stage 3 rejoins it and the tokens come out identical. The gap
// list is stage 1+2's own output and is the only oracle here that binds stage 2's predicate.
func TestPDFiumWords_AnOverlappingRunIsOneFragment(t *testing.T) {
	rect := func(text string, left, right float64) *responses.GetPageTextStructuredRect {
		return &responses.GetPageTextStructuredRect{Text: text, PointPosition: responses.CharPosition{Left: left, Right: right, Top: 10, Bottom: 0}}
	}
	char := func(text string, left, right float64) *responses.GetPageTextStructuredChar {
		return &responses.GetPageTextStructuredChar{Text: text, PointPosition: responses.CharPosition{Left: left, Right: right, Top: 10, Bottom: 0}}
	}
	// S sits 1 pt past the second R: close enough that a predicate which joined NEAR-touching
	// rects would swallow it, far enough that strict overlap does not.
	rects := []*responses.GetPageTextStructuredRect{rect("K", 0, 8), rect("W", 6, 14), rect("R", 30, 38), rect("R", 40, 48), rect("S", 49, 57)}
	chars := []*responses.GetPageTextStructuredChar{char("K", 0, 8), char("W", 6, 14), char("R", 30, 38), char("R", 40, 48), char("S", 49, 57)}

	// Stage 1+2 alone: K+W is one fragment; R, R and S are three more; so the line carries
	// exactly three adjacent-fragment gaps. Only the gap list binds stage 2 -- see the note above.
	gaps := extraction.PDFiumGapsForTest(rects, chars)
	if len(gaps) != 3 {
		t.Fatalf("stage 2 formed %d gap(s) (%d fragment(s)), want 3 (4 fragments: KW, R, R, S)", len(gaps), len(gaps)+1)
	}
	for i, want := range [3][2]string{{"KW", "R"}, {"R", "R"}, {"R", "S"}} {
		if gaps[i].Left != want[0] || gaps[i].Right != want[1] {
			t.Errorf("gap %d spans %q|%q, want %q|%q", i, gaps[i].Left, gaps[i].Right, want[0], want[1])
		}
	}

	tokens, _, groups := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, 0)
	if groups != 4 {
		t.Fatalf("%d group(s), want 4 (K+W merged, then two separate R's and an S)", groups)
	}
	texts := pdwTexts(tokens)
	if !slices.Contains(texts, "KW") {
		t.Errorf("tokens = %v, want one reading %q (the bleeding pair collapsed)", texts, "KW")
	}
	rCount := 0
	for _, tx := range texts {
		if tx == "R" {
			rCount++
		}
	}
	if rCount != 2 {
		t.Errorf("tokens = %v, want exactly 2 separate %q tokens", texts, "R")
	}
}

// --- AC-3/5: splitGap's measured window ------------------------------------------------------

func TestPDFiumWords_SplitGapStaysInsideItsMeasuredWindow(t *testing.T) {
	floor := pdwFloor(t)
	ceiling, ceilFixture, ceilLeft, ceilRight := pdwCeiling(t)

	if ceiling <= floor {
		t.Fatalf("ceiling %.17g is not above floor %.17g -- the window is empty (ceiling source: %s %q|%q)", ceiling, floor, ceilFixture, ceilLeft, ceilRight)
	}
	if !(floor < extraction.PdfiumSplitGapForTest && extraction.PdfiumSplitGapForTest <= ceiling) {
		t.Errorf("splitGap %.4f is not inside (floor %.17g, ceiling %.17g]", extraction.PdfiumSplitGapForTest, floor, ceiling)
	}

	t.Run("floor_binds", func(t *testing.T) {
		tokens, _, _ := pdwMerged(t, chrRegister, floor)
		texts := pdwTexts(tokens)
		whole := "Three sittings, Lagos tax office"
		if slices.Contains(texts, whole) {
			t.Errorf("at splitGap==floor (%.17g), %q is still one token", floor, whole)
		}
		if !slices.Contains(texts, "Three sittings,") {
			t.Errorf("at splitGap==floor, tokens = %v, want %q present", texts, "Three sittings,")
		}
		if !slices.Contains(texts, "Lagos tax office") {
			t.Errorf("at splitGap==floor, tokens = %v, want %q present", texts, "Lagos tax office")
		}
	})

	t.Run("ceiling_binds", func(t *testing.T) {
		for _, name := range []string{"wild_ruled_lines_totals.pdf", "wild_ruled_lines_totals_asprinted.pdf"} {
			t.Run(name, func(t *testing.T) {
				atCeiling, _, _ := pdwMerged(t, name, ceiling)
				texts := pdwTexts(atCeiling)
				if !slices.Contains(texts, "500.00 ") {
					t.Errorf("at splitGap==ceiling (%.17g), tokens = %v, want %q present (still two tokens)", ceiling, texts, "500.00 ")
				}
				if !slices.Contains(texts, "Total") {
					t.Errorf("at splitGap==ceiling, tokens = %v, want %q present", texts, "Total")
				}

				past := math.Nextafter(ceiling, math.Inf(1))
				atPast, _, _ := pdwMerged(t, name, past)
				pastTexts := pdwTexts(atPast)
				if !slices.Contains(pastTexts, "500.00 Total") {
					t.Errorf("at splitGap==Nextafter(ceiling,+Inf) (%.17g), tokens = %v, want %q present (glued)", past, pastTexts, "500.00 Total")
				}
			})
		}
	})

	// Never fails: a documentation record of the window's own tightness.
	t.Run("the_window_is_narrow", func(t *testing.T) {
		t.Logf("window width = %.17g (floor %.17g, ceiling %.17g); constant %.2f clears floor by %.17g and ceiling by %.17g",
			ceiling-floor, floor, ceiling, extraction.PdfiumSplitGapForTest,
			extraction.PdfiumSplitGapForTest-floor, ceiling-extraction.PdfiumSplitGapForTest)
	})
}

// --- AC-4: text and box rules -----------------------------------------------------------------

// AC-4/AC-12 control: on the non-Chrome fixtures every group is single-rect, so the merged
// slice must be byte-identical to today's, index for index.
func TestPDFiumWords_ASingleRectTokenIsUnchanged(t *testing.T) {
	names := pdcNonChromeFixtures(t)
	if len(names) != 38 {
		t.Fatalf("pdcNonChromeFixtures returned %d name(s), want exactly 38", len(names))
	}

	compared := 0
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			before := pdwPreMergeTokens(t, name)
			compared += len(before)

			after, _, groups := pdwMerged(t, name, extraction.PdfiumSplitGapForTest)
			if groups != len(before) {
				t.Fatalf("%d group(s) post-merge, %d token(s) before -- a word-level fixture must keep one group per rect", groups, len(before))
			}
			if len(after) != len(before) {
				t.Fatalf("%d token(s) post-merge, %d before", len(after), len(before))
			}
			for i := range before {
				if after[i].Text != before[i].Text {
					t.Errorf("token %d: text %q post-merge, want %q (unchanged)", i, after[i].Text, before[i].Text)
				}
				if after[i].Region != before[i].Region {
					t.Errorf("token %d: region %+v post-merge, want %+v (unchanged)", i, after[i].Region, before[i].Region)
				}
			}
		})
	}
	if compared < pdcMinNonChromeRects {
		t.Fatalf("compared %d pre-merge token(s) across %d fixture(s), want at least %d -- the loop above checked too little", compared, len(names), pdcMinNonChromeRects)
	}
}

// AC-12 IS WRONG in the story (see architecture note H): under the relative rule at 0.60, zero
// of the 29 generator-built fixtures move. Only the two Chrome fixtures do, and each collapses to
// exactly advisory_register.pdf's own word-level count -- the two fixtures become one layout.
//
// "before" is the RAW rect count, never Read(): Read() now merges, so sourcing it from there
// compared the merged result against itself and could not fail.
func TestPDFiumWords_OnlyTheTwoChromeFixturesMove(t *testing.T) {
	// AC-12 wants each moving fixture named with its before and after count. Before is
	// structural -- one non-empty rect, one pre-merge token -- and the content-stream pins
	// (TestChromeRegister_TheContentStreamEmitsOneTjPerGlyph) hold these two numbers still.
	chromeBefore := map[string]int{chrRegister: 802, chrRegisterTwin: 800}

	names := slices.Sorted(maps.Keys(fingerprintGoldens))
	if len(names) < fgGoldenFloor {
		t.Fatalf("fingerprintGoldens holds %d row(s), want at least %d", len(names), fgGoldenFloor)
	}

	advisoryPages, _ := ptRead(t, fxAdvisoryRegister)
	wantChromeAfter := len(ptTokens(advisoryPages))
	if wantChromeAfter == 0 {
		t.Fatalf("%s carries no token; the Chrome comparison below would be vacuous", fxAdvisoryRegister)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			before := len(pdwPreMergeTokens(t, name))

			after, _, groups := pdwMerged(t, name, extraction.PdfiumSplitGapForTest)
			if groups != len(after) {
				t.Fatalf("%d group(s), %d token(s) -- one token per group must hold", groups, len(after))
			}

			switch name {
			case chrRegister, chrRegisterTwin:
				if want := chromeBefore[name]; before != want {
					t.Errorf("%s: pre-merge token count = %d, want %d -- the fixture itself moved", name, before, want)
				}
				if len(after) != wantChromeAfter {
					t.Errorf("%s: merged token count = %d, want %d (advisory_register.pdf's own word-level count)", name, len(after), wantChromeAfter)
				}
				if len(after) >= before {
					t.Errorf("%s: %d rect(s) merged to %d token(s) -- the merge did nothing", name, before, len(after))
				}
			default:
				if len(after) != before {
					t.Errorf("%s: %d rect(s) merged to %d token(s), want one token per rect (the merge is identity on a word-level PDF)", name, before, len(after))
				}
			}
		})
	}
}

// AC-11: a line of exactly one rect emits that rect unchanged -- no division by an empty
// neighbour set (a NaN box would fail the equality check below just as loudly as a wrong one).
func TestPDFiumWords_ALineOfOneRectEmitsThatRect(t *testing.T) {
	box := responses.CharPosition{Left: 10, Right: 40, Top: 20, Bottom: 10}
	rects := []*responses.GetPageTextStructuredRect{{Text: "SOLO", PointPosition: box}}
	chars := []*responses.GetPageTextStructuredChar{{Text: "SOLO", PointPosition: box}}

	tokens, _, groups := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
	if groups != 1 {
		t.Fatalf("%d group(s), want 1", groups)
	}
	if len(tokens) != 1 {
		t.Fatalf("%d token(s), want 1", len(tokens))
	}
	if tokens[0].Text != "SOLO" {
		t.Errorf("token text = %q, want %q", tokens[0].Text, "SOLO")
	}
	want := extraction.Region{Page: 1, X0: 10.0 / 1000, Y0: (1000 - 20.0) / 1000, X1: 40.0 / 1000, Y1: (1000 - 10.0) / 1000}
	if tokens[0].Region != want {
		t.Errorf("token region = %+v, want %+v", tokens[0].Region, want)
	}
}

// AC-4/D: after grouping, emitted token count equals the group count, and no token text is
// empty. 626 groups measured at HEAD; a floor, not a pin, since Stage 3's real count depends on
// its own implementation.
func TestPDFiumWords_NoTokenVanishes(t *testing.T) {
	const groupFloor = 600

	names := slices.Sorted(maps.Keys(fingerprintGoldens))
	if len(names) < fgGoldenFloor {
		t.Fatalf("fingerprintGoldens holds %d row(s), want at least %d", len(names), fgGoldenFloor)
	}

	totalTokens, totalGroups := 0, 0
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			tokens, _, groups := pdwMerged(t, name, extraction.PdfiumSplitGapForTest)
			if len(tokens) != groups {
				t.Errorf("%d token(s) emitted, %d group(s) formed -- a group vanished or split", len(tokens), groups)
			}
			for i, tok := range tokens {
				if tok.Text == "" {
					t.Errorf("token %d carries empty text", i)
				}
			}
			totalTokens += len(tokens)
			totalGroups += groups
		})
	}

	if totalGroups < groupFloor {
		t.Fatalf("scanned %d group(s) across %d fixture(s), want at least %d -- the loop above would have checked too little", totalGroups, len(names), groupFloor)
	}
	if totalTokens != totalGroups {
		t.Errorf("total tokens %d != total groups %d", totalTokens, totalGroups)
	}
}

// AC-4: an empty (nil) chars slice must fall back to the leftmost member's own rect text, never
// to an empty string that would make the group vanish.
func TestPDFiumWords_AnEmptyCharsInFallsBackToTheRectText(t *testing.T) {
	rects := []*responses.GetPageTextStructuredRect{
		{Text: "AB", PointPosition: responses.CharPosition{Left: 0, Right: 10, Top: 10, Bottom: 0}},
		{Text: "CD", PointPosition: responses.CharPosition{Left: 6, Right: 16, Top: 10, Bottom: 0}},
	}
	tokens, _, groups := extraction.PDFiumWordsForTest(rects, nil, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
	if groups != 1 {
		t.Fatalf("%d group(s), want 1 (the pair overlaps and must merge into one fragment)", groups)
	}
	if len(tokens) != 1 {
		t.Fatalf("%d token(s), want 1", len(tokens))
	}
	if tokens[0].Text == "" {
		t.Errorf("merged token carries empty text with a nil chars slice -- the group vanished")
	}
	if tokens[0].Text != "AB" {
		t.Errorf("merged token text = %q, want %q (the leftmost member's own rect text)", tokens[0].Text, "AB")
	}
}

// [charsin-stream-contiguity]: each multi-rect token's charsIn selection must be one contiguous
// run of the chars stream, with zero interlopers from a neighbouring token.
func TestPDFiumWords_AMergedTokensCharsAreAContiguousStreamRun(t *testing.T) {
	for _, name := range []string{chrRegister, chrRegisterTwin} {
		t.Run(name, func(t *testing.T) {
			for _, p := range pdcStructured(t, name) {
				tokens, _, _ := extraction.PDFiumWordsForTest(p.Rects, p.Chars, p.Number, p.WidthPt, p.HeightPt, extraction.PdfiumSplitGapForTest)
				checked := 0
				for _, tok := range tokens {
					box := responses.CharPosition{
						Left:   tok.Region.X0 * p.WidthPt,
						Right:  tok.Region.X1 * p.WidthPt,
						Top:    p.HeightPt - tok.Region.Y0*p.HeightPt,
						Bottom: p.HeightPt - tok.Region.Y1*p.HeightPt,
					}
					var idx []int
					for i, c := range p.Chars {
						if c == nil || c.Text == "\r" || c.Text == "\n" {
							continue
						}
						cx := (c.PointPosition.Left + c.PointPosition.Right) / 2
						cy := (c.PointPosition.Top + c.PointPosition.Bottom) / 2
						if cx >= box.Left && cx <= box.Right && cy >= box.Bottom && cy <= box.Top {
							idx = append(idx, i)
						}
					}
					if len(idx) < 2 {
						continue
					}
					checked++
					if span := idx[len(idx)-1] - idx[0] + 1; span != len(idx) {
						t.Errorf("token %q spans char indices %v -- not contiguous (span %d, count %d)", tok.Text, idx, span, len(idx))
					}
				}
				if checked == 0 {
					t.Errorf("%s page %d: no multi-char merged token found to check", name, p.Number)
				}
			}
		})
	}
}

// --- QA additions (EXTR-36-03 verification) --------------------------------------------------

// pdwRect / pdwChar build one synthetic box in pdfium's own bottom-up space.
func pdwRect(text string, left, right, bottom, top float64) *responses.GetPageTextStructuredRect {
	return &responses.GetPageTextStructuredRect{Text: text, PointPosition: responses.CharPosition{Left: left, Right: right, Bottom: bottom, Top: top}}
}

func pdwChar(text string, left, right, bottom, top float64) *responses.GetPageTextStructuredChar {
	return &responses.GetPageTextStructuredChar{Text: text, PointPosition: responses.CharPosition{Left: left, Right: right, Bottom: bottom, Top: top}}
}

// AC-1: the line tolerance is a production number, and the window spec above asserts the literal
// 0 against bounds computed from raw geometry -- it never reads what pdfiumLineGroups actually
// uses. These two arms do: each fails if production's tolerance crosses its measured bound.
//
// Measured bounds (architecture Section C): a 7 pt hyphen overlaps its line by 0.6300 pt and MUST
// still join; advisory_dense.pdf's two closest lines sit 0.1620 pt apart and MUST stay apart.
func TestPDFiumWords_TheLineToleranceBindsAtBothMeasuredBounds(t *testing.T) {
	t.Run("must_not_merge_at_0.1620pt_apart", func(t *testing.T) {
		rects := []*responses.GetPageTextStructuredRect{
			pdwRect("AA", 0, 10, 10, 20),
			pdwRect("BB", 11, 21, 20.162, 30.162),
		}
		chars := []*responses.GetPageTextStructuredChar{
			pdwChar("AA", 0, 10, 10, 20),
			pdwChar("BB", 11, 21, 20.162, 30.162),
		}
		tokens, _, _ := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
		if got := pdwTexts(tokens); !slices.Equal(got, []string{"AA", "BB"}) {
			t.Errorf("tokens = %v, want [AA BB] -- two rows 0.1620 pt apart joined into one line, so the tolerance has gone positive", got)
		}
	})

	t.Run("must_join_at_0.6300pt_of_overlap", func(t *testing.T) {
		rects := []*responses.GetPageTextStructuredRect{
			pdwRect("CC", 0, 10, 10, 20),
			pdwRect("DD", 11, 21, 19.37, 29.37),
		}
		chars := []*responses.GetPageTextStructuredChar{
			pdwChar("CC", 0, 10, 10, 20),
			pdwChar("DD", 11, 21, 19.37, 29.37),
		}
		tokens, _, _ := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
		if got := pdwTexts(tokens); !slices.Equal(got, []string{"CCDD"}) {
			t.Errorf("tokens = %v, want [CCDD] -- a 0.6300 pt overlap (the 7 pt hyphen) no longer joins its line", got)
		}
	})
}

// Section A's emission rule: ascending first-member stream index, NOT line-then-X. The two rects
// below are on one line in right-to-left stream order, so the two orderings disagree -- and no
// fingerprint can tell them apart, since Fingerprint sorts its own observations.
func TestPDFiumWords_EmissionFollowsStreamOrderNotVisualOrder(t *testing.T) {
	rects := []*responses.GetPageTextStructuredRect{
		pdwRect("RIGHT", 30, 38, 0, 10),
		pdwRect("LEFT", 0, 8, 0, 10),
	}
	chars := []*responses.GetPageTextStructuredChar{
		pdwChar("RIGHT", 30, 38, 0, 10),
		pdwChar("LEFT", 0, 8, 0, 10),
	}
	tokens, _, groups := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
	if groups != 2 {
		t.Fatalf("%d group(s), want 2 (the gap is 2.2 line heights, far past splitGap)", groups)
	}
	if got := pdwTexts(tokens); !slices.Equal(got, []string{"RIGHT", "LEFT"}) {
		t.Errorf("tokens = %v, want [RIGHT LEFT] -- emission must follow first-member stream index, not left-to-right", got)
	}
}

// The two hand-rolled insertion sorts (pdfiumSortRects, pdfiumSortWordsByFirst) exist because
// pdfium.go may not import sort or slices. Their edge cases have no other oracle.
func TestPDFiumWords_DegenerateInputIsHandled(t *testing.T) {
	t.Run("nil_and_empty", func(t *testing.T) {
		for _, rects := range [][]*responses.GetPageTextStructuredRect{nil, {}} {
			tokens, _, groups := extraction.PDFiumWordsForTest(rects, nil, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
			if len(tokens) != 0 || groups != 0 {
				t.Errorf("%d token(s) / %d group(s) from an empty page, want 0 / 0", len(tokens), groups)
			}
		}
	})

	t.Run("a_nil_or_empty_rect_never_reaches_grouping", func(t *testing.T) {
		rects := []*responses.GetPageTextStructuredRect{nil, pdwRect("", 0, 10, 0, 10), pdwRect("ONLY", 0, 10, 0, 10)}
		chars := []*responses.GetPageTextStructuredChar{pdwChar("ONLY", 0, 10, 0, 10)}
		tokens, _, groups := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
		if groups != 1 || len(tokens) != 1 || tokens[0].Text != "ONLY" {
			t.Errorf("%d group(s), tokens = %v, want 1 group reading [ONLY]", groups, pdwTexts(tokens))
		}
	})

	t.Run("equal_sort_keys_keep_stream_order", func(t *testing.T) {
		box := [4]float64{0, 10, 0, 10}
		rects := []*responses.GetPageTextStructuredRect{
			pdwRect("X", box[0], box[1], box[2], box[3]),
			pdwRect("Y", box[0], box[1], box[2], box[3]),
			pdwRect("Z", box[0], box[1], box[2], box[3]),
		}
		chars := []*responses.GetPageTextStructuredChar{
			pdwChar("X", 0, 10, 0, 10), pdwChar("Y", 0, 10, 0, 10), pdwChar("Z", 0, 10, 0, 10),
		}
		first, _, groups := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
		if groups != 1 {
			t.Fatalf("%d group(s) from three identical boxes, want 1", groups)
		}
		if got := pdwTexts(first); !slices.Equal(got, []string{"XYZ"}) {
			t.Errorf("tokens = %v, want [XYZ] -- three identical boxes must collapse in stream order", got)
		}
		second, _, _ := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
		if !slices.Equal(pdwTexts(first), pdwTexts(second)) {
			t.Errorf("two runs over equal sort keys disagree: %v vs %v -- the comparator is not a total order", pdwTexts(first), pdwTexts(second))
		}
	})

	t.Run("a_mutually_overlapping_line_is_one_fragment", func(t *testing.T) {
		rects := []*responses.GetPageTextStructuredRect{
			pdwRect("AB", 0, 10, 0, 10), pdwRect("CD", 5, 15, 0, 10), pdwRect("EF", 8, 20, 0, 10),
		}
		chars := []*responses.GetPageTextStructuredChar{
			pdwChar("AB", 0, 10, 0, 10), pdwChar("CD", 5, 15, 0, 10), pdwChar("EF", 8, 20, 0, 10),
		}
		if gaps := extraction.PDFiumGapsForTest(rects, chars); len(gaps) != 0 {
			t.Errorf("%d gap(s) across a mutually overlapping line, want 0 (one fragment)", len(gaps))
		}
		tokens, _, groups := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
		if groups != 1 || len(tokens) != 1 || tokens[0].Text != "ABCDEF" {
			t.Errorf("%d group(s), tokens = %v, want 1 reading [ABCDEF]", groups, pdwTexts(tokens))
		}
	})

	// An inverted rect (Top < Bottom) can never join a line -- the overlap test is strictly
	// negative against every box including its own twin -- so no union box is ever inverted.
	// Each inverted rect still emits its own token rather than vanishing.
	t.Run("inverted_boxes_still_emit_their_own_text", func(t *testing.T) {
		rects := []*responses.GetPageTextStructuredRect{
			pdwRect("UP", 0, 10, 10, 0), pdwRect("DOWN", 6, 16, 10, 0),
		}
		tokens, _, groups := extraction.PDFiumWordsForTest(rects, nil, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
		if groups != 2 {
			t.Fatalf("%d group(s) from two inverted boxes, want 2 (an inverted box overlaps nothing)", groups)
		}
		if got := pdwTexts(tokens); !slices.Equal(got, []string{"UP", "DOWN"}) {
			t.Errorf("tokens = %v, want [UP DOWN] -- an inverted box must not swallow or drop text", got)
		}
	})
}

// AC-4 companion to _AnEmptyCharsInFallsBackToTheRectText: the fallback must fire on a chars
// slice that is present but misses the union box -- the genuine-bug shape, not just a nil slice.
func TestPDFiumWords_CharsThatMissTheUnionBoxStillFallBack(t *testing.T) {
	rects := []*responses.GetPageTextStructuredRect{
		pdwRect("AB", 0, 10, 0, 10), pdwRect("CD", 6, 16, 0, 10),
	}
	chars := []*responses.GetPageTextStructuredChar{pdwChar("ZZ", 0, 10, 90, 100)} // another line entirely

	tokens, _, groups := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, extraction.PdfiumSplitGapForTest)
	if groups != 1 || len(tokens) != 1 {
		t.Fatalf("%d group(s) / %d token(s), want 1 / 1", groups, len(tokens))
	}
	if tokens[0].Text != "AB" {
		t.Errorf("merged token text = %q, want %q -- charsIn returned nothing and the fallback did not fire", tokens[0].Text, "AB")
	}
}

// Section D records the fallback as dead code on the corpus. That claim only holds if charsIn
// returns something for every merged token on the two fixtures whose groups are all multi-rect.
func TestPDFiumWords_TheFallbackIsDeadOnTheChromeFixtures(t *testing.T) {
	for _, name := range []string{chrRegister, chrRegisterTwin} {
		t.Run(name, func(t *testing.T) {
			checked := 0
			for _, p := range pdcStructured(t, name) {
				tokens, _, _ := extraction.PDFiumWordsForTest(p.Rects, p.Chars, p.Number, p.WidthPt, p.HeightPt, extraction.PdfiumSplitGapForTest)
				for _, tok := range tokens {
					box := responses.CharPosition{
						Left:   tok.Region.X0 * p.WidthPt,
						Right:  tok.Region.X1 * p.WidthPt,
						Top:    p.HeightPt - tok.Region.Y0*p.HeightPt,
						Bottom: p.HeightPt - tok.Region.Y1*p.HeightPt,
					}
					if extraction.CharsInForTest(p.Chars, box) == "" {
						t.Errorf("token %q: charsIn over its own box returns %q -- the fallback fired here, so it is not dead code", tok.Text, "")
					}
					checked++
				}
			}
			if checked == 0 {
				t.Errorf("no merged token checked on %s", name)
			}
		})
	}
}

// --- AC-3: the window recorded in docs/extraction-corpus.md ---------------------------------

// AC-3. The doc must carry the live-measured floor, ceiling, shipped constant and window width,
// each formatted to the same precision the window test itself uses, plus the fixtures, the two
// tokens the ceiling sits between, and the residual name the window's narrowness earns.
func TestCorpusDoc_RecordsTheSplitGapWindow(t *testing.T) {
	floor := pdwFloor(t)
	ceiling, ceilFixture, ceilLeft, ceilRight := pdwCeiling(t)
	if ceiling <= floor {
		t.Fatalf("ceiling %.17g is not above floor %.17g -- the window is empty", ceiling, floor)
	}
	width := ceiling - floor

	const wantCeilFixture = "wild_ruled_lines_totals.pdf"
	if ceilFixture != wantCeilFixture {
		t.Fatalf("pdwCeiling's source fixture is %s, want %s -- the doc below would name the wrong one", ceilFixture, wantCeilFixture)
	}
	if got := strings.TrimSpace(ceilLeft); got != "500.00" {
		t.Fatalf("pdwCeiling's left token is %q, want \"500.00\"", ceilLeft)
	}
	if got := strings.TrimSpace(ceilRight); got != "Total" {
		t.Fatalf("pdwCeiling's right token is %q, want \"Total\"", ceilRight)
	}

	doc := acRepoFile(t, acDoc)
	section := acDocSectionText(t, doc, chrDocHeading)

	fmt6 := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	for _, needle := range []string{
		fmt6(floor),
		fmt6(ceiling),
		fmt6(extraction.PdfiumSplitGapForTest),
		fmt6(width),
		chrRegister,
		"Three sittings,",
		"Lagos tax office",
		wantCeilFixture,
		"500.00",
		"Total",
		"[splitgap-window-is-narrow]",
	} {
		if !strings.Contains(section, needle) {
			t.Errorf("%s's %q section never says %q", acDoc, chrDocHeading, needle)
		}
	}
}
