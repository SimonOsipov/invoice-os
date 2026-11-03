package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertype"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

var (
	ErrNotResendable = errors.New("invoice: not resendable")
	ErrResendBusy    = errors.New("invoice: a submission job is already running")
)

// The refusal sentences of POST /v1/invoices/{id}/send-again; the GET gate reuses them byte for byte.
const (
	sendAgainNotFailedReason     = "This invoice is no longer failed. Refresh to see its status."
	sendAgainResolvedReason      = "This invoice is marked resolved outside ASComply and cannot be sent again."
	sendAgainNoteChecksReason    = "This note no longer passes its checks and cannot be sent again."
	sendAgainNotResendableReason = "This invoice cannot be sent again from this screen. Ask ASComply support."
	sendAgainBusyReason          = "This invoice is already being sent. Refresh to see its status."
	sendAgainPreparerReason      = "An approver can send this invoice again."
)

// SendAgain re-queues a failed invoice's newest submission job under its original idempotency key.
// It mirrors staffjobs.Store.Redrive; TestRLS_SendAgainLeavesTheSameStateAsRedrive pins the parity.
func (s *Submitter) SendAgain(ctx context.Context, id string) (Invoice, error) {
	actor := actorFromContext(ctx)
	var out Invoice
	err := db.WithinRequestTenantTx(ctx, s.store.pool, func(tx pgx.Tx) error {
		// Lock order: job, then invoice, then (inside requeueFailedTx) the original of a credit note.
		var jobID, key, jobState string
		var pollRef *string
		var riverJobID *int64
		haveJob := true
		if err := tx.QueryRow(ctx, `SELECT id::text, idempotency_key, state, poll_ref, river_job_id
			FROM submission_jobs WHERE invoice_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1 FOR UPDATE`, id).
			Scan(&jobID, &key, &jobState, &pollRef, &riverJobID); err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				haveJob = false
			case pgCode(err) == "22P02":
				return ErrValidation
			default:
				return fmt.Errorf("invoice: lock job: %w", err)
			}
		}
		var status Status
		var kind *string
		if err := tx.QueryRow(ctx, `SELECT status, failure_kind FROM invoices WHERE id = $1 FOR UPDATE`, id).
			Scan(&status, &kind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != StatusFailed {
			return StatusError{Have: status}
		}
		if kind == nil || !submission.FailureKind(*kind).Resendable() {
			return ErrNotResendable
		}
		inv, err := requeueFailedTx(ctx, tx, id, actor)
		if err != nil {
			return err
		}
		// A failed job qualifies for payload_not_built: the worker marks it failed, not dead_lettered.
		wantState := "dead_lettered"
		if submission.FailureKind(*kind) == submission.FailurePayloadNotBuilt {
			wantState = "failed"
		}
		if !haveJob || pollRef != nil || jobState != wantState {
			return ErrNotResendable
		}
		if riverJobID != nil {
			if _, err := s.queue.River().JobDeleteTx(ctx, tx, *riverJobID); err != nil {
				switch {
				case errors.Is(err, rivertype.ErrNotFound):
				case errors.Is(err, rivertype.ErrJobRunning):
					return ErrResendBusy
				default:
					return fmt.Errorf("invoice: delete old River job: %w", err)
				}
			}
		}
		res, err := s.queue.River().InsertTx(ctx, tx, submission.SubmitArgs{
			TenantID: actor.TenantID, InvoiceID: id, IdempotencyKey: key,
		}, nil)
		if err != nil {
			return fmt.Errorf("invoice: insert River job: %w", err)
		}
		if res.UniqueSkippedAsDuplicate {
			return ErrResendBusy
		}
		if _, err := tx.Exec(ctx,
			`UPDATE submission_jobs SET state = 'queued', attempts = 0, river_job_id = $2 WHERE id = $1`,
			jobID, res.Job.ID); err != nil {
			return fmt.Errorf("invoice: requeue job: %w", err)
		}
		out = inv
		return nil
	})
	if err != nil {
		return Invoice{}, err
	}
	return out, nil
}
