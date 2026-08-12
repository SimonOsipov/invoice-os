// task-483 (APPR-06-07, Mode A): RED specs for approval.CancelLiveRunTx -- not yet
// written, so every call below fails to compile ("undefined: CancelLiveRunTx"), the
// honest RED for a missing function (mirrors arm_test.go's own task-480 RED). DB-backed,
// so it lives here rather than engine_test.go, whose header asserts it is a pure test
// that never calls dbTestPools and so cannot skip.
//
// Reuses policyTenant/seedBusinessEntity/seedInvoice/seedApprovalPolicy/
// seedApprovalPolicyVersionN/auditCount/pgCode (policy_crud_test.go /
// schema_constraints_test.go / workflow_roles_test.go, same package).
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate
// step fails the build on any skip.
package approval

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// approvalCancelledPayload is invoice.approval_cancelled's payload shape.
type approvalCancelledPayload struct {
	ID    string `json:"id"`
	RunID string `json:"run_id"`
}

// cancel runs CancelLiveRunTx inside a fresh tenant-scoped transaction -- mirrors
// arm_test.go's own arm() helper. Every spec reads the committed (or rolled-back)
// result back through the superuser pool.
func cancel(t *testing.T, pool *pgxpool.Pool, tenantID, invoiceID, actor string) (bool, error) {
	t.Helper()
	var cancelled bool
	err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		cancelled, err = CancelLiveRunTx(context.Background(), tx, invoiceID, actor)
		return err
	})
	return cancelled, err
}

// closeApprovalRunFor force-closes a run directly, for fixtures that need one already
// closed BEFORE the call under test runs -- bypasses both ArmTx's own closure branch
// and CancelLiveRunTx, each under test elsewhere.
func closeApprovalRunFor(t *testing.T, super *pgxpool.Pool, runID, state, closedBy string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET state = $1, closed_at = now(), closed_by = $2 WHERE id = $3`,
		state, closedBy, runID)
	if err != nil {
		t.Fatalf("close approval_runs %s: %v", runID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("close approval_runs %s affected %d rows, want 1", runID, tag.RowsAffected())
	}
}

// TestCancelLiveRun_NoLiveRunIsSilent (AC-3, AC-5): three invoices whose run is
// missing, already cancelled, or rejected -- CancelLiveRunTx returns false and writes
// no audit row for all three; the rejected run's own row stays byte-identical (a
// refusal must never be rewritten as a cancellation). Positive control: a fourth
// invoice with an open run returns true and IS cancelled -- without it, "returns
// false" could vacuously pass against a CancelLiveRunTx that never cancels anything.
func TestCancelLiveRun_NoLiveRunIsSilent(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06-07 cancel-no-live-run")
	entityID := seedBusinessEntity(t, super, tenantID, "No Live Run Corp")
	policyID := seedApprovalPolicy(t, super, tenantID, "No live run policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	noRunInvoice := seedInvoice(t, super, tenantID, entityID, "no-run-invoice")

	cancelledInvoice := seedInvoice(t, super, tenantID, entityID, "already-cancelled-invoice")
	cancelledRunID := seedApprovalRun(t, super, tenantID, cancelledInvoice, versionID)
	closeApprovalRunFor(t, super, cancelledRunID, "cancelled", "someone-else")

	rejectedInvoice := seedInvoice(t, super, tenantID, entityID, "rejected-invoice")
	rejectedRunID := seedApprovalRun(t, super, tenantID, rejectedInvoice, versionID)
	closeApprovalRunFor(t, super, rejectedRunID, "rejected", "reviewer-1")

	openInvoice := seedInvoice(t, super, tenantID, entityID, "open-invoice") // positive control
	openRunID := seedApprovalRun(t, super, tenantID, openInvoice, versionID) // defaults to open

	for _, tc := range []struct {
		name          string
		invoiceID     string
		wantCancelled bool
	}{
		{"no run at all", noRunInvoice, false},
		{"already cancelled", cancelledInvoice, false},
		{"rejected", rejectedInvoice, false},
		{"positive control: open run", openInvoice, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforeAudit := auditCount(t, super, tenantID, "invoice.approval_cancelled")

			got, err := cancel(t, app, tenantID, tc.invoiceID, "cancel-test-actor")
			if err != nil {
				t.Fatalf("CancelLiveRunTx: %v (want nil)", err)
			}
			if got != tc.wantCancelled {
				t.Errorf("CancelLiveRunTx returned %v, want %v", got, tc.wantCancelled)
			}

			wantAudit := beforeAudit
			if tc.wantCancelled {
				wantAudit++
			}
			if n := auditCount(t, super, tenantID, "invoice.approval_cancelled"); n != wantAudit {
				t.Errorf("invoice.approval_cancelled audit rows = %d, want %d", n, wantAudit)
			}
		})
	}

	// The rejected run's own row must be byte-identical to what closeApprovalRunFor
	// seeded -- a refusal must never be rewritten as a cancellation.
	var rejectedState, rejectedClosedBy string
	if err := super.QueryRow(context.Background(),
		`SELECT state, closed_by FROM approval_runs WHERE id = $1`, rejectedRunID,
	).Scan(&rejectedState, &rejectedClosedBy); err != nil {
		t.Fatalf("read back the rejected run: %v", err)
	}
	if rejectedState != "rejected" {
		t.Errorf("rejected run state after CancelLiveRunTx = %q, want unchanged %q", rejectedState, "rejected")
	}
	if rejectedClosedBy != "reviewer-1" {
		t.Errorf("rejected run closed_by after CancelLiveRunTx = %q, want unchanged %q", rejectedClosedBy, "reviewer-1")
	}

	// The positive control DID get cancelled, closed_by the caller passed above.
	var openState, openClosedBy string
	if err := super.QueryRow(context.Background(),
		`SELECT state, closed_by FROM approval_runs WHERE id = $1`, openRunID,
	).Scan(&openState, &openClosedBy); err != nil {
		t.Fatalf("read back the positive-control run: %v", err)
	}
	if openState != "cancelled" {
		t.Errorf("positive-control run state = %q, want %q", openState, "cancelled")
	}
	if openClosedBy != "cancel-test-actor" {
		t.Errorf("positive-control run closed_by = %q, want %q", openClosedBy, "cancel-test-actor")
	}
}
