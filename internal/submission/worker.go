// Package submission is the 05 Submission context: the async worker that submits invoices
// to the FIRS/APP. SubmitWorker (this subtask, M5-04-05) drives the tx1 / adapter / tx2
// decision flow described alongside adapter.go and result.go; PollWorker (M5-04-06) follows
// a deferred verdict the same way. Workers(pool, logger) still returns an EMPTY
// river.Workers bundle here -- SubmitWorker is independently constructible (Pool, Adapter,
// InvoicePort, Limiter, RateLimit, Logger are all exported struct fields) so a caller (or a
// test) builds one directly and calls Work without a live queue.Client. Wiring SubmitWorker
// (and M5-04-06's PollWorker) into the bundle cmd/submission actually runs is M5-04-08's
// job, atomically with widening this function's signature and updating its two call sites
// (cmd/submission/main.go and this package's own worker_smoke_test.go) -- see the M5-04-05
// story's Stage-2 architect validation, item 9.
package submission

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// errWorkerNotImplemented is the RED-stage stub body SubmitWorker.Work returns below
// (M5-04-05, Mode A / RALPH Stage 2.5): the real tx1 / adapter / tx2 decision flow lands in
// this subtask's Mode B (Executor, Stage 3) pass. Mirrors the errActorNotImplemented /
// errPortNotImplemented / errRateLimiterNotImplemented precedent from M5-04-02/03/04.
var errWorkerNotImplemented = errors.New("submission: submit worker not implemented")

// SubmitArgs is one submission attempt's job payload. Per [job-row-is-the-workers],
// SubmitWorker.Work creates the submission_jobs row itself on its own first attempt -- the
// batch endpoint (M5-04-07) never does. IdempotencyKey is "<request idempotency_key>:<invoice
// id>" ([per-invoice-key-derivation]), used both for River's own ByArgs uniqueness below and
// as the adapter's wire-level Idempotency-Key header.
type SubmitArgs struct {
	TenantID       string `json:"tenant_id"`
	InvoiceID      string `json:"invoice_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Kind is River's stable job-type key, persisted on every row -- never rename it.
func (SubmitArgs) Kind() string { return "submission_submit" }

// Tenant satisfies queue.TenantScoped: the tenant this job runs its work under. EnqueueTx
// requires it and fails closed if it diverges from the outbox tenant (docs/migrations.md §8).
func (a SubmitArgs) Tenant() string { return a.TenantID }

// InsertOpts: MaxAttempts: 8 under River's unmodified attempt^4s policy is the
// [retry-budget-is-max-attempts-8] schedule -- retries at ~1s, 16s, 81s, 256s, 625s, 1296s,
// 2401s (±10% jitter), ~1.3h total ("hours, not weeks", Core AC-7). UniqueOpts.ByArgs is
// River's in-flight complement to the authoritative idempotency_keys dedupe (the two-layer
// idempotency of decision #2).
func (SubmitArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 8,
		UniqueOpts:  river.UniqueOpts{ByArgs: true},
	}
}

// SubmitWorker runs SubmitArgs jobs: tx1 (ensure the job row, terminal / already-cleared /
// rate-limit gates, state='submitting') -> adapter.Transform / adapter.Submit with NO
// transaction held ([adapters-are-db-free]) -> tx2 (record the exchange, advance job state,
// drive InvoicePort). Every dependency is an exported struct field rather than something
// threaded through Workers(...), mirroring DemoWorker's own Pool/Logger fields -- see the
// M5-04-05 story's Stage-2 architect validation, item 9.
type SubmitWorker struct {
	river.WorkerDefaults[SubmitArgs]
	Pool        *pgxpool.Pool
	Adapter     Adapter
	InvoicePort InvoicePort
	Limiter     *RateLimiter
	RateLimit   int
	Logger      *slog.Logger
}

// Work is stubbed for RALPH Stage 2.5 (Mode A, test-first): the real tx1 / adapter / tx2
// decision flow lands in Stage 3. Returning (never panicking) the sentinel below is what
// lets T05-1..T05-15 reach their own assertions and fail on THOSE -- never on a compile
// error, a connection failure, or a panic.
func (w *SubmitWorker) Work(ctx context.Context, job *river.Job[SubmitArgs]) error {
	return errWorkerNotImplemented
}

// Workers builds the River worker bundle for the submission service. It returns an
// otherwise-empty bundle here: SubmitWorker needs an Adapter / InvoicePort / RateLimiter /
// default rate limit that only cmd/submission's composition root can build, and wiring those
// in (plus M5-04-06's PollWorker) is M5-04-08's job. Until then an empty bundle is harmless
// -- nothing in this package's own tests enqueues a submission_submit job through a live
// queue.Client, so an unregistered kind is never fetched.
func Workers(pool *pgxpool.Pool, logger *slog.Logger) *river.Workers {
	return river.NewWorkers()
}
