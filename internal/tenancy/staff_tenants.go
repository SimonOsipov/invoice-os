package tenancy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return nil, err
	}
	var out []StaffTenant
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT t.id::text, t.name, t.kind, t.created_at, t.id::text = ANY($2::text[]),
			coalesce(a.active_members, 0), coalesce(a.invoice_count, 0), a.last_activity_at
			FROM public.staff_read_tenants($1) t
			LEFT JOIN public.staff_read_tenant_activity($1) a ON a.tenant_id = t.id
			ORDER BY a.last_activity_at DESC NULLS LAST, t.created_at DESC, t.id`, actor, db.DemoTenants)
		if err != nil {
			return db.StaffReadError("tenancy: staff list tenants", err)
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[StaffTenant])
		return db.StaffReadError("tenancy: staff list tenants", err)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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

type StaffInvoice struct {
	ID            string    `json:"id"`
	InvoiceNumber string    `json:"invoice_number"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type StaffTenantDetail struct {
	Tenant   StaffTenant    `json:"tenant"`
	Members  []Membership   `json:"members"`
	Invoices []StaffInvoice `json:"invoices"`
}

// StaffTenant reads one tenant, its members and its 10 newest invoices. READ COMMITTED: the header counts can trail the lists by one write.
func (s *Store) StaffTenant(ctx context.Context, id string) (StaffTenantDetail, error) {
	var out StaffTenantDetail
	actor, err := db.StaffActor(ctx)
	if err != nil {
		return out, err
	}
	tenant, err := uuid.Parse(id)
	if err != nil {
		return out, ErrTenantNotFound
	}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT t.id::text, t.name, t.kind, t.created_at, t.id::text = ANY($2::text[]),
			coalesce(a.active_members, 0), coalesce(a.invoice_count, 0), a.last_activity_at
			FROM public.staff_read_tenants($1) t
			LEFT JOIN public.staff_read_tenant_activity($1) a ON a.tenant_id = t.id
			WHERE t.id = $3`, actor, db.DemoTenants, tenant)
		if err != nil {
			return db.StaffReadError("tenancy: staff read tenant", err)
		}
		header, err := pgx.CollectRows(rows, pgx.RowToStructByPos[StaffTenant])
		if err != nil {
			return db.StaffReadError("tenancy: staff read tenant", err)
		}
		if len(header) == 0 {
			return ErrTenantNotFound
		}
		out.Tenant = header[0]

		rows, err = tx.Query(ctx, `SELECT user_id::text, role, status, display_name, email
			FROM public.staff_read_tenant_members($1, $2)`, actor, tenant)
		if err != nil {
			return db.StaffReadError("tenancy: staff read tenant", err)
		}
		if out.Members, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Membership]); err != nil {
			return db.StaffReadError("tenancy: staff read tenant", err)
		}

		rows, err = tx.Query(ctx, `SELECT id::text, invoice_number, status, created_at
			FROM public.staff_read_tenant_invoices($1, $2)`, actor, tenant)
		if err != nil {
			return db.StaffReadError("tenancy: staff read tenant", err)
		}
		out.Invoices, err = pgx.CollectRows(rows, pgx.RowToStructByPos[StaffInvoice])
		return db.StaffReadError("tenancy: staff read tenant", err)
	})
	if err != nil {
		return StaffTenantDetail{}, err
	}
	return out, nil
}

// StaffTenantHandler repeats no staff check: auth.RequireStaff runs before it.
func StaffTenantHandler(get func(context.Context, string) (StaffTenantDetail, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail, err := get(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, db.ErrNotStaff):
			writeError(w, http.StatusForbidden, "forbidden")
		case errors.Is(err, ErrTenantNotFound):
			writeError(w, http.StatusNotFound, "tenant not found")
		case err != nil:
			log.Error("tenancy: staff tenant response", "err", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		default:
			if detail.Members == nil {
				detail.Members = []Membership{}
			}
			if detail.Invoices == nil {
				detail.Invoices = []StaffInvoice{}
			}
			writeJSON(w, http.StatusOK, detail)
		}
	}
}
