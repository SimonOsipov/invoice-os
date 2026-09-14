// reconcile_find_total_adversarial_test.go: EXTR-29-01 AC-10 -- findTotal is wired at exactly
// one site, inside Reconcile, after corroborateTotal, gated by totalField.
package extraction_test

import (
	"go/ast"
	"reflect"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// ftWiring is what reconcile.go's AST says about findTotal: how often it is declared, how often
// it is called, whether that call sits inside Reconcile, whether the nearest enclosing if names
// totalField, and whether the call's own statement follows corroborateTotal's in the same block.
type ftWiring struct {
	decls, calls                            int
	inReconcile, gatedByField, afterReferee bool
}

// ftStmtCalls reports whether stmt itself calls name. Restated rather than reusing
// rtaReadWiring's own walk, so a mutation to that reader does not blind this one too.
func ftStmtCalls(stmt ast.Stmt, name string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

func ftReadWiring(f *ast.File) ftWiring {
	var w ftWiring
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "findTotal" {
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
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "findTotal" {
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
		for i := len(stack) - 1; i >= 0; i-- {
			block, ok := stack[i].(*ast.BlockStmt)
			if !ok {
				continue
			}
			refereeIdx, findIdx := -1, -1
			for j, stmt := range block.List {
				if ftStmtCalls(stmt, "corroborateTotal") {
					refereeIdx = j
				}
				if ftStmtCalls(stmt, "findTotal") {
					findIdx = j
				}
			}
			w.afterReferee = refereeIdx >= 0 && findIdx >= 0 && refereeIdx < findIdx
			break // the nearest enclosing block is the referee's own if-body
		}
		return true
	})
	return w
}

// ftWiredSrc is a positive fixture: findTotal wired exactly as required. Written with a
// different call arity and a lower-case field than reconcile.go's own, so it never matches a
// mutation needle aimed at the production wiring.
const ftWiredSrc = `package p

func Reconcile(out []cell) {
	for i := range out {
		if out[i].name == totalField {
			out[i] = corroborateTotal(out[i])
			out[i] = findTotal(out[i])
			break
		}
	}
}

func corroborateTotal(c cell) cell { return c }
func findTotal(c cell) cell        { return c }
`

// ftUnwiredSrc is the negative fixture: findTotal called outside Reconcile, before
// corroborateTotal, behind a string literal rather than totalField.
const ftUnwiredSrc = `package p

func someOtherPass(out []cell) {
	for i := range out {
		if out[i].name == "total" {
			out[i] = findTotal(out[i])
			out[i] = corroborateTotal(out[i])
		}
	}
}

func corroborateTotal(c cell) cell { return c }
func findTotal(c cell) cell        { return c }
`

// AC-10. findTotal is wired at exactly one site: inside Reconcile, after corroborateTotal in the
// same block, gated by totalField -- an AST reader over both, plus the header-field walk that
// shows the wiring's actual reach.
func TestReconcile_FindTotalIsWiredForTotalAlone(t *testing.T) {
	got := ftReadWiring(rcParse(t, "reconcile.go", nil))
	if got.decls != 1 {
		t.Fatalf("reconcile.go declares findTotal %d time(s), want 1; the wiring below was read over nothing", got.decls)
	}
	if got.calls != 1 {
		t.Errorf("reconcile.go calls findTotal at %d site(s), want 1", got.calls)
	}
	if !got.inReconcile {
		t.Errorf("no call to findTotal sits inside Reconcile")
	}
	if !got.gatedByField {
		t.Errorf("the if guarding findTotal does not name totalField")
	}
	if !got.afterReferee {
		t.Errorf("findTotal is not called after corroborateTotal in the same block")
	}

	// Needle and control. A reader that cannot report the wiring PRESENT, or cannot report it
	// ABSENT, reads exactly like a reader that never fired.
	if w := ftReadWiring(rcParse(t, "needle.go", ftWiredSrc)); w.decls != 1 || w.calls != 1 || !w.inReconcile || !w.gatedByField || !w.afterReferee {
		t.Errorf("the reader reads %+v over a source wired exactly as required; it cannot report the wiring present", w)
	}
	if w := ftReadWiring(rcParse(t, "control.go", ftUnwiredSrc)); w.calls != 1 || w.inReconcile || w.gatedByField || w.afterReferee {
		t.Errorf("the reader reads %+v over a source calling findTotal outside Reconcile before corroborateTotal, behind a literal; it cannot report the wiring absent", w)
	}

	// The behavioural half: the same F0 pages, with a plausible candidate on every non-money
	// header field. findTotal must move the header total alone.
	moneyFields := map[string]bool{"subtotal": true, "vat": true, "total": true}
	var others []string
	for _, f := range extraction.HeaderFields {
		if !moneyFields[f] {
			others = append(others, f)
		}
	}
	if len(others) != 7 {
		t.Fatalf("%d non-money header field(s), want 7; the partition below was measured over a different vocabulary", len(others))
	}

	cands := ftF0Candidates()
	for _, f := range others {
		cands = append(cands, rcCandAt(f, "v-"+f, extraction.TierGeneric, 0))
	}
	diff := ftHeaderDiff(t, ftRun(cands, nil, nil), ftRun(cands, nil, ftF0Pages()))
	if len(diff) == 0 {
		t.Fatalf("ftHeaderDiff is empty; the all-clear below proves nothing if findTotal never ran")
	}
	if want := []string{"total"}; !slices.Equal(diff, want) {
		t.Errorf("ftHeaderDiff = %v, want %v -- findTotal must only ever move the header total", diff, want)
	}
}

// Page tokens are read through ShapeAmount: a currency prefix and a comma-less spelling are each a
// reading, and two spellings of one value at two positions are two matches.
func TestReconcile_AFoundTotalIsReadThroughTheAmountShape(t *testing.T) {
	cands := ftF0Candidates()
	pagesWith := func(extra ...extraction.Token) []extraction.TokenPage {
		toks := []extraction.Token{ftTok("8,000.00", ftBoxA), ftTok("600.00", ftBoxB), ftTok("1,000.00", ftBoxC)}
		return []extraction.TokenPage{{Number: 1, Tokens: append(toks, extra...)}}
	}
	wantAlts := []extraction.Field{{Name: "total", Value: rcStr("1000.00"), Region: &ftBoxC, Reason: extraction.ReasonNone}}

	for _, text := range []string{"₦8,600.00", "8600.00"} {
		t.Run("found alone: "+text, func(t *testing.T) {
			got := rtaFind(t, ftRun(cands, nil, pagesWith(ftTok(text, ftBoxD))), "total")
			if got.Value == nil || *got.Value != "8600.00" || got.Reason != extraction.ReasonAmbiguous {
				t.Fatalf("total = %v/%q, want 8600.00/ReasonAmbiguous", got.Value, got.Reason)
			}
			if got.Region == nil || *got.Region != ftBoxD {
				t.Errorf("total.Region = %+v, want %+v", got.Region, ftBoxD)
			}
			if !reflect.DeepEqual(got.Alternatives, wantAlts) {
				t.Errorf("alternatives = %+v, want %+v", got.Alternatives, wantAlts)
			}
		})
	}

	// Each spelling alone is found above, so this silence is the count, not a rejected spelling.
	t.Run("two spellings at two positions", func(t *testing.T) {
		without := ftRun(cands, nil, nil)
		with := ftRun(cands, nil, pagesWith(ftTok("8600.00", ftBoxD), ftTok("8,600.00", ftBoxE)))
		got := rtaFind(t, with, "total")
		if got.Value == nil || *got.Value != "1000.00" || got.Reason != extraction.ReasonNone {
			t.Errorf("total = %v/%q, want 1000.00/ReasonNone", got.Value, got.Reason)
		}
		if diff := ftHeaderDiff(t, without, with); diff != nil {
			t.Errorf("ftHeaderDiff = %v, want none", diff)
		}
	})
}

// ftAssertFound8600At asserts the F0 total was found at box, with the anchored 1,000.00 kept.
func ftAssertFound8600At(t *testing.T, results []extraction.FieldResult, box extraction.Region) {
	t.Helper()
	got := rtaFind(t, results, "total")
	if got.Value == nil || *got.Value != "8600.00" || got.Reason != extraction.ReasonAmbiguous {
		t.Fatalf("total = %v/%q, want 8600.00/ReasonAmbiguous", got.Value, got.Reason)
	}
	if got.Region == nil || *got.Region != box {
		t.Errorf("total.Region = %+v, want %+v", got.Region, box)
	}
	wantAlts := []extraction.Field{{Name: "total", Value: rcStr("1000.00"), Region: &ftBoxC, Reason: extraction.ReasonNone}}
	if !reflect.DeepEqual(got.Alternatives, wantAlts) {
		t.Errorf("alternatives = %+v, want %+v", got.Alternatives, wantAlts)
	}
}

// Only a taught TOTAL silences the pass; a learned addend is the evidence it balances against.
func TestReconcile_ALearnedAddendDoesNotSilenceTheFoundTotal(t *testing.T) {
	learnedSub := rcCandAt("subtotal", "8000.00", extraction.TierLearned, 0)
	learnedSub.Region = &ftBoxA
	learnedVAT := rcCandAt("vat", "600.00", extraction.TierLearned, 0)
	learnedVAT.Region = &ftBoxB

	arms := []struct {
		name  string
		cands []extraction.Candidate
	}{
		{"learned subtotal", []extraction.Candidate{learnedSub, ftVAT(), ftTotalCand()}},
		{"learned vat", []extraction.Candidate{ftSubtotal(), learnedVAT, ftTotalCand()}},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			with := ftRun(arm.cands, nil, ftF0Pages())
			for _, want := range [][2]string{{"subtotal", "8000.00"}, {"vat", "600.00"}} {
				if got := rtaFind(t, with, want[0]); got.Value == nil || *got.Value != want[1] || got.Reason != extraction.ReasonNone {
					t.Fatalf("%s = %v/%q, want %s/ReasonNone -- the addend gate would silence this arm", want[0], got.Value, got.Reason, want[1])
				}
			}
			ftAssertFound8600At(t, with, ftBoxD)
		})
	}
}

// An addend read from a boxless token has no box to exclude; that token is already outside the
// population, so the printed total is still found.
func TestReconcile_ABoxlessAddendExcludesNothing(t *testing.T) {
	cands := []extraction.Candidate{
		rcAdjacentAt("subtotal", "8000.00", extraction.TierGeneric, 0.03),
		rcAdjacentAt("vat", "600.00", extraction.TierGeneric, 0.03),
		ftTotalCand(),
	}
	zeroWidth := extraction.Region{Page: 1, X0: 0.60, Y0: 0.70, X1: 0.60, Y1: 0.72}
	pages := []extraction.TokenPage{{Number: 1, Tokens: []extraction.Token{
		ftTok("8,000.00", zeroWidth), ftTok("600.00", zeroWidth), ftTok("1,000.00", ftBoxC), ftTok("8,600.00", ftBoxD),
	}}}

	with := ftRun(cands, nil, pages)
	for _, name := range []string{"subtotal", "vat"} {
		if got := rtaFind(t, with, name); got.Region != nil || got.Reason != extraction.ReasonNone {
			t.Fatalf("%s = region %+v/%q, want nil/ReasonNone -- this arm needs a decided boxless addend", name, got.Region, got.Reason)
		}
	}
	ftAssertFound8600At(t, with, ftBoxD)
}

// A box includes its page: an amount on page 2 at an addend's page-1 coordinates is another token.
func TestReconcile_AnAddendsBoxOnAnotherPageIsStillEvidence(t *testing.T) {
	for _, arm := range []struct {
		name string
		box  extraction.Region
	}{{"subtotal", ftBoxA}, {"vat", ftBoxB}} {
		t.Run(arm.name, func(t *testing.T) {
			onPage2 := arm.box
			onPage2.Page = 2
			pages := []extraction.TokenPage{
				{Number: 1, Tokens: []extraction.Token{ftTok("8,000.00", ftBoxA), ftTok("600.00", ftBoxB), ftTok("1,000.00", ftBoxC)}},
				{Number: 2, Tokens: []extraction.Token{ftTok("8,600.00", onPage2)}},
			}
			ftAssertFound8600At(t, ftRun(ftF0Candidates(), nil, pages), onPage2)
		})
	}
}
