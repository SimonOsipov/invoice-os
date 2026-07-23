// Package submission is the 05 Submission context: the async worker that submits invoices
// to the FIRS/APP. SubmitWorker (M5-04-05) drives the tx1 / adapter / tx2 decision flow
// described alongside adapter.go and result.go; PollWorker (this subtask, M5-04-06) follows
// a deferred verdict the same way, re-reading submission_jobs.poll_ref off the row on every
// hop rather than trusting anything carried in its own args ([poll-ticket]). PollWorker.Work
// is a RALPH Stage 2.5 (Mode A) stub -- the real re-poll decision flow is this subtask's own
// Stage 3 (Executor) pass; SubmitWorker's Pending branch (case Pending, below) is UNCHANGED
// by this subtask and still enqueues nothing, so the poll chain stays dead until Stage 3
// adds the EnqueueTx call there. Workers(pool, logger) still returns an EMPTY river.Workers
// bundle here -- both workers are independently constructible (every dependency, including
// the new Queue field, is an exported struct field) so a caller (or a test) builds one
// directly and calls Work without a live queue.Client. Wiring SubmitWorker/PollWorker into
// the bundle cmd/submission actually runs is M5-04-08's job, atomically with widening this
// function's signature and updating its two call sites (cmd/submission/main.go and this
// package's own worker_smoke_test.go) -- see the M5-04-05 story's Stage-2 architect
// validation, item 9.
package submission

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

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
	// Queue is the outbox client the Pending branch (case Pending, below) uses to enqueue the
	// follow-up PollArgs job at river.InsertOpts.ScheduledAt == Pending.PollAfter (M5-04-06).
	// Declared now so every construction site (newTestWorker, and M5-04-08's eventual
	// composition root) wires it once; the enqueue CALL itself is Stage 3's job -- this
	// subtask's case Pending is otherwise byte-for-byte what M5-04-05 shipped.
	Queue  *queue.Client
	Logger *slog.Logger
}

// tx1Outcome is what tx1 decided before it committed: whether Work should short-circuit
// (and how) or proceed to the adapter.
type tx1Outcome int

const (
	tx1Proceed tx1Outcome = iota
	tx1Terminal
	tx1AlreadyCleared
	tx1RateLimited
)

// Work drives the tx1 / adapter / tx2 decision flow (worker.go's own doc comment, and the
// M5-04-05 story's System Design). tx1 ensures the job row, applies the terminal /
// already-cleared / rate-limit gates and, if none fire, hands the invoice's Canonical
// projection back and commits. The adapter is then called with NO transaction held
// ([adapters-are-db-free], T05-15). tx2 records the outcome, advances the job's state and
// drives InvoicePort.
func (w *SubmitWorker) Work(ctx context.Context, job *river.Job[SubmitArgs]) error {
	args := job.Args
	adapterName := w.Adapter.Name()
	adapterVersion := w.Adapter.Version()

	var (
		jobID      string
		attempts   int
		canonical  Canonical
		outcome    tx1Outcome
		retryAfter time.Duration
	)

	tx1Err := db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
		id, state, att, err := ensureSubmissionJob(ctx, tx, args.TenantID, args.InvoiceID,
			args.IdempotencyKey, adapterName, adapterVersion, job.ID)
		if err != nil {
			return err
		}
		jobID, attempts = id, att

		// Terminal short-circuit: a crash-replay of an already-finished job (T05-12).
		if isTerminalJobState(state) {
			outcome = tx1Terminal
			return nil
		}

		// Already-cleared gate: a sibling job already got Accepted, or the invoice already
		// carries a stored fiscal outcome (T05-8, T05-9). adapter.Submit must never be
		// reached from here.
		alreadyCleared, err := invoiceHasSiblingAcceptedJob(ctx, tx, args.TenantID, args.InvoiceID, jobID)
		if err != nil {
			return err
		}
		if !alreadyCleared {
			if alreadyCleared, err = w.InvoicePort.HasFiscalOutcome(ctx, tx, args.InvoiceID); err != nil {
				return err
			}
		}
		if alreadyCleared {
			ex := Exchange{
				SubmissionJobID: jobID,
				InvoiceID:       args.InvoiceID,
				Operation:       string(OpSubmit),
				Outcome:         OutcomeSkippedAlreadyCleared,
				Attempt:         attempts + 1,
				Adapter:         adapterName,
				AdapterVersion:  adapterVersion,
			}
			if err := RecordExchange(ctx, tx, ex); err != nil {
				return err
			}
			if err := markJobAlreadyCleared(ctx, tx, jobID); err != nil {
				return err
			}
			outcome = tx1AlreadyCleared
			return nil
		}

		// Rate-limit gate (T05-10). A denial does not consume the retry budget.
		limit, err := RateLimitFor(ctx, tx, w.RateLimit)
		if err != nil {
			return err
		}
		allowed, ra := w.Limiter.Allow(args.TenantID, limit, time.Now())
		if !allowed {
			ex := Exchange{
				SubmissionJobID: jobID,
				InvoiceID:       args.InvoiceID,
				Operation:       string(OpSubmit),
				Outcome:         OutcomeBlockedRateLimit,
				Attempt:         attempts + 1,
				Adapter:         adapterName,
				AdapterVersion:  adapterVersion,
			}
			if err := RecordExchange(ctx, tx, ex); err != nil {
				return err
			}
			outcome = tx1RateLimited
			retryAfter = ra
			return nil
		}

		c, err := w.InvoicePort.Canonical(ctx, tx, args.InvoiceID)
		if err != nil {
			return err
		}
		canonical = c
		return markJobSubmitting(ctx, tx, jobID)
	})
	if tx1Err != nil {
		return tx1Err
	}

	switch outcome {
	case tx1Terminal, tx1AlreadyCleared:
		return nil
	case tx1RateLimited:
		return river.JobSnooze(retryAfter)
	}

	// No transaction held from here through adapter.Submit's return
	// ([adapters-are-db-free], proven by T05-15).
	wire, transformErr := w.Adapter.Transform(ctx, canonical)
	if transformErr != nil {
		ex := Exchange{
			SubmissionJobID: jobID,
			InvoiceID:       args.InvoiceID,
			Operation:       string(OpSubmit),
			Outcome:         OutcomeTransformFailed,
			Attempt:         attempts + 1,
			Adapter:         adapterName,
			AdapterVersion:  adapterVersion,
		}
		txErr := db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
			_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
				if err := RecordExchange(ctx, tx, ex); err != nil {
					return err
				}
				if err := markJobTransformFailed(ctx, tx, jobID); err != nil {
					return err
				}
				return w.InvoicePort.MarkFailed(ctx, tx, args.InvoiceID, args.TenantID)
			})
			return err
		})
		if txErr != nil {
			return txErr
		}
		return river.JobCancel(transformErr)
	}

	result, evidence := w.Adapter.Submit(ctx, wire, args.IdempotencyKey)

	var submitErr error
	tx2Err := db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
		newAttempts := attempts + 1
		switch r := result.(type) {
		case Accepted:
			ex := ExchangeFor(w.Adapter, OpSubmit, newAttempts, jobID, args.InvoiceID, evidence)
			_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
				if err := RecordExchange(ctx, tx, ex); err != nil {
					return err
				}
				if err := markJobAccepted(ctx, tx, jobID, newAttempts); err != nil {
					return err
				}
				return w.InvoicePort.MarkSubmitted(ctx, tx, args.InvoiceID, args.TenantID)
			})
			return err

		case Rejected:
			// [reaching-the-app-means-a-verdict]: Rejected still moves the invoice to
			// submitted -- the invoice was read and refused, not left in limbo.
			ex := ExchangeFor(w.Adapter, OpSubmit, newAttempts, jobID, args.InvoiceID, evidence)
			_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
				if err := RecordExchange(ctx, tx, ex); err != nil {
					return err
				}
				if err := markJobRejected(ctx, tx, jobID, newAttempts); err != nil {
					return err
				}
				return w.InvoicePort.MarkSubmitted(ctx, tx, args.InvoiceID, args.TenantID)
			})
			return err

		case Pending:
			ex := ExchangeFor(w.Adapter, OpSubmit, newAttempts, jobID, args.InvoiceID, evidence)
			_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
				if err := RecordExchange(ctx, tx, ex); err != nil {
					return err
				}
				if err := markJobPending(ctx, tx, jobID, newAttempts, string(r.Ref), r.PollAfter); err != nil {
					return err
				}
				return w.InvoicePort.MarkSubmitted(ctx, tx, args.InvoiceID, args.TenantID)
			})
			return err

		case Retryable:
			ex := ExchangeFor(w.Adapter, OpSubmit, newAttempts, jobID, args.InvoiceID, evidence)
			if job.Attempt >= job.MaxAttempts {
				// Final attempt: dead-letter. Wrapped in OncePerJob -- this is a terminal
				// write, unlike the mid-budget branch below.
				_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
					if err := RecordExchange(ctx, tx, ex); err != nil {
						return err
					}
					if err := markJobDeadLettered(ctx, tx, jobID, newAttempts, r.Err.Error()); err != nil {
						return err
					}
					return w.InvoicePort.MarkFailed(ctx, tx, args.InvoiceID, args.TenantID)
				})
				if err != nil {
					return err
				}
				submitErr = r.Err
				return nil
			}
			// Mid-budget: deliberately OUTSIDE queue.OncePerJob -- see markJobRetry's doc
			// comment for why wrapping it would silently no-op the next attempt.
			if err := RecordExchange(ctx, tx, ex); err != nil {
				return err
			}
			if err := markJobRetry(ctx, tx, jobID, newAttempts, r.Err.Error()); err != nil {
				return err
			}
			submitErr = r.Err
			return nil

		default:
			return fmt.Errorf("submission: unknown Result variant %T", result)
		}
	})
	if tx2Err != nil {
		return tx2Err
	}
	return submitErr
}

// PollArgs is one poll hop's job payload (M5-04-06). It carries NO Ref/PollRef field --
// PollWorker.Work always re-reads the CURRENT submission_jobs.poll_ref off the row it locks,
// never anything carried in its own args. That is what makes "never re-polls the original"
// ([poll-ticket]) hold even under a River retry of a stale poll job: a retried PollArgs job
// has the same stale Sequence, but the ref it drives Poll with is read fresh every time.
// Sequence is 1-based and makes each hop's outbox key ("poll:<submission_job_id>:<sequence>")
// unique, so a later hop's enqueue can never collide with an earlier one's.
type PollArgs struct {
	TenantID        string `json:"tenant_id"`
	InvoiceID       string `json:"invoice_id"`
	SubmissionJobID string `json:"submission_job_id"`
	Sequence        int    `json:"sequence"`
}

// Kind is River's stable job-type key, persisted on every row -- never rename it.
func (PollArgs) Kind() string { return "submission_poll" }

// Tenant satisfies queue.TenantScoped, identical contract to SubmitArgs.Tenant.
func (a PollArgs) Tenant() string { return a.TenantID }

// InsertOpts: MaxAttempts: 8, mirroring SubmitArgs' own [retry-budget-is-max-attempts-8].
// Without an explicit budget here a real poll job would silently inherit River's own default
// of 25 attempts (~3 weeks) under the unmodified attempt^4s policy, contradicting the same
// "hours, not weeks" intent SubmitArgs already pins one type above. No UniqueOpts.ByArgs
// (unlike SubmitArgs): each hop's Sequence makes its args distinct by construction, so
// ByArgs uniqueness would be a no-op at best -- the authoritative dedupe is the
// poll:<job>:<seq> idempotency key, the same layering the submit side already uses.
func (PollArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 8}
}

// errPollWorkerNotImplemented is the RED-stage stub body PollWorker.Work returns below
// (M5-04-06, Mode A / RALPH Stage 2.5): the real re-poll decision flow lands in this
// subtask's Mode B (Executor, Stage 3) pass. Mirrors the errWorkerNotImplemented precedent
// from M5-04-05 (and, before that, errActorNotImplemented / errPortNotImplemented /
// errRateLimiterNotImplemented from M5-04-02/03/04).
var errPollWorkerNotImplemented = errors.New("submission: poll worker not implemented")

// PollWorker runs PollArgs jobs: re-read the CURRENT poll_ref off the locked row, call
// adapter.Poll with NO transaction held (mirroring SubmitWorker's own
// [adapters-are-db-free]), then record the outcome through the same Accepted / Rejected /
// Pending / Retryable shape SubmitWorker's tx2 already uses -- see the M5-04-06 story's
// Implementation Plan, "PollWorker decision flow". Every dependency is an exported struct
// field, mirroring SubmitWorker's own shape exactly.
type PollWorker struct {
	river.WorkerDefaults[PollArgs]
	Pool        *pgxpool.Pool
	Adapter     Adapter
	InvoicePort InvoicePort
	Queue       *queue.Client
	Logger      *slog.Logger
}

// Work is stubbed for RALPH Stage 2.5 (Mode A, test-first): the real re-read-poll_ref /
// adapter.Poll / record-outcome decision flow lands in Stage 3. Returning (never panicking)
// the sentinel below is what lets T06-1..T06-9 reach their own assertions and fail on
// THOSE -- never on a compile error, a connection failure, or a panic.
func (w *PollWorker) Work(ctx context.Context, job *river.Job[PollArgs]) error {
	return errPollWorkerNotImplemented
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
