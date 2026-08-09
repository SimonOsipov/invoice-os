package approval

// The store seam under a real Postgres: ListRoles + CreateRole as invoice_app,
// through db.WithinRequestTenantTx, with RLS as the only tenant filter.
//
// Every test below except TestWorkflowRole_StoreSatisfiesTheHandlerSeam self-skips
// without DATABASE_URL + DATABASE_SUPERUSER_URL, and no `rls` CI job runs this
// package until subtask 07 — so CI is green on skips until then. Run locally with
// `DATABASE_URL=... DATABASE_SUPERUSER_URL=... go test -p 1 ./internal/approval/...`.

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
// or skips when the per-role DSNs are unset — the same gate `make test-rls` uses
// (idiom copied from internal/tenancy/tenancy_test.go).
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("approval db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
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

func auditCount(t *testing.T, super *pgxpool.Pool, tenantID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'workflow_role.created'`,
		tenantID).Scan(&n); err != nil {
		t.Fatalf("count audit_log: %v", err)
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
// handlers to these two types), and pins that both methods resolve the identity
// before touching the pool — a store that dialled first would panic on the nil pool.
func TestWorkflowRole_StoreSatisfiesTheHandlerSeam(t *testing.T) {
	nilPool := NewStore(nil) // never dialled: the identity is resolved first
	var list RolesLister = nilPool.ListRoles
	var create RoleCreator = nilPool.CreateRole

	if _, err := list(context.Background()); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("ListRoles with no identity in ctx: err = %v, want db.ErrNoTenant", err)
	}
	if _, err := create(context.Background(), "Engagement Partner", ""); !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("CreateRole with no identity in ctx: err = %v, want db.ErrNoTenant", err)
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
	if n := auditCount(t, super, tenantID); n != 0 {
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
	if n := auditCount(t, super, tenantID); n != 0 {
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
