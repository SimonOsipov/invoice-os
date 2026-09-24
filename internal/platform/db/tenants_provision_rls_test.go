package db_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const provisionCall = `SELECT public.provision_workspace($1::uuid, $2::text, $3::text, $4::uuid, $5::text, $6::text)`

type provisionArgs struct {
	tenantID, name  string
	kind            *string
	userID, display string
	email           string
}

func newProvisionArgs(tenantID string) provisionArgs {
	return provisionArgs{
		tenantID: tenantID,
		name:     "Provision probe " + tenantID[:8],
		userID:   uuid.NewString(),
		display:  "Ada Admin",
		email:    "ada+" + tenantID[:8] + "@example.test",
	}
}

func (a provisionArgs) exec(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, provisionCall, a.tenantID, a.name, a.kind, a.userID, a.display, a.email)
	return err
}

// provisionAs calls the function on pool inside the ungated core, GUC = guc.
func provisionAs(ctx context.Context, pool *pgxpool.Pool, guc string, a provisionArgs) error {
	return db.WithinTenantTx(ctx, pool, guc, func(tx pgx.Tx) error { return a.exec(ctx, tx) })
}

func cleanupTenant(t *testing.T, ids ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
		}
	})
}

// assertPgRefusal names the clause: a bare SQLSTATE cannot tell RLS from a missing grant.
func assertPgRefusal(t *testing.T, what string, err error, code, msgPart string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: want SQLSTATE %s (%q), got non-pg error %v", what, code, msgPart, err)
	}
	if pgErr.Code != code || !strings.Contains(pgErr.Message, msgPart) {
		t.Fatalf("%s: want SQLSTATE %s containing %q, got %s %q", what, code, msgPart, pgErr.Code, pgErr.Message)
	}
}

func assertNothingWritten(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if n := mustCount(t, h.super, `SELECT count(*) FROM tenants WHERE id = $1`, id); n != 0 {
			t.Errorf("tenants rows for %s = %d, want 0", id, n)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM memberships WHERE tenant_id = $1`, id); n != 0 {
			t.Errorf("memberships rows for %s = %d, want 0", id, n)
		}
	}
}

func TestRLS_ProvisionWorkspace_AppCreatesTenantAndActiveAdmin(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	id := uuid.NewString()
	cleanupTenant(t, id)
	in := "in_house"
	a := newProvisionArgs(id)
	a.kind = &in

	if err := provisionAs(ctx, h.app, id, a); err != nil {
		t.Fatalf("provision_workspace as invoice_app under a matching GUC: want success, got %v", err)
	}

	err := db.WithinTenantTx(ctx, h.app, id, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM tenants`); n != 1 {
			t.Errorf("tenants visible under the new GUC = %d, want 1", n)
		}
		var name, kind string
		if err := tx.QueryRow(ctx, `SELECT name, kind FROM tenants WHERE id = $1`, id).Scan(&name, &kind); err != nil {
			return fmt.Errorf("read tenant: %w", err)
		}
		if name != a.name || kind != "in_house" {
			t.Errorf("tenant (name, kind) = (%q, %q), want (%q, in_house)", name, kind, a.name)
		}

		if n := mustCount(t, tx, `SELECT count(*) FROM memberships`); n != 1 {
			t.Errorf("memberships visible under the new GUC = %d, want 1", n)
		}
		var userID, role, status, display, email string
		if err := tx.QueryRow(ctx,
			`SELECT user_id::text, role, status, display_name, email FROM memberships WHERE tenant_id = $1`, id,
		).Scan(&userID, &role, &status, &display, &email); err != nil {
			return fmt.Errorf("read membership: %w", err)
		}
		got := []string{userID, role, status, display, email}
		want := []string{a.userID, "admin", "active", a.display, a.email}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("membership (user_id, role, status, display_name, email) = %v, want %v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read back under the new GUC: %v", err)
	}
}

func TestRLS_ProvisionWorkspace_MismatchedGUCRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	gucA, idB := uuid.NewString(), uuid.NewString()
	cleanupTenant(t, gucA, idB)

	err := provisionAs(ctx, h.app, gucA, newProvisionArgs(idB))
	assertPgRefusal(t, "provision id B under GUC A", err, "42501", "row-level security")
	assertNothingWritten(t, gucA, idB)
}

func TestRLS_ProvisionWorkspace_NoGUCRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	id := uuid.NewString()
	cleanupTenant(t, id)

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var guc *string
	if err := tx.QueryRow(ctx, `SELECT nullif(current_setting('app.current_tenant', true), '')`).Scan(&guc); err != nil {
		t.Fatalf("read GUC: %v", err)
	}
	if guc != nil {
		t.Fatalf("app.current_tenant in a raw app tx = %q, want unset — the probe would not test the no-GUC case", *guc)
	}

	err = newProvisionArgs(id).exec(ctx, tx)
	assertPgRefusal(t, "provision with no GUC", err, "42501", "row-level security")
	_ = tx.Rollback(ctx)
	assertNothingWritten(t, id)
}

// Green at head by design: AC-4 keeps invoice_app SELECT-only on tenants.
func TestRLS_AppCannotInsertTenantsDirectly(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	id := uuid.NewString()
	cleanupTenant(t, id)

	var canSelect bool
	if err := h.super.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', 'public.tenants', 'SELECT')`).Scan(&canSelect); err != nil {
		t.Fatalf("has_table_privilege: %v", err)
	}
	if !canSelect {
		t.Fatalf("invoice_app has no SELECT on tenants — the refusal below would not be the INSERT grant's")
	}

	err := db.WithinTenantTx(ctx, h.app, id, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'direct insert probe')`, id)
		return e
	})
	assertPgRefusal(t, "direct INSERT INTO tenants as invoice_app", err, "42501", "permission denied for table tenants")
	assertNothingWritten(t, id)
}

func TestRLS_ProvisionWorkspace_OnlyAppMayExecute(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		who  string
		pool *pgxpool.Pool
	}{
		{"invoice_tenant_reader", h.reader},
		{"supabase_auth_admin", authAdminPool(t)},
	} {
		id := uuid.NewString()
		cleanupTenant(t, id)
		err := provisionAs(ctx, c.pool, id, newProvisionArgs(id))
		assertPgRefusal(t, c.who+" executes provision_workspace", err, "42501", "permission denied for function provision_workspace")
		assertNothingWritten(t, id)
	}

	// Positive control: the same call succeeds for the one granted role.
	id := uuid.NewString()
	cleanupTenant(t, id)
	if err := provisionAs(ctx, h.app, id, newProvisionArgs(id)); err != nil {
		t.Fatalf("invoice_app executes provision_workspace: want success, got %v", err)
	}
}

func TestRLS_ProvisionWorkspace_KindDefaultsAndChecks(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	in, bogus := "in_house", "bogus"

	for _, c := range []struct {
		kind *string
		want string
	}{{nil, "firm"}, {&in, "in_house"}} {
		id := uuid.NewString()
		cleanupTenant(t, id)
		a := newProvisionArgs(id)
		a.kind = c.kind
		if err := provisionAs(ctx, h.app, id, a); err != nil {
			t.Fatalf("provision with kind %v: want success, got %v", c.kind, err)
		}
		var kind string
		if err := h.super.QueryRow(ctx, `SELECT kind FROM tenants WHERE id = $1`, id).Scan(&kind); err != nil {
			t.Fatalf("read kind: %v", err)
		}
		if kind != c.want {
			t.Errorf("kind after provisioning with %v = %q, want %q", c.kind, kind, c.want)
		}
	}

	id := uuid.NewString()
	cleanupTenant(t, id)
	a := newProvisionArgs(id)
	a.kind = &bogus
	err := provisionAs(ctx, h.app, id, a)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "tenants_kind_check" {
		t.Fatalf("provision with kind 'bogus': want 23514 on tenants_kind_check, got %v", err)
	}
	assertNothingWritten(t, id)
}

func TestRLS_ProvisionWorkspace_SecondCallRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	id := uuid.NewString()
	cleanupTenant(t, id)
	first := newProvisionArgs(id)

	if err := provisionAs(ctx, h.app, id, first); err != nil {
		t.Fatalf("first provision: want success, got %v", err)
	}

	second := newProvisionArgs(id)
	second.name = "Second name"
	err := provisionAs(ctx, h.app, id, second)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "tenants_pkey" {
		t.Fatalf("second provision of the same id: want 23505 on tenants_pkey, got %v", err)
	}

	var name string
	if err := h.super.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`, id).Scan(&name); err != nil {
		t.Fatalf("read tenant: %v", err)
	}
	if name != first.name {
		t.Errorf("tenant name after the refused second call = %q, want %q", name, first.name)
	}
	rows, err := h.super.Query(ctx, `SELECT user_id::text FROM memberships WHERE tenant_id = $1`, id)
	if err != nil {
		t.Fatalf("read memberships: %v", err)
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect memberships: %v", err)
	}
	if !reflect.DeepEqual(users, []string{first.userID}) {
		t.Errorf("memberships after the refused second call = %v, want only the first caller %s", users, first.userID)
	}
}

const provisionSig = "public.provision_workspace(uuid, text, text, uuid, text, text)"

func TestRLS_ProvisionWorkspace_FunctionShape(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var (
		found     bool
		secdef    bool
		owner     string
		config    []string
		argNames  []string
		returns   string
		grantees  []string
		publicAny bool
	)
	if err := h.super.QueryRow(ctx, `SELECT to_regprocedure($1) IS NOT NULL`, provisionSig).Scan(&found); err != nil {
		t.Fatalf("to_regprocedure: %v", err)
	}
	if !found {
		t.Fatalf("%s: want present, got absent", provisionSig)
	}
	if err := h.super.QueryRow(ctx, `
		SELECT p.prosecdef, pg_get_userbyid(p.proowner), coalesce(p.proconfig, '{}'), p.proargnames, p.prorettype::regtype::text,
		       coalesce((SELECT array_agg(pg_get_userbyid(a.grantee) || '=' || a.privilege_type ORDER BY 1)
		                 FROM aclexplode(p.proacl) a WHERE a.grantee <> 0), '{}'),
		       p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee = 0)
		FROM pg_proc p WHERE p.oid = to_regprocedure($1)`, provisionSig,
	).Scan(&secdef, &owner, &config, &argNames, &returns, &grantees, &publicAny); err != nil {
		t.Fatalf("read pg_proc: %v", err)
	}

	if !secdef {
		t.Errorf("prosecdef = false, want true (SECURITY DEFINER)")
	}
	if owner != "invoice_migrator" {
		t.Errorf("owner = %q, want invoice_migrator", owner)
	}
	if !reflect.DeepEqual(config, []string{`search_path=""`}) {
		t.Errorf("proconfig = %q, want [search_path=\"\"]", config)
	}
	wantArgs := []string{"p_tenant_id", "p_name", "p_kind", "p_user_id", "p_display_name", "p_email"}
	if !reflect.DeepEqual(argNames, wantArgs) {
		t.Errorf("proargnames = %v, want %v", argNames, wantArgs)
	}
	if returns != "void" {
		t.Errorf("return type = %q, want void", returns)
	}
	// A NULL proacl means the default grant, which includes PUBLIC.
	if publicAny {
		t.Errorf("PUBLIC holds EXECUTE (or proacl is the NULL default), want revoked")
	}
	sort.Strings(grantees)
	if want := []string{"invoice_app=EXECUTE", "invoice_migrator=EXECUTE"}; !reflect.DeepEqual(grantees, want) {
		t.Errorf("ACL = %v, want exactly %v", grantees, want)
	}
}

type provisionSchemaSnapshot struct {
	Functions, Relations, Columns, Policies []string
}

func snapshotProvisionSchema(t *testing.T, ctx context.Context, tx pgx.Tx) provisionSchemaSnapshot {
	t.Helper()
	collect := func(sql string) []string {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			t.Fatalf("snapshot %q: %v", sql, err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("snapshot %q: %v", sql, err)
		}
		return out
	}
	return provisionSchemaSnapshot{
		Functions: collect(`SELECT p.oid::regprocedure::text FROM pg_proc p
			JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public' ORDER BY 1`),
		Relations: collect(`SELECT c.relname || ':' || c.relkind::text FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' ORDER BY 1`),
		Columns: collect(`SELECT table_name || '.' || column_name || ':' || data_type || ':' || is_nullable || ':' || coalesce(column_default, '')
			FROM information_schema.columns WHERE table_schema = 'public' AND table_name IN ('tenants', 'memberships') ORDER BY 1`),
		Policies: collect(`SELECT polrelid::regclass::text || '.' || polname || ':' || coalesce(pg_get_expr(polqual, polrelid), '')
			FROM pg_policy WHERE polrelid IN ('public.tenants'::regclass, 'public.memberships'::regclass) ORDER BY 1`),
	}
}

func TestRLS_ProvisionWorkspaceDownDropsOnlyTheFunction(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// Non-vacuous guard first: an absent function would make "absent after the Down" pass.
	if n := mustCount(t, h.super, `SELECT count(*) FROM pg_proc WHERE oid = to_regprocedure($1)`, provisionSig); n != 1 {
		t.Fatalf("%s before the Down: want 1 pg_proc row, got %d", provisionSig, n)
	}
	stmts := shippedDownStatements(t, "*_provision_workspace.sql")
	if len(stmts) != 1 {
		t.Fatalf("Down statements = %v, want exactly one DROP FUNCTION", stmts)
	}

	tx, err := h.mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() {
		if e := tx.Rollback(context.Background()); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
			t.Errorf("rollback the Down probe: %v", e)
		}
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}

	before := snapshotProvisionSchema(t, ctx, tx)
	if len(before.Columns) == 0 || len(before.Policies) == 0 {
		t.Fatalf("snapshot before the Down is empty (%+v) — the unchanged-schema check would be vacuous", before)
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("execute the shipped Down statement %q as the migrator: %v", s, err)
		}
	}
	after := snapshotProvisionSchema(t, ctx, tx)

	var wantFns []string
	for _, f := range before.Functions {
		if f != "provision_workspace(uuid,text,text,uuid,text,text)" {
			wantFns = append(wantFns, f)
		}
	}
	if len(wantFns) != len(before.Functions)-1 {
		t.Fatalf("provision_workspace not in the public function snapshot %v — the function-drop check would be vacuous", before.Functions)
	}
	if !reflect.DeepEqual(after.Functions, wantFns) {
		t.Errorf("public functions after the Down = %v, want %v (only provision_workspace dropped)", after.Functions, wantFns)
	}
	if !reflect.DeepEqual(after.Relations, before.Relations) {
		t.Errorf("public relations changed by the Down: before %v, after %v", before.Relations, after.Relations)
	}
	if !reflect.DeepEqual(after.Columns, before.Columns) {
		t.Errorf("tenants/memberships columns changed by the Down: before %v, after %v", before.Columns, after.Columns)
	}
	if !reflect.DeepEqual(after.Policies, before.Policies) {
		t.Errorf("tenants/memberships policies changed by the Down: before %v, after %v", before.Policies, after.Policies)
	}
}
