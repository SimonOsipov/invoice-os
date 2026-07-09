package tenancy

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// CurrentTenant returns the caller's tenant, scoped by RLS. The query is a bare
// SELECT with no WHERE: under the tenant_isolation policy the invoice_app role sees
// exactly the one tenants row whose id equals app.current_tenant, so the policy — not
// the query — is what limits the result. That is precisely the property M2-13 proves.
// No visible row (an identity whose tenant does not exist) maps to ErrTenantNotFound;
// a missing/invalid tenant id fails closed inside WithinRequestTenantTx (db.ErrNoTenant).
func (s *Store) CurrentTenant(ctx context.Context) (Tenant, error) {
	var t Tenant
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, name FROM tenants`).Scan(&t.ID, &t.Name)
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Tenant{}, ErrTenantNotFound
	case err != nil:
		return Tenant{}, err
	}
	return t, nil
}
