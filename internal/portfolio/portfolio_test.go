package portfolio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- handler tests (httptest + stubbed store closures, no DB) ------------------------

// createRequest mirrors the POST /v1/entities wire body (snake_case JSON
// tags, per the story's Request/response DTO convention).
type createRequest struct {
	Name         string  `json:"name"`
	TIN          string  `json:"tin"`
	Registration *string `json:"registration,omitempty"`
	Sector       *string `json:"sector,omitempty"`
	Address      *string `json:"address,omitempty"`
}

// entityBody mirrors the Entity JSON so handler tests can assert the
// contract, plus an Error field for the {"error":msg} envelope.
type entityBody struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	TIN          *string   `json:"tin"`
	Registration *string   `json:"registration"`
	Sector       *string   `json:"sector"`
	Address      *string   `json:"address"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	Error        string    `json:"error"`
}

func doCreate(t *testing.T, create func(ctx context.Context, in CreateInput) (Entity, error), id *auth.Identity, body createRequest) (*httptest.ResponseRecorder, entityBody) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	r := httptest.NewRequest("POST", "/v1/entities", bytes.NewReader(b))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CreateHandler(create, nil).ServeHTTP(rec, r)
	var resp entityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

func doGet(t *testing.T, get func(ctx context.Context, id string) (Entity, error), id *auth.Identity, entityID string) (*httptest.ResponseRecorder, entityBody) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/entities/"+entityID, nil)
	r.SetPathValue("id", entityID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	GetHandler(get, nil).ServeHTTP(rec, r)
	var resp entityBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// TestCreateHandler_201 (task-38 Test Specs, AC1): a valid body with identity
// present must produce 201, with the returned body's id set and
// status=="active".
func TestCreateHandler_201(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	const tin = "1234567897"
	want := Entity{ID: uuid.NewString(), Name: "Acme Ltd", TIN: strPtr(tin), Status: "active", CreatedAt: time.Now()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		if in.Name != "Acme Ltd" || in.TIN != tin {
			t.Fatalf("create called with unexpected input: %+v", in)
		}
		return want, nil
	}
	rec, body := doCreate(t, create, &id, createRequest{Name: "Acme Ltd", TIN: tin})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if body.ID != want.ID {
		t.Errorf("id = %q, want %q (id set from the created entity)", body.ID, want.ID)
	}
	if body.Status != "active" {
		t.Errorf("status = %q, want %q", body.Status, "active")
	}
}

// TestCreateHandler_InvalidTIN400 (AC2): a body with a bad TIN must produce
// 400 with a non-empty error message.
func TestCreateHandler_InvalidTIN400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		return Entity{}, fmt.Errorf("%w: tin checksum is invalid", ErrInvalidTIN)
	}
	rec, body := doCreate(t, create, &id, createRequest{Name: "Acme Ltd", TIN: "1234567890"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_MissingName400 (AC1): a body without a name must 400
// before create ever runs -- asserted by failing the test if create is
// called.
func TestCreateHandler_MissingName400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		t.Fatal("create must not run when name is missing")
		return Entity{}, nil
	}
	rec, body := doCreate(t, create, &id, createRequest{TIN: "1234567897"})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_DuplicateTIN409 (AC4): the store returning ErrDuplicateTIN
// must map to 409 with a non-empty error message.
func TestCreateHandler_DuplicateTIN409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		return Entity{}, ErrDuplicateTIN
	}
	rec, body := doCreate(t, create, &id, createRequest{Name: "Acme Ltd", TIN: "1234567897"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_NoIdentity401 (AC6): no identity in the request context
// must 401 before create ever runs -- asserted by failing the test if create
// is called.
func TestCreateHandler_NoIdentity401(t *testing.T) {
	create := func(ctx context.Context, in CreateInput) (Entity, error) {
		t.Fatal("create must not run without an identity")
		return Entity{}, nil
	}
	rec, body := doCreate(t, create, nil, createRequest{Name: "Acme Ltd", TIN: "1234567897"})

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestGetHandler_200 (AC5): a get resolving an entity must produce 200 with
// the entity's id in the body.
func TestGetHandler_200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	want := Entity{ID: entityID, Name: "Acme Ltd", TIN: strPtr("1234567897"), Status: "active", CreatedAt: time.Now()}
	get := func(ctx context.Context, gotID string) (Entity, error) {
		if gotID != entityID {
			t.Fatalf("get called with id = %q, want %q", gotID, entityID)
		}
		return want, nil
	}
	rec, body := doGet(t, get, &id, entityID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body.ID != entityID {
		t.Errorf("id = %q, want %q", body.ID, entityID)
	}
}

// TestGetHandler_404 (AC5): the store returning ErrNotFound must map to 404
// with a non-empty error message -- the shape a cross-tenant id resolves to.
func TestGetHandler_404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(ctx context.Context, gotID string) (Entity, error) {
		return Entity{}, ErrNotFound
	}
	rec, body := doGet(t, get, &id, uuid.NewString())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestGetHandler_NoIdentity401 (AC6): no identity in the request context must
// 401 before get ever runs -- asserted by failing the test if get is called.
func TestGetHandler_NoIdentity401(t *testing.T) {
	get := func(ctx context.Context, gotID string) (Entity, error) {
		t.Fatal("get must not run without an identity")
		return Entity{}, nil
	}
	rec, body := doGet(t, get, nil, uuid.NewString())

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context", rec.Code)
	}
	if body.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

func strPtr(s string) *string { return &s }

// --- DB-backed store tests (real Postgres, dbTestPools) ------------------------------

// dbTestPools returns the superuser (seed) and app-role (Store) pools for the
// portfolio db-integration suite below, or skips the test when the per-role
// DSNs are unset -- copied verbatim from internal/tenancy/tenancy_test.go
// (~line 155), the same env gate `make test-rls` and TestCurrentTenant_RoundTrip
// use (DATABASE_URL for invoice_app, DATABASE_SUPERUSER_URL for seeding as the
// BYPASSRLS superuser).
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("portfolio db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	// Registered before the app pool's Cleanup, so per LIFO ordering it closes
	// AFTER app's pool -- and callers that register a row-delete Cleanup of
	// their own (after calling dbTestPools) get it run BEFORE either pool
	// closes.
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

// seedEntity inserts one business_entities row for tenantID as the superuser
// (BYPASSRLS, so seeding needs no tenant context) and registers its own
// cleanup. tin may be nil (the column is nullable).
func seedEntity(t *testing.T, super *pgxpool.Pool, tenantID, name string, tin *string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, name, tin,
	).Scan(&id); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

// auditCount counts audit_log rows for tenantID+event, scoped via
// db.WithinTenantTx (FORCE RLS) -- mirrors internal/audit/audit_test.go's
// auditCount helper.
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

// TestStoreCreate_PersistsAndAudits (task-38 Test Specs, AC1/AC3): a Create
// with valid input, as tenant A's identity, must (a) persist a row visible to
// A via GetByID with status "active", and (b) write exactly one
// "portfolio.entity.created" audit_log row, actor == the caller's subject, in
// the same tenant.
func TestStoreCreate_PersistsAndAudits(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio create-test tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.created"
	before := auditCount(t, app, tenantID, event)

	const tin = "1234567897"
	entity, err := store.Create(c, CreateInput{Name: "Acme Ltd", TIN: tin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entity.ID == "" {
		t.Fatal("Create: entity.ID is empty, want a generated id")
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entity.ID)
	})
	if entity.Status != "active" {
		t.Errorf("Create: status = %q, want %q", entity.Status, "active")
	}
	if entity.TIN == nil || *entity.TIN != tin {
		t.Errorf("Create: tin = %v, want %q", entity.TIN, tin)
	}

	got, err := store.GetByID(c, entity.ID)
	if err != nil {
		t.Fatalf("GetByID after Create: %v", err)
	}
	if got.ID != entity.ID || got.Status != "active" {
		t.Errorf("GetByID after Create = %+v, want id=%s status=active", got, entity.ID)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before+1 {
		t.Fatalf("audit_log rows for %s = %d, want %d (exactly one new row)", event, after, before+1)
	}
	if actor := auditActor(t, app, tenantID, event); actor != userID {
		t.Errorf("audit actor = %q, want %q", actor, userID)
	}
}

// TestStoreCreate_FailedCreateWritesNoAudit (task-38 Test Specs, AC3/AC4):
// tenant A already has a row with TIN X; Create-ing another row with the same
// canonical TIN must fail with ErrDuplicateTIN AND leave the audit_log count
// unchanged -- proving the insert and the audit write share one transaction
// (the failed insert's audit.Record call, if any, rolled back too).
func TestStoreCreate_FailedCreateWritesNoAudit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio dup-tin-test tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tin = "1234567897"
	seedEntity(t, super, tenantID, "Existing Co", strPtr(tin))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.created"
	before := auditCount(t, app, tenantID, event)

	_, err := store.Create(c, CreateInput{Name: "Second Co", TIN: tin})
	if !errors.Is(err, ErrDuplicateTIN) {
		t.Fatalf("Create with duplicate tin err = %v, want ErrDuplicateTIN", err)
	}

	after := auditCount(t, app, tenantID, event)
	if after != before {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (failed create must write no audit row)", event, after, before)
	}
}

// TestStoreGetByID_OwnTenant (task-38 Test Specs, AC5): an entity seeded in
// tenant A must be returned by GetByID when called as A.
func TestStoreGetByID_OwnTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	userID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio getbyid-test tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tin = "1234567897"
	entityID := seedEntity(t, super, tenantID, "Acme Ltd", strPtr(tin))

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID})

	got, err := store.GetByID(c, entityID)
	if err != nil {
		t.Fatalf("GetByID(own tenant): %v", err)
	}
	if got.ID != entityID || got.Name != "Acme Ltd" || got.Status != "active" {
		t.Errorf("GetByID = %+v, want id=%s name=Acme Ltd status=active", got, entityID)
	}
}

// TestStoreGetByID_CrossTenantNotFound (task-38 Test Specs, AC5): an entity
// seeded in tenant B must resolve to ErrNotFound when GetByID is called as
// tenant A -- the service-layer half of cross-tenant read refusal (RLS makes
// B's row invisible to A's tx; the store must translate that absence to
// ErrNotFound, not a bare pgx.ErrNoRows).
func TestStoreGetByID_CrossTenantNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio cross-tenant A', 'firm'), ($2, 'portfolio cross-tenant B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	entityIDInB := seedEntity(t, super, tenantB, "B Corp", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	_, err := store.GetByID(c, entityIDInB)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID(cross-tenant id) err = %v, want ErrNotFound", err)
	}
}
