package tenancy

import (
	"context"
	"log/slog"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
)

// DeletionNotifier schedules a workspace deletion, then mails the other active admins a cancel link.
type DeletionNotifier struct {
	Store  *Store
	Sender accountmail.Sender
	Logger *slog.Logger
}

// Schedule is a red-phase stub: it schedules and mails nothing.
func (n *DeletionNotifier) Schedule(ctx context.Context, typedName string) (time.Time, error) {
	return n.Store.ScheduleWorkspaceDeletion(ctx, typedName)
}

// RecordDeletionLinkDelivery is a red-phase stub: it records nothing.
func (s *Store) RecordDeletionLinkDelivery(ctx context.Context, id string, sent bool) error {
	return nil
}
