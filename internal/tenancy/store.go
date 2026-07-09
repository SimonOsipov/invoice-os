package tenancy

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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

// Me returns the caller's tenant (id, name, kind) and their domain role, both
// resolved under RLS: SELECT id, name, kind FROM tenants (bare — the
// app.current_tenant GUC is the filter, not a WHERE clause) then SELECT role FROM
// memberships WHERE user_id = $1 (identity.Subject, read inside the closure — RLS
// scopes the row set to the current tenant). No visible tenant row maps to
// ErrTenantNotFound; no membership row maps to ErrNoMembership (never defaulted).
//
// Both queries run inside the SAME db.WithinRequestTenantTx call, so a missing
// tenant row surfaces as ErrTenantNotFound before the membership query ever runs.
func (s *Store) Me(ctx context.Context) (Tenant, string, error) {
	var t Tenant
	var role string
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id, name, kind FROM tenants`).Scan(&t.ID, &t.Name, &t.Kind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTenantNotFound
			}
			return err
		}

		// The identity is guaranteed present here: WithinRequestTenantTx already
		// resolved it (as the tenant id) before this closure ran, returning
		// db.ErrNoTenant otherwise.
		id, _ := auth.IdentityFromContext(ctx)
		if err := tx.QueryRow(ctx,
			`SELECT role FROM memberships WHERE user_id = $1`, id.Subject,
		).Scan(&role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoMembership
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Tenant{}, "", err
	}
	return t, role, nil
}

// ListMemberships lists the caller's tenant's memberships (user_id, role),
// RLS-scoped: SELECT user_id, role FROM memberships ORDER BY created_at,
// user_id -- bare (no WHERE tenant_id), same as Me's tenant query, inside a
// SINGLE db.WithinRequestTenantTx call. An empty tenant returns an empty
// non-nil slice and a nil error (never nil, nil).
//
// STUB (M3-02-02 RED stage, task-30): not implemented yet -- Stage 3
// (executor) replaces this body with the real query above.
func (s *Store) ListMemberships(ctx context.Context) ([]Membership, error) {
	return nil, errors.New("tenancy: ListMemberships not implemented")
}
