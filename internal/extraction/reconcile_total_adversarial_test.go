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
		t.Errorf("the alternative carries %v, want %q", alt.Value, far.Value)
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
		t.Errorf("total = %v, want %q", total.Value, near)
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
