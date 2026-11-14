package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const linkAddressTimeout = 5 * time.Second

// LinkAddress maps a confirm token to the masked address of its account.
type LinkAddress func(ctx context.Context, token string) (masked string, found bool, err error)

// PoolLinkAddress reads auth.confirm_link_address as invoice_gateway; a nil pool gives a nil LinkAddress.
func PoolLinkAddress(pool *pgxpool.Pool) LinkAddress {
	if pool == nil {
		return nil
	}
	return func(ctx context.Context, token string) (string, bool, error) {
		var masked *string
		if err := pool.QueryRow(ctx, `SELECT auth.confirm_link_address($1)`, token).Scan(&masked); err != nil {
			return "", false, err
		}
		if masked == nil {
			return "", false, nil
		}
		return *masked, true, nil
	}
}

// lookupLinkAddress spends a link-redeem count, then reads the address; ok=false means answer the failed notice.
// A found link gets its count back. The log line carries the error class only, never the token or the address.
//
// ceiling: [get-throttle] no Armor rule covers GET /auth/verify, so this count is its only limit.
// ceiling: [nil-lookup] a nil address (no pool) renders the unnamed page and spends no count.
func lookupLinkAddress(r *http.Request, address LinkAddress, perIP *SignInThrottle, enforce bool, label string, log *slog.Logger, token string) (masked string, ok bool) {
	if address == nil {
		return "", true
	}
	if !admitLinkRedeem(r, perIP, enforce, label, log) {
		return "", false
	}
	ctx, cancel := context.WithTimeout(r.Context(), linkAddressTimeout)
	defer cancel()
	masked, found, err := address(ctx, token)
	if err != nil {
		var pg *pgconn.PgError
		class := ""
		if errors.As(err, &pg) {
			class = pg.Code
		}
		log.WarnContext(r.Context(), label+": link address lookup failed", slog.String("class", class), slog.Bool("timeout", errors.Is(err, context.DeadlineExceeded)))
		return "", false
	}
	if found {
		perIP.Refund(gotrueAddress(r))
	}
	return masked, true
}
