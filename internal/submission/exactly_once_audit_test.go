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
	"sync"
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

// twoPartyRendezvous returns an onSubmit-shaped hook that blocks its first caller until a
// second arrives, then releases both -- an explicit counter under a mutex plus sync.Once,
// not len() on a channel (which races). A 10s timeout t.Errors rather than hanging: a
// rendezvous bug must fail the test, not deadlock it.
func twoPartyRendezvous(t *testing.T) func() {
	t.Helper()
	var (
		mu      sync.Mutex
		arrived int
		once    sync.Once
	)
	release := make(chan struct{})
	return func() {
		mu.Lock()
		arrived++
		n := arrived
		mu.Unlock()
		if n == 2 {
			once.Do(func() { close(release) })
			return
		}
		select {
		case <-release:
		case <-time.After(10 * time.Second):
			t.Error("the second goroutine never reached Submit")
		}
	}
}

// distinctSubmissionEvents runs the vacuous-pass floor a distinct-set claim needs ruled out
// (an empty result trivially "satisfies" set equality otherwise).
func distinctSubmissionEvents(t *testing.T, f *effectsFixture, tenantID string) []string {
	t.Helper()
	ctx := context.Background()
	var events []string
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT DISTINCT event FROM audit_log WHERE event LIKE 'submission.%'`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var ev string
			if e := rows.Scan(&ev); e != nil {
				return e
			}
			events = append(events, ev)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("select distinct submission.* events (tenant=%s): %v", tenantID, err)
	}
	return events
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

// --- COMMIT 5 ------------------------------------------------------------------------------

// The executable form of markJobRetry's doc comment: the mid-budget branch is deliberately
// outside queue.OncePerJob, so a second attempt on the SAME River job id, after budget was
// left on the first, still runs its closure and writes exactly the accepted row -- never a
// failed one from attempt 1.
func TestSubmissionAudit_MidBudgetRetryThenSuccessWritesOnlyAccepted(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(
		scriptedOutcome{result: submission.Retryable{Err: errors.New("wsub: transient, attempt 1")}, evidence: submission.Evidence{ReachedWire: true}},
		scriptedOutcome{result: submission.Accepted{IRN: "IRN-AC2", CSID: "CSID-AC2", QRPayload: "QR-AC2"}, evidence: submission.Evidence{ReachedWire: true}},
	)
	w := newTestWorker(f.app, adapter)

	job1 := newSubmitJob(44, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})
	if err := w.Work(ctx, job1); err == nil {
		t.Fatal("attempt 1 (mid-budget Retryable) returned nil, want the original error so River retries")
	}
	if n := auditFamilyCount(t, f, tenantID); n != 0 {
		t.Fatalf("submission.* audit rows after attempt 1 = %d, want 0 -- precondition for attempt 2 below", n)
	}

	// SAME River job id 44.
	job2 := newSubmitJob(44, 2, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})
	if err := w.Work(ctx, job2); err != nil {
		t.Fatalf("attempt 2 (Accepted) returned %v, want nil", err)
	}

	if n := auditCount(t, f, tenantID, "submission.accepted"); n != 1 {
		t.Errorf("submission.accepted audit rows = %d, want 1", n)
	}
	if n := auditCount(t, f, tenantID, "submission.failed"); n != 0 {
		t.Errorf("submission.failed audit rows = %d, want 0", n)
	}
	if n := auditFamilyCount(t, f, tenantID); n != 1 {
		t.Errorf("submission.* audit rows = %d, want 1", n)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "accepted" {
		t.Errorf("invoice status = %q, want \"accepted\"", inv.status)
	}
}

// --- COMMIT 6 ------------------------------------------------------------------------------

// Two goroutines redeliver the SAME River job (one id, one idempotency key) concurrently,
// rendezvousing inside scriptedAdapter.Submit so BOTH clear tx1 (and so both would call the
// adapter) before either proceeds to tx2 -- without the barrier this degrades into the
// sequential redelivery case above, since the loser's tx1 would already see a terminal state
// and short-circuit before ever reaching Submit. queue.OncePerJob is the ONLY guard here: it
// is per-(tenant, River job id), and both goroutines share both.
func TestSubmissionAudit_ConcurrentRedeliveryOfOneJobWritesOneRow(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Retryable{Err: errors.New("wsub: upstream 503, final attempt (concurrent redelivery)")},
		evidence: submission.Evidence{ReachedWire: true},
	})
	adapter.onSubmit = twoPartyRendezvous(t)

	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(45, 8, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = w.Work(ctx, job) }()
	go func() { defer wg.Done(); errs[1] = w.Work(ctx, job) }()
	wg.Wait()

	// Control: a tx1 short-circuit returns nil (worker.go:192 [case tx1Terminal, tx1AlreadyCleared]);
	// worker.go:350 [return submitErr] returns a non-nil error on BOTH the applied and skipped
	// OncePerJob paths. Both non-nil proves both goroutines independently reached tx2.
	if errs[0] == nil || errs[1] == nil {
		t.Fatalf("Work() returned (%v, %v), want BOTH non-nil", errs[0], errs[1])
	}

	if n := auditCount(t, f, tenantID, "submission.failed"); n != 1 {
		t.Errorf("submission.failed audit rows = %d, want exactly 1", n)
	}
	if n := auditFamilyCount(t, f, tenantID); n != 1 {
		t.Errorf("submission.* audit rows = %d, want exactly 1", n)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if n := exCountRows(t, f, tenantID, wj.id); n != 1 {
		t.Errorf("app_exchange rows for the job = %d, want exactly 1 -- RecordExchange is the "+
			"closure's first statement, so this proves the loser's closure was skipped in its entirety", n)
	}
	assertJobMarkers(t, f, tenantID, 45)

	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "failed" {
		t.Errorf("invoice status = %q, want \"failed\"", inv.status)
	}
	if inv.failureKind == nil {
		t.Error("invoice failure_kind = nil, want non-nil")
	}
	if wj.state != "dead_lettered" {
		t.Errorf("submission_jobs.state = %q, want \"dead_lettered\"", wj.state)
	}
	if wj.attempts != 1 {
		t.Errorf("submission_jobs.attempts = %d, want 1 -- the loser must never have incremented it", wj.attempts)
	}
}

// --- COMMIT 7 ------------------------------------------------------------------------------

// Every driven path in one tenant, across the whole vocabulary this seam can produce, must
// resolve to exactly one of the three declared events. A DISTINCT-set assertion alone is
// satisfied vacuously by an empty result, so three layers back it: exact per-event counts
// (not a floor), the marker census as an independent control on which closures ran, and two
// non-audit oracles for the two paths that deliberately write no audit row at all.
func TestSubmissionAudit_EveryDrivenPathSpellsOneOfThreeEvents(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID := seedTenant(t, f)
	defer cleanupTenant(t, f, tenantID)
	entityID := seedEntity(t, f, tenantID)

	// Accepted (id 46).
	invAccepted := seedInvoice(t, f, tenantID, entityID)
	idemAccepted := "req-" + uuid.NewString() + ":" + invAccepted
	adapterAccepted := newScriptedAdapter(scriptedOutcome{
		result:   submission.Accepted{IRN: "IRN-EVT-46", CSID: "CSID-46", QRPayload: "QR-46"},
		evidence: submission.Evidence{ReachedWire: true},
	})
	if err := newTestWorker(f.app, adapterAccepted).Work(ctx,
		newSubmitJob(46, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invAccepted, IdempotencyKey: idemAccepted})); err != nil {
		t.Fatalf("accepted path: %v", err)
	}

	// Rejected (id 47).
	invRejected := seedInvoice(t, f, tenantID, entityID)
	idemRejected := "req-" + uuid.NewString() + ":" + invRejected
	adapterRejected := newScriptedAdapter(scriptedOutcome{
		result:   submission.Rejected{Reasons: []submission.Reason{{Code: "E1", Message: "evt-47 rejection"}}},
		evidence: submission.Evidence{ReachedWire: true},
	})
	if err := newTestWorker(f.app, adapterRejected).Work(ctx,
		newSubmitJob(47, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invRejected, IdempotencyKey: idemRejected})); err != nil {
		t.Fatalf("rejected path: %v", err)
	}

	// Pending (id 48).
	invPending := seedInvoice(t, f, tenantID, entityID)
	idemPending := "req-" + uuid.NewString() + ":" + invPending
	adapterPending := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "r-evt-48", PollAfter: time.Now().Add(time.Hour)},
		evidence: submission.Evidence{ReachedWire: true},
	})
	if err := newTestWorker(f.app, adapterPending).Work(ctx,
		newSubmitJob(48, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invPending, IdempotencyKey: idemPending})); err != nil {
		t.Fatalf("pending path: %v", err)
	}
	wjPending := wjRequire(t, f, tenantID, idemPending)

	// Mid-budget Retryable (id 49) -- deliberately outside OncePerJob (see commit 5).
	invMidBudget := seedInvoice(t, f, tenantID, entityID)
	idemMidBudget := "req-" + uuid.NewString() + ":" + invMidBudget
	adapterMidBudget := newScriptedAdapter(scriptedOutcome{
		result:   submission.Retryable{Err: errors.New("wsub: transient, evt-49")},
		evidence: submission.Evidence{ReachedWire: true},
	})
	if err := newTestWorker(f.app, adapterMidBudget).Work(ctx,
		newSubmitJob(49, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invMidBudget, IdempotencyKey: idemMidBudget})); err == nil {
		t.Fatal("mid-budget path returned nil, want the original error so River retries")
	}

	// Transform-failed (workTransformFailure hardcodes River id 1).
	invTransform := seedInvoice(t, f, tenantID, entityID)
	workTransformFailure(t, f, tenantID, invTransform)

	// Submit-exhausted (workRetryExhaustion hardcodes River id 2).
	invSubmitExhausted := seedInvoice(t, f, tenantID, entityID)
	workRetryExhaustion(t, f, tenantID, invSubmitExhausted)

	// Poll-exhausted (submit id 50 -> Pending, poll id 51 -> dead-letter).
	invPollExhausted := seedInvoice(t, f, tenantID, entityID)
	workPollExhaustion(t, f, tenantID, invPollExhausted, 50, 51)

	// Layer 1: exact counts, not a floor.
	if n := auditFamilyCount(t, f, tenantID); n != 5 {
		t.Fatalf("submission.* audit rows = %d, want exactly 5", n)
	}
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != 1 {
		t.Errorf("submission.accepted rows = %d, want 1", n)
	}
	if n := auditCount(t, f, tenantID, "submission.rejected"); n != 1 {
		t.Errorf("submission.rejected rows = %d, want 1", n)
	}
	if n := auditCount(t, f, tenantID, "submission.failed"); n != 3 {
		t.Errorf("submission.failed rows = %d, want 3", n)
	}

	// Layer 2: the marker census. Six closures ran (accepted, rejected, pending,
	// transform-failed, submit-exhausted, poll's own submit-Pending and dead-letter) and no
	// eighth did; id 49's mid-budget marker is deliberately absent (layer 3a below).
	assertJobMarkers(t, f, tenantID, 1, 2, 46, 47, 48, 50, 51)

	// Layer 3a: the mid-budget path ran, proven without the audit table or the marker
	// census -- job:49 is absent from the census above precisely because this branch sits
	// outside queue.OncePerJob.
	wjMid := wjRequire(t, f, tenantID, idemMidBudget)
	if wjMid.state != "queued" {
		t.Errorf("mid-budget job state = %q, want \"queued\"", wjMid.state)
	}
	if wjMid.attempts != 1 {
		t.Errorf("mid-budget job attempts = %d, want 1", wjMid.attempts)
	}
	if wjMid.lastError == nil {
		t.Error("mid-budget job last_error = nil, want non-nil")
	}

	// Layer 3b: the Pending path ran, proven via its own state plus the poll hop it enqueued
	// in the same OncePerJob closure that wrote its job:48 marker.
	if wjPending.state != "pending" {
		t.Errorf("pending job state = %q, want \"pending\"", wjPending.state)
	}
	if n := countKeys(t, f.app, tenantID, "poll:"+wjPending.id+":1"); n != 1 {
		t.Errorf("poll outbox key count for job %s = %d, want 1", wjPending.id, n)
	}

	// The claim this whole test exists to raise above vacuous.
	events := distinctSubmissionEvents(t, f, tenantID)
	want := map[string]bool{"submission.accepted": true, "submission.rejected": true, "submission.failed": true}
	if len(events) != len(want) {
		t.Fatalf("distinct submission.* events = %v, want exactly %v", events, want)
	}
	for _, e := range events {
		if !want[e] {
			t.Errorf("unexpected distinct event %q", e)
		}
	}
}

// --- COMMIT 8 ------------------------------------------------------------------------------

// TestRLS_PollWorkerFailureAuditRowNotVisibleToAnotherTenant is the poll dead-letter path's
// own RLS case for the submission.failed row -- worker_poll_adversarial_test.go's own
// TestRLS_PollWorkerDeadLetterFailureKindNotVisibleToAnotherTenant covers invoices.status /
// failure_kind after a poll dead-letter, but says explicitly that the dead-letter's audit row
// "still has no RLS case of its own". The submit path already has one
// (TestRLS_SubmitWorkerFailureAuditRowNotVisibleToAnotherTenant); audit_log's own
// tenant_isolation policy is table-wide, so this is second-write-path coverage, not a novel
// RLS proof. Its mutation oracle is not runnable in CI without a migration -- none is claimed
// here.
func TestRLS_PollWorkerFailureAuditRowNotVisibleToAnotherTenant(t *testing.T) {
	f := requireExchangeDB(t)
	tenantA, invoiceA, cleanupA := seedQueuedInvoice(t, f)
	defer cleanupA()
	tenantB := seedTenant(t, f)
	defer cleanupTenant(t, f, tenantB)

	workPollExhaustion(t, f, tenantA, invoiceA, 52, 53)

	if n := auditCount(t, f, tenantA, "submission.failed"); n != 1 {
		t.Fatalf("tenant A's own submission.failed audit rows = %d, want 1 -- "+
			"precondition for the isolation check below", n)
	}
	if n := auditCount(t, f, tenantB, "submission.failed"); n != 0 {
		t.Errorf("tenant B's view of submission.failed audit rows = %d, want 0 -- "+
			"RLS must hide tenant A's poll dead-letter row from tenant B", n)
	}
}
