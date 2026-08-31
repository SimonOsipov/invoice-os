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

// aaFieldRowRE is one row of the doc's per-field table: | `field` | hits | total |. Disjoint
// from acDocRowRE by construction: a layout name carries a dot, which [a-z_]+ cannot match.
var aaFieldRowRE = regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| ([0-9]+) \\| ([0-9]+) \\|")

// aaDecisionRowRE is one row of the decision section's false-decision table:
// | `corpus_x.pdf` | `field` | `value` |. Disjoint from acDocRowRE, whose second cell is a
// number, and read from a different section anyway.
var aaDecisionRowRE = regexp.MustCompile("(?m)^\\| `(corpus_[a-z0-9_]+\\.pdf)` \\| `([a-z_]+)` \\| `([^`]+)` \\|")

// aaRunFilterRE reads the -run pattern out of the ci.yml reporting step.
var aaRunFilterRE = regexp.MustCompile(`-run '([^']+)'`)

// aaTestFuncRE is a top-level test declaration in this package.
var aaTestFuncRE = regexp.MustCompile(`(?m)^func (Test\w+)\(t \*testing\.T\) \{`)

// aaByField is the live per-field measurement, keyed by field name.
func aaByField(t *testing.T) map[string]acRow {
	t.Helper()

	out := make(map[string]acRow, len(extraction.HeaderFields))
	for _, r := range acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules).byField {
		out[r.name] = r
	}
	return out
}

// aaDistance is the distance at which ruleID reaches value on file, under rules.
func aaDistance(t *testing.T, file, ruleID, value string, rules []extraction.Tier1Rule) (float64, bool) {
	t.Helper()

	got := extraction.Resolve(rvCorpusPages(t, file), extraction.RuleSet{Tier1: rules})
	rvControl(t, got, "the rule set under test over "+file)
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

// The doc's per-field table is prose to TestCorpusDoc_RecordsTheMeasuredFloor: acDocRowRE reads
// only the per-LAYOUT rows. "Moving the floor" step 3 tells the next author that both tables
// are enforced, so leaving one unread makes the doc wrong about its own oracle.
func TestCorpusDoc_ThePerFieldTableMatchesTheMeasurement(t *testing.T) {
	section := acDocSectionText(t, acRepoFile(t, acDoc), acDocSection)

	rows := aaFieldRowRE.FindAllStringSubmatch(section, -1)
	if len(rows) == 0 {
		t.Fatalf("%s's %q section holds no per-field row; a row reads exactly: | `field` | <hits> | <total> |", acDoc, acDocSection)
	}
	// Control: the two scans must read different tables, or one table satisfies both oracles.
	if n := len(acDocRowRE.FindAllStringSubmatch(section, -1)); n != len(corpusLayouts) {
		t.Fatalf("the per-layout scan reads %d row(s), want %d; this test is measuring the wrong table", n, len(corpusLayouts))
	}

	measured := aaByField(t)
	hits, total := 0, 0
	named := make(map[string]bool, len(rows))
	for _, m := range rows {
		if strings.Contains(m[1], "corpus_") {
			t.Errorf("the per-field scan matched the layout row %q; the two tables are not disjoint", m[1])
			continue
		}
		h, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("%s row %q: hits %q is not a number: %v", acDoc, m[1], m[2], err)
		}
		n, err := strconv.Atoi(m[3])
		if err != nil {
			t.Fatalf("%s row %q: total %q is not a number: %v", acDoc, m[1], m[3], err)
		}
		if named[m[1]] {
			t.Errorf("%s's per-field table names %q twice", acDoc, m[1])
		}
		named[m[1]] = true
		hits += h
		total += n

		want, ok := measured[m[1]]
		if !ok {
			t.Errorf("%s has a per-field row for %q, which is not in HeaderFields", acDoc, m[1])
			continue
		}
		if h != want.hits || n != want.total {
			t.Errorf("%s says %s is %d/%d; it measures %d/%d", acDoc, m[1], h, n, want.hits, want.total)
		}
	}
	for _, field := range extraction.HeaderFields {
		if !named[field] {
			t.Errorf("%s's per-field table has no row for %s; a field missing from the table reads as untested", acDoc, field)
		}
	}
	if hits != tier1RecallHits || total != tier1RecallPairs {
		t.Errorf("%s's per-field table sums to %d/%d, want %d/%d", acDoc, hits, total, tier1RecallHits, tier1RecallPairs)
	}
}

// The recall rate cannot see a false decision at all: corpusExpect names no such pair, so
// nothing scores it. The doc is where an operator meets both numbers, and prose beside a pinned
// var is how the two drift apart.
func TestCorpusDoc_RecordsTheDecisionRate(t *testing.T) {
	section := acDocSectionText(t, acRepoFile(t, acDoc), acDocDecisionSection)

	for _, want := range []string{
		fmt.Sprintf("%d of %d", tier1DecisionHits, tier1DecisionPairs),
		strconv.FormatFloat(tier1DecisionRate, 'f', 4, 64),
		fmt.Sprintf("False decisions: %d", len(acFalseDecided)),
	} {
		if !strings.Contains(section, want) {
			t.Errorf("%s's %q section does not carry %q", acDoc, acDocDecisionSection, want)
		}
	}

	if len(acFalseDecided) == 0 {
		t.Fatal("acFalseDecided pins no false decision; the comparison below would be vacuous and the doc could say anything")
	}
	rows := aaDecisionRowRE.FindAllStringSubmatch(section, -1)
	// Control needle: a scan that stopped matching finds no row, which reads exactly like a doc
	// with no fabrication left to record.
	if len(rows) == 0 {
		t.Fatalf("%s's %q section holds no false-decision row for the %d acFalseDecided pins; a row reads exactly: | `corpus_x.pdf` | `field` | `value` |", acDoc, acDocDecisionSection, len(acFalseDecided))
	}

	// By identity in both directions, as TestTier1Accuracy_DecisionRateOverTheCorpus compares
	// the live set: a count alone passes when one fabrication is replaced by another.
	pinned := make(map[acPair]string, len(acFalseDecided))
	for _, f := range acFalseDecided {
		pinned[acPair{file: f.file, field: f.field}] = f.value
	}
	for _, m := range rows {
		p := acPair{file: m[1], field: m[2]}
		want, ok := pinned[p]
		if !ok {
			t.Errorf("%s records a false decision for %s / %s, which acFalseDecided does not pin", acDoc, p.file, p.field)
			continue
		}
		if want != m[3] {
			t.Errorf("%s says %s / %s decides %q; acFalseDecided pins %q", acDoc, p.file, p.field, m[3], want)
		}
		delete(pinned, p)
	}
	for p, v := range pinned {
		t.Errorf("%s's table has no row for the pinned false decision %s / %s = %q", acDoc, p.file, p.field, v)
	}
}

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

	// The two merges that bound the dials from above, each measured on the candidate the
	// widened dial produces rather than read back off the prose.
	for _, c := range []struct {
		name, file, ruleID, value, want string
		kind                            extraction.RelationKind
	}{
		{"below_merge", "corpus_stacked_labels.pdf", "t1.supplier_name.below", "Invoice Date", "0.087010", extraction.RelBelow},
		{"right_merge", "corpus_two_column.pdf", "t1.supplier_name.right", "Buyer", "0.465497", extraction.RelRight},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Widened far past both bounds, so the merge appears and its distance is readable.
			d, ok := aaDistance(t, c.file, c.ruleID, c.value, acWithDistance(t, c.kind, 0.9))
			if !ok {
				t.Fatalf("%s reaches no %q on %s even at 0.9; the recorded merge does not exist and the upper bound rests on nothing", c.ruleID, c.value, c.file)
			}
			if got := strconv.FormatFloat(d, 'f', 6, 64); got != c.want {
				t.Errorf("%s reaches %q on %s at %s; tier1.go records %s", c.ruleID, c.value, c.file, got, c.want)
			}
			if !strings.Contains(src, c.want) {
				t.Errorf("tier1.go records no %s, the measured distance of this merge", c.want)
			}
		})
	}
}
