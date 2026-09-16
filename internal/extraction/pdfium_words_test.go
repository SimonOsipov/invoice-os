// pdfium_words_test.go: EXTR-36-03's word stage -- lines -> fragments -> words -- and its
// measured splitGap window. pdfiumWords/pdfiumFragmentGaps do not exist yet, so every
// PDFiumWordsForTest/PDFiumGapsForTest call is compile-red until pdfium.go adds them (subtask
// 03's own merge). Same convention as pdfium_chars_test.go's charsIn note.
package extraction_test

import (
	"maps"
	"math"
	"slices"
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
// across the 27 generator-built fixtures.
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
// non-overlapping repeated glyph must not be swallowed into it. splitGap 0 disables stage 3
// entirely, isolating stage 2's own predicate.
func TestPDFiumWords_AnOverlappingRunIsOneFragment(t *testing.T) {
	rect := func(text string, left, right float64) *responses.GetPageTextStructuredRect {
		return &responses.GetPageTextStructuredRect{Text: text, PointPosition: responses.CharPosition{Left: left, Right: right, Top: 10, Bottom: 0}}
	}
	char := func(text string, left, right float64) *responses.GetPageTextStructuredChar {
		return &responses.GetPageTextStructuredChar{Text: text, PointPosition: responses.CharPosition{Left: left, Right: right, Top: 10, Bottom: 0}}
	}
	rects := []*responses.GetPageTextStructuredRect{rect("K", 0, 8), rect("W", 6, 14), rect("R", 30, 38), rect("R", 40, 48)}
	chars := []*responses.GetPageTextStructuredChar{char("K", 0, 8), char("W", 6, 14), char("R", 30, 38), char("R", 40, 48)}

	tokens, _, groups := extraction.PDFiumWordsForTest(rects, chars, 1, 1000, 1000, 0)
	if groups != 3 {
		t.Fatalf("%d group(s), want 3 (K+W merged, then two separate R's)", groups)
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

// AC-4/AC-12 control: on the 27 every group is single-rect, so the merged slice must be
// byte-identical to today's, index for index.
func TestPDFiumWords_ASingleRectTokenIsUnchanged(t *testing.T) {
	names := pdcNonChromeFixtures(t)
	if len(names) != 27 {
		t.Fatalf("pdcNonChromeFixtures returned %d name(s), want exactly 27", len(names))
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			pages, _ := ptRead(t, name)
			before := ptTokens(pages)

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
}

// AC-12 IS WRONG in the story (see architecture note H): under the relative rule at 0.60, zero
// of the 27 generator-built fixtures move. Only the two Chrome fixtures do, and each collapses to
// exactly advisory_register.pdf's own word-level count -- the two fixtures become one layout.
func TestPDFiumWords_OnlyTheTwoChromeFixturesMove(t *testing.T) {
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
			beforePages, _ := ptRead(t, name)
			before := len(ptTokens(beforePages))

			after, _, groups := pdwMerged(t, name, extraction.PdfiumSplitGapForTest)
			if groups != len(after) {
				t.Fatalf("%d group(s), %d token(s) -- one token per group must hold", groups, len(after))
			}

			switch name {
			case chrRegister, chrRegisterTwin:
				if len(after) != wantChromeAfter {
					t.Errorf("%s: merged token count = %d, want %d (advisory_register.pdf's own word-level count)", name, len(after), wantChromeAfter)
				}
				if len(after) == before {
					t.Errorf("%s: merged token count unchanged at %d -- the merge did nothing", name, before)
				}
			default:
				if len(after) != before {
					t.Errorf("%s: merged token count moved %d -> %d, want unchanged", name, before, len(after))
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
