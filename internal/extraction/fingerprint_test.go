// fingerprint_test.go: F-01..F-10 (EXTR-04-03 AC). External package: every spec reaches only
// exported symbols -- Fingerprint, CollectTokens, FingerprintVersion and TokenPage.
//
// Every spec asserting EQUALITY (F-01, F-03, F-04, F-07, F-08) also asserts the result is 67
// bytes, so two degenerate returns cannot satisfy it vacuously. F-09 pairs its retention check
// with a positive page count for the same reason.
package extraction_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// headerTokens is a canonical page-1 header: four anchor labels at distinct rows, split
// across the left and right column bands.
func headerTokens() []extraction.Token {
	return []extraction.Token{
		{Text: "Invoice No: INV-2026-001", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.08, X1: 0.35, Y1: 0.10}},
		{Text: "Issue Date: 2026-03-12", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.12, X1: 0.35, Y1: 0.14}},
		{Text: "Supplier: Acme Nigeria Ltd", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.16, X1: 0.35, Y1: 0.18}},
		{Text: "Total: 15000.00", Region: extraction.Region{Page: 1, X0: 0.65, Y0: 0.20, X1: 0.95, Y1: 0.22}},
	}
}

func shiftY(tokens []extraction.Token, dy float64) []extraction.Token {
	out := make([]extraction.Token, len(tokens))
	for i, tok := range tokens {
		r := tok.Region
		r.Y0 += dy
		r.Y1 += dy
		out[i] = extraction.Token{Text: tok.Text, Region: r}
	}
	return out
}

// F-01: a uniform vertical shift changes no reading order and no column band, so the
// fingerprint must not change.
func TestFingerprint_IsStableUnderVerticalReflow(t *testing.T) {
	base := headerTokens()
	shifted := shiftY(base, 0.04)

	fpBase := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: base}})
	fpShifted := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: shifted}})

	if len(fpBase) != 67 {
		t.Fatalf("Fingerprint(base header) = %q (%d bytes), want 67 bytes", fpBase, len(fpBase))
	}
	if fpBase != fpShifted {
		t.Errorf("Fingerprint(base) = %q, Fingerprint(shifted +0.04 Y) = %q, want equal: a uniform vertical shift must not change reading order or column band", fpBase, fpShifted)
	}
}

// F-02: moving a label from the left third to the right third of the page changes its column
// band, and must change the fingerprint.
func TestFingerprint_ChangesWhenALabelChangesColumn(t *testing.T) {
	base := headerTokens()
	moved := make([]extraction.Token, len(base))
	copy(moved, base)
	// invoice_no's centre X moves from ~0.20 (band 0) to ~0.835 (band 2); every other token
	// is untouched.
	moved[0] = extraction.Token{
		Text:   base[0].Text,
		Region: extraction.Region{Page: 1, X0: 0.72, Y0: base[0].Region.Y0, X1: 0.95, Y1: base[0].Region.Y1},
	}

	fpBase := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: base}})
	fpMoved := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: moved}})

	if fpBase == fpMoved {
		t.Errorf("Fingerprint(base) = %q, Fingerprint(invoice_no moved to the right third) = %q, want different", fpBase, fpMoved)
	}
}

// F-03: the fingerprint is built from matched LABELS only. Two pages with identical label
// positions but different supplier names and TINs embedded in the same tokens must fingerprint
// equal.
func TestFingerprint_IgnoresSupplierIdentity(t *testing.T) {
	labelsA := []extraction.Token{
		{Text: "Invoice No: INV-2026-001", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.08, X1: 0.35, Y1: 0.10}},
		{Text: "Supplier: Acme Nigeria Ltd", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.14, X1: 0.35, Y1: 0.16}},
		{Text: "Supplier TIN: 12345678-9012", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.18, X1: 0.35, Y1: 0.20}},
	}
	labelsB := []extraction.Token{
		{Text: "Invoice No: INV-9999-777", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.08, X1: 0.35, Y1: 0.10}},
		{Text: "Supplier: Globex Trading Corp", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.14, X1: 0.35, Y1: 0.16}},
		{Text: "Supplier TIN: 87654321-0001", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.18, X1: 0.35, Y1: 0.20}},
	}

	fpA := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: labelsA}})
	fpB := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: labelsB}})

	if len(fpA) != 67 {
		t.Fatalf("Fingerprint(supplier A) = %q (%d bytes), want 67 bytes", fpA, len(fpA))
	}
	if fpA != fpB {
		t.Errorf("Fingerprint(supplier A) = %q, Fingerprint(supplier B) = %q, want equal: only matched labels and their positions may drive the fingerprint, never a name or TIN value embedded in the same token", fpA, fpB)
	}
}

// F-04: Fingerprint must select the TokenPage whose Number == 1, never pages[0]. Both slices
// below carry an identical page-1 entry at position 1 and differ only in the page-2 entry at
// position 0, so a positional implementation would see the varying content and disagree.
func TestFingerprint_ReadsPageOneOnly(t *testing.T) {
	page1 := extraction.TokenPage{Number: 1, Tokens: headerTokens()}

	page2Variant1 := extraction.TokenPage{Number: 2, Tokens: []extraction.Token{
		{Text: "Line 1: Widget x 10", Region: extraction.Region{Page: 2, X0: 0.05, Y0: 0.30, X1: 0.40, Y1: 0.32}},
	}}
	page2Variant2 := extraction.TokenPage{Number: 2, Tokens: []extraction.Token{
		{Text: "Total: 999999.00", Region: extraction.Region{Page: 2, X0: 0.05, Y0: 0.30, X1: 0.40, Y1: 0.32}},
	}}

	pagesA := []extraction.TokenPage{page2Variant1, page1}
	pagesB := []extraction.TokenPage{page2Variant2, page1}

	fpA := extraction.Fingerprint(pagesA)
	fpB := extraction.Fingerprint(pagesB)

	if len(fpA) != 67 {
		t.Fatalf("Fingerprint(pagesA) = %q (%d bytes), want 67 bytes", fpA, len(fpA))
	}
	if fpA != fpB {
		t.Errorf("Fingerprint(pagesA) = %q, Fingerprint(pagesB) = %q, want equal: both carry the same page-1 entry at position 1 and differ only in position 0's page-2 entry, which must be ignored", fpA, fpB)
	}
}

// F-05: the result is always FingerprintVersion + ":" + 64 lowercase hex characters.
func TestFingerprint_IsVersionPrefixed(t *testing.T) {
	fp := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: headerTokens()}})

	wantPrefix := extraction.FingerprintVersion + ":"
	if !strings.HasPrefix(fp, wantPrefix) {
		t.Fatalf("Fingerprint() = %q, want it to start with %q", fp, wantPrefix)
	}
	hexPart := strings.TrimPrefix(fp, wantPrefix)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hexPart) {
		t.Errorf("Fingerprint() hex part = %q (%d chars), want 64 lowercase hex characters", hexPart, len(hexPart))
	}
}

// F-06: nil input and a page whose tokens match no lexicon entry both fingerprint to
// FingerprintVersion + ":" + sha256(""), because the observation set is empty either way.
func TestFingerprint_EmptyInputStillFingerprints(t *testing.T) {
	const wantEmpty = "v1:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	if got := extraction.Fingerprint(nil); got != wantEmpty {
		t.Errorf("Fingerprint(nil) = %q, want %q", got, wantEmpty)
	}

	unmatched := []extraction.Token{
		{Text: "xyzzy plugh", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.1, X1: 0.2, Y1: 0.12}},
	}
	got := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: unmatched}})
	if got != wantEmpty {
		t.Errorf("Fingerprint(all-unmatched page) = %q, want %q -- no lexicon entry recognises this token, so the observation set is empty, same as nil input", got, wantEmpty)
	}
}

// F-07: the fingerprint does not depend on input token order. tokTotalNarrow and tokTotalWide
// share the same Y0 and X0 but land in different column bands (0 vs 1) and both match the
// "total" label -- a sort key of (Y0, X0, labelID) alone ties on these two observations, so
// whichever comes first in the input slice would leak into a stable sort's output. Only a
// fourth key (band) breaks that tie deterministically.
func TestFingerprint_IsDeterministicAcrossRuns(t *testing.T) {
	tokTotalNarrow := extraction.Token{Text: "Total", Region: extraction.Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.20, Y1: 0.32}}
	tokTotalWide := extraction.Token{Text: "Total Amount Due Immediately", Region: extraction.Region{Page: 1, X0: 0.10, Y0: 0.30, X1: 0.90, Y1: 0.32}}
	tokOther := extraction.Token{Text: "Invoice No: INV-2026-001", Region: extraction.Region{Page: 1, X0: 0.05, Y0: 0.08, X1: 0.35, Y1: 0.10}}

	orders := [][]extraction.Token{
		{tokTotalNarrow, tokTotalWide, tokOther},
		{tokTotalWide, tokTotalNarrow, tokOther},
		{tokOther, tokTotalWide, tokTotalNarrow},
	}

	var got []string
	for i, order := range orders {
		fp := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: order}})
		if len(fp) != 67 {
			t.Fatalf("Fingerprint(order %d) = %q (%d bytes), want 67 bytes", i, fp, len(fp))
		}
		got = append(got, fp)
	}

	for i := 1; i < len(got); i++ {
		if got[i] != got[0] {
			t.Errorf("Fingerprint differs across input orderings: order 0 = %q, order %d = %q -- "+
				"the sort key must include column band as a fourth component so two observations "+
				"tied on (Y0, X0, labelID) do not leak input order into the result", got[0], i, got[i])
		}
	}
}

// F-08: boxless tokens (every DOCX token) all sort equal and land in band 0, so the fingerprint
// degenerates to the sorted set of matched labels: equal under reordering, different when a
// label is missing. That degeneracy is the defect BoxlessFingerprint exists to answer --
// TestBoxlessFingerprint_ChangesWhenTheLabelParagraphsAreReordered is its sibling.
func TestFingerprint_BoxlessTokensDegradeToTheLabelSet(t *testing.T) {
	zero := func(text string) extraction.Token {
		return extraction.Token{Text: text, Region: extraction.Region{Page: 1, X0: 0, Y0: 0, X1: 0, Y1: 0}}
	}

	full := []extraction.Token{
		zero("Invoice No: INV-2026-001"),
		zero("Issue Date: 2026-03-12"),
		zero("Total: 15000.00"),
	}
	reordered := []extraction.Token{full[2], full[0], full[1]}
	missingOne := []extraction.Token{full[0], full[1]}

	fpFull := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: full}})
	fpReordered := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: reordered}})
	fpMissing := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: missingOne}})

	if len(fpFull) != 67 {
		t.Fatalf("Fingerprint(full boxless set) = %q (%d bytes), want 67 bytes", fpFull, len(fpFull))
	}
	if fpFull != fpReordered {
		t.Errorf("Fingerprint(full) = %q, Fingerprint(reordered) = %q, want equal: boxless tokens all sort equal and land in band 0, so only the sorted label set should matter", fpFull, fpReordered)
	}
	if fpFull == fpMissing {
		t.Errorf("Fingerprint(full) = %q, Fingerprint(missing one label) = %q, want different", fpFull, fpMissing)
	}
}

// F-09: CollectTokens must not retain Page.ImagePNG, which is only valid for the duration of
// the onPage call. The length check below keeps the retention assertions from passing vacuously
// over zero collected pages.
func TestCollectTokens_DoesNotRetainTheBorrowedImage(t *testing.T) {
	var pages []extraction.TokenPage
	onPage := extraction.CollectTokens(&pages)

	img := []byte{0xAA, 0xBB, 0xCC}
	page := extraction.Page{
		Number:   1,
		WidthPt:  612,
		HeightPt: 792,
		ImagePNG: img,
		Tokens: []extraction.Token{
			{Text: "Invoice No: INV-001", Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.1, X1: 0.4, Y1: 0.12}},
		},
	}

	if err := onPage(page); err != nil {
		t.Fatalf("onPage() error = %v, want nil", err)
	}

	// mutate the caller's backing array after the callback returns, as a PageReader reusing
	// its render buffer across pages would.
	for i := range img {
		img[i] = 0xFF
	}

	if len(pages) != 1 {
		t.Fatalf("len(pages) = %d, want 1: CollectTokens must append every page it is handed", len(pages))
	}
	if pages[0].Number != 1 || len(pages[0].Tokens) != 1 || pages[0].Tokens[0].Text != "Invoice No: INV-001" {
		t.Errorf("pages[0] = %+v, want Number and Tokens preserved from the collected page", pages[0])
	}

	typ := reflect.TypeOf(extraction.TokenPage{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "image") || strings.Contains(name, "png") {
			t.Errorf("TokenPage declares field %q; it must carry no image, so no collected page can hold a borrowed, later-mutated ImagePNG", typ.Field(i).Name)
		}
	}
}

// F-10: the output length is a constant 67 bytes -- "v1:" plus a 64-hex SHA-256 digest -- for
// every input, never proportional to page busyness. The 128-byte layout_fingerprint CHECK
// ceiling has headroom to spare.
func TestFingerprint_FitsTheColumnCap(t *testing.T) {
	var busy []extraction.Token
	for i := 0; i < 40; i++ {
		y := 0.02 * float64(i+1)
		busy = append(busy, extraction.Token{
			Text:   "Total row",
			Region: extraction.Region{Page: 1, X0: 0.1, Y0: y, X1: 0.3, Y1: y + 0.01},
		})
	}

	got := extraction.Fingerprint([]extraction.TokenPage{{Number: 1, Tokens: busy}})
	if len(got) != 67 {
		t.Errorf("len(Fingerprint(40-row busy page)) = %d, want 67", len(got))
	}
}

// --- EXTR-16-02: the fingerprint may not move (D-3) --------------------------

// fpCorpusPinned is every committed layout's fingerprint, measured at e763fccd, before anchor
// specificity. anchorLexicon compiles into anchorLabelMatchers and Fingerprint reads those, so
// a pattern edit silently invalidates every stored document's layout fingerprint and every rule
// learned against it. These six are what says such an edit happened.
var fpCorpusPinned = map[string]string{
	"corpus_inline_labels.pdf":  "v1:60be15050c9a80950f7d1ea69d21178fe23e6fb61021668a937cabfa139c086d",
	"corpus_split_labels.pdf":   "v1:1ca4de9d55d90a037fa1187ff70158b635cc48cd697e50f5ae52768413b0e680",
	"corpus_stacked_labels.pdf": "v1:fdd95d43c0d4a79dbe0e3c5c3ea09b23a8bba6b3bed73c3a7d51dfb23e4e1846",
	"corpus_two_column.pdf":     "v1:452b9167485fb91b77eb67c9008dfb3893eefbfe819ec762b7392a06242473c5",
	"corpus_ambiguous_date.pdf": "v1:5dd14339eef9ccb517bf3d96a2cb19fba6c6b0544b8f9d4499c65dbad6a807c5",
	"corpus_totals_block.pdf":   "v1:91279772deacd7b7e8ffc8f7f168d3bc9735feb1917631d186d9362896f94ba4",
}

// EXTR-16-02 AC-4. Green before the fix and green after it, by design: D-3 forbids the
// anchorLexicon edit, and this is the oracle that catches one.
func TestFingerprint_IsUnchangedByAnchorSpecificity(t *testing.T) {
	if len(corpusLayouts) != 6 || len(fpCorpusPinned) != 6 {
		t.Fatalf("corpusLayouts names %d layout(s) and fpCorpusPinned pins %d; want 6 each, or the loop below asserts over less than the corpus",
			len(corpusLayouts), len(fpCorpusPinned))
	}

	for _, name := range corpusLayouts {
		want, ok := fpCorpusPinned[name]
		if !ok {
			t.Errorf("%s has no pinned fingerprint", name)
			continue
		}
		if got := extraction.Fingerprint(rvCorpusPages(t, name)); got != want {
			t.Errorf("Fingerprint(%s) = %q, want %q -- the layout fingerprint moved, which means anchorLexicon changed; that invalidates every stored rule and needs a FingerprintVersion bump, and EXTR-16 does not touch the lexicon",
				name, got, want)
		}
	}
}

// --- EXTR-14-02 -------------------------------------------------------------

// O-02: AnchorObservations on corpus_two_column.pdf returns exactly seven observations, in the
// exact order Fingerprint hashes them. Row 3 (buyer_name) and row 5 (supplier_tin) sit in band
// 2 because the layout is two-column; row 5 is the buyer's own TIN, caught by supplier_tin's
// optional party word -- the Tier-1 defect EXTR-14-09 exploits, not a bug in this ordering.
func TestAnchorObservations_OrdersTheTwoColumnCorpusExactly(t *testing.T) {
	obs := extraction.AnchorObservations(rvCorpusPages(t, "corpus_two_column.pdf"))

	if len(obs) != 7 {
		t.Fatalf("len(AnchorObservations(corpus_two_column.pdf)) = %d, want 7: %+v", len(obs), obs)
	}

	wantLabel := []string{"invoice_no", "issue_date", "supplier_name", "buyer_name", "supplier_tin", "supplier_tin", "total"}
	wantBand := []int{0, 0, 0, 2, 0, 2, 0}
	for i, o := range obs {
		if o.Page != 1 {
			t.Errorf("obs[%d].Page = %d, want 1", i, o.Page)
		}
		if o.Label != wantLabel[i] || o.Band != wantBand[i] {
			t.Errorf("obs[%d] = {Label:%q Band:%d}, want {Label:%q Band:%d}", i, o.Label, o.Band, wantLabel[i], wantBand[i])
		}
	}
}

// O-03: joining label:band over AnchorObservations and hashing reproduces Fingerprint exactly,
// for all six corpus layouts -- the hashed set and the stored observation list are the same
// list by construction and can never drift apart.
func TestAnchorObservations_ProjectionReproducesFingerprint(t *testing.T) {
	sawNonEmpty := false
	for _, name := range corpusLayouts {
		pages := rvCorpusPages(t, name)
		obs := extraction.AnchorObservations(pages)
		if len(obs) > 0 {
			sawNonEmpty = true
		}

		elems := make([]string, len(obs))
		for i, o := range obs {
			elems[i] = o.Label + ":" + strconv.Itoa(o.Band)
		}
		sum := sha256.Sum256([]byte(strings.Join(elems, "|")))
		want := extraction.FingerprintVersion + ":" + hex.EncodeToString(sum[:])

		if got := extraction.Fingerprint(pages); got != want {
			t.Errorf("%s: Fingerprint(pages) = %q, want %q computed from AnchorObservations' own (label,band) projection", name, got, want)
		}
	}
	if !sawNonEmpty {
		t.Fatal("every layout yielded zero observations; the equality above would pass vacuously")
	}
}

// --- EXTR-19-01: the discriminating pair and its control ----------------------------------
//
// Written Mode A: red until EXTR-19-01 commits the two new .docx files and their goldens.
// The fixture constants and the transcribed paragraph lists live in corpus_wired_db_test.go
// (bxStackedGolden, bxInlineGolden, bxInlineParagraphs, ...), same package.

// bxPage1 serves a committed golden through the real DoclingReader and returns the page whose
// Number is 1 -- AnchorObservations' own page selection (fingerprint.go:105-109), not slice
// position. Fails loudly on an empty page so no assertion below can pass over zero tokens.
func bxPage1(t *testing.T, golden string) extraction.TokenPage {
	t.Helper()

	_, pages, _ := dcServeGolden(t, dcReadNamedGolden(t, golden))
	for _, p := range pages {
		if p.Number != 1 {
			continue
		}
		if len(p.Tokens) == 0 {
			t.Fatalf("%s page 1 carries no token; every assertion over it would be vacuous", golden)
		}
		return p
	}
	t.Fatalf("%s carries no page numbered 1 (%d page(s) read)", golden, len(pages))
	return extraction.TokenPage{}
}

func bxOnePage(p extraction.TokenPage) []extraction.TokenPage {
	return []extraction.TokenPage{p}
}

// bxTokenIndex is the slice index of the page-1 token reading exactly want, or -1. Indexed by
// text and never through AnchorObservations: its tiebreak sort (fingerprint.go:134-146)
// reorders zero-box observations alphabetically by label, which destroys the raw ordinal.
func bxTokenIndex(page extraction.TokenPage, want string) int {
	for i, tok := range page.Tokens {
		if tok.Text == want {
			return i
		}
	}
	return -1
}

func bxTexts(page extraction.TokenPage) []string {
	out := make([]string, len(page.Tokens))
	for i, tok := range page.Tokens {
		out[i] = tok.Text
	}
	return out
}

func bxSortedLabels(obs []extraction.AnchorObservation) []string {
	out := make([]string, len(obs))
	for i, o := range obs {
		out[i] = o.Label
	}
	slices.Sort(out)
	return out
}

// bxWantLabels is asserted as a literal rather than by cross-comparing the three fixtures:
// three empty observation lists are also all equal to each other.
var bxWantLabels = []string{"invoice_no", "issue_date", "total"}

// Story AC #6. The pair must discriminate on STRUCTURE, never on label vocabulary -- without
// this, a later regeneration could drift B into carrying a fourth label and the split that
// EXTR-19-02 measures would be meaningless.
func TestBoxlessFixtures_ShareTheSameAnchorLabelSet(t *testing.T) {
	fixtures := []struct{ role, golden string }{
		{"A (invoice.docx)", dxGolden},
		{"A-prime (boxless_inline_variant.docx)", bxInlineGolden},
		{"B (boxless_stacked_invoice.docx)", bxStackedGolden},
	}
	if len(fixtures) != 3 {
		t.Fatalf("this test names %d fixture(s), want A, A-prime and B", len(fixtures))
	}

	for _, f := range fixtures {
		got := bxSortedLabels(extraction.AnchorObservations(bxOnePage(bxPage1(t, f.golden))))
		if !slices.Equal(got, bxWantLabels) {
			t.Errorf("%s yields anchor labels %v, want exactly %v once each", f.role, got, bxWantLabels)
		}
	}
}

// Story AC #6, second half: the defect this story exists to fix, pinned. A and B collide under
// the geometric fingerprint because every DOCX token carries the zero box
// (TestFingerprint_BoxlessTokensDegradeToTheLabelSet is the mechanism). Pinned so a later
// change that accidentally splits them at v1 is visible.
//
// This is a change-detector, not evidence that the pair discriminates: any two boxless pages
// carrying the same three labels collide here. The discrimination is EXTR-19-02's AC-1.
func TestBoxlessFixtures_CollideUnderTheGeometricFingerprint(t *testing.T) {
	a := bxOnePage(bxPage1(t, dxGolden))
	b := bxOnePage(bxPage1(t, bxStackedGolden))

	fpA := extraction.Fingerprint(a)
	fpB := extraction.Fingerprint(b)

	// A label-less page also collides with any other label-less page, on sha256(""). Rule it
	// out before asserting the equality.
	fpEmpty := extraction.Fingerprint([]extraction.TokenPage{{Number: 1}})
	if fpA == fpEmpty || fpB == fpEmpty {
		t.Fatalf("Fingerprint(A) = %q, Fingerprint(B) = %q, Fingerprint(an empty page) = %q -- a fixture matched no anchor label, so the equality below would be vacuous", fpA, fpB, fpEmpty)
	}
	if len(fpA) != 67 {
		t.Fatalf("Fingerprint(A) = %q (%d bytes), want 67", fpA, len(fpA))
	}
	if fpA != fpB {
		t.Errorf("Fingerprint(A) = %q, Fingerprint(B) = %q, want equal -- v1 sees only the sorted label set on boxless tokens, so this pair MUST collide today; if it no longer does, the geometric fingerprint moved and every stored rule is invalidated", fpA, fpB)
	}
}

// Story AC #7. The control is a near-miss, not a tie: A-prime's extra line item sits INSIDE
// the anchor run, so at least one anchor's raw token ordinal differs from A's. This is what
// falsifies the rejected token-ordinal fingerprint scheme (D-5) -- appended after Total it
// would shift nothing and prove nothing.
func TestBoxlessFixtures_TheControlShiftsAnAnchorOrdinal(t *testing.T) {
	a := bxPage1(t, dxGolden)
	ap := bxPage1(t, bxInlineGolden)

	aTotal := bxTokenIndex(a, dxParagraphs[2])
	apIssue := bxTokenIndex(ap, bxInlineParagraphs[1])
	apExtra := bxTokenIndex(ap, bxInlineExtraParagraph)
	apTotal := bxTokenIndex(ap, bxInlineTotalParagraph)

	for _, probe := range []struct {
		what  string
		index int
	}{
		{"A / " + dxParagraphs[2], aTotal},
		{"A-prime / " + bxInlineParagraphs[1], apIssue},
		{"A-prime / " + bxInlineExtraParagraph, apExtra},
		{"A-prime / " + bxInlineTotalParagraph, apTotal},
	} {
		if probe.index < 0 {
			t.Fatalf("no page-1 token reads %s; the index assertions below cannot be trusted.\n  A       = %q\n  A-prime = %q", probe.what, bxTexts(a), bxTexts(ap))
		}
	}

	if len(ap.Tokens) != len(a.Tokens)+1 {
		t.Errorf("A-prime carries %d page-1 token(s), A carries %d; want exactly one more.\n  A       = %q\n  A-prime = %q", len(ap.Tokens), len(a.Tokens), bxTexts(a), bxTexts(ap))
	}
	if apTotal <= aTotal {
		t.Errorf("A-prime's total sits at page-1 token index %d, A's at %d; want strictly greater -- the control must shift at least one anchor ordinal.\n  A       = %q\n  A-prime = %q", apTotal, aTotal, bxTexts(a), bxTexts(ap))
	}
	if apExtra <= apIssue || apExtra >= apTotal {
		t.Errorf("A-prime's extra line item sits at index %d, outside the open interval (issue_date %d, total %d); an appended paragraph shifts no ordinal and degenerates the control into a tie.\n  A-prime = %q", apExtra, apIssue, apTotal, bxTexts(ap))
	}
}

// Story AC #8. An absence assertion, so it carries a control needle that must be FOUND. If the
// line item tripped \btotal\b, \bdate\b or \btax\b it would add a fourth AnchorObservation and
// silently break EXTR-19-02 AC-2 (A-prime equals A) -- with A-prime still passing every other
// test in this subtask.
func TestBoxlessFixtures_TheExtraParagraphMatchesNoAnchor(t *testing.T) {
	oneToken := func(text string) []extraction.TokenPage {
		return []extraction.TokenPage{{
			Number: 1,
			Tokens: []extraction.Token{{Text: text, Region: extraction.Region{Page: 1}}},
		}}
	}

	if got := extraction.AnchorObservations(oneToken(bxInlineTotalParagraph)); len(got) != 1 {
		t.Fatalf("control needle: AnchorObservations(%q) = %d observation(s), want exactly 1 -- the lexicon scan does not find a known anchor, so the absence assertion below proves nothing", bxInlineTotalParagraph, len(got))
	}

	if got := extraction.AnchorObservations(oneToken(bxInlineExtraParagraph)); len(got) != 0 {
		t.Errorf("AnchorObservations(%q) = %+v, want none -- A-prime's line item must trip no anchorLexicon pattern", bxInlineExtraParagraph, got)
	}

	// ...and it must be the paragraph the committed fixture actually carries, not a string that
	// only ever existed in this file.
	ap := bxPage1(t, bxInlineGolden)
	if bxTokenIndex(ap, bxInlineExtraParagraph) < 0 {
		t.Errorf("%s carries no page-1 token equal to %q; the guard above is scanning a string the fixture does not contain.\n  A-prime = %q", bxInlineGolden, bxInlineExtraParagraph, bxTexts(ap))
	}
}

// --- EXTR-19-01 QA (Mode B): adversarial coverage ------------------------------------------

// bxPlacements classifies every lexicon-matched page-1 token as "whole" (the matched label IS
// the token) or "leading" (the token carries a value after the label). Derived from the live
// anchorLexicon through AnchorObservations, never from a transcribed literal, so a fixture
// regenerated with a different label shape cannot hide behind an edited constant.
func bxPlacements(t *testing.T, page extraction.TokenPage) []string {
	t.Helper()
	out := make([]string, 0, len(page.Tokens))
	for _, tok := range page.Tokens {
		one := []extraction.TokenPage{{
			Number: 1,
			Tokens: []extraction.Token{{Text: tok.Text, Region: extraction.Region{Page: 1}}},
		}}
		for _, o := range extraction.AnchorObservations(one) {
			placement := "leading"
			if o.Text == tok.Text {
				placement = "whole"
			}
			out = append(out, o.Label+"="+placement)
		}
	}
	return out
}

// Story AC #6/#7, the structural difference itself. AC #6 pins that A, A-prime and B share a
// label SET, and AC #5 pins each golden's exact token texts -- but nothing asserts the property
// that makes B a different LAYOUT: B's anchor label spans its whole token, A's is a prefix with
// the value trailing. That is what EXTR-19-02's BoxlessFingerprint must read, and a fixture
// regenerated as "Invoice No:" (still stacked, still six paragraphs, still the same label set)
// would pass every other test in this subtask while erasing it.
func TestBoxlessFixtures_TheStackedFixtureAloneCarriesWholeTokenLabels(t *testing.T) {
	a := bxPlacements(t, bxPage1(t, dxGolden))
	ap := bxPlacements(t, bxPage1(t, bxInlineGolden))
	b := bxPlacements(t, bxPage1(t, bxStackedGolden))

	wantLeading := []string{"invoice_no=leading", "issue_date=leading", "total=leading"}
	wantWhole := []string{"invoice_no=whole", "issue_date=whole", "total=whole"}

	if !slices.Equal(a, wantLeading) {
		t.Errorf("A (%s) placements = %v, want %v", dxGolden, a, wantLeading)
	}
	if !slices.Equal(ap, wantLeading) {
		t.Errorf("A-prime (%s) placements = %v, want %v -- the control must match A structurally, extra line item included", bxInlineGolden, ap, wantLeading)
	}
	if !slices.Equal(b, wantWhole) {
		t.Errorf("B (%s) placements = %v, want %v -- B is the STACKED layout: each label alone on its paragraph, its value on the next", bxStackedGolden, b, wantWhole)
	}
	// The pair is only discriminating if these two differ. Asserted after the literals above
	// so two empty lists cannot satisfy it.
	if slices.Equal(a, b) {
		t.Errorf("A and B carry identical anchor placements %v; the pair discriminates on nothing and EXTR-19-02 has no signal to read", a)
	}
}

// bxInvoiceNumber is the ASC-YYYY-NNNN printed on a golden's page 1, or "".
var bxInvoiceNumberRE = regexp.MustCompile(`ASC-\d{4}-\d{4}`)

func bxInvoiceNumber(page extraction.TokenPage) string {
	for _, tok := range page.Tokens {
		if m := bxInvoiceNumberRE.FindString(tok.Text); m != "" {
			return m
		}
	}
	return ""
}

// EXTR-19-01 QA. The three fixtures import under one business entity in EXTR-19-02's wired
// specs; a repeated number collides on invoices_tenant_entity_number_uq. The subtask chose
// 0919/0920/0921 for exactly this reason and pinned it nowhere.
func TestBoxlessFixtures_InvoiceNumbersDoNotCollide(t *testing.T) {
	seen := map[string]string{}
	for _, golden := range []string{dxGolden, bxInlineGolden, bxStackedGolden} {
		num := bxInvoiceNumber(bxPage1(t, golden))
		if num == "" {
			t.Fatalf("%s prints no ASC-YYYY-NNNN invoice number; the uniqueness assertion below would compare empty strings", golden)
		}
		if prior, dup := seen[num]; dup {
			t.Errorf("%s and %s both print invoice number %s; importing both under one entity violates invoices_tenant_entity_number_uq", prior, golden, num)
		}
		seen[num] = golden
	}
	if len(seen) != 3 {
		t.Errorf("the three fixtures yield %d distinct invoice number(s) %v, want 3", len(seen), seen)
	}
}

// --- EXTR-19-02: BoxlessFingerprint and its namespace --------------------------------------
//
// Written Mode A: red against fingerprint.go's stub until Stage 3 implements
// BoxlessFingerprint and labelPlacement. Every equality spec here first asserts the value is
// 67 bytes and is not the zero-observation value, so the stub's "" cannot satisfy one
// vacuously -- the same guard EXTR-04-03's own red commit needed on Fingerprint.

// bxColumnCapBytes is layout_fingerprint's CHECK on both extraction_anchor_rules and
// extraction_jobs. A boxless value must fit it, which is why this story ships no migration.
const bxColumnCapBytes = 128

// bxZeroTok is a page-1 token at the zero box -- every DOCX token, and the whole reason a
// second identity function exists.
func bxZeroTok(text string) extraction.Token {
	return extraction.Token{Text: text, Region: extraction.Region{Page: 1}}
}

// bxElements rebuilds the element list BoxlessFingerprint hashes, through the production
// classifier. Failure messages and fixture preconditions only; never compared to a digest.
func bxElements(page extraction.TokenPage) []string {
	out := make([]string, 0, len(page.Tokens))
	for _, tk := range page.Tokens {
		out = append(out, extraction.AnchorLabelPlacementsForTest(tk.Text)...)
	}
	return out
}

// bxRealValue fails unless fp is a usable boxless fingerprint. Run before every equality and
// every difference assertion below.
func bxRealValue(t *testing.T, role, fp string) {
	t.Helper()
	want := extraction.BoxlessFingerprintVersion + ":"
	if len(fp) != 67 || !strings.HasPrefix(fp, want) {
		t.Fatalf("BoxlessFingerprint(%s) = %q (%d bytes), want 67 bytes prefixed %q", role, fp, len(fp), want)
	}
	if fp == bxEmptyFingerprint {
		t.Fatalf("BoxlessFingerprint(%s) = %q, the zero-observation value -- that input matched no anchor label, so any assertion over it proves nothing", role, fp)
	}
}

func bxDeepCopy(p extraction.TokenPage) extraction.TokenPage {
	out := p
	out.Tokens = make([]extraction.Token, len(p.Tokens))
	copy(out.Tokens, p.Tokens)
	return out
}

func bxDistinct(in []string) int {
	seen := map[string]struct{}{}
	for _, s := range in {
		seen[s] = struct{}{}
	}
	return len(seen)
}

// AC-1. A and B carry the same three labels in the same order and collide under Fingerprint --
// TestBoxlessFixtures_CollideUnderTheGeometricFingerprint pins that collision. This story
// exists so that they do not collide here.
func TestBoxlessFingerprint_DiscriminatesTheStackedLayoutFromTheInlineOne(t *testing.T) {
	aPage := bxPage1(t, dxGolden)
	bPage := bxPage1(t, bxStackedGolden)

	fpA := extraction.BoxlessFingerprint(bxOnePage(aPage))
	fpB := extraction.BoxlessFingerprint(bxOnePage(bPage))
	bxRealValue(t, "A / "+dxGolden, fpA)
	bxRealValue(t, "B / "+bxStackedGolden, fpB)

	if fpA == fpB {
		t.Errorf("BoxlessFingerprint(A) = %q, BoxlessFingerprint(B) = %q, want different -- A's labels lead their values, B's stand alone.\n  A elements = %v\n  B elements = %v",
			fpA, fpB, bxElements(aPage), bxElements(bPage))
	}
}

// AC-2, Core AC-1's control. Same template, different number/date/total, one extra interior
// line item: a fingerprint that moved here would match no stored rule for a layout it has
// already seen.
func TestBoxlessFingerprint_MatchesTheSameTemplateWithDifferentData(t *testing.T) {
	aPage := bxPage1(t, dxGolden)
	apPage := bxPage1(t, bxInlineGolden)

	fpA := extraction.BoxlessFingerprint(bxOnePage(aPage))
	fpAP := extraction.BoxlessFingerprint(bxOnePage(apPage))
	bxRealValue(t, "A / "+dxGolden, fpA)
	bxRealValue(t, "A-prime / "+bxInlineGolden, fpAP)

	// The control must differ as a document, or the equality is trivially true.
	if slices.Equal(bxTexts(aPage), bxTexts(apPage)) {
		t.Fatalf("A and A-prime carry identical page-1 token texts %q; the equality below proves nothing", bxTexts(aPage))
	}

	if fpA != fpAP {
		t.Errorf("BoxlessFingerprint(A) = %q, BoxlessFingerprint(A-prime) = %q, want equal -- only the values beside the labels and one non-anchor line item differ.\n  A       elements = %v\n  A-prime elements = %v",
			fpA, fpAP, bxElements(aPage), bxElements(apPage))
	}
}

// AC-3, Core AC-2. Purity over token texts and page numbers: two independent decodes of one
// golden, and a deep copy of one of them, agree.
func TestBoxlessFingerprint_IsStableAcrossReads(t *testing.T) {
	first := bxPage1(t, dxGolden)
	second := bxPage1(t, dxGolden) // a second httptest server and a second decode

	if len(first.Tokens) == 0 || len(second.Tokens) == 0 {
		t.Fatalf("read 1 carries %d token(s), read 2 carries %d; want both non-empty", len(first.Tokens), len(second.Tokens))
	}
	// Independence asserted, not assumed: two aliases of one slice agree for the wrong reason.
	if &first.Tokens[0] == &second.Tokens[0] {
		t.Fatal("the two reads share one backing array, so they are not independent values")
	}
	third := bxDeepCopy(first)
	if &third.Tokens[0] == &first.Tokens[0] {
		t.Fatal("bxDeepCopy returned an alias of its input")
	}

	fp := extraction.BoxlessFingerprint(bxOnePage(first))
	bxRealValue(t, "read 1 of "+dxGolden, fp)

	for _, c := range []struct{ name, got string }{
		{"a second independent read", extraction.BoxlessFingerprint(bxOnePage(second))},
		{"a deep copy of read 1", extraction.BoxlessFingerprint(bxOnePage(third))},
	} {
		if c.got != fp {
			t.Errorf("BoxlessFingerprint(%s) = %q, want %q", c.name, c.got, fp)
		}
	}
}

// bxFillerTokens builds n page-1 tokens cycling two anchor-bearing paragraphs and one that
// trips no pattern. The element list is long on purpose; the digest still has to be 64 hex.
func bxFillerTokens(n int) []extraction.Token {
	out := make([]extraction.Token, n)
	for i := range out {
		switch i % 3 {
		case 0:
			out[i] = bxZeroTok(fmt.Sprintf("Invoice No: X-%d", i))
		case 1:
			out[i] = bxZeroTok(fmt.Sprintf("Total: %d", i))
		default:
			out[i] = bxZeroTok(fmt.Sprintf("Widget %d shipped in a crate", i))
		}
	}
	return out
}

// AC-4, D-AC-1. The value is written to the existing layout_fingerprint column, so its width
// is a schema fact, not a style choice.
func TestBoxlessFingerprint_IsVersionPrefixedAndFitsTheColumnCap(t *testing.T) {
	hexOnly := regexp.MustCompile(`^[0-9a-f]{64}$`)

	three := []extraction.Token{
		bxZeroTok("Invoice No: ASC-2026-0919"),
		bxZeroTok("Issue Date: 14 Aug 2026"),
		bxZeroTok("Total: NGN 4,300.00"),
	}
	big := bxFillerTokens(200)

	// The wide case must really hash a long element list, or fixed width is only ever proved
	// on short input.
	if got := len(bxElements(extraction.TokenPage{Number: 1, Tokens: big})); got < 100 {
		t.Fatalf("the 200-token page yields %d hashed element(s), want at least 100", got)
	}

	cases := []struct {
		name  string
		pages []extraction.TokenPage
	}{
		{"an empty page 1", []extraction.TokenPage{{Number: 1}}},
		{"a 3-token page", []extraction.TokenPage{{Number: 1, Tokens: three}}},
		{"a 200-token page", []extraction.TokenPage{{Number: 1, Tokens: big}}},
	}

	got := make([]string, 0, len(cases))
	for _, c := range cases {
		fp := extraction.BoxlessFingerprint(c.pages)
		got = append(got, fp)

		if len(fp) != 67 {
			t.Errorf("BoxlessFingerprint(%s) = %q, %d bytes, want exactly 67", c.name, fp, len(fp))
			continue
		}
		if len(fp) > bxColumnCapBytes {
			t.Errorf("BoxlessFingerprint(%s) is %d bytes, over layout_fingerprint's %d-byte CHECK", c.name, len(fp), bxColumnCapBytes)
		}
		if !strings.HasPrefix(fp, "b1:") {
			t.Errorf("BoxlessFingerprint(%s) = %q, want it to start with \"b1:\"", c.name, fp)
			continue
		}
		if h := strings.TrimPrefix(fp, "b1:"); !hexOnly.MatchString(h) {
			t.Errorf("BoxlessFingerprint(%s) hex part = %q (%d chars), want 64 lowercase hex characters", c.name, h, len(h))
		}
	}

	// Fixed width must not come from a fixed value.
	if n := bxDistinct(got); n != len(cases) {
		t.Errorf("the %d inputs yield %d distinct fingerprint(s) %q, want %d -- a constant return is also 67 bytes", len(cases), n, got, len(cases))
	}
}

// AC-4, D-AC-2, the namespace claim. The cross product below is a backstop, not the proof: two
// unequal digests never collide however the versions are spelled. What holds for EVERY input
// is the prefix pair -- each function stamps its own version and the two versions differ -- so
// this test fails if either constant moves or one is ever derived from the other.
func TestBoxlessFingerprint_CanNeverEqualAGeometricFingerprint(t *testing.T) {
	if extraction.BoxlessFingerprintVersion != "b1" {
		t.Errorf("BoxlessFingerprintVersion = %q, want %q", extraction.BoxlessFingerprintVersion, "b1")
	}
	if extraction.FingerprintVersion != "v1" {
		t.Errorf("FingerprintVersion = %q, want %q", extraction.FingerprintVersion, "v1")
	}
	if extraction.BoxlessFingerprintVersion == extraction.FingerprintVersion {
		t.Fatalf("both versions are %q; the two namespaces have merged and every assertion below is meaningless", extraction.FingerprintVersion)
	}

	boxed := headerTokens()
	stacked := []extraction.Token{bxZeroTok("Invoice No"), bxZeroTok("Issue Date"), bxZeroTok("Total")}
	inline := []extraction.Token{bxZeroTok("Invoice No: A-1"), bxZeroTok("Issue Date: 14 Aug 2026"), bxZeroTok("Total: 1.00")}

	inputs := []struct {
		name  string
		pages []extraction.TokenPage
	}{
		{"nil", nil},
		{"no pages", []extraction.TokenPage{}},
		{"an empty page 1", []extraction.TokenPage{{Number: 1}}},
		{"a page 1 matching no pattern", []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{bxZeroTok("xyzzy plugh")}}}},
		{"a boxed header", []extraction.TokenPage{{Number: 1, Tokens: boxed}}},
		{"a boxless stacked page", []extraction.TokenPage{{Number: 1, Tokens: stacked}}},
		{"a boxless inline page", []extraction.TokenPage{{Number: 1, Tokens: inline}}},
		{"page 2 first, then a boxed page 1", []extraction.TokenPage{{Number: 2, Tokens: stacked}, {Number: 1, Tokens: boxed}}},
	}
	if len(inputs) != 8 {
		t.Fatalf("this test spans %d input(s), want 8", len(inputs))
	}

	bx := make([]string, 0, len(inputs))
	geo := make([]string, 0, len(inputs))
	for _, in := range inputs {
		b := extraction.BoxlessFingerprint(in.pages)
		g := extraction.Fingerprint(in.pages)

		if len(b) != 67 || !strings.HasPrefix(b, extraction.BoxlessFingerprintVersion+":") {
			t.Errorf("BoxlessFingerprint(%s) = %q (%d bytes), want 67 bytes prefixed %q", in.name, b, len(b), extraction.BoxlessFingerprintVersion+":")
		}
		if len(g) != 67 || !strings.HasPrefix(g, extraction.FingerprintVersion+":") {
			t.Errorf("Fingerprint(%s) = %q (%d bytes), want 67 bytes prefixed %q", in.name, g, len(g), extraction.FingerprintVersion+":")
		}
		bx = append(bx, b)
		geo = append(geo, g)
	}

	// Eight inputs, not one input eight times.
	if n := bxDistinct(bx); n < 2 {
		t.Errorf("the eight inputs yield %d distinct boxless value(s) %q; the cross product below compares one value against itself", n, bx)
	}
	if n := bxDistinct(geo); n < 2 {
		t.Errorf("the eight inputs yield %d distinct geometric value(s) %q", n, geo)
	}

	for i, b := range bx {
		for j, g := range geo {
			if b == g {
				t.Errorf("BoxlessFingerprint(%s) = %q equals Fingerprint(%s); the two namespaces collide", inputs[i].name, b, inputs[j].name)
			}
		}
	}
}

// --- EXTR-19-02 QA (Mode B) -----------------------------------------------------------------

// bxFixturePinned is the boxless value of each committed DOCX golden, derived independently of
// the implementation from the golden's page-1 texts and anchorLexicon. Everything downstream
// (EXTR-19-04/05 rule lookups) keys on these, so a moved value is a rule-invalidation event.
var bxFixturePinned = []struct{ golden, want string }{
	{dxGolden, "b1:8e005c5c3eec09db6aae241a1709eb15f13a9c237aeb7f115e35df922249d8af"},
	{bxInlineGolden, "b1:8e005c5c3eec09db6aae241a1709eb15f13a9c237aeb7f115e35df922249d8af"},
	{bxStackedGolden, "b1:e82dee0d7804a84fd98841b2b133c0b5f7f677f91db9c239b154b8ca1a33256f"},
}

// AC-1/AC-2 as absolute values. The relational specs above say A != B and A == A-prime; they
// hold under any implementation that moves all three together. This one does not.
func TestBoxlessFingerprint_IsUnchangedByTheCommittedFixtures(t *testing.T) {
	if len(bxFixturePinned) != 3 {
		t.Fatalf("bxFixturePinned pins %d fixture(s), want 3", len(bxFixturePinned))
	}
	for _, c := range bxFixturePinned {
		got := extraction.BoxlessFingerprint(bxOnePage(bxPage1(t, c.golden)))
		if got != c.want {
			t.Errorf("BoxlessFingerprint(%s) = %q, want %q -- the boxless identity moved, which invalidates every stored b1: rule and needs a BoxlessFingerprintVersion bump.\n  elements = %v",
				c.golden, got, c.want, bxElements(bxPage1(t, c.golden)))
		}
	}
}
