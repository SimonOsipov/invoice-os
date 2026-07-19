// [M4-22-03] Test-first (RED) suite for the demo-curation + rule-re-enable half of
// db.Seed (task-162): folding db/demo-reset.sql's global rule re-enable and the 27
// curated business_entities rows into db/seed.dev.sql as an idempotent UPSERT, per
// binding decision [demo-seed-shape]. Pre-authored BEFORE that SQL exists — the
// checked-in db/seed.dev.sql only seeds tenants/memberships, so every case below is
// RED against it on an assertion (wrong row count, rule still disabled), never on a
// missing symbol or connection error; each test's doc comment says exactly which
// assertion fails today.
//
// Design mirrors this package's established conventions:
//   - Env-gated skip on DATABASE_SUPERUSER_URL only (db.Seed runs exclusively as the
//     superuser — tenants/business_entities are FORCE RLS), reusing seed_test.go's
//     requireSuperuserDSN/bootstrapSuperuserPool rather than duplicating them.
//   - Deliberately does NOT use the package's shared RLS harness (rls_harness_test.go,
//     requireHarness/h) — that harness additionally gates on DATABASE_URL/
//     DATABASE_MIGRATION_URL/DATABASE_READER_URL, all four required simultaneously,
//     which is a heavier precondition than this suite (and seed_test.go/
//     provision_test.go) needs. It reuses demo_reset_test.go's package-level
//     entityRow type and demoTenantID/honeywellTenantID constants (pure
//     data/types, no dependency on h), but defines its own pool-parameterized
//     fetch/reset helpers below rather than demo_reset_test.go's h.super-bound
//     fetchEntities/disableRule/ruleEnabled/disabledRuleCount, since this suite's
//     pool comes from bootstrapSuperuserPool(t, superDSN), not h.super.
//   - "against a migrated, empty database" (Test Spec row 1) is realized against the
//     shared dev/CI Postgres every other test in this package depends on by
//     explicitly clearing the demo tenant's own business_entities rows first
//     (resetDemoBusinessEntities) rather than requiring a literal empty schema — the
//     same practical interpretation TestRLS_DemoResetCuratesExactSet
//     (demo_reset_test.go) relies on for its own "empty portfolio" precondition.
//   - No t.Parallel(): every test shares the same demo-tenant business_entities rows
//     and the same global `rules` table (matches demo_reset_test.go's rationale).
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

// curatedDemoEntities is db/demo-reset.sql's 27 curated business_entities rows
// (name, tin, status), transcribed verbatim (demo-reset.sql:31-57) — the exact set
// task-162 requires db/seed.dev.sql to converge to via UPSERT. 21 active + 6
// archived. Order here matches the source file; comparisons below sort before
// asserting so this literal's ordering is not itself a hidden assumption.
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

// resetDemoBusinessEntities clears every business_entities row for the demo tenant
// (11111111-…), giving each test a known, empty-for-that-tenant starting point —
// this suite's stand-in for "against a migrated, empty database" against a shared
// DB. Scoped strictly by tenant_id, so it never touches any other tenant's rows.
func resetDemoBusinessEntities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM business_entities WHERE tenant_id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("clear demo tenant's business_entities (precondition): %v", err)
	}
}

// fetchDemoBusinessEntities returns every business_entities row for tenantID as
// (name, tin, status) tuples, ordered by name, queried via pool — the
// pool-parameterized counterpart to demo_reset_test.go's h.super-bound
// fetchEntities, needed because this suite's pool comes from
// bootstrapSuperuserPool(t, superDSN), not the shared RLS harness.
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

// sortedEntityRows returns a copy of rows sorted by name, matching
// fetchDemoBusinessEntities's ORDER BY name — so curatedDemoEntities (transcribed in
// db/demo-reset.sql's own row order) can be compared directly against a fetched,
// name-ordered result with reflect.DeepEqual.
func sortedEntityRows(rows []entityRow) []entityRow {
	out := make([]entityRow, len(rows))
	copy(out, rows)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// TestSeedCreatesCuratedDemoEntities: Test Spec row 1 / Core AC 4 (task-162 AC-1).
// After db.Seed runs against an empty demo-tenant portfolio, the demo tenant must
// have exactly the 27 curated business_entities rows — 21 active / 6 archived — and
// the (name, tin, status) set must equal db/demo-reset.sql's curated list exactly.
//
// RED against the checked-in db/seed.dev.sql (tenants/memberships only, no
// business_entities logic): Seed leaves the demo tenant's portfolio untouched, so
// fetchDemoBusinessEntities returns 0 rows — this fails the "got equals the 27-row
// curated set" reflect.DeepEqual assertion, not a compile/connection error.
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

// TestSeedDemoEntitiesIsIdempotent: Test Spec row 2 (task-162 AC-2). Running db.Seed
// twice must leave exactly 27 rows for the demo tenant — no duplication, no
// duplicate TIN — and the (name, tin, status) set must be byte-identical across both
// runs (the upsert conflict target is business_entities_tenant_tin_uq, so a second
// run with unchanged curated data must be a true no-op on top of the first).
//
// RED against the checked-in db/seed.dev.sql: business_entities stays untouched by
// either call, so len(first) = 0, failing the "want 27 after the FIRST Seed"
// assertion, not a compile/connection error.
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

// TestSeedRepairsMutatedDemoEntity: Test Spec row 3 (task-162 AC-3) — the test that
// distinguishes a real `DO UPDATE` upsert from a no-op `DO NOTHING`. Seeds the
// curated baseline, mutates one curated row's name and status in place (same tenant,
// same TIN — the conflict target), then re-runs Seed: the row must be restored to
// its curated (name, status), not left at the mutated values.
//
// RED against the checked-in db/seed.dev.sql: Seed never establishes the curated
// baseline in the first place (business_entities stays empty), so the "read back the
// repaired row" query at the end finds no row at all — this fails on the readback
// error/assertion, not a compile/connection error. (Once db/seed.dev.sql exists but
// uses ON CONFLICT ... DO NOTHING instead of DO UPDATE, this test is exactly what
// would catch that: the mutated name/status would survive the second Seed
// unchanged.)
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

// TestSeedDoesNotTouchOtherTenants: Test Spec row 4 (task-162 AC-4) — the regression
// guard for the dropped `DELETE FROM business_entities WHERE tenant_id = <demo>`
// (db/demo-reset.sql:31, deliberately NOT ported per binding decision
// [demo-seed-shape]: a per-PR env never accumulates rows, so there is nothing to
// clear, and porting the DELETE risks it one day being mis-scoped). Seeds a
// foreign-tenant (Honeywell, 2222…) business_entities probe row first, runs Seed,
// and asserts the probe survives byte-identical and no curated demo rows leak into
// Honeywell's portfolio.
//
// Also asserts the demo tenant itself reaches 27 curated rows in the SAME test, so
// this is a meaningful isolation check (the write reaches its own tenant) rather than
// a vacuous "Seed touches business_entities nowhere at all" pass.
//
// RED against the checked-in db/seed.dev.sql: business_entities logic doesn't exist
// yet, so the demo-tenant curated-count assertion fails first (0 != 27) — a genuine
// assertion failure, not a compile/connection error. (The foreign-tenant-untouched
// assertion alone would trivially — and misleadingly — pass today, since Seed
// currently writes to business_entities nowhere at all; the paired curated-count
// assertion is what keeps this test meaningfully RED.)
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

	// Precondition for the isolation check below to be meaningful: Seed must
	// actually write the curated rows for its own tenant.
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

// TestSeedReenablesDisabledRules: Test Spec row 5 (task-162 AC-5), folding in the
// idempotence half of the same row (RALPH coverage items 4+5): disables
// 'vat-standard-rate', runs Seed, asserts it (and every rule sharing that key across
// rule_set_versions, via the WHERE key = $1 predicate — key is unique only per
// (rule_set_version_id, key)) is re-enabled and the global disabled count is 0; runs
// Seed a SECOND time and asserts it is still 0 (idempotent) with no error — proving
// the UPDATE rules SET enabled = true WHERE enabled = false does not trip the M4-17
// rules_content_lock trigger (rules_content_lock's carve-out is enabled-only; ANY
// error from either Seed call here means either the repair failed or the lock fired).
//
// RED against the checked-in db/seed.dev.sql (no rules logic at all): the rule stays
// disabled after the first Seed, failing the "count(rules WHERE key=... AND
// enabled=false) == 0" assertion — a genuine assertion failure, not a
// compile/connection error.
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

	// Second Seed: idempotent, and must not trip the immutability lock either
	// (the active rule set is sealed — see M4-17/M4-18).
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (idempotency): %v", err)
	}
	disabledAfterSecond := mustCount(t, pool, `SELECT count(*) FROM rules WHERE enabled = false`)
	if disabledAfterSecond != 0 {
		t.Errorf("count(rules WHERE enabled=false) after the SECOND Seed = %d, want 0", disabledAfterSecond)
	}
}

// destructiveStatementPattern matches DELETE, TRUNCATE, or DROP as whole SQL
// keywords (word-bounded, case-insensitive) — TestSeedFileHasNoDestructiveStatements
// below strips `--` comments first so a comment merely mentioning one of these words
// (e.g. explaining why the file must not contain one) cannot trip a false positive.
var destructiveStatementPattern = regexp.MustCompile(`(?i)\b(DELETE|TRUNCATE|DROP)\b`)

// TestSeedFileHasNoDestructiveStatements: Test Spec row 6 (task-162 AC-7). A
// source-level, no-database assertion that the embedded db/seed.dev.sql bytes
// contain no DELETE, TRUNCATE, or DROP statement — mechanically pinning binding
// decision [demo-seed-shape] ("do NOT port the DELETE") structurally, so a future
// edit can't silently reintroduce a destructive statement into the boot-time seed.
//
// NOT RED by design (a regression-pinning guard, not new-behavior coverage,
// matching TestRLS_DemoResetLeavesOtherTenantUntouched's precedent in
// demo_reset_test.go): the checked-in db/seed.dev.sql already contains no
// DELETE/TRUNCATE/DROP today (it only INSERTs tenants/memberships), so this test
// already passes before task-162's SQL is added — it stays green through
// implementation specifically because the executor's additions must not introduce
// one.
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

// ---- Test Spec row 7 (task-162 AC-6): "the guard still gates seeding" -------------
//
// Deliberately NOT authored as a new test here. TestProvisionSkippedWhenGuardOff
// (provision_test.go:172) already covers this exact case: guard off
// (ENVIRONMENT="production"), SuperuserDSN set to a deliberately-unparseable poison
// value, MigrationDSN pointing at a real reachable migration DB. Because
// db.Provision's boot order is bootstrap -> migrate -> seed, and Bootstrap/Seed both
// share the SAME SuperuserDSN, if the guard failed to skip Seed (or Bootstrap) that
// call would attempt to dial the poison DSN and Provision would return a non-nil
// error containing its marker — which the test explicitly asserts does NOT appear.
// So "Provision succeeds with the guard off and a poison superuser DSN" already
// proves Seed (this subtask's new business_entities/rules writes included, once
// implemented — Seed has no code path that inspects *what* seed.dev.sql contains
// before deciding whether to run) was never invoked; a near-duplicate test asserting
// the same thing again would add no new coverage. See task-162's Test Spec row 7 /
// AC-6, and the M4-22-03 RALPH brief's explicit instruction not to re-author it.
