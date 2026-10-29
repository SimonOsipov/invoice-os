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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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
		// A non-UUID subject is an in-process actor (the seam skips its membership gate): admin.
		// A UUID subject with no membership row is not admin and keeps its own account rows only.
		id, _ := auth.IdentityFromContext(ctx)
		_, uuidErr := uuid.Parse(id.Subject)
		inProcess := uuidErr != nil
		var scope string
		var admin bool
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(current_setting('app.entity_scope', true), ''),
			        coalesce((SELECT m.role = 'admin' FROM memberships m WHERE m.user_id = $1::uuid), false)`,
			nullUUID(id.Subject, uuidErr)).Scan(&scope, &admin); err != nil {
			return fmt.Errorf("audit: read client scope: %w", err)
		}
		f.scope = scopeFromGUC(scope)
		f.scope.admin = admin || inProcess
		f.scope.self = id.Subject
		var err error
		out, err = Query(ctx, tx, f)
		if err != nil {
			return err
		}
		// Only when nothing matched. A non-zero total already proves the log has rows, so
		// probing on a populated request is dead work on the common path
		// (TestAuditStore_SkipsTheEmptyProbeWhenRowsMatched).
		if out.Total == 0 {
			out.LogIsEmpty, err = logIsEmpty(ctx, tx, f.scope)
		}
		return err
	})
	if err != nil {
		return Response{}, err
	}
	return out, nil
}

// logIsEmpty reports whether the caller's log holds any row at all. It applies the caller
// scope and no request filter (TestAuditRead_LogIsEmptyIgnoresEveryFilter).
//
// The ORDER BY is what makes the planner choose audit_log_tenant_created_idx; measured,
// the unordered form and SELECT EXISTS are index-served too, so this shape is the story's
// choice rather than the only fast one. The scoped probe has no ORDER BY: it would sort.
func logIsEmpty(ctx context.Context, tx pgx.Tx, scope callerScope) (bool, error) {
	var one int
	sql := `SELECT 1 FROM audit_log ORDER BY created_at DESC, id DESC LIMIT 1`
	var args []any
	if scope.restricts() {
		where, scopeArgs, err := filterPredicates(Filter{scope: scope}, searchTargets{})
		if err != nil {
			return false, fmt.Errorf("audit: probe scope: %w", err)
		}
		sql = `SELECT 1 FROM audit_log a WHERE ` + where + ` LIMIT 1`
		args = scopeArgs
	}
	err := tx.QueryRow(ctx, sql, args...).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("audit: probe for an empty log: %w", err)
	}
	return false, nil
}

// nullUUID is the subject as a bind value: NULL when it is not a UUID, so the role lookup finds no row.
func nullUUID(subject string, parseErr error) any {
	if parseErr != nil {
		return nil
	}
	return subject
}
