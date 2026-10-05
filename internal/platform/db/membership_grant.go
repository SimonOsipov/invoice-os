package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MemberGrant is one membership row to write.
type MemberGrant struct {
	TenantID    uuid.UUID
	UserID      uuid.UUID
	Role        string
	DisplayName string
	Email       string
}

// GrantMembership upserts an active memberships row over one connection to dsn, inside one
// transaction that sets app.current_tenant (memberships is FORCE ROW LEVEL SECURITY).
// ceiling: one connection per call; it serves the E2E only, so pool it if a non-test caller appears
func GrantMembership(ctx context.Context, dsn string, g MemberGrant) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: grant membership: connect: %w", err)
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: grant membership: begin: %w", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, g.TenantID.String()); err != nil {
		return fmt.Errorf("db: grant membership: set tenant: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memberships (tenant_id, user_id, role, display_name, email, status)
VALUES ($1, $2, $3, $4, $5, 'active')
ON CONFLICT (tenant_id, user_id) DO UPDATE SET
  role = EXCLUDED.role, display_name = EXCLUDED.display_name, email = EXCLUDED.email, status = 'active'`,
		g.TenantID, g.UserID, g.Role, g.DisplayName, g.Email); err != nil {
		return fmt.Errorf("db: grant membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: grant membership: commit: %w", err)
	}
	return nil
}
