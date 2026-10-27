package tenancy

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// StaffSchedule is the staff schedule plus the mail to every active admin. A mail failure never fails it.
func (n *DeletionNotifier) StaffSchedule(ctx context.Context, tenantID, reason string) (time.Time, error) {
	s, err := n.Store.StaffScheduleWorkspaceDeletion(ctx, tenantID, reason)
	if err != nil {
		return time.Time{}, err
	}
	n.deliver(ctx, s, n.Store.recordStaffLinkDelivery(tenantID))
	return s.DueAt, nil
}

// StaffCancel is the staff cancel plus the mail to every active admin. A mail failure never fails it.
func (n *DeletionNotifier) StaffCancel(ctx context.Context, tenantID, reason string) error {
	c, err := n.Store.StaffCancelWorkspaceDeletion(ctx, tenantID, reason)
	if err != nil {
		return err
	}
	n.deliverCancelled(ctx, c, n.Store.recordStaffCancelMailDelivery(tenantID))
	return nil
}

const (
	setLinkStatusSQL       = `UPDATE workspace_deletion_links SET send_status = $2 WHERE id = $1`
	setCancelMailStatusSQL = `UPDATE workspace_deletion_cancel_mails SET send_status = $2 WHERE id = $1`
)

// recordStaffMailStatus writes a send outcome on the path tenant: the staff caller's own tenant is the house one.
func (s *Store) recordStaffMailStatus(ctx context.Context, tenantID, query, id string, sent bool) error {
	status := "failed"
	if sent {
		status = "sent"
	}
	return db.WithinTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, query, id, status)
		return err
	})
}

func (s *Store) recordStaffLinkDelivery(tenantID string) deliveryRecorder {
	return func(ctx context.Context, id string, sent bool) error {
		return s.recordStaffMailStatus(ctx, tenantID, setLinkStatusSQL, id, sent)
	}
}

func (s *Store) recordStaffCancelMailDelivery(tenantID string) deliveryRecorder {
	return func(ctx context.Context, id string, sent bool) error {
		return s.recordStaffMailStatus(ctx, tenantID, setCancelMailStatusSQL, id, sent)
	}
}
