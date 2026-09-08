// total_test.go: EXTR-23's total column against the shipped corpus. How many total candidates
// each arrangement reaches, which value it decides, and -- because "never two" is an ABSENCE --
// two planted controls that drive the SAME instrument to a positive. No database.
//
// Every walk sources each layout the way bdByLayout does: pdfium for ten, the committed docling
// golden for the image-only one.
//
// The planted rules below are test-local. Resolve takes its RuleSet by value (resolve.go), so
// extraction.Tier1Rules is read and never assigned, no row is persisted, and nothing escapes the
// test function. The story's Out-of-Scope "a new candidate SOURCE for total" constrains
// production rules; a fixture that proves an absence assertion CAN fail is its opposite.
//
// Deliberate overlap with TestWildLayouts_TheRuledTableReproducesACompetingTotal
// (wild_adversarial_test.go): that spec pins the candidate VALUES on one arrangement, this file
// pins the COUNT across all eleven. Two angles on one fact, kept on purpose.
package endtoend

import (
	"fmt"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	ttField = "total"

	// ttRuled is the arrangement EXTR-23 exists for, and the only one whose addends are both
	// decided AND sum to a DIFFERENT number than the decided total: subtotal 8000.00 +
	// vat 600.00 = 8600.00, while total reads the last line amount 1000.00. That gap is what
	// makes it the one layout able to drive corroborateTotal to a positive.
	ttRuled = "wild_ruled_lines_totals.pdf"

	// ttPlantedRuleID is the pure twin of eeDecoyRule / eeRefereeRule
	// (mutilation_db_test.go), which plant through the store on corpus_totals_block.pdf.
	ttPlantedRuleID = "ee.planted.total"
)

// ttLayoutTotal is one arrangement's total reality, measured at d294e337. count is pinned
// EXACTLY, never as an upper bound: 1 -> 0 is as much a change as 1 -> 2.
type ttLayoutTotal struct {
	file   string
	count  int
	value  string // the decided total; "" is ReasonMissing
	ruleID string // the head candidate's rule; "" when count is 0
}

var ttByLayout = []ttLayoutTotal{
	{"corpus_inline_labels.pdf", 1, "1075.00", "t1.total.same_token"},
	{"corpus_split_labels.pdf", 1, "2150.00", "t1.total.right"},
	{"corpus_stacked_labels.pdf", 1, "3225.00", "t1.total.below"},
	{"corpus_two_column.pdf", 1, "6450.00", "t1.total.same_token"},
	{"corpus_ambiguous_date.pdf", 1, "4300.00", "t1.total.same_token"},
	{"corpus_totals_block.pdf", 1, "5375.00", "t1.total.right"},
	{"wild_two_party_bare_tin.pdf", 1, "1290.00", "t1.total.right"},
	{ttRuled, 1, "1000.00", "t1.total.right"},
	{"wild_rc_due_naira.pdf", 1, "2687.50", "t1.total.right"},
	{"wild_stacked_borderless.pdf", 0, "", ""},
	{"wild_scanned_no_number.pdf", 1, "1935.00", "t1.total.right"},
}

// ttMinResolving is how many rows must reach a candidate. A table quietly rewritten to all
// zeroes would otherwise agree with a reader that had stopped reading.
const ttMinResolving = 10

// ttComplain compares one measured layout against its pinned row. It returns complaints rather
// than calling t.Errorf so the planted controls can assert the check FIRES -- an absence
// assertion whose instrument is never seen going positive is decoration.
func ttComplain(want ttLayoutTotal, totals []extraction.Candidate, decided extraction.FieldResult) []string {
	var out []string
	if got := len(totals); got != want.count {
		out = append(out, fmt.Sprintf("%s resolves %d total candidate(s), want exactly %d", want.file, got, want.count))
	}
	if len(totals) > 0 && want.ruleID != "" && totals[0].RuleID != want.ruleID {
		out = append(out, fmt.Sprintf("%s reads its head total via %s, want %s", want.file, totals[0].RuleID, want.ruleID))
	}
	if got := dtValue(decided); got != want.value {
		out = append(out, fmt.Sprintf("%s decides total = %q, want %q", want.file, got, want.value))
	}
	// decideField returns ReasonMissing only when the field reached no candidate at all, so
	// this clause and the count clause are two readings of one fact and must agree.
	wantReason := extraction.ReasonNone
	if want.count == 0 {
		wantReason = extraction.ReasonMissing
	}
	if decided.Reason != wantReason {
		out = append(out, fmt.Sprintf("%s reads total with reason %q, want %q", want.file, decided.Reason, wantReason))
	}
	return out
}

// ttPlant resolves one layout with a single test-local learned rule for total beside the
// shipped Tier-1 set. label is a JSON-escaped RE2 pattern.
func ttPlant(t *testing.T, file, label string) ([]extraction.Candidate, []extraction.FieldResult) {
	t.Helper()
	rule, err := extraction.ParseRule([]byte(
		`{"label":"` + label + `","relation":{"kind":"same_token","max_distance":0},"shape":"amount"}`))
	if err != nil {
		t.Fatalf("planted rule %q did not parse: %v", label, err)
	}
	pages, tokens := bdPages(t, dtLayout(t, file))
	if tokens == 0 {
		t.Fatalf("%s read 0 token(s); a plant over an empty page reaches nothing for a reason that is not the rule", file)
	}
	cands := extraction.Resolve(pages, extraction.RuleSet{
		Tier1:   extraction.Tier1Rules,
		Learned: []extraction.AnchorRule{{ID: ttPlantedRuleID, Field: ttField, Rule: rule}},
	})
	return cands, extraction.Reconcile(extraction.Input{Candidates: cands})
}

// ttWithoutAddends drops every subtotal and vat candidate. corroborateTotal returns its cell
// untouched unless BOTH addends reconcile to ReasonNone, so this is the input on which the
// referee provably cannot run.
func ttWithoutAddends(cands []extraction.Candidate) []extraction.Candidate {
	out := make([]extraction.Candidate, 0, len(cands))
	for _, c := range cands {
		if c.Field == "subtotal" || c.Field == "vat" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// AC-1. The exact per-layout total candidate count over all eleven arrangements, with the
// decided value and head rule each count implies. Exact, never <= 1: a layout dropping to 0 REDs
// as loudly as one reaching 2. The instrument this drives is shown going positive by
// TestEndToEnd_ThePlantedSecondTotalIsSeenByTheCountWalk.
func TestEndToEnd_NoArrangementReachesTwoCompetingTotals(t *testing.T) {
	if len(ttByLayout) == 0 || len(bdByLayout) == 0 {
		t.Fatalf("ttByLayout names %d layout(s) and bdByLayout %d; there is nothing to walk", len(ttByLayout), len(bdByLayout))
	}
	if len(ttByLayout) != len(bdByLayout) {
		t.Fatalf("ttByLayout names %d layout(s) against bdByLayout's %d; the two walks cover different corpora", len(ttByLayout), len(bdByLayout))
	}
	for i, l := range bdByLayout {
		if ttByLayout[i].file != l.file {
			t.Fatalf("ttByLayout[%d] is %s and bdByLayout[%d] is %s; the count table has drifted off the walk", i, ttByLayout[i].file, i, l.file)
		}
	}
	resolving := 0
	for _, r := range ttByLayout {
		if r.count > 0 {
			resolving++
		}
	}
	if resolving < ttMinResolving {
		t.Fatalf("the pinned table expects a total candidate on %d of %d layout(s), want at least %d; a table of zeroes agrees with a reader that stopped reading", resolving, len(ttByLayout), ttMinResolving)
	}

	walked := 0
	for i, l := range bdByLayout {
		cands, res, _ := dtRun(t, l) // fatals on a zero-token read
		walked++
		for _, c := range ttComplain(ttByLayout[i], dtFor(cands, ttField), dtResult(t, res, ttField)) {
			t.Error(c)
		}
	}
	if walked != len(bdByLayout) {
		t.Fatalf("walked %d of %d layout(s)", walked, len(bdByLayout))
	}
}

// AC-1's control. A single-branch plant reaching the printed grand total puts the ruled table at
// exactly two total candidates -- the condition AC-1 names -- and the count walk must say so.
// Without this, AC-1's green is compatible with an instrument that counts nothing.
func TestEndToEnd_ThePlantedSecondTotalIsSeenByTheCountWalk(t *testing.T) {
	want := ttByLayout[slices.IndexFunc(ttByLayout, func(r ttLayoutTotal) bool { return r.file == ttRuled })]
	if want.count != 1 {
		t.Fatalf("%s is pinned at %d total candidate(s), want 1; the plant below is built to make it 2", ttRuled, want.count)
	}

	// The unplanted arm: the plant is the ONLY difference. Without it this spec would also pass
	// on a reader that had started emitting two candidates on its own.
	bare, bareRes, _ := dtRun(t, dtLayout(t, ttRuled))
	if got := dtFor(bare, ttField); len(got) != 1 {
		t.Fatalf("%s reaches %d total candidate(s) unplanted, want 1; the plant is not the only difference and this control proves nothing", ttRuled, len(got))
	}
	if c := ttComplain(want, dtFor(bare, ttField), dtResult(t, bareRes, ttField)); len(c) != 0 {
		t.Fatalf("%s disagrees with its pinned row before anything is planted: %v", ttRuled, c)
	}

	cands, res := ttPlant(t, ttRuled, `^\\s*8,600\\.00\\s*$`)
	totals := dtFor(cands, ttField)
	if len(totals) != 2 {
		t.Fatalf("%s reaches %d total candidate(s) with one planted reading, want exactly 2: %v", ttRuled, len(totals), dtDistinct(totals))
	}

	complaints := ttComplain(want, totals, dtResult(t, res, ttField))
	if len(complaints) == 0 {
		t.Fatalf("the count walk reports nothing against %s at two total candidates; TestEndToEnd_NoArrangementReachesTwoCompetingTotals cannot fail and its green means nothing", ttRuled)
	}
	named := fmt.Sprintf("%s resolves 2 total candidate(s)", ttRuled)
	if !slices.ContainsFunc(complaints, func(c string) bool { return c == named+", want exactly 1" }) {
		t.Errorf("the count walk complains %v against %s, none of them the count clause %q; some OTHER clause is carrying the absence assertion", complaints, ttRuled, named)
	}
}

// AC-1's second control, and EXTR-29's ready oracle. The referee is inert on the corpus because
// no arrangement reaches two total candidates -- not because the mechanism cannot reach the
// corpus. This is what makes that distinction falsifiable.
//
// The plant is an ALTERNATION over both competing readings, so neither out-ranks the other and
// the cell arrives at corroborateTotal genuinely tied. Arm B strips the addends, which is the
// only input on which the referee provably cannot run; that arm is what proves arm A is the
// arithmetic (8000.00 + 600.00 = 8600.00) and not head selection.
//
// The corpus never reaches this state on its own: 8,600.00 is not a candidate at any dial.
// Total sits at x=[0.621190,0.663190] y=[0.448717,0.459808], 8,600.00 at
// x=[0.817739,0.892562] y=[0.524702,0.537566] -- right fails on a y-overlap of -0.064894, below
// on an x-overlap of -0.154549. That the plant reaches 8,600.00 at all is also this file's proof
// that the printed total IS on the page, so "absent from the candidate list" is not vacuous.
func TestEndToEnd_ThePlantedTieIsBrokenByArithmeticOnTheRuledTable(t *testing.T) {
	cands, armA := ttPlant(t, ttRuled, `^\\s*(1,000|8,600)\\.00\\s*$`)

	totals := dtFor(cands, ttField)
	for _, want := range []string{"1000.00", "8600.00"} {
		if !slices.Contains(dtDistinct(totals), want) {
			t.Fatalf("%s reaches total candidates %v with the alternation planted; %s is absent and there is no tie to referee", ttRuled, dtDistinct(totals), want)
		}
	}

	// The referee's own precondition: both addends decided, and their sum is the OTHER reading.
	for _, addend := range []struct{ field, value string }{{"subtotal", "8000.00"}, {"vat", "600.00"}} {
		f := dtResult(t, armA, addend.field)
		if dtValue(f) != addend.value || f.Reason != extraction.ReasonNone {
			t.Fatalf("%s decides %s = %q reason %q, want %q decided; decidedMoney reads only a ReasonNone addend, so the referee could not run", ttRuled, addend.field, dtValue(f), f.Reason, addend.value)
		}
	}

	a := dtResult(t, armA, ttField)
	if dtValue(a) != "8600.00" || a.Reason != extraction.ReasonNone || len(a.Alternatives) != 0 {
		t.Errorf("with the addends present %s decides total = %q reason %q alts %v, want \"8600.00\" decided with no alternative -- the arithmetic must break the tie", ttRuled, dtValue(a), a.Reason, dtAltValues(a.Alternatives))
	}

	b := dtResult(t, extraction.Reconcile(extraction.Input{Candidates: ttWithoutAddends(cands)}), ttField)
	if dtValue(b) != "1000.00" || b.Reason != extraction.ReasonAmbiguous || !slices.Equal(dtAltValues(b.Alternatives), []string{"8600.00"}) {
		t.Errorf("with the addends stripped %s decides total = %q reason %q alts %v, want \"1000.00\" ambiguous offering [\"8600.00\"] -- without this arm, arm A could be head selection rather than the referee", ttRuled, dtValue(b), b.Reason, dtAltValues(b.Alternatives))
	}
}

// AC-4. The stacked-borderless arrangement's exclusion from the story's measurement AC is a
// measured fact here, not a sentence: it resolves NO total candidate, so ReasonMissing is
// decideField's len(peers) == 0 arm and nothing else. The buyer_tin floor is what stops "no
// total candidate" meaning "no candidate at all".
func TestEndToEnd_TheStackedBorderlessArrangementResolvesNoTotal(t *testing.T) {
	const layout, tin = "wild_stacked_borderless.pdf", "99999999-1102"
	cands, res, tokens := dtRun(t, dtLayout(t, layout))
	if got := dtValue(dtResult(t, res, "buyer_tin")); got != tin {
		t.Fatalf("%s reads %d token(s) and decides buyer_tin = %q, want %q; a page that resolved nothing satisfies the assertions below for free", layout, tokens, got, tin)
	}

	if got := dtFor(cands, ttField); len(got) != 0 {
		t.Errorf("%s resolves %d total candidate(s) %v, want none", layout, len(got), dtDistinct(got))
	}
	f := dtResult(t, res, ttField)
	if f.Value != nil || f.Reason != extraction.ReasonMissing {
		t.Errorf("%s reads total = %q reason %q, want no value and %q", layout, dtValue(f), f.Reason, extraction.ReasonMissing)
	}
}
