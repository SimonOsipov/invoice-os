package portfolio

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// List returns the caller's tenant's business_entities filtered by f
// (status, name/tin search), ordered name ASC, id ASC, paginated by
// f.Limit/f.Offset, plus the total filtered count (ignoring limit/offset)
// for the response envelope's pagination.total -- M3-03-03 (task-36).
//
// RLS (not a `WHERE tenant_id`) scopes both queries to the caller's tenant,
// same as GetByID. Filters are appended as bound params -- never string
// interpolation of user input -- and the same WHERE clause is reused for the
// COUNT(*) (no limit/offset) that produces total.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Entity, int, error) {
	items := []Entity{}
	var total int
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var conditions []string
		var args []any

		if f.Status != nil {
			args = append(args, *f.Status)
			conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
		}
		if f.Q != "" {
			args = append(args, f.Q)
			conditions = append(conditions, fmt.Sprintf("(name ILIKE '%%'||$%d||'%%' OR tin ILIKE '%%'||$%d||'%%')", len(args), len(args)))
		}

		where := ""
		if len(conditions) > 0 {
			where = " WHERE " + strings.Join(conditions, " AND ")
		}

		if err := tx.QueryRow(ctx,
			"SELECT count(*) FROM business_entities"+where, args...,
		).Scan(&total); err != nil {
			return err
		}

		selectArgs := append(append([]any{}, args...), f.Limit, f.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf(
			`SELECT id, name, tin, registration, sector, address, status, created_at
			 FROM business_entities%s
			 ORDER BY name ASC, id ASC
			 LIMIT $%d OFFSET $%d`, where, len(args)+1, len(args)+2,
		), selectArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Entity
			if err := rows.Scan(&e.ID, &e.Name, &e.TIN, &e.Registration, &e.Sector, &e.Address, &e.Status, &e.CreatedAt); err != nil {
				return err
			}
			items = append(items, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
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
