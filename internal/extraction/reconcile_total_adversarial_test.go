// reconcile_total_adversarial_test.go: the header total's doubt past its acceptance criteria --
// the dedup, more than two competitors, the region an alternative carries, and the two other
// Reconcile passes that write a reason.
package extraction_test

import (
	"go/ast"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rtaFind returns the result named name, failing rather than handing back a zero FieldResult a
// later assertion would read as "decided with no value".
func rtaFind(t *testing.T, results []extraction.FieldResult, name string) extraction.FieldResult {
	t.Helper()
	got, ok := rcFind(results, name)
	if !ok {
		t.Fatalf("Reconcile emitted no %s", name)
	}
	return got
}

// D-15 on the widened field: two readings of one printed total are one answer. The widening is
// what puts the farther reading in the group at all, so only the dedup keeps this decided.
func TestReconcile_TwoAdjacentTotalReadingsOfOneValueStayDecided(t *testing.T) {
	const v = "1000.00"
	near := rcAdjacentAt("total", v, extraction.TierGeneric, 0.03)
	far := rcAdjacentAt("total", v, extraction.TierGeneric, 0.05)

	// Non-vacuity: both arms satisfy every conjunct of the doubt and stand at different
	// distances, so nothing but the dedup can decide this.
	if !near.Adjacent || near.Tier != extraction.TierGeneric || near.Distance == far.Distance || near.Value != far.Value {
		t.Fatalf("fixture %+v / %+v: two adjacent generic readings of ONE value at different distances, or the zero below is earned by the wrong clause", near, far)
	}

	got := rcDecide(t, "total", far, near)
	if *got.Value != v {
		t.Errorf("total = %q, want %q", *got.Value, v)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("total reason = %q, want %q -- two readings of one value are one answer", got.Reason, extraction.ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("total alternatives = %q, want none -- a reviewer asked to choose between 1000.00 and 1000.00 has no choice", valuesOf(got.Alternatives))
	}

	// The discriminator: the same pair, one value different. Without it the zero above is also
	// what a doubt that never reaches total produces.
	competing := far
	competing.Value = "8600.00"
	doubted := rcDecide(t, "total", competing, near)
	if doubted.Reason != extraction.ReasonAmbiguous || !slices.Equal(valuesOf(doubted.Alternatives), []string{competing.Value}) {
		t.Fatalf("the same pair carrying two values reads %q with alternatives %q, want %q with [%q]", doubted.Reason, valuesOf(doubted.Alternatives), extraction.ReasonAmbiguous, competing.Value)
	}
}

// Two competitors and a repeat of one of them: a fixture of two cannot tell a walk that stops
// at the first alternative from one that stops at the first duplicate. The order is the
// comparator's, nearest competitor first, and the review screen renders the chips in it.
func TestReconcile_EveryCompetingTotalReadingIsOfferedOnceNearestFirst(t *testing.T) {
	head := rcAdjacentAt("total", "1000.00", extraction.TierGeneric, 0.03)
	second := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.05)
	repeat := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.07)
	third := rcAdjacentAt("total", "250.00", extraction.TierGeneric, 0.09)

	// Input order is deliberately not output order: the head is the comparator's choice.
	got := rcDecide(t, "total", third, repeat, second, head)
	if *got.Value != head.Value {
		t.Errorf("total = %q, want %q -- the nearest reading still decides the value", *got.Value, head.Value)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q", got.Reason, extraction.ReasonAmbiguous)
	}
	alts := valuesOf(got.Alternatives)
	if len(alts) == 0 {
		t.Fatalf("total reads ambiguous with no alternative; every assertion below reads an empty list")
	}
	if want := []string{second.Value, third.Value}; !slices.Equal(alts, want) {
		t.Errorf("total alternatives = %q, want %q -- both competitors, the repeat collapsed, nearest first", alts, want)
	}
}

// An alternative on another page is what the review screen points the reviewer at, and no spec
// read Alternatives[i].Region before. The doubt also crosses pages, which no corpus layout does.
func TestReconcile_ATotalCompetingOnAnotherPageCarriesThatPagesRegion(t *testing.T) {
	head := rcAdjacentAt("total", "1000.00", extraction.TierGeneric, 0.03)
	head.Region = &extraction.Region{Page: 1, X0: 0.70, Y0: 0.80, X1: 0.90, Y1: 0.83}
	far := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.05)
	far.Region = &extraction.Region{Page: 3, X0: 0.70, Y0: 0.10, X1: 0.90, Y1: 0.13}

	got := rcDecide(t, "total", far, head)
	if got.Reason != extraction.ReasonAmbiguous {
		t.Fatalf("total reason = %q, want %q -- the doubt is keyed on the head's flag, not on the two readings sharing a page", got.Reason, extraction.ReasonAmbiguous)
	}
	if got.Region == nil || *got.Region != *head.Region {
		t.Errorf("total region = %+v, want %+v -- the decided cell points at the reading that decided it", got.Region, head.Region)
	}
	if len(got.Alternatives) != 1 {
		t.Fatalf("total carries %d alternative(s) %q, want 1", len(got.Alternatives), valuesOf(got.Alternatives))
	}
	alt := got.Alternatives[0]
	if alt.Value == nil || *alt.Value != far.Value {
		t.Errorf("the alternative carries %q, want %q", rtVal(alt.Value), far.Value)
	}
	if alt.Reason != extraction.ReasonNone {
		t.Errorf("the alternative reads %q, want %q -- the reason lives on the decided cell alone", alt.Reason, extraction.ReasonNone)
	}
	if alt.Region == nil || *alt.Region != *far.Region {
		t.Errorf("the alternative points at %+v, want %+v -- a reviewer sent to page 1 for a page 3 amount is sent nowhere", alt.Region, far.Region)
	}
}

// Reconcile writes a reason in three places. This runs all three at once: the line sum condemns
// subtotal, the entity check condemns supplier_name, and neither may reach the doubtful total.
func TestReconcile_ADoubtfulTotalSurvivesTheSubtotalAndSupplierPasses(t *testing.T) {
	const near, far = "1000.00", "8600.00"
	n, f := rcDoubtPair("total", near, far, extraction.TierGeneric)
	results := extraction.Reconcile(extraction.Input{
		Candidates: []extraction.Candidate{
			f, n,
			rcCandidate("subtotal", "1000.00"), // the lines below sum to 900.00
			rcCandidate("supplier_name", "Acme Ltd"),
		},
		Lines:  []extraction.DocLine{{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("900.00"), LineTotal: rcStr("900.00")}},
		Entity: extraction.Entity{Name: "Zenith Ltd"},
	})

	// Floors: both other passes must actually have fired, or the total below is untouched by
	// two passes that never ran.
	if got := rtaFind(t, results, "subtotal"); got.Reason != extraction.ReasonInconsistent {
		t.Fatalf("subtotal reads %q, want %q -- the line-sum pass did not fire and this fixture proves nothing about it", got.Reason, extraction.ReasonInconsistent)
	}
	if got := rtaFind(t, results, "supplier_name"); got.Reason != extraction.ReasonInconsistent {
		t.Fatalf("supplier_name reads %q, want %q -- the entity pass did not fire and this fixture proves nothing about it", got.Reason, extraction.ReasonInconsistent)
	}

	total := rtaFind(t, results, "total")
	if total.Value == nil || *total.Value != near {
		t.Errorf("total = %q, want %q", rtVal(total.Value), near)
	}
	if total.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- a doubtful total is not rewritten by a pass keyed on another field", total.Reason, extraction.ReasonAmbiguous)
	}
	if want := []string{far}; !slices.Equal(valuesOf(total.Alternatives), want) {
		t.Errorf("total alternatives = %q, want %q", valuesOf(total.Alternatives), want)
	}

	// The header total and a row's line_total are different names; the doubt reaches neither
	// the rows nor their arithmetic.
	if got := rcLineValues(results); len(got) == 0 {
		t.Errorf("Reconcile emitted no line-item value row from one clean line")
	}
	if got := rcLineFlags(results); len(got) != 0 {
		t.Errorf("Reconcile flagged %d line row(s) %+v on arithmetic that balances", len(got), got)
	}
}

// --- EXTR-23-02: the arithmetic referee is wired for the header total alone ----------

// rtaReferee is the name the referee ships under. Named once so the AST reader below and its
// two control sources cannot drift apart.
const rtaReferee = "corroborateTotal"

// rtaWiring is what reconcile.go's AST says about the referee: how often it is declared, how
// often it is called, whether that call sits inside Reconcile, and whether the if guarding it
// names totalField rather than a bare literal a rename would walk past.
type rtaWiring struct {
	decls, calls int
	inReconcile  bool
	gatedByField bool
}

func rtaReadWiring(f *ast.File) rtaWiring {
	var w rtaWiring
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == rtaReferee {
			w.decls++
		}
	}

	var stack []ast.Node // the callback always returns true, so every push has its nil pop
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != rtaReferee {
			return true
		}
		w.calls++
		for _, anc := range stack {
			if fn, ok := anc.(*ast.FuncDecl); ok && fn.Name.Name == "Reconcile" {
				w.inReconcile = true
			}
		}
		for i := len(stack) - 1; i >= 0; i-- {
			ifs, ok := stack[i].(*ast.IfStmt)
			if !ok {
				continue
			}
			w.gatedByField = rtaNamesIdent(ifs.Cond, "totalField")
			break // the NEAREST enclosing if is the gate; an outer one guards something else
		}
		return true
	})
	return w
}

// rtaNamesIdent reports whether n names the identifier name anywhere inside it.
func rtaNamesIdent(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// The two control sources. Both are written with a different call arity and a lower-case field
// than reconcile.go's own, so this file never matches a mutation needle aimed at the referee.
const rtaWiredSrc = `package p

func Reconcile(out []cell) {
	for i := range out {
		if out[i].name == totalField {
			out[i] = corroborateTotal(out[i])
			break
		}
	}
}

func corroborateTotal(c cell) cell { return c }
`

const rtaUnwiredSrc = `package p

func someOtherPass(out []cell) {
	for i := range out {
		if out[i].name == "total" {
			out[i] = corroborateTotal(out[i])
		}
	}
}

func corroborateTotal(c cell) cell { return c }
`

// AC-7. The referee adjudicates the header total and nothing else. Two halves, because either
// alone is satisfiable by the wrong code: the AST says the referee is called once, inside
// Reconcile, behind a gate naming totalField; the walk says that on the other nine header
// fields the same competing pair and the same decided addends change nothing at all.
func TestReconcile_TheRefereeIsWiredForTotalAlone(t *testing.T) {
	got := rtaReadWiring(rcParse(t, "reconcile.go", nil))
	if got.decls != 1 {
		t.Fatalf("reconcile.go declares %s %d time(s), want 1; the wiring below was read over nothing", rtaReferee, got.decls)
	}
	if got.calls != 1 {
		t.Errorf("reconcile.go calls %s at %d site(s), want 1 -- a second call site adjudicates a second field with no acceptance criterion of its own", rtaReferee, got.calls)
	}
	if !got.inReconcile {
		t.Errorf("no call to %s sits inside Reconcile; a referee the decision stage never runs decides nothing", rtaReferee)
	}
	if !got.gatedByField {
		t.Errorf("the if guarding %s does not name totalField; a bare literal is what lets the one wired field drift without a red", rtaReferee)
	}

	// Needle and control. A reader that cannot report the wiring PRESENT, or cannot report it
	// ABSENT, reads exactly like a reader that never fired.
	if w := rtaReadWiring(rcParse(t, "needle.go", rtaWiredSrc)); w.decls != 1 || w.calls != 1 || !w.inReconcile || !w.gatedByField {
		t.Errorf("the reader reads %+v over a source wired exactly as required; it cannot report the wiring present", w)
	}
	if w := rtaReadWiring(rcParse(t, "control.go", rtaUnwiredSrc)); w.calls != 1 || w.inReconcile || w.gatedByField {
		t.Errorf("the reader reads %+v over a source calling the referee outside Reconcile behind a literal; it cannot report the wiring absent", w)
	}

	// The behavioural half: the same competing pair on every header field in turn, with both
	// addends decided except where the field under test IS the addend.
	if len(extraction.HeaderFields) != 10 {
		t.Fatalf("HeaderFields names %d field(s), want 10; the partition below was measured over a different vocabulary", len(extraction.HeaderFields))
	}
	var corroborated, untouched []string
	for _, field := range extraction.HeaderFields {
		var cands []extraction.Candidate
		if slices.Contains(rdqScope, field) {
			n, f := rcDoubtPair(field, rtNear, rtFar, extraction.TierGeneric)
			cands = append(cands, f, n)
		} else {
			// Outside the doubt's scope only equal standing competes (D-14): same Tier AND same
			// Distance. compareCandidates then heads on the lower value, rtNear.
			cands = append(cands,
				rcCandAt(field, rtFar, extraction.TierGeneric, 0.03),
				rcCandAt(field, rtNear, extraction.TierGeneric, 0.03))
		}
		if field != "subtotal" {
			cands = append(cands, rcCandidate("subtotal", rtSub))
		}
		if field != "vat" {
			cands = append(cands, rcCandidate("vat", rtVAT))
		}

		res := rcDecide(t, field, cands...)
		switch {
		case *res.Value == rtFar && res.Reason == extraction.ReasonNone && len(res.Alternatives) == 0:
			corroborated = append(corroborated, field)
		case *res.Value == rtNear && res.Reason == extraction.ReasonAmbiguous && slices.Equal(valuesOf(res.Alternatives), []string{rtFar}):
			untouched = append(untouched, field)
		default:
			t.Errorf("%s reads %q / %q offering %q; want either %q decided with no alternative, or %q still doubtful offering [%q]", field, *res.Value, res.Reason, valuesOf(res.Alternatives), rtFar, rtNear, rtFar)
		}
	}
	if !slices.Equal(corroborated, []string{"total"}) {
		t.Errorf("the arithmetic settled %q, want exactly [total] -- a referee wired for a second field decides a number no acceptance criterion covers", corroborated)
	}
	if want := len(extraction.HeaderFields) - 1; len(untouched) != want {
		t.Errorf("%d of %d header field(s) came back untouched (%q), want %d; the [total] above is otherwise what a referee that ran on everything also produces", len(untouched), len(extraction.HeaderFields), untouched, want)
	}
}

// --- EXTR-23-02 QA: the referee past its acceptance criteria -------------------------
//
// Sign, the tolerance boundary, three-way competition, the head as winner, unparseable readings,
// sub-kobo precision. corroborateTotal adds no tenant-owned table and reads nothing from the
// database, so no cross-tenant refusal is owed here; the referee's DB-side behaviour is
// TestRLS_EndToEndTheRefereeRepairsTheRankOneTotalDecoy's.

// A credit note's identity is negative on every term. A referee comparing magnitudes finds BOTH
// readings zero from |subtotal + vat| and picks neither, so the sign is what this measures.
func TestReconcile_ACreditNoteTotalIsCorroboratedOnItsSign(t *testing.T) {
	const sub, vat = "-1000.00", "-75.00"
	const wrongSign, rightSign = "1075.00", "-1075.00" // -1000.00 + -75.00 = -1075.00

	// Floor: the two readings are equal in magnitude, so only the sign can tell them apart.
	if !rtDec(t, wrongSign).Abs().Equal(rtDec(t, rightSign).Abs()) {
		t.Fatalf("%s and %s differ in magnitude; a sign-blind referee would already discriminate them", wrongSign, rightSign)
	}
	if !rtDec(t, sub).Add(rtDec(t, vat)).Equal(rtDec(t, rightSign)) {
		t.Fatalf("%s + %s is not %s; the fixture does not balance", sub, vat, rightSign)
	}

	near, far := rcDoubtPair("total", wrongSign, rightSign, extraction.TierGeneric)
	got := rcDecide(t, "total", far, near, rcCandidate("subtotal", sub), rcCandidate("vat", vat))
	if *got.Value != rightSign {
		t.Errorf("total = %q, want %q -- a credit note's total is negative, and the positive reading of the same magnitude is the wrong one", *got.Value, rightSign)
	}
	if got.Reason != extraction.ReasonNone || len(got.Alternatives) != 0 {
		t.Errorf("total reads %s, want %q decided with no alternative", rtShow(got), rightSign)
	}

	// Non-vacuity: the same pair with no addends heads on the wrong sign, doubtful. The pick
	// above is the arithmetic's, not the comparator's.
	if doubted := rcDecide(t, "total", far, near); doubted.Reason != extraction.ReasonAmbiguous || *doubted.Value != wrongSign {
		t.Fatalf("the same pair with no addends reads %s, want %q still doubtful", rtShow(doubted), wrongSign)
	}
}

// The head is a competing reading like any other. A referee walking only res.Alternatives finds
// nothing balancing here and leaves the whole cell doubtful.
func TestReconcile_TheCorroboratedTotalMayBeTheHeadItself(t *testing.T) {
	head := rcAdjacentAt("total", rtFar, extraction.TierGeneric, 0.03) // 8000.00 + 600.00 = 8600.00
	head.Region = &extraction.Region{Page: 1, X0: 0.70, Y0: 0.80, X1: 0.90, Y1: 0.83}
	comp := rcAdjacentAt("total", rtNear, extraction.TierGeneric, 0.05)
	comp.Region = &extraction.Region{Page: 3, X0: 0.70, Y0: 0.10, X1: 0.90, Y1: 0.13}

	got := rcDecide(t, "total", comp, head, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *got.Value != rtFar {
		t.Errorf("total = %q, want %q -- the balancing reading wins whether or not the comparator already headed on it", *got.Value, rtFar)
	}
	if got.Region == nil || *got.Region != *head.Region {
		t.Errorf("total region = %+v, want %+v -- the winner's own region survives the rebuild", got.Region, head.Region)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("total reason = %q, want %q", got.Reason, extraction.ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("total still offers %q, want none -- the losing reading is settled, not shortlisted", valuesOf(got.Alternatives))
	}
	if got.Alternatives == nil {
		t.Error("total carries a nil Alternatives; it marshals to null rather than an empty list")
	}

	// Non-vacuity: the same pair with no addends keeps the loser as the reviewer's alternative.
	doubted := rcDecide(t, "total", comp, head)
	if doubted.Reason != extraction.ReasonAmbiguous || !slices.Equal(valuesOf(doubted.Alternatives), []string{rtNear}) {
		t.Fatalf("the same pair with no addends reads %s, want %q doubtful offering [%q]", rtShow(doubted), rtFar, rtNear)
	}
}

// Three readings, two of them balancing, and the head balancing NEITHER. AC-4 counts two winners
// drawn from the head and its one alternative; here both winners are alternatives and the whole
// chooser survives in order. A walk short of the last reading (readings[:2]) counts one winner
// here and decides a number, which AC-4's pair of two is too small to notice.
func TestReconcile_ThreeCompetingTotalsWhereTwoBalanceStayDoubtful(t *testing.T) {
	const alsoBalances = "8600.01" // one kobo from 8600.00, and the boundary is inclusive
	head := rcAdjacentAt("total", rtNear, extraction.TierGeneric, 0.03)
	second := rcAdjacentAt("total", rtFar, extraction.TierGeneric, 0.05)
	third := rcAdjacentAt("total", alsoBalances, extraction.TierGeneric, 0.07)

	// Floor: two of the three really are within tolerance of the identity, and the head is not.
	want, tol := rtDec(t, rtSub).Add(rtDec(t, rtVAT)), rtDec(t, "0.01")
	for _, v := range []string{rtFar, alsoBalances} {
		if want.Sub(rtDec(t, v)).Abs().GreaterThan(tol) {
			t.Fatalf("%s is more than %s from %s; only one reading balances and this is AC-1 restated", v, tol, want)
		}
	}
	if !want.Sub(rtDec(t, rtNear)).Abs().GreaterThan(tol) {
		t.Fatalf("the head %s also balances; the fixture has three winners, not two", rtNear)
	}

	got := rcDecide(t, "total", third, second, head, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *got.Value != rtNear {
		t.Errorf("total = %q, want %q -- two readings balance, so the arithmetic breaks nothing and moves no value", *got.Value, rtNear)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- a referee that stopped at the first balancing reading would have decided %q here", got.Reason, extraction.ReasonAmbiguous, rtFar)
	}
	if wantAlts := []string{rtFar, alsoBalances}; !slices.Equal(valuesOf(got.Alternatives), wantAlts) {
		t.Errorf("total alternatives = %q, want %q -- both competitors survive, nearest first", valuesOf(got.Alternatives), wantAlts)
	}

	// The instrument. The third reading moved off the identity and the referee must fire, or the
	// unchanged cell above is what a referee that never ran also produces.
	off := third
	off.Value = "3000.00"
	single := rcDecide(t, "total", off, second, head, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *single.Value != rtFar || single.Reason != extraction.ReasonNone || len(single.Alternatives) != 0 {
		t.Errorf("with one balancing reading of three the total reads %s, want %q decided with no alternative", rtShow(single), rtFar)
	}
}

// The winner sits at the LAST alternative. A walk reading only the head and the first
// alternative finds nothing and leaves the cell doubtful.
func TestReconcile_TheBalancingTotalIsFoundAtTheLastAlternative(t *testing.T) {
	head := rcAdjacentAt("total", rtNear, extraction.TierGeneric, 0.03)
	middle := rcAdjacentAt("total", "3000.00", extraction.TierGeneric, 0.05)
	last := rcAdjacentAt("total", rtFar, extraction.TierGeneric, 0.07)
	last.Region = &extraction.Region{Page: 3, X0: 0.70, Y0: 0.10, X1: 0.90, Y1: 0.13}

	// Floor: the winner really is last in the comparator's order, so a short walk misses it.
	doubted := rcDecide(t, "total", last, middle, head)
	if got := valuesOf(doubted.Alternatives); !slices.Equal(got, []string{middle.Value, rtFar}) {
		t.Fatalf("the same three readings are offered as %q; the balancing one is not last and a short walk would still reach it", got)
	}

	got := rcDecide(t, "total", last, middle, head, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *got.Value != rtFar {
		t.Errorf("total = %q, want %q -- the balancing reading is the answer wherever it sits in the chooser", *got.Value, rtFar)
	}
	if got.Region == nil || *got.Region != *last.Region {
		t.Errorf("total region = %+v, want %+v", got.Region, last.Region)
	}
	if got.Reason != extraction.ReasonNone || len(got.Alternatives) != 0 {
		t.Errorf("total reads %s, want %q decided with no alternative", rtShow(got), rtFar)
	}
}

// An unparseable reading is skipped, and skipped is not folded to zero. The addends print 0.00,
// so a referee reading "N/A" as zero would corroborate it and decide a cell holding "N/A".
func TestReconcile_AnUnparseableTotalReadingIsSkippedNotZeroed(t *testing.T) {
	const unparseable, printed = "N/A", "500.00"
	head := rcAdjacentAt("total", unparseable, extraction.TierGeneric, 0.03)
	comp := rcAdjacentAt("total", printed, extraction.TierGeneric, 0.05)
	zeroAddends := []extraction.Candidate{rcCandidate("subtotal", "0.00"), rcCandidate("vat", "0.00")}

	// Floor: both addends are DECIDED zeros, so the referee really did run and really did compute
	// a target of 0.00 -- the result below is earned by the skip, not by a gate that held it off.
	results := extraction.Reconcile(extraction.Input{Candidates: append([]extraction.Candidate{comp, head}, zeroAddends...)})
	for _, name := range []string{"subtotal", "vat"} {
		addend := rtaFind(t, results, name)
		if addend.Reason != extraction.ReasonNone || addend.Value == nil || *addend.Value != "0.00" {
			t.Fatalf("%s reads %s, want a decided %q; the referee was held off before it ever read the total", name, rtShow(addend), "0.00")
		}
	}

	got := rtaFind(t, results, "total")
	if got.Value == nil || *got.Value != unparseable {
		t.Errorf("total = %q, want %q -- a reading no parser accepts is still the head the comparator chose", rtVal(got.Value), unparseable)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- %q is not a printed 0.00 and corroborates nothing", got.Reason, extraction.ReasonAmbiguous, unparseable)
	}
	if want := []string{printed}; !slices.Equal(valuesOf(got.Alternatives), want) {
		t.Errorf("total alternatives = %q, want %q", valuesOf(got.Alternatives), want)
	}

	// The instrument: the same shape with a PRINTED zero head, which the same addends do
	// corroborate. Without it the untouched cell above is what a referee that never ran produces.
	zeroHead := head
	zeroHead.Value = "0.00"
	fired := rcDecide(t, "total", comp, zeroHead, zeroAddends[0], zeroAddends[1])
	if *fired.Value != "0.00" || fired.Reason != extraction.ReasonNone || len(fired.Alternatives) != 0 {
		t.Errorf("with a printed 0.00 head the total reads %s, want %q decided with no alternative", rtShow(fired), "0.00")
	}
}

// The tolerance boundary on the referee's own path: one kobo balances, one kobo and a hair does
// not. reconcileTolerance's VALUE is pinned by TestReconcile_ToleranceIsOneMinorUnit; the
// STRICTNESS of the comparison is what this pins.
func TestReconcile_TheCorroborationBoundaryIsOneKoboInclusive(t *testing.T) {
	const atBoundary, pastBoundary = "8600.01", "8600.0101" // 8000.00 + 600.00 = 8600.00
	want, tol := rtDec(t, rtSub).Add(rtDec(t, rtVAT)), rtDec(t, "0.01")

	// Floor: the two readings sit either side of exactly one kobo, and by a hair -- a fixture a
	// full kobo apart would pass under either comparison the constant permits.
	if !want.Sub(rtDec(t, atBoundary)).Abs().Equal(tol) {
		t.Fatalf("%s is %s from %s, not exactly %s; this fixture does not sit ON the boundary", atBoundary, want.Sub(rtDec(t, atBoundary)).Abs(), want, tol)
	}
	if !want.Sub(rtDec(t, pastBoundary)).Abs().GreaterThan(tol) {
		t.Fatalf("%s is within %s of %s; both arms are on the same side of the boundary", pastBoundary, tol, want)
	}

	head := rcAdjacentAt("total", rtNear, extraction.TierGeneric, 0.03)
	on := rcAdjacentAt("total", atBoundary, extraction.TierGeneric, 0.05)
	got := rcDecide(t, "total", on, head, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *got.Value != atBoundary || got.Reason != extraction.ReasonNone || len(got.Alternatives) != 0 {
		t.Errorf("a reading exactly one kobo from the identity reads %s, want %q decided -- the printed subtotal is itself rounded, so the boundary is inclusive", rtShow(got), atBoundary)
	}

	past := on
	past.Value = pastBoundary
	unmoved := rcDecide(t, "total", past, head, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *unmoved.Value != rtNear || unmoved.Reason != extraction.ReasonAmbiguous {
		t.Errorf("a reading %s from the identity reads %s, want %q still doubtful -- past the tolerance is not corroboration", want.Sub(rtDec(t, pastBoundary)).Abs(), rtShow(unmoved), rtNear)
	}
	if wantAlts := []string{pastBoundary}; !slices.Equal(valuesOf(unmoved.Alternatives), wantAlts) {
		t.Errorf("total alternatives = %q, want %q", valuesOf(unmoved.Alternatives), wantAlts)
	}
}

// Addends carrying a third decimal. The identity is compared at full precision: the target is the
// exact sum, not the sum truncated to the two decimals a printed amount usually has.
func TestReconcile_TheCorroboratedSumKeepsItsThirdDecimal(t *testing.T) {
	const reading = "8600.00"
	cases := []struct {
		name       string
		sub, vat   string
		wantValue  string
		wantReason extraction.Reason
	}{
		// 8000.005 + 600.004 = 8600.009, which is 0.009 from the reading: inside the kobo.
		{"nine thousandths inside", "8000.005", "600.004", reading, extraction.ReasonNone},
		// 8000.005 + 600.006 = 8600.011, which is 0.011 away: outside. Truncated to two decimals
		// both sums read 8600.00 and both arms would corroborate.
		{"eleven thousandths outside", "8000.005", "600.006", rtNear, extraction.ReasonAmbiguous},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Floor: the exact sum really does land where this arm is named for.
			sum := rtDec(t, tc.sub).Add(rtDec(t, tc.vat))
			inside := !sum.Sub(rtDec(t, reading)).Abs().GreaterThan(rtDec(t, "0.01"))
			if inside != (tc.wantReason == extraction.ReasonNone) {
				t.Fatalf("%s + %s = %s, which is %s from %s; this arm asserts the opposite of its own arithmetic", tc.sub, tc.vat, sum, sum.Sub(rtDec(t, reading)).Abs(), reading)
			}

			head := rcAdjacentAt("total", rtNear, extraction.TierGeneric, 0.03)
			comp := rcAdjacentAt("total", reading, extraction.TierGeneric, 0.05)
			got := rcDecide(t, "total", comp, head, rcCandidate("subtotal", tc.sub), rcCandidate("vat", tc.vat))
			if *got.Value != tc.wantValue || got.Reason != tc.wantReason {
				t.Errorf("total reads %s, want %q / %q", rtShow(got), tc.wantValue, tc.wantReason)
			}
		})
	}
}

// Sixteen significant digits, where one float64 step is 0.125 -- wider than the whole kobo
// tolerance. The identity holds EXACTLY under decimal; the same two addends summed as float64
// land 0.04 from the reading and corroborate nothing. reconcile.go names no float
// (TestReconcile_NamesNoFloatOnTheMoneyPath); this is that contract measured by outcome.
func TestReconcile_ACorroboratedTotalAtSixteenDigitsNeedsDecimalNotFloat(t *testing.T) {
	const sub, vat, balances = "999999999999999.99", "0.05", "1000000000000000.04"

	// Floor: the identity is EXACT here, so any daylight the referee reports is arithmetic it did
	// not do in decimal.
	if diff := rtDec(t, sub).Add(rtDec(t, vat)).Sub(rtDec(t, balances)); !diff.IsZero() {
		t.Fatalf("%s + %s is %s from %s; the fixture is not exact and proves nothing about precision", sub, vat, diff, balances)
	}

	head := rcAdjacentAt("total", rtNear, extraction.TierGeneric, 0.03)
	comp := rcAdjacentAt("total", balances, extraction.TierGeneric, 0.05)
	got := rcDecide(t, "total", comp, head, rcCandidate("subtotal", sub), rcCandidate("vat", vat))
	if *got.Value != balances {
		t.Errorf("total = %q, want %q -- summed as float64 these addends land 0.04 from the reading and nothing balances", *got.Value, balances)
	}
	if got.Reason != extraction.ReasonNone || len(got.Alternatives) != 0 {
		t.Errorf("total reads %s, want %q decided with no alternative", rtShow(got), balances)
	}
}
