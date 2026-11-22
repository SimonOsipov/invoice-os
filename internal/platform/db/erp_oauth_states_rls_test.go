// Storage suite for erp_oauth_states and the token-custody grants: shape, RLS, column grants, the keyless-pending index.
package db_test

import (
	"context"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const erpCustodyGlob = "*_erp_token_custody.sql"

const erpStateInsert = `INSERT INTO erp_oauth_states (tenant_id, state_hash, connection_id, user_id, expires_at)
	VALUES ($1, $2, $3, $4, now() + interval '10 minutes') RETURNING id::text`

// erpCustodyMigration returns the token-custody migration's name, or "" while it is not written.
func erpCustodyMigration(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, erpCustodyGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", erpCustodyGlob, err)
	}
	if len(matches) > 1 {
		t.Fatalf("migrations matching %s = %v, want at most 1", erpCustodyGlob, matches)
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// failIfStatesUndefined names the missing migration when the table or a column is absent.
func failIfStatesUndefined(t *testing.T, what string, err error) {
	t.Helper()
	if c := pgCode(err); c == "42P01" || c == "42703" {
		t.Fatalf("%s: %s — the erp_token_custody migration is not applied yet: %v", what, c, err)
	}
}

func requireStatesTable(t *testing.T, q erpQuerier) {
	t.Helper()
	var reg *string
	if err := q.QueryRow(context.Background(), `SELECT to_regclass('public.erp_oauth_states')::text`).Scan(&reg); err != nil {
		t.Fatalf("to_regclass(public.erp_oauth_states): %v", err)
	}
	if reg == nil {
		t.Fatal("public.erp_oauth_states does not exist — the erp_token_custody migration is not applied yet")
	}
}

// erpSeedState inserts one state as the superuser and returns its id.
func erpSeedState(t *testing.T, tenant, conn string) string {
	t.Helper()
	var id string
	err := h.super.QueryRow(context.Background(), erpStateInsert, tenant, tokenHash(t, 32), conn, uuid.NewString()).Scan(&id)
	failIfStatesUndefined(t, "seed erp_oauth_states", err)
	if err != nil {
		t.Fatalf("seed erp_oauth_states: %v", err)
	}
	return id
}

// erpTenantWithConn returns a throwaway tenant with one client and one keyed pending connection.
func erpTenantWithConn(t *testing.T, name string) (tenant, entity, conn string) {
	t.Helper()
	tenant = erpTenant(t)
	entity = erpEntity(t, tenant, name)
	conn = erpSeedConn(t, tenant, entity, "fake", erpStr("k1"), nil, "pending")
	return tenant, entity, conn
}

// ---- shape ----------------------------------------------------------------

var erpStatesShape = map[string]map[string]erpCol{
	"erp_oauth_states": {
		"tenant_id":     {"uuid", true, ""},
		"id":            {"uuid", true, "gen_random_uuid()"},
		"state_hash":    {"bytea", true, ""},
		"connection_id": {"uuid", true, ""},
		"user_id":       {"uuid", true, ""},
		"created_at":    {"timestamp with time zone", true, "now()"},
		"expires_at":    {"timestamp with time zone", true, ""},
		"used_at":       {"timestamp with time zone", false, ""},
	},
}

// The named constraints; the NOT NULLs are derived from erpStatesShape (PG18 names each <table>_<col>_not_null).
var erpStatesNamedConstraints = []string{
	"erp_oauth_states_pk", "erp_oauth_states_hash_uq", "erp_oauth_states_hash_len",
	"erp_oauth_states_tenant_connection_fk", "erp_oauth_states_tenant_id_fkey",
}

func erpStatesProblems(q erpQuerier) []string {
	ctx := context.Background()
	problems := erpShapeProblemsOf(q, erpStatesShape)

	want := slices.Clone(erpStatesNamedConstraints)
	for col, c := range erpStatesShape["erp_oauth_states"] {
		if c.notNull {
			want = append(want, "erp_oauth_states_"+col+"_not_null")
		}
	}
	sort.Strings(want)
	rows, err := q.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid = to_regclass('public.erp_oauth_states') ORDER BY conname`)
	if err != nil {
		return append(problems, "read pg_constraint: "+err.Error())
	}
	var got []string
	for rows.Next() {
		var n string
		if e := rows.Scan(&n); e != nil {
			rows.Close()
			return append(problems, "scan conname: "+e.Error())
		}
		got = append(got, n)
	}
	rows.Close()
	if !slices.Equal(got, want) {
		problems = append(problems, "constraints of erp_oauth_states = "+strings.Join(got, ",")+"\n  want "+strings.Join(want, ","))
	}

	defs := []struct {
		name string
		has  []string
	}{
		{"erp_oauth_states_tenant_id_fkey", []string{"FOREIGN KEY (tenant_id)", "REFERENCES tenants(id)", "ON DELETE CASCADE"}},
		// Composite: RI checks bypass RLS, so only the pair refuses another tenant's connection.
		{"erp_oauth_states_tenant_connection_fk",
			[]string{"FOREIGN KEY (tenant_id, connection_id)", "REFERENCES erp_connections(tenant_id, id)", "ON DELETE CASCADE"}},
		{"erp_oauth_states_hash_uq", []string{"UNIQUE (state_hash)"}},
		{"erp_oauth_states_hash_len", []string{"octet_length(state_hash) = 32"}},
	}
	for _, d := range defs {
		var def string
		if err := q.QueryRow(ctx,
			`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = to_regclass('public.erp_oauth_states') AND conname = $1`,
			d.name).Scan(&def); err != nil {
			problems = append(problems, d.name+": read definition: "+err.Error())
			continue
		}
		for _, frag := range d.has {
			if !strings.Contains(def, frag) {
				problems = append(problems, d.name+" = "+def+", want it to contain "+frag)
			}
		}
	}
	return problems
}

// The partial unique index that makes a repeated connect reuse its keyless pending row.
func erpKeylessIndexProblems(q erpQuerier) []string {
	var def string
	err := q.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'erp_connections' AND indexname = 'erp_connections_one_keyless_pending'`,
	).Scan(&def)
	if err != nil {
		return []string{"index erp_connections_one_keyless_pending: " + err.Error()}
	}
	var problems []string
	for _, frag := range []string{"CREATE UNIQUE INDEX", "(tenant_id, entity_id, system)", "external_key IS NULL", "status = 'pending'"} {
		if !strings.Contains(def, frag) {
			problems = append(problems, "erp_connections_one_keyless_pending = "+def+", want it to contain "+frag)
		}
	}
	return problems
}

func TestRLS_ErpOAuthStatesShape(t *testing.T) {
	h := requireHarness(t)
	requireStatesTable(t, h.super)
	for _, p := range erpStatesProblems(h.super) {
		t.Error(p)
	}
	for _, p := range erpKeylessIndexProblems(h.super) {
		t.Error(p)
	}
}

// ---- RLS ------------------------------------------------------------------

// ENABLE alone would let the owner bypass the policy; the callback binds the tenant only, so the policy must not read entity_scope.
func TestRLS_ErpOAuthStatesForceRLSAndPolicy(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	requireStatesTable(t, h.super)

	var enabled, forced bool
	if err := h.super.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.erp_oauth_states')`,
	).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read pg_class: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("erp_oauth_states relrowsecurity/relforcerowsecurity = %v/%v, want true/true", enabled, forced)
	}
	rows, err := h.super.Query(ctx,
		`SELECT policyname, cmd, roles::text[], coalesce(qual, '') FROM pg_policies WHERE schemaname = 'public' AND tablename = 'erp_oauth_states'`)
	if err != nil {
		t.Fatalf("query pg_policies: %v", err)
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
		t.Errorf("policies on erp_oauth_states = %d (%v), want exactly 1 (tenant_isolation)", len(got), got)
	}
	iso, ok := got["tenant_isolation"]
	if !ok {
		t.Fatalf("no tenant_isolation policy on erp_oauth_states: %v", got)
	}
	if iso.cmd != "ALL" || strings.Join(iso.roles, ",") != "public" {
		t.Errorf("tenant_isolation cmd/roles = %s/%v, want ALL/[public] (no TO clause)", iso.cmd, iso.roles)
	}
	if !strings.Contains(iso.qual, "app.current_tenant") {
		t.Errorf("tenant_isolation qual = %q, want a comparison against app.current_tenant", iso.qual)
	}
	if strings.Contains(iso.qual, "app.entity_scope") {
		t.Errorf("tenant_isolation qual = %q, want the tenant only: the callback runs with no client scope", iso.qual)
	}
}

func TestRLS_ErpOAuthStatesCrossTenantRefused(t *testing.T) {
	h := requireHarness(t)
	a, _, ca := erpTenantWithConn(t, "erp-states-xt-a")
	b, _, cb := erpTenantWithConn(t, "erp-states-xt-b")
	sa, sb := erpSeedState(t, a, ca), erpSeedState(t, b, cb)

	if got, want := erpAppIDs(t, a, nil, "erp_oauth_states"), []string{sa}; !slices.Equal(got, want) {
		t.Errorf("states visible to A = %v, want exactly A's %v", got, want)
	}
	if got, want := erpAppIDs(t, b, nil, "erp_oauth_states"), []string{sb}; !slices.Equal(got, want) {
		t.Errorf("states visible to B = %v, want exactly B's %v", got, want)
	}

	// The own-tenant control separates the WITH CHECK clause from a missing INSERT grant: both answer 42501.
	err := erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), erpStateInsert, a, tokenHash(t, 32), ca, uuid.NewString())
		return e
	})
	failIfStatesUndefined(t, "control state INSERT in the own tenant", err)
	if err != nil {
		t.Fatalf("control state INSERT in the own tenant: %v", err)
	}
	err = erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(context.Background(), erpStateInsert, b, tokenHash(t, 32), cb, uuid.NewString())
		return e
	})
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") || !strings.Contains(msg, "erp_oauth_states") {
		t.Errorf("state refusal message = %q, want the erp_oauth_states RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_oauth_states WHERE tenant_id = $1`, b); n != 1 {
		t.Errorf("states of tenant B after the refused INSERT = %d, want the 1 seeded", n)
	}
}

func TestRLS_ErpOAuthStatesMissingContextAndOwner(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a, _, ca := erpTenantWithConn(t, "erp-states-noguc")
	erpSeedState(t, a, ca)

	// Control: the zeros below mean nothing unless there is a row to hide.
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_oauth_states WHERE tenant_id = $1`, a); n != 1 {
		t.Fatalf("superuser sees %d states, want 1", n)
	}
	// A pooled connection keeps app.current_tenant = '' after a tenant tx: no nullif() would raise 22P02 there.
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
		n, err := scanCount(ctx, tx, `SELECT count(*) FROM erp_oauth_states`)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("SELECT with no tenant context (empty GUC %v): %v", emptyGUC, err)
		}
		if n != 0 {
			t.Errorf("states visible with no tenant set (empty GUC %v) = %d, want 0", emptyGUC, n)
		}
		_, err = tx.Exec(ctx, erpStateInsert, a, tokenHash(t, 32), ca, uuid.NewString())
		_ = tx.Rollback(ctx)
		assertRLSViolation(t, err)
	}

	// FORCE subjects the owner to the policy: with no GUC it matches nothing.
	if n := mustCount(t, h.mig, `SELECT count(*) FROM erp_oauth_states`); n != 0 {
		t.Errorf("owner sees %d states with no tenant context, want 0 (FORCE ROW LEVEL SECURITY)", n)
	}
	_, err := h.mig.Exec(ctx, erpStateInsert, a, tokenHash(t, 32), ca, uuid.NewString())
	erpWantErr(t, err, "42501", "", "the owner inserting a state with no tenant GUC")
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_oauth_states WHERE tenant_id = $1`, a); n != 1 {
		t.Errorf("states after the refused owner insert = %d, want the 1 seeded", n)
	}
	// Control: bound to the row's own tenant the owner may insert, so the refusal above is the forced policy.
	err = db.WithinTenantTx(ctx, h.mig, a, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, erpStateInsert, a, tokenHash(t, 32), ca, uuid.NewString())
		return e
	})
	if err != nil {
		t.Fatalf("the owner inserting a state under its own tenant GUC: want success, got %v", err)
	}
}

// ---- constraints and cascades ---------------------------------------------

// RI checks bypass RLS, so only the composite FK on (tenant_id, connection_id) refuses B's connection.
func TestRLS_ErpOAuthStatesCrossTenantConnectionRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a, _, ca := erpTenantWithConn(t, "erp-states-fk-a")
	b, _, cb := erpTenantWithConn(t, "erp-states-fk-b")

	// Control: the same statement naming A's own connection passes.
	erpSeedState(t, a, ca)
	_, err := h.super.Exec(ctx, erpStateInsert, a, tokenHash(t, 32), cb, uuid.NewString())
	erpWantErr(t, err, "23503", "erp_oauth_states_tenant_connection_fk", "a state of A naming B's connection")
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_oauth_states WHERE connection_id = $1`, cb); n != 0 {
		t.Errorf("states naming B's connection = %d, want 0", n)
	}
	_ = b
}

func TestRLS_ErpOAuthStatesCascade(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	count := func(where string, arg any) int {
		return mustCount(t, h.super, `SELECT count(*) FROM erp_oauth_states WHERE `+where, arg)
	}

	// Deleting a client removes its connections' states and leaves another client's.
	a := erpTenant(t)
	e1, e2 := erpEntity(t, a, "erp-states-casc-1"), erpEntity(t, a, "erp-states-casc-2")
	c1 := erpSeedConn(t, a, e1, "fake", erpStr("k1"), nil, "pending")
	c2 := erpSeedConn(t, a, e2, "fake", erpStr("k1"), nil, "pending")
	erpSeedState(t, a, c1)
	erpSeedState(t, a, c1)
	erpSeedState(t, a, c2)
	if n := count("tenant_id = $1", a); n != 3 {
		t.Fatalf("states seeded = %d, want 3", n)
	}
	if _, err := h.super.Exec(ctx, `DELETE FROM business_entities WHERE id = $1`, e1); err != nil {
		t.Fatalf("delete the client: %v", err)
	}
	if n := count("connection_id = $1", c1); n != 0 {
		t.Errorf("states of the deleted client's connection = %d, want 0", n)
	}
	if n := count("connection_id = $1", c2); n != 1 {
		t.Errorf("states of the other client's connection = %d, want 1", n)
	}

	// Deleting a connection removes its states.
	c3 := erpSeedConn(t, a, e2, "fake", erpStr("k3"), nil, "pending")
	erpSeedState(t, a, c3)
	if _, err := h.super.Exec(ctx, `DELETE FROM erp_connections WHERE id = $1`, c3); err != nil {
		t.Fatalf("delete the connection: %v", err)
	}
	if n := count("connection_id = $1", c3); n != 0 {
		t.Errorf("states of the deleted connection = %d, want 0", n)
	}
	if n := count("connection_id = $1", c2); n != 1 {
		t.Errorf("states of the surviving connection = %d, want 1", n)
	}

	// Deleting the tenant removes every state of it and none of another tenant's.
	b, _, cb := erpTenantWithConn(t, "erp-states-casc-b")
	erpSeedState(t, b, cb)
	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, a); err != nil {
		t.Fatalf("delete the tenant: %v", err)
	}
	if n := count("tenant_id = $1", a); n != 0 {
		t.Errorf("states after the tenant delete = %d, want 0", n)
	}
	if n := count("tenant_id = $1", b); n != 1 {
		t.Errorf("states of the other tenant = %d, want 1", n)
	}
}

func TestRLS_ErpOAuthStatesHashChecks(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a, _, ca := erpTenantWithConn(t, "erp-states-hash-a")
	b, _, cb := erpTenantWithConn(t, "erp-states-hash-b")

	hash := tokenHash(t, 32)
	if _, err := h.super.Exec(ctx, erpStateInsert, a, hash, ca, uuid.NewString()); err != nil {
		failIfStatesUndefined(t, "control state with a 32-byte hash", err)
		t.Fatalf("control state with a 32-byte hash: %v", err)
	}
	// The hash is unique across tenants: the lookup by hash carries no tenant.
	_, err := h.super.Exec(ctx, erpStateInsert, b, hash, cb, uuid.NewString())
	erpWantErr(t, err, "23505", "erp_oauth_states_hash_uq", "the same state hash in another tenant")
	for _, n := range []int{31, 33} {
		_, err := h.super.Exec(ctx, erpStateInsert, a, tokenHash(t, n), ca, uuid.NewString())
		erpWantErr(t, err, "23514", "erp_oauth_states_hash_len", "a state hash of the wrong length")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_oauth_states WHERE tenant_id IN ($1, $2)`, a, b); n != 1 {
		t.Errorf("states after the refused inserts = %d, want the 1 control", n)
	}
}

// ---- grants ---------------------------------------------------------------

// A missing column privilege answers 42501 with "permission denied for table", a policy with "row-level security".
func TestRLS_ErpOAuthStatesUpdateOnlyUsedAt(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a, _, ca := erpTenantWithConn(t, "erp-states-upd-a")
	b, _, cb := erpTenantWithConn(t, "erp-states-upd-b")
	id, bid := erpSeedState(t, a, ca), erpSeedState(t, b, cb)

	var n int64
	err := erpApp(a, nil, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE erp_oauth_states SET used_at = now() WHERE id = $1`, id)
		n = tag.RowsAffected()
		return e
	})
	failIfStatesUndefined(t, "UPDATE of a state", err)
	if err != nil || n != 1 {
		t.Fatalf("UPDATE used_at of the own state = %d rows (err %v), want 1", n, err)
	}
	if c := mustCount(t, h.super, `SELECT count(*) FROM erp_oauth_states WHERE id = $1 AND used_at IS NOT NULL`, id); c != 1 {
		t.Errorf("states marked used = %d, want 1", c)
	}

	for _, set := range []string{
		"expires_at = now() + interval '1 day'", "state_hash = decode(repeat('00', 32), 'hex')", "connection_id = gen_random_uuid()",
		"user_id = gen_random_uuid()", "tenant_id = '" + b + "'", "id = gen_random_uuid()", "created_at = now()",
	} {
		err := erpApp(a, nil, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE erp_oauth_states SET `+set+` WHERE id = $1`, id)
			return e
		})
		erpWantErr(t, err, "42501", "", "invoice_app running UPDATE erp_oauth_states SET "+set)
		if msg := pgMessage(err); !strings.Contains(msg, "permission denied for table erp_oauth_states") {
			t.Errorf("UPDATE SET %s refusal message = %q, want the missing column privilege, not a policy", set, msg)
		}
	}

	// Another tenant's state: the policy hides it, so used_at moves no row.
	err = erpApp(a, nil, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE erp_oauth_states SET used_at = now() WHERE id = $1`, bid)
		n = tag.RowsAffected()
		return e
	})
	if err != nil || n != 0 {
		t.Errorf("UPDATE used_at of another tenant's state = %d rows (err %v), want 0", n, err)
	}
	if c := mustCount(t, h.super, `SELECT count(*) FROM erp_oauth_states WHERE id = $1 AND used_at IS NULL`, bid); c != 1 {
		t.Errorf("B's state still unused = %d, want 1", c)
	}
}

// ---- one keyless pending row ----------------------------------------------

func TestRLS_ErpConnectionsOneKeylessPendingPerClientSystem(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	a := erpTenant(t)
	e1, e2 := erpEntity(t, a, "erp-keyless-1"), erpEntity(t, a, "erp-keyless-2")
	if p := erpKeylessIndexProblems(h.super); len(p) > 0 {
		t.Errorf("the keyless-pending index is not in place: %v", p)
	}

	erpSeedConn(t, a, e1, "fake", nil, nil, "pending")
	_, err := h.super.Exec(ctx, erpConnInsert, a, e1, "fake", nil, "pending", nil)
	erpWantErr(t, err, "23505", "erp_connections_one_keyless_pending", "a second keyless pending row for the same client and system")
	// The index binds every writer: invoice_app hits it too, where the policy would hide nothing of the own tenant.
	err = erpApp(a, nil, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, erpConnInsert, a, e1, "fake", nil, "pending", nil)
		return e
	})
	erpWantErr(t, err, "23505", "erp_connections_one_keyless_pending", "invoice_app adding a second keyless pending row")

	for _, c := range []struct {
		what, entity, system string
		key                  *string
		status               string
	}{
		{"a keyless disconnected row of the same client and system", e1, "fake", nil, "disconnected"},
		{"a keyless pending row of another client", e2, "fake", nil, "pending"},
		{"a keyless pending row of another system", e1, "other", nil, "pending"},
		{"a keyed pending row beside the keyless one", e1, "fake", erpStr("k1"), "pending"},
		{"a second keyed pending row", e1, "fake", erpStr("k2"), "pending"},
	} {
		if _, err := h.super.Exec(ctx, erpConnInsert, a, c.entity, c.system, c.key, c.status, nil); err != nil {
			t.Errorf("%s: want it accepted, got %v", c.what, err)
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM erp_connections WHERE tenant_id = $1`, a); n != 6 {
		t.Errorf("connections after the cases = %d, want 6 (1 keyless pending + 5 accepted)", n)
	}
}

// ---- migration ------------------------------------------------------------

// The shipped Down drops the table, the index and the column grants; the shipped Up rebuilds them.
// Runs on the migrator inside a transaction that is always rolled back.
func TestRLS_ErpTokenCustodyMigrationIsReversible(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, erpCustodyGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", erpCustodyGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching %s = %v, want exactly 1 — the erp_token_custody migration is not written yet", erpCustodyGlob, matches)
	}
	migration := matches[0]
	requireStatesTable(t, h.super)

	colGrants := func(q erpQuerier) (grants, states int) {
		if err := q.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM pg_attribute WHERE attrelid = 'public.erp_grants'::regclass AND attnum > 0 AND attacl IS NOT NULL),
			        (SELECT count(*) FROM pg_attribute WHERE attrelid = to_regclass('public.erp_oauth_states') AND attnum > 0 AND attacl IS NOT NULL)`,
		).Scan(&grants, &states); err != nil {
			t.Fatalf("count column ACLs: %v", err)
		}
		return grants, states
	}

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		t.Fatalf("set lock_timeout: %v", err)
	}
	before := erpPublicTables(t, tx, "before the Down")
	if !slices.Contains(before, "erp_oauth_states") || !slices.Contains(before, "erp_connections") {
		t.Fatalf("erp_oauth_states and erp_connections are not both among the public tables before the Down: %v", before)
	}
	if g, s := colGrants(tx); g != 3 || s != 1 {
		t.Fatalf("columns with an ACL before the Down: erp_grants %d, erp_oauth_states %d, want 3 and 1", g, s)
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
	if want := []string{"erp_oauth_states"}; !slices.Equal(dropped, want) {
		t.Errorf("tables the Down dropped = %v, want exactly %v", dropped, want)
	}
	if !slices.Contains(after, "erp_connections") || !slices.Contains(after, "erp_grants") {
		t.Errorf("erp_connections and erp_grants must survive the Down, tables = %v", after)
	}
	var idx int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'erp_connections_one_keyless_pending'`).Scan(&idx); err != nil || idx != 0 {
		t.Errorf("keyless-pending index after the Down = %d (err %v), want 0", idx, err)
	}
	if g, s := colGrants(tx); g != 0 || s != 0 {
		t.Errorf("columns with an ACL after the Down: erp_grants %d, erp_oauth_states %d, want 0 and 0", g, s)
	}
	var upd bool
	if err := tx.QueryRow(ctx, `SELECT has_column_privilege('invoice_app', 'public.erp_grants', 'version', 'UPDATE')`).Scan(&upd); err != nil || upd {
		t.Errorf("invoice_app UPDATE on erp_grants.version after the Down = %v (err %v), want false", upd, err)
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", migration, err)
	}
	requireStatesTable(t, tx)
	for _, p := range erpStatesProblems(tx) {
		t.Errorf("after Down then Up: %s", p)
	}
	for _, p := range erpKeylessIndexProblems(tx) {
		t.Errorf("after Down then Up: %s", p)
	}
	var forced bool
	var policies int
	if err := tx.QueryRow(ctx,
		`SELECT relrowsecurity AND relforcerowsecurity,
		        (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'erp_oauth_states')
		   FROM pg_class WHERE oid = to_regclass('public.erp_oauth_states')`).Scan(&forced, &policies); err != nil || !forced || policies != 1 {
		t.Errorf("after Down then Up: FORCE RLS = %v, policies = %d (err %v), want true and 1", forced, policies, err)
	}
	var grants bool
	if err := tx.QueryRow(ctx,
		`SELECT has_table_privilege('invoice_app', 'public.erp_oauth_states', 'SELECT, INSERT, DELETE')
		    AND NOT has_table_privilege('invoice_app', 'public.erp_oauth_states', 'UPDATE')
		    AND has_column_privilege('invoice_app', 'public.erp_oauth_states', 'used_at', 'UPDATE')
		    AND NOT has_column_privilege('invoice_app', 'public.erp_oauth_states', 'expires_at', 'UPDATE')
		    AND has_column_privilege('invoice_app', 'public.erp_grants', 'version', 'UPDATE')
		    AND has_column_privilege('invoice_app', 'public.erp_grants', 'expires_at', 'UPDATE')
		    AND has_column_privilege('invoice_app', 'public.erp_grants', 'updated_at', 'UPDATE')
		    AND NOT has_column_privilege('invoice_app', 'public.erp_grants', 'secret_ref', 'UPDATE')`).Scan(&grants); err != nil || !grants {
		t.Errorf("after Down then Up: invoice_app holds the Design's verbs = %v (err %v), want true", grants, err)
	}
	if g, s := colGrants(tx); g != 3 || s != 1 {
		t.Errorf("columns with an ACL after Down then Up: erp_grants %d, erp_oauth_states %d, want 3 and 1", g, s)
	}

	// Nothing committed: once rolled back, the schema is exactly as before.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := erpPublicTables(t, h.super, "after the rollback"); !slices.Equal(got, before) {
		t.Errorf("public tables after the rollback = %v, want the %v seen before the Down", got, before)
	}
}
