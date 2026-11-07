package tenancy

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// StrandedAlert is what a committed account deletion left with nobody to approve it, and who to tell.
// The zero value means nothing to send.
type StrandedAlert struct {
	TenantID  string
	Workspace string
	Stranded  approval.Stranded
	Admins    []adminRecipient
	err       error // a failed judgement read; the deletion committed anyway
}

// signedBefore collects, in a savepoint, every waiting step target can sign. Run it before endMembership.
func signedBefore(ctx context.Context, tx pgx.Tx, target string) ([]approval.SignedStep, error) {
	var signed []approval.SignedStep
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) (err error) {
		signed, err = approval.SignedWaitingStepsTx(ctx, sp, target, nil)
		return err
	})
	return signed, err
}

// judgeStranded runs after endMembership. A failed read leaves only alert.err: a deletion is never blocked.
// ceiling: a role save or an arm racing the deletion can strand a run unalerted; take the tenant lock first if an alert is missed
func judgeStranded(ctx context.Context, tx pgx.Tx, target string, signed []approval.SignedStep, alert *StrandedAlert) {
	var st approval.Stranded
	var workspace string
	var admins []adminRecipient
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) (err error) {
		if st, err = approval.StrandedStepsTx(ctx, sp, signed); err != nil || st.Total == 0 {
			return err
		}
		if err = sp.QueryRow(ctx, `SELECT name FROM tenants`).Scan(&workspace); err != nil {
			return err
		}
		admins, err = otherActiveAdmins(ctx, sp, target)
		return err
	})
	if err != nil {
		alert.err = err
		return
	}
	alert.Stranded, alert.Workspace, alert.Admins = st, workspace, admins
}

// DeleteMyAccount deletes the caller's account, then alerts the admins of what it stranded.
func (n *DeletionNotifier) DeleteMyAccount(ctx context.Context, want AccountDeletionOutcome, heirUserID string) error {
	a, err := n.Store.DeleteMyAccount(ctx, want, heirUserID)
	if err != nil {
		return err
	}
	n.alertStranded(ctx, a)
	return nil
}

// StaffDeleteAccount deletes the path member's account, then alerts the admins of what it stranded.
func (n *DeletionNotifier) StaffDeleteAccount(ctx context.Context, tenantID, userID string, want AccountDeletionOutcome, heirUserID, reason string) error {
	a, err := n.Store.StaffDeleteAccount(ctx, tenantID, userID, want, heirUserID, reason)
	if err != nil {
		return err
	}
	n.alertStranded(ctx, a)
	return nil
}

// alertStranded runs after the commit and never fails the caller.
func (n *DeletionNotifier) alertStranded(ctx context.Context, a StrandedAlert) {
	if a.err != nil {
		ctx2 := context.WithoutCancel(ctx)
		n.Logger.ErrorContext(ctx2, "tenancy: stranded check failed", slog.Any("err", a.err))
		platform.CaptureError(ctx2, a.err)
		return
	}
	if a.Stranded.Total == 0 || len(a.Admins) == 0 {
		return
	}
	mail := accountmail.StrandedAlertMail{Workspace: a.Workspace}
	listed := map[string]bool{}
	for _, inv := range a.Stranded.Invoices {
		role := inv.RoleTitle
		if role == "" {
			role = inv.RoleKey
		}
		mail.Invoices = append(mail.Invoices, accountmail.StrandedInvoice{Number: inv.Number, Role: role})
		listed[inv.ID] = true
	}
	mail.More = a.Stranded.Total - len(listed)
	items := make([]outbound, len(a.Admins))
	for i, ad := range a.Admins {
		items[i] = outbound{id: ad.id, to: ad.email, render: func() (string, string, error) {
			return accountmail.RenderStrandedAlert(mail)
		}}
	}
	accepted := 0
	n.sendAndRecord(ctx, "stranded alert", items, func(_ context.Context, _ string, sent bool) error {
		if sent {
			accepted++
		}
		return nil
	})
	delivery := "failed"
	if accepted > 0 {
		delivery = "sent"
	}
	ctx2 := context.WithoutCancel(ctx)
	if err := n.Store.recordStrandedAlert(ctx2, a, delivery); err != nil {
		n.Logger.ErrorContext(ctx2, "tenancy: stranded alert outcome not recorded", slog.Any("err", err))
		platform.CaptureError(ctx2, err)
	}
}

// recordStrandedAlert writes the alert row in the deletion's tenant, after the send.
func (s *Store) recordStrandedAlert(ctx context.Context, a StrandedAlert, delivery string) error {
	return db.WithinTenantTx(ctx, s.pool, a.TenantID, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, "system", "membership.stranded_alert", map[string]any{
			"stranded_role_keys":      a.Stranded.RoleKeys,
			"stranded_invoices_total": a.Stranded.Total,
			"admins":                  len(a.Admins),
			"delivery":                delivery,
		})
	})
}
