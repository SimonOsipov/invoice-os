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
