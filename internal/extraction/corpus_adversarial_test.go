// corpus_adversarial_test.go: what corpus_test.go's C-01..C-09 do not reach -- the geometry
// each layout exists to exercise, and whether the committed bytes still carry the values
// corpusExpect claims for them.
package extraction_test

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// corpusTokenFloor is the token count each layout read at when it was committed. A floor, not
// an equality: a finer-splitting pdfium is not a regression, an almost-empty PDF is.
var corpusTokenFloor = map[string]int{
	"corpus_inline_labels.pdf":  11,
	"corpus_split_labels.pdf":   21,
	"corpus_stacked_labels.pdf": 13,
	"corpus_two_column.pdf":     10,
	"corpus_ambiguous_date.pdf": 6,
	"corpus_totals_block.pdf":   9,
}

// corpusFieldShape maps a HeaderFields key to the normaliser that reads its value.
var corpusFieldShape = map[string]extraction.Shape{
	"invoice_number": extraction.ShapeInvoiceNumber,
	"issue_date":     extraction.ShapeDate,
	"supplier_tin":   extraction.ShapeTIN,
	"supplier_name":  extraction.ShapeName,
	"buyer_tin":      extraction.ShapeTIN,
	"buyer_name":     extraction.ShapeName,
	"currency":       extraction.ShapeCurrency,
	"subtotal":       extraction.ShapeAmount,
	"vat":            extraction.ShapeAmount,
	"total":          extraction.ShapeAmount,
}

// corpusReservedTIN is the suffix range no fixture may carry: mock_script.go's scripted
// outcomes and its never-allocate pair.
var corpusReservedTIN = regexp.MustCompile(`99999999-000[1-9]`)

// --- harness ----------------------------------------------------------------

func corpusTokens(t *testing.T, name string) []extraction.Token {
	t.Helper()
	pages, _ := ptRead(t, name)
	toks := ptTokens(pages)
	if len(toks) == 0 {
		t.Fatalf("%s yielded no token; every assertion over it would report nothing", name)
	}
	return toks
}

// corpusToken finds the one token whose trimmed text is want. pdfium appends a trailing space
// to a rect followed by another on the same line, so the compare is trimmed.
func corpusToken(t *testing.T, toks []extraction.Token, want string) extraction.Token {
	t.Helper()

	var hits []extraction.Token
	for _, tok := range toks {
		if strings.TrimSpace(tok.Text) == want {
			hits = append(hits, tok)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("found %d token(s) reading %q, want exactly 1", len(hits), want)
	}
	return hits[0]
}

// corpusSupports reports whether any contiguous run of words in any token normalises to want.
// Presence in the document, not resolution by a rule -- EXTR-04-09 owns that.
func corpusSupports(toks []extraction.Token, shape extraction.Shape, want string) bool {
	for _, tok := range toks {
		words := strings.Fields(tok.Text)
		for i := range words {
			for j := i + 1; j <= len(words); j++ {
				for _, got := range shape.Normalize(strings.Join(words[i:j], " ")) {
					if got == want {
						return true
					}
				}
			}
		}
	}
	return false
}

func corpusLabelIDs(text string) map[string]bool {
	out := map[string]bool{}
	for _, id := range extraction.AnchorLabelIDsForTest(text) {
		out[id] = true
	}
	return out
}

// --- the tests --------------------------------------------------------------

// corpus_two_column.pdf exists to put its two party blocks in different column bands. Without
// this, x is a comment: at x=340 the buyer block centres at 0.58/0.64 and lands in the MIDDLE
// band, and every C-0N spec stays green.
func TestCorpus_TwoColumnPartiesLandInTheOuterBands(t *testing.T) {
	corpusRequireCommitted(t)

	toks := corpusTokens(t, "corpus_two_column.pdf")
	supplier := extraction.ColumnBandForTest(corpusToken(t, toks, "Supplier").Region)
	buyer := extraction.ColumnBandForTest(corpusToken(t, toks, "Buyer").Region)

	if supplier != 0 {
		t.Errorf("the supplier label is in column band %d, want 0 (left third)", supplier)
	}
	if buyer != 2 {
		t.Errorf("the buyer label is in column band %d, want 2 (right third) -- at x=340 it centres inside the middle third and the layout stops separating the parties by band", buyer)
	}
	if supplier == buyer {
		t.Errorf("both party labels are in column band %d; the layout's whole purpose is that they differ", supplier)
	}

	// The corpus-wide half: two_column is the only layout reaching the right third at all, so
	// a buyer block that drifts left is caught here even if the two assertions above are kept
	// in step with it.
	reach := map[string]bool{}
	for _, name := range corpusLayouts {
		for _, tok := range corpusTokens(t, name) {
			if len(extraction.AnchorLabelIDsForTest(tok.Text)) > 0 && extraction.ColumnBandForTest(tok.Region) == 2 {
				reach[name] = true
			}
		}
	}
	if len(reach) != 1 || !reach["corpus_two_column.pdf"] {
		t.Errorf("anchor labels reach column band 2 in %v, want exactly {corpus_two_column.pdf}", reach)
	}
}

// C-06 accepts one non-empty token per file, which an almost-empty PDF also passes.
func TestCorpus_EachLayoutMeetsItsTokenFloor(t *testing.T) {
	corpusRequireCommitted(t)

	if len(corpusTokenFloor) != len(corpusLayouts) {
		t.Fatalf("corpusTokenFloor covers %d layout(s), corpusLayouts names %d", len(corpusTokenFloor), len(corpusLayouts))
	}
	for _, name := range corpusLayouts {
		floor, ok := corpusTokenFloor[name]
		if !ok {
			t.Errorf("%s has no token floor", name)
			continue
		}
		if got := len(corpusTokens(t, name)); got < floor {
			t.Errorf("%s read as %d token(s), want at least %d", name, got, floor)
		}
	}
}

// D-10: the fixture must really be ambiguous, and the two values in its row must be the two
// readings the production shape returns -- not two dates someone typed.
func TestCorpus_AmbiguousDateCarriesBothReadings(t *testing.T) {
	corpusRequireCommitted(t)

	const file, literal = "corpus_ambiguous_date.pdf", "12/03/2026"

	var row map[string][]string
	twoValued := map[string]int{}
	for _, want := range corpusExpect {
		if want.file == file {
			row = want.fields
		}
		for _, values := range want.fields {
			if len(values) > 1 {
				twoValued[want.file]++
			}
		}
	}
	if row == nil {
		t.Fatalf("%s has no corpusExpect row; the assertions below would run over nothing", file)
	}
	if len(twoValued) != 1 || twoValued[file] != 1 {
		t.Errorf("rows carrying a multi-value field: %v, want exactly one in %s", twoValued, file)
	}

	found := false
	for _, tok := range corpusTokens(t, file) {
		if strings.Contains(tok.Text, literal) {
			found = true
		}
	}
	if !found {
		t.Errorf("%s carries no token containing %q; the ambiguity it exists for is not in the bytes", file, literal)
	}

	readings := extraction.ShapeDate.Normalize(literal)
	if len(readings) != 2 {
		t.Fatalf("ShapeDate.Normalize(%q) returned %v, want two readings", literal, readings)
	}
	if readings[0] == readings[1] {
		t.Fatalf("ShapeDate.Normalize(%q) returned the same reading twice: %v", literal, readings)
	}
	got := map[string]bool{}
	for _, r := range row["issue_date"] {
		got[r] = true
	}
	if len(got) != 2 {
		t.Fatalf("%s expects %d issue_date value(s), want 2", file, len(got))
	}
	for _, r := range readings {
		if !got[r] {
			t.Errorf("%s does not expect issue_date %q, which ShapeDate returns for %q", file, r, literal)
		}
	}
}

// The stacked layout's values must sit BELOW their labels and overlap them horizontally, and
// no label may reach the next group sooner than its own values.
func TestCorpus_StackedValuesSitBelowTheirLabels(t *testing.T) {
	corpusRequireCommitted(t)

	groups := []struct {
		label  string
		values []string
	}{
		{"Invoice No", []string{"INV-1003"}},
		{"Supplier", []string{"Adeyemi Trading Limited", "99999999-0301"}},
		{"Invoice Date", []string{"22 Apr 2026"}},
		{"Buyer", []string{"Honeywell Group", "99999999-0302"}},
		{"Total", []string{"NGN 3,225.00"}},
	}

	toks := corpusTokens(t, "corpus_stacked_labels.pdf")
	maxIntra, minNext := 0.0, 0.0
	for gi, g := range groups {
		label := corpusToken(t, toks, g.label)
		if len(g.values) == 0 {
			t.Fatalf("group %q names no value", g.label)
		}
		for _, v := range g.values {
			value := corpusToken(t, toks, v)
			// Region is top-left origin, so below means a larger Y.
			if value.Region.Y0 <= label.Region.Y1 {
				t.Errorf("%q (Y0 %.4f) does not sit below %q (Y1 %.4f)", v, value.Region.Y0, g.label, label.Region.Y1)
				continue
			}
			if value.Region.X0 >= label.Region.X1 || value.Region.X1 <= label.Region.X0 {
				t.Errorf("%q does not overlap %q horizontally; below needs the same column", v, g.label)
			}
			if gap := value.Region.Y0 - label.Region.Y1; gap > maxIntra {
				maxIntra = gap
			}
		}
		if gi+1 < len(groups) {
			next := corpusToken(t, toks, groups[gi+1].label)
			gap := next.Region.Y0 - label.Region.Y1
			if minNext == 0 || gap < minNext {
				minNext = gap
			}
		}
	}

	if maxIntra == 0 || minNext == 0 {
		t.Fatalf("measured maxIntra=%.4f minNext=%.4f; a zero means the loop above asserted nothing", maxIntra, minNext)
	}
	if minNext <= 3*maxIntra {
		t.Errorf("a label reaches its own group's values within %.4f and the next group's label at %.4f, a %.1fx margin -- want above 3x, or a below rule cannot tell the two apart",
			maxIntra, minNext, minNext/maxIntra)
	}
}

// D-9's deliberate overlap: "Sub-total" matches subtotal AND \btotal\b, so one label mints a
// candidate for two fields. The lexicon comes from production, not a copy.
func TestCorpus_TotalsBlockCarriesTheLexiconOverlap(t *testing.T) {
	corpusRequireCommitted(t)

	toks := corpusTokens(t, "corpus_totals_block.pdf")
	ids := corpusLabelIDs(corpusToken(t, toks, "Sub-total").Text)
	if len(ids) == 0 {
		t.Fatal(`"Sub-total" matches no lexicon entry at all; the overlap assertion below would report nothing`)
	}
	for _, want := range []string{"subtotal", "total"} {
		if !ids[want] {
			t.Errorf(`"Sub-total" does not match the %s lexicon entry; it matches %v`, want, ids)
		}
	}

	// The VAT label must stay bare: a "7.5%" remainder would mint a spurious amount candidate.
	if vat := corpusToken(t, toks, "VAT"); !corpusLabelIDs(vat.Text)["vat"] {
		t.Errorf(`the VAT label reads %q and does not match the vat lexicon entry`, vat.Text)
	}
}

// C-07 reads the text layer. This reads the bytes, so a reserved TIN in metadata or in a
// stream the reader skips is caught too.
func TestCorpus_CarriesNoReservedTINInItsBytes(t *testing.T) {
	corpusRequireCommitted(t)

	if !corpusReservedTIN.MatchString("99999999-0007") {
		t.Fatal("corpusReservedTIN does not match a reserved TIN; the absence scan below would find nothing either way")
	}
	for _, name := range corpusLayouts {
		if hits := corpusReservedTIN.FindAllString(string(fxRead(t, name)), -1); len(hits) > 0 {
			t.Errorf("%s carries %v in its bytes -- internal/submission/mock_script.go:76-93 reserves those", name, hits)
		}
	}
}

// Six entries that produce the same PDF, or the same fingerprint, are one layout six times.
func TestCorpus_LayoutsAreDistinct(t *testing.T) {
	corpusRequireCommitted(t)

	if len(corpusLayouts) < 2 {
		t.Fatalf("corpusLayouts names %d layout(s); distinctness over fewer than 2 asserts nothing", len(corpusLayouts))
	}
	bytesBy, fpBy := map[string]string{}, map[string]string{}
	for _, name := range corpusLayouts {
		sum := fmt.Sprintf("%x", sha256.Sum256(fxRead(t, name)))
		if other, dup := bytesBy[sum]; dup {
			t.Errorf("%s is byte-identical to %s", name, other)
		}
		bytesBy[sum] = name

		var pages []extraction.TokenPage
		if _, err := extraction.NewPDFiumReader().Read(t.Context(), ptDoc(t, name), extraction.CollectTokens(&pages)); err != nil {
			t.Fatalf("Read(%s): %v", name, err)
		}
		fp := extraction.Fingerprint(pages)
		if other, dup := fpBy[fp]; dup {
			t.Errorf("%s fingerprints to %s, the same as %s -- one stored rule set would serve both", name, fp, other)
		}
		fpBy[fp] = name
	}
}

// The one thing C-01..C-09 cannot see: a generator whose value drifts while corpusExpect keeps
// the old one. Presence in the bytes, not resolution by a rule.
func TestCorpus_EveryExpectedValueAppearsInItsFixture(t *testing.T) {
	corpusRequireCommitted(t)

	for _, field := range extraction.HeaderFields {
		if _, ok := corpusFieldShape[field]; !ok {
			t.Fatalf("corpusFieldShape has no shape for %q; its expectations would go unchecked", field)
		}
	}

	checked := 0
	for _, want := range corpusExpect {
		t.Run(want.file, func(t *testing.T) {
			toks := corpusTokens(t, want.file)
			for field, values := range want.fields {
				shape, ok := corpusFieldShape[field]
				if !ok {
					t.Errorf("no shape for %q", field)
					continue
				}
				if len(values) == 0 {
					t.Errorf("%q expects no value", field)
					continue
				}
				for _, value := range values {
					checked++
					if !corpusSupports(toks, shape, value) {
						t.Errorf("no token normalises to %q for %s under %s -- the generator and corpusExpect have drifted apart", value, field, shape)
					}
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("checked no value at all; corpusExpect carries nothing to compare")
	}
}

// C-02 builds each layout twice, which catches map-order non-determinism about 30% of the
// time (measured: 6 of 20 runs). Repeating the build raises that to 20 of 20 without touching
// C-02. Corpus entries only -- the raster fixtures are slow and are not this subtask's.
func TestCorpus_GeneratorsStayDeterministicUnderRepetition(t *testing.T) {
	const builds = 64

	layouts := 0
	for _, f := range fxCorpus {
		if !strings.HasPrefix(f.name, corpusPrefix) {
			continue
		}
		layouts++
		t.Run(f.name, func(t *testing.T) {
			first := f.build()
			if len(first) == 0 {
				t.Fatalf("%s built 0 bytes", f.name)
			}
			for i := 2; i <= builds; i++ {
				if got := f.build(); string(got) != string(first) {
					t.Fatalf("%s built %d byte(s) on pass 1 and %d on pass %d", f.name, len(first), len(got), i)
				}
			}
		})
	}
	if layouts == 0 {
		t.Fatalf("fxCorpus carries no %s-prefixed entry; the repetition above built nothing", corpusPrefix)
	}
}
