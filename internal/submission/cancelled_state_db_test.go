// a submit job whose row is `cancelled` is terminal; Work returns before the adapter.
package submission_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func TestSubmitWorker_CancelledJobShortCircuits(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	jobID := seedSiblingJob(t, f, tenantID, invoiceID, "cancelled")
	if err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE submission_jobs SET idempotency_key = $1 WHERE id = $2`, idemKey, jobID)
		return e
	}); err != nil {
		t.Fatalf("give the cancelled job the worker's idempotency key: %v", err)
	}
	histBefore := len(wiHistory(t, f, tenantID, invoiceID))
	auditBefore := countTenantAudit(t, f, tenantID)

	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Accepted{IRN: "NG-1", CSID: "CSID-1", QRPayload: "QR-1"},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Errorf("Work on a cancelled job returned %v, want nil", err)
	}
	if got := adapter.calls(); got != 0 {
		t.Errorf("adapter.Submit calls on a cancelled job = %d, want 0", got)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.id != jobID || wj.state != "cancelled" || wj.attempts != 1 {
		t.Errorf("job after Work = {id %s, state %q, attempts %d}, want {%s, cancelled, 1} unchanged", wj.id, wj.state, wj.attempts, jobID)
	}
	if n := wjCount(t, f, tenantID, idemKey); n != 1 {
		t.Errorf("submission_jobs rows for the key = %d, want 1", n)
	}
	if inv := wiRead(t, f, tenantID, invoiceID); inv.status != "queued" {
		t.Errorf("invoice status after Work = %q, want queued unchanged", inv.status)
	}
	if got := len(wiHistory(t, f, tenantID, invoiceID)); got != histBefore {
		t.Errorf("invoice_status_history rows = %d, want %d unchanged", got, histBefore)
	}
	if got := countTenantAudit(t, f, tenantID); got != auditBefore {
		t.Errorf("audit_log rows = %d, want %d unchanged", got, auditBefore)
	}
}

func countTenantAudit(t *testing.T, f *effectsFixture, tenantID string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log (tenant=%s): %v", tenantID, err)
	}
	return n
}
