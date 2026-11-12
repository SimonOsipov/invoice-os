package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SetReadinessConfirmToken replaces the token hash of the address's newest live held readiness-check row with
// sha256(token) over one connection to dsn. Older rows keep their links. It reports whether one row changed.
// ceiling: one connection per call; it serves the E2E only, so pool it if a non-test caller appears
func SetReadinessConfirmToken(ctx context.Context, dsn, email, token string) (bool, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, fmt.Errorf("db: set readiness confirm token: connect: %w", err)
	}
	defer conn.Close(context.Background())
	tag, err := conn.Exec(ctx, `UPDATE readiness_check_requests SET token_hash = sha256(convert_to($2, 'UTF8'))
WHERE token_hash = (SELECT token_hash FROM readiness_check_requests
	WHERE email = lower(btrim($1)) AND expires_at > now() ORDER BY created_at DESC, token_hash LIMIT 1)`,
		email, token)
	if err != nil {
		return false, fmt.Errorf("db: set readiness confirm token: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
