// total_test.go: EXTR-23's total column against the shipped corpus. How many total candidates
// each arrangement reaches, which value it decides, and -- because "never two" is an ABSENCE --
// two planted controls that drive the SAME instrument to a positive. No database.
//
// Every walk sources each layout the way bdByLayout does: pdfium for thirteen, the committed docling
// golden for the image-only one.
//
// The planted rules below are test-local. Resolve takes its RuleSet by value (resolve.go), so
// extraction.Tier1Rules is read and never assigned, no row is persisted, and nothing escapes the
// test function. The story's Out-of-Scope "a new candidate SOURCE for total" constrains
// production rules; a fixture that proves an absence assertion CAN fail is its opposite.
//
// AC-5 -- score_test.go and score_db_test.go unmodified -- is checked by hand, not by a spec: CI
// checks out at fetch-depth 1, so origin/main is absent there and a git-diff test cannot run.
//
// Deliberate overlap with TestWildLayouts_TheRuledTableReproducesACompetingTotal
// (wild_adversarial_test.go): that spec pins the candidate VALUES on one arrangement, this file
// pins the COUNT across all fourteen. Two angles on one fact, kept on purpose.
package endtoend

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	ttField = "total"

	// ttRuled is the arrangement EXTR-23 exists for, and the only one whose addends are both
	// decided AND sum to a DIFFERENT number than the anchored total: subtotal 8000.00 +
	// vat 600.00 = 8600.00, while the anchored candidate reads the last line amount 1000.00.
	// With Pages wired, EXTR-29 decides this cell by arithmetic (findTotal), never a referee pick.
	ttRuled = "wild_ruled_lines_totals.pdf"

	// ttPlantedRuleID is the pure twin of eeDecoyRule / eeRefereeRule
	// (mutilation_db_test.go), which plant through the store on corpus_totals_block.pdf.
	ttPlantedRuleID = "ee.planted.total"
)

// ttLayoutTotal is one arrangement's total reality, measured at f6c0406c. count is pinned
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
	{ttRuled, 1, "8600.00", "t1.total.right"},
	{"wild_rc_due_naira.pdf", 1, "2687.50", "t1.total.right"},
	{"wild_stacked_borderless.pdf", 1, "1612.50", "t1.total.right"},
	{"wild_scanned_no_number.pdf", 1, "1935.00", "t1.total.right"},
	{"wild_two_party_bare_tin_asprinted.pdf", 1, "1290.00", "t1.total.right"},
	{"wild_ruled_lines_totals_asprinted.pdf", 1, "8600.00", "t1.total.right"},
	{"wild_stacked_borderless_asprinted.pdf", 1, "1612.50", "t1.total.right"},
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
	// dtAmbiguous is the one table that owns which cells are doubtful: count==0 means missing, a
	// listed cell means ambiguous with its alternatives, otherwise none.
	wantReason := extraction.ReasonNone
	var wantAlts []string
	if want.count == 0 {
		wantReason = extraction.ReasonMissing
	} else if i := slices.IndexFunc(dtAmbiguous, func(c dtCell) bool { return c.layout == want.file && c.field == ttField }); i >= 0 {
		wantReason = extraction.ReasonAmbiguous
		wantAlts = dtAmbiguous[i].alts
	}
	if decided.Reason != wantReason {
		out = append(out, fmt.Sprintf("%s reads total with reason %q, want %q", want.file, decided.Reason, wantReason))
	}
	if got := dtAltValues(decided.Alternatives); !slices.Equal(got, wantAlts) {
		out = append(out, fmt.Sprintf("%s offers %q as total alternatives, want %q", want.file, got, wantAlts))
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

// AC-1. The exact per-layout total candidate count over all fourteen arrangements, with the
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

// AC-1's second control, and EXTR-29's ready oracle for the LEARNED-head shape. Both plants are
// learned rules and TierLearned outranks TierGeneric, so the head here is learned and the tie is
// D-14 equal standing, never EXTR-23-01's group widening -- that route is covered by
// TestReconcile_TheBalancingTotalWinsTheTie in reconcile_total_test.go.
//
// The referee is inert on the corpus because no arrangement reaches two total candidates -- not
// because the mechanism cannot reach the corpus. This is what makes that distinction falsifiable.
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

// The twin's total sits below its label's line; right's drop band reads it
// (TestTier1_TheDropBandStaysInsideItsMeasuredWindow).
func TestEndToEnd_TheStackedBorderlessArrangementResolvesItsOffsetTotal(t *testing.T) {
	const layout, tin = "wild_stacked_borderless.pdf", "99999999-1102"
	cands, res, tokens := dtRun(t, dtLayout(t, layout))
	if got := dtValue(dtResult(t, res, "buyer_tin")); got != tin {
		t.Fatalf("%s reads %d token(s) and decides buyer_tin = %q, want %q; a page that resolved nothing would fail the total checks below for the wrong reason", layout, tokens, got, tin)
	}

	totals := dtFor(cands, ttField)
	if got := dtDistinct(totals); !slices.Equal(got, []string{"1612.50"}) {
		t.Fatalf("%s resolves %d total candidate(s) %v, want [1612.50]", layout, len(totals), got)
	}
	if totals[0].RuleID != "t1.total.right" {
		t.Errorf("%s reads its head total via %s, want t1.total.right", layout, totals[0].RuleID)
	}
	f := dtResult(t, res, ttField)
	if dtValue(f) != "1612.50" || f.Reason != extraction.ReasonNone || len(f.Alternatives) != 0 {
		t.Errorf("%s decides total = %q reason %q alts %v, want \"1612.50\" none, no alternatives", layout, dtValue(f), f.Reason, dtAltValues(f.Alternatives))
	}
}

// ttValueEqual compares two *string by dereferenced value; both nil counts as equal.
func ttValueEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ttRegionEqual compares two *extraction.Region by value; both nil counts as equal.
func ttRegionEqual(a, b *extraction.Region) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ttFieldEqual compares one Field by value, region and reason -- never by identity.
func ttFieldEqual(a, b extraction.Field) bool {
	return ttValueEqual(a.Value, b.Value) && ttRegionEqual(a.Region, b.Region) && a.Reason == b.Reason
}

// ttCandidateEqual compares one Candidate by the fields a plant must leave untouched: Field,
// Value, RuleID, Tier, Adjacent, Distance, Region. Not Reason -- Resolve never sets it.
func ttCandidateEqual(a, b extraction.Candidate) bool {
	return a.Field == b.Field && a.Value == b.Value && a.RuleID == b.RuleID &&
		a.Tier == b.Tier && a.Adjacent == b.Adjacent && a.Distance == b.Distance &&
		ttRegionEqual(a.Region, b.Region)
}

// ttFind looks up one named field in a FieldResult slice. A missing name is a floor failure:
// the caller cannot compare against a zero value and call that agreement.
func ttFind(t *testing.T, res []extraction.FieldResult, name string) extraction.FieldResult {
	t.Helper()
	for _, r := range res {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %s in the result set; the floor over extraction.HeaderFields cannot be met", name)
	return extraction.FieldResult{}
}

// ttResultsEqual is AC-2's comparator: every extraction.HeaderFields name, decided value/region/
// reason plus alternatives element-wise. Both sides must carry all ten names, or this fatals
// rather than silently comparing a hole to a zero value.
func ttResultsEqual(t *testing.T, with, without []extraction.FieldResult) bool {
	t.Helper()
	equal := true
	for _, name := range extraction.HeaderFields {
		a, b := ttFind(t, with, name), ttFind(t, without, name)
		if !ttFieldEqual(a.Field, b.Field) || len(a.Alternatives) != len(b.Alternatives) {
			equal = false
			continue
		}
		for i := range a.Alternatives {
			if !ttFieldEqual(a.Alternatives[i], b.Alternatives[i]) {
				equal = false
				break
			}
		}
	}
	return equal
}

// ttResultsEqual sees every component it compares, not only the value the ruled pair moves.
func TestEndToEnd_TheHeaderComparatorSeesEveryComponent(t *testing.T) {
	str := func(v string) *string { return &v }
	box := func(x float64) *extraction.Region {
		return &extraction.Region{Page: 1, X0: x, Y0: 0.1, X1: x + 0.1, Y1: 0.2}
	}
	build := func() []extraction.FieldResult {
		var out []extraction.FieldResult
		for _, name := range extraction.HeaderFields {
			out = append(out, extraction.FieldResult{
				Field: extraction.Field{Name: name, Value: str("1.00"), Region: box(0.1), Reason: extraction.ReasonAmbiguous},
				Alternatives: []extraction.Field{
					{Name: name, Value: str("2.00"), Region: box(0.3), Reason: extraction.ReasonNone},
					{Name: name, Value: str("3.00"), Region: box(0.5), Reason: extraction.ReasonNone},
				},
			})
		}
		return out
	}
	// Separate builds share no pointer, so equality here is by value.
	if !ttResultsEqual(t, build(), build()) {
		t.Fatalf("two identical result sets built separately compare unequal")
	}

	edits := []struct {
		name string
		edit func(r *extraction.FieldResult)
	}{
		{"value", func(r *extraction.FieldResult) { r.Value = str("9.00") }},
		{"region", func(r *extraction.FieldResult) { r.Region = box(0.7) }},
		{"nil region", func(r *extraction.FieldResult) { r.Region = nil }},
		{"reason", func(r *extraction.FieldResult) { r.Reason = extraction.ReasonNone }},
		{"alternative value", func(r *extraction.FieldResult) { r.Alternatives[1].Value = str("9.00") }},
		{"alternative region", func(r *extraction.FieldResult) { r.Alternatives[1].Region = box(0.7) }},
		{"alternative reason", func(r *extraction.FieldResult) { r.Alternatives[1].Reason = extraction.ReasonAmbiguous }},
		{"alternatives order", func(r *extraction.FieldResult) {
			r.Alternatives[0], r.Alternatives[1] = r.Alternatives[1], r.Alternatives[0]
		}},
		{"alternative count", func(r *extraction.FieldResult) { r.Alternatives = r.Alternatives[:1] }},
	}
	last := len(extraction.HeaderFields) - 1
	for _, e := range edits {
		for _, onWith := range []bool{true, false} {
			with, without := build(), build()
			if onWith {
				e.edit(&with[last])
			} else {
				e.edit(&without[last])
			}
			if ttResultsEqual(t, with, without) {
				t.Errorf("a %s change (on the with side: %v) compares equal", e.name, onWith)
			}
		}
	}
}

// AC-1. Arithmetic finds the ruled table's printed total -- at the printed token's own box --
// under both readers, and only when Pages is present.
func TestEndToEnd_ArithmeticFindsTheRuledTablesPrintedTotalUnderBothReaders(t *testing.T) {
	readers := []struct {
		name   string
		pages  func(t *testing.T, layout string) []extraction.TokenPage
		wantY0 float64
	}{
		{"pdfium", eeTokenPages, 0.524702},
		{"docling", bdGoldenTokenPages, 0.524475},
	}
	layouts := []string{ttRuled, "wild_ruled_lines_totals_asprinted.pdf"}

	for _, r := range readers {
		for _, l := range layouts {
			t.Run(r.name+"/"+l, func(t *testing.T) {
				pages := r.pages(t, l)
				tokens := 0
				for _, p := range pages {
					tokens += len(p.Tokens)
				}
				if tokens == 0 {
					t.Fatalf("%s via %s read 0 token(s)", l, r.name)
				}

				var found extraction.Token
				count := 0
				for _, p := range pages {
					for _, tok := range p.Tokens {
						if tok.Text == "8,600.00" {
							found = tok
							count++
						}
					}
				}
				if count != 1 {
					t.Fatalf("%s via %s carries %d page token(s) reading \"8,600.00\", want exactly 1", l, r.name, count)
				}

				cands := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
				if got := dtDistinct(dtFor(cands, ttField)); !slices.Equal(got, []string{"1000.00"}) {
					t.Fatalf("%s via %s resolves total to %v, want exactly [\"1000.00\"]", l, r.name, got)
				}

				with := dtResult(t, extraction.Reconcile(extraction.Input{Candidates: cands, Pages: pages}), ttField)
				if dtValue(with) != "8600.00" || with.Reason != extraction.ReasonAmbiguous {
					t.Fatalf("%s via %s decides total = %q reason %q, want \"8600.00\" ambiguous", l, r.name, dtValue(with), with.Reason)
				}
				if got := dtAltValues(with.Alternatives); !slices.Equal(got, []string{"1000.00"}) {
					t.Errorf("%s via %s offers %v as alternatives, want [\"1000.00\"]", l, r.name, got)
				}
				for _, a := range with.Alternatives {
					if a.Reason != extraction.ReasonNone {
						t.Errorf("%s via %s alternative reads reason %q, want %q", l, r.name, a.Reason, extraction.ReasonNone)
					}
				}
				if with.Region == nil || *with.Region != found.Region {
					t.Fatalf("%s via %s region %v, want the printed token's own box %v", l, r.name, with.Region, found.Region)
				}
				if with.Region.Page != 1 {
					t.Errorf("%s via %s region page %d, want 1", l, r.name, with.Region.Page)
				}
				if diff := math.Abs(with.Region.Y0 - r.wantY0); diff > 1e-6 {
					t.Errorf("%s via %s region Y0 = %v, want %v within 1e-6 (diff %v)", l, r.name, with.Region.Y0, r.wantY0, diff)
				}

				without := dtResult(t, extraction.Reconcile(extraction.Input{Candidates: cands}), ttField)
				if dtValue(without) != "1000.00" || without.Reason != extraction.ReasonNone || len(without.Alternatives) != 0 {
					t.Errorf("%s via %s decides total = %q reason %q alts %v without Pages, want \"1000.00\" none, no alternatives", l, r.name, dtValue(without), without.Reason, dtAltValues(without.Alternatives))
				}
			})
		}
	}
}

// AC-2. Twice over every arrangement -- once through dtRun's own walk (pdfium for thirteen, the
// docling golden for the fourteenth, exactly how the worker will run it) and once through every
// layout's docling golden directly -- Pages moves exactly the ruled pair's header results and
// nothing else moves. The walk half is why this spec is RED before Stage 3: dtRun stays
// page-less until then.
func TestEndToEnd_FindTotalMovesExactlyTheRuledPair(t *testing.T) {
	type ttComparedRun struct {
		key           string
		with, without []extraction.FieldResult
	}
	var runs []ttComparedRun

	for _, l := range bdByLayout {
		cands, with, _ := dtRun(t, l)
		without := extraction.Reconcile(extraction.Input{Candidates: cands})
		runs = append(runs, ttComparedRun{"walk/" + l.file, with, without})
	}
	for _, l := range bdByLayout {
		pages := bdGoldenTokenPages(t, l.file)
		tokens := 0
		for _, p := range pages {
			tokens += len(p.Tokens)
		}
		if tokens == 0 {
			t.Fatalf("%s's docling golden read 0 token(s); every reason it reports is the reason of an empty page", l.file)
		}
		cands := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
		with := extraction.Reconcile(extraction.Input{Candidates: cands, Pages: pages})
		without := extraction.Reconcile(extraction.Input{Candidates: cands})
		runs = append(runs, ttComparedRun{"golden/" + l.file, with, without})
	}

	if want := 2 * len(bdByLayout); len(runs) != want {
		t.Fatalf("compared %d run(s), want %d", len(runs), want)
	}

	var differing []string
	for _, r := range runs {
		if !ttResultsEqual(t, r.with, r.without) {
			differing = append(differing, r.key)
		}
	}
	slices.Sort(differing)
	want := []string{
		"golden/wild_ruled_lines_totals.pdf",
		"golden/wild_ruled_lines_totals_asprinted.pdf",
		"walk/wild_ruled_lines_totals.pdf",
		"walk/wild_ruled_lines_totals_asprinted.pdf",
	}
	if !slices.Equal(differing, want) {
		t.Fatalf("Pages moves %v, want exactly %v", differing, want)
	}
}

// AC-3. A planted second token that also balances silences the ruled table entirely: the arm's
// result equals the page-less run. The unplanted control still finds.
func TestEndToEnd_APlantedSecondBalancingAmountSilencesTheRuledTable(t *testing.T) {
	pages := eeTokenPages(t, ttRuled)

	planted := slices.Clone(pages)
	for i := range planted {
		planted[i].Tokens = slices.Clone(planted[i].Tokens)
	}
	plantBox := extraction.Region{Page: 1, X0: 0.05, Y0: 0.95, X1: 0.12, Y1: 0.96}
	for i := range planted {
		if planted[i].Number == 1 {
			planted[i].Tokens = append(planted[i].Tokens, extraction.Token{Text: "8,600.00", Region: plantBox})
			break
		}
	}

	if plantBox.Page < 1 || plantBox.X0 < 0 || plantBox.X0 >= plantBox.X1 || plantBox.X1 > 1 ||
		plantBox.Y0 < 0 || plantBox.Y0 >= plantBox.Y1 || plantBox.Y1 > 1 {
		t.Fatalf("the plant box %+v fails usableBox's own bounds", plantBox)
	}
	for _, p := range pages {
		for _, tok := range p.Tokens {
			if tok.Region == plantBox {
				t.Fatalf("an existing token already carries the plant box %+v; the plant is not new evidence", plantBox)
			}
		}
	}

	before := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	after := extraction.Resolve(planted, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	if !slices.EqualFunc(before, after, ttCandidateEqual) {
		t.Fatalf("planting the box moves Resolve's own candidate set; the plant is not evidence-only")
	}

	bare := extraction.Reconcile(extraction.Input{Candidates: before})
	arm := extraction.Reconcile(extraction.Input{Candidates: after, Pages: planted})
	if !ttResultsEqual(t, arm, bare) {
		t.Fatalf("the plant does not silence the ruled table; the arm's result differs from the page-less run")
	}
	tot := dtResult(t, arm, ttField)
	if dtValue(tot) != "1000.00" || tot.Reason != extraction.ReasonNone || len(tot.Alternatives) != 0 {
		t.Errorf("with the plant, total decides %q reason %q alts %v, want \"1000.00\" none, no alternatives", dtValue(tot), tot.Reason, dtAltValues(tot.Alternatives))
	}

	control := dtResult(t, extraction.Reconcile(extraction.Input{Candidates: before, Pages: pages}), ttField)
	if dtValue(control) != "8600.00" || control.Reason != extraction.ReasonAmbiguous {
		t.Errorf("the unplanted control decides total = %q reason %q, want \"8600.00\" ambiguous", dtValue(control), control.Reason)
	}
}
