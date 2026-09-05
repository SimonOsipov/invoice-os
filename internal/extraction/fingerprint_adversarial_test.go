// fingerprint_adversarial_test.go: edge, boundary and negative cases beyond F-01..F-10.
package extraction_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// sha256("") behind the version prefix: the fingerprint of zero observations.
const emptyFingerprint = "v1:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// onePage fingerprints a single page-1 TokenPage built from tokens.
func onePage(tokens ...extraction.Token) string {
	return extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: tokens}})
}

// tok is a page-1 token at an explicit box.
func tok(text string, x0, y0, x1, y1 float64) extraction.Token {
	return extraction.Token{Text: text, Region: extraction.Region{Page: 1, X0: x0, Y0: y0, X1: x1, Y1: y1}}
}

// The band comes from the box's CENTRE X, not its left edge. Two tokens sharing X0 but not X1
// straddle the 1/3 boundary by centre and not by left edge, so a left-edge implementation
// passes every one of F-01..F-10 -- this is the only spec that separates them.
func TestFingerprint_BandsByCentreXNotLeftEdge(t *testing.T) {
	narrow := onePage(tok("Total", 0.10, 0.30, 0.20, 0.32)) // centre 0.15 -> band 0
	wide := onePage(tok("Total", 0.10, 0.30, 0.98, 0.32))   // centre 0.54 -> band 1

	if len(narrow) != 67 || len(wide) != 67 {
		t.Fatalf("Fingerprint(narrow) = %q, Fingerprint(wide) = %q, want 67 bytes each", narrow, wide)
	}
	if narrow == wide {
		t.Errorf("Fingerprint(X0 0.10 X1 0.20) = %q, Fingerprint(X0 0.10 X1 0.98) = %q, want different: "+
			"the two share a left edge but their centres sit in different thirds", narrow, wide)
	}

	// Positive half: the wide token bands with a box whose centre matches it, not with one whose
	// left edge matches it.
	if middle := onePage(tok("Total", 0.45, 0.30, 0.63, 0.32)); wide != middle { // centre 0.54
		t.Errorf("Fingerprint(wide, centre 0.54) = %q, Fingerprint(middle, centre 0.54) = %q, want equal", wide, middle)
	}
}

// A coordinate outside [0,1] or an inverted box must land in a band, not panic: columnBand is
// two ordered comparisons with a default, so there is no arithmetic that can fail.
func TestFingerprint_OutOfRangeAndInvertedBoxesDegradeToAnEdgeBand(t *testing.T) {
	left := onePage(tok("Total", 0.05, 0.30, 0.15, 0.32))   // centre 0.10 -> band 0
	right := onePage(tok("Total", 0.80, 0.30, 0.90, 0.32))  // centre 0.85 -> band 2
	middle := onePage(tok("Total", 0.45, 0.30, 0.55, 0.32)) // centre 0.50 -> band 1

	if len(left) != 67 {
		t.Fatalf("Fingerprint(in-range reference) = %q (%d bytes), want 67", left, len(left))
	}
	if left == right {
		t.Fatalf("band 0 and band 2 fingerprint alike (%q); the equalities below would prove nothing", left)
	}

	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"far negative centre (-4.0) bands left", onePage(tok("Total", -5.0, 0.30, -3.0, 0.32)), left},
		{"far positive centre (6.0) bands right", onePage(tok("Total", 5.0, 0.30, 7.0, 0.32)), right},
		{"inverted box X1 < X0, centre 0.50", onePage(tok("Total", 0.90, 0.30, 0.10, 0.32)), middle},
	} {
		if c.got != c.want {
			t.Errorf("%s: Fingerprint() = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// Fingerprint takes the FIRST TokenPage numbered 1 and stops. A duplicate page 1 later in the
// slice is ignored rather than merged, so the result never depends on how many the caller built.
func TestFingerprint_TakesTheFirstPageNumberedOne(t *testing.T) {
	first := extraction.TokenPage{Number: 1, Tokens: []extraction.Token{tok("Invoice No: INV-1", 0.05, 0.08, 0.35, 0.10)}}
	second := extraction.TokenPage{Number: 1, Tokens: []extraction.Token{tok("Currency: NGN", 0.05, 0.08, 0.35, 0.10)}}

	fpFirst := extraction.Fingerprint([]extraction.TokenPage{first})
	fpSecond := extraction.Fingerprint([]extraction.TokenPage{second})
	if len(fpFirst) != 67 {
		t.Fatalf("Fingerprint(first alone) = %q (%d bytes), want 67", fpFirst, len(fpFirst))
	}
	if fpFirst == fpSecond {
		t.Fatalf("the two page-1 fixtures fingerprint alike (%q); the assertions below would prove nothing", fpFirst)
	}

	if got := extraction.Fingerprint([]extraction.TokenPage{first, second}); got != fpFirst {
		t.Errorf("Fingerprint([first, second]) = %q, want %q: the first page numbered 1 wins", got, fpFirst)
	}
	if got := extraction.Fingerprint([]extraction.TokenPage{second, first}); got != fpSecond {
		t.Errorf("Fingerprint([second, first]) = %q, want %q: the first page numbered 1 wins", got, fpSecond)
	}
}

// A page 1 carrying no tokens and a document with no page 1 at all both yield zero observations,
// so both fingerprint to sha256(""). The two are deliberately indistinguishable: the fingerprint
// says which labels a layout shows, and neither shows any.
func TestFingerprint_EmptyPageOneMatchesNoPageOne(t *testing.T) {
	emptyPage1 := extraction.Fingerprint([]extraction.TokenPage{{Number: 1}})
	noPage1 := extraction.Fingerprint([]extraction.TokenPage{
		{Number: 7, Tokens: []extraction.Token{tok("Total: 15000.00", 0.05, 0.08, 0.35, 0.10)}},
	})

	if emptyPage1 != emptyFingerprint {
		t.Errorf("Fingerprint(page 1 with no tokens) = %q, want %q", emptyPage1, emptyFingerprint)
	}
	if noPage1 != emptyFingerprint {
		t.Errorf("Fingerprint(no page numbered 1) = %q, want %q", noPage1, emptyFingerprint)
	}
}

// A token matching several lexicon entries emits one observation per match, not a first-match
// winner: "Sub-total" is both a subtotal and a total, and must fingerprint the same as two
// tokens at the same box that match one label each.
func TestFingerprint_EmitsOneObservationPerLexiconMatch(t *testing.T) {
	both := onePage(tok("Sub-total", 0.10, 0.30, 0.30, 0.32))
	split := onePage(
		tok("Net Amount", 0.10, 0.30, 0.30, 0.32),  // subtotal only
		tok("Grand Total", 0.10, 0.30, 0.30, 0.32), // total only
	)
	totalOnly := onePage(tok("Grand Total", 0.10, 0.30, 0.30, 0.32))

	if len(both) != 67 {
		t.Fatalf(`Fingerprint("Sub-total") = %q (%d bytes), want 67`, both, len(both))
	}
	if both != split {
		t.Errorf(`Fingerprint("Sub-total") = %q, Fingerprint("Net Amount"+"Grand Total") = %q, want equal: `+
			`a double match must emit both labels`, both, split)
	}
	if both == totalOnly {
		t.Errorf(`Fingerprint("Sub-total") = %q, Fingerprint("Grand Total") = %q, want different: `+
			`the second match must not be dropped`, both, totalOnly)
	}
}

// Only the matched label id reaches the hash, so case and any non-label text in the same token
// are invisible. An accented look-alike is not a match at all.
func TestFingerprint_IgnoresCaseAndNonLabelText(t *testing.T) {
	plain := onePage(tok("Total", 0.10, 0.30, 0.30, 0.32))
	if plain == emptyFingerprint {
		t.Fatalf(`Fingerprint("Total") = %q, want a matched-label fingerprint; the equalities below would prove nothing`, plain)
	}

	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"upper case", onePage(tok("TOTAL: 15000", 0.10, 0.30, 0.30, 0.32)), plain},
		{"lower case", onePage(tok("total: 15000", 0.10, 0.30, 0.30, 0.32)), plain},
		{"unicode payload", onePage(tok("Total — 15 000,00 ₦", 0.10, 0.30, 0.30, 0.32)), plain},
		{"accented look-alike is no match", onePage(tok("Tótal: 5", 0.10, 0.30, 0.30, 0.32)), emptyFingerprint},
	} {
		if c.got != c.want {
			t.Errorf("%s: Fingerprint() = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// F-07 ties three observations, which sort.Slice handles with insertion sort. Above about a
// dozen elements it switches to pdqsort, whose pivot choice reads the input order -- so the
// order-independence claim needs a second run at that scale.
func TestFingerprint_IsDeterministicAtSortScale(t *testing.T) {
	const n = 3000

	base := make([]extraction.Token, 0, n)
	for i := 0; i < n; i++ {
		y := 0.0002 * float64(i%500) // 500 rows of 6, so every row is a 6-way tie
		// Width alternates across a row's six members, so each tie group holds both bands:
		// i/500, not i%2, because 500 is even and i%2 would make every member of a row alike.
		x1 := 0.20
		if (i/500)%2 == 1 {
			x1 = 0.98
		}
		base = append(base, tok("Total row", 0.10, y, x1, y+0.0001))
	}

	// Fixture self-check: the widths must really straddle a band boundary, or every tie group
	// is byte-identical and the orderings below agree no matter what the sort does.
	allNarrow := make([]extraction.Token, len(base))
	for i, tk := range base {
		allNarrow[i] = tok(tk.Text, tk.Region.X0, tk.Region.Y0, 0.20, tk.Region.Y1)
	}
	if extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: base}}) ==
		extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: allNarrow}}) {
		t.Fatal("fixture invalid: the two widths land in one band, so no tie group mixes bands")
	}

	orders := [][]extraction.Token{base, reversed(base), rotated(base, n/3)}

	var got []string
	for i, order := range orders {
		fp := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: order}})
		if len(fp) != 67 {
			t.Fatalf("order %d: Fingerprint(%d tokens) = %q (%d bytes), want 67", i, n, fp, len(fp))
		}
		got = append(got, fp)
	}
	for i := 1; i < len(got); i++ {
		t.Run(fmt.Sprintf("order-%d", i), func(t *testing.T) {
			if got[i] != got[0] {
				t.Errorf("Fingerprint(order 0) = %q, Fingerprint(order %d) = %q, want equal at %d tokens",
					got[0], i, got[i], n)
			}
		})
	}
}

func reversed(in []extraction.Token) []extraction.Token {
	out := make([]extraction.Token, len(in))
	for i, tk := range in {
		out[len(in)-1-i] = tk
	}
	return out
}

func rotated(in []extraction.Token, by int) []extraction.Token {
	out := make([]extraction.Token, 0, len(in))
	out = append(out, in[by:]...)
	return append(out, in[:by]...)
}

// CollectTokens appends to dst and never resets it: a caller may reuse one destination across
// several reads, and PageReader.Read calls the callback once per page.
func TestCollectTokens_AppendsAndNeverResetsTheDestination(t *testing.T) {
	pages := []extraction.TokenPage{{Number: 99}}

	page := func(n int) extraction.Page {
		return extraction.Page{
			Number:   n,
			WidthPt:  612,
			HeightPt: 792,
			Tokens:   []extraction.Token{tok(fmt.Sprintf("Total: page %d", n), 0.1, 0.1, 0.4, 0.12)},
		}
	}

	first := extraction.CollectTokens(&pages)
	for _, n := range []int{1, 2} {
		if err := first(page(n)); err != nil {
			t.Fatalf("onPage(page %d) error = %v, want nil", n, err)
		}
	}
	if err := extraction.CollectTokens(&pages)(page(3)); err != nil {
		t.Fatalf("second collector onPage() error = %v, want nil", err)
	}

	want := []int{99, 1, 2, 3}
	if len(pages) != len(want) {
		t.Fatalf("len(pages) = %d, want %d: CollectTokens must append, never replace", len(pages), len(want))
	}
	for i, n := range want {
		if pages[i].Number != n {
			t.Errorf("pages[%d].Number = %d, want %d", i, pages[i].Number, n)
		}
	}
	if pages[1].WidthPt != 612 || pages[1].HeightPt != 792 {
		t.Errorf("pages[1] geometry = %vx%v, want 612x792 carried from the collected page", pages[1].WidthPt, pages[1].HeightPt)
	}
}

// --- EXTR-14-02 -------------------------------------------------------------

// O-04: a page carrying no recognised label yields an empty, non-nil AnchorObservations slice --
// len(x)==0 is true for nil too, so the nil check is asserted on its own. A nil result would
// defeat the layout_anchors column's array CHECK, which needs "[]", never "null".
func TestAnchorObservations_NoMatchIsEmptyNotNil(t *testing.T) {
	unmatched := []extraction.Token{tok("xyzzy plugh", 0.1, 0.1, 0.2, 0.12)}
	page := []extraction.TokenPage{{Number: 1, Tokens: unmatched}}

	obs := extraction.AnchorObservations(page)
	if obs == nil {
		t.Fatal("AnchorObservations(page with no recognised label) = nil, want a non-nil, zero-length slice")
	}
	if len(obs) != 0 {
		t.Errorf("len(AnchorObservations(...)) = %d, want 0: %+v", len(obs), obs)
	}

	if got := extraction.AnchorObservations(nil); got == nil {
		t.Error("AnchorObservations(nil) = nil, want a non-nil, zero-length slice")
	} else if len(got) != 0 {
		t.Errorf("len(AnchorObservations(nil)) = %d, want 0", len(got))
	}

	if fp := extraction.Fingerprint(page); fp != emptyFingerprint {
		t.Errorf("Fingerprint(page with no recognised label) = %q, want %q -- a real v1: value, not a degenerate one", fp, emptyFingerprint)
	}
}

// O-05: observations from page 2 are excluded even when page 2 sorts first in the slice --
// AnchorObservations must select by Number, not by slice position. The two pages carry
// disjoint, distinguishable label sets, so a leak is provable rather than merely possible.
func TestAnchorObservations_ExcludesPageTwoEvenWhenFirst(t *testing.T) {
	page1Tokens := []extraction.Token{
		tok("Invoice No: INV-1", 0.05, 0.08, 0.35, 0.10),
		tok("Total: 15000.00", 0.65, 0.30, 0.95, 0.32),
	}
	page2Tokens := []extraction.Token{
		tok("Supplier: Acme Nigeria Ltd", 0.05, 0.50, 0.35, 0.52),
		tok("Currency: NGN", 0.65, 0.60, 0.95, 0.62),
	}
	pages := []extraction.TokenPage{
		{Number: 2, Tokens: page2Tokens},
		{Number: 1, Tokens: page1Tokens},
	}

	obs := extraction.AnchorObservations(pages)
	if len(obs) != 2 {
		t.Fatalf("len(AnchorObservations) = %d, want 2 (page 1's own hits only): %+v", len(obs), obs)
	}
	for _, o := range obs {
		if o.Page != 1 {
			t.Errorf("obs %+v carries Page %d, want 1", o, o.Page)
		}
		if o.Label == "supplier_name" || o.Label == "currency" {
			t.Errorf("obs %+v carries a page-2-only label; page 2 leaked in because it sorted first in the input slice", o)
		}
	}
}

// A document with anchors on page 2 only -- no TokenPage numbered 1 at all -- must not leak
// them into AnchorObservations, which stays non-nil and empty, and Fingerprint must be
// unaffected by page 2's content.
func TestAnchorObservations_NoPageOneAtAllYieldsNonNilEmpty(t *testing.T) {
	pages := []extraction.TokenPage{
		{Number: 2, Tokens: []extraction.Token{tok("Total: 15000.00", 0.65, 0.30, 0.95, 0.32)}},
	}

	obs := extraction.AnchorObservations(pages)
	if obs == nil {
		t.Fatal("AnchorObservations(no page 1 at all) = nil, want a non-nil, zero-length slice")
	}
	if len(obs) != 0 {
		t.Errorf("len(AnchorObservations(no page 1 at all)) = %d, want 0: %+v", len(obs), obs)
	}
	if fp := extraction.Fingerprint(pages); fp != emptyFingerprint {
		t.Errorf("Fingerprint(no page 1 at all) = %q, want %q -- page 2's anchors must not leak in", fp, emptyFingerprint)
	}
}

// Two distinct tokens tie on all four sort keys (Y0, X0, Label, Band): both are "Total"-family
// matches for the "total" label at the identical box. The hash cannot see which of the two
// comes first, but a stored observation list can, so AnchorObservations must place them in the
// same order every time it is called, not just once.
func TestAnchorObservations_TiedObservationsOrderDeterministically(t *testing.T) {
	first := tok("Total", 0.10, 0.30, 0.20, 0.32)
	second := tok("Grand Total", 0.10, 0.30, 0.20, 0.32)
	pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{first, second}}}

	baseline := extraction.AnchorObservations(pages)
	if len(baseline) != 2 {
		t.Fatalf("fixture invalid: len(AnchorObservations) = %d, want 2 tied observations: %+v", len(baseline), baseline)
	}
	if baseline[0].Label != "total" || baseline[1].Label != "total" || baseline[0].Band != baseline[1].Band {
		t.Fatalf("fixture invalid: both observations must share Label %q and Band for a genuine tie: %+v", "total", baseline)
	}
	if baseline[0].Text == baseline[1].Text {
		t.Fatalf("fixture invalid: the two observations must come from distinguishable tokens, or determinism cannot be told from coincidence: %+v", baseline)
	}

	for i := 0; i < 20; i++ {
		got := extraction.AnchorObservations(pages)
		if len(got) != 2 || got[0].Text != baseline[0].Text || got[1].Text != baseline[1].Text {
			t.Fatalf("run %d: AnchorObservations = %+v, want the same order as run 0 = %+v", i, got, baseline)
		}
	}
}

// --- EXTR-19-02: BoxlessFingerprint, the order rule and the empty case ----------------------
//
// Written Mode A: red against fingerprint.go's stub.

// bxEmptyFingerprint is sha256("") behind the boxless prefix -- the same digest as
// emptyFingerprint above, deliberately, so the two schemes agree on a page with no label.
const bxEmptyFingerprint = "b1:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// AC-5, and the ONLY assertion in this subtask that guards the no-sort rule.
//
// Building BoxlessFingerprint on AnchorObservations would compile, hash three elements, and
// pass AC-1 and AC-2: every DOCX token carries the zero box, so its tiebreak sort
// (fingerprint.go:134-146) falls through to Label, and on all three committed fixtures the
// alphabetical order (invoice_no < issue_date < total) coincides with document order. Only a
// deliberately permuted input tells the two implementations apart. Narrowing this test to a
// fixture pair deletes the requirement outright.
func TestBoxlessFingerprint_ChangesWhenTheLabelParagraphsAreReordered(t *testing.T) {
	invNo := bxZeroTok("Invoice No: ASC-2026-0919")
	date := bxZeroTok("Issue Date: 14 Aug 2026")
	total := bxZeroTok("Total: NGN 4,300.00")

	orders := []struct {
		name   string
		tokens []extraction.Token
	}{
		{"document order, which is also alphabetical", []extraction.Token{invNo, date, total}},
		{"total first", []extraction.Token{total, invNo, date}},
		{"date first, total second", []extraction.Token{date, total, invNo}},
	}

	bx := make([]string, 0, len(orders))
	geo := make([]string, 0, len(orders))
	for _, o := range orders {
		page := extraction.TokenPage{Number: 1, Tokens: o.tokens}
		if n := len(bxElements(page)); n != 3 {
			t.Fatalf("%s yields %d hashed element(s) %v, want 3", o.name, n, bxElements(page))
		}
		fp := extraction.BoxlessFingerprint([]extraction.TokenPage{page})
		bxRealValue(t, o.name, fp)
		bx = append(bx, fp)
		geo = append(geo, extraction.Fingerprint([]extraction.TokenPage{page}))
	}

	if n := bxDistinct(bx); n != len(orders) {
		t.Errorf("the %d orderings yield %d distinct boxless fingerprint(s) %q, want %d -- BoxlessFingerprint must walk page.Tokens in slice order and never sort",
			len(orders), n, bx, len(orders))
	}

	// The control, in the same test. Fingerprint is blind to all three orderings
	// (TestFingerprint_BoxlessTokensDegradeToTheLabelSet is the mechanism), which is what makes
	// the permutation invisible to a sorting implementation and this test the discriminator.
	if len(geo[0]) != 67 || geo[0] == emptyFingerprint {
		t.Fatalf("Fingerprint(%s) = %q, want a real 67-byte value; the control below proves nothing", orders[0].name, geo[0])
	}
	if n := bxDistinct(geo); n != 1 {
		t.Errorf("Fingerprint yields %d distinct value(s) %q across the same three orderings, want 1 -- the geometric fingerprint must not see this permutation, or the boxless assertion above is not the discriminator it claims to be",
			n, geo)
	}
}

// AC-5. Only label placement is hashed, never the value beside the label: two invoices off one
// template must land on one identity.
func TestBoxlessFingerprint_IgnoresTheValuesBesideTheLabels(t *testing.T) {
	lean := extraction.TokenPage{Number: 1, Tokens: []extraction.Token{
		bxZeroTok("Invoice No: A-1"),
		bxZeroTok("Issue Date: 14 Aug 2026"),
		bxZeroTok("Total: 1.00"),
	}}
	fat := extraction.TokenPage{Number: 1, Tokens: []extraction.Token{
		bxZeroTok("Invoice No: ZZZZ-99999"),
		bxZeroTok("Issue Date: 31 December 2099"),
		bxZeroTok("Total: 999999.99"),
	}}

	fpLean := extraction.BoxlessFingerprint([]extraction.TokenPage{lean})
	fpFat := extraction.BoxlessFingerprint([]extraction.TokenPage{fat})
	bxRealValue(t, "lean values", fpLean)
	bxRealValue(t, "fat values", fpFat)

	// The pair must differ as text and agree element for element, or the equality could be
	// satisfied by a variant that quietly changed a placement.
	if slices.Equal(bxTexts(lean), bxTexts(fat)) {
		t.Fatalf("the two pages carry identical token texts %q; the equality below proves nothing", bxTexts(lean))
	}
	le, fe := bxElements(lean), bxElements(fat)
	if len(le) != 3 {
		t.Fatalf("the lean page yields %d hashed element(s) %v, want 3", len(le), le)
	}
	if !slices.Equal(le, fe) {
		t.Fatalf("the two pages yield different element lists %v and %v; one of the fat values trips a lexicon pattern of its own", le, fe)
	}

	if fpLean != fpFat {
		t.Errorf("BoxlessFingerprint(lean) = %q, BoxlessFingerprint(fat) = %q, want equal -- only the values beside the labels differ", fpLean, fpFat)
	}
}

// AC-5. The three-way split itself. "Invoice No:" and "Sub-total" are measured cases, not
// invented ones: the first is the fixture-regeneration hazard EXTR-19-01's QA comment warns
// about, and the second shows "i" arising from one real token under two real patterns.
func TestBoxlessFingerprint_SplitsWholeLeadingAndInlineLabels(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"Invoice No", []string{"invoice_no:w"}},
		{"Invoice No: A-1", []string{"invoice_no:l"}},
		{"Ref (Invoice No) 12", []string{"invoice_no:i"}},
		{"Invoice No:", []string{"invoice_no:l"}},
		{"Sub-total", []string{"subtotal:w", "total:i"}},
	}

	fp := map[string]string{}
	for _, c := range cases {
		got := extraction.AnchorLabelPlacementsForTest(c.text)
		if len(got) == 0 {
			t.Errorf("AnchorLabelPlacementsForTest(%q) matched nothing, want %v", c.text, c.want)
			continue
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("AnchorLabelPlacementsForTest(%q) = %v, want %v", c.text, got, c.want)
		}

		v := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{bxZeroTok(c.text)}}})
		bxRealValue(t, c.text, v)
		fp[c.text] = v
	}

	// w, l and i must be three identities, not one.
	split := []string{fp["Invoice No"], fp["Invoice No: A-1"], fp["Ref (Invoice No) 12"]}
	if n := bxDistinct(split); n != 3 {
		t.Errorf("the whole/leading/inline trio yields %d distinct fingerprint(s) %q, want 3", n, split)
	}

	// The w/l boundary sits at the trailing separator, not at the label. A stacked fixture
	// regenerated as "Invoice No:" classifies l and collapses B onto A.
	if fp["Invoice No:"] != fp["Invoice No: A-1"] {
		t.Errorf("BoxlessFingerprint(%q) = %q, BoxlessFingerprint(%q) = %q, want equal -- a trailing separator makes the label lead a value, even an empty one",
			"Invoice No:", fp["Invoice No:"], "Invoice No: A-1", fp["Invoice No: A-1"])
	}
}

// AC-6. A page showing no recognised label still gets a real identity, matching Fingerprint's
// own documented behaviour: a value that simply matches no stored rule.
func TestBoxlessFingerprint_EmptyInputStillFingerprints(t *testing.T) {
	// Parity with Fingerprint, spelled: the same digest under the other prefix.
	if strings.TrimPrefix(bxEmptyFingerprint, "b1:") != strings.TrimPrefix(emptyFingerprint, "v1:") {
		t.Fatalf("bxEmptyFingerprint = %q and emptyFingerprint = %q carry different digests; one of the two constants is wrong", bxEmptyFingerprint, emptyFingerprint)
	}

	// Control needle: a page that DOES carry a label must not land on the empty value, or the
	// three equalities below would also hold for a function returning one constant.
	hit := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{bxZeroTok("Total: NGN 4,300.00")}}})
	if len(hit) != 67 || hit == bxEmptyFingerprint {
		t.Fatalf("BoxlessFingerprint(a page carrying \"Total: NGN 4,300.00\") = %q, want a real 67-byte value other than %q", hit, bxEmptyFingerprint)
	}

	for _, c := range []struct {
		name  string
		pages []extraction.TokenPage
	}{
		{"nil pages", nil},
		{"a page numbered 2 only", []extraction.TokenPage{{Number: 2, Tokens: []extraction.Token{bxZeroTok("Total: NGN 4,300.00")}}}},
		{"a page 1 whose token trips no pattern", []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{bxZeroTok("xyzzy plugh")}}}},
	} {
		if got := extraction.BoxlessFingerprint(c.pages); got != bxEmptyFingerprint {
			t.Errorf("BoxlessFingerprint(%s) = %q, want %q", c.name, got, bxEmptyFingerprint)
		}
	}
}

// AC-6. Page 1 is selected by Number, not by slice position -- the caller's order is not the
// page order (fingerprint.go:107-109).
func TestBoxlessFingerprint_ReadsPageOneOnly(t *testing.T) {
	page1 := []extraction.Token{bxZeroTok("Invoice No: ASC-2026-0919"), bxZeroTok("Total: NGN 4,300.00")}
	page2 := []extraction.Token{bxZeroTok("Supplier: Acme Nigeria Ltd"), bxZeroTok("Currency: NGN")}

	alone := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: page1}})
	bxRealValue(t, "page 1 alone", alone)

	// The two label sets must be distinguishable, or a leak cannot be told from no leak.
	asIfPage1 := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: page2}})
	bxRealValue(t, "page 2's tokens read as page 1", asIfPage1)
	if asIfPage1 == alone {
		t.Fatalf("the two pages fingerprint alike (%q); the assertion below would pass whether or not page 2 leaked in", alone)
	}

	got := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 2, Tokens: page2}, {Number: 1, Tokens: page1}})
	if got != alone {
		t.Errorf("BoxlessFingerprint([page 2, page 1]) = %q, want %q -- page 2 leaked in because it sorted first in the input slice", got, alone)
	}
}

// --- EXTR-19-02 QA (Mode B): three surviving mutants from the Stage-4 pass -------------------

// AC-3/AC-5. One element per (token, matcher), from the LEFTMOST match only. Switching to
// FindAllStringIndex compiles, reads as a fix, and moves every stored b1: value; nothing in the
// Stage-2.5 set noticed, because no fixture repeats a label inside one token.
func TestBoxlessFingerprint_EmitsOneElementPerTokenAndMatcher(t *testing.T) {
	repeats := []string{"Total Total", "Invoice No and Invoice Number", "Grand Total and Total"}
	for _, text := range repeats {
		got := extraction.AnchorLabelPlacementsForTest(text)
		if len(got) != 1 {
			t.Errorf("AnchorLabelPlacementsForTest(%q) = %v, want exactly 1 element -- a repeated label inside one token is still one hit", text, got)
		}
	}

	// Control needle: two DIFFERENT matchers over one token DO yield two elements, so the
	// count above is a leftmost-match rule and not a blanket one-per-token cap.
	if got := extraction.AnchorLabelPlacementsForTest("Sub-total"); !slices.Equal(got, []string{"subtotal:w", "total:i"}) {
		t.Errorf("AnchorLabelPlacementsForTest(%q) = %v, want [subtotal:w total:i]", "Sub-total", got)
	}

	// The digest, not only the helper: a page whose token names total twice must land on the
	// page whose token names it once.
	twice := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{bxZeroTok("Total Total")}}})
	once := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{bxZeroTok("Total: NGN 1.00")}}})
	bxRealValue(t, "Total Total", twice)
	bxRealValue(t, "Total: NGN 1.00", once)
	if twice != once {
		t.Errorf("BoxlessFingerprint(%q) = %q, BoxlessFingerprint(%q) = %q, want equal -- both are one leading total and nothing else",
			"Total Total", twice, "Total: NGN 1.00", once)
	}
}

// AC-6. The FIRST page numbered 1 is the whole input; a second one is not appended. Deleting
// BoxlessFingerprint's break survived the whole Stage-2.5 set, because no spec there hands it
// two page-1s.
func TestBoxlessFingerprint_ReadsTheFirstPageNumberedOne(t *testing.T) {
	first := []extraction.Token{bxZeroTok("Invoice No")}
	second := []extraction.Token{bxZeroTok("Total")}

	alone := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: first}})
	other := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: second}})
	both := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: append(append([]extraction.Token{}, first...), second...)}})
	for _, c := range []struct{ role, fp string }{{"page 1 alone", alone}, {"the second page-1 alone", other}, {"both token runs on one page", both}} {
		bxRealValue(t, c.role, c.fp)
	}
	// Three distinguishable outcomes, or "first wins" cannot be told from "both" or "last".
	if n := bxDistinct([]string{alone, other, both}); n != 3 {
		t.Fatalf("the three token runs yield %d distinct value(s) [%q %q %q], want 3; the assertion below would pass whichever page won", n, alone, other, both)
	}

	got := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: first}, {Number: 1, Tokens: second}})
	if got != alone {
		t.Errorf("BoxlessFingerprint([page 1, page 1]) = %q, want %q -- only the first page numbered 1 is read", got, alone)
	}
}

// AC-5. labelPlacement compares BYTE offsets against len(text): the whole-token arm requires an
// exact byte-for-byte fit, so an invisible trailing space demotes "Total" from w to l and moves
// the layout onto the inline identity. Same hazard class as "Invoice No:" -- see
// TestBoxlessFingerprint_SplitsWholeLeadingAndInlineLabels -- and the reason a stacked fixture
// must be generated, never hand-typed. Trimming here is a deliberate change, not a tidy-up.
func TestBoxlessFingerprint_ClassifiesOnRawBytes(t *testing.T) {
	cases := []struct{ text, want string }{
		{"Total", "total:w"},
		{"Total ", "total:l"},
		{" Total", "total:i"},
		{"Totalé", "total:l"}, // a multi-byte tail is a tail; len is bytes, not runes
	}

	fps := make([]string, 0, len(cases))
	for _, c := range cases {
		got := extraction.AnchorLabelPlacementsForTest(c.text)
		if !slices.Equal(got, []string{c.want}) {
			t.Errorf("AnchorLabelPlacementsForTest(%q) = %v, want [%s]", c.text, got, c.want)
		}
		fp := extraction.BoxlessFingerprint([]extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{bxZeroTok(c.text)}}})
		bxRealValue(t, c.text, fp)
		fps = append(fps, fp)
	}
	if len(fps) != len(cases) {
		t.Fatalf("fingerprinted %d of %d case(s)", len(fps), len(cases))
	}

	// w, l and i are three identities here too, so the placements above are load-bearing.
	if n := bxDistinct(fps[:3]); n != 3 {
		t.Errorf("%q, %q and %q yield %d distinct fingerprint(s) %q, want 3", cases[0].text, cases[1].text, cases[2].text, n, fps[:3])
	}
}
