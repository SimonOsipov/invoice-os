// Package submission is the 05 Submission context: the async worker that submits invoices
// to the FIRS/APP. SubmitWorker (M5-04-05) drives the tx1 / adapter / tx2 decision flow
// described alongside adapter.go and result.go; PollWorker (M5-04-06) follows a deferred
// verdict the same way, re-reading submission_jobs.poll_ref off the row on every hop rather
// than trusting anything carried in its own args ([poll-ticket]). SubmitWorker's Pending
// branch (case Pending, below) enqueues the first poll hop at the adapter's exact PollAfter
// inside the same OncePerJob closure that persists poll_ref/next_poll_at, so the enqueue
// commits atomically with the state write; PollWorker's own Pending branch does the same for
// every hop after that. Workers(sw, pw) (M5-04-08) registers both, built by the caller with
// every field except Queue set -- Queue is backfilled onto the same pointers after
// queue.New returns, breaking the circular dependency between the client (which needs the
// Workers bundle) and the workers (which need the client). Both workers are independently
// constructible (every dependency is an exported struct field), so a caller (or a test) can
// still build one directly and call Work without going through the bundle at all.
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
	// Queue is the outbox client the Pending branch (case Pending, below) uses to enqueue the
	// follow-up PollArgs job at river.InsertOpts.ScheduledAt == Pending.PollAfter (M5-04-06).
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
				if err := w.InvoicePort.MarkAccepted(ctx, tx, args.InvoiceID, args.TenantID, r); err != nil {
					return err
				}
				// M5-05-04 (task-240): the 08 audit event. Must stay the closure's LAST
				// statement so OncePerJob's exactly-once guarantee covers it too (AC#5).
				return recordVerdictAudit(ctx, tx, args.InvoiceID, jobID, "accepted", r.IRN)
			})
			return err

		case Rejected:
			// [reaching-the-app-means-a-verdict]: Rejected moves the invoice to rejected --
			// the invoice was read and refused, not left in limbo.
			ex := ExchangeFor(w.Adapter, OpSubmit, newAttempts, jobID, args.InvoiceID, evidence)
			_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
				if err := RecordExchange(ctx, tx, ex); err != nil {
					return err
				}
				if err := markJobRejected(ctx, tx, jobID, newAttempts); err != nil {
					return err
				}
				if err := w.InvoicePort.MarkRejected(ctx, tx, args.InvoiceID, args.TenantID, r); err != nil {
					return err
				}
				// M5-05-04 (task-240): the 08 audit event, no reference on Rejected
				// ([audit-reference-is-the-irn] -- Rejected has no IRN field to pass).
				return recordVerdictAudit(ctx, tx, args.InvoiceID, jobID, "rejected", "")
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
				if err := w.InvoicePort.MarkSubmitted(ctx, tx, args.InvoiceID, args.TenantID); err != nil {
					return err
				}
				// M5-04-06: enqueue the first poll hop at the adapter's exact PollAfter, inside
				// this same OncePerJob closure so the enqueue commits atomically with the state
				// write. Sequence 1 gives it the outbox key "poll:<jobID>:1" (T06-6).
				_, err := w.Queue.EnqueueTx(ctx, tx, args.TenantID, fmt.Sprintf("poll:%s:1", jobID),
					PollArgs{TenantID: args.TenantID, InvoiceID: args.InvoiceID, SubmissionJobID: jobID, Sequence: 1},
					&river.InsertOpts{ScheduledAt: r.PollAfter})
				return err
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

// Work drives PollWorker's own tx1 / adapter / tx2 decision flow, structurally mirroring
// SubmitWorker.Work above.
//
// tx1 locks the submission_jobs row by (tenantID, SubmissionJobID) and reads its CURRENT
// state and poll_ref. A state other than 'pending' means a fresher hop already advanced this
// job past pending, or it was never pending at all -- a superseded/terminal no-op (T06-7):
// tx1 commits and Work returns nil WITHOUT ever calling the adapter. There is no rate-limit
// gate here ([rate-limit-gates-submits-only] -- polls are never throttled).
//
// No transaction is held across adapter.Poll ([adapters-are-db-free], mirroring
// SubmitWorker's own T05-15).
//
// tx2 records the outcome. Accepted/Rejected (M5-05-05 (task-241)) drive the invoice
// submitted->accepted / submitted->rejected via InvoicePort.MarkAccepted/MarkRejected plus
// the same recordVerdictAudit helper SubmitWorker's own synchronous verdicts use
// (M5-05-04 (task-240), System Design §6) -- one submission.accepted/rejected audit row per
// terminal poll hop, inside the same OncePerJob(job.ID) closure as the exchange/job-state
// writes. Pending OVERWRITES poll_ref/next_poll_at with the NEW ticket and enqueues the next hop at
// Sequence+1, scheduled at the adapter's exact new PollAfter ([poll-ticket],
// [unbounded-poll-chain] -- no hop ceiling). Retryable on the final attempt dead-letters the
// job and moves the invoice submitted -> failed via the pre-existing edge; Retryable with
// budget remaining leaves the job 'pending' (not 'queued' -- there is no "back to queued" for
// a poll, it is still waiting on the same deferred verdict) and advances attempts/last_error
// OUTSIDE queue.OncePerJob, mirroring markJobRetry's own rationale exactly. Every terminal
// branch is wrapped in queue.OncePerJob(job.ID) -- each poll hop is its own River job row, so
// this marker never collides across hops. Evidence is always recorded with
// Operation=OpPoll and an attempt value continuing the submit's own numbering (T06-9).
func (w *PollWorker) Work(ctx context.Context, job *river.Job[PollArgs]) error {
	args := job.Args

	var (
		pollRef    string
		attempts   int
		superseded bool
	)

	tx1Err := db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
		state, ref, att, err := lockSubmissionJobForPoll(ctx, tx, args.TenantID, args.SubmissionJobID)
		if err != nil {
			return err
		}
		attempts = att

		if state != "pending" {
			superseded = true
			return nil
		}
		if ref != nil {
			pollRef = *ref
		}
		return nil
	})
	if tx1Err != nil {
		return tx1Err
	}
	if superseded {
		return nil
	}

	// No transaction held from here through adapter.Poll's return.
	result, evidence := w.Adapter.Poll(ctx, Ref(pollRef))

	var pollErr error
	tx2Err := db.WithinTenantTx(ctx, w.Pool, args.TenantID, func(tx pgx.Tx) error {
		newAttempts := attempts + 1
		switch r := result.(type) {
		case Accepted:
			ex := ExchangeFor(w.Adapter, OpPoll, newAttempts, args.SubmissionJobID, args.InvoiceID, evidence)
			_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
				if err := RecordExchange(ctx, tx, ex); err != nil {
					return err
				}
				if err := markJobAccepted(ctx, tx, args.SubmissionJobID, newAttempts); err != nil {
					return err
				}
				if err := w.InvoicePort.MarkAccepted(ctx, tx, args.InvoiceID, args.TenantID, r); err != nil {
					return err
				}
				// M5-05-05 (task-241): the 08 audit event, same helper SubmitWorker's synchronous
				// Accepted branch uses (M5-05-04 (task-240), System Design §6). Must stay the
				// closure's LAST statement so OncePerJob's exactly-once guarantee covers it too.
				return recordVerdictAudit(ctx, tx, args.InvoiceID, args.SubmissionJobID, "accepted", r.IRN)
			})
			return err

		case Rejected:
			ex := ExchangeFor(w.Adapter, OpPoll, newAttempts, args.SubmissionJobID, args.InvoiceID, evidence)
			_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
				if err := RecordExchange(ctx, tx, ex); err != nil {
					return err
				}
				if err := markJobRejected(ctx, tx, args.SubmissionJobID, newAttempts); err != nil {
					return err
				}
				if err := w.InvoicePort.MarkRejected(ctx, tx, args.InvoiceID, args.TenantID, r); err != nil {
					return err
				}
				// M5-05-05 (task-241): the 08 audit event, no reference on Rejected
				// ([audit-reference-is-the-irn]).
				return recordVerdictAudit(ctx, tx, args.InvoiceID, args.SubmissionJobID, "rejected", "")
			})
			return err

		case Pending:
			ex := ExchangeFor(w.Adapter, OpPoll, newAttempts, args.SubmissionJobID, args.InvoiceID, evidence)
			nextSeq := args.Sequence + 1
			_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
				if err := RecordExchange(ctx, tx, ex); err != nil {
					return err
				}
				if err := markJobPending(ctx, tx, args.SubmissionJobID, newAttempts, string(r.Ref), r.PollAfter); err != nil {
					return err
				}
				_, err := w.Queue.EnqueueTx(ctx, tx, args.TenantID,
					fmt.Sprintf("poll:%s:%d", args.SubmissionJobID, nextSeq),
					PollArgs{TenantID: args.TenantID, InvoiceID: args.InvoiceID, SubmissionJobID: args.SubmissionJobID, Sequence: nextSeq},
					&river.InsertOpts{ScheduledAt: r.PollAfter})
				return err
			})
			return err

		case Retryable:
			ex := ExchangeFor(w.Adapter, OpPoll, newAttempts, args.SubmissionJobID, args.InvoiceID, evidence)
			if job.Attempt >= job.MaxAttempts {
				// Final attempt: dead-letter. Wrapped in OncePerJob -- this is a terminal
				// write, unlike the mid-budget branch below.
				_, err := queue.OncePerJob(ctx, tx, args.TenantID, job.ID, func() error {
					if err := RecordExchange(ctx, tx, ex); err != nil {
						return err
					}
					if err := markJobDeadLettered(ctx, tx, args.SubmissionJobID, newAttempts, r.Err.Error()); err != nil {
						return err
					}
					return w.InvoicePort.MarkFailed(ctx, tx, args.InvoiceID, args.TenantID)
				})
				if err != nil {
					return err
				}
				pollErr = r.Err
				return nil
			}
			// Mid-budget: deliberately OUTSIDE queue.OncePerJob -- see markJobPollRetry's doc
			// comment for why wrapping it would silently no-op the next attempt.
			if err := RecordExchange(ctx, tx, ex); err != nil {
				return err
			}
			if err := markJobPollRetry(ctx, tx, args.SubmissionJobID, newAttempts, r.Err.Error()); err != nil {
				return err
			}
			pollErr = r.Err
			return nil

		default:
			return fmt.Errorf("submission: unknown Result variant %T", result)
		}
	})
	if tx2Err != nil {
		return tx2Err
	}
	return pollErr
}

// Workers builds the River worker bundle for the submission service: sw and pw, already
// constructed by the caller (composition root: cmd/submission/main.go, or a test's own
// smoke client) with every dependency set except Queue, which does not exist yet at this
// point -- queue.New needs this very bundle to build the client. The caller backfills
// sw.Queue/pw.Queue onto these same pointers once queue.New returns (river.NewClient only
// reads Workers to register Work funcs by kind at construction time; it never inspects
// worker fields afterward, so mutating them post-registration is safe).
func Workers(sw *SubmitWorker, pw *PollWorker) *river.Workers {
	workers := river.NewWorkers()
	river.AddWorker(workers, sw)
	river.AddWorker(workers, pw)
	return workers
}
