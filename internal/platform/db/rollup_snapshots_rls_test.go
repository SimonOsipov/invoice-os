// Storage suite for rollup_snapshots: one row per company per WAT day.
//
// Rows are seeded per test, never in the shared harness.seed(). A skip is a failure under the CI gate.
package db_test

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const rsUpsert = `INSERT INTO rollup_snapshots (tenant_id, entity_id, day, total, readiness_num, needs_attention, not_submitted, vat_kobo)
	VALUES ($1, $2, $3::date, $4, $5, $6, $7, $8)
	ON CONFLICT (tenant_id, entity_id, day) DO UPDATE SET
	    total = EXCLUDED.total, readiness_num = EXCLUDED.readiness_num, needs_attention = EXCLUDED.needs_attention,
	    not_submitted = EXCLUDED.not_submitted, vat_kobo = EXCLUDED.vat_kobo`

const rsPlainInsert = `INSERT INTO rollup_snapshots (tenant_id, entity_id, day, total, readiness_num, needs_attention, not_submitted, vat_kobo)
	VALUES ($1, $2, $3::date, $4, 0, 0, 0, 0)`

// failIfUndefinedRollupSnapshots turns a pre-migration 42P01 / 42703 into an explicit message.
func failIfUndefinedRollupSnapshots(t *testing.T, what string, err error) {
	t.Helper()
	if c := pgCode(err); c == "42P01" || c == "42703" {
		t.Fatalf("%s: %s — the rollup_snapshots migration is not applied yet: %v", what, c, err)
	}
}

// requireRollupSnapshotsTable fails with an explicit message while the table is absent.
func requireRollupSnapshotsTable(t *testing.T, q querier) {
	t.Helper()
	var reg *string
	if err := q.QueryRow(context.Background(), `SELECT to_regclass('public.rollup_snapshots')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass('public.rollup_snapshots'): %v", err)
	}
	if reg == nil {
		t.Fatal("public.rollup_snapshots does not exist — the rollup_snapshots migration is not applied yet")
	}
}

// rsSeed inserts one row as the superuser (BYPASSRLS) and returns its cleanup.
func rsSeed(t *testing.T, tenantID, entityID, day string, total int) func() {
	t.Helper()
	_, err := h.super.Exec(context.Background(), rsPlainInsert, tenantID, entityID, day, total)
	failIfUndefinedRollupSnapshots(t, "seed rollup_snapshots", err)
	if err != nil {
		t.Fatalf("seed rollup_snapshots: %v", err)
	}
	return func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM rollup_snapshots WHERE tenant_id = $1 AND entity_id = $2 AND day = $3::date`,
			tenantID, entityID, day)
	}
}

// rsThrowawayTenant creates a tenant so deleting it cannot touch the shared harness tenants.
func rsThrowawayTenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO tenants (id, name) VALUES ($1, 'rollup-snapshots throwaway tenant')`, id); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

// rsVisible returns the sorted entity ids among want that tenant sees under scope (nil = unset).
func rsVisible(t *testing.T, tenant string, scope *string, want ...string) []string {
	t.Helper()
	ctx := context.Background()
	var got []string
	err := db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
		if scope != nil {
			if _, e := tx.Exec(ctx, `SELECT set_config('app.entity_scope', $1, true)`, *scope); e != nil {
				return e
			}
		}
		rows, e := tx.Query(ctx,
			`SELECT entity_id::text FROM rollup_snapshots WHERE entity_id::text = ANY($1) ORDER BY 1`, want)
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
	failIfUndefinedRollupSnapshots(t, "read rollup_snapshots", err)
	if err != nil {
		t.Fatalf("read rollup_snapshots (scope %v): %v", scope, err)
	}
	return got
}

func TestRLS_RollupSnapshotsColumnShape(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireRollupSnapshotsTable(t, h.super)

	want := map[string]string{
		"tenant_id": "uuid", "entity_id": "uuid", "day": "date", "total": "integer",
		"readiness_num": "integer", "needs_attention": "integer", "not_submitted": "integer", "vat_kobo": "bigint",
	}
	rows, err := h.super.Query(ctx,
		`SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = 'rollup_snapshots'`)
	if err != nil {
		t.Fatalf("read information_schema.columns: %v", err)
	}
	got := map[string]string{}
	for rows.Next() {
		var name, typ, nullable string
		var def *string
		if e := rows.Scan(&name, &typ, &nullable, &def); e != nil {
			rows.Close()
			t.Fatalf("scan column: %v", e)
		}
		got[name] = typ
		if nullable != "NO" {
			t.Errorf("rollup_snapshots.%s is nullable, want NOT NULL", name)
		}
		if def != nil {
			t.Errorf("rollup_snapshots.%s default = %q, want none (the worker writes every value)", name, *def)
		}
	}
	rows.Close()
	if len(got) != len(want) {
		t.Errorf("rollup_snapshots columns = %v, want exactly %v", got, want)
	}
	for col, typ := range want {
		if got[col] != typ {
			t.Errorf("rollup_snapshots.%s type = %q, want %q", col, got[col], typ)
		}
	}

	var pk []string
	if err := h.super.QueryRow(ctx,
		`SELECT array(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum, ord)
		                JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		               ORDER BY k.ord)
		   FROM pg_constraint c
		  WHERE c.conrelid = 'public.rollup_snapshots'::regclass AND c.conname = 'rollup_snapshots_pk' AND c.contype = 'p'`,
	).Scan(&pk); err != nil {
		t.Fatalf("read rollup_snapshots_pk: %v", err)
	}
	if strings.Join(pk, ",") != "tenant_id,entity_id,day" {
		t.Errorf("rollup_snapshots_pk columns = %v, want [tenant_id entity_id day]", pk)
	}

	var def string
	if err := h.super.QueryRow(ctx,
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		  WHERE conrelid = 'public.rollup_snapshots'::regclass AND conname = 'rollup_snapshots_tenant_entity_fk'`,
	).Scan(&def); err != nil {
		t.Fatalf("read rollup_snapshots_tenant_entity_fk: %v", err)
	}
	for _, frag := range []string{"(tenant_id, entity_id)", "business_entities(tenant_id, id)", "ON DELETE CASCADE"} {
		if !strings.Contains(def, frag) {
			t.Errorf("rollup_snapshots_tenant_entity_fk = %q, want it to contain %q", def, frag)
		}
	}
}

// ENABLE alone would let the owner bypass the policy; a policy `TO invoice_app` would leave
// the migrator with none. Both pass every behavioural case, so the catalog is read here.
func TestRLS_RollupSnapshotsForceRLSAndPoliciesDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireRollupSnapshotsTable(t, h.super)

	var enabled, forced bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.rollup_snapshots')`,
	).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read pg_class for rollup_snapshots: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("rollup_snapshots relrowsecurity/relforcerowsecurity = %v/%v, want true/true", enabled, forced)
	}

	rows, err := h.super.Query(ctx,
		`SELECT policyname, roles::text[], coalesce(qual, '') FROM pg_policies
		  WHERE schemaname = 'public' AND tablename = 'rollup_snapshots'`)
	if err != nil {
		t.Fatalf("query pg_policies: %v", err)
	}
	type policy struct {
		roles []string
		qual  string
	}
	got := map[string]policy{}
	for rows.Next() {
		var name string
		var p policy
		if e := rows.Scan(&name, &p.roles, &p.qual); e != nil {
			rows.Close()
			t.Fatalf("scan pg_policies row: %v", e)
		}
		got[name] = p
	}
	rows.Close()
	if len(got) != 1 {
		t.Errorf("policies on rollup_snapshots = %d (%v), want exactly 1 (tenant_isolation)", len(got), got)
	}
	iso, ok := got["tenant_isolation"]
	if !ok {
		t.Fatal("no tenant_isolation policy on rollup_snapshots")
	}
	if strings.Join(iso.roles, ",") != "public" {
		t.Errorf("tenant_isolation roles = %v, want [public] (no TO clause)", iso.roles)
	}
	for _, guc := range []string{"app.current_tenant", "app.entity_scope"} {
		if !strings.Contains(iso.qual, guc) {
			t.Errorf("tenant_isolation qual = %q, want a comparison against %s", iso.qual, guc)
		}
	}
}

func TestRLS_RollupSnapshotsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entA, cleanupEA := seedBusinessEntity(t, h.tenantA, "rs-select-a")
	defer cleanupEA()
	entB, cleanupEB := seedBusinessEntity(t, h.tenantB, "rs-select-b")
	defer cleanupEB()
	defer rsSeed(t, h.tenantA, entA, "2026-10-21", 1)()
	defer rsSeed(t, h.tenantB, entB, "2026-10-21", 1)()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, entA); n != 1 {
			t.Errorf("A's own row visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, entB); n != 0 {
			t.Errorf("B's row visible to A = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

func TestRLS_RollupSnapshotsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	ent, cleanupE := seedBusinessEntity(t, h.tenantA, "rs-noguc")
	defer cleanupE()
	defer rsSeed(t, h.tenantA, ent, "2026-10-21", 1)()

	// Control: the zero below means nothing unless the row is there to hide.
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, ent); n != 1 {
		t.Fatalf("superuser sees %d seeded rows, want 1", n)
	}
	// A pooled connection keeps app.current_tenant = '' after a tenant tx, so a policy
	// without nullif() raises 22P02 there instead of returning zero rows.
	for _, emptyGUC := range []bool{false, true} {
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
		n, err := scanCount(ctx, tx, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, ent)
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatalf("SELECT with no tenant context (empty GUC %v): %v", emptyGUC, err)
		}
		if n != 0 {
			t.Errorf("rows visible with no tenant set (empty GUC %v) = %d, want 0", emptyGUC, n)
		}
	}
}

func TestRLS_RollupSnapshotsLimitedScopeSeesOnlyItsClients(t *testing.T) {
	h := requireHarness(t)

	x, cleanupX := seedBusinessEntity(t, h.tenantA, "rs-scope-x")
	defer cleanupX()
	y, cleanupY := seedBusinessEntity(t, h.tenantA, "rs-scope-y")
	defer cleanupY()
	defer rsSeed(t, h.tenantA, x, "2026-10-21", 1)()
	defer rsSeed(t, h.tenantA, y, "2026-10-21", 1)()

	both := []string{x, y}
	slices.Sort(both)
	for _, c := range []struct {
		scope string
		want  []string
	}{
		{"{" + x + "}", []string{x}},
		{"", both},
		{"*", both},
		{"{}", nil},
	} {
		scope := c.scope
		if got := rsVisible(t, h.tenantA, &scope, x, y); !slices.Equal(got, c.want) {
			t.Errorf("scope %q: visible entities = %v, want %v", c.scope, got, c.want)
		}
	}
}

// The control insert separates the policy from a missing INSERT grant: both answer 42501.
func TestRLS_RollupSnapshotsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entA, cleanupEA := seedBusinessEntity(t, h.tenantA, "rs-xinsert-a")
	defer cleanupEA()
	entB, cleanupEB := seedBusinessEntity(t, h.tenantB, "rs-xinsert-b")
	defer cleanupEB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, rsPlainInsert, h.tenantA, entA, "2026-10-21", 1)
		return e
	})
	failIfUndefinedRollupSnapshots(t, "control INSERT in the own tenant", err)
	if err != nil {
		t.Fatalf("control INSERT in the own tenant: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(ctx, `DELETE FROM rollup_snapshots WHERE entity_id = $1`, entA)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, rsPlainInsert, h.tenantB, entB, "2026-10-21", 1)
		return e
	})
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, entB); n != 0 {
		t.Errorf("rows after the refused INSERT = %d, want 0", n)
	}
}

func TestRLS_RollupSnapshotsInsertOutsideTheScopeRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	x, cleanupX := seedBusinessEntity(t, h.tenantA, "rs-scopeins-x")
	defer cleanupX()
	y, cleanupY := seedBusinessEntity(t, h.tenantA, "rs-scopeins-y")
	defer cleanupY()

	insert := func(entity string) error {
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT set_config('app.entity_scope', $1, true)`, "{"+x+"}"); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, rsPlainInsert, h.tenantA, entity, "2026-10-21", 1)
			return e
		})
	}
	err := insert(x)
	failIfUndefinedRollupSnapshots(t, "control INSERT inside the scope", err)
	if err != nil {
		t.Fatalf("control INSERT inside the scope: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(ctx, `DELETE FROM rollup_snapshots WHERE entity_id = $1`, x)
	}()

	assertRLSViolation(t, insert(y))
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, y); n != 0 {
		t.Errorf("rows for the out-of-scope entity after the refused INSERT = %d, want 0", n)
	}
}

// The grant allows UPDATE, so the policy alone keeps another tenant, or a client out of scope, from editing a row.
func TestRLS_RollupSnapshotsUpdateIsTenantAndScopeBound(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	x, cleanupX := seedBusinessEntity(t, h.tenantA, "rs-upd-x")
	defer cleanupX()
	y, cleanupY := seedBusinessEntity(t, h.tenantA, "rs-upd-y")
	defer cleanupY()
	defer rsSeed(t, h.tenantA, x, "2026-10-21", 1)()
	defer rsSeed(t, h.tenantA, y, "2026-10-21", 1)()

	update := func(tenant, scope, entity string) (int64, error) {
		var n int64
		err := db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
			if scope != "" {
				if _, e := tx.Exec(ctx, `SELECT set_config('app.entity_scope', $1, true)`, scope); e != nil {
					return e
				}
			}
			tag, e := tx.Exec(ctx, `UPDATE rollup_snapshots SET total = 99 WHERE entity_id = $1`, entity)
			n = tag.RowsAffected()
			return e
		})
		return n, err
	}
	// Control: the own tenant inside its scope updates the row, so the zeros below are the policy.
	if n, err := update(h.tenantA, "{"+x+"}", x); err != nil || n != 1 {
		t.Fatalf("control UPDATE in the own tenant and scope = %d rows (err %v), want 1", n, err)
	}
	if n, err := update(h.tenantB, "", x); err != nil || n != 0 {
		t.Errorf("UPDATE of A's row from tenant B = %d rows (err %v), want 0", n, err)
	}
	if n, err := update(h.tenantA, "{"+x+"}", y); err != nil || n != 0 {
		t.Errorf("UPDATE of an out-of-scope client's row = %d rows (err %v), want 0", n, err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1 AND total = 99`, y); n != 0 {
		t.Errorf("out-of-scope row changed: %d rows have total 99, want 0", n)
	}

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE rollup_snapshots SET tenant_id = $1 WHERE entity_id = $2`, h.tenantB, y)
		return e
	})
	assertRLSViolation(t, err)
}

// RI checks bypass RLS, so only a composite FK on (tenant_id, entity_id) refuses B's entity.
func TestRLS_RollupSnapshotsCrossTenantEntityRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entA, cleanupEA := seedBusinessEntity(t, h.tenantA, "rs-xentity-a")
	defer cleanupEA()
	entB, cleanupEB := seedBusinessEntity(t, h.tenantB, "rs-xentity-b")
	defer cleanupEB()
	defer rsSeed(t, h.tenantA, entA, "2026-10-21", 1)() // control: the own entity is accepted

	_, err := h.super.Exec(ctx, rsPlainInsert, h.tenantA, entB, "2026-10-21", 1)
	failIfUndefinedRollupSnapshots(t, "cross-tenant entity INSERT", err)
	if err == nil {
		t.Fatal("naming tenant B's entity under tenant A succeeded, want 23503")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("SQLSTATE = %q, want 23503: %v", code, err)
	}
	if name := pgConstraint(err); name != "rollup_snapshots_tenant_entity_fk" {
		t.Errorf("constraint = %q, want rollup_snapshots_tenant_entity_fk", name)
	}
}

func TestRLS_RollupSnapshotsDuplicateDayRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	ent, cleanupE := seedBusinessEntity(t, h.tenantA, "rs-dup")
	defer cleanupE()
	defer rsSeed(t, h.tenantA, ent, "2026-10-21", 1)()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, rsPlainInsert, h.tenantA, ent, "2026-10-21", 2)
		return e
	})
	if err == nil {
		t.Fatal("duplicate (tenant, entity, day) INSERT succeeded, want 23505")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("SQLSTATE = %q, want 23505: %v", code, err)
	}
	if name := pgConstraint(err); name != "rollup_snapshots_pk" {
		t.Errorf("constraint = %q, want the primary key rollup_snapshots_pk", name)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, ent); n != 1 {
		t.Errorf("rows after the refused duplicate = %d, want 1", n)
	}
}

// ON CONFLICT DO UPDATE needs SELECT, INSERT and UPDATE together: this is the worker's real statement.
func TestRLS_RollupSnapshotsUpsertReplacesTheDay(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	ent, cleanupE := seedBusinessEntity(t, h.tenantA, "rs-upsert")
	defer cleanupE()
	defer rsSeed(t, h.tenantA, ent, "2026-10-21", 1)()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, rsUpsert, h.tenantA, ent, "2026-10-21", 2, 3, 4, 5, 6); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, rsUpsert, h.tenantA, ent, "2026-10-22", 9, 8, 7, 6, 5)
		return e
	})
	failIfUndefinedRollupSnapshots(t, "upsert as invoice_app", err)
	if err != nil {
		t.Fatalf("upsert as invoice_app in the own tenant: %v", err)
	}

	var n, total, num, att, notSub int
	var vat int64
	if err := h.super.QueryRow(context.Background(),
		`SELECT count(*), max(total), max(readiness_num), max(needs_attention), max(not_submitted), max(vat_kobo)
		   FROM rollup_snapshots WHERE entity_id = $1 AND day = '2026-10-21'`, ent,
	).Scan(&n, &total, &num, &att, &notSub, &vat); err != nil {
		t.Fatalf("read the replaced row: %v", err)
	}
	if n != 1 || total != 2 || num != 3 || att != 4 || notSub != 5 || vat != 6 {
		t.Errorf("row for the day = %d rows, total %d num %d attention %d not_submitted %d vat %d; want 1 row, 2 3 4 5 6",
			n, total, num, att, notSub, vat)
	}
	if got := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, ent); got != 2 {
		t.Errorf("rows for the entity after upserting a second day = %d, want 2", got)
	}
	defer func() { _, _ = h.super.Exec(ctx, `DELETE FROM rollup_snapshots WHERE entity_id = $1`, ent) }()
}

// Deleting an entity or a tenant removes its snapshot rows and only those.
func TestRLS_RollupSnapshotsCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := rsThrowawayTenant(t)
	x, _ := seedBusinessEntity(t, tenantID, "rs-cascade-x")
	y, _ := seedBusinessEntity(t, tenantID, "rs-cascade-y")
	rsSeed(t, tenantID, x, "2026-10-20", 1)
	rsSeed(t, tenantID, x, "2026-10-21", 1)
	rsSeed(t, tenantID, y, "2026-10-21", 1)
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE tenant_id = $1`, tenantID); n != 3 {
		t.Fatalf("rows seeded = %d, want 3", n)
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM business_entities WHERE id = $1`, x); err != nil {
		t.Fatalf("delete entity X: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, x); n != 0 {
		t.Errorf("rows of the deleted entity = %d, want 0", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE entity_id = $1`, y); n != 1 {
		t.Errorf("rows of the kept entity = %d, want 1", n)
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM rollup_snapshots WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("rows after the tenant delete = %d, want 0", n)
	}
}

// A missing privilege and a policy refusal are both 42501 on the wire, so the catalog answers here.
func TestRLS_RollupSnapshotsGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		priv string
		want bool
	}{
		{"SELECT", true}, {"INSERT", true}, {"UPDATE", true}, {"DELETE", false}, {"TRUNCATE", false},
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege('invoice_app', 'public.rollup_snapshots', $1)`, c.priv).Scan(&got)
		failIfUndefinedRollupSnapshots(t, "has_table_privilege(invoice_app, rollup_snapshots, "+c.priv+")", err)
		if err != nil {
			t.Fatalf("has_table_privilege(invoice_app, rollup_snapshots, %s): %v", c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(invoice_app, rollup_snapshots, %s) = %v, want %v", c.priv, got, c.want)
		}
	}

	// The probes miss REFERENCES, TRIGGER and any other grantee: read the ACL.
	rows, err := h.super.Query(ctx,
		`SELECT coalesce(r.rolname, 'PUBLIC') || ':' || a.privilege_type
		   FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a
		   LEFT JOIN pg_roles r ON r.oid = a.grantee
		  WHERE c.oid = to_regclass('public.rollup_snapshots') AND a.grantee <> c.relowner
		  ORDER BY 1`)
	if err != nil {
		t.Fatalf("aclexplode(rollup_snapshots): %v", err)
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
	if got, want := strings.Join(acl, ","), "invoice_app:INSERT,invoice_app:SELECT,invoice_app:UPDATE"; got != want {
		t.Errorf("rollup_snapshots non-owner ACL = %q, want %q", got, want)
	}
	var colACLs int
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM pg_attribute
		  WHERE attrelid = to_regclass('public.rollup_snapshots') AND attnum > 0 AND attacl IS NOT NULL`,
	).Scan(&colACLs); err != nil || colACLs != 0 {
		t.Errorf("rollup_snapshots columns with their own ACL = %d (err %v), want 0", colACLs, err)
	}
}

// The shipped Down removes the table and the shipped Up applies again over its result.
// Runs on the migrator inside a transaction that is always rolled back.
func TestRLS_RollupSnapshotsDownThenUpRoundTrips(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, "*_rollup_snapshots.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("migrations.FS *_rollup_snapshots.sql = %v (err %v), want exactly one file — the migration is not written yet", matches, err)
	}
	up := auditEntitySectionOf(t, matches[0], "Up")
	down := shippedDownStatements(t, "*_rollup_snapshots.sql")
	if len(down) == 0 {
		t.Fatal("the Down block holds no statement")
	}

	tx, err := h.mig.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}

	present := func(want int, phase string) {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname = 'rollup_snapshots'`).Scan(&n); err != nil {
			t.Fatalf("count rollup_snapshots %s: %v", phase, err)
		}
		if n != want {
			t.Fatalf("rollup_snapshots relations %s = %d, want %d", phase, n, want)
		}
	}
	present(1, "before the Down")
	for _, s := range down {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("Down statement %q: %v", s, err)
		}
	}
	present(0, "after the Down")
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("Up over the Down's result: %v", err)
	}
	present(1, "after the Up")

	var enabled, forced bool
	var policies int
	if err := tx.QueryRow(ctx,
		`SELECT c.relrowsecurity, c.relforcerowsecurity,
		        (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'rollup_snapshots')
		   FROM pg_class c WHERE c.oid = to_regclass('public.rollup_snapshots')`,
	).Scan(&enabled, &forced, &policies); err != nil {
		t.Fatalf("read RLS state after the Up: %v", err)
	}
	if !enabled || !forced || policies != 1 {
		t.Errorf("after the Up: relrowsecurity %v relforcerowsecurity %v policies %d, want true true 1", enabled, forced, policies)
	}
	var upsertable bool
	if err := tx.QueryRow(ctx,
		`SELECT has_table_privilege('invoice_app', 'public.rollup_snapshots', 'SELECT, INSERT, UPDATE')
		    AND NOT has_table_privilege('invoice_app', 'public.rollup_snapshots', 'DELETE')`,
	).Scan(&upsertable); err != nil || !upsertable {
		t.Errorf("after the Up: invoice_app holds SELECT, INSERT, UPDATE and not DELETE = %v (err %v), want true", upsertable, err)
	}
}
