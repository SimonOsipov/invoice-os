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
	Logger      *slog.Logger
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

// Workers builds the River worker bundle for the submission service. It returns an
// otherwise-empty bundle here: SubmitWorker needs an Adapter / InvoicePort / RateLimiter /
// default rate limit that only cmd/submission's composition root can build, and wiring those
// in (plus M5-04-06's PollWorker) is M5-04-08's job. Until then an empty bundle is harmless
// -- nothing in this package's own tests enqueues a submission_submit job through a live
// queue.Client, so an unregistered kind is never fetched.
func Workers(pool *pgxpool.Pool, logger *slog.Logger) *river.Workers {
	return river.NewWorkers()
}
