// tier1_adversarial_test.go: coverage G-01..G-16 leave open. Two mutants survived the whole
// package: disabling every `below` rule, and shipping a rule keyed `.below` that carries
// RelRight. The first is closed here, the second in tier1_internal_test.go.
//
// Same two rules bind every spec: a quantifier over Resolve's output carries a floor first, and
// an asserted zero carries a positive control in the same test.
package extraction_test

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- harness ----------------------------------------------------------------

// t1aGaps are the corpusExpect pairs Tier-1 cannot reach. Asserted STILL missing, so closing one
// in EXTR-04-09 is a deliberate diff rather than a silent pass. buyer_tin's party word is
// required (anchor.go:128) while supplier_tin's is optional (:127), and corpus_two_column.pdf
// puts both TINs in the same page half, so neither the label path nor the banded sweep reaches
// the buyer's.
var t1aGaps = []struct{ file, field string }{
	{"corpus_two_column.pdf", "buyer_tin"},
}

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

// t1aTIN is a bare TIN token: the sweep pattern matches it whole, so the band is read off the
// token's own box.
func t1aTIN(page int, text string, y0, y1 float64) extraction.Token {
	return extraction.Token{Text: text, Region: extraction.Region{Page: page, X0: 0.10, Y0: y0, X1: 0.30, Y1: y1}}
}

func t1aPage(number int, tokens ...extraction.Token) extraction.TokenPage {
	return extraction.TokenPage{Number: number, WidthPt: 612, HeightPt: 792, Tokens: tokens}
}

// t1aUnbanded is the shipped set with every band cleared -- the paired control for a spec whose
// point is that a band withheld something.
func t1aUnbanded() []extraction.Tier1Rule {
	out := slices.Clone(extraction.Tier1Rules)
	for i := range out {
		out[i].Band = extraction.BandAnywhere
	}
	return out
}

// --- the specs --------------------------------------------------------------

// AC-4's breadth oracle. G-04 checks three fields on one layout; corpusExpect is 47 pairs across
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

// Banding is defined on page 1 only. G-15 says so over one-page documents; a real invoice runs to
// several, and inBand reads TokenPage.Number rather than a loop index.
func TestTier1_ABandedSweepIgnoresALaterPage(t *testing.T) {
	t1Floor(t)

	pages := []extraction.TokenPage{
		t1aPage(1, t1aTIN(1, "99999999-0301", 0.20, 0.27), t1aTIN(1, "99999999-0302", 0.60, 0.67)),
		t1aPage(2, t1aTIN(2, "99999999-0303", 0.20, 0.27), t1aTIN(2, "99999999-0304", 0.60, 0.67)),
	}

	got := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, got, "two pages of bare TIN tokens under the shipped set")
	if v := rvValues(rvFor(got, "supplier_tin")); !slices.Equal(v, []string{"99999999-0301"}) {
		t.Errorf("supplier_tin = %v, want exactly [99999999-0301]; page 2's top TIN is outside BandPage1Top", v)
	}
	if v := rvValues(rvFor(got, "buyer_tin")); !slices.Equal(v, []string{"99999999-0302"}) {
		t.Errorf("buyer_tin = %v, want exactly [99999999-0302]; page 2's lower TIN is outside BandPage1Bottom", v)
	}

	// Paired control: the same four tokens with every band cleared. Without it the two zeros
	// above hold equally against a sweep that reads nothing on page 2 for some other reason.
	ctl := extraction.Resolve(pages, extraction.RuleSet{Tier1: t1aUnbanded()})
	rvControl(t, ctl, "the same four tokens with every band cleared")
	for _, v := range []string{"99999999-0303", "99999999-0304"} {
		if !slices.Contains(rvValues(rvFor(ctl, "supplier_tin")), v) {
			t.Errorf("unbanded, supplier_tin = %v, want it to contain %q; the page-2 tokens are readable and only the band withheld them", rvValues(rvFor(ctl, "supplier_tin")), v)
		}
	}
}

// The page-half split is the sweeps' ONLY discriminator, so a layout that puts both TINs in one
// half gives both to one field and none to the other. corpus_two_column.pdf is the real
// instance; this is the mechanism, and it is EXTR-04-09's to fix.
func TestTier1_TheSweepCannotSeparateTwoTINsInOnePageHalf(t *testing.T) {
	t1Floor(t)

	sameHalf := []extraction.TokenPage{t1aPage(1,
		t1aTIN(1, "99999999-0401", 0.20, 0.27),
		t1aTIN(1, "99999999-0402", 0.30, 0.37),
	)}

	got := extraction.Resolve(sameHalf, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, got, "two bare TINs in the top half under the shipped set")
	if v := rvValues(rvFor(got, "supplier_tin")); !slices.Equal(v, []string{"99999999-0401", "99999999-0402"}) {
		t.Errorf("supplier_tin = %v, want both TINs; the top sweep takes every bare TIN above the split", v)
	}
	if v := rvValues(rvFor(got, "buyer_tin")); len(v) != 0 {
		t.Errorf("buyer_tin = %v, want none; nothing sits below the split", v)
	}

	// Positive control, and the fix's shape: the same two TINs, one per half, separate cleanly.
	split := []extraction.TokenPage{t1aPage(1,
		t1aTIN(1, "99999999-0401", 0.20, 0.27),
		t1aTIN(1, "99999999-0402", 0.60, 0.67),
	)}
	ctl := extraction.Resolve(split, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvControl(t, ctl, "the same two TINs, one per page half")
	if v := rvValues(rvFor(ctl, "buyer_tin")); !slices.Equal(v, []string{"99999999-0402"}) {
		t.Errorf("with one TIN per half, buyer_tin = %v, want [99999999-0402]", v)
	}
}

// Two paths reach supplier_tin on a split layout and they disagree. Both survive, and RuleID is
// what tells them apart -- picking between them is EXTR-05's.
func TestTier1_TheSweepAndTheLabelPathBothSurviveAndDisagree(t *testing.T) {
	got := rvFor(t1Shipped(t, t1Split), "supplier_tin")
	rvFloor(t, got, "supplier_tin on "+t1Split)

	if len(got) < 2 {
		t.Fatalf("supplier_tin carries %d candidate(s) %v; the disagreement needs at least two", len(got), rvValues(got))
	}
	if got[0].RuleID != "t1.supplier_tin.sweep" || got[0].Value != "99999999-0201" {
		t.Errorf("supplier_tin[0] = %q from %q, want 99999999-0201 from t1.supplier_tin.sweep at distance 0", got[0].Value, got[0].RuleID)
	}

	// The label path reaches the BUYER's TIN too: the supplier pattern's party word is optional
	// (anchor.go:127). Harmless here because the sweep outranks it; recorded for EXTR-04-09.
	var fromLabel []string
	for _, c := range got[1:] {
		if c.RuleID == "t1.supplier_tin.sweep" {
			continue
		}
		fromLabel = append(fromLabel, c.Value)
	}
	if !slices.Contains(fromLabel, "99999999-0202") {
		t.Errorf("the label path contributed %v, want it to contain the buyer's 99999999-0202; without the disagreement this spec pins nothing", fromLabel)
	}
	for _, c := range got {
		if c.RuleID == "" {
			t.Errorf("a supplier_tin candidate %q carries no RuleID; the surviving pair is indistinguishable downstream", c.Value)
		}
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
