package gateway

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (t *SignInThrottle) hashKey(email string) []byte {
	h := sha256.Sum256([]byte(signInKey(email)))
	return h[:]
}

func (t *SignInThrottle) logErr(op string, err error) {
	t.log.Error(t.name+" throttle: "+op, "error", err)
}

// admitShared sweeps when due, then reserves in one upsert; no row back means the limit is reached.
func (t *SignInThrottle) admitShared(email string) (bool, error) {
	t.mu.Lock()
	now := t.now()
	sweep := now.Sub(t.lastSweep) >= signInSweepEvery
	if sweep {
		t.lastSweep = now
	}
	t.mu.Unlock()

	// The sweep has its own budget so a slow one cannot starve the reservation.
	if sweep {
		sctx, scancel := context.WithTimeout(context.Background(), handoffDBTimeout)
		if _, err := t.pool.Exec(sctx, `DELETE FROM gateway_throttle_counts WHERE expires_at <= now()`); err != nil {
			t.logErr("sweep", err)
		}
		scancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), handoffDBTimeout)
	defer cancel()
	var n int
	err := t.pool.QueryRow(ctx, `
INSERT INTO gateway_throttle_counts AS t (scope, key_hash, n, expires_at)
VALUES ($1, $2, 1, now() + $3 * interval '1 microsecond')
ON CONFLICT (scope, key_hash) DO UPDATE SET
    n          = CASE WHEN t.expires_at <= now() THEN 1 ELSE t.n + 1 END,
    expires_at = CASE WHEN t.expires_at <= now() THEN EXCLUDED.expires_at ELSE t.expires_at END
WHERE t.expires_at <= now() OR t.n < $4
RETURNING n`, t.name, t.hashKey(email), t.window.Microseconds(), t.max).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		t.logErr("reserve", err)
		return false, err
	}
	return true, nil
}

func (t *SignInThrottle) refundShared(email string) {
	ctx, cancel := context.WithTimeout(context.Background(), handoffDBTimeout)
	defer cancel()
	if _, err := t.pool.Exec(ctx, `UPDATE gateway_throttle_counts SET n = n - 1 WHERE scope = $1 AND key_hash = $2 AND n > 0`,
		t.name, t.hashKey(email)); err != nil {
		t.logErr("refund", err)
	}
}

func (t *SignInThrottle) resetShared(email string) {
	ctx, cancel := context.WithTimeout(context.Background(), handoffDBTimeout)
	defer cancel()
	if _, err := t.pool.Exec(ctx, `DELETE FROM gateway_throttle_counts WHERE scope = $1 AND key_hash = $2`,
		t.name, t.hashKey(email)); err != nil {
		t.logErr("reset", err)
	}
}
