// INFRA-04-01 AC-8. Calls db.SeedShards directly, so it does not compile until
// that function exists; kept apart so the Provision-level tests in
// shard_seed_test.go stay runnable while it is missing.
package db_test

import (
	"context"
	"slices"
	"strings"
	"testing"

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

// AC-8
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
