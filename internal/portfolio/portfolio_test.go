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
	"strings"
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

// --- List handler tests (task-36, M3-03-03 RED stage) --------------------------------

// doList issues a GET /v1/entities request (optionally with a raw query
// string, e.g. "?status=pending") through ListHandler.
func doList(t *testing.T, list func(ctx context.Context, f ListFilter) ([]Entity, int, error), id *auth.Identity, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/entities"+query, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ListHandler(list, nil).ServeHTTP(rec, r)
	return rec
}

// TestListHandler_EmptyState200 (task-36 Test Specs, AC-5): the store
// returning ([]Entity{}, 0, nil) must produce 200 with the RAW response body
// containing "entities":[] (never "entities":null) and "total":0.
//
// FAILS today (M3-03-03 RED stage): ListHandler is a compiling stub that
// always answers 501 without calling list -- this must go green once the
// executor implements the real envelope in Mode B.
func TestListHandler_EmptyState200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	called := false
	list := func(ctx context.Context, f ListFilter) ([]Entity, int, error) {
		called = true
		return []Entity{}, 0, nil
	}
	rec := doList(t, list, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (list called=%v, body=%s)", rec.Code, called, rec.Body.String())
	}
	if !called {
		t.Error("store.List was not called")
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte(`"entities":[]`)) {
		t.Errorf("body = %s, want raw JSON to contain \"entities\":[] (not null)", body)
	}
	if !bytes.Contains(body, []byte(`"total":0`)) {
		t.Errorf("body = %s, want raw JSON to contain \"total\":0", body)
	}
}

// TestListHandler_BadStatus400 (AC-2/4): an invalid ?status= value (anything
// other than "active"/"archived") must 400 with a non-empty error, and must
// never call the store -- validation happens in the handler before List
// runs.
//
// FAILS today (M3-03-03 RED stage): the stub returns 501 regardless of the
// query string.
func TestListHandler_BadStatus400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Entity, int, error) {
		t.Fatal("store.List must not run when status is invalid")
		return nil, 0, nil
	}
	rec := doList(t, list, &id, "?status=pending")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestListHandler_LimitDefaultAndClamp (AC-4): the ListFilter the handler
// passes to the store must default an omitted ?limit= to 50, and clamp an
// over-large ?limit=500 down to 200.
//
// FAILS today (M3-03-03 RED stage): the stub never calls list, so "called"
// stays false regardless of the query string -- the t.Fatalf below fires for
// both subtests.
func TestListHandler_LimitDefaultAndClamp(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantLimit int
	}{
		{"omitted defaults to 50", "", 50},
		{"500 clamps to 200", "?limit=500", 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			var captured ListFilter
			called := false
			list := func(ctx context.Context, f ListFilter) ([]Entity, int, error) {
				called = true
				captured = f
				return []Entity{}, 0, nil
			}
			rec := doList(t, list, &id, tc.query)
			if !called {
				t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
			}
			if captured.Limit != tc.wantLimit {
				t.Errorf("captured ListFilter.Limit = %d, want %d", captured.Limit, tc.wantLimit)
			}
		})
	}
}

// TestListHandler_NoIdentity401 (established pattern, same as Create/Get):
// no identity in the request context must 401 before list ever runs --
// asserted by failing the test if list is called.
func TestListHandler_NoIdentity401(t *testing.T) {
	list := func(ctx context.Context, f ListFilter) ([]Entity, int, error) {
		t.Fatal("store.List must not run without an identity")
		return nil, 0, nil
	}
	rec := doList(t, list, nil, "")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if body["error"] == "" {
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

// --- QA-added adversarial / edge-case tests (task-38 Part 2, QA Verify) --------------

// TestStoreCreate_TINUniquenessIsPerTenantNotGlobal (critical correctness): the
// duplicate-TIN unique index is (tenant_id, tin) -- a PARTIAL index scoped to
// tenant_id, not a global uniqueness constraint. The SAME canonical TIN
// created in TWO DIFFERENT tenants must NOT collide -- both Creates must
// succeed, each producing its own row under its own tenant. (Contrast
// TestStoreCreate_FailedCreateWritesNoAudit, which proves the 409 case: a
// duplicate TIN WITHIN one tenant.)
func TestStoreCreate_TINUniquenessIsPerTenantNotGlobal(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio per-tenant-tin A', 'firm'), ($2, 'portfolio per-tenant-tin B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	store := NewStore(app)
	const tin = "1234567897"

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	entityA, err := store.Create(cA, CreateInput{Name: "Tenant A Co", TIN: tin})
	if err != nil {
		t.Fatalf("Create in tenant A: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entityA.ID)
	})

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	entityB, err := store.Create(cB, CreateInput{Name: "Tenant B Co", TIN: tin})
	if err != nil {
		t.Fatalf("Create in tenant B with the SAME canonical TIN as tenant A: %v (want success -- the unique index is per-tenant, not global)", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entityB.ID)
	})

	if entityA.ID == entityB.ID {
		t.Fatalf("entityA.ID == entityB.ID (%s), want two distinct rows", entityA.ID)
	}

	gotA, err := store.GetByID(cA, entityA.ID)
	if err != nil || gotA.TIN == nil || *gotA.TIN != tin {
		t.Fatalf("GetByID(A) = %+v, err=%v, want tin=%q under tenant A", gotA, err, tin)
	}
	gotB, err := store.GetByID(cB, entityB.ID)
	if err != nil || gotB.TIN == nil || *gotB.TIN != tin {
		t.Fatalf("GetByID(B) = %+v, err=%v, want tin=%q under tenant B", gotB, err, tin)
	}
}

// TestStoreCreate_NullableOptionalFieldsRoundTrip (edge case): Registration,
// Sector, and Address are optional (*string, nullable columns). Omitted (nil)
// on Create must persist as SQL NULL and round-trip through GetByID as a nil
// *string -- NOT an empty string. When provided, the values must round-trip
// unchanged.
func TestStoreCreate_NullableOptionalFieldsRoundTrip(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio nullable-fields tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// Omitted -> nil -> persisted as SQL NULL -> nil *string on read-back.
	omitted, err := store.Create(c, CreateInput{Name: "Nil Fields Co", TIN: "1234567897"})
	if err != nil {
		t.Fatalf("Create (omitted optional fields): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, omitted.ID)
	})
	if omitted.Registration != nil || omitted.Sector != nil || omitted.Address != nil {
		t.Errorf("Create (omitted): registration=%v sector=%v address=%v, want all nil",
			omitted.Registration, omitted.Sector, omitted.Address)
	}
	got, err := store.GetByID(c, omitted.ID)
	if err != nil {
		t.Fatalf("GetByID (omitted): %v", err)
	}
	if got.Registration != nil || got.Sector != nil || got.Address != nil {
		t.Errorf("GetByID (omitted): registration=%v sector=%v address=%v, want all nil (SQL NULL, not empty string)",
			got.Registration, got.Sector, got.Address)
	}

	// Set -> round-trips the values unchanged.
	reg, sec, addr := "RC-12345", "Manufacturing", "12 Marina Rd, Lagos"
	set, err := store.Create(c, CreateInput{Name: "Set Fields Co", TIN: "123456780006", Registration: &reg, Sector: &sec, Address: &addr})
	if err != nil {
		t.Fatalf("Create (set optional fields): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, set.ID)
	})
	if set.Registration == nil || *set.Registration != reg || set.Sector == nil || *set.Sector != sec || set.Address == nil || *set.Address != addr {
		t.Errorf("Create (set): registration=%v sector=%v address=%v, want %q/%q/%q",
			set.Registration, set.Sector, set.Address, reg, sec, addr)
	}
	got2, err := store.GetByID(c, set.ID)
	if err != nil {
		t.Fatalf("GetByID (set): %v", err)
	}
	if got2.Registration == nil || *got2.Registration != reg || got2.Sector == nil || *got2.Sector != sec || got2.Address == nil || *got2.Address != addr {
		t.Errorf("GetByID (set): registration=%v sector=%v address=%v, want %q/%q/%q",
			got2.Registration, got2.Sector, got2.Address, reg, sec, addr)
	}
}

// TestStoreCreate_AuditRowIsTenantScoped (AC3, AC5): after a successful Create
// in tenant A, the portfolio.entity.created audit_log row must be visible
// under tenant A's GUC context with actor == the caller's Subject, and must
// NOT be visible under a different tenant B's GUC context -- audit_log's own
// FORCE-RLS tenant isolation applies to this event just as it does to any
// other row.
func TestStoreCreate_AuditRowIsTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio audit-scope A', 'firm'), ($2, 'portfolio audit-scope B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	store := NewStore(app)
	userID := uuid.NewString()
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantA})

	const event = "portfolio.entity.created"
	entity, err := store.Create(cA, CreateInput{Name: "Audit Scope Co", TIN: "1234567897"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entity.ID)
	})

	if n := auditCount(t, app, tenantA, event); n != 1 {
		t.Fatalf("audit_log rows for %s under tenant A = %d, want 1", event, n)
	}
	if actor := auditActor(t, app, tenantA, event); actor != userID {
		t.Errorf("audit actor under tenant A = %q, want %q", actor, userID)
	}
	if n := auditCount(t, app, tenantB, event); n != 0 {
		t.Errorf("audit_log rows for %s under tenant B = %d, want 0 (tenant A's audit row must not be visible to tenant B)", event, n)
	}
}

// TestStoreGetByID_UnknownIDNotFound (AC5): a well-formed uuid that does not
// exist in ANY tenant (not merely a different tenant, per
// TestStoreGetByID_CrossTenantNotFound) must resolve to ErrNotFound.
func TestStoreGetByID_UnknownIDNotFound(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()})

	_, err := store.GetByID(c, uuid.NewString())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID(unknown id) err = %v, want ErrNotFound", err)
	}
}

// TestStoreGetByID_MalformedIDNotUUID (AC5, edge case): a non-uuid id string
// does NOT map to ErrNotFound. Postgres rejects it client-side as invalid
// input syntax for type uuid (SQLSTATE 22P02) before any row-matching
// happens, so GetByID surfaces that raw error, which statusForErr's default
// case maps to 500 -- a conscious, documented behavior (not silently assumed)
// for a malformed path param, distinct from the 404 a well-formed-but-absent
// or cross-tenant id produces.
func TestStoreGetByID_MalformedIDNotUUID(t *testing.T) {
	_, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()})

	_, err := store.GetByID(c, "not-a-uuid")
	if err == nil {
		t.Fatal("GetByID(malformed id) err = nil, want a non-nil error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("GetByID(malformed id) should NOT map to ErrNotFound -- it is a raw Postgres invalid-input error (22P02), which statusForErr maps to 500, not 404")
	}
}

// TestStoreCreate_InvalidTINRejectedAtStoreLayer (AC2, store layer): Store.Create
// itself -- not just the handler's decode/validation path -- must reject a
// Luhn-failing TIN via ValidateTIN before any INSERT runs: no business_entities
// row and no audit_log row.
func TestStoreCreate_InvalidTINRejectedAtStoreLayer(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio store-invalid-tin tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// No business_entities cleanup registered: none is expected to exist (asserted
	// below), and the FK's ON DELETE CASCADE would remove any surprise row anyway
	// when this tenant is deleted.
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const event = "portfolio.entity.created"
	beforeAudit := auditCount(t, app, tenantID, event)
	var beforeRows int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, tenantID).Scan(&beforeRows); err != nil {
		t.Fatalf("count business_entities before: %v", err)
	}

	// Format-valid (10 digits) but Luhn-failing -- see tin_test.go
	// TestValidateTIN_ChecksumRejected, which proves ValidateTIN itself rejects
	// "1234567890" on checksum grounds alone.
	_, err := store.Create(c, CreateInput{Name: "Bad TIN Co", TIN: "1234567890"})
	if !errors.Is(err, ErrInvalidTIN) {
		t.Fatalf("Create with Luhn-failing TIN err = %v, want ErrInvalidTIN", err)
	}

	var afterRows int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, tenantID).Scan(&afterRows); err != nil {
		t.Fatalf("count business_entities after: %v", err)
	}
	if afterRows != beforeRows {
		t.Errorf("business_entities rows for tenant = %d, want unchanged %d (invalid TIN must write no row)", afterRows, beforeRows)
	}
	if after := auditCount(t, app, tenantID, event); after != beforeAudit {
		t.Errorf("audit_log rows for %s = %d, want unchanged %d (invalid TIN must write no audit row)", event, after, beforeAudit)
	}
}

// --- DB-backed Store.List tests (task-36, M3-03-03 RED stage) ------------------------

// seedEntityStatus inserts one business_entities row for tenantID with an
// EXPLICIT status ("active"|"archived") -- seedEntity always takes the
// column DEFAULT ("active"), so List's status-filter tests need this sibling
// helper to seed archived rows too. Registers its own cleanup, same as
// seedEntity.
func seedEntityStatus(t *testing.T, super *pgxpool.Pool, tenantID, name string, tin *string, status string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, name, tin, status,
	).Scan(&id); err != nil {
		t.Fatalf("seed business_entities (status=%s): %v", status, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

// TestStoreList_OwnTenantOnly (task-36 Test Specs, AC-1): tenant A has 3
// entities, tenant B has 2 -- List as A must return exactly A's 3, none of
// B's. This is the service-layer half of cross-tenant isolation; table-level
// RLS for business_entities is already covered by
// internal/platform/db/business_entities_rls_test.go (M3-01) and is not
// re-tested here.
//
// FAILS today (M3-03-03 RED stage): Store.List is a stub returning
// errors.New("not implemented: M3-03-03").
func TestStoreList_OwnTenantOnly(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-own-tenant A', 'firm'), ($2, 'portfolio list-own-tenant B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	seedEntity(t, super, tenantA, "A Co One", nil)
	seedEntity(t, super, tenantA, "A Co Two", nil)
	seedEntity(t, super, tenantA, "A Co Three", nil)
	seedEntity(t, super, tenantB, "B Co One", nil)
	seedEntity(t, super, tenantB, "B Co Two", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	items, total, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	for _, e := range items {
		if strings.HasPrefix(e.Name, "B Co") {
			t.Errorf("List(as tenant A) returned tenant B's row: %+v", e)
		}
	}
}

// TestStoreList_StatusFilter (AC-2): tenant A has 2 active + 1 archived --
// List(status="archived") must return exactly the 1 archived row.
//
// FAILS today (M3-03-03 RED stage): stub.
func TestStoreList_StatusFilter(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-status-filter tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	seedEntity(t, super, tenantID, "Active One", nil)
	seedEntity(t, super, tenantID, "Active Two", nil)
	archivedID := seedEntityStatus(t, super, tenantID, "Archived One", nil, "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	status := "archived"
	items, total, err := store.List(c, ListFilter{Status: &status, Limit: 50})
	if err != nil {
		t.Fatalf("List(status=archived): %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len(items)=%d, want 1/1", total, len(items))
	}
	if items[0].ID != archivedID {
		t.Errorf("items[0].ID = %q, want %q (the archived row)", items[0].ID, archivedID)
	}
	if items[0].Status != "archived" {
		t.Errorf("items[0].Status = %q, want %q", items[0].Status, "archived")
	}
}

// TestStoreList_StatusOmittedReturnsBoth (AC-2): the same tenant/data shape
// as TestStoreList_StatusFilter (2 active + 1 archived) -- List(status=nil)
// must return all 3 rows.
//
// FAILS today (M3-03-03 RED stage): stub.
func TestStoreList_StatusOmittedReturnsBoth(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-status-omitted tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	seedEntity(t, super, tenantID, "Active One", nil)
	seedEntity(t, super, tenantID, "Active Two", nil)
	seedEntityStatus(t, super, tenantID, "Archived One", nil, "archived")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List(status omitted): %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("total=%d len(items)=%d, want 3/3", total, len(items))
	}
}

// TestStoreList_SearchQ (AC-3): tenant A has "Okafor Ltd" and "Lagos Foods"
// -- List(q="okaf") must match "Okafor Ltd" only, case-insensitive.
//
// FAILS today (M3-03-03 RED stage): stub.
func TestStoreList_SearchQ(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-search-q tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	okaforID := seedEntity(t, super, tenantID, "Okafor Ltd", nil)
	seedEntity(t, super, tenantID, "Lagos Foods", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Q: "okaf", Limit: 50})
	if err != nil {
		t.Fatalf("List(q=okaf): %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len(items)=%d, want 1/1", total, len(items))
	}
	if items[0].ID != okaforID {
		t.Errorf("items[0].ID = %q, want %q (Okafor Ltd)", items[0].ID, okaforID)
	}
}

// TestStoreList_SearchQMatchesTIN (AC-3): tenant A has an entity with the
// known canonical TIN "1234567897" (valid per ValidateTIN's Luhn check, and
// already used as a known-good TIN elsewhere in this file, e.g.
// TestCreateHandler_201) -- List(q="34567"), a substring of that TIN, must
// match it.
//
// FAILS today (M3-03-03 RED stage): stub.
func TestStoreList_SearchQMatchesTIN(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-search-tin tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	const tin = "1234567897"
	entityID := seedEntity(t, super, tenantID, "Tin Match Co", strPtr(tin))
	seedEntity(t, super, tenantID, "No Tin Co", nil)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Q: "34567", Limit: 50})
	if err != nil {
		t.Fatalf("List(q=34567): %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len(items)=%d, want 1/1", total, len(items))
	}
	if items[0].ID != entityID {
		t.Errorf("items[0].ID = %q, want %q (the TIN-matching row)", items[0].ID, entityID)
	}
}

// TestStoreList_Pagination (AC-4): tenant A has 5 entities -- List(limit=2,
// offset=2) must return exactly 2 rows, the middle page in stable name ASC,
// id ASC order, with total=5 (the full filtered count, not the page size).
//
// FAILS today (M3-03-03 RED stage): stub.
func TestStoreList_Pagination(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-pagination tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	names := []string{"Co A", "Co B", "Co C", "Co D", "Co E"}
	for _, name := range names {
		seedEntity(t, super, tenantID, name, nil)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List(limit=2,offset=2): %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	// name ASC lexically: Co A, Co B, Co C, Co D, Co E -- offset=2 skips A,B,
	// so the stable middle page is [Co C, Co D].
	if items[0].Name != "Co C" || items[1].Name != "Co D" {
		t.Errorf("items = [%q, %q], want [\"Co C\", \"Co D\"] (stable name ASC order, middle page)", items[0].Name, items[1].Name)
	}
}

// TestStoreList_EmptyTenant (AC-5): a fresh tenant with 0 rows -- List must
// return a non-nil empty slice, total 0, nil err (never a nil slice, which
// would serialize to JSON null instead of []).
//
// FAILS today (M3-03-03 RED stage): the stub returns a nil slice AND a
// non-nil error, so both checks below fail.
func TestStoreList_EmptyTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio list-empty-tenant tenant', 'firm')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List(empty tenant): %v", err)
	}
	if items == nil {
		t.Error("items = nil, want a non-nil empty slice (so JSON marshals [] not null)")
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}
