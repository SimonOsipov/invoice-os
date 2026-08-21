// verdict_audit_adversarial_test.go: edge cases for the audit seam. Same whitebox package
// and same rollback-only harness as verdict_audit_test.go, whose helpers these reuse.
package submission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
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
		if err := recordVerdictAudit(ctx, tx, invoiceID, jobID, "accepted", irn); err != nil {
			return err
		}
		if err := recordVerdictAudit(ctx, tx, invoiceID, jobID, "rejected", ""); err != nil {
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
		if got := vaRawKeys(t, accepted.rawJSON); len(got) != 4 {
			t.Errorf("stored accepted payload keys = %v (%d), want 4: %s", got, len(got), accepted.rawJSON)
		}
		if !strings.Contains(accepted.rawJSON, irn) {
			t.Errorf("stored accepted payload %s does not carry the reference %q", accepted.rawJSON, irn)
		}

		if got := vaRawKeys(t, rejected.rawJSON); len(got) != 3 {
			t.Errorf("stored rejected payload keys = %v (%d), want 3: %s", got, len(got), rejected.rawJSON)
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
		if err := recordFailureAudit(ctx, tx, uuid.NewString(), uuid.NewString(), FailureNeverAcknowledged); err != nil {
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
		if err := recordFailureAudit(ctx, tx, "", "", FailurePayloadNotBuilt); err != nil {
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
		want := []string{"failure_kind", "invoice_id", "outcome", "submission_job_id"}
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
		if err := recordFailureAudit(ctx, tx, uuid.NewString(), uuid.NewString(), FailureAcknowledgedNoVerdict); err != nil {
			return err
		}
		if err := recordVerdictAudit(ctx, tx, uuid.NewString(), uuid.NewString(), "accepted", "IRN-VA-ADV-2"); err != nil {
			return err
		}
		if err := recordVerdictAudit(ctx, tx, uuid.NewString(), uuid.NewString(), "rejected", ""); err != nil {
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
// The floor is >= 2, not exact: PollWorker's failure site is wired too now, putting the real
// count at three -- SubmitWorker's two plus PollWorker's one. The last-statement property
// below is asserted over EVERY matched closure, so the floor costs the check no strength.
// Tightening it to an exact count belongs with the subtask that finishes the set.
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
	if matched < 2 {
		t.Fatalf("closures calling recordFailureAudit = %d, want >= 2 (found at %v) -- every "+
			"terminal-failure branch, SubmitWorker's and PollWorker's alike, must write the event", matched, sites)
	}
	for _, loc := range sites {
		if !strings.HasPrefix(loc, "worker.go:") {
			t.Errorf("recordFailureAudit is called from %s, want worker.go only -- the terminal "+
				"branches are the only callers", loc)
		}
	}
}
