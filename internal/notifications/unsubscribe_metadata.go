package notifications

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/gotrueadmin"
)

const (
	unsubscribeMetadataTimeout = 10 * time.Minute
	unsubscribeMetadataPage    = 100
)

// MetadataAccount is the part of the GoTrue admin client the backfill uses.
type MetadataAccount interface {
	ListUsers(ctx context.Context, page, perPage int) ([]gotrueadmin.AdminUser, error)
	SetUserMetadata(ctx context.Context, userID string, meta map[string]any) error
}

// UnsubscribeMetadata writes each account's unsubscribe token into its GoTrue user_metadata, where
// the confirmation and recovery mails read it.
type UnsubscribeMetadata struct {
	Pool    *pgxpool.Pool
	Account MetadataAccount
	Logger  *slog.Logger
}

// RunOnce pages the accounts and PUTs the key where it is absent or differs. A failing account is
// skipped and its error joined into the result. It logs no address.
// ceiling: O(accounts/100) list calls per run, move to a cursor above ~5k accounts.
func (u *UnsubscribeMetadata) RunOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, unsubscribeMetadataTimeout)
	defer cancel()
	tokens := accountmail.PoolTokens(u.Pool)
	written := 0
	var failed []error
	for page := 1; ; page++ {
		users, err := u.Account.ListUsers(ctx, page, unsubscribeMetadataPage)
		if err != nil {
			return err
		}
		for _, user := range users {
			if user.Email == "" {
				continue
			}
			token, err := tokens(ctx, user.Email)
			if err != nil {
				failed = append(failed, err)
				continue
			}
			if user.Unsubscribe == token {
				continue
			}
			if err := u.Account.SetUserMetadata(ctx, user.ID, map[string]any{accountmail.UnsubscribeMetadataKey: token}); err != nil {
				failed = append(failed, err)
				continue
			}
			written++
		}
		if len(users) < unsubscribeMetadataPage {
			break
		}
	}
	if written > 0 && u.Logger != nil {
		u.Logger.InfoContext(ctx, "notifications: unsubscribe metadata written", "accounts", written)
	}
	return errors.Join(failed...)
}
