// task-490 (APPR-07-05, Mode A): the composition test that proves reject demotes
// through the REAL transitionTx edge. internal/approval cannot import internal/invoice
// ("import cycle"), so internal/approval's own reject specs can only prove a stub
// Demoter was called; this file exercises the REAL invoice.DemoteApprovalRejectedTx,
// mirroring publish_sweep_fingerprint_test.go's identical cross-package problem for
// the Fingerprinter.
package invoice

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedWorkflowRoleFor/staffWorkflowRoleFor port internal/approval's own test helpers
// (workflow_roles_test.go) -- unreachable from this package, same reason
// apply_validation_arming_test.go's header gives for seedApprovalPolicyFor et al.

func seedWorkflowRoleFor(t *testing.T, super *pgxpool.Pool, tenantID, key, title string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, key, title).Scan(&id); err != nil {
		t.Fatalf("seed workflow role %q: %v", key, err)
	}
	return id
}

func staffWorkflowRoleFor(t *testing.T, super *pgxpool.Pool, tenantID, roleID, userID string, ord int) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, $4)`,
		tenantID, roleID, userID, ord); err != nil {
		t.Fatalf("staff role %s with %s at ord %d: %v", roleID, userID, ord, err)
	}
}

// TestReject_DemotesThroughTheRealTransitionEdge (task-490 AC-8): a validated invoice
// with an open run, rejected via the real approval.Store wired with
// invoice.DemoteApprovalRejectedTx -> invoice draft, exactly ONE new
// invoice_status_history row (validated->draft), actor = the rejecting subject, never
// "system". Fails today: decideTx's rejected branch is still a stub (no close, no
// demotion, no audit), so the invoice never leaves validated.
func TestReject_DemotesThroughTheRealTransitionEdge(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-07-05 real-edge tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-07-05 real-edge entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "reject-real-edge-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ruleSetVersionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)
	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID, fp); err != nil {
		t.Fatalf("ApplyValidation (clean, no active policy yet): %v, want success", err)
	}

	adminCtx := seedActiveAdminFor(t, super, tenantID)
	roleKey := "real-edge-role"
	roleID := seedWorkflowRoleFor(t, super, tenantID, roleKey, "Real Edge Role") // must exist before sealing references it
	policyID := seedApprovalPolicyFor(t, super, tenantID, "APPR-07-05 real-edge policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	seedApprovalStepFor(t, super, tenantID, versionID, approvalStepSpecFor{
		Ord: 0, Kind: "approval", WorkflowRoleKey: &roleKey,
	})
	// PublishPolicy itself seals+activates the draft version and sweeps -- calling
	// activateApprovalPolicyVersionFor first would leave nothing for it to publish
	// (mirrors TestPublish_SweepFingerprintMatchesInvoiceContent's fixture shape).

	if _, err := approval.NewStore(app, FingerprintTx, DemoteApprovalRejectedTx).PublishPolicy(adminCtx, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	reviewerID := uuid.NewString()
	seedMembership(t, super, tenantID, reviewerID, "reviewer")
	staffWorkflowRoleFor(t, super, tenantID, roleID, reviewerID, 0)
	reviewerCtx := auth.WithIdentity(context.Background(), auth.Identity{Subject: reviewerID, Role: "authenticated", TenantID: tenantID})

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	reason := "wrong VAT"
	if _, err := approval.NewStore(app, FingerprintTx, DemoteApprovalRejectedTx).Decide(reviewerCtx, inv.ID, "rejected", &reason); err != nil {
		t.Fatalf("Decide(rejected): %v, want success", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("invoice status after reject = %q, want draft", status)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory+1 {
		t.Errorf("invoice_status_history rows = %d, want exactly %d (one new row)", n, beforeHistory+1)
	}

	var fromStatus, toStatus, actor string
	if err := super.QueryRow(ctx,
		`SELECT from_status, to_status, actor FROM invoice_status_history
		  WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`, inv.ID,
	).Scan(&fromStatus, &toStatus, &actor); err != nil {
		t.Fatalf("read back invoice_status_history: %v", err)
	}
	if fromStatus != "validated" || toStatus != "draft" {
		t.Errorf("history row = {from:%q to:%q}, want {from:validated to:draft}", fromStatus, toStatus)
	}
	if actor != reviewerID {
		t.Errorf("history actor = %q, want the rejecting subject %q, never system", actor, reviewerID)
	}
}
