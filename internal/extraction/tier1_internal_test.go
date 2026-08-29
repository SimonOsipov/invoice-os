// tier1_internal_test.go: G-01, G-14, G-16 and the adversarial specs below. Package extraction,
// not extraction_test: these read the unexported compiled matcher on the SHIPPED value, compare
// labels against anchorLexicon byte for byte, and call mustTier1Rule and anchorPattern.
package extraction

import (
	"fmt"
	"slices"
	"strings"
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
	// a retune edits the constants and nothing else reads a distance.
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

// --- adversarial -------------------------------------------------------------

// t1KeySuffixKind pairs a key suffix with the relation it names.
var t1KeySuffixKind = []struct {
	suffix string
	kind   RelationKind
}{
	{".same_token", RelSameToken},
	{".right", RelRight},
	{".below", RelBelow},
	{".sweep", RelSameToken},
}

// A rule keyed ".below" carrying RelRight at tier1MaxDistanceRight survived every other spec:
// the key list, the count, the lexicon check and t1Defective's distance pair all stay green.
// The key lands in Candidate.RuleID and is persisted, so it must name the relation it used.
func TestTier1_EveryKeyNamesItsOwnRelation(t *testing.T) {
	if len(Tier1Rules) != tier1RuleCount {
		t.Fatalf("Tier1Rules holds %d rule(s), want %d; the suffix scan would run over the wrong set", len(Tier1Rules), tier1RuleCount)
	}

	matched := 0
	for _, r := range Tier1Rules {
		var want RelationKind
		found := false
		for _, s := range t1KeySuffixKind {
			if strings.HasSuffix(r.Key, s.suffix) {
				want, found = s.kind, true
				break
			}
		}
		if !found {
			t.Errorf("key %q ends in no known relation suffix; nothing then ties it to the relation it used", r.Key)
			continue
		}
		matched++
		if r.Rule.Relation.Kind != want {
			t.Errorf("key %q names %q but the rule uses %q; RuleID is persisted and would attribute the candidate to the wrong relation", r.Key, want, r.Rule.Relation.Kind)
		}
	}
	if matched != tier1RuleCount {
		t.Errorf("%d of %d key(s) carried a known suffix; the comparison above ran over a subset", matched, tier1RuleCount)
	}
}

// mustTier1Rule panics rather than shipping a rule that resolves nothing and says nothing.
func TestTier1_MustTier1RulePanicsOnARuleParseRuleRefuses(t *testing.T) {
	// Positive control first: the same call with a sound label must NOT panic, or the recover
	// below would fire on any input and prove nothing.
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("a sound rule panicked with %v; the panic assertion below would hold for the wrong reason", p)
			}
		}()
		if r := mustTier1Rule("t1.total.control", "total", `(?i)\btotal\b`, RelSameToken, tier1MaxDistanceSameTokenJSON, ShapeAmount, BandAnywhere); r.Rule.re == nil {
			t.Fatal("the control rule built with no compiled matcher")
		}
	}()

	for _, c := range []struct {
		name, label, maxDistance string
		kind                     RelationKind
		shape                    Shape
	}{
		{"a label RE2 refuses", `(?i)\b(unclosed`, tier1MaxDistanceSameTokenJSON, RelSameToken, ShapeAmount},
		{"an unknown relation kind", `(?i)\btotal\b`, tier1MaxDistanceSameTokenJSON, RelationKind("diagonal"), ShapeAmount},
		{"an unknown shape", `(?i)\btotal\b`, tier1MaxDistanceSameTokenJSON, RelSameToken, Shape("money")},
		{"a max_distance outside [0,1]", `(?i)\btotal\b`, "2", RelRight, ShapeAmount},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("mustTier1Rule returned instead of panicking; a rule that failed to build resolves nothing and reports nothing")
				}
			}()
			mustTier1Rule("t1.total.bad", "total", c.label, c.kind, c.maxDistance, c.shape, BandAnywhere)
		})
	}
}

// anchorPattern panics on an id no lexicon entry owns: an empty Label compiles and then matches
// every token.
func TestTier1_AnchorPatternPanicsOnAnUnknownID(t *testing.T) {
	if got := anchorPattern("total"); got == "" {
		t.Fatal("anchorPattern returned an empty pattern for a known id; the panic below would prove nothing")
	}
	defer func() {
		if recover() == nil {
			t.Error("anchorPattern returned for an unknown id; a typo would ship a rule with an empty Label")
		}
	}()
	anchorPattern("no_such_lexicon_entry")
}

// jsonString is hand-rolled because encoding/json is outside this file's import allowlist. No
// lexicon pattern carries a double quote, so nothing else exercises that branch.
func TestTier1_JSONStringRoundTripsABackslashAndAQuote(t *testing.T) {
	for _, label := range []string{
		`(?i)\bre"f\\d\b`,
		`(?i)\b(total|"grand"\s*total)\b`,
		`^\s*[0-9]{8}-[0-9]{4}\s*$`,
	} {
		r := mustTier1Rule("t1.total.escape", "total", label, RelSameToken, tier1MaxDistanceSameTokenJSON, ShapeAmount, BandAnywhere)
		if r.Rule.Label != label {
			t.Errorf("Label round-tripped as %q, want %q; the escape and the decode disagree", r.Rule.Label, label)
		}
		if r.Rule.re == nil {
			t.Errorf("label %q built no compiled matcher", label)
		}
	}
	// Needle: an unescaped body must fail, or the round-trip above holds over an encoder that
	// does nothing.
	if _, err := ParseRule([]byte(`{"label":"a"b","relation":{"kind":"same_token","max_distance":0},"shape":"amount"}`)); err == nil {
		t.Error("ParseRule accepted an unescaped double quote; jsonString's escaping is untested by the round trip above")
	}
}
