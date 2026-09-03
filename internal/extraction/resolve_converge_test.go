// resolve_converge_test.go: V-01, V-02, V-03, V-06. Convergence -- per field, only the first
// rule in RuleSet.Learned that produces anything contributes.
//
// It lives in its own file because the accuracy floor and the totality oracle must stay
// byte-unedited, and V-06 is a Tier-1 control that would otherwise land in accuracy_test.go.
//
// Every fixture here is corpus_inline_labels.pdf read through the real reader. Its three
// buyer_tin rules carry DISTINCT values, so "the newer won" is provable by value and not only
// by rule id.
package extraction_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// The three labels are learnedLabel's exact output for the anchors the lexicon finds on
// corpus_inline_labels.pdf -- "(?i)" + \b + QuoteMeta(text) + \b. A rule the learn path could
// actually have written, not a hand-tuned pattern.
const (
	cvLabelBuyerTIN    = `(?i)\bBuyer TIN\b`
	cvLabelSupplierTIN = `(?i)\bSupplier TIN\b`
	cvLabelCurrency    = `(?i)\bCurrency\b`
)

// Measured on that page: same_token/"Buyer TIN" reads the buyer's TIN, same_token/
// "Supplier TIN" reads the supplier's, and below/"Buyer TIN" reads nothing at all.
const (
	cvBuyerTIN    = "99999999-0102"
	cvSupplierTIN = "99999999-0101"
	cvCurrency    = "NGN"
)

// cvBuyerTINRule builds a buyer_tin rule from one anchor label. Every rule in this file shares
// a field, so only the label and the relation separate them.
func cvBuyerTINRule(t *testing.T, id, label string) extraction.AnchorRule {
	t.Helper()
	return rvLearned(t, id, "buyer_tin", label, extraction.RelSameToken, 0, extraction.ShapeTIN)
}

// cvBarrenRule is below/"Buyer TIN" at the shipped dial: it reaches tokens, and ShapeTIN
// rejects every one of them. TestResolve_ANewerRuleThatProducesNothingDoesNotSuppressTheOlderOne
// pins that it still reads zero.
func cvBarrenRule(t *testing.T, id string) extraction.AnchorRule {
	t.Helper()
	return rvLearned(t, id, "buyer_tin", cvLabelBuyerTIN, extraction.RelBelow,
		extraction.Tier1MaxDistanceBelowForTest, extraction.ShapeTIN)
}

// cvRuleIDs and cvHasValue read the two things every spec below asserts on.
func cvRuleIDs(cs []extraction.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.RuleID
	}
	return out
}

func cvHasValue(cs []extraction.Candidate, want string) bool {
	for _, c := range cs {
		if c.Value == want {
			return true
		}
	}
	return false
}

// cvOnly fails unless every candidate carries the one rule id and the one value.
func cvOnly(t *testing.T, cs []extraction.Candidate, wantID, wantValue, what string) {
	t.Helper()
	rvFloor(t, cs, what)
	for _, c := range cs {
		if c.RuleID != wantID || c.Value != wantValue {
			t.Errorf("%s: candidate %q from rule %q; want every candidate to be %q from rule %q -- ids %v, values %v",
				what, c.Value, c.RuleID, wantValue, wantID, cvRuleIDs(cs), rvValues(cs))
		}
	}
}

// --- AC-1: two learned rules for one field, only the newer produces ---------------------

// V-01 / V-02. RuleSet.Learned is AnchorRulesFor's seq DESC order, so slice position 0 is the
// newest correction. Both rules read a real value here and the two values differ, so a result
// carrying the supplier's TIN means the superseded rule still fired.
func TestResolve_ASupersededRuleStopsFiring(t *testing.T) {
	pages := rvCorpusPages(t, rvCorpusInline)

	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		cvBuyerTINRule(t, "newer", cvLabelBuyerTIN),
		cvBuyerTINRule(t, "older", cvLabelSupplierTIN),
	}}

	got := extraction.Resolve(pages, rules)
	buyer := rvFor(got, "buyer_tin")
	cvOnly(t, buyer, "newer", cvBuyerTIN, "buyer_tin with a newer rule ahead of an older one")

	if cvHasValue(got, cvSupplierTIN) {
		t.Errorf("the superseded rule's value %q is still in the output: %+v", cvSupplierTIN, got)
	}
	if slices.Contains(cvRuleIDs(got), "older") {
		t.Errorf("the superseded rule id still stamps a candidate: ids %v", cvRuleIDs(got))
	}

	// V-02, the paired control: the older rule alone DOES read this page, so the absence
	// asserted above is suppression and not a rule that never matched anything.
	alone := rvFor(extraction.Resolve(pages, extraction.RuleSet{Learned: []extraction.AnchorRule{
		cvBuyerTINRule(t, "older", cvLabelSupplierTIN),
	}}), "buyer_tin")
	rvControl(t, alone, "the older rule as the only learned rule")
	if !cvHasValue(alone, cvSupplierTIN) {
		t.Fatalf("the older rule alone did not read %q (%v); it was never a rule this page suppresses",
			cvSupplierTIN, rvValues(alone))
	}
}

// --- AC-2: two learned rules for different fields, both produce -------------------------

// V-03. A claim taken globally rather than per field would let the first rule silence the
// second, so this is the spec that says the claim is keyed by field.
func TestResolve_TwoLearnedRulesForDifferentFieldsBothFire(t *testing.T) {
	pages := rvCorpusPages(t, rvCorpusInline)

	rules := extraction.RuleSet{Learned: []extraction.AnchorRule{
		cvBuyerTINRule(t, "rule-buyer-tin", cvLabelBuyerTIN),
		rvLearned(t, "rule-currency", "currency", cvLabelCurrency, extraction.RelSameToken, 0, extraction.ShapeCurrency),
	}}

	got := extraction.Resolve(pages, rules)
	cvOnly(t, rvFor(got, "buyer_tin"), "rule-buyer-tin", cvBuyerTIN, "buyer_tin alongside a currency rule")
	cvOnly(t, rvFor(got, "currency"), "rule-currency", cvCurrency, "currency alongside a buyer_tin rule")
}

// --- AC-4: the Tier-1 path is untouched -------------------------------------------------

// cvGoldenLayouts is the order the golden was written in. It is not corpusLayouts' order, and
// the check below pins that the two name the same six files.
var cvGoldenLayouts = []string{
	"corpus_inline_labels.pdf",
	"corpus_stacked_labels.pdf",
	"corpus_split_labels.pdf",
	"corpus_two_column.pdf",
	"corpus_totals_block.pdf",
	"corpus_ambiguous_date.pdf",
}

const cvGoldenFile = "tier1_only_candidates.golden.txt"

// cvRenderTier1 is the golden's format: one "layout \t count" line per layout, then one line
// per candidate. Every float is %.6f so the text is stable across runs.
func cvRenderTier1(t *testing.T) string {
	t.Helper()

	var b strings.Builder
	total := 0
	for _, name := range cvGoldenLayouts {
		got := extraction.Resolve(rvCorpusPages(t, name), extraction.RuleSet{Tier1: extraction.Tier1Rules})
		fmt.Fprintf(&b, "%s\t%d\n", name, len(got))
		total += len(got)
		for _, c := range got {
			region := "nil"
			if c.Region != nil {
				region = fmt.Sprintf("%d/%.6f/%.6f/%.6f/%.6f",
					c.Region.Page, c.Region.X0, c.Region.Y0, c.Region.X1, c.Region.Y1)
			}
			fmt.Fprintf(&b, "\t%s\t%s\t%s\t%d\t%.6f\t%s\n",
				c.Field, c.Value, c.RuleID, c.Tier, c.Distance, region)
		}
	}
	if total == 0 {
		t.Fatalf("the Tier-1 sweep over %d layout(s) produced no candidate at all; a byte comparison against the golden would be comparing two headers",
			len(cvGoldenLayouts))
	}
	return b.String()
}

// V-06. The golden was generated BEFORE any edit to resolve.go, so it is the no-collateral-
// damage control on the generic path: a convergence loop that also changed what Tier-1 emits,
// or what order it emits it in, moves these bytes.
//
// Unlike the accuracy floor this is not monotone in the distance dials -- it pins every value,
// rule id, distance and box, so widening a dial adds a line and fails.
func TestResolve_Tier1OnlyOutputIsUnchanged(t *testing.T) {
	corpusRequireCommitted(t)
	if len(cvGoldenLayouts) != len(corpusLayouts) {
		t.Fatalf("the golden covers %d layout(s), corpusLayouts names %d", len(cvGoldenLayouts), len(corpusLayouts))
	}
	for _, name := range corpusLayouts {
		if !slices.Contains(cvGoldenLayouts, name) {
			t.Fatalf("corpus layout %s is not covered by the golden; regenerating it after a change to resolve.go would prove nothing, so add it deliberately", name)
		}
	}

	path := filepath.Join(fxDir, cvGoldenFile)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v -- the golden is committed pre-change and is the only oracle for the generic path here", path, err)
	}
	if len(want) == 0 {
		t.Fatalf("%s is empty; a byte comparison against it would pass on any output", path)
	}

	got := cvRenderTier1(t)
	if got == string(want) {
		return
	}

	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		g, w := "<missing>", "<missing>"
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("Tier-1-only output differs from %s at line %d:\n got: %q\nwant: %q\n(%d line(s) got, %d want)",
				path, i+1, g, w, len(gotLines), len(wantLines))
		}
	}
}

// --- AC-2 under a mixed slice: the claim is a skip, not a stop, and not a global watermark ---
//
// Every convergence spec above holds Learned to two rules. Two mutations survive that shape and
// die only on three: `continue` written as `break` (a claimed field ends the whole loop, so a
// later field's rule never runs), and `before := len(all)` written as `before := 0` (once ANY
// rule has produced, every later rule claims its field whether it produced or not).
func TestResolve_ConvergenceIsPerFieldAcrossAMixedSlice(t *testing.T) {
	pages := rvCorpusPages(t, rvCorpusInline)
	currency := rvLearned(t, "rule-currency", "currency", cvLabelCurrency, extraction.RelSameToken, 0, extraction.ShapeCurrency)

	// A superseded rule sits between the winner and another field's rule. Skipping it must not
	// end the loop.
	t.Run("a claimed field does not stop the rules behind it", func(t *testing.T) {
		got := extraction.Resolve(pages, extraction.RuleSet{Learned: []extraction.AnchorRule{
			cvBuyerTINRule(t, "newer", cvLabelBuyerTIN),
			cvBuyerTINRule(t, "older", cvLabelSupplierTIN),
			currency,
		}})
		cvOnly(t, rvFor(got, "buyer_tin"), "newer", cvBuyerTIN, "buyer_tin ahead of a currency rule")
		cvOnly(t, rvFor(got, "currency"), "rule-currency", cvCurrency, "currency behind a superseded buyer_tin rule")
	})

	// A barren rule that follows a productive rule for ANOTHER field must still claim nothing.
	// Emission is counted per rule, so it is the growth of `all` across this one call that
	// decides -- not whether `all` is non-empty by the time the rule runs.
	t.Run("a barren rule behind another field's rule still claims nothing", func(t *testing.T) {
		got := extraction.Resolve(pages, extraction.RuleSet{Learned: []extraction.AnchorRule{
			currency,
			cvBarrenRule(t, "barren"),
			cvBuyerTINRule(t, "older", cvLabelSupplierTIN),
		}})
		buyer := rvFor(got, "buyer_tin")
		rvFloor(t, buyer, "buyer_tin behind a barren rule that follows a productive currency rule")
		cvOnly(t, buyer, "older", cvSupplierTIN, "buyer_tin behind a barren rule that follows a productive currency rule")
		cvOnly(t, rvFor(got, "currency"), "rule-currency", cvCurrency, "currency ahead of a barren buyer_tin rule")
		if slices.Contains(cvRuleIDs(got), "barren") {
			t.Errorf("the barren rule stamped a candidate: %+v", got)
		}
	})
}
