package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SetDeletionLinkToken replaces the token hash of userID's live cancel link with sha256(token) over one connection
// to dsn, inside one transaction that sets app.current_tenant (the table is FORCE ROW LEVEL SECURITY).
// A link is live while its expires_at equals the tenant's deletion_due_at. It reports whether one row changed.
// ceiling: one connection per call; it serves the E2E only, so pool it if a non-test caller appears
func SetDeletionLinkToken(ctx context.Context, dsn string, tenantID, userID uuid.UUID, token string) (bool, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, fmt.Errorf("db: set deletion link token: connect: %w", err)
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("db: set deletion link token: begin: %w", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID.String()); err != nil {
		return false, fmt.Errorf("db: set deletion link token: set tenant: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE workspace_deletion_links l SET token_hash = sha256(convert_to($3, 'UTF8'))
FROM tenants t
WHERE t.id = l.tenant_id AND l.tenant_id = $1 AND l.recipient_user_id = $2 AND l.expires_at = t.deletion_due_at`,
		tenantID, userID, token)
	if err != nil {
		return false, fmt.Errorf("db: set deletion link token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("db: set deletion link token: commit: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
