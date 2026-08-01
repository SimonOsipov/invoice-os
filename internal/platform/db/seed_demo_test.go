// [M4-22-03] Suite for the demo-curation + rule-re-enable half of db.Seed
// (task-162): the boot-time UPSERT of 27 curated business_entities rows into
// the demo tenant, plus the global rule re-enable, per binding decision
// [demo-seed-shape]. Also covers task-322: irn/csid/qr_payload on the demo
// tenant's seeded invoices (bottom of file).
//
// Design notes:
//   - Env-gated on DATABASE_SUPERUSER_URL only (db.Seed runs as the
//     superuser; tenants/business_entities are FORCE RLS), reusing
//     seed_test.go's requireSuperuserDSN/bootstrapSuperuserPool.
//   - Does not use the package's shared RLS harness (rls_harness_test.go) —
//     that harness requires all four DB DSNs; this suite only needs the
//     superuser one.
//   - No t.Parallel(): every test shares the demo tenant's business_entities
//     and invoices rows, plus the global rules table.
package db_test

import (
	"context"
	"encoding/base64"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/qrcode"
)

// demoTenantID is the demo-tenant fixture id (db/seed.dev.sql) — Okafor &
// Partners, kind='firm'.
const demoTenantID = "11111111-1111-1111-1111-111111111111"

// honeywellTenantID is the second seeded tenant fixture (db/seed.dev.sql) —
// Honeywell Group, kind='in_house'.
const honeywellTenantID = "22222222-2222-2222-2222-222222222222"

// entityRow is a business_entities row's presentable identity. id is
// excluded: entity ids use gen_random_uuid() and aren't part of the fixed
// curated state. sector added persona-handoff-fix step 5 (Task B) — the
// seed's INSERT column list grew from (tenant_id, name, tin, status) to
// include sector, so the shape this suite pins grows with it.
type entityRow struct {
	name   string
	tin    string
	sector string
	status string
}

// curatedDemoEntities is the 27 curated business_entities rows (21 active,
// 6 archived) db/seed.dev.sql's UPSERT converges the demo tenant to. sector
// values are copied verbatim from that file — a mismatch here means either
// this literal or the seed itself drifted. Comparisons below sort both sides
// first, so this literal's declaration order isn't a hidden assumption.
var curatedDemoEntities = []entityRow{
	{name: "Adeyemi & Sons Trading Ltd", tin: "10012345-0001", sector: "Trading", status: "active"},
	{name: "Chukwu Global Ventures Ltd", tin: "10023456-0002", sector: "Trading", status: "active"},
	{name: "Okonkwo Textiles Nigeria Ltd", tin: "10034567-0003", sector: "Textiles", status: "active"},
	{name: "Balogun Agro-Allied Ltd", tin: "10045678-0004", sector: "Agriculture", status: "active"},
	{name: "Emeka Pharmaceuticals Ltd", tin: "10056789-0005", sector: "Pharmaceuticals", status: "active"},
	{name: "Aliyu Logistics Services Ltd", tin: "10067890-0006", sector: "Logistics", status: "active"},
	{name: "Ifeoma Fashion House Ltd", tin: "10078901-0007", sector: "Fashion", status: "active"},
	{name: "Bello Construction Nigeria Ltd", tin: "10089012-0008", sector: "Construction", status: "active"},
	{name: "Nwosu Foods & Beverages Ltd", tin: "10090123-0009", sector: "Food & Beverage", status: "active"},
	{name: "Yakubu Motors Ltd", tin: "10101234-0010", sector: "Automotive", status: "active"},
	{name: "Chidinma Cosmetics Ltd", tin: "10112345-0011", sector: "Cosmetics", status: "active"},
	{name: "Obiora Steel Works Ltd", tin: "10123456-0012", sector: "Manufacturing", status: "active"},
	{name: "Funmilayo Catering Services Ltd", tin: "10134567-0013", sector: "Catering", status: "active"},
	{name: "Danjuma Petroleum Ltd", tin: "10145678-0014", sector: "Oil & Gas", status: "active"},
	{name: "Ngozi Interiors Ltd", tin: "10156789-0015", sector: "Interior Design", status: "active"},
	{name: "Uche Digital Solutions Ltd", tin: "10167890-0016", sector: "Technology", status: "active"},
	{name: "Ibrahim Farms Ltd", tin: "10178901-0017", sector: "Agriculture", status: "active"},
	{name: "Amara Publishing Ltd", tin: "10189012-0018", sector: "Publishing", status: "active"},
	{name: "Tunde Electricals Ltd", tin: "10190123-0019", sector: "Electricals", status: "active"},
	{name: "Kemi Beauty Concepts Ltd", tin: "10201234-0020", sector: "Beauty & Personal Care", status: "active"},
	{name: "Segun Haulage Ltd", tin: "10212345-0021", sector: "Logistics", status: "active"},
	{name: "Olumide Printing Press Ltd", tin: "10223456-0022", sector: "Printing", status: "archived"},
	{name: "Halima Boutique Ltd", tin: "10234567-0023", sector: "Retail", status: "archived"},
	{name: "Chinwe Poultry Farms Ltd", tin: "10245678-0024", sector: "Agriculture", status: "archived"},
	{name: "Musa Hardware Stores Ltd", tin: "10256789-0025", sector: "Retail", status: "archived"},
	{name: "Bisi Event Planners Ltd", tin: "10267890-0026", sector: "Events", status: "archived"},
	{name: "Ekene Auto Parts Ltd", tin: "10278901-0027", sector: "Automotive", status: "archived"},
}

// resetDemoBusinessEntities clears the demo tenant's business_entities rows
// so each test starts from empty, without touching other tenants.
//
// invoices.entity_id -> business_entities(id) is ON DELETE RESTRICT
// (migrations/20260714103137_invoices.sql: "an invoice is a durable legal/
// fiscal record ... must not be silently destroyed by a business_entities
// hard delete") -- db/seed.dev.sql now also seeds invoices against 6 of the
// 27 curated entities (persona-handoff-fix step 4, [demo-invoice-seed]), so
// deleting business_entities first would hit that RESTRICT (23001). Invoices
// are cleared FIRST -- line_items and invoice_status_history cascade off
// invoice_id (ON DELETE CASCADE on both) -- so the entities delete below
// always succeeds regardless of what Seed most recently wrote.
func resetDemoBusinessEntities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM invoices WHERE tenant_id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("clear demo tenant's invoices (precondition): %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM business_entities WHERE tenant_id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("clear demo tenant's business_entities (precondition): %v", err)
	}
}

// fetchDemoBusinessEntities returns tenantID's business_entities rows as
// (name, tin, sector, status), ordered by name. sector is coalesced to ''
// like tin — a junk/probe row inserted without one (several tests below)
// has sector NULL, and entityRow has no room for a NULL.
func fetchDemoBusinessEntities(t *testing.T, pool *pgxpool.Pool, tenantID string) []entityRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT name, coalesce(tin, ''), coalesce(sector, ''), status FROM business_entities WHERE tenant_id = $1 ORDER BY name`,
		tenantID,
	)
	if err != nil {
		t.Fatalf("query business_entities for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	var got []entityRow
	for rows.Next() {
		var r entityRow
		if err := rows.Scan(&r.name, &r.tin, &r.sector, &r.status); err != nil {
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

	const curatedTIN = "10012345-0001" // curated row #1: Adeyemi & Sons Trading Ltd, active, sector Trading
	const curatedName = "Adeyemi & Sons Trading Ltd"
	const curatedSector = "Trading"
	// sector mutated too (persona-handoff-fix step 5, Task B): proves the DO UPDATE
	// clause's `sector = EXCLUDED.sector` repairs a hand-edited NON-NULL sector, not
	// just name/status. TestSeedBackfillsSectorOntoPreexistingRow below covers the
	// OTHER sector case — a pre-existing row where sector was never set at all (NULL).
	if _, err := pool.Exec(ctx,
		`UPDATE business_entities SET name = 'MUTATED JUNK NAME', sector = 'MUTATED JUNK SECTOR', status = 'archived' WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	); err != nil {
		t.Fatalf("mutate curated row (precondition): %v", err)
	}

	var mutatedName, mutatedSector, mutatedStatus string
	if err := pool.QueryRow(ctx,
		`SELECT name, sector, status FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	).Scan(&mutatedName, &mutatedSector, &mutatedStatus); err != nil {
		t.Fatalf("read back mutated row (precondition): %v", err)
	}
	if mutatedName != "MUTATED JUNK NAME" || mutatedSector != "MUTATED JUNK SECTOR" || mutatedStatus != "archived" {
		t.Fatalf("precondition: row after mutation = (%q, %q, %q), want (\"MUTATED JUNK NAME\", \"MUTATED JUNK SECTOR\", \"archived\")", mutatedName, mutatedSector, mutatedStatus)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (repair): %v", err)
	}

	var name, sector, status string
	if err := pool.QueryRow(ctx,
		`SELECT name, sector, status FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	).Scan(&name, &sector, &status); err != nil {
		t.Fatalf("read back repaired row: %v", err)
	}
	if name != curatedName {
		t.Errorf("name after repair Seed = %q, want curated %q — an ON CONFLICT DO NOTHING upsert would leave the mutated name in place", name, curatedName)
	}
	if sector != curatedSector {
		t.Errorf("sector after repair Seed = %q, want curated %q — a DO UPDATE clause missing `sector = EXCLUDED.sector` would leave the mutated sector in place", sector, curatedSector)
	}
	if status != "active" {
		t.Errorf("status after repair Seed = %q, want curated %q — an ON CONFLICT DO NOTHING upsert would leave the mutated status in place", status, "active")
	}
}

// TestSeedBackfillsSectorOntoPreexistingRow: persona-handoff-fix step 5 (Task B).
// PR/dev environments deployed BEFORE this story have curated business_entities rows
// with sector left NULL — the seed's own INSERT column list used to be (tenant_id,
// name, tin, status), sector wasn't in it at all. This simulates that pre-existing
// state directly (bypassing Seed, inserting the row exactly as an older deploy would
// have left it) and asserts a Seed run BACKFILLS sector — proving the [demo-seed-shape]
// UPSERT's `DO UPDATE SET ... sector = EXCLUDED.sector` repairs a NULL, not just a
// mutated non-NULL value (TestSeedRepairsMutatedDemoEntity above only covers the
// latter — a DO NOTHING, or a DO UPDATE that omitted sector from its SET list, would
// both pass that test while still leaving a NULL sector like this one unfixed forever).
func TestSeedBackfillsSectorOntoPreexistingRow(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	const curatedTIN = "10012345-0001" // curated row #1: Adeyemi & Sons Trading Ltd
	const curatedName = "Adeyemi & Sons Trading Ltd"
	const curatedSector = "Trading"

	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, $2, $3, 'active')`,
		demoTenantID, curatedName, curatedTIN,
	); err != nil {
		t.Fatalf("seed pre-existing sector-less row (precondition): %v", err)
	}

	var precondSector *string
	if err := pool.QueryRow(ctx,
		`SELECT sector FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	).Scan(&precondSector); err != nil {
		t.Fatalf("read back precondition row: %v", err)
	}
	if precondSector != nil {
		t.Fatalf("precondition: sector = %v, want NULL (this row must start exactly as an older, pre-sector deploy would leave it)", *precondSector)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed (sector backfill): %v", err)
	}

	var sector *string
	if err := pool.QueryRow(ctx,
		`SELECT sector FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	).Scan(&sector); err != nil {
		t.Fatalf("read back row after Seed: %v", err)
	}
	if sector == nil || *sector != curatedSector {
		got := "NULL"
		if sector != nil {
			got = *sector
		}
		t.Errorf("sector after Seed = %s, want %q — a DO NOTHING upsert (or a DO UPDATE missing sector from its SET clause) would leave this NULL forever, exactly the state every PR/dev env deployed before this story is in", got, curatedSector)
	}
}

// curatedHoneywellEntity is Honeywell's own single curated business_entities
// row (task-304, INVCR-01-19, AC-1) — db/seed.dev.sql's second INSERT block,
// seeded so the in-house persona has a real entity to resolve and file
// against (App.tsx's resolveActiveClient, frontend/app/src/lib/clients.ts).
// The TIN is deliberately the hyphenated NNNNNNNN-NNNN wire spelling
// (supplier-tin-format's own shape, internal/invoice/supplier_tin.go), not a
// 10-digit JTB TIN and not a 12-bare-digit canonical FIRS TIN —
// MBSSupplierTIN only rewrites the latter, so this literal must stay exactly
// as seeded or a manual/imported invoice against Honeywell would fail
// supplier-tin-format.
var curatedHoneywellEntity = entityRow{name: "Honeywell Group", tin: "20665510-0001", sector: "Manufacturing", status: "active"}

// resetHoneywellBusinessEntities mirrors resetDemoBusinessEntities above,
// scoped to honeywellTenantID — clears any invoices then business_entities
// rows so each test below starts from a genuinely empty Honeywell portfolio.
func resetHoneywellBusinessEntities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM invoices WHERE tenant_id = $1`, honeywellTenantID,
	); err != nil {
		t.Fatalf("clear Honeywell's invoices (precondition): %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM business_entities WHERE tenant_id = $1`, honeywellTenantID,
	); err != nil {
		t.Fatalf("clear Honeywell's business_entities (precondition): %v", err)
	}
}

// TestSeedCreatesCuratedHoneywellEntity: task-304 (INVCR-01-19) AC-1 — after
// Seed runs against an empty Honeywell portfolio, the in-house tenant has
// EXACTLY the one curated row db/seed.dev.sql now seeds for it, with the
// literal TIN spelling AC-1 requires. QA gap-fill: no existing test pinned
// this. The demo-tenant suite above (TestSeedCreatesCuratedDemoEntities etc.)
// is scoped to demoTenantID only, and TestSeedDoesNotTouchOtherTenants below
// was repointed OFF Honeywell onto Tenant A the moment this row started
// existing (see foreignTenantID's own doc comment) — so nothing else in this
// file actually proves the new row's shape, count, or literal TIN spelling.
func TestSeedCreatesCuratedHoneywellEntity(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetHoneywellBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	got := fetchDemoBusinessEntities(t, pool, honeywellTenantID)
	want := []entityRow{curatedHoneywellEntity}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("business_entities for Honeywell after Seed = %+v, want exactly one curated row %+v", got, want)
	}
}

// TestSeedHoneywellEntityIsIdempotent: companion to
// TestSeedDemoEntitiesIsIdempotent, scoped to Honeywell's one-row block —
// running Seed twice must leave exactly one row, byte-identical, never a
// duplicate under the (tenant_id, tin) partial unique index.
func TestSeedHoneywellEntityIsIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetHoneywellBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	first := fetchDemoBusinessEntities(t, pool, honeywellTenantID)
	if len(first) != 1 {
		t.Fatalf("count(business_entities) for Honeywell after the FIRST Seed = %d, want 1", len(first))
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (idempotency): %v", err)
	}
	second := fetchDemoBusinessEntities(t, pool, honeywellTenantID)
	if len(second) != 1 {
		t.Fatalf("count(business_entities) for Honeywell after the SECOND Seed = %d, want 1 (no duplication)", len(second))
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Honeywell's curated row differs between the first and second Seed call, want byte-identical\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

// TestSeedRepairsMutatedHoneywellEntity: companion to
// TestSeedRepairsMutatedDemoEntity — mutates Honeywell's curated row's
// name/sector/status in place, then re-runs Seed: the row must be restored,
// proving the Honeywell block's upsert is DO UPDATE, not DO NOTHING, same as
// the demo tenant's block.
func TestSeedRepairsMutatedHoneywellEntity(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetHoneywellBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish curated baseline): %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE business_entities SET name = 'MUTATED JUNK NAME', sector = 'MUTATED JUNK SECTOR', status = 'archived' WHERE tenant_id = $1 AND tin = $2`,
		honeywellTenantID, curatedHoneywellEntity.tin,
	); err != nil {
		t.Fatalf("mutate curated row (precondition): %v", err)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (repair): %v", err)
	}

	var name, sector, status string
	if err := pool.QueryRow(ctx,
		`SELECT name, sector, status FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		honeywellTenantID, curatedHoneywellEntity.tin,
	).Scan(&name, &sector, &status); err != nil {
		t.Fatalf("read back repaired row: %v", err)
	}
	if name != curatedHoneywellEntity.name || sector != curatedHoneywellEntity.sector || status != curatedHoneywellEntity.status {
		t.Errorf("Honeywell's row after repair Seed = (%q, %q, %q), want curated (%q, %q, %q) — an ON CONFLICT DO NOTHING upsert would leave the mutated values in place",
			name, sector, status, curatedHoneywellEntity.name, curatedHoneywellEntity.sector, curatedHoneywellEntity.status)
	}
}

// foreignTenantID: task-304 (INVCR-01-19) repointed this test off
// honeywellTenantID onto "Tenant A (dev)". Honeywell stopped being a neutral
// "other tenant" the moment db/seed.dev.sql started seeding it its OWN one
// curated business_entities row (task-304 AC-1, so an in-house sign-in has a
// real entity to file against) — Seed now legitimately writes Honeywell's
// table too, which is exactly the kind of write this test exists to rule
// OUT for a tenant Seed has no business touching. Tenant A has no
// business_entities curation anywhere in seed.dev.sql (only the demo/firm
// tenant's 27-row block and Honeywell's own 1-row block name a tenant_id),
// so it is still the neutral bystander the original test wanted — this is a
// swap of WHICH tenant stands in for "other", not a weakening of the claim.
const foreignTenantID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

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
		foreignTenantID, probeTIN,
	); err != nil {
		t.Fatalf("clear stale probe row (precondition): %v", err)
	}

	// Precondition, made explicit rather than left implicit in the post-Seed
	// count below: foreignTenantID must have ZERO business_entities rows of
	// its own BEFORE this test plants its probe. If db/seed.dev.sql is ever
	// edited to seed Tenant A something of its own, this fails LOUDLY and
	// immediately, naming the actual cause -- rather than surfacing later as
	// a confusing "want 1, got N+1" from the post-Seed assertion, which would
	// look like a Seed bug rather than what it actually is (this test's
	// "foreign tenant" stand-in silently stopped being neutral).
	if pre := fetchDemoBusinessEntities(t, pool, foreignTenantID); len(pre) != 0 {
		t.Fatalf("precondition: tenant %s already has %d business_entities row(s) before this test ran — it is no longer a neutral \"foreign tenant\" stand-in (has db/seed.dev.sql started seeding it something?); pick a different tenant for this test", foreignTenantID, len(pre))
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin, status) VALUES ($1, 'QA Foreign Tenant Probe', $2, 'active')`,
		foreignTenantID, probeTIN,
	); err != nil {
		t.Fatalf("seed foreign-tenant probe row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`, foreignTenantID, probeTIN)
	})

	before := fetchDemoBusinessEntities(t, pool, foreignTenantID)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// Meaningful only if Seed actually wrote its own tenant's rows first.
	demoAfter := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(demoAfter) != 27 {
		t.Fatalf("count(business_entities) for the demo tenant after Seed = %d, want 27", len(demoAfter))
	}

	after := fetchDemoBusinessEntities(t, pool, foreignTenantID)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("tenant %s's business_entities changed after Seed, want untouched\nbefore: %+v\nafter:  %+v", foreignTenantID, before, after)
	}
	if len(after) != 1 {
		t.Errorf("tenant %s has %d business_entities row(s) after Seed, want exactly 1 (the probe) — no curated demo rows should leak into another tenant", foreignTenantID, len(after))
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

// task-322: irn/csid/qr_payload on the demo tenant's seeded `accepted` invoices
// (Core AC-5). Ground truth verified against db/seed.dev.sql's invoice_seed CTE: 27
// DEMO-2026-* invoices, 8 of them accepted.
const (
	demoInvoiceTotalCount    = 27
	demoAcceptedInvoiceCount = 8

	// demoIRNServiceID / demoIRNDateLayout mirror mock_script.go's mockServiceID /
	// mockIRNDateLayout — the IRN shape is <docRef>-FBMOCK01-<YYYYMMDD>.
	demoIRNServiceID  = "FBMOCK01"
	demoIRNDateLayout = "20060102"
)

// rawURLBase64Pattern is base64.RawURLEncoding's own alphabet, unpadded: no '+', '/',
// '=', and no '\n'. Postgres encode(bytea,'base64') wraps output with a newline every 76
// characters — a corruption the char_length CHECK and qrcode.RenderBase64 both let
// through silently (Stage 1 architecture validation, ADD 1).
var rawURLBase64Pattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// qrPayloadShapePattern pins the decoded qr_payload's exact key order (irn,csid,tin,amt,
// cur) and zero whitespace. jsonb_build_object reorders keys by length and
// json_build_object inserts spaces around ':' — decoding into a map or struct would pass
// against either wrong shape, so this matches the literal string instead (Stage 1
// architecture validation, ADD 2).
var qrPayloadShapePattern = regexp.MustCompile(`^\{"irn":"([^"]*)","csid":"([^"]*)","tin":"([^"]*)","amt":"([^"]*)","cur":"([^"]*)"\}$`)

// fiscalInvoiceRow is a seeded DEMO-2026-* invoice's identity plus the three
// fiscal-outcome columns. irn/csid/qrPayload are *string (not coalesced) since NULL vs
// "" is exactly what AC-1/AC-2 turn on.
type fiscalInvoiceRow struct {
	invoiceNumber string
	status        string
	issueDate     time.Time
	supplierTIN   string
	currency      string
	total         string // numeric(14,2) rendered via ::text
	irn           *string
	csid          *string
	qrPayload     *string
}

// fetchDemoFiscalInvoices returns tenantID's DEMO-2026-* seeded invoices (the
// invoice_seed CTE's own rows, never a junk/probe row a test planted), ordered by
// invoice_number for deterministic assertions.
func fetchDemoFiscalInvoices(t *testing.T, pool *pgxpool.Pool, tenantID string) []fiscalInvoiceRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT invoice_number, status, issue_date, coalesce(supplier_tin, ''), coalesce(currency, ''),
		        total::text, irn, csid, qr_payload
		 FROM invoices
		 WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%'
		 ORDER BY invoice_number`,
		tenantID,
	)
	if err != nil {
		t.Fatalf("query DEMO-2026-* invoices for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	var got []fiscalInvoiceRow
	for rows.Next() {
		var r fiscalInvoiceRow
		if err := rows.Scan(&r.invoiceNumber, &r.status, &r.issueDate, &r.supplierTIN, &r.currency,
			&r.total, &r.irn, &r.csid, &r.qrPayload); err != nil {
			t.Fatalf("scan invoices row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate invoices rows for tenant %s: %v", tenantID, err)
	}
	return got
}

// decodeQRPayload base64url-decodes payload and asserts its decoded JSON matches
// mock_script.go's mockQR compact shape exactly, returning the five embedded fields.
// Fatal rather than Error: a caller can't meaningfully compare embedded values once the
// shape itself is wrong.
func decodeQRPayload(t *testing.T, payload string) (irn, csid, tin, amt, cur string) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("qr_payload %q is not valid base64url: %v", payload, err)
	}
	m := qrPayloadShapePattern.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf(`decoded qr_payload = %q, want compact JSON {"irn":..,"csid":..,"tin":..,"amt":..,"cur":..} in exactly that key order with no whitespace`, string(raw))
	}
	return m[1], m[2], m[3], m[4], m[5]
}

// TestSeedPopulatesFiscalIdentifiersOnAcceptedInvoices: Test Spec row 1 (task-322 AC-1).
// Every seeded `accepted` invoice gets a non-null, non-empty irn/csid/qr_payload, each
// pinned to the mock adapter's exact shape (mock_script.go:221-250) — not just
// "present", since a newline-corrupted or key-reordered payload would satisfy a weaker
// check while still failing to decode for a real consumer.
func TestSeedPopulatesFiscalIdentifiersOnAcceptedInvoices(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	all := fetchDemoFiscalInvoices(t, pool, demoTenantID)
	var accepted []fiscalInvoiceRow
	for _, r := range all {
		if r.status == "accepted" {
			accepted = append(accepted, r)
		}
	}
	if len(accepted) != demoAcceptedInvoiceCount {
		t.Fatalf("count(accepted DEMO-2026-* invoices) = %d, want %d", len(accepted), demoAcceptedInvoiceCount)
	}

	for _, r := range accepted {
		if r.irn == nil || *r.irn == "" {
			t.Errorf("%s: irn = %v, want non-null non-empty", r.invoiceNumber, r.irn)
			continue
		}
		if r.csid == nil || *r.csid == "" {
			t.Errorf("%s: csid = %v, want non-null non-empty", r.invoiceNumber, r.csid)
			continue
		}
		if r.qrPayload == nil || *r.qrPayload == "" {
			t.Errorf("%s: qr_payload = %v, want non-null non-empty", r.invoiceNumber, r.qrPayload)
			continue
		}

		wantIRN := r.invoiceNumber + "-" + demoIRNServiceID + "-" + r.issueDate.Format(demoIRNDateLayout)
		if *r.irn != wantIRN {
			t.Errorf("%s: irn = %q, want %q (<docRef>-FBMOCK01-<YYYYMMDD>)", r.invoiceNumber, *r.irn, wantIRN)
		}

		if len(*r.csid) != 43 {
			t.Errorf("%s: len(csid) = %d, want 43 (unpadded base64url of a 32-byte sha256 digest)", r.invoiceNumber, len(*r.csid))
		}
		if !rawURLBase64Pattern.MatchString(*r.csid) {
			t.Errorf("%s: csid = %q, contains characters outside base64.RawURLEncoding's alphabet", r.invoiceNumber, *r.csid)
		}

		// The newline trap: Postgres encode(bytea,'base64') wraps at 76 chars with '\n'; a
		// direct check on the raw column is the only thing that catches it (ADD 1).
		if strings.ContainsAny(*r.qrPayload, " \t\r\n") {
			t.Errorf("%s: qr_payload contains whitespace — encode(...,'base64') wraps every 76 chars; wanted translate(...,'+/=\\n','-_')", r.invoiceNumber)
		}
		if !rawURLBase64Pattern.MatchString(*r.qrPayload) {
			t.Errorf("%s: qr_payload = %q, contains characters outside base64.RawURLEncoding's alphabet", r.invoiceNumber, *r.qrPayload)
		}

		irn, csid, tin, amt, cur := decodeQRPayload(t, *r.qrPayload)
		if irn != *r.irn {
			t.Errorf("%s: qr_payload embeds irn=%q, want the row's own irn %q", r.invoiceNumber, irn, *r.irn)
		}
		if csid != *r.csid {
			t.Errorf("%s: qr_payload embeds csid=%q, want the row's own csid %q", r.invoiceNumber, csid, *r.csid)
		}
		if tin != r.supplierTIN {
			t.Errorf("%s: qr_payload embeds tin=%q, want the row's own SUPPLIER tin %q (not the buyer TIN — mock_script.go:239-240)", r.invoiceNumber, tin, r.supplierTIN)
		}
		amtF, errAmt := strconv.ParseFloat(amt, 64)
		totalF, errTotal := strconv.ParseFloat(r.total, 64)
		if errAmt != nil {
			t.Errorf("%s: qr_payload embeds amt=%q, not a number: %v", r.invoiceNumber, amt, errAmt)
		} else if errTotal != nil {
			t.Fatalf("%s: row's own total = %q, not a number: %v", r.invoiceNumber, r.total, errTotal)
		} else if amtF != totalF {
			t.Errorf("%s: qr_payload embeds amt=%q, want the row's own TOTAL %q (not the subtotal)", r.invoiceNumber, amt, r.total)
		}
		if cur != r.currency {
			t.Errorf("%s: qr_payload embeds cur=%q, want the row's own currency %q", r.invoiceNumber, cur, r.currency)
		}
	}

	// A constant CSID would pass every check above (length, alphabet, self-consistency
	// against qr_payload) — only cross-row comparison catches it.
	seen := make(map[string]string, len(accepted))
	for _, r := range accepted {
		if r.csid == nil {
			continue
		}
		if dupe, ok := seen[*r.csid]; ok {
			t.Errorf("%s and %s share csid %q, want distinct per row", dupe, r.invoiceNumber, *r.csid)
		}
		seen[*r.csid] = r.invoiceNumber
	}
}

// TestSeedLeavesFiscalIdentifiersNullOnNonAcceptedInvoices: Test Spec row 2 (task-322
// AC-2). A stray irn is invoices' "already cleared" sentinel
// (internal/invoice/submission_port.go:36) and the reconciliation.go
// irn_without_accepted drift signature, so leaking one onto a non-accepted row makes it
// permanently unsubmittable, not just untidy.
func TestSeedLeavesFiscalIdentifiersNullOnNonAcceptedInvoices(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	all := fetchDemoFiscalInvoices(t, pool, demoTenantID)
	var nonAccepted []fiscalInvoiceRow
	for _, r := range all {
		if r.status != "accepted" {
			nonAccepted = append(nonAccepted, r)
		}
	}
	if want := demoInvoiceTotalCount - demoAcceptedInvoiceCount; len(nonAccepted) != want {
		t.Fatalf("count(non-accepted DEMO-2026-* invoices) = %d, want %d", len(nonAccepted), want)
	}

	for _, r := range nonAccepted {
		if r.irn != nil {
			t.Errorf("%s (status=%s): irn = %q, want NULL", r.invoiceNumber, r.status, *r.irn)
		}
		if r.csid != nil {
			t.Errorf("%s (status=%s): csid = %q, want NULL", r.invoiceNumber, r.status, *r.csid)
		}
		if r.qrPayload != nil {
			t.Errorf("%s (status=%s): qr_payload = %q, want NULL", r.invoiceNumber, r.status, *r.qrPayload)
		}
	}
}

// TestSeedBackfillsFiscalIdentifiersOntoPreexistingAcceptedRows: Test Spec row 3
// (task-322 AC-3/AC-4) — the load-bearing case, proving the ON CONFLICT ... DO UPDATE
// SET list backfills these columns, not just the INSERT (db/seed.dev.sql:66-69's sector
// precedent). Mirrors TestSeedBackfillsSectorOntoPreexistingRow's seed -> null out ->
// assert precondition -> re-seed -> assert repopulated shape, including the precondition
// assertion — without it a passing test proves nothing.
func TestSeedBackfillsFiscalIdentifiersOntoPreexistingAcceptedRows(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish baseline): %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE invoices SET irn = NULL, csid = NULL, qr_payload = NULL
		 WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%' AND status = 'accepted'`,
		demoTenantID,
	); err != nil {
		t.Fatalf("null out fiscal columns on the accepted demo rows (precondition): %v", err)
	}

	precond := fetchDemoFiscalInvoices(t, pool, demoTenantID)
	for _, r := range precond {
		if r.status != "accepted" {
			continue
		}
		if r.irn != nil || r.csid != nil || r.qrPayload != nil {
			t.Fatalf("precondition: %s still has a non-null fiscal column (irn=%v csid=%v qr_payload=%v), want all three NULL — this row must start exactly as a pre-this-story deploy would leave it", r.invoiceNumber, r.irn, r.csid, r.qrPayload)
		}
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (backfill): %v", err)
	}

	after := fetchDemoFiscalInvoices(t, pool, demoTenantID)
	var checked int
	for _, r := range after {
		if r.status != "accepted" {
			continue
		}
		checked++
		if r.irn == nil || *r.irn == "" {
			t.Errorf("%s: irn after backfill Seed = %v, want non-null non-empty — a DO UPDATE missing irn from its SET clause would leave this NULL forever", r.invoiceNumber, r.irn)
		}
		if r.csid == nil || *r.csid == "" {
			t.Errorf("%s: csid after backfill Seed = %v, want non-null non-empty — a DO UPDATE missing csid from its SET clause would leave this NULL forever", r.invoiceNumber, r.csid)
		}
		if r.qrPayload == nil || *r.qrPayload == "" {
			t.Errorf("%s: qr_payload after backfill Seed = %v, want non-null non-empty — a DO UPDATE missing qr_payload from its SET clause would leave this NULL forever", r.invoiceNumber, r.qrPayload)
		}
	}
	if checked != demoAcceptedInvoiceCount {
		t.Fatalf("checked %d accepted rows after backfill Seed, want %d", checked, demoAcceptedInvoiceCount)
	}
}

// TestSeedFiscalIdentifiersAreIdempotent: Test Spec row 4 (task-322 AC-5). The values
// are DERIVED (sha256 of the invoice's own content, mock_script.go's
// mockIdentifiersFor), not randomised, so a second Seed must reproduce them
// byte-identical, not merely leave them non-null.
func TestSeedFiscalIdentifiersAreIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	first := fetchDemoFiscalInvoices(t, pool, demoTenantID)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (idempotency): %v", err)
	}
	second := fetchDemoFiscalInvoices(t, pool, demoTenantID)

	if !reflect.DeepEqual(first, second) {
		t.Errorf("DEMO-2026-* invoices' fiscal columns differ between the first and second Seed call, want byte-identical\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

// TestSeedLeavesJunkInvoiceFiscalColumnsUntouched: Test Spec row 5 (task-322, residue
// preserved) — companion to TestSeedLeavesJunkRowsInPlace. A non-curated invoice_number
// never matches Seed's ON CONFLICT target (invoices_tenant_entity_number_uq), so its
// fiscal columns must survive untouched.
func TestSeedLeavesJunkInvoiceFiscalColumnsUntouched(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish curated entities): %v", err)
	}

	const curatedTIN = "10012345-0001" // Adeyemi & Sons Trading Ltd
	var entityID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, curatedTIN,
	).Scan(&entityID); err != nil {
		t.Fatalf("read back curated entity id (precondition): %v", err)
	}

	const junkNumber = "QA-JUNK-FISCAL-0001"
	const junkIRN, junkCSID, junkQR = "JUNK-IRN-VALUE", "JUNK-CSID-VALUE", "JUNK-QR-PAYLOAD-VALUE"
	if _, err := pool.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, irn, csid, qr_payload)
		 VALUES ($1, $2, $3, 'accepted', $4, $5, $6)`,
		demoTenantID, entityID, junkNumber, junkIRN, junkCSID, junkQR,
	); err != nil {
		t.Fatalf("seed junk invoice (precondition): %v", err)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed: %v", err)
	}

	var irn, csid, qrPayload string
	if err := pool.QueryRow(ctx,
		`SELECT irn, csid, qr_payload FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`,
		demoTenantID, junkNumber,
	).Scan(&irn, &csid, &qrPayload); err != nil {
		t.Fatalf("read back junk invoice after Seed: %v", err)
	}
	if irn != junkIRN || csid != junkCSID || qrPayload != junkQR {
		t.Errorf("junk invoice's fiscal columns after Seed = (%q, %q, %q), want unchanged (%q, %q, %q)", irn, csid, qrPayload, junkIRN, junkCSID, junkQR)
	}
}

// TestSeedFiscalIdentifierEmptyStringRejected: Test Spec row 6 (task-322). Regression
// guard for the pre-existing CHECK (irn IS NULL OR char_length(irn) > 0,
// migrations/20260722083015_invoices_fiscal_outcome.sql:58) — not new behavior this
// story adds, but load-bearing: a writer meaning "no IRN yet" must get 23514, never a
// second silent encoding of NULL.
func TestSeedFiscalIdentifierEmptyStringRejected(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed (establish baseline): %v", err)
	}

	_, err := pool.Exec(ctx,
		`UPDATE invoices SET irn = '' WHERE tenant_id = $1 AND invoice_number = 'DEMO-2026-1001'`,
		demoTenantID,
	)
	if err == nil {
		t.Fatal("UPDATE invoices SET irn = '' succeeded, want SQLSTATE 23514 (invoices_irn_check)")
	}
	if code := pgCode(err); code != "23514" {
		t.Errorf("UPDATE invoices SET irn = '' failed with SQLSTATE %q, want 23514", code)
	}
}

// TestSeedAcceptedInvoiceQRPayloadRendersAsQRImage: Test Spec row 7 (task-322). The
// fiscal card's own read path (internal/invoice/handlers.go:270-278) derives
// qr_png_base64 by calling qrcode.RenderBase64(*inv.QRPayload) — exercised directly
// since this package has no HTTP server harness.
//
// This alone does NOT catch the newline-corruption trap: a newline is legal QR
// byte-mode content, so a corrupted payload still renders an image. It only proves
// AC-6's "renders a QR image" clause; the whitespace/alphabet checks above catch
// corruption.
func TestSeedAcceptedInvoiceQRPayloadRendersAsQRImage(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	var qrPayload *string
	if err := pool.QueryRow(ctx,
		`SELECT qr_payload FROM invoices WHERE tenant_id = $1 AND invoice_number = 'DEMO-2026-1001'`,
		demoTenantID,
	).Scan(&qrPayload); err != nil {
		t.Fatalf("read back DEMO-2026-1001's qr_payload: %v", err)
	}
	if qrPayload == nil {
		t.Fatal("DEMO-2026-1001's qr_payload = NULL, want a derivable payload (it is an accepted invoice)")
	}

	png, err := qrcode.RenderBase64(*qrPayload)
	if err != nil {
		t.Fatalf("qrcode.RenderBase64(qr_payload): %v", err)
	}
	if png == "" {
		t.Error("qrcode.RenderBase64(qr_payload) returned an empty string, want a non-empty base64 PNG")
	}
}
