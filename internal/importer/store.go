package importer

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a lookup resolves to zero rows under the
// caller's tenant (RLS-scoped) — mirrors internal/invoice's sentinel.
var ErrNotFound = errors.New("importer: not found")

// RowError is one entry in an import_batches.errors jsonb array — either a
// single-row problem (Row set) or one shared across several rows (Rows set),
// e.g. rows that disagree with each other on a value. Exactly one of
// Row/Rows is set per entry; row numbers are 1-based spreadsheet rows.
type RowError struct {
	Row     int    `json:"row,omitempty"`
	Rows    []int  `json:"rows,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// Store persists import_batches rows as the invoice_app role, mirroring
// internal/invoice.Store's shape: it holds the app-role pool and every
// method wraps db.WithinRequestTenantTx so RLS enforces tenant isolation.
//
// STUB (M4-03-03 Mode A): method bodies are not-implemented placeholders —
// the real logic is the Executor's Stage 3. This file exists only so
// store_test.go compiles and its IB-STORE-01..06 assertions fail on
// behavior (RED), not on a build error.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateBatch is not yet implemented (stub).
func (s *Store) CreateBatch(ctx context.Context, entityID string) (string, error) {
	return "", nil
}

// Finalize is not yet implemented (stub).
func (s *Store) Finalize(ctx context.Context, id string, rowsTotal, rowsValid, rowsInvalid int, errs []RowError, status string) error {
	return nil
}

// ExistingNumbers is not yet implemented (stub).
func (s *Store) ExistingNumbers(ctx context.Context, entityID string, numbers []string) (map[string]bool, error) {
	return nil, nil
}

// EntitySupplier is not yet implemented (stub).
func (s *Store) EntitySupplier(ctx context.Context, entityID string) (name string, tin *string, err error) {
	return "", nil, nil
}
