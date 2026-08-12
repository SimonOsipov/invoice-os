// task-483 (APPR-06-07, Mode A / QA phase): FIX-4's adversarial pin -- an invoice can
// carry BOTH an approved run and an open run at once (approval_runs_one_open only
// constrains 'open'), plus a rejected one. CancelLiveRunTx must cancel BOTH live runs
// and write ONE invoice.approval_cancelled audit row PER cancelled run -- a
// tx.QueryRow(...).Scan(&runID) implementation cancels both but audits only one, and
// this is the only spec that catches it. Fails to compile today
// ("undefined: CancelLiveRunTx"), same reason as cancel_test.go.
package approval

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
)

// TestCancelLiveRun_CancelsEveryLiveRunAndAuditsEach (AC-1, FIX-4): one invoice seeded
// with three runs -- approved (closed 2020-01-01 by 'system'), open, rejected --
// CancelLiveRunTx cancels BOTH live runs, the approved one keeps its original
// closed_at/closed_by (the COALESCE), the rejected one is untouched, and exactly two
// invoice.approval_cancelled audit rows exist, one naming each cancelled run id.
func TestCancelLiveRun_CancelsEveryLiveRunAndAuditsEach(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06-07 cancel-every-live-run")
	entityID := seedBusinessEntity(t, super, tenantID, "Every Live Run Corp")
	policyID := seedApprovalPolicy(t, super, tenantID, "Every live run policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	invoiceID := seedInvoice(t, super, tenantID, entityID, "every-live-run-invoice")

	approvedRunID := seedApprovalRun(t, super, tenantID, invoiceID, versionID)
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET state = 'approved', closed_at = '2020-01-01T00:00:00Z'::timestamptz, closed_by = 'system' WHERE id = $1`,
		approvedRunID); err != nil {
		t.Fatalf("seed approved run: %v", err)
	}

	openRunID := seedApprovalRun(t, super, tenantID, invoiceID, versionID) // defaults to open

	rejectedRunID := seedApprovalRun(t, super, tenantID, invoiceID, versionID)
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET state = 'rejected', closed_at = now(), closed_by = 'reviewer-1' WHERE id = $1`,
		rejectedRunID); err != nil {
		t.Fatalf("seed rejected run: %v", err)
	}

	got, err := cancel(t, app, tenantID, invoiceID, "adversarial-test-actor")
	if err != nil {
		t.Fatalf("CancelLiveRunTx: %v (want nil)", err)
	}
	if !got {
		t.Fatal("CancelLiveRunTx returned false, want true -- two live runs exist")
	}

	// Both live runs are cancelled.
	var approvedState, approvedClosedBy, approvedClosedAt string
	if err := super.QueryRow(context.Background(),
		`SELECT state, closed_by, closed_at::text FROM approval_runs WHERE id = $1`, approvedRunID,
	).Scan(&approvedState, &approvedClosedBy, &approvedClosedAt); err != nil {
		t.Fatalf("read back the approved run: %v", err)
	}
	if approvedState != "cancelled" {
		t.Errorf("approved run state after cancel = %q, want %q", approvedState, "cancelled")
	}
	if approvedClosedBy != "system" {
		t.Errorf("approved run closed_by after cancel = %q, want unchanged %q (COALESCE)", approvedClosedBy, "system")
	}
	if len(approvedClosedAt) < 10 || approvedClosedAt[:10] != "2020-01-01" {
		t.Errorf("approved run closed_at after cancel = %q, want unchanged (starts with 2020-01-01)", approvedClosedAt)
	}

	var openState, openClosedBy string
	if err := super.QueryRow(context.Background(),
		`SELECT state, closed_by FROM approval_runs WHERE id = $1`, openRunID,
	).Scan(&openState, &openClosedBy); err != nil {
		t.Fatalf("read back the open run: %v", err)
	}
	if openState != "cancelled" {
		t.Errorf("open run state after cancel = %q, want %q", openState, "cancelled")
	}
	if openClosedBy != "adversarial-test-actor" {
		t.Errorf("open run closed_by after cancel = %q, want %q", openClosedBy, "adversarial-test-actor")
	}

	// The rejected run is untouched.
	var rejectedState, rejectedClosedBy string
	if err := super.QueryRow(context.Background(),
		`SELECT state, closed_by FROM approval_runs WHERE id = $1`, rejectedRunID,
	).Scan(&rejectedState, &rejectedClosedBy); err != nil {
		t.Fatalf("read back the rejected run: %v", err)
	}
	if rejectedState != "rejected" {
		t.Errorf("rejected run state after cancel = %q, want unchanged %q", rejectedState, "rejected")
	}
	if rejectedClosedBy != "reviewer-1" {
		t.Errorf("rejected run closed_by after cancel = %q, want unchanged %q", rejectedClosedBy, "reviewer-1")
	}

	// Exactly TWO invoice.approval_cancelled audit rows, one naming each cancelled run
	// id -- the FIX-4 guard: a tx.QueryRow(...).Scan(&runID) implementation cancels
	// both runs above but reads (and so audits) only ONE RETURNING row.
	if n := auditCount(t, super, tenantID, "invoice.approval_cancelled"); n != 2 {
		t.Fatalf("invoice.approval_cancelled audit rows = %d, want exactly 2", n)
	}
	rows, err := super.Query(context.Background(),
		`SELECT payload FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.approval_cancelled' ORDER BY id`,
		tenantID)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	var gotRunIDs []string
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan audit_log payload: %v", err)
		}
		var payload approvalCancelledPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("unmarshal invoice.approval_cancelled payload %s: %v", raw, err)
		}
		if payload.ID != invoiceID {
			t.Errorf("audit payload id = %q, want %q", payload.ID, invoiceID)
		}
		gotRunIDs = append(gotRunIDs, payload.RunID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log: %v", err)
	}
	sort.Strings(gotRunIDs)
	wantRunIDs := []string{approvedRunID, openRunID}
	sort.Strings(wantRunIDs)
	if len(gotRunIDs) != 2 || gotRunIDs[0] != wantRunIDs[0] || gotRunIDs[1] != wantRunIDs[1] {
		t.Errorf("audit payload run_ids = %v, want one naming each cancelled run %v", gotRunIDs, wantRunIDs)
	}
}
