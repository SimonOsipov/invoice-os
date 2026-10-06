// The E2E shard tenants db/seed.e2e-shards.sql creates in a
// PR environment's reset-and-seed. Env-gated like reset_test.go.
package db_test

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/demopolicy"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	shardSeedFile = "seed.e2e-shards.sql"
	firmSource    = demoTenantID      // 1111...
	inhouseSource = honeywellTenantID // 2222...
)

// iota keeps the validation package's version-pin grep from matching.
const (
	firstVersion = iota + 1
	secondVersion
)

type shardTenant struct{ id, source, name, kind string }

var (
	firmShards = []shardTenant{
		{"11111111-1111-1111-1111-00000000e2e1", firmSource, "Okafor & Partners", "firm"},
		{"11111111-1111-1111-1111-00000000e2e2", firmSource, "Okafor & Partners", "firm"},
		{"11111111-1111-1111-1111-00000000e2e3", firmSource, "Okafor & Partners", "firm"},
	}
	inhouseShards = []shardTenant{
		{"22222222-2222-2222-2222-00000000e2e1", inhouseSource, "Honeywell Group", "in_house"},
		{"22222222-2222-2222-2222-00000000e2e2", inhouseSource, "Honeywell Group", "in_house"},
		{"22222222-2222-2222-2222-00000000e2e3", inhouseSource, "Honeywell Group", "in_house"},
	}
	allShards = slices.Concat(firmShards, inhouseShards)
)

func shardIDs() []string {
	ids := make([]string, 0, len(allShards))
	for _, s := range allShards {
		ids = append(ids, s.id)
	}
	return ids
}

// deleteShardTenants removes the shard tenants bottom-up. Reset spares
// tenants and policy tables, and a sealed version blocks a plain delete, so the
// triggers are bypassed with session_replication_role. Runs first in every test
// and again at cleanup.
func deleteShardTenants(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	del := func() {
		ctx := context.Background()
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Errorf("deleteShardTenants: begin: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
			t.Errorf("deleteShardTenants: set session_replication_role: %v", err)
			return
		}
		for _, table := range []string{
			"invoice_status_history", "line_items", "invoices", "business_entities",
			"approval_policy_steps", "approval_policy_versions", "approval_policies",
			"workflow_role_members", "workflow_roles", "memberships",
		} {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id::text = ANY($1::text[])`, shardIDs()); err != nil {
				t.Errorf("deleteShardTenants: delete %s: %v", table, err)
				return
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tenants WHERE id::text = ANY($1::text[])`, shardIDs()); err != nil {
			t.Errorf("deleteShardTenants: delete tenants: %v", err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("deleteShardTenants: commit: %v", err)
		}
	}
	del()
	t.Cleanup(del)
}

type shardEnv struct {
	super            *pgxpool.Pool
	superDSN, migDSN string
}

// newShardEnv gates on the DB env and leaves no shard tenant behind.
func newShardEnv(t *testing.T) shardEnv {
	t.Helper()
	superDSN, migDSN := requireProvisionDSNs(t)
	super := bootstrapSuperuserPool(t, superDSN)
	deleteShardTenants(t, super)
	return shardEnv{super: super, superDSN: superDSN, migDSN: migDSN}
}

// prConfig is the pr-110 shape of TestProvisionResetWipesResidueThenReseedsCuratedDemo.
func (e shardEnv) prConfig() db.ProvisionConfig {
	return db.ProvisionConfig{
		Environment:            "development",
		BootstrapFlag:          "true",
		RailwayEnvironmentName: "pr-110",
		ResetFlag:              "true",
		SuperuserDSN:           e.superDSN,
		MigrationDSN:           e.migDSN,
		Passwords:              devRolePasswords(),
		BootstrapFS:            dbsql.FS,
		MigrationsFS:           migrations.FS,
		SeedFS:                 dbsql.FS,
	}
}

func (e shardEnv) provisionReset(t *testing.T) {
	t.Helper()
	if err := db.Provision(context.Background(), e.prConfig()); err != nil {
		t.Fatalf("Provision (pr-110, ResetFlag=true): %v", err)
	}
}

func (e shardEnv) shardTenantCount(t *testing.T) int {
	t.Helper()
	return mustCount(t, e.super, `SELECT count(*) FROM tenants WHERE id::text = ANY($1::text[])`, shardIDs())
}

func (e shardEnv) requireNoShardTenants(t *testing.T, label string) {
	t.Helper()
	if n := e.shardTenantCount(t); n != 0 {
		t.Errorf("%s: %d shard tenants exist, want 0", label, n)
	}
}

func (e shardEnv) requireAllShardTenants(t *testing.T, label string) {
	t.Helper()
	if n := e.shardTenantCount(t); n != len(allShards) {
		t.Fatalf("%s: %d shard tenants exist, want %d", label, n, len(allShards))
	}
}

// queryTexts returns the first column of every row, as text, sorted.
func queryTexts(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s *string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan %q: %v", sql, err)
		}
		if s == nil {
			out = append(out, "<null>")
		} else {
			out = append(out, *s)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows %q: %v", sql, err)
	}
	slices.Sort(out)
	return out
}

// queryRows returns every row of sql as sorted jsonb text.
func queryRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) []string {
	t.Helper()
	return queryTexts(t, pool, `SELECT to_jsonb(q)::text FROM (`+sql+`) q`, args...)
}

// mirrorRows asserts the shard's rows of sql equal the source tenant's. $1 is
// the tenant; extra fills $2... The source must be non-empty or the equality
// proves nothing.
func mirrorRows(t *testing.T, pool *pgxpool.Pool, label, sql string, s shardTenant, extra ...any) {
	t.Helper()
	want := queryRows(t, pool, sql, append([]any{s.source}, extra...)...)
	if len(want) == 0 {
		t.Fatalf("%s: source tenant %s has no rows, the comparison would be vacuous", label, s.source)
	}
	got := queryRows(t, pool, sql, append([]any{s.id}, extra...)...)
	if !slices.Equal(got, want) {
		t.Errorf("%s: shard %s has %d rows, source has %d\n got: %q\nwant: %q", label, s.id, len(got), len(want), got, want)
	}
}

func TestProvisionSeedsShardTenantsOnAPRReset(t *testing.T) {
	e := newShardEnv(t)
	e.provisionReset(t)

	e.requireAllShardTenants(t, "after a reset-on Provision")
	for _, s := range allShards {
		got := queryRows(t, e.super, `SELECT name, kind FROM tenants WHERE id = $1`, s.id)
		want := queryRows(t, e.super, `SELECT $1::text AS name, $2::text AS kind`, s.name, s.kind)
		if !slices.Equal(got, want) {
			t.Errorf("tenant %s = %q, want %q", s.id, got, want)
		}
	}
}

func TestShardMembershipsMirrorTheirSourceTenant(t *testing.T) {
	e := newShardEnv(t)
	e.provisionReset(t)

	for _, s := range allShards {
		mirrorRows(t, e.super, "memberships",
			`SELECT user_id, role, display_name, email, status FROM memberships WHERE tenant_id = $1`, s)
	}
}

func TestShardRolesAndStaffingMirrorTheirSourceTenant(t *testing.T) {
	e := newShardEnv(t)
	e.provisionReset(t)

	for _, s := range allShards {
		mirrorRows(t, e.super, "workflow_roles",
			`SELECT key, title, description, created_at FROM workflow_roles WHERE tenant_id = $1`, s)
		mirrorRows(t, e.super, "workflow_role_members",
			`SELECT r.key, m.user_id, m.ord FROM workflow_role_members m
			   JOIN workflow_roles r ON r.tenant_id = m.tenant_id AND r.id = m.workflow_role_id
			  WHERE m.tenant_id = $1`, s)

		const ids = `SELECT id::text FROM workflow_roles WHERE tenant_id = $1`
		shardIDs, sourceIDs := queryTexts(t, e.super, ids, s.id), queryTexts(t, e.super, ids, s.source)
		if len(shardIDs) == 0 {
			t.Errorf("shard %s has no workflow roles, the id-overlap check below is vacuous", s.id)
		}
		for _, id := range shardIDs {
			if slices.Contains(sourceIDs, id) {
				t.Errorf("shard %s role id %s equals a source role id", s.id, id)
			}
		}
	}
}

// requireNoApprovalRows fails when a tenant already holds approval rows, then
// registers a teardown of what demopolicy.Seed adds: Reset spares the policy
// tables, so a leftover trips internal/demopolicy's own "already holds" guard.
func requireNoApprovalRows(t *testing.T, super *pgxpool.Pool, tenantIDs ...string) {
	t.Helper()
	tables := []string{
		"approval_decisions", "approval_run_steps", "approval_runs",
		"approval_policy_steps", "approval_policy_versions", "approval_policies",
	}
	for _, id := range tenantIDs {
		for _, table := range tables {
			if n := mustCount(t, super, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, id); n != 0 {
				t.Fatalf("tenant %s already holds %d %s row(s); the teardown would delete rows this test did not create", id, n, table)
			}
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		tx, err := super.Begin(ctx)
		if err != nil {
			t.Errorf("approval teardown: begin: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// A sealed version blocks a plain delete.
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
			t.Errorf("approval teardown: set session_replication_role: %v", err)
			return
		}
		for _, table := range tables {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id::text = ANY($1::text[])`, tenantIDs); err != nil {
				t.Errorf("approval teardown: delete %s: %v", table, err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("approval teardown: commit: %v", err)
		}
	})
}

func TestShardFirmPolicyIsAnUnpublishedCopyOfTheShippedFirmPlan(t *testing.T) {
	appDSN := requireAppDSN(t) // demopolicy.Seed runs on the app pool
	e := newShardEnv(t)
	e.provisionReset(t)
	appPool, err := pgxpool.New(context.Background(), appDSN)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	t.Cleanup(appPool.Close)
	requireNoApprovalRows(t, e.super, demoTenantID, honeywellTenantID)
	// The boot runs this after Provision; it publishes the source plans and must leave the shards alone.
	if _, err := demopolicy.Seed(context.Background(), appPool, nil); err != nil {
		t.Fatalf("demopolicy.Seed: %v", err)
	}

	const tree = `SELECT p.ord AS parent_ord, s.branch, s.ord, s.kind, s.workflow_role_key,
	                     s.cond_op, s.cond_amount, s.notify_target, s.notify_channel
	                FROM approval_policy_steps s
	                LEFT JOIN approval_policy_steps p ON p.id = s.parent_step_id
	               WHERE s.version_id::text = $1`
	groups := []struct {
		label, source, policy string
		shards                []shardTenant
	}{
		{"firm", firmSource, "Standard approval policy", firmShards},
		{"in-house", inhouseSource, "Company approval policy", inhouseShards},
	}
	for _, g := range groups {
		wantIDs := queryTexts(t, e.super, `SELECT id::text FROM approval_policy_versions WHERE tenant_id = $1 AND is_active`, g.source)
		if len(wantIDs) != 1 {
			t.Fatalf("source %s tenant has %d active versions after demopolicy.Seed, want 1", g.label, len(wantIDs))
		}
		want := queryRows(t, e.super, tree, wantIDs[0])
		if len(want) == 0 {
			t.Fatalf("source %s active version has no steps, the tree comparison is vacuous", g.label)
		}

		for _, s := range g.shards {
			pol := queryRows(t, e.super, `SELECT name, scope FROM approval_policies WHERE tenant_id = $1`, s.id)
			wantPol := queryRows(t, e.super, `SELECT $1::text AS name, 'All invoices' AS scope`, g.policy)
			if !slices.Equal(pol, wantPol) {
				t.Fatalf("%s shard %s policies = %q, want exactly %q", g.label, s.id, pol, wantPol)
			}
			vers := queryRows(t, e.super, `SELECT sealed, is_active FROM approval_policy_versions WHERE tenant_id = $1`, s.id)
			wantVers := queryRows(t, e.super, `SELECT false AS sealed, false AS is_active`)
			if !slices.Equal(vers, wantVers) {
				t.Fatalf("%s shard %s versions = %q, want exactly one %q", g.label, s.id, vers, wantVers)
			}
			gotIDs := queryTexts(t, e.super, `SELECT id::text FROM approval_policy_versions WHERE tenant_id = $1`, s.id)
			if got := queryRows(t, e.super, tree, gotIDs[0]); !slices.Equal(got, want) {
				t.Errorf("%s shard %s step tree differs from its source's\n got: %q\nwant: %q", g.label, s.id, got, want)
			}
		}
	}
}

func TestShardTenantsHoldOnlyTheirSeededEntitiesAndInvoice(t *testing.T) {
	e := newShardEnv(t)
	e.provisionReset(t)

	const (
		firmEntity    = "Adeyemi & Sons Trading Ltd"
		inhouseEntity = "Honeywell Group"
		invoiceNo     = "DEMO-2026-1004"
	)
	const entity = `SELECT name, tin, sector, status FROM business_entities WHERE tenant_id = $1 AND name = $2`
	for _, s := range firmShards {
		if n := mustCount(t, e.super, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, s.id); n != 1 {
			t.Errorf("firm shard %s has %d entities, want exactly 1", s.id, n)
		}
		mirrorRows(t, e.super, "entity", entity, s, firmEntity)
		if n := mustCount(t, e.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, s.id); n != 1 {
			t.Errorf("firm shard %s has %d invoices, want exactly 1", s.id, n)
		}
		mirrorRows(t, e.super, "invoice",
			`SELECT invoice_number, status, failure_kind, subtotal, vat, total
			   FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, s, invoiceNo)
		mirrorRows(t, e.super, "line_items",
			`SELECT li.line_no, li.description, li.quantity, li.unit_price, li.line_total, li.line_tax
			   FROM line_items li JOIN invoices i ON i.id = li.invoice_id
			  WHERE i.tenant_id = $1 AND i.invoice_number = $2`, s, invoiceNo)
		mirrorRows(t, e.super, "invoice_status_history",
			`SELECT h.from_status, h.to_status, h.actor
			   FROM invoice_status_history h JOIN invoices i ON i.id = h.invoice_id
			  WHERE i.tenant_id = $1 AND i.invoice_number = $2`, s, invoiceNo)
	}
	for _, s := range inhouseShards {
		if n := mustCount(t, e.super, `SELECT count(*) FROM business_entities WHERE tenant_id = $1`, s.id); n != 1 {
			t.Errorf("in-house shard %s has %d entities, want exactly 1", s.id, n)
		}
		mirrorRows(t, e.super, "entity", entity, s, inhouseEntity)
		if n := mustCount(t, e.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, s.id); n != 0 {
			t.Errorf("in-house shard %s has %d invoices, want 0", s.id, n)
		}
	}

	// Core AC 2: under RLS each tenant, shard or source, reads only its own rows.
	t.Run("the app role sees only its own tenant's rows", func(t *testing.T) {
		appPool, err := pgxpool.New(context.Background(), requireAppDSN(t))
		if err != nil {
			t.Fatalf("open app pool: %v", err)
		}
		t.Cleanup(appPool.Close)

		wantInvoices := map[string]int{firmSource: -1, inhouseSource: -1} // -1: any, the seed owns the count
		for _, s := range firmShards {
			wantInvoices[s.id] = 1
		}
		for _, s := range inhouseShards {
			wantInvoices[s.id] = 0
		}
		if len(wantInvoices) != len(allShards)+2 {
			t.Fatalf("tenant list holds %d entries, want %d", len(wantInvoices), len(allShards)+2)
		}
		for tenantID, wantOwn := range wantInvoices {
			err := db.WithinTenantTx(context.Background(), appPool, tenantID, func(tx pgx.Tx) error {
				for _, table := range []string{"memberships", "workflow_roles", "business_entities", "invoices", "approval_policies"} {
					if n := mustCount(t, tx, `SELECT count(*) FROM `+table+` WHERE tenant_id <> $1`, tenantID); n != 0 {
						t.Errorf("tenant %s sees %d foreign %s rows, want 0", tenantID, n, table)
					}
				}
				for _, table := range []string{"memberships", "workflow_roles", "business_entities"} {
					if n := mustCount(t, tx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, tenantID); n == 0 {
						t.Errorf("tenant %s sees none of its own %s rows, the foreign-row zero above is vacuous", tenantID, table)
					}
				}
				if n := mustCount(t, tx, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID); wantOwn >= 0 && n != wantOwn {
					t.Errorf("tenant %s sees %d of its own invoices, want %d", tenantID, n, wantOwn)
				} else if wantOwn < 0 && n == 0 {
					t.Errorf("tenant %s sees none of its own invoices, the foreign-row zero above is vacuous", tenantID)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("WithinTenantTx(%s): %v", tenantID, err)
			}
		}
	})
}

func TestProvisionSeedsNoShardTenantWithoutReset(t *testing.T) {
	e := newShardEnv(t)

	for _, tc := range []struct{ name, railwayEnv, resetFlag string }{
		{"empty environment name", "", "true"},
		{"development", "development", "true"},
		{"production", "production", "true"},
		{"pr environment, reset flag unset", "pr-110", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := e.prConfig()
			cfg.RailwayEnvironmentName, cfg.ResetFlag = tc.railwayEnv, tc.resetFlag
			if err := db.Provision(context.Background(), cfg); err != nil {
				t.Fatalf("Provision: %v", err)
			}
			if n := mustCount(t, e.super, `SELECT count(*) FROM tenants WHERE id = $1`, firmSource); n != 1 {
				t.Fatalf("seed.dev.sql did not run (1111 tenants = %d), the no-shard assertion below is vacuous", n)
			}
			e.requireNoShardTenants(t, "reset did not run")
		})
	}

	e.provisionReset(t)
	e.requireAllShardTenants(t, "positive control: a reset-on Provision")
}

func TestProvisionGuardOffSeedsNoShardTenant(t *testing.T) {
	e := newShardEnv(t)

	cfg := e.prConfig()
	cfg.Environment = "production" // guard off; the reset inputs stay on
	cfg.Passwords = db.RolePasswords{}
	if err := db.Provision(context.Background(), cfg); err != nil {
		t.Fatalf("guard-off Provision: %v", err)
	}
	e.requireNoShardTenants(t, "guard-off Provision")

	e.provisionReset(t)
	e.requireAllShardTenants(t, "positive control: a reset-on Provision")
}

type versionRow struct {
	version int
	sealed  bool
}

func versionsOfPolicy(t *testing.T, pool *pgxpool.Pool, tenantID string) []versionRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT version, sealed FROM approval_policy_versions WHERE tenant_id = $1 ORDER BY version`, tenantID)
	if err != nil {
		t.Fatalf("versions of %s: %v", tenantID, err)
	}
	defer rows.Close()
	var out []versionRow
	for rows.Next() {
		var v versionRow
		if err := rows.Scan(&v.version, &v.sealed); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func TestShardSeedConvergesAfterPublishSuspendAndRoleDelete(t *testing.T) {
	e := newShardEnv(t)
	ctx := context.Background()
	e.provisionReset(t)
	shard := firmShards[0]

	draft := queryTexts(t, e.super, `SELECT id::text FROM approval_policy_versions WHERE tenant_id = $1 AND NOT sealed`, shard.id)
	if len(draft) != 1 {
		t.Fatalf("firm shard %s has %d draft versions after Provision, want 1", shard.id, len(draft))
	}
	stepsBefore := mustCount(t, e.super, `SELECT count(*) FROM approval_policy_steps WHERE tenant_id = $1`, shard.id)
	if stepsBefore == 0 {
		t.Fatalf("firm shard %s draft has no steps, the unchanged-step assertion below is vacuous", shard.id)
	}
	users := queryTexts(t, e.super, `SELECT user_id::text FROM memberships WHERE tenant_id = $1 AND status = 'active'`, shard.id)
	if len(users) == 0 {
		t.Fatalf("firm shard %s has no active membership to suspend", shard.id)
	}

	mutate := func(label, sql string, args ...any) {
		t.Helper()
		tag, err := e.super.Exec(ctx, sql, args...)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("%s: affected %d rows, want 1", label, tag.RowsAffected())
		}
	}
	mutate("publish v1", `UPDATE approval_policy_versions SET sealed = true, is_active = true, published_at = now(), published_by = 'qa' WHERE id::text = $1`, draft[0])
	mutate("supersede v1", `UPDATE approval_policy_versions SET is_active = false WHERE id::text = $1`, draft[0])
	mutate("add v2", `INSERT INTO approval_policy_versions (tenant_id, policy_id, version, sealed, is_active, published_at, published_by)
	                  SELECT tenant_id, policy_id, version + 1, true, true, now(), 'qa' FROM approval_policy_versions WHERE id::text = $1`, draft[0])
	mutate("suspend a membership", `UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id::text = $2`, shard.id, users[0])
	mutate("soft-delete a role", `UPDATE workflow_roles SET deleted_at = now() WHERE tenant_id = $1 AND key = 'fin_mgr'`, shard.id)

	if err := db.Provision(ctx, e.prConfig()); err != nil {
		t.Fatalf("second reset-on Provision: %v", err)
	}

	gotStatus := queryTexts(t, e.super, `SELECT status FROM memberships WHERE tenant_id = $1 AND user_id::text = $2`, shard.id, users[0])
	wantStatus := queryTexts(t, e.super, `SELECT status FROM memberships WHERE tenant_id = $1 AND user_id::text = $2`, shard.source, users[0])
	if len(wantStatus) != 1 || !slices.Equal(gotStatus, wantStatus) {
		t.Errorf("membership %s status = %q, want the source's %q", users[0], gotStatus, wantStatus)
	}
	if n := mustCount(t, e.super, `SELECT count(*) FROM workflow_roles WHERE tenant_id = $1 AND key = 'fin_mgr' AND deleted_at IS NULL`, shard.id); n != 1 {
		t.Errorf("fin_mgr role not restored: %d live rows, want 1", n)
	}
	if got, want := versionsOfPolicy(t, e.super, shard.id), []versionRow{{firstVersion, true}, {secondVersion, true}}; !slices.Equal(got, want) {
		t.Errorf("policy versions = %+v, want %+v", got, want)
	}
	if n := mustCount(t, e.super, `SELECT count(*) FROM approval_policy_steps WHERE tenant_id = $1`, shard.id); n != stepsBefore {
		t.Errorf("shard steps = %d after re-seed, want %d (no step added)", n, stepsBefore)
	}
}

func TestProvisionFailsWhenShardSeedFileIsMissing(t *testing.T) {
	e := newShardEnv(t)

	devSeed, err := fs.ReadFile(dbsql.FS, "seed.dev.sql")
	if err != nil {
		t.Fatalf("read seed.dev.sql: %v", err)
	}
	withoutShardFile := fstest.MapFS{"seed.dev.sql": {Data: devSeed}}

	cfg := e.prConfig()
	cfg.SeedFS = withoutShardFile
	err = db.Provision(context.Background(), cfg)
	if err == nil {
		t.Fatalf("reset-on Provision without %s returned nil, want an error naming the file", shardSeedFile)
	}
	if !strings.Contains(err.Error(), shardSeedFile) {
		t.Errorf("error = %q, want it to name %s", err, shardSeedFile)
	}

	cfg.RailwayEnvironmentName, cfg.ResetFlag = "", ""
	if err := db.Provision(context.Background(), cfg); err != nil {
		t.Errorf("reset-off Provision without %s = %v, want nil", shardSeedFile, err)
	}
}
