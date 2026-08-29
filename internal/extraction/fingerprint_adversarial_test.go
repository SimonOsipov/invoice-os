// fingerprint_adversarial_test.go: edge, boundary and negative cases beyond F-01..F-10.
package extraction_test

import (
	"fmt"
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
