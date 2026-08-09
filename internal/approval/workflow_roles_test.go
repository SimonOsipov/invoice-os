package approval

// The store seam under a real Postgres: ListRoles, CreateRole, UpdateRole and
// DeleteRole as invoice_app, through db.WithinRequestTenantTx, with RLS as the only
// tenant filter.
//
// Every test below except TestWorkflowRole_StoreSatisfiesTheHandlerSeam self-skips
// without DATABASE_URL + DATABASE_SUPERUSER_URL. Run locally with
// `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate step fails the
// build on any skip (TestApproval_CIRLSJobRunsThisPackage guards that the step exists).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness ---------------------------------------------------------------

// dbTestPools returns the superuser (seed + read-back) and app-role (Store) pools,
// or skips when the per-role DSNs are unset — the same gate the sibling suites use
// (idiom copied from internal/tenancy/tenancy_test.go).
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("approval db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-approvals`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
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

func seedTenant(t *testing.T, super *pgxpool.Pool, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, name); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// Cascades to memberships, workflow_roles and workflow_role_members. audit_log has
	// no tenants FK and is append-only, so its rows outlive the tenant by design.
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

func seedMembership(t *testing.T, super *pgxpool.Pool, tenantID, userID, role, status string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
		tenantID, userID, role, status); err != nil {
		t.Fatalf("seed membership (%s/%s): %v", role, status, err)
	}
}

// callerCtx returns a ctx carrying a seeded membership's identity, plus its user id.
func callerCtx(t *testing.T, super *pgxpool.Pool, tenantID, role, status string) (context.Context, string) {
	t.Helper()
	userID := uuid.NewString()
	seedMembership(t, super, tenantID, userID, role, status)
	return auth.WithIdentity(context.Background(),
		auth.Identity{Subject: userID, Role: "authenticated", TenantID: tenantID}), userID
}

// activeAdmin is the caller CreateRole requires.
func activeAdmin(t *testing.T, super *pgxpool.Pool, tenantID string) (context.Context, string) {
	t.Helper()
	return callerCtx(t, super, tenantID, "admin", "active")
}

// seedWorkflowRole inserts one live role as the superuser and returns its id.
func seedWorkflowRole(t *testing.T, super *pgxpool.Pool, tenantID, key, title string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, key, title).Scan(&id); err != nil {
		t.Fatalf("seed workflow role %q: %v", key, err)
	}
	return id
}

// softDeleteWorkflowRole stamps deleted_at, and Fatals if no live row matched —
// without that guard a mis-seed silently turns the soft-delete tests into
// duplicate-title tests that pass for the wrong reason.
func softDeleteWorkflowRole(t *testing.T, super *pgxpool.Pool, roleID string) {
	t.Helper()
	var at time.Time
	if err := super.QueryRow(context.Background(),
		`UPDATE workflow_roles SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL RETURNING deleted_at`,
		roleID).Scan(&at); err != nil {
		t.Fatalf("soft-delete workflow role %s: %v", roleID, err)
	}
}

// seedRoleDesc gives a seeded role a blurb, so "description unchanged" assertions are
// not vacuously ” == ”.
func seedRoleDesc(t *testing.T, super *pgxpool.Pool, roleID, desc string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE workflow_roles SET description = $2 WHERE id = $1`, roleID, desc)
	if err != nil {
		t.Fatalf("seed description on %s: %v", roleID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("seed description on %s affected %d rows, want 1", roleID, tag.RowsAffected())
	}
}

// storedRole is a role's full six-column image, for the "deleted_at is the ONLY
// changed column" assertions.
type storedRole struct {
	ID, Key, Title, Desc string
	DeletedAt            *time.Time
	CreatedAt            time.Time
}

func roleRow(t *testing.T, super *pgxpool.Pool, roleID string) storedRole {
	t.Helper()
	var r storedRole
	if err := super.QueryRow(context.Background(),
		`SELECT id, key, title, description, deleted_at, created_at FROM workflow_roles WHERE id = $1`,
		roleID).Scan(&r.ID, &r.Key, &r.Title, &r.Desc, &r.DeletedAt, &r.CreatedAt); err != nil {
		t.Fatalf("read back role %s: %v", roleID, err)
	}
	return r
}

// equal compares by instant, not by time.Time representation.
func (r storedRole) equal(o storedRole) bool {
	if r.ID != o.ID || r.Key != o.Key || r.Title != o.Title || r.Desc != o.Desc || !r.CreatedAt.Equal(o.CreatedAt) {
		return false
	}
	switch {
	case r.DeletedAt == nil && o.DeletedAt == nil:
		return true
	case r.DeletedAt == nil || o.DeletedAt == nil:
		return false
	default:
		return r.DeletedAt.Equal(*o.DeletedAt)
	}
}

func (r storedRole) String() string {
	deleted := "NULL"
	if r.DeletedAt != nil {
		deleted = r.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("{key:%q title:%q desc:%q deleted_at:%s created_at:%s}",
		r.Key, r.Title, r.Desc, deleted, r.CreatedAt.UTC().Format(time.RFC3339Nano))
}

func ptr[T any](v T) *T { return &v }

// stampCreatedAt forces a role's created_at so the L1 ordering (and its tie-break)
// is observable rather than at the mercy of now() resolution.
func stampCreatedAt(t *testing.T, super *pgxpool.Pool, roleID string, at time.Time) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE workflow_roles SET created_at = $2 WHERE id = $1`, roleID, at)
	if err != nil {
		t.Fatalf("stamp created_at on %s: %v", roleID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("stamp created_at on %s affected %d rows, want 1", roleID, tag.RowsAffected())
	}
}

func staffWorkflowRole(t *testing.T, super *pgxpool.Pool, tenantID, roleID, userID string, ord int) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, $4)`,
		tenantID, roleID, userID, ord); err != nil {
		t.Fatalf("staff role %s with %s at ord %d: %v", roleID, userID, ord, err)
	}
}

// liveRoleKeys reads the tenant's live keys as the superuser — a read-back of what
// the store committed, never a way around RLS for a domain call.
func liveRoleKeys(t *testing.T, super *pgxpool.Pool, tenantID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT key FROM workflow_roles WHERE tenant_id = $1 AND deleted_at IS NULL ORDER BY key`, tenantID)
	if err != nil {
		t.Fatalf("read back live keys: %v", err)
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan key: %v", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read back live keys: %v", err)
	}
	return keys
}

func rowCount(t *testing.T, super *pgxpool.Pool, table, tenantID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, tenantID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func auditCount(t *testing.T, super *pgxpool.Pool, tenantID, event string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`,
		tenantID, event).Scan(&n); err != nil {
		t.Fatalf("count audit_log %s: %v", event, err)
	}
	return n
}

func keysOf(roles []Role) []string {
	keys := make([]string, 0, len(roles))
	for _, r := range roles {
		keys = append(keys, r.Key)
	}
	return keys
}

// pgCode extracts the SQLSTATE from err, or "" if err wraps no *pgconn.PgError.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// sqlRecorder records the SQL of every statement its pool issues, so a statement
// COUNT can be asserted — the only way to see an N+1, whose results are identical.
type sqlRecorder struct {
	mu  sync.Mutex
	sql []string
}

func (r *sqlRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	r.sql = append(r.sql, d.SQL)
	r.mu.Unlock()
	return ctx
}

func (r *sqlRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *sqlRecorder) reset() {
	r.mu.Lock()
	r.sql = nil
	r.mu.Unlock()
}

// mentioning filters to the statements containing substr, which keeps the count
// immune to the pool's own begin/commit/health-check traffic.
func (r *sqlRecorder) mentioning(substr string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.sql {
		if strings.Contains(s, substr) {
			out = append(out, s)
		}
	}
	return out
}

// tracedAppPool is a second app-role pool whose statements are recorded. Callers
// must already have gone through dbTestPools, which owns the skip gate.
func tracedAppPool(t *testing.T) (*pgxpool.Pool, *sqlRecorder) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	rec := &sqlRecorder{}
	cfg.ConnConfig.Tracer = rec
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect traced app pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p, rec
}

// --- the seam (no DB, cannot skip) -----------------------------------------

// TestWorkflowRole_StoreSatisfiesTheHandlerSeam is the only assertion in this file
// that runs in CI's `go` job: it fails the BUILD on signature drift (06 wires the
// handlers to these five types), and pins that every method resolves the identity
// before touching the pool — a store that dialled first would panic on the nil pool.
//
// UpdateRole and SetRoleMembers are called with VALID arguments on purpose: (nil, nil)
// and a malformed or repeated member id are ErrValidation above the tx, which would
// silently stop proving the identity-first property.
func TestWorkflowRole_StoreSatisfiesTheHandlerSeam(t *testing.T) {
	nilPool := NewStore(nil) // never dialled: the identity is resolved first
	var list RolesLister = nilPool.ListRoles
	var create RoleCreator = nilPool.CreateRole
	var update RoleUpdater = nilPool.UpdateRole
	var remove RoleDeleter = nilPool.DeleteRole
	var staff RoleStaffer = nilPool.SetRoleMembers

	if _, err := list(context.Background()); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("ListRoles with no identity in ctx: err = %v, want db.ErrNoTenant", err)
	}
	if _, err := create(context.Background(), "Engagement Partner", ""); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("CreateRole with no identity in ctx: err = %v, want db.ErrNoTenant", err)
	}
	if _, err := update(context.Background(), "engagement-partner", ptr("New title"), nil); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("UpdateRole with no identity in ctx: err = %v, want db.ErrNoTenant", err)
	}
	if _, err := remove(context.Background(), "engagement-partner"); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("DeleteRole with no identity in ctx: err = %v, want db.ErrNoTenant", err)
	}
	if _, err := staff(context.Background(), "engagement-partner", []string{uuid.NewString()}); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("SetRoleMembers with no identity in ctx: err = %v, want db.ErrNoTenant", err)
	}
}

// --- CreateRole ------------------------------------------------------------

// TestWorkflowRole_CreateMintsKeyFromTitle: the key is minted server-side from the
// title (the request type carries no key field), and the returned Role is the four
// wire fields with a non-nil members.
func TestWorkflowRole_CreateMintsKeyFromTitle(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 create-mints")
	c, _ := activeAdmin(t, super, tenantID)

	got, err := NewStore(app).CreateRole(c, "Engagement Partner", "First sign-off")
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	want := Role{Key: "engagement-partner", Title: "Engagement Partner", Desc: "First sign-off", Members: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CreateRole = %+v, want %+v", got, want)
	}
	if got.Members == nil {
		t.Error("members is nil; the producer must construct it as []string{} or the wire renders null")
	}

	var key, title, desc string
	var deletedAt *time.Time
	if err := super.QueryRow(context.Background(),
		`SELECT key, title, description, deleted_at FROM workflow_roles WHERE tenant_id = $1`,
		tenantID).Scan(&key, &title, &desc, &deletedAt); err != nil {
		t.Fatalf("read back the row: %v", err)
	}
	if key != "engagement-partner" || title != "Engagement Partner" || desc != "First sign-off" {
		t.Errorf("stored row = (%q, %q, %q), want (engagement-partner, Engagement Partner, First sign-off)", key, title, desc)
	}
	if deletedAt != nil {
		t.Errorf("stored deleted_at = %v, want NULL — a new role is live", deletedAt)
	}
}

// TestWorkflowRole_CreateNeverReusesASoftDeletedKey: the collision set is read with
// NO deleted_at filter. Add one and the taken-set is empty, the key mints back to
// `tax-reviewer`, and the INSERT hits workflow_roles_tenant_key_uq — this returns
// ErrConflict instead of a key.
func TestWorkflowRole_CreateNeverReusesASoftDeletedKey(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 deleted-key")
	c, _ := activeAdmin(t, super, tenantID)

	deletedID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	softDeleteWorkflowRole(t, super, deletedID)

	got, err := NewStore(app).CreateRole(c, "Tax Reviewer", "")
	if err != nil {
		t.Fatalf("CreateRole: %v, want a fresh key — a deleted_at filter on the key query makes this ErrConflict", err)
	}
	if got.Key != "tax-reviewer-2" {
		t.Errorf("key = %q, want tax-reviewer-2", got.Key)
	}

	var deletedKey string
	if err := super.QueryRow(context.Background(),
		`SELECT key FROM workflow_roles WHERE id = $1`, deletedID).Scan(&deletedKey); err != nil {
		t.Fatalf("read back the deleted role: %v", err)
	}
	if deletedKey != "tax-reviewer" {
		t.Errorf("the soft-deleted role's key = %q, want it left at tax-reviewer", deletedKey)
	}
}

// TestWorkflowRole_CreateAllowsDuplicateTitles: canSaveRole gates on an empty name
// and nothing else, so a duplicate title is legal and only the key is suffixed.
func TestWorkflowRole_CreateAllowsDuplicateTitles(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 duplicate-title")
	c, _ := activeAdmin(t, super, tenantID)
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	got, err := NewStore(app).CreateRole(c, "Tax Reviewer", "")
	if err != nil {
		t.Fatalf("CreateRole with a duplicate title: %v, want success", err)
	}
	if got.Key != "tax-reviewer-2" {
		t.Errorf("key = %q, want tax-reviewer-2", got.Key)
	}
	if got.Title != "Tax Reviewer" {
		t.Errorf("title = %q, want the duplicate preserved verbatim", got.Title)
	}
	if keys := liveRoleKeys(t, super, tenantID); !reflect.DeepEqual(keys, []string{"tax-reviewer", "tax-reviewer-2"}) {
		t.Errorf("live keys = %v, want both rows live", keys)
	}
}

// TestWorkflowRole_CreateRejectsEmptyTitle: an empty or whitespace-only title is
// ErrValidation and writes nothing. The trailing control creates successfully in the
// same tenant, so the zero-row assertions discriminate validation from a store that
// refuses every call.
func TestWorkflowRole_CreateRejectsEmptyTitle(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 empty-title")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	for _, title := range []string{"", "   ", "\t\n", " "} {
		t.Run(fmt.Sprintf("title=%q", title), func(t *testing.T) {
			if _, err := store.CreateRole(c, title, "a blurb"); !errors.Is(err, ErrValidation) {
				t.Errorf("CreateRole(%q) err = %v, want ErrValidation", title, err)
			}
		})
	}
	if n := rowCount(t, super, "workflow_roles", tenantID); n != 0 {
		t.Errorf("workflow_roles rows = %d, want 0 — a rejected title must write nothing", n)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.created"); n != 0 {
		t.Errorf("workflow_role.created audit rows = %d, want 0", n)
	}

	if _, err := store.CreateRole(c, "Quality Reviewer", ""); err != nil {
		t.Fatalf("control: CreateRole with a valid title: %v — the zero-row assertions above are vacuous unless this succeeds", err)
	}
	if n := rowCount(t, super, "workflow_roles", tenantID); n != 1 {
		t.Errorf("control: workflow_roles rows = %d, want 1", n)
	}
}

// TestWorkflowRole_CreateTitleIsValidatedBeforeTheCallerRoleIsRead pins the guard
// order, which nothing else does: a non-admin sending a blank title gets
// ErrValidation (400), not ErrNotPermitted (403). The check reads no row and depends
// only on the caller's own argument, so answering it first reveals nothing about the
// tenant and spares an unauthorized caller a transaction. Lower it back into the
// closure and the first assertion goes red.
func TestWorkflowRole_CreateTitleIsValidatedBeforeTheCallerRoleIsRead(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 guard-order")
	store := NewStore(app)
	c, _ := callerCtx(t, super, tenantID, "preparer", "active")

	if _, err := store.CreateRole(c, "   ", "a blurb"); !errors.Is(err, ErrValidation) {
		t.Errorf("non-admin with a blank title: err = %v, want ErrValidation — the title is checked first", err)
	}
	// The same caller with a storable title reaches the permission gate, so the
	// assertion above is about ORDER, not about a store that refuses everything.
	if _, err := store.CreateRole(c, "Engagement Partner", ""); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("non-admin with a valid title: err = %v, want ErrNotPermitted", err)
	}
	if n := rowCount(t, super, "workflow_roles", tenantID); n != 0 {
		t.Errorf("workflow_roles rows = %d, want 0 — neither refusal may write", n)
	}
}

// TestWorkflowRole_CreateTrimsTitleAndDesc: RoleModal.tsx:82-83 trims BOTH fields,
// and the trimmed title is what is stored and what feeds the key.
func TestWorkflowRole_CreateTrimsTitleAndDesc(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 trim")
	c, _ := activeAdmin(t, super, tenantID)

	got, err := NewStore(app).CreateRole(c, "  Tax Reviewer  ", "  blurb  ")
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	want := Role{Key: "tax-reviewer", Title: "Tax Reviewer", Desc: "blurb", Members: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CreateRole = %+v, want %+v", got, want)
	}

	var title, desc string
	if err := super.QueryRow(context.Background(),
		`SELECT title, description FROM workflow_roles WHERE tenant_id = $1`, tenantID).Scan(&title, &desc); err != nil {
		t.Fatalf("read back the row: %v", err)
	}
	if title != "Tax Reviewer" || desc != "blurb" {
		t.Errorf("stored (title, description) = (%q, %q), want (Tax Reviewer, blurb)", title, desc)
	}
}

// TestWorkflowRole_CreateStoresEmptyDescNotNull: an omitted desc is stored empty —
// the column is NOT NULL and the Go field is a non-pointer string, so `null` can
// reach neither the row nor the wire.
func TestWorkflowRole_CreateStoresEmptyDescNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 empty-desc")
	c, _ := activeAdmin(t, super, tenantID)

	got, err := NewStore(app).CreateRole(c, "Quality Reviewer", "")
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if got.Desc != "" {
		t.Errorf("desc = %q, want the empty string", got.Desc)
	}

	var desc *string
	if err := super.QueryRow(context.Background(),
		`SELECT description FROM workflow_roles WHERE tenant_id = $1`, tenantID).Scan(&desc); err != nil {
		t.Fatalf("read back description: %v", err)
	}
	if desc == nil {
		t.Fatal("stored description IS NULL, want ''")
	}
	if *desc != "" {
		t.Errorf("stored description = %q, want ''", *desc)
	}
	if raw, err := json.Marshal(got); err != nil {
		t.Fatalf("marshal: %v", err)
	} else if !strings.Contains(string(raw), `"desc":""`) {
		t.Errorf("wire = %s, want it to carry an empty desc rather than omit it", raw)
	}
}

// TestWorkflowRole_CreateRequiresActiveAdmin: the CALLER axis. The caller-role read
// carries AND status = 'active', so a suspended or invited admin is refused as
// firmly as a non-admin, and a caller with no membership row at all is refused too.
// (This is not Decision Q2, which leaves the staffed SUBJECT unrestricted.)
func TestWorkflowRole_CreateRequiresActiveAdmin(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 caller-axis")
	store := NewStore(app)

	refused := map[string]context.Context{}
	for _, caller := range []struct{ name, role, status string }{
		{"active preparer", "preparer", "active"},
		{"active reviewer", "reviewer", "active"},
		{"suspended admin", "admin", "suspended"},
		{"invited admin", "admin", "invited"},
	} {
		c, _ := callerCtx(t, super, tenantID, caller.role, caller.status)
		refused[caller.name] = c
	}
	// No membership row: a valid tenant claim for a user the tenant does not know.
	refused["no membership row"] = auth.WithIdentity(context.Background(),
		auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	if len(refused) != 5 {
		t.Fatalf("built %d callers, want 5 — a short table would pass vacuously", len(refused))
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CreateRole(c, "Engagement Partner", ""); !errors.Is(err, ErrNotPermitted) {
				t.Errorf("CreateRole as %s: err = %v, want ErrNotPermitted", name, err)
			}
		})
	}
	if n := rowCount(t, super, "workflow_roles", tenantID); n != 0 {
		t.Errorf("workflow_roles rows = %d, want 0", n)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.created"); n != 0 {
		t.Errorf("workflow_role.created audit rows = %d, want 0", n)
	}

	c, _ := activeAdmin(t, super, tenantID)
	if _, err := store.CreateRole(c, "Engagement Partner", ""); err != nil {
		t.Fatalf("control: CreateRole as an active admin: %v — the refusals above are vacuous unless this succeeds", err)
	}
}

// TestWorkflowRole_CreateCallerRoleIsScopedToTheCallersTenant: the caller-role read
// carries no tenant predicate, so RLS on memberships is the only thing keeping one
// human's admin row in tenant B out of a tenant-A create. Widen that policy and this
// is the test that goes red; nothing else in the package would notice.
func TestWorkflowRole_CreateCallerRoleIsScopedToTheCallersTenant(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "APPR-02 caller-scope A")
	tenantB := seedTenant(t, super, "APPR-02 caller-scope B")
	store := NewStore(app)

	dual := uuid.NewString() // admin in B, preparer in A
	seedMembership(t, super, tenantB, dual, "admin", "active")
	seedMembership(t, super, tenantA, dual, "preparer", "active")

	stranger := uuid.NewString() // admin in B, unknown to A
	seedMembership(t, super, tenantB, stranger, "admin", "active")

	for name, subject := range map[string]string{
		"admin in B, preparer in A": dual,
		"admin in B, no row in A":   stranger,
	} {
		t.Run(name, func(t *testing.T) {
			c := auth.WithIdentity(context.Background(),
				auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantA})
			if _, err := store.CreateRole(c, "Engagement Partner", ""); !errors.Is(err, ErrNotPermitted) {
				t.Errorf("CreateRole into A as %s: err = %v, want ErrNotPermitted", name, err)
			}
		})
	}
	if n := rowCount(t, super, "workflow_roles", tenantA); n != 0 {
		t.Errorf("tenant A workflow_roles rows = %d, want 0", n)
	}

	// Control: the same subject IS an active admin in B, so the refusals above are
	// about the tenant axis rather than a mis-seeded membership.
	cB := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: dual, Role: "authenticated", TenantID: tenantB})
	if _, err := store.CreateRole(cB, "Engagement Partner", ""); err != nil {
		t.Fatalf("control: CreateRole into B as B's admin: %v — the refusals above are vacuous unless this succeeds", err)
	}
	if n := rowCount(t, super, "workflow_roles", tenantA); n != 0 {
		t.Errorf("tenant A rows after B's create = %d, want 0 — the row must land in B", n)
	}
	if n := rowCount(t, super, "workflow_roles", tenantB); n != 1 {
		t.Errorf("tenant B workflow_roles rows = %d, want 1", n)
	}
}

// TestWorkflowRole_CreateAuditsInSameTx proves atomicity positively: two rows
// sharing an xmin were inserted by one transaction. The AC's rollback form is not
// reachable here (no external lever fails AFTER the audit — an over-long actor dies
// at the memberships uuid cast, a NUL byte dies in `title text`), and it would pass
// vacuously against a two-transaction store, since any failure raised before the
// audit statement also leaves "neither row" behind. This form fails a
// two-transaction store outright.
func TestWorkflowRole_CreateAuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 audit-atomicity")
	c, adminID := activeAdmin(t, super, tenantID)

	created, err := NewStore(app).CreateRole(c, "Engagement Partner", "First sign-off")
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	var roleXmin, auditXmin, actor, auditTitle string
	if err := super.QueryRow(context.Background(),
		`SELECT r.xmin::text, a.xmin::text, a.actor, a.payload->>'title'
		   FROM workflow_roles r, audit_log a
		  WHERE r.tenant_id = $1 AND r.key = $2
		    AND a.tenant_id = $1 AND a.event = 'workflow_role.created' AND a.payload->>'key' = $2`,
		tenantID, created.Key).Scan(&roleXmin, &auditXmin, &actor, &auditTitle); err != nil {
		t.Fatalf("xmin join (no row means the role and its audit event do not both exist): %v", err)
	}
	// Frozen or invalid xids read as 2 and 0; either would make the comparison meaningless.
	for label, x := range map[string]string{"workflow_roles": roleXmin, "audit_log": auditXmin} {
		if x == "0" || x == "2" {
			t.Fatalf("%s.xmin = %s — a frozen/invalid xid makes this proof vacuous", label, x)
		}
	}
	if roleXmin != auditXmin {
		t.Errorf("xmin: workflow_roles = %s, audit_log = %s — the audit must be written on the same tx as the INSERT", roleXmin, auditXmin)
	}
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}
	if auditTitle != "Engagement Partner" {
		t.Errorf("audit payload title = %q, want Engagement Partner", auditTitle)
	}
}

// TestWorkflowRole_UniqueViolationOnlyMapsTheKeyConstraint: the 409 is discriminated
// by constraint NAME. A SQLSTATE-only check passes both provocations below, which is
// exactly what the second assertion catches — a gen_random_uuid collision on
// workflow_roles_tenant_id_id_uq, or any other 23505, must still surface as a 500.
func TestWorkflowRole_UniqueViolationOnlyMapsTheKeyConstraint(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-02 constraint-name")
	_, adminID := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	keyErr := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, 'tax-reviewer', 'Dup')`, tenantID)
		return err
	})
	memberErr := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		for ord := range 2 {
			if _, err := tx.Exec(ctx,
				`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, $4)`,
				tenantID, roleID, adminID, ord); err != nil {
				return err
			}
		}
		return nil
	})

	// Both provocations must be 23505, or the discrimination below proves nothing.
	for label, err := range map[string]error{"key": keyErr, "member": memberErr} {
		if code := pgCode(err); code != "23505" {
			t.Fatalf("%s provocation: err = %v (SQLSTATE %q), want 23505", label, err, code)
		}
	}
	if !uniqueViolationOn(keyErr, "workflow_roles_tenant_key_uq") {
		t.Errorf("uniqueViolationOn(keyErr, workflow_roles_tenant_key_uq) = false, want true")
	}
	if uniqueViolationOn(memberErr, "workflow_roles_tenant_key_uq") {
		t.Errorf("uniqueViolationOn(memberErr, workflow_roles_tenant_key_uq) = true — a 23505 on another constraint must not map to ErrConflict")
	}
}

// TestWorkflowRole_ConcurrentCreateEitherSucceedsOrConflicts: the key query takes no
// lock, so two same-title creates can both mint the same key. The loser must surface
// ErrConflict, never a raw 23505; the store does not retry, because a retry would
// hand back a key the client's title does not imply. Tolerant of serialisation (then
// both succeed), so the assertion is the invariant read back from the database.
func TestWorkflowRole_ConcurrentCreateEitherSucceedsOrConflicts(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	const rounds = 8
	for round := range rounds {
		tenantID := seedTenant(t, super, "APPR-02 concurrent-create")
		c, _ := activeAdmin(t, super, tenantID)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		got := make([]Role, 2)
		start := make(chan struct{})
		wg.Add(2)
		for i := range 2 {
			go func() {
				defer wg.Done()
				<-start
				got[i], errs[i] = store.CreateRole(c, "Tax Reviewer", "")
			}()
		}
		close(start)
		wg.Wait()

		won := []string{}
		for i, err := range errs {
			switch {
			case err == nil:
				won = append(won, got[i].Key)
			case errors.Is(err, ErrConflict):
			default:
				t.Fatalf("round %d: errs[%d] = %v (SQLSTATE %q), want nil or ErrConflict", round, i, err, pgCode(err))
			}
		}
		if len(won) == 0 {
			t.Fatalf("round %d: both calls failed (%v); at least one must commit", round, errs)
		}
		sort.Strings(won)
		if committed := liveRoleKeys(t, super, tenantID); !reflect.DeepEqual(committed, won) {
			t.Fatalf("round %d: committed keys = %v, but the successes returned %v", round, committed, won)
		}
	}
}

// --- ListRoles -------------------------------------------------------------

// TestWorkflowRole_ListReturnsMembersInOrdOrder: members come back in the role's own
// `ord` order. The staffing below is deliberately out of both insertion order and
// user_id order, so a missing or swapped ORDER BY is caught.
func TestWorkflowRole_ListReturnsMembersInOrdOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 ord-order")

	users := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	sort.Strings(users) // insertion order below == user_id order, so neither can pass for `ord`
	for _, u := range users {
		seedMembership(t, super, tenantID, u, "preparer", "active")
	}
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	for i, ord := range []int{2, 0, 1} {
		staffWorkflowRole(t, super, tenantID, roleID, users[i], ord)
	}
	want := []string{users[1], users[2], users[0]}
	if reflect.DeepEqual(want, users) {
		t.Fatal("the expected order equals insertion order; this test would pass without any ORDER BY")
	}

	c, _ := activeAdmin(t, super, tenantID)
	roles, err := NewStore(app).ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("ListRoles returned %d roles, want 1", len(roles))
	}
	if !reflect.DeepEqual(roles[0].Members, want) {
		t.Errorf("members = %v, want %v (ord 0,1,2)", roles[0].Members, want)
	}
}

// TestWorkflowRole_ListIssuesTwoQueriesRegardlessOfRoleCount: staffing is fetched
// for ALL roles in one statement, so the count does not grow with the role count.
// A per-role members query returns identical results, so only the statement count
// discriminates it — and this is a hot settings read.
func TestWorkflowRole_ListIssuesTwoQueriesRegardlessOfRoleCount(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 query-count")
	c, _ := activeAdmin(t, super, tenantID)
	app, rec := tracedAppPool(t)
	store := NewStore(app)

	user := uuid.NewString()
	seedMembership(t, super, tenantID, user, "preparer", "active")

	seeded := 0
	for _, total := range []int{1, 5} {
		for ; seeded < total; seeded++ {
			key := fmt.Sprintf("role-%d", seeded)
			staffWorkflowRole(t, super, tenantID, seedWorkflowRole(t, super, tenantID, key, key), user, 0)
		}
		rec.reset()
		roles, err := store.ListRoles(c)
		if err != nil {
			t.Fatalf("ListRoles with %d roles: %v", total, err)
		}
		if len(roles) != total {
			t.Fatalf("ListRoles returned %d roles, want %d", len(roles), total)
		}
		for _, r := range roles {
			if !reflect.DeepEqual(r.Members, []string{user}) {
				t.Errorf("with %d roles, %s members = %v, want [%s]", total, r.Key, r.Members, user)
			}
		}
		if got := rec.mentioning("workflow_role"); len(got) != 2 {
			t.Errorf("with %d roles ListRoles issued %d workflow_role statements, want 2 (the roles, then all staffing at once): %v",
				total, len(got), got)
		}
	}
}

// TestWorkflowRole_ListExcludesSoftDeleted: L1 carries WHERE deleted_at IS NULL —
// the opposite filter from the key query. Dropping it returns the deleted role;
// mis-grouping the single members query attributes its staffing to the survivor.
func TestWorkflowRole_ListExcludesSoftDeleted(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 list-excludes-deleted")

	liveUser, deadUser := uuid.NewString(), uuid.NewString()
	seedMembership(t, super, tenantID, liveUser, "preparer", "active")
	seedMembership(t, super, tenantID, deadUser, "preparer", "active")
	liveID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	deadID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	staffWorkflowRole(t, super, tenantID, liveID, liveUser, 0)
	staffWorkflowRole(t, super, tenantID, deadID, deadUser, 0)
	softDeleteWorkflowRole(t, super, deadID)

	c, _ := activeAdmin(t, super, tenantID)
	roles, err := NewStore(app).ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if got := keysOf(roles); !reflect.DeepEqual(got, []string{"tax-reviewer"}) {
		t.Fatalf("keys = %v, want only the live role", got)
	}
	if !reflect.DeepEqual(roles[0].Members, []string{liveUser}) {
		t.Errorf("members = %v, want only %s — the deleted role's staffing must not be grouped onto the survivor", roles[0].Members, liveUser)
	}
}

// TestWorkflowRole_ListEmptyTenantReturnsEmptySlice: never nil, nil — the SPA renders
// the result directly and `null` would break it.
func TestWorkflowRole_ListEmptyTenantReturnsEmptySlice(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 empty-tenant")
	c, _ := activeAdmin(t, super, tenantID)

	roles, err := NewStore(app).ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if roles == nil {
		t.Error("ListRoles returned a nil slice for an empty tenant; want []Role{}")
	}
	if len(roles) != 0 {
		t.Errorf("ListRoles returned %d roles, want 0", len(roles))
	}
	if raw, err := json.Marshal(roles); err != nil {
		t.Fatalf("marshal: %v", err)
	} else if string(raw) != "[]" {
		t.Errorf("wire = %s, want []", raw)
	}
}

// TestWorkflowRole_ListRoleWithNoMembersIsEmptyNotNil asserts on raw bytes: decoding
// turns `[]` and `null` into the same nil slice, so only the wire discriminates.
func TestWorkflowRole_ListRoleWithNoMembersIsEmptyNotNil(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 unstaffed-role")
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	c, _ := activeAdmin(t, super, tenantID)

	roles, err := NewStore(app).ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("ListRoles returned %d roles, want 1", len(roles))
	}
	if roles[0].Members == nil {
		t.Error("members is nil for an unstaffed role; the producer must construct []string{}")
	}
	if len(roles[0].Members) != 0 {
		t.Errorf("members = %v, want empty", roles[0].Members)
	}
	raw, err := json.Marshal(roles[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"members":[]`) {
		t.Errorf("wire = %s, want it to carry \"members\":[]", raw)
	}
}

// TestWorkflowRole_ListIsTenantScoped: no statement carries a tenant_id predicate;
// RLS is the filter. Forced by two seeded tenants, both holding roles and staffing.
func TestWorkflowRole_ListIsTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "APPR-02 scope A")
	tenantB := seedTenant(t, super, "APPR-02 scope B")

	userB := uuid.NewString()
	seedMembership(t, super, tenantB, userB, "preparer", "active")
	roleB := seedWorkflowRole(t, super, tenantB, "b-only", "B Only")
	staffWorkflowRole(t, super, tenantB, roleB, userB, 0)

	userA := uuid.NewString()
	seedMembership(t, super, tenantA, userA, "preparer", "active")
	roleA := seedWorkflowRole(t, super, tenantA, "a-only", "A Only")
	staffWorkflowRole(t, super, tenantA, roleA, userA, 0)

	c, _ := activeAdmin(t, super, tenantA)
	roles, err := NewStore(app).ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if got := keysOf(roles); !reflect.DeepEqual(got, []string{"a-only"}) {
		t.Fatalf("keys = %v, want only tenant A's", got)
	}
	for _, m := range roles[0].Members {
		if m == userB {
			t.Errorf("members = %v, leaked tenant B's user %s", roles[0].Members, userB)
		}
	}
}

// TestWorkflowRole_ListOrderedByCreatedAtThenKey: ordered by created_at then key.
// The later role's key sorts first alphabetically, so a key-only ORDER BY is caught;
// the two roles sharing a created_at have their tie broken by key, and were inserted
// in the opposite order.
func TestWorkflowRole_ListOrderedByCreatedAtThenKey(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 list-order")

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	nRole := seedWorkflowRole(t, super, tenantID, "n-role", "N")
	mRole := seedWorkflowRole(t, super, tenantID, "m-role", "M")
	aRole := seedWorkflowRole(t, super, tenantID, "a-role", "A")
	stampCreatedAt(t, super, nRole, base)
	stampCreatedAt(t, super, mRole, base)
	stampCreatedAt(t, super, aRole, base.Add(time.Hour))

	c, _ := activeAdmin(t, super, tenantID)
	roles, err := NewStore(app).ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if got, want := keysOf(roles), []string{"m-role", "n-role", "a-role"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v (created_at, then key)", got, want)
	}
}

// TestWorkflowRole_ListNeedsNoAdminRole: ListRoles applies no access-role gate — the
// Roles screen is readable by anyone who can see the workspace.
func TestWorkflowRole_ListNeedsNoAdminRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 list-any-member")
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	c, _ := callerCtx(t, super, tenantID, "preparer", "active")

	roles, err := NewStore(app).ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles as a preparer: %v, want success", err)
	}
	if len(roles) != 2 {
		t.Errorf("ListRoles returned %d roles, want 2", len(roles))
	}
}

// TestWorkflowRole_ListRequiresNoMembershipRow pins observed behaviour, not a
// decision: ListRoles reads no memberships row, so a valid tenant claim with no
// membership can list — the same as the shipped GET /v1/memberships. Here so a
// silent "hardening" has to argue with a failing test first.
func TestWorkflowRole_ListRequiresNoMembershipRow(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 list-no-membership")
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	roles, err := NewStore(app).ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles with no membership row: %v, want the roles — parity with GET /v1/memberships", err)
	}
	if got := keysOf(roles); !reflect.DeepEqual(got, []string{"tax-reviewer"}) {
		t.Errorf("keys = %v, want [tax-reviewer]", got)
	}
}

// --- UpdateRole ------------------------------------------------------------

// TestWorkflowRole_RenameKeepsKey: the key is never re-derived on rename. The seeded
// key is one the slugifier would produce from neither title, so the assertion cannot
// pass by coincidence, and id/created_at pin that this is an UPDATE of the same row
// rather than a delete-and-recreate.
func TestWorkflowRole_RenameKeepsKey(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 rename-keeps-key")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "fin_mgr", "Engagement Manager")
	seedRoleDesc(t, super, roleID, "signs off first")
	before := roleRow(t, super, roleID)

	const newTitle = "Chief Engagement Officer"
	if minted := newRoleKey(nil, newTitle); minted == before.Key {
		t.Fatalf("newRoleKey(%q) = %q, the seeded key — a re-deriving store would pass this test", newTitle, minted)
	}

	got, err := NewStore(app).UpdateRole(c, "fin_mgr", ptr(newTitle), nil)
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	want := Role{Key: "fin_mgr", Title: newTitle, Desc: "signs off first", Members: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UpdateRole = %+v, want %+v", got, want)
	}

	after := roleRow(t, super, roleID)
	if after.ID != before.ID || after.Key != before.Key || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("stored row = %v, want id/key/created_at from %v", after, before)
	}
	if after.Title != newTitle || after.Desc != before.Desc {
		t.Errorf("stored (title, description) = (%q, %q), want (%q, %q)", after.Title, after.Desc, newTitle, before.Desc)
	}
	if after.DeletedAt != nil {
		t.Errorf("stored deleted_at = %v, want NULL — a rename does not delete", after.DeletedAt)
	}
}

// TestWorkflowRole_UpdateRejectsNoFieldsAndEmptyTitle: both fields omitted is
// ErrValidation, and so is an empty-after-trim title. The trailing control renames
// successfully, so the zero-write assertions discriminate validation from a store
// that refuses every call.
func TestWorkflowRole_UpdateRejectsNoFieldsAndEmptyTitle(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 update-validation")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedRoleDesc(t, super, roleID, "a blurb")
	before := roleRow(t, super, roleID)

	for _, tc := range []struct {
		name        string
		title, desc *string
	}{
		{"both omitted", nil, nil},
		{"empty title", ptr(""), nil},
		{"blank title", ptr("   "), nil},
		{"whitespace-only title", ptr("\t\n"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.UpdateRole(c, "tax-reviewer", tc.title, tc.desc); !errors.Is(err, ErrValidation) {
				t.Errorf("UpdateRole(%s) err = %v, want ErrValidation", tc.name, err)
			}
		})
	}
	if after := roleRow(t, super, roleID); !after.equal(before) {
		t.Errorf("stored row = %v, want it byte-identical to %v — a rejected update must write nothing", after, before)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.updated"); n != 0 {
		t.Errorf("workflow_role.updated audit rows = %d, want 0", n)
	}

	if _, err := store.UpdateRole(c, "tax-reviewer", ptr("Quality Reviewer"), nil); err != nil {
		t.Fatalf("control: UpdateRole with a storable title: %v — the no-write assertions above are vacuous unless this succeeds", err)
	}
	if after := roleRow(t, super, roleID); after.Title != "Quality Reviewer" {
		t.Errorf("control: stored title = %q, want Quality Reviewer", after.Title)
	}
}

// TestWorkflowRole_UpdateDescOnlyLeavesTitle: a desc-only PATCH leaves title and key
// alone, and clearing the blurb is a real edit — coalesce($n, col) must see an empty
// string, not treat it as "omitted".
func TestWorkflowRole_UpdateDescOnlyLeavesTitle(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 update-desc-only")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedRoleDesc(t, super, roleID, "old blurb")

	for _, want := range []string{"new blurb", ""} {
		got, err := store.UpdateRole(c, "tax-reviewer", nil, ptr(want))
		if err != nil {
			t.Fatalf("UpdateRole(desc=%q): %v", want, err)
		}
		if got.Desc != want {
			t.Errorf("returned desc = %q, want %q", got.Desc, want)
		}
		if got.Key != "tax-reviewer" || got.Title != "Tax Reviewer" {
			t.Errorf("returned (key, title) = (%q, %q), want (tax-reviewer, Tax Reviewer)", got.Key, got.Title)
		}
		after := roleRow(t, super, roleID)
		if after.Desc != want {
			t.Errorf("stored description = %q, want %q", after.Desc, want)
		}
		if after.Key != "tax-reviewer" || after.Title != "Tax Reviewer" {
			t.Errorf("stored (key, title) = (%q, %q), want (tax-reviewer, Tax Reviewer)", after.Key, after.Title)
		}
	}
}

// TestWorkflowRole_UpdateTrimsWhatItStores: the shipped modal trims both fields
// (RoleModal.tsx:82-83), the same as CreateRole.
func TestWorkflowRole_UpdateTrimsWhatItStores(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 update-trim")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	got, err := NewStore(app).UpdateRole(c, "tax-reviewer",
		ptr("  Chief Engagement Officer  "), ptr("  blurb  "))
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	want := Role{Key: "tax-reviewer", Title: "Chief Engagement Officer", Desc: "blurb", Members: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UpdateRole = %+v, want %+v", got, want)
	}
	if after := roleRow(t, super, roleID); after.Title != "Chief Engagement Officer" || after.Desc != "blurb" {
		t.Errorf("stored (title, description) = (%q, %q), want (Chief Engagement Officer, blurb)", after.Title, after.Desc)
	}
}

// TestWorkflowRole_UpdateReturnsTheRolesMembers: PATCH answers with a full Role, so
// the SPA can replace its card outright. Staffing is seeded out of both insertion and
// user_id order, so a missing ORDER BY ord is caught, and the wire is asserted on raw
// bytes because decoding collapses [] and null.
func TestWorkflowRole_UpdateReturnsTheRolesMembers(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 update-members")
	c, _ := activeAdmin(t, super, tenantID)

	users := []string{uuid.NewString(), uuid.NewString()}
	sort.Strings(users)
	for _, u := range users {
		seedMembership(t, super, tenantID, u, "preparer", "active")
	}
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	staffWorkflowRole(t, super, tenantID, roleID, users[0], 1)
	staffWorkflowRole(t, super, tenantID, roleID, users[1], 0)
	want := []string{users[1], users[0]}

	got, err := NewStore(app).UpdateRole(c, "tax-reviewer", ptr("Renamed"), nil)
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if !reflect.DeepEqual(got.Members, want) {
		t.Errorf("members = %v, want %v (ord 0,1)", got.Members, want)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantMembers, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if !strings.Contains(string(raw), `"members":`+string(wantMembers)) {
		t.Errorf("wire = %s, want it to carry \"members\":%s", raw, wantMembers)
	}
}

// TestWorkflowRole_UpdateReturnsOnlyItsOwnMembers: the staffing read is scoped to the
// renamed role's id. A second staffed role in the same tenant is what makes that
// scoping observable — RLS alone would let the whole tenant's staffing through.
func TestWorkflowRole_UpdateReturnsOnlyItsOwnMembers(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 update-members-scope")
	c, _ := activeAdmin(t, super, tenantID)

	mine, theirs := uuid.NewString(), uuid.NewString()
	seedMembership(t, super, tenantID, mine, "preparer", "active")
	seedMembership(t, super, tenantID, theirs, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	otherID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	staffWorkflowRole(t, super, tenantID, roleID, mine, 0)
	staffWorkflowRole(t, super, tenantID, otherID, theirs, 0)

	got, err := NewStore(app).UpdateRole(c, "tax-reviewer", ptr("Renamed"), nil)
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if !reflect.DeepEqual(got.Members, []string{mine}) {
		t.Errorf("members = %v, want only %s — the other role's staffing must not be returned", got.Members, mine)
	}
}

// TestWorkflowRole_UpdateValidatedBeforeTheCallerRoleIsRead pins the guard order: a
// non-admin sending a blank title gets ErrValidation (400), not ErrNotPermitted (403).
// The check reads no row and depends only on the caller's own argument, so answering
// it first reveals nothing. The second call proves this is about ORDER, not a store
// that refuses everything.
func TestWorkflowRole_UpdateValidatedBeforeTheCallerRoleIsRead(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 update-guard-order")
	store := NewStore(app)
	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	before := roleRow(t, super, roleID)

	if _, err := store.UpdateRole(c, "tax-reviewer", ptr("   "), nil); !errors.Is(err, ErrValidation) {
		t.Errorf("non-admin with a blank title: err = %v, want ErrValidation — the title is checked first", err)
	}
	if _, err := store.UpdateRole(c, "tax-reviewer", ptr("New Title"), nil); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("non-admin with a valid title: err = %v, want ErrNotPermitted", err)
	}
	if after := roleRow(t, super, roleID); !after.equal(before) {
		t.Errorf("stored row = %v, want %v — neither refusal may write", after, before)
	}
}

// TestWorkflowRole_UpdateAndDeletePermissionCheckedBeforeRowRead: the caller-role read
// is the first statement and takes no target argument, so a non-admin is refused
// identically whether the key exists or not — no 403-vs-404 existence oracle
// (the house rule at internal/tenancy/store.go:96-118). The admin control proves 403
// is not simply the only error either method can reach.
//
// All four target classes a key can fall into are probed, and the refusal is compared as
// the status+message the SPA actually sees, the bar TestTransmitGate_NoExistenceOracle
// sets (internal/invoice/transmission_rbac_test.go:299-347).
func TestWorkflowRole_UpdateAndDeletePermissionCheckedBeforeRowRead(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 no-existence-oracle")
	otherTenant := seedTenant(t, super, "APPR-02 no-existence-oracle other")
	store := NewStore(app)
	preparer, _ := callerCtx(t, super, tenantID, "preparer", "active")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	before := roleRow(t, super, roleID)
	deadID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	softDeleteWorkflowRole(t, super, deadID)
	seedWorkflowRole(t, super, otherTenant, "other-tenants-role", "Other Tenant's Role")

	type refusal struct {
		status int
		msg    string
	}
	seen := map[string][]refusal{}
	for _, tc := range []struct{ class, key string }{
		{"live", "tax-reviewer"},
		{"soft-deleted", "engagement-partner"},
		{"another tenant's", "other-tenants-role"},
		{"garbage", "no-such-role"},
	} {
		t.Run("UpdateRole/"+tc.class, func(t *testing.T) {
			_, err := store.UpdateRole(preparer, tc.key, ptr("New Title"), nil)
			if !errors.Is(err, ErrNotPermitted) {
				t.Errorf("UpdateRole(%s) as a preparer: err = %v, want ErrNotPermitted", tc.class, err)
			}
			status, msg := statusForErr(err)
			seen["UpdateRole"] = append(seen["UpdateRole"], refusal{status, msg})
		})
		t.Run("DeleteRole/"+tc.class, func(t *testing.T) {
			_, err := store.DeleteRole(preparer, tc.key)
			if !errors.Is(err, ErrNotPermitted) {
				t.Errorf("DeleteRole(%s) as a preparer: err = %v, want ErrNotPermitted", tc.class, err)
			}
			status, msg := statusForErr(err)
			seen["DeleteRole"] = append(seen["DeleteRole"], refusal{status, msg})
		})
	}
	for method, got := range seen {
		if len(got) != 4 {
			t.Fatalf("%s: probed %d target classes, want 4 — a short table would pass vacuously", method, len(got))
		}
		for i, r := range got[1:] {
			if r != got[0] {
				t.Errorf("%s: class %d answered %d %q but class 0 answered %d %q — the refusals must be indistinguishable",
					method, i+1, r.status, r.msg, got[0].status, got[0].msg)
			}
		}
	}
	if after := roleRow(t, super, roleID); !after.equal(before) {
		t.Errorf("stored row = %v, want %v — a refused call may not write", after, before)
	}

	admin, _ := activeAdmin(t, super, tenantID)
	if _, err := store.UpdateRole(admin, "no-such-role", ptr("New Title"), nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("control: UpdateRole(no-such-role) as an admin: err = %v, want ErrNotFound", err)
	}
	if _, err := store.DeleteRole(admin, "no-such-role"); !errors.Is(err, ErrNotFound) {
		t.Errorf("control: DeleteRole(no-such-role) as an admin: err = %v, want ErrNotFound", err)
	}
}

// TestWorkflowRole_UpdateAndDeleteRequireActiveAdmin: the CALLER axis, copied from
// CreateRole's. The caller-role read carries AND status = 'active', so a suspended or
// invited admin is refused as firmly as a preparer, and a caller with no membership
// row at all is refused too.
func TestWorkflowRole_UpdateAndDeleteRequireActiveAdmin(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 write-caller-axis")
	store := NewStore(app)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	before := roleRow(t, super, roleID)

	refused := map[string]context.Context{}
	for _, caller := range []struct{ name, role, status string }{
		{"active preparer", "preparer", "active"},
		{"active reviewer", "reviewer", "active"},
		{"suspended admin", "admin", "suspended"},
		{"invited admin", "admin", "invited"},
	} {
		c, _ := callerCtx(t, super, tenantID, caller.role, caller.status)
		refused[caller.name] = c
	}
	refused["no membership row"] = auth.WithIdentity(context.Background(),
		auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	if len(refused) != 5 {
		t.Fatalf("built %d callers, want 5 — a short table would pass vacuously", len(refused))
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			if _, err := store.UpdateRole(c, "tax-reviewer", ptr("New Title"), nil); !errors.Is(err, ErrNotPermitted) {
				t.Errorf("UpdateRole as %s: err = %v, want ErrNotPermitted", name, err)
			}
			if _, err := store.DeleteRole(c, "tax-reviewer"); !errors.Is(err, ErrNotPermitted) {
				t.Errorf("DeleteRole as %s: err = %v, want ErrNotPermitted", name, err)
			}
		})
	}
	if after := roleRow(t, super, roleID); !after.equal(before) {
		t.Errorf("stored row = %v, want %v", after, before)
	}
	for _, event := range []string{"workflow_role.updated", "workflow_role.deleted"} {
		if n := auditCount(t, super, tenantID, event); n != 0 {
			t.Errorf("%s audit rows = %d, want 0", event, n)
		}
	}

	// Controls, in order: the refusals above are vacuous unless an active admin can do
	// both things to this very row.
	admin, _ := activeAdmin(t, super, tenantID)
	if _, err := store.UpdateRole(admin, "tax-reviewer", ptr("New Title"), nil); err != nil {
		t.Fatalf("control: UpdateRole as an active admin: %v", err)
	}
	if _, err := store.DeleteRole(admin, "tax-reviewer"); err != nil {
		t.Fatalf("control: DeleteRole as an active admin: %v", err)
	}
}

// TestWorkflowRole_UpdateAndDeleteAreTenantScoped: no statement carries a tenant_id
// predicate, so RLS is the only thing keeping a by-key write inside the caller's tenant.
// Both tenants hold the same key, which is what makes a mis-scoped write observable — an
// out-of-tenant key that simply does not exist would 404 either way.
func TestWorkflowRole_UpdateAndDeleteAreTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := seedTenant(t, super, "APPR-02 write-scope A")
	tenantB := seedTenant(t, super, "APPR-02 write-scope B")
	store := NewStore(app)

	sharedA := seedWorkflowRole(t, super, tenantA, "shared", "A Shared")
	sharedB := seedWorkflowRole(t, super, tenantB, "shared", "B Shared")
	seedRoleDesc(t, super, sharedB, "B's blurb")
	bOnly := seedWorkflowRole(t, super, tenantB, "b-only", "B Only")
	beforeB, beforeBOnly := roleRow(t, super, sharedB), roleRow(t, super, bOnly)

	admin, _ := activeAdmin(t, super, tenantA)
	preparer, _ := callerCtx(t, super, tenantA, "preparer", "active")

	// A key only B holds is simply absent for A's admin — ErrNotFound, never B's row.
	if _, err := store.UpdateRole(admin, "b-only", ptr("Renamed by A"), nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateRole on another tenant's key: err = %v, want ErrNotFound", err)
	}
	if _, err := store.DeleteRole(admin, "b-only"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteRole on another tenant's key: err = %v, want ErrNotFound", err)
	}
	// And A's non-admin gets its usual refusal, so the caller gate leaks nothing either.
	if _, err := store.UpdateRole(preparer, "b-only", ptr("Renamed by A"), nil); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("UpdateRole on another tenant's key as a preparer: err = %v, want ErrNotPermitted", err)
	}
	if _, err := store.DeleteRole(preparer, "b-only"); !errors.Is(err, ErrNotPermitted) {
		t.Errorf("DeleteRole on another tenant's key as a preparer: err = %v, want ErrNotPermitted", err)
	}

	// The shared key resolves to A's row on both paths.
	if got, err := store.UpdateRole(admin, "shared", ptr("A Renamed"), nil); err != nil {
		t.Fatalf("UpdateRole on the shared key: %v", err)
	} else if got.Title != "A Renamed" {
		t.Errorf("UpdateRole title = %q, want A Renamed", got.Title)
	}
	if after := roleRow(t, super, sharedA); after.Title != "A Renamed" {
		t.Errorf("tenant A's row title = %q, want A Renamed", after.Title)
	}
	if _, err := store.DeleteRole(admin, "shared"); err != nil {
		t.Fatalf("DeleteRole on the shared key: %v", err)
	}
	if after := roleRow(t, super, sharedA); after.DeletedAt == nil {
		t.Error("tenant A's row deleted_at IS NULL after DeleteRole")
	}

	for _, b := range []struct {
		label  string
		id     string
		before storedRole
	}{{"shared", sharedB, beforeB}, {"b-only", bOnly, beforeBOnly}} {
		if after := roleRow(t, super, b.id); !after.equal(b.before) {
			t.Errorf("tenant B's %s row = %v, want %v — no write may cross tenants", b.label, after, b.before)
		}
	}
	for _, event := range []string{"workflow_role.updated", "workflow_role.deleted"} {
		if n := auditCount(t, super, tenantB, event); n != 0 {
			t.Errorf("%s audit rows in tenant B = %d, want 0", event, n)
		}
		if n := auditCount(t, super, tenantA, event); n != 1 {
			t.Errorf("%s audit rows in tenant A = %d, want 1", event, n)
		}
	}
}

// TestWorkflowRole_RenameToAnExistingTitleIsLegal: duplicate titles are legal (only
// the key is unique), and `key` is absent from the SET list, so a rename can never
// reach workflow_roles_tenant_key_uq — UpdateRole has no ErrConflict path at all.
func TestWorkflowRole_RenameToAnExistingTitleIsLegal(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 rename-duplicate-title")
	c, _ := activeAdmin(t, super, tenantID)
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	partnerID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")

	got, err := NewStore(app).UpdateRole(c, "engagement-partner", ptr("Tax Reviewer"), nil)
	if err != nil {
		t.Fatalf("UpdateRole to a title another role already holds: %v, want success", err)
	}
	if got.Key != "engagement-partner" || got.Title != "Tax Reviewer" {
		t.Errorf("UpdateRole = (%q, %q), want (engagement-partner, Tax Reviewer)", got.Key, got.Title)
	}
	if after := roleRow(t, super, partnerID); after.Key != "engagement-partner" || after.Title != "Tax Reviewer" {
		t.Errorf("stored (key, title) = (%q, %q), want (engagement-partner, Tax Reviewer)", after.Key, after.Title)
	}
	if keys := liveRoleKeys(t, super, tenantID); !reflect.DeepEqual(keys, []string{"engagement-partner", "tax-reviewer"}) {
		t.Errorf("live keys = %v, want both rows live under their original keys", keys)
	}
}

// TestWorkflowRole_UpdateAuditsInSameTx proves atomicity positively: two rows sharing
// an xmin were written by one transaction (an UPDATE creates a new row version, so the
// role's xmin is the updating xid). The AC's rollback form is unreachable — no external
// lever fails after audit.Record — and would pass vacuously against a two-transaction
// store; see TestWorkflowRole_CreateAuditsInSameTx.
//
// The payload is compared as a whole map: both field pairs are named, and a widened or
// renamed key is a wire change the log's only reader would have to be taught.
func TestWorkflowRole_UpdateAuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 update-audit-atomicity")
	c, adminID := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedRoleDesc(t, super, roleID, "old blurb")

	if _, err := NewStore(app).UpdateRole(c, "tax-reviewer", ptr("Quality Reviewer"), ptr("new blurb")); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}

	var roleXmin, auditXmin, actor, payloadJSON string
	if err := super.QueryRow(context.Background(),
		`SELECT r.xmin::text, a.xmin::text, a.actor, a.payload::text
		   FROM workflow_roles r, audit_log a
		  WHERE r.id = $1
		    AND a.tenant_id = $2 AND a.event = 'workflow_role.updated' AND a.payload->>'key' = 'tax-reviewer'`,
		roleID, tenantID).Scan(&roleXmin, &auditXmin, &actor, &payloadJSON); err != nil {
		t.Fatalf("xmin join (no row means the rename and its audit event do not both exist): %v", err)
	}
	// Frozen or invalid xids read as 2 and 0; either would make the comparison meaningless.
	for label, x := range map[string]string{"workflow_roles": roleXmin, "audit_log": auditXmin} {
		if x == "0" || x == "2" {
			t.Fatalf("%s.xmin = %s — a frozen/invalid xid makes this proof vacuous", label, x)
		}
	}
	if roleXmin != auditXmin {
		t.Errorf("xmin: workflow_roles = %s, audit_log = %s — the audit must be written on the same tx as the UPDATE", roleXmin, auditXmin)
	}
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload %s: %v", payloadJSON, err)
	}
	want := map[string]any{
		"key":        "tax-reviewer",
		"from_title": "Tax Reviewer",
		"to_title":   "Quality Reviewer",
		"from_desc":  "old blurb",
		"to_desc":    "new blurb",
	}
	if !reflect.DeepEqual(payload, want) {
		t.Errorf("audit payload = %v, want %v", payload, want)
	}
}

// TestWorkflowRole_UpdateAuditsAnEditThatChangesNothing: an edit resolving to the stored
// values is still audited, with from == to on both pairs. The modal gates Save on a
// non-empty name and nothing else (canSaveRole, roles.ts:386-388), so this is a routine
// request, and it follows portfolio.Update, which logs the fields SENT
// (portfolio/store.go:215-218). tenancy's 200-no-op-without-audit
// (tenancy/store.go:183-186) does not transfer: that guard exists to stop a redundant
// transition tripping the last-active-admin 409, and this method has no such guard.
func TestWorkflowRole_UpdateAuditsAnEditThatChangesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 update-no-change")
	c, _ := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedRoleDesc(t, super, roleID, "a blurb")
	before := roleRow(t, super, roleID)

	got, err := NewStore(app).UpdateRole(c, "tax-reviewer", ptr("Tax Reviewer"), ptr("a blurb"))
	if err != nil {
		t.Fatalf("UpdateRole with the stored values: %v, want success", err)
	}
	want := Role{Key: "tax-reviewer", Title: "Tax Reviewer", Desc: "a blurb", Members: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UpdateRole = %+v, want %+v", got, want)
	}
	if after := roleRow(t, super, roleID); !after.equal(before) {
		t.Errorf("stored row = %v, want %v", after, before)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.updated"); n != 1 {
		t.Errorf("workflow_role.updated rows = %d, want 1 — a no-change edit is audited, not dropped", n)
	}

	var payloadJSON string
	if err := super.QueryRow(context.Background(),
		`SELECT payload::text FROM audit_log
		  WHERE tenant_id = $1 AND event = 'workflow_role.updated'`, tenantID).Scan(&payloadJSON); err != nil {
		t.Fatalf("read audit payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload %s: %v", payloadJSON, err)
	}
	wantPayload := map[string]any{
		"key":        "tax-reviewer",
		"from_title": "Tax Reviewer",
		"to_title":   "Tax Reviewer",
		"from_desc":  "a blurb",
		"to_desc":    "a blurb",
	}
	if !reflect.DeepEqual(payload, wantPayload) {
		t.Errorf("audit payload = %v, want %v — from/to is what lets a reader tell a no-op from a rename", payload, wantPayload)
	}
}

// TestWorkflowRole_ConcurrentRenamesChainInTheAudit: from_title/from_desc are read in
// Go and then written as audit fact, so the pre-image read must be locked. Without
// FOR UPDATE both renames read the seeded title and the log claims the same value was
// replaced twice — a lost update in the only record of what a role used to be called.
// Several rounds because a lockless store also passes whenever the two transactions
// happen not to overlap.
func TestWorkflowRole_ConcurrentRenamesChainInTheAudit(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	const rounds = 8
	for round := range rounds {
		tenantID := seedTenant(t, super, "APPR-02 concurrent-rename")
		c, _ := activeAdmin(t, super, tenantID)
		roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "A")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		for i, title := range []string{"B", "C"} {
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = store.UpdateRole(c, "tax-reviewer", ptr(title), nil)
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: errs[%d] = %v — two disjoint renames must both commit", round, i, err)
			}
		}

		rows, err := super.Query(context.Background(),
			`SELECT payload->>'from_title', payload->>'to_title'
			   FROM audit_log
			  WHERE tenant_id = $1 AND event = 'workflow_role.updated'
			  ORDER BY id`, tenantID)
		if err != nil {
			t.Fatalf("round %d: read audit rows: %v", round, err)
		}
		type edge struct{ from, to string }
		var edges []edge
		for rows.Next() {
			var e edge
			if err := rows.Scan(&e.from, &e.to); err != nil {
				rows.Close()
				t.Fatalf("round %d: scan audit row: %v", round, err)
			}
			edges = append(edges, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("round %d: read audit rows: %v", round, err)
		}
		if len(edges) != 2 {
			t.Fatalf("round %d: workflow_role.updated rows = %d, want 2", round, len(edges))
		}

		// Order-agnostic: exactly one edge starts at the seeded title, and the other
		// starts where it ended. Both reporting from_title "A" is the lockless bug.
		first, second := edges[0], edges[1]
		if second.from == "A" {
			first, second = second, first
		}
		if first.from != "A" {
			t.Fatalf("round %d: audit edges = %v, want exactly one starting at the seeded title A", round, edges)
		}
		if second.from == "A" {
			t.Fatalf("round %d: audit edges = %v, both claim from_title A — the pre-image read is unlocked", round, edges)
		}
		if second.from != first.to {
			t.Errorf("round %d: audit edges = %v, want a chain (A->x, x->y)", round, edges)
		}
		if after := roleRow(t, super, roleID); after.Title != second.to {
			t.Errorf("round %d: stored title = %q, want the last audited to_title %q", round, after.Title, second.to)
		}
	}
}

// --- DeleteRole ------------------------------------------------------------

// TestWorkflowRole_DeletedRoleIsNotListed: the delete is soft. The role leaves
// ListRoles, and a superuser read confirms the row is still there with deleted_at set.
func TestWorkflowRole_DeletedRoleIsNotListed(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 delete-unlisted")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	deadID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")

	got, err := store.DeleteRole(c, "engagement-partner")
	if err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	if got.Key != "engagement-partner" || got.Title != "Engagement Partner" {
		t.Errorf("DeleteRole = (%q, %q), want (engagement-partner, Engagement Partner)", got.Key, got.Title)
	}

	roles, err := store.ListRoles(c)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if keys := keysOf(roles); !reflect.DeepEqual(keys, []string{"tax-reviewer"}) {
		t.Errorf("keys = %v, want only the survivor", keys)
	}
	after := roleRow(t, super, deadID)
	if after.DeletedAt == nil {
		t.Error("stored deleted_at IS NULL — the role must be soft-deleted, not merely hidden")
	}
	if after.Key != "engagement-partner" {
		t.Errorf("the deleted role's key = %q, want it left at engagement-partner", after.Key)
	}
}

// TestWorkflowRole_DeleteLeavesPublishedStepsIntact: delete never refuses and nothing
// cascades on an UPDATE. deleted_at is the ONLY changed column, so the key a future
// policy step stores is still on the row; the staffing rows survive, inert; and a
// same-title create mints key-2 rather than inheriting the key.
//
// Deferred to APPR-03: that a published step naming this key then BLOCKS. There is no
// policy table until then.
func TestWorkflowRole_DeleteLeavesPublishedStepsIntact(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 delete-keeps-staffing")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	seedRoleDesc(t, super, roleID, "a blurb")
	for ord := range 2 {
		u := uuid.NewString()
		seedMembership(t, super, tenantID, u, "preparer", "active")
		staffWorkflowRole(t, super, tenantID, roleID, u, ord)
	}
	before := roleRow(t, super, roleID)

	got, err := store.DeleteRole(c, "tax-reviewer")
	if err != nil {
		t.Fatalf("DeleteRole on a staffed role: %v, want success — delete never refuses", err)
	}
	if got.Key != before.Key || got.Title != before.Title || got.Desc != before.Desc {
		t.Errorf("DeleteRole = %+v, want the deleted row's key/title/desc from %v", got, before)
	}
	if raw, err := json.Marshal(got); err != nil {
		t.Fatalf("marshal: %v", err)
	} else if !strings.Contains(string(raw), `"members":[]`) {
		t.Errorf("wire = %s, want it to carry \"members\":[] — a deleted role has no addressable holders, and null would break the SPA", raw)
	}

	after := roleRow(t, super, roleID)
	if after.DeletedAt == nil {
		t.Fatal("stored deleted_at IS NULL after DeleteRole")
	}
	untouched := after
	untouched.DeletedAt = nil
	if !untouched.equal(before) {
		t.Errorf("stored row = %v, want deleted_at to be the only change from %v", after, before)
	}
	if n := rowCount(t, super, "workflow_role_members", tenantID); n != 2 {
		t.Errorf("workflow_role_members rows = %d, want 2 — nothing cascades on an UPDATE", n)
	}

	reminted, err := store.CreateRole(c, "Tax Reviewer", "")
	if err != nil {
		t.Fatalf("CreateRole with the deleted role's title: %v", err)
	}
	if reminted.Key != "tax-reviewer-2" {
		t.Errorf("re-minted key = %q, want tax-reviewer-2 — a new role must never inherit a deleted key", reminted.Key)
	}
}

// TestWorkflowRole_DeletedRoleIsNotAddressable: deleted_at IS NULL is this resource's
// existence predicate, so a soft-deleted role is ErrNotFound on every by-key write path.
// The live-role control keeps this from passing against a store that refuses everything.
// (SetRoleMembers is APPR-02-05's.)
func TestWorkflowRole_DeletedRoleIsNotAddressable(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 deleted-unaddressable")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)

	deadID := seedWorkflowRole(t, super, tenantID, "engagement-partner", "Engagement Partner")
	softDeleteWorkflowRole(t, super, deadID)
	before := roleRow(t, super, deadID)
	seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	t.Run("UpdateRole", func(t *testing.T) {
		if _, err := store.UpdateRole(c, "engagement-partner", ptr("New Title"), nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateRole on a soft-deleted role: err = %v, want ErrNotFound", err)
		}
	})
	t.Run("DeleteRole", func(t *testing.T) {
		if _, err := store.DeleteRole(c, "engagement-partner"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteRole on a soft-deleted role: err = %v, want ErrNotFound", err)
		}
	})
	if after := roleRow(t, super, deadID); !after.equal(before) {
		t.Errorf("stored row = %v, want %v — a refused call may not write", after, before)
	}

	if _, err := store.UpdateRole(c, "tax-reviewer", ptr("New Title"), nil); err != nil {
		t.Fatalf("control: UpdateRole on the live role: %v — the refusals above are vacuous unless this succeeds", err)
	}
}

// TestWorkflowRole_SecondDeleteIsNotFoundAndDoesNotRestamp: the second call matches no
// live row, so it is ErrNotFound rather than a re-stamp. Drop deleted_at IS NULL from
// the UPDATE and deleted_at moves and a second audit row appears — this is the test
// that catches it.
func TestWorkflowRole_SecondDeleteIsNotFoundAndDoesNotRestamp(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 second-delete")
	c, _ := activeAdmin(t, super, tenantID)
	store := NewStore(app)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	got, err := store.DeleteRole(c, "tax-reviewer")
	if err != nil {
		t.Fatalf("first DeleteRole: %v", err)
	}
	if got.Key != "tax-reviewer" {
		t.Errorf("first DeleteRole key = %q, want tax-reviewer", got.Key)
	}
	first := roleRow(t, super, roleID)
	if first.DeletedAt == nil {
		t.Fatal("stored deleted_at IS NULL after the first DeleteRole")
	}

	// A re-stamp would land on a later now(); the sleep removes any doubt about clock
	// resolution between two back-to-back transactions.
	time.Sleep(5 * time.Millisecond)
	if _, err := store.DeleteRole(c, "tax-reviewer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteRole: err = %v, want ErrNotFound", err)
	}
	if after := roleRow(t, super, roleID); !after.equal(first) {
		t.Errorf("stored row = %v, want it unchanged at %v — the second delete must not re-stamp", after, first)
	}
	if n := auditCount(t, super, tenantID, "workflow_role.deleted"); n != 1 {
		t.Errorf("workflow_role.deleted audit rows = %d, want 1", n)
	}
}

// TestWorkflowRole_ConcurrentDeleteExactlyOneWins: under READ COMMITTED the loser's
// UPDATE waits on the row lock and then re-evaluates deleted_at IS NULL, so exactly one
// call stamps and audits — the idempotency ruling holds with no explicit lock.
func TestWorkflowRole_ConcurrentDeleteExactlyOneWins(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	const rounds = 8
	for round := range rounds {
		tenantID := seedTenant(t, super, "APPR-02 concurrent-delete")
		c, _ := activeAdmin(t, super, tenantID)
		roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Add(2)
		for i := range 2 {
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = store.DeleteRole(c, "tax-reviewer")
			}()
		}
		close(start)
		wg.Wait()

		won, notFound := 0, 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrNotFound):
				notFound++
			default:
				t.Fatalf("round %d: errs[%d] = %v (SQLSTATE %q), want nil or ErrNotFound", round, i, err, pgCode(err))
			}
		}
		if won != 1 || notFound != 1 {
			t.Fatalf("round %d: %d successes and %d ErrNotFound, want exactly one of each (%v)", round, won, notFound, errs)
		}
		if after := roleRow(t, super, roleID); after.DeletedAt == nil {
			t.Errorf("round %d: stored deleted_at IS NULL, want the winner's stamp", round)
		}
		if n := auditCount(t, super, tenantID, "workflow_role.deleted"); n != 1 {
			t.Errorf("round %d: workflow_role.deleted audit rows = %d, want 1", round, n)
		}
	}
}

// TestWorkflowRole_ConcurrentDeleteAndRenameOneLosesCoherently: both take a lock on the
// same role row, so they serialize either way round. If the rename lands first the delete
// re-qualifies deleted_at IS NULL against the renamed version and audits the NEW title; if
// the delete lands first the rename's locking read finds no live row and is ErrNotFound.
// The delete can only ever lose to another delete, and a rename that failed must audit
// nothing.
func TestWorkflowRole_ConcurrentDeleteAndRenameOneLosesCoherently(t *testing.T) {
	super, app := dbTestPools(t)
	store := NewStore(app)

	const rounds = 8
	for round := range rounds {
		tenantID := seedTenant(t, super, "APPR-02 concurrent-delete-rename")
		c, _ := activeAdmin(t, super, tenantID)
		roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "A")

		var wg sync.WaitGroup
		var renameErr, deleteErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, renameErr = store.UpdateRole(c, "tax-reviewer", ptr("B"), nil)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, deleteErr = store.DeleteRole(c, "tax-reviewer")
		}()
		close(start)
		wg.Wait()

		if deleteErr != nil {
			t.Fatalf("round %d: DeleteRole = %v (SQLSTATE %q), want success", round, deleteErr, pgCode(deleteErr))
		}
		row := roleRow(t, super, roleID)
		if row.DeletedAt == nil {
			t.Fatalf("round %d: stored deleted_at IS NULL, want the delete's stamp", round)
		}
		if n := auditCount(t, super, tenantID, "workflow_role.deleted"); n != 1 {
			t.Fatalf("round %d: workflow_role.deleted rows = %d, want 1", round, n)
		}
		var deletedTitle string
		if err := super.QueryRow(context.Background(),
			`SELECT payload->>'title' FROM audit_log
			  WHERE tenant_id = $1 AND event = 'workflow_role.deleted'`, tenantID).Scan(&deletedTitle); err != nil {
			t.Fatalf("round %d: read the delete's audited title: %v", round, err)
		}
		renames := auditCount(t, super, tenantID, "workflow_role.updated")

		switch {
		case renameErr == nil:
			if row.Title != "B" || deletedTitle != "B" || renames != 1 {
				t.Errorf("round %d: rename won but stored title = %q, audited delete title = %q, updated rows = %d; want B, B, 1 — the delete must audit the title it actually removed",
					round, row.Title, deletedTitle, renames)
			}
		case errors.Is(renameErr, ErrNotFound):
			if row.Title != "A" || deletedTitle != "A" || renames != 0 {
				t.Errorf("round %d: delete won but stored title = %q, audited delete title = %q, updated rows = %d; want A, A, 0 — a refused rename may not write",
					round, row.Title, deletedTitle, renames)
			}
		default:
			t.Fatalf("round %d: UpdateRole = %v (SQLSTATE %q), want nil or ErrNotFound", round, renameErr, pgCode(renameErr))
		}
	}
}

// TestWorkflowRole_DeleteAuditsInSameTx: same positive form as the create and rename
// proofs — an UPDATE writes a new row version, so its xmin is the deleting xid.
//
// deleted_at is also compared against the audit row's created_at: workflow_roles is the
// repo's first soft-deleted table, and both columns come from now() (the transaction
// timestamp), so a Go-side time.Now() would land microseconds later and fail here.
func TestWorkflowRole_DeleteAuditsInSameTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-02 delete-audit-atomicity")
	c, adminID := activeAdmin(t, super, tenantID)
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	if _, err := NewStore(app).DeleteRole(c, "tax-reviewer"); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}

	var roleXmin, auditXmin, actor, payloadJSON string
	var deletedAt, auditAt time.Time
	if err := super.QueryRow(context.Background(),
		`SELECT r.xmin::text, a.xmin::text, a.actor, a.payload::text, r.deleted_at, a.created_at
		   FROM workflow_roles r, audit_log a
		  WHERE r.id = $1
		    AND a.tenant_id = $2 AND a.event = 'workflow_role.deleted' AND a.payload->>'key' = 'tax-reviewer'`,
		roleID, tenantID).Scan(&roleXmin, &auditXmin, &actor, &payloadJSON, &deletedAt, &auditAt); err != nil {
		t.Fatalf("xmin join (no row means the delete and its audit event do not both exist): %v", err)
	}
	for label, x := range map[string]string{"workflow_roles": roleXmin, "audit_log": auditXmin} {
		if x == "0" || x == "2" {
			t.Fatalf("%s.xmin = %s — a frozen/invalid xid makes this proof vacuous", label, x)
		}
	}
	if roleXmin != auditXmin {
		t.Errorf("xmin: workflow_roles = %s, audit_log = %s — the audit must be written on the same tx as the UPDATE", roleXmin, auditXmin)
	}
	if actor != adminID {
		t.Errorf("audit actor = %q, want the caller's subject %q", actor, adminID)
	}
	if !deletedAt.Equal(auditAt) {
		t.Errorf("deleted_at = %s, audit created_at = %s — deleted_at must come from now(), not a Go clock",
			deletedAt.UTC().Format(time.RFC3339Nano), auditAt.UTC().Format(time.RFC3339Nano))
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload %s: %v", payloadJSON, err)
	}
	want := map[string]any{"key": "tax-reviewer", "title": "Tax Reviewer"}
	if !reflect.DeepEqual(payload, want) {
		t.Errorf("audit payload = %v, want %v", payload, want)
	}
}
