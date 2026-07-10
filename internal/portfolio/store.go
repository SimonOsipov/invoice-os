package portfolio

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists business_entities rows as the invoice_app role. It holds
// the app-role pool (DATABASE_URL); every method wraps
// db.WithinRequestTenantTx, so the app.current_tenant GUC is set for the
// transaction and RLS enforces isolation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Create's intended contract (filled in by the executor): validate in.TIN via
// ValidateTIN, then, inside ONE db.WithinRequestTenantTx closure, INSERT a
// business_entities row owned by the caller's tenant (tenant_id passed
// explicitly, id left to the column DEFAULT gen_random_uuid()) and write a
// "portfolio.entity.created" audit.Record row in the SAME transaction, AFTER
// the successful INSERT and BEFORE the closure returns nil — so a failed
// audit write rolls back the insert too. A unique_violation (23505, via
// pgCode) on the duplicate-TIN partial index maps to ErrDuplicateTIN.
//
// STUB (M3-03-02 Test-Spec, RED): always returns a not-implemented error
// regardless of input, and touches neither business_entities nor audit_log —
// the executor replaces this body.
func (s *Store) Create(ctx context.Context, in CreateInput) (Entity, error) {
	return Entity{}, errors.New("not implemented: M3-03-02")
}

// GetByID's intended contract (filled in by the executor): a bare SELECT by
// id inside db.WithinRequestTenantTx — RLS scopes the row set to the caller's
// tenant, so a cross-tenant id naturally 0-rows; pgx.ErrNoRows maps to
// ErrNotFound.
//
// STUB (M3-03-02 Test-Spec, RED): always returns a not-implemented error
// regardless of input, and touches no table — the executor replaces this
// body.
func (s *Store) GetByID(ctx context.Context, id string) (Entity, error) {
	return Entity{}, errors.New("not implemented: M3-03-02")
}
