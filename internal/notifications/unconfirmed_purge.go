package notifications

import (
	"context"
	"fmt"
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
	// Purge replaces the SQL call in tests.
	Purge func(ctx context.Context, id string) (bool, error)
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

func (p *UnconfirmedPurge) purge(ctx context.Context, id string) (bool, error) {
	if p.Purge != nil {
		return p.Purge(ctx, id)
	}
	var gone bool
	err := p.Pool.QueryRow(ctx, `SELECT auth.purge_unconfirmed_account($1)`, id).Scan(&gone)
	return gone, err
}

// RunOnce lists every page first, since a delete shifts later rows, then asks
// auth.purge_unconfirmed_account about each candidate; the function re-checks and decides.
// A list failure still purges the candidates already read. The first purge error ends the run. It logs the count only.
// ceiling: O(accounts/100) list calls per run, move to a cursor above ~5k accounts.
func (p *UnconfirmedPurge) RunOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, unconfirmedPurgeTimeout)
	defer cancel()
	removed := 0
	defer func() {
		if removed > 0 && p.Logger != nil {
			p.Logger.InfoContext(ctx, "notifications: unconfirmed accounts removed", "accounts", removed)
		}
	}()
	var ids []string
	var listErr error
	for page := 1; ; page++ {
		users, err := p.Account.ListUsers(ctx, page, unconfirmedPurgePage)
		if err != nil {
			listErr = err
			break
		}
		for _, u := range users {
			if stale(u, p.Now()) {
				ids = append(ids, u.ID)
			}
		}
		if len(users) < unconfirmedPurgePage {
			break
		}
	}
	for _, id := range ids {
		gone, err := p.purge(ctx, id)
		if err != nil {
			return fmt.Errorf("unconfirmed purge stopped after removing %d: %w", removed, err)
		}
		if gone {
			removed++
		}
	}
	if listErr != nil {
		return fmt.Errorf("unconfirmed purge list failed after removing %d: %w", removed, listErr)
	}
	return nil
}
