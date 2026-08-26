// verdict_audit_adversarial_test.go: edge cases for the audit seam. Same whitebox package
// and same rollback-only harness as verdict_audit_test.go, whose helpers these reuse.
package submission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// vaRawRow is one audit_log row read back as stored, before any Go decoding.
type vaRawRow struct {
	event    string
	rawJSON  string
	entityID *string
}

func vaReadRawRows(ctx context.Context, tx pgx.Tx) ([]vaRawRow, error) {
	rows, err := tx.Query(ctx, `SELECT event, payload::text, entity_id::text FROM audit_log ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []vaRawRow
	for rows.Next() {
		var r vaRawRow
		if err := rows.Scan(&r.event, &r.rawJSON, &r.entityID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// vaRawKeys decodes the stored JSON text into its key list. An empty-string value still
// yields its key, which is what separates absent from present-and-empty.
func vaRawKeys(t *testing.T, raw string) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal stored payload %q: %v", raw, err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// vaRequireRollback ends every case in this file: the sentinel is what rolls the
// transaction back, so a case that commits fails here instead of leaving a row.
func vaRequireRollback(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errVARollback) {
		t.Fatalf("WithinTenantTx returned %v, want the rollback sentinel -- the case must commit nothing", err)
	}
}

// TestRecordVerdictAudit_RejectedStoredJSONOmitsReference reads audit_log.payload as text,
// so a reference stored as "" cannot pass as absent the way a decoded nil would.
func TestRecordVerdictAudit_RejectedStoredJSONOmitsReference(t *testing.T) {
	invoiceID, jobID := uuid.NewString(), uuid.NewString()
	const irn = "IRN-VA-ADV-1"

	pool := vaRequireAppPool(t)
	ctx := context.Background()
	vaRequireRollback(t, db.WithinTenantTx(ctx, pool, uuid.NewString(), func(tx pgx.Tx) error {
		if err := recordVerdictAudit(ctx, tx, invoiceID, jobID, "accepted", irn, vaInvoiceNumber); err != nil {
			return err
		}
		if err := recordVerdictAudit(ctx, tx, invoiceID, jobID, "rejected", "", vaInvoiceNumber); err != nil {
			return err
		}
		rows, err := vaReadRawRows(ctx, tx)
		if err != nil {
			return err
		}
		if len(rows) != 2 {
			t.Fatalf("rows visible under this tenant = %d, want 2 -- the assertions below would read nothing", len(rows))
		}

		accepted, rejected := rows[0], rows[1]
		if got := vaRawKeys(t, accepted.rawJSON); len(got) != 5 {
			t.Errorf("stored accepted payload keys = %v (%d), want 5: %s", got, len(got), accepted.rawJSON)
		}
		if !strings.Contains(accepted.rawJSON, irn) {
			t.Errorf("stored accepted payload %s does not carry the reference %q", accepted.rawJSON, irn)
		}

		if got := vaRawKeys(t, rejected.rawJSON); len(got) != 4 {
			t.Errorf("stored rejected payload keys = %v (%d), want 4: %s", got, len(got), rejected.rawJSON)
		}
		if strings.Contains(rejected.rawJSON, "reference") {
			t.Errorf("stored rejected payload %s spells reference at all, want the key left out", rejected.rawJSON)
		}
		return errVARollback
	}))
}

// TestRecordFailureAudit_IsScopedToTheWritingTenant flips app.current_tenant inside the
// same transaction, so only the isolation policy can hide the row -- reading an
// uncommitted write from a second connection would be hidden by MVCC whatever RLS said.
func TestRecordFailureAudit_IsScopedToTheWritingTenant(t *testing.T) {
	pool := vaRequireAppPool(t)
	ctx := context.Background()
	tenantA, tenantB := uuid.NewString(), uuid.NewString()

	vaRequireRollback(t, db.WithinTenantTx(ctx, pool, tenantA, func(tx pgx.Tx) error {
		if err := recordFailureAudit(ctx, tx, uuid.NewString(), uuid.NewString(), FailureNeverAcknowledged, vaInvoiceNumber); err != nil {
			return err
		}
		setTenant := func(id string) error {
			_, e := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", id)
			return e
		}
		count := func() (int, error) {
			var n int
			e := tx.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
			return n, e
		}

		nA, err := count()
		if err != nil {
			return err
		}
		if nA != 1 {
			t.Fatalf("the writing tenant sees %d rows, want 1 -- the cross-tenant assertion below would pass on no row at all", nA)
		}

		if err := setTenant(tenantB); err != nil {
			return err
		}
		nB, err := count()
		if err != nil {
			return err
		}
		if nB != 0 {
			t.Errorf("a second tenant sees %d of the failure rows, want 0", nB)
		}

		if err := setTenant(tenantA); err != nil {
			return err
		}
		again, err := count()
		if err != nil {
			return err
		}
		if again != 1 {
			t.Errorf("the writing tenant sees %d rows after the switch back, want 1 -- the zero above must come from the policy, not from a lost row", again)
		}
		return errVARollback
	}))
}

// TestRecordFailureAudit_EmptyIdentifiersDoNotMisattribute pins the seam against
// audit_log_entity_for: a blank invoice_id resolves to a NULL entity_id, which the read
// contract spells firm-wide, rather than raising 22P02 or landing on another company.
func TestRecordFailureAudit_EmptyIdentifiersDoNotMisattribute(t *testing.T) {
	pool := vaRequireAppPool(t)
	ctx := context.Background()

	vaRequireRollback(t, db.WithinTenantTx(ctx, pool, uuid.NewString(), func(tx pgx.Tx) error {
		if err := recordFailureAudit(ctx, tx, "", "", FailurePayloadNotBuilt, vaInvoiceNumber); err != nil {
			return err
		}
		rows, err := vaReadRawRows(ctx, tx)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("rows visible under this tenant = %d, want 1 -- the assertions below would read nothing", len(rows))
		}
		r := rows[0]
		if r.event != "submission.failed" {
			t.Errorf("event = %q, want %q", r.event, "submission.failed")
		}
		if r.entityID != nil {
			t.Errorf("entity_id = %q, want NULL -- a blank invoice_id must attribute to no company", *r.entityID)
		}
		want := []string{"failure_kind", "invoice_id", "invoice_number", "outcome", "submission_job_id"}
		got := vaRawKeys(t, r.rawJSON)
		if len(got) != len(want) || !vaSameSet(got, want) {
			t.Fatalf("stored payload keys = %v, want exactly %v", got, want)
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(r.rawJSON), &m); err != nil {
			return err
		}
		if m["invoice_id"] != "" || m["submission_job_id"] != "" {
			t.Errorf("stored ids = %q / %q, want both blank -- the seam must echo what the caller passed, not invent one",
				m["invoice_id"], m["submission_job_id"])
		}
		return errVARollback
	}))
}

// TestSubmissionAudit_ActorAndEventFitTheAuditLogChecks measures the two audit_log CHECK
// bounds against every value this seam writes, so a longer event name cannot reach the
// table only to be refused there.
func TestSubmissionAudit_ActorAndEventFitTheAuditLogChecks(t *testing.T) {
	pool := vaRequireAppPool(t)
	ctx := context.Background()

	vaRequireRollback(t, db.WithinTenantTx(ctx, pool, uuid.NewString(), func(tx pgx.Tx) error {
		if err := recordFailureAudit(ctx, tx, uuid.NewString(), uuid.NewString(), FailureAcknowledgedNoVerdict, vaInvoiceNumber); err != nil {
			return err
		}
		if err := recordVerdictAudit(ctx, tx, uuid.NewString(), uuid.NewString(), "accepted", "IRN-VA-ADV-2", vaInvoiceNumber); err != nil {
			return err
		}
		if err := recordVerdictAudit(ctx, tx, uuid.NewString(), uuid.NewString(), "rejected", "", vaInvoiceNumber); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT actor, event, char_length(actor), char_length(event) FROM audit_log ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var seen int
		for rows.Next() {
			var actor, event string
			var la, le int
			if err := rows.Scan(&actor, &event, &la, &le); err != nil {
				return err
			}
			seen++
			if la <= 0 || la > 255 {
				t.Errorf("actor %q length %d is outside the audit_actor_length bound (0, 255]", actor, la)
			}
			if le <= 0 || le >= 128 {
				t.Errorf("event %q length %d is outside the audit_event_length bound (0, 128)", event, le)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if seen != 3 {
			t.Errorf("measured %d rows, want 3 -- the bounds above were not checked against every event this seam writes", seen)
		}
		return errVARollback
	}))
}

// --- the failure audit write is its closure's last statement ----------------------------

// vaDirectCalls returns every CallExpr under body whose callee spells want, WITHOUT
// descending into a nested *ast.FuncLit -- a call belongs to its innermost closure. Naive
// containment would also attribute the OncePerJob closure's call to the WithinTenantTx
// closure wrapping it, whose own last statement is a plain `return err`.
func vaDirectCalls(body *ast.BlockStmt, want string) []*ast.CallExpr {
	var out []*ast.CallExpr
	visit := func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if c, ok := n.(*ast.CallExpr); ok && vaCalleeName(c.Fun) == want {
			out = append(out, c)
		}
		return true
	}
	for _, st := range body.List {
		ast.Inspect(st, visit)
	}
	return out
}

// The audit row must be written by the LAST statement of the closure that writes it, so
// queue.OncePerJob's exactly-once guarantee covers the row exactly as it covers the job
// state and the invoice transition. A statement placed after it would commit outside that
// guarantee's reach; an early return placed after it could skip the row entirely on a path
// that still lands the invoice in failed.
//
// The set is closed at exactly three: SubmitWorker's two failure sites plus PollWorker's
// one. A fourth call site is a deliberate decision -- a new terminal-failure branch -- not
// an accident this test should wave through.
func TestSubmissionAudit_FailureWriteIsLastInItsClosure(t *testing.T) {
	files := vaScanSubmissionPackage(t)
	vaRequirePopulation(t, files)

	var scanned, matched int
	var sites []string
	for _, f := range files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if !ok || lit.Body == nil {
				return true
			}
			scanned++
			calls := vaDirectCalls(lit.Body, "recordFailureAudit")
			if len(calls) == 0 {
				return true
			}
			matched++
			loc := fmt.Sprintf("%s:%d", f.name, f.fset.Position(lit.Pos()).Line)
			sites = append(sites, loc)
			if len(calls) != 1 {
				t.Errorf("closure at %s calls recordFailureAudit %d times, want exactly 1 -- "+
					"one terminal failure is one event", loc, len(calls))
				return true
			}
			if len(lit.Body.List) == 0 {
				t.Errorf("closure at %s has an empty body yet matched a call -- the walk is wrong", loc)
				return true
			}
			last := lit.Body.List[len(lit.Body.List)-1]
			ret, isReturn := last.(*ast.ReturnStmt)
			if !isReturn {
				t.Errorf("closure at %s ends in %T, want its recordFailureAudit call to be the "+
					"final return -- anything after it commits outside OncePerJob's reach", loc, last)
				return true
			}
			if len(ret.Results) != 1 || ret.Results[0] != calls[0] {
				t.Errorf("closure at %s ends in a return that is not its recordFailureAudit call "+
					"(returns %d expression(s)) -- the audit write must be the last statement",
					loc, len(ret.Results))
			}
			return true
		})
	}

	// Control: a broken walk would find no closures at all and report every claim above as
	// vacuously satisfied. 15 function literals in the package today.
	if scanned < 10 {
		t.Fatalf("walked %d function literals across %d files, want >= 10 -- the walk is broken, "+
			"so the counts below are vacuous", scanned, len(files))
	}
	if matched != 3 {
		t.Fatalf("closures calling recordFailureAudit = %d, want exactly 3 (found at %v) -- the "+
			"set is closed; a fourth site is a deliberate decision, not an accident this test "+
			"should wave through", matched, sites)
	}
	for _, loc := range sites {
		if !strings.HasPrefix(loc, "worker.go:") {
			t.Errorf("recordFailureAudit is called from %s, want worker.go only -- the terminal "+
				"branches are the only callers", loc)
		}
	}
}

// --- every recordFailureAudit kind argument resolves to a declared constant --------------

// vaParseFixture parses src as a standalone file, giving the helpers below a synthetic
// package to run over without touching worker.go.
func vaParseFixture(t *testing.T, name, src string) vaFile {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return vaFile{name: name, fset: fset, ast: f}
}

// vaFailureKindConstants collects every constant name declared with type FailureKind.
// failure.go spells the type on EACH const spec (no iota carryover), so checking vs.Type
// per spec, not the block header, is what finds all three.
func vaFailureKindConstants(files []vaFile) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		for _, d := range f.ast.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				id, ok := vs.Type.(*ast.Ident)
				if !ok || id.Name != "FailureKind" {
					continue
				}
				for _, n := range vs.Names {
					out[n.Name] = true
				}
			}
		}
	}
	return out
}

// vaDirectAssigns returns the RHS of every `name := ...` / `name = ...` assignment directly
// in body, WITHOUT descending into a nested *ast.FuncLit -- mirrors vaDirectCalls's
// closure-scoping rule above: an assignment belongs to its innermost closure.
func vaDirectAssigns(body *ast.BlockStmt, name string) []ast.Expr {
	var out []ast.Expr
	visit := func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
				out = append(out, as.Rhs[i])
			}
		}
		return true
	}
	for _, st := range body.List {
		ast.Inspect(st, visit)
	}
	return out
}

// vaFailureKindArg classifies one recordFailureAudit call's kind argument: a declared
// constant, a local alias resolved through exactly one assignment to one, or a violation.
// Anything not an *ast.Ident is flagged directly -- only a name can be locally reassigned.
func vaFailureKindArg(body *ast.BlockStmt, arg ast.Expr, constSet map[string]bool) (ok bool, reason string) {
	id, isIdent := arg.(*ast.Ident)
	if !isIdent {
		return false, fmt.Sprintf("%T argument, want a FailureKind constant or a local alias of one", arg)
	}
	if constSet[id.Name] {
		return true, ""
	}
	assigns := vaDirectAssigns(body, id.Name)
	if len(assigns) != 1 {
		return false, fmt.Sprintf("%d assignment(s) to %q in this closure, want exactly 1 to resolve it", len(assigns), id.Name)
	}
	rhs, ok2 := assigns[0].(*ast.Ident)
	if !ok2 || !constSet[rhs.Name] {
		return false, fmt.Sprintf("%q resolves to %T, not a FailureKind constant", id.Name, assigns[0])
	}
	return true, ""
}

// vaFailureKindViolations walks f for closures calling recordFailureAudit and classifies
// each call's kind argument (args[4]) against constSet. Shared by the production scan below
// and its control-needle subtest, so both run the identical classifier.
func vaFailureKindViolations(f vaFile, constSet map[string]bool) (violations []string, closures int) {
	ast.Inspect(f.ast, func(n ast.Node) bool {
		lit, isLit := n.(*ast.FuncLit)
		if !isLit || lit.Body == nil {
			return true
		}
		calls := vaDirectCalls(lit.Body, "recordFailureAudit")
		if len(calls) == 0 {
			return true
		}
		closures++
		loc := fmt.Sprintf("%s:%d", f.name, f.fset.Position(lit.Pos()).Line)
		for _, c := range calls {
			if len(c.Args) < 5 {
				violations = append(violations, fmt.Sprintf("%s: recordFailureAudit called with %d args, want >= 5", loc, len(c.Args)))
				continue
			}
			if ok, reason := vaFailureKindArg(lit.Body, c.Args[4], constSet); !ok {
				violations = append(violations, fmt.Sprintf("%s: %s", loc, reason))
			}
		}
		return true
	})
	return violations, closures
}

// TestSubmissionAudit_FailureKindArgumentIsAConstant pins the failure_kind vocabulary shut:
// every recordFailureAudit call must pass a declared FailureKind constant, directly or
// through one local alias, never a literal, conversion, or computed expression that could
// drift from invoices.failure_kind's CHECK IN-list.
func TestSubmissionAudit_FailureKindArgumentIsAConstant(t *testing.T) {
	files := vaScanSubmissionPackage(t)
	vaRequirePopulation(t, files)

	constSet := vaFailureKindConstants(files)
	if len(constSet) != 3 {
		t.Fatalf("FailureKind constants found = %d, want exactly 3 -- the const walk is broken, "+
			"so the classification below is meaningless", len(constSet))
	}

	var violations []string
	var closures int
	for _, f := range files {
		v, c := vaFailureKindViolations(f, constSet)
		violations = append(violations, v...)
		closures += c
	}
	if closures != 3 {
		t.Fatalf("closures calling recordFailureAudit = %d, want exactly 3 -- the call-site set "+
			"moved without updating this test's floor", closures)
	}
	for _, v := range violations {
		t.Errorf("%s", v)
	}

	// Control: prove the classifier can actually flag a violation, not just clear real code.
	// A raw string parsed by go/parser, never compiled into this package or written to
	// disk, so vaScanSubmissionPackage's on-disk walk above can never pick it up itself.
	t.Run("control_needle", func(t *testing.T) {
		src := `package submission

const FailurePayloadNotBuilt FailureKind = "payload_not_built"

func fixture() {
	_ = func() error {
		kind := FailureKind(sourceKind)
		return recordFailureAudit(ctx, tx, invoiceID, jobID, kind)
	}
	_ = func() error {
		return recordFailureAudit(ctx, tx, invoiceID, jobID, FailureKind("payload_not_built"))
	}
	_ = func() error {
		kind := FailurePayloadNotBuilt
		return recordFailureAudit(ctx, tx, invoiceID, jobID, kind)
	}
}
`
		f := vaParseFixture(t, "fixture.go", src)
		fixtureConsts := vaFailureKindConstants([]vaFile{f})
		got, closures := vaFailureKindViolations(f, fixtureConsts)
		if closures != 3 {
			t.Fatalf("fixture matched %d closures, want exactly 3 -- the fixture itself is malformed", closures)
		}
		if len(got) != 2 {
			t.Fatalf("fixture produced %d violations, want exactly 2 (FailureKind(sourceKind) and "+
				"the bare literal) -- a checker that only ever clears catches nothing: %v", len(got), got)
		}
	})
}
