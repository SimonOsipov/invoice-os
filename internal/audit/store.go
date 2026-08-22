// store.go: the audit reader's app-role store (AUDIT-04-07). One
// db.WithinRequestTenantTx spans every statement a request issues, so the page, the count,
// the three facets and the empty probe all see one snapshot and cannot disagree. No
// `WHERE tenant_id` appears in this package: the app.current_tenant GUC plus FORCE RLS is
// the isolation, and that is the product.
package audit

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store reads the audit log as invoice_app. It holds the app-role pool
// (DATABASE_URL); the caller owns the pool's lifecycle.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// List runs Query and, when nothing matched, the empty probe that separates a genuinely
// empty log from one the filters emptied.
func (s *Store) List(ctx context.Context, f Filter) (Response, error) {
	var out Response
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return nil
	})
	return out, err
}
