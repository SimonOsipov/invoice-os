package tenancy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type StaffTenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
	// Demo is true for ids in db.DemoTenants.
	Demo           bool       `json:"demo"`
	ActiveMembers  int64      `json:"active_members"`
	InvoiceCount   int64      `json:"invoice_count"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

// StaffTenants lists every tenant with its counts through the staff_read_ functions, which refuse a non-house actor.
// ceiling: unpaged list; page it above ~1k tenants
func (s *Store) StaffTenants(ctx context.Context) ([]StaffTenant, error) {
	id, ok := auth.StaffFromContext(ctx)
	if !ok {
		return nil, db.ErrNotStaff
	}
	actor, err := uuid.Parse(id.Subject)
	if err != nil {
		return nil, db.ErrNotStaff
	}
	var out []StaffTenant
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT t.id::text, t.name, t.kind, t.created_at, t.id::text = ANY($2::text[]),
			coalesce(a.active_members, 0), coalesce(a.invoice_count, 0), a.last_activity_at
			FROM public.staff_read_tenants($1) t
			LEFT JOIN public.staff_read_tenant_activity($1) a ON a.tenant_id = t.id
			ORDER BY a.last_activity_at DESC NULLS LAST, t.created_at DESC, t.id`, actor, db.DemoTenants)
		if err != nil {
			return staffReadError("tenancy: staff list tenants", err)
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[StaffTenant])
		return staffReadError("tenancy: staff list tenants", err)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// staffReadError maps only the function's own refusal to ErrNotStaff; a lost grant is also 42501 and stays a server fault.
func staffReadError(op string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.HasSuffix(pgErr.Message, ": actor is not staff") {
		return db.ErrNotStaff
	}
	return fmt.Errorf("%s: %w", op, err)
}

// StaffTenantsHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffTenantsHandler(list func(context.Context) ([]StaffTenant, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenants, err := list(r.Context())
		switch {
		case errors.Is(err, db.ErrNotStaff):
			writeError(w, http.StatusForbidden, "forbidden")
		case err != nil:
			log.Error("tenancy: staff tenants response", "err", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		default:
			if tenants == nil {
				tenants = []StaffTenant{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"tenants": tenants})
		}
	}
}
