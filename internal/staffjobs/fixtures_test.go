package staffjobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const devHouseTenant = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"

// dbPools returns the superuser (fixtures) and invoice_app (store) pools, skipping when the DSNs are unset.
func dbPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL, superURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("staffjobs db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-staffjobs`)")
	}
	ctx := context.Background()
	super, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(super.Close)
	app, err = pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(app.Close)
	return super, app
}

// staffCtx returns the context auth.RequireStaff builds for a staff caller: the key is private to auth.
func staffCtx(subject string) context.Context {
	var got context.Context
	h := auth.RequireStaff(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Context() }))
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", Staff: true})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/staff/jobs", nil).WithContext(ctx))
	return got
}

// adoptHouseTenant returns the committed house tenant, writing the dev house row only when the table is empty.
func adoptHouseTenant(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	read := func() (string, error) {
		var id string
		err := super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&id)
		return id, err
	}
	id, err := read()
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the dev house tenant: %v", err)
		}
		if _, err := super.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the house row: %v", err)
		}
		id, err = read()
	}
	if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	return id
}

func seedMembership(t *testing.T, super *pgxpool.Pool, tenant, subject string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')`, tenant, subject); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenant, subject)
	})
}

// houseMember returns the subject of an active member of the house tenant: a staff actor.
func houseMember(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	subject := uuid.NewString()
	seedMembership(t, super, adoptHouseTenant(t, super), subject)
	return subject
}

// customerTenant seeds a customer tenant with one active member, returned as a non-staff actor.
func customerTenant(t *testing.T, super *pgxpool.Pool) (tenant, member string) {
	t.Helper()
	tenant = uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, tenant, "staffjobs "+tenant[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// Deleting the tenant cascades jobs and history; invoices and entities are RESTRICT, so they go first.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = super.Exec(ctx, `DELETE FROM submission_jobs WHERE tenant_id = $1`, tenant)
		_, _ = super.Exec(ctx, `DELETE FROM invoices WHERE tenant_id = $1`, tenant)
		_, _ = super.Exec(ctx, `DELETE FROM business_entities WHERE tenant_id = $1`, tenant)
		_, _ = super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenant)
	})
	member = uuid.NewString()
	seedMembership(t, super, tenant, member)
	return tenant, member
}

// seedInvoice commits an entity and an invoice at status under tenant and returns the invoice id.
func seedInvoice(t *testing.T, super *pgxpool.Pool, tenant, number, status string) string {
	t.Helper()
	ctx := context.Background()
	entity, invoice := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx, `INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`, entity, tenant, "entity-"+entity[:8]); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	if _, err := super.Exec(ctx,
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status) VALUES ($1, $2, $3, $4, $5)`,
		invoice, tenant, entity, number, status); err != nil {
		t.Fatalf("seed invoice %s: %v", number, err)
	}
	return invoice
}

// seedJob commits a submission job at state and returns its id.
func seedJob(t *testing.T, super *pgxpool.Pool, tenant, invoice, state string, createdAt time.Time, lastError *string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state, attempts, last_error, created_at)
		 VALUES ($1, $2, $3, $4, 'mock', 'v1', $5, 3, $6, $7)`,
		id, tenant, invoice, "staffjobs-"+id, state, lastError, createdAt); err != nil {
		t.Fatalf("seed a %s job: %v", state, err)
	}
	return id
}

func seedHistory(t *testing.T, super *pgxpool.Pool, tenant, invoice string, from *string, to string, changedAt time.Time) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, changed_at) VALUES ($1, $2, $3, $4, 'system', $5)`,
		tenant, invoice, from, to, changedAt); err != nil {
		t.Fatalf("seed history %s: %v", to, err)
	}
}
