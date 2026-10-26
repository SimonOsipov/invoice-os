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

// DeletionNotifier mails the other active admins.
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

// Cancel cancels the deletion, then mails the other active admins. A mail failure never fails the cancel.
func (n *DeletionNotifier) Cancel(ctx context.Context) error {
	c, err := n.Store.CancelWorkspaceDeletion(ctx)
	if err != nil {
		return err
	}
	n.deliverCancelled(ctx, c)
	return nil
}

// CancelByLink is Cancel with a link token.
func (n *DeletionNotifier) CancelByLink(ctx context.Context, token string) error {
	c, err := n.Store.CancelWorkspaceDeletionByLink(ctx, token)
	if err != nil {
		return err
	}
	n.deliverCancelled(ctx, c)
	return nil
}

// outbound is one mail to render, send and record.
type outbound struct {
	id, to string
	render func() (subject, html string, err error)
}

func (n *DeletionNotifier) deliver(ctx context.Context, s ScheduledDeletion) {
	items := make([]outbound, len(s.Links))
	for i, l := range s.Links {
		items[i] = outbound{id: l.ID, to: l.Email, render: func() (string, string, error) {
			return accountmail.RenderDeletionNotice(accountmail.DeletionNoticeMail{
				Workspace: s.Workspace, Requester: s.Requester, Token: l.Token, DueAt: s.DueAt,
			})
		}}
	}
	n.sendAndRecord(ctx, "deletion mail", items, n.Store.RecordDeletionLinkDelivery)
}

func (n *DeletionNotifier) deliverCancelled(ctx context.Context, c CancelledDeletion) {
	items := make([]outbound, len(c.Mails))
	for i, m := range c.Mails {
		items[i] = outbound{id: m.ID, to: m.Email, render: func() (string, string, error) {
			return accountmail.RenderDeletionCancelled(accountmail.DeletionCancelledMail{
				Workspace: c.Workspace, Canceller: c.Canceller, DueAt: c.DueAt,
			})
		}}
	}
	n.sendAndRecord(ctx, "deletion cancel mail", items, n.Store.RecordCancelMailDelivery)
}

// sendAndRecord runs on a context that outlives the client, so a send after the commit is always recorded.
// The log lines carry counts and the vendor status only.
func (n *DeletionNotifier) sendAndRecord(ctx context.Context, label string, items []outbound,
	record func(ctx context.Context, id string, sent bool) error) {
	ctx2 := context.WithoutCancel(ctx)
	var msgs []accountmail.Message
	var ids []string
	var firstErr error
	failed := 0
	for _, it := range items {
		subject, html, err := it.render()
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		msgs = append(msgs, accountmail.Message{To: it.to, Subject: subject, HTML: html})
		ids = append(ids, it.id)
	}
	sent := false
	if len(msgs) > 0 {
		// ceiling: one strict Resend batch, so one rejected address or more than 100 admins fails every mail; revisit when a reader of send_status ships
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
		n.Logger.ErrorContext(ctx2, "tenancy: "+label+" failed", slog.Int("count", failed), slog.Int("status", status), slog.Any("err", firstErr))
		platform.CaptureError(ctx2, firstErr)
	}
	for _, it := range items {
		ok := sent && slices.Contains(ids, it.id)
		if err := record(ctx2, it.id, ok); err != nil {
			n.Logger.ErrorContext(ctx2, "tenancy: "+label+" outcome not recorded", slog.Any("err", err))
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

// RecordCancelMailDelivery writes a cancel mail's send outcome through the gated seam: the cancel has unlocked the workspace.
// ceiling: a schedule committed after this cancel refuses the record; the rows stay sending, logged
func (s *Store) RecordCancelMailDelivery(ctx context.Context, id string, sent bool) error {
	status := "failed"
	if sent {
		status = "sent"
	}
	return db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE workspace_deletion_cancel_mails SET send_status = $2 WHERE id = $1`, id, status)
		return err
	})
}
