// tier1_adversarial_test.go: coverage G-01..G-16 leave open. Two mutants survived the whole
// package: disabling every `below` rule, and shipping a rule keyed `.below` that carries
// RelRight. The first is closed here, the second in tier1_internal_test.go.
//
// Same two rules bind every spec: a quantifier over Resolve's output carries a floor first, and
// an asserted zero carries a positive control in the same test.
package extraction_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- harness ----------------------------------------------------------------

// t1aGaps are the corpusExpect pairs Tier-1 cannot reach. Asserted STILL missing, so closing one
// is a deliberate diff rather than a silent pass. Empty since EXTR-22-02 bound each party's TIN
// to its own heading; TestWildLayouts_DoNotEnterTheCorpusRatchets still bounds the declaration.
var t1aGaps = []struct{ file, field string }{}

func t1aIsGap(file, field string) bool {
	for _, g := range t1aGaps {
		if g.file == file && g.field == field {
			return true
		}
	}
	return false
}

// t1aWithoutRelation is the shipped set minus every rule using kind.
func t1aWithoutRelation(kind extraction.RelationKind) []extraction.Tier1Rule {
	out := make([]extraction.Tier1Rule, 0, len(extraction.Tier1Rules))
	for _, r := range extraction.Tier1Rules {
		if r.Rule.Relation.Kind == kind {
			continue
		}
		out = append(out, r)
	}
	return out
}

// t1aTIN is a bare TIN token: the sweep pattern matches it whole, so the label IS the value.
func t1aTIN(page int, text string, y0, y1 float64) extraction.Token {
	return extraction.Token{Text: text, Region: extraction.Region{Page: page, X0: 0.10, Y0: y0, X1: 0.30, Y1: y1}}
}

func t1aPage(number int, tokens ...extraction.Token) extraction.TokenPage {
	return extraction.TokenPage{Number: number, WidthPt: 612, HeightPt: 792, Tokens: tokens}
}

// --- the specs --------------------------------------------------------------

// AC-4's breadth oracle. G-04 checks three fields on one layout; corpusExpect is 44 pairs across
// six, and it is the table that says what Tier-1 must produce.
func TestTier1_ReachesEveryCorpusExpectation(t *testing.T) {
	t1Floor(t)
	if len(corpusExpect) != len(corpusLayouts) {
		t.Fatalf("corpusExpect holds %d row(s) and the corpus %d layout(s); the sweep below would miss a layout", len(corpusExpect), len(corpusLayouts))
	}

	checked, missed := 0, 0
	for _, want := range corpusExpect {
		got := t1Shipped(t, want.file)
		rvFloor(t, got, "the shipped Tier-1 set over "+want.file)
		if len(want.fields) == 0 {
			t.Fatalf("%s expects no field at all; its row measures nothing", want.file)
		}

		// HeaderFields, not a range over the map: the report order has to be stable.
		for _, field := range extraction.HeaderFields {
			values, ok := want.fields[field]
			if !ok {
				continue
			}
			if len(values) == 0 {
				t.Fatalf("%s expects %s with no value; the reach test below would pass over nothing", want.file, field)
			}
			checked++

			reached := false
			for _, v := range values {
				if slices.Contains(rvValues(rvFor(got, field)), v) {
					reached = true
				}
			}
			switch {
			case !reached && !t1aIsGap(want.file, field):
				missed++
				t.Errorf("%s: %s reached none of %v; Tier-1 alone must fill it (AC-5). got %v", want.file, field, values, rvValues(rvFor(got, field)))
			case reached && t1aIsGap(want.file, field):
				t.Errorf("%s: %s now reaches %v; the recorded gap is closed -- drop it from t1aGaps", want.file, field, values)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no expectation was checked; corpusExpect names no field in HeaderFields")
	}
	// Without this the exception list could be padded until nothing is asserted.
	if missed != 0 {
		t.Errorf("%d expectation(s) unreached beyond the %d recorded gap(s)", missed, len(t1aGaps))
	}
	for _, g := range t1aGaps {
		found := false
		for _, want := range corpusExpect {
			if want.file == g.file {
				_, found = want.fields[g.field]
			}
		}
		if !found {
			t.Errorf("t1aGaps names %s/%s, which corpusExpect does not expect; the exception excuses nothing", g.file, g.field)
		}
	}
}

// The set gives every field all three relations because the corpus needs all three. Removing
// every `below` rule left all 101 specs in this package green before this one existed.
func TestTier1_EveryRelationIsLoadBearing(t *testing.T) {
	t1Floor(t)

	cases := []struct {
		kind              extraction.RelationKind
		file, field, want string
	}{
		{extraction.RelSameToken, "corpus_inline_labels.pdf", "invoice_number", "INV-1001"},
		{extraction.RelRight, "corpus_split_labels.pdf", "issue_date", "2026-04-15"},
		{extraction.RelBelow, "corpus_stacked_labels.pdf", "invoice_number", "INV-1003"},
	}
	seen := make([]extraction.RelationKind, 0, len(cases))

	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			pages := rvCorpusPages(t, c.file)

			full := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
			if !slices.Contains(rvValues(rvFor(full, c.field)), c.want) {
				t.Fatalf("the full set does not reach %s = %q on %s; removing a relation would prove nothing", c.field, c.want, c.file)
			}

			without := t1aWithoutRelation(c.kind)
			if len(without) == len(extraction.Tier1Rules) {
				t.Fatalf("dropping %q removed no rule from the shipped set", c.kind)
			}

			cut := extraction.Resolve(pages, extraction.RuleSet{Tier1: without})
			rvControl(t, cut, "the shipped set minus every "+string(c.kind)+" rule over "+c.file)
			if slices.Contains(rvValues(rvFor(cut, c.field)), c.want) {
				t.Errorf("%s = %q survives with no %q rule left; that relation is not what reaches it", c.field, c.want, c.kind)
			}
		})
		seen = append(seen, c.kind)
	}

	for _, kind := range []extraction.RelationKind{extraction.RelSameToken, extraction.RelRight, extraction.RelBelow} {
		if !slices.Contains(seen, kind) {
			t.Errorf("no case covers %q; a relation could be dropped from the set unnoticed", kind)
		}
	}
}

// t1aSweepVsLabel is one page carrying both paths to supplier_tin and making them disagree: a
// bare TIN the sweep takes, and a labelled one for the label path. The page carries no party
// heading is the supplier's, so every candidate on it routes to supplier_tin. Synthetic
// because the corpus lost this control -- the disagreement there WAS the cross-party reading
// TestTier1_TheLabelPathNoLongerReachesTheOtherPartysTin now closes.
func t1aSweepVsLabel() []extraction.TokenPage {
	return []extraction.TokenPage{t1aPage(1,
		t1aTIN(1, "99999999-0501", 0.20, 0.27),
		rvTok("Supplier TIN", 0.10, 0.60, 0.30, 0.67),
		rvTok("99999999-0502", 0.35, 0.60, 0.55, 0.67),
	)}
}

// Two paths reach supplier_tin and they disagree. Both survive, and RuleID is what tells them
// apart -- picking between them is EXTR-05's.
func TestTier1_TheSweepAndTheLabelPathBothSurviveAndDisagree(t *testing.T) {
	got := rvFor(t1Shipped(t, t1Split), "supplier_tin")
	rvFloor(t, got, "supplier_tin on "+t1Split)

	if len(got) < 2 {
		t.Fatalf("supplier_tin carries %d candidate(s) %v; the disagreement needs at least two", len(got), rvValues(got))
	}
	if got[0].RuleID != "t1.tin.sweep" || got[0].Value != "99999999-0201" {
		t.Errorf("supplier_tin[0] = %q from %q, want 99999999-0201 from t1.tin.sweep at distance 0", got[0].Value, got[0].RuleID)
	}
	for _, c := range got {
		if c.RuleID == "" {
			t.Errorf("a supplier_tin candidate %q carries no RuleID; the surviving pair is indistinguishable downstream", c.Value)
		}
	}

	// The disagreement itself, on a page that still has one.
	synth := rvFor(extraction.Resolve(t1aSweepVsLabel(), extraction.RuleSet{Tier1: extraction.Tier1Rules}), "supplier_tin")
	rvFloor(t, synth, "supplier_tin on the sweep-versus-label page")

	// A slice per rule, not one value: the sweep is page-wide since EXTR-22-02 and reaches the
	// labelled row's value token as well as the bare one.
	byRule := make(map[string][]string, len(synth))
	for _, c := range synth {
		if c.RuleID == "" {
			t.Errorf("a supplier_tin candidate %q carries no RuleID; the surviving pair is indistinguishable downstream", c.Value)
		}
		byRule[c.RuleID] = append(byRule[c.RuleID], c.Value)
	}
	for _, want := range []struct{ ruleID, value string }{
		{"t1.tin.sweep", "99999999-0501"},
		{"t1.supplier_tin.right", "99999999-0502"},
	} {
		if !slices.Contains(byRule[want.ruleID], want.value) {
			t.Errorf("%s contributed %v, want it to hold %q; without both paths this spec pins no disagreement", want.ruleID, byRule[want.ruleID], want.value)
		}
	}
	// The label path reaches the bare TIN through no rule at all, so the two paths still
	// disagree about which token is the supplier's.
	if slices.Contains(byRule["t1.supplier_tin.right"], "99999999-0501") {
		t.Errorf("t1.supplier_tin.right contributed %v and reached the bare token too; the two paths no longer disagree", byRule["t1.supplier_tin.right"])
	}
}

// The supplier pattern's party word was optional before EXTR-22, so before EXTR-16 the supplier
// label read across the party split and collected the buyer's TIN. Closed by anchor specificity:
// on corpus_split_labels.pdf the buyer's own label claims the wider span of that token.
func TestTier1_TheLabelPathNoLongerReachesTheOtherPartysTin(t *testing.T) {
	t1Floor(t)

	got := t1Shipped(t, t1Split)
	rvFloor(t, got, "the shipped set over "+t1Split)
	for _, c := range rvFor(got, "supplier_tin") {
		if c.Value == "99999999-0202" {
			t.Errorf("supplier_tin carries the buyer's %q from %q; no rule may read across the party split", c.Value, c.RuleID)
		}
	}

	// Positive control: the buyer's TIN is on the page and IS reached -- as buyer_tin.
	if v := rvValues(rvFor(got, "buyer_tin")); !slices.Contains(v, "99999999-0202") {
		t.Fatalf("buyer_tin = %v on %s; the absence asserted above holds equally against a page that never carried the buyer's TIN", v, t1Split)
	}
}

// G-13 passes on ANY difference between the two orders. The difference is one named pair, and
// pinning it is what stops G-13 passing on a coincidence.
func TestTier1_ShippedOrderPutsBuyerTinBeforeSupplierName(t *testing.T) {
	t1Floor(t)

	var order []string
	for _, r := range extraction.Tier1Rules {
		if !slices.Contains(order, r.Field) {
			order = append(order, r.Field)
		}
	}
	for _, f := range []string{"buyer_tin", "supplier_name"} {
		if !slices.Contains(order, f) || !slices.Contains(extraction.HeaderFields, f) {
			t.Fatalf("%q is missing from the shipped order %v or from HeaderFields %v", f, order, extraction.HeaderFields)
		}
	}
	if slices.Index(order, "buyer_tin") > slices.Index(order, "supplier_name") {
		t.Errorf("the shipped set orders %v; the anchor lexicon puts buyer_tin BEFORE supplier_name", order)
	}
	if slices.Index(extraction.HeaderFields, "buyer_tin") < slices.Index(extraction.HeaderFields, "supplier_name") {
		t.Errorf("HeaderFields orders %v; the vocabulary puts supplier_name before buyer_tin, and that inversion is the whole divergence", extraction.HeaderFields)
	}
}

// --- the party block, where the page half used to be -------------------------

// t1aHeading is a party heading token, boxed for the page it sits on. Sibling of t1aTIN: rvTok
// hard-codes page 1 and cannot carry a page-2 heading.
func t1aHeading(page int, text string, y0, y1 float64) extraction.Token {
	return extraction.Token{Text: text, Region: extraction.Region{Page: page, X0: 0.10, Y0: y0, X1: 0.30, Y1: y1}}
}

// Two bare TINs in ONE page half, which the retired sweeps could never tell apart. Replaces
// TestTier1_TheSweepCannotSeparateTwoTINsInOnePageHalf, whose whole claim was that this is
// unfixed.
func TestTier1_TwoBareTINsInOnePageHalfSeparateByPartyBlock(t *testing.T) {
	t1Floor(t)

	const supplierTIN, buyerTIN = "99999999-0401", "99999999-0402"
	headed := []extraction.TokenPage{t1aPage(1,
		t1aHeading(1, "Supplier", 0.10, 0.13),
		t1aTIN(1, supplierTIN, 0.15, 0.18),
		t1aHeading(1, "Buyer", 0.22, 0.25),
		t1aTIN(1, buyerTIN, 0.27, 0.30),
	)}

	got := extraction.Resolve(headed, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, got, "two headed bare TINs in the top half under the shipped set")
	if v := rvValues(rvFor(got, "supplier_tin")); !slices.Equal(v, []string{supplierTIN}) {
		t.Errorf("supplier_tin = %v, want exactly [%s]; the Buyer heading owns the second TIN and the page half no longer decides", v, supplierTIN)
	}
	if v := rvValues(rvFor(got, "buyer_tin")); !slices.Equal(v, []string{buyerTIN}) {
		t.Errorf("buyer_tin = %v, want exactly [%s]", v, buyerTIN)
	}

	// The paired control: the same two TINs, same boxes, both headings dropped. Both fall to
	// the supplier by PartyUnknown fallback, so the split above is the headings' doing and not
	// the geometry's.
	headless := []extraction.TokenPage{t1aPage(1,
		t1aTIN(1, supplierTIN, 0.15, 0.18),
		t1aTIN(1, buyerTIN, 0.27, 0.30),
	)}
	ctl := extraction.Resolve(headless, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvControl(t, ctl, "the same two TINs with both headings dropped")
	if v := rvValues(rvFor(ctl, "supplier_tin")); !slices.Equal(v, []string{supplierTIN, buyerTIN}) {
		t.Errorf("with no heading supplier_tin = %v, want both TINs; PartyUnknown falls back to the supplier", v)
	}
	if v := rvValues(rvFor(ctl, "buyer_tin")); len(v) != 0 {
		t.Errorf("with no heading buyer_tin = %v, want none; no heading claims either token", v)
	}
}

// A bare "TIN:" label carries no party of its own, so the heading before it decides which field
// it fills -- and with no heading before it, the supplier's.
func TestTier1_ABareTINLabelBindsToTheHeadingBeforeIt(t *testing.T) {
	t1Floor(t)

	const supplierTIN, buyerTIN = "99999999-0801", "99999999-0802"
	pages := []extraction.TokenPage{t1aPage(1,
		rvTok("TIN:", 0.10, 0.10, 0.20, 0.13),
		rvTok(supplierTIN, 0.25, 0.10, 0.45, 0.13),
		t1aHeading(1, "Invoice to", 0.20, 0.23),
		rvTok("TIN:", 0.10, 0.25, 0.20, 0.28),
		rvTok(buyerTIN, 0.25, 0.25, 0.45, 0.28),
	)}

	got := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, got, "two bare TIN labels either side of a buyer heading")

	supplier := rvValues(rvFor(got, "supplier_tin"))
	buyer := rvValues(rvFor(got, "buyer_tin"))
	if len(supplier) == 0 || len(buyer) == 0 {
		t.Fatalf("supplier_tin = %v, buyer_tin = %v; each exclusion below needs its own field to hold something first", supplier, buyer)
	}
	if !slices.Contains(supplier, supplierTIN) || slices.Contains(supplier, buyerTIN) {
		t.Errorf("supplier_tin = %v, want it to hold %s and not %s; the label before the heading belongs to no party and falls back to the supplier", supplier, supplierTIN, buyerTIN)
	}
	if !slices.Contains(buyer, buyerTIN) || slices.Contains(buyer, supplierTIN) {
		t.Errorf("buyer_tin = %v, want it to hold %s and not %s; the label after \"Invoice to\" is the buyer's", buyer, buyerTIN, supplierTIN)
	}
}

// A heading owns nothing on the next page: partyOrder takes one TokenPage, which is what the
// retired page-1 banding used to guarantee. Replaces TestTier1_ABandedSweepIgnoresALaterPage.
func TestTier1_APartyHeadingOnPageOneClaimsNothingOnPageTwo(t *testing.T) {
	t1Floor(t)

	const p1Supplier, p1Buyer = "99999999-0301", "99999999-0302"
	const p2First, p2Second = "99999999-0303", "99999999-0304"
	page1 := t1aPage(1,
		t1aHeading(1, "Supplier", 0.10, 0.13),
		t1aTIN(1, p1Supplier, 0.15, 0.18),
		t1aHeading(1, "Buyer", 0.22, 0.25),
		t1aTIN(1, p1Buyer, 0.27, 0.30),
	)
	page2 := t1aPage(2, t1aTIN(2, p2First, 0.20, 0.23), t1aTIN(2, p2Second, 0.60, 0.63))

	got := extraction.Resolve([]extraction.TokenPage{page1, page2}, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, got, "a headed page 1 followed by a headingless page 2 under the shipped set")

	supplier := rvValues(rvFor(got, "supplier_tin"))
	for _, want := range []string{p1Supplier, p2First, p2Second} {
		if !slices.Contains(supplier, want) {
			t.Errorf("supplier_tin = %v, want it to hold %s; page 2 carries no heading, so both its TINs fall back to the supplier", supplier, want)
		}
	}
	if v := rvValues(rvFor(got, "buyer_tin")); !slices.Equal(v, []string{p1Buyer}) {
		t.Errorf("buyer_tin = %v, want exactly [%s]; page 1's Buyer heading reaches nothing on page 2", v, p1Buyer)
	}

	// The paired control: prepend a Buyer heading to page 2 and its two TINs move. Without it
	// the exclusion above holds equally against a page 2 nothing reads.
	page2Headed := t1aPage(2,
		t1aHeading(2, "Buyer", 0.10, 0.13),
		t1aTIN(2, p2First, 0.20, 0.23),
		t1aTIN(2, p2Second, 0.60, 0.63),
	)
	ctl := extraction.Resolve([]extraction.TokenPage{page1, page2Headed}, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvControl(t, ctl, "the same two pages with a Buyer heading on page 2")
	ctlBuyer := rvValues(rvFor(ctl, "buyer_tin"))
	for _, want := range []string{p2First, p2Second} {
		if !slices.Contains(ctlBuyer, want) {
			t.Errorf("with a Buyer heading on page 2, buyer_tin = %v, want it to hold %s; page 2's tokens are readable and only the page bound withheld them", ctlBuyer, want)
		}
	}
}

// --- the owning phrase, where the party word used to anchor --------------------

// t1aOwningPage is a party block laid out on wild_two_party_bare_tin.pdf's own vertical
// spacing: heading, owning phrase, name, signature. The phrase sits NEARER the name than the
// heading does, so a resolver that still reads it wins on distance.
func t1aOwningPage(heading, phrase, name, signature string) []extraction.TokenPage {
	return []extraction.TokenPage{t1aPage(1,
		rvTok(heading, 0.590020, 0.173465, 0.671471, 0.184556),
		rvTok(phrase, 0.589098, 0.193379, 0.707471, 0.204818),
		rvTok(name, 0.589745, 0.213581, 0.737843, 0.227975),
		rvTok(signature, 0.589686, 0.281763, 0.742196, 0.296247),
	)}
}

// "Customer No." and "Buyer's Signature" carry a party word but name no party: the whole phrase
// is the label. Neither the phrase nor the fragment the party word leaves behind may become a
// name, and the real name below the heading must rank first.
func TestTier1_ALabelFragmentIsNeverAPartyName(t *testing.T) {
	t1Floor(t)

	for _, arm := range []struct{ field, heading, phrase, name, signature string }{
		{"buyer_name", "Invoice to", "Customer No.", "Honeywell Group", "Buyer's Signature"},
		{"supplier_name", "Supplier", "Supplier No.", "Adeyemi Trading Limited", "Supplier's Signature"},
	} {
		got := extraction.Resolve(t1aOwningPage(arm.heading, arm.phrase, arm.name, arm.signature),
			extraction.RuleSet{Tier1: extraction.Tier1Rules})
		rvFloor(t, got, arm.field+" over a heading, an owning phrase, a name and a signature")

		vals := rvValues(rvFor(got, arm.field))
		if len(vals) == 0 || vals[0] != arm.name {
			t.Errorf("%s = %v, want %q at rank 0; the phrases around the name must not outrank it", arm.field, vals, arm.name)
		}
		for _, v := range vals {
			if strings.Contains(v, "No.") || strings.Contains(v, "Signature") {
				t.Errorf("%s reaches %q (%v); a fragment of an owning phrase is a label, never a name", arm.field, v, vals)
			}
		}
	}

	// The paired control: the same heading and name with both phrases dropped. Without it the
	// two absences above hold equally against a page shape nothing reads.
	ctl := extraction.Resolve([]extraction.TokenPage{t1aPage(1,
		rvTok("Invoice to", 0.590020, 0.173465, 0.671471, 0.184556),
		rvTok("Honeywell Group", 0.589745, 0.213581, 0.737843, 0.227975),
	)}, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvControl(t, ctl, "the same heading and name with both owning phrases dropped")
	if v := rvValues(rvFor(ctl, "buyer_name")); !slices.Contains(v, "Honeywell Group") {
		t.Errorf("with both phrases dropped buyer_name = %v, want it to hold the name; the geometry alone never reached it", v)
	}
}

// t1aBelowReach is the shipped below relation's max distance, read off the rule rather than
// respelled, so the geometry check below moves with the dial.
func t1aBelowReach(t *testing.T, key string) float64 {
	t.Helper()
	for _, r := range extraction.Tier1Rules {
		if r.Key == key {
			return r.Rule.Relation.MaxDistance
		}
	}
	t.Fatalf("Tier1Rules carries no %s; the reach below is read off nothing", key)
	return 0
}

// t1aOwningPage only discriminates while the phrase sits NEARER the name than the heading does:
// a resolver that still read the phrase would win on distance. Its own comment says so, and
// moving the phrase away leaves the arms above green, so this is what holds it.
func TestTier1_TheOwningPagePutsThePhraseNearestTheName(t *testing.T) {
	pages := t1aOwningPage("Invoice to", "Customer No.", "Honeywell Group", "Buyer's Signature")
	if len(pages) != 1 || len(pages[0].Tokens) != 4 {
		t.Fatalf("t1aOwningPage built %d page(s), want 1; the token indices below name other tokens", len(pages))
	}
	heading, phrase, name := pages[0].Tokens[0], pages[0].Tokens[1], pages[0].Tokens[2]

	phraseGap, headingGap := name.Region.Y0-phrase.Region.Y1, name.Region.Y0-heading.Region.Y1
	if phraseGap <= 0 || headingGap <= 0 {
		t.Fatalf("phrase gap %v, heading gap %v; both must sit above the name or neither reaches it below", phraseGap, headingGap)
	}
	if phraseGap >= headingGap {
		t.Errorf("the phrase is %v from the name and the heading %v; the phrase must be the NEARER anchor or the page stops reproducing the ranking it was measured from", phraseGap, headingGap)
	}
	if reach := t1aBelowReach(t, "t1.buyer_name.below"); headingGap > reach {
		t.Errorf("the heading is %v from the name, past the below relation's %v; the surviving anchor could not reach the name and the arms above would rank nothing", headingGap, reach)
	}
}

// --- the owning phrase, where the amount label used to anchor -------------------

// t1aBesideItsValue is a label and its value on one baseline. The value's left edge is fixed, so
// a longer label sits CLOSER to it than a bare one -- which is what lets a registration phrase
// beat every other reading on distance.
func t1aBesideItsValue(label string, labelX1 float64, value string) []extraction.TokenPage {
	return rvPage(
		rvTok(label, 0.10, 0.30, labelX1, 0.315),
		rvTok(value, 0.36, 0.30, 0.46, 0.315),
	)
}

// t1aOverItsValue is the same pair one line down, inside the below dial.
func t1aOverItsValue(label string, labelX1 float64, value string) []extraction.TokenPage {
	return rvPage(
		rvTok(label, 0.10, 0.30, labelX1, 0.315),
		rvTok(value, 0.10, 0.335, 0.20, 0.350),
	)
}

// t1aVAT is one page's vat candidate values under the shipped set.
func t1aVAT(pages []extraction.TokenPage) []string {
	return rvValues(rvFor(extraction.Resolve(pages, rvGeneric()), "vat"))
}

// A VAT registration number is an identifier, never an amount. The whole phrase is the label, so
// the bare "VAT" inside it must anchor nothing -- at either relation, in either spelling.
func TestTier1_AVATRegistrationNumberIsNotTheVATAmount(t *testing.T) {
	t1Floor(t)

	const reg = "1234567"
	for _, arm := range []struct {
		name  string
		pages []extraction.TokenPage
	}{
		{"VAT REG NO beside the number", t1aBesideItsValue("VAT REG NO", 0.25, reg)},
		{"VAT REGISTRATION NUMBER beside the number", t1aBesideItsValue("VAT REGISTRATION NUMBER", 0.28, reg)},
		{"TAX REGISTRATION NUMBER beside the number", t1aBesideItsValue("TAX REGISTRATION NUMBER", 0.28, reg)},
		{"VAT REG NO over the number", t1aOverItsValue("VAT REG NO", 0.25, reg)},
	} {
		if got := t1aVAT(arm.pages); len(got) != 0 {
			t.Errorf("%s: vat = %v, want none; a registration identifier read as the VAT amount is filed as tax", arm.name, got)
		}
	}

	// The paired controls, one per relation: the same geometry with the bare label. Without them
	// the zeros above hold equally against a Resolve that reads nothing off this page shape.
	for _, ctl := range []struct {
		name  string
		pages []extraction.TokenPage
	}{
		{"the bare label beside the number", t1aBesideItsValue("VAT", 0.17, reg)},
		{"the bare label over the number", t1aOverItsValue("VAT", 0.17, reg)},
	} {
		got := extraction.Resolve(ctl.pages, rvGeneric())
		rvControl(t, got, ctl.name)
		if v := rvValues(rvFor(got, "vat")); !slices.Equal(v, []string{reg}) {
			t.Errorf("%s: vat = %v, want [%s]; the geometry the arms above assert nothing on never reached the number", ctl.name, v, reg)
		}
	}
}

// "TAX INVOICE" is the document's own title. The bare "TAX" inside it must anchor nothing, and
// the title's span must stop at "invoice" so the invoice-number label behind it survives.
func TestTier1_ATaxInvoiceTitleIsNotAVATAnchor(t *testing.T) {
	t1Floor(t)

	for _, arm := range []struct {
		name  string
		pages []extraction.TokenPage
	}{
		{"TAX INVOICE beside the amount", t1aBesideItsValue("TAX INVOICE", 0.26, "1,500.00")},
		{"VAT INVOICE beside the amount", t1aBesideItsValue("VAT INVOICE", 0.26, "1,500.00")},
		{"TAX INVOICE over the amount", t1aOverItsValue("TAX INVOICE", 0.26, "2,687.50")},
	} {
		if got := t1aVAT(arm.pages); len(got) != 0 {
			t.Errorf("%s: vat = %v, want none; the document's own title is not a VAT label", arm.name, got)
		}
	}

	for _, ctl := range []struct {
		name  string
		pages []extraction.TokenPage
		want  string
	}{
		{"the bare label beside the amount", t1aBesideItsValue("TAX", 0.17, "1,500.00"), "1500.00"},
		{"the bare label over the amount", t1aOverItsValue("TAX", 0.17, "2,687.50"), "2687.50"},
	} {
		got := extraction.Resolve(ctl.pages, rvGeneric())
		rvControl(t, got, ctl.name)
		if v := rvValues(rvFor(got, "vat")); !slices.Equal(v, []string{ctl.want}) {
			t.Errorf("%s: vat = %v, want [%s]; the geometry the arms above assert nothing on never reached the amount", ctl.name, v, ctl.want)
		}
	}

	// The over-reach guard: a title entry reaching past "invoice" would swallow the
	// invoice-number label sharing the token.
	const titledToken = "TAX INVOICE NO: INV-2103"
	titled := extraction.Resolve(rvPage(rvTok(titledToken, 0.10, 0.30, 0.30, 0.315)), rvGeneric())
	rvControl(t, titled, "a title token carrying the invoice-number label")
	if v := rvValues(rvFor(titled, "invoice_number")); len(v) == 0 || v[0] != "INV-2103" {
		t.Errorf("%q ranks invoice_number %v, want INV-2103 at rank 0; the title swallowed the label behind it", titledToken, v)
	}

	// The near-miss: "INVOICES" continues past the title's trailing word boundary, so that token
	// keeps its amount label.
	if v := t1aVAT(t1aBesideItsValue("TAX INVOICES", 0.27, "1,500.00")); !slices.Contains(v, "1500.00") {
		t.Errorf("\"TAX INVOICES\" beside an amount: vat = %v, want it to hold 1500.00; the title entry widened past its own word boundary", v)
	}
}

// The over-reach guard for both new patterns: a bare amount label is still an amount label.
// Green before the owning phrases and green after -- a pattern that loses its required tail reds
// here rather than on a corpus figure.
func TestTier1_ABareTaxLabelStillAnchorsTheAmount(t *testing.T) {
	t1Floor(t)

	for _, c := range []struct {
		name  string
		pages []extraction.TokenPage
		want  string
	}{
		{"Tax beside the amount", t1aBesideItsValue("Tax", 0.17, "187.50"), "187.50"},
		{"VAT beside the amount", t1aBesideItsValue("VAT", 0.17, "1,500.00"), "1500.00"},
		{"VAT: 75.00 on one token", rvPage(rvTok("VAT: 75.00", 0.10, 0.30, 0.25, 0.315)), "75.00"},
	} {
		got := extraction.Resolve(c.pages, rvGeneric())
		rvFloor(t, got, c.name)
		if v := rvValues(rvFor(got, "vat")); !slices.Equal(v, []string{c.want}) {
			t.Errorf("%s: vat = %v, want [%s]; an owning phrase suppressed the bare label it only contains", c.name, v, c.want)
		}
	}
}

// Every other arrangement here starts the phrase at offset 0, so an entry anchored to the start
// of the token satisfies all of them while a real document printing "PROFORMA TAX INVOICE"
// anchors an amount again.
func TestTier1_AnOwningPhraseSuppressesWhereverItSitsOnTheToken(t *testing.T) {
	t1Floor(t)

	for _, arm := range []struct {
		name  string
		pages []extraction.TokenPage
	}{
		{"a prefixed title", t1aBesideItsValue("PROFORMA TAX INVOICE", 0.30, "1,500.00")},
		{"a prefixed registration phrase", t1aBesideItsValue("Statement VAT REG NO", 0.30, "1234567")},
	} {
		if got := t1aVAT(arm.pages); len(got) != 0 {
			t.Errorf("%s: vat = %v, want none; the phrase claims the token only when it opens it", arm.name, got)
		}
	}

	// The paired controls: the same offsets with the phrase cut back to the bare label, so the
	// zeros above cannot hold against a page shape Resolve reads nothing off.
	for _, ctl := range []struct {
		name  string
		pages []extraction.TokenPage
		want  string
	}{
		{"a prefixed bare label beside the amount", t1aBesideItsValue("PROFORMA TAX", 0.22, "1,500.00"), "1500.00"},
		{"a prefixed bare label beside the number", t1aBesideItsValue("Statement VAT", 0.22, "1234567"), "1234567"},
	} {
		got := extraction.Resolve(ctl.pages, rvGeneric())
		rvControl(t, got, ctl.name)
		if v := rvValues(rvFor(got, "vat")); !slices.Equal(v, []string{ctl.want}) {
			t.Errorf("%s: vat = %v, want [%s]; the geometry the arms above assert nothing on never reached the value", ctl.name, v, ctl.want)
		}
	}
}
