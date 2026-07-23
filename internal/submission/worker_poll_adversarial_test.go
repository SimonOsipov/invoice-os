// worker_poll_adversarial_test.go: QA Mode B adversarial coverage for M5-04-06 (task-234),
// beyond the nine AC-derived Test Specs (T06-1..T06-9) plus GAP-2 already GREEN in
// worker_poll_test.go. Reuses that file's helpers (newTestPollWorker, newPollJob,
// newQueueClient, pjScheduledAts, pjPollExchanges, pjNextPollAt, mockPendingTIN) and
// submit_worker_test.go's (seedQueuedInvoice, newTestWorker, newSubmitJob, wjRequire,
// wiRead, scriptedAdapter, newScriptedAdapter) rather than duplicating them -- same package
// (submission_test), same suite. No new writer function beyond racingPollAdapter (the
// Poll-side mirror of submit_worker_adversarial_test.go's own racingAdapter), no testify, no
// t.Skip beyond requireExchangeDB's established gate.
//
// Seven cases, motivated by the M5-04-06 QA (Stage 4, Mode B) mutation-testing pass:
//
//  1. TestPollWorker_ThreeHopConvergence -- [unbounded-poll-chain] is deliberately uncapped;
//     the ONLY spec that exercises multi-hop convergence (T06-5) does so against the real
//     M5-03 mock adapter, which happens to converge in exactly two hops. A worker that
//     silently capped hop count at 2 (an off-by-one baked into a "the mock only needs two"
//     assumption) would still pass every existing spec. This scripts Pending -> Pending ->
//     Pending -> Accepted (three hops) and asserts convergence plus three distinct outbox
//     keys, kept as a PERMANENT regression spec per the QA charter's Part 1 instruction.
//  2. TestPollWorker_RejectedLeavesInvoiceSubmitted -- T06-9 only exercises the Accepted
//     branch's exchange bookkeeping; nothing in the existing suite drives a POLL Rejected
//     outcome at all. Mirrors [reaching-the-app-means-a-verdict]'s submit-side shape
//     (T05-4/TestSubmitWorker_RejectedDoesNotMoveInvoiceToRejected) for the poll side: the
//     invoice was already moved to 'submitted' by the ORIGINAL Pending submit, and a later
//     poll's Rejected must not touch it further (persisting rejection_reasons is M5-05's job,
//     out of scope here exactly like Accepted's IRN/QR).
//  3. TestPollWorker_ConcurrentPollsForSameJobBothNoOpCleanly -- the QA charter's own prompt:
//     tx1's row lock is released BEFORE adapter.Poll runs ([adapters-are-db-free]), so two
//     concurrent deliveries of the SAME poll job (a River redelivery race, mirroring
//     submit_worker_adversarial_test.go's own TestSubmitWorker_ConcurrentRedeliveryBothReachSubmit)
//     both reach the adapter -- but queue.OncePerJob (keyed by job.ID, identical for a
//     redelivery of "the same" job) must still keep the DB-side outcome exactly-once.
//  4. TestPollWorker_PollAfterFarInFutureIsHonouredExactly -- T06-1's own PollAfter is 90
//     minutes out; this asserts a PollAfter 45 days out (an adapter that genuinely means "come
//     back next month") round-trips through river_job.scheduled_at with no silent clamp or
//     cap -- the natural adversarial complement to [unbounded-poll-chain]'s "no hop ceiling":
//     there must also be no SCHEDULE ceiling.
//  5. TestPollWorker_DeadLetterWhenInvoiceAlreadyFailedIsIdempotent -- invoice_port.go's own
//     doc comment promises "a redundant call on an already-failed invoice returns nil"; T06-8a
//     never exercises that path (its invoice is 'submitted', not already 'failed', when the
//     dead-letter fires). This forces the invoice to already be 'failed' by the time a poll's
//     final-attempt Retryable dead-letters the job, and asserts MarkFailed's idempotent
//     no-op still lets the job-side write commit (no invoice_status_history row added; the
//     job still transitions to dead_lettered).
//  6. TestPollWorker_AcceptedDoesNotSmuggleM505Scope -- the poll-side mirror of
//     submit_worker_test.go's own T05-3 (TestSubmitWorker_AcceptedDoesNotSmuggleM505Scope):
//     nothing in worker_poll_test.go's own T06-5/T06-9 (the two specs that DO drive a poll
//     Accepted) ever reads invoices.irn/csid/qr_payload back, so a worker that started writing
//     Accepted.IRN itself (persisting IRN/QR is M5-05's job per this subtask's own Out of
//     Scope list) would pass every existing spec silently.
//  7. TestPollWorker_OutboxKeyReplayEnqueuesExactlyOnePollJob -- T06-6 asserts the replayed
//     hop's idempotency_keys count stays at 1; this asserts the river_job SIDE stays at
//     exactly one scheduled row for that hop too (the actual queue-depth consequence the
//     idempotency_keys count is a proxy for).
package submission_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// --- 1: three-hop convergence (permanent regression spec for [unbounded-poll-chain]) ------

func TestPollWorker_ThreeHopConvergence(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	base := time.Now()
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "r1", PollAfter: base.Add(time.Hour)},
		evidence: submission.Evidence{ReachedWire: true},
	})
	sw := newTestWorker(f.app, adapter)
	if err := sw.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})); err != nil {
		t.Fatalf("submit to pending: %v", err)
	}
	jobID := wjRequire(t, f, tenantID, idemKey).id

	hop2At := base.Add(2 * time.Hour)
	hop3At := base.Add(3 * time.Hour)
	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Pending{Ref: "r2", PollAfter: hop2At}, evidence: submission.Evidence{ReachedWire: true}},
		{result: submission.Pending{Ref: "r3", PollAfter: hop3At}, evidence: submission.Evidence{ReachedWire: true}},
		{result: submission.Accepted{IRN: "NG-3HOP", CSID: "C", QRPayload: "Q"}, evidence: submission.Evidence{ReachedWire: true}},
	}

	pw := newTestPollWorker(f.app, adapter)
	for seq, id := range []int64{10, 11, 12} {
		if err := pw.Work(ctx, newPollJob(id, 1, 8, submission.PollArgs{
			TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: seq + 1,
		})); err != nil {
			t.Fatalf("poll hop %d: %v", seq+1, err)
		}
	}

	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "accepted" {
		t.Errorf("job state after three poll hops = %q, want \"accepted\" -- a hidden hop ceiling "+
			"would leave this stuck at \"pending\"", wj.state)
	}
	if got := adapter.pollRefs; len(got) != 3 || got[0] != "r1" || got[1] != "r2" || got[2] != "r3" {
		t.Errorf("adapter.Poll refs in call order = %v, want [\"r1\" \"r2\" \"r3\"] -- each hop must "+
			"supersede the previous ticket, never re-polling an earlier one", got)
	}
	for seq, want := range map[int]time.Time{1: base.Add(time.Hour), 2: hop2At, 3: hop3At} {
		rows := pjScheduledAts(t, f.app, jobID, seq)
		if len(rows) != 1 {
			t.Fatalf("river_job rows for hop %d = %d, want exactly 1", seq, len(rows))
		}
		if diff := rows[0].Sub(want); diff < -time.Millisecond || diff > time.Millisecond {
			t.Errorf("hop %d scheduled_at = %s, want %s", seq, rows[0], want)
		}
		key := fmt.Sprintf("poll:%s:%d", jobID, seq)
		if n := countKeys(t, f.app, tenantID, key); n != 1 {
			t.Errorf("idempotency_keys for %q = %d, want 1 -- hop %d must own a DISTINCT outbox key", key, n, seq)
		}
	}
}

// --- 2: Rejected leaves the invoice at 'submitted' -----------------------------------------

func TestPollWorker_RejectedLeavesInvoiceSubmitted(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	future := time.Now().Add(time.Hour)
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "r1", PollAfter: future},
		evidence: submission.Evidence{ReachedWire: true},
	})
	sw := newTestWorker(f.app, adapter)
	if err := sw.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})); err != nil {
		t.Fatalf("submit to pending: %v", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)

	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Rejected{Reasons: []submission.Reason{{Code: "L01", Message: "bad TIN"}}},
			evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	if err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll to Rejected: %v", err)
	}

	wj2 := wjRequire(t, f, tenantID, idemKey)
	if wj2.state != "rejected" {
		t.Errorf("job state after a poll Rejected = %q, want \"rejected\"", wj2.state)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "submitted" {
		t.Errorf("invoice status after a poll Rejected = %q, want unchanged \"submitted\" "+
			"(persisting rejection_reasons is M5-05's job)", inv.status)
	}
	rows := pjPollExchanges(t, f, tenantID, wj.id)
	var pollRows int
	for _, r := range rows {
		if r.operation == "poll" {
			pollRows++
		}
	}
	if pollRows != 1 {
		t.Errorf("app_exchange rows with operation='poll' = %d, want exactly 1", pollRows)
	}
}

// --- 3: two concurrent deliveries of the SAME poll job both reach the adapter, but the ------
// --- DB-side outcome stays exactly-once (mirrors submit_worker_adversarial_test.go's own ---
// --- TestSubmitWorker_ConcurrentRedeliveryBothReachSubmit for the poll side) ----------------

// racingPollAdapter wraps refAdapter with a synchronized Poll: the first caller blocks until
// the second has also entered Poll, guaranteeing genuine overlap rather than an accidental
// happens-before ordering the Go scheduler could otherwise hide.
type racingPollAdapter struct {
	*refAdapter
	mu      sync.Mutex
	n       int
	release chan struct{}
	result  submission.Result
	ev      submission.Evidence
}

func newRacingPollAdapter(result submission.Result, ev submission.Evidence) *racingPollAdapter {
	return &racingPollAdapter{
		refAdapter: &refAdapter{name: "reference", version: "v1"},
		release:    make(chan struct{}),
		result:     result,
		ev:         ev,
	}
}

func (a *racingPollAdapter) Poll(ctx context.Context, ref submission.Ref) (submission.Result, submission.Evidence) {
	a.mu.Lock()
	a.n++
	first := a.n == 1
	a.mu.Unlock()
	if first {
		<-a.release
	} else {
		close(a.release)
	}
	return a.result, a.ev
}

func (a *racingPollAdapter) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

func TestPollWorker_ConcurrentPollsForSameJobBothNoOpCleanly(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	future := time.Now().Add(time.Hour)
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	submitAdapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "r1", PollAfter: future},
		evidence: submission.Evidence{ReachedWire: true},
	})
	sw := newTestWorker(f.app, submitAdapter)
	if err := sw.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})); err != nil {
		t.Fatalf("submit to pending: %v", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)

	adapter := newRacingPollAdapter(
		submission.Accepted{IRN: "NG-RACE-POLL", CSID: "C", QRPayload: "Q"},
		submission.Evidence{ReachedWire: true},
	)
	pw := &submission.PollWorker{
		Pool:        f.app,
		Adapter:     adapter,
		InvoicePort: testInvoicePort{},
		Queue:       newQueueClient(f.app),
	}
	// SAME job.ID for both calls -- a redelivery of "the same" River poll job, exactly the
	// SubmitWorker precedent's own shape.
	job := newPollJob(10, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = pw.Work(ctx, job) }()
	go func() { defer wg.Done(); errs[1] = pw.Work(ctx, job) }()
	wg.Wait()

	// FINDING (report, not a fix): identical residual to SubmitWorker's own -- tx1's row lock
	// is released before adapter.Poll runs ([adapters-are-db-free]), so a genuine concurrent
	// redelivery of the SAME poll job.ID reaches Adapter.Poll TWICE. River's own single-
	// delivery guarantee is what actually prevents this in production.
	if got := adapter.calls(); got != 2 {
		t.Errorf("adapter.Poll call count under concurrent redelivery = %d, want 2 (both callers "+
			"reach the adapter -- documenting the residual, not asserting it away)", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("poll Work call %d returned %v, want nil (OncePerJob absorbs the loser silently)", i, err)
		}
	}

	wj2 := wjRequire(t, f, tenantID, idemKey)
	if wj2.state != "accepted" {
		t.Errorf("job state after concurrent poll redelivery = %q, want \"accepted\"", wj2.state)
	}
	if n := exCountRows(t, f, tenantID, wj.id); n != 2 { // 1 submit row + 1 poll row
		t.Errorf("app_exchange rows after concurrent poll redelivery = %d, want exactly 2 "+
			"(1 submit + 1 poll) -- OncePerJob must have absorbed the loser's write", n)
	}
}

// --- 4: a PollAfter far in the future is honoured exactly, no silent clamp -----------------

func TestPollWorker_PollAfterFarInFutureIsHonouredExactly(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	farFuture := time.Now().Add(45 * 24 * time.Hour) // 45 days out
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "r1", PollAfter: farFuture},
		evidence: submission.Evidence{ReachedWire: true},
	})
	sw := newTestWorker(f.app, adapter)
	if err := sw.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})); err != nil {
		t.Fatalf("submit to pending: %v", err)
	}

	got := pjNextPollAt(t, f, tenantID, idemKey)
	if diff := got.Sub(farFuture); diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("next_poll_at = %s, want %s (PollAfter 45 days out, honoured exactly -- no "+
			"silent clamp or cap on a far-future schedule)", got, farFuture)
	}
	jobID := wjRequire(t, f, tenantID, idemKey).id
	rows := pjScheduledAts(t, f.app, jobID, 1)
	if len(rows) != 1 {
		t.Fatalf("river_job rows for hop 1 = %d, want exactly 1", len(rows))
	}
	if diff := rows[0].Sub(farFuture); diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("river_job.scheduled_at = %s, want %s (same far-future PollAfter)", rows[0], farFuture)
	}
}

// --- 5: dead-letter when the invoice is already 'failed' is an idempotent no-op -----------

func TestPollWorker_DeadLetterWhenInvoiceAlreadyFailedIsIdempotent(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	future := time.Now().Add(time.Hour)
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "r1", PollAfter: future},
		evidence: submission.Evidence{ReachedWire: true},
	})
	sw := newTestWorker(f.app, adapter)
	if err := sw.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})); err != nil {
		t.Fatalf("submit to pending: %v", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)

	// Force the invoice to already be 'failed' -- e.g. an operator-driven override, or another
	// delivery's dead-letter that already fired -- BEFORE this poll's own dead-letter runs.
	// A raw UPDATE (bypassing the worker) is deliberate: this fixture only needs the invoice
	// AT 'failed', not a realistic path there.
	err := db.WithinTenantTx(ctx, f.mig, tenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE invoices SET status = 'failed' WHERE id = $1`, invoiceID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
			 VALUES ($1, $2, 'submitted', 'failed', 'system')`,
			tenantID, invoiceID)
		return e
	})
	if err != nil {
		t.Fatalf("force invoice to failed: %v", err)
	}
	preHistory := len(wiHistory(t, f, tenantID, invoiceID))

	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Retryable{Err: errors.New("wsub: poll upstream 503, final attempt (adversarial)")},
			evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	job := newPollJob(10, 8, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1}) // final attempt

	if err := pw.Work(ctx, job); err == nil {
		t.Error("PollWorker.Work on a final-attempt Retryable returned nil, want a non-nil error so River discards the job")
	}

	wj2 := wjRequire(t, f, tenantID, idemKey)
	if wj2.state != "dead_lettered" {
		t.Errorf("job state = %q, want \"dead_lettered\" -- the job-side write must still commit "+
			"even though the invoice-side MarkFailed is a no-op", wj2.state)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "failed" {
		t.Errorf("invoice status = %q, want unchanged \"failed\"", inv.status)
	}
	if got := len(wiHistory(t, f, tenantID, invoiceID)); got != preHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d -- MarkFailed's idempotent "+
			"no-op (invoice_port.go: \"a redundant call on an already-failed invoice returns nil\") "+
			"must not add a second history row", got, preHistory)
	}
}

// --- 6: a poll Accepted does not smuggle M5-05 scope (IRN/QR persistence) ------------------

func TestPollWorker_AcceptedDoesNotSmuggleM505Scope(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	future := time.Now().Add(time.Hour)
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "r1", PollAfter: future},
		evidence: submission.Evidence{ReachedWire: true},
	})
	sw := newTestWorker(f.app, adapter)
	if err := sw.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})); err != nil {
		t.Fatalf("submit to pending: %v", err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)

	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Accepted{IRN: "NG-SMUGGLE", CSID: "CSID-SMUGGLE", QRPayload: "QR-SMUGGLE"},
			evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	// A t.Fatalf (not Errorf) here is load-bearing, mirroring T05-3's own rationale exactly:
	// without it, a broken Work would just leave irn/rejection_reasons untouched and this test
	// would pass vacuously for the wrong reason.
	if err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll to Accepted: %v", err)
	}

	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.irn != nil {
		t.Errorf("invoices.irn = %q, want NULL -- storing IRN is M5-05's job, not this worker's", *inv.irn)
	}
	if inv.rejectionReasons != "[]" {
		t.Errorf("invoices.rejection_reasons = %s, want \"[]\" -- this worker never writes it", inv.rejectionReasons)
	}
}

// --- 7: a replayed outbox key enqueues no SECOND river_job row -----------------------------

func TestPollWorker_OutboxKeyReplayEnqueuesExactlyOnePollJob(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	future := time.Now().Add(time.Hour)
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "r1", PollAfter: future},
		evidence: submission.Evidence{ReachedWire: true},
	})
	sw := newTestWorker(f.app, adapter)
	if err := sw.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})); err != nil {
		t.Fatalf("submit to pending: %v", err)
	}
	jobID := wjRequire(t, f, tenantID, idemKey).id

	hop2At := future.Add(time.Hour)
	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Pending{Ref: "r2", PollAfter: hop2At}, evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	if err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll hop 1: %v", err)
	}

	if n := len(pjScheduledAts(t, f.app, jobID, 2)); n != 1 {
		t.Fatalf("river_job rows for hop 2 before any replay = %d, want exactly 1", n)
	}

	// Replay hop 2's own outbox key directly through EnqueueTx -- exercising the SAME
	// dedupe path a duplicate River delivery of PollWorker's own Pending branch would hit.
	q := newQueueClient(f.app)
	key2 := "poll:" + jobID + ":2"
	var skipped bool
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		s, e := q.EnqueueTx(ctx, tx, tenantID, key2,
			submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 2},
			&river.InsertOpts{ScheduledAt: hop2At})
		skipped = s
		return e
	})
	if err != nil {
		t.Fatalf("replay hop 2's own outbox key: %v", err)
	}
	if !skipped {
		t.Error("replaying hop 2's own outbox key was NOT skipped -- want the duplicate business key refused")
	}
	if n := len(pjScheduledAts(t, f.app, jobID, 2)); n != 1 {
		t.Errorf("river_job rows for hop 2 after a replay = %d, want still exactly 1 (unchanged)", n)
	}
}
