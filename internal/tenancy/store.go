package tenancy

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store reads tenancy data as the invoice_app role. It holds the app-role pool
// (DATABASE_URL); every read goes through db.WithinRequestTenantTx, so the
// app.current_tenant GUC is set for the transaction and RLS enforces isolation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Me returns the caller's tenant (id, name, kind) and their domain role, both
// resolved under RLS: SELECT id, name, kind FROM tenants (bare — the
// app.current_tenant GUC is the filter, not a WHERE clause) then SELECT role FROM
// memberships WHERE user_id = $1 (identity.Subject, read inside the closure — RLS
// scopes the row set to the current tenant). No visible tenant row maps to
// ErrTenantNotFound; no membership row maps to ErrNoMembership (never defaulted).
//
// STUB (M3-02-01, RED stage): not yet implemented — Stage 3 wires both queries
// inside one db.WithinRequestTenantTx call. This always returns a
// not-implemented error so the RED tests fail on assertion, never on a compile
// or skip path.
func (s *Store) Me(ctx context.Context) (Tenant, string, error) {
	return Tenant{}, "", errors.New("tenancy: Store.Me not implemented")
}
