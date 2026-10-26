package tenancy

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// DeletionNotifier schedules a workspace deletion, then mails the other active admins a cancel link.
type DeletionNotifier struct {
	Store  *Store
	Sender accountmail.Sender
	Logger *slog.Logger
}

// Schedule schedules the deletion and mails the links. A mail failure never fails the schedule.
func (n *DeletionNotifier) Schedule(ctx context.Context, typedName string) (time.Time, error) {
	s, err := n.Store.ScheduleWorkspaceDeletion(ctx, typedName)
	if err != nil {
		return time.Time{}, err
	}
	n.deliver(ctx, s)
	return s.DueAt, nil
}

// deliver runs on a context that outlives the client, so a send after the commit is always recorded.
func (n *DeletionNotifier) deliver(ctx context.Context, s ScheduledDeletion) {
	ctx2 := context.WithoutCancel(ctx)
	var msgs []accountmail.Message
	var ids []string
	var firstErr error
	failed := 0
	for _, l := range s.Links {
		subject, html, err := accountmail.RenderDeletionNotice(accountmail.DeletionNoticeMail{
			Workspace: s.Workspace, Requester: s.Requester, Token: l.Token, DueAt: s.DueAt,
		})
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		msgs = append(msgs, accountmail.Message{To: l.Email, Subject: subject, HTML: html})
		ids = append(ids, l.ID)
	}
	sent := false
	if len(msgs) > 0 {
		// ceiling: one strict Resend batch, so one rejected address or more than 100 admins fails every link; revisit when a reader of send_status ships
		if err := n.Sender.Send(ctx2, msgs); err != nil {
			failed += len(msgs)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			sent = true
		}
	}
	if firstErr != nil {
		var status int
		var se *accountmail.SendError
		if errors.As(firstErr, &se) {
			status = se.Status
		}
		n.Logger.ErrorContext(ctx2, "tenancy: deletion mail failed", slog.Int("count", failed), slog.Int("status", status), slog.Any("err", firstErr))
		platform.CaptureError(ctx2, firstErr)
	}
	for _, l := range s.Links {
		ok := sent && slices.Contains(ids, l.ID)
		if err := n.Store.RecordDeletionLinkDelivery(ctx2, l.ID, ok); err != nil {
			n.Logger.ErrorContext(ctx2, "tenancy: deletion mail outcome not recorded", slog.Any("err", err))
		}
	}
}

// RecordDeletionLinkDelivery writes a link's send outcome. It skips the seam, which refuses the
// workspace the schedule just locked, and stays scoped to the caller's own tenant.
func (s *Store) RecordDeletionLinkDelivery(ctx context.Context, id string, sent bool) error {
	caller, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return db.ErrNoTenant
	}
	if _, err := uuid.Parse(caller.Subject); err != nil {
		return db.ErrNoTenant
	}
	status := "failed"
	if sent {
		status = "sent"
	}
	return db.WithinTenantTx(ctx, s.pool, caller.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE workspace_deletion_links SET send_status = $2 WHERE id = $1`, id, status)
		return err
	})
}
