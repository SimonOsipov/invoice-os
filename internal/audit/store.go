// store.go: the audit reader's app-role store (AUDIT-04-07). One
// db.WithinRequestTenantTx spans every statement a request issues, so the page, the count,
// the three facets and the empty probe all see one snapshot and cannot disagree. No
// `WHERE tenant_id` appears in this package: the app.current_tenant GUC plus FORCE RLS is
// the isolation, and that is the product.
package audit

import (
	"context"
	"errors"
	"fmt"

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
		var err error
		out, err = Query(ctx, tx, f)
		if err != nil {
			return err
		}
		// Only when nothing matched. A non-zero total already proves the log has rows, so
		// probing on a populated request is dead work on the common path
		// (TestAuditStore_SkipsTheEmptyProbeWhenRowsMatched).
		if out.Total == 0 {
			out.LogIsEmpty, err = logIsEmpty(ctx, tx)
		}
		return err
	})
	if err != nil {
		return Response{}, err
	}
	return out, nil
}

// logIsEmpty reports whether the caller's log holds any row at all. It applies NO filter:
// the whole point is to tell a workspace with no history from one whose filters excluded
// everything, and a filtered probe would just re-answer total
// (TestAuditRead_LogIsEmptyIgnoresEveryFilter).
//
// The ORDER BY is what makes the planner choose audit_log_tenant_created_idx; measured,
// the unordered form and SELECT EXISTS are index-served too, so this shape is the story's
// choice rather than the only fast one.
func logIsEmpty(ctx context.Context, tx pgx.Tx) (bool, error) {
	var one int
	err := tx.QueryRow(ctx,
		`SELECT 1 FROM audit_log ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("audit: probe for an empty log: %w", err)
	}
	return false, nil
}
