// task-491 (APPR-07-06, Mode B / QA phase): the HTTP-wired twin of
// TestReject_DemotesThroughTheRealTransitionEdge (reject_demotion_test.go). That test
// calls approval.Store.Decide directly; nothing anywhere drives a rejection through the
// actual production route -- POST /v1/invoices/{id}/approvals, approval.DecideHandler,
// approval.Store.DecideSeam, invoice.DemoteApprovalRejectedTx -- end to end. This file
// is that missing proof.
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestDecideHandler_RejectOverHTTPDemotesThroughTheRealTransitionEdge: a validated
// invoice with an open run, rejected via a real http.ServeMux carrying the exact
// production route wired to the real approval.Store (real FingerprintTx, real
// DemoteApprovalRejectedTx) -> invoice draft, exactly ONE new invoice_status_history
// row (validated->draft), actor = the rejecting subject, never "system". Same fixture
// as TestReject_DemotesThroughTheRealTransitionEdge, but decided over HTTP instead of a
// direct Decide() call.
func TestDecideHandler_RejectOverHTTPDemotesThroughTheRealTransitionEdge(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "APPR-07-06 http-real-edge tenant")
	entityID := seedEntity(t, super, tenantID, "APPR-07-06 http-real-edge entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	store := NewStore(app)
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "reject-http-real-edge-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ruleSetVersionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)
	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID, fp); err != nil {
		t.Fatalf("ApplyValidation (clean, no active policy yet): %v, want success", err)
	}

	adminCtx := seedActiveAdminFor(t, super, tenantID)
	roleKey := "http-real-edge-role"
	roleID := seedWorkflowRoleFor(t, super, tenantID, roleKey, "HTTP Real Edge Role")
	policyID := seedApprovalPolicyFor(t, super, tenantID, "APPR-07-06 http-real-edge policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	seedApprovalStepFor(t, super, tenantID, versionID, approvalStepSpecFor{
		Ord: 0, Kind: "approval", WorkflowRoleKey: &roleKey,
	})

	approvalStore := approval.NewStore(app, FingerprintTx, DemoteApprovalRejectedTx)
	if _, err := approvalStore.PublishPolicy(adminCtx, policyID); err != nil {
		t.Fatalf("PublishPolicy: %v, want success", err)
	}

	reviewerID := uuid.NewString()
	seedMembership(t, super, tenantID, reviewerID, "reviewer")
	staffWorkflowRoleFor(t, super, tenantID, roleID, reviewerID, 0)

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)

	// The exact production pattern (cmd/invoice/main.go), wired to the real
	// DecideSeam -- not a hand-written Decider closure.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/invoices/{id}/approvals", approval.DecideHandler(approvalStore.DecideSeam, nil))

	reviewerReq := httptest.NewRequest("POST", "/v1/invoices/"+inv.ID+"/approvals", strings.NewReader(`{"decision":"rejected","reason":"wrong VAT"}`))
	reviewerReq = reviewerReq.WithContext(auth.WithIdentity(context.Background(),
		auth.Identity{Subject: reviewerID, Role: "authenticated", TenantID: tenantID}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reviewerReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body approval.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body.State != "rejected" {
		t.Errorf("response state = %q, want rejected", body.State)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if Status(status) != StatusDraft {
		t.Errorf("invoice status after a reject over HTTP = %q, want draft", status)
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
