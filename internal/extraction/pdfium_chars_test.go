// pdfium_chars_test.go: EXTR-36-02's char-sourcing specs. External package, never
// pdfium_internal_test.go -- reading a fixture builds the pdfium pool, and
// TestPDFiumPool_NotBuiltOnACancelledContext (pdfium_pool_internal_test.go) fatals if an
// internal test gets there first (export_test.go:96-99's convention).
//
// charsIn does not exist yet, so every CharsInForTest call is compile-red ("undefined: charsIn")
// until pdfium.go adds it. That breaks the whole package's test binary, not just these tests --
// expected for a Go red commit, not a defect in the tests themselves.
package extraction_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/klippa-app/go-pdfium/responses"
)

// pdcMinTotalRects floors the corpus-wide loops below: measured 2144 rects across 29 fixtures
// at HEAD 83099dd9 (re-derive with a throwaway probe over testdata/*.pdf if this ever moves).
const pdcMinTotalRects = 2000

// pdcMinNonChromeRects floors the 27-fixture loop (2144 total minus chrome_register.pdf's 802
// and chrome_register_twin.pdf's 800, measured the same way).
const pdcMinNonChromeRects = 500

// pdcAdvisoryRegisterMinRects: advisory_register.pdf carries 42 rects across its 2 pages,
// measured directly -- not the 39 the story's Test Specs table names, which does not match.
const pdcAdvisoryRegisterMinRects = 42

// --- harness ----------------------------------------------------------------

// pdcStructured reads one fixture's raw rects and chars at ModeBoth.
func pdcStructured(t *testing.T, name string) []extraction.PDFiumStructuredPageForTest {
	t.Helper()
	pages, err := extraction.PDFiumStructuredTextForTest(t.Context(), ptDoc(t, name), "both")
	if err != nil {
		t.Fatalf("PDFiumStructuredTextForTest(%s, both): %v", name, err)
	}
	if len(pages) == 0 {
		t.Fatalf("%s yielded no page", name)
	}
	return pages
}

// pdcPage1 selects the page numbered 1, not slice position 0.
func pdcPage1(t *testing.T, pages []extraction.PDFiumStructuredPageForTest) extraction.PDFiumStructuredPageForTest {
	t.Helper()
	for _, p := range pages {
		if p.Number == 1 {
			return p
		}
	}
	t.Fatalf("no page numbered 1 among %d page(s)", len(pages))
	return extraction.PDFiumStructuredPageForTest{}
}

// pdcNonChromeFixtures is every committed fixture except the two Chrome ones, read off
// fingerprintGoldens' own key set rather than a second hard-coded list (fingerprint_goldens_test.go).
func pdcNonChromeFixtures(t *testing.T) []string {
	t.Helper()
	if len(fingerprintGoldens) < fgGoldenFloor {
		t.Fatalf("fingerprintGoldens holds %d row(s), want at least %d", len(fingerprintGoldens), fgGoldenFloor)
	}
	var out []string
	for name := range fingerprintGoldens {
		if !fgChromeNames[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// pdcOverlapRuns groups rects, in stream order, into maximal runs whose consecutive boxes both
// X- and Y-overlap -- the bleed shape a merged token's union box will span (subtask 03). Located
// by predicate over the boxes, never by a hard-coded index: a fixture regeneration renumbers
// rects freely.
func pdcOverlapRuns(rects []*responses.GetPageTextStructuredRect) [][]int {
	var runs [][]int
	i := 0
	for i < len(rects) {
		j := i
		for j+1 < len(rects) {
			a, b := rects[j].PointPosition, rects[j+1].PointPosition
			xOverlap := a.Left < b.Right && b.Left < a.Right
			yOverlap := a.Bottom < b.Top && b.Bottom < a.Top
			if !xOverlap || !yOverlap {
				break
			}
			j++
		}
		if j > i {
			run := make([]int, j-i+1)
			for k := range run {
				run[k] = i + k
			}
			runs = append(runs, run)
		}
		i = j + 1
	}
	return runs
}

func pdcNaiveText(rects []*responses.GetPageTextStructuredRect, idx []int) string {
	var b strings.Builder
	for _, i := range idx {
		b.WriteString(rects[i].Text)
	}
	return b.String()
}

func pdcUnionBox(rects []*responses.GetPageTextStructuredRect, idx []int) responses.CharPosition {
	box := rects[idx[0]].PointPosition
	for _, i := range idx[1:] {
		p := rects[i].PointPosition
		if p.Left < box.Left {
			box.Left = p.Left
		}
		if p.Right > box.Right {
			box.Right = p.Right
		}
		if p.Top > box.Top {
			box.Top = p.Top
		}
		if p.Bottom < box.Bottom {
			box.Bottom = p.Bottom
		}
	}
	return box
}

// --- AC-1's oracle: ModeBoth reproduces ModeRects' own rects, and only ModeBoth carries chars --

// TestPDFiumText_CharsExistOnlyAtModeBoth is the direct evidence for rule 2: the "chars exist
// wherever rects do" predicate is false at ModeRects and true at ModeBoth, on the same document
// -- proof the oracle below is sensitive to mode rather than passing regardless of it.
func TestPDFiumText_CharsExistOnlyAtModeBoth(t *testing.T) {
	doc := ptDoc(t, fxNative)

	rectMode, err := extraction.PDFiumStructuredTextForTest(t.Context(), doc, "rect")
	if err != nil {
		t.Fatalf("PDFiumStructuredTextForTest(rect): %v", err)
	}
	bothMode, err := extraction.PDFiumStructuredTextForTest(t.Context(), doc, "both")
	if err != nil {
		t.Fatalf("PDFiumStructuredTextForTest(both): %v", err)
	}

	page1Rect, page1Both := pdcPage1(t, rectMode), pdcPage1(t, bothMode)
	if len(page1Rect.Rects) == 0 {
		t.Fatalf("%s page 1 carries no rects; the predicate below would be vacuous", fxNative)
	}
	if len(page1Both.Rects) != len(page1Rect.Rects) {
		t.Fatalf("%s page 1 rect count moved between modes: %d (rect) vs %d (both)", fxNative, len(page1Rect.Rects), len(page1Both.Rects))
	}

	if len(page1Rect.Chars) != 0 {
		t.Fatalf("%s page 1 carries %d char(s) at ModeRects, want 0 -- ModeRects never collects chars", fxNative, len(page1Rect.Chars))
	}
	if redHolds := len(page1Rect.Rects) > 0 && len(page1Rect.Chars) > 0; redHolds {
		t.Errorf("RED: the chars-wherever-rects predicate holds at ModeRects (%d rect(s), %d char(s)); it must fail there to be evidence of anything", len(page1Rect.Rects), len(page1Rect.Chars))
	} else {
		t.Logf("RED confirmed at ModeRects: %d rect(s), 0 char(s)", len(page1Rect.Rects))
	}

	if greenHolds := len(page1Both.Rects) > 0 && len(page1Both.Chars) > 0; !greenHolds {
		t.Errorf("GREEN failed at ModeBoth: %d rect(s), %d char(s), want chars > 0", len(page1Both.Rects), len(page1Both.Chars))
	} else {
		t.Logf("GREEN confirmed at ModeBoth: %d rect(s), %d char(s)", len(page1Both.Rects), len(page1Both.Chars))
	}
}

// TestPDFiumText_ModeBothReturnsTheSameRects: AC-1's main control. Every committed fixture,
// every page, read twice through the same request shape Read issues -- once ModeRects, once
// ModeBoth -- asserting equal rects and that only ModeBoth carries chars. Reds if a future
// change ever reverts the rects themselves, which pinning a mode constant alone cannot catch.
func TestPDFiumText_ModeBothReturnsTheSameRects(t *testing.T) {
	names := slices.Sorted(maps.Keys(fingerprintGoldens))
	if len(names) < fgGoldenFloor {
		t.Fatalf("fingerprintGoldens holds %d row(s), want at least %d", len(names), fgGoldenFloor)
	}

	totalRects := 0
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			doc := ptDoc(t, name)
			rectPages, err := extraction.PDFiumStructuredTextForTest(t.Context(), doc, "rect")
			if err != nil {
				t.Fatalf("PDFiumStructuredTextForTest(rect): %v", err)
			}
			bothPages, err := extraction.PDFiumStructuredTextForTest(t.Context(), doc, "both")
			if err != nil {
				t.Fatalf("PDFiumStructuredTextForTest(both): %v", err)
			}
			if len(rectPages) != len(bothPages) {
				t.Fatalf("ModeRects yielded %d page(s), ModeBoth %d", len(rectPages), len(bothPages))
			}

			for i := range rectPages {
				rp, bp := rectPages[i], bothPages[i]
				if rp.Number != bp.Number {
					t.Fatalf("page index %d: ModeRects reports page %d, ModeBoth page %d", i, rp.Number, bp.Number)
				}
				if len(rp.Rects) != len(bp.Rects) {
					t.Fatalf("page %d: ModeRects %d rect(s), ModeBoth %d", rp.Number, len(rp.Rects), len(bp.Rects))
				}
				totalRects += len(rp.Rects)

				for j := range rp.Rects {
					if rp.Rects[j].Text != bp.Rects[j].Text {
						t.Errorf("page %d rect %d: text %q (ModeRects) vs %q (ModeBoth)", rp.Number, j, rp.Rects[j].Text, bp.Rects[j].Text)
					}
					if rp.Rects[j].PointPosition != bp.Rects[j].PointPosition {
						t.Errorf("page %d rect %d: box %+v (ModeRects) vs %+v (ModeBoth)", rp.Number, j, rp.Rects[j].PointPosition, bp.Rects[j].PointPosition)
					}
				}

				if len(rp.Chars) != 0 {
					t.Errorf("page %d: ModeRects reported %d char(s), want 0", rp.Number, len(rp.Chars))
				}
				if len(bp.Rects) > 0 && len(bp.Chars) == 0 {
					t.Errorf("page %d: %d rect(s) but 0 chars at ModeBoth", rp.Number, len(bp.Rects))
				}
			}
		})
	}

	if totalRects < pdcMinTotalRects {
		t.Fatalf("scanned %d rect(s) across %d fixture(s), want at least %d -- the loops above would have passed vacuously", totalRects, len(names), pdcMinTotalRects)
	}
}

// TestPDFiumReader_ProductionCallSiteRequestsModeBoth closes a gap the control above cannot:
// PDFiumStructuredTextForTest takes an explicit mode argument, so it reads ModeBoth regardless of
// what Read's own literal passes -- PDFium honours ModeBoth either way. This parses pdfium.go and
// asserts Read's own GetPageTextStructured{...} sets Mode: requests.GetPageTextStructuredModeBoth,
// so reverting that one line -- and nothing else -- fails here.
func TestPDFiumReader_ProductionCallSiteRequestsModeBoth(t *testing.T) {
	const file = "pdfium.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	// isGetPageTextStructured reports whether call is GetPageTextStructured(&T{...}) and, if so,
	// returns that composite literal's Mode field value.
	isGetPageTextStructured := func(call *ast.CallExpr) (ast.Expr, bool) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "GetPageTextStructured" || len(call.Args) != 1 {
			return nil, false
		}
		unary, ok := call.Args[0].(*ast.UnaryExpr)
		if !ok || unary.Op != token.AND {
			return nil, false
		}
		lit, ok := unary.X.(*ast.CompositeLit)
		if !ok {
			return nil, false
		}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Mode" {
					return kv.Value, true
				}
			}
		}
		return nil, true
	}

	// Every such call anywhere in the file: closes the gap where a second call, inside or
	// outside Read, would silently keep only the last match.
	var everywhere []ast.Expr
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if mode, ok := isGetPageTextStructured(call); ok {
			everywhere = append(everywhere, mode)
		}
		return true
	})
	if len(everywhere) != 1 {
		t.Fatalf("%s: found %d GetPageTextStructured(...) call(s), want exactly 1 (all reads must share Read's own ModeBoth call)", file, len(everywhere))
	}

	var modeExpr ast.Expr
	var inRead bool
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Read" || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if mode, found := isGetPageTextStructured(call); found {
				inRead = true
				modeExpr = mode
				return false
			}
			return true
		})
	}

	if !inRead {
		t.Fatalf("%s: the sole GetPageTextStructured(...) call is not inside PDFiumReader.Read", file)
	}
	if modeExpr == nil {
		t.Fatalf("%s: Read's GetPageTextStructured{...} call sets no Mode field", file)
	}
	sel, ok := modeExpr.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("%s: Read's GetPageTextStructured.Mode is %s, want requests.GetPageTextStructuredModeBoth", file, ptcRender(modeExpr))
	}
	if pkg, isIdent := sel.X.(*ast.Ident); !isIdent || pkg.Name != "requests" || sel.Sel.Name != "GetPageTextStructuredModeBoth" {
		t.Errorf("%s: Read's GetPageTextStructured.Mode is %s, want requests.GetPageTextStructuredModeBoth", file, ptcRender(modeExpr))
	}
}

// ptcRender renders a Mode expression for a failure message. Only Ident and SelectorExpr occur
// in a Mode field's value on this call site, so the fallback is a type name, not a general printer.
func ptcRender(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok {
			return pkg.Name + "." + x.Sel.Name
		}
	}
	return fmt.Sprintf("%T", e)
}

// --- AC-3: charsIn reproduces every rect's own text on a word-level page --------------------

// TestCharsIn_ReproducesEveryRectTextOnAWordLevelPage: charsIn(rect.box) must equal rect.Text
// once both sides are trimmed of a trailing space -- pdfium puts a trailing space's glyph box
// past the rect's own right edge, so no containment rule recovers it on either side.
func TestCharsIn_ReproducesEveryRectTextOnAWordLevelPage(t *testing.T) {
	pages := pdcStructured(t, fxAdvisoryRegister)

	total := 0
	for _, p := range pages {
		if len(p.Chars) == 0 {
			t.Fatalf("%s page %d carries %d rect(s) but 0 chars at ModeBoth", fxAdvisoryRegister, p.Number, len(p.Rects))
		}
		for i, r := range p.Rects {
			total++
			got := strings.TrimRight(extraction.CharsInForTest(p.Chars, r.PointPosition), " ")
			want := strings.TrimRight(r.Text, " ")
			if got != want {
				t.Errorf("%s page %d rect %d: charsIn(rect box) = %q, want %q (rect.Text trimmed)", fxAdvisoryRegister, p.Number, i, got, want)
			}
		}
	}
	if total < pdcAdvisoryRegisterMinRects {
		t.Fatalf("%s carries %d rect(s), want at least %d -- the loop above would have checked too little", fxAdvisoryRegister, total, pdcAdvisoryRegisterMinRects)
	}
}

// TestCharsIn_ReproducesEveryRectTextOnTheCorpus widens the no-op control to every committed
// fixture except the two Chrome ones (27 files): the loop is identical and covers 27 for free.
func TestCharsIn_ReproducesEveryRectTextOnTheCorpus(t *testing.T) {
	names := pdcNonChromeFixtures(t)
	if len(names) != 27 {
		t.Fatalf("pdcNonChromeFixtures returned %d name(s), want exactly 27 (29 committed minus %s and %s)", len(names), chrRegister, chrRegisterTwin)
	}

	totalRects := 0
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			pages := pdcStructured(t, name)
			for _, p := range pages {
				if len(p.Rects) == 0 {
					continue
				}
				if len(p.Chars) == 0 {
					t.Fatalf("%s page %d carries %d rect(s) but 0 chars at ModeBoth", name, p.Number, len(p.Rects))
				}
				for i, r := range p.Rects {
					totalRects++
					got := strings.TrimRight(extraction.CharsInForTest(p.Chars, r.PointPosition), " ")
					want := strings.TrimRight(r.Text, " ")
					if got != want {
						t.Errorf("%s page %d rect %d: charsIn(rect box) = %q, want %q (rect.Text trimmed)", name, p.Number, i, got, want)
					}
				}
			}
		})
	}
	if totalRects < pdcMinNonChromeRects {
		t.Fatalf("scanned %d rect(s) across %d fixture(s), want at least %d", totalRects, len(names), pdcMinNonChromeRects)
	}
}

// --- AC-4: a bled run reads once, not once per glyph -----------------------------------------

// TestCharsIn_ABledPairReadsOnce: chrome_register.pdf page 1 carries a two-rect bled run whose
// rects both read "KW" (naive concatenation "KWKW"); charsIn(union) must read "KW" once.
func TestCharsIn_ABledPairReadsOnce(t *testing.T) {
	page1 := pdcPage1(t, pdcStructured(t, chrRegister))
	runs := pdcOverlapRuns(page1.Rects)
	if len(runs) == 0 {
		t.Fatalf("no overlap run found on %s page 1", chrRegister)
	}

	found := false
	for _, run := range runs {
		if len(run) != 2 {
			continue
		}
		if naive := pdcNaiveText(page1.Rects, run); naive != "KWKW" {
			continue
		}
		found = true
		got := extraction.CharsInForTest(page1.Chars, pdcUnionBox(page1.Rects, run))
		if got != "KW" {
			t.Errorf("charsIn(union of rects %v) = %q, want %q -- naive concatenation reads %q", run, got, "KW", "KWKW")
		}
	}
	if !found {
		t.Fatalf("no 2-rect overlap run on %s page 1 naively reads %q", chrRegister, "KWKW")
	}
}

// TestCharsIn_ASlidingRunReadsOnce: the same page carries a three-rect bled run reading
// "VA"/"VAT "/"AT " (naive concatenation "VAVAT AT "); charsIn(union) must read "VAT".
func TestCharsIn_ASlidingRunReadsOnce(t *testing.T) {
	page1 := pdcPage1(t, pdcStructured(t, chrRegister))
	runs := pdcOverlapRuns(page1.Rects)
	if len(runs) == 0 {
		t.Fatalf("no overlap run found on %s page 1", chrRegister)
	}

	found := false
	for _, run := range runs {
		if len(run) != 3 {
			continue
		}
		if naive := pdcNaiveText(page1.Rects, run); naive != "VAVAT AT " {
			continue
		}
		found = true
		got := extraction.CharsInForTest(page1.Chars, pdcUnionBox(page1.Rects, run))
		if got != "VAT" {
			t.Errorf("charsIn(union of rects %v) = %q, want %q -- naive concatenation reads %q", run, got, "VAT", "VAVAT AT ")
		}
	}
	if !found {
		t.Fatalf("no 3-rect overlap run on %s page 1 naively reads %q", chrRegister, "VAVAT AT ")
	}
}

// --- AC-5: TextChars is unmoved -------------------------------------------------------------

// pdcTextCharsGoldens pins (pages, pagesWithText, textChars) per fixture on today's reader.
// pdfiumTokens' non-whitespace char count is unaffected by the mode flip or by charsIn existing
// -- neither is wired into it in this subtask -- so none of these 29 rows may move.
var pdcTextCharsGoldens = map[string]struct{ pages, withText, textChars int }{
	"advisory_dense.pdf":                    {1, 1, 1766},
	"advisory_register.pdf":                 {2, 2, 804},
	"advisory_register_unspaced.pdf":        {2, 2, 804},
	"chrome_register.pdf":                   {2, 2, 808},
	"chrome_register_twin.pdf":              {2, 2, 806},
	"corpus_ambiguous_date.pdf":             {1, 1, 119},
	"corpus_inline_labels.pdf":              {1, 1, 197},
	"corpus_split_labels.pdf":               {1, 1, 188},
	"corpus_stacked_labels.pdf":             {1, 1, 134},
	"corpus_totals_block.pdf":               {1, 1, 89},
	"corpus_two_column.pdf":                 {1, 1, 146},
	"dense_invoice.pdf":                     {1, 0, 0},
	"hybrid_invoice.pdf":                    {2, 1, 41},
	"learned_two_party.pdf":                 {1, 1, 138},
	"learned_typed_total.pdf":               {1, 1, 130},
	"learned_typed_total_twin.pdf":          {1, 1, 131},
	"native_3page.pdf":                      {3, 3, 96},
	"native_invoice.pdf":                    {1, 1, 41},
	"rich_invoice.pdf":                      {1, 1, 247},
	"scanned_invoice.pdf":                   {1, 0, 0},
	"table_invoice.pdf":                     {1, 1, 74},
	"wild_rc_due_naira.pdf":                 {1, 1, 218},
	"wild_ruled_lines_totals.pdf":           {1, 1, 315},
	"wild_ruled_lines_totals_asprinted.pdf": {1, 1, 341},
	"wild_scanned_no_number.pdf":            {1, 0, 0},
	"wild_stacked_borderless.pdf":           {1, 1, 185},
	"wild_stacked_borderless_asprinted.pdf": {1, 1, 197},
	"wild_two_party_bare_tin.pdf":           {1, 1, 202},
	"wild_two_party_bare_tin_asprinted.pdf": {1, 1, 225},
}

func TestPDFiumReader_TextCharsUnchanged(t *testing.T) {
	if len(pdcTextCharsGoldens) < fgGoldenFloor {
		t.Fatalf("pdcTextCharsGoldens holds %d row(s), want at least %d", len(pdcTextCharsGoldens), fgGoldenFloor)
	}

	for _, name := range slices.Sorted(maps.Keys(pdcTextCharsGoldens)) {
		t.Run(name, func(t *testing.T) {
			want := pdcTextCharsGoldens[name]
			_, res := ptRead(t, name)
			if res.Pages != want.pages || res.PagesWithText != want.withText || res.TextChars != want.textChars {
				t.Errorf("Read reported pages=%d withText=%d textChars=%d, want pages=%d withText=%d textChars=%d",
					res.Pages, res.PagesWithText, res.TextChars, want.pages, want.withText, want.textChars)
			}
		})
	}
}

// --- AC-6: one token per rect, on the word-level fixtures only ------------------------------

// AC-6 (re-pointed for EXTR-36-03): the merge collapses per-glyph rects into words, so "one
// token per rect" only holds on the 27 word-level fixtures now. chrome_register.pdf's 802 rects
// merge to 42 tokens, the twin's 800 to 42 as well -- each checked against its own rect count,
// not against the other's.
func TestPDFiumTokens_OneTokenPerRectOnEveryWordLevelFixture(t *testing.T) {
	names := pdcNonChromeFixtures(t)
	if len(names) != 27 {
		t.Fatalf("pdcNonChromeFixtures returned %d name(s), want exactly 27", len(names))
	}

	totalTokens := 0
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			pages, _ := ptRead(t, name)
			tokens := len(ptTokens(pages))

			rectPages, err := extraction.PDFiumStructuredTextForTest(t.Context(), ptDoc(t, name), "both")
			if err != nil {
				t.Fatalf("PDFiumStructuredTextForTest(both): %v", err)
			}
			rects := 0
			for _, p := range rectPages {
				rects += len(p.Rects)
			}

			if tokens != rects {
				t.Errorf("%d token(s), %d rect(s) -- a word-level fixture must still emit one token per rect", tokens, rects)
			}
			totalTokens += tokens
		})
	}
	if totalTokens < pdcMinNonChromeRects {
		t.Fatalf("scanned %d token(s) across %d fixture(s), want at least %d", totalTokens, len(names), pdcMinNonChromeRects)
	}

	advisoryPages, _ := ptRead(t, fxAdvisoryRegister)
	wantChromeTokens := len(ptTokens(advisoryPages))
	if wantChromeTokens == 0 {
		t.Fatalf("%s carries no token; the Chrome comparison below would be vacuous", fxAdvisoryRegister)
	}

	for _, name := range []string{chrRegister, chrRegisterTwin} {
		t.Run(name, func(t *testing.T) {
			rectPages, err := extraction.PDFiumStructuredTextForTest(t.Context(), ptDoc(t, name), "both")
			if err != nil {
				t.Fatalf("PDFiumStructuredTextForTest(both): %v", err)
			}
			rects, tokens := 0, 0
			for _, p := range rectPages {
				rects += len(p.Rects)
				tk, _, _ := extraction.PDFiumWordsForTest(p.Rects, p.Chars, p.Number, p.WidthPt, p.HeightPt, extraction.PdfiumSplitGapForTest)
				tokens += len(tk)
			}
			if tokens != wantChromeTokens {
				t.Errorf("%s merges %d rect(s) to %d token(s), want %d (advisory_register.pdf's own word-level count)", name, rects, tokens, wantChromeTokens)
			}
		})
	}
}
