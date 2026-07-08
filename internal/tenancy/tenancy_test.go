package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// meBody mirrors the GET /v1/me JSON so the handler tests can assert the contract.
type meBody struct {
	Tenant struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"tenant"`
	User struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"user"`
	Error string `json:"error"`
}

func doMe(t *testing.T, load TenantLoader, id *auth.Identity) (*httptest.ResponseRecorder, meBody) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/me", nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	MeHandler(load, nil).ServeHTTP(rec, r)
	var body meBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestMeHandler_OK(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: "11111111-1111-1111-1111-111111111111"}
	load := func(context.Context) (Tenant, error) {
		return Tenant{ID: id.TenantID, Name: "Okafor & Partners"}, nil
	}
	rec, body := doMe(t, load, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.Tenant.ID != id.TenantID || body.Tenant.Name != "Okafor & Partners" {
		t.Errorf("tenant = %+v, want id=%s name=Okafor & Partners", body.Tenant, id.TenantID)
	}
	if body.User.ID != "user-1" || body.User.Role != "authenticated" {
		t.Errorf("user = %+v, want id=user-1 role=authenticated", body.User)
	}
}

func TestMeHandler_NoIdentity401(t *testing.T) {
	load := func(context.Context) (Tenant, error) {
		t.Fatal("loader must not run without an identity")
		return Tenant{}, nil
	}
	rec, _ := doMe(t, load, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context", rec.Code)
	}
}

func TestMeHandler_ErrorMapping(t *testing.T) {
	id := auth.Identity{Subject: "u", Role: "authenticated", TenantID: uuid.NewString()}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"no tenant fails closed", db.ErrNoTenant, http.StatusUnauthorized},
		{"unknown tenant", ErrTenantNotFound, http.StatusNotFound},
		{"unexpected error", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			load := func(context.Context) (Tenant, error) { return Tenant{}, tc.err }
			rec, body := doMe(t, load, &id)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			if body.Error == "" {
				t.Error("expected an error message in the body")
			}
		})
	}
}

// TestCurrentTenant_RoundTrip proves the M2-13 claim end to end against a real
// Postgres: Store.CurrentTenant's bare `SELECT ... FROM tenants` returns exactly the
// caller's tenant because RLS + the app.current_tenant GUC (set by WithinRequestTenantTx)
// is the filter — not a WHERE clause. It also pins the fail-closed and not-found paths.
// It reuses the same Postgres-service-container + role-bootstrap path as the CI `rls`
// job and SKIPS itself when the per-role URLs are absent, so `go test ./...` stays green
// without a database. Requires DATABASE_URL (invoice_app) + DATABASE_SUPERUSER_URL (seed).
func TestCurrentTenant_RoundTrip(t *testing.T) {
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("tenancy round-trip skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	super, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	// Register pool closes via Cleanup (not defer) so they run AFTER the row-delete
	// Cleanup below — Cleanups run LIFO, and a delete on a closed pool would no-op.
	t.Cleanup(super.Close)
	if err := super.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(app.Close)

	// Seed two tenants as the superuser (BYPASSRLS, so no tenant context needed).
	// Random ids stay collision-free against any pre-existing rows in a shared dev DB.
	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'tenancy me-test A'), ($2, 'tenancy me-test B')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	// Registered last → runs first (before the pool closes above), so the delete
	// executes against a still-open superuser pool.
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	store := NewStore(app)

	// As tenant A, the bare SELECT resolves to A only; as tenant B, to B only.
	for _, tc := range []struct{ id, name string }{
		{tenantA, "tenancy me-test A"},
		{tenantB, "tenancy me-test B"},
	} {
		c := auth.WithIdentity(ctx, auth.Identity{Subject: "u", Role: "authenticated", TenantID: tc.id})
		got, err := store.CurrentTenant(c)
		if err != nil {
			t.Fatalf("CurrentTenant(%s): %v", tc.id, err)
		}
		if got.ID != tc.id || got.Name != tc.name {
			t.Errorf("CurrentTenant = %+v, want id=%s name=%s", got, tc.id, tc.name)
		}
	}

	// A well-formed tenant id with no row → ErrTenantNotFound (RLS makes it invisible).
	unknown := auth.WithIdentity(ctx, auth.Identity{Subject: "u", Role: "authenticated", TenantID: uuid.NewString()})
	if _, err := store.CurrentTenant(unknown); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("unknown tenant err = %v, want ErrTenantNotFound", err)
	}

	// No identity → fail closed before any statement runs.
	if _, err := store.CurrentTenant(ctx); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("no-identity err = %v, want db.ErrNoTenant", err)
	}
}
