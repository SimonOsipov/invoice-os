// exactly_once_audit_test.go: AUDIT-03-05 part B -- characterization tests over the
// submission audit seam's OncePerJob boundary: redelivery, a mid-budget retry that later
// succeeds, concurrent redelivery, the full event vocabulary, the poll RLS gap, and
// same-invoice/same-key multi-job behavior. Package submission_test (external), reusing
// every fixture/helper this package's other DB-backed files already declare -- see
// submit_worker_test.go's own header for GATING and the shared TestMain.
package submission_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// assertJobMarkers reads the OncePerJob marker census for tenantID (idempotency_keys rows
// under the "job:<id>" namespace) and asserts SET EQUALITY with want -- an unexpected extra
// marker is as much a bug as a missing one. Runs inside db.WithinTenantTx: idempotency_keys
// is FORCE RLS.
func assertJobMarkers(t *testing.T, f *effectsFixture, tenantID string, want ...int64) {
	t.Helper()
	ctx := context.Background()
	var got []string
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx,
			`SELECT key FROM idempotency_keys WHERE tenant_id = $1 AND key LIKE 'job:%' ORDER BY key`,
			tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if e := rows.Scan(&k); e != nil {
				return e
			}
			got = append(got, k)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read job marker census (tenant=%s): %v", tenantID, err)
	}

	wantKeys := make([]string, len(want))
	for i, id := range want {
		wantKeys[i] = fmt.Sprintf("job:%d", id)
	}
	sort.Strings(wantKeys) // matches the query's own ORDER BY key (lexical, not numeric)

	same := len(got) == len(wantKeys)
	if same {
		for i := range got {
			if got[i] != wantKeys[i] {
				same = false
				break
			}
		}
	}
	if !same {
		t.Errorf("job marker census (tenant=%s) = %v, want exactly %v", tenantID, got, wantKeys)
	}
}

// --- COMMIT 4 ------------------------------------------------------------------------------

// A River redelivery of the identical job (same id, same attempt) must not double the audit
// row. The guard is tx1's terminal short-circuit -- worker.go:120 [tx1Terminal] for submit,
// worker.go:443 [state != "pending"] for poll -- NOT queue.OncePerJob: for a redelivered job,
// tx1 returns before the adapter is ever called again. The second call returning nil (rather
// than an error) is the control proving the short-circuit fired, not some unrelated early
// error.
func TestSubmissionAudit_RedeliveredTerminalFailureWritesOneRow(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()

	t.Run("transform-failure", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()

		idemKey := "req-" + uuid.NewString() + ":" + invoiceID
		adapter := newScriptedAdapter() // empty queue: Submit must never fire
		adapter.transformErr = errors.New("wsub: cannot build wire from this invoice")
		w := newTestWorker(f.app, adapter)
		job := newSubmitJob(40, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

		if err := w.Work(ctx, job); err == nil {
			t.Fatal("first delivery returned nil, want river.JobCancel")
		}
		if err := w.Work(ctx, job); err != nil {
			t.Fatalf("redelivered job returned %v, want nil -- tx1's terminal short-circuit should have fired", err)
		}

		if n := auditCount(t, f, tenantID, "submission.failed"); n != 1 {
			t.Errorf("submission.failed audit rows after redelivery = %d, want 1", n)
		}
		if n := auditFamilyCount(t, f, tenantID); n != 1 {
			t.Errorf("submission.* audit rows after redelivery = %d, want 1", n)
		}
	})

	t.Run("submit-exhaustion", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()

		idemKey := "req-" + uuid.NewString() + ":" + invoiceID
		adapter := newScriptedAdapter(scriptedOutcome{
			result:   submission.Retryable{Err: errors.New("wsub: upstream 503, final attempt")},
			evidence: submission.Evidence{ReachedWire: true},
		})
		w := newTestWorker(f.app, adapter)
		job := newSubmitJob(41, 8, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

		if err := w.Work(ctx, job); err == nil {
			t.Fatal("first delivery returned nil, want the dead-letter error")
		}
		if err := w.Work(ctx, job); err != nil {
			t.Fatalf("redelivered job returned %v, want nil -- tx1's terminal short-circuit should have fired", err)
		}

		if n := auditCount(t, f, tenantID, "submission.failed"); n != 1 {
			t.Errorf("submission.failed audit rows after redelivery = %d, want 1", n)
		}
		if n := auditFamilyCount(t, f, tenantID); n != 1 {
			t.Errorf("submission.* audit rows after redelivery = %d, want 1", n)
		}
	})

	t.Run("poll-exhaustion", func(t *testing.T) {
		tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
		defer cleanup()

		future := time.Now().Add(time.Hour)
		idemKey := "req-" + uuid.NewString() + ":" + invoiceID
		adapter := newScriptedAdapter(scriptedOutcome{
			result:   submission.Pending{Ref: "r1", PollAfter: future},
			evidence: submission.Evidence{ReachedWire: true},
		})
		sw := newTestWorker(f.app, adapter)
		submitJob := newSubmitJob(42, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})
		if err := sw.Work(ctx, submitJob); err != nil {
			t.Fatalf("submit to pending: %v", err)
		}
		wj := wjRequire(t, f, tenantID, idemKey)

		adapter.pollQueue = []scriptedOutcome{
			{result: submission.Retryable{Err: errors.New("wsub: poll upstream 503, final attempt")}, evidence: submission.Evidence{ReachedWire: true}},
		}
		pw := newTestPollWorker(f.app, adapter)
		pollJob := newPollJob(43, 8, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1})

		if err := pw.Work(ctx, pollJob); err == nil {
			t.Fatal("first delivery returned nil, want the dead-letter error")
		}
		if err := pw.Work(ctx, pollJob); err != nil {
			t.Fatalf("redelivered poll job returned %v, want nil -- the superseded short-circuit should have fired", err)
		}

		if n := auditCount(t, f, tenantID, "submission.failed"); n != 1 {
			t.Errorf("submission.failed audit rows after redelivery = %d, want 1", n)
		}
		if n := auditFamilyCount(t, f, tenantID); n != 1 {
			t.Errorf("submission.* audit rows after redelivery = %d, want 1", n)
		}
	})
}
