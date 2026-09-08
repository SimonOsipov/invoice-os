// reconcile_doubt_adversarial_test.go: the doubt pass's negative and edge cases. Which of the
// ten header fields it covers, the conjunct the corpus is its only witness for, the
// alternatives invariant over generated input, and the relation the corpus never exercises.
package extraction_test

import (
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rdqScope is the doubt's scope list, re-typed here rather than read from the package: the
// point of the walk below is that the two agree.
var rdqScope = []string{"buyer_tin", "buyer_name", "vat", "total"}

// rdqPair is an in-scope-shaped doubt fixture on any field: an adjacent generic head and a
// farther adjacent reading carrying a different value.
func rdqPair(field string) (near, far extraction.Candidate) {
	near = extraction.Candidate{Field: field, Value: "NEAR-" + field, Reason: extraction.ReasonNone, Tier: extraction.TierGeneric, Distance: 0.03, Adjacent: true}
	far = extraction.Candidate{Field: field, Value: "FAR-" + field, Reason: extraction.ReasonNone, Tier: extraction.TierGeneric, Distance: 0.05, Adjacent: true}
	return near, far
}

// AC-4. Only the scope members with no corpus witness have a named spec each, so a FIFTH
// member added to doubtfulFields is silent unless some other spec happens to name that field.
// invoice_number, currency and subtotal are named by none. This walks all ten header fields and
// pins the partition, so the scope list can neither grow nor shrink without a red.
func TestReconcile_TheDoubtCoversExactlyFourOfTheTenHeaderFields(t *testing.T) {
	if len(extraction.HeaderFields) != 10 {
		t.Fatalf("HeaderFields names %d field(s), want 10; the partition below was measured over a different vocabulary", len(extraction.HeaderFields))
	}

	var doubted, decided []string
	for _, field := range extraction.HeaderFields {
		near, far := rdqPair(field)
		got := rcDecide(t, field, far, near) // far first: the head is the comparator's, not the caller's
		if *got.Value != near.Value {
			t.Errorf("%s = %q, want %q -- the doubt moves a reason, never a value", field, *got.Value, near.Value)
		}
		switch got.Reason {
		case extraction.ReasonAmbiguous:
			doubted = append(doubted, field)
			if want := []string{far.Value}; !slices.Equal(valuesOf(got.Alternatives), want) {
				t.Errorf("%s alternatives = %q, want %q", field, valuesOf(got.Alternatives), want)
			}
		case extraction.ReasonNone:
			decided = append(decided, field)
			if len(got.Alternatives) != 0 {
				t.Errorf("%s is decided yet carries alternatives %q", field, valuesOf(got.Alternatives))
			}
		default:
			t.Errorf("%s reads %q on a two-candidate fixture, want %q or %q", field, got.Reason, extraction.ReasonAmbiguous, extraction.ReasonNone)
		}
	}

	if !slices.Equal(doubted, rdqScope) {
		t.Errorf("the doubt covers %q, want %q -- a scope list that grew or shrank changes who loses their free-text input", doubted, rdqScope)
	}
	if want := len(extraction.HeaderFields) - len(rdqScope); len(decided) != want {
		t.Errorf("%d field(s) stay decided %q, want %d -- D-4 governs every field outside the scope", len(decided), decided, want)
	}
}

// AC-3. A head read from inside its own label token is corroborated by that label. The corpus
// witnesses this (corpus_inline_labels.pdf, wild_ruled_lines_totals.pdf) but no unit spec did,
// so dropping head.Adjacent from uncorroborated died only in the eleven-layout and DB walks.
// The two arms differ in one field and nothing else.
func TestReconcile_ASameTokenHeadOnAnInScopeFieldStaysDecided(t *testing.T) {
	const field = "buyer_name"
	near, far := rdqPair(field)

	sameToken := near
	sameToken.Adjacent, sameToken.Distance = false, 0 // what RelSameToken emits
	got := rcDecide(t, field, far, sameToken)
	if *got.Value != sameToken.Value {
		t.Errorf("%s = %q, want %q", field, *got.Value, sameToken.Value)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("a same_token head reads %q, want %q -- the label the value came out of is its corroboration", got.Reason, extraction.ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("a same_token head carries alternatives %q, want none", valuesOf(got.Alternatives))
	}

	// The discriminator: the same head one field different. Without it the zero above is also
	// what a doubt that never fires produces.
	adjacent := sameToken
	adjacent.Adjacent = true
	doubted := rcDecide(t, field, far, adjacent)
	if doubted.Reason != extraction.ReasonAmbiguous || !slices.Equal(valuesOf(doubted.Alternatives), []string{far.Value}) {
		t.Fatalf("the same head marked adjacent reads %q with alternatives %q, want %q with [%q]; the decided answer above is a zero the doubt never reaches", doubted.Reason, valuesOf(doubted.Alternatives), extraction.ReasonAmbiguous, far.Value)
	}
}

// AC-2. The invariant runs both ways -- ambiguous carries at least one alternative, and every
// other reason carries none -- and it is asserted over generated candidate sets rather than the
// four shapes the corpus supplies. An AST scan sees one assignment site; this sees the emitted
// value, so a path that empties the list after the gate, or a dedup that collapses it to one,
// fails here.
func TestReconcile_AmbiguousCarriesAnAlternativeOverGeneratedCandidateSets(t *testing.T) {
	rng := rand.New(rand.NewSource(0x22_06))
	tiers := []extraction.Tier{extraction.TierLearned, extraction.TierGeneric}
	distances := []float64{0, 0.01, 0.03, 0.05}
	values := []string{"A", "B", "C"}

	ambiguous, plain := 0, 0
	for round := 0; round < 3000; round++ {
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

		got := rcDecide(t, field, cands...)
		alts := valuesOf(got.Alternatives)
		if got.Reason == extraction.ReasonAmbiguous {
			ambiguous++
			if len(alts) == 0 {
				t.Fatalf("round %d: %s reads ambiguous with no alternative, from %+v -- the review screen renders chips instead of an input on the strength of this never happening", round, field, cands)
			}
		} else {
			plain++
			if len(alts) != 0 {
				t.Fatalf("round %d: %s reads %q yet carries alternatives %q, from %+v", round, field, got.Reason, alts, cands)
			}
		}
		for i, a := range alts {
			if a == "<nil>" {
				t.Fatalf("round %d: %s offers a nil-valued alternative at %d, from %+v", round, field, i, cands)
			}
			if a == *got.Value {
				t.Fatalf("round %d: %s offers its own decided value %q as an alternative, from %+v", round, field, a, cands)
			}
			for _, b := range alts[i+1:] {
				if a == b {
					t.Fatalf("round %d: %s offers %q twice, from %+v -- the dedup is what makes two readings of one value one answer", round, field, a, cands)
				}
			}
		}
	}

	// Floors: a one-sided generator satisfies either branch above by never entering it.
	if ambiguous == 0 || plain == 0 {
		t.Fatalf("the generator produced %d ambiguous and %d decided result(s); both branches must be entered or the invariant was checked on one side only", ambiguous, plain)
	}
}

// AC-1. Every doubtful cell the corpus produces heads on a below read, so the rightward
// relation's half of the doubt has no corpus witness at all. Resolve marks both relations
// adjacent (TestResolve_EveryRelationBesideTheLabelMarksItsCandidateAdjacent) and decideField
// reads the flag, not the rule id -- this is the walk that puts the two together on real
// tokens.
func TestResolve_ARightwardHeadReachesTheDoubtToo(t *testing.T) {
	const field = "vat"
	// "VAT:" anchors two amounts in its own band. Neither amount carries a lexicon label, so
	// the nearer one does not block the farther one at crossesALabel.
	page := rvPage(
		rvTok("VAT:", 0.10, 0.20, 0.20, 0.23),
		rvTok("75.00", 0.25, 0.20, 0.35, 0.23),
		rvTok("150.00", 0.45, 0.20, 0.55, 0.23),
	)
	rules := extraction.RuleSet{Tier1: []extraction.Tier1Rule{
		rvTier1(t, "g.vat.right", field, `(?i)\bvat\b`, extraction.RelRight, 0.40, extraction.ShapeAmount),
	}}

	cands := extraction.Resolve(page, rules)
	rvFloor(t, cands, "the rightward VAT arrangement")
	vats := rvFor(cands, field)
	if len(vats) != 2 {
		t.Fatalf("the arrangement reaches %d %s candidate(s) %q, want 2 -- one head and one competitor", len(vats), field, rvValues(vats))
	}
	for _, c := range vats {
		if !c.Adjacent || c.Tier != extraction.TierGeneric || !strings.HasSuffix(c.RuleID, ".right") {
			t.Fatalf("%s = %q reads adjacent=%v tier=%d rule=%s, want an adjacent generic RIGHTWARD read -- this spec's whole subject is the relation the corpus never doubts", field, c.Value, c.Adjacent, c.Tier, c.RuleID)
		}
	}

	got := rcDecide(t, field, cands...)
	if *got.Value != "75.00" {
		t.Errorf("%s = %q, want %q -- the nearer reading decides", field, *got.Value, "75.00")
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("%s reads %q, want %q -- the doubt is keyed on the flag, not on the below relation the corpus happens to supply", field, got.Reason, extraction.ReasonAmbiguous)
	}
	if want := []string{"150.00"}; !slices.Equal(valuesOf(got.Alternatives), want) {
		t.Errorf("%s alternatives = %q, want %q", field, valuesOf(got.Alternatives), want)
	}
}
