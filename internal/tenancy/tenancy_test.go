package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
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

// TestMe_UserKeySetUnchanged (AC-4, regression guard -- expected to pass
// today and must stay green): GET /v1/me's user object key set is exactly
// {id, role}, pinning that /v1/me is byte-unchanged by the memberships
// widening.
func TestMe_UserKeySetUnchanged(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) (Tenant, string, error) {
		return Tenant{ID: id.TenantID, Name: "Okafor & Partners", Kind: "firm"}, "admin", nil
	}
	r := httptest.NewRequest("GET", "/v1/me", nil)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	MeHandler(load, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	var user map[string]json.RawMessage
	if err := json.Unmarshal(body["user"], &user); err != nil {
		t.Fatalf("decode user %q: %v", body["user"], err)
	}

	want := []string{"id", "role"}
	got := make([]string, 0, len(user))
	for k := range user {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("user keys = %v, want %v", got, want)
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
// fail closed with db.ErrNoTenant before any statement runs. Me reads the
// identity itself (AUDIT-10 §5 exempts it from the request seam's membership
// gate), so this pins the fail-closed behaviour independently of which db helper
// Me happens to call.
func TestStoreMe_NoIdentityFailsClosed(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	store := NewStore(app)
	_, _, err := store.Me(ctx)
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("Me err = %v, want db.ErrNoTenant", err)
	}
}

// TestStoreMe_AnswersForASuspendedMember (AUDIT-10 AC-5, D-5): /v1/me is the ONE
// deliberate hole in the read-path gate. auth.ts signIn throws on failure, so
// gating it turns every suspended session into an unexplained sign-in failure
// with nothing able to say why. The body must be unchanged, not merely non-empty.
func TestStoreMe_AnswersForASuspendedMember(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	const tenantName = "tenancy me-test suspended firm"
	tenantID := seedTenant(t, super, tenantName)
	userID := uuid.NewString()
	seedMembership(t, super, tenantID, userID, "admin", "suspended")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})
	tenant, role, err := store.Me(c)
	if err != nil {
		t.Fatalf("Me: a suspended member must still get their boot payload, got %v", err)
	}
	if tenant.ID != tenantID {
		t.Errorf("tenant.ID = %q, want %q", tenant.ID, tenantID)
	}
	if tenant.Name != tenantName {
		t.Errorf("tenant.Name = %q, want %q", tenant.Name, tenantName)
	}
	if tenant.Kind != "firm" {
		t.Errorf("tenant.Kind = %q, want %q", tenant.Kind, "firm")
	}
	if role != "admin" {
		t.Errorf("role = %q, want %q", role, "admin")
	}
}

// TestMe_SuspendedMemberStillGets200 (AUDIT-10 AC-5, D-5): the store-level exemption
// has to reach the wire. MeHandler runs over the REAL Store.Me here --
// TestStoreMe_AnswersForASuspendedMember stops at the store and TestMe_OKShape uses a
// fake loader, so neither sees a gated Me turn into a non-200.
func TestMe_SuspendedMemberStillGets200(t *testing.T) {
	super, app := dbTestPools(t)

	const tenantName = "tenancy me-handler suspended firm"
	tenantID := seedTenant(t, super, tenantName)
	userID := uuid.NewString()
	seedMembership(t, super, tenantID, userID, "admin", "suspended")

	id := auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID}
	rec, body := doMe(t, NewStore(app).Me, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if body.Error != "" {
		t.Errorf("error = %q, want empty", body.Error)
	}
	if body.Tenant.ID != tenantID {
		t.Errorf("tenant.id = %q, want %q", body.Tenant.ID, tenantID)
	}
	if body.Tenant.Name != tenantName {
		t.Errorf("tenant.name = %q, want %q", body.Tenant.Name, tenantName)
	}
	if body.Tenant.Kind != "firm" {
		t.Errorf("tenant.kind = %q, want %q", body.Tenant.Kind, "firm")
	}
	if body.User.ID != userID {
		t.Errorf("user.id = %q, want %q", body.User.ID, userID)
	}
	if body.User.Role != "admin" {
		t.Errorf("user.role = %q, want %q", body.User.Role, "admin")
	}
}

// TestStoreListMemberships_RefusesASuspendedMember (AUDIT-10 AC-6): every other
// tenancy read stays gated -- D-5 rejected extending Me's exemption to
// /v1/memberships.
func TestStoreListMemberships_RefusesASuspendedMember(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "tenancy list-memberships suspended firm")
	userID := uuid.NewString()
	seedMembership(t, super, tenantID, userID, "admin", "suspended")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})
	got, err := store.ListMemberships(c)
	if !errors.Is(err, db.ErrNotActiveMember) {
		t.Fatalf("ListMemberships err = %v, want db.ErrNotActiveMember", err)
	}
	if got != nil {
		t.Errorf("ListMemberships returned %d row(s) alongside the refusal, want none: %+v", len(got), got)
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

// membershipsBody mirrors the GET /v1/memberships JSON so the handler tests
// can assert the contract (A3: snake_case {user_id, role} items) and, for
// TestMemberships_Empty200, inspect the raw JSON of the memberships field
// directly -- json.RawMessage lets that test distinguish a literal `[]` from
// `null`, which decoding straight into a Go slice could not (both unmarshal
// to a zero-length/nil slice).
type membershipsBody struct {
	Memberships json.RawMessage `json:"memberships"`
	Error       string          `json:"error"`
}

func doMemberships(t *testing.T, load MembershipsLoader, id *auth.Identity) (*httptest.ResponseRecorder, membershipsBody) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/memberships", nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	MembershipsHandler(load, nil).ServeHTTP(rec, r)
	var body membershipsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

// TestMemberships_OK (M3-02-02 Test Specs, AC #2): a loader resolving two
// memberships must produce 200 with both {user_id,role} items, in the
// loader's order.
func TestMemberships_OK(t *testing.T) {
	id := auth.Identity{Subject: "caller", Role: "authenticated", TenantID: uuid.NewString()}
	want := []Membership{{UserID: "u1", Role: "admin"}, {UserID: "u2", Role: "preparer"}}
	load := func(context.Context) ([]Membership, error) { return want, nil }
	rec, body := doMemberships(t, load, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var items []struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := json.Unmarshal(body.Memberships, &items); err != nil {
		t.Fatalf("decode memberships %q: %v", body.Memberships, err)
	}
	if len(items) != len(want) {
		t.Fatalf("len(memberships) = %d, want %d: %+v", len(items), len(want), items)
	}
	for i, m := range want {
		if items[i].UserID != m.UserID || items[i].Role != m.Role {
			t.Errorf("memberships[%d] = %+v, want {user_id:%s role:%s}", i, items[i], m.UserID, m.Role)
		}
	}
}

// TestMemberships_Empty200 (AC #2, A4): an empty loader result must still be
// 200 with the memberships field serialized as a literal `[]`, never `null`
// -- a client that does `res.memberships.map(...)` must not crash on a bare
// null field.
func TestMemberships_Empty200(t *testing.T) {
	id := auth.Identity{Subject: "caller", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) ([]Membership, error) { return []Membership{}, nil }
	rec, body := doMemberships(t, load, &id)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if string(body.Memberships) != "[]" {
		t.Errorf("memberships raw JSON = %s, want [] (not null)", body.Memberships)
	}
}

// TestMemberships_NoIdentity401 (AC #2): no identity in the request context
// must 401 before the loader ever runs -- asserted by failing the test if
// load is called.
func TestMemberships_NoIdentity401(t *testing.T) {
	load := func(context.Context) ([]Membership, error) {
		t.Fatal("loader must not run without an identity")
		return nil, nil
	}
	rec, body := doMemberships(t, load, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestMemberships_NoTenantCtx401 (AC #2, A4): the loader returning
// db.ErrNoTenant must fail closed with 401, same as MeHandler.
func TestMemberships_NoTenantCtx401(t *testing.T) {
	id := auth.Identity{Subject: "caller", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) ([]Membership, error) { return nil, db.ErrNoTenant }
	rec, body := doMemberships(t, load, &id)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestMemberships_Error500 (AC #2, A4): any other loader error must map to
// 500 -- and specifically NEVER 403/404, since A4 says the member-list does
// not independently gate on the caller holding a membership.
func TestMemberships_Error500(t *testing.T) {
	id := auth.Identity{Subject: "caller", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) ([]Membership, error) { return nil, errors.New("boom") }
	rec, body := doMemberships(t, load, &id)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestMemberships_ProjectsFiveKeys (AC-1, RED until Membership widens): a
// loader-returned membership must serialize with exactly the identity
// projection's five keys -- user_id, role, status, display_name, email --
// no fewer, no more.
func TestMemberships_ProjectsFiveKeys(t *testing.T) {
	id := auth.Identity{Subject: "caller", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) ([]Membership, error) {
		return []Membership{{UserID: "u1", Role: "admin"}}, nil
	}
	rec, body := doMemberships(t, load, &id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body.Memberships, &items); err != nil {
		t.Fatalf("decode memberships %q: %v", body.Memberships, err)
	}
	if len(items) != 1 {
		t.Fatalf("len(memberships) = %d, want 1", len(items))
	}

	want := []string{"display_name", "email", "role", "status", "user_id"}
	got := make([]string, 0, len(items[0]))
	for k := range items[0] {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("memberships[0] keys = %v, want %v", got, want)
	}
}

// TestMemberships_NullIdentityKeysArePresent (AC-1, RED until Membership
// widens): a membership with no display_name/email on file must still
// serialize both keys as explicit JSON null -- never omitted.
func TestMemberships_NullIdentityKeysArePresent(t *testing.T) {
	id := auth.Identity{Subject: "caller", Role: "authenticated", TenantID: uuid.NewString()}
	load := func(context.Context) ([]Membership, error) {
		return []Membership{{UserID: "u1", Role: "admin"}}, nil
	}
	rec, body := doMemberships(t, load, &id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body.Memberships, &items); err != nil {
		t.Fatalf("decode memberships %q: %v", body.Memberships, err)
	}
	if len(items) != 1 {
		t.Fatalf("len(memberships) = %d, want 1", len(items))
	}

	for _, key := range []string{"display_name", "email"} {
		raw, ok := items[0][key]
		if !ok {
			t.Errorf("memberships[0] missing key %q, want present with JSON null", key)
			continue
		}
		if string(raw) != "null" {
			t.Errorf("memberships[0][%q] = %s, want null", key, raw)
		}
	}
}

// TestStoreListMemberships_OwnTenantOnly (M3-02-02 Test Specs, core AC #6 --
// service-layer isolation): tenant A seeded with 3 memberships (admin,
// preparer, reviewer) and tenant B seeded with 1 unrelated membership;
// ListMemberships as tenant A must return exactly A's 3 rows -- B's row must
// be absent. This is the service-layer half of core AC #6 (the RLS half is
// already covered by internal/platform/db/memberships_rls_test.go, M3-01).
func TestStoreListMemberships_OwnTenantOnly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	userA1, userA2, userA3 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	userB := uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test memberships A', 'firm'), ($2, 'tenancy qa-test memberships B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'preparer'), ($1, $4, 'reviewer')`,
		tenantA, userA1, userA2, userA3); err != nil {
		t.Fatalf("seed tenant A memberships: %v", err)
	}
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin')`,
		tenantB, userB); err != nil {
		t.Fatalf("seed tenant B membership: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userA1, Role: "authenticated", TenantID: tenantA})
	got, err := store.ListMemberships(c)
	if err != nil {
		t.Fatalf("ListMemberships(tenant A): %v", err)
	}

	want := map[string]string{userA1: "admin", userA2: "preparer", userA3: "reviewer"}
	if len(got) != len(want) {
		t.Fatalf("len(memberships) = %d, want %d: %+v", len(got), len(want), got)
	}
	for _, m := range got {
		if m.UserID == userB {
			t.Fatalf("tenant B's membership (user %s) leaked into tenant A's list: %+v", userB, got)
		}
		wantRole, ok := want[m.UserID]
		if !ok {
			t.Errorf("unexpected membership user_id %q in tenant A's list", m.UserID)
			continue
		}
		if m.Role != wantRole {
			t.Errorf("role for %s = %q, want %q", m.UserID, m.Role, wantRole)
		}
	}
}

// TestStoreListMemberships_DeterministicOrder (AC #2): 3 rows seeded in one
// tenant; calling ListMemberships twice must produce identical order (ORDER
// BY created_at, user_id) -- the ordering guarantee the member-list response
// depends on for stable rendering across repeated requests.
func TestStoreListMemberships_DeterministicOrder(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	callerID := uuid.NewString()
	u1, u2 := uuid.NewString(), uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test memberships order', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'preparer'), ($1, $4, 'reviewer')`,
		tenantID, callerID, u1, u2); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: callerID, Role: "authenticated", TenantID: tenantID})

	first, err := store.ListMemberships(c)
	if err != nil {
		t.Fatalf("ListMemberships (1st call): %v", err)
	}
	second, err := store.ListMemberships(c)
	if err != nil {
		t.Fatalf("ListMemberships (2nd call): %v", err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("len(first)=%d len(second)=%d, want 3 each", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("order differs at index %d: first=%+v second=%+v", i, first[i], second[i])
		}
	}
}

// TestStoreListMemberships_NoContextFailsClosed (AC #2, A4): a context with
// no identity must fail closed with db.ErrNoTenant before any statement
// runs -- the WithinRequestTenantTx contract, same as Store.Me.
func TestStoreListMemberships_NoContextFailsClosed(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()

	store := NewStore(app)
	got, err := store.ListMemberships(ctx)
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("ListMemberships err = %v, want db.ErrNoTenant", err)
	}
	if got != nil {
		t.Errorf("ListMemberships rows = %+v, want nil on fail-closed error", got)
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

// TestStoreListMemberships_ReverseIsolation (core AC #6 adversarial, QA-added
// task-30): seeds tenant A with 2 memberships and tenant B with 3, then calls
// ListMemberships as EACH tenant in turn. TestStoreListMemberships_OwnTenantOnly
// only proves B's row can't leak into A's list; this proves isolation holds in
// both directions -- A's rows must not leak into B's list either, and each
// tenant's count must match exactly what was seeded for it (not the total).
func TestStoreListMemberships_ReverseIsolation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	a1, a2 := uuid.NewString(), uuid.NewString()
	b1, b2, b3 := uuid.NewString(), uuid.NewString(), uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test reverse-iso A', 'firm'), ($2, 'tenancy qa-test reverse-iso B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'preparer')`,
		tenantA, a1, a2); err != nil {
		t.Fatalf("seed tenant A memberships: %v", err)
	}
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'preparer'), ($1, $4, 'reviewer')`,
		tenantB, b1, b2, b3); err != nil {
		t.Fatalf("seed tenant B memberships: %v", err)
	}

	store := NewStore(app)

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: a1, Role: "authenticated", TenantID: tenantA})
	gotA, err := store.ListMemberships(cA)
	if err != nil {
		t.Fatalf("ListMemberships(tenant A): %v", err)
	}
	if len(gotA) != 2 {
		t.Fatalf("len(tenant A memberships) = %d, want 2: %+v", len(gotA), gotA)
	}
	for _, m := range gotA {
		if m.UserID == b1 || m.UserID == b2 || m.UserID == b3 {
			t.Fatalf("tenant B's membership (user %s) leaked into tenant A's list: %+v", m.UserID, gotA)
		}
	}

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: b1, Role: "authenticated", TenantID: tenantB})
	gotB, err := store.ListMemberships(cB)
	if err != nil {
		t.Fatalf("ListMemberships(tenant B): %v", err)
	}
	if len(gotB) != 3 {
		t.Fatalf("len(tenant B memberships) = %d, want 3: %+v", len(gotB), gotB)
	}
	for _, m := range gotB {
		if m.UserID == a1 || m.UserID == a2 {
			t.Fatalf("tenant A's membership (user %s) leaked into tenant B's list: %+v", m.UserID, gotB)
		}
	}
}

// TestStoreListMemberships_EmptyTenantReturnsEmptySlice (AC #2/A4 adversarial,
// QA-added task-30): a seeded, visible tenant with ZERO membership rows must
// resolve to a non-nil, zero-length slice and a nil error at the SERVICE
// LAYER -- complements the handler-level TestMemberships_Empty200 (which only
// exercises a stubbed loader) by proving Store.ListMemberships itself, backed
// by a real RLS-scoped query with no rows to return, never produces (nil, nil)
// (which would defeat the handler's nil-normalization) nor a spurious error.
func TestStoreListMemberships_EmptyTenantReturnsEmptySlice(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test memberships empty', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	// Deliberately NO membership rows seeded for this tenant.

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})
	got, err := store.ListMemberships(c)
	if err != nil {
		t.Fatalf("ListMemberships(empty tenant): %v", err)
	}
	if got == nil {
		t.Fatal("ListMemberships(empty tenant) = nil slice, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(memberships) = %d, want 0: %+v", len(got), got)
	}
}

// TestStoreListMemberships_ContentFidelity (core AC #2 adversarial, QA-added
// task-30): seeds one tenant with a known {user_id,role} triple for EACH of
// the three catalog roles and asserts the returned set matches EXACTLY --
// every seeded user_id present with its correct role. Guards against a
// column-order/scan mix-up (e.g. `SELECT user_id, role` accidentally scanned
// as role-then-user_id) that TestStoreListMemberships_OwnTenantOnly's
// same-role-per-index style could theoretically miss if roles collided.
func TestStoreListMemberships_ContentFidelity(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	uAdmin, uPreparer, uReviewer := uuid.NewString(), uuid.NewString(), uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test content-fidelity', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role) VALUES ($1, $2, 'admin'), ($1, $3, 'preparer'), ($1, $4, 'reviewer')`,
		tenantID, uAdmin, uPreparer, uReviewer); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uAdmin, Role: "authenticated", TenantID: tenantID})
	got, err := store.ListMemberships(c)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}

	want := map[string]string{uAdmin: "admin", uPreparer: "preparer", uReviewer: "reviewer"}
	if len(got) != len(want) {
		t.Fatalf("len(memberships) = %d, want %d: %+v", len(got), len(want), got)
	}
	seen := make(map[string]bool, len(want))
	for _, m := range got {
		wantRole, ok := want[m.UserID]
		if !ok {
			t.Errorf("unexpected user_id %q in result: %+v", m.UserID, m)
			continue
		}
		if m.Role != wantRole {
			t.Errorf("role for %s = %q, want %q (possible column/scan mix-up)", m.UserID, m.Role, wantRole)
		}
		seen[m.UserID] = true
	}
	for userID := range want {
		if !seen[userID] {
			t.Errorf("expected user_id %q missing from result: %+v", userID, got)
		}
	}
}

// TestStoreListMemberships_ReadsStatusAndIdentity (AC-2, RED until Membership
// and the ListMemberships SELECT widen): two rows seeded in one tenant --
// 'suspended' with a display_name and email on file, and a bare 'active' row
// with neither -- must round-trip through ListMemberships with the stored
// values (nulls preserved for the bare row) in created_at, user_id order.
// Membership doesn't (yet) expose Status/DisplayName/Email as Go fields, so
// this asserts on the JSON-marshaled wire shape rather than referencing
// fields that don't exist yet -- a compile error is not an acceptable RED.
func TestStoreListMemberships_ReadsStatusAndIdentity(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userSuspended, userBare := uuid.NewString(), uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test status-identity', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email, created_at)
		 VALUES ($1, $2, 'reviewer', 'suspended', 'Ada Okafor', 'ada@example.com', '2026-01-01T00:00:00Z')`,
		tenantID, userSuspended); err != nil {
		t.Fatalf("seed suspended membership: %v", err)
	}
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, created_at) VALUES ($1, $2, 'admin', '2026-01-02T00:00:00Z')`,
		tenantID, userBare); err != nil {
		t.Fatalf("seed bare membership: %v", err)
	}

	store := NewStore(app)
	// The request seam refuses a non-active caller before the store reads anything
	// (db.WithinRequestTenantTxOpts), so the caller is the active row and the
	// suspended row stays the subject under test.
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userBare, Role: "authenticated", TenantID: tenantID})
	got, err := store.ListMemberships(c)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(memberships) = %d, want 2: %+v", len(got), got)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal memberships: %v", err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatalf("decode marshaled memberships %s: %v", b, err)
	}

	var gotSuspended, gotBare string
	if err := json.Unmarshal(items[0]["user_id"], &gotSuspended); err != nil {
		t.Fatalf("memberships[0].user_id: %v", err)
	}
	if err := json.Unmarshal(items[1]["user_id"], &gotBare); err != nil {
		t.Fatalf("memberships[1].user_id: %v", err)
	}
	if gotSuspended != userSuspended || gotBare != userBare {
		t.Fatalf("order = [%s, %s], want [%s, %s] (created_at, user_id)", gotSuspended, gotBare, userSuspended, userBare)
	}

	if raw, ok := items[0]["status"]; !ok {
		t.Error(`memberships[0] missing key "status"`)
	} else if string(raw) != `"suspended"` {
		t.Errorf(`memberships[0]["status"] = %s, want "suspended"`, raw)
	}
	if raw, ok := items[0]["display_name"]; !ok {
		t.Error(`memberships[0] missing key "display_name"`)
	} else if string(raw) != `"Ada Okafor"` {
		t.Errorf(`memberships[0]["display_name"] = %s, want "Ada Okafor"`, raw)
	}
	if raw, ok := items[0]["email"]; !ok {
		t.Error(`memberships[0] missing key "email"`)
	} else if string(raw) != `"ada@example.com"` {
		t.Errorf(`memberships[0]["email"] = %s, want "ada@example.com"`, raw)
	}

	if raw, ok := items[1]["status"]; !ok {
		t.Error(`memberships[1] missing key "status"`)
	} else if string(raw) != `"active"` {
		t.Errorf(`memberships[1]["status"] = %s, want "active"`, raw)
	}
	if raw, ok := items[1]["display_name"]; !ok {
		t.Error(`memberships[1] missing key "display_name", want present with JSON null`)
	} else if string(raw) != "null" {
		t.Errorf(`memberships[1]["display_name"] = %s, want null`, raw)
	}
	if raw, ok := items[1]["email"]; !ok {
		t.Error(`memberships[1] missing key "email", want present with JSON null`)
	} else if string(raw) != "null" {
		t.Errorf(`memberships[1]["email"] = %s, want null`, raw)
	}
}

// TestMembershipsHandler_CrossTenantIdentityNotLeaked (AC-1/AC-2 adversarial,
// QA-added): through the real HTTP handler wired to a real Store.ListMemberships
// loader, tenant B's display_name/email must never reach tenant A's GET
// /v1/memberships response. memberships_rls_test.go already proves this at
// the raw-SQL/table level (TestRLS_MembershipsCrossTenantIdentityColumnsInvisible);
// this proves it holds through the widened wire contract this subtask added.
func TestMembershipsHandler_CrossTenantIdentityNotLeaked(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	userA, userB := uuid.NewString(), uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test leak A', 'firm'), ($2, 'tenancy qa-test leak B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, display_name, email) VALUES ($1, $2, 'admin', 'A Person', 'a@example.com')`,
		tenantA, userA); err != nil {
		t.Fatalf("seed tenant A membership: %v", err)
	}
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, display_name, email) VALUES ($1, $2, 'admin', 'B Secret', 'b-secret@example.com')`,
		tenantB, userB); err != nil {
		t.Fatalf("seed tenant B membership: %v", err)
	}

	store := NewStore(app)
	handler := MembershipsHandler(store.ListMemberships, nil)

	r := httptest.NewRequest("GET", "/v1/memberships", nil)
	id := auth.Identity{Subject: userA, Role: "authenticated", TenantID: tenantA}
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "B Secret") || strings.Contains(rec.Body.String(), "b-secret@example.com") {
		t.Fatalf("tenant B's identity data present in tenant A's response body: %s", rec.Body.String())
	}

	var body membershipsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body.Memberships, &items); err != nil {
		t.Fatalf("decode memberships %q: %v", body.Memberships, err)
	}
	if len(items) != 1 {
		t.Fatalf("len(memberships) = %d, want 1 (tenant A only): %s", len(items), body.Memberships)
	}
	var gotUserID string
	if err := json.Unmarshal(items[0]["user_id"], &gotUserID); err != nil {
		t.Fatalf("memberships[0].user_id: %v", err)
	}
	if gotUserID != userA {
		t.Fatalf("user_id = %q, want %q (tenant B's row must not appear)", gotUserID, userA)
	}
	var gotName string
	if err := json.Unmarshal(items[0]["display_name"], &gotName); err != nil {
		t.Fatalf("memberships[0].display_name: %v", err)
	}
	if gotName != "A Person" {
		t.Errorf("display_name = %q, want %q (tenant A's own value)", gotName, "A Person")
	}
}

// TestStoreListMemberships_EmptyStringDisplayNameNotNull (AC-1 adversarial,
// QA-added): display_name set to the empty string is a different state than
// NULL and must round-trip through the *string scan as "", not as a nil
// pointer -- and must marshal to JSON "" rather than null.
func TestStoreListMemberships_EmptyStringDisplayNameNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test empty-string identity', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, display_name) VALUES ($1, $2, 'admin', '')`,
		tenantID, userID); err != nil {
		t.Fatalf("seed membership with empty-string display_name: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})
	got, err := store.ListMemberships(c)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(memberships) = %d, want 1: %+v", len(got), got)
	}
	if got[0].DisplayName == nil {
		t.Fatal("DisplayName = nil, want a non-nil pointer to \"\" (empty string is not NULL)")
	}
	if *got[0].DisplayName != "" {
		t.Errorf("DisplayName = %q, want empty string", *got[0].DisplayName)
	}
	if got[0].Email != nil {
		t.Errorf("Email = %q, want nil (not named on insert -> NULL)", *got[0].Email)
	}

	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("decode marshaled membership %s: %v", b, err)
	}
	if string(wire["display_name"]) != `""` {
		t.Errorf(`wire display_name = %s, want "" (not null)`, wire["display_name"])
	}
	if string(wire["email"]) != "null" {
		t.Errorf("wire email = %s, want null", wire["email"])
	}
}

// TestStoreListMemberships_OrderTiebreakOnEqualCreatedAt (AC-2 adversarial,
// QA-added): two rows sharing the exact same created_at must break the tie by
// user_id ascending -- proves the ORDER BY's second clause specifically, not
// just that the query is stable across repeated calls (as
// TestStoreListMemberships_DeterministicOrder already does).
func TestStoreListMemberships_OrderTiebreakOnEqualCreatedAt(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	uHigh, uLow := uuid.NewString(), uuid.NewString()
	if uHigh < uLow {
		uHigh, uLow = uLow, uHigh
	}

	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'tenancy qa-test order tiebreak', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	// Insert the lexically-later user_id FIRST, same created_at for both -- if
	// the tiebreak were missing (or reversed), physical/insertion order could
	// leak through instead of the required user_id ascending order.
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, created_at) VALUES
		 ($1, $2, 'admin', '2026-03-01T00:00:00Z'), ($1, $3, 'preparer', '2026-03-01T00:00:00Z')`,
		tenantID, uHigh, uLow); err != nil {
		t.Fatalf("seed tied-created_at memberships: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uHigh, Role: "authenticated", TenantID: tenantID})
	got, err := store.ListMemberships(c)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(memberships) = %d, want 2: %+v", len(got), got)
	}
	if got[0].UserID != uLow || got[1].UserID != uHigh {
		t.Errorf("order = [%s, %s], want [%s, %s] (user_id ascending tiebreak on equal created_at)",
			got[0].UserID, got[1].UserID, uLow, uHigh)
	}
}

// --- PATCH /v1/memberships/{user_id} (SetMembershipStatus) -----------------

// seedTenant inserts one throwaway tenants row (kind 'firm') and registers a
// cleanup -- new here because none of the inline tenant INSERTs above are
// shared, and the PATCH suite below needs many.
func seedTenant(t *testing.T, super *pgxpool.Pool, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, name); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

// seedMembership inserts one memberships row with an explicit status -- the
// inline INSERTs above rely on the 'active' DEFAULT and can't seed
// suspended/invited rows, which the PATCH suite below needs.
func seedMembership(t *testing.T, super *pgxpool.Pool, tenantID, userID, role, status string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
		tenantID, userID, role, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// auditCount counts audit_log rows for tenantID+event -- mirrors
// internal/invoice/store_test.go's helper of the same name (a _test.go
// helper in another package, not importable).
func auditCount(t *testing.T, pool *pgxpool.Pool, tenantID, event string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE event = $1`, event).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	return n
}

// auditActor returns the actor of the most recent audit_log row for
// tenantID+event.
func auditActor(t *testing.T, pool *pgxpool.Pool, tenantID, event string) string {
	t.Helper()
	ctx := context.Background()
	var actor string
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT actor FROM audit_log WHERE event = $1 ORDER BY created_at DESC LIMIT 1`, event,
		).Scan(&actor)
	}); err != nil {
		t.Fatalf("read audit_log actor: %v", err)
	}
	return actor
}

// pgCode extracts the SQLSTATE from err, or "" if err does not wrap a
// *pgconn.PgError. Copied verbatim from internal/platform/db/tenants_kind_test.go.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TestMembership_SuspendRequiresAdmin (AC-2): a preparer caller cannot PATCH
// another member's status; the target is left unchanged.
func TestMembership_SuspendRequiresAdmin(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-ADMIN-ONLY tenant")
	callerID := uuid.NewString()
	seedMembership(t, super, tenantID, callerID, "preparer", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "reviewer", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: callerID, Role: "authenticated", TenantID: tenantID})
	if _, err := store.SetMembershipStatus(c, targetID, "suspended"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("SetMembershipStatus (preparer caller) err = %v, want ErrNotPermitted", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM memberships WHERE user_id = $1`, targetID).Scan(&status); err != nil {
		t.Fatalf("read back target status: %v", err)
	}
	if status != "active" {
		t.Errorf("target status after a refused PATCH = %q, want unchanged %q", status, "active")
	}
}

// TestMembership_PermissionCheckedBeforeRowRead (AC-2): a non-admin caller
// targeting a user_id with no membership row at all still gets
// ErrNotPermitted, never ErrMembershipNotFound -- no 403-vs-404 existence
// probe.
func TestMembership_PermissionCheckedBeforeRowRead(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-GUARD-ORDER tenant")
	callerID := uuid.NewString()
	seedMembership(t, super, tenantID, callerID, "preparer", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: callerID, Role: "authenticated", TenantID: tenantID})
	if _, err := store.SetMembershipStatus(c, uuid.NewString(), "suspended"); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("SetMembershipStatus (non-admin, unknown target) err = %v, want ErrNotPermitted (never ErrMembershipNotFound)", err)
	}
}

// TestMembership_SuspendAndReactivate (AC-3): an admin can suspend an active
// target, then reactivate it.
func TestMembership_SuspendAndReactivate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-SUSPEND-REACTIVATE tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "reviewer", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})

	got, err := store.SetMembershipStatus(c, targetID, "suspended")
	if err != nil {
		t.Fatalf("SetMembershipStatus (suspend): %v", err)
	}
	if got.Status != "suspended" {
		t.Errorf("status after suspend = %q, want %q", got.Status, "suspended")
	}

	got, err = store.SetMembershipStatus(c, targetID, "active")
	if err != nil {
		t.Fatalf("SetMembershipStatus (reactivate): %v", err)
	}
	if got.Status != "active" {
		t.Errorf("status after reactivate = %q, want %q", got.Status, "active")
	}
}

// TestMembership_InvalidStatusRejected (AC-3): every malformed or
// out-of-vocabulary request body maps to 400, and the setter must never run.
// Driven through the real HTTP boundary since the malformed-JSON case has no
// store-level equivalent (there is no JSON body at that layer).
func TestMembership_InvalidStatusRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"invited status", `{"status":"invited"}`},
		{"empty status", `{"status":""}`},
		{"empty object", `{}`},
		{"malformed json", `{"status":`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
			set := func(context.Context, string, string) (Membership, error) {
				t.Fatal("setter must not run for a rejected request body")
				return Membership{}, nil
			}
			r := httptest.NewRequest("PATCH", "/v1/memberships/"+uuid.NewString(), strings.NewReader(c.body))
			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			rec := httptest.NewRecorder()
			SetMembershipStatusHandler(set, nil).ServeHTTP(rec, r)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestMembership_BodyOverCapRejected (AC-14): a request body over the cap
// maps to 400, not 413 -- the invoice handlers.go shape, not the
// validation/importer "house pattern". The plan's cap is 4 KiB; a legitimate
// body is ~30 bytes.
func TestMembership_BodyOverCapRejected(t *testing.T) {
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	set := func(context.Context, string, string) (Membership, error) {
		t.Fatal("setter must not run for an over-cap body")
		return Membership{}, nil
	}
	oversized := `{"status":"suspended","padding":"` + strings.Repeat("x", 8*1024) + `"}`
	r := httptest.NewRequest("PATCH", "/v1/memberships/"+uuid.NewString(), strings.NewReader(oversized))
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	SetMembershipStatusHandler(set, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (not 413): %s", rec.Code, rec.Body.String())
	}
}

// TestMembership_InvitedTargetNotTransitionable (AC-3): a target still
// `invited` cannot be transitioned; the row is unchanged.
func TestMembership_InvitedTargetNotTransitionable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-INVITED tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "preparer", "invited")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
	if _, err := store.SetMembershipStatus(c, targetID, "active"); !errors.Is(err, ErrInvitedNotTransitionable) {
		t.Fatalf("SetMembershipStatus (invited target) err = %v, want ErrInvitedNotTransitionable", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM memberships WHERE user_id = $1`, targetID).Scan(&status); err != nil {
		t.Fatalf("read back target status: %v", err)
	}
	if status != "invited" {
		t.Errorf("target status after a refused PATCH = %q, want unchanged %q", status, "invited")
	}
}

// TestMembership_SameStatusIsNoOpWithoutAudit (AC-3): PATCHing a target to
// its current status is a 200 no-op -- no audit row.
func TestMembership_SameStatusIsNoOpWithoutAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-NOOP tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "reviewer", "active")

	const event = "membership.reactivated"
	before := auditCount(t, app, tenantID, event)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
	got, err := store.SetMembershipStatus(c, targetID, "active")
	if err != nil {
		t.Fatalf("SetMembershipStatus (already active): %v", err)
	}
	if got.Status != "active" {
		t.Errorf("status = %q, want unchanged %q", got.Status, "active")
	}
	if after := auditCount(t, app, tenantID, event); after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (a no-op must not audit)", event, after, before)
	}
}

// TestMembership_UnknownTargetNotFound (AC-3): a well-formed but unseeded
// user_id maps to ErrMembershipNotFound.
func TestMembership_UnknownTargetNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-UNKNOWN-TARGET tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
	if _, err := store.SetMembershipStatus(c, uuid.NewString(), "suspended"); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("SetMembershipStatus (unknown target) err = %v, want ErrMembershipNotFound", err)
	}
}

// TestMembership_CrossTenantTargetNotFound (AC-3, plan §F): an admin of
// tenant A targeting tenant B's user_id gets ErrMembershipNotFound (RLS makes
// the row invisible, not merely denied); B's row is untouched.
// TestRLS_MembershipsCrossTenantStatusUpdateInvisible (subtask 01) already
// proves the raw-SQL RLS half -- this is the new endpoint-level half.
func TestMembership_CrossTenantTargetNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "T-CROSS-TENANT tenant A")
	tenantB := seedTenant(t, super, "T-CROSS-TENANT tenant B")
	adminA := uuid.NewString()
	seedMembership(t, super, tenantA, adminA, "admin", "active")
	targetB := uuid.NewString()
	seedMembership(t, super, tenantB, targetB, "reviewer", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminA, Role: "authenticated", TenantID: tenantA})
	if _, err := store.SetMembershipStatus(c, targetB, "suspended"); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("SetMembershipStatus (tenant A admin on tenant B's user_id) err = %v, want ErrMembershipNotFound", err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM memberships WHERE user_id = $1`, targetB).Scan(&status); err != nil {
		t.Fatalf("read back tenant B's target status: %v", err)
	}
	if status != "active" {
		t.Errorf("tenant B's target status after a refused cross-tenant PATCH = %q, want unchanged %q", status, "active")
	}
}

// TestMembership_SuspendWritesAudit (AC-4): suspend writes
// "membership.suspended" with the caller as actor; reactivate writes
// "membership.reactivated".
func TestMembership_SuspendWritesAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-AUDIT tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "reviewer", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})

	const suspendEvent = "membership.suspended"
	beforeSuspend := auditCount(t, app, tenantID, suspendEvent)
	if _, err := store.SetMembershipStatus(c, targetID, "suspended"); err != nil {
		t.Fatalf("SetMembershipStatus (suspend): %v", err)
	}
	if after := auditCount(t, app, tenantID, suspendEvent); after != beforeSuspend+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (+1)", suspendEvent, after, beforeSuspend+1)
	}
	if actor := auditActor(t, app, tenantID, suspendEvent); actor != adminID {
		t.Errorf("audit actor = %q, want caller subject %q", actor, adminID)
	}

	const reactivateEvent = "membership.reactivated"
	beforeReactivate := auditCount(t, app, tenantID, reactivateEvent)
	if _, err := store.SetMembershipStatus(c, targetID, "active"); err != nil {
		t.Fatalf("SetMembershipStatus (reactivate): %v", err)
	}
	if after := auditCount(t, app, tenantID, reactivateEvent); after != beforeReactivate+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (+1)", reactivateEvent, after, beforeReactivate+1)
	}
	if actor := auditActor(t, app, tenantID, reactivateEvent); actor != adminID {
		t.Errorf("audit actor = %q, want caller subject %q", actor, adminID)
	}
}

// TestMembership_AuditFailureRollsBackStatus (AC-4): mirrors
// internal/invoice's TestResolveOutside_AuditFailureRollsBackColumnWrite.
// SetMembershipStatus's own caller-role read binds the identity Subject
// against memberships.user_id (uuid NOT NULL), so a malformed 256-char
// Subject 22P02s there before ever reaching the audit write -- so this
// reconstructs the store method's write-tx body directly on the app pool: a
// real target row, a bad actor bound only to audit.Record, proving the
// UPDATE rolls back with a failing audit write. This exercises audit_log's
// own CHECK constraint and transactional atomicity rather than any
// not-yet-written application code, so it passes today; it continues to hold
// once SetMembershipStatus is wired to this identical shape.
func TestMembership_AuditFailureRollsBackStatus(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-AUDIT-TX tenant")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "reviewer", "active")

	badActor := strings.Repeat("a", 256)
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		var current string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM memberships WHERE user_id = $1 FOR UPDATE`, targetID,
		).Scan(&current); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE memberships SET status = 'suspended' WHERE user_id = $1`, targetID,
		); err != nil {
			return err
		}
		return audit.Record(ctx, tx, badActor, "membership.suspended", map[string]any{
			"user_id": targetID, "from": current, "to": "suspended",
		})
	})
	if err == nil {
		t.Fatal("write-tx with a 256-char audit actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM memberships WHERE user_id = $1`, targetID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if status != "active" {
		t.Errorf("status after a rolled-back write = %q, want unchanged %q", status, "active")
	}
}

// TestMembership_LastActiveAdminRefused (AC-5): both shapes of the
// last-active-admin rule -- a sole admin suspending themselves, and the
// surviving admin of a tenant already narrowed to one active admin.
func TestMembership_LastActiveAdminRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	t.Run("sole admin", func(t *testing.T) {
		tenantID := seedTenant(t, super, "T-LAST-ADMIN tenant sole")
		adminID := uuid.NewString()
		seedMembership(t, super, tenantID, adminID, "admin", "active")

		c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
		if _, err := store.SetMembershipStatus(c, adminID, "suspended"); !errors.Is(err, ErrLastActiveAdmin) {
			t.Fatalf("SetMembershipStatus (sole active admin, self-suspend) err = %v, want ErrLastActiveAdmin", err)
		}
		var status string
		if err := super.QueryRow(ctx, `SELECT status FROM memberships WHERE user_id = $1`, adminID).Scan(&status); err != nil {
			t.Fatalf("read back status: %v", err)
		}
		if status != "active" {
			t.Errorf("status after a refused self-suspend = %q, want unchanged %q", status, "active")
		}
	})

	t.Run("last other admin already suspended", func(t *testing.T) {
		tenantID := seedTenant(t, super, "T-LAST-ADMIN tenant narrowed")
		survivor := uuid.NewString()
		seedMembership(t, super, tenantID, survivor, "admin", "active")
		other := uuid.NewString()
		seedMembership(t, super, tenantID, other, "admin", "suspended")

		c := auth.WithIdentity(ctx, auth.Identity{Subject: survivor, Role: "authenticated", TenantID: tenantID})
		if _, err := store.SetMembershipStatus(c, survivor, "suspended"); !errors.Is(err, ErrLastActiveAdmin) {
			t.Fatalf("SetMembershipStatus (last active admin, other already suspended) err = %v, want ErrLastActiveAdmin", err)
		}
	})
}

// TestMembership_AdminSuspendableWhileAnotherActiveAdminExists (AC-5): with
// two active admins, suspending one succeeds.
func TestMembership_AdminSuspendableWhileAnotherActiveAdminExists(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-TWO-ADMINS tenant")
	callerAdmin, targetAdmin := uuid.NewString(), uuid.NewString()
	seedMembership(t, super, tenantID, callerAdmin, "admin", "active")
	seedMembership(t, super, tenantID, targetAdmin, "admin", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: callerAdmin, Role: "authenticated", TenantID: tenantID})
	got, err := store.SetMembershipStatus(c, targetAdmin, "suspended")
	if err != nil {
		t.Fatalf("SetMembershipStatus (two active admins, suspend one): %v", err)
	}
	if got.Status != "suspended" {
		t.Errorf("status = %q, want %q", got.Status, "suspended")
	}
}

// TestMembership_SuspendedAdminCannotAdminister (AC-10, [suspension-is-not-self-undoable]):
// a suspended admin is refused whether targeting themselves (the self-undo
// case this rule exists for) or another member. Without the caller-role
// read's own AND status='active' predicate, a suspended admin could PATCH
// themselves back to active.
func TestMembership_SuspendedAdminCannotAdminister(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-SUSPENDED-ADMIN tenant")
	suspendedAdmin := uuid.NewString()
	seedMembership(t, super, tenantID, suspendedAdmin, "admin", "suspended")
	otherAdmin := uuid.NewString()
	seedMembership(t, super, tenantID, otherAdmin, "admin", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: suspendedAdmin, Role: "authenticated", TenantID: tenantID})

	// The request seam refuses a non-active caller before the store reads anything
	// (db.WithinRequestTenantTxOpts), so the refusal is now the status sentinel.
	t.Run("self reactivate", func(t *testing.T) {
		if _, err := store.SetMembershipStatus(c, suspendedAdmin, "active"); !errors.Is(err, db.ErrNotActiveMember) {
			t.Fatalf("SetMembershipStatus (suspended admin reactivates self) err = %v, want db.ErrNotActiveMember -- suspension must not be self-undoable", err)
		}
	})
	t.Run("suspend another", func(t *testing.T) {
		if _, err := store.SetMembershipStatus(c, otherAdmin, "suspended"); !errors.Is(err, db.ErrNotActiveMember) {
			t.Fatalf("SetMembershipStatus (suspended admin targets another) err = %v, want db.ErrNotActiveMember", err)
		}
	})
}

// TestMembership_ConcurrentLastTwoAdmins (AC-11, [last-admin-guard-is-race-safe]):
// two active admins concurrently suspend each other. The assertion is the
// invariant, read back from the database: exactly one suspension COMMITS and an
// active admin survives. Without FOR UPDATE over the admin set both transactions
// see two active admins, both commit, and the tenant is stranded at zero.
//
// The refusal is ErrLastActiveAdmin when the transactions overlap and
// ErrNotPermitted when they serialize (the loser's caller-role read already sees
// itself suspended -- AC-10). Both are legal; pinning the exact pairing instead
// failed ~6.7% of runs under contention while the invariant held every time.
// Repeated because one round only exercises the unsafe path when they overlap.
func TestMembership_ConcurrentLastTwoAdmins(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	const rounds = 8
	for round := range rounds {
		tenantID := seedTenant(t, super, "T-CONCURRENT-ADMINS tenant")
		adminX, adminY := uuid.NewString(), uuid.NewString()
		seedMembership(t, super, tenantID, adminX, "admin", "active")
		seedMembership(t, super, tenantID, adminY, "admin", "active")

		cX := auth.WithIdentity(ctx, auth.Identity{Subject: adminX, Role: "authenticated", TenantID: tenantID})
		cY := auth.WithIdentity(ctx, auth.Identity{Subject: adminY, Role: "authenticated", TenantID: tenantID})

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, errs[0] = store.SetMembershipStatus(cX, adminY, "suspended") }()
		go func() { defer wg.Done(); <-start; _, errs[1] = store.SetMembershipStatus(cY, adminX, "suspended") }()
		close(start)
		wg.Wait()

		oks := 0
		for i, err := range errs {
			switch {
			case err == nil:
				oks++
			case errors.Is(err, ErrLastActiveAdmin), errors.Is(err, ErrNotPermitted):
			default:
				t.Fatalf("round %d: errs[%d] = %v, want nil, ErrLastActiveAdmin or ErrNotPermitted (a deadlock or serialization failure is a defect)", round, i, err)
			}
		}
		if oks != 1 {
			t.Fatalf("round %d: results = %v, want exactly one success", round, errs)
		}

		var suspended, active int
		if err := super.QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE status = 'suspended'), count(*) FILTER (WHERE status = 'active')
			   FROM memberships WHERE tenant_id = $1 AND role = 'admin'`, tenantID,
		).Scan(&suspended, &active); err != nil {
			t.Fatalf("round %d: count admins: %v", round, err)
		}
		if suspended != 1 || active != 1 {
			t.Fatalf("round %d: admins after the race = %d suspended / %d active, want exactly 1 / 1 -- errs = %v", round, suspended, active, errs)
		}
	}
}

// --- Stage-4 adversarial coverage -----------------------------------------

// patchViaMux drives one PATCH through a real http.ServeMux registered with the
// same pattern cmd/tenancy uses, so {user_id} is populated the way production
// populates it -- the handler-only tests above leave PathValue empty.
func patchViaMux(t *testing.T, set MembershipStatusSetter, id auth.Identity, pathID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v1/memberships/{user_id}", SetMembershipStatusHandler(set, nil))
	r := httptest.NewRequest("PATCH", "/v1/memberships/"+pathID, strings.NewReader(body))
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// mustStatus reads one membership's status as the superuser.
func mustStatus(t *testing.T, super *pgxpool.Pool, userID string) string {
	t.Helper()
	var status string
	if err := super.QueryRow(context.Background(),
		`SELECT status FROM memberships WHERE user_id = $1`, userID).Scan(&status); err != nil {
		t.Fatalf("read back status of %s: %v", userID, err)
	}
	return status
}

// auditPayload returns the payload of the newest audit_log row for
// tenantID+event whose payload names userID, RLS-scoped like auditCount.
func auditPayload(t *testing.T, pool *pgxpool.Pool, tenantID, event, userID string) map[string]any {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	if err := db.WithinTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT payload FROM audit_log WHERE event = $1 AND payload->>'user_id' = $2 ORDER BY created_at DESC LIMIT 1`,
			event, userID).Scan(&raw)
	}); err != nil {
		t.Fatalf("read audit_log payload (%s / %s): %v", event, userID, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode audit payload %q: %v", raw, err)
	}
	return got
}

// quoteLiteral renders a value as a SQL string literal. Only ever fed
// uuid.NewString() output here.
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// TestMembership_NonAdminRefusalIdenticalForEveryTargetShape (AC-2): a
// non-admin must not be able to probe for a member's existence. Every
// well-formed target -- a real member of the caller's own tenant, another
// tenant's member, and an unseeded uuid -- must produce a byte-identical 403.
// Driven through the real mux and the real store so the ordering under test is
// the shipped one, not a stub's.
func TestMembership_NonAdminRefusalIdenticalForEveryTargetShape(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "T-NO-ORACLE tenant A")
	tenantB := seedTenant(t, super, "T-NO-ORACLE tenant B")
	callerID := uuid.NewString()
	seedMembership(t, super, tenantA, callerID, "preparer", "active")
	sameTenant := uuid.NewString()
	seedMembership(t, super, tenantA, sameTenant, "reviewer", "active")
	crossTenant := uuid.NewString()
	seedMembership(t, super, tenantB, crossTenant, "reviewer", "active")

	store := NewStore(app)
	id := auth.Identity{Subject: callerID, Role: "authenticated", TenantID: tenantA}

	shapes := []struct{ name, pathID string }{
		{"existing same-tenant member", sameTenant},
		{"cross-tenant member", crossTenant},
		{"unknown uuid", uuid.NewString()},
	}
	var wantCode int
	var wantBody string
	for i, s := range shapes {
		rec := patchViaMux(t, store.SetMembershipStatus, id, s.pathID, `{"status":"suspended"}`)
		if i == 0 {
			wantCode, wantBody = rec.Code, rec.Body.String()
			if wantCode != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want 403: %s", s.name, wantCode, wantBody)
			}
		}
		if rec.Code != wantCode || rec.Body.String() != wantBody {
			t.Errorf("%s: (%d, %q), want byte-identical to %q's (%d, %q)",
				s.name, rec.Code, rec.Body.String(), shapes[0].name, wantCode, wantBody)
		}
	}

	if got := mustStatus(t, super, sameTenant); got != "active" {
		t.Errorf("same-tenant target status = %q, want unchanged %q", got, "active")
	}
	if got := mustStatus(t, super, crossTenant); got != "active" {
		t.Errorf("cross-tenant target status = %q, want unchanged %q", got, "active")
	}

	// A malformed path id is 404, not 403: the handler cannot call the store
	// without a uuid. It is not an existence oracle -- the caller already knows
	// the string is not a uuid, and it distinguishes no real member from any
	// other. Pinned so changing it stays a decision rather than a drift.
	rec := patchViaMux(t, store.SetMembershipStatus, id, "not-a-uuid", `{"status":"suspended"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("malformed path id: status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestMembership_AuditFailureRollsBackRealSetMembershipStatus (AC-4): a failing
// audit rolls the UPDATE back in the SHIPPED method.
// TestMembership_AuditFailureRollsBackStatus proves it against a
// hand-reconstructed tx, which cannot observe whether the real closure writes
// its audit row inside its own transaction. The failure is forced by a trigger
// keyed to this one target's payload, so no concurrent test can see it.
func TestMembership_AuditFailureRollsBackRealSetMembershipStatus(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-AUDIT-REAL tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "reviewer", "active")

	name := "qa_audit_fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := super.Exec(ctx, fmt.Sprintf(
		`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN RAISE EXCEPTION 'forced audit failure' USING ERRCODE = 'check_violation'; END; $$`, name)); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s() CASCADE`, name))
	})
	if _, err := super.Exec(ctx, fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE INSERT ON audit_log FOR EACH ROW
		 WHEN (NEW.payload->>'user_id' = %s) EXECUTE FUNCTION %s()`,
		name, quoteLiteral(targetID), name)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
	_, err := store.SetMembershipStatus(c, targetID, "suspended")
	if err == nil {
		t.Fatal("SetMembershipStatus succeeded with a failing audit write, want the whole transaction to abort")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("pgCode = %q, want 23514 from the forced audit failure: %v", code, err)
	}
	if got := mustStatus(t, super, targetID); got != "active" {
		t.Errorf("status after a failed audit = %q, want the UPDATE rolled back to %q", got, "active")
	}
}

// TestMembership_AuditPayloadRecordsTransition (AC-4): the audit row carries the
// target and both ends of the transition, not just the event name -- a trail
// that records only "someone was suspended" answers no question.
func TestMembership_AuditPayloadRecordsTransition(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-AUDIT-PAYLOAD tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "reviewer", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})

	if _, err := store.SetMembershipStatus(c, targetID, "suspended"); err != nil {
		t.Fatalf("SetMembershipStatus (suspend): %v", err)
	}
	got := auditPayload(t, app, tenantID, "membership.suspended", targetID)
	for k, want := range map[string]any{"user_id": targetID, "from": "active", "to": "suspended"} {
		if got[k] != want {
			t.Errorf("suspend payload[%q] = %v, want %v (full payload %v)", k, got[k], want, got)
		}
	}

	if _, err := store.SetMembershipStatus(c, targetID, "active"); err != nil {
		t.Fatalf("SetMembershipStatus (reactivate): %v", err)
	}
	got = auditPayload(t, app, tenantID, "membership.reactivated", targetID)
	for k, want := range map[string]any{"user_id": targetID, "from": "suspended", "to": "active"} {
		if got[k] != want {
			t.Errorf("reactivate payload[%q] = %v, want %v (full payload %v)", k, got[k], want, got)
		}
	}
}

// TestMembership_CrossTenantPatchLeavesNoAuditTrace: a refused cross-tenant
// PATCH must leave nothing behind in the TARGET tenant's log either -- a
// refusal that still wrote an audit row would leak the attempt, and the
// attacker's subject, into a tenant that never consented to see it.
func TestMembership_CrossTenantPatchLeavesNoAuditTrace(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "T-CROSS-AUDIT tenant A")
	tenantB := seedTenant(t, super, "T-CROSS-AUDIT tenant B")
	adminA := uuid.NewString()
	seedMembership(t, super, tenantA, adminA, "admin", "active")
	targetB := uuid.NewString()
	seedMembership(t, super, tenantB, targetB, "reviewer", "active")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminA, Role: "authenticated", TenantID: tenantA})
	if _, err := store.SetMembershipStatus(c, targetB, "suspended"); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("cross-tenant PATCH err = %v, want ErrMembershipNotFound", err)
	}

	for _, tenantID := range []string{tenantA, tenantB} {
		var n int
		if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT count(*) FROM audit_log WHERE payload->>'user_id' = $1`, targetB).Scan(&n)
		}); err != nil {
			t.Fatalf("count audit rows in %s: %v", tenantID, err)
		}
		if n != 0 {
			t.Errorf("audit rows naming the cross-tenant target in tenant %s = %d, want 0", tenantID, n)
		}
	}
}

// TestMembership_CallerGateRefusesEveryNonActiveAdmin (AC-2, AC-10): the caller
// gate is "an ACTIVE ADMIN", so every other caller shape is refused
// identically. A suspended reviewer and an admin who has only been invited are
// the shapes no pre-existing test covered.
func TestMembership_CallerGateRefusesEveryNonActiveAdmin(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	// The request seam refuses a non-active caller before the store reads anything
	// (db.WithinRequestTenantTxOpts); an ACTIVE non-admin is still a role refusal,
	// and keeping the two sentinels apart is what this table exists to catch.
	cases := []struct {
		name, role, status string
		want               error
	}{
		{"active reviewer", "reviewer", "active", ErrNotPermitted},
		{"suspended reviewer", "reviewer", "suspended", db.ErrNotActiveMember},
		{"suspended preparer", "preparer", "suspended", db.ErrNotActiveMember},
		{"suspended admin", "admin", "suspended", db.ErrNotActiveMember},
		{"invited admin", "admin", "invited", db.ErrNotActiveMember},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := seedTenant(t, super, "T-CALLER-GATE tenant")
			callerID := uuid.NewString()
			seedMembership(t, super, tenantID, callerID, tc.role, tc.status)
			// Two other active admins, so a wrongly-passed gate would really
			// suspend the target rather than trip the last-admin guard.
			targetID := uuid.NewString()
			seedMembership(t, super, tenantID, targetID, "admin", "active")
			seedMembership(t, super, tenantID, uuid.NewString(), "admin", "active")

			c := auth.WithIdentity(ctx, auth.Identity{Subject: callerID, Role: "authenticated", TenantID: tenantID})
			if _, err := store.SetMembershipStatus(c, targetID, "suspended"); !errors.Is(err, tc.want) {
				t.Fatalf("SetMembershipStatus (%s caller) err = %v, want %v", tc.name, err, tc.want)
			}
			if got := mustStatus(t, super, targetID); got != "active" {
				t.Errorf("target status after a refused PATCH = %q, want unchanged %q", got, "active")
			}
		})
	}
}

// TestMembership_ZeroActiveAdminsIsUnrecoverable records the escape hatch that
// does not exist: with no active admin the tenant cannot be repaired through
// this endpoint by anyone, and recovery is a superuser write. The endpoint can
// never CREATE that state (the last-active-admin guard), so it is reachable only
// via a seed, a migration, or a future role-change path.
func TestMembership_ZeroActiveAdminsIsUnrecoverable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "T-LOCKOUT tenant")
	suspendedAdmin := uuid.NewString()
	seedMembership(t, super, tenantID, suspendedAdmin, "admin", "suspended")
	reviewer := uuid.NewString()
	seedMembership(t, super, tenantID, reviewer, "reviewer", "active")

	// The request seam refuses a non-active caller before the store reads anything
	// (db.WithinRequestTenantTxOpts); the active reviewer is still a role refusal.
	for name, tc := range map[string]struct {
		callerID string
		want     error
	}{
		"the suspended admin themselves": {suspendedAdmin, db.ErrNotActiveMember},
		"an active reviewer":             {reviewer, ErrNotPermitted},
	} {
		c := auth.WithIdentity(ctx, auth.Identity{Subject: tc.callerID, Role: "authenticated", TenantID: tenantID})
		if _, err := store.SetMembershipStatus(c, suspendedAdmin, "active"); !errors.Is(err, tc.want) {
			t.Errorf("reactivate by %s err = %v, want %v", name, err, tc.want)
		}
	}
	if got := mustStatus(t, super, suspendedAdmin); got != "suspended" {
		t.Errorf("admin status = %q, want still %q (nobody in the tenant can reactivate)", got, "suspended")
	}
}

// TestMembership_MixedCaseTargetIDMatches: a non-canonical (uppercase) uuid must
// resolve to the same row. is_target is computed in SQL rather than by a Go
// string compare because pgx returns the canonical lowercase text, which an
// uppercase argument would never equal -- the failure mode is a spurious 404 on
// a target that plainly exists.
func TestMembership_MixedCaseTargetIDMatches(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-MIXED-CASE tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "reviewer", "active")

	store := NewStore(app)
	id := auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID}

	// Store level: the uppercase id reaches the SQL unchanged.
	got, err := store.SetMembershipStatus(auth.WithIdentity(ctx, id), strings.ToUpper(targetID), "suspended")
	if err != nil {
		t.Fatalf("SetMembershipStatus with an uppercase uuid: %v", err)
	}
	if got.Status != "suspended" {
		t.Errorf("status = %q, want %q", got.Status, "suspended")
	}
	if got.UserID != targetID {
		t.Errorf("returned user_id = %q, want the canonical %q", got.UserID, targetID)
	}
	if s := mustStatus(t, super, targetID); s != "suspended" {
		t.Errorf("row status = %q, want %q", s, "suspended")
	}

	// HTTP level: the handler normalises before the store ever sees it.
	rec := patchViaMux(t, store.SetMembershipStatus, id, strings.ToUpper(targetID), `{"status":"active"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH with an uppercase path id: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if s := mustStatus(t, super, targetID); s != "active" {
		t.Errorf("row status after the uppercase PATCH = %q, want %q", s, "active")
	}
}

// TestMembership_PathIDAndBodyOrdering pins the shipped ordering: the body is
// validated before the path id. Both orderings agree on every single-fault
// request; only a request bad in BOTH places tells them apart, and no
// acceptance criterion constrains that case. The setter must never run.
func TestMembership_PathIDAndBodyOrdering(t *testing.T) {
	cases := []struct {
		name, pathID, body string
		want               int
	}{
		{"good id, bad body", uuid.NewString(), `{"status":"nope"}`, http.StatusBadRequest},
		{"bad id, good body", "not-a-uuid", `{"status":"suspended"}`, http.StatusNotFound},
		{"bad id, bad body", "not-a-uuid", `{"status":"nope"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := func(context.Context, string, string) (Membership, error) {
				t.Fatal("setter must not run for a request rejected at the handler")
				return Membership{}, nil
			}
			id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
			if rec := patchViaMux(t, set, id, tc.pathID, tc.body); rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestMembership_StatusForErrTable (AC-3, AC-6, AC-13): each store sentinel maps
// to its specified status and message on the wire, wrapped or bare, and no
// message leaks the "tenancy: " sentinel prefix the SPA would render.
func TestMembership_StatusForErrTable(t *testing.T) {
	cases := []struct {
		err     error
		want    int
		wantMsg string
	}{
		{db.ErrNoTenant, http.StatusUnauthorized, "unauthorized"},
		{ErrInvalidStatus, http.StatusBadRequest, `status must be "active" or "suspended"`},
		{ErrNotPermitted, http.StatusForbidden, "only an admin can change a member's status"},
		{ErrMembershipNotFound, http.StatusNotFound, "membership not found"},
		{ErrInvitedNotTransitionable, http.StatusConflict, "an invited member has no sign-in to suspend or reactivate"},
		{ErrLastActiveAdmin, http.StatusConflict, "this is the tenant's last active admin — make another member an active admin first"},
		{errors.New("boom"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			err, name := tc.err, tc.err.Error()
			if wrapped {
				err, name = fmt.Errorf("store: %w", tc.err), name+" (wrapped)"
			}
			t.Run(name, func(t *testing.T) {
				set := func(context.Context, string, string) (Membership, error) { return Membership{}, err }
				id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
				rec := patchViaMux(t, set, id, uuid.NewString(), `{"status":"suspended"}`)
				if rec.Code != tc.want {
					t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
				}
				var body struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode body %q: %v", rec.Body.String(), err)
				}
				if body.Error != tc.wantMsg {
					t.Errorf("error = %q, want %q", body.Error, tc.wantMsg)
				}
				if strings.Contains(body.Error, "tenancy: ") {
					t.Errorf("error %q leaks the sentinel prefix to the SPA", body.Error)
				}
			})
		}
	}
}

// TestMembership_OKBodyIsFiveKeyShape (AC-1): a 200 returns the updated
// membership in the same five-key shape as a GET /v1/memberships element, with
// a null email present rather than omitted.
func TestMembership_OKBodyIsFiveKeyShape(t *testing.T) {
	displayName := "Ada"
	set := func(_ context.Context, userID, status string) (Membership, error) {
		return Membership{UserID: userID, Role: "reviewer", Status: status, DisplayName: &displayName}, nil
	}
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	targetID := uuid.NewString()
	rec := patchViaMux(t, set, id, targetID, `{"status":"suspended"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	gotKeys := make([]string, 0, len(raw))
	for k := range raw {
		gotKeys = append(gotKeys, k)
	}
	slices.Sort(gotKeys)
	wantKeys := []string{"display_name", "email", "role", "status", "user_id"}
	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("keys = %v, want exactly %v", gotKeys, wantKeys)
	}
	if string(raw["email"]) != "null" {
		t.Errorf("email = %s, want null (present, never omitted)", raw["email"])
	}
	if string(raw["user_id"]) != `"`+targetID+`"` || string(raw["status"]) != `"suspended"` {
		t.Errorf("body = %s, want the updated row for %s", rec.Body.String(), targetID)
	}
}

// TestMembership_NoIdentity401: the PATCH endpoint answers nothing without a
// verified caller, and the setter never runs -- the same fail-closed shape
// MeHandler and MembershipsHandler already carry.
func TestMembership_NoIdentity401(t *testing.T) {
	set := func(context.Context, string, string) (Membership, error) {
		t.Fatal("setter must not run without an identity")
		return Membership{}, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v1/memberships/{user_id}", SetMembershipStatusHandler(set, nil))
	r := httptest.NewRequest("PATCH", "/v1/memberships/"+uuid.NewString(), strings.NewReader(`{"status":"suspended"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// TestMembership_NoOpOnAlreadySuspendedAdminTarget (AC-12): the no-op guard runs
// before the last-active-admin guard, so re-suspending an already-suspended
// admin returns 200 without an UPDATE or an audit row.
func TestMembership_NoOpOnAlreadySuspendedAdminTarget(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "T-NOOP-ADMIN tenant")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	targetID := uuid.NewString()
	seedMembership(t, super, tenantID, targetID, "admin", "suspended")

	const event = "membership.suspended"
	before := auditCount(t, app, tenantID, event)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantID})
	got, err := store.SetMembershipStatus(c, targetID, "suspended")
	if err != nil {
		t.Fatalf("re-suspending an already-suspended admin: %v", err)
	}
	if got.Status != "suspended" || got.Role != "admin" {
		t.Errorf("no-op returned %+v, want the unchanged admin/suspended row", got)
	}
	if after := auditCount(t, app, tenantID, event); after != before {
		t.Errorf("audit rows for %s = %d, want unchanged %d", event, after, before)
	}
}
