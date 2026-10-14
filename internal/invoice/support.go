package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// ErrResolvedOutside: the customer already closed the failed invoice
// (kept_as_is_at set), so support must not re-queue it.
var ErrResolvedOutside = errors.New("invoice: resolved outside the platform")

// StatusError reports the status that made a support transition refuse.
type StatusError struct{ Have Status }

func (e StatusError) Error() string {
	return fmt.Sprintf("invoice: status is %s", e.Have)
}

func (e StatusError) Is(target error) bool { return target == ErrIllegalTransition }

func lockStatusTx(ctx context.Context, tx pgx.Tx, id string) (Status, bool, error) {
	var status Status
	var keptAt *string
	if err := tx.QueryRow(ctx,
		`SELECT status, kept_as_is_at::text FROM invoices WHERE id = $1 FOR UPDATE`, id,
	).Scan(&status, &keptAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, ErrNotFound
		}
		if pgCode(err) == "22P02" {
			return "", false, ErrValidation
		}
		return "", false, err
	}
	return status, keptAt != nil, nil
}

// RequeueFailedTx moves a failed invoice back to queued as support. The edge is
// outside legalTransitions, so the customer route still refuses it.
func (s *Store) RequeueFailedTx(ctx context.Context, tx pgx.Tx, id, tenantID string) error {
	status, kept, err := lockStatusTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if status != StatusFailed {
		return StatusError{Have: status}
	}
	if kept {
		return ErrResolvedOutside
	}
	_, err = applyTransitionTx(ctx, tx, id, StatusFailed, StatusQueued,
		Actor{TenantID: tenantID, Subject: audit.SupportActor}, demotionCause{})
	return err
}

// CancelQueuedTx moves a queued invoice to failed with no failure_kind.
func (s *Store) CancelQueuedTx(ctx context.Context, tx pgx.Tx, id, tenantID string) error {
	status, _, err := lockStatusTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if status != StatusQueued {
		return StatusError{Have: status}
	}
	_, err = transitionCausedTx(ctx, tx, id, StatusQueued, StatusFailed,
		Actor{TenantID: tenantID, Subject: audit.SupportActor}, demotionCause{})
	return err
}
