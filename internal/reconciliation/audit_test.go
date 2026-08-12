// M5-06-04 (task-246): the reconciliation audit writes (recordDriftAudit,
// recordAutoFixAudit). RED against the reconciliation.go stub, which always returns nil and
// writes no row — every case here reads audit_log back and fails on pgx.ErrNoRows (via
// t.Fatalf), never on a compile or connection error.
//
// audit_log has NO FK to tenants (its tenant_id defaults from the app.current_tenant GUC,
// migrations/20260708062657_audit_log.sql:34-40 — see audit.go's own doc comment), so these
// cases use a bare fresh uuid as the tenant rather than the full rcSeedInvoice chain: only
// db.WithinTenantTx needs a syntactically valid uuid, not a `tenants` row.
//
// Spec-to-test map (M5-06 story, [M5-06-04] Test Specs table):
//
//	AC-1 TestRLS_DriftAuditWritten
//	AC-2 TestRLS_AutoFixAuditWritten
//	AC-3 TestRLS_AuditPayloadSummaryOnly
//
// APPR-06-09 (task-485) adds one case proving AC-2/AC-6 (no new audit event, no new payload
// key) against a REAL SweepOnce call over two seeded approval-drift findings — the case
// above only proves this off a hand-built Finding literal, never off the two new SQL arms:
//
//	AC-2,6 TestRLS_ApprovalDriftAuditsWritten
package reconciliation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// AC-1: a flagged finding writes one reconciliation.drift_detected row, actor system,
// payload drift_kind, scoped to the tx's tenant.
func TestRLS_DriftAuditWritten(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	invoiceID := uuid.NewString()
	jobID := uuid.NewString()
	f := Finding{InvoiceID: invoiceID, SubmissionJobID: &jobID, Kind: SubmittingOrphan, Healable: false}

	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		return recordDriftAudit(ctx, tx, f)
	}); err != nil {
		t.Fatalf("recordDriftAudit: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()

	var gotTenantID, actor, event string
	var payload []byte
	err := h.super.QueryRow(ctx,
		`SELECT tenant_id::text, actor, event, payload FROM audit_log
		  WHERE tenant_id = $1 ORDER BY id DESC LIMIT 1`, tenantID,
	).Scan(&gotTenantID, &actor, &event, &payload)
	if err != nil {
		t.Fatalf("read the drift audit row: %v — want exactly one reconciliation.drift_detected "+
			"row written", err)
	}
	if event != "reconciliation.drift_detected" {
		t.Errorf("event = %q, want %q", event, "reconciliation.drift_detected")
	}
	if actor != "system" {
		t.Errorf("actor = %q, want %q", actor, "system")
	}
	if gotTenantID != tenantID {
		t.Errorf("tenant_id = %q, want %q (the tx's own tenant)", gotTenantID, tenantID)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := body["drift_kind"].(string); got != string(SubmittingOrphan) {
		t.Errorf("payload drift_kind = %v, want %q", body["drift_kind"], SubmittingOrphan)
	}
}

// AC-2: a heal writes one reconciliation.auto_fixed row, payload drift_kind:"lost_poll",
// action:"repoll_reenqueued".
func TestRLS_AutoFixAuditWritten(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	invoiceID := uuid.NewString()
	jobID := uuid.NewString()
	f := Finding{InvoiceID: invoiceID, SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}

	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		return recordAutoFixAudit(ctx, tx, f)
	}); err != nil {
		t.Fatalf("recordAutoFixAudit: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()

	var event string
	var payload []byte
	err := h.super.QueryRow(ctx,
		`SELECT event, payload FROM audit_log WHERE tenant_id = $1 ORDER BY id DESC LIMIT 1`, tenantID,
	).Scan(&event, &payload)
	if err != nil {
		t.Fatalf("read the auto-fix audit row: %v — want exactly one reconciliation.auto_fixed "+
			"row written", err)
	}
	if event != "reconciliation.auto_fixed" {
		t.Errorf("event = %q, want %q", event, "reconciliation.auto_fixed")
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := body["drift_kind"].(string); got != string(LostPoll) {
		t.Errorf("payload drift_kind = %v, want %q", body["drift_kind"], LostPoll)
	}
	if got, _ := body["action"].(string); got != "repoll_reenqueued" {
		t.Errorf("payload action = %v, want %q", body["action"], "repoll_reenqueued")
	}
}

// AC-3: payloads carry ids + kind (+ action on auto_fixed) ONLY — no wire bodies. The
// len(body)==0 guard stops an empty {} (audit.Record's own default for a nil payload) from
// vacuously satisfying "every key is in the allowlist".
func TestRLS_AuditPayloadSummaryOnly(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	invoiceID := uuid.NewString()
	jobID := uuid.NewString()
	f := Finding{InvoiceID: invoiceID, SubmissionJobID: &jobID, Kind: LostPoll, Healable: true}

	if err := db.WithinTenantTx(ctx, h.app, tenantID, func(tx pgx.Tx) error {
		return recordAutoFixAudit(ctx, tx, f)
	}); err != nil {
		t.Fatalf("recordAutoFixAudit: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()

	var payload []byte
	err := h.super.QueryRow(ctx,
		`SELECT payload FROM audit_log WHERE tenant_id = $1 ORDER BY id DESC LIMIT 1`, tenantID,
	).Scan(&payload)
	if err != nil {
		t.Fatalf("read the audit row: %v — want exactly one row written", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("payload is empty {} — want invoice_id/submission_job_id/drift_kind/action " +
			"populated; an empty payload would trivially (and vacuously) satisfy the key-set " +
			"check below without proving anything was actually written")
	}

	allowed := map[string]bool{"invoice_id": true, "submission_job_id": true, "drift_kind": true, "action": true}
	for k := range body {
		if !allowed[k] {
			t.Errorf("payload key %q is not in the summary-only allowlist "+
				"{invoice_id, submission_job_id, drift_kind, action} — no wire bodies", k)
		}
	}
}

// AC-2, AC-6: the two new approval-drift kinds route through the SAME recordDriftAudit path
// as every other flag-only finding — no new audit event, no new payload key — proven against
// a REAL SweepOnce call over a tenant holding one orphaned run and one blocked run. Both
// findings are Healable = false, so exactly two reconciliation.drift_detected rows and zero
// reconciliation.auto_fixed rows must result.
func TestRLS_ApprovalDriftAuditsWritten(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID, entityID, invoiceOrphaned, cleanupOrphaned := rcSeedInvoice(t, h, rcInvoiceOpts{status: "validated"})
	defer cleanupOrphaned()
	invoiceBlocked, cleanupBlocked := rcSeedInvoiceIn(t, h, tenantID, entityID, rcInvoiceOpts{status: "validated"})
	defer cleanupBlocked()

	versionID, cleanupPolicy := rcSeedApprovalPolicy(t, h, tenantID)
	defer cleanupPolicy()

	// Orphaned: open run, invoice demoted out of band.
	rcSeedRun(t, h, tenantID, invoiceOrphaned, versionID, "open")
	if _, err := h.super.Exec(ctx, `UPDATE invoices SET status = 'draft' WHERE id = $1`, invoiceOrphaned); err != nil {
		t.Fatalf("flip invoice to draft: %v", err)
	}

	// Blocked: open run, current pending approval step names a role with zero holders.
	rcSeedWorkflowRole(t, h, tenantID, "empty-audit-role", false)
	blockedRun := rcSeedRun(t, h, tenantID, invoiceBlocked, versionID, "open")
	roleKey := "empty-audit-role"
	rcSeedRunStep(t, h, tenantID, blockedRun, 1, "approval", &roleKey, "pending")

	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantID)
	}()

	if err := rcReconciler(h).SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	drifted := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.drift_detected'`, tenantID)
	if drifted != 2 {
		t.Errorf("reconciliation.drift_detected rows for tenant %q = %d, want exactly 2", tenantID, drifted)
	}
	fixed := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantID)
	if fixed != 0 {
		t.Errorf("reconciliation.auto_fixed rows for tenant %q = %d, want 0 (both findings are "+
			"Healable = false)", tenantID, fixed)
	}

	rows, err := h.super.Query(ctx,
		`SELECT payload FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.drift_detected'`, tenantID)
	if err != nil {
		t.Fatalf("read drift audit rows: %v", err)
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		n++
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan payload: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if len(body) != 2 {
			t.Errorf("payload = %+v, want exactly {invoice_id, drift_kind} (submission_job_id "+
				"absent, action absent)", body)
			continue
		}
		if _, ok := body["invoice_id"]; !ok {
			t.Errorf("payload = %+v, want invoice_id present", body)
		}
		if _, ok := body["drift_kind"]; !ok {
			t.Errorf("payload = %+v, want drift_kind present", body)
		}
		if _, ok := body["submission_job_id"]; ok {
			t.Errorf("payload = %+v, want submission_job_id ABSENT (neither finding carries a job row)", body)
		}
		if _, ok := body["action"]; ok {
			t.Errorf("payload = %+v, want action ABSENT (action is auto_fixed-only)", body)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate drift audit rows: %v", err)
	}
	if n != 2 {
		t.Errorf("iterated %d drift audit rows, want 2", n)
	}
}
