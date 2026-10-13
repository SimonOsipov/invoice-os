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
}

// StaffTenants lists every tenant through staff_read_tenants, which refuses a non-house actor.
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
		rows, err := tx.Query(ctx, `SELECT id::text, name, kind, created_at FROM public.staff_read_tenants($1)`, actor)
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
