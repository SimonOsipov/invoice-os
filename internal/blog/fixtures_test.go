package blog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
		t.Skip("blog db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-blog`)")
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
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/staff/blog/categories", nil).WithContext(ctx))
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

// staffActor seeds a house member and returns its uuid and a staff context.
func staffActor(t *testing.T, super *pgxpool.Pool) (string, context.Context) {
	t.Helper()
	subject := uuid.NewString()
	seedMembership(t, super, adoptHouseTenant(t, super), subject)
	return subject, staffCtx(subject)
}

// uniq returns a slug-safe suffix; cleanupBlog deletes every row whose id holds it.
func uniq(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	r := "t" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = super.Exec(ctx, `DELETE FROM public.blog_posts WHERE id LIKE '%' || $1 || '%'`, r)
		_, _ = super.Exec(ctx, `DELETE FROM public.blog_categories WHERE id LIKE '%' || $1 || '%'`, r)
	})
	return r
}

type auditRow struct {
	Event   string
	Payload map[string]any
	Tenant  *string
	Subject *string
}

// auditRowsOf reads the actor's staff_audit_log rows oldest first, as the owner.
func auditRowsOf(t *testing.T, super *pgxpool.Pool, actor string) []auditRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT event, payload::text, tenant_id::text, subject_id::text FROM staff_audit_log WHERE actor = $1 ORDER BY id`, actor)
	if err != nil {
		t.Fatalf("read staff_audit_log: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var payload string
		if err := rows.Scan(&r.Event, &payload, &r.Tenant, &r.Subject); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(payload), &r.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}
