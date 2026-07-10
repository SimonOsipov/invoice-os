package portfolio

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// Create validates in.TIN via ValidateTIN, then, inside ONE
// db.WithinRequestTenantTx closure, INSERTs a business_entities row owned by
// the caller's tenant (tenant_id passed explicitly, id left to the column
// DEFAULT gen_random_uuid()) and writes a "portfolio.entity.created"
// audit.Record row in the SAME transaction, AFTER the successful INSERT and
// BEFORE the closure returns nil — so a failed audit write rolls back the
// insert too. A unique_violation (23505, via pgCode) on the duplicate-TIN
// partial index maps to ErrDuplicateTIN.
func (s *Store) Create(ctx context.Context, in CreateInput) (Entity, error) {
	canonicalTIN, err := ValidateTIN(in.TIN)
	if err != nil {
		return Entity{}, err
	}

	var entity Entity
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The identity is guaranteed present here: WithinRequestTenantTx already
		// resolved it (as the tenant id) before this closure ran, returning
		// db.ErrNoTenant otherwise.
		id, _ := auth.IdentityFromContext(ctx)

		if err := tx.QueryRow(ctx,
			`INSERT INTO business_entities (tenant_id, name, tin, registration, sector, address)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING id, name, tin, registration, sector, address, status, created_at`,
			id.TenantID, in.Name, canonicalTIN, in.Registration, in.Sector, in.Address,
		).Scan(&entity.ID, &entity.Name, &entity.TIN, &entity.Registration, &entity.Sector, &entity.Address, &entity.Status, &entity.CreatedAt); err != nil {
			if pgCode(err) == "23505" {
				return ErrDuplicateTIN
			}
			return err
		}

		return audit.Record(ctx, tx, id.Subject, "portfolio.entity.created", map[string]any{
			"id":  entity.ID,
			"tin": canonicalTIN,
		})
	})
	if err != nil {
		return Entity{}, err
	}
	return entity, nil
}

// GetByID runs a bare SELECT by id inside db.WithinRequestTenantTx — RLS
// scopes the row set to the caller's tenant, so a cross-tenant id naturally
// 0-rows; pgx.ErrNoRows maps to ErrNotFound.
func (s *Store) GetByID(ctx context.Context, id string) (Entity, error) {
	var entity Entity
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT id, name, tin, registration, sector, address, status, created_at
			 FROM business_entities WHERE id = $1`, id,
		).Scan(&entity.ID, &entity.Name, &entity.TIN, &entity.Registration, &entity.Sector, &entity.Address, &entity.Status, &entity.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return Entity{}, err
	}
	return entity, nil
}
