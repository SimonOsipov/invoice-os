package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"
)

// Spec values; a drift here changes the security bound.
func TestHandoffAndThrottle_ConstantsPinned(t *testing.T) {
	if HandoffTTL != 60*time.Second {
		t.Errorf("HandoffTTL = %v, want 60s", HandoffTTL)
	}
	if SignInMaxFailures != 10 {
		t.Errorf("SignInMaxFailures = %d, want 10", SignInMaxFailures)
	}
	if SignInWindow != 15*time.Minute {
		t.Errorf("SignInWindow = %v, want 15m", SignInWindow)
	}
	if SignInMaxKeys != 100_000 {
		t.Errorf("SignInMaxKeys = %d, want 100000", SignInMaxKeys)
	}
}

func TestHandoffStore_PutAfterTakeIsFresh(t *testing.T) {
	s := NewHandoffStore(HandoffTTL, time.Now)
	st := randomState(t)
	c1, _ := s.Put("T1", stateHash(st))
	if tok, ok := s.Take(c1, st); !ok || tok != "T1" {
		t.Fatalf("Take(c1) = (%q, %v), want (\"T1\", true)", tok, ok)
	}
	c2, _ := s.Put("T2", stateHash(st))
	if c2 == c1 {
		t.Fatal("Put after Take reused the spent code")
	}
	if tok, ok := s.Take(c1, st); ok || tok != "" {
		t.Fatalf("Take(c1) after a new Put = (%q, %v), want (\"\", false)", tok, ok)
	}
	if tok, ok := s.Take(c2, st); !ok || tok != "T2" {
		t.Fatalf("Take(c2) = (%q, %v), want (\"T2\", true)", tok, ok)
	}
}

func TestHandoffStore_ExpiredWrongStateStillRemoves(t *testing.T) {
	clk := newTestClock()
	s := NewHandoffStore(HandoffTTL, clk.Now)
	s1, s2 := randomState(t), randomState(t)
	c, _ := s.Put("T", stateHash(s1))
	control, _ := s.Put("C", stateHash(s1))

	clk.Advance(HandoffTTL)
	if tok, ok := s.Take(c, s2); ok || tok != "" {
		t.Fatalf("expired Take with wrong state = (%q, %v), want (\"\", false)", tok, ok)
	}
	clk.Advance(-HandoffTTL)
	if tok, ok := s.Take(control, s1); !ok || tok != "C" {
		t.Fatalf("control Take after rewind = (%q, %v), want (\"C\", true)", tok, ok)
	}
	if _, ok := s.Take(c, s1); ok {
		t.Fatal("code survived an expired wrong-state Take")
	}
}

// The state hash is compared in constant time. The AST carries no
// comments, so a commented-out call does not count. Go identifiers are
// case-sensitive, so the match is exact on purpose.
func TestHandoffStore_TakeComparesStateInConstantTime(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handoff.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var take *ast.FuncDecl
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Name.Name == "Take" && fd.Recv != nil && fd.Body != nil {
			take = fd
		}
	}
	if take == nil {
		t.Fatal("HandoffStore.Take not found in handoff.go")
	}
	if n := len(take.Body.List); n < 3 {
		t.Fatalf("Take body has %d statements; scan population too small", n)
	}

	ctCalls, eqOnHash := 0, 0
	// A call's result is not the hash, so the operand walk stops at calls.
	mentionsStateHash := func(e ast.Expr) bool {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "stateHash" {
				found = true
			}
			_, call := n.(*ast.CallExpr)
			return !call
		})
		return found
	}
	ast.Inspect(take.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, _ := sel.X.(*ast.Ident)
			if pkg != nil && pkg.Name == "subtle" && sel.Sel.Name == "ConstantTimeCompare" && len(x.Args) == 2 &&
				(mentionsStateHash(x.Args[0]) || mentionsStateHash(x.Args[1])) {
				ctCalls++
			}
			if pkg != nil && (pkg.Name == "bytes" || pkg.Name == "reflect") {
				for _, a := range x.Args {
					if mentionsStateHash(a) {
						eqOnHash++
					}
				}
			}
		case *ast.BinaryExpr:
			if (x.Op == token.EQL || x.Op == token.NEQ) && (mentionsStateHash(x.X) || mentionsStateHash(x.Y)) {
				eqOnHash++
			}
		}
		return true
	})
	if ctCalls != 1 {
		t.Errorf("subtle.ConstantTimeCompare on stateHash in Take = %d calls, want 1", ctCalls)
	}
	if eqOnHash != 0 {
		t.Errorf("Take compares stateHash with a variable-time operator %d times, want 0", eqOnHash)
	}
}
