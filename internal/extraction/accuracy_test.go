// accuracy_test.go: how much of the golden corpus the shipped Tier-1 set reaches, and the
// ratchet that stops the number falling. M-01..M-09.
//
// Separate from corpus_test.go, which owns the expectation table: that table says what must be
// reachable, this file says how much of it IS reached and refuses a regression. The floor is a
// ratchet set to the measured value, never a target (D-17).
//
// Same two rules as the rest of the package bind every spec here: a quantifier over Resolve's
// output carries a floor first, and an asserted zero carries a positive control in the same test.
package extraction_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// --- the ratchet ------------------------------------------------------------

// RECALL, not accuracy: a hit is the expected value appearing anywhere among the field's
// candidates. Which candidate decideField picks is the decision rate, pinned separately below.
//
// Measured 2026-08-29 on feature/extr-04-anchor-rules-and-field-resolution: the shipped Tier-1
// set reaches 43 of the 44 (layout, field) pairs corpusExpect names across the six committed
// layouts. The one miss is corpus_two_column.pdf / buyer_tin, the pair t1aGaps records.
// A ratchet: docs/extraction-corpus.md says how to move it and why it may only go up.
//
// Written as a quotient of two pinned integers rather than a rounded decimal, so the run-time
// float64(hits)/float64(total) M-01 compares against is bit-identical to the boundary.
const (
	tier1RecallHits  = 43
	tier1RecallPairs = 44
	tier1RecallFloor = float64(tier1RecallHits) / float64(tier1RecallPairs)
)

// acReportMarker titles the report. ci.yml greps for it (M-09) and M-02 asserts the report
// carries it, so a rename cannot leave the workflow silently grepping for nothing.
const acReportMarker = "tier-1 accuracy over the golden corpus"

// M-05's mutilation: every rule for one field, removed. invoice_number has no format-only
// sweep, so its three relation rules are the whole of its reach.
const (
	acMutilatedField = "invoice_number"
	acMutilatedRules = 29 // 32 shipped minus invoice_number's three
	acMutilatedHits  = 37 // 43 minus the six pairs invoice_number carries
)

// The window each distance dial must stay inside, measured on this branch. Both dials are
// bounded on BOTH sides. Distance is the box GAP (resolve.go:177), not centre-to-centre.
const (
	acRightLower     = 0.2060 // 43/44 here; 42/44 at acRightTooNarrow
	acRightUpper     = 0.4655 // t1.supplier_name.right reaches "Buyer" on corpus_two_column.pdf
	acRightTooNarrow = 0.2059
	acBelowLower     = 0.0095 // 43/44 here; 41/44 at acBelowTooNarrow
	acBelowUpper     = 0.0870 // the widest clean value; the merge sits just above, at acBelowMerges
	acBelowMerges    = 0.0875 // t1.supplier_name.below reaches "Invoice Date" on the stacked layout
	acBelowTooNarrow = 0.0090
)

// --- harness ----------------------------------------------------------------

// acPair is one (layout, field) expectation -- the unit the rate counts.
type acPair struct{ file, field string }

// acRow is one line of the report: a layout's or a field's hits over its own denominator.
type acRow struct {
	name        string
	hits, total int
}

// acScore is one walk of corpusExpect under one rule set.
type acScore struct {
	hits, total int
	missed      []acPair            // in corpusExpect x HeaderFields order
	saw         map[acPair][]string // what each missed pair did produce, for the failure message
	byFile      []acRow             // one per corpusExpect row, in table order
	byField     []acRow             // one per HeaderFields entry, INCLUDING any at zero
}

// rate is NaN over an empty denominator, and every NaN comparison is false, so a caller that
// forgot its denominator floor fails rather than reading 0/0 as a pass.
func (s acScore) rate() float64 { return float64(s.hits) / float64(s.total) }

// acCountPairs is the denominator, counted straight off corpusExpect without resolving
// anything. M-03 pins it against a hand-written constant so a deleted row cannot flatter the
// rate by shrinking what it is measured over.
func acCountPairs() int {
	n := 0
	for _, want := range corpusExpect {
		for _, field := range extraction.HeaderFields {
			if _, ok := want.fields[field]; ok {
				n++
			}
		}
	}
	return n
}

// acExpectedValues is what corpusExpect names for one pair, for a failure message.
func acExpectedValues(p acPair) []string {
	for _, want := range corpusExpect {
		if want.file == p.file {
			return want.fields[p.field]
		}
	}
	return nil
}

// acScoreRules resolves every corpus layout against rules alone and counts the pairs whose
// expected value appears ANYWHERE among that field's candidates. Rank is deliberately not read
// here -- that is what makes this recall; the decision rate below is what reads rank.
func acScoreRules(t *testing.T, what string, rules []extraction.Tier1Rule) acScore {
	t.Helper()

	if len(rules) == 0 {
		t.Fatalf("%s holds no rule; the rate would be 0 for a reason that is not a regression", what)
	}
	corpusRequireCommitted(t)
	if len(corpusExpect) == 0 {
		t.Fatal("corpusExpect is empty; the walk below would score 0/0 and every rate assertion would read NaN")
	}

	s := acScore{saw: map[acPair][]string{}}
	idx := make(map[string]int, len(extraction.HeaderFields))
	for i, field := range extraction.HeaderFields {
		idx[field] = i
		s.byField = append(s.byField, acRow{name: field})
	}

	for _, want := range corpusExpect {
		got := extraction.Resolve(rvCorpusPages(t, want.file), extraction.RuleSet{Learned: nil, Tier1: rules})
		// A rate assembled from six empty results is 0/44 and reads as a rule regression rather
		// than as a broken reader.
		rvFloor(t, got, what+" over "+want.file)

		file := acRow{name: want.file}
		// HeaderFields, not a range over the map: the report order has to be stable.
		for _, field := range extraction.HeaderFields {
			values, ok := want.fields[field]
			if !ok {
				continue
			}
			if len(values) == 0 {
				t.Fatalf("%s expects %s with no value; the pair would count as a miss for the wrong reason", want.file, field)
			}
			s.total++
			file.total++
			s.byField[idx[field]].total++

			seen := rvValues(rvFor(got, field))
			hit := false
			for _, v := range values {
				if slices.Contains(seen, v) {
					hit = true
				}
			}
			if hit {
				s.hits++
				file.hits++
				s.byField[idx[field]].hits++
				continue
			}
			p := acPair{file: want.file, field: field}
			s.missed = append(s.missed, p)
			s.saw[p] = seen
		}
		s.byFile = append(s.byFile, file)
	}
	return s
}

// acRenderReport is the table AC #1 puts in CI output. byField is indexed by HeaderFields, so a
// field with no expectation renders 0/0 rather than vanishing -- a field silently absent from
// the report reads as tested.
func acRenderReport(s acScore) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s: %d / %d = %s (floor %d / %d = %s)\n",
		acReportMarker, s.hits, s.total, strconv.FormatFloat(s.rate(), 'f', 4, 64),
		tier1RecallHits, tier1RecallPairs, strconv.FormatFloat(tier1RecallFloor, 'f', 4, 64))

	b.WriteString("  per layout:\n")
	for _, r := range s.byFile {
		fmt.Fprintf(&b, "    %-28s %d/%d\n", r.name, r.hits, r.total)
	}
	b.WriteString("  per field:\n")
	for _, r := range s.byField {
		fmt.Fprintf(&b, "    %-28s %d/%d\n", r.name, r.hits, r.total)
	}
	for _, p := range s.missed {
		fmt.Fprintf(&b, "  MISS %s / %s wanted %v, candidates were %v\n", p.file, p.field, acExpectedValues(p), s.saw[p])
	}
	return b.String()
}

// acWithDistance clones the shipped set and retunes every rule of kind. A struct copy keeps the
// compiled matcher and the lexicon-sourced label, so a variant never re-parses a rule and forks
// the pattern the fingerprint is built from (G-14).
func acWithDistance(t *testing.T, kind extraction.RelationKind, maxDistance float64) []extraction.Tier1Rule {
	t.Helper()

	out := slices.Clone(extraction.Tier1Rules)
	n := 0
	for i := range out {
		if out[i].Rule.Relation.Kind != kind {
			continue
		}
		if out[i].Rule.Relation.MaxDistance == maxDistance {
			t.Fatalf("%s already sits at %v; the variant is the shipped set and asserts nothing about the dial", out[i].Key, maxDistance)
		}
		out[i].Rule.Relation.MaxDistance = maxDistance
		n++
	}
	if n == 0 {
		t.Fatalf("no shipped rule uses relation %q; the variant is the shipped set and asserts nothing", kind)
	}
	return out
}

// acDoc is the operator page both numbers are recorded on; acDocSection and
// acDocDecisionSection are the headings they live under. Scoping each scan to its own section
// keeps it off the layout table in "## The six layouts", whose rows share the leading cell but
// carry prose in the second column, and keeps the two numbers' tables disjoint.
const (
	acDoc                = "docs/extraction-corpus.md"
	acDocSection         = "## Tier-1 recall and the floor"
	acDocDecisionSection = "## Tier-1 decision rate"
)

// acDocRowRE is one row of the measured per-layout table: | `corpus_x.pdf` | hits | total |
var acDocRowRE = regexp.MustCompile("(?m)^\\| `(corpus_[a-z0-9_]+\\.pdf)` \\| ([0-9]+) \\| ([0-9]+) \\|")

// acRepoFile reads a repo-relative path. A test binary's working directory is its own package
// directory, so the repo root is two levels up.
func acRepoFile(t *testing.T, rel string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s is empty; every scan below would find nothing and report it as a clean file", rel)
	}
	return string(raw)
}

// acDocSectionText is heading's body in doc, up to the next heading of the same level.
func acDocSectionText(t *testing.T, doc, heading string) string {
	t.Helper()

	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("%s carries no %q section; the measured numbers this scan reads all live there", acDoc, heading)
	}
	body := doc[i+len(heading):]
	if j := strings.Index(body, "\n## "); j >= 0 {
		body = body[:j]
	}
	if strings.TrimSpace(body) == "" {
		t.Fatalf("%s's %q section is empty", acDoc, heading)
	}
	return body
}

// acCIStep is the ci.yml step that prints the accuracy report, cut on the workflow's own step
// indentation so M-09's assertions hold against ONE step rather than three unrelated ones.
func acCIStep(t *testing.T, yaml string) string {
	t.Helper()

	for _, chunk := range strings.Split(yaml, "\n      - ") {
		if strings.Contains(chunk, "TestTier1Accuracy") {
			return chunk
		}
	}
	t.Fatalf("no .github/workflows/ci.yml step runs `go test -run TestTier1Accuracy`; rlsgate deletes a passing test's output (internal/tools/rlsgate/rlsgate.go:63-66) and the gated step runs this package, so without a step of its own the report never reaches CI output (AC #1)")
	return ""
}

// --- the specs --------------------------------------------------------------

// M-01. The ratchet itself. It sees what the per-pair exemption list absorbs: add two layouts
// whose pairs Tier-1 cannot reach and record both in t1aGaps, and
// TestTier1_ReachesEveryCorpusExpectation goes green while the rate falls and this goes red.
func TestTier1Accuracy_MeetsTheFloor(t *testing.T) {
	t1Floor(t)

	s := acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules)
	t.Log("\n" + acRenderReport(s))

	if s.total != tier1RecallPairs {
		t.Fatalf("scored %d pair(s), want %d; the rate would be taken over the wrong denominator", s.total, tier1RecallPairs)
	}
	rate := s.rate()
	if rate >= tier1RecallFloor {
		return
	}
	for _, p := range s.missed {
		t.Errorf("%s: %s reached none of %v; candidates were %v", p.file, p.field, acExpectedValues(p), s.saw[p])
	}
	t.Errorf("tier-1 reaches %d/%d = %v, below the floor %d/%d = %v. The floor is a ratchet: fix the rules, never lower it (docs/extraction-corpus.md)",
		s.hits, s.total, rate, tier1RecallHits, tier1RecallPairs, tier1RecallFloor)
}

// M-02. AC #1's report, asserted rather than observed. The denominators must sum to the pinned
// total and every HeaderFields name must be rendered, including one standing at zero.
func TestTier1Accuracy_ReportsPerFieldNumbers(t *testing.T) {
	s := acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules)
	report := acRenderReport(s)
	t.Log("\n" + report)

	if len(s.byField) != len(extraction.HeaderFields) {
		t.Fatalf("the report holds %d field row(s), want %d; it is not indexed by HeaderFields", len(s.byField), len(extraction.HeaderFields))
	}
	if len(s.byFile) != len(corpusExpect) {
		t.Errorf("the report holds %d layout row(s), want %d; a layout is missing from the table", len(s.byFile), len(corpusExpect))
	}
	if !strings.Contains(report, acReportMarker) {
		t.Errorf("the report does not carry %q; ci.yml greps for that marker and would pass having printed nothing", acReportMarker)
	}

	sum := 0
	for i, r := range s.byField {
		if r.name != extraction.HeaderFields[i] {
			t.Errorf("field row %d names %q, want %q; the rows are not in vocabulary order", i, r.name, extraction.HeaderFields[i])
		}
		if r.hits > r.total {
			t.Errorf("%s reports %d hit(s) over %d pair(s)", r.name, r.hits, r.total)
		}
		sum += r.total
		if !strings.Contains(report, r.name) {
			t.Errorf("the rendered report never names %q; a field missing from the table reads as untested", r.name)
		}
	}
	if sum != tier1RecallPairs {
		t.Errorf("the per-field denominators sum to %d, want %d; a field was dropped from the report or counted twice", sum, tier1RecallPairs)
	}

	files := 0
	for _, r := range s.byFile {
		files += r.total
		if !strings.Contains(report, r.name) {
			t.Errorf("the rendered report never names layout %q", r.name)
		}
	}
	if files != tier1RecallPairs {
		t.Errorf("the per-layout denominators sum to %d, want %d", files, tier1RecallPairs)
	}

	// The needle for `if total == 0 { continue }`. Every field on this corpus carries at least
	// one expectation, so only a synthetic all-zero score can prove a 0/0 field still renders.
	zero := acScore{}
	for _, field := range extraction.HeaderFields {
		zero.byField = append(zero.byField, acRow{name: field})
	}
	empty := acRenderReport(zero)
	for _, field := range extraction.HeaderFields {
		if !strings.Contains(empty, field) {
			t.Errorf("a score with no expectation at all renders no row for %q; a zero-total field must render 0/0, never be skipped", field)
		}
	}
	if !strings.Contains(empty, "0/0") {
		t.Errorf("a score with no expectation at all renders no 0/0 row:\n%s", empty)
	}
}

// M-03. The denominator, pinned to a hand-written count. `> 0` alone survives deleting five of
// the six rows; exact equality catches a deleted row -- which would flatter the rate towards
// 1.0 -- and an unannounced added one.
func TestTier1Accuracy_ScoresAgainstANonEmptyExpectation(t *testing.T) {
	pairs := acCountPairs()
	if pairs == 0 {
		t.Fatal("corpusExpect names no field in HeaderFields; the rate would be taken over nothing and 0/0 is not a rate")
	}
	if pairs != tier1RecallPairs {
		t.Errorf("corpusExpect names %d (layout, field) pair(s), want %d -- move tier1RecallHits and tier1RecallPairs together, and the table in %s with them", pairs, tier1RecallPairs, acDoc)
	}
	if len(corpusExpect) != len(corpusLayouts) {
		t.Errorf("corpusExpect holds %d row(s) and the corpus %d layout(s); the walk would miss a layout", len(corpusExpect), len(corpusLayouts))
	}
	if tier1RecallHits > tier1RecallPairs {
		t.Errorf("tier1RecallHits %d exceeds tier1RecallPairs %d; the floor is above 1 and M-01 can never pass", tier1RecallHits, tier1RecallPairs)
	}
	if tier1RecallHits <= 0 {
		t.Errorf("tier1RecallHits is %d; a floor at or below zero makes M-01 unfailable", tier1RecallHits)
	}
}

// M-04. The floor leaves less than one pair of slack, so a single further miss fails M-01.
// `0 < floor <= 1` alone survives floor = 0.01, which makes M-01 unfailable -- the exact defect
// this spec exists to prevent. It is also what forces the floor UP when a gap closes.
func TestTier1Accuracy_FloorIsNotVacuous(t *testing.T) {
	if tier1RecallFloor <= 0 || tier1RecallFloor > 1 {
		t.Fatalf("the floor is %v, outside (0, 1]; no rate can be compared against it meaningfully", tier1RecallFloor)
	}

	s := acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules)
	if s.total != tier1RecallPairs {
		t.Fatalf("scored %d pair(s), want %d; one pair of slack would be the wrong size", s.total, tier1RecallPairs)
	}

	rate := s.rate()
	onePair := 1.0 / float64(s.total)
	if tier1RecallFloor <= rate-onePair {
		t.Errorf("tier-1 reaches %v and the floor is %v, a slack of %v -- a whole pair could regress unnoticed. Raise tier1RecallHits to %d (a ratchet only goes up) and update %s in the same commit",
			rate, tier1RecallFloor, rate-tier1RecallFloor, s.hits, acDoc)
	}
	if tier1RecallFloor > rate {
		t.Errorf("the floor %v is above the measured rate %v; M-01 can never pass and the floor is a prediction, not a ratchet", tier1RecallFloor, rate)
	}
}

// M-05. The needle proving the floor can be crossed at all: without it, "the rate is above the
// floor" is equally satisfied by a Resolve that returns nothing. The whole sandwich
// 0 < mutilated < floor <= full sits in this one test.
func TestTier1Accuracy_AMutilatedRuleSetFallsBelowTheFloor(t *testing.T) {
	t1Floor(t)

	mutilated := make([]extraction.Tier1Rule, 0, len(extraction.Tier1Rules))
	for _, r := range extraction.Tier1Rules {
		if r.Field == acMutilatedField {
			continue
		}
		mutilated = append(mutilated, r)
	}
	if len(mutilated) != acMutilatedRules {
		t.Fatalf("the mutilated set holds %d rule(s), want %d; %q lost a different number of rules than this spec assumes", len(mutilated), acMutilatedRules, acMutilatedField)
	}

	cut := acScoreRules(t, "the shipped set minus every "+acMutilatedField+" rule", mutilated)
	full := acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules)
	if cut.total != tier1RecallPairs || full.total != tier1RecallPairs {
		t.Fatalf("scored %d and %d pair(s), want %d each; the two rates are not taken over one denominator", cut.total, full.total, tier1RecallPairs)
	}
	cutRate, fullRate := cut.rate(), full.rate()
	t.Logf("mutilated %d/%d = %v, shipped %d/%d = %v, floor %v", cut.hits, cut.total, cutRate, full.hits, full.total, fullRate, tier1RecallFloor)

	if cut.hits != acMutilatedHits {
		t.Errorf("the mutilated set reaches %d pair(s), want %d; %q contributes a different number of hits than measured", cut.hits, acMutilatedHits, acMutilatedField)
	}
	if cutRate <= 0 {
		t.Errorf("the mutilated set scores %v; a zero means Resolve returned nothing at all, not that removing those rules is what cost the hits", cutRate)
	}
	if cutRate >= tier1RecallFloor {
		t.Errorf("the mutilated set scores %v, not below the floor %v; the floor cannot be crossed by removing rules, so M-01 asserts nothing", cutRate, tier1RecallFloor)
	}
	if tier1RecallFloor > fullRate {
		t.Errorf("the floor %v is above the shipped set's own rate %v; the control half of the sandwich does not hold", tier1RecallFloor, fullRate)
	}
}

// M-06. The doc is a live oracle, not prose beside the number: its per-layout table is parsed
// and summed. A bare substring search for the float passes over a doc that never had a table.
func TestCorpusDoc_RecordsTheMeasuredFloor(t *testing.T) {
	section := acDocSectionText(t, acRepoFile(t, acDoc), acDocSection)

	floor := strconv.FormatFloat(tier1RecallFloor, 'f', 4, 64)
	if !strings.Contains(section, floor) {
		t.Errorf("%s's %q section does not carry the floor %s (AC #3)", acDoc, acDocSection, floor)
	}
	reach := fmt.Sprintf("%d of %d", tier1RecallHits, tier1RecallPairs)
	if !strings.Contains(section, reach) {
		t.Errorf("%s's %q section does not carry %q", acDoc, acDocSection, reach)
	}

	rows := acDocRowRE.FindAllStringSubmatch(section, -1)
	if len(rows) == 0 {
		t.Fatalf("%s's %q section holds no per-layout row; a row reads exactly: | `corpus_x.pdf` | <hits> | <total> |", acDoc, acDocSection)
	}
	if len(rows) != len(corpusLayouts) {
		t.Errorf("%s's table holds %d layout row(s), want %d -- one per name in corpusLayouts", acDoc, len(rows), len(corpusLayouts))
	}

	// The rows are compared against a live measurement, not merely summed: a table that sums to
	// 43/44 with the numbers in the wrong layouts would otherwise read as correct.
	measured := make(map[string]acRow, len(corpusLayouts))
	for _, r := range acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules).byFile {
		measured[r.name] = r
	}

	hits, total := 0, 0
	named := make(map[string]bool, len(rows))
	for _, m := range rows {
		h, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("%s row %q: hits %q is not a number: %v", acDoc, m[1], m[2], err)
		}
		n, err := strconv.Atoi(m[3])
		if err != nil {
			t.Fatalf("%s row %q: total %q is not a number: %v", acDoc, m[1], m[3], err)
		}
		if named[m[1]] {
			t.Errorf("%s names %q twice", acDoc, m[1])
		}
		named[m[1]] = true
		hits += h
		total += n

		want, ok := measured[m[1]]
		if !ok {
			t.Errorf("%s has a row for %q, which is not a corpus layout", acDoc, m[1])
			continue
		}
		if h != want.hits || n != want.total {
			t.Errorf("%s says %s is %d/%d; it measures %d/%d", acDoc, m[1], h, n, want.hits, want.total)
		}
	}
	for _, name := range corpusLayouts {
		if !named[name] {
			t.Errorf("%s's table has no row for %s", acDoc, name)
		}
	}
	if hits != tier1RecallHits || total != tier1RecallPairs {
		t.Errorf("%s's table sums to %d/%d, want %d/%d; the doc and the constants disagree", acDoc, hits, total, tier1RecallHits, tier1RecallPairs)
	}

	// AC #5: the doc must say how to move the floor and why it may only go up, not merely
	// record the number. Without these the table is a snapshot and nothing tells the next
	// author that lowering the constant is the one move a ratchet forbids.
	for _, phrase := range []string{"tier1RecallHits", "t1aGaps", "TestTier1Accuracy", "only go up"} {
		if !strings.Contains(section, phrase) {
			t.Errorf("%s's %q section never mentions %q; the procedure for moving the floor is incomplete (AC #5)", acDoc, acDocSection, phrase)
		}
	}
}

// M-07. The weld between the two oracles. The rate's miss set and the per-pair exemption list
// are one set, compared element by element in both directions -- otherwise the floor and
// t1aGaps can move independently and each will look green while contradicting the other.
func TestTier1Accuracy_TheMissedPairsAreExactlyTheRecordedGaps(t *testing.T) {
	s := acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules)
	if s.total != tier1RecallPairs {
		t.Fatalf("scored %d pair(s), want %d; over an empty walk both sets below would be empty and equal", s.total, tier1RecallPairs)
	}
	if s.hits+len(s.missed) != s.total {
		t.Fatalf("%d hit(s) plus %d miss(es) is not the %d pair(s) scored", s.hits, len(s.missed), s.total)
	}

	recorded := make(map[acPair]bool, len(t1aGaps))
	for _, g := range t1aGaps {
		recorded[acPair{file: g.file, field: g.field}] = true
	}
	missed := make(map[acPair]bool, len(s.missed))
	for _, p := range s.missed {
		missed[p] = true
	}

	for p := range missed {
		if !recorded[p] {
			t.Errorf("%s / %s is missed by the rate but not recorded in t1aGaps; the two oracles disagree", p.file, p.field)
		}
	}
	for p := range recorded {
		if !missed[p] {
			t.Errorf("t1aGaps records %s / %s, which the rate reaches; drop it from t1aGaps and raise tier1RecallHits in the same commit", p.file, p.field)
		}
	}
	if len(missed) != len(recorded) {
		t.Errorf("%d missed pair(s) against %d recorded gap(s)", len(missed), len(recorded))
	}
	if s.hits != tier1RecallPairs-len(t1aGaps) {
		t.Errorf("%d hit(s) over %d pair(s) with %d recorded gap(s); the pinned hits, the pinned pairs and the exemption list cannot all be right", s.hits, s.total, len(t1aGaps))
	}
}

// M-08. The only spec that can see an OVER-WIDE dial. The accuracy rate is monotone
// non-decreasing in both distances -- widening one only adds candidates and can never lose an
// expected value -- so M-01's floor bounds them from below and nothing else does. Every
// asserted absence here carries the widened variant that produces the wrong candidate, so no
// zero in this test is vacuous.
func TestTier1_DialsStayInsideTheirMeasuredWindow(t *testing.T) {
	t1Floor(t)

	right, below := extraction.Tier1MaxDistanceRightForTest, extraction.Tier1MaxDistanceBelowForTest
	if right < acRightLower || right >= acRightUpper {
		t.Errorf("tier1MaxDistanceRight is %v, outside the measured window [%v, %v)", right, acRightLower, acRightUpper)
	}
	if below < acBelowLower || below >= acBelowUpper {
		t.Errorf("tier1MaxDistanceBelow is %v, outside the measured window [%v, %v)", below, acBelowLower, acBelowUpper)
	}

	t.Run("right_lower_bound", func(t *testing.T) {
		narrow := acScoreRules(t, fmt.Sprintf("right narrowed to %v", acRightTooNarrow), acWithDistance(t, extraction.RelRight, acRightTooNarrow))
		if narrow.total != tier1RecallPairs {
			t.Fatalf("scored %d pair(s), want %d", narrow.total, tier1RecallPairs)
		}
		if narrow.rate() >= tier1RecallFloor {
			t.Errorf("with right narrowed to %v the rate is %v, still at or above the floor %v; %v is not the binding lower bound and this bound pins nothing", acRightTooNarrow, narrow.rate(), tier1RecallFloor, acRightLower)
		}
		at := acScoreRules(t, fmt.Sprintf("right at %v", acRightLower), acWithDistance(t, extraction.RelRight, acRightLower))
		if at.rate() < tier1RecallFloor {
			t.Errorf("with right at the recorded lower bound %v the rate is %v, below the floor %v; the recorded bound is too narrow", acRightLower, at.rate(), tier1RecallFloor)
		}
	})

	t.Run("right_upper_bound", func(t *testing.T) {
		const file, field, wrong = "corpus_two_column.pdf", "supplier_name", "Buyer"
		pages := rvCorpusPages(t, file)

		shipped := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
		rvFloor(t, shipped, "the shipped set over "+file)
		if v := rvValues(rvFor(shipped, field)); slices.Contains(v, wrong) {
			t.Errorf("at the shipped right dial %v, %s = %v on %s and has already crossed into the buyer column; the two columns are merged", right, field, v, file)
		}

		wide := extraction.Resolve(pages, extraction.RuleSet{Tier1: acWithDistance(t, extraction.RelRight, acRightUpper)})
		rvControl(t, wide, fmt.Sprintf("right widened to %v over %s", acRightUpper, file))
		if v := rvValues(rvFor(wide, field)); !slices.Contains(v, wrong) {
			t.Errorf("with right widened to %v, %s = %v and still does not contain %q; the absence asserted above holds for some other reason and bounds the dial from nowhere", acRightUpper, field, v, wrong)
		}
	})

	t.Run("below_lower_bound", func(t *testing.T) {
		narrow := acScoreRules(t, fmt.Sprintf("below narrowed to %v", acBelowTooNarrow), acWithDistance(t, extraction.RelBelow, acBelowTooNarrow))
		if narrow.total != tier1RecallPairs {
			t.Fatalf("scored %d pair(s), want %d", narrow.total, tier1RecallPairs)
		}
		if narrow.rate() >= tier1RecallFloor {
			t.Errorf("with below narrowed to %v the rate is %v, still at or above the floor %v; %v is not the binding lower bound and this bound pins nothing", acBelowTooNarrow, narrow.rate(), tier1RecallFloor, acBelowLower)
		}
		at := acScoreRules(t, fmt.Sprintf("below at %v", acBelowLower), acWithDistance(t, extraction.RelBelow, acBelowLower))
		if at.rate() < tier1RecallFloor {
			t.Errorf("with below at the recorded lower bound %v the rate is %v, below the floor %v; the recorded bound is too narrow", acBelowLower, at.rate(), tier1RecallFloor)
		}
	})

	t.Run("below_upper_bound", func(t *testing.T) {
		const file, field, wrong = "corpus_stacked_labels.pdf", "supplier_name", "Invoice Date"
		pages := rvCorpusPages(t, file)

		shipped := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
		rvFloor(t, shipped, "the shipped set over "+file)
		if v := rvValues(rvFor(shipped, field)); slices.Contains(v, wrong) {
			t.Errorf("at the shipped below dial %v, %s = %v on %s and has already reached the NEXT group's label; the stacked groups are merged", below, field, v, file)
		}

		wide := extraction.Resolve(pages, extraction.RuleSet{Tier1: acWithDistance(t, extraction.RelBelow, acBelowMerges)})
		rvControl(t, wide, fmt.Sprintf("below widened to %v over %s", acBelowMerges, file))
		if v := rvValues(rvFor(wide, field)); !slices.Contains(v, wrong) {
			t.Errorf("with below widened to %v, %s = %v and still does not contain %q; the absence asserted above holds for some other reason and bounds the dial from nowhere", acBelowMerges, field, v, wrong)
		}
	})
}

// M-09. AC #1 says the numbers appear in CI output. rlsgate DELETES a passing test's output
// (internal/tools/rlsgate/rlsgate.go:63-66) and the gated ci.yml step runs this package through
// it, so a passing M-01 prints nothing without a reporting step of its own. Not one of the
// subtask plan's M-01..M-08: the plan specifies the step but leaves it with no oracle.
func TestTier1Accuracy_CIPrintsTheReport(t *testing.T) {
	yaml := acRepoFile(t, ".github/workflows/ci.yml")

	// Control needle: a scan whose shape stopped matching is indistinguishable from a missing
	// step unless something that must be found is looked for alongside it.
	if !strings.Contains(yaml, "./internal/extraction/...") {
		t.Fatalf("ci.yml never names ./internal/extraction/...; this scan is reading the wrong file and every assertion below would report a missing step that is there")
	}

	step := acCIStep(t, yaml)
	for _, want := range []struct{ needle, why string }{
		{"go test", "the step must actually run the suite"},
		{"./internal/extraction", "the step must name this package"},
		{"set -o pipefail", "ci.yml has no defaults.run.shell, so run: is bash -e and a `| tee` would mask a failed go test"},
		{acReportMarker, "the step must grep for the report marker, or a broken -run filter prints nothing and still passes"},
	} {
		if !strings.Contains(step, want.needle) {
			t.Errorf("the accuracy report step does not carry %q: %s", want.needle, want.why)
		}
	}
	if strings.Contains(step, "rls-test-gate.sh") {
		t.Errorf("the accuracy report step runs through rls-test-gate.sh, which deletes a passing test's output; the report would never print (AC #1)")
	}
}

// M-10. No prose in this repo may state a distance the corpus does not require. tier1.go and
// docs/extraction-corpus.md both call 0.0267 the reach tier1MaxDistanceBelow must clear; the
// binding reach is 0.009111 (t1.invoice_number.below and t1.issue_date.below on
// corpus_stacked_labels.pdf), and 0.0267 is the party block's TIN line, which no corpusExpect
// row asserts. Not one of the subtask plan's M-01..M-08: the plan schedules both edits but a
// comment has no other oracle, and this figure has already been carried forward twice.
func TestTier1_TheRecordedDistanceClaimsAreTheMeasuredOnes(t *testing.T) {
	for _, c := range []struct {
		file, needle string
		want, unwant []string
	}{
		{
			file:   "internal/extraction/tier1.go",
			needle: "tier1MaxDistanceBelow",
			// The binding reach, and the two merges that bound the dials from above (M-08).
			want:   []string{"0.009111", "0.087010", "0.465497"},
			unwant: []string{"0.0267"},
		},
		{
			file:   acDoc,
			needle: "corpus_stacked_labels.pdf",
			want:   []string{"0.009111"},
			// 0.087 / 0.027. The measured margin is 0.087010 / 0.009111 = 9.55x.
			unwant: []string{"3.3x"},
		},
	} {
		t.Run(c.file, func(t *testing.T) {
			src := acRepoFile(t, c.file)
			// Control needle: a scan whose file moved reads as a clean file.
			if !strings.Contains(src, c.needle) {
				t.Fatalf("%s does not carry %q; this scan is reading the wrong file and would report every claim below as corrected", c.file, c.needle)
			}
			for _, w := range c.want {
				if !strings.Contains(src, w) {
					t.Errorf("%s records no %s; that is the measured figure the false one is replaced with", c.file, w)
				}
			}
			for _, u := range c.unwant {
				if strings.Contains(src, u) {
					t.Errorf("%s still states %s; nothing in corpusExpect requires that reach, and a false number in prose is read as fact", c.file, u)
				}
			}
		})
	}
}

// --- the decision rate ------------------------------------------------------

// What the pipeline DECIDES, over the same 44 pairs the rate above scores. Measured after
// anchor specificity and the label/value split: every pair Resolve reaches is now also the pair
// Reconcile decides, so the two numbers coincide on this corpus and the remaining miss is the
// unreachable t1aGaps pair. The decision measure is still not the recall measure -- the decoy
// set in TestTier1Accuracy_TheDecisionRateIsNotTheRecallRate is what separates them.
const (
	tier1DecisionHits  = 43
	tier1DecisionPairs = 44 // the recall denominator; asserted equal below, never assumed
	tier1DecisionRate  = float64(tier1DecisionHits) / float64(tier1DecisionPairs)
)

// The decision rate with acMutilatedField's rules removed -- the pairs invoice_number decides
// correctly, gone. M-05's acMutilatedHits is the same cut scored on recall.
const acMutilatedDecisionHits = 37

// acFalseDecided is every field decided with a value its layout never prints. corpusExpect
// names the fields each layout carries, so a decided field it does not name is a reading
// fabricated out of a label. Pinned by identity, and empty since the label/value split closed
// the one fabrication: "no false decision" is asserted against the live set, not assumed.
var acFalseDecided = []struct{ file, field, value string }{}

// acDecided is one field's decided reading, for a failure message. value is "" when the field
// decided nothing.
type acDecided struct {
	acPair
	value string
}

// acDecisionScore is one walk of corpusExpect scored on Reconcile's decided value.
type acDecisionScore struct {
	hits, total int
	wrong       []acDecided // an expected pair whose decided value is not one corpusExpect names
	fabricated  []acDecided // a decided field corpusExpect's layout does not name at all
	byFile      []acRow
}

func (s acDecisionScore) rate() float64 { return float64(s.hits) / float64(s.total) }

// acCorpusPages reads every layout once, so scoring a rule-set variant costs Resolve plus
// Reconcile rather than another pdfium pass.
func acCorpusPages(t *testing.T) map[string][]extraction.TokenPage {
	t.Helper()

	corpusRequireCommitted(t)
	pages := make(map[string][]extraction.TokenPage, len(corpusLayouts))
	for _, name := range corpusLayouts {
		pages[name] = rvCorpusPages(t, name)
	}
	return pages
}

// acScoreDecisions runs Resolve then Reconcile over each layout and scores the DECIDED value.
// Same denominator as acScoreRules -- one walk of corpusExpect -- so the two are comparable.
func acScoreDecisions(t *testing.T, pages map[string][]extraction.TokenPage, what string, rules []extraction.Tier1Rule) acDecisionScore {
	t.Helper()

	if len(rules) == 0 {
		t.Fatalf("%s holds no rule; the rate would be 0 for a reason that is not a regression", what)
	}
	if len(corpusExpect) == 0 {
		t.Fatal("corpusExpect is empty; the walk below would score 0/0 and every rate assertion would read NaN")
	}

	s := acDecisionScore{}
	for _, want := range corpusExpect {
		page, ok := pages[want.file]
		if !ok {
			t.Fatalf("%s was never read; every pair on it would score as a miss for the wrong reason", want.file)
		}
		out := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(page, extraction.RuleSet{Tier1: rules})})
		if len(out) == 0 {
			t.Fatalf("%s produced no result at all under %s", want.file, what)
		}
		byName := make(map[string]extraction.FieldResult, len(out))
		for _, r := range out {
			byName[r.Name] = r
		}

		row := acRow{name: want.file}
		decided := 0
		for _, field := range extraction.HeaderFields {
			res, ok := byName[field]
			if !ok {
				t.Fatalf("%s: Reconcile returned no %s result; the walk is scoring a partial vocabulary", want.file, field)
			}
			got := ""
			if res.Value != nil {
				got = *res.Value
				decided++
			}

			values, named := want.fields[field]
			if !named {
				if res.Value != nil {
					s.fabricated = append(s.fabricated, acDecided{acPair{file: want.file, field: field}, got})
				}
				continue
			}
			if len(values) == 0 {
				t.Fatalf("%s expects %s with no value; the pair would count as a miss for the wrong reason", want.file, field)
			}
			s.total++
			row.total++

			if res.Value != nil && slices.Contains(values, got) {
				s.hits++
				row.hits++
				continue
			}
			s.wrong = append(s.wrong, acDecided{acPair{file: want.file, field: field}, got})
		}
		// The analogue of rvFloor: a layout that decides nothing at all scores 0 for a reason
		// that is not a ranking defect.
		if decided == 0 {
			t.Fatalf("%s decided no field at all under %s", want.file, what)
		}
		s.byFile = append(s.byFile, row)
	}
	return s
}

// acRenderDecisionLines is the decision block, printed under acReportMarker beside the recall
// line so CI carries both numbers or neither.
func acRenderDecisionLines(s acDecisionScore) string {
	var b strings.Builder

	fmt.Fprintf(&b, "  decision rate (what Reconcile decides): %d / %d = %s (pinned %d / %d = %s)\n",
		s.hits, s.total, strconv.FormatFloat(s.rate(), 'f', 4, 64),
		tier1DecisionHits, tier1DecisionPairs, strconv.FormatFloat(tier1DecisionRate, 'f', 4, 64))
	fmt.Fprintf(&b, "  false decisions (a field the layout never prints): %d (pinned %d)\n", len(s.fabricated), len(acFalseDecided))

	b.WriteString("  decided per layout:\n")
	for _, r := range s.byFile {
		fmt.Fprintf(&b, "    %-28s %d/%d\n", r.name, r.hits, r.total)
	}
	for _, d := range s.wrong {
		fmt.Fprintf(&b, "  DECIDED %s / %s = %q, want one of %v\n", d.file, d.field, d.value, acExpectedValues(d.acPair))
	}
	for _, d := range s.fabricated {
		fmt.Fprintf(&b, "  FALSE %s / %s = %q; the layout prints no such field\n", d.file, d.field, d.value)
	}
	return b.String()
}

// acMutilatedRuleSet is the shipped set minus every rule for acMutilatedField.
func acMutilatedRuleSet(t *testing.T) []extraction.Tier1Rule {
	t.Helper()

	out := make([]extraction.Tier1Rule, 0, len(extraction.Tier1Rules))
	for _, r := range extraction.Tier1Rules {
		if r.Field == acMutilatedField {
			continue
		}
		out = append(out, r)
	}
	if len(out) != acMutilatedRules {
		t.Fatalf("the mutilated set holds %d rule(s), want %d; %q lost a different number of rules than this spec assumes", len(out), acMutilatedRules, acMutilatedField)
	}
	return out
}

// M-11. The rank-aware measure EXTR-16 moves: 30/44 before anchor specificity and the
// label/value split, 43/44 after. The floor above is recall and is monotone in the candidate
// list, so it read 43/44 throughout. The mutilation subtest is what stops this becoming a
// second blind number.
func TestTier1Accuracy_DecisionRateOverTheCorpus(t *testing.T) {
	t1Floor(t)
	pages := acCorpusPages(t)

	s := acScoreDecisions(t, pages, "the shipped Tier-1 set", extraction.Tier1Rules)
	t.Log("\n" + acRenderReport(acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules)) + acRenderDecisionLines(s))

	// The denominator, asserted three ways: a measure whose denominator can drift flatters
	// itself by losing pairs (M-03's reason, and this rate shares that table).
	if tier1DecisionPairs != tier1RecallPairs {
		t.Fatalf("the decision rate is taken over %d pair(s) and the recall rate over %d; the two numbers are not comparable", tier1DecisionPairs, tier1RecallPairs)
	}
	if pairs := acCountPairs(); pairs != tier1DecisionPairs {
		t.Fatalf("corpusExpect names %d (layout, field) pair(s), want %d", pairs, tier1DecisionPairs)
	}
	if s.total != tier1DecisionPairs {
		t.Fatalf("scored %d pair(s), want %d; the rate would be taken over the wrong denominator", s.total, tier1DecisionPairs)
	}
	if s.hits+len(s.wrong) != s.total {
		t.Fatalf("%d hit(s) plus %d wrong decision(s) is not the %d pair(s) scored", s.hits, len(s.wrong), s.total)
	}

	if s.hits != tier1DecisionHits {
		for _, d := range s.wrong {
			t.Errorf("%s: %s decided %q, want one of %v", d.file, d.field, d.value, acExpectedValues(d.acPair))
		}
		t.Errorf("the pipeline decides %d/%d = %v, want the pinned %d/%d = %v. This is a measurement, not a ratchet: move the pin and say which layouts moved",
			s.hits, s.total, s.rate(), tier1DecisionHits, tier1DecisionPairs, tier1DecisionRate)
	}

	// The false decisions, compared by identity in both directions: a count alone passes when
	// one fabrication is fixed and another appears.
	pinned := make(map[acPair]string, len(acFalseDecided))
	for _, f := range acFalseDecided {
		pinned[acPair{file: f.file, field: f.field}] = f.value
	}
	for _, d := range s.fabricated {
		want, ok := pinned[d.acPair]
		if !ok {
			t.Errorf("%s: %s is decided %q, and corpusExpect names no such field on that layout; a value the page never printed is a false decision", d.file, d.field, d.value)
			continue
		}
		if want != d.value {
			t.Errorf("%s: %s is decided %q, want the pinned %q", d.file, d.field, d.value, want)
		}
		delete(pinned, d.acPair)
	}
	for p, v := range pinned {
		t.Errorf("%s: %s no longer decides the pinned false value %q; drop the row from acFalseDecided in the same commit as the fix", p.file, p.field, v)
	}
	if len(s.fabricated) != len(acFalseDecided) {
		t.Errorf("%d false decision(s) against %d pinned", len(s.fabricated), len(acFalseDecided))
	}

	// The control. Without it "the rate is 30/44" is equally satisfied by a measure that scores
	// something other than the decision -- the exact defect the recall rate shipped with.
	t.Run("mutilated", func(t *testing.T) {
		cut := acScoreDecisions(t, pages, "the shipped set minus every "+acMutilatedField+" rule", acMutilatedRuleSet(t))
		if cut.total != tier1DecisionPairs {
			t.Fatalf("scored %d pair(s), want %d; the two rates are not taken over one denominator", cut.total, tier1DecisionPairs)
		}
		t.Logf("mutilated %d/%d = %v, shipped %d/%d = %v", cut.hits, cut.total, cut.rate(), s.hits, s.total, s.rate())

		if cut.hits <= 0 {
			t.Errorf("the mutilated set decides %d pair(s); a zero means the pipeline read nothing at all, not that removing those rules is what cost the hits", cut.hits)
		}
		if cut.hits >= s.hits {
			t.Errorf("dropping every %s rule leaves the decision rate at %d/%d, not below the shipped %d/%d; the measure does not read what those rules decide and would score the same over any rule set",
				acMutilatedField, cut.hits, cut.total, s.hits, s.total)
		}
		if cut.hits != acMutilatedDecisionHits {
			t.Errorf("the mutilated set decides %d pair(s), want %d; %q contributes a different number of decisions than measured", cut.hits, acMutilatedDecisionHits, acMutilatedField)
		}
	})
}

// The ranking decoy: one same_token rule that reads corpus_totals_block.pdf's Sub-total AMOUNT
// token whole and files it under total. A same_token relation sits at Distance 0, and that
// layout's only real total candidate sits at 0.154431, so the decoy takes the rank while
// Resolve still reaches 5375.00. No lexicon entry matches a bare amount, so anchor specificity
// leaves the decoy alone -- it is a ranking decoy, not a reach decoy.
const (
	acDecoyKey   = "decoy.total.same_token"
	acDecoyLabel = `^\s*5,000\.00\s*$`
	acDecoyFile  = "corpus_totals_block.pdf"
	acDecoyField = "total"
	acDecoyValue = "5000.00"
)

func acDecoyRuleSet(t *testing.T) []extraction.Tier1Rule {
	t.Helper()
	return append(slices.Clone(extraction.Tier1Rules),
		rvTier1(t, acDecoyKey, acDecoyField, acDecoyLabel, extraction.RelSameToken, 0, extraction.ShapeAmount))
}

// M-12. The two measures are not one measure. On the shipped set they now coincide at 43/44 --
// every pair Resolve reaches, Reconcile decides -- so the shipped numbers alone can no longer
// tell a decision measure from a candidate-containment one. The decoy set can: it out-ranks a
// value Resolve still reaches, so recall holds and only the decision rate drops.
func TestTier1Accuracy_TheDecisionRateIsNotTheRecallRate(t *testing.T) {
	t1Floor(t)
	pages := acCorpusPages(t)

	decision := acScoreDecisions(t, pages, "the shipped Tier-1 set", extraction.Tier1Rules)
	recall := acScoreRules(t, "the shipped Tier-1 set", extraction.Tier1Rules)

	if decision.total != tier1DecisionPairs || recall.total != tier1RecallPairs || decision.total != recall.total {
		t.Fatalf("the decision rate scored %d pair(s) and the recall rate %d, want %d each; comparing them says nothing", decision.total, recall.total, tier1DecisionPairs)
	}
	if recall.hits != tier1RecallHits {
		t.Errorf("recall reaches %d/%d, want the pinned %d/%d", recall.hits, recall.total, tier1RecallHits, tier1RecallPairs)
	}
	if decision.hits != tier1DecisionHits {
		t.Errorf("the pipeline decides %d/%d, want the pinned %d/%d", decision.hits, decision.total, tier1DecisionHits, tier1DecisionPairs)
	}

	if decision.hits > recall.hits {
		t.Errorf("the decision rate %d/%d is above recall %d/%d; decideField picks one of the candidates, so it can never decide a value Resolve did not reach -- one of the two measures is wrong", decision.hits, decision.total, recall.hits, recall.total)
	}

	// The per-pair half of the same claim. Two sets of the same size can still disagree about
	// which pairs they hold, and then neither number describes the other.
	if len(recall.missed) != tier1RecallPairs-tier1RecallHits || len(recall.missed) == 0 {
		t.Fatalf("recall missed %d pair(s), want %d; with no miss the implication below is vacuous", len(recall.missed), tier1RecallPairs-tier1RecallHits)
	}
	wrong := make(map[acPair]bool, len(decision.wrong))
	for _, d := range decision.wrong {
		wrong[d.acPair] = true
	}
	for _, p := range recall.missed {
		if !wrong[p] {
			t.Errorf("recall never reaches %s / %s, yet the decision rate counts it as decided correctly; the decision measure is scoring something Resolve did not produce", p.file, p.field)
		}
	}

	// The discriminator. A measure that scored candidate containment reads the decoy set at
	// 43/44 exactly as recall does; only a measure that reads rank sees the pair the decoy takes.
	t.Run("decoy", func(t *testing.T) {
		rules := acDecoyRuleSet(t)
		decoyDecision := acScoreDecisions(t, pages, "the shipped set plus the ranking decoy", rules)
		decoyRecall := acScoreRules(t, "the shipped set plus the ranking decoy", rules)

		if decoyRecall.hits != recall.hits {
			t.Fatalf("the decoy set reaches %d pair(s) and the shipped set %d; a decoy that changes REACH cannot separate the two measures", decoyRecall.hits, recall.hits)
		}
		if decoyDecision.hits >= decoyRecall.hits {
			t.Errorf("the decoy set decides %d/%d and reaches %d/%d; a decision measure that agrees with candidate containment even here is reading the candidate list, not the decision",
				decoyDecision.hits, decoyDecision.total, decoyRecall.hits, decoyRecall.total)
		}
		if want := decision.hits - 1; decoyDecision.hits != want {
			t.Errorf("the decoy set decides %d pair(s), want %d; the decoy out-ranks exactly one reached value", decoyDecision.hits, want)
		}

		// By identity, not by count: a drop of one somewhere else is a different fact.
		taken := acPair{file: acDecoyFile, field: acDecoyField}
		found := false
		for _, d := range decoyDecision.wrong {
			if d.acPair != taken {
				continue
			}
			found = true
			if d.value != acDecoyValue {
				t.Errorf("%s / %s decides %q under the decoy set, want %q", d.file, d.field, d.value, acDecoyValue)
			}
		}
		if !found {
			t.Errorf("%s / %s is still decided correctly under the decoy set; the decoy never took the rank it exists to take", acDecoyFile, acDecoyField)
		}
	})
}
