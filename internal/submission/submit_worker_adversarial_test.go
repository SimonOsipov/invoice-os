// submit_worker_adversarial_test.go: QA Mode B adversarial coverage for M5-04-05 (task-230),
// beyond the fifteen AC-derived Test Specs (T05-1..T05-15) already GREEN in
// submit_worker_test.go. Reuses that file's helpers (seedQueuedInvoice, newTestWorker,
// newSubmitJob, wjRequire, wjExchanges, wiRead, wiHistory, scriptedAdapter,
// newScriptedAdapter) rather than duplicating them -- same package (submission_test), same
// file. No new writer function, no testify, no t.Skip beyond requireExchangeDB's established
// gate.
//
// Eight cases, motivated by the mutation-testing pass in task-230's Mode B QA report:
//
//  1. TestSubmitWorker_PendingSetsPollRefAndMovesInvoiceToSubmitted -- the Pending branch had
//     ZERO test coverage anywhere in the suite (grep confirmed: no T05 spec, and
//     poll_ref_db_test.go/poll_ref_adversarial_test.go exercise the RAW COLUMN, never
//     SubmitWorker.Work's own case Pending). This closes that gap for 05's own scope
//     (poll_ref/next_poll_at/state/MarkSubmitted) -- the FOLLOW-UP ENQUEUE is still 06's.
//  2. TestSubmitWorker_TwoConsecutiveRetryablesBothConsumeBudget -- the design's OWN named
//     hazard (worker.go's markJobRetry doc comment: wrapping the mid-budget write in
//     OncePerJob would make attempt 2 a silent no-op) proven with a PURE two-Retryable
//     sequence, not the mixed Retryable-then-Accepted sequence T05-11 happens to also catch
//     this hazard through.
//  3. TestSubmitWorker_RetryableOneShortOfFinalStaysMidBudget -- the dead-letter boundary's
//     off-by-one risk: job.Attempt == MaxAttempts-1 must NOT dead-letter (only T05-6, at
//     job.Attempt == MaxAttempts, is specced).
//  4. TestSubmitWorker_UnknownResultVariantRollsBackAndErrors -- a nil Result hits the type
//     switch's default branch; tx2 must roll back cleanly (job stays "submitting", no
//     exchange row, invoice untouched) and Work must return a non-nil error.
//  5. TestSubmitWorker_RecordExchangeFailureMidTx2RollsBackAndIsRerunnable -- a CHECK
//     violation (negative latency_ms) inside tx2's OncePerJob closure must roll back the
//     WHOLE transaction, including the OncePerJob marker itself -- proven by re-running Work
//     with a corrected adapter afterward and seeing it succeed cleanly, not silently no-op.
//  6. TestSubmitWorker_ConcurrentRedeliveryBothReachSubmit -- two goroutines calling Work for
//     the SAME job.ID (a River redelivery race), synchronized so the second's tx1 provably
//     observes the first's already-committed state="submitting". "submitting" is not in
//     isTerminalJobState's set, so this is NOT blocked at the gate. FINDING: the adapter is
//     genuinely invoked twice (a live authority would see two submissions); OncePerJob still
//     keeps the DB-side outcome exactly-once (one exchange row, one invoice transition). This
//     is a residual of [adapters-are-db-free] (the row lock is released before the adapter
//     call, by design) that River's own single-delivery guarantee is what actually prevents
//     in production -- reported, not fixed, per the QA charter.
//  7. TestSubmitWorker_InvoiceDeletedBeforeFirstAttemptFailsClosed -- the invoice named by
//     SubmitArgs no longer exists when ensureSubmissionJob's INSERT first runs: the composite
//     FK (submission_jobs_tenant_invoice_fk) refuses the row, tx1 rolls back, Work returns a
//     plain error (River retries normally), and no submission_jobs row is left behind.
//  8. TestSubmitWorker_AcceptedWithBlankIRNStillMovesInvoiceToSubmitted -- the worker itself
//     never reads Accepted.IRN (persisting it is M5-05's job); a law-violating (blank) IRN
//     from a misbehaving adapter still drives job/invoice state exactly like any other
//     Accepted, since the worker has nothing to validate.
package submission_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// --- shared read-back helper (poll_ref/next_poll_at -- not in submit_worker_test.go's
// wjState, added here rather than touching that file) -----------------------------------

type wjPollState struct {
	state      string
	pollRef    *string
	nextPollAt *bool // true if non-NULL; the exact timestamp isn't asserted, only presence
}

func wjPollRequire(t *testing.T, f *effectsFixture, tenantID, idemKey string) wjPollState {
	t.Helper()
	var got wjPollState
	var nextPollAtSet bool
	err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT state, poll_ref, next_poll_at IS NOT NULL
			   FROM submission_jobs WHERE tenant_id = $1 AND idempotency_key = $2`,
			tenantID, idemKey).Scan(&got.state, &got.pollRef, &nextPollAtSet)
	})
	if err != nil {
		t.Fatalf("read submission_jobs poll columns (tenant=%s key=%s): %v", tenantID, idemKey, err)
	}
	got.nextPollAt = &nextPollAtSet
	return got
}

// --- 1: Pending ---------------------------------------------------------------------------

func TestSubmitWorker_PendingSetsPollRefAndMovesInvoiceToSubmitted(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	future := time.Now().Add(time.Hour)
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "poll-ref-1", PollAfter: future},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Fatalf("Work on a Pending outcome: %v", err)
	}
	if got := adapter.calls(); got != 1 {
		t.Errorf("adapter.Submit call count = %d, want exactly 1", got)
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "pending" {
		t.Errorf("job state = %q, want \"pending\"", wj.state)
	}
	if wj.attempts != 1 {
		t.Errorf("job attempts = %d, want 1", wj.attempts)
	}

	poll := wjPollRequire(t, f, tenantID, idemKey)
	if poll.pollRef == nil || *poll.pollRef != "poll-ref-1" {
		t.Errorf("job poll_ref = %v, want \"poll-ref-1\"", poll.pollRef)
	}
	if poll.nextPollAt == nil || !*poll.nextPollAt {
		t.Error("job next_poll_at is NULL, want set from Pending.PollAfter")
	}

	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "submitted" {
		t.Errorf("invoice status = %q, want \"submitted\" -- Pending still reached the APP", inv.status)
	}
	hist := wiHistory(t, f, tenantID, invoiceID)
	if len(hist) != 1 || hist[0].toStatus != "submitted" {
		t.Errorf("invoice_status_history = %+v, want exactly one queued->submitted row", hist)
	}
	exchanges := wjExchanges(t, f, tenantID, wj.id)
	if len(exchanges) != 1 || exchanges[0].outcome != "sent" {
		t.Errorf("app_exchange rows = %+v, want exactly one 'sent' row", exchanges)
	}
}

// --- 2: two consecutive mid-budget Retryables (the design's own named hazard) -------------

func TestSubmitWorker_TwoConsecutiveRetryablesBothConsumeBudget(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(
		scriptedOutcome{result: submission.Retryable{Err: errors.New("wsub: transient #1")}, evidence: submission.Evidence{ReachedWire: true}},
		scriptedOutcome{result: submission.Retryable{Err: errors.New("wsub: transient #2")}, evidence: submission.Evidence{ReachedWire: true}},
	)
	w := newTestWorker(f.app, adapter)
	args := submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey}

	// job.ID is CONSTANT across retries of "the same" River job -- exactly the shape that
	// makes queue.OncePerJob's "job:<jobID>" marker collide if the mid-budget branch were
	// ever wrapped in it.
	if err := w.Work(ctx, newSubmitJob(1, 1, 8, args)); err == nil {
		t.Fatal("first Retryable attempt returned nil, want the original error")
	}
	if err := w.Work(ctx, newSubmitJob(1, 2, 8, args)); err == nil {
		t.Fatal("second Retryable attempt returned nil, want the original error")
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "queued" {
		t.Errorf("job state after two mid-budget retries = %q, want \"queued\"", wj.state)
	}
	if wj.attempts != 2 {
		t.Errorf("job attempts after two mid-budget retries = %d, want 2 -- if this is 1, the "+
			"second attempt's bookkeeping silently no-opped", wj.attempts)
	}
	if wj.lastError == nil || *wj.lastError != "wsub: transient #2" {
		t.Errorf("job last_error = %v, want the SECOND attempt's error message", wj.lastError)
	}
	exchanges := wjExchanges(t, f, tenantID, wj.id)
	if len(exchanges) != 2 {
		t.Fatalf("app_exchange rows after two mid-budget retries = %d, want exactly 2 -- if this "+
			"is 1, the second attempt's evidence was silently dropped", len(exchanges))
	}
	if exchanges[0].attempt != 1 || exchanges[1].attempt != 2 {
		t.Errorf("app_exchange.attempt values = {%d,%d}, want {1,2}", exchanges[0].attempt, exchanges[1].attempt)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "queued" {
		t.Errorf("invoice status = %q, want unchanged \"queued\"", inv.status)
	}
}

// --- 3: dead-letter boundary off-by-one ----------------------------------------------------

func TestSubmitWorker_RetryableOneShortOfFinalStaysMidBudget(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Retryable{Err: errors.New("wsub: upstream 503, one short of final")},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	// job.Attempt == MaxAttempts-1: the LAST attempt where dead-lettering must NOT fire.
	job := newSubmitJob(1, 7, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	err := w.Work(ctx, job)
	if err == nil {
		t.Fatal("Work on a one-short-of-final Retryable returned nil, want the original error")
	}
	var cancelErr *river.JobCancelError
	if errors.As(err, &cancelErr) {
		t.Error("Work returned river.JobCancelError one attempt short of final -- that stops retries prematurely")
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "queued" {
		t.Errorf("job state one attempt short of final = %q, want \"queued\" (still mid-budget), "+
			"not \"dead_lettered\" -- off-by-one on the dead-letter boundary", wj.state)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "queued" {
		t.Errorf("invoice status = %q, want unchanged \"queued\"", inv.status)
	}
}

// --- 4: unknown Result variant (nil Result hits the default branch) -----------------------

func TestSubmitWorker_UnknownResultVariantRollsBackAndErrors(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   nil, // no Accepted/Rejected/Pending/Retryable -- the type switch's default case
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	err := w.Work(ctx, job)
	if err == nil {
		t.Fatal("Work on a nil Result returned nil, want a non-nil error from the default branch")
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "submitting" {
		t.Errorf("job state after a nil Result = %q, want unchanged \"submitting\" (tx2 must have "+
			"rolled back entirely)", wj.state)
	}
	if n := exCountRows(t, f, tenantID, wj.id); n != 0 {
		t.Errorf("app_exchange rows after a nil Result = %d, want 0", n)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "queued" {
		t.Errorf("invoice status after a nil Result = %q, want unchanged \"queued\"", inv.status)
	}
}

// --- 5: RecordExchange failure mid-tx2 rolls back the WHOLE transaction, including the
//        OncePerJob marker, leaving the job re-runnable -------------------------------------

func TestSubmitWorker_RecordExchangeFailureMidTx2RollsBackAndIsRerunnable(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	badLatency := -1 // violates app_exchange_latency_ms_check (latency_ms IS NULL OR >= 0)
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Accepted{IRN: "NG-1", CSID: "C", QRPayload: "Q"},
		evidence: submission.Evidence{ReachedWire: true, LatencyMS: &badLatency},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	err := w.Work(ctx, job)
	if err == nil {
		t.Fatal("Work with an evidence value that violates a CHECK returned nil, want the DB error")
	}
	if code := exPgCode(err); code != "23514" {
		t.Errorf("Work error SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "submitting" {
		t.Errorf("job state after the failed RecordExchange = %q, want unchanged \"submitting\" -- "+
			"the whole tx2, including the OncePerJob marker, must have rolled back", wj.state)
	}
	if n := exCountRows(t, f, tenantID, wj.id); n != 0 {
		t.Errorf("app_exchange rows after the failed RecordExchange = %d, want 0", n)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "queued" {
		t.Errorf("invoice status after the failed RecordExchange = %q, want unchanged \"queued\"", inv.status)
	}

	// Re-run with corrected evidence, same job.ID: this only proves anything if the marker
	// genuinely rolled back above -- a leaked marker would make this attempt a silent no-op.
	adapter.mu.Lock()
	adapter.submitQueue = []scriptedOutcome{{
		result:   submission.Accepted{IRN: "NG-1", CSID: "C", QRPayload: "Q"},
		evidence: submission.Evidence{ReachedWire: true},
	}}
	adapter.submitCalls = 0
	adapter.mu.Unlock()

	if err := w.Work(ctx, job); err != nil {
		t.Fatalf("Work re-run with corrected evidence: %v", err)
	}
	wj2 := wjRequire(t, f, tenantID, idemKey)
	if wj2.state != "accepted" {
		t.Errorf("job state after re-run = %q, want \"accepted\"", wj2.state)
	}
	if n := exCountRows(t, f, tenantID, wj.id); n != 1 {
		t.Errorf("app_exchange rows after re-run = %d, want exactly 1", n)
	}
	inv2 := wiRead(t, f, tenantID, invoiceID)
	if inv2.status != "submitted" {
		t.Errorf("invoice status after re-run = %q, want \"submitted\"", inv2.status)
	}
}

// --- 6: concurrent redelivery of the SAME job.ID -- the FOR UPDATE lock does not span the
//        adapter call, by design ([adapters-are-db-free]) -----------------------------------

// racingAdapter lets two concurrent Submit calls rendezvous deterministically: the first
// caller in blocks until the second caller has also entered Submit, proving the second
// caller's tx1 ran (and committed) AFTER the first's tx1 committed state='submitting' --
// exactly the window a River redelivery race would occupy in production.
type racingAdapter struct {
	*refAdapter
	mu      sync.Mutex
	n       int
	release chan struct{}
}

func newRacingAdapter(result submission.Result, evidence submission.Evidence) *racingAdapter {
	return &racingAdapter{
		refAdapter: &refAdapter{name: "reference", version: "v1", submitResult: result, submitEvidence: evidence},
		release:    make(chan struct{}),
	}
}

func (a *racingAdapter) Submit(ctx context.Context, w submission.Wire, idemKey string) (submission.Result, submission.Evidence) {
	a.mu.Lock()
	a.n++
	first := a.n == 1
	a.mu.Unlock()
	if first {
		<-a.release // block until the second concurrent caller has also entered Submit
	} else {
		close(a.release) // unblock the first
	}
	return a.refAdapter.Submit(ctx, w, idemKey)
}

func (a *racingAdapter) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

func TestSubmitWorker_ConcurrentRedeliveryBothReachSubmit(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newRacingAdapter(
		submission.Accepted{IRN: "NG-RACE", CSID: "C", QRPayload: "Q"},
		submission.Evidence{ReachedWire: true},
	)
	w := newTestWorker(f.app, adapter)
	// SAME job.ID for both calls: a redelivery of "the same" River job, not two different jobs.
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = w.Work(ctx, job) }()
	go func() { defer wg.Done(); errs[1] = w.Work(ctx, job) }()
	wg.Wait()

	// FINDING (report, not a fix): the row lock tx1 takes is released at tx1's commit, BEFORE
	// the adapter call ([adapters-are-db-free], T05-15) -- so a genuine concurrent redelivery
	// of the SAME job.ID reaches Adapter.Submit TWICE. In production this is the slice River's
	// own FOR UPDATE SKIP LOCKED single-delivery guarantee is what actually prevents (the
	// Stage-2 architect validation's item 2 already names the adjacent "crash before tx2"
	// residual); this test demonstrates the worker's OWN defenses stop at the DB-write layer.
	if got := adapter.calls(); got != 2 {
		t.Errorf("adapter.Submit call count under concurrent redelivery = %d, want 2 (both callers "+
			"reach the adapter -- documenting the residual, not asserting it away)", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("Work call %d returned %v, want nil (OncePerJob absorbs the loser silently)", i, err)
		}
	}

	// The DB-side outcome must still be exactly-once, regardless of the double adapter call.
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Errorf("job state after concurrent redelivery = %q, want \"accepted\"", wj.state)
	}
	if n := exCountRows(t, f, tenantID, wj.id); n != 1 {
		t.Errorf("app_exchange rows after concurrent redelivery = %d, want exactly 1 -- OncePerJob "+
			"must have absorbed the loser's write", n)
	}
	hist := wiHistory(t, f, tenantID, invoiceID)
	if len(hist) != 1 {
		t.Errorf("invoice_status_history rows after concurrent redelivery = %d, want exactly 1", len(hist))
	}
}

// --- 7: invoice deleted before the job row is ever created ---------------------------------

func TestSubmitWorker_InvoiceDeletedBeforeFirstAttemptFailsClosed(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	// Delete the invoice as the migrator (bypassing app-level status-machine rules) before
	// Work ever runs -- simulates a race between enqueue and work where the invoice record
	// itself is gone by the time the job row would first be created.
	if err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceID)
		return e
	}); err != nil {
		t.Fatalf("delete invoice ahead of Work: %v", err)
	}

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter() // Submit must never fire
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	err := w.Work(ctx, job)
	if err == nil {
		t.Fatal("Work naming a deleted invoice returned nil, want the composite FK violation surfaced")
	}
	if code := exPgCode(err); code != "23503" {
		t.Errorf("Work error SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	var cancelErr *river.JobCancelError
	if errors.As(err, &cancelErr) {
		t.Error("Work returned river.JobCancelError for a deleted invoice -- want a plain error so River retries normally")
	}
	if got := adapter.calls(); got != 0 {
		t.Errorf("adapter.Submit call count = %d, want 0", got)
	}
	if n := wjCount(t, f, tenantID, idemKey); n != 0 {
		t.Errorf("submission_jobs rows for a deleted invoice = %d, want 0 (tx1 must have rolled back)", n)
	}
}

// --- 8: Accepted with a blank IRN -- the worker doesn't validate it (M5-05's job) ----------

func TestSubmitWorker_AcceptedWithBlankIRNStillMovesInvoiceToSubmitted(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	// Accepted.IRN required and non-blank is an ADAPTER law (L07, M5-02-06's contract suite);
	// this double deliberately violates it to prove the WORKER itself never inspects IRN.
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Accepted{IRN: "", CSID: "", QRPayload: ""},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Fatalf("Work on an Accepted with a blank IRN: %v", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Errorf("job state = %q, want \"accepted\" -- the worker does not validate IRN content", wj.state)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "submitted" {
		t.Errorf("invoice status = %q, want \"submitted\"", inv.status)
	}
	if inv.irn != nil {
		t.Errorf("invoices.irn = %q, want NULL -- this worker never writes it regardless of IRN content", *inv.irn)
	}
}
