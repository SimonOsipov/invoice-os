package invoice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists invoices/line_items/invoice_status_history rows as the
// invoice_app role. It holds the app-role pool (DATABASE_URL); every method
// wraps db.WithinRequestTenantTx, so the app.current_tenant GUC is set for
// the transaction and RLS enforces isolation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// errNotImplemented is the RED-stage stub body every method below returns
// (M4-02-01, Mode A / RALPH Stage 2.5): a real implementation lands in this
// subtask's Mode B (Executor, Stage 3) pass. Every method still returns
// (rather than panics) so store_test.go / cross_tenant_integration_test.go
// reach their assertions and fail for the right reason (assertion
// mismatch), not a panic or compile error.
var errNotImplemented = errors.New("invoice: not implemented")

// Create will, inside ONE db.WithinRequestTenantTx closure: INSERT an
// invoices row (status DEFAULT 'draft'), INSERT its line_items (line_no
// system-assigned 1..N by CreateInput.LineItems' array position), INSERT a
// genesis invoice_status_history row (from_status NULL -> to_status
// 'draft'), then audit.Record("invoice.created") — in that order, so a
// later failure rolls the earlier writes back too (System Design table,
// M4-02-01). A unique_violation (23505, via pgCode) on
// invoices_tenant_entity_number_uq maps to ErrDuplicateNumber; a
// foreign_key_violation (23503) on a non-existent entity_id maps to
// ErrValidation.
func (s *Store) Create(ctx context.Context, in CreateInput) (Invoice, error) {
	return Invoice{}, errNotImplemented
}

// Get will SELECT an invoice + its line_items (ordered by line_no) inside
// one db.WithinRequestTenantTx; pgx.ErrNoRows (RLS-invisible or genuinely
// absent) maps to ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Invoice, error) {
	return Invoice{}, errNotImplemented
}

// List will return the caller's tenant's invoice headers (LineItems left
// nil, [D7]), ordered created_at DESC, id DESC, paginated by
// f.Limit/f.Offset, plus the total filtered count (no filters, [D8]). An
// empty result is []Invoice{}, never a nil slice.
func (s *Store) List(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
	return nil, 0, errNotImplemented
}

// Update will apply only in's non-nil MBS-content fields to an invoices row
// and write an "invoice.updated" audit row in the same transaction; an
// all-nil in is rejected as ErrValidation BEFORE any tx opens (a no-op
// UPDATE is forbidden, [D9]). Zero rows affected (cross-tenant id,
// RLS-invisible) maps to ErrNotFound.
func (s *Store) Update(ctx context.Context, id string, in UpdateInput) (Invoice, error) {
	return Invoice{}, errNotImplemented
}
