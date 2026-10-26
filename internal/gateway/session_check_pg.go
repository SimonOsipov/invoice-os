package gateway

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	revocationSlack       = time.Second
	revocationKeep        = 2 * SessionCheckTTL
	revocationReadTimeout = time.Second
)

// revocationWindow is how far back a sign-out row can sit and still invalidate a cache entry of this age.
func revocationWindow(age time.Duration) time.Duration { return age + revocationSlack }

// revokeSubject records a sign-out, then sweeps rows no cache entry can still match.
func revokeSubject(ctx context.Context, pool *pgxpool.Pool, sub string) error {
	if _, err := pool.Exec(ctx, `
INSERT INTO gateway_session_revocations (subject, revoked_at) VALUES ($1, now())
ON CONFLICT (subject) DO UPDATE SET revoked_at = now()`, sub); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `
DELETE FROM gateway_session_revocations WHERE revoked_at < now() - $1 * interval '1 microsecond'`,
		revocationKeep.Microseconds())
	return err
}

// revokedSince reports a sign-out of sub inside window; every cached live request reads it.
// ceiling: one primary-key read on the gateway pool per cached live request; revisit when pool acquire waits appear.
func revokedSince(ctx context.Context, pool *pgxpool.Pool, sub string, window time.Duration) (bool, error) {
	var revoked bool
	err := pool.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM gateway_session_revocations
               WHERE subject = $1 AND revoked_at > now() - $2 * interval '1 microsecond')`,
		sub, window.Microseconds()).Scan(&revoked)
	return revoked, err
}
