// store_option_test.go: APPR-08-02 RED spec (Mode A) for StoreOption /
// WithApprovalsEnforced.
//
// POOL-FREE BY CONSTRUCTION, and that is the point. NewStore only stores the
// pointer (store.go), so nil is safe here. Routed through dbTestPools instead,
// every test below would t.Skip on every PR: internal/invoice sits in no CI job
// that sets DATABASE_URL, and CI's go job runs a bare `go test ./...` with no
// Postgres. Do not add a pool to this file.
package invoice

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The exported constructor shape cmd/invoice and APPR-08-03/04/05 depend on.
// internal/document/service_test.go's plain func(*pgxpool.Pool) *Store pin does
// NOT compile against a variadic constructor; the option parameter belongs in
// the pinned type.
var _ func(*pgxpool.Pool, ...StoreOption) *Store = NewStore

func TestNewStore_DefaultsToNotEnforced(t *testing.T) {
	if got := NewStore(nil).approvalsEnforced; got {
		t.Errorf("NewStore(nil).approvalsEnforced = %v, want false — the zero value must leave both doors into queued exactly as they were", got)
	}
}

func TestWithApprovalsEnforced_SetsTheField(t *testing.T) {
	if got := NewStore(nil, WithApprovalsEnforced(true)).approvalsEnforced; !got {
		t.Errorf("NewStore(nil, WithApprovalsEnforced(true)).approvalsEnforced = %v, want true", got)
	}
}

// TestNewStore_BothStatesCoexistInOneProcess is why the flag is a StoreOption
// rather than an env read or a package-level var: no t.Setenv, no subprocess
// re-exec, no pool, both states live in the same test binary.
func TestNewStore_BothStatesCoexistInOneProcess(t *testing.T) {
	off := NewStore(nil)
	on := NewStore(nil, WithApprovalsEnforced(true))
	explicitOff := NewStore(nil, WithApprovalsEnforced(false))

	if off.approvalsEnforced {
		t.Errorf("default store: approvalsEnforced = true, want false")
	}
	if !on.approvalsEnforced {
		t.Errorf("WithApprovalsEnforced(true) store: approvalsEnforced = false, want true")
	}
	if explicitOff.approvalsEnforced {
		t.Errorf("WithApprovalsEnforced(false) store: approvalsEnforced = true, want false")
	}
	if off.approvalsEnforced == on.approvalsEnforced {
		t.Errorf("both stores read %v — the two flag states are not distinguishable in one process, which is the whole reason this is an option and not an env read", on.approvalsEnforced)
	}
}

// TestStoreOptions_ApplyInOrderLastWins pins the for-range apply loop.
// The true-then-false half alone would pass against a NewStore that ignores its
// options entirely, so the reversed pair is asserted alongside it.
func TestStoreOptions_ApplyInOrderLastWins(t *testing.T) {
	if got := NewStore(nil, WithApprovalsEnforced(false), WithApprovalsEnforced(true)).approvalsEnforced; !got {
		t.Errorf("NewStore(nil, WithApprovalsEnforced(false), WithApprovalsEnforced(true)).approvalsEnforced = %v, want true — options must apply in order, last wins", got)
	}
	if got := NewStore(nil, WithApprovalsEnforced(true), WithApprovalsEnforced(false)).approvalsEnforced; got {
		t.Errorf("NewStore(nil, WithApprovalsEnforced(true), WithApprovalsEnforced(false)).approvalsEnforced = %v, want false — options must apply in order, last wins", got)
	}
}

// TestNewStore_BothAritiesCompile is AC #2's in-test half: the one-argument form
// the ~477 existing call sites use, and the two-argument form cmd/invoice will
// use, in one body. The 477-site proof itself is `go build ./...` + `go test
// ./...`, not this test.
func TestNewStore_BothAritiesCompile(t *testing.T) {
	if NewStore(nil) == nil {
		t.Fatal("NewStore(nil) returned nil")
	}
	if NewStore(nil, WithApprovalsEnforced(true)) == nil {
		t.Fatal("NewStore(nil, WithApprovalsEnforced(true)) returned nil")
	}

	// Zero options spread from a nil slice must be the one-argument form.
	var none []StoreOption
	if NewStore(nil, none...).approvalsEnforced != NewStore(nil).approvalsEnforced {
		t.Error("NewStore(nil, none...) and NewStore(nil) disagree on approvalsEnforced")
	}
}

// TestApprovalsEnforced_WrittenOnceReadNowhere is AC #6's inertness proof.
//
// It is a source assertion rather than a behavioural one, and honestly so: every
// method on Store reaches Postgres through s.pool or a caller-supplied tx (Create,
// Get, List, Transition, Edit, CallerRole, the Mark*Tx family, …), so there is no
// pool-free Store surface on which an ON store could be observed behaving like an
// OFF one. "The field is written once and read nowhere in production code" is the
// strongest claim testable at this subtask's boundary.
//
// An AST walk, not a grep, mirroring TestTransitionTx_DocCommentNamesEveryCaller:
// comments never parse into a SelectorExpr, so a doc comment naming the field
// cannot register as a read. APPR-08-03/04/05 are expected to delete this test
// when they add the first real read.
func TestApprovalsEnforced_WrittenOnceReadNowhere(t *testing.T) {
	const field = "approvalsEnforced"

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}

	fset := token.NewFileSet()
	decls, writes := 0, 0
	var reads []string

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		// Assignment targets first, so a write is not then counted as a read.
		assigned := map[*ast.SelectorExpr]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range as.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == field {
					assigned[sel] = true
					writes++
				}
			}
			return true
		})

		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Field:
				for _, name := range node.Names {
					if name.Name == field {
						decls++
					}
				}
			case *ast.SelectorExpr:
				if node.Sel.Name == field && !assigned[node] {
					reads = append(reads, fmt.Sprintf("%s:%d", path, fset.Position(node.Pos()).Line))
				}
			}
			return true
		})
	}

	if decls != 1 {
		t.Errorf("found %d declaration(s) of %s in internal/invoice's production files, want exactly 1", decls, field)
	}
	if writes != 1 {
		t.Errorf("found %d write site(s) of %s, want exactly 1 (WithApprovalsEnforced's closure) — a flag nothing writes is not plumbed", writes, field)
	}
	if len(reads) != 0 {
		t.Errorf("found %d read site(s) of %s: %v — this subtask is inert (AC #6); the first real read belongs to APPR-08-03/04/05", len(reads), field, reads)
	}
}
