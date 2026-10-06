package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SetInvitationToken replaces a pending invite's token hash with sha256(token) over one connection to dsn,
// inside one transaction that sets app.current_tenant (invitations is FORCE ROW LEVEL SECURITY).
// It reports whether one row changed.
// ceiling: one connection per call; it serves the E2E only, so pool it if a non-test caller appears
func SetInvitationToken(ctx context.Context, dsn string, tenantID, invitationID uuid.UUID, token string) (bool, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, fmt.Errorf("db: set invitation token: connect: %w", err)
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("db: set invitation token: begin: %w", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID.String()); err != nil {
		return false, fmt.Errorf("db: set invitation token: set tenant: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE invitations SET token_hash = sha256(convert_to($3, 'UTF8'))
WHERE tenant_id = $1 AND id = $2 AND status = 'pending'`, tenantID, invitationID, token)
	if err != nil {
		return false, fmt.Errorf("db: set invitation token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("db: set invitation token: commit: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
