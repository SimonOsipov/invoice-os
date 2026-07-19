// [M4-22-03] Suite for the demo-curation + rule-re-enable half of db.Seed
// (task-162): the boot-time UPSERT of 27 curated business_entities rows into
// the demo tenant, plus the global rule re-enable, per binding decision
// [demo-seed-shape].
//
// Design notes:
//   - Env-gated on DATABASE_SUPERUSER_URL only (db.Seed runs as the
//     superuser; tenants/business_entities are FORCE RLS), reusing
//     seed_test.go's requireSuperuserDSN/bootstrapSuperuserPool.
//   - Does not use the package's shared RLS harness (rls_harness_test.go) —
//     that harness requires all four DB DSNs; this suite only needs the
//     superuser one.
//   - No t.Parallel(): every test shares the demo tenant's business_entities
//     rows and the global rules table.
package db_test

import (
	"context"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// demoTenantID is the demo-tenant fixture id (db/seed.dev.sql) — Okafor &
// Partners, kind='firm'.
const demoTenantID = "11111111-1111-1111-1111-111111111111"

// honeywellTenantID is the second seeded tenant fixture (db/seed.dev.sql) —
// Honeywell Group, kind='in_house'.
const honeywellTenantID = "22222222-2222-2222-2222-222222222222"

// entityRow is a business_entities row's presentable identity. id is
// excluded: entity ids use gen_random_uuid() and aren't part of the fixed
// curated state.
type entityRow struct {
	name   string
	tin    string
	status string
}

// curatedDemoEntities is the 27 curated business_entities rows (21 active,
// 6 archived) db/seed.dev.sql's UPSERT converges the demo tenant to.
// Comparisons below sort both sides first, so this literal's declaration
// order isn't a hidden assumption.
var curatedDemoEntities = []entityRow{
	{name: "Adeyemi & Sons Trading Ltd", tin: "10012345-0001", status: "active"},
	{name: "Chukwu Global Ventures Ltd", tin: "10023456-0002", status: "active"},
	{name: "Okonkwo Textiles Nigeria Ltd", tin: "10034567-0003", status: "active"},
	{name: "Balogun Agro-Allied Ltd", tin: "10045678-0004", status: "active"},
	{name: "Emeka Pharmaceuticals Ltd", tin: "10056789-0005", status: "active"},
	{name: "Aliyu Logistics Services Ltd", tin: "10067890-0006", status: "active"},
	{name: "Ifeoma Fashion House Ltd", tin: "10078901-0007", status: "active"},
	{name: "Bello Construction Nigeria Ltd", tin: "10089012-0008", status: "active"},
	{name: "Nwosu Foods & Beverages Ltd", tin: "10090123-0009", status: "active"},
	{name: "Yakubu Motors Ltd", tin: "10101234-0010", status: "active"},
	{name: "Chidinma Cosmetics Ltd", tin: "10112345-0011", status: "active"},
	{name: "Obiora Steel Works Ltd", tin: "10123456-0012", status: "active"},
	{name: "Funmilayo Catering Services Ltd", tin: "10134567-0013", status: "active"},
	{name: "Danjuma Petroleum Ltd", tin: "10145678-0014", status: "active"},
	{name: "Ngozi Interiors Ltd", tin: "10156789-0015", status: "active"},
	{name: "Uche Digital Solutions Ltd", tin: "10167890-0016", status: "active"},
	{name: "Ibrahim Farms Ltd", tin: "10178901-0017", status: "active"},
	{name: "Amara Publishing Ltd", tin: "10189012-0018", status: "active"},
	{name: "Tunde Electricals Ltd", tin: "10190123-0019", status: "active"},
	{name: "Kemi Beauty Concepts Ltd", tin: "10201234-0020", status: "active"},
	{name: "Segun Haulage Ltd", tin: "10212345-0021", status: "active"},
	{name: "Olumide Printing Press Ltd", tin: "10223456-0022", status: "archived"},
	{name: "Halima Boutique Ltd", tin: "10234567-0023", status: "archived"},
	{name: "Chinwe Poultry Farms Ltd", tin: "10245678-0024", status: "archived"},
	{name: "Musa Hardware Stores Ltd", tin: "10256789-0025", status: "archived"},
	{name: "Bisi Event Planners Ltd", tin: "10267890-0026", status: "archived"},
	{name: "Ekene Auto Parts Ltd", tin: "10278901-0027", status: "archived"},
}

// resetDemoBusinessEntities clears the demo tenant's business_entities rows
// so each test starts from empty, without touching other tenants.
func resetDemoBusinessEntities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM business_entities WHERE tenant_id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("clear demo tenant's business_entities (precondition): %v", err)
	}
}

// fetchDemoBusinessEntities returns tenantID's business_entities rows as
// (name, tin, status), ordered by name.
func fetchDemoBusinessEntities(t *testing.T, pool *pgxpool.Pool, tenantID string) []entityRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT name, coalesce(tin, ''), status FROM business_entities WHERE tenant_id = $1 ORDER BY name`,
		tenantID,
	)
	if err != nil {
		t.Fatalf("query business_entities for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	var got []entityRow
	for rows.Next() {
		var r entityRow
		if err := rows.Scan(&r.name, &r.tin, &r.status); err != nil {
			t.Fatalf("scan business_entities row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate business_entities rows for tenant %s: %v", tenantID, err)
	}
	return got
}

// sortedEntityRows sorts a copy of rows by name, matching
// fetchDemoBusinessEntities's ORDER BY.
func sortedEntityRows(rows []entityRow) []entityRow {
	out := make([]entityRow, len(rows))
	copy(out, rows)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// TestSeedCreatesCuratedDemoEntities: Test Spec row 1 (task-162 AC-1). After
// Seed runs against an empty demo-tenant portfolio, the demo tenant has
// exactly the 27 curated rows (21 active / 6 archived).
func TestSeedCreatesCuratedDemoEntities(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	got := fetchDemoBusinessEntities(t, pool, demoTenantID)
	want := sortedEntityRows(curatedDemoEntities)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("business_entities for the demo tenant after Seed does not match the curated set exactly\ngot:  %+v\nwant: %+v", got, want)
	}

	var active, archived int
	for _, r := range got {
		switch r.status {
		case "active":
			active++
		case "archived":
			archived++
		}
	}
	if active != 21 {
		t.Errorf("count(active) = %d, want 21", active)
	}
	if archived != 6 {
		t.Errorf("count(archived) = %d, want 6", archived)
	}
}

// TestSeedDemoEntitiesIsIdempotent: Test Spec row 2 (task-162 AC-2). Running
// Seed twice leaves exactly 27 rows, no duplicate TIN, and byte-identical
// results across both runs.
func TestSeedDemoEntitiesIsIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	first := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(first) != 27 {
		t.Fatalf("count(business_entities) for the demo tenant after the FIRST Seed = %d, want 27", len(first))
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (idempotency): %v", err)
	}
	second := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(second) != 27 {
		t.Fatalf("count(business_entities) for the demo tenant after the SECOND Seed = %d, want 27 (no duplication)", len(second))
	}

	tins := make(map[string]int, len(second))
	for _, r := range second {
		tins[r.tin]++
	}
	for tin, n := range tins {
		if n != 1 {
			t.Errorf("TIN %q appears %d times after two Seed calls, want exactly 1 (unique)", tin, n)
		}
	}

	if !reflect.DeepEqual(first, second) {
		t.Errorf("curated (name,tin,status) set differs between the first and second Seed call, want byte-identical\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

// TestSeedRepairsMutatedDemoEntity: Test Spec row 3 (task-162 AC-3). Mutates
// a curated row's name/status in place, then re-runs Seed: the row must be
// restored — proves the upsert is DO UPDATE, not DO NOTHING.
func TestSeedRepairsMutatedDemoEntity(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish curated baseline): %v", err)
	}

	const curatedTIN = "10012345-0001" // curated row #1: Adeyemi & Sons Trading Ltd, active
	const curatedName = "Adeyemi & Sons Trading Ltd"
	if _, err := pool.Exec(ctx,
		`UPDATE business_entities SET name = 'MUTATED JUNK NAME', status = 'archived' WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	); err != nil {
		t.Fatalf("mutate curated row (precondition): %v", err)
	}

	var mutatedName, mutatedStatus string
	if err := pool.QueryRow(ctx,
		`SELECT name, status FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	).Scan(&mutatedName, &mutatedStatus); err != nil {
		t.Fatalf("read back mutated row (precondition): %v", err)
	}
	if mutatedName != "MUTATED JUNK NAME" || mutatedStatus != "archived" {
		t.Fatalf("precondition: row after mutation = (%q, %q), want (\"MUTATED JUNK NAME\", \"archived\")", mutatedName, mutatedStatus)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (repair): %v", err)
	}

	var name, status string
	if err := pool.QueryRow(ctx,
		`SELECT name, status FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	).Scan(&name, &status); err != nil {
		t.Fatalf("read back repaired row: %v", err)
	}
	if name != curatedName {
		t.Errorf("name after repair Seed = %q, want curated %q — an ON CONFLICT DO NOTHING upsert would leave the mutated name in place", name, curatedName)
	}
	if status != "active" {
		t.Errorf("status after repair Seed = %q, want curated %q — an ON CONFLICT DO NOTHING upsert would leave the mutated status in place", status, "active")
	}
}

// TestSeedDoesNotTouchOtherTenants: Test Spec row 4 (task-162 AC-4) —
// regression guard for the dropped cross-tenant DELETE ([demo-seed-shape]).
// Seeds a foreign-tenant probe row, runs Seed, and asserts the probe is
// untouched while the demo tenant reaches its 27 curated rows (the paired
// assertion, so this isn't a vacuous "touches nothing" pass).
func TestSeedDoesNotTouchOtherTenants(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	const probeTIN = "77999999-0099"
	if _, err := pool.Exec(ctx,
		`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		honeywellTenantID, probeTIN,
	); err != nil {
		t.Fatalf("clear stale probe row (precondition): %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, 'QA Foreign Tenant Probe', $2, 'active')`,
		honeywellTenantID, probeTIN,
	); err != nil {
		t.Fatalf("seed foreign-tenant probe row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`, honeywellTenantID, probeTIN)
	})

	before := fetchDemoBusinessEntities(t, pool, honeywellTenantID)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// Meaningful only if Seed actually wrote its own tenant's rows first.
	demoAfter := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(demoAfter) != 27 {
		t.Fatalf("count(business_entities) for the demo tenant after Seed = %d, want 27", len(demoAfter))
	}

	after := fetchDemoBusinessEntities(t, pool, honeywellTenantID)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("tenant %s's business_entities changed after Seed, want untouched\nbefore: %+v\nafter:  %+v", honeywellTenantID, before, after)
	}
	if len(after) != 1 {
		t.Errorf("tenant %s has %d business_entities row(s) after Seed, want exactly 1 (the probe) — no curated demo rows should leak into another tenant", honeywellTenantID, len(after))
	}
}

// TestSeedReenablesDisabledRules: Test Spec row 5 (task-162 AC-5). Disables
// a rule, runs Seed, and asserts it (and every rule sharing that key across
// rule_set_versions) is re-enabled; a second Seed stays idempotent. The
// enabled-only UPDATE must not trip the M4-17 rules_content_lock trigger —
// its carve-out only allows toggling `enabled`, which is exactly this repair.
func TestSeedReenablesDisabledRules(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	const ruleKey = "vat-standard-rate"
	if _, err := pool.Exec(ctx, `UPDATE rules SET enabled = false WHERE key = $1`, ruleKey); err != nil {
		t.Fatalf("disable rule %q (precondition): %v", ruleKey, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE rules SET enabled = true WHERE enabled = false`)
	})

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (rule re-enable): %v — must not trip rules_content_lock, since UPDATE ... SET enabled = true only touches the enabled column (the M3-06 kill-switch carve-out)", err)
	}

	stillDisabled := mustCount(t, pool, `SELECT count(*) FROM rules WHERE key = $1 AND enabled = false`, ruleKey)
	if stillDisabled != 0 {
		t.Errorf("rule key %q: %d row(s) still enabled=false after Seed, want 0", ruleKey, stillDisabled)
	}

	totalRules := mustCount(t, pool, `SELECT count(*) FROM rules`)
	enabledRules := mustCount(t, pool, `SELECT count(*) FROM rules WHERE enabled = true`)
	if enabledRules != totalRules {
		t.Errorf("count(rules WHERE enabled=true) = %d after Seed, want %d (the full rule count — no rule left disabled)", enabledRules, totalRules)
	}

	// Idempotent, and must not trip the same immutability lock either
	// (sealed rule set — see M4-17/M4-18).
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (idempotency): %v", err)
	}
	disabledAfterSecond := mustCount(t, pool, `SELECT count(*) FROM rules WHERE enabled = false`)
	if disabledAfterSecond != 0 {
		t.Errorf("count(rules WHERE enabled=false) after the SECOND Seed = %d, want 0", disabledAfterSecond)
	}
}

// destructiveStatementPattern matches DELETE, TRUNCATE, or DROP as whole
// keywords; TestSeedFileHasNoDestructiveStatements strips `--` comments
// first so a comment mentioning one of these words can't false-positive.
var destructiveStatementPattern = regexp.MustCompile(`(?i)\b(DELETE|TRUNCATE|DROP)\b`)

// TestSeedFileHasNoDestructiveStatements: Test Spec row 6 (task-162 AC-7).
// Pins binding decision [demo-seed-shape] structurally: the embedded
// db/seed.dev.sql must never contain DELETE, TRUNCATE, or DROP — a per-PR
// env never accumulates rows, so the boot-time seed has nothing to clear,
// and seed.dev.sql only UPSERTs the curated rows deliberately (never
// deletes) so it can't clobber a tenant's own data.
func TestSeedFileHasNoDestructiveStatements(t *testing.T) {
	b, err := fs.ReadFile(dbsql.FS, "seed.dev.sql")
	if err != nil {
		t.Fatalf("read embedded seed.dev.sql: %v", err)
	}

	var stripped strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if idx := strings.Index(line, "--"); idx != -1 {
			line = line[:idx]
		}
		stripped.WriteString(line)
		stripped.WriteString("\n")
	}

	if m := destructiveStatementPattern.FindString(stripped.String()); m != "" {
		t.Errorf("db/seed.dev.sql contains a destructive statement keyword %q — the boot-time seed must never DELETE, TRUNCATE, or DROP (binding decision [demo-seed-shape])", m)
	}
}

// TestSeedRecreatesDeletedDemoEntity: adversarial coverage for AC-1/AC-3 —
// TestSeedRepairsMutatedDemoEntity only proves a mutated row is restored;
// this proves a fully DELETEd curated row is recreated too (e.g. after a
// failed E2E test's incomplete cleanup).
func TestSeedRecreatesDeletedDemoEntity(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish curated baseline): %v", err)
	}

	const deletedTIN = "10278901-0027" // curated row #27: Ekene Auto Parts Ltd, archived
	res, err := pool.Exec(ctx,
		`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, deletedTIN,
	)
	if err != nil {
		t.Fatalf("delete curated row (precondition): %v", err)
	}
	if res.RowsAffected() != 1 {
		t.Fatalf("precondition: delete affected %d row(s), want exactly 1", res.RowsAffected())
	}

	afterDelete := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(afterDelete) != 26 {
		t.Fatalf("precondition: count(business_entities) after deleting one curated row = %d, want 26", len(afterDelete))
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (recreate the deleted row): %v", err)
	}

	got := fetchDemoBusinessEntities(t, pool, demoTenantID)
	want := sortedEntityRows(curatedDemoEntities)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("business_entities for the demo tenant after Seed recreates a fully-deleted curated row does not match the curated set exactly (an UPSERT alone must recreate a MISSING row, not just repair a mutated one)\ngot:  %+v\nwant: %+v", got, want)
	}
}

// TestSeedLeavesJunkRowsInPlace: pins the actual behavior of the dropped
// DELETE ([demo-seed-shape]) — a non-curated row (e.g. an E2E leftover)
// survives Seed untouched, since seed.dev.sql only upserts the 27 curated
// TINs.
func TestSeedLeavesJunkRowsInPlace(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	const junkTIN = "55555555-9999"
	const junkName = "Demo Client (E2E leftover)"
	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, $2, $3, 'active')`,
		demoTenantID, junkName, junkTIN,
	); err != nil {
		t.Fatalf("seed junk row (precondition): %v", err)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	got := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(got) != 28 {
		t.Fatalf("count(business_entities) for the demo tenant after Seed with one pre-existing junk row = %d, want 28 (27 curated + 1 surviving junk row — [demo-seed-shape] deliberately drops the DELETE, so junk is NOT cleaned by the boot-time seed)", len(got))
	}

	var found bool
	for _, r := range got {
		if r.tin == junkTIN {
			found = true
			if r.name != junkName || r.status != "active" {
				t.Errorf("junk row after Seed = %+v, want unchanged (name=%q, status=active)", r, junkName)
			}
		}
	}
	if !found {
		t.Errorf("junk row (tin=%q) not found after Seed, want it to survive untouched — Seed must never delete a row it did not itself curate", junkTIN)
	}
}

// TestSeedSameTINUnderDifferentTenantIsSafe: adversarial coverage for the
// UPSERT's conflict target, business_entities_tenant_tin_uq — a partial
// unique index scoped to (tenant_id, tin), not global. Seeds Honeywell with
// a row sharing one of the demo tenant's curated TINs, then asserts Seed
// succeeds and neither tenant's row bleeds into the other.
func TestSeedSameTINUnderDifferentTenantIsSafe(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	const sharedTIN = "10012345-0001" // curated row #1's TIN (Adeyemi & Sons Trading Ltd)
	const honeywellName = "Honeywell Cross-Tenant TIN Probe"

	if _, err := pool.Exec(ctx,
		`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		honeywellTenantID, sharedTIN,
	); err != nil {
		t.Fatalf("clear stale probe row (precondition): %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, $2, $3, 'active')`,
		honeywellTenantID, honeywellName, sharedTIN,
	); err != nil {
		t.Fatalf("seed Honeywell same-TIN row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`, honeywellTenantID, sharedTIN)
	})

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed with a same-TIN row pre-existing under a different tenant: %v — the partial unique index is scoped to (tenant_id, tin), so this must never collide", err)
	}

	var name, status string
	if err := pool.QueryRow(ctx,
		`SELECT name, status FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		honeywellTenantID, sharedTIN,
	).Scan(&name, &status); err != nil {
		t.Fatalf("read back Honeywell's row: %v", err)
	}
	if name != honeywellName || status != "active" {
		t.Errorf("Honeywell's row after Seed = (%q, %q), want unchanged (%q, \"active\") — the demo tenant's UPSERT must not bleed across tenants", name, status, honeywellName)
	}

	var demoName, demoStatus string
	if err := pool.QueryRow(ctx,
		`SELECT name, status FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, sharedTIN,
	).Scan(&demoName, &demoStatus); err != nil {
		t.Fatalf("read back demo tenant's curated row: %v", err)
	}
	if demoName != "Adeyemi & Sons Trading Ltd" || demoStatus != "active" {
		t.Errorf("demo tenant's row for TIN %q = (%q, %q), want (\"Adeyemi & Sons Trading Ltd\", \"active\")", sharedTIN, demoName, demoStatus)
	}
}

// Test Spec row 7 (task-162 AC-6, "the guard still gates seeding") is
// covered by TestProvisionSkippedWhenGuardOff in provision_test.go — not
// duplicated here.
