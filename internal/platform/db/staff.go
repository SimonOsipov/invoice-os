package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// GrantStaff inserts a public.staff_members row for userID, with rulesRole, over one connection to dsn.
// A repeat grant is a no-op.
// ceiling: one connection per call; it serves the E2E only, so pool it if a non-test caller appears
func GrantStaff(ctx context.Context, dsn string, userID uuid.UUID, rulesRole bool) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: grant staff: connect: %w", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, `INSERT INTO public.staff_members (user_id, rules_role) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, rulesRole); err != nil {
		return fmt.Errorf("db: grant staff: %w", err)
	}
	return nil
}
