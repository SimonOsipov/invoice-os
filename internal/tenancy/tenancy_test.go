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

// meBody mirrors the GET /v1/me JSON so the handler tests can assert the
// contract, including the M3-02-01 additions (tenant.kind, domain user.role).
type meBody struct {
	Tenant struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"tenant"`
	User struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"user"`
	Error string `json:"error"`
}

func doMe(t *testing.T, load MeLoader, id *auth.Identity) (*httptest.ResponseRecorder, meBody) {
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

// TestMe_OKShape (M3-02-01 Test Specs, AC #1): a loader resolving tenant
// {kind:"firm"} + domain role "admin" must produce 200 with tenant.kind=="firm"
// and user.role=="admin" — the domain role from memberships, NOT the JWT
// "authenticated" claim the identity below deliberately carries instead, so this
// assertion only passes once Stage 3 wires the loader's role into the response.
func TestMe_OKShape(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) (Tenant, string, error) {
		return Tenant{ID: id.TenantID, Name: "Okafor & Partners", Kind: "firm"}, "admin", nil
	}
	rec, body := doMe(t, load, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.Tenant.ID != id.TenantID || body.Tenant.Name != "Okafor & Partners" {
		t.Errorf("tenant = %+v, want id=%s name=Okafor & Partners", body.Tenant, id.TenantID)
	}
	if body.Tenant.Kind != "firm" {
		t.Errorf("tenant.kind = %q, want %q", body.Tenant.Kind, "firm")
	}
	if body.User.ID != "user-1" {
		t.Errorf("user.id = %q, want %q", body.User.ID, "user-1")
	}
	if body.User.Role != "admin" {
		t.Errorf("user.role = %q, want %q (the domain role from memberships, not the JWT role)", body.User.Role, "admin")
	}
}

// TestMe_NoMembership403 (AC #3, A1): ErrNoMembership must map to 403 with a
// non-empty error body — distinct from 401 (no identity) and 404 (no tenant).
func TestMe_NoMembership403(t *testing.T) {
	id := auth.Identity{Subject: "u", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) (Tenant, string, error) { return Tenant{}, "", ErrNoMembership }
	rec, body := doMe(t, load, &id)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestMe_TenantNotFound404 (AC #1): the pre-existing ErrTenantNotFound->404
// mapping must be preserved unchanged by the M3-02-01 loader-signature widening.
func TestMe_TenantNotFound404(t *testing.T) {
	id := auth.Identity{Subject: "u", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) (Tenant, string, error) { return Tenant{}, "", ErrTenantNotFound }
	rec, body := doMe(t, load, &id)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestMe_NoTenantCtx401 (AC #3): the pre-existing db.ErrNoTenant->401 fail-closed
// mapping must be preserved unchanged.
func TestMe_NoTenantCtx401(t *testing.T) {
	id := auth.Identity{Subject: "u", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) (Tenant, string, error) { return Tenant{}, "", db.ErrNoTenant }
	rec, body := doMe(t, load, &id)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestMe_NoIdentity401 (AC #1): no identity in the request context must 401
// before the loader ever runs — asserted by failing the test if load is called.
func TestMe_NoIdentity401(t *testing.T) {
	load := func(context.Context) (Tenant, string, error) {
		t.Fatal("loader must not run without an identity")
		return Tenant{}, "", nil
	}
	rec, body := doMe(t, load, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestMe_InternalError500: an unrecognized loader error must map to 500, not
// leak internals into the body, but still include a non-empty error message.
func TestMe_InternalError500(t *testing.T) {
	id := auth.Identity{Subject: "u", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) (Tenant, string, error) { return Tenant{}, "", errors.New("boom") }
	rec, body := doMe(t, load, &id)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// dbTestPools returns the superuser (seed) and app-role (Store) pools for the
// tenancy db-integration suite below, or skips the test when the per-role DSNs
// are unset — the same env gate `make test-rls` and the pre-existing
// TestCurrentTenant_RoundTrip used (DATABASE_URL for invoice_app,
// DATABASE_SUPERUSER_URL for seeding as the BYPASSRLS superuser).
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("tenancy db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	// Registered before the app pool's Cleanup, so per LIFO ordering it closes
	// AFTER app's pool — and callers that register a row-delete Cleanup of their
	// own (after calling dbTestPools) get it run BEFORE either pool closes.
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// TestStoreMe_ResolvesTenantAndRole (M3-02-01 Test Specs, AC #1): a
// superuser-seeded kind='firm' tenant plus a membership (user, 'admin') must
// resolve, via Store.Me, to tenant{id,name,kind:"firm"} and role "admin".
// Requires DATABASE_URL (invoice_app) + DATABASE_SUPERUSER_URL (seed); run via
// `make test-rls` or with both env vars set directly.
func TestStoreMe_ResolvesTenantAndRole(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	const tenantName = "tenancy me-test firm"

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, tenantID, tenantName); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin')`, tenantID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})
	tenant, role, err := store.Me(c)
	if err != nil {
		t.Fatalf("Me(%s): %v", tenantID, err)
	}
	if tenant.ID != tenantID || tenant.Name != tenantName || tenant.Kind != "firm" {
		t.Errorf("tenant = %+v, want id=%s name=%s kind=firm", tenant, tenantID, tenantName)
	}
	if role != "admin" {
		t.Errorf("role = %q, want %q", role, "admin")
	}
}

// TestStoreMe_NoMembershipFailsClosed (AC #3, A1): a seeded, visible tenant with
// NO membership row for the caller must resolve to ErrNoMembership, never a
// defaulted role.
func TestStoreMe_NoMembershipFailsClosed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy me-test no-membership', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	_, _, err := store.Me(c)
	if !errors.Is(err, ErrNoMembership) {
		t.Fatalf("Me err = %v, want ErrNoMembership", err)
	}
}

// TestStoreMe_UnknownTenant (AC #1): a well-formed tenant id with no visible row
// (RLS makes it invisible / it does not exist) must resolve to ErrTenantNotFound.
func TestStoreMe_UnknownTenant(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()})
	_, _, err := store.Me(c)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("Me err = %v, want ErrTenantNotFound", err)
	}
}

// TestStoreMe_NoIdentityFailsClosed (AC #3): a context with no identity must
// fail closed with db.ErrNoTenant before any statement runs (the
// WithinRequestTenantTx contract).
func TestStoreMe_NoIdentityFailsClosed(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	store := NewStore(app)
	_, _, err := store.Me(ctx)
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("Me err = %v, want db.ErrNoTenant", err)
	}
}

// TestStoreMe_RolePerTenant (AC #3): the SAME user_id seeded as 'admin' in
// tenant A and 'preparer' in tenant B must resolve to the role of whichever
// tenant is current — proving role resolution is scoped to the current tenant,
// not merely to the user.
func TestStoreMe_RolePerTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	userID := uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy me-test role A', 'firm'), ($2, 'tenancy me-test role B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin'), ($3, $2, 'preparer')`,
		tenantA, userID, tenantB); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	store := NewStore(app)

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantA})
	_, roleA, err := store.Me(cA)
	if err != nil {
		t.Fatalf("Me(tenant A): %v", err)
	}
	if roleA != "admin" {
		t.Errorf("role in tenant A = %q, want %q", roleA, "admin")
	}

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantB})
	_, roleB, err := store.Me(cB)
	if err != nil {
		t.Fatalf("Me(tenant B): %v", err)
	}
	if roleB != "preparer" {
		t.Errorf("role in tenant B = %q, want %q", roleB, "preparer")
	}
}

// TestStoreMe_CrossTenantRoleBorrowFailsClosed (AC #3 adversarial, QA-added
// task-29): the SAME user_id seeded as 'admin' in tenant A ONLY must NOT
// resolve any role when the caller's current tenant is B (a real, seeded
// tenant where the user holds no membership row). This is the load-bearing
// isolation property of role resolution: Store.Me's membership query has no
// explicit `AND tenant_id = ...` clause (see store.go) — it relies entirely on
// the memberships table's tenant_isolation RLS policy to scope
// `WHERE user_id = $1` to the current tenant. If that policy (or the GUC
// plumbing) ever regressed, this test is what would catch a user borrowing
// their role from a tenant they are not currently acting in.
func TestStoreMe_CrossTenantRoleBorrowFailsClosed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	userID := uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test borrow A', 'firm'), ($2, 'tenancy qa-test borrow B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})
	// U is admin in A ONLY — deliberately no membership row in B.
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin')`,
		tenantA, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	store := NewStore(app)
	// Caller's current tenant is B, not A — U must not borrow A's admin role.
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantB})
	_, role, err := store.Me(c)
	if !errors.Is(err, ErrNoMembership) {
		t.Fatalf("Me(tenant B) err = %v, role = %q, want ErrNoMembership (must not borrow tenant A's admin role)", err, role)
	}
}

// TestStoreMe_RoleValueIntegrity (AC #1/#3 adversarial, QA-added task-29):
// seeding the caller as 'preparer' (not 'admin') must resolve to exactly
// "preparer" — guards against a hardcoded/defaulted role value that would
// happen to pass the 'admin'-only assertions in TestStoreMe_ResolvesTenantAndRole.
func TestStoreMe_RoleValueIntegrity(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test role-integrity', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'preparer')`, tenantID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})
	_, role, err := store.Me(c)
	if err != nil {
		t.Fatalf("Me(%s): %v", tenantID, err)
	}
	if role != "preparer" {
		t.Errorf("role = %q, want exactly %q (not admin/blank/defaulted)", role, "preparer")
	}
}

// TestStoreMe_RoleIsCatalogValueForEachRole (AC #1/#3 adversarial, QA-added
// task-29): for each of the three catalog roles (roles table:
// admin/preparer/reviewer — migrations/20260709151759_roles.sql), a caller
// seeded with that role must get back that EXACT non-empty string — no code
// path may return ("", nil) on success. Covers 'reviewer', the one role none
// of the Stage 2.5 tests exercised.
func TestStoreMe_RoleIsCatalogValueForEachRole(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	for _, want := range []string{"admin", "preparer", "reviewer"} {
		t.Run(want, func(t *testing.T) {
			tenantID := uuid.NewString()
			userID := uuid.NewString()

			if _, err := super.Exec(ctx,
				`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test catalog-role', 'firm')`, tenantID); err != nil {
				t.Fatalf("seed tenant: %v", err)
			}
			t.Cleanup(func() {
				_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
			})
			if _, err := super.Exec(ctx,
				`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, $3)`, tenantID, userID, want); err != nil {
				t.Fatalf("seed membership: %v", err)
			}

			c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})
			_, role, err := store.Me(c)
			if err != nil {
				t.Fatalf("Me(%s): %v", tenantID, err)
			}
			if role == "" {
				t.Fatal("role = \"\" on success — a role must never be empty/defaulted")
			}
			if role != want {
				t.Errorf("role = %q, want %q", role, want)
			}
		})
	}
}
