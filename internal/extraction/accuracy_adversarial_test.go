// accuracy_adversarial_test.go: the ratchet's blind spots. accuracy_test.go guards the floor,
// the denominator, the per-layout table and the dial windows; these guard the four things it
// does not read -- the doc's per-field table, the CI step's -run filter, the report's miss
// lines, and the six-decimal distances tier1.go states as fact.
package extraction_test

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- harness ----------------------------------------------------------------

// aaRunFilterRE reads the -run pattern out of the ci.yml reporting step.
var aaRunFilterRE = regexp.MustCompile(`-run '([^']+)'`)

// aaTestFuncRE is a top-level test declaration in this package.
var aaTestFuncRE = regexp.MustCompile(`(?m)^func (Test\w+)\(t \*testing\.T\) \{`)

// aaTokenBox is the box of the token whose text is exactly text. Fatal on a miss or on a
// duplicate: either would silently measure some other token's edge.
func aaTokenBox(t *testing.T, pages []extraction.TokenPage, text string) extraction.Region {
	t.Helper()

	var out []extraction.Region
	for _, p := range pages {
		for _, tok := range p.Tokens {
			if tok.Text == text {
				out = append(out, tok.Region)
			}
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d token(s) read %q, want exactly 1; the edge measured below would be some other token's", len(out), text)
	}
	return out[0]
}

func aaStackedPages(t *testing.T) []extraction.TokenPage {
	t.Helper()
	return rvCorpusPages(t, "corpus_stacked_labels.pdf")
}

// aaDistance is the distance at which ruleID reaches value on pages, under rules. what names
// the page set for the failure message.
func aaDistance(t *testing.T, pages []extraction.TokenPage, what, ruleID, value string, rules []extraction.Tier1Rule) (float64, bool) {
	t.Helper()

	got := extraction.Resolve(pages, extraction.RuleSet{Tier1: rules})
	rvControl(t, got, "the rule set under test over "+what)
	for _, c := range got {
		if c.RuleID == ruleID && c.Value == value {
			return c.Distance, true
		}
	}
	return 0, false
}

// aaRequiredReach is the widest distance any corpusExpect value is reached at by a rule of this
// relation -- the dial's binding lower bound, measured rather than remembered.
func aaRequiredReach(t *testing.T, suffix string) (float64, string) {
	t.Helper()

	worst, what := 0.0, ""
	for _, want := range corpusExpect {
		got := extraction.Resolve(rvCorpusPages(t, want.file), extraction.RuleSet{Tier1: extraction.Tier1Rules})
		rvFloor(t, got, "the shipped Tier-1 set over "+want.file)
		for _, field := range extraction.HeaderFields {
			values, ok := want.fields[field]
			if !ok {
				continue
			}
			best, found := 0.0, false
			for _, c := range got {
				if c.Field != field || !strings.HasSuffix(c.RuleID, suffix) || !slices.Contains(values, c.Value) {
					continue
				}
				if !found || c.Distance < best {
					best, found = c.Distance, true
				}
			}
			if found && best > worst {
				worst, what = best, want.file+" / "+field
			}
		}
	}
	return worst, what
}

// --- the specs --------------------------------------------------------------

// TestTier1Accuracy_CIPrintsTheReport finds the step by the substring TestTier1Accuracy, so a
// filter of TestTier1AccuracyXX still cuts the right step and carries every needle. CI would
// catch it -- the step greps its own output -- but only after a full workflow run.
func TestTier1Accuracy_TheCIStepsRunFilterNamesARealTest(t *testing.T) {
	step := acCIStep(t, acRepoFile(t, ".github/workflows/ci.yml"))

	m := aaRunFilterRE.FindStringSubmatch(step)
	if m == nil {
		t.Fatalf("the accuracy report step carries no -run '<pattern>'; it would run the whole package and the report would be buried")
	}
	filter, err := regexp.Compile(m[1])
	if err != nil {
		t.Fatalf("the step's -run pattern %q does not compile: %v", m[1], err)
	}

	src := acRepoFile(t, "internal/extraction/accuracy_test.go")
	var declared, renders []string
	for _, chunk := range strings.Split(src, "\nfunc ") {
		d := aaTestFuncRE.FindStringSubmatch("\nfunc " + chunk)
		if d == nil {
			continue
		}
		declared = append(declared, d[1])
		if strings.Contains(chunk, "acRenderReport(") {
			renders = append(renders, d[1])
		}
	}
	// Control: a scan that stopped matching reads as a file with no tests in it.
	if len(declared) == 0 {
		t.Fatalf("no test declaration found in accuracy_test.go; this scan is reading the wrong file")
	}
	if len(renders) == 0 {
		t.Fatalf("no test in accuracy_test.go renders the report; the step would print nothing whatever its filter says")
	}

	matched := 0
	for _, name := range declared {
		if filter.MatchString(name) {
			matched++
		}
	}
	if matched == 0 {
		t.Errorf("the step's -run pattern %q matches none of the %d test(s) in accuracy_test.go; the step would run nothing and print nothing", m[1], len(declared))
	}
	for _, name := range renders {
		if !filter.MatchString(name) {
			t.Errorf("the step's -run pattern %q does not match %s, which is what renders the report; the grep would find no marker", m[1], name)
		}
	}
}

// AC #1's report is what the PR body quotes, and the named miss is the load-bearing line in it.
// Nothing in TestTier1Accuracy_ReportsPerFieldNumbers reads it: the miss lines can be dropped
// and every other assertion still holds.
func TestTier1Accuracy_TheReportNamesEveryMissedPair(t *testing.T) {
	// The needle. A live corpus with no miss left would make the loop below vacuous, so the
	// rendering is proved against a score that always carries one.
	p := acPair{file: "corpus_needle.pdf", field: "buyer_tin"}
	synthetic := acScore{
		total:  1,
		missed: []acPair{p},
		saw:    map[acPair][]string{p: {"99999999-0999"}},
	}
	rendered := acRenderReport(synthetic)
	for _, needle := range []string{"MISS", p.file, p.field, "99999999-0999"} {
		if !strings.Contains(rendered, needle) {
			t.Errorf("a score carrying one missed pair renders no %q:\n%s", needle, rendered)
		}
	}

	s := acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules)
	if s.total != tier1RecallPairs {
		t.Fatalf("scored %d pair(s), want %d", s.total, tier1RecallPairs)
	}
	report := acRenderReport(s)
	for _, miss := range s.missed {
		line := fmt.Sprintf("MISS %s / %s", miss.file, miss.field)
		if !strings.Contains(report, line) {
			t.Errorf("the report never carries %q; the PR body quotes the named miss:\n%s", line, report)
		}
		for _, v := range acExpectedValues(miss) {
			if !strings.Contains(report, v) {
				t.Errorf("the report names %s / %s but not the value %q it wanted", miss.file, miss.field, v)
			}
		}
	}
	if strings.Count(report, "MISS ") != len(s.missed) {
		t.Errorf("the report carries %d MISS line(s) for %d missed pair(s)", strings.Count(report, "MISS "), len(s.missed))
	}
}

// TestTier1_TheRecordedDistanceClaimsAreTheMeasuredOnes asserts the six-decimal figures are
// PRESENT in tier1.go. Present is not correct: a retune can leave the digits standing and make
// them false. Here each one is re-measured against the candidate it describes.
func TestTier1_TheRecordedDistancesAreTheMeasuredDistances(t *testing.T) {
	t1Floor(t)

	src := acRepoFile(t, "internal/extraction/tier1.go")

	t.Run("required_reach", func(t *testing.T) {
		below, what := aaRequiredReach(t, ".below")
		if got := strconv.FormatFloat(below, 'f', 6, 64); got != "0.009111" {
			t.Errorf("the widest below reach corpusExpect requires is %s (%s); tier1.go records 0.009111", got, what)
		}
		if below < acBelowTooNarrow || below >= acBelowLower {
			t.Errorf("the required below reach %v does not sit in [%v, %v); the window constants bound a dial nothing needs", below, acBelowTooNarrow, acBelowLower)
		}

		right, what := aaRequiredReach(t, ".right")
		if right < acRightTooNarrow || right >= acRightLower {
			t.Errorf("the required right reach %v (%s) does not sit in [%v, %v); tier1.go says right must reach %v", right, what, acRightTooNarrow, acRightLower, acRightLower)
		}
	})

	// The merges that bound the dials from above, each measured on the candidate the widened
	// dial produces rather than read back off the prose. Every one reaches a printed VALUE:
	// since EXTR-16 a bare label is not one, and both dials' old oracles were labels.
	for _, c := range []struct {
		name, what, ruleID, value, want string
		kind                            extraction.RelationKind
		pages                           func(*testing.T) []extraction.TokenPage
	}{
		{"below_merge", "corpus_stacked_labels.pdf", "t1.supplier_name.below", "22 Apr 2026", "0.107212", extraction.RelBelow, aaStackedPages},
		{"below_cross_party", "corpus_stacked_labels.pdf", "t1.supplier_name.below", "Honeywell Group", "0.321571", extraction.RelBelow, aaStackedPages},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Widened far past both bounds, so the merge appears and its distance is readable.
			d, ok := aaDistance(t, c.pages(t), c.what, c.ruleID, c.value, acWithDistance(t, c.kind, 0.9))
			if !ok {
				t.Fatalf("%s reaches no %q on %s even at 0.9; the recorded merge does not exist and the upper bound rests on nothing", c.ruleID, c.value, c.what)
			}
			if got := strconv.FormatFloat(d, 'f', 6, 64); got != c.want {
				t.Errorf("%s reaches %q on %s at %s; tier1.go records %s", c.ruleID, c.value, c.what, got, c.want)
			}
			if !strings.Contains(src, c.want) {
				t.Errorf("tier1.go records no %s, the measured distance of this merge", c.want)
			}
		})
	}

	// The right dial's merge has no corpus instance left, so it is measured on a synthetic page
	// -- and a fixture whose geometry is invented bounds the dial at a number nobody measured.
	// The gap is therefore re-read off corpus_two_column.pdf in the same subtest.
	t.Run("right_merge", func(t *testing.T) {
		const want = "0.465497"

		d, ok := aaDistance(t, acRightColumnPage(), "acRightColumnPage", "t1.supplier_name.right", "Honeywell Group", acWithDistance(t, extraction.RelRight, 0.9))
		if !ok {
			t.Fatalf("t1.supplier_name.right reaches no %q on acRightColumnPage even at 0.9; the recorded merge does not exist and the upper bound rests on nothing", "Honeywell Group")
		}
		if got := strconv.FormatFloat(d, 'f', 6, 64); got != want {
			t.Errorf("t1.supplier_name.right reaches the buyer column on acRightColumnPage at %s; tier1.go records %s", got, want)
		}
		if !strings.Contains(src, want) {
			t.Errorf("tier1.go records no %s, the measured distance of this merge", want)
		}

		real := rvCorpusPages(t, "corpus_two_column.pdf")
		supplier := aaTokenBox(t, real, "Supplier")
		buyer := aaTokenBox(t, real, "Buyer")
		if got := strconv.FormatFloat(buyer.X0-supplier.X1, 'f', 6, 64); got != want {
			t.Errorf("on corpus_two_column.pdf the gap from %q to the buyer column is %s; acRightColumnPage spans %s and no longer carries the corpus's own geometry", "Supplier", got, want)
		}
		// Each edge, not only their difference: a fixture that shifted both by the same amount
		// would keep the gap and stop being the corpus's geometry.
		for _, e := range []struct {
			what       string
			real, fixt float64
		}{
			{"the \"Supplier\" label's right edge", supplier.X1, acSupplierLabelX1},
			{"the buyer column's left edge", buyer.X0, acBuyerColumnX0},
		} {
			if got := strconv.FormatFloat(e.real, 'f', 6, 64); got != strconv.FormatFloat(e.fixt, 'f', 6, 64) {
				t.Errorf("corpus_two_column.pdf puts %s at %s; acRightColumnPage is built from %v", e.what, got, e.fixt)
			}
		}
	})
}
