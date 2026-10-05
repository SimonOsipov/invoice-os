// Calls db.SeedShards directly; the Provision-level tests are in shard_seed_test.go.
package db_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Tables seed.e2e-shards.sql writes, each with the column naming its tenant.
var shardSeedTables = []struct{ table, tenantCol string }{
	{"tenants", "id"},
	{"memberships", "tenant_id"},
	{"workflow_roles", "tenant_id"},
	{"workflow_role_members", "tenant_id"},
	{"approval_policies", "tenant_id"},
	{"approval_policy_versions", "tenant_id"},
	{"approval_policy_steps", "tenant_id"},
	{"business_entities", "tenant_id"},
	{"invoices", "tenant_id"},
	{"line_items", "tenant_id"},
	{"invoice_status_history", "tenant_id"},
}

func TestShardSeedIsIdempotentAndLeavesTheDemoTenantsUntouched(t *testing.T) {
	e := newShardEnv(t)
	e.provisionReset(t)
	if len(db.DemoTenants) == 0 {
		t.Fatal("db.DemoTenants is empty, the demo-tenant digest below proves nothing")
	}

	counts := func() map[string]int {
		m := map[string]int{}
		for _, tb := range shardSeedTables {
			m[tb.table] = mustCount(t, e.super,
				`SELECT count(*) FROM `+tb.table+` WHERE `+tb.tenantCol+`::text = ANY($1::text[])`, shardIDs())
		}
		return m
	}
	// Digest of the ordered rows of the four seed.dev.sql tenants, per table.
	digests := func() map[string][]string {
		m := map[string][]string{}
		for _, tb := range shardSeedTables {
			m[tb.table] = queryTexts(t, e.super,
				`SELECT md5(coalesce(string_agg(r, '|' ORDER BY r), '')) || ' ' || count(*)
				   FROM (SELECT to_jsonb(x)::text AS r FROM `+tb.table+` x
				          WHERE `+tb.tenantCol+`::text = ANY($1::text[])) s`, db.DemoTenants)
		}
		return m
	}

	countsBefore, digestsBefore := counts(), digests()
	if countsBefore["tenants"] != len(allShards) || countsBefore["invoices"] == 0 {
		t.Fatalf("shard counts after Provision = %v, want %d tenants and some invoices", countsBefore, len(allShards))
	}
	for _, table := range []string{"tenants", "invoices"} {
		if strings.HasSuffix(digestsBefore[table][0], " 0") {
			t.Fatalf("demo tenants have no rows in %s, its digest below proves nothing", table)
		}
	}

	if err := db.SeedShards(context.Background(), e.superDSN, dbsql.FS); err != nil {
		t.Fatalf("second SeedShards without Reset: %v", err)
	}

	for table, n := range counts() {
		if n != countsBefore[table] {
			t.Errorf("shard rows in %s = %d after a second SeedShards, want %d", table, n, countsBefore[table])
		}
	}
	for table, d := range digests() {
		if !slices.Equal(d, digestsBefore[table]) {
			t.Errorf("demo-tenant rows in %s changed: %v, want %v", table, d, digestsBefore[table])
		}
	}
}

// seededMember is the subject prefix seed.dev.sql gives its members; a shard copies only these.
const seededMember = `c0000000-0000-0000-0000-%`

func TestShardSeedCopiesOnlySeededMembers(t *testing.T) {
	e := newShardEnv(t)
	e.provisionReset(t)
	deleteShardTenants(t, e.super)

	ctx := context.Background()
	extras := map[string]string{} // user -> source tenant
	for _, source := range []string{firmSource, inhouseSource} {
		user := uuid.NewString()
		extras[user] = source
		if _, err := e.super.Exec(ctx,
			`INSERT INTO memberships (tenant_id, user_id, role, display_name, email, status)
			 VALUES ($1, $2, 'admin', 'E2E Member', 'e2e-member@example.test', 'active')`, source, user); err != nil {
			t.Fatalf("insert an extra member into %s: %v", source, err)
		}
		t.Cleanup(func() {
			_, _ = e.super.Exec(context.Background(), `DELETE FROM memberships WHERE user_id = $1`, user)
		})
	}

	if err := db.SeedShards(ctx, e.superDSN, dbsql.FS); err != nil {
		t.Fatalf("SeedShards: %v", err)
	}

	for user, source := range extras {
		if n := mustCount(t, e.super, `SELECT count(*) FROM memberships WHERE user_id = $1 AND tenant_id = $2`, user, source); n != 1 {
			t.Fatalf("the extra member has %d rows in its source tenant %s, want 1, so a zero below proves nothing", n, source)
		}
		if n := mustCount(t, e.super, `SELECT count(*) FROM memberships WHERE user_id = $1 AND tenant_id::text = ANY($2::text[])`, user, shardIDs()); n != 0 {
			t.Errorf("the extra member of %s has %d rows in the shard tenants, want 0", source, n)
		}
	}
	for _, s := range allShards {
		want := mustCount(t, e.super, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id::text LIKE $2`, s.source, seededMember)
		if want == 0 {
			t.Fatalf("source %s has no seeded members, the count below proves nothing", s.source)
		}
		if got := mustCount(t, e.super, `SELECT count(*) FROM memberships WHERE tenant_id = $1`, s.id); got != want {
			t.Errorf("shard %s holds %d members, want the %d seeded members of %s", s.id, got, want, s.source)
		}
	}
}
