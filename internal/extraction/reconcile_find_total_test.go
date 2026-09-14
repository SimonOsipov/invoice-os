// reconcile_find_total_test.go: EXTR-29-01's arithmetic pass -- the one printed page amount that
// balances subtotal + vat becomes the total when every anchored reading fails the identity.
package extraction_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// F0's token boxes, one row apart down the page.
var (
	ftBoxA = extraction.Region{Page: 1, X0: 0.60, Y0: 0.70, X1: 0.90, Y1: 0.72}
	ftBoxB = extraction.Region{Page: 1, X0: 0.60, Y0: 0.74, X1: 0.90, Y1: 0.76}
	ftBoxC = extraction.Region{Page: 1, X0: 0.60, Y0: 0.78, X1: 0.90, Y1: 0.80}
	ftBoxD = extraction.Region{Page: 1, X0: 0.60, Y0: 0.82, X1: 0.90, Y1: 0.84}
	ftBoxE = extraction.Region{Page: 1, X0: 0.60, Y0: 0.86, X1: 0.90, Y1: 0.88}
	ftBoxF = extraction.Region{Page: 1, X0: 0.10, Y0: 0.70, X1: 0.40, Y1: 0.72}
	ftBoxG = extraction.Region{Page: 1, X0: 0.10, Y0: 0.74, X1: 0.40, Y1: 0.76}
	ftBoxH = extraction.Region{Page: 1, X0: 0.10, Y0: 0.78, X1: 0.40, Y1: 0.80}
)

// ftTok builds a positioned token, copying box so no fixture can alias another's.
func ftTok(text string, box extraction.Region) extraction.Token {
	return extraction.Token{Text: text, Region: box}
}

// ftSubtotal, ftVAT and ftTotalCand are F0's three anchored addends: 8,000.00 + 600.00, and an
// uncorroborated 1,000.00 total reading that does not balance the printed 8,600.00.
func ftSubtotal() extraction.Candidate {
	c := rcAdjacentAt("subtotal", "8000.00", extraction.TierGeneric, 0.03)
	c.Region = &ftBoxA
	return c
}

func ftVAT() extraction.Candidate {
	c := rcAdjacentAt("vat", "600.00", extraction.TierGeneric, 0.03)
	c.Region = &ftBoxB
	return c
}

func ftTotalCand() extraction.Candidate {
	c := rcAdjacentAt("total", "1000.00", extraction.TierGeneric, 0.047611)
	c.Region = &ftBoxC
	return c
}

func ftF0Candidates() []extraction.Candidate {
	return []extraction.Candidate{ftSubtotal(), ftVAT(), ftTotalCand()}
}

func ftF0Pages() []extraction.TokenPage {
	return []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
		ftTok("8,000.00", ftBoxA),
		ftTok("600.00", ftBoxB),
		ftTok("1,000.00", ftBoxC),
		ftTok("8,600.00", ftBoxD),
	}}}
}

// ftRun is Reconcile called directly on cands/lines/pages, skipping Input's other fields.
func ftRun(cands []extraction.Candidate, lines []extraction.DocLine, pages []extraction.TokenPage) []extraction.FieldResult {
	return extraction.Reconcile(extraction.Input{Candidates: cands, Lines: lines, Pages: pages})
}

// ftHeaderDiff names every HeaderFields cell where a and b disagree -- the "silent" oracle every
// fixture below that must not move a total's siblings runs through.
func ftHeaderDiff(t *testing.T, a, b []extraction.FieldResult) []string {
	t.Helper()
	n := len(extraction.HeaderFields)
	if n != 10 {
		t.Fatalf("HeaderFields names %d field(s), want 10; the diff below was measured over a different vocabulary", n)
	}
	if len(a) < n || len(b) < n {
		t.Fatalf("got %d and %d result(s), want at least %d header cells", len(a), len(b), n)
	}
	var diff []string
	for i := 0; i < n; i++ {
		if !reflect.DeepEqual(a[i], b[i]) {
			diff = append(diff, a[i].Name)
		}
	}
	return diff
}

// ftAssertControlFinds8600 proves the pass fires when every addend is genuinely decided -- the
// control every silent fixture below is measured against.
func ftAssertControlFinds8600(t *testing.T, cands []extraction.Candidate, lines []extraction.DocLine) {
	t.Helper()
	got := rtaFind(t, ftRun(cands, lines, ftF0Pages()), "total")
	if got.Value == nil || *got.Value != "8600.00" || got.Reason != extraction.ReasonAmbiguous {
		t.Fatalf("control total = %v/%q, want 8600.00/ReasonAmbiguous -- the silence above is otherwise a pass that never fires", got.Value, got.Reason)
	}
}

// AC-1. Every anchored total reading fails subtotal + vat, and exactly one page amount matches
// it to the kobo: that amount becomes the decided total, presented doubtful with the anchored
// reading as its alternative.
func TestReconcile_TheOneBalancingAmountBecomesTheTotal(t *testing.T) {
	cands, pages := ftF0Candidates(), ftF0Pages()

	withPages := rtaFind(t, ftRun(cands, nil, pages), "total")
	if withPages.Value == nil || *withPages.Value != "8600.00" {
		t.Fatalf("total = %v, want %q", withPages.Value, "8600.00")
	}
	if withPages.Region == nil || *withPages.Region != ftBoxD {
		t.Errorf("total.Region = %+v, want %+v", withPages.Region, ftBoxD)
	}
	if withPages.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total.Reason = %q, want ReasonAmbiguous", withPages.Reason)
	}
	if len(withPages.Alternatives) == 0 {
		t.Fatalf("alternatives empty, want the anchored 1000.00 reading")
	}
	wantAlts := []extraction.Field{{Name: "total", Value: rcStr("1000.00"), Region: &ftBoxC, Reason: extraction.ReasonNone}}
	if !reflect.DeepEqual(withPages.Alternatives, wantAlts) {
		t.Errorf("alternatives = %+v, want %+v", withPages.Alternatives, wantAlts)
	}

	// Without Pages, EXTR-23 left this decided at the anchored reading alone.
	withoutPages := rtaFind(t, ftRun(cands, nil, nil), "total")
	if withoutPages.Value == nil || *withoutPages.Value != "1000.00" {
		t.Errorf("without Pages total = %v, want %q", withoutPages.Value, "1000.00")
	}
	if withoutPages.Reason != extraction.ReasonNone {
		t.Errorf("without Pages total.Reason = %q, want ReasonNone", withoutPages.Reason)
	}
	if len(withoutPages.Alternatives) != 0 {
		t.Errorf("without Pages alternatives = %+v, want none", withoutPages.Alternatives)
	}
}

// AC-2. Two anchored total readings already compete; a found total keeps both as alternatives,
// in decideField's own order, each still ReasonNone.
func TestReconcile_AFoundTotalKeepsEveryAnchoredReadingAsAnAlternative(t *testing.T) {
	second := rcAdjacentAt("total", "3000.00", extraction.TierGeneric, 0.05)
	second.Region = &ftBoxE
	cands := append(ftF0Candidates(), second)

	pages := ftF0Pages()
	pages[0].Tokens = append(pages[0].Tokens, ftTok("3,000.00", ftBoxE))

	got := rtaFind(t, ftRun(cands, nil, pages), "total")
	if got.Value == nil || *got.Value != "8600.00" {
		t.Fatalf("total = %v, want %q", got.Value, "8600.00")
	}
	if got.Region == nil || *got.Region != ftBoxD {
		t.Errorf("total.Region = %+v, want %+v", got.Region, ftBoxD)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total.Reason = %q, want ReasonAmbiguous", got.Reason)
	}
	if len(got.Alternatives) == 0 {
		t.Fatalf("alternatives empty, want both anchored readings")
	}
	wantAlts := []extraction.Field{
		{Name: "total", Value: rcStr("1000.00"), Region: &ftBoxC, Reason: extraction.ReasonNone},
		{Name: "total", Value: rcStr("3000.00"), Region: &ftBoxE, Reason: extraction.ReasonNone},
	}
	if !reflect.DeepEqual(got.Alternatives, wantAlts) {
		t.Errorf("alternatives = %+v, want %+v", got.Alternatives, wantAlts)
	}
}

// AC-3. Two unrelated printed amounts each balance the identity; arithmetic that cannot
// discriminate between them finds nothing at all.
func TestReconcile_TwoUnrelatedBalancingAmountsFindNothing(t *testing.T) {
	cands := ftF0Candidates()
	pages := ftF0Pages()
	pages[0].Tokens = append(pages[0].Tokens, ftTok("8,600.00", ftBoxE))

	without, with := ftRun(cands, nil, nil), ftRun(cands, nil, pages)
	got := rtaFind(t, with, "total")
	if got.Value == nil || *got.Value != "1000.00" || got.Reason != extraction.ReasonNone {
		t.Errorf("total = %v/%q, want 1000.00/ReasonNone -- two balancing amounts must not be picked between", got.Value, got.Reason)
	}
	if diff := ftHeaderDiff(t, without, with); diff != nil {
		t.Errorf("ftHeaderDiff = %v, want none -- a second balancing amount must not move any header cell", diff)
	}
	ftAssertControlFinds8600(t, cands, nil)
}

// AC-3. The same grand total printed twice is the identical shape: two boxes agreeing on one
// value give arithmetic nothing to discriminate on.
func TestReconcile_AGrandTotalPrintedTwiceFindsNothing(t *testing.T) {
	cands := ftF0Candidates()
	pages := ftF0Pages()
	pages[0].Tokens = append(pages[0].Tokens, ftTok("8,600.00", ftBoxE))

	without, with := ftRun(cands, nil, nil), ftRun(cands, nil, pages)
	got := rtaFind(t, with, "total")
	if got.Value == nil || *got.Value != "1000.00" || got.Reason != extraction.ReasonNone {
		t.Errorf("total = %v/%q, want 1000.00/ReasonNone -- a repeated grand total must not be picked between", got.Value, got.Reason)
	}
	if diff := ftHeaderDiff(t, without, with); diff != nil {
		t.Errorf("ftHeaderDiff = %v, want none -- a repeated grand total must not move any header cell", diff)
	}
	ftAssertControlFinds8600(t, cands, nil)
}

// AC-3. Two candidate amounts a kobo apart both sit within tolerance of the identity; arithmetic
// still cannot discriminate between them.
func TestReconcile_TwoAmountsInsideOneKoboFindNothing(t *testing.T) {
	cands := ftF0Candidates()
	pages := ftF0Pages()
	pages[0].Tokens = append(pages[0].Tokens, ftTok("8,600.01", ftBoxE))

	without, with := ftRun(cands, nil, nil), ftRun(cands, nil, pages)
	got := rtaFind(t, with, "total")
	if got.Value == nil || *got.Value != "1000.00" || got.Reason != extraction.ReasonNone {
		t.Errorf("total = %v/%q, want 1000.00/ReasonNone -- two readings within tolerance of each other must not be picked between", got.Value, got.Reason)
	}
	if diff := ftHeaderDiff(t, without, with); diff != nil {
		t.Errorf("ftHeaderDiff = %v, want none -- two readings a kobo apart must not move any header cell", diff)
	}
	ftAssertControlFinds8600(t, cands, nil)
}

// AC-3. Matching is sign-exact: a credit note's own negative grand total balances on its
// negative subtotal + vat, and an unsigned reading of the same magnitude is not a match.
func TestReconcile_ACreditNoteTotalIsFoundOnItsSign(t *testing.T) {
	sub := rcAdjacentAt("subtotal", "-8000.00", extraction.TierGeneric, 0.03)
	sub.Region = &ftBoxA
	vat := rcAdjacentAt("vat", "-600.00", extraction.TierGeneric, 0.03)
	vat.Region = &ftBoxB
	total := rcAdjacentAt("total", "-1000.00", extraction.TierGeneric, 0.047611)
	total.Region = &ftBoxC
	cands := []extraction.Candidate{sub, vat, total}

	basePages := func(extra ...extraction.Token) []extraction.TokenPage {
		toks := []extraction.Token{
			ftTok("-8,000.00", ftBoxA),
			ftTok("-600.00", ftBoxB),
			ftTok("-1,000.00", ftBoxC),
			ftTok("-2,000.00", ftBoxF),
			ftTok("-5,000.00", ftBoxG),
			ftTok("-9,200.00", ftBoxH),
		}
		return []extraction.TokenPage{{Number: 1, Tokens: append(toks, extra...)}}
	}

	// Floor: the distractors really do straddle the target from both sides, and the positive
	// control below carries the same magnitude, so a sign slip could pass unnoticed.
	want, tol := rtDec(t, "-8000.00").Add(rtDec(t, "-600.00")), rtDec(t, "0.01")
	if !want.Equal(rtDec(t, "-8600.00")) {
		t.Fatalf("subtotal + vat = %s, want -8600.00", want)
	}
	for _, v := range []string{"-1000.00", "-2000.00", "-5000.00", "-9200.00"} {
		if !want.Sub(rtDec(t, v)).Abs().GreaterThan(tol) {
			t.Fatalf("%s is not more than %s from %s; the fixture claims it is a distractor", v, tol, want)
		}
	}
	if !rtDec(t, "8600.00").Equal(want.Abs()) {
		t.Fatalf("the positive control's magnitude does not match |%s|; the sign test below proves nothing", want)
	}

	// (a) the credit note's own negative grand total is found.
	found := rtaFind(t, ftRun(cands, nil, basePages(ftTok("-8,600.00", ftBoxD))), "total")
	if found.Value == nil || *found.Value != "-8600.00" {
		t.Fatalf("total = %v, want %q", found.Value, "-8600.00")
	}
	if found.Region == nil || *found.Region != ftBoxD {
		t.Errorf("total.Region = %+v, want %+v", found.Region, ftBoxD)
	}
	if found.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total.Reason = %q, want ReasonAmbiguous", found.Reason)
	}
	wantAlts := []extraction.Field{{Name: "total", Value: rcStr("-1000.00"), Region: &ftBoxC, Reason: extraction.ReasonNone}}
	if len(found.Alternatives) == 0 {
		t.Fatalf("alternatives empty, want the anchored -1000.00 reading")
	}
	if !reflect.DeepEqual(found.Alternatives, wantAlts) {
		t.Errorf("alternatives = %+v, want %+v", found.Alternatives, wantAlts)
	}

	// (b) the same magnitude printed UNSIGNED is not a match.
	unsignedWithout, unsignedWith := ftRun(cands, nil, nil), ftRun(cands, nil, basePages(ftTok("8,600.00", ftBoxD)))
	unsigned := rtaFind(t, unsignedWith, "total")
	if unsigned.Value == nil || *unsigned.Value != "-1000.00" || unsigned.Reason != extraction.ReasonNone {
		t.Errorf("total = %v/%q, want -1000.00/ReasonNone -- an unsigned reading of the same magnitude must not match", unsigned.Value, unsigned.Reason)
	}
	if diff := ftHeaderDiff(t, unsignedWithout, unsignedWith); diff != nil {
		t.Errorf("ftHeaderDiff = %v, want none", diff)
	}

	// (c) a genuine match plus an unsigned distractor of the same magnitude still finds the
	// signed one alone.
	both := rtaFind(t, ftRun(cands, nil, basePages(ftTok("-8,600.00", ftBoxD), ftTok("8,600.00", ftBoxE))), "total")
	if both.Value == nil || *both.Value != "-8600.00" {
		t.Fatalf("total = %v, want %q", both.Value, "-8600.00")
	}
	if len(both.Alternatives) == 0 {
		t.Fatalf("alternatives empty, want the anchored -1000.00 reading")
	}
	if !reflect.DeepEqual(both.Alternatives, wantAlts) {
		t.Errorf("alternatives = %+v, want %+v", both.Alternatives, wantAlts)
	}
}

// AC-4. An addend that is not itself DECIDED holds the pass off entirely: missing, ambiguous,
// line-sum-condemned or unparseable, each case names its own rejecting clause.
func TestReconcile_AnUndecidedAddendFindsNothing(t *testing.T) {
	ambigNear := rcAdjacentAt("vat", "600.00", extraction.TierGeneric, 0.03)
	ambigNear.Region = &ftBoxB
	ambigFar := rcAdjacentAt("vat", "650.00", extraction.TierGeneric, 0.05)
	ambigFar.Region = &ftBoxE

	badSubtotal := rcAdjacentAt("subtotal", "eight thousand", extraction.TierGeneric, 0.03)
	badSubtotal.Region = &ftBoxA

	cases := []struct {
		name       string
		cands      []extraction.Candidate
		lines      []extraction.DocLine
		addend     string
		wantReason extraction.Reason
	}{
		{name: "no subtotal candidate", cands: []extraction.Candidate{ftVAT(), ftTotalCand()}, addend: "subtotal", wantReason: extraction.ReasonMissing},
		{name: "no vat candidate", cands: []extraction.Candidate{ftSubtotal(), ftTotalCand()}, addend: "vat", wantReason: extraction.ReasonMissing},
		{name: "an ambiguous vat", cands: []extraction.Candidate{ftSubtotal(), ambigNear, ambigFar, ftTotalCand()}, addend: "vat", wantReason: extraction.ReasonAmbiguous},
		{
			name:       "a subtotal the line sum condemned",
			cands:      ftF0Candidates(),
			lines:      []extraction.DocLine{{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("7000.00"), LineTotal: rcStr("7000.00")}},
			addend:     "subtotal",
			wantReason: extraction.ReasonInconsistent,
		},
		{name: "an unparseable subtotal", cands: []extraction.Candidate{badSubtotal, ftVAT(), ftTotalCand()}, addend: "subtotal", wantReason: extraction.ReasonNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			without := ftRun(tc.cands, tc.lines, nil)
			with := ftRun(tc.cands, tc.lines, ftF0Pages())

			// Floor: the addend really did reach the state this case names -- otherwise the
			// silence below is held off by some other clause entirely.
			addend := rtaFind(t, with, tc.addend)
			if addend.Reason != tc.wantReason {
				t.Fatalf("%s reads %q, want %q -- this case never reached the state it names", tc.addend, addend.Reason, tc.wantReason)
			}

			got := rtaFind(t, with, "total")
			if got.Value == nil || *got.Value != "1000.00" || got.Reason != extraction.ReasonNone {
				t.Errorf("total = %v/%q, want 1000.00/ReasonNone -- an undecided addend must hold the pass off", got.Value, got.Reason)
			}
			if diff := ftHeaderDiff(t, without, with); diff != nil {
				t.Errorf("ftHeaderDiff = %v, want none", diff)
			}
		})
	}

	// The instrument: the same pages with every addend actually decided do find the total.
	ftAssertControlFinds8600(t, ftF0Candidates(), nil)
}

// AC-5. Where any anchored total candidate satisfies the identity -- including one outside
// decideField's own competing group -- the result is exactly what EXTR-23 already produced.
func TestReconcile_ACorroboratedAnchoredTotalIsNeverReDecided(t *testing.T) {
	t.Run("the referee already corroborated the winner", func(t *testing.T) {
		far := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.05)
		far.Region = &ftBoxD
		cands := append(ftF0Candidates(), far)

		without, with := ftRun(cands, nil, nil), ftRun(cands, nil, ftF0Pages())
		got := rtaFind(t, with, "total")
		if got.Value == nil || *got.Value != "8600.00" || got.Reason != extraction.ReasonNone {
			t.Errorf("total = %v/%q, want 8600.00/ReasonNone", got.Value, got.Reason)
		}
		if len(got.Alternatives) != 0 {
			t.Errorf("alternatives = %+v, want none -- the referee already settled this", got.Alternatives)
		}
		if diff := ftHeaderDiff(t, without, with); diff != nil {
			t.Errorf("ftHeaderDiff = %v, want none", diff)
		}
	})

	t.Run("a satisfying candidate outside decideField's own competing group", func(t *testing.T) {
		head := rcCandAt("total", "1000.00", extraction.TierGeneric, 0)
		head.Region = &ftBoxC
		outside := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.05)
		outside.Region = &ftBoxD
		cands := []extraction.Candidate{ftSubtotal(), ftVAT(), head, outside}

		// Floor: decideField really did leave the outside candidate ungrouped, or this arm
		// tests the same shape as the one above.
		decided := rtaFind(t, ftRun(cands, nil, nil), "total")
		if decided.Value == nil || *decided.Value != "1000.00" || decided.Reason != extraction.ReasonNone || len(decided.Alternatives) != 0 {
			t.Fatalf("without Pages total = %v/%q/%+v, want 1000.00/ReasonNone/none -- the outside candidate must not already be grouped in", decided.Value, decided.Reason, decided.Alternatives)
		}

		without, with := ftRun(cands, nil, nil), ftRun(cands, nil, ftF0Pages())
		got := rtaFind(t, with, "total")
		if got.Value == nil || *got.Value != "1000.00" || got.Reason != extraction.ReasonNone {
			t.Errorf("total = %v/%q, want 1000.00/ReasonNone", got.Value, got.Reason)
		}
		if len(got.Alternatives) != 0 {
			t.Errorf("alternatives = %+v, want none", got.Alternatives)
		}
		if diff := ftHeaderDiff(t, without, with); diff != nil {
			t.Errorf("ftHeaderDiff = %v, want none", diff)
		}
	})

	ftAssertControlFinds8600(t, ftF0Candidates(), nil)
}

// AC-6. With no anchored total candidate at all, the field stays ReasonMissing -- arithmetic
// never manufactures a value decideField itself did not reach.
func TestReconcile_AMissingTotalIsNotFilledByArithmetic(t *testing.T) {
	cands := []extraction.Candidate{ftSubtotal(), ftVAT()}
	without, with := ftRun(cands, nil, nil), ftRun(cands, nil, ftF0Pages())

	got := rtaFind(t, with, "total")
	if got.Value != nil {
		t.Errorf("total value = %v, want nil", got.Value)
	}
	if got.Reason != extraction.ReasonMissing {
		t.Errorf("total.Reason = %q, want ReasonMissing", got.Reason)
	}
	if got.Alternatives == nil || len(got.Alternatives) != 0 {
		t.Errorf("alternatives = %v, want a non-nil empty slice", got.Alternatives)
	}
	if diff := ftHeaderDiff(t, without, with); diff != nil {
		t.Errorf("ftHeaderDiff = %v, want none", diff)
	}
	ftAssertControlFinds8600(t, ftF0Candidates(), nil)
}

// AC-7. findTotal reads every page, not only the first; and when the same balancing amount is
// also readable on another page, that too holds the pass off.
func TestReconcile_FindTotalReadsEveryPage(t *testing.T) {
	cands := ftF0Candidates()
	page1 := []extraction.Token{ftTok("8,000.00", ftBoxA), ftTok("600.00", ftBoxB), ftTok("1,000.00", ftBoxC)}
	boxD2 := extraction.Region{Page: 2, X0: ftBoxD.X0, Y0: ftBoxD.Y0, X1: ftBoxD.X1, Y1: ftBoxD.Y1}

	// (a) the balancing amount sits on page 2 alone.
	onlyPage2 := []extraction.TokenPage{
		{Number: 1, Tokens: page1},
		{Number: 2, Tokens: []extraction.Token{ftTok("8,600.00", boxD2)}},
	}
	got := rtaFind(t, ftRun(cands, nil, onlyPage2), "total")
	if got.Value == nil || *got.Value != "8600.00" {
		t.Fatalf("total = %v, want %q -- a balancing amount on a later page must still be found", got.Value, "8600.00")
	}
	if got.Region == nil || got.Region.Page != 2 {
		t.Errorf("total.Region.Page = %v, want 2", got.Region)
	}

	// (b) the same document with the grand total ALSO printed on page 1: two matches, silent.
	page1WithDup := append(append([]extraction.Token{}, page1...), ftTok("8,600.00", ftBoxE))
	bothPages := []extraction.TokenPage{
		{Number: 1, Tokens: page1WithDup},
		{Number: 2, Tokens: []extraction.Token{ftTok("8,600.00", boxD2)}},
	}
	without, with := ftRun(cands, nil, nil), ftRun(cands, nil, bothPages)
	silent := rtaFind(t, with, "total")
	if silent.Value == nil || *silent.Value != "1000.00" || silent.Reason != extraction.ReasonNone {
		t.Errorf("total = %v/%q, want 1000.00/ReasonNone -- a balancing amount readable on two pages must not be picked between", silent.Value, silent.Reason)
	}
	if diff := ftHeaderDiff(t, without, with); diff != nil {
		t.Errorf("ftHeaderDiff = %v, want none", diff)
	}
}

// AC-7. A token whose Region carries no usable box is not part of the population findTotal
// draws its evidence from.
func TestReconcile_ATokenWithNoUsableBoxIsNotEvidence(t *testing.T) {
	cands := ftF0Candidates()
	pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
		ftTok("8,000.00", ftBoxA),
		ftTok("600.00", ftBoxB),
		ftTok("1,000.00", ftBoxC),
		{Text: "8,600.00", Region: extraction.Region{}},
	}}}

	without, with := ftRun(cands, nil, nil), ftRun(cands, nil, pages)
	got := rtaFind(t, with, "total")
	if got.Value == nil || *got.Value != "1000.00" || got.Reason != extraction.ReasonNone {
		t.Errorf("total = %v/%q, want 1000.00/ReasonNone -- a token with no usable box must not corroborate", got.Value, got.Reason)
	}
	if diff := ftHeaderDiff(t, without, with); diff != nil {
		t.Errorf("ftHeaderDiff = %v, want none", diff)
	}
	ftAssertControlFinds8600(t, cands, nil)
}

// AC-8. Every fixture above where the pass could not help leaves reconcile.go's own header
// results byte-identical to a run with no Pages at all; the instrument proves ftHeaderDiff can
// see a real change, so the all-clear above is not blind.
func TestReconcile_ADocumentFindTotalCannotHelpIsLeftExactlyAsEXTR23LeftIt(t *testing.T) {
	unrelatedOrTwice := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		pages := ftF0Pages()
		pages[0].Tokens = append(pages[0].Tokens, ftTok("8,600.00", ftBoxE))
		return ftF0Candidates(), nil, pages
	}
	kobo := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		pages := ftF0Pages()
		pages[0].Tokens = append(pages[0].Tokens, ftTok("8,600.01", ftBoxE))
		return ftF0Candidates(), nil, pages
	}
	creditUnsigned := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		sub := rcAdjacentAt("subtotal", "-8000.00", extraction.TierGeneric, 0.03)
		sub.Region = &ftBoxA
		vat := rcAdjacentAt("vat", "-600.00", extraction.TierGeneric, 0.03)
		vat.Region = &ftBoxB
		total := rcAdjacentAt("total", "-1000.00", extraction.TierGeneric, 0.047611)
		total.Region = &ftBoxC
		pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
			ftTok("-8,000.00", ftBoxA), ftTok("-600.00", ftBoxB), ftTok("-1,000.00", ftBoxC),
			ftTok("8,600.00", ftBoxD), // unsigned: not a match for -8600.00
		}}}
		return []extraction.Candidate{sub, vat, total}, nil, pages
	}
	noSubtotal := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		return []extraction.Candidate{ftVAT(), ftTotalCand()}, nil, ftF0Pages()
	}
	noVAT := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		return []extraction.Candidate{ftSubtotal(), ftTotalCand()}, nil, ftF0Pages()
	}
	ambiguousVAT := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		near := rcAdjacentAt("vat", "600.00", extraction.TierGeneric, 0.03)
		near.Region = &ftBoxB
		far := rcAdjacentAt("vat", "650.00", extraction.TierGeneric, 0.05)
		far.Region = &ftBoxE
		return []extraction.Candidate{ftSubtotal(), near, far, ftTotalCand()}, nil, ftF0Pages()
	}
	lineSumCondemned := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		lines := []extraction.DocLine{{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("7000.00"), LineTotal: rcStr("7000.00")}}
		return ftF0Candidates(), lines, ftF0Pages()
	}
	unparseableSubtotal := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		bad := rcAdjacentAt("subtotal", "eight thousand", extraction.TierGeneric, 0.03)
		bad.Region = &ftBoxA
		return []extraction.Candidate{bad, ftVAT(), ftTotalCand()}, nil, ftF0Pages()
	}
	alreadyCorroborated := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		far := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.05)
		far.Region = &ftBoxD
		return append(ftF0Candidates(), far), nil, ftF0Pages()
	}
	outsideGroup := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		head := rcCandAt("total", "1000.00", extraction.TierGeneric, 0)
		head.Region = &ftBoxC
		outside := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.05)
		outside.Region = &ftBoxD
		return []extraction.Candidate{ftSubtotal(), ftVAT(), head, outside}, nil, ftF0Pages()
	}
	missingTotal := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		return []extraction.Candidate{ftSubtotal(), ftVAT()}, nil, ftF0Pages()
	}
	duplicateAcrossPages := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		page1 := []extraction.Token{ftTok("8,000.00", ftBoxA), ftTok("600.00", ftBoxB), ftTok("1,000.00", ftBoxC), ftTok("8,600.00", ftBoxE)}
		boxD2 := extraction.Region{Page: 2, X0: ftBoxD.X0, Y0: ftBoxD.Y0, X1: ftBoxD.X1, Y1: ftBoxD.Y1}
		pages := []extraction.TokenPage{{Number: 1, Tokens: page1}, {Number: 2, Tokens: []extraction.Token{ftTok("8,600.00", boxD2)}}}
		return ftF0Candidates(), nil, pages
	}
	noUsableBox := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
			ftTok("8,000.00", ftBoxA), ftTok("600.00", ftBoxB), ftTok("1,000.00", ftBoxC),
			{Text: "8,600.00", Region: extraction.Region{}},
		}}}
		return ftF0Candidates(), nil, pages
	}
	whtArm1 := func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage) {
		return ftWHTCandidates(), nil, ftWHTPages()
	}

	cases := []struct {
		name  string
		build func() ([]extraction.Candidate, []extraction.DocLine, []extraction.TokenPage)
	}{
		{"two unrelated balancing amounts", unrelatedOrTwice},
		{"a grand total printed twice", unrelatedOrTwice},
		{"two amounts inside one kobo", kobo},
		{"a credit note total against an unsigned distractor", creditUnsigned},
		{"no subtotal candidate", noSubtotal},
		{"no vat candidate", noVAT},
		{"an ambiguous vat", ambiguousVAT},
		{"a subtotal the line sum condemned", lineSumCondemned},
		{"an unparseable subtotal", unparseableSubtotal},
		{"an anchored total already corroborated", alreadyCorroborated},
		{"a satisfying candidate outside the competing group", outsideGroup},
		{"a missing total", missingTotal},
		{"a grand total printed across two pages", duplicateAcrossPages},
		{"a token with no usable box", noUsableBox},
		{"a withholding net payable with no printed gross", whtArm1},
	}
	if len(cases) != 15 {
		t.Fatalf("%d case(s) registered, want 15 -- the aggregation below covers fewer fixtures than the acceptance criterion names", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cands, lines, pages := tc.build()
			if diff := ftHeaderDiff(t, ftRun(cands, lines, nil), ftRun(cands, lines, pages)); diff != nil {
				t.Errorf("ftHeaderDiff = %v, want none -- findTotal must leave every header cell exactly as EXTR-23 left it", diff)
			}
		})
	}

	// The instrument: plain F0 DOES move the total cell, so the all-clear above is not blind.
	diff := ftHeaderDiff(t, ftRun(ftF0Candidates(), nil, nil), ftRun(ftF0Candidates(), nil, ftF0Pages()))
	if len(diff) == 0 {
		t.Fatalf("ftHeaderDiff(F0) is empty; the all-clear above proves nothing if the oracle cannot see a real change")
	}
	if want := []string{"total"}; !slices.Equal(diff, want) {
		t.Errorf("ftHeaderDiff(F0) = %v, want %v", diff, want)
	}
}

// whtBox* are the withholding-tax fixture's own boxes: subtotal, vat, the withholding label and
// amount, the anchored net payable, and where a printed gross would sit.
var (
	whtBoxSub   = extraction.Region{Page: 1, X0: 0.10, Y0: 0.60, X1: 0.30, Y1: 0.62}
	whtBoxVAT   = extraction.Region{Page: 1, X0: 0.10, Y0: 0.65, X1: 0.30, Y1: 0.67}
	whtBoxLabel = extraction.Region{Page: 1, X0: 0.10, Y0: 0.70, X1: 0.40, Y1: 0.72}
	whtBoxAmt   = extraction.Region{Page: 1, X0: 0.10, Y0: 0.75, X1: 0.30, Y1: 0.77}
	whtBoxNet   = extraction.Region{Page: 1, X0: 0.10, Y0: 0.80, X1: 0.30, Y1: 0.82}
	whtBoxGross = extraction.Region{Page: 1, X0: 0.10, Y0: 0.85, X1: 0.30, Y1: 0.87}
)

// ftWHTCandidates is the withholding invoice's anchored addends and its net payable, which does
// not itself balance subtotal + vat (1000.00 + 75.00 = 1075.00, not 1025.00).
func ftWHTCandidates() []extraction.Candidate {
	sub := rcAdjacentAt("subtotal", "1000.00", extraction.TierGeneric, 0.03)
	sub.Region = &whtBoxSub
	vat := rcAdjacentAt("vat", "75.00", extraction.TierGeneric, 0.03)
	vat.Region = &whtBoxVAT
	total := rcAdjacentAt("total", "1025.00", extraction.TierGeneric, 0.047611)
	total.Region = &whtBoxNet
	return []extraction.Candidate{sub, vat, total}
}

// ftWHTPages is arm 1: the gross (1,075.00) is nowhere printed.
func ftWHTPages() []extraction.TokenPage {
	return []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
		ftTok("1,000.00", whtBoxSub),
		ftTok("75.00", whtBoxVAT),
		ftTok("Withholding tax", whtBoxLabel),
		ftTok("50.00", whtBoxAmt),
		ftTok("1,025.00", whtBoxNet),
	}}}
}

// EXTR-29-02 AC-1..3. The decided subtotal and vat cells' own tokens are evidence, never a total
// candidate: without the exclusion, a zero addend makes the OTHER addend's own token equal
// subtotal + vat, and the pass would file that evidence as its own conclusion.
func TestReconcile_AnAddendsOwnTokenIsNeverItsTotal(t *testing.T) {
	// assertAddendsDecided is the non-vacuity floor every arm below shares: the silence (or find)
	// must come from the exclusion, never from an undecided addend or the learned gate.
	assertAddendsDecided := func(t *testing.T, cands []extraction.Candidate, with []extraction.FieldResult, subtotal, vat string) {
		t.Helper()
		if slices.ContainsFunc(cands, func(c extraction.Candidate) bool { return c.Tier == extraction.TierLearned }) {
			t.Fatal("a TierLearned candidate is present -- this arm would test the learned gate, not the exclusion")
		}
		got := rtaFind(t, with, "subtotal")
		if got.Value == nil || *got.Value != subtotal || got.Reason != extraction.ReasonNone {
			t.Fatalf("subtotal = %v/%q, want %s/ReasonNone -- this arm never reached a decided addend", got.Value, got.Reason, subtotal)
		}
		got = rtaFind(t, with, "vat")
		if got.Value == nil || *got.Value != vat || got.Reason != extraction.ReasonNone {
			t.Fatalf("vat = %v/%q, want %s/ReasonNone -- this arm never reached a decided addend", got.Value, got.Reason, vat)
		}
	}

	t.Run("vat 0.00: the subtotal token is not the total", func(t *testing.T) {
		sub := rcAdjacentAt("subtotal", "1000.00", extraction.TierGeneric, 0.03)
		sub.Region = &ftBoxA
		vat := rcAdjacentAt("vat", "0.00", extraction.TierGeneric, 0.03)
		vat.Region = &ftBoxB
		total := rcAdjacentAt("total", "950.00", extraction.TierGeneric, 0.047611)
		total.Region = &ftBoxC
		cands := []extraction.Candidate{sub, vat, total}
		pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
			ftTok("1,000.00", ftBoxA), ftTok("0.00", ftBoxB), ftTok("950.00", ftBoxC),
		}}}

		without, with := ftRun(cands, nil, nil), ftRun(cands, nil, pages)
		assertAddendsDecided(t, cands, with, "1000.00", "0.00")

		got := rtaFind(t, with, "total")
		if got.Value == nil || *got.Value != "950.00" || got.Reason != extraction.ReasonNone {
			t.Errorf("total = %v/%q, want 950.00/ReasonNone -- the subtotal's own token must not become its total", got.Value, got.Reason)
		}
		if got.Region == nil || *got.Region != ftBoxC {
			t.Errorf("total.Region = %+v, want %+v", got.Region, ftBoxC)
		}
		if got.Alternatives == nil || len(got.Alternatives) != 0 {
			t.Errorf("alternatives = %v, want a non-nil empty slice", got.Alternatives)
		}
		if diff := ftHeaderDiff(t, without, with); diff != nil {
			t.Errorf("ftHeaderDiff = %v, want none -- the subtotal's own token must not move any header cell", diff)
		}
	})

	// Fire control for the arm above: the same page, plus a genuine printed grand total. That
	// token alone is found, proving the silence above is the exclusion's, not a dead pass.
	t.Run("vat 0.00 with a printed grand total: that token is found", func(t *testing.T) {
		sub := rcAdjacentAt("subtotal", "1000.00", extraction.TierGeneric, 0.03)
		sub.Region = &ftBoxA
		vat := rcAdjacentAt("vat", "0.00", extraction.TierGeneric, 0.03)
		vat.Region = &ftBoxB
		total := rcAdjacentAt("total", "950.00", extraction.TierGeneric, 0.047611)
		total.Region = &ftBoxC
		cands := []extraction.Candidate{sub, vat, total}
		pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
			ftTok("1,000.00", ftBoxA), ftTok("0.00", ftBoxB), ftTok("950.00", ftBoxC), ftTok("1,000.00", ftBoxG),
		}}}

		with := ftRun(cands, nil, pages)
		assertAddendsDecided(t, cands, with, "1000.00", "0.00")

		got := rtaFind(t, with, "total")
		if got.Value == nil || *got.Value != "1000.00" {
			t.Fatalf("total = %v, want %q", got.Value, "1000.00")
		}
		if got.Region == nil || *got.Region != ftBoxG {
			t.Errorf("total.Region = %+v, want %+v", got.Region, ftBoxG)
		}
		if got.Reason != extraction.ReasonAmbiguous {
			t.Errorf("total.Reason = %q, want ReasonAmbiguous", got.Reason)
		}
		wantAlts := []extraction.Field{{Name: "total", Value: rcStr("950.00"), Region: &ftBoxC, Reason: extraction.ReasonNone}}
		if !reflect.DeepEqual(got.Alternatives, wantAlts) {
			t.Errorf("alternatives = %+v, want %+v", got.Alternatives, wantAlts)
		}
	})

	t.Run("subtotal 0.00: the vat token is not the total", func(t *testing.T) {
		sub := rcAdjacentAt("subtotal", "0.00", extraction.TierGeneric, 0.03)
		sub.Region = &ftBoxA
		vat := rcAdjacentAt("vat", "75.00", extraction.TierGeneric, 0.03)
		vat.Region = &ftBoxB
		total := rcAdjacentAt("total", "70.00", extraction.TierGeneric, 0.047611)
		total.Region = &ftBoxC
		cands := []extraction.Candidate{sub, vat, total}
		pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
			ftTok("0.00", ftBoxA), ftTok("75.00", ftBoxB), ftTok("70.00", ftBoxC),
		}}}

		without, with := ftRun(cands, nil, nil), ftRun(cands, nil, pages)
		assertAddendsDecided(t, cands, with, "0.00", "75.00")

		got := rtaFind(t, with, "total")
		if got.Value == nil || *got.Value != "70.00" || got.Reason != extraction.ReasonNone {
			t.Errorf("total = %v/%q, want 70.00/ReasonNone -- the vat's own token must not become its total", got.Value, got.Reason)
		}
		if got.Alternatives == nil || len(got.Alternatives) != 0 {
			t.Errorf("alternatives = %v, want a non-nil empty slice", got.Alternatives)
		}
		if diff := ftHeaderDiff(t, without, with); diff != nil {
			t.Errorf("ftHeaderDiff = %v, want none -- the vat's own token must not move any header cell", diff)
		}
	})

	// Fire control for the arm above.
	t.Run("subtotal 0.00 with a printed grand total: that token is found", func(t *testing.T) {
		sub := rcAdjacentAt("subtotal", "0.00", extraction.TierGeneric, 0.03)
		sub.Region = &ftBoxA
		vat := rcAdjacentAt("vat", "75.00", extraction.TierGeneric, 0.03)
		vat.Region = &ftBoxB
		total := rcAdjacentAt("total", "70.00", extraction.TierGeneric, 0.047611)
		total.Region = &ftBoxC
		cands := []extraction.Candidate{sub, vat, total}
		pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
			ftTok("0.00", ftBoxA), ftTok("75.00", ftBoxB), ftTok("70.00", ftBoxC), ftTok("75.00", ftBoxH),
		}}}

		with := ftRun(cands, nil, pages)
		assertAddendsDecided(t, cands, with, "0.00", "75.00")

		got := rtaFind(t, with, "total")
		if got.Value == nil || *got.Value != "75.00" {
			t.Fatalf("total = %v, want %q", got.Value, "75.00")
		}
		if got.Region == nil || *got.Region != ftBoxH {
			t.Errorf("total.Region = %+v, want %+v", got.Region, ftBoxH)
		}
		if got.Reason != extraction.ReasonAmbiguous {
			t.Errorf("total.Reason = %q, want ReasonAmbiguous", got.Reason)
		}
		wantAlts := []extraction.Field{{Name: "total", Value: rcStr("70.00"), Region: &ftBoxC, Reason: extraction.ReasonNone}}
		if !reflect.DeepEqual(got.Alternatives, wantAlts) {
			t.Errorf("alternatives = %+v, want %+v", got.Alternatives, wantAlts)
		}
	})
}

// EXTR-29-02 AC-4. A TierLearned total candidate is the tenant's own taught answer; arithmetic
// must never override it, even when a unique balancing token sits on the page.
func TestReconcile_ALearnedTotalIsNeverOverriddenByArithmetic(t *testing.T) {
	if slices.ContainsFunc(ftF0Candidates(), func(c extraction.Candidate) bool { return c.Tier == extraction.TierLearned }) {
		t.Fatal("ftF0Candidates carries a TierLearned candidate -- EXTR-29-01's fixtures must stay explicit TierGeneric")
	}
	if slices.ContainsFunc(ftWHTCandidates(), func(c extraction.Candidate) bool { return c.Tier == extraction.TierLearned }) {
		t.Fatal("ftWHTCandidates carries a TierLearned candidate -- EXTR-29-01's fixtures must stay explicit TierGeneric")
	}

	t.Run("a learned total stands", func(t *testing.T) {
		learned := rcCandAt("total", "1000.00", extraction.TierLearned, 0)
		learned.Region = &ftBoxC
		cands := []extraction.Candidate{ftSubtotal(), ftVAT(), learned}

		without, with := ftRun(cands, nil, nil), ftRun(cands, nil, ftF0Pages())
		got := rtaFind(t, with, "total")
		if got.Value == nil || *got.Value != "1000.00" || got.Reason != extraction.ReasonNone {
			t.Errorf("total = %v/%q, want 1000.00/ReasonNone -- a taught total must never be overridden by arithmetic", got.Value, got.Reason)
		}
		if got.Region == nil || *got.Region != ftBoxC {
			t.Errorf("total.Region = %+v, want %+v", got.Region, ftBoxC)
		}
		if got.Alternatives == nil || len(got.Alternatives) != 0 {
			t.Errorf("alternatives = %v, want a non-nil empty slice", got.Alternatives)
		}
		if diff := ftHeaderDiff(t, without, with); diff != nil {
			t.Errorf("ftHeaderDiff = %v, want none -- a unique balancing token must not move a taught total", diff)
		}
	})

	// Control: the same fixture, untaught -- proves the arm above is held by the learned gate,
	// not by some other silence.
	t.Run("control: the same total untaught is found", func(t *testing.T) {
		generic := rcCandAt("total", "1000.00", extraction.TierGeneric, 0)
		generic.Region = &ftBoxC
		cands := []extraction.Candidate{ftSubtotal(), ftVAT(), generic}

		got := rtaFind(t, ftRun(cands, nil, ftF0Pages()), "total")
		if got.Value == nil || *got.Value != "8600.00" {
			t.Fatalf("total = %v, want %q -- the untaught control must still be found", got.Value, "8600.00")
		}
		if got.Region == nil || *got.Region != ftBoxD {
			t.Errorf("total.Region = %+v, want %+v", got.Region, ftBoxD)
		}
		if got.Reason != extraction.ReasonAmbiguous {
			t.Errorf("total.Reason = %q, want ReasonAmbiguous", got.Reason)
		}
		wantAlts := []extraction.Field{{Name: "total", Value: rcStr("1000.00"), Region: &ftBoxC, Reason: extraction.ReasonNone}}
		if !reflect.DeepEqual(got.Alternatives, wantAlts) {
			t.Errorf("alternatives = %+v, want %+v", got.Alternatives, wantAlts)
		}
	})
}

// AC-9. A withholding-tax invoice's anchored net payable is not condemned when the gross is
// nowhere printed; where the gross IS printed once, it is found and the net payable becomes its
// alternative -- there is no withholding-label gate (0.6d gate, Q2 = Option A).
func TestReconcile_AWithholdingNetPayableIsNotCondemned(t *testing.T) {
	cands := ftWHTCandidates()

	// Arm 1: no gross printed. The net payable stays exactly as decideField/corroborateTotal
	// left it.
	without, with := ftRun(cands, nil, nil), ftRun(cands, nil, ftWHTPages())
	arm1 := rtaFind(t, with, "total")
	if arm1.Value == nil || *arm1.Value != "1025.00" || arm1.Reason != extraction.ReasonNone {
		t.Errorf("arm 1 total = %v/%q, want 1025.00/ReasonNone -- an unprinted gross must not condemn the net payable", arm1.Value, arm1.Reason)
	}
	if len(arm1.Alternatives) != 0 {
		t.Errorf("arm 1 alternatives = %+v, want none", arm1.Alternatives)
	}
	if diff := ftHeaderDiff(t, without, with); diff != nil {
		t.Errorf("ftHeaderDiff = %v, want none", diff)
	}

	// Arm 2: the gross IS printed once. [wht-gross-found]: it is found, and the net payable
	// becomes its alternative -- no withholding-label gate holds it off.
	pages := ftWHTPages()
	pages[0].Tokens = append(pages[0].Tokens, ftTok("1,075.00", whtBoxGross))
	arm2 := rtaFind(t, ftRun(cands, nil, pages), "total")
	if arm2.Value == nil || *arm2.Value != "1075.00" {
		t.Fatalf("arm 2 total = %v, want %q", arm2.Value, "1075.00")
	}
	if arm2.Region == nil || *arm2.Region != whtBoxGross {
		t.Errorf("arm 2 total.Region = %+v, want %+v", arm2.Region, whtBoxGross)
	}
	if arm2.Reason != extraction.ReasonAmbiguous {
		t.Errorf("arm 2 total.Reason = %q, want ReasonAmbiguous", arm2.Reason)
	}
	wantAlts := []extraction.Field{{Name: "total", Value: rcStr("1025.00"), Region: &whtBoxNet, Reason: extraction.ReasonNone}}
	if len(arm2.Alternatives) == 0 {
		t.Fatalf("arm 2 alternatives empty, want the anchored 1025.00 reading")
	}
	if !reflect.DeepEqual(arm2.Alternatives, wantAlts) {
		t.Errorf("arm 2 alternatives = %+v, want %+v", arm2.Alternatives, wantAlts)
	}
}
