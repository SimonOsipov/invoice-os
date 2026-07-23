// worker_poll_test.go: the DB-backed (T06-1..T06-3, T06-5..T06-9) plus two pure-unit
// (T06-4, the GAP-2 MaxAttempts pin) specs for M5-04-06's (task-234) PollWorker.Work -- the
// re-poll decision flow that resumes a deferred Result.Pending verdict. Authored BEFORE
// PollWorker's real Work body exists (RALPH Stage 2.5, Mode A): worker.go ships only the
// compiling PollArgs/PollWorker surface with Work stubbed to return
// errPollWorkerNotImplemented, so every DB-backed case below fails on ITS OWN target
// assertion -- never on a compile error, a connection failure, a fixture failure, or a skip.
//
// Package submission_test (external, matching every other file in this package). Reuses
// submit_worker_test.go's fixtures and doubles rather than duplicating them: seedQueuedInvoice
// / seedTerminalJob / wjRequire / wjExchanges / wiRead / newSubmitJob / newScriptedAdapter /
// scriptedAdapter (extended there with pollQueue/pollCalls/pollRefs and its own Poll override,
// M5-04-06's share of that file) / testInvoicePort (extended there with buyerTIN and
// tipLegalTransitions, this subtask's share of that file too). newQueueClient below is net-new
// -- both this file and submit_worker_test.go's own newTestWorker use it to wire the Queue
// field every SubmitWorker/PollWorker construction site now needs.
//
// GATING. Every DB-backed case self-skips via requireExchangeDB when the suite is
// unconfigured, and ONLY then -- the same pair of env vars every other case in this package
// gates on. T06-4 and the MaxAttempts pin are pure unit tests (reflection over the PollArgs
// type) and carry no DB gate at all, matching the story's own "unit" label for T06-4.
//
// Spec-to-test map (task-234's Test Specs table, plus the Stage-1 architect's GAP-2 pin):
//
//	T06-1  TestPollWorker_ScheduleHonoursPollAfterExactly
//	T06-2  TestPollWorker_TicketPersistedFromPendingSubmit
//	T06-3  TestPollWorker_SupersessionNeverRepollsOriginalRef
//	T06-4  TestPollArgs_CarriesNoStaleRefField
//	T06-5  TestPollWorker_ConvergesAgainstTheRealMockAdapterInTwoPolls
//	T06-6  TestPollWorker_EachHopGetsDistinctOutboxKey
//	T06-7  TestPollWorker_SupersededPollIsANoOp
//	T06-8  TestPollWorker_DeadLetterOnFinalAttemptViaExistingEdge (AC-5, final-attempt half)
//	T06-8  TestPollWorker_RetryableMidBudgetLeavesJobPendingAndAttemptsAdvance (AC-5, mid-budget half)
//	T06-9  TestPollWorker_ExchangeRecordedAsPollContinuingAttemptNumbering
//	GAP-2  TestPollArgs_InsertOptsPinsMaxAttemptsToEight
//
// Local run: `DEV_DB_PORT=5433 make test-queue` from this worktree, or export DATABASE_URL /
// DATABASE_MIGRATION_URL and run `go test ./internal/submission/... -run 'PollWorker|PollArgs'`.
package submission_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// mockPendingTIN is mock_script.go's mockTINPending ("99999999-0003") -- unexported inside
// package submission, so this file redeclares the LITERAL rather than reaching across the
// seam. T06-5 is the only spec that uses it.
const mockPendingTIN = "99999999-0003"

// --- construction --------------------------------------------------------------------

// newQueueClient builds an insert-only *queue.Client over pool -- the same shape as
// failure_modes_test.go's newInsertClient, but panicking rather than t.Fatalf'ing so it can
// be called from plain (non-*testing.T) constructors: newTestWorker (submit_worker_test.go)
// and newTestPollWorker (below) are each called from dozens of specs, and threading *testing.T
// through every one of those call sites just to build a trivial insert-only client is exactly
// the kind of churn [surgical-changes] rules out. queue.New(pool, queue.Config{}) validates a
// static config and touches no network, so an error here means malformed configuration, not a
// flaky DB call -- see river's own Config.validate (client.go:522), which this trivial config
// passes unconditionally.
func newQueueClient(pool *pgxpool.Pool) *queue.Client {
	q, err := queue.New(pool, queue.Config{})
	if err != nil {
		panic(fmt.Sprintf("submission_test: build insert-only queue client: %v", err))
	}
	return q
}

// newTestPollWorker builds a PollWorker over pool/adapter, mirroring newTestWorker's own
// shape (submit_worker_test.go).
func newTestPollWorker(pool *pgxpool.Pool, adapter submission.Adapter) *submission.PollWorker {
	return &submission.PollWorker{
		Pool:        pool,
		Adapter:     adapter,
		InvoicePort: testInvoicePort{},
		Queue:       newQueueClient(pool),
	}
}

// newPollJob builds a *river.Job[PollArgs] the way River hands one to Work -- id is CONSTANT
// across simulated retries of "the same" job; attempt/maxAttempts are what T06-8's two halves
// vary. Mirrors newSubmitJob (submit_worker_test.go) exactly.
func newPollJob(id int64, attempt, maxAttempts int, args submission.PollArgs) *river.Job[submission.PollArgs] {
	return &river.Job[submission.PollArgs]{
		JobRow: &rivertype.JobRow{ID: id, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   args,
	}
}

// --- read-back helpers (river_job / app_exchange -- not covered by submit_worker_test.go's
// own wj*/wi* helpers) -------------------------------------------------------------------

// pjScheduledAts returns every river_job.scheduled_at for the submission_poll job(s) enqueued
// for (submissionJobID, sequence). river_job carries no RLS (cross-tenant infrastructure), so
// a bare pool query sees every row -- the same idiom worker_smoke_test.go's countSubmitJobs
// already uses.
func pjScheduledAts(t *testing.T, pool *pgxpool.Pool, submissionJobID string, sequence int) []time.Time {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx,
		`SELECT scheduled_at FROM river_job
		  WHERE kind = $1 AND args->>'submission_job_id' = $2 AND (args->>'sequence')::int = $3`,
		submission.PollArgs{}.Kind(), submissionJobID, sequence)
	if err != nil {
		t.Fatalf("query river_job for submission job %s hop %d: %v", submissionJobID, sequence, err)
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var ts time.Time
		if err := rows.Scan(&ts); err != nil {
			t.Fatalf("scan river_job.scheduled_at: %v", err)
		}
		out = append(out, ts)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate river_job rows for submission job %s hop %d: %v", submissionJobID, sequence, err)
	}
	return out
}

// pollExchangeRow is one app_exchange row's operation/attempt -- T06-9's own target columns,
// not carried by submit_worker_test.go's wjExchangeRow.
type pollExchangeRow struct {
	operation string
	attempt   int
}

// pjPollExchanges reads every app_exchange row for jobID (any operation), ordered by
// occurred_at -- through the app role under tenant context, the same idiom as
// submit_worker_test.go's own wjExchanges.
func pjPollExchanges(t *testing.T, f *effectsFixture, tenantID, jobID string) []pollExchangeRow {
	t.Helper()
	var rows []pollExchangeRow
	err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		r, e := tx.Query(context.Background(),
			`SELECT operation, attempt FROM app_exchange WHERE submission_job_id = $1 ORDER BY occurred_at`, jobID)
		if e != nil {
			return e
		}
		defer r.Close()
		for r.Next() {
			var row pollExchangeRow
			if e := r.Scan(&row.operation, &row.attempt); e != nil {
				return e
			}
			rows = append(rows, row)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatalf("read app_exchange rows (tenant=%s job=%s): %v", tenantID, jobID, err)
	}
	return rows
}

// pjNextPollAt reads submission_jobs.next_poll_at for (tenantID, idemKey) -- the exact-value
// counterpart to wjPollRequire's (submit_worker_adversarial_test.go) mere non-null check.
func pjNextPollAt(t *testing.T, f *effectsFixture, tenantID, idemKey string) time.Time {
	t.Helper()
	var got time.Time
	err := db.WithinTenantTx(context.Background(), f.app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT next_poll_at FROM submission_jobs WHERE tenant_id = $1 AND idempotency_key = $2`,
			tenantID, idemKey).Scan(&got)
	})
	if err != nil {
		t.Fatalf("read next_poll_at (tenant=%s key=%s): %v", tenantID, idemKey, err)
	}
	return got
}

// --- T06-1 -------------------------------------------------------------------------------

// TestPollWorker_ScheduleHonoursPollAfterExactly (AC-1's own "one scheduled River job" half):
// a Pending submit's follow-up poll job's river_job.scheduled_at must equal Pending.PollAfter
// exactly, not now(). SubmitWorker's own Pending branch (worker.go's case Pending) is
// UNCHANGED by this subtask and enqueues nothing yet -- this spec is the one that PROVES that
// gap, per the Stage-1 architect re-verification.
func TestPollWorker_ScheduleHonoursPollAfterExactly(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	future := time.Now().Add(90 * time.Minute)
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
	jobID := wjRequire(t, f, tenantID, idemKey).id

	rows := pjScheduledAts(t, f.app, jobID, 1)
	if len(rows) != 1 {
		t.Fatalf("river_job rows for submission job %s hop 1 (kind=%s) = %d, want exactly 1 "+
			"-- a Pending submit must enqueue exactly one scheduled poll job",
			jobID, submission.PollArgs{}.Kind(), len(rows))
	}
	if diff := rows[0].Sub(future); diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("river_job.scheduled_at = %s, want %s (Pending.PollAfter, honoured exactly -- not now())",
			rows[0], future)
	}
}

// --- T06-2 -------------------------------------------------------------------------------

// TestPollWorker_TicketPersistedFromPendingSubmit (AC-1's "ticket persisted" half): after a
// Pending submit, submission_jobs.poll_ref equals Pending.Ref and next_poll_at equals
// Pending.PollAfter EXACTLY -- not merely non-null (submit_worker_adversarial_test.go's own
// TestSubmitWorker_PendingSetsPollRefAndMovesInvoiceToSubmitted already covers non-null; this
// spec's own target, per task-234's Test Specs table, is the exact-value comparison).
//
// HONEST NOTE: SubmitWorker's Pending branch (worker.go's case Pending) already persisted
// poll_ref/next_poll_at before this subtask -- M5-04-05 shipped it. This spec may therefore
// already be GREEN; see this subtask's own QA report for the actually-observed result.
func TestPollWorker_TicketPersistedFromPendingSubmit(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	want := time.Now().Add(45 * time.Minute)
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	adapter := newScriptedAdapter(scriptedOutcome{
		result:   submission.Pending{Ref: "poll-ticket-1", PollAfter: want},
		evidence: submission.Evidence{ReachedWire: true},
	})
	w := newTestWorker(f.app, adapter)
	job := newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})

	if err := w.Work(ctx, job); err != nil {
		t.Fatalf("Work on a Pending outcome: %v", err)
	}

	poll := wjPollRequire(t, f, tenantID, idemKey)
	if poll.pollRef == nil || *poll.pollRef != "poll-ticket-1" {
		t.Errorf("poll_ref = %v, want \"poll-ticket-1\"", poll.pollRef)
	}
	if poll.nextPollAt == nil || !*poll.nextPollAt {
		t.Fatal("next_poll_at is NULL, want set from Pending.PollAfter")
	}

	got := pjNextPollAt(t, f, tenantID, idemKey)
	if diff := got.Sub(want); diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("next_poll_at = %s, want %s (Pending.PollAfter, exact)", got, want)
	}
}

// --- T06-3 -------------------------------------------------------------------------------

// TestPollWorker_SupersessionNeverRepollsOriginalRef (AC-2): after a poll hop returns
// Pending{Ref:"r2"}, the row holds "r2", and the NEXT poll hop must call the adapter with
// "r2" -- never "r1", the original ref the submit itself set.
func TestPollWorker_SupersessionNeverRepollsOriginalRef(t *testing.T) {
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

	// hop 1's poll converges to ANOTHER Pending carrying "r2"; hop 2 (whichever ref it is
	// actually called with) converges to Accepted.
	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Pending{Ref: "r2", PollAfter: future.Add(time.Hour)}, evidence: submission.Evidence{ReachedWire: true}},
		{result: submission.Accepted{IRN: "NG-1", CSID: "C", QRPayload: "Q"}, evidence: submission.Evidence{ReachedWire: true}},
	}

	pw := newTestPollWorker(f.app, adapter)
	_ = pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1}))
	_ = pw.Work(ctx, newPollJob(11, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 2}))

	poll := wjPollRequire(t, f, tenantID, idemKey)
	if poll.pollRef == nil || *poll.pollRef != "r2" {
		t.Errorf("submission_jobs.poll_ref after two poll hops = %v, want \"r2\"", poll.pollRef)
	}
	if got := adapter.pollRefs; len(got) < 2 {
		t.Fatalf("adapter.Poll was called %d time(s), want at least 2 (one per hop)", len(got))
	} else if got[1] != "r2" {
		t.Errorf("second Poll call's ref = %q, want \"r2\" (the SUPERSEDING ticket) -- never \"r1\" (the original)", got[1])
	}
}

// --- T06-4 -------------------------------------------------------------------------------

// TestPollArgs_CarriesNoStaleRefField (AC-2's structural half): PollArgs must have no
// Ref/PollRef field at all -- the worker always re-reads the CURRENT poll_ref off the row,
// never anything carried in its own args ([poll-ticket]). Pure unit test, no DB gate.
func TestPollArgs_CarriesNoStaleRefField(t *testing.T) {
	typ := reflect.TypeOf(submission.PollArgs{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if name == "Ref" || name == "PollRef" {
			t.Fatalf("PollArgs has a field named %q -- the worker must always re-read the "+
				"CURRENT poll_ref off the row, never carry one in its own args ([poll-ticket])", name)
		}
	}
}

// --- T06-5 -------------------------------------------------------------------------------

// TestPollWorker_ConvergesAgainstTheRealMockAdapterInTwoPolls (AC-3): against the REAL M5-03
// mock adapter (not a scripted double), an invoice whose buyer TIN is the reserved
// mockTINPending value ("99999999-0003") converges to job state "accepted" after exactly two
// poll hops -- mockPendingPolls (mock_adapter.go) is 2.
func TestPollWorker_ConvergesAgainstTheRealMockAdapterInTwoPolls(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	mock := submission.NewMockAdapter(submission.MockConfig{}) // zero Latency: instant
	sw := &submission.SubmitWorker{
		Pool:        f.app,
		Adapter:     mock,
		InvoicePort: testInvoicePort{buyerTIN: mockPendingTIN},
		Limiter:     submission.NewRateLimiter(),
		RateLimit:   60,
		Queue:       newQueueClient(f.app),
	}
	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	if err := sw.Work(ctx, newSubmitJob(1, 1, 8, submission.SubmitArgs{TenantID: tenantID, InvoiceID: invoiceID, IdempotencyKey: idemKey})); err != nil {
		t.Fatalf("submit against the real mock adapter (TIN %s): %v", mockPendingTIN, err)
	}
	wj := wjRequire(t, f, tenantID, idemKey)
	if wj.state != "pending" {
		t.Fatalf("precondition: job state after submitting a pending-trigger TIN = %q, want \"pending\"", wj.state)
	}

	pw := &submission.PollWorker{
		Pool:        f.app,
		Adapter:     mock,
		InvoicePort: testInvoicePort{},
		Queue:       newQueueClient(f.app),
	}
	_ = pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1}))
	_ = pw.Work(ctx, newPollJob(11, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 2}))

	wj2 := wjRequire(t, f, tenantID, idemKey)
	if wj2.state != "accepted" {
		t.Errorf("job state after exactly two poll hops against the real mock (TIN %s) = %q, want \"accepted\"",
			mockPendingTIN, wj2.state)
	}
}

// --- T06-6 -------------------------------------------------------------------------------

// TestPollWorker_EachHopGetsDistinctOutboxKey: hop 1's enqueue (from the submit side) and hop
// 2's enqueue (from PollWorker's own Pending branch) each record their OWN idempotency key --
// "poll:<jobid>:1" and "poll:<jobid>:2" -- never colliding.
func TestPollWorker_EachHopGetsDistinctOutboxKey(t *testing.T) {
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

	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Pending{Ref: "r2", PollAfter: future.Add(time.Hour)}, evidence: submission.Evidence{ReachedWire: true}},
		{result: submission.Accepted{IRN: "NG-1", CSID: "C", QRPayload: "Q"}, evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	_ = pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1}))
	_ = pw.Work(ctx, newPollJob(11, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 2}))

	key1 := fmt.Sprintf("poll:%s:1", jobID)
	key2 := fmt.Sprintf("poll:%s:2", jobID)
	if n := countKeys(t, f.app, tenantID, key1); n != 1 {
		t.Errorf("idempotency_keys for %q = %d, want 1 -- hop 1's own enqueue must record this outbox key", key1, n)
	}
	if n := countKeys(t, f.app, tenantID, key2); n != 1 {
		t.Errorf("idempotency_keys for %q = %d, want 1 -- hop 2's own enqueue must record this outbox key", key2, n)
	}

	// A replay of hop 1 (same outbox key, a fresh EnqueueTx call) must be a no-op: neither a
	// second key nor a second river_job row. This half of the assertion exercises EnqueueTx's
	// OWN dedupe mechanics (already proven elsewhere, e.g. TestQueueSmoke_Outbox's "duplicate
	// business key" case) rather than PollWorker.Work, so it is expected to hold regardless of
	// whether the two assertions above are red.
	q := newQueueClient(f.app)
	var skipped bool
	err := db.WithinTenantTx(ctx, f.app, tenantID, func(tx pgx.Tx) error {
		s, e := q.EnqueueTx(ctx, tx, tenantID, key1,
			submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 1},
			&river.InsertOpts{ScheduledAt: future})
		skipped = s
		return e
	})
	if err != nil {
		t.Fatalf("replay EnqueueTx for hop 1's own key: %v", err)
	}
	if !skipped {
		t.Error("replaying hop 1's own outbox key was NOT skipped -- want the duplicate business key refused")
	}
	if n := countKeys(t, f.app, tenantID, key1); n != 1 {
		t.Errorf("idempotency_keys for %q after a replay = %d, want still 1 (unchanged)", key1, n)
	}
}

// --- T06-7 -------------------------------------------------------------------------------

// TestPollWorker_SupersededPollIsANoOp (AC-4): with the row already at a terminal state
// ("accepted"), PollWorker.Work must return nil WITHOUT calling the adapter.
func TestPollWorker_SupersededPollIsANoOp(t *testing.T) {
	f := requireExchangeDB(t)
	ctx := context.Background()
	tenantID, invoiceID, cleanup := seedQueuedInvoice(t, f)
	defer cleanup()

	idemKey := "req-" + uuid.NewString() + ":" + invoiceID
	jobID := seedTerminalJob(t, f, tenantID, invoiceID, idemKey) // state='accepted'

	adapter := newScriptedAdapter() // Poll must never fire
	pw := newTestPollWorker(f.app, adapter)
	job := newPollJob(10, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: jobID, Sequence: 5})

	if err := pw.Work(ctx, job); err != nil {
		t.Errorf("PollWorker.Work on a row already at \"accepted\" returned %v, want nil "+
			"(a superseded poll must be a no-op)", err)
	}
	if got := adapter.pollCallCount(); got != 0 {
		t.Errorf("adapter.Poll call count = %d, want 0 -- the superseded-poll short-circuit "+
			"must return before ever reaching the adapter", got)
	}
}

// --- T06-8 (AC-5) --------------------------------------------------------------------------

// TestPollWorker_DeadLetterOnFinalAttemptViaExistingEdge: a poll job's FINAL attempt
// (job.Attempt == job.MaxAttempts) returning Retryable dead-letters the job and moves the
// invoice submitted -> failed through the pre-existing edge (no new transition added).
func TestPollWorker_DeadLetterOnFinalAttemptViaExistingEdge(t *testing.T) {
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
		{result: submission.Retryable{Err: errors.New("wsub: poll upstream 503, final attempt")}, evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	job := newPollJob(10, 8, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1}) // final attempt

	if err := pw.Work(ctx, job); err == nil {
		t.Error("PollWorker.Work on a final-attempt Retryable returned nil, want a non-nil error so River discards the job")
	}

	wj2 := wjRequire(t, f, tenantID, idemKey)
	if wj2.state != "dead_lettered" {
		t.Errorf("job state = %q, want \"dead_lettered\"", wj2.state)
	}
	if wj2.attempts != 2 {
		t.Errorf("job attempts = %d, want 2 (the submit's 1 plus this poll attempt)", wj2.attempts)
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "failed" {
		t.Errorf("invoice status = %q, want \"failed\" (submitted -> failed via the existing edge)", inv.status)
	}
}

// TestPollWorker_RetryableMidBudgetLeavesJobPendingAndAttemptsAdvance: a poll job's Retryable
// with budget remaining (job.Attempt < job.MaxAttempts) leaves the job state "pending" and the
// invoice untouched, but still advances submission_jobs.attempts and records last_error --
// this poll attempt genuinely reached the wire.
func TestPollWorker_RetryableMidBudgetLeavesJobPendingAndAttemptsAdvance(t *testing.T) {
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
		{result: submission.Retryable{Err: errors.New("wsub: poll upstream 503, mid-budget")}, evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	job := newPollJob(10, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1}) // mid-budget

	if err := pw.Work(ctx, job); err == nil {
		t.Error("PollWorker.Work on a mid-budget Retryable returned nil, want the original error so River retries")
	}

	wj2 := wjRequire(t, f, tenantID, idemKey)
	if wj2.state != "pending" {
		t.Errorf("job state after a mid-budget poll Retryable = %q, want unchanged \"pending\"", wj2.state)
	}
	if wj2.attempts != 2 {
		t.Errorf("job attempts after a mid-budget poll Retryable = %d, want 2 -- this poll attempt "+
			"consumed the budget same as any submit attempt does", wj2.attempts)
	}
	if wj2.lastError == nil || *wj2.lastError == "" {
		t.Error("job last_error is empty, want the poll Retryable's error message recorded")
	}
	inv := wiRead(t, f, tenantID, invoiceID)
	if inv.status != "submitted" {
		t.Errorf("invoice status = %q, want unchanged \"submitted\"", inv.status)
	}
}

// --- T06-9 -------------------------------------------------------------------------------

// TestPollWorker_ExchangeRecordedAsPollContinuingAttemptNumbering (AC-6): the app_exchange row
// a poll writes carries operation='poll' (never 'submit') and an attempt value continuing the
// submit's own numbering (never resetting to 1).
func TestPollWorker_ExchangeRecordedAsPollContinuingAttemptNumbering(t *testing.T) {
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
	if wj.attempts != 1 {
		t.Fatalf("precondition: job attempts after submit = %d, want 1", wj.attempts)
	}

	adapter.pollQueue = []scriptedOutcome{
		{result: submission.Accepted{IRN: "NG-1", CSID: "C", QRPayload: "Q"}, evidence: submission.Evidence{ReachedWire: true}},
	}
	pw := newTestPollWorker(f.app, adapter)
	_ = pw.Work(ctx, newPollJob(10, 1, 8, submission.PollArgs{TenantID: tenantID, InvoiceID: invoiceID, SubmissionJobID: wj.id, Sequence: 1}))

	rows := pjPollExchanges(t, f, tenantID, wj.id)
	var pollRows []pollExchangeRow
	for _, r := range rows {
		if r.operation == "poll" {
			pollRows = append(pollRows, r)
		}
	}
	if len(pollRows) != 1 {
		t.Fatalf("app_exchange rows with operation='poll' for job %s = %d, want exactly 1 (got rows: %+v)",
			wj.id, len(pollRows), rows)
	}
	if pollRows[0].attempt != 2 {
		t.Errorf("poll exchange row attempt = %d, want 2 (continuing the submit's own numbering, never resetting to 1)",
			pollRows[0].attempt)
	}
	for _, r := range rows {
		if r.operation == "submit" && r.attempt != 1 {
			t.Errorf("submit exchange row attempt = %d, want unchanged 1", r.attempt)
		}
	}
}

// --- GAP-2 -------------------------------------------------------------------------------

// TestPollArgs_InsertOptsPinsMaxAttemptsToEight (Stage-1 architect re-verification, GAP 2):
// without an explicit MaxAttempts, a real poll job would silently inherit River's own default
// of 25 attempts (~3 weeks), contradicting [retry-budget]'s "hours, not weeks" intent. Pure
// unit test, no DB gate.
func TestPollArgs_InsertOptsPinsMaxAttemptsToEight(t *testing.T) {
	got := submission.PollArgs{}.InsertOpts().MaxAttempts
	if got != 8 {
		t.Errorf("PollArgs{}.InsertOpts().MaxAttempts = %d, want 8 -- without an explicit "+
			"budget a real poll job silently inherits River's own default of 25 attempts "+
			"(~3 weeks), contradicting [retry-budget]", got)
	}
}
