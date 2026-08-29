// tier1_internal_test.go: G-01, G-14 and G-16. Package extraction, not extraction_test: these
// read the unexported compiled matcher on the SHIPPED value, and compare labels against
// anchorLexicon byte for byte.
package extraction

import (
	"fmt"
	"slices"
	"testing"
)

// t1Defective reports why a Tier-1 rule could never resolve, or "" when it is sound. One
// predicate, used on the shipped set and on G-01's near-miss control, so a spec that reports
// nothing is distinguishable from a predicate that cannot report.
func t1Defective(r Tier1Rule) string {
	switch {
	case r.Key == "":
		return "empty Key; it lands in Candidate.RuleID"
	case r.Field == "":
		return "empty Field; Resolve drops it"
	case r.Rule.re == nil:
		return "no compiled matcher; only ParseRule sets one and Resolve emits nothing without it"
	case r.Rule.Label == "":
		return "empty Label; regexp.Compile accepts it and every token then matches"
	}

	switch r.Rule.Relation.Kind {
	case RelSameToken, RelRight, RelBelow:
	default:
		return fmt.Sprintf("unknown relation kind %q", r.Rule.Relation.Kind)
	}

	switch r.Rule.Shape {
	case ShapeInvoiceNumber, ShapeDate, ShapeAmount, ShapeTIN, ShapeCurrency, ShapeName:
	default:
		return fmt.Sprintf("unknown shape %q", r.Rule.Shape)
	}

	// The distance a rule ships with must be the named constant, not a literal beside it:
	// EXTR-04-09 retunes by editing the constants and nothing else reads a distance.
	want := map[RelationKind]float64{
		RelSameToken: 0,
		RelRight:     tier1MaxDistanceRight,
		RelBelow:     tier1MaxDistanceBelow,
	}[r.Rule.Relation.Kind]
	if r.Rule.Relation.MaxDistance != want {
		return fmt.Sprintf("max_distance %v on a %q rule, want %v", r.Rule.Relation.MaxDistance, r.Rule.Relation.Kind, want)
	}
	return ""
}

// G-01
func TestTier1_EveryRuleHasACompiledMatcher(t *testing.T) {
	if len(Tier1Rules) != tier1RuleCount {
		t.Fatalf("Tier1Rules holds %d rule(s), want %d; every assertion below would run over the wrong set", len(Tier1Rules), tier1RuleCount)
	}

	for i, r := range Tier1Rules {
		if why := t1Defective(r); why != "" {
			t.Errorf("Tier1Rules[%d] %q: %s", i, r.Key, why)
		}
	}

	// Near-miss control: a rule built as a composite literal, skipping ParseRule. The
	// predicate must report it, or it is clean by construction and the loop above proves
	// nothing about the shipped set.
	control := Tier1Rule{
		Key:   "control.uncompiled",
		Field: "total",
		Rule:  Rule{Label: tier1TINSweepLabel, Relation: Relation{Kind: RelSameToken}, Shape: ShapeAmount},
	}
	if why := t1Defective(control); why == "" {
		t.Error("the near-miss control (a composite-literal Rule, no compiled matcher) was reported sound; t1Defective cannot fail, so every rule above passed vacuously")
	}
}

// G-14
func TestTier1_ReusesTheAnchorLexiconPatterns(t *testing.T) {
	if len(anchorLexicon) != 10 {
		t.Fatalf("anchorLexicon holds %d entry/entries, want 10; the coverage assertion below would run over the wrong table", len(anchorLexicon))
	}
	if len(Tier1Rules) != tier1RuleCount {
		t.Fatalf("Tier1Rules holds %d rule(s), want %d; every assertion below would run over the wrong set", len(Tier1Rules), tier1RuleCount)
	}

	patterns := make([]string, len(anchorLexicon))
	ids := make([]string, len(anchorLexicon))
	for i, l := range anchorLexicon {
		patterns[i] = l.Pattern
		ids[i] = l.ID
	}

	used := make([]int, len(anchorLexicon))
	var order []string // lexicon ids, in the order the label rules first reach them
	labelRules, sweepRules := 0, 0

	for _, r := range Tier1Rules {
		i := slices.Index(patterns, r.Rule.Label)
		if i < 0 {
			sweepRules++
			if r.Rule.Label != tier1TINSweepLabel {
				t.Errorf("Tier1Rules[%q] carries label %q, which is neither an anchorLexicon pattern nor tier1TINSweepLabel; a forked copy of a lexicon regex drifts from the fingerprint silently", r.Key, r.Rule.Label)
			}
			continue
		}
		labelRules++
		if used[i] == 0 {
			order = append(order, ids[i])
		}
		used[i]++
	}

	if labelRules != 30 || sweepRules != 2 {
		t.Errorf("the set splits %d label rule(s) and %d non-lexicon rule(s), want 30 and 2", labelRules, sweepRules)
	}
	for i, n := range used {
		if n == 0 {
			t.Errorf("anchorLexicon %q is used by no Tier-1 rule; its label ships in the fingerprint but resolves nothing", ids[i])
		}
	}
	if !slices.Equal(order, ids) {
		t.Errorf("the label rules reach the lexicon in the order %v, want %v; the shipped set follows anchorLexicon, which is what makes TestResolve_ReturnsFieldsInVocabularyOrder tell rule order from vocabulary order", order, ids)
	}
}

// G-16: the anchors on tier1TINSweepLabel are load-bearing. Without them the pattern still
// matches every corpus fixture the same way, so no corpus spec can tell the two apart.
func TestTier1_TheTINSweepMatchesOnlyABareToken(t *testing.T) {
	if len(Tier1Rules) != tier1RuleCount {
		t.Fatalf("Tier1Rules holds %d rule(s), want %d; the sweep lookup below would run over the wrong set", len(Tier1Rules), tier1RuleCount)
	}

	sweeps := 0
	for _, r := range Tier1Rules {
		if r.Rule.Label != tier1TINSweepLabel {
			continue
		}
		sweeps++
		if loc := r.Rule.re.FindStringIndex("99999999-0301"); loc == nil || loc[0] != 0 || loc[1] != 13 {
			t.Errorf("Tier1Rules[%q] matched a bare TIN token at %v, want the whole token [0 13]; only a whole-token match makes the label its own value", r.Key, loc)
		}
		// A labelled token is the label path's, and the remainder after the match is empty,
		// so an unanchored sweep would silently emit nothing there and look identical.
		if loc := r.Rule.re.FindStringIndex("TIN: 99999999-0301"); loc != nil {
			t.Errorf("Tier1Rules[%q] matched inside %q at %v; the sweep must recognise a bare token only", r.Key, "TIN: 99999999-0301", loc)
		}
	}
	if sweeps != 2 {
		t.Fatalf("found %d rule(s) carrying tier1TINSweepLabel, want 2; the assertions above ran over the wrong rules", sweeps)
	}
}
