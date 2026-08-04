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
	"os"
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
	"github.com/SimonOsipov/invoice-os/internal/reconciliation"
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
//
// app_exchange and submission_jobs are cleared BEFORE invoices (task-323,
// DEMO-01-06): submission_jobs -> invoices is ON DELETE RESTRICT too, and
// app_exchange -> submission_jobs the same, so once Seed writes job/evidence
// rows the old invoices-first order fails 23001.
func resetDemoBusinessEntities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM app_exchange WHERE tenant_id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("clear demo tenant's app_exchange (precondition): %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM submission_jobs WHERE tenant_id = $1`, demoTenantID,
	); err != nil {
		t.Fatalf("clear demo tenant's submission_jobs (precondition): %v", err)
	}
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
// (name, tin, sector, status), ordered by name. sector is coalesced to the
// empty string in the query below, same as tin — a junk/probe row inserted
// without one (several tests below) has sector NULL, and entityRow has no
// room for a NULL.
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
//
// app_exchange and submission_jobs cleared first (task-323, DEMO-01-06):
// same RESTRICT-FK order fix as resetDemoBusinessEntities above.
func resetHoneywellBusinessEntities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM app_exchange WHERE tenant_id = $1`, honeywellTenantID,
	); err != nil {
		t.Fatalf("clear Honeywell's app_exchange (precondition): %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM submission_jobs WHERE tenant_id = $1`, honeywellTenantID,
	); err != nil {
		t.Fatalf("clear Honeywell's submission_jobs (precondition): %v", err)
	}
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
// (Core AC-5). Ground truth verified against db/seed.dev.sql's invoice_seed CTE: 31
// DEMO-2026-* invoices, 8 of them accepted.
const (
	demoInvoiceTotalCount    = 31
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

// task-323 (DEMO-01-06): in-house portfolio, full outcome coverage, nothing left in
// flight. RED authored against the plan's Core AC-4/5/6 plus the Stage-1 architecture
// corrections appended to the task -- db/seed.dev.sql does not implement any of this yet.

// resetBothDemoTenants resets both seeded tenants, for the assertions below that span
// the firm and in-house portfolios together (nothing-in-flight, outcome coverage,
// reconciliation, idempotency).
func resetBothDemoTenants(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	resetDemoBusinessEntities(t, pool)
	resetHoneywellBusinessEntities(t, pool)
}

// reservedTINReachableStatus pins each reserved buyer TIN this subtask uses to the ONE
// invoice status the mock adapter can actually converge on (Stage-1 correction C3).
// -0004 and -0006 return Retryable on every attempt and never accept; seeding either as
// "accepted" fabricates an outcome the sandbox cannot produce.
var reservedTINReachableStatus = map[string]string{
	"99999999-0001": "accepted",
	"99999999-0002": "rejected",
	"99999999-0003": "accepted", // converges via a poll cycle ([ref-carries-the-verdict])
	"99999999-0004": "failed",   // Retryable forever -> dead_lettered -> failed
	"99999999-0006": "failed",   // Retryable forever (timeout) -> dead_lettered -> failed
}

// terminalJobStates is submission_jobs' 4-value terminal subset of its 7-value state
// CHECK (jobstore.go's isTerminalJobState) -- dead_lettered is terminal and must not be
// rejected by a check that only knows accepted/rejected/failed.
var terminalJobStates = map[string]bool{
	"accepted": true, "rejected": true, "failed": true, "dead_lettered": true,
}

// invoiceOutcomeRow is a seeded invoice's buyer TIN and current status. tenantID is
// carried explicitly -- the two tenants' DEMO-2026-#### number spaces are not proven
// disjoint, so a caller keying a map by invoiceNumber alone would silently merge two
// different tenants' rows if they ever collide.
type invoiceOutcomeRow struct {
	tenantID      string
	invoiceNumber string
	buyerTIN      string
	status        string
	irn           *string
	csid          *string
	qrPayload     *string
}

// fetchDemoInvoiceOutcomes returns every seeded DEMO-2026-* invoice across BOTH tenants
// (buyer_tin, status, fiscal columns), for the reachability and history-consistency checks
// below.
func fetchDemoInvoiceOutcomes(t *testing.T, pool *pgxpool.Pool) []invoiceOutcomeRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT tenant_id, invoice_number, coalesce(buyer_tin, ''), status, irn, csid, qr_payload
		   FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'
		  ORDER BY tenant_id, invoice_number`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query DEMO-2026-* invoice outcomes: %v", err)
	}
	defer rows.Close()

	var got []invoiceOutcomeRow
	for rows.Next() {
		var r invoiceOutcomeRow
		if err := rows.Scan(&r.tenantID, &r.invoiceNumber, &r.buyerTIN, &r.status,
			&r.irn, &r.csid, &r.qrPayload); err != nil {
			t.Fatalf("scan invoice outcome row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate invoice outcome rows: %v", err)
	}
	return got
}

// submissionJobRow is a seeded submission_jobs row, joined back onto its invoice_number.
type submissionJobRow struct {
	invoiceNumber string
	state         string
	attempts      int
	adapter       string
}

// fetchDemoSubmissionJobs returns every seeded submission_jobs row across both tenants,
// joined through invoices on the same composite (tenant_id, invoice_id) the FK enforces
// -- the join itself is part of what proves AC-5's "resolves through its composite FK".
func fetchDemoSubmissionJobs(t *testing.T, pool *pgxpool.Pool) []submissionJobRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT i.invoice_number, j.state, j.attempts, j.adapter
		   FROM submission_jobs j
		   JOIN invoices i ON i.tenant_id = j.tenant_id AND i.id = j.invoice_id
		  WHERE j.tenant_id = ANY($1) AND i.invoice_number LIKE 'DEMO-2026-%'
		  ORDER BY i.invoice_number, j.created_at`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query seeded submission_jobs: %v", err)
	}
	defer rows.Close()

	var got []submissionJobRow
	for rows.Next() {
		var r submissionJobRow
		if err := rows.Scan(&r.invoiceNumber, &r.state, &r.attempts, &r.adapter); err != nil {
			t.Fatalf("scan submission_jobs row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate submission_jobs rows: %v", err)
	}
	return got
}

// appExchangeRow is a seeded app_exchange row, joined back onto its invoice_number.
type appExchangeRow struct {
	invoiceNumber string
	operation     string
	outcome       string
	attempt       int
	httpStatus    *int
}

// fetchDemoAppExchange returns every seeded app_exchange row across both tenants,
// joined through invoices the same way fetchDemoSubmissionJobs does.
func fetchDemoAppExchange(t *testing.T, pool *pgxpool.Pool) []appExchangeRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT i.invoice_number, e.operation, e.outcome, e.attempt, e.http_status
		   FROM app_exchange e
		   JOIN invoices i ON i.tenant_id = e.tenant_id AND i.id = e.invoice_id
		  WHERE e.tenant_id = ANY($1) AND i.invoice_number LIKE 'DEMO-2026-%'
		  ORDER BY i.invoice_number, e.attempt`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query seeded app_exchange: %v", err)
	}
	defer rows.Close()

	var got []appExchangeRow
	for rows.Next() {
		var r appExchangeRow
		if err := rows.Scan(&r.invoiceNumber, &r.operation, &r.outcome, &r.attempt, &r.httpStatus); err != nil {
			t.Fatalf("scan app_exchange row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate app_exchange rows: %v", err)
	}
	return got
}

// statusHistoryRow is a seeded invoice_status_history row, joined back onto its
// invoice_number. tenantID carried explicitly for the same reason as
// invoiceOutcomeRow above.
type statusHistoryRow struct {
	tenantID      string
	invoiceNumber string
	fromStatus    *string
	toStatus      string
	changedAt     time.Time
}

// fetchDemoStatusHistory returns every seeded invoice_status_history row across both
// tenants, ordered the same way History's own read path orders them
// (changed_at ASC, id ASC).
func fetchDemoStatusHistory(t *testing.T, pool *pgxpool.Pool) []statusHistoryRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT i.tenant_id, i.invoice_number, h.from_status, h.to_status, h.changed_at
		   FROM invoice_status_history h
		   JOIN invoices i ON i.tenant_id = h.tenant_id AND i.id = h.invoice_id
		  WHERE h.tenant_id = ANY($1) AND i.invoice_number LIKE 'DEMO-2026-%'
		  ORDER BY i.tenant_id, i.invoice_number, h.changed_at, h.id`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query seeded invoice_status_history: %v", err)
	}
	defer rows.Close()

	var got []statusHistoryRow
	for rows.Next() {
		var r statusHistoryRow
		if err := rows.Scan(&r.tenantID, &r.invoiceNumber, &r.fromStatus, &r.toStatus, &r.changedAt); err != nil {
			t.Fatalf("scan invoice_status_history row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate invoice_status_history rows: %v", err)
	}
	return got
}

// TestSeedPopulatesInHouseInvoicePortfolio: Test Spec rows "in-house populated" /
// "in-house single entity" (Core AC-4). Honeywell (2222...) has zero invoices and
// exactly one curated entity today -- Overview/Invoices/Approvals/Reports all render
// empty on a fresh in-house sign-in. In-house stays a DEGENERATE single-entity case
// ([in-house-single-entity]): the portfolio must land on invoices, never on a second
// entity.
func TestSeedPopulatesInHouseInvoicePortfolio(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	invoiceCount := mustCount(t, pool, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, honeywellTenantID)
	if invoiceCount == 0 {
		t.Error("count(invoices) for Honeywell (in-house tenant) after Seed = 0, want > 0 -- Overview/Invoices/Approvals/Reports all render an empty state on a fresh in-house sign-in otherwise")
	}

	entities := fetchDemoBusinessEntities(t, pool, honeywellTenantID)
	if len(entities) != 1 {
		t.Errorf("count(business_entities) for Honeywell after Seed = %d, want exactly 1 -- in-house is a degenerate single-entity case ([in-house-single-entity]), never a portfolio of entities", len(entities))
	}
}

// TestSeedLeavesNoInvoiceInFlight: Test Spec row "nothing in flight" (Core AC-6). The
// SPA polls isInFlight(status) = status IN ('queued','submitted') every 2s while the tab
// is visible; the seed today leaves 3 queued + 1 submitted (C6: DEMO-2026-1004/2002/5004
// queued, DEMO-2026-2003 submitted) with zero submission_jobs rows behind them, which
// poll forever with nothing to advance them.
func TestSeedLeavesNoInvoiceInFlight(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	n := mustCount(t, pool,
		`SELECT count(*) FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%' AND status IN ('queued', 'submitted')`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if n != 0 {
		t.Errorf("count(seeded invoices with status IN queued/submitted) = %d, want 0", n)
	}
}

// A row that has never been submitted claims no APP outcome, so the reachable-status map has
// nothing to check it against. TestSeedSeedsSubmittableTriggerTwinsInBothTenants is what stops
// a SKIPPED row carrying a non-convergent trigger; do not weaken it alone.
var preSubmissionStatuses = map[string]bool{"draft": true, "validated": true}

// TestSeedNeverClaimsUnreachableOutcomeForReservedTIN: the sharpest test in this
// subtask. Fails if any seeded invoice claims a terminal status its OWN buyer TIN
// cannot produce -- e.g. -0004 seeded as "accepted" (unreachable: Retryable on every
// attempt, MaxAttempts=8 exhausts to dead_lettered/failed). Seeding a fabricated
// outcome is the exact dishonesty Core AC-5 exists to remove.
func TestSeedNeverClaimsUnreachableOutcomeForReservedTIN(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	var checked int
	for _, r := range fetchDemoInvoiceOutcomes(t, pool) {
		want, reserved := reservedTINReachableStatus[r.buyerTIN]
		if !reserved {
			continue
		}
		if preSubmissionStatuses[r.status] {
			if r.irn != nil || r.csid != nil || r.qrPayload != nil {
				t.Errorf("%s: buyer_tin=%s status=%q carries a fiscal identifier (irn/csid/qr_payload), want all three NULL -- a row that was never submitted has no APP outcome to claim", r.invoiceNumber, r.buyerTIN, r.status)
			}
			continue
		}
		checked++
		if r.status != want {
			t.Errorf("%s: buyer_tin=%s status=%q, want %q -- the mock adapter can never converge this TIN on any other terminal status (Stage-1 correction C3)", r.invoiceNumber, r.buyerTIN, r.status, want)
		}
	}
	if checked == 0 {
		t.Fatal("no seeded invoice uses a reserved outcome-coverage buyer TIN -- Core AC-5's outcome coverage is not exercised at all")
	}
}

// TestSeedSubmissionJobsAreTerminalAndLinkToSeededInvoices: Test Spec rows "jobs are
// terminal" / "job <-> invoice integrity" (Core AC-6). The state CHECK has SEVEN
// values; the terminal set is FOUR (jobstore.go's isTerminalJobState) --
// accepted/rejected/failed/dead_lettered. A job asserting only {accepted,rejected,
// failed} would reject a legitimately dead-lettered row (Stage-1 correction C4.1).
func TestSeedSubmissionJobsAreTerminalAndLinkToSeededInvoices(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	jobs := fetchDemoSubmissionJobs(t, pool)
	if len(jobs) == 0 {
		t.Fatal("count(seeded submission_jobs rows) = 0, want > 0 -- Core AC-5's outcome-coverage invoices need job+evidence rows behind them")
	}
	for _, j := range jobs {
		if !terminalJobStates[j.state] {
			t.Errorf("%s: submission_jobs.state = %q, want one of accepted/rejected/failed/dead_lettered (terminal) -- a live queued/submitting/pending job polls forever", j.invoiceNumber, j.state)
		}
		if j.attempts < 1 {
			t.Errorf("%s: submission_jobs.attempts = %d, want >= 1 -- a terminal job made at least one real attempt", j.invoiceNumber, j.attempts)
		}
		if j.adapter != "mock" {
			t.Errorf("%s: submission_jobs.adapter = %q, want %q -- the sandbox adapter is the only one wired for the demo environment", j.invoiceNumber, j.adapter, "mock")
		}
	}
}

// TestSeedOutcomeCoverageAtTheEvidenceLayer: Test Spec row "outcome coverage" (Core
// AC-5). invoices.status only reaches 3 values across the 5 reserved TINs this subtask
// uses (accepted/rejected/failed, both -0004 and -0006 landing on failed) -- AC-5's five
// distinct outcomes are visible only at the job+evidence layer (Stage-1 correction C3):
// submission_jobs.state + app_exchange.operation/http_status, never invoices.status
// alone.
func TestSeedOutcomeCoverageAtTheEvidenceLayer(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	exchanges := fetchDemoAppExchange(t, pool)
	if len(exchanges) == 0 {
		t.Fatal("count(seeded app_exchange rows) = 0, want > 0")
	}

	type outcomeMarker struct {
		name string
		seen bool
	}
	markers := []outcomeMarker{
		{name: "accepted (submit, http 200)"},
		{name: "rejected (submit, http 422)"},
		{name: "pending-initial (submit, http 202)"},
		{name: "pending-converge (poll)"},
		{name: "retry/unavailable (submit, http 503)"},
		{name: "timeout (submit, http_status NULL)"},
	}

	for _, e := range exchanges {
		// All five reserved TINs this subtask uses reach the wire; only -0007
		// (connection, unused here) would produce connection_failed.
		if e.outcome != "sent" {
			t.Errorf("%s: app_exchange.outcome = %q, want %q", e.invoiceNumber, e.outcome, "sent")
		}
		if e.attempt < 1 {
			t.Errorf("%s: app_exchange.attempt = %d, want >= 1 (CHECK attempt >= 1, never 0)", e.invoiceNumber, e.attempt)
		}

		switch {
		case e.operation == "submit" && e.httpStatus != nil && *e.httpStatus == 200:
			markers[0].seen = true
		case e.operation == "submit" && e.httpStatus != nil && *e.httpStatus == 422:
			markers[1].seen = true
		case e.operation == "submit" && e.httpStatus != nil && *e.httpStatus == 202:
			markers[2].seen = true
		case e.operation == "poll":
			markers[3].seen = true
		case e.operation == "submit" && e.httpStatus != nil && *e.httpStatus == 503:
			markers[4].seen = true
		case e.operation == "submit" && e.httpStatus == nil:
			markers[5].seen = true
		}
	}

	for _, m := range markers {
		if !m.seen {
			t.Errorf("no seeded app_exchange evidence for outcome %q -- Core AC-5 requires at least one invoice per sandbox-adapter outcome", m.name)
		}
	}
}

// TestSeedPopulatesFiscalIdentifiersOnAcceptedInHouseInvoices: the in-house block is
// STRUCTURALLY NEW (Stage-1 correction C2) -- it cannot extend the firm INSERT, whose
// CTE carries no tenant column and whose fiscal derivation is hardcoded to 1111....  A
// copied block that drops the derivation makes every accepted in-house row trip
// accepted_without_irn on cmd/reconciliation's live 5-minute sweep. Mirrors
// TestSeedPopulatesFiscalIdentifiersOnAcceptedInvoices, scoped to Honeywell.
func TestSeedPopulatesFiscalIdentifiersOnAcceptedInHouseInvoices(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	var accepted []fiscalInvoiceRow
	for _, r := range fetchDemoFiscalInvoices(t, pool, honeywellTenantID) {
		if r.status == "accepted" {
			accepted = append(accepted, r)
		}
	}
	if len(accepted) == 0 {
		t.Fatal("count(accepted DEMO-2026-* invoices) for Honeywell (in-house) = 0, want > 0 -- outcome coverage requires at least one accepted in-house row")
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
		if len(*r.csid) != 43 || !rawURLBase64Pattern.MatchString(*r.csid) {
			t.Errorf("%s: csid = %q, want 43-char base64.RawURLEncoding of a 32-byte sha256 digest", r.invoiceNumber, *r.csid)
		}
		if strings.ContainsAny(*r.qrPayload, " \t\r\n") || !rawURLBase64Pattern.MatchString(*r.qrPayload) {
			t.Errorf("%s: qr_payload = %q, want clean base64.RawURLEncoding with no whitespace", r.invoiceNumber, *r.qrPayload)
		}

		irn, csid, tin, amt, cur := decodeQRPayload(t, *r.qrPayload)
		if irn != *r.irn {
			t.Errorf("%s: qr_payload embeds irn=%q, want the row's own irn %q", r.invoiceNumber, irn, *r.irn)
		}
		if csid != *r.csid {
			t.Errorf("%s: qr_payload embeds csid=%q, want the row's own csid %q", r.invoiceNumber, csid, *r.csid)
		}
		if tin != r.supplierTIN {
			t.Errorf("%s: qr_payload embeds tin=%q, want the row's own SUPPLIER tin %q", r.invoiceNumber, tin, r.supplierTIN)
		}
		if r.supplierTIN != "20665510-0001" {
			t.Errorf("%s: supplier_tin = %q, want Honeywell's curated TIN %q, or supplier-tin-format fires", r.invoiceNumber, r.supplierTIN, "20665510-0001")
		}
		amtF, errAmt := strconv.ParseFloat(amt, 64)
		totalF, errTotal := strconv.ParseFloat(r.total, 64)
		if errAmt != nil {
			t.Errorf("%s: qr_payload embeds amt=%q, not a number: %v", r.invoiceNumber, amt, errAmt)
		} else if errTotal != nil {
			t.Fatalf("%s: row's own total = %q, not a number: %v", r.invoiceNumber, r.total, errTotal)
		} else if amtF != totalF {
			t.Errorf("%s: qr_payload embeds amt=%q, want the row's own TOTAL %q", r.invoiceNumber, amt, r.total)
		}
		if cur != r.currency {
			t.Errorf("%s: qr_payload embeds cur=%q, want the row's own currency %q", r.invoiceNumber, cur, r.currency)
		}
	}
}

// TestSeedStatusHistoryConsistentWithFinalStatus: Test Spec row "history consistent"
// (Core AC-6). Each invoice's LAST history row (changed_at ASC, id ASC, matching
// History's own read order) must land on its current status. changed_at is an explicit
// anchor+ord*15min offset (:363-369), never now() -- but only safe if ord stays distinct
// WITHIN each invoice's own chain, or History's own ORDER BY tie-breaks on a random
// uuid and scrambles the visible timeline.
func TestSeedStatusHistoryConsistentWithFinalStatus(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// Keyed by (tenantID, invoiceNumber): the two tenants' DEMO-2026-#### number spaces
	// are not proven disjoint, so a bare invoiceNumber key could silently merge two
	// different tenants' chains.
	type invoiceKey struct{ tenantID, invoiceNumber string }

	outcomes := fetchDemoInvoiceOutcomes(t, pool)
	finalStatus := make(map[invoiceKey]string, len(outcomes))
	for _, r := range outcomes {
		finalStatus[invoiceKey{r.tenantID, r.invoiceNumber}] = r.status
	}

	history := fetchDemoStatusHistory(t, pool)
	if len(history) == 0 {
		t.Fatal("count(seeded invoice_status_history rows) = 0, want > 0")
	}

	byInvoice := make(map[invoiceKey][]statusHistoryRow)
	for _, h := range history {
		key := invoiceKey{h.tenantID, h.invoiceNumber}
		byInvoice[key] = append(byInvoice[key], h)
	}

	for key, rows := range byInvoice {
		last := rows[len(rows)-1]
		if last.toStatus != finalStatus[key] {
			t.Errorf("%s: last history row's to_status = %q, want the invoice's own final status %q", key.invoiceNumber, last.toStatus, finalStatus[key])
		}

		seen := make(map[time.Time]bool, len(rows))
		for _, r := range rows {
			if seen[r.changedAt] {
				t.Errorf("%s: two history rows share changed_at %v, want distinct offsets within the chain (or ORDER BY changed_at ASC, id ASC ties-break on a random uuid)", key.invoiceNumber, r.changedAt)
			}
			seen[r.changedAt] = true
		}
	}
}

// TestSeedTripsNoReconciliationDrift: "no reconciliation predicate matches any seeded
// row". Reads all eight drift signatures directly off internal/reconciliation.Scan (the
// same query cmd/reconciliation runs on its live 5-minute sweep) and asserts none of
// them flags a seeded invoice. Today queued_never_sent = 3 on the un-fixed seed; this
// subtask must take it to 0 without introducing a new one (accepted_without_irn is the
// one real hazard the in-house block risks -- Stage-1 correction C2).
func TestSeedTripsNoReconciliationDrift(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	seededInvoiceIDs := make(map[string]bool)
	idRows, err := pool.Query(ctx, `SELECT id FROM invoices WHERE tenant_id = ANY($1)`,
		[]string{demoTenantID, honeywellTenantID})
	if err != nil {
		t.Fatalf("query seeded invoice ids: %v", err)
	}
	for idRows.Next() {
		var id string
		if err := idRows.Scan(&id); err != nil {
			t.Fatalf("scan invoice id: %v", err)
		}
		seededInvoiceIDs[id] = true
	}
	if err := idRows.Err(); err != nil {
		t.Fatalf("iterate invoice ids: %v", err)
	}
	idRows.Close()
	if len(seededInvoiceIDs) == 0 {
		t.Fatal("precondition: zero invoices for the seeded tenants after Seed")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx for reconciliation.Scan: %v", err)
	}
	defer tx.Rollback(ctx)

	// Matches cmd/reconciliation's own documented defaults (sweep.go) -- irrelevant to a
	// fully-terminal seed, but keeps this test honest about what production runs.
	th := reconciliation.Thresholds{Grace: 15 * time.Minute, MaxPendingAge: 24 * time.Hour, HopCeiling: 20}
	findings, err := reconciliation.Scan(ctx, tx, th)
	if err != nil {
		t.Fatalf("reconciliation.Scan: %v", err)
	}

	for _, f := range findings {
		if seededInvoiceIDs[f.InvoiceID] {
			t.Errorf("reconciliation.Scan flagged seeded invoice %s: kind=%s -- cmd/reconciliation sweeps every 5 minutes and would write a real reconciliation.drift_detected audit row for this", f.InvoiceID, f.Kind)
		}
	}
}

// TestSeedConvergesPreexistingInFlightInvoiceOnReseed: proves the UPSERT/CONFLICT path,
// not just the INSERT. The demo environment is never reset -- forces a seeded invoice
// back to 'queued' (simulating what ascomply.com looks like today), re-seeds, and
// asserts it converges to a terminal status through ON CONFLICT ... DO UPDATE SET, not
// just on a fresh INSERT.
func TestSeedConvergesPreexistingInFlightInvoiceOnReseed(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish baseline): %v", err)
	}

	const probeInvoice = "DEMO-2026-1004" // one of C6's four named in-flight rows
	if _, err := pool.Exec(ctx,
		`UPDATE invoices SET status = 'queued' WHERE tenant_id = $1 AND invoice_number = $2`,
		demoTenantID, probeInvoice,
	); err != nil {
		t.Fatalf("force %s back to queued (precondition): %v", probeInvoice, err)
	}

	var precond string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`,
		demoTenantID, probeInvoice,
	).Scan(&precond); err != nil {
		t.Fatalf("read back precondition: %v", err)
	}
	if precond != "queued" {
		t.Fatalf("precondition: %s status = %q, want queued", probeInvoice, precond)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (convergence): %v", err)
	}

	var after string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`,
		demoTenantID, probeInvoice,
	).Scan(&after); err != nil {
		t.Fatalf("read back after second Seed: %v", err)
	}
	if after == "queued" || after == "submitted" {
		t.Errorf("%s: status after re-seed = %q, want terminal -- a status change must converge through ON CONFLICT ... DO UPDATE SET, not just the INSERT", probeInvoice, after)
	}
}

// TestSeedOutcomeCoverageIsIdempotent: Test Spec row "idempotent". A second Seed must
// leave submission_jobs, app_exchange and invoice_status_history byte-identical to the
// first -- invoice_status_history has no unique constraint to key an ON CONFLICT off
// (idempotency is a NOT EXISTS guard on (invoice_id, from_status, to_status)), so this
// is the one table where a re-run duplicating a row is structurally possible.
func TestSeedOutcomeCoverageIsIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	firstJobs := fetchDemoSubmissionJobs(t, pool)
	firstExchanges := fetchDemoAppExchange(t, pool)
	firstHistory := fetchDemoStatusHistory(t, pool)
	if len(firstJobs) == 0 {
		t.Fatal("precondition: zero seeded submission_jobs rows after the FIRST Seed")
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (idempotency): %v", err)
	}
	secondJobs := fetchDemoSubmissionJobs(t, pool)
	secondExchanges := fetchDemoAppExchange(t, pool)
	secondHistory := fetchDemoStatusHistory(t, pool)

	if !reflect.DeepEqual(firstJobs, secondJobs) {
		t.Errorf("submission_jobs differ between the first and second Seed call, want byte-identical\nfirst:  %+v\nsecond: %+v", firstJobs, secondJobs)
	}
	if !reflect.DeepEqual(firstExchanges, secondExchanges) {
		t.Errorf("app_exchange differs between the first and second Seed call, want byte-identical\nfirst:  %+v\nsecond: %+v", firstExchanges, secondExchanges)
	}
	if !reflect.DeepEqual(firstHistory, secondHistory) {
		t.Errorf("invoice_status_history differs between the first and second Seed call, want byte-identical\nfirst:  %+v\nsecond: %+v", firstHistory, secondHistory)
	}
}

// TestSeedWritesNoSubmissionEvidenceForOtherTenants: "other tenants untouched" —
// companion to TestSeedDoesNotTouchOtherTenants, scoped to the two new tables this
// subtask writes. Guards against a copy-paste bug in the in-house block landing a
// job/exchange row under the wrong tenant_id.
func TestSeedWritesNoSubmissionEvidenceForOtherTenants(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	if n := mustCount(t, pool, `SELECT count(*) FROM submission_jobs WHERE tenant_id = $1`, foreignTenantID); n != 0 {
		t.Errorf("count(submission_jobs) for foreign tenant %s after Seed = %d, want 0", foreignTenantID, n)
	}
	if n := mustCount(t, pool, `SELECT count(*) FROM app_exchange WHERE tenant_id = $1`, foreignTenantID); n != 0 {
		t.Errorf("count(app_exchange) for foreign tenant %s after Seed = %d, want 0", foreignTenantID, n)
	}
}

// TestSeedFailedHistoryOutranksPreexistingSubmittedResidue: QA gap-fill, regression for the
// chains CTE's ord=5 (not 4) on the failed chain's queued->failed row.
//
// On a never-reset production DB, an invoice this subtask newly terminalizes to 'failed' may
// already carry a queued->submitted row from an OLDER deploy of this file (back when the
// invoice's status was still 'submitted' and the 'submitted' chain's own ord=4 row wrote it).
// Residue deletion is out of scope, so that row survives forever. If the failed chain's own
// queued->failed row used the SAME ord (4 -> anchor+60min), it would land at the identical
// changed_at as the residue, and History's own ORDER BY changed_at ASC, id ASC would then
// tie-break on a random uuid -- sometimes rendering 'submitted' as the LAST step of an invoice
// whose real status is 'failed'. ord=5 (anchor+75min) keeps it strictly after the residue.
func TestSeedFailedHistoryOutranksPreexistingSubmittedResidue(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish baseline): %v", err)
	}

	const probeInvoice = "DEMO-2026-2003" // one of C6's four named in-flight rows
	var invoiceID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`,
		demoTenantID, probeInvoice,
	).Scan(&invoiceID); err != nil {
		t.Fatalf("look up %s id (precondition): %v", probeInvoice, err)
	}

	// Plant the pre-existing residue an older deploy would have left: the 'submitted' chain's
	// own ord=4 row, at the exact anchor+4*15min offset every other ord=4 row in this file uses.
	const residueChangedAt = "2026-06-01 09:00:00+00"
	if _, err := pool.Exec(ctx,
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, changed_at)
		 VALUES ($1, $2, 'queued', 'submitted', 'system', $3)`,
		demoTenantID, invoiceID, residueChangedAt,
	); err != nil {
		t.Fatalf("plant pre-existing queued->submitted residue (precondition): %v", err)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (deploy onto the residue): %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT to_status, changed_at FROM invoice_status_history
		  WHERE tenant_id = $1 AND invoice_id = $2 ORDER BY changed_at ASC, id ASC`,
		demoTenantID, invoiceID,
	)
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	defer rows.Close()

	type histStep struct {
		to string
		at time.Time
	}
	var got []histStep
	for rows.Next() {
		var r histStep
		if err := rows.Scan(&r.to, &r.at); err != nil {
			t.Fatalf("scan history row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate history rows: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no history rows for the probe invoice after re-seed")
	}

	// The determinism check: no two rows may share changed_at. A shared timestamp is what
	// makes the "last" row's identity depend on a random uuid instead of on what actually
	// happened last -- this is the direct, order-independent detector for the tie itself.
	seen := make(map[time.Time]int, len(got))
	for _, r := range got {
		seen[r.at]++
	}
	for at, n := range seen {
		if n > 1 {
			t.Errorf("changed_at %v is shared by %d history rows, want every row distinct — a tie makes the LAST row's identity depend on a random uuid rather than on which change actually happened last", at, n)
		}
	}

	if last := got[len(got)-1]; last.to != "failed" {
		t.Errorf("last history row (changed_at ASC, id ASC) has to_status %q, want %q — the residue's queued->submitted row must not outrank the failed chain's own queued->failed row", last.to, "failed")
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&status); err != nil {
		t.Fatalf("read back invoice status: %v", err)
	}
	if status != "failed" {
		t.Fatalf("invoice status after re-seed = %q, want failed", status)
	}
}

// task-324 (DEMO-01-07): re-anchor the curated invoice set ahead of E2E residue on every
// boot. RED against the Stage-1-corrected Core AC-1..7 (C0-C13) -- neither invoice upsert's
// SET list carries created_at yet.

// invoiceOrderRow is a seeded invoice's number and created_at. Mirrors
// fetchDemoStatusHistory's shape (:1414).
type invoiceOrderRow struct {
	invoiceNumber string
	createdAt     time.Time
}

// fetchDemoInvoiceOrder returns tenantID's seeded DEMO-2026-* invoices ordered exactly
// like the register (store.go:666): created_at DESC, id DESC.
func fetchDemoInvoiceOrder(t *testing.T, pool *pgxpool.Pool, tenantID string) []invoiceOrderRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT invoice_number, created_at FROM invoices
		  WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%'
		  ORDER BY created_at DESC, id DESC`,
		tenantID,
	)
	if err != nil {
		t.Fatalf("query seeded invoice order for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	var got []invoiceOrderRow
	for rows.Next() {
		var r invoiceOrderRow
		if err := rows.Scan(&r.invoiceNumber, &r.createdAt); err != nil {
			t.Fatalf("scan invoice order row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate invoice order rows for tenant %s: %v", tenantID, err)
	}
	return got
}

// wantFirmInvoiceOrder / wantInHouseInvoiceOrder: each tenant's own DEMO-2026-* invoice
// numbers ordered by issue_date DESC, invoice_number DESC (C3/C4) -- hand-derived from
// db/seed.dev.sql's invoice_seed / inhouse_invoice_seed CTEs, the "declared expected
// order" C2's detector checks the observed created_at DESC, id DESC sequence against.
var wantFirmInvoiceOrder = []string{
	"DEMO-2026-1009", "DEMO-2026-1008", "DEMO-2026-1007",
	"DEMO-2026-6003", "DEMO-2026-6002", "DEMO-2026-6001",
	"DEMO-2026-5005", "DEMO-2026-5004", "DEMO-2026-5003", "DEMO-2026-5002", "DEMO-2026-5001",
	"DEMO-2026-4004", "DEMO-2026-4003", "DEMO-2026-4002", "DEMO-2026-4001",
	"DEMO-2026-3005", "DEMO-2026-3004", "DEMO-2026-3003", "DEMO-2026-3002", "DEMO-2026-3001",
	"DEMO-2026-2005", "DEMO-2026-2004", "DEMO-2026-2003", "DEMO-2026-2002", "DEMO-2026-2001",
	"DEMO-2026-1006", "DEMO-2026-1005", "DEMO-2026-1004", "DEMO-2026-1003", "DEMO-2026-1002", "DEMO-2026-1001",
}

var wantInHouseInvoiceOrder = []string{
	"DEMO-2026-9003", "DEMO-2026-9002", "DEMO-2026-9001",
	"DEMO-2026-8005", "DEMO-2026-8004", "DEMO-2026-8003", "DEMO-2026-8002",
	"DEMO-2026-8001", "DEMO-2026-7006", "DEMO-2026-7004", "DEMO-2026-7003", "DEMO-2026-7002", "DEMO-2026-7001",
}

// reanchorOffsetUnit: the declared per-row spacing (C3/C5). clock_timestamp() would also
// make every row distinct, but its deltas are microseconds, not this.
const reanchorOffsetUnit = time.Second

// TestSeedInvoiceCreatedAtDistinctWithinTenant: C12 row 1 (AC-1, AC-2). A bare now() is
// identical for every row this script writes (one implicit transaction) -- distinctness
// per tenant is the cheapest kill for it.
func TestSeedInvoiceCreatedAtDistinctWithinTenant(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	for _, tc := range []struct {
		tenantID string
		want     int
	}{
		{demoTenantID, demoInvoiceTotalCount},
		{honeywellTenantID, len(wantInHouseInvoiceOrder)},
	} {
		got := fetchDemoInvoiceOrder(t, pool, tc.tenantID)
		if len(got) != tc.want {
			t.Fatalf("tenant %s: count(seeded DEMO-2026-* invoices) = %d, want %d", tc.tenantID, len(got), tc.want)
		}
		distinct := make(map[time.Time]bool, len(got))
		for _, r := range got {
			distinct[r.createdAt] = true
		}
		if len(distinct) != len(got) {
			t.Errorf("tenant %s: count(DISTINCT created_at) = %d, want %d -- a bare now() collapses every seeded row onto one timestamp", tc.tenantID, len(distinct), len(got))
		}
	}
}

// TestSeedInvoiceCreatedAtSpacingIsExact: C12 row 2 (AC-1, AC-2). Consecutive seeded rows
// (created_at DESC) must differ by exactly reanchorOffsetUnit -- kills clock_timestamp(),
// which would also pass the distinctness test above but with microsecond deltas.
func TestSeedInvoiceCreatedAtSpacingIsExact(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	for _, tenantID := range []string{demoTenantID, honeywellTenantID} {
		got := fetchDemoInvoiceOrder(t, pool, tenantID)
		for i := 1; i < len(got); i++ {
			delta := got[i-1].createdAt.Sub(got[i].createdAt)
			if delta != reanchorOffsetUnit {
				t.Errorf("tenant %s: created_at gap between %s and %s = %v, want exactly %v", tenantID, got[i-1].invoiceNumber, got[i].invoiceNumber, delta, reanchorOffsetUnit)
			}
		}
	}
}

// TestSeedInvoiceCreatedAtMatchesDeclaredOrder: C12 row 3 (AC-1, AC-2). ON CONFLICT ...
// DO UPDATE preserves id (5/5 verified), so a bare now() reproduces the SAME uuid
// tie-break across two runs and a two-run comparison alone would pass it -- comparing
// against a declared expected order instead kills it regardless of that id-preservation
// luck, and doubles as C4's issue_date-ordering pin.
func TestSeedInvoiceCreatedAtMatchesDeclaredOrder(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	for _, tc := range []struct {
		name     string
		tenantID string
		want     []string
	}{
		{"firm", demoTenantID, wantFirmInvoiceOrder},
		{"in-house", honeywellTenantID, wantInHouseInvoiceOrder},
	} {
		rows := fetchDemoInvoiceOrder(t, pool, tc.tenantID)
		got := make([]string, len(rows))
		for i, r := range rows {
			got[i] = r.invoiceNumber
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s tenant: created_at DESC, id DESC invoice_number order =\n%v\nwant (issue_date DESC, invoice_number DESC):\n%v", tc.name, got, tc.want)
		}
	}
}

// TestSeedInvoiceCreatedAtStrictlyPast: C12 row 4 (AC-1, AC-6). Rows created during an
// E2E run (which starts after the seed) must still sort ahead of the curated set.
func TestSeedInvoiceCreatedAtStrictlyPast(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	var dbNow time.Time
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatalf("read db now(): %v", err)
	}

	for _, tenantID := range []string{demoTenantID, honeywellTenantID} {
		for _, r := range fetchDemoInvoiceOrder(t, pool, tenantID) {
			if !r.createdAt.Before(dbNow) {
				t.Errorf("tenant %s: %s created_at = %v, want strictly before %v", tenantID, r.invoiceNumber, r.createdAt, dbNow)
			}
		}
	}
}

// TestSeedReanchorsFirmInvoicesAheadOfOlderResidue: C12 row 5 (AC-3). Backdating first is
// what forces the UPDATE path -- a test that only ever inserts fresh rows proves nothing
// about the ON CONFLICT ... DO UPDATE SET list. The firm register is entity-scoped
// (InvoicesList.tsx:71/:83 -> store.go:598); Adeyemi & Sons is clients[0] with 9 curated
// rows, so this asserts positionally against the entity-scoped page (C8), not "dominates
// the first 50".
func TestSeedReanchorsFirmInvoicesAheadOfOlderResidue(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish baseline): %v", err)
	}

	const adeyemiTIN = "10012345-0001"
	var adeyemiEntityID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, adeyemiTIN,
	).Scan(&adeyemiEntityID); err != nil {
		t.Fatalf("look up Adeyemi entity id (precondition): %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE invoices SET created_at = now() - interval '30 days'
		  WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%'`,
		demoTenantID,
	); err != nil {
		t.Fatalf("backdate the demo tenant's seeded invoices (precondition): %v", err)
	}

	const residueNumber = "E2E-FIRM-RESIDUE-0001"
	if _, err := pool.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, created_at) VALUES ($1, $2, $3, now() - interval '1 hour')`,
		demoTenantID, adeyemiEntityID, residueNumber,
	); err != nil {
		t.Fatalf("insert residue newer than the backdated seed (precondition): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, demoTenantID, residueNumber)
	})

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (re-anchor over the backdated + residue rows): %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT invoice_number FROM invoices WHERE tenant_id = $1 AND entity_id = $2
		  ORDER BY created_at DESC, id DESC LIMIT 50`,
		demoTenantID, adeyemiEntityID,
	)
	if err != nil {
		t.Fatalf("query Adeyemi's entity-scoped page: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan invoice_number: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Adeyemi's entity-scoped page: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("Adeyemi's entity-scoped page has %d rows, want 10 (9 curated + 1 residue)", len(got))
	}

	top9 := append([]string{}, got[:9]...)
	sort.Strings(top9)
	wantTop9 := []string{
		"DEMO-2026-1001", "DEMO-2026-1002", "DEMO-2026-1003", "DEMO-2026-1004", "DEMO-2026-1005",
		"DEMO-2026-1006", "DEMO-2026-1007", "DEMO-2026-1008", "DEMO-2026-1009",
	}
	sort.Strings(wantTop9)
	if !reflect.DeepEqual(top9, wantTop9) {
		t.Errorf("Adeyemi's top 9 entity-scoped rows = %v, want exactly the 9 curated DEMO-2026-100x rows %v", got[:9], wantTop9)
	}
	if got[9] != residueNumber {
		t.Errorf("Adeyemi's entity-scoped row 10 = %q, want the older residue %q to sort last", got[9], residueNumber)
	}
}

// TestSeedReanchorsInHouseInvoicesAheadOfOlderResidue: C12 row 6 (AC-3, AC-5) -- the C1
// detector. task-323 added a SECOND invoice upsert (in-house, ON CONFLICT at :366-382);
// created_at must join BOTH SET lists. If only the firm block (:286-302) gained it, the
// backdated Honeywell rows here stay 30 days old and the 1-hour-old residue outranks them,
// failing the position assertions below. Also pins AC-5: outcome coverage is in-house
// only, so every DEMO-01-06 outcome must be visible on this same page.
func TestSeedReanchorsInHouseInvoicesAheadOfOlderResidue(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish baseline): %v", err)
	}

	var honeywellEntityID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		honeywellTenantID, curatedHoneywellEntity.tin,
	).Scan(&honeywellEntityID); err != nil {
		t.Fatalf("look up Honeywell entity id (precondition): %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE invoices SET created_at = now() - interval '30 days'
		  WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%'`,
		honeywellTenantID,
	); err != nil {
		t.Fatalf("backdate Honeywell's seeded invoices (precondition): %v", err)
	}

	const residueNumber = "E2E-INHOUSE-RESIDUE-0001"
	if _, err := pool.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, created_at) VALUES ($1, $2, $3, now() - interval '1 hour')`,
		honeywellTenantID, honeywellEntityID, residueNumber,
	); err != nil {
		t.Fatalf("insert residue newer than the backdated seed (precondition): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, honeywellTenantID, residueNumber)
	})

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (re-anchor over the backdated + residue rows): %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT invoice_number, status FROM invoices WHERE tenant_id = $1
		  ORDER BY created_at DESC, id DESC LIMIT 50`,
		honeywellTenantID,
	)
	if err != nil {
		t.Fatalf("query Honeywell's tenant-wide page: %v", err)
	}
	defer rows.Close()
	var numbers []string
	statuses := make(map[string]bool)
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			t.Fatalf("scan invoice row: %v", err)
		}
		numbers = append(numbers, n)
		statuses[s] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Honeywell's tenant-wide page: %v", err)
	}
	if len(numbers) != 14 {
		t.Fatalf("Honeywell's tenant-wide page has %d rows, want 14 (13 curated + 1 residue)", len(numbers))
	}

	top13 := append([]string{}, numbers[:13]...)
	sort.Strings(top13)
	wantTop13 := append([]string{}, wantInHouseInvoiceOrder...)
	sort.Strings(wantTop13)
	if !reflect.DeepEqual(top13, wantTop13) {
		t.Errorf("Honeywell's top 13 rows = %v, want exactly the 13 curated DEMO-2026-7xxx/8xxx/9xxx rows %v -- fails if created_at was added to the FIRM upsert's SET list only (C1)", numbers[:13], wantTop13)
	}
	if numbers[13] != residueNumber {
		t.Errorf("Honeywell's row 14 = %q, want the older residue %q to sort last", numbers[13], residueNumber)
	}

	for _, want := range []string{"accepted", "rejected", "failed"} {
		if !statuses[want] {
			t.Errorf("Honeywell's page is missing invoice status %q -- every DEMO-01-06 outcome must stay visible on the in-house register's first page (AC-5)", want)
		}
	}

	jobRows, err := pool.Query(ctx,
		`SELECT DISTINCT j.state FROM submission_jobs j
		   JOIN invoices i ON i.tenant_id = j.tenant_id AND i.id = j.invoice_id
		  WHERE j.tenant_id = $1`,
		honeywellTenantID,
	)
	if err != nil {
		t.Fatalf("query Honeywell's submission_jobs states: %v", err)
	}
	defer jobRows.Close()
	jobStates := make(map[string]bool)
	for jobRows.Next() {
		var s string
		if err := jobRows.Scan(&s); err != nil {
			t.Fatalf("scan job state: %v", err)
		}
		jobStates[s] = true
	}
	if err := jobRows.Err(); err != nil {
		t.Fatalf("iterate Honeywell's submission_jobs states: %v", err)
	}
	for _, want := range []string{"accepted", "rejected", "dead_lettered"} {
		if !jobStates[want] {
			t.Errorf("Honeywell's submission_jobs is missing state %q after re-seed", want)
		}
	}
}

// TestSeedNewInvoiceOutranksSeededFirmPortfolio: C12 row 7 (AC-6). A row created strictly
// after the seed runs (default created_at = now()) must sort ahead of the whole curated
// set -- the three e2e page-1 assumptions (persona-inhouse.spec.ts:274-279,
// perf.spec.ts:135, persona-surfaces.spec.ts:438-440) depend on this holding at gateway
// boot, before any spec runs.
func TestSeedNewInvoiceOutranksSeededFirmPortfolio(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	const adeyemiTIN = "10012345-0001"
	var adeyemiEntityID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, adeyemiTIN,
	).Scan(&adeyemiEntityID); err != nil {
		t.Fatalf("look up Adeyemi entity id (precondition): %v", err)
	}

	const newNumber = "E2E-NEW-FIRM-0001"
	if _, err := pool.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3)`,
		demoTenantID, adeyemiEntityID, newNumber,
	); err != nil {
		t.Fatalf("insert a fresh post-seed invoice: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, demoTenantID, newNumber)
	})

	var first string
	if err := pool.QueryRow(ctx,
		`SELECT invoice_number FROM invoices WHERE tenant_id = $1 AND entity_id = $2
		  ORDER BY created_at DESC, id DESC LIMIT 1`,
		demoTenantID, adeyemiEntityID,
	).Scan(&first); err != nil {
		t.Fatalf("query Adeyemi's entity-scoped page row 1: %v", err)
	}
	if first != newNumber {
		t.Errorf("Adeyemi's entity-scoped page row 1 = %q, want the freshly-created %q", first, newNumber)
	}
}

// TestSeedLeavesJunkInvoiceCreatedAtUntouched: C12 row 8 (AC-7). Companion to
// TestSeedLeavesJunkInvoiceFiscalColumnsUntouched (:1133) -- a non-curated invoice_number
// never matches Seed's ON CONFLICT target, so the re-anchoring UPDATE must not touch it
// either. Uses that test's shape (a junk invoice), not TestSeedLeavesJunkRowsInPlace's
// (:709), which plants a business_entities row.
func TestSeedLeavesJunkInvoiceCreatedAtUntouched(t *testing.T) {
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

	const junkNumber = "QA-JUNK-CREATED-AT-0001"
	const junkCreatedAt = "2020-01-01 00:00:00+00"
	if _, err := pool.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, created_at) VALUES ($1, $2, $3, $4::timestamptz)`,
		demoTenantID, entityID, junkNumber, junkCreatedAt,
	); err != nil {
		t.Fatalf("seed junk invoice (precondition): %v", err)
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed: %v", err)
	}

	var gotCreatedAt time.Time
	if err := pool.QueryRow(ctx,
		`SELECT created_at FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`,
		demoTenantID, junkNumber,
	).Scan(&gotCreatedAt); err != nil {
		t.Fatalf("read back junk invoice after Seed (want it to survive untouched): %v", err)
	}

	var unchanged bool
	if err := pool.QueryRow(ctx, `SELECT $1::timestamptz = $2::timestamptz`, gotCreatedAt, junkCreatedAt).Scan(&unchanged); err != nil {
		t.Fatalf("compare junk invoice's created_at: %v", err)
	}
	if !unchanged {
		t.Errorf("junk invoice's created_at after Seed = %v, want unchanged (%s) -- the upsert must not touch a row it does not curate", gotCreatedAt, junkCreatedAt)
	}
}

// submittableTriggerTINs are the three reserved TINs the mock adapter converges on, so a
// `validated` row carrying one is genuinely submittable from the demo UI.
var submittableTriggerTINs = []string{"99999999-0001", "99999999-0002", "99999999-0003"}

// nonConvergentTriggerTINs return Retryable forever (or are unallocated to a converging
// script), so a submittable row carrying one strands at `failed` after eight attempts.
var nonConvergentTriggerTINs = []string{"99999999-0004", "99999999-0006", "99999999-0007"}

// terminalTwinBuyerName is the DEMO-01 terminal row's buyer_name for each reserved trigger
// TIN. AC-3 makes buyer_name a per-TIN canonical name, so a twin shares its terminal row's
// counterparty -- status, not the name, is what distinguishes the two.
var terminalTwinBuyerName = map[string]string{
	"99999999-0001": "Sandbox APP (always accepts)",
	"99999999-0002": "Sandbox APP (always rejects)",
	"99999999-0003": "Sandbox APP (defers, then accepts)",
}

// submittableTwinRow is one seeded trigger twin plus everything a claimed-outcome check needs.
type submittableTwinRow struct {
	id               string
	invoiceNumber    string
	buyerTIN         string
	buyerName        string
	irn              *string
	csid             *string
	qrPayload        *string
	ruleSetVersionID *string
	violations       string
	rejectionReasons string
}

// reservedTINList is reservedTINReachableStatus's key set, sorted so a failure message reads
// the same on every run.
func reservedTINList() []string {
	out := make([]string, 0, len(reservedTINReachableStatus))
	for tin := range reservedTINReachableStatus {
		out = append(out, tin)
	}
	sort.Strings(out)
	return out
}

// TestSeedSeedsSubmittableTriggerTwinsInBothTenants: each demo tenant carries exactly three
// `validated` rows on the accept/reject/deferred triggers, so an operator can click Submit and
// watch a real clearance, refusal and poll cycle. Every clause is a sub-assertion of the count
// check on purpose: the twins do not exist yet, so a per-row test split out of this function
// would iterate an empty set and pass vacuously.
func TestSeedSeedsSubmittableTriggerTwinsInBothTenants(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	twinIDs := []string{}
	for _, tc := range []struct{ name, tenantID string }{
		{"firm", demoTenantID},
		{"in-house", honeywellTenantID},
	} {
		rows, err := pool.Query(ctx,
			`SELECT id, invoice_number, coalesce(buyer_tin, ''), coalesce(buyer_name, ''),
			        irn, csid, qr_payload, rule_set_version_id,
			        violations::text, rejection_reasons::text
			   FROM invoices
			  WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%'
			    AND status = 'validated' AND buyer_tin = ANY($2)
			  ORDER BY buyer_tin`,
			tc.tenantID, reservedTINList(),
		)
		if err != nil {
			t.Fatalf("%s: query validated reserved-TIN invoices: %v", tc.name, err)
		}
		var got []submittableTwinRow
		for rows.Next() {
			var r submittableTwinRow
			if err := rows.Scan(&r.id, &r.invoiceNumber, &r.buyerTIN, &r.buyerName,
				&r.irn, &r.csid, &r.qrPayload, &r.ruleSetVersionID,
				&r.violations, &r.rejectionReasons); err != nil {
				rows.Close()
				t.Fatalf("%s: scan twin row: %v", tc.name, err)
			}
			got = append(got, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("%s: iterate twin rows: %v", tc.name, err)
		}
		rows.Close()

		gotTINs := make([]string, len(got))
		for i, r := range got {
			gotTINs[i] = r.buyerTIN
			twinIDs = append(twinIDs, r.id)
		}
		if len(got) != len(submittableTriggerTINs) {
			t.Errorf("%s tenant: %d validated reserved-TIN invoices %v, want %d carrying exactly %v -- nothing in this tenant is submittable otherwise",
				tc.name, len(got), gotTINs, len(submittableTriggerTINs), submittableTriggerTINs)
		}
		if !reflect.DeepEqual(gotTINs, submittableTriggerTINs) {
			t.Errorf("%s tenant: validated reserved buyer TINs = %v, want exactly %v (one each, no non-convergent trigger)",
				tc.name, gotTINs, submittableTriggerTINs)
		}

		for _, r := range got {
			if r.buyerName == "" {
				t.Errorf("%s tenant: %s (buyer_tin=%s) has an empty buyer_name", tc.name, r.invoiceNumber, r.buyerTIN)
			}
			if terminal, ok := terminalTwinBuyerName[r.buyerTIN]; ok && r.buyerName != terminal {
				t.Errorf("%s tenant: %s buyer_name = %q, want it to equal %q -- a twin shares its terminal row's counterparty, status is what distinguishes them",
					tc.name, r.invoiceNumber, r.buyerName, terminal)
			}
			if r.irn != nil || r.csid != nil || r.qrPayload != nil {
				t.Errorf("%s tenant: %s carries irn/csid/qr_payload, want all three NULL -- a non-NULL irn is the \"already cleared\" sentinel and makes the twin unsubmittable",
					tc.name, r.invoiceNumber)
			}
			if r.ruleSetVersionID == nil {
				t.Errorf("%s tenant: %s has a NULL rule_set_version_id, want the active rule set", tc.name, r.invoiceNumber)
			}
			if r.violations != "[]" {
				t.Errorf("%s tenant: %s violations = %s, want []", tc.name, r.invoiceNumber, r.violations)
			}
			if r.rejectionReasons != "[]" {
				t.Errorf("%s tenant: %s rejection_reasons = %s, want []", tc.name, r.invoiceNumber, r.rejectionReasons)
			}
		}
	}

	// The only thing stopping a non-convergent trigger from being seeded submittable:
	// TestSeedNeverClaimsUnreachableOutcomeForReservedTIN now skips pre-submission statuses,
	// and its exemption is keyed on status, not TIN.
	stranded := mustCount(t, pool,
		`SELECT count(*) FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'
		    AND status IN ('draft', 'validated') AND buyer_tin = ANY($2)`,
		[]string{demoTenantID, honeywellTenantID}, nonConvergentTriggerTINs,
	)
	if stranded != 0 {
		t.Errorf("count(seeded draft/validated invoices on a non-convergent trigger %v) = %d, want 0 -- submitting one strands it at failed after eight attempts",
			nonConvergentTriggerTINs, stranded)
	}

	if n := mustCount(t, pool, `SELECT count(*) FROM submission_jobs WHERE invoice_id = ANY($1)`, twinIDs); n != 0 {
		t.Errorf("count(submission_jobs for the twins) = %d, want 0 -- they were never submitted, and seeding evidence is the fabrication DEMO-01 removed", n)
	}
	if n := mustCount(t, pool, `SELECT count(*) FROM app_exchange WHERE invoice_id = ANY($1)`, twinIDs); n != 0 {
		t.Errorf("count(app_exchange for the twins) = %d, want 0 -- they were never submitted", n)
	}
}

// fetchRegisterFirstPage returns a register's first page in the SPA's own order. entityID ""
// means tenant-wide (the in-house register); a non-empty one scopes to a firm client.
func fetchRegisterFirstPage(t *testing.T, pool *pgxpool.Pool, tenantID, entityID string) []string {
	t.Helper()
	sql := `SELECT invoice_number FROM invoices WHERE tenant_id = $1
	          ORDER BY created_at DESC, id DESC LIMIT 50`
	args := []any{tenantID}
	if entityID != "" {
		sql = `SELECT invoice_number FROM invoices WHERE tenant_id = $1 AND entity_id = $2
		        ORDER BY created_at DESC, id DESC LIMIT 50`
		args = append(args, entityID)
	}
	rows, err := pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query register first page for tenant %s: %v", tenantID, err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan invoice_number: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate register first page for tenant %s: %v", tenantID, err)
	}
	return got
}

// TestSeedSubmittableTwinsLeadTheRegisterFirstPage: the twins must be rows 1-3 of each
// register, not merely present somewhere in the first page -- an operator opening the demo has
// to reach something submittable without paging or filtering. Positional, not membership: a
// membership check fails today too, but says nothing about where the rows landed.
func TestSeedSubmittableTwinsLeadTheRegisterFirstPage(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// The firm register is entity-scoped and Adeyemi & Sons is clients[0], the default
	// workspace -- twins under any other entity sit behind a client switch.
	const adeyemiTIN = "10012345-0001"
	var adeyemiEntityID string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM business_entities WHERE tenant_id = $1 AND tin = $2`,
		demoTenantID, adeyemiTIN,
	).Scan(&adeyemiEntityID); err != nil {
		t.Fatalf("look up Adeyemi entity id (precondition): %v", err)
	}

	for _, tc := range []struct {
		name     string
		tenantID string
		entityID string
		want     []string
	}{
		{"firm (Adeyemi-scoped)", demoTenantID, adeyemiEntityID,
			[]string{"DEMO-2026-1009", "DEMO-2026-1008", "DEMO-2026-1007"}},
		{"in-house (tenant-wide)", honeywellTenantID, "",
			[]string{"DEMO-2026-9003", "DEMO-2026-9002", "DEMO-2026-9001"}},
	} {
		got := fetchRegisterFirstPage(t, pool, tc.tenantID, tc.entityID)
		if len(got) < len(tc.want) {
			t.Errorf("%s register first page has %d rows, want at least %d", tc.name, len(got), len(tc.want))
			continue
		}
		if !reflect.DeepEqual(got[:len(tc.want)], tc.want) {
			t.Errorf("%s register rows 1-3 = %v, want the submittable twins %v leading the page", tc.name, got[:len(tc.want)], tc.want)
		}
	}
}

// twinInvoiceNumbers is each demo tenant's three submittable trigger twins.
var twinInvoiceNumbers = map[string][]string{
	demoTenantID:      {"DEMO-2026-1007", "DEMO-2026-1008", "DEMO-2026-1009"},
	honeywellTenantID: {"DEMO-2026-9001", "DEMO-2026-9002", "DEMO-2026-9003"},
}

// TestSeedRearmsAFiredTwinOnReseed: a twin is the one seeded row a demo actually submits, so
// re-seeding has to land it back on exactly `validated` with no fiscal residue. "Terminal" is
// too weak a target here -- the SPA only offers Submit on `validated`, so a twin left at
// `accepted` (or stranded at `queued`) is unusable for the next demo. Covers both states a
// live submit leaves behind: mid-flight, and cleared with a real IRN.
func TestSeedRearmsAFiredTwinOnReseed(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("first Seed (establish baseline): %v", err)
	}

	cases := []struct {
		name     string
		tenantID string
		number   string
		fire     string
	}{
		{"in-flight twin", demoTenantID, "DEMO-2026-1007",
			`UPDATE invoices SET status = 'queued' WHERE tenant_id = $1 AND invoice_number = $2`},
		{"cleared twin", honeywellTenantID, "DEMO-2026-9001",
			`UPDATE invoices SET status = 'accepted', irn = 'LIVE-IRN', csid = 'LIVE-CSID', qr_payload = 'LIVE-QR'
			  WHERE tenant_id = $1 AND invoice_number = $2`},
	}

	for _, tc := range cases {
		ct, err := pool.Exec(ctx, tc.fire, tc.tenantID, tc.number)
		if err != nil {
			t.Fatalf("%s: fire %s (precondition): %v", tc.name, tc.number, err)
		}
		if ct.RowsAffected() != 1 {
			t.Fatalf("%s: firing %s updated %d rows, want 1 -- the twin is missing", tc.name, tc.number, ct.RowsAffected())
		}
	}

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("second Seed (re-arm): %v", err)
	}

	for _, tc := range cases {
		var status string
		var irn, csid, qr *string
		if err := pool.QueryRow(ctx,
			`SELECT status, irn, csid, qr_payload FROM invoices
			  WHERE tenant_id = $1 AND invoice_number = $2`,
			tc.tenantID, tc.number,
		).Scan(&status, &irn, &csid, &qr); err != nil {
			t.Fatalf("%s: read %s back after re-seed: %v", tc.name, tc.number, err)
		}
		if status != "validated" {
			t.Errorf("%s: %s status after re-seed = %q, want validated -- Submit is offered on validated only, so the twin is not re-armed",
				tc.name, tc.number, status)
		}
		if irn != nil || csid != nil || qr != nil {
			t.Errorf("%s: %s kept a fiscal identifier through re-seed (irn=%v csid=%v qr=%v), want all three NULL -- a surviving irn is the \"already cleared\" sentinel",
				tc.name, tc.number, derefOrNil(irn), derefOrNil(csid), derefOrNil(qr))
		}
	}
}

// derefOrNil renders a *string for a failure message without panicking on NULL.
func derefOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// TestSeedDemoInvoiceNumbersAreDisjointAcrossTenants: line_item_seed's INSERT joins invoices
// on invoice_number ALONE across both demo tenants (db/seed.dev.sql), so one number reused in
// the other tenant attaches a single seeded line item to two invoices across a tenant
// boundary. Nothing else in the suite pins that precondition.
func TestSeedDemoInvoiceNumbersAreDisjointAcrossTenants(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	demoTenants := []string{demoTenantID, honeywellTenantID}
	total := mustCount(t, pool,
		`SELECT count(*) FROM invoices WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'`,
		demoTenants)
	if total == 0 {
		t.Fatal("precondition: zero DEMO-2026-* invoices across both tenants after Seed")
	}

	rows, err := pool.Query(ctx,
		`SELECT invoice_number, count(DISTINCT tenant_id)
		   FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'
		  GROUP BY invoice_number
		 HAVING count(DISTINCT tenant_id) > 1
		  ORDER BY invoice_number`,
		demoTenants)
	if err != nil {
		t.Fatalf("query cross-tenant invoice_number collisions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var number string
		var tenants int
		if err := rows.Scan(&number, &tenants); err != nil {
			t.Fatalf("scan collision row: %v", err)
		}
		t.Errorf("invoice_number %s is seeded in %d demo tenants, want 1 -- line_item_seed joins on invoice_number alone, so its line item now attaches to both",
			number, tenants)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate collision rows: %v", err)
	}

	// A number that exists in only one tenant still fans out if the join reaches nothing --
	// a typo'd line_item_seed number renders a hollow detail screen and trips no other test.
	// Exactly one member is allowed on purpose (the no-source demo invoice); a second
	// member here means a typo'd line_item_seed number, not a feature.
	zeroLineItem := zeroLineItemDemoInvoices(t, pool)
	wantZeroLineItem := []string{"DEMO-2026-5005"}
	if !reflect.DeepEqual(zeroLineItem, wantZeroLineItem) {
		t.Errorf("zero-line-item DEMO-2026-* invoices = %v, want exactly %v", zeroLineItem, wantZeroLineItem)
	}
}

// zeroLineItemDemoInvoices returns every seeded DEMO-2026-* invoice number, across both
// demo tenants, that has zero line_items -- the set demodocs.Seed's INNER JOIN
// line_items can never reach.
func zeroLineItemDemoInvoices(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT invoice_number FROM invoices i
		  WHERE i.tenant_id = ANY($1) AND i.invoice_number LIKE 'DEMO-2026-%'
		    AND NOT EXISTS (SELECT 1 FROM line_items l WHERE l.invoice_id = i.id)
		  ORDER BY i.invoice_number`,
		[]string{demoTenantID, honeywellTenantID})
	if err != nil {
		t.Fatalf("query zero-line-item DEMO-2026-* invoices: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var number string
		if err := rows.Scan(&number); err != nil {
			t.Fatalf("scan zero-line-item row: %v", err)
		}
		out = append(out, number)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate zero-line-item rows: %v", err)
	}
	return out
}

// demodocsSeedCommentPath is internal/demodocs/demodocs.go, read straight off disk --
// it is not go:embed'd, and the doc comment BUG-02-03 pins lives only there.
const demodocsSeedCommentPath = "../../demodocs/demodocs.go"

// demodocsNumberPattern matches one DEMO-2026-#### invoice number.
var demodocsNumberPattern = regexp.MustCompile(`DEMO-2026-\d{4}`)

// extractDemodocsSeedComment returns the single DEMO-2026-#### number demodocs.go's Seed
// doc comment cites, failing if the comment cites zero or more than one.
func extractDemodocsSeedComment(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(demodocsSeedCommentPath)
	if err != nil {
		t.Fatalf("read %s: %v", demodocsSeedCommentPath, err)
	}
	start := strings.Index(string(src), "// Seed attaches")
	end := strings.Index(string(src), "func Seed(")
	if start < 0 || end < 0 || end < start {
		t.Fatalf("could not locate Seed's doc comment in %s", demodocsSeedCommentPath)
	}
	matches := demodocsNumberPattern.FindAllString(string(src)[start:end], -1)
	if len(matches) != 1 {
		t.Fatalf("Seed doc comment cites %d DEMO-2026-#### numbers (%v), want exactly 1", len(matches), matches)
	}
	return matches[0]
}

// isZeroLineItemDemoInvoice reports whether number is a seeded DEMO-2026-* invoice, in
// either demo tenant, with zero line_items.
func isZeroLineItemDemoInvoice(t *testing.T, pool *pgxpool.Pool, number string) bool {
	t.Helper()
	return mustCount(t, pool,
		`SELECT count(*) FROM invoices i
		  WHERE i.tenant_id = ANY($1) AND i.invoice_number = $2
		    AND NOT EXISTS (SELECT 1 FROM line_items l WHERE l.invoice_id = i.id)`,
		[]string{demoTenantID, honeywellTenantID}, number) == 1
}

// TestSeedSeedsALineItemFreeDemoInvoice: BUG-02-03. The seed must carry exactly one
// DEMO-2026-* invoice with no line items -- an invoice a real import could never have
// produced -- so the source-document previewer's empty state is reachable from demo data.
func TestSeedSeedsALineItemFreeDemoInvoice(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	zeroLineItem := zeroLineItemDemoInvoices(t, pool)
	if len(zeroLineItem) != 1 || zeroLineItem[0] != "DEMO-2026-5005" {
		t.Errorf("zero-line-item DEMO-2026-* invoices = %v, want exactly [DEMO-2026-5005]", zeroLineItem)
	}

	t.Run("honest_shape", func(t *testing.T) {
		var status string
		var ruleSetVersionID, irn, csid, qrPayload *string
		var violations, rejectionReasons string
		var vatOK, totalOK bool
		err := pool.QueryRow(ctx,
			`SELECT status, rule_set_version_id, irn, csid, qr_payload,
			        violations::text, rejection_reasons::text,
			        vat = round(subtotal * 0.075, 2), total = subtotal + vat
			   FROM invoices WHERE tenant_id = $1 AND invoice_number = 'DEMO-2026-5005'`,
			demoTenantID,
		).Scan(&status, &ruleSetVersionID, &irn, &csid, &qrPayload,
			&violations, &rejectionReasons, &vatOK, &totalOK)
		if err != nil {
			t.Fatalf("read DEMO-2026-5005 (want it seeded per BUG-02-03): %v", err)
		}
		if status != "draft" {
			t.Errorf("status = %q, want draft", status)
		}
		if ruleSetVersionID != nil {
			t.Errorf("rule_set_version_id = %v, want NULL (validated=false)", *ruleSetVersionID)
		}
		if irn != nil || csid != nil || qrPayload != nil {
			t.Errorf("irn/csid/qr_payload = %v/%v/%v, want all NULL", irn, csid, qrPayload)
		}
		if violations != "[]" {
			t.Errorf("violations = %s, want []", violations)
		}
		if rejectionReasons != "[]" {
			t.Errorf("rejection_reasons = %s, want []", rejectionReasons)
		}
		if !vatOK {
			t.Error("vat != round(subtotal * 0.075, 2)")
		}
		if !totalOK {
			t.Error("total != subtotal + vat")
		}
	})

	t.Run("ordinary_buyer_tin", func(t *testing.T) {
		var buyerTIN string
		if err := pool.QueryRow(ctx,
			`SELECT coalesce(buyer_tin, '') FROM invoices WHERE tenant_id = $1 AND invoice_number = 'DEMO-2026-5005'`,
			demoTenantID,
		).Scan(&buyerTIN); err != nil {
			t.Fatalf("read DEMO-2026-5005 buyer_tin (want it seeded per BUG-02-03): %v", err)
		}
		reserved := map[string]bool{
			"99999999-0001": true, "99999999-0002": true, "99999999-0003": true, "99999999-0004": true,
			"99999999-0005": true, "99999999-0006": true, "99999999-0007": true,
		}
		if reserved[buyerTIN] {
			t.Errorf("buyer_tin = %s, want an ordinary TIN -- not one of the reserved triggers (99999999-0001..0007)", buyerTIN)
		}
	})
}

// TestSeedNoSourceDemoInvoiceMatchesDemodocsComment: BUG-02-03. demodocs.go's Seed doc
// comment names its own "no source document" example -- this is the check whose absence
// let that comment cite a phantom invoice number for as long as it did.
func TestSeedNoSourceDemoInvoiceMatchesDemodocsComment(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	number := extractDemodocsSeedComment(t)
	if !isZeroLineItemDemoInvoice(t, pool, number) {
		t.Errorf("demodocs.go's Seed comment cites %s, but it is not the seeded zero-line-item demo invoice", number)
	}

	t.Run("fails_on_a_phantom", func(t *testing.T) {
		const phantom = "DEMO-2026-7005" // the seed's real 7000-block gap; never seeded
		if isZeroLineItemDemoInvoice(t, pool, phantom) {
			t.Fatalf("%s reports present -- it must never be seeded, or this detector is vacuous", phantom)
		}
		if number == phantom {
			t.Errorf("demodocs.go's Seed comment still names the phantom %s", phantom)
		}
	})
}

// TestSeedTwinLineItemsReconcileWithSubtotal: the twins are seeded `validated` with
// violations [], so their own arithmetic has to hold -- a twin whose line items miss the
// subtotal, or whose VAT is off the standard rate, fires line-items-sum-subtotal or
// vat-standard-rate the moment a demo re-validates it, and the clean claim was a lie.
// Every comparison is evaluated as numeric in SQL: exact decimal, no float rounding.
func TestSeedTwinLineItemsReconcileWithSubtotal(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	checked := 0
	for _, tenantID := range []string{demoTenantID, honeywellTenantID} {
		for _, number := range twinInvoiceNumbers[tenantID] {
			var lines int
			var subtotal, vat, total, lineSum, qtyPrice string
			var lineSumOK, qtyPriceOK, vatOK, totalOK bool
			err := pool.QueryRow(ctx,
				`SELECT count(l.id),
				        i.subtotal::text, i.vat::text, i.total::text,
				        coalesce(sum(l.line_total), 0)::text,
				        coalesce(sum(l.quantity * l.unit_price), 0)::text,
				        coalesce(sum(l.line_total), 0) = i.subtotal,
				        coalesce(sum(l.quantity * l.unit_price), 0) = i.subtotal,
				        i.vat = round(i.subtotal * 0.075, 2),
				        i.total = i.subtotal + i.vat
				   FROM invoices i
				   LEFT JOIN line_items l ON l.invoice_id = i.id
				  WHERE i.tenant_id = $1 AND i.invoice_number = $2
				  GROUP BY i.id, i.subtotal, i.vat, i.total`,
				tenantID, number,
			).Scan(&lines, &subtotal, &vat, &total, &lineSum, &qtyPrice,
				&lineSumOK, &qtyPriceOK, &vatOK, &totalOK)
			if err != nil {
				t.Fatalf("%s: read twin arithmetic: %v", number, err)
			}
			checked++

			if lines != 1 {
				t.Errorf("%s has %d line items, want 1 -- more means line_item_seed fanned out across the tenant boundary, fewer means its join reached nothing", number, lines)
			}
			if !qtyPriceOK {
				t.Errorf("%s: sum(quantity * unit_price) = %s, want subtotal %s", number, qtyPrice, subtotal)
			}
			if !lineSumOK {
				t.Errorf("%s: sum(line_total) = %s, want subtotal %s -- fires line-items-sum-subtotal on a row claiming violations []", number, lineSum, subtotal)
			}
			if !vatOK {
				t.Errorf("%s: vat = %s, want 7.5%% of subtotal %s -- fires vat-standard-rate", number, vat, subtotal)
			}
			if !totalOK {
				t.Errorf("%s: total = %s, want subtotal %s + vat %s", number, total, subtotal, vat)
			}
		}
	}
	if checked != 6 {
		t.Fatalf("checked %d twins, want 6 -- twinInvoiceNumbers drifted from the seed", checked)
	}
}

// labelledBuyerNamePattern matches "<name> (<parenthetical>)" -- AC-1 requires every seeded
// DEMO-2026-* buyer_name to state, in that parenthetical, the outcome or violation its row
// demonstrates.
var labelledBuyerNamePattern = regexp.MustCompile(`^.+ \(.+\)$`)

// TestSeedEveryDemoInvoiceCarriesALabelledCounterparty: Test Spec row "every invoice
// carries a labelled counterparty" (AC-1). All 44 seeded DEMO-2026-* rows, across both
// tenants, must carry a non-empty buyer_name matching labelledBuyerNamePattern.
func TestSeedEveryDemoInvoiceCarriesALabelledCounterparty(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT invoice_number, coalesce(buyer_name, '')
		   FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'
		  ORDER BY invoice_number`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query DEMO-2026-* buyer_names: %v", err)
	}
	defer rows.Close()

	var checked int
	for rows.Next() {
		var number, name string
		if err := rows.Scan(&number, &name); err != nil {
			t.Fatalf("scan buyer_name row: %v", err)
		}
		checked++
		if name == "" {
			t.Errorf("%s: buyer_name is empty, want a name that states the scenario this row demonstrates", number)
			continue
		}
		if !labelledBuyerNamePattern.MatchString(name) {
			t.Errorf("%s: buyer_name = %q, does not match %s -- every seeded counterparty name must state the outcome or violation the row demonstrates",
				number, name, labelledBuyerNamePattern.String())
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate buyer_name rows: %v", err)
	}
	if checked != 44 {
		t.Fatalf("checked %d DEMO-2026-* invoices, want exactly 44", checked)
	}
}

// TestSeedOneBuyerNamePerBuyerTIN: Test Spec row "one buyer name per buyer TIN" (AC-3).
// Every seeded buyer_tin, reserved TINs included, resolves to exactly one buyer_name
// across the whole seed -- a TIN carrying two names is the same counterparty telling two
// different stories depending on which invoice a demo happens to open.
func TestSeedOneBuyerNamePerBuyerTIN(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT buyer_tin, array_agg(DISTINCT buyer_name ORDER BY buyer_name)
		   FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'
		  GROUP BY buyer_tin
		 HAVING count(DISTINCT buyer_name) > 1
		  ORDER BY buyer_tin`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query buyer_tin -> buyer_name fan-out: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tin string
		var names []string
		if err := rows.Scan(&tin, &names); err != nil {
			t.Fatalf("scan buyer_tin fan-out row: %v", err)
		}
		t.Errorf("buyer_tin=%s carries %d distinct buyer_names %v, want exactly 1 -- the same counterparty must not read as two different companies depending on which invoice is open",
			tin, len(names), names)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate buyer_tin fan-out rows: %v", err)
	}
}

// scenarioRow is one seeded DEMO-2026-* invoice's buyer_tin plus the components the
// scenario-key derivation below groups on.
type scenarioRow struct {
	invoiceNumber string
	buyerTIN      string
	status        string
	ruleKey       string
	rejected      bool
	unvalidated   bool
	supplierOK    bool
}

// supplierTINShapePattern is the well-formed supplier_tin shape (NNNNNNNN-NNNN).
// DEMO-2026-6002 is the one seeded row that deliberately fails it (its buyer_tin, not
// supplier_tin, is what changes under the roster) -- that mismatch is part of what keeps
// its scenario key distinct from every other draft row.
var supplierTINShapePattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{4}$`)

// scenarioKey is the Test Spec's composite key: (status, violations->0->>'rule_key',
// rejection_reasons <> '[]', rule_set_version_id IS NULL, supplier_tin shape). Two rows
// with the same key demonstrate the same scenario.
func (r scenarioRow) scenarioKey() string {
	return strings.Join([]string{
		r.status, r.ruleKey,
		strconv.FormatBool(r.rejected),
		strconv.FormatBool(r.unvalidated),
		strconv.FormatBool(r.supplierOK),
	}, "|")
}

// fetchScenarioRows returns every seeded DEMO-2026-* invoice across both tenants with its
// scenario-key components. excludeReserved true drops buyer_tin LIKE '99999999-%' -- the
// reserved trigger TINs legitimately carry both a terminal row and a validated twin that
// differ only on status, so the bijectivity check in TestSeedOneScenarioPerCounterparty
// only holds with them excluded.
func fetchScenarioRows(t *testing.T, pool *pgxpool.Pool, excludeReserved bool) []scenarioRow {
	t.Helper()
	sql := `SELECT invoice_number, buyer_tin, status,
	               coalesce(violations->0->>'rule_key', ''),
	               (rejection_reasons <> '[]'::jsonb),
	               (rule_set_version_id IS NULL),
	               supplier_tin
	          FROM invoices
	         WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'`
	if excludeReserved {
		sql += ` AND buyer_tin NOT LIKE '99999999-%'`
	}
	rows, err := pool.Query(context.Background(), sql, []string{demoTenantID, honeywellTenantID})
	if err != nil {
		t.Fatalf("query scenario rows (excludeReserved=%t): %v", excludeReserved, err)
	}
	defer rows.Close()

	var got []scenarioRow
	for rows.Next() {
		var r scenarioRow
		var supplierTIN string
		if err := rows.Scan(&r.invoiceNumber, &r.buyerTIN, &r.status, &r.ruleKey,
			&r.rejected, &r.unvalidated, &supplierTIN); err != nil {
			t.Fatalf("scan scenario row: %v", err)
		}
		r.supplierOK = supplierTINShapePattern.MatchString(supplierTIN)
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate scenario rows: %v", err)
	}
	return got
}

// sortedStringSet returns a string set's keys, sorted, so a failure message reads the
// same on every run (map iteration order is otherwise random).
func sortedStringSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestSeedOneScenarioPerCounterparty: Test Spec row "one scenario per counterparty"
// (AC-3), scoped to buyer_tin NOT LIKE '99999999-%'. Each of the nine ordinary scenarios
// must map to exactly one buyer_tin, and each ordinary buyer_tin to exactly one scenario --
// reserved trigger TINs are excluded because they legitimately carry both a terminal and a
// twin row that differ on status (see TestSeedOneScenarioPerCounterpartyExcludesReservedTINs
// below, which proves that exclusion is load-bearing).
func TestSeedOneScenarioPerCounterparty(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows := fetchScenarioRows(t, pool, true)
	if len(rows) == 0 {
		t.Fatal("zero non-reserved DEMO-2026-* invoices -- the exclusion filter or the LIKE pattern is wrong")
	}

	tinToKeys := map[string]map[string]bool{}
	keyToTINs := map[string]map[string]bool{}
	for _, r := range rows {
		key := r.scenarioKey()
		if tinToKeys[r.buyerTIN] == nil {
			tinToKeys[r.buyerTIN] = map[string]bool{}
		}
		tinToKeys[r.buyerTIN][key] = true
		if keyToTINs[key] == nil {
			keyToTINs[key] = map[string]bool{}
		}
		keyToTINs[key][r.buyerTIN] = true
	}

	for tin, keys := range tinToKeys {
		if len(keys) > 1 {
			t.Errorf("buyer_tin=%s maps to %d distinct scenario keys %v, want exactly 1 -- one counterparty must demonstrate exactly one scenario",
				tin, len(keys), sortedStringSet(keys))
		}
	}
	for key, tins := range keyToTINs {
		if len(tins) > 1 {
			t.Errorf("scenario key %q maps to %d distinct buyer_tins %v, want exactly 1 -- two counterparties are demonstrating the same scenario",
				key, len(tins), sortedStringSet(tins))
		}
	}
}

// TestSeedOneScenarioPerCounterpartyExcludesReservedTINs: the same derivation as
// TestSeedOneScenarioPerCounterparty, but WITHOUT excluding buyer_tin LIKE '99999999-%'.
// Reserved TINs legitimately carry two scenario keys (a terminal row and a validated twin
// that differ only on status), so proves the exclusion above is load-bearing -- if it ever
// decayed into a no-op filter, TestSeedOneScenarioPerCounterparty would start failing on a
// reserved TIN for a reason that has nothing to do with AC-3.
func TestSeedOneScenarioPerCounterpartyExcludesReservedTINs(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows := fetchScenarioRows(t, pool, false)

	reservedKeys := map[string]map[string]bool{}
	for _, r := range rows {
		if !strings.HasPrefix(r.buyerTIN, "99999999-") {
			continue
		}
		if reservedKeys[r.buyerTIN] == nil {
			reservedKeys[r.buyerTIN] = map[string]bool{}
		}
		reservedKeys[r.buyerTIN][r.scenarioKey()] = true
	}
	if len(reservedKeys) == 0 {
		t.Fatal("zero reserved-TIN DEMO-2026-* invoices found -- fetchScenarioRows(excludeReserved=false) or the reserved TIN prefix is wrong")
	}

	multi := 0
	for tin, keys := range reservedKeys {
		if len(keys) > 1 {
			multi++
			t.Logf("buyer_tin=%s maps to %d scenario keys %v (expected -- terminal row + twin)", tin, len(keys), sortedStringSet(keys))
		}
	}
	if multi == 0 {
		t.Fatal("no reserved buyer_tin maps to more than one scenario key -- TestSeedOneScenarioPerCounterparty's buyer_tin NOT LIKE '99999999-%' exclusion excludes nothing, i.e. it is vacuous")
	}
}

// TestSeedPreservesDeliberatelyMalformedTINs: DEMO-2026-6002's supplier_tin and
// DEMO-2026-6003's buyer_tin are malformed ON PURPOSE -- the bad value IS the
// supplier-tin-format / buyer-tin-format violation each row demonstrates. A relabel that
// "cleans up" either value would delete the violation it exists to show.
func TestSeedPreservesDeliberatelyMalformedTINs(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	var supplierTIN, ruleKey6002 string
	if err := pool.QueryRow(ctx,
		`SELECT supplier_tin, coalesce(violations->0->>'rule_key', '') FROM invoices
		  WHERE tenant_id = $1 AND invoice_number = 'DEMO-2026-6002'`,
		demoTenantID,
	).Scan(&supplierTIN, &ruleKey6002); err != nil {
		t.Fatalf("read DEMO-2026-6002: %v", err)
	}
	if supplierTIN != "BADTIN" {
		t.Errorf("DEMO-2026-6002 supplier_tin = %q, want the malformed %q -- the value IS the violation", supplierTIN, "BADTIN")
	}
	if ruleKey6002 != "supplier-tin-format" {
		t.Errorf("DEMO-2026-6002 violations[0].rule_key = %q, want %q", ruleKey6002, "supplier-tin-format")
	}

	var buyerTIN, ruleKey6003 string
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(buyer_tin, ''), coalesce(violations->0->>'rule_key', '') FROM invoices
		  WHERE tenant_id = $1 AND invoice_number = 'DEMO-2026-6003'`,
		demoTenantID,
	).Scan(&buyerTIN, &ruleKey6003); err != nil {
		t.Fatalf("read DEMO-2026-6003: %v", err)
	}
	if buyerTIN != "12345678" {
		t.Errorf("DEMO-2026-6003 buyer_tin = %q, want the malformed %q -- the value IS the violation", buyerTIN, "12345678")
	}
	if ruleKey6003 != "buyer-tin-format" {
		t.Errorf("DEMO-2026-6003 violations[0].rule_key = %q, want %q", ruleKey6003, "buyer-tin-format")
	}
}

// TestSeedCleanDemoInvoicesReconcile: every seeded demo invoice carrying no violations, and
// at least one line item, satisfies the whole-portfolio arithmetic -- vat is 7.5% of
// subtotal, total is subtotal+vat, and the line items sum to the subtotal. INNER JOIN
// line_items, not LEFT JOIN + coalesce(sum(...),0): an invoice with a non-zero subtotal and
// zero line items by design has no line_items row to join to, so it is excluded here rather
// than wrongly forced through a sum(line_total)=subtotal check it was never meant to satisfy.
func TestSeedCleanDemoInvoicesReconcile(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT i.invoice_number,
		        i.subtotal::text, i.vat::text, i.total::text, sum(l.line_total)::text,
		        i.vat = round(i.subtotal * 0.075, 2),
		        i.total = i.subtotal + i.vat,
		        sum(l.line_total) = i.subtotal
		   FROM invoices i
		   INNER JOIN line_items l ON l.invoice_id = i.id
		  WHERE i.tenant_id = ANY($1) AND i.invoice_number LIKE 'DEMO-2026-%' AND i.violations::text = '[]'
		  GROUP BY i.id, i.subtotal, i.vat, i.total
		  ORDER BY i.invoice_number`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query clean demo invoices: %v", err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var number, subtotal, vat, total, lineSum string
		var vatOK, totalOK, lineSumOK bool
		if err := rows.Scan(&number, &subtotal, &vat, &total, &lineSum, &vatOK, &totalOK, &lineSumOK); err != nil {
			t.Fatalf("scan clean invoice row: %v", err)
		}
		checked++
		if !vatOK {
			t.Errorf("%s: vat = %s, want 7.5%% of subtotal %s", number, vat, subtotal)
		}
		if !totalOK {
			t.Errorf("%s: total = %s, want subtotal %s + vat %s", number, total, subtotal, vat)
		}
		if !lineSumOK {
			t.Errorf("%s: sum(line_total) = %s, want subtotal %s", number, lineSum, subtotal)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate clean invoice rows: %v", err)
	}
	if checked == 0 {
		t.Fatal("zero clean (violations=[]) demo invoices with line items found -- query or seed drifted")
	}
}

// TestSeedVATViolationIsVisiblyWrong: every seeded demo invoice flagged with the
// vat-standard-rate violation must be wrong by a round fraction a reader can check by eye --
// vat exactly a tenth of the subtotal -- not merely some other number that isn't 7.5%.
func TestSeedVATViolationIsVisiblyWrong(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT invoice_number, subtotal::text, vat::text,
		        vat = round(subtotal / 10, 2),
		        vat = round(subtotal * 0.075, 2)
		   FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'
		    AND coalesce(violations->0->>'rule_key', '') = 'vat-standard-rate'
		  ORDER BY invoice_number`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query vat-standard-rate invoices: %v", err)
	}
	defer rows.Close()

	var numbers []string
	for rows.Next() {
		var number, subtotal, vat string
		var tenthOK, standardOK bool
		if err := rows.Scan(&number, &subtotal, &vat, &tenthOK, &standardOK); err != nil {
			t.Fatalf("scan vat-standard-rate row: %v", err)
		}
		numbers = append(numbers, number)
		if !tenthOK {
			t.Errorf("%s: vat = %s, want exactly a tenth of subtotal %s", number, vat, subtotal)
		}
		if standardOK {
			t.Errorf("%s: vat = %s equals the standard 7.5%% rate on subtotal %s -- the violation is invisible", number, vat, subtotal)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate vat-standard-rate rows: %v", err)
	}

	want := []string{"DEMO-2026-1006", "DEMO-2026-2005", "DEMO-2026-8005"}
	if !reflect.DeepEqual(numbers, want) {
		t.Fatalf("vat-standard-rate demo invoices = %v, want exactly %v", numbers, want)
	}
}

// TestSeedSubtotalMismatchIsExactlyOneInvoice: DEMO-2026-4002 stays the single deliberate
// line-items-sum-subtotal example, with a shortfall a reader can check by eye. INNER JOIN,
// not LEFT JOIN + coalesce: a zero-line-item invoice with a non-zero subtotal by design has
// no line_items row to join to, so it can never surface here as a second, spurious mismatch.
func TestSeedSubtotalMismatchIsExactlyOneInvoice(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT i.invoice_number, i.subtotal::text, sum(l.line_total)::text,
		        mod(i.subtotal - sum(l.line_total), 1000::numeric) = 0
		   FROM invoices i
		   INNER JOIN line_items l ON l.invoice_id = i.id
		  WHERE i.tenant_id = ANY($1) AND i.invoice_number LIKE 'DEMO-2026-%'
		  GROUP BY i.id, i.subtotal
		 HAVING sum(l.line_total) <> i.subtotal
		  ORDER BY i.invoice_number`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query subtotal-mismatch invoices: %v", err)
	}
	defer rows.Close()

	var numbers []string
	for rows.Next() {
		var number, subtotal, lineSum string
		var wholeThousand bool
		if err := rows.Scan(&number, &subtotal, &lineSum, &wholeThousand); err != nil {
			t.Fatalf("scan subtotal-mismatch row: %v", err)
		}
		numbers = append(numbers, number)
		if !wholeThousand {
			t.Errorf("%s: subtotal %s minus sum(line_total) %s is not a whole number of thousands", number, subtotal, lineSum)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate subtotal-mismatch rows: %v", err)
	}

	want := []string{"DEMO-2026-4002"}
	if !reflect.DeepEqual(numbers, want) {
		t.Fatalf("subtotal-mismatch demo invoices = %v, want exactly %v", numbers, want)
	}
}

// TestSeedEveryLineTotalIsQuantityTimesUnitPrice: every seeded demo line item's stated total
// is its own quantity times unit price, independent of whether that line's invoice reconciles
// to its subtotal -- DEMO-2026-4002 fails that check on purpose, but its one line item must
// still pass this one.
func TestSeedEveryLineTotalIsQuantityTimesUnitPrice(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT i.invoice_number, l.line_no,
		        l.quantity::text, l.unit_price::text, l.line_total::text,
		        l.line_total = l.quantity * l.unit_price
		   FROM line_items l
		   JOIN invoices i ON i.id = l.invoice_id
		  WHERE i.tenant_id = ANY($1) AND i.invoice_number LIKE 'DEMO-2026-%'
		  ORDER BY i.invoice_number, l.line_no`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query demo line items: %v", err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var number string
		var lineNo int
		var quantity, unitPrice, lineTotal string
		var ok bool
		if err := rows.Scan(&number, &lineNo, &quantity, &unitPrice, &lineTotal, &ok); err != nil {
			t.Fatalf("scan demo line item row: %v", err)
		}
		checked++
		if !ok {
			t.Errorf("%s line %d: line_total = %s, want quantity %s * unit_price %s", number, lineNo, lineTotal, quantity, unitPrice)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate demo line item rows: %v", err)
	}
	if checked == 0 {
		t.Fatal("zero demo line items found -- query or seed drifted")
	}
}

// TestSeedVATViolationRowsStayAdditionConsistent: QA gap-fill. The three vat-standard-rate
// rows are excluded from TestSeedCleanDemoInvoicesReconcile (they carry a violation) and
// TestSeedVATViolationIsVisiblyWrong only checks the rate, not the sum -- so nothing
// currently catches an edit that fixes vat but leaves total stale, which would silently
// swap what the row demonstrates (a rate violation) for a broken-addition one.
func TestSeedVATViolationRowsStayAdditionConsistent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT invoice_number, subtotal::text, vat::text, total::text,
		        total = subtotal + vat
		   FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'
		    AND coalesce(violations->0->>'rule_key', '') = 'vat-standard-rate'
		  ORDER BY invoice_number`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query vat-standard-rate invoices: %v", err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var number, subtotal, vat, total string
		var ok bool
		if err := rows.Scan(&number, &subtotal, &vat, &total, &ok); err != nil {
			t.Fatalf("scan vat-standard-rate row: %v", err)
		}
		checked++
		if !ok {
			t.Errorf("%s: total = %s, want subtotal %s + vat %s", number, total, subtotal, vat)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate vat-standard-rate rows: %v", err)
	}
	if checked != 3 {
		t.Fatalf("checked %d vat-standard-rate rows, want 3", checked)
	}
}

// TestSeedLineItemMismatchRowHasStandardVAT: QA gap-fill for AC-2 ("every other demo
// invoice satisfies vat=round(subtotal*0.075,2) and total=subtotal+vat"). DEMO-2026-4002 is
// excluded from TestSeedCleanDemoInvoicesReconcile (its own violations isn't '[]') and it
// isn't a vat-standard-rate row either, so no existing test checks its VAT arithmetic --
// only its deliberate line-sum mismatch. It must demonstrate exactly one thing
// (line-items-sum-subtotal), not silently also drift off the standard VAT rate.
func TestSeedLineItemMismatchRowHasStandardVAT(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	var subtotal, vat, total string
	var vatOK, totalOK bool
	err := pool.QueryRow(ctx,
		`SELECT subtotal::text, vat::text, total::text,
		        vat = round(subtotal * 0.075, 2),
		        total = subtotal + vat
		   FROM invoices
		  WHERE tenant_id = $1 AND invoice_number = 'DEMO-2026-4002'`,
		demoTenantID,
	).Scan(&subtotal, &vat, &total, &vatOK, &totalOK)
	if err != nil {
		t.Fatalf("query DEMO-2026-4002: %v", err)
	}
	if !vatOK {
		t.Errorf("DEMO-2026-4002: vat = %s, want 7.5%% of subtotal %s", vat, subtotal)
	}
	if !totalOK {
		t.Errorf("DEMO-2026-4002: total = %s, want subtotal %s + vat %s", total, subtotal, vat)
	}
}

// issueDateMonthSpan is the minimum count of distinct calendar months a tenant's seeded
// issue_dates must cover -- a single-month cluster reads as "this month only", never a
// working book over time.
const issueDateMonthSpan = 5

// TestSeedIssueDatesSpanMultipleMonths: pins the SHAPE of the spread, not a hardcoded date
// list -- each tenant's DEMO-2026-* issue_dates must land in at least issueDateMonthSpan
// distinct calendar months, leaving the executor free to choose which dates.
func TestSeedIssueDatesSpanMultipleMonths(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	for _, tenantID := range []string{demoTenantID, honeywellTenantID} {
		n := mustCount(t, pool,
			`SELECT count(DISTINCT date_trunc('month', issue_date)) FROM invoices
			  WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%'`,
			tenantID,
		)
		if n < issueDateMonthSpan {
			t.Errorf("tenant %s: count(DISTINCT month(issue_date)) = %d, want >= %d", tenantID, n, issueDateMonthSpan)
		}
	}
}

// TestSeedIssueDatesAreDistinctWithinTenant: count(DISTINCT issue_date) must equal the
// tenant's own DEMO-2026-* invoice count -- any shared date means two rows still cluster on
// the same day.
func TestSeedIssueDatesAreDistinctWithinTenant(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	for _, tc := range []struct {
		tenantID string
		want     int
	}{
		{demoTenantID, demoInvoiceTotalCount},
		{honeywellTenantID, len(wantInHouseInvoiceOrder)},
	} {
		total := mustCount(t, pool,
			`SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%'`,
			tc.tenantID,
		)
		if total != tc.want {
			t.Fatalf("tenant %s: count(DEMO-2026-* invoices) = %d, want %d", tc.tenantID, total, tc.want)
		}
		distinct := mustCount(t, pool,
			`SELECT count(DISTINCT issue_date) FROM invoices WHERE tenant_id = $1 AND invoice_number LIKE 'DEMO-2026-%'`,
			tc.tenantID,
		)
		if distinct != tc.want {
			t.Errorf("tenant %s: count(DISTINCT issue_date) = %d, want %d -- two invoices still share a date", tc.tenantID, distinct, tc.want)
		}
	}
}

// TestSeedIssueDatesArePastRelativeToCreatedAt: created_at is derived as now() minus a small
// per-row offset (db/seed.dev.sql's row_number()-based anchor), so it sits at ~seed-run time.
// Every committed 2026 H1 date already precedes any real seed run, so this is GREEN BY DESIGN
// today -- it exists as a regression guard against a future-dated issue_date (or an anchor
// that stops trailing the dates it anchors), a property no other test in this file checks.
func TestSeedIssueDatesArePastRelativeToCreatedAt(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT tenant_id, invoice_number, issue_date, created_at FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query issue_date/created_at: %v", err)
	}
	defer rows.Close()

	var checked int
	for rows.Next() {
		var tenantID, invoiceNumber string
		var issueDate, createdAt time.Time
		if err := rows.Scan(&tenantID, &invoiceNumber, &issueDate, &createdAt); err != nil {
			t.Fatalf("scan issue_date/created_at row: %v", err)
		}
		checked++
		if !issueDate.Before(createdAt) {
			t.Errorf("tenant %s: %s issue_date = %v, want strictly before its own created_at anchor %v", tenantID, invoiceNumber, issueDate, createdAt)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate issue_date/created_at rows: %v", err)
	}
	wantChecked := demoInvoiceTotalCount + len(wantInHouseInvoiceOrder)
	if checked != wantChecked {
		t.Fatalf("checked %d rows, want %d", checked, wantChecked)
	}
}

// TestSeedIssueDatesAreWithinDeclared2026H1Window: the span/distinctness checks above tolerate
// a stray out-of-range date (a 2025/2027 typo, or a month past June) as long as the month-count
// and distinctness they check still hold -- this pins the literal year/month bound AC-1/AC-5
// assume.
func TestSeedIssueDatesAreWithinDeclared2026H1Window(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetBothDemoTenants(t, pool)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT invoice_number, issue_date FROM invoices
		  WHERE tenant_id = ANY($1) AND invoice_number LIKE 'DEMO-2026-%'`,
		[]string{demoTenantID, honeywellTenantID},
	)
	if err != nil {
		t.Fatalf("query issue_date: %v", err)
	}
	defer rows.Close()

	var checked int
	for rows.Next() {
		var invoiceNumber string
		var issueDate time.Time
		if err := rows.Scan(&invoiceNumber, &issueDate); err != nil {
			t.Fatalf("scan issue_date row: %v", err)
		}
		checked++
		if issueDate.Year() != 2026 || issueDate.Month() < time.January || issueDate.Month() > time.June {
			t.Errorf("%s: issue_date = %v, want within 2026-01 .. 2026-06", invoiceNumber, issueDate)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate issue_date rows: %v", err)
	}
	wantChecked := demoInvoiceTotalCount + len(wantInHouseInvoiceOrder)
	if checked != wantChecked {
		t.Fatalf("checked %d rows, want %d", checked, wantChecked)
	}
}

// demoTrimmedActiveTINs / demoTrimmedArchivedTINs are the 10 TINs a future
// trim of the demo firm's business_entities block keeps. Sourced from
// curatedDemoEntities by TIN rather than retyped, so a name/sector edit
// there can't silently drift out of sync with this list.
var (
	demoTrimmedActiveTINs = []string{
		"10012345-0001", "10023456-0002", "10034567-0003", "10045678-0004",
		"10056789-0005", "10067890-0006", "10078901-0007", "10089012-0008",
	}
	demoTrimmedArchivedTINs = []string{"10223456-0022", "10234567-0023"}
	demoHistoryBearingTINs  = demoTrimmedActiveTINs[:6]
)

// demoEntitiesByTINs looks up each tin in curatedDemoEntities, preserving
// input order.
func demoEntitiesByTINs(t *testing.T, tins []string) []entityRow {
	t.Helper()
	out := make([]entityRow, 0, len(tins))
	for _, tin := range tins {
		found := false
		for _, r := range curatedDemoEntities {
			if r.tin == tin {
				out = append(out, r)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no curatedDemoEntities row for tin %q", tin)
		}
	}
	return out
}

// TestSeedDemoFirmEntitiesTrimToTenEightActiveTwoArchived pins the target
// shape the demo firm's business_entities block converges to: 10 rows, 8
// active (6 history-bearing + Ifeoma + Bello), 2 archived (Olumide,
// Halima). Independent of curatedDemoEntities, which still lists all 27.
// FAILS today — the seed still upserts 27 rows.
func TestSeedDemoFirmEntitiesTrimToTenEightActiveTwoArchived(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	got := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(got) != 10 {
		t.Fatalf("count(business_entities) for the demo tenant after Seed = %d, want 10", len(got))
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
	if active != 8 {
		t.Errorf("count(active) = %d, want 8", active)
	}
	if archived != 2 {
		t.Errorf("count(archived) = %d, want 2", archived)
	}

	want := sortedEntityRows(append(
		demoEntitiesByTINs(t, demoTrimmedActiveTINs),
		demoEntitiesByTINs(t, demoTrimmedArchivedTINs)...,
	))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("business_entities for the demo tenant after Seed does not match the trimmed 10-row target\ngot:  %+v\nwant: %+v", got, want)
	}
}

// TestSeedHistoryBearingEntitiesSurviveWithInvoices: the six invoice-history
// entities must survive any future trim and keep their invoices — the
// seed's invoice CTE JOINs business_entities, so a dropped or mistyped TIN
// would silently zero out that entity's rows rather than fail loudly.
// Green today: nothing has been removed yet, so this only pins that a trim
// must not touch these six.
func TestSeedHistoryBearingEntitiesSurviveWithInvoices(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT e.tin, e.status, count(i.id)
		   FROM business_entities e
		   LEFT JOIN invoices i ON i.entity_id = e.id
		  WHERE e.tenant_id = $1 AND e.tin = ANY($2)
		  GROUP BY e.tin, e.status`,
		demoTenantID, demoHistoryBearingTINs,
	)
	if err != nil {
		t.Fatalf("query history-bearing entities: %v", err)
	}
	defer rows.Close()

	seen := make(map[string]int, len(demoHistoryBearingTINs))
	for rows.Next() {
		var tin, status string
		var n int
		if err := rows.Scan(&tin, &status, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[tin] = n
		if status != "active" {
			t.Errorf("%s: status = %q, want active", tin, status)
		}
		if n == 0 {
			t.Errorf("%s: count(invoices) = 0, want > 0 — this entity is supposed to carry the demo's invoice history", tin)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	for _, tin := range demoHistoryBearingTINs {
		if _, ok := seen[tin]; !ok {
			t.Errorf("%s: not found in business_entities for the demo tenant, want present", tin)
		}
	}
}

// TestSeedDemoPortfolioHasAnEmptyActiveAndAnArchivedClient: the empty-state
// and the archived filter both need live data behind them — at least one
// active entity with zero invoices, at least one archived entity. Green
// today: the current portfolio already has both; this must keep holding
// after any trim.
func TestSeedDemoPortfolioHasAnEmptyActiveAndAnArchivedClient(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	activeEmpty := mustCount(t, pool,
		`SELECT count(*) FROM business_entities e
		  WHERE e.tenant_id = $1 AND e.status = 'active'
		    AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.entity_id = e.id)`,
		demoTenantID,
	)
	if activeEmpty == 0 {
		t.Error("count(active entities with zero invoices) = 0, want >= 1 — the \"no invoices yet\" empty state has nothing to render otherwise")
	}

	archived := mustCount(t, pool,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND status = 'archived'`,
		demoTenantID,
	)
	if archived == 0 {
		t.Error("count(archived entities) = 0, want >= 1 — the archived filter has nothing to render otherwise")
	}
}

// TestSeedLeavesNoDanglingEntityReferences: no invoice / line_item /
// invoice_status_history / submission_job / app_exchange row may reference
// a missing entity. Green-by-design: every path is FK-enforced
// (invoices.entity_id ... ON DELETE RESTRICT; the rest chain through
// invoice_id). Kept explicit rather than left implicit across five
// migration files.
func TestSeedLeavesNoDanglingEntityReferences(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	resetDemoBusinessEntities(t, pool)

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	orphanInvoices := mustCount(t, pool,
		`SELECT count(*) FROM invoices i
		  WHERE i.tenant_id = $1
		    AND NOT EXISTS (SELECT 1 FROM business_entities e WHERE e.id = i.entity_id)`,
		demoTenantID,
	)
	if orphanInvoices != 0 {
		t.Errorf("count(invoices referencing a missing entity) = %d, want 0", orphanInvoices)
	}

	orphanLineItems := mustCount(t, pool,
		`SELECT count(*) FROM line_items li
		  WHERE li.tenant_id = $1
		    AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.id = li.invoice_id)`,
		demoTenantID,
	)
	if orphanLineItems != 0 {
		t.Errorf("count(line_items referencing a missing invoice) = %d, want 0", orphanLineItems)
	}

	orphanHistory := mustCount(t, pool,
		`SELECT count(*) FROM invoice_status_history h
		  WHERE h.tenant_id = $1
		    AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.id = h.invoice_id)`,
		demoTenantID,
	)
	if orphanHistory != 0 {
		t.Errorf("count(invoice_status_history referencing a missing invoice) = %d, want 0", orphanHistory)
	}

	orphanJobs := mustCount(t, pool,
		`SELECT count(*) FROM submission_jobs j
		  WHERE j.tenant_id = $1
		    AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.id = j.invoice_id)`,
		demoTenantID,
	)
	if orphanJobs != 0 {
		t.Errorf("count(submission_jobs referencing a missing invoice) = %d, want 0", orphanJobs)
	}

	orphanExchange := mustCount(t, pool,
		`SELECT count(*) FROM app_exchange x
		  WHERE x.tenant_id = $1
		    AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.id = x.invoice_id)`,
		demoTenantID,
	)
	if orphanExchange != 0 {
		t.Errorf("count(app_exchange referencing a missing invoice) = %d, want 0", orphanExchange)
	}
}
