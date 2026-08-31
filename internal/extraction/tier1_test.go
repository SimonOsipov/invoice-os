// tier1_test.go: G-02..G-10, G-13, G-15. External package: every spec here reaches only
// exported symbols, and reuses resolve_test.go's rv* harness.
//
// Same two rules as resolve_test.go bind every spec. An assertion quantifying over Resolve's
// output or over Tier1Rules carries a floor first. An assertion that a result is EMPTY carries
// a positive control in the same test.
package extraction_test

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- harness ----------------------------------------------------------------

// t1RuleCount is the shipped set's size, written out here rather than read from the package:
// a floor that reads the value it guards cannot fail.
const t1RuleCount = 32

const (
	t1Inline  = "corpus_inline_labels.pdf"
	t1Split   = "corpus_split_labels.pdf"
	t1Stacked = "corpus_stacked_labels.pdf"
	t1Totals  = "corpus_totals_block.pdf"
)

// t1SweepLabel is the sweep pattern, spelled out for the band specs. G-14 is what pins the
// shipped rules to it.
const t1SweepLabel = `^\s*[0-9]{8}-[0-9]{4}\s*$`

// t1WantKeys is the shipped key list, sorted. RuleID lands in a persisted row, so a rename has
// to show up as a deliberate diff here.
var t1WantKeys = []string{
	"t1.buyer_name.below",
	"t1.buyer_name.right",
	"t1.buyer_name.same_token",
	"t1.buyer_tin.below",
	"t1.buyer_tin.right",
	"t1.buyer_tin.same_token",
	"t1.buyer_tin.sweep",
	"t1.currency.below",
	"t1.currency.right",
	"t1.currency.same_token",
	"t1.invoice_number.below",
	"t1.invoice_number.right",
	"t1.invoice_number.same_token",
	"t1.issue_date.below",
	"t1.issue_date.right",
	"t1.issue_date.same_token",
	"t1.subtotal.below",
	"t1.subtotal.right",
	"t1.subtotal.same_token",
	"t1.supplier_name.below",
	"t1.supplier_name.right",
	"t1.supplier_name.same_token",
	"t1.supplier_tin.below",
	"t1.supplier_tin.right",
	"t1.supplier_tin.same_token",
	"t1.supplier_tin.sweep",
	"t1.total.below",
	"t1.total.right",
	"t1.total.same_token",
	"t1.vat.below",
	"t1.vat.right",
	"t1.vat.same_token",
}

// t1Rule builds a banded Tier-1 rule through ParseRule.
func t1Rule(t *testing.T, key, field, label string, kind extraction.RelationKind, maxDist float64, shape extraction.Shape, band extraction.PageBand) extraction.Tier1Rule {
	t.Helper()

	r := rvTier1(t, key, field, label, kind, maxDist, shape)
	r.Band = band
	return r
}

// t1WithoutSweeps is the shipped set minus the two format-only rules -- G-10's negative control.
func t1WithoutSweeps(rules []extraction.Tier1Rule) []extraction.Tier1Rule {
	out := make([]extraction.Tier1Rule, 0, len(rules))
	for _, r := range rules {
		if strings.HasSuffix(r.Key, ".sweep") {
			continue
		}
		out = append(out, r)
	}
	return out
}

// t1Shipped resolves one corpus layout against the shipped set alone.
func t1Shipped(t *testing.T, file string) []extraction.Candidate {
	t.Helper()
	return extraction.Resolve(rvCorpusPages(t, file), extraction.RuleSet{Learned: nil, Tier1: extraction.Tier1Rules})
}

// t1Floor fails when the shipped set is not the size every count assertion assumes.
func t1Floor(t *testing.T) {
	t.Helper()
	if len(extraction.Tier1Rules) != t1RuleCount {
		t.Fatalf("Tier1Rules holds %d rule(s), want %d; every assertion below would run over the wrong set", len(extraction.Tier1Rules), t1RuleCount)
	}
}

// --- the specs --------------------------------------------------------------

// G-02
func TestTier1_CoversEveryHeaderField(t *testing.T) {
	if len(extraction.HeaderFields) != 10 {
		t.Fatalf("HeaderFields holds %d field(s), want 10; the coverage loop below would run over the wrong vocabulary", len(extraction.HeaderFields))
	}
	t1Floor(t)

	for _, field := range extraction.HeaderFields {
		n := 0
		for _, r := range extraction.Tier1Rules {
			if r.Field == field {
				n++
			}
		}
		if n == 0 {
			t.Errorf("no Tier-1 rule names %q; a tenant's first document can never fill it (AC-5)", field)
		}
	}

	for _, r := range extraction.Tier1Rules {
		if !slices.Contains(extraction.HeaderFields, r.Field) {
			t.Errorf("Tier1Rules[%q] names field %q, which is outside HeaderFields; Resolve drops it and the rule ships dead", r.Key, r.Field)
		}
	}
}

// G-03
func TestTier1_KeysAreUniqueAndNonEmpty(t *testing.T) {
	t1Floor(t)

	got := make([]string, 0, len(extraction.Tier1Rules))
	for _, r := range extraction.Tier1Rules {
		got = append(got, r.Key)
		if r.Key == "" {
			t.Errorf("a rule for %q ships with an empty Key; it lands in Candidate.RuleID", r.Field)
			continue
		}
		if !strings.HasPrefix(r.Key, "t1."+r.Field+".") {
			t.Errorf("key %q does not name its own field %q; the two disagree in every persisted candidate", r.Key, r.Field)
		}
	}

	sorted := slices.Sorted(slices.Values(got))
	if n := len(slices.Compact(slices.Clone(sorted))); n != len(sorted) {
		t.Errorf("the set carries %d distinct key(s) across %d rule(s); two rules sharing a RuleID are indistinguishable downstream", n, len(sorted))
	}
	if !slices.Equal(sorted, t1WantKeys) {
		t.Errorf("the shipped key list changed\n got %v\nwant %v", sorted, t1WantKeys)
	}
}

// G-04
func TestTier1_ResolvesWithNoLearnedRules(t *testing.T) {
	got := t1Shipped(t, t1Inline)
	rvFloor(t, got, "the shipped Tier-1 set over "+t1Inline)

	// Contains, never rank: the total rule mints a candidate off the Sub-total label too, and
	// measured, "1000.00" ranks first. Ranking beyond tier precedence is EXTR-05's.
	for _, want := range []struct{ field, value string }{
		{"invoice_number", "INV-1001"},
		{"issue_date", "2026-03-04"},
		{"total", "1075.00"},
	} {
		values := rvValues(rvFor(got, want.field))
		if len(values) == 0 {
			t.Errorf("%s got no candidate at all; Tier-1 alone must reach it (AC-5)", want.field)
			continue
		}
		if !slices.Contains(values, want.value) {
			t.Errorf("%s = %v, want it to contain %q", want.field, values, want.value)
		}
	}
}

// G-05
func TestTier1_ANilLearnedSetIsNotAnError(t *testing.T) {
	got := t1Shipped(t, t1Inline)
	rvFloor(t, got, "a nil learned set over "+t1Inline)

	for _, c := range got {
		if c.Tier != extraction.TierGeneric {
			t.Errorf("candidate %s=%q from rule %q carries tier %v, want TierGeneric; nothing learned went in", c.Field, c.Value, c.RuleID, c.Tier)
		}
		if !strings.HasPrefix(c.RuleID, "t1.") {
			t.Errorf("candidate %s=%q carries RuleID %q, which no shipped rule owns", c.Field, c.Value, c.RuleID)
		}
	}
}

// G-06
func TestResolve_LearnedRuleOrdersBeforeTier1ForTheSameField(t *testing.T) {
	t1Floor(t)

	// "Payable" is in no lexicon entry, so only the learned rule reaches the second token, and
	// the 0.27 vertical gap is well past tier1MaxDistanceBelow. The generic anchor sits ABOVE
	// the learned one at the same distance 0, so reading order alone would put the generic
	// first: only the tier key produces the wanted order.
	pages := rvPage(
		rvTok("Total: 1,000.00", 0.10, 0.10, 0.30, 0.13),
		rvTok("Payable: 3,000.00", 0.10, 0.40, 0.35, 0.43),
	)
	rules := extraction.RuleSet{
		Learned: []extraction.AnchorRule{
			rvLearned(t, "learned-total", "total", `(?i)\bpayable\b`, extraction.RelSameToken, 0, extraction.ShapeAmount),
		},
		Tier1: extraction.Tier1Rules,
	}

	got := rvFor(extraction.Resolve(pages, rules), "total")
	rvFloor(t, got, "one learned and one Tier-1 rule on total")
	if len(got) != 2 {
		t.Fatalf("got %d candidate(s) for total, want exactly 2: %+v", len(got), got)
	}
	if got[0].Tier != extraction.TierLearned || got[0].Value != "3000.00" {
		t.Errorf("got[0] = %v %q from %q, want the learned candidate 3000.00 (AC-7)", got[0].Tier, got[0].Value, got[0].RuleID)
	}
	if got[1].Tier != extraction.TierGeneric || got[1].Value != "1000.00" {
		t.Errorf("got[1] = %v %q from %q, want the Tier-1 candidate 1000.00 still present beneath it; AC-7 orders, it never suppresses (D-5)", got[1].Tier, got[1].Value, got[1].RuleID)
	}
}

// G-07
func TestTier1_LearnedRuleOutranksTheShippedSetAtAGreaterDistance(t *testing.T) {
	// Measured on corpus_totals_block.pdf: the learned rule reaches "375.00" at 0.15888 and
	// the shipped set reaches "5000.00" at 0.11955, so the learned one is genuinely FURTHER.
	learned := rvLearned(t, "learned-total-far", "total", `(?i)\bVAT\b`, extraction.RelRight, 0.35, extraction.ShapeAmount)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{learned}, Tier1: extraction.Tier1Rules}

	got := rvFor(extraction.Resolve(rvCorpusPages(t, t1Totals), rules), "total")
	rvFloor(t, got, "a learned total rule plus the shipped set over "+t1Totals)

	nearestGeneric := math.Inf(1)
	for _, c := range got {
		if c.Tier == extraction.TierGeneric {
			nearestGeneric = math.Min(nearestGeneric, c.Distance)
		}
	}
	if math.IsInf(nearestGeneric, 1) {
		t.Fatalf("total carries no TierGeneric competitor: %+v; \"the learned one is first\" would hold over a list of one", got)
	}
	if got[0].Tier != extraction.TierLearned || got[0].RuleID != learned.ID {
		t.Fatalf("got[0] = %v from rule %q, want the learned candidate first", got[0].Tier, got[0].RuleID)
	}
	if got[0].Distance <= nearestGeneric {
		t.Fatalf("the learned candidate sits at distance %v and the nearest generic at %v; the tier key is only under test when the learned one is further", got[0].Distance, nearestGeneric)
	}
}

// G-08
func TestResolve_TierPrecedenceDoesNotSuppress(t *testing.T) {
	t1Floor(t)

	pages := rvPage(rvTok("Total: 1,000.00", 0.10, 0.10, 0.30, 0.13))
	rules := extraction.RuleSet{
		Learned: []extraction.AnchorRule{
			rvLearned(t, "learned-total", "total", `(?i)\btotal\b`, extraction.RelSameToken, 0, extraction.ShapeAmount),
		},
		Tier1: extraction.Tier1Rules,
	}

	got := rvFor(extraction.Resolve(pages, rules), "total")
	rvFloor(t, got, "two rules, one per tier, finding the same value")
	if len(got) != 2 {
		t.Fatalf("got %d candidate(s) for total, want exactly 2; deduping the pair is EXTR-05's, not this layer's: %+v", len(got), got)
	}
	if got[0].Tier != extraction.TierLearned || got[1].Tier != extraction.TierGeneric {
		t.Errorf("tiers came back %v then %v, want TierLearned then TierGeneric", got[0].Tier, got[1].Tier)
	}
	if got[0].Value != got[1].Value {
		t.Errorf("values came back %q and %q; the two rules read the same token and must agree", got[0].Value, got[1].Value)
	}
	if got[0].RuleID == got[1].RuleID {
		t.Errorf("both candidates carry RuleID %q; the surviving pair is indistinguishable downstream", got[0].RuleID)
	}
}

// G-09
func TestTier1_SubtotalAndTotalBothMatchAStackedTotalsBlock(t *testing.T) {
	got := t1Shipped(t, t1Totals)
	rvFloor(t, got, "the shipped set over "+t1Totals)

	sub := rvValues(rvFor(got, "subtotal"))
	total := rvValues(rvFor(got, "total"))
	if len(sub) == 0 || len(total) == 0 {
		t.Fatalf("subtotal = %v, total = %v; the overlap assertions need candidates on both", sub, total)
	}

	if !slices.Contains(sub, "5000.00") {
		t.Errorf("subtotal = %v, want it to contain 5000.00", sub)
	}
	if !slices.Contains(total, "5375.00") {
		t.Errorf("total = %v, want it to contain 5375.00", total)
	}

	// Neither rule reaches the other's row. \btotal\b still matches inside "Sub-total", but the
	// subtotal entry claims a strictly wider span of that token, so the total rule does not
	// anchor there. sub[\s-]*total requires "sub", so the subtotal rule never had the Total row.
	if slices.Contains(total, "5000.00") {
		t.Errorf("total = %v and reached the Sub-total row; the wider subtotal match owns that token", total)
	}
	if slices.Contains(sub, "5375.00") {
		t.Errorf("subtotal = %v and reached the Total row; the lexicon overlap runs one way only", sub)
	}
}

// G-10
func TestTier1_TINSweepSeparatesSupplierFromBuyerByPageHalf(t *testing.T) {
	t.Run("stacked labels, where nothing but the sweep reaches a TIN", func(t *testing.T) {
		pages := rvCorpusPages(t, t1Stacked)
		got := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})

		supplier := rvValues(rvFor(got, "supplier_tin"))
		buyer := rvValues(rvFor(got, "buyer_tin"))
		if len(supplier) == 0 || len(buyer) == 0 {
			t.Fatalf("supplier_tin = %v, buyer_tin = %v; the separation assertions need both", supplier, buyer)
		}
		if !slices.Equal(supplier, []string{"99999999-0301"}) {
			t.Errorf("supplier_tin = %v, want exactly [99999999-0301]; one TIN each is the whole point, not both on both", supplier)
		}
		if !slices.Equal(buyer, []string{"99999999-0302"}) {
			t.Errorf("buyer_tin = %v, want exactly [99999999-0302]", buyer)
		}

		// Negative control, and it is what makes the spec's name true: this layout carries no
		// TIN label token at all, so without the two sweeps neither field is reachable.
		control := t1WithoutSweeps(extraction.Tier1Rules)
		if len(control) != len(extraction.Tier1Rules)-2 {
			t.Fatalf("the control set dropped %d rule(s), want exactly the 2 sweeps", len(extraction.Tier1Rules)-len(control))
		}
		ctl := extraction.Resolve(pages, extraction.RuleSet{Tier1: control})
		rvControl(t, ctl, "the shipped set minus the two sweeps over "+t1Stacked)
		if v := rvValues(rvFor(ctl, "supplier_tin")); len(v) != 0 {
			t.Errorf("without the sweeps supplier_tin = %v, want none; a label rule did the work and this spec does not test the sweep", v)
		}
		if v := rvValues(rvFor(ctl, "buyer_tin")); len(v) != 0 {
			t.Errorf("without the sweeps buyer_tin = %v, want none", v)
		}
	})

	t.Run("split labels, where the sweep and the label path agree", func(t *testing.T) {
		got := t1Shipped(t, t1Split)
		supplier := rvFor(got, "supplier_tin")
		buyer := rvFor(got, "buyer_tin")
		if len(supplier) == 0 || len(buyer) == 0 {
			t.Fatalf("supplier_tin = %v, buyer_tin = %v; the rank assertions need both", rvValues(supplier), rvValues(buyer))
		}
		for _, c := range []struct {
			field  string
			got    extraction.Candidate
			value  string
			ruleID string
		}{
			{"supplier_tin", supplier[0], "99999999-0201", "t1.supplier_tin.sweep"},
			{"buyer_tin", buyer[0], "99999999-0202", "t1.buyer_tin.sweep"},
		} {
			if c.got.Value != c.value || c.got.RuleID != c.ruleID {
				t.Errorf("%s[0] = %q from rule %q, want %q from %q; the sweep sits at distance 0 and ranks first on this layout", c.field, c.got.Value, c.got.RuleID, c.value, c.ruleID)
			}
		}
	})
}

// G-13
func TestTier1_RulesAreNotInVocabularyOrder(t *testing.T) {
	if len(extraction.HeaderFields) == 0 || len(extraction.Tier1Rules) == 0 {
		t.Fatalf("HeaderFields holds %d and Tier1Rules %d; the order comparison below would run over nothing", len(extraction.HeaderFields), len(extraction.Tier1Rules))
	}

	var order []string
	for _, r := range extraction.Tier1Rules {
		if !slices.Contains(order, r.Field) {
			order = append(order, r.Field)
		}
	}
	if len(order) != len(extraction.HeaderFields) {
		t.Fatalf("the shipped set names %d field(s) and HeaderFields %d; the comparison below only means something over the same membership", len(order), len(extraction.HeaderFields))
	}
	for _, field := range extraction.HeaderFields {
		if !slices.Contains(order, field) {
			t.Fatalf("the shipped set never names %q; the two sequences hold different fields and comparing their order proves nothing", field)
		}
	}

	if slices.Equal(order, extraction.HeaderFields) {
		t.Errorf("Tier1Rules runs in HeaderFields order %v, so TestResolve_ReturnsFieldsInVocabularyOrder cannot tell rule order from vocabulary order and passes by luck; the shipped set follows the anchor lexicon, which puts buyer_tin before supplier_name", order)
	}
}

// G-15
func TestResolve_ABandedRuleIgnoresAnAnchorOutsideItsBand(t *testing.T) {
	box := func(page int, y0, y1 float64) extraction.Region {
		return extraction.Region{Page: page, X0: 0.10, Y0: y0, X1: 0.30, Y1: y1}
	}
	// One bare TIN token: the sweep pattern matches it whole, so the label IS the value and
	// the band is read off the anchor's own box.
	pageOf := func(r extraction.Region) []extraction.TokenPage {
		return []extraction.TokenPage{{
			Number: r.Page, WidthPt: 612, HeightPt: 792,
			Tokens: []extraction.Token{{Text: "99999999-0301", Region: r}},
		}}
	}
	sweep := func(t *testing.T, band extraction.PageBand) extraction.RuleSet {
		t.Helper()
		return extraction.RuleSet{Tier1: []extraction.Tier1Rule{
			t1Rule(t, "t1.supplier_tin.sweep", "supplier_tin", t1SweepLabel, extraction.RelSameToken, 0, extraction.ShapeTIN, band),
		}}
	}

	cases := []struct {
		name   string
		band   extraction.PageBand
		region extraction.Region
		want   bool
	}{
		{"top takes a box wholly above the split", extraction.BandPage1Top, box(1, 0.20, 0.27), true},
		{"top leaves a box wholly below the split", extraction.BandPage1Top, box(1, 0.55, 0.60), false},
		{"bottom takes a box wholly below the split", extraction.BandPage1Bottom, box(1, 0.55, 0.60), true},
		{"bottom leaves a box wholly above the split", extraction.BandPage1Bottom, box(1, 0.20, 0.27), false},
		{"top leaves a box straddling the split", extraction.BandPage1Top, box(1, 0.45, 0.55), false},
		{"bottom leaves a box straddling the split", extraction.BandPage1Bottom, box(1, 0.45, 0.55), false},
		{"top takes a box whose lower edge is exactly the split", extraction.BandPage1Top, box(1, 0.45, 0.50), true},
		{"bottom takes a box whose upper edge is exactly the split", extraction.BandPage1Bottom, box(1, 0.50, 0.55), true},
		{"top leaves a boxless token", extraction.BandPage1Top, extraction.Region{Page: 1}, false},
		{"bottom leaves a boxless token", extraction.BandPage1Bottom, extraction.Region{Page: 1}, false},
		{"top leaves a page-2 token", extraction.BandPage1Top, box(2, 0.20, 0.27), false},
		{"bottom leaves a page-2 token", extraction.BandPage1Bottom, box(2, 0.55, 0.60), false},
		{"an unrecognised band takes nothing", extraction.PageBand(99), box(1, 0.20, 0.27), false},
		{"anywhere takes a top box", extraction.BandAnywhere, box(1, 0.20, 0.27), true},
		{"anywhere takes a bottom box", extraction.BandAnywhere, box(1, 0.55, 0.60), true},
		{"anywhere takes a box straddling the split", extraction.BandAnywhere, box(1, 0.45, 0.55), true},
		{"anywhere takes a boxless token", extraction.BandAnywhere, extraction.Region{Page: 1}, true},
		{"anywhere takes a page-2 token", extraction.BandAnywhere, box(2, 0.20, 0.27), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rvFor(extraction.Resolve(pageOf(c.region), sweep(t, c.band)), "supplier_tin")
			switch {
			case c.want && len(got) == 0:
				t.Fatalf("the band took no candidate from a token it must accept")
			case !c.want && len(got) != 0:
				t.Fatalf("the band took %d candidate(s) %v from a token outside it", len(got), rvValues(got))
			}
			if c.want {
				return
			}
			// Paired control: the SAME token under BandAnywhere. Without it the zero above
			// holds equally against a rule that matches nothing anywhere.
			ctl := rvFor(extraction.Resolve(pageOf(c.region), sweep(t, extraction.BandAnywhere)), "supplier_tin")
			rvControl(t, ctl, "the same token under BandAnywhere")
		})
	}
}
