package readinesscheck

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store reads and writes readiness_checks on the pool: rows carry no tenant.
type Store struct{ Pool *pgxpool.Pool }

// Summary is one completed check; the file and the client IP are never stored.
type Summary struct {
	Email         string
	FileType      string
	RowCount      int
	FailedRuleIDs []string
}

// Count returns the checks already recorded for the address.
func (s *Store) Count(ctx context.Context, email string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM readiness_checks WHERE email = lower(btrim($1))`, email).Scan(&n)
	return n, err
}

// Insert records one check; created_at is the database time.
func (s *Store) Insert(ctx context.Context, m Summary) error {
	ids := m.FailedRuleIDs
	if ids == nil {
		ids = []string{}
	}
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO readiness_checks (email, file_type, row_count, failed_rule_ids) VALUES (lower(btrim($1)), $2, $3, $4)`,
		m.Email, m.FileType, m.RowCount, ids)
	return err
}

// AISpendReached reports whether the check's AI spend for the UTC day has reached the ceiling.
func (s *Store) AISpendReached(ctx context.Context, ceiling string) (bool, error) {
	var reached bool
	err := s.Pool.QueryRow(ctx, `SELECT public.public_check_ai_cost_today() >= $1::numeric`, ceiling).Scan(&reached)
	return reached, err
}
