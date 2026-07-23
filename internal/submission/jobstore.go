// jobstore.go: M5-04-05 (task-230) -- submission_jobs reads and writes for SubmitWorker's
// tx1 / adapter / tx2 decision flow (worker.go). Every function here takes the caller's
// tenant-scoped transaction (db.WithinTenantTx already set the app.current_tenant GUC on
// tx) and touches submission_jobs alone -- app_exchange writes go through RecordExchange
// (exchange.go), invoice-side writes through InvoicePort (invoice_port.go). PollWorker
// (M5-04-06) is expected to reuse these same functions rather than duplicating them.
//
// submission_jobs.updated_at is trigger-maintained (20260722085427_submission_jobs.sql) --
// nothing here writes it.
package submission

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// isTerminalJobState reports whether state is one submission_jobs never leaves: a
// crash-replay of an already-finished job must short-circuit before ever reaching the
// adapter again (T05-12).
func isTerminalJobState(state string) bool {
	switch state {
	case "accepted", "rejected", "failed", "dead_lettered":
		return true
	}
	return false
}

// ensureSubmissionJob is the ONLY place a submission_jobs row is created
// ([job-row-is-the-workers] -- the batch-submit endpoint, M5-04-07, never does): INSERT ...
// ON CONFLICT (tenant_id, idempotency_key) DO NOTHING, stitching riverJobID into the row on
// the INSERT branch only (ON CONFLICT DO NOTHING silently drops every value on a later
// attempt of the same job, since no row is actually written then), followed by
// SELECT ... FOR UPDATE to lock and read back the row's current (id, state, attempts)
// regardless of which branch fired.
func ensureSubmissionJob(ctx context.Context, tx pgx.Tx, tenantID, invoiceID, idemKey, adapterName, adapterVersion string, riverJobID int64) (jobID, state string, attempts int, err error) {
	if _, err = tx.Exec(ctx,
		`INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter,
		                              adapter_version, state, river_job_id)
		 VALUES ($1, $2, $3, $4, $5, 'queued', $6)
		 ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
		tenantID, invoiceID, idemKey, adapterName, adapterVersion, riverJobID); err != nil {
		return "", "", 0, fmt.Errorf("submission: ensure job row: %w", err)
	}
	if err = tx.QueryRow(ctx,
		`SELECT id, state, attempts FROM submission_jobs
		  WHERE tenant_id = $1 AND idempotency_key = $2 FOR UPDATE`,
		tenantID, idemKey).Scan(&jobID, &state, &attempts); err != nil {
		return "", "", 0, fmt.Errorf("submission: lock job row: %w", err)
	}
	return jobID, state, attempts, nil
}

// invoiceHasSiblingAcceptedJob reports whether a DIFFERENT submission_jobs row for the same
// (tenantID, invoiceID) already sits at state='accepted' -- one half of tx1's already-
// cleared gate (T05-8); the other half is InvoicePort.HasFiscalOutcome.
func invoiceHasSiblingAcceptedJob(ctx context.Context, tx pgx.Tx, tenantID, invoiceID, jobID string) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM submission_jobs
		    WHERE tenant_id = $1 AND invoice_id = $2 AND state = 'accepted' AND id <> $3)`,
		tenantID, invoiceID, jobID).Scan(&exists); err != nil {
		return false, fmt.Errorf("submission: check sibling accepted job: %w", err)
	}
	return exists, nil
}

// markJobSubmitting is tx1's final write (step 5): the job is about to leave the
// transaction and the adapter is called with no transaction held.
func markJobSubmitting(ctx context.Context, tx pgx.Tx, jobID string) error {
	return execJobUpdate(ctx, tx, `UPDATE submission_jobs SET state = 'submitting' WHERE id = $1`, jobID)
}

// markJobAlreadyCleared is the already-cleared short-circuit's write (T05-8/T05-9):
// state='accepted', attempts left UNCHANGED -- this job never actually reached the wire.
func markJobAlreadyCleared(ctx context.Context, tx pgx.Tx, jobID string) error {
	return execJobUpdate(ctx, tx, `UPDATE submission_jobs SET state = 'accepted' WHERE id = $1`, jobID)
}

// markJobTransformFailed is the transform-error branch's write (T05-7): state='failed',
// attempts left UNCHANGED -- a transform failure never reaches the wire either, so it must
// not consume the retry budget ([transform-failure]).
func markJobTransformFailed(ctx context.Context, tx pgx.Tx, jobID string) error {
	return execJobUpdate(ctx, tx, `UPDATE submission_jobs SET state = 'failed' WHERE id = $1`, jobID)
}

// markJobAccepted is tx2's Accepted write: state='accepted', attempts advanced to the
// POST-increment value (this attempt genuinely reached the wire).
func markJobAccepted(ctx context.Context, tx pgx.Tx, jobID string, attempts int) error {
	return execJobUpdate(ctx, tx,
		`UPDATE submission_jobs SET state = 'accepted', attempts = $2 WHERE id = $1`, jobID, attempts)
}

// markJobRejected is tx2's Rejected write: state='rejected', attempts advanced -- reaching
// the APP means a verdict ([reaching-the-app-means-a-verdict]) even when that verdict is a
// refusal.
func markJobRejected(ctx context.Context, tx pgx.Tx, jobID string, attempts int) error {
	return execJobUpdate(ctx, tx,
		`UPDATE submission_jobs SET state = 'rejected', attempts = $2 WHERE id = $1`, jobID, attempts)
}

// markJobPending is tx2's Pending write: state='pending', attempts advanced, plus the
// deferred-verdict handle a later poll (M5-04-06) resumes from.
func markJobPending(ctx context.Context, tx pgx.Tx, jobID string, attempts int, ref string, pollAfter time.Time) error {
	return execJobUpdate(ctx, tx,
		`UPDATE submission_jobs SET state = 'pending', attempts = $2, poll_ref = $3, next_poll_at = $4
		  WHERE id = $1`, jobID, attempts, ref, pollAfter)
}

// markJobRetry is tx2's mid-budget Retryable write (T05-5): state back to 'queued', attempts
// advanced, last_error recorded -- the invoice is left untouched. The caller wraps this
// UPDATE deliberately OUTSIDE queue.OncePerJob: OncePerJob's marker key is constant across
// retries of the same River job, so wrapping a mid-budget write would make the SECOND
// attempt's bookkeeping a silent no-op.
func markJobRetry(ctx context.Context, tx pgx.Tx, jobID string, attempts int, lastErr string) error {
	return execJobUpdate(ctx, tx,
		`UPDATE submission_jobs SET state = 'queued', attempts = $2, last_error = $3 WHERE id = $1`,
		jobID, attempts, lastErr)
}

// markJobDeadLettered is tx2's final-attempt Retryable write (T05-6): state='dead_lettered',
// attempts advanced, last_error recorded -- a job state independent of River's own
// `discarded`, distinguishing an authority-side exhaustion from any other reason River gives
// up ([dead-letter-state]).
func markJobDeadLettered(ctx context.Context, tx pgx.Tx, jobID string, attempts int, lastErr string) error {
	return execJobUpdate(ctx, tx,
		`UPDATE submission_jobs SET state = 'dead_lettered', attempts = $2, last_error = $3 WHERE id = $1`,
		jobID, attempts, lastErr)
}

// execJobUpdate runs one submission_jobs UPDATE by primary key and turns a
// zero-rows-affected result into an error: every call site above targets the row this same
// transaction just ensured/locked, so 0 rows means the id vanished out from under an open
// transaction, not a legitimate no-op.
func execJobUpdate(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	ct, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("submission: update job row: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("submission: update job row %v: no row affected", args[0])
	}
	return nil
}
