package tenancy

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform"
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
	}
}
