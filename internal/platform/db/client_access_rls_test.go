// Storage suite for client access: memberships.all_clients, invitations.all_clients,
// membership_clients and invitation_clients. Written before the migration, so every case
// fails on a runtime assertion (missing column, table or constraint), never on a compile error.
//
// Rows are seeded per test, never in the shared harness.seed(). A missing table fails only
// these cases. Run: `DEV_DB_PORT=5462 make test-rls`; a skip is a failure under the CI gate.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// caTable names one join table with the owner column and the constraint names its FKs carry.
type caTable struct {
	name, ownerCol, ownerFK, entityFK, pk string
}

var caTables = []caTable{
	{"membership_clients", "user_id", "membership_clients_tenant_user_fk",
		"membership_clients_tenant_entity_fk", "membership_clients_pk"},
	{"invitation_clients", "invitation_id", "invitation_clients_tenant_invitation_fk",
		"invitation_clients_tenant_entity_fk", "invitation_clients_pk"},
}

// failIfUndefinedClientAccess turns a pre-migration 42P01 / 42703 into an explicit message.
func failIfUndefinedClientAccess(t *testing.T, what string, err error) {
	t.Helper()
	if c := pgCode(err); c == "42P01" || c == "42703" {
		t.Fatalf("%s: %s — the client_access migration is not applied yet: %v", what, c, err)
	}
}

// caSeed inserts one join row as the superuser (BYPASSRLS) and returns its cleanup.
func caSeed(t *testing.T, tb caTable, tenantID, ownerID, entityID string) func() {
	t.Helper()
	_, err := h.super.Exec(context.Background(),
		`INSERT INTO `+tb.name+` (tenant_id, `+tb.ownerCol+`, entity_id) VALUES ($1, $2, $3)`,
		tenantID, ownerID, entityID)
	failIfUndefinedClientAccess(t, "seed "+tb.name, err)
	if err != nil {
		t.Fatalf("seed %s: %v", tb.name, err)
	}
	return func() {
		_, _ = h.super.Exec(context.Background(),
			`DELETE FROM `+tb.name+` WHERE tenant_id = $1 AND `+tb.ownerCol+` = $2 AND entity_id = $3`,
			tenantID, ownerID, entityID)
	}
}

// caOwner seeds a membership (user) or an invitation for tenantID and returns the owner id
// that tb.ownerCol takes, plus a cleanup. Cleanups run LIFO, before the entity's.
func caOwner(t *testing.T, tb caTable, tenantID string) (string, func()) {
	t.Helper()
	if tb.ownerCol == "user_id" {
		user := uuid.NewString()
		_, cleanup := seedMembership(t, tenantID, user, "reviewer")
		return user, cleanup
	}
	return seedInvitation(t, tenantID, "preparer", "ca-"+uuid.NewString()[:8]+"@example.com")
}

// caThrowawayTenant creates a tenant so deleting it cannot touch the shared harness tenants.
func caThrowawayTenant(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO tenants (id, name) VALUES ($1, 'client-access throwaway tenant')`, id); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
	return id
}

func TestRLS_ClientAccessColumnsDefaultFalse(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, table := range []string{"memberships", "invitations"} {
		var dataType, nullable string
		var def *string
		err := h.super.QueryRow(ctx,
			`SELECT data_type, is_nullable, column_default FROM information_schema.columns
			  WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'all_clients'`, table,
		).Scan(&dataType, &nullable, &def)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("%s.all_clients does not exist — the client_access migration is not applied yet", table)
		}
		if err != nil {
			t.Fatalf("read %s.all_clients from information_schema: %v", table, err)
		}
		if dataType != "boolean" || nullable != "NO" || def == nil || *def != "false" {
			t.Errorf("%s.all_clients = %s nullable=%s default=%v, want boolean NOT NULL DEFAULT false",
				table, dataType, nullable, def)
		}
	}

	// Rows inserted without the column read false: the default, not the DDL text, is the claim.
	_, cleanupM := seedMembership(t, h.tenantA, uuid.NewString(), "reviewer")
	defer cleanupM()
	inv, cleanupI := seedInvitation(t, h.tenantA, "preparer", "ca-default@example.com")
	defer cleanupI()
	for _, q := range []struct{ table, where, arg string }{
		{"memberships", `tenant_id = $1`, h.tenantA},
		{"invitations", `id = $1`, inv},
	} {
		var n, trues int
		err := h.super.QueryRow(ctx,
			`SELECT count(*), count(*) FILTER (WHERE all_clients) FROM `+q.table+` WHERE `+q.where, q.arg,
		).Scan(&n, &trues)
		failIfUndefinedClientAccess(t, "read "+q.table+".all_clients", err)
		if err != nil {
			t.Fatalf("read %s.all_clients: %v", q.table, err)
		}
		if n == 0 {
			t.Fatalf("no %s row seeded — the false check below would pass vacuously", q.table)
		}
		if trues != 0 {
			t.Errorf("%s rows reading all_clients=true after a default insert = %d, want 0", q.table, trues)
		}
	}
}

func TestRLS_ClientAccessExistingMembershipHasNoClients(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := caThrowawayTenant(t)
	user := uuid.NewString()
	seedMembership(t, tenantID, user, "reviewer")

	var flag bool
	err := h.super.QueryRow(ctx,
		`SELECT all_clients FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenantID, user).Scan(&flag)
	failIfUndefinedClientAccess(t, "read memberships.all_clients", err)
	if err != nil {
		t.Fatalf("read memberships.all_clients: %v", err)
	}
	if flag {
		t.Error("a membership created without the flag reads all_clients=true, want false")
	}
	var n int
	err = h.super.QueryRow(ctx,
		`SELECT count(*) FROM membership_clients WHERE tenant_id = $1 AND user_id = $2`, tenantID, user).Scan(&n)
	failIfUndefinedClientAccess(t, "count membership_clients", err)
	if err != nil {
		t.Fatalf("count membership_clients: %v", err)
	}
	if n != 0 {
		t.Errorf("membership_clients rows for a new membership = %d, want 0", n)
	}
}

// ENABLE alone would let the owner bypass the policy; a policy `TO invoice_app` would leave
// the migrator with none. Both pass every behavioural case, so the catalog is read here.
func TestRLS_ClientAccessForceRLSAndPoliciesDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, tb := range caTables {
		var enabled, forced bool
		err := h.super.QueryRow(ctx,
			`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`,
			tb.name).Scan(&enabled, &forced)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("no pg_class row for public.%s — the client_access migration is not applied yet", tb.name)
		}
		if err != nil {
			t.Fatalf("read pg_class for %s: %v", tb.name, err)
		}
		if !enabled || !forced {
			t.Errorf("%s relrowsecurity/relforcerowsecurity = %v/%v, want true/true", tb.name, enabled, forced)
		}

		rows, err := h.super.Query(ctx,
			`SELECT policyname, roles::text[], coalesce(qual, '') FROM pg_policies
			  WHERE schemaname = 'public' AND tablename = $1`, tb.name)
		if err != nil {
			t.Fatalf("query pg_policies for %s: %v", tb.name, err)
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
				t.Fatalf("scan pg_policies row for %s: %v", tb.name, e)
			}
			got[name] = p
		}
		rows.Close()
		if len(got) != 1 {
			t.Errorf("policies on %s = %d (%v), want exactly 1 (tenant_isolation)", tb.name, len(got), got)
		}
		iso, ok := got["tenant_isolation"]
		if !ok {
			t.Fatalf("no tenant_isolation policy on %s — the client_access migration is not applied yet", tb.name)
		}
		if strings.Join(iso.roles, ",") != "public" {
			t.Errorf("%s tenant_isolation roles = %v, want [public] (no TO clause)", tb.name, iso.roles)
		}
		if !strings.Contains(iso.qual, "app.current_tenant") {
			t.Errorf("%s tenant_isolation qual = %q, want a comparison against app.current_tenant", tb.name, iso.qual)
		}
	}
}

func TestRLS_ClientAccessCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, tb := range caTables {
		entA, cleanupEA := seedBusinessEntity(t, h.tenantA, "ca-select-a")
		defer cleanupEA()
		entB, cleanupEB := seedBusinessEntity(t, h.tenantB, "ca-select-b")
		defer cleanupEB()
		ownerA, cleanupOA := caOwner(t, tb, h.tenantA)
		defer cleanupOA()
		ownerB, cleanupOB := caOwner(t, tb, h.tenantB)
		defer cleanupOB()
		defer caSeed(t, tb, h.tenantA, ownerA, entA)()
		defer caSeed(t, tb, h.tenantB, ownerB, entB)()

		if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			if n := mustCount(t, tx, `SELECT count(*) FROM `+tb.name+` WHERE entity_id = $1`, entA); n != 1 {
				t.Errorf("%s: A's own row visible to A = %d, want 1", tb.name, n)
			}
			if n := mustCount(t, tx, `SELECT count(*) FROM `+tb.name+` WHERE entity_id = $1`, entB); n != 0 {
				t.Errorf("%s: B's row visible to A = %d, want 0", tb.name, n)
			}
			return nil
		}); err != nil {
			t.Fatalf("WithinTenantTx: %v", err)
		}
	}
}

func TestRLS_ClientAccessMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, tb := range caTables {
		ent, cleanupE := seedBusinessEntity(t, h.tenantA, "ca-noguc")
		defer cleanupE()
		owner, cleanupO := caOwner(t, tb, h.tenantA)
		defer cleanupO()
		defer caSeed(t, tb, h.tenantA, owner, ent)()

		// Control: the zero below means nothing unless the row is there to hide.
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+tb.name+` WHERE entity_id = $1`, ent); n != 1 {
			t.Fatalf("%s: superuser sees %d seeded rows, want 1", tb.name, n)
		}
		tx, err := h.app.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		n, err := scanCount(ctx, tx, `SELECT count(*) FROM `+tb.name+` WHERE entity_id = $1`, ent)
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatalf("%s: SELECT with no tenant context: %v", tb.name, err)
		}
		if n != 0 {
			t.Errorf("%s: rows visible with no tenant set = %d, want 0", tb.name, n)
		}
	}
}

// B's membership/invitation and entity are real, so no FK can be what refuses: only the policy.
func TestRLS_ClientAccessCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, tb := range caTables {
		entB, cleanupE := seedBusinessEntity(t, h.tenantB, "ca-xinsert")
		defer cleanupE()
		ownerB, cleanupO := caOwner(t, tb, h.tenantB)
		defer cleanupO()

		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO `+tb.name+` (tenant_id, `+tb.ownerCol+`, entity_id) VALUES ($1, $2, $3)`,
				h.tenantB, ownerB, entB)
			return e
		})
		failIfUndefinedClientAccess(t, tb.name+" cross-tenant INSERT", err)
		assertRLSViolation(t, err)
		if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
			t.Errorf("%s: refusal message = %q, want an RLS WITH CHECK violation", tb.name, msg)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+tb.name+` WHERE entity_id = $1`, entB); n != 0 {
			t.Errorf("%s: rows after the refused INSERT = %d, want 0", tb.name, n)
		}
	}
}

// RI checks bypass RLS, so only a composite FK on (tenant_id, entity_id) refuses B's entity.
func TestRLS_ClientAccessCrossTenantEntityRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, tb := range caTables {
		entB, cleanupE := seedBusinessEntity(t, h.tenantB, "ca-xentity")
		defer cleanupE()
		ownerA, cleanupO := caOwner(t, tb, h.tenantA)
		defer cleanupO()

		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO `+tb.name+` (tenant_id, `+tb.ownerCol+`, entity_id) VALUES ($1, $2, $3)`,
				h.tenantA, ownerA, entB)
			return e
		})
		failIfUndefinedClientAccess(t, tb.name+" cross-tenant entity INSERT", err)
		if err == nil {
			t.Fatalf("%s: naming tenant B's entity under tenant A succeeded, want 23503", tb.name)
		}
		if code := pgCode(err); code != "23503" {
			t.Fatalf("%s: SQLSTATE = %q, want 23503: %v", tb.name, code, err)
		}
		if name := pgConstraint(err); name != tb.entityFK {
			t.Errorf("%s: constraint = %q, want %q", tb.name, name, tb.entityFK)
		}
	}
}

func TestRLS_ClientAccessForeignUserAndInvitationRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entA, cleanupE := seedBusinessEntity(t, h.tenantA, "ca-foreign-owner")
	defer cleanupE()
	insert := func(tb caTable, owner string) error {
		return db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO `+tb.name+` (tenant_id, `+tb.ownerCol+`, entity_id) VALUES ($1, $2, $3)`,
				h.tenantA, owner, entA)
			return e
		})
	}
	refused := func(what string, tb caTable, owner, wantFK string) {
		t.Helper()
		err := insert(tb, owner)
		failIfUndefinedClientAccess(t, what, err)
		if err == nil {
			t.Fatalf("%s: INSERT succeeded, want 23503 on %s", what, wantFK)
		}
		if code := pgCode(err); code != "23503" {
			t.Fatalf("%s: SQLSTATE = %q, want 23503: %v", what, code, err)
		}
		if name := pgConstraint(err); name != wantFK {
			t.Errorf("%s: constraint = %q, want %q", what, name, wantFK)
		}
	}

	mc, ic := caTables[0], caTables[1]
	refused("user with no membership anywhere", mc, uuid.NewString(), mc.ownerFK)

	otherTenantUser := uuid.NewString()
	_, cleanupM := seedMembership(t, h.tenantB, otherTenantUser, "reviewer")
	defer cleanupM()
	refused("user who is a member of tenant B only", mc, otherTenantUser, mc.ownerFK)

	invB, cleanupI := seedInvitation(t, h.tenantB, "preparer", "ca-foreign-inv@example.com")
	defer cleanupI()
	refused("invitation of tenant B", ic, invB, ic.ownerFK)
}

func TestRLS_ClientAccessDuplicateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, tb := range caTables {
		ent, cleanupE := seedBusinessEntity(t, h.tenantA, "ca-dup")
		defer cleanupE()
		owner, cleanupO := caOwner(t, tb, h.tenantA)
		defer cleanupO()
		defer caSeed(t, tb, h.tenantA, owner, ent)()

		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO `+tb.name+` (tenant_id, `+tb.ownerCol+`, entity_id) VALUES ($1, $2, $3)`,
				h.tenantA, owner, ent)
			return e
		})
		if err == nil {
			t.Fatalf("%s: duplicate (tenant, owner, entity) INSERT succeeded, want 23505", tb.name)
		}
		if code := pgCode(err); code != "23505" {
			t.Fatalf("%s: SQLSTATE = %q, want 23505: %v", tb.name, code, err)
		}
		if name := pgConstraint(err); name != tb.pk {
			t.Errorf("%s: constraint = %q, want the primary key %q", tb.name, name, tb.pk)
		}
		if n := mustCount(t, h.super, `SELECT count(*) FROM `+tb.name+` WHERE entity_id = $1`, ent); n != 1 {
			t.Errorf("%s: rows after the refused duplicate = %d, want 1", tb.name, n)
		}
	}
}

// invitation_clients' composite FK targets this constraint.
func TestRLS_InvitationsTenantIdIdUniqueConstraintExists(t *testing.T) {
	h := requireHarness(t)

	var contype string
	var cols []string
	err := h.super.QueryRow(context.Background(),
		`SELECT c.contype::text,
		        array(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum, ord)
		                JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		               ORDER BY k.ord)
		   FROM pg_constraint c
		  WHERE c.conrelid = 'public.invitations'::regclass AND c.conname = 'invitations_tenant_id_id_uq'`,
	).Scan(&contype, &cols)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("no invitations_tenant_id_id_uq constraint — the client_access migration is not applied yet")
	}
	if err != nil {
		t.Fatalf("read invitations_tenant_id_id_uq: %v", err)
	}
	if contype != "u" {
		t.Errorf("invitations_tenant_id_id_uq contype = %q, want \"u\" (UNIQUE constraint)", contype)
	}
	if strings.Join(cols, ",") != "tenant_id,id" {
		t.Errorf("invitations_tenant_id_id_uq columns = %v, want [tenant_id id]", cols)
	}
}

// The migrator's catalog answers here: a missing privilege and a policy refusal are both
// 42501 on the wire. Whole-set replace is DELETE then INSERT, so UPDATE stays ungranted.
func TestRLS_ClientAccessGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, tb := range caTables {
		for _, c := range []struct {
			priv string
			want bool
		}{
			{"SELECT", true}, {"INSERT", true}, {"DELETE", true}, {"UPDATE", false},
		} {
			var got bool
			err := h.super.QueryRow(ctx,
				`SELECT has_table_privilege('invoice_app', 'public.' || $1, $2)`, tb.name, c.priv).Scan(&got)
			failIfUndefinedClientAccess(t, "has_table_privilege(invoice_app, "+tb.name+", "+c.priv+")", err)
			if err != nil {
				t.Fatalf("has_table_privilege(invoice_app, %s, %s): %v", tb.name, c.priv, err)
			}
			if got != c.want {
				t.Errorf("has_table_privilege(invoice_app, %s, %s) = %v, want %v", tb.name, c.priv, got, c.want)
			}
		}
	}
}

// Deleting a membership, an invitation, an entity or a tenant removes its join rows and only those.
func TestRLS_ClientAccessCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := caThrowawayTenant(t)
	var ents, users, invs [2]string
	for i := range ents {
		ents[i], _ = seedBusinessEntity(t, tenantID, "ca-cascade")
		users[i] = uuid.NewString()
		seedMembership(t, tenantID, users[i], "reviewer")
		invs[i], _ = seedInvitation(t, tenantID, "preparer", "ca-cascade-"+uuid.NewString()[:8]+"@example.com")
	}
	mc, ic := caTables[0], caTables[1]
	for _, e := range ents {
		for _, u := range users {
			caSeed(t, mc, tenantID, u, e)
		}
		for _, inv := range invs {
			caSeed(t, ic, tenantID, inv, e)
		}
	}

	count := func(tb caTable, where string, arg string) int {
		return mustCount(t, h.super, `SELECT count(*) FROM `+tb.name+` WHERE `+where, arg)
	}
	del := func(sql, id string) {
		t.Helper()
		if _, err := h.super.Exec(ctx, sql, id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	if n := count(mc, `tenant_id = $1`, tenantID); n != 4 {
		t.Fatalf("membership_clients rows seeded = %d, want 4", n)
	}
	del(`DELETE FROM memberships WHERE user_id = $1`, users[0])
	if n := count(mc, `user_id = '`+users[0]+`' AND tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("deleted member's membership_clients rows = %d, want 0", n)
	}
	if n := count(mc, `user_id = '`+users[1]+`' AND tenant_id = $1`, tenantID); n != 2 {
		t.Errorf("other member's membership_clients rows = %d, want 2", n)
	}

	del(`DELETE FROM invitations WHERE id = $1`, invs[0])
	if n := count(ic, `invitation_id = '`+invs[0]+`' AND tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("deleted invitation's invitation_clients rows = %d, want 0", n)
	}
	if n := count(ic, `invitation_id = '`+invs[1]+`' AND tenant_id = $1`, tenantID); n != 2 {
		t.Errorf("other invitation's invitation_clients rows = %d, want 2", n)
	}

	del(`DELETE FROM business_entities WHERE id = $1`, ents[1])
	for _, tb := range caTables {
		if n := count(tb, `entity_id = '`+ents[1]+`' AND tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("%s rows naming the deleted entity = %d, want 0", tb.name, n)
		}
		if n := count(tb, `entity_id = '`+ents[0]+`' AND tenant_id = $1`, tenantID); n != 1 {
			t.Errorf("%s rows naming the kept entity = %d, want 1", tb.name, n)
		}
	}

	del(`DELETE FROM tenants WHERE id = $1`, tenantID)
	for _, tb := range caTables {
		if n := count(tb, `tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("%s rows after the tenant delete = %d, want 0", tb.name, n)
		}
	}
}

// AC6: the shipped Down removes the objects and the shipped Up applies again over its result.
// Runs on the migrator inside a transaction that is always rolled back.
func TestRLS_ClientAccessDownThenUpRoundTrips(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, "*_client_access.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("migrations.FS *_client_access.sql = %v (err %v), want exactly one file", matches, err)
	}
	up := auditEntitySectionOf(t, matches[0], "Up")
	down := shippedDownStatements(t, "*_client_access.sql")
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
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM pg_class WHERE relname IN ('membership_clients', 'invitation_clients')`).Scan(&n); err != nil {
			t.Fatalf("count join tables %s: %v", phase, err)
		}
		if n != want {
			t.Fatalf("join tables present %s = %d, want %d", phase, n, want)
		}
	}
	present(2, "before the Down")
	for _, s := range down {
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("Down statement %q: %v", s, err)
		}
	}
	present(0, "after the Down")
	var cols int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name IN ('memberships', 'invitations') AND column_name = 'all_clients'`,
	).Scan(&cols); err != nil || cols != 0 {
		t.Fatalf("all_clients columns after the Down = %d (err %v), want 0", cols, err)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("Up over the Down's result: %v", err)
	}
	present(2, "after the Up")
}
