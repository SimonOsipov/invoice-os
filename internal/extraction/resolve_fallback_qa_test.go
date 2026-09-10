// resolve_fallback_qa_test.go: the edges EXTR-25-02's own specs leave open. AC-2.1 only ever
// pits TierFallback against TierGeneric and only ever with ONE fallback candidate, and AC-2.5
// hands appendRuleCandidates an explicit tier rather than letting Resolve choose it. Here the
// fallback tier meets a LEARNED reading, meets a label that loses on every geometric axis, and
// meets both guards through the exported Resolve.
package extraction_test

import (
	"math/rand"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rvaRetier copies cands with every Tier set to tier. Each control below re-tiers rather than
// rebuilding the page, so the control and the assertion it backs differ in the tier and in
// nothing else.
func rvaRetier(cands []extraction.Candidate, tier extraction.Tier) []extraction.Candidate {
	out := make([]extraction.Candidate, len(cands))
	for i, c := range cands {
		c.Tier = tier
		out[i] = c
	}
	return out
}

// rvaTierCount counts cands at tier: a fixture that stopped producing the tier it is named for
// fails loudly instead of passing on an empty quantifier.
func rvaTierCount(cands []extraction.Candidate, tier extraction.Tier) int {
	n := 0
	for _, c := range cands {
		if c.Tier == tier {
			n++
		}
	}
	return n
}

// Nothing in this story may let a shape-only reading outrank the tenant's own correction. A
// comparator that ordered Tier by distance from TierGeneric would satisfy AC-2.1 and fail here.
func TestResolve_AFallbackNeverOutranksALearnedReading(t *testing.T) {
	learned := rvLearned(t, "L-1", "currency", `(?i)paid in`, extraction.RelRight, 0.35, extraction.ShapeName)
	fallback := extraction.Tier1Rule{
		Key: "g.currency.sweep", Field: "currency",
		Rule:     rvRule(t, `^NGN$`, extraction.RelSameToken, 0, extraction.ShapeName),
		Fallback: true,
	}
	page := rvPage(
		rvTok("NGN", 0.10, 0.10, 0.20, 0.13),     // reads first, same_token, distance 0
		rvTok("Paid in", 0.10, 0.60, 0.22, 0.63), // the tenant's own anchor, lower and farther
		rvTok("EUR", 0.34, 0.60, 0.42, 0.63),     // gap 0.12 from its label
	)
	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{learned}, Tier1: []extraction.Tier1Rule{fallback}}

	got := extraction.Resolve(page, rules)
	cands := rvFor(got, "currency")
	rvFloor(t, cands, "a learned EUR reading beside a bare NGN token")
	if n := rvaTierCount(cands, extraction.TierLearned); n != 1 {
		t.Fatalf("currency holds %d TierLearned candidate(s) in %+v, want 1; the learned arm is the whole point of this fixture", n, cands)
	}
	if n := rvaTierCount(cands, extraction.TierFallback); n != 1 {
		t.Fatalf("currency holds %d TierFallback candidate(s) in %+v, want 1; without one the pass below proves nothing", n, cands)
	}

	field, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: got}), "currency")
	if !ok || field.Value == nil {
		t.Fatalf("currency decided nothing from %+v", cands)
	}
	if *field.Value != "EUR" {
		t.Errorf("currency = %q, want %q -- a learned rule is the tenant's own answer and a shape-only fallback must never displace it", *field.Value, "EUR")
	}
	if field.Reason != extraction.ReasonNone || len(field.Alternatives) != 0 {
		t.Errorf("currency reason = %q alternatives = %q, want %q with none", field.Reason, valuesOf(field.Alternatives), extraction.ReasonNone)
	}

	// Control: with both candidates on one tier the closer NGN wins on Distance, so the EUR
	// above is the tier ordering's doing and not the fixture's geometry.
	ctl, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: rvaRetier(cands, extraction.TierFallback)}), "currency")
	if !ok || ctl.Value == nil || *ctl.Value != "NGN" {
		t.Fatalf("control: currency with both candidates at one tier = %+v, want NGN decided -- otherwise the EUR pass above holds equally for a comparator that ignores Tier", ctl)
	}
}

// Two fallback readings against one label that loses on distance, on reading order and on page
// position. Every AC-2.1 fixture carries ONE fallback candidate; a decideField that grouped by
// majority value rather than by the head's own tier would pass those and fail this.
func TestResolve_ALabelledReadingFartherOnEveryAxisStillBeatsTwoFallbacks(t *testing.T) {
	label := rvTier1(t, "g.currency.right", "currency", `(?i)currency:`, extraction.RelRight, 0.35, extraction.ShapeName)
	sweepA := extraction.Tier1Rule{
		Key: "g.currency.sweepA", Field: "currency",
		Rule:     rvRule(t, `^NGN$`, extraction.RelSameToken, 0, extraction.ShapeName),
		Fallback: true,
	}
	sweepB := extraction.Tier1Rule{
		Key: "g.currency.sweepB", Field: "currency",
		Rule:     rvRule(t, `^GHS$`, extraction.RelSameToken, 0, extraction.ShapeName),
		Fallback: true,
	}
	page := rvPage(
		rvTok("NGN", 0.10, 0.05, 0.20, 0.08),       // first in reading order, distance 0
		rvTok("GHS", 0.10, 0.12, 0.20, 0.15),       // second, distance 0
		rvTok("Currency:", 0.10, 0.85, 0.22, 0.88), // the label, last and lowest
		rvTok("USD", 0.44, 0.85, 0.52, 0.88),       // gap 0.22 from its label
	)
	rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{label, sweepA, sweepB}}

	got := extraction.Resolve(page, rules)
	cands := rvFor(got, "currency")
	rvFloor(t, cands, "two bare currency tokens above a labelled one")
	if n := rvaTierCount(cands, extraction.TierFallback); n != 2 {
		t.Fatalf("currency holds %d TierFallback candidate(s) in %+v, want 2; the point of this fixture is that the label is outnumbered", n, cands)
	}
	if n := rvaTierCount(cands, extraction.TierGeneric); n != 1 {
		t.Fatalf("currency holds %d TierGeneric candidate(s) in %+v, want 1", n, cands)
	}

	field, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: got}), "currency")
	if !ok || field.Value == nil {
		t.Fatalf("currency decided nothing from %+v", cands)
	}
	if *field.Value != "USD" {
		t.Errorf("currency = %q, want %q -- one label outranks any number of shape-only readings", *field.Value, "USD")
	}
	if field.Reason != extraction.ReasonNone {
		t.Errorf("currency reason = %q, want %q -- the two fallback readings sit a tier below the head and never join its group", field.Reason, extraction.ReasonNone)
	}
	if len(field.Alternatives) != 0 {
		t.Errorf("currency alternatives = %q, want none", valuesOf(field.Alternatives))
	}

	// Control: flattened onto one tier the two distance-0 sweeps tie into doubt, so the clean
	// USD above is not the fixture reading USD by accident.
	ctl, ok := rcFind(extraction.Reconcile(extraction.Input{Candidates: rvaRetier(cands, extraction.TierGeneric)}), "currency")
	if !ok || ctl.Reason != extraction.ReasonAmbiguous {
		t.Fatalf("control: currency with every candidate at one tier reads %+v, want ambiguous -- otherwise the tier split is not what produced the clean USD above", ctl)
	}
}

// Both guards, driven through the EXPORTED Resolve. AC-2.5 hands appendRuleCandidates an
// explicit TierFallback, so it pins the guards but not that Resolve threads a Fallback rule's
// own tier into them. Every zero here is paired with controls that name the clause that
// refused: the suppression is the guard's, not a missing match, a rejected shape or a band.
func TestResolve_AFallbackRuleKeepsTheShippedPostureThroughResolve(t *testing.T) {
	t.Run("anchorOutranked reaches a Fallback rule", func(t *testing.T) {
		rule := extraction.Tier1Rule{
			Key: "g.supplier_name.sweep", Field: "supplier_name",
			Rule:     rvRule(t, `(?i)\bsupplier\b`, extraction.RelSameToken, 0, extraction.ShapeName),
			Fallback: true,
		}
		rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{rule}}

		// The shipped lexicon's supplier_tin entry claims a strictly wider span over this token
		// than the rule's own bare "Supplier" match.
		outranked := rvPage(rvTok("Supplier TIN: 99999999-0101", 0.10, 0.10, 0.45, 0.13))
		if got := rvFor(extraction.Resolve(outranked, rules), "supplier_name"); len(got) != 0 {
			t.Errorf("supplier_name over an outranked anchor = %+v, want none: Resolve must hand a Fallback rule's tier to anchorOutranked", got)
		}

		// Clause control: the SAME rule over a token no lexicon entry outranks resolves, so the
		// zero above is anchorOutranked's and not the label failing to match.
		clear := rvPage(rvTok("Supplier Holdings Ltd", 0.10, 0.10, 0.45, 0.13))
		rvControl(t, rvFor(extraction.Resolve(clear, rules), "supplier_name"), "the same Fallback rule over a token no lexicon entry outranks")

		// Tier-clause control: the same label over the same token as a LEARNED rule resolves, so
		// the zero above is the tier != TierLearned conjunct and not the span arithmetic
		// refusing every reading of this page.
		learned := extraction.RuleSet{Learned: []extraction.AnchorRule{rvLearned(t, "L-1", "supplier_name", `(?i)\bsupplier\b`, extraction.RelSameToken, 0, extraction.ShapeName)}}
		rvControl(t, rvFor(extraction.Resolve(outranked, learned), "supplier_name"), "the same label over the same outranked token as a learned rule")
	})

	t.Run("the rightward label boundary reaches a Fallback rule", func(t *testing.T) {
		rule := extraction.Tier1Rule{
			Key: "g.vat.sweep", Field: "vat",
			Rule:     rvRule(t, `(?i)\bvat\b`, extraction.RelRight, 0.35, extraction.ShapeAmount),
			Fallback: true,
		}
		rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{rule}}

		blocked := rvPage(
			rvTok("VAT", 0.10, 0.70, 0.16, 0.72),
			rvTok("Total", 0.30, 0.70, 0.38, 0.72), // a shipped label, between anchor and value
			rvTok("2,687.50", 0.45, 0.70, 0.58, 0.72),
		)
		if got := rvFor(extraction.Resolve(blocked, rules), "vat"); len(got) != 0 {
			t.Errorf("vat crossing an intervening label = %+v, want none: Resolve must hand a Fallback rule's tier to the bounded clause", got)
		}

		// Clause control: the same three boxes with the middle token carrying no shipped label
		// resolve, so the zero above is crossesALabel's and not the distance dial's.
		open := rvPage(
			rvTok("VAT", 0.10, 0.70, 0.16, 0.72),
			rvTok("Zzzz", 0.30, 0.70, 0.38, 0.72),
			rvTok("2,687.50", 0.45, 0.70, 0.58, 0.72),
		)
		rvControl(t, rvFor(extraction.Resolve(open, rules), "vat"), "the same geometry with no shipped label between anchor and value")

		// Tier-clause control: the same blocked page as a learned rule resolves, because bounded
		// is false for TierLearned.
		learned := extraction.RuleSet{Learned: []extraction.AnchorRule{rvLearned(t, "L-2", "vat", `(?i)\bvat\b`, extraction.RelRight, 0.35, extraction.ShapeAmount)}}
		rvControl(t, rvFor(extraction.Resolve(blocked, learned), "vat"), "the same blocked page as a learned rule")
	})
}

// AC-2.8 shows a fallback head escaping the doubt WIDENING. It does not show that a fallback
// head can still be doubted at all, and an uncorroborated() mutated to exempt TierFallback from
// decideField outright would pass it. Two fallback readings at ONE distance must still tie.
func TestReconcile_AFallbackHeadIsStillSubjectToOrdinaryDoubt(t *testing.T) {
	a := rcAdjacentAt("total", "2500.00", extraction.TierFallback, 0.05)
	b := rcAdjacentAt("total", "2687.50", extraction.TierFallback, 0.05)
	got := rcDecide(t, "total", a, b)
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- the fallback tier is exempt from the widening, never from equal-standing doubt", got.Reason, extraction.ReasonAmbiguous)
	}
	if len(got.Alternatives) != 1 {
		t.Errorf("total alternatives = %q, want exactly one", valuesOf(got.Alternatives))
	}
}

// TestReconcile_AmbiguousCarriesAnAlternativeOverGeneratedCandidateSets enumerates
// []Tier{TierLearned, TierGeneric} -- written before a third tier existed, so it never generates
// one and its invariant is unproven over TierFallback. This runs the same invariant with the
// tier included, and counts the fallback heads it reached so a set that stopped producing them
// fails instead of passing over the two tiers the older test already covers.
func TestReconcile_AmbiguousCarriesAnAlternativeOverAllThreeTiers(t *testing.T) {
	rng := rand.New(rand.NewSource(0x25_02))
	tiers := []extraction.Tier{extraction.TierLearned, extraction.TierGeneric, extraction.TierFallback}
	distances := []float64{0, 0.01, 0.03, 0.05}
	values := []string{"A", "B", "C"}

	ambiguous, plain, fallbackHeads := 0, 0, 0
	const rounds = 3000
	for round := 0; round < rounds; round++ {
		field := extraction.HeaderFields[rng.Intn(len(extraction.HeaderFields))]
		cands := make([]extraction.Candidate, 0, 4)
		for i := 0; i < 1+rng.Intn(4); i++ {
			cands = append(cands, extraction.Candidate{
				Field:    field,
				Value:    values[rng.Intn(len(values))],
				Reason:   extraction.ReasonNone,
				Tier:     tiers[rng.Intn(len(tiers))],
				Distance: distances[rng.Intn(len(distances))],
				Adjacent: rng.Intn(2) == 1,
				RuleID:   "r" + string(rune('a'+rng.Intn(3))),
			})
		}
		lowest := extraction.TierFallback
		for _, c := range cands {
			if c.Tier < lowest {
				lowest = c.Tier
			}
		}
		if lowest == extraction.TierFallback {
			fallbackHeads++
		}

		got := rcDecide(t, field, cands...)
		alts := valuesOf(got.Alternatives)
		if got.Reason == extraction.ReasonAmbiguous {
			ambiguous++
			if len(alts) == 0 {
				t.Fatalf("round %d: %s reads ambiguous with no alternative, from %+v", round, field, cands)
			}
		} else {
			plain++
			if len(alts) != 0 {
				t.Fatalf("round %d: %s reads %q yet carries alternatives %q, from %+v", round, field, got.Reason, alts, cands)
			}
		}
		for i, a := range alts {
			if a == *got.Value {
				t.Fatalf("round %d: %s offers its own decided value %q as an alternative, from %+v", round, field, a, cands)
			}
			for _, b := range alts[i+1:] {
				if a == b {
					t.Fatalf("round %d: %s offers %q twice, from %+v", round, field, a, cands)
				}
			}
		}
	}

	if ambiguous == 0 || plain == 0 {
		t.Fatalf("the walk produced %d ambiguous and %d plain result(s) over %d rounds; the invariant needs both arms or it is a one-sided quantifier", ambiguous, plain, rounds)
	}
	if fallbackHeads == 0 {
		t.Fatalf("no round of %d put a TierFallback candidate at the head; this test then covers exactly what the two-tier one already did", rounds)
	}
}
