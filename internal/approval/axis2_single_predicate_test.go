package approval

// task-538 (APPR-16-06): decideTx (decision.go) held a second, byte-identical copy of
// AXIS 2's role-holder EXISTS query that HeldRoleKeysTx (gate.go) already owns. This scan
// proves the copy is gone. "Exactly one copy" cannot be the assertion (D-23): after the
// refactor the surviving copy is HeldRoleKeysTx's plain SELECT, not an EXISTS -- so the
// right count is ZERO EXISTS predicates naming workflow_role_members, with a non-vacuity
// control proving the scanner reads real SQL and is not silently matching nothing.
//
// Mutation-oracle note: of AXIS 2's three clauses, wr.deleted_at IS NULL and
// wrm.user_id = $2 are each caught by a refusal test (TestApprove_HolderOfASoftDeletedRoleRefused,
// decision_test.go:365; TestApprove_OtherHolderStaffedInRoleDoesNotAuthorizeUnstaffedCaller,
// decision_adversarial_test.go:195). m.role IN ('admin','reviewer') is an equivalent
// mutant: requireApprover (AXIS 1, decision.go:103-116) runs first, unconditionally, with
// a logically identical predicate on the same memberships row, so no decideTx refusal test
// can observe a break there.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// axis2SQLLiteral is one backtick-quoted (raw) string literal found in a non-test .go
// file under internal/approval, tagged with its source location.
type axis2SQLLiteral struct {
	loc  string
	text string
}

// axis2ScanApprovalPackage extracts every backtick-quoted string literal from
// internal/approval's non-test .go files via the AST -- not a raw substring match, since
// 12 non-test lines name workflow_role_members and 11 of those are comments, plain-SELECT
// joins, DELETE/INSERT statements or index names, not EXISTS predicates.
func axis2ScanApprovalPackage(t *testing.T) []axis2SQLLiteral {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "approval")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var out []axis2SQLLiteral
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, "`") {
				return true
			}
			pos := fset.Position(lit.Pos())
			out = append(out, axis2SQLLiteral{
				loc:  fmt.Sprintf("%s:%d", name, pos.Line),
				text: strings.Trim(lit.Value, "`"),
			})
			return true
		})
	}
	return out
}

// TestAxis2_ScanControlTheScannerSeesRealSQL is the non-vacuity control for
// TestAxis2_ZeroWorkflowRoleMembersExistsPredicatesRemain: without it, a renamed file,
// an empty directory, or a broken AST walk would make that test's zero pass vacuously
// instead of proving anything.
func TestAxis2_ScanControlTheScannerSeesRealSQL(t *testing.T) {
	lits := axis2ScanApprovalPackage(t)

	var existsCount, wrmCount int
	for _, l := range lits {
		lower := strings.ToLower(l.text)
		if strings.Contains(lower, "exists") {
			existsCount++
		}
		if strings.Contains(lower, "workflow_role_members") {
			wrmCount++
		}
	}
	if existsCount < 5 {
		t.Fatalf("backtick literals containing EXISTS = %d, want >= 5 -- the scanner is not reading real SQL, so the zero-match assertion below would be a silent parse failure, not a true zero", existsCount)
	}
	if wrmCount < 1 {
		t.Fatalf("backtick literals containing workflow_role_members = %d, want >= 1 -- the scanner cannot see the table name it must rule out", wrmCount)
	}
}

// TestAxis2_ZeroWorkflowRoleMembersExistsPredicatesRemain (AC-1/AC-4): decideTx's own
// EXISTS-wrapped copy of AXIS 2 must be gone; HeldRoleKeysTx's surviving plain SELECT
// still names workflow_role_members but is not an EXISTS, so it does not match.
func TestAxis2_ZeroWorkflowRoleMembersExistsPredicatesRemain(t *testing.T) {
	lits := axis2ScanApprovalPackage(t)

	var both []string
	for _, l := range lits {
		lower := strings.ToLower(l.text)
		if strings.Contains(lower, "exists") && strings.Contains(lower, "workflow_role_members") {
			both = append(both, l.loc)
		}
	}
	if len(both) != 0 {
		t.Errorf("backtick SQL literals naming both EXISTS and workflow_role_members = %d, want 0: %v -- decideTx must call HeldRoleKeysTx instead of keeping its own EXISTS-wrapped copy (D-12/D-23)", len(both), both)
	}
}
