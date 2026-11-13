package notifications

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	unconfirmedPurgeTimeout = 10 * time.Minute
	unconfirmedPurgePage    = 100
)

// PurgeLister is the part of the GoTrue admin client the purge uses.
type PurgeLister interface {
	ListUsers(ctx context.Context, page, perPage int) ([]gotrueadmin.AdminUser, error)
}

// UnconfirmedPurge removes GoTrue accounts whose address was never confirmed. It erases no
// address-keyed row and calls no vendor; see contacts.md.
type UnconfirmedPurge struct {
	Pool    *pgxpool.Pool
	Account PurgeLister
	Logger  *slog.Logger
	Now     func() time.Time
}

// stale nominates an unconfirmed account whose latest mail is past db.UnconfirmedPurgeDays.
func stale(u gotrueadmin.AdminUser, now time.Time) bool {
	if u.EmailConfirmedAt != nil {
		return false
	}
	last := u.CreatedAt
	for _, t := range []*time.Time{u.ConfirmationSentAt, u.RecoverySentAt} {
		if t != nil && t.After(last) {
			last = *t
		}
	}
	return last.Before(now.Add(-db.UnconfirmedPurgeDays * 24 * time.Hour))
}

// RunOnce pages the accounts and asks auth.purge_unconfirmed_account about each candidate; the
// function re-checks and decides. A failing account is joined into the result. It logs the count only.
// ceiling: O(accounts/100) list calls per run, move to a cursor above ~5k accounts.
func (p *UnconfirmedPurge) RunOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, unconfirmedPurgeTimeout)
	defer cancel()
	removed := 0
	var failed []error
	for page := 1; ; page++ {
		users, err := p.Account.ListUsers(ctx, page, unconfirmedPurgePage)
		if err != nil {
			return err
		}
		for _, u := range users {
			if !stale(u, p.Now()) {
				continue
			}
			var gone bool
			if err := p.Pool.QueryRow(ctx, `SELECT auth.purge_unconfirmed_account($1)`, u.ID).Scan(&gone); err != nil {
				failed = append(failed, err)
				continue
			}
			if gone {
				removed++
			}
		}
		if len(users) < unconfirmedPurgePage {
			break
		}
	}
	if removed > 0 && p.Logger != nil {
		p.Logger.InfoContext(ctx, "notifications: unconfirmed accounts removed", "accounts", removed)
	}
	return errors.Join(failed...)
}
