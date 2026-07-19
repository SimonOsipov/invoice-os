package dashboard

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store computes the per-tenant dashboard rollup as the invoice_app role. It
// holds the app-role pool (DATABASE_URL); Rollup wraps
// db.WithinRequestTenantTx, so the app.current_tenant GUC is set for the
// transaction and RLS enforces isolation — no `WHERE tenant_id` appears
// anywhere in this package (AC-7).
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Rollup is a STUB for M4-07-01 (Test-Spec, Mode A): the real per-entity
// query, scan, and Totals summation are M4-07-01's executor's job in Stage
// 3. This body exists only so the package compiles and every DASH-0x test
// below reddens on an assertion (or this not-implemented error), never on a
// compile error.
func (s *Store) Rollup(ctx context.Context) (Rollup, error) {
	return Rollup{}, errors.New("dashboard: Rollup not implemented")
}
