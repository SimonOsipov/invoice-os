package archive

import (
	"context"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store assembles evidence bundles as invoice_app. It holds the app-role pool
// (DATABASE_URL); the caller owns the pool's lifecycle.
type Store struct {
	pool        *pgxpool.Pool
	maxInvoices int
}

// NewStore wires the production cap (D-34) -- assemble treats a non-positive
// cap as a programming error, not a default, so a forgotten wiring cannot
// silently pass.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, maxInvoices: maxBundleInvoices}
}

// Assemble streams one evidence bundle to w. Nothing reaches w until the entity
// resolves and the invoice count clears the cap (D-33, D-37). onStart, when
// non-nil, fires once with the bundle filename before the first byte (D-41);
// a nil onStart is a no-op.
func (s *Store) Assemble(ctx context.Context, r Request, w io.Writer, onStart func(filename string)) error {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return db.ErrNoTenant
	}
	return db.WithinRequestTenantTxOpts(ctx, s.pool, bundleTxOptions, func(tx pgx.Tx) error {
		return assemble(ctx, tx, r, w, assembleOpts{
			tenantID:    id.TenantID,
			subject:     id.Subject,
			maxInvoices: s.maxInvoices,
			now:         time.Now(),
			onStart:     onStart,
		})
	})
}
