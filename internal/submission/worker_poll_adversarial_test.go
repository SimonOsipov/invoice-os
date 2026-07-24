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
// Eleven cases, motivated by the M5-04-06 QA (Stage 4, Mode B) mutation-testing pass
// (1-7) plus M5-05-05 (task-241)'s own Test Specs table (8-11):
//
//  1. TestPollWorker_ThreeHopConvergence -- [unbounded-poll-chain] is deliberately uncapped;
//     the ONLY spec that exercises multi-hop convergence (T06-5) does so against the real
//     M5-03 mock adapter, which happens to converge in exactly two hops. A worker that
//     silently capped hop count at 2 (an off-by-one baked into a "the mock only needs two"
//     assumption) would still pass every existing spec. This scripts Pending -> Pending ->
//     Pending -> Accepted (three hops) and asserts convergence plus three distinct outbox
//     keys, kept as a PERMANENT regression spec per the QA charter's Part 1 instruction.
//  2. TestPollWorker_RejectedMovesInvoiceToRejected -- inverted by M5-05-05 (task-241,
//     register #17): the invoice was already moved to 'submitted' by the ORIGINAL Pending
//     submit, and a later poll's Rejected now drives it the rest of the way to 'rejected' via
//     InvoicePort.MarkRejected, plus exactly one submission.rejected audit row (reference key
//     ABSENT, [audit-reference-is-the-irn]) and zero submission.accepted rows -- mirroring
//     [reaching-the-app-means-a-verdict]'s submit-side shape
//     (T05-4/TestSubmitWorker_RejectedMovesInvoiceToRejected) for the poll side. Column
//     persistence of rejection_reasons itself is untestable through this package's plain
//     testInvoicePort fake ([mapper-lives-in-03]; see this file's own header) -- proven
//     instead by internal/invoice/system_actor_test.go (M5-05-03/task-239).
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
//  6. TestPollWorker_AcceptedRoutesVerdictAndAudits -- repurposed by M5-05-05 (task-241,
//     register #18): TestPollWorker_AcceptedDoesNotSmuggleM505Scope's original premise --
//     "the worker must NOT write the outcome" -- is now OBSOLETE, mirroring the submit side's
//     own T05-3 repurpose (M5-05-04/task-240, submit_worker_test.go). Repurposed into a
//     POSITIVE proof: a poll Accepted drives the invoice the rest of the way to 'accepted' via
//     InvoicePort.MarkAccepted, plus exactly one submission.accepted audit row whose payload
//     "reference" equals the scripted IRN, and zero submission.rejected rows -- proving the
//     worker forwarded the REAL adapter verdict, not a hardcoded literal. Column persistence
//     of irn/csid/qr_payload itself is untestable through this package's plain testInvoicePort
//     fake (it only writes status) -- proven instead by internal/invoice/system_actor_test.go
//     (M5-05-03/task-239).
//  7. TestPollWorker_OutboxKeyReplayEnqueuesExactlyOnePollJob -- T06-6 asserts the replayed
//     hop's idempotency_keys count stays at 1; this asserts the river_job SIDE stays at
//     exactly one scheduled row for that hop too (the actual queue-depth consequence the
//     idempotency_keys count is a proxy for).
//  8. TestPollWorker_RejectedWritesExactlyOneSubmissionRejectedAuditRow -- net-new poll-side
//     audit coverage mirroring TestSubmitWorker_RejectedWritesExactlyOneSubmissionRejectedAuditRow
//     (submit_worker_test.go): a poll Rejected writes exactly one submission.rejected audit
//     row whose payload is {invoice_id, submission_job_id, outcome}, reference key ABSENT.
//  9. TestPollWorker_AcceptedAuditPayloadIsStrictSummaryNoWireLeak /
//     TestPollWorker_RejectedAuditPayloadIsStrictSummaryNoWireLeak -- the poll-side mirror of
//     submit_worker_adversarial_test.go's own strict-summary pair (case 10 there): the audit
//     payload's key SET is exactly {invoice_id, submission_job_id, outcome[, reference]}, no
//     CSID/QR/reasons-array leak, and 05 (app_exchange) + 08 (audit_log) both fire from the
//     SAME poll verdict.
//  10. TestPollWorker_ConcurrentPollsForSameJobBothNoOpCleanly (extended) -- the existing
//      OncePerJob concurrent-redelivery case above (case 3) now also asserts the audit write
//      stays exactly-once under the same race, mirroring
//      TestSubmitWorker_ConcurrentRedeliveryBothReachSubmit's own audit assertion
//      (submit_worker_adversarial_test.go).
//  11. TestPollWorker_PendingHopWritesNoVerdictAudit -- a poll hop that comes back Pending
//      again (not yet terminal) must write ZERO submission.accepted/submission.rejected rows
//      and leave the invoice at 'submitted' -- the audit write is scoped to the
//      Accepted/Rejected branches only, never Pending.
//
// All of M5-05-05 (task-241)'s own doc-comment gate sites (register rows #17-#20) are
// resolved in this file's edits above; worker.go's own stale sentence (:394-395) is the
// executor's Stage-3 rewrite, out of scope here.
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

func TestPollWorker_RejectedMovesInvoiceToRejected(t *testing.T) {
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

	beforeAccepted := auditCount(t, f, tenantID, "submission.accepted")
	beforeRejected := auditCount(t, f, tenantID, "submission.rejected")

	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Rejected{Reasons: []submission.Reason{{Code: "L01", Message: "bad TIN"}}},
			evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	// M5-05-05 (task-241): a t.Fatalf (not Errorf) here is load-bearing, mirroring
	// TestSubmitWorker_AcceptedRoutesVerdictAndAudits' own rationale (submit_worker_test.go):
	// every assertion below reads state Work itself must have written.
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
	if inv.status != "rejected" {
		t.Errorf("invoice status after a poll Rejected = %q, want \"rejected\" -- "+
			"M5-05-05 (task-241): the deferred poll path now drives submitted -> rejected via "+
			"InvoicePort.MarkRejected, mirroring the submit side's own edge", inv.status)
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

	if n := auditCount(t, f, tenantID, "submission.rejected"); n != beforeRejected+1 {
		t.Errorf("submission.rejected audit rows = %d, want %d (exactly one new row)", n, beforeRejected+1)
	}
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != beforeAccepted {
		t.Errorf("submission.accepted audit rows = %d, want unchanged %d -- a poll Rejected must never write an accepted row", n, beforeAccepted)
	}
	payload := auditPayloadMap(t, f, tenantID, "submission.rejected")
	if _, ok := payload["reference"]; ok {
		t.Errorf("submission.rejected payload carries a \"reference\" key (value %v), want it ABSENT -- "+
			"[audit-reference-is-the-irn]: Rejected has no IRN", payload["reference"])
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
	// M5-05-05 (task-241): the audit write is the closure's own LAST statement inside
	// OncePerJob, so it must be exactly-once under the same race the exchange/job-state rows
	// already prove -- not a second, unguarded write appended after OncePerJob returns
	// (mirrors TestSubmitWorker_ConcurrentRedeliveryBothReachSubmit,
	// submit_worker_adversarial_test.go).
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != 1 {
		t.Errorf("submission.accepted audit rows after concurrent poll redelivery = %d, want exactly 1 -- "+
			"OncePerJob must have absorbed the loser's audit write too", n)
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

// --- 6: a poll Accepted routes the verdict and writes the 08 audit event ------------------
// (repurposed by M5-05-05, task-241, register #18 -- see this file's own header) ------------

func TestPollWorker_AcceptedRoutesVerdictAndAudits(t *testing.T) {
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

	const distinctIRN = "IRN-POLL-77" // distinctive, not "NG-1"/"NG-RACE-POLL" reused elsewhere --
	// guards against a coincidental match to a hardcoded/constant reference.
	beforeAccepted := auditCount(t, f, tenantID, "submission.accepted")
	beforeRejected := auditCount(t, f, tenantID, "submission.rejected")

	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Accepted{IRN: distinctIRN, CSID: "CSID-POLL-77", QRPayload: "QR-POLL-77"},
			evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	// M5-05-05 (task-241): a t.Fatalf (not Errorf) here is load-bearing, mirroring
	// TestSubmitWorker_AcceptedRoutesVerdictAndAudits' own rationale (submit_worker_test.go):
	// every assertion below reads state Work itself must have written, so a failed Work makes
	// the rest meaningless.
	if err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll to Accepted: %v", err)
	}

	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "accepted" {
		t.Errorf("invoice status after a poll Accepted = %q, want \"accepted\" -- "+
			"M5-05-05 (task-241): the deferred poll path now drives submitted -> accepted via "+
			"InvoicePort.MarkAccepted", inv.status)
	}
	if inv.rejectionReasons != "[]" {
		t.Errorf("invoices.rejection_reasons = %s, want \"[]\" -- this worker never writes it", inv.rejectionReasons)
	}

	if n := auditCount(t, f, tenantID, "submission.accepted"); n != beforeAccepted+1 {
		t.Errorf("submission.accepted audit rows = %d, want %d (exactly one new row)", n, beforeAccepted+1)
	}
	if n := auditCount(t, f, tenantID, "submission.rejected"); n != beforeRejected {
		t.Errorf("submission.rejected audit rows = %d, want unchanged %d -- a poll Accepted must never write a rejected row", n, beforeRejected)
	}
	payload := auditPayloadMap(t, f, tenantID, "submission.accepted")
	if payload["invoice_id"] != invoiceID {
		t.Errorf("submission.accepted payload invoice_id = %v, want %q", payload["invoice_id"], invoiceID)
	}
	if payload["submission_job_id"] != wj.id {
		t.Errorf("submission.accepted payload submission_job_id = %v, want %q", payload["submission_job_id"], wj.id)
	}
	if payload["outcome"] != "accepted" {
		t.Errorf("submission.accepted payload outcome = %v, want \"accepted\"", payload["outcome"])
	}
	if payload["reference"] != distinctIRN {
		t.Errorf("submission.accepted payload reference = %v, want %q (the scripted IRN) -- "+
			"proves the worker forwarded the real adapter verdict, not a hardcoded literal", payload["reference"], distinctIRN)
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

// --- 8-11: net-new poll-side audit coverage, mirroring the submit-side templates -----------
// M5-05-05 (task-241): the register (rows #17-#20) only covers inverting cases 2 and 6 above;
// these are NEW coverage the register does not already imply, closing the same audit-shape
// gaps M5-05-04 (task-240) closed for SubmitWorker's own synchronous verdicts
// (submit_worker_test.go / submit_worker_adversarial_test.go).

func TestPollWorker_RejectedWritesExactlyOneSubmissionRejectedAuditRow(t *testing.T) {
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

	beforeRejected := auditCount(t, f, tenantID, "submission.rejected")
	beforeAccepted := auditCount(t, f, tenantID, "submission.accepted")

	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Rejected{Reasons: []submission.Reason{{Code: "E1", Message: "bad TIN"}}},
			evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	if err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll to Rejected: %v", err)
	}

	if n := auditCount(t, f, tenantID, "submission.rejected"); n != beforeRejected+1 {
		t.Errorf("submission.rejected audit rows = %d, want %d (exactly one new row)", n, beforeRejected+1)
	}
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != beforeAccepted {
		t.Errorf("submission.accepted audit rows = %d, want unchanged %d -- a poll Rejected must never write an accepted row", n, beforeAccepted)
	}

	payload := auditPayloadMap(t, f, tenantID, "submission.rejected")
	if payload["invoice_id"] != invoiceID {
		t.Errorf("submission.rejected payload invoice_id = %v, want %q", payload["invoice_id"], invoiceID)
	}
	if payload["submission_job_id"] != wj.id {
		t.Errorf("submission.rejected payload submission_job_id = %v, want %q", payload["submission_job_id"], wj.id)
	}
	if payload["outcome"] != "rejected" {
		t.Errorf("submission.rejected payload outcome = %v, want \"rejected\"", payload["outcome"])
	}
	if _, ok := payload["reference"]; ok {
		t.Errorf("submission.rejected payload carries a \"reference\" key (value %v), want it ABSENT -- "+
			"[audit-reference-is-the-irn]: Rejected has no IRN", payload["reference"])
	}
}

// --- 9: the audit payload is a strict summary, both directions (poll-side mirror of ---------
// --- submit_worker_adversarial_test.go's own case 10) ---------------------------------------

func TestPollWorker_AcceptedAuditPayloadIsStrictSummaryNoWireLeak(t *testing.T) {
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

	const distinctIRN = "IRN-ZZZ-POLL-42" // distinctive, guards against a coincidental match to
	// a hardcoded/constant reference.
	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Accepted{IRN: distinctIRN, CSID: "CSID-ZZZ-POLL", QRPayload: "QR-ZZZ-POLL-BLOB"},
			evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	if err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll to Accepted: %v", err)
	}

	// 05 (app_exchange) and 08 (audit_log) must BOTH exist for this hop -- the audit row is
	// additional evidence, not a replacement for the exchange row.
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
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != 1 {
		t.Errorf("submission.accepted audit rows = %d, want exactly 1", n)
	}

	payload := auditPayloadMap(t, f, tenantID, "submission.accepted")
	wantKeys := map[string]bool{"invoice_id": true, "submission_job_id": true, "outcome": true, "reference": true}
	if len(payload) != len(wantKeys) {
		t.Errorf("submission.accepted payload has %d keys (%v), want exactly the 4 in %v -- "+
			"a wire-payload leak (csid/qr_payload) would show up as extra keys here", len(payload), payload, wantKeys)
	}
	for k := range payload {
		if !wantKeys[k] {
			t.Errorf("submission.accepted payload has unexpected key %q (full payload %v) -- "+
				"want the strict summary set only, no wire-payload leak", k, payload)
		}
	}
	if payload["reference"] != distinctIRN {
		t.Errorf("submission.accepted payload reference = %v, want %q verbatim", payload["reference"], distinctIRN)
	}
	if _, ok := payload["csid"]; ok {
		t.Errorf("submission.accepted payload carries a csid key -- the full wire payload must never leak into audit_log")
	}
	if _, ok := payload["qr_payload"]; ok {
		t.Errorf("submission.accepted payload carries a qr_payload key -- the full wire payload must never leak into audit_log")
	}
}

func TestPollWorker_RejectedAuditPayloadIsStrictSummaryNoWireLeak(t *testing.T) {
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
		{result: submission.Rejected{Reasons: []submission.Reason{
			{Code: "E1", Message: "bad TIN"},
			{Code: "E2", Message: "bad currency", Path: "currency"},
		}},
			evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	if err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll to Rejected: %v", err)
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
	if n := auditCount(t, f, tenantID, "submission.rejected"); n != 1 {
		t.Errorf("submission.rejected audit rows = %d, want exactly 1", n)
	}

	payload := auditPayloadMap(t, f, tenantID, "submission.rejected")
	wantKeys := map[string]bool{"invoice_id": true, "submission_job_id": true, "outcome": true}
	if len(payload) != len(wantKeys) {
		t.Errorf("submission.rejected payload has %d keys (%v), want exactly the 3 in %v -- "+
			"a wire-payload leak (the reasons array) would show up as extra keys here", len(payload), payload, wantKeys)
	}
	for k := range payload {
		if !wantKeys[k] {
			t.Errorf("submission.rejected payload has unexpected key %q (full payload %v) -- "+
				"want the strict summary set only, no wire-payload leak", k, payload)
		}
	}
	if _, ok := payload["reasons"]; ok {
		t.Errorf("submission.rejected payload carries a reasons key -- the full reasons array must never leak into audit_log (that is app_exchange's job)")
	}
}

// --- 10: Pending poll hop writes no verdict audit row ----------------------------------------

func TestPollWorker_PendingHopWritesNoVerdictAudit(t *testing.T) {
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

	beforeAccepted := auditCount(t, f, tenantID, "submission.accepted")
	beforeRejected := auditCount(t, f, tenantID, "submission.rejected")

	hop2At := future.Add(time.Hour)
	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Pending{Ref: "r2", PollAfter: hop2At}, evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	if err := pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{
		TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1,
	})); err != nil {
		t.Fatalf("poll hop still Pending: %v", err)
	}

	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "submitted" {
		t.Errorf("invoice status after a still-Pending poll hop = %q, want unchanged \"submitted\"", inv.status)
	}
	if n := auditCount(t, f, tenantID, "submission.accepted"); n != beforeAccepted {
		t.Errorf("submission.accepted audit rows = %d, want unchanged %d -- a Pending hop is not a verdict", n, beforeAccepted)
	}
	if n := auditCount(t, f, tenantID, "submission.rejected"); n != beforeRejected {
		t.Errorf("submission.rejected audit rows = %d, want unchanged %d -- a Pending hop is not a verdict", n, beforeRejected)
	}
}
