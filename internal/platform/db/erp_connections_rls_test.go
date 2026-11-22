// Storage suite for erp_grants and erp_connections: shape, RLS, grants, constraints, cascades.
package db_test

import (
	"context"
	"io/fs"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const erpMigrationGlob = "*_erp_connections.sql"

const (
	erpGrantInsert = `INSERT INTO erp_grants (tenant_id, system, kind) VALUES ($1, $2, $3) RETURNING id::text`
	erpConnInsert  = `INSERT INTO erp_connections (tenant_id, entity_id, system, external_key, status, grant_id)
	VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`
)

// erpQuerier is the catalog-read surface shared by pgx.Tx and *pgxpool.Pool.
type erpQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// failIfErpUndefined turns a pre-migration 42P01 / 42703 into an explicit message.
func failIfErpUndefined(t *testing.T, what string, err error) {
	t.Helper()
	if c := pgCode(err); c == "42P01" || c == "42703" {
		t.Fatalf("%s: %s — the erp_connections migration is not applied yet: %v", what, c, err)
	}
}

// requireErpTables fails with an explicit message while either table is absent.
func requireErpTables(t *testing.T, q erpQuerier) {
	t.Helper()
	for _, table := range []string{"erp_grants", "erp_connections"} {
		var reg *string
		if err := q.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, "public."+table).Scan(&reg); err != nil {
			t.Fatalf("to_regclass(public.%s): %v", table, err)
		}
		if reg == nil {
			t.Fatalf("public.%s does not exist — the erp_connections migration is not applied yet", table)
		}
	}
}

// erpTenant creates a throwaway tenant: deleting it cascades both tables, the clients and the grants.
func erpTenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO tenants (id, name) VALUES ($1, 'erp-connections throwaway tenant')`, id); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

func erpEntity(t *testing.T, tenant, name string) string {
	t.Helper()
	id, _ := seedBusinessEntity(t, tenant, name)
	return id
}

func erpSeedGrant(t *testing.T, tenant string) string {
	t.Helper()
	var id string
	err := h.super.QueryRow(context.Background(), erpGrantInsert, tenant, "fake", "oauth_refresh").Scan(&id)
	failIfErpUndefined(t, "seed erp_grants", err)
	if err != nil {
		t.Fatalf("seed erp_grants: %v", err)
	}
	return id
}

// erpSeedConn inserts one connection as the superuser; a nil key or grant is NULL.
func erpSeedConn(t *testing.T, tenant, entity, system string, key, grant *string, status string) string {
	t.Helper()
	var id string
	err := h.super.QueryRow(context.Background(), erpConnInsert, tenant, entity, system, key, status, grant).Scan(&id)
	failIfErpUndefined(t, "seed erp_connections", err)
	if err != nil {
		t.Fatalf("seed erp_connections: %v", err)
	}
	return id
}

func erpStr(s string) *string { return &s }

// erpApp runs fn as invoice_app bound to tenant, with app.entity_scope set when scope is non-nil.
func erpApp(tenant string, scope *string, fn func(tx pgx.Tx) error) error {
	ctx := context.Background()
	return db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
		if scope != nil {
			if _, e := tx.Exec(ctx, `SELECT set_config('app.entity_scope', $1, true)`, *scope); e != nil {
				return e
			}
		}
		return fn(tx)
	})
}

// erpAppIDs returns the sorted ids of every row of table that tenant sees under scope.
func erpAppIDs(t *testing.T, tenant string, scope *string, table string) []string {
	t.Helper()
	ctx := context.Background()
	var got []string
	err := erpApp(tenant, scope, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM `+table+` ORDER BY 1`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				return e
			}
			got = append(got, id)
		}
		return rows.Err()
	})
	failIfErpUndefined(t, "read "+table, err)
	if err != nil {
		t.Fatalf("read %s (scope %v): %v", table, scope, err)
	}
	return got
}

func sortedIDs(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// erpWantErr checks the SQLSTATE and, when constraint is non-empty, the constraint that raised it.
func erpWantErr(t *testing.T, err error, code, constraint, what string) {
	t.Helper()
	failIfErpUndefined(t, what, err)
	if err == nil {
		t.Errorf("%s succeeded, want SQLSTATE %s", what, code)
		return
	}
	if got := pgCode(err); got != code {
		t.Errorf("%s: SQLSTATE = %q, want %q: %v", what, got, code, err)
		return
	}
	if constraint != "" {
		if got := pgConstraint(err); got != constraint {
			t.Errorf("%s tripped constraint %q, want %q", what, got, constraint)
		}
	}
}

// ---- shape ----------------------------------------------------------------

type erpCol struct {
	typ     string
	notNull bool
	def     string // information_schema.column_default, "" for none
}

var erpShape = map[string]map[string]erpCol{
	"erp_grants": {
		"tenant_id":  {"uuid", true, ""},
		"id":         {"uuid", true, "gen_random_uuid()"},
		"system":     {"text", true, ""},
		"kind":       {"text", true, ""},
		"secret_ref": {"text", false, ""},
		"expires_at": {"timestamp with time zone", false, ""},
		"version":    {"bigint", true, "1"},
		"created_at": {"timestamp with time zone", true, "now()"},
		"updated_at": {"timestamp with time zone", true, "now()"},
	},
	"erp_connections": {
		"tenant_id":    {"uuid", true, ""},
		"id":           {"uuid", true, "gen_random_uuid()"},
		"entity_id":    {"uuid", true, ""},
		"system":       {"text", true, ""},
		"external_key": {"text", false, ""},
		"status":       {"text", true, "'pending'::text"},
		"grant_id":     {"uuid", false, ""},
		"cursor":       {"bytea", false, ""},
		"last_sync_at": {"timestamp with time zone", false, ""},
		"created_at":   {"timestamp with time zone", true, "now()"},
		"updated_at":   {"timestamp with time zone", true, "now()"},
	},
}

// Named constraints other than the NOT NULLs, which erpConstraintsWant derives from erpShape (PG18 names them).
var erpNamedConstraints = map[string][]string{
	"erp_grants": {
		"erp_grants_pk", "erp_grants_tenant_id_id_uq", "erp_grants_kind_check", "erp_grants_tenant_id_fkey",
	},
	"erp_connections": {
		"erp_connections_pk", "erp_connections_tenant_id_id_uq", "erp_connections_client_system_key_uq",
		"erp_connections_status_check", "erp_connections_active_is_complete",
		"erp_connections_tenant_entity_fk", "erp_connections_tenant_grant_fk", "erp_connections_tenant_id_fkey",
	},
}

func erpConstraintsWant(table string) []string {
	want := slices.Clone(erpNamedConstraints[table])
	for col, c := range erpShape[table] {
		if c.notNull {
			want = append(want, table+"_"+col+"_not_null")
		}
	}
	sort.Strings(want)
	return want
}

// erpShapeProblems compares the live columns of both tables to erpShape.
func erpShapeProblems(q erpQuerier) []string { return erpShapeProblemsOf(q, erpShape) }

func erpShapeProblemsOf(q erpQuerier, shape map[string]map[string]erpCol) []string {
	var problems []string
	for table, want := range shape {
		rows, err := q.Query(context.Background(),
			`SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = $1`, table)
		if err != nil {
			return append(problems, table+": read information_schema.columns: "+err.Error())
		}
		got := map[string]erpCol{}
		for rows.Next() {
			var name, typ, nullable string
			var def *string
			if e := rows.Scan(&name, &typ, &nullable, &def); e != nil {
				rows.Close()
				return append(problems, table+": scan column: "+e.Error())
			}
			c := erpCol{typ: typ, notNull: nullable == "NO"}
			if def != nil {
				c.def = *def
			}
			got[name] = c
		}
		rows.Close()
		if len(got) == 0 {
			problems = append(problems, table+": information_schema.columns returned no column")
			continue
		}
		if !reflect.DeepEqual(got, want) {
			problems = append(problems, "columns of "+table+" differ from the Design: got "+fmtCols(got)+" want "+fmtCols(want))
		}
	}
	return problems
}

func fmtCols(m map[string]erpCol) string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		c := m[n]
		b.WriteString("\n  " + n + " " + c.typ)
		if c.notNull {
			b.WriteString(" NOT NULL")
		}
		if c.def != "" {
			b.WriteString(" DEFAULT " + c.def)
		}
	}
	return b.String()
}

// erpConstraintProblems compares pg_constraint names, and the definitions that carry a disposition, to the Design.
func erpConstraintProblems(q erpQuerier) []string {
	ctx := context.Background()
	var problems []string
	for table := range erpShape {
		rows, err := q.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid = $1::regclass ORDER BY conname`, "public."+table)
		if err != nil {
			return append(problems, table+": read pg_constraint: "+err.Error())
		}
		var got []string
		for rows.Next() {
			var n string
			if e := rows.Scan(&n); e != nil {
				rows.Close()
				return append(problems, table+": scan conname: "+e.Error())
			}
			got = append(got, n)
		}
		rows.Close()
		if len(got) == 0 {
			problems = append(problems, table+": pg_constraint returned no constraint")
			continue
		}
		if want := erpConstraintsWant(table); !slices.Equal(got, want) {
			problems = append(problems, "constraints of "+table+" = "+strings.Join(got, ",")+"\n  want "+strings.Join(want, ","))
		}
	}
	defs := []struct {
		table, name string
		has, lacks  []string
	}{
		{"erp_grants", "erp_grants_tenant_id_fkey", []string{"FOREIGN KEY (tenant_id)", "REFERENCES tenants(id)", "ON DELETE CASCADE"}, nil},
		{"erp_connections", "erp_connections_tenant_id_fkey", []string{"FOREIGN KEY (tenant_id)", "REFERENCES tenants(id)", "ON DELETE CASCADE"}, nil},
		{"erp_connections", "erp_connections_tenant_entity_fk",
			[]string{"FOREIGN KEY (tenant_id, entity_id)", "REFERENCES business_entities(tenant_id, id)", "ON DELETE CASCADE"}, nil},
		// NO ACTION, not RESTRICT or CASCADE: a referenced grant is refused with 23503 and the tenant cascade still passes.
		{"erp_connections", "erp_connections_tenant_grant_fk",
			[]string{"FOREIGN KEY (tenant_id, grant_id)", "REFERENCES erp_grants(tenant_id, id)"}, []string{"ON DELETE", "ON UPDATE"}},
		{"erp_connections", "erp_connections_client_system_key_uq", []string{"UNIQUE (tenant_id, entity_id, system, external_key)"}, nil},
	}
	for _, d := range defs {
		var def string
		if err := q.QueryRow(ctx,
			`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2`,
			"public."+d.table, d.name).Scan(&def); err != nil {
			problems = append(problems, d.name+": read definition: "+err.Error())
			continue
		}
		for _, frag := range d.has {
			if !strings.Contains(def, frag) {
				problems = append(problems, d.name+" = "+def+", want it to contain "+frag)
			}
		}
		for _, frag := range d.lacks {
			if strings.Contains(def, frag) {
				problems = append(problems, d.name+" = "+def+", want it not to contain "+frag)
			}
		}
	}
	return problems
}

func TestRLS_ErpTablesColumnShape(t *testing.T) {
	h := requireHarness(t)
	requireErpTables(t, h.super)
	for _, p := range erpShapeProblems(h.super) {
		t.Error(p)
	}
}

// PG18 lists every NOT NULL as its own <table>_<col>_not_null constraint.
func TestRLS_ErpTablesConstraintSet(t *testing.T) {
	h := requireHarness(t)
	requireErpTables(t, h.super)
	for _, p := range erpConstraintProblems(h.super) {
		t.Error(p)
	}
}

// ---- RLS ------------------------------------------------------------------

// ENABLE alone would let the owner bypass the policy; a policy TO invoice_app would leave the migrator with none.
func TestRLS_ErpTablesForceRLSAndPolicyDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireErpTables(t, h.super)

	for _, table := range []string{"erp_grants", "erp_connections"} {
		var enabled, forced bool
		if err := h.super.QueryRow(ctx,
			`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass($1)`, "public."+table,
		).Scan(&enabled, &forced); err != nil {
			t.Fatalf("read pg_class for %s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s relrowsecurity/relforcerowsecurity = %v/%v, want true/true", table, enabled, forced)
		}

		rows, err := h.super.Query(ctx,
			`SELECT policyname, cmd, roles::text[], coalesce(qual, '') FROM pg_policies
		  WHERE schemaname = 'public' AND tablename = $1`, table)
		if err != nil {
			t.Fatalf("query pg_policies for %s: %v", table, err)
		}
		type policy struct {
			cmd   string
			roles []string
			qual  string
		}
		got := map[string]policy{}
		for rows.Next() {
			var name string
			var p policy
			if e := rows.Scan(&name, &p.cmd, &p.roles, &p.qual); e != nil {
				rows.Close()
				t.Fatalf("scan pg_policies row: %v", e)
			}
			got[name] = p
		}
		rows.Close()
		if len(got) != 1 {
			t.Errorf("policies on %s = %d (%v), want exactly 1 (tenant_isolation)", table, len(got), got)
		}
		iso, ok := got["tenant_isolation"]
		if !ok {
			t.Errorf("no tenant_isolation policy on %s", table)
			continue
		}
		if iso.cmd != "ALL" || strings.Join(iso.roles, ",") != "public" {
			t.Errorf("%s tenant_isolation cmd/roles = %s/%v, want ALL/[public] (no TO clause)", table, iso.cmd, iso.roles)
		}
		if !strings.Contains(iso.qual, "app.current_tenant") {
			t.Errorf("%s tenant_isolation qual = %q, want a comparison against app.current_tenant", table, iso.qual)
		}
		hasScope := strings.Contains(iso.qual, "app.entity_scope")
		if table == "erp_connections" && !hasScope {
			t.Errorf("erp_connections tenant_isolation qual = %q, want a comparison against app.entity_scope", iso.qual)
		}
		if table == "erp_grants" && hasScope {
			t.Errorf("erp_grants tenant_isolation qual = %q, want the tenant only: a grant has no client scope", iso.qual)
		}
	}
}

func TestRLS_ErpConnectionsCrossTenantSelectRefused(t *testing.T) {
	requireHarness(t)
	a, b := erpTenant(t), erpTenant(t)
	ea1, ea2, eb := erpEntity(t, a, "erp-xsel-a1"), erpEntity(t, a, "erp-xsel-a2"), erpEntity(t, b, "erp-xsel-b")
	a1 := erpSeedConn(t, a, ea1, "fake", erpStr("k1"), nil, "pending")
	a2 := erpSeedConn(t, a, ea2, "fake", erpStr("k2"), nil, "pending")
	b1 := erpSeedConn(t, b, eb, "fake", erpStr("k1"), nil, "pending")

	if got, want := erpAppIDs(t, a, nil, "erp_connections"), sortedIDs(a1, a2); !slices.Equal(got, want) {
		t.Errorf("connections visible to A = %v, want exactly A's %v", got, want)
	}
	if got, want := erpAppIDs(t, b, nil, "erp_connections"), []string{b1}; !slices.Equal(got, want) {
		t.Errorf("connections visible to B = %v, want exactly B's %v", got, want)
	}
}

func TestRLS_ErpGrantsCrossTenantSelectRefused(t *testing.T) {
	requireHarness(t)
	a, b := erpTenant(t), erpTenant(t)
	a1, a2, b1 := erpSeedGrant(t, a), erpSeedGrant(t, a), erpSeedGrant(t, b)

	if got, want := erpAppIDs(t, a, nil, "erp_grants"), sortedIDs(a1, a2); !slices.Equal(got, want) {
		t.Errorf("grants visible to A = %v, want exactly A's %v", got, want)
	}
	if got, want := erpAppIDs(t, b, nil, "erp_grants"), []string{b1}; !slices.Equal(got, want) {
		t.Errorf("grants visible to B = %v, want exactly B's %v", got, want)
	}
}

// The own-tenant control separates the WITH CHECK clause from a missing INSERT grant: both answer 42501.
func TestRLS_ErpTablesCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	a, b := erpTenant(t), erpTenant(t)
	ea, eb := erpEntity(t, a, "erp-xins-a"), erpEntity(t, b, "erp-xins-b")

	// erp_grants
	err := erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), erpGrantInsert, a, "fake", "api_key")
		return e
	})
	failIfErpUndefined(t, "control grant INSERT in the own tenant", err)
	if err != nil {
		t.Fatalf("control grant INSERT in the own tenant: %v", err)
	}
	err = erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), erpGrantInsert, b, "fake", "api_key")
		return e
	})
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") || !strings.Contains(msg, "erp_grants") {
		t.Errorf("grant refusal message = %q, want the erp_grants RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_grants WHERE tenant_id = $1`, b); n != 0 {
		t.Errorf("grants of tenant B after the refused INSERT = %d, want 0", n)
	}

	// erp_connections: B's own client, so only the policy can refuse the row.
	err = erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), erpConnInsert, a, ea, "fake", "k1", "pending", nil)
		return e
	})
	if err != nil {
		t.Fatalf("control connection INSERT in the own tenant: %v", err)
	}
	err = erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), erpConnInsert, b, eb, "fake", "k1", "pending", nil)
		return e
	})
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") || !strings.Contains(msg, "erp_connections") {
		t.Errorf("connection refusal message = %q, want the erp_connections RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE tenant_id = $1`, b); n != 0 {
		t.Errorf("connections of tenant B after the refused INSERT = %d, want 0", n)
	}
}

func TestRLS_ErpTablesMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a := erpTenant(t)
	ea := erpEntity(t, a, "erp-noguc")
	erpSeedGrant(t, a)
	erpSeedConn(t, a, ea, "fake", erpStr("k1"), nil, "pending")

	// Control: the zeros below mean nothing unless there are rows to hide.
	for _, table := range []string{"erp_grants", "erp_connections"} {
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, a); n != 1 {
			t.Fatalf("superuser sees %d %s rows, want 1", n, table)
		}
	}
	// A pooled connection keeps app.current_tenant = '' after a tenant tx, so a policy
	// without nullif() raises 22P02 there instead of returning zero rows.
	for _, emptyGUC := range []bool{false, true} {
		for _, table := range []string{"erp_grants", "erp_connections"} {
			tx, err := h.app.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if emptyGUC {
				if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', '', true)`); err != nil {
					_ = tx.Rollback(ctx)
					t.Fatalf("set_config: %v", err)
				}
			}
			n, err := scanCount(ctx, tx, `SELECT count(*) FROM `+table)
			if err != nil {
				_ = tx.Rollback(ctx)
				t.Fatalf("SELECT %s with no tenant context (empty GUC %v): %v", table, emptyGUC, err)
			}
			if n != 0 {
				t.Errorf("%s rows visible with no tenant set (empty GUC %v) = %d, want 0", table, emptyGUC, n)
			}
			insert, args := erpGrantInsert, []any{a, "fake", "api_key"}
			if table == "erp_connections" {
				insert, args = erpConnInsert, []any{a, ea, "fake", "k2", "pending", nil}
			}
			_, err = tx.Exec(ctx, insert, args...)
			_ = tx.Rollback(ctx)
			assertRLSViolation(t, err)
		}
	}
}

// FORCE subjects the migrator (the table owner) to the policy: with no GUC it matches nothing.
func TestRLS_ErpTablesOwnerRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a := erpTenant(t)
	ea := erpEntity(t, a, "erp-owner")
	erpSeedGrant(t, a)
	erpSeedConn(t, a, ea, "fake", erpStr("k1"), nil, "pending")

	for _, table := range []string{"erp_grants", "erp_connections"} {
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, a); n != 1 {
			t.Fatalf("superuser sees %d %s rows, want 1", n, table)
		}
		if n := mustCount(t, h.mig, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("owner sees %d %s rows with no tenant context, want 0 (FORCE ROW LEVEL SECURITY)", n, table)
		}
	}
	_, err := h.mig.Exec(ctx, erpGrantInsert, a, "fake", "api_key")
	erpWantErr(t, err, "42501", "", "the owner inserting a grant with no tenant GUC")
	_, err = h.mig.Exec(ctx, erpConnInsert, a, ea, "fake", "k2", "pending", nil)
	erpWantErr(t, err, "42501", "", "the owner inserting a connection with no tenant GUC")
	for _, table := range []string{"erp_grants", "erp_connections"} {
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, a); n != 1 {
			t.Errorf("%s rows after the refused owner inserts = %d, want the 1 seeded", table, n)
		}
	}

	// Control: bound to the row's own tenant the owner may insert, so the refusals above are the forced policy.
	err = db.WithinTenantTx(ctx, h.mig, a, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, erpGrantInsert, a, "fake", "api_key")
		return e
	})
	if err != nil {
		t.Fatalf("the owner inserting a grant under its own tenant GUC: want success, got %v", err)
	}
}

func TestRLS_ErpConnectionsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	a, b := erpTenant(t), erpTenant(t)
	ea := erpEntity(t, a, "erp-reassign")
	id := erpSeedConn(t, a, ea, "fake", erpStr("k1"), nil, "pending")

	// Control: the row is editable in place, so the refusal below is the WITH CHECK clause and not a missing grant.
	var n int64
	err := erpApp(a, nil, func(tx pgx.Tx) error {
		tag, e := tx.Exec(context.Background(), `UPDATE erp_connections SET status = 'disconnected' WHERE id = $1`, id)
		n = tag.RowsAffected()
		return e
	})
	if err != nil || n != 1 {
		t.Fatalf("control UPDATE of the own row = %d rows (err %v), want 1", n, err)
	}
	err = erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), `UPDATE erp_connections SET tenant_id = $1 WHERE id = $2`, b, id)
		return e
	})
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("reassignment refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	var owner string
	if err := h.super.QueryRow(context.Background(), `SELECT tenant_id::text FROM erp_connections WHERE id = $1`, id).Scan(&owner); err != nil || owner != a {
		t.Errorf("tenant_id after the refused reassignment = %q (err %v), want %s", owner, err, a)
	}
}

func TestRLS_ErpConnectionsLimitedScopeSeesOnlyItsClients(t *testing.T) {
	requireHarness(t)
	a, b := erpTenant(t), erpTenant(t)
	e1, e2 := erpEntity(t, a, "erp-scope-1"), erpEntity(t, a, "erp-scope-2")
	eb := erpEntity(t, b, "erp-scope-b")

	// e1 holds 2 and e2 holds 3, so a scope that leaks a client or a count is told apart.
	var for1, for2 []string
	for _, key := range []string{"k1", "k2"} {
		for1 = append(for1, erpSeedConn(t, a, e1, "fake", erpStr(key), nil, "pending"))
	}
	for _, key := range []string{"k1", "k2", "k3"} {
		for2 = append(for2, erpSeedConn(t, a, e2, "fake", erpStr(key), nil, "pending"))
	}
	erpSeedConn(t, b, eb, "fake", erpStr("k1"), nil, "pending")
	all := sortedIDs(append(slices.Clone(for1), for2...)...)

	for _, c := range []struct {
		scope string
		want  []string
	}{
		{"{" + e1 + "}", sortedIDs(for1...)},
		{"{" + e2 + "}", sortedIDs(for2...)},
		{"{" + e1 + "," + e2 + "}", all},
		{"*", all},
		{"", all},
		{"{}", nil},
	} {
		scope := c.scope
		if got := erpAppIDs(t, a, &scope, "erp_connections"); !slices.Equal(got, c.want) {
			t.Errorf("scope %q: visible connections = %v, want %v", c.scope, got, c.want)
		}
	}
	if got := erpAppIDs(t, a, nil, "erp_connections"); !slices.Equal(got, all) {
		t.Errorf("no scope set: visible connections = %v, want all %v", got, all)
	}
}

func TestRLS_ErpConnectionsInsertOutsideTheScopeRefused(t *testing.T) {
	h := requireHarness(t)
	a := erpTenant(t)
	e1, e2 := erpEntity(t, a, "erp-scopeins-1"), erpEntity(t, a, "erp-scopeins-2")
	scope := "{" + e1 + "}"

	insert := func(entity, key string) error {
		return erpApp(a, &scope, func(tx pgx.Tx) error {
			_, e := tx.Exec(context.Background(), erpConnInsert, a, entity, "fake", key, "pending", nil)
			return e
		})
	}
	err := insert(e1, "k1")
	failIfErpUndefined(t, "control INSERT inside the scope", err)
	if err != nil {
		t.Fatalf("control INSERT inside the scope: %v", err)
	}
	assertRLSViolation(t, insert(e2, "k1"))

	// Without RETURNING the row is never read back, so only the policy's insert check can refuse it.
	bare := func(entity, key string) error {
		return erpApp(a, &scope, func(tx pgx.Tx) error {
			_, e := tx.Exec(context.Background(),
				`INSERT INTO erp_connections (tenant_id, entity_id, system, external_key, status) VALUES ($1, $2, 'fake', $3, 'pending')`,
				a, entity, key)
			return e
		})
	}
	if err := bare(e1, "k4"); err != nil {
		t.Fatalf("control INSERT without RETURNING inside the scope: %v", err)
	}
	assertRLSViolation(t, bare(e2, "k4"))
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE entity_id = $1`, e2); n != 0 {
		t.Errorf("connections of the out-of-scope client after the refused INSERT = %d, want 0", n)
	}

	// The grant allows UPDATE, so the policy alone keeps an out-of-scope row from being edited.
	out := erpSeedConn(t, a, e2, "fake", erpStr("k2"), nil, "pending")
	in := erpSeedConn(t, a, e1, "fake", erpStr("k2"), nil, "pending")
	update := func(id string) int64 {
		var n int64
		err := erpApp(a, &scope, func(tx pgx.Tx) error {
			tag, e := tx.Exec(context.Background(), `UPDATE erp_connections SET status = 'disconnected' WHERE id = $1`, id)
			n = tag.RowsAffected()
			return e
		})
		if err != nil {
			t.Fatalf("UPDATE %s: %v", id, err)
		}
		return n
	}
	if n := update(in); n != 1 {
		t.Fatalf("control UPDATE of an in-scope row = %d rows, want 1", n)
	}
	if n := update(out); n != 0 {
		t.Errorf("UPDATE of an out-of-scope row = %d rows, want 0", n)
	}
}

// RI checks bypass RLS, so only a composite FK on (tenant_id, entity_id) refuses B's client.
func TestRLS_ErpConnectionsCrossTenantEntityRefused(t *testing.T) {
	h := requireHarness(t)
	a, b := erpTenant(t), erpTenant(t)
	ea, eb := erpEntity(t, a, "erp-xent-a"), erpEntity(t, b, "erp-xent-b")
	erpSeedConn(t, a, ea, "fake", erpStr("k1"), nil, "pending") // control: the own client is accepted

	_, err := h.super.Exec(context.Background(), erpConnInsert, a, eb, "fake", "k1", "pending", nil)
	erpWantErr(t, err, "23503", "erp_connections_tenant_entity_fk", "naming tenant B's client under tenant A")
}

func TestRLS_ErpConnectionsCrossTenantGrantRefused(t *testing.T) {
	h := requireHarness(t)
	a, b := erpTenant(t), erpTenant(t)
	ea := erpEntity(t, a, "erp-xgrant-a")
	ga, gb := erpSeedGrant(t, a), erpSeedGrant(t, b)
	erpSeedConn(t, a, ea, "fake", erpStr("k1"), &ga, "pending") // control: the own grant is accepted

	_, err := h.super.Exec(context.Background(), erpConnInsert, a, ea, "fake", "k2", "pending", gb)
	erpWantErr(t, err, "23503", "erp_connections_tenant_grant_fk", "naming tenant B's grant under tenant A")
}

func TestRLS_ErpConnectionsUniquePerClientSystemKey(t *testing.T) {
	h := requireHarness(t)
	a := erpTenant(t)
	e1, e2 := erpEntity(t, a, "erp-uq-1"), erpEntity(t, a, "erp-uq-2")
	erpSeedConn(t, a, e1, "fake", erpStr("fake-co-1"), nil, "pending")

	_, err := h.super.Exec(context.Background(), erpConnInsert, a, e1, "fake", "fake-co-1", "pending", nil)
	erpWantErr(t, err, "23505", "erp_connections_client_system_key_uq", "the same (client, system, key) twice")
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE entity_id = $1`, e1); n != 1 {
		t.Errorf("rows of the client after the refused duplicate = %d, want 1", n)
	}

	// Each column of the key is load-bearing: change one and the row is accepted.
	erpSeedConn(t, a, e2, "fake", erpStr("fake-co-1"), nil, "pending")
	erpSeedConn(t, a, e1, "other", erpStr("fake-co-1"), nil, "pending")
	erpSeedConn(t, a, e1, "fake", erpStr("fake-co-2"), nil, "pending")
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE tenant_id = $1`, a); n != 4 {
		t.Errorf("rows of the tenant = %d, want 4", n)
	}
}

// Each rejection is a case only its named constraint can refuse: the other CHECK passes on that row.
func TestRLS_ErpTablesValueChecks(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a := erpTenant(t)
	ent := erpEntity(t, a, "erp-checks")
	grant := erpSeedGrant(t, a)
	key := func() *string { return erpStr("k-" + uuid.NewString()) }

	type connCase struct {
		name   string
		status string
		key    *string
		grant  *string
		want   string // constraint that refuses; "" accepts
	}
	var accepted int
	for _, c := range []connCase{
		{"paused status", "paused", key(), &grant, "erp_connections_status_check"},
		{"empty status", "", key(), &grant, "erp_connections_status_check"},
		{"active with a NULL key", "active", nil, &grant, "erp_connections_active_is_complete"},
		{"active with a NULL grant", "active", key(), nil, "erp_connections_active_is_complete"},
		{"active with neither", "active", nil, nil, "erp_connections_active_is_complete"},
		{"disconnected with a NULL grant and key", "disconnected", nil, nil, ""},
		{"pending with a NULL key", "pending", nil, &grant, ""},
		{"reconsent with a NULL key", "reconsent", nil, nil, ""},
		{"active with a key and a grant", "active", key(), &grant, ""},
	} {
		_, err := h.super.Exec(ctx, erpConnInsert, a, ent, "fake", c.key, c.status, c.grant)
		if c.want == "" {
			failIfErpUndefined(t, c.name, err)
			if err != nil {
				t.Errorf("%s: want accepted, got %v", c.name, err)
			} else {
				accepted++
			}
			continue
		}
		erpWantErr(t, err, "23514", c.want, c.name)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE tenant_id = $1`, a); n != accepted || n == 0 {
		t.Errorf("rows after the cases = %d, want the %d accepted", n, accepted)
	}

	// The default status is a valid one. A second client: the first already holds its one keyless pending row.
	other := erpEntity(t, a, "erp-checks-default")
	var status string
	if err := h.super.QueryRow(ctx,
		`INSERT INTO erp_connections (tenant_id, entity_id, system) VALUES ($1, $2, 'fake') RETURNING status`, a, other).Scan(&status); err != nil || status != "pending" {
		t.Errorf("default status = %q (err %v), want pending", status, err)
	}

	for _, kind := range []string{"oauth_refresh", "client_credentials", "api_key"} {
		if _, err := h.super.Exec(ctx, erpGrantInsert, a, "fake", kind); err != nil {
			t.Errorf("grant kind %q: want accepted, got %v", kind, err)
		}
	}
	_, err := h.super.Exec(ctx, erpGrantInsert, a, "fake", "password")
	erpWantErr(t, err, "23514", "erp_grants_kind_check", "grant kind password")
	_, err = h.super.Exec(ctx, erpGrantInsert, a, "fake", "")
	erpWantErr(t, err, "23514", "erp_grants_kind_check", "grant with an empty kind")
	// 1 from the setup + 3 accepted kinds.
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_grants WHERE tenant_id = $1`, a); n != 4 {
		t.Errorf("grants after the cases = %d, want 4", n)
	}
}

// A missing privilege and a policy refusal are both 42501 on the wire, so the catalog answers here.
// Column grants are listed per column: a table-level grant would also answer true to every column probe.
func TestRLS_ErpTablesGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireErpTables(t, h.super)

	cases := []struct {
		table      string
		privs      map[string]bool
		acl        string
		updateCols []string // the columns invoice_app may UPDATE through a column grant, sorted
	}{
		{"erp_connections", map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": false, "TRUNCATE": false},
			"invoice_app:INSERT,invoice_app:SELECT,invoice_app:UPDATE", nil},
		{"erp_grants", map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": false, "DELETE": true, "TRUNCATE": false},
			"invoice_app:DELETE,invoice_app:INSERT,invoice_app:SELECT", []string{"expires_at", "updated_at", "version"}},
		{"erp_oauth_states", map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": false, "DELETE": true, "TRUNCATE": false},
			"invoice_app:DELETE,invoice_app:INSERT,invoice_app:SELECT", []string{"used_at"}},
	}
	for _, c := range cases {
		var reg *string
		if err := h.super.QueryRow(ctx, `SELECT to_regclass($1)::text`, "public."+c.table).Scan(&reg); err != nil {
			t.Fatalf("to_regclass(public.%s): %v", c.table, err)
		}
		if reg == nil {
			t.Errorf("public.%s does not exist — the erp_token_custody migration is not applied yet", c.table)
			continue
		}
		for priv, want := range c.privs {
			var got bool
			if err := h.super.QueryRow(ctx, `SELECT has_table_privilege('invoice_app', $1, $2)`, "public."+c.table, priv).Scan(&got); err != nil {
				t.Fatalf("has_table_privilege(invoice_app, %s, %s): %v", c.table, priv, err)
			}
			if got != want {
				t.Errorf("has_table_privilege(invoice_app, %s, %s) = %v, want %v", c.table, priv, got, want)
			}
		}

		// The probes miss REFERENCES, TRIGGER and any other grantee: read the ACL.
		rows, err := h.super.Query(ctx,
			`SELECT coalesce(r.rolname, 'PUBLIC') || ':' || a.privilege_type
			   FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a
			   LEFT JOIN pg_roles r ON r.oid = a.grantee
			  WHERE c.oid = to_regclass($1) AND a.grantee <> c.relowner
			  ORDER BY 1`, "public."+c.table)
		if err != nil {
			t.Fatalf("aclexplode(%s): %v", c.table, err)
		}
		var acl []string
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err != nil {
				rows.Close()
				t.Fatalf("scan acl: %v", err)
			}
			acl = append(acl, g)
		}
		rows.Close()
		if got := strings.Join(acl, ","); got != c.acl {
			t.Errorf("%s non-owner ACL = %q, want %q", c.table, got, c.acl)
		}

		// Column ACLs: exactly the UPDATE columns, to invoice_app and nobody else.
		crows, err := h.super.Query(ctx,
			`SELECT att.attname || ':' || coalesce(r.rolname, 'PUBLIC') || ':' || a.privilege_type
			   FROM pg_attribute att CROSS JOIN LATERAL aclexplode(att.attacl) a
			   LEFT JOIN pg_roles r ON r.oid = a.grantee
			  WHERE att.attrelid = to_regclass($1) AND att.attnum > 0 AND att.attacl IS NOT NULL
			  ORDER BY 1`, "public."+c.table)
		if err != nil {
			t.Fatalf("aclexplode(attacl of %s): %v", c.table, err)
		}
		var colACL []string
		for crows.Next() {
			var g string
			if err := crows.Scan(&g); err != nil {
				crows.Close()
				t.Fatalf("scan column acl: %v", err)
			}
			colACL = append(colACL, g)
		}
		crows.Close()
		var wantColACL []string
		for _, col := range c.updateCols {
			wantColACL = append(wantColACL, col+":invoice_app:UPDATE")
		}
		if !slices.Equal(colACL, wantColACL) {
			t.Errorf("%s column ACLs = %v, want %v", c.table, colACL, wantColACL)
		}

		// Per column: UPDATE only where granted (a table-level UPDATE grants every column), SELECT and INSERT everywhere.
		var cols []string
		crow, err := h.super.Query(ctx, `SELECT attname FROM pg_attribute WHERE attrelid = to_regclass($1) AND attnum > 0 AND NOT attisdropped ORDER BY 1`, "public."+c.table)
		if err != nil {
			t.Fatalf("columns of %s: %v", c.table, err)
		}
		for crow.Next() {
			var n string
			if err := crow.Scan(&n); err != nil {
				crow.Close()
				t.Fatalf("scan column: %v", err)
			}
			cols = append(cols, n)
		}
		crow.Close()
		if len(cols) == 0 {
			t.Fatalf("%s has no columns in pg_attribute", c.table)
		}
		for _, col := range cols {
			for priv, want := range map[string]bool{
				"SELECT": true, "INSERT": true,
				"UPDATE": c.privs["UPDATE"] || slices.Contains(c.updateCols, col),
			} {
				var got bool
				if err := h.super.QueryRow(ctx, `SELECT has_column_privilege('invoice_app', $1, $2, $3)`, "public."+c.table, col, priv).Scan(&got); err != nil {
					t.Fatalf("has_column_privilege(invoice_app, %s.%s, %s): %v", c.table, col, priv, err)
				}
				if got != want {
					t.Errorf("has_column_privilege(invoice_app, %s.%s, %s) = %v, want %v", c.table, col, priv, got, want)
				}
			}
		}
	}
}

// Disconnect keeps the row, so the grant is absent: the privilege, not the policy, refuses.
func TestRLS_ErpConnectionsDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	a := erpTenant(t)
	ea := erpEntity(t, a, "erp-del")
	id := erpSeedConn(t, a, ea, "fake", erpStr("k1"), nil, "pending")

	err := erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), `DELETE FROM erp_connections WHERE id = $1`, id)
		return e
	})
	failIfErpUndefined(t, "DELETE of a connection", err)
	erpWantErr(t, err, "42501", "", "invoice_app deleting its own connection")
	if msg := pgMessage(err); !strings.Contains(msg, "permission denied for table erp_connections") {
		t.Errorf("DELETE refusal message = %q, want the missing table privilege, not a policy", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE id = $1`, id); n != 1 {
		t.Errorf("connection rows after the refused DELETE = %d, want 1", n)
	}
}

// The refresh compare-and-swap may move version, expires_at and updated_at; secret_ref is written at insert only.
func TestRLS_ErpGrantsUpdateLimitedToTheSwapColumns(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a, b := erpTenant(t), erpTenant(t)
	id, bid := erpSeedGrant(t, a), erpSeedGrant(t, b)

	// The swap columns: one row, and the counter moved.
	var n int64
	err := erpApp(a, nil, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE erp_grants SET version = version + 1, expires_at = now(), updated_at = now() WHERE id = $1`, id)
		n = tag.RowsAffected()
		return e
	})
	failIfErpUndefined(t, "UPDATE of a grant", err)
	if err != nil || n != 1 {
		t.Fatalf("the swap UPDATE of the own grant = %d rows (err %v), want 1", n, err)
	}
	var version int64
	if err := h.super.QueryRow(ctx, `SELECT version FROM erp_grants WHERE id = $1`, id).Scan(&version); err != nil || version != 2 {
		t.Errorf("grant version after the swap = %d (err %v), want 2", version, err)
	}

	// Every other column: the privilege refuses, not a policy.
	for _, set := range []string{
		"secret_ref = 'x'", "tenant_id = '" + b + "'", "id = gen_random_uuid()", "system = 'other'", "kind = 'api_key'", "created_at = now()",
	} {
		err := erpApp(a, nil, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE erp_grants SET `+set+` WHERE id = $1`, id)
			return e
		})
		erpWantErr(t, err, "42501", "", "invoice_app running UPDATE erp_grants SET "+set)
		if msg := pgMessage(err); !strings.Contains(msg, "permission denied for table erp_grants") {
			t.Errorf("UPDATE SET %s refusal message = %q, want the missing column privilege, not a policy", set, msg)
		}
	}
	var secretRef *string
	if err := h.super.QueryRow(ctx, `SELECT secret_ref FROM erp_grants WHERE id = $1`, id).Scan(&secretRef); err != nil || secretRef != nil {
		t.Errorf("grant secret_ref after the refused UPDATEs = %v (err %v), want NULL", secretRef, err)
	}

	// Another tenant's grant: the policy hides it, so the swap moves no row.
	err = erpApp(a, nil, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE erp_grants SET version = version + 1 WHERE id = $1`, bid)
		n = tag.RowsAffected()
		return e
	})
	if err != nil || n != 0 {
		t.Errorf("the swap UPDATE of another tenant's grant = %d rows (err %v), want 0", n, err)
	}
}

// The grant FK is NO ACTION: RESTRICT would answer 23001 and the tenant cascade below would fail.
func TestRLS_ErpGrantInUseCannotBeDeleted(t *testing.T) {
	h := requireHarness(t)
	a := erpTenant(t)
	ea := erpEntity(t, a, "erp-inuse")
	grant := erpSeedGrant(t, a)
	conn := erpSeedConn(t, a, ea, "fake", erpStr("k1"), &grant, "pending")
	free := erpSeedGrant(t, a)

	deleteGrant := func(id string) error {
		return erpApp(a, nil, func(tx pgx.Tx) error {
			_, e := tx.Exec(context.Background(), `DELETE FROM erp_grants WHERE id = $1`, id)
			return e
		})
	}
	// Control: an unreferenced grant is deletable, so the refusal below is the reference.
	if err := deleteGrant(free); err != nil {
		t.Fatalf("DELETE of an unreferenced grant: %v", err)
	}
	erpWantErr(t, deleteGrant(grant), "23503", "erp_connections_tenant_grant_fk", "deleting a grant a connection references")
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_grants WHERE id = $1`, grant); n != 1 {
		t.Errorf("grant rows after the refused DELETE = %d, want 1", n)
	}

	err := erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), `UPDATE erp_connections SET grant_id = NULL WHERE id = $1`, conn)
		return e
	})
	if err != nil {
		t.Fatalf("clearing the connection's grant_id: %v", err)
	}
	if err := deleteGrant(grant); err != nil {
		t.Errorf("DELETE of the grant once no connection references it: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_grants WHERE id = $1`, grant); n != 0 {
		t.Errorf("grant rows after the released DELETE = %d, want 0", n)
	}
}

func TestRLS_ErpConnectionsCascadeWithClient(t *testing.T) {
	h := requireHarness(t)
	a := erpTenant(t)
	e1, e2 := erpEntity(t, a, "erp-casc-1"), erpEntity(t, a, "erp-casc-2")
	grant := erpSeedGrant(t, a)
	erpSeedConn(t, a, e1, "fake", erpStr("k1"), &grant, "pending")
	erpSeedConn(t, a, e1, "fake", erpStr("k2"), &grant, "pending")
	erpSeedConn(t, a, e2, "fake", erpStr("k1"), &grant, "pending")
	erpSeedConn(t, a, e2, "fake", erpStr("k2"), nil, "pending")
	erpSeedConn(t, a, e2, "fake", erpStr("k3"), nil, "pending")
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE tenant_id = $1`, a); n != 5 {
		t.Fatalf("connections seeded = %d, want 5", n)
	}

	if _, err := h.super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, e1); err != nil {
		t.Fatalf("delete client e1: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE entity_id = $1`, e1); n != 0 {
		t.Errorf("connections of the deleted client = %d, want 0", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE entity_id = $1`, e2); n != 3 {
		t.Errorf("connections of the kept client = %d, want 3", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_grants WHERE id = $1`, grant); n != 1 {
		t.Errorf("the shared grant after a client delete = %d rows, want it kept", n)
	}
}

// Both tables carry a tenant FK and the grant FK has no ON DELETE: one statement must still remove everything.
func TestRLS_ErpTablesCascadeWithTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a, b := erpTenant(t), erpTenant(t)
	ea, eb := erpEntity(t, a, "erp-tcasc-a"), erpEntity(t, b, "erp-tcasc-b")
	ga, gb := erpSeedGrant(t, a), erpSeedGrant(t, b)
	erpSeedConn(t, a, ea, "fake", erpStr("k1"), &ga, "active")
	erpSeedConn(t, a, ea, "fake", erpStr("k2"), &ga, "pending")
	erpSeedConn(t, b, eb, "fake", erpStr("k1"), &gb, "active")
	for _, table := range []string{"erp_grants", "erp_connections"} {
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, a); n == 0 {
			t.Fatalf("%s rows seeded for the tenant = 0, want some", table)
		}
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, a); err != nil {
		t.Fatalf("delete the tenant: %v", err)
	}
	for _, table := range []string{"erp_grants", "erp_connections"} {
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, a); n != 0 {
			t.Errorf("%s rows after the tenant delete = %d, want 0", table, n)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, b); n != 1 {
			t.Errorf("%s rows of the other tenant = %d, want 1", table, n)
		}
	}
}

// ---- migration ------------------------------------------------------------

func erpPublicTables(t *testing.T, q erpQuerier, phase string) []string {
	t.Helper()
	var out []string
	err := q.QueryRow(context.Background(),
		`SELECT coalesce(array_agg(c.relname::text ORDER BY c.relname), '{}')
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')`).Scan(&out)
	if err != nil {
		t.Fatalf("list public tables %s: %v", phase, err)
	}
	return out
}

// The shipped Down drops exactly the two tables and the shipped Up rebuilds them.
// Runs on the migrator inside a transaction that is always rolled back, so no other test sees the tables vanish.
// The token-custody migration is undone first and redone last: erp_oauth_states references erp_connections.
func TestRLS_ErpConnectionsMigrationIsReversible(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, erpMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", erpMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching %s = %v, want exactly 1 — the erp_connections migration is not written yet", erpMigrationGlob, matches)
	}
	migration := matches[0]
	requireErpTables(t, h.super)

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	whole := erpPublicTables(t, tx, "before any Down")
	custody := erpCustodyMigration(t)
	if custody != "" {
		if _, err := tx.Exec(ctx, auditEntitySectionOf(t, custody, "Down")); err != nil {
			t.Fatalf("Down body of %s failed: %v", custody, err)
		}
	}
	before := erpPublicTables(t, tx, "before the Down")
	for _, table := range []string{"erp_grants", "erp_connections"} {
		if !slices.Contains(before, table) {
			t.Fatalf("%s is not among the public tables before the Down: %v", table, before)
		}
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Down")); err != nil {
		t.Fatalf("Down body of %s failed: %v", migration, err)
	}
	after := erpPublicTables(t, tx, "after the Down")
	var dropped []string
	for _, table := range before {
		if !slices.Contains(after, table) {
			dropped = append(dropped, table)
		}
	}
	if want := []string{"erp_connections", "erp_grants"}; !slices.Equal(dropped, want) {
		t.Errorf("tables the Down dropped = %v, want exactly %v", dropped, want)
	}
	if len(after) != len(before)-2 {
		t.Errorf("public tables after the Down = %d, want %d: it created or dropped something else", len(after), len(before)-2)
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", migration, err)
	}
	requireErpTables(t, tx)
	for _, p := range erpShapeProblems(tx) {
		t.Errorf("after Down then Up: %s", p)
	}
	for _, p := range erpConstraintProblems(tx) {
		t.Errorf("after Down then Up: %s", p)
	}
	var forced bool
	var policies int
	if err := tx.QueryRow(ctx,
		`SELECT bool_and(c.relrowsecurity AND c.relforcerowsecurity),
		        (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename IN ('erp_grants', 'erp_connections'))
		   FROM pg_class c WHERE c.oid IN (to_regclass('public.erp_grants'), to_regclass('public.erp_connections'))`,
	).Scan(&forced, &policies); err != nil || !forced || policies != 2 {
		t.Errorf("after Down then Up: FORCE RLS on both = %v, policies = %d (err %v), want true and 2", forced, policies, err)
	}
	var grants bool
	if err := tx.QueryRow(ctx,
		`SELECT has_table_privilege('invoice_app', 'public.erp_connections', 'SELECT, INSERT, UPDATE')
		    AND NOT has_table_privilege('invoice_app', 'public.erp_connections', 'DELETE')
		    AND has_table_privilege('invoice_app', 'public.erp_grants', 'SELECT, INSERT, DELETE')
		    AND NOT has_table_privilege('invoice_app', 'public.erp_grants', 'UPDATE')`).Scan(&grants); err != nil || !grants {
		t.Errorf("after Down then Up: invoice_app holds the Design's verbs = %v (err %v), want true", grants, err)
	}

	if custody != "" {
		if _, err := tx.Exec(ctx, auditEntitySectionOf(t, custody, "Up")); err != nil {
			t.Fatalf("Up body of %s failed: %v", custody, err)
		}
	}

	// Nothing committed: once rolled back, the tables the suite already relies on are exactly as before.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := erpPublicTables(t, h.super, "after the rollback"); !slices.Equal(got, whole) {
		t.Errorf("public tables after the rollback = %v, want the %v seen before the Down", got, whole)
	}
}
