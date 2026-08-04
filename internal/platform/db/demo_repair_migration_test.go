// Test-first (RED) suite for the demo-portfolio repair migration: the two
// corrections db/seed.dev.sql structurally cannot make on an already-deployed
// demo environment — deleting the 17 withdrawn business_entities, and unlinking
// the stale seeded source documents.
//
// Every DB-backed case here executes the migration's own Up/Down body — read out
// of migrations.FS — on a connection opened as invoice_migrator (NOBYPASSRLS,
// bound by both tables' FORCE'd tenant_isolation policy) inside one explicit
// transaction, with NO app.current_tenant pre-set. The migration's own
// set_config calls are the thing under test; a superuser-executed body would
// pass every case below vacuously, and a body missing those calls matches zero
// rows while goose still records "MIGRATE OK".
//
// The body is NOT driven through db.MigrateUp: goose applies only what is
// pending, and every path that reaches this package (`make dev-db`, the CI
// migrations job) runs `migrate-up` first — so by the time a test body runs the
// migration is already applied and MigrateUp is a total no-op.
//
// Fixtures are set up and read back on the superuser pool (BYPASSRLS), and the
// suite removes exactly the rows it created — so it leaves an empty CI database
// empty and a seeded dev database seeded.
package db_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/migrations"
)

// withdrawnDemoEntities is the 17 rows db/seed.dev.sql stopped declaring (13
// active + 4 archived). Already-deployed demo environments still carry them;
// removing them is what this migration is for. Verbatim from the seed as it
// stood before the trim.
var withdrawnDemoEntities = []entityRow{
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
	{name: "Chinwe Poultry Farms Ltd", tin: "10245678-0024", sector: "Agriculture", status: "archived"},
	{name: "Musa Hardware Stores Ltd", tin: "10256789-0025", sector: "Retail", status: "archived"},
	{name: "Bisi Event Planners Ltd", tin: "10267890-0026", sector: "Events", status: "archived"},
	{name: "Ekene Auto Parts Ltd", tin: "10278901-0027", sector: "Automotive", status: "archived"},
}

func withdrawnDemoTINs() []string {
	out := make([]string, 0, len(withdrawnDemoEntities))
	for _, e := range withdrawnDemoEntities {
		out = append(out, e.tin)
	}
	sort.Strings(out)
	return out
}

// ---- the migration file itself -------------------------------------------

const demoRepairMigrationGlob = "*_demo_portfolio_repair.sql"

// demoRepairMigrationFile returns the migration's embedded name and raw text.
// Reading it out of migrations.FS (not off disk) is what proves the go:embed the
// gateway ships carries the same body these tests exercise.
func demoRepairMigrationFile(t *testing.T) (string, string) {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, demoRepairMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", demoRepairMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1", len(matches), demoRepairMigrationGlob, matches)
	}
	raw, err := fs.ReadFile(migrations.FS, matches[0])
	if err != nil {
		t.Fatalf("read %s from migrations.FS: %v", matches[0], err)
	}
	return matches[0], string(raw)
}

const (
	gooseUp   = "-- +goose Up"
	gooseDown = "-- +goose Down"
)

// demoRepairStatements returns one goose section ("Up" or "Down") split into
// individual statements, comments stripped. The splitter is deliberately naive
// (line comments + top-level semicolons) and fails loudly rather than
// mis-splitting if the migration ever grows a function body.
func demoRepairStatements(t *testing.T, section string) []string {
	t.Helper()
	name, raw := demoRepairMigrationFile(t)

	up := strings.Index(raw, gooseUp)
	down := strings.Index(raw, gooseDown)
	if up < 0 || down < 0 || down < up {
		t.Fatalf("%s: want both %q and %q, in that order (up=%d down=%d)", name, gooseUp, gooseDown, up, down)
	}

	var body string
	switch section {
	case "Up":
		body = raw[up+len(gooseUp) : down]
	case "Down":
		body = raw[down+len(gooseDown):]
	default:
		t.Fatalf("unknown goose section %q", section)
	}

	if strings.Contains(body, "$$") || strings.Contains(body, "+goose StatementBegin") {
		t.Fatalf("%s %s: contains a function body — this suite's statement splitter would mis-split it", name, section)
	}

	var stripped []string
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		stripped = append(stripped, line)
	}

	var out []string
	for _, stmt := range strings.Split(strings.Join(stripped, "\n"), ";") {
		if s := strings.TrimSpace(stmt); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var (
	dmlLeadRE       = regexp.MustCompile(`(?is)^\s*(DELETE|UPDATE|INSERT)\b`)
	setTenantRE     = regexp.MustCompile(`(?is)set_config\s*\(\s*'app\.current_tenant'\s*,\s*'([0-9a-fA-F-]{36})'`)
	demoTenantUUIDs = map[string]bool{demoTenantID: true, honeywellTenantID: true}
)

func dmlVerb(stmt string) string {
	m := dmlLeadRE.FindStringSubmatch(stmt)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// countVerbs tallies the DML statements in a section by leading keyword.
func countVerbs(stmts []string) map[string]int {
	out := map[string]int{}
	for _, s := range stmts {
		if v := dmlVerb(s); v != "" {
			out[v]++
		}
	}
	return out
}

// ---- executing the body as the migrator ----------------------------------

// migratorTx opens a fresh invoice_migrator connection — NOBYPASSRLS, and with
// no app.current_tenant set — and begins one explicit transaction. Nothing here
// sets the GUC: the migration's own set_config calls are what is under test.
// The transaction is rolled back on cleanup, so a caller that never commits
// leaves nothing behind.
func migratorTx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	_, migDSN := requireProvisionDSNs(t)
	conn, err := pgx.Connect(ctx, migDSN)
	if err != nil {
		t.Fatalf("connect as invoice_migrator: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator transaction: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(ctx) // no-op once committed
		_ = conn.Close(ctx)
	})
	return tx
}

// execStatements runs stmts in order and returns the rows the DML statements
// touched. `SELECT set_config(...)` reports one row and is deliberately not
// counted — otherwise "the migration mutated something" could never be told
// apart from "the migration only set GUCs".
func execStatements(t *testing.T, ctx context.Context, tx pgx.Tx, stmts []string) int64 {
	t.Helper()
	var affected int64
	for i, stmt := range stmts {
		tag, err := tx.Exec(ctx, stmt)
		if err != nil {
			t.Fatalf("statement %d failed: %v\n%s", i+1, err, stmt)
		}
		if dmlVerb(stmt) != "" {
			affected += tag.RowsAffected()
		}
	}
	return affected
}

// applyDemoRepairUp executes the Up body as invoice_migrator with no pre-set
// tenant context, commits it, and returns the rows its DML statements touched.
func applyDemoRepairUp(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	tx := migratorTx(t, ctx)
	affected := execStatements(t, ctx, tx, demoRepairStatements(t, "Up"))
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the Up body: %v", err)
	}
	return affected
}

// ---- fixtures -------------------------------------------------------------

func randomSuffix(t *testing.T) string {
	t.Helper()
	return uuid.NewString()[:8]
}

// ensureTenant creates the tenant if it is absent, and in that case removes it
// again on cleanup — so this suite restores an empty database to empty without
// disturbing a seeded one.
func ensureTenant(t *testing.T, pool *pgxpool.Pool, id, name, kind string) {
	t.Helper()
	ctx := context.Background()
	tag, err := pool.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		id, name, kind)
	if err != nil {
		t.Fatalf("ensure tenant %s: %v", id, err)
	}
	if tag.RowsAffected() == 0 {
		return
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup tenant %s: %v", id, err)
		}
	})
}

// ensureEntity restores the row's pre-test presence on cleanup in BOTH
// directions: one it created is deleted again, and one that pre-existed is put
// back if the body under test removed it. The second half matters because these
// cases commit — a wrong DELETE would otherwise strip curated clients out of a
// shared dev database permanently.
func ensureEntity(t *testing.T, pool *pgxpool.Pool, tenantID string, e entityRow) {
	t.Helper()
	ctx := context.Background()
	const upsert = `INSERT INTO business_entities (tenant_id, name, tin, sector, status) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (tenant_id, tin) WHERE tin IS NOT NULL DO NOTHING`
	tag, err := pool.Exec(ctx, upsert, tenantID, e.name, e.tin, e.sector, e.status)
	if err != nil {
		t.Fatalf("ensure entity %s/%s: %v", tenantID, e.tin, err)
	}
	created := tag.RowsAffected() == 1
	t.Cleanup(func() {
		cctx := context.Background()
		if created {
			if _, err := pool.Exec(cctx,
				`DELETE FROM business_entities WHERE tenant_id = $1 AND tin = $2`, tenantID, e.tin); err != nil {
				t.Errorf("cleanup entity %s/%s: %v", tenantID, e.tin, err)
			}
			return
		}
		if _, err := pool.Exec(cctx, upsert, tenantID, e.name, e.tin, e.sector, e.status); err != nil {
			t.Errorf("restore pre-existing entity %s/%s: %v", tenantID, e.tin, err)
		}
	})
}

// seedLegacyDemoPortfolio brings the two demo tenants up to the 27-entity shape
// an already-deployed demo environment still carries.
func seedLegacyDemoPortfolio(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ensureTenant(t, pool, demoTenantID, "Okafor & Partners", "firm")
	ensureTenant(t, pool, honeywellTenantID, "Honeywell Group", "in_house")
	for _, e := range curatedDemoEntities {
		ensureEntity(t, pool, demoTenantID, e)
	}
	ensureEntity(t, pool, honeywellTenantID, curatedHoneywellEntity)
	for _, e := range withdrawnDemoEntities {
		ensureEntity(t, pool, demoTenantID, e)
	}
	if n := mustCount(t, pool,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1`, demoTenantID); n != 27 {
		t.Fatalf("precondition: demo firm holds %d business_entities, want the legacy 27 (10 curated + 17 withdrawn) — the database carries rows this fixture did not create", n)
	}
}

func entityIDByTIN(t *testing.T, pool *pgxpool.Pool, tenantID, tin string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM business_entities WHERE tenant_id = $1 AND tin = $2`, tenantID, tin).Scan(&id); err != nil {
		t.Fatalf("look up entity %s/%s: %v", tenantID, tin, err)
	}
	return id
}

func seedRepairInvoice(t *testing.T, pool *pgxpool.Pool, tenantID, entityID, number string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, entityID, number).Scan(&id); err != nil {
		t.Fatalf("seed invoice %s: %v", number, err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup invoice %s: %v", number, err)
		}
	})
	return id
}

func seedRepairDocument(t *testing.T, pool *pgxpool.Pool, tenantID string) string {
	t.Helper()
	var hash [32]byte
	if _, err := rand.Read(hash[:]); err != nil {
		t.Fatalf("random content hash: %v", err)
	}
	digest := hex.EncodeToString(hash[:])
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, filename)
		 VALUES ($1, $2, $3, 1, 'demo-repair-fixture.xlsx') RETURNING id`,
		tenantID, "qa/demo-repair/"+digest, digest).Scan(&id); err != nil {
		t.Fatalf("seed document for tenant %s: %v", tenantID, err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM documents WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup document %s: %v", id, err)
		}
	})
	return id
}

// linkInvoiceToDocument reproduces what the demo-document seeder writes: a
// source document AND the sheet rows that became the invoice.
func linkInvoiceToDocument(t *testing.T, pool *pgxpool.Pool, invoiceID, documentID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE invoices SET source_document_id = $1, source_rows = '{2,3}' WHERE id = $2`,
		documentID, invoiceID); err != nil {
		t.Fatalf("link invoice %s to document %s: %v", invoiceID, documentID, err)
	}
}

func seedRepairImportBatch(t *testing.T, pool *pgxpool.Pool, tenantID, entityID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO import_batches (tenant_id, entity_id, status) VALUES ($1, $2, 'completed') RETURNING id`,
		tenantID, entityID).Scan(&id); err != nil {
		t.Fatalf("seed import batch: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM import_batches WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup import batch %s: %v", id, err)
		}
	})
	return id
}

// remainingWithdrawnTINs is which of the 17 the demo firm still holds, sorted.
func remainingWithdrawnTINs(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT tin FROM business_entities WHERE tenant_id = $1 AND tin = ANY($2) ORDER BY tin`,
		demoTenantID, withdrawnDemoTINs())
	if err != nil {
		t.Fatalf("query surviving withdrawn entities: %v", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var tin string
		if err := rows.Scan(&tin); err != nil {
			t.Fatalf("scan surviving withdrawn tin: %v", err)
		}
		out = append(out, tin)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate surviving withdrawn entities: %v", err)
	}
	return out
}

// documentLink is an invoice's source-document state.
type documentLink struct {
	documentID string
	sourceRows []int32
}

func fetchDocumentLink(t *testing.T, pool *pgxpool.Pool, invoiceID string) documentLink {
	t.Helper()
	var link documentLink
	var docID *string
	if err := pool.QueryRow(context.Background(),
		`SELECT source_document_id, source_rows FROM invoices WHERE id = $1`, invoiceID).
		Scan(&docID, &link.sourceRows); err != nil {
		t.Fatalf("read source-document link for invoice %s: %v", invoiceID, err)
	}
	if docID != nil {
		link.documentID = *docID
	}
	return link
}

// ---- Test Specs -----------------------------------------------------------

// The direct regression test for the blocker: migrations run as
// invoice_migrator against FORCE'd RLS tables, so a body without its own
// set_config calls matches zero rows while goose still reports MIGRATE OK.
// A suite that only asserted "nothing unexpected changed" would pass against
// exactly that bug.
func TestDemoRepairMigrationMutatesRowsWithoutPreSetTenantContext(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	seedLegacyDemoPortfolio(t, pool)

	entity := entityIDByTIN(t, pool, demoTenantID, "10012345-0001")
	doc := seedRepairDocument(t, pool, demoTenantID)
	invoice := seedRepairInvoice(t, pool, demoTenantID, entity, "DEMO-2026-GUC-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, invoice, doc)

	affected := applyDemoRepairUp(t)

	if affected == 0 {
		t.Fatalf("the Up body touched 0 rows as invoice_migrator with no app.current_tenant pre-set: every statement matched nothing under FORCE'd row-level security, which is the silent no-op the migration's own set_config calls exist to prevent")
	}
	if got := remainingWithdrawnTINs(t, pool); len(got) != 0 {
		t.Errorf("withdrawn entities still present after the repair: %v, want none", got)
	}
}

// A blanket "delete every unreferenced client" would take 21 of the 27 and leave
// 6 — the exact 10 curated survivors are what an explicit-TIN-list selector
// produces.
func TestDemoRepairMigrationLeavesExactlyTheTenCuratedClients(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	seedLegacyDemoPortfolio(t, pool)

	applyDemoRepairUp(t)

	got := fetchDemoBusinessEntities(t, pool, demoTenantID)
	if len(got) != 10 {
		t.Fatalf("demo firm holds %d business_entities after the repair, want exactly 10 (27 before; a blanket unreferenced-delete would leave 6)", len(got))
	}
	if want := sortedEntityRows(curatedDemoEntities); !reflect.DeepEqual(got, want) {
		t.Fatalf("surviving portfolio is not the curated set\ngot:  %+v\nwant: %+v", got, want)
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
	if active != 8 || archived != 2 {
		t.Errorf("surviving split = %d active / %d archived, want 8 / 2", active, archived)
	}

	// Both carry zero invoices, so an unreferenced-delete removes them.
	for _, tin := range []string{"10078901-0007", "10223456-0022"} {
		if n := mustCount(t, pool,
			`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = $2`, demoTenantID, tin); n != 1 {
			t.Errorf("curated survivor %s: found %d rows, want 1", tin, n)
		}
	}
}

// invoices.entity_id is ON DELETE RESTRICT: an unguarded DELETE raises 23001 and
// crash-loops the gateway at boot. The second half — the other 16 still removed
// — is what stops a no-op migration from passing this.
func TestDemoRepairMigrationSkipsAReferencedClient(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	seedLegacyDemoPortfolio(t, pool)

	const referencedTIN = "10090123-0009"
	entity := entityIDByTIN(t, pool, demoTenantID, referencedTIN)
	number := "QA-REPAIR-REF-" + randomSuffix(t)
	invoice := seedRepairInvoice(t, pool, demoTenantID, entity, number)

	applyDemoRepairUp(t)

	if n := mustCount(t, pool,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = $2`, demoTenantID, referencedTIN); n != 1 {
		t.Errorf("referenced client %s: found %d rows after the repair, want it left in place", referencedTIN, n)
	}
	if n := mustCount(t, pool,
		`SELECT count(*) FROM invoices WHERE id = $1 AND entity_id = $2 AND invoice_number = $3`,
		invoice, entity, number); n != 1 {
		t.Errorf("the referenced client's invoice: found %d matching rows, want it untouched", n)
	}
	if got, want := remainingWithdrawnTINs(t, pool), []string{referencedTIN}; !reflect.DeepEqual(got, want) {
		t.Errorf("surviving withdrawn entities = %v, want only the referenced one %v", got, want)
	}
}

// import_batches.entity_id is ON DELETE CASCADE, so an unguarded DELETE destroys
// the batch silently — no error to notice. Assert the batch itself, not just
// that the statement succeeded.
func TestDemoRepairMigrationSkipsAClientWithAnImportBatch(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	seedLegacyDemoPortfolio(t, pool)

	const batchedTIN = "10101234-0010"
	entity := entityIDByTIN(t, pool, demoTenantID, batchedTIN)
	batch := seedRepairImportBatch(t, pool, demoTenantID, entity)

	applyDemoRepairUp(t)

	if n := mustCount(t, pool,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = $2`, demoTenantID, batchedTIN); n != 1 {
		t.Errorf("client with an import batch %s: found %d rows after the repair, want it left in place", batchedTIN, n)
	}
	if n := mustCount(t, pool,
		`SELECT count(*) FROM import_batches WHERE id = $1 AND entity_id = $2`, batch, entity); n != 1 {
		t.Errorf("the import batch: found %d rows, want 1 — an unguarded DELETE cascades it away without raising", n)
	}
	if got, want := remainingWithdrawnTINs(t, pool), []string{batchedTIN}; !reflect.DeepEqual(got, want) {
		t.Errorf("surviving withdrawn entities = %v, want only the batch-carrying one %v", got, want)
	}
}

// RLS admits one tenant per set_config, so a single `tenant_id IN (…)` statement
// reaches the first demo tenant only and silently misses the second's invoices.
func TestDemoRepairMigrationUnlinksStaleDemoSourceDocumentsInBothTenants(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	seedLegacyDemoPortfolio(t, pool)

	firmEntity := entityIDByTIN(t, pool, demoTenantID, "10012345-0001")
	firmDoc := seedRepairDocument(t, pool, demoTenantID)
	firmInvoice := seedRepairInvoice(t, pool, demoTenantID, firmEntity, "DEMO-2026-FIRM-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, firmInvoice, firmDoc)

	// Same tenant, same document, but not a demo invoice number: its link stays.
	keptInvoice := seedRepairInvoice(t, pool, demoTenantID, firmEntity, "QA-REPAIR-KEEP-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, keptInvoice, firmDoc)

	inHouseEntity := entityIDByTIN(t, pool, honeywellTenantID, curatedHoneywellEntity.tin)
	inHouseDoc := seedRepairDocument(t, pool, honeywellTenantID)
	inHouseInvoice := seedRepairInvoice(t, pool, honeywellTenantID, inHouseEntity, "DEMO-2026-INHOUSE-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, inHouseInvoice, inHouseDoc)

	applyDemoRepairUp(t)

	for _, tc := range []struct {
		label   string
		invoice string
	}{
		{"firm tenant", firmInvoice},
		{"in-house tenant", inHouseInvoice},
	} {
		link := fetchDocumentLink(t, pool, tc.invoice)
		if link.documentID != "" {
			t.Errorf("%s: demo invoice still points at document %s, want the link cleared", tc.label, link.documentID)
		}
		if link.sourceRows != nil {
			t.Errorf("%s: demo invoice still carries source_rows %v, want NULL", tc.label, link.sourceRows)
		}
	}

	if link := fetchDocumentLink(t, pool, keptInvoice); link.documentID != firmDoc {
		t.Errorf("non-demo invoice's document link = %q, want it left at %s", link.documentID, firmDoc)
	}
}

// Every mutating statement names a demo tenant, so no other tenant's identical
// rows are reachable. The demo-side control in the same run proves the migration
// actually ran — "nothing changed anywhere" is not a pass.
func TestDemoRepairMigrationLeavesNonDemoTenantsUntouched(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	seedLegacyDemoPortfolio(t, pool)

	demoEntity := entityIDByTIN(t, pool, demoTenantID, "10012345-0001")
	demoDoc := seedRepairDocument(t, pool, demoTenantID)
	demoInvoice := seedRepairInvoice(t, pool, demoTenantID, demoEntity, "DEMO-2026-CONTROL-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, demoInvoice, demoDoc)

	otherTenant := uuid.NewString()
	ensureTenant(t, pool, otherTenant, "Non-demo firm "+randomSuffix(t), "firm")
	otherRow := withdrawnDemoEntities[0]
	ensureEntity(t, pool, otherTenant, otherRow)
	otherEntity := entityIDByTIN(t, pool, otherTenant, otherRow.tin)
	otherDoc := seedRepairDocument(t, pool, otherTenant)
	otherInvoice := seedRepairInvoice(t, pool, otherTenant, otherEntity, "DEMO-2026-OTHER-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, otherInvoice, otherDoc)

	applyDemoRepairUp(t)

	if n := mustCount(t, pool,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = $2`, otherTenant, otherRow.tin); n != 1 {
		t.Errorf("non-demo tenant's entity on withdrawn TIN %s: found %d rows, want it untouched", otherRow.tin, n)
	}
	link := fetchDocumentLink(t, pool, otherInvoice)
	if link.documentID != otherDoc {
		t.Errorf("non-demo tenant's DEMO-2026-numbered invoice: document link = %q, want it untouched at %s", link.documentID, otherDoc)
	}
	if !reflect.DeepEqual(link.sourceRows, []int32{2, 3}) {
		t.Errorf("non-demo tenant's invoice source_rows = %v, want {2 3} untouched", link.sourceRows)
	}

	if got := remainingWithdrawnTINs(t, pool); len(got) != 0 {
		t.Errorf("control: withdrawn entities still present in the demo firm: %v, want none — the migration did not run", got)
	}
	if control := fetchDocumentLink(t, pool, demoInvoice); control.documentID != "" {
		t.Errorf("control: the demo tenant's own invoice is still linked to %s — the migration did not run", control.documentID)
	}
}

// Re-running the Up body must be a no-op for the right reason: the targets are
// already gone. Paired with the first application's own mutation count so
// "changed nothing twice" cannot pass.
func TestDemoRepairMigrationIsIdempotent(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	seedLegacyDemoPortfolio(t, pool)

	entity := entityIDByTIN(t, pool, demoTenantID, "10012345-0001")
	doc := seedRepairDocument(t, pool, demoTenantID)
	invoice := seedRepairInvoice(t, pool, demoTenantID, entity, "DEMO-2026-IDEM-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, invoice, doc)

	if first := applyDemoRepairUp(t); first == 0 {
		t.Fatalf("first application touched 0 rows — idempotency below would be vacuous")
	}

	entitiesAfterFirst := fetchDemoBusinessEntities(t, pool, demoTenantID)

	if second := applyDemoRepairUp(t); second != 0 {
		t.Errorf("second application touched %d rows, want 0 — the targets are already gone", second)
	}
	if got := fetchDemoBusinessEntities(t, pool, demoTenantID); !reflect.DeepEqual(got, entitiesAfterFirst) {
		t.Errorf("portfolio changed on the second application\ngot:  %+v\nwant: %+v", got, entitiesAfterFirst)
	}
	if link := fetchDocumentLink(t, pool, invoice); link.documentID != "" || link.sourceRows != nil {
		t.Errorf("invoice re-acquired a document link on the second application: %+v", link)
	}
}

// Static: no statement is reachable for a tenant that is not a demo tenant, and
// each is preceded by the set_config that makes it reachable at all.
func TestDemoRepairMigrationStatementsAreTenantScoped(t *testing.T) {
	for _, section := range []string{"Up", "Down"} {
		stmts := demoRepairStatements(t, section)
		verbs := countVerbs(stmts)

		switch section {
		case "Up":
			if verbs["DELETE"] < 1 {
				t.Errorf("Up: %d DELETE statements, want at least 1 (the 17 withdrawn clients)", verbs["DELETE"])
			}
			if verbs["UPDATE"] < 2 {
				t.Errorf("Up: %d UPDATE statements, want at least 2 — one per demo tenant, since RLS admits one tenant per set_config", verbs["UPDATE"])
			}
		case "Down":
			if verbs["INSERT"] < 1 {
				t.Errorf("Down: %d INSERT statements, want at least 1 (re-insert the 17)", verbs["INSERT"])
			}
		}

		current := ""
		for i, stmt := range stmts {
			if m := setTenantRE.FindStringSubmatch(stmt); m != nil {
				current = strings.ToLower(m[1])
				if !demoTenantUUIDs[current] {
					t.Errorf("%s statement %d sets app.current_tenant to %s, which is not a demo tenant", section, i+1, current)
				}
				continue
			}
			verb := dmlVerb(stmt)
			if verb == "" {
				continue
			}
			if current == "" {
				t.Errorf("%s statement %d (%s) runs with no app.current_tenant set — under FORCE'd RLS it matches zero rows:\n%s", section, i+1, verb, stmt)
				continue
			}
			if !strings.Contains(strings.ToLower(stmt), current) {
				t.Errorf("%s statement %d (%s) does not name tenant %s in its own predicate:\n%s", section, i+1, verb, current, stmt)
			}
		}
	}
}

// `-- +goose NO TRANSACTION` would make every set_config(..., true) evaporate at
// the end of its own statement, so the whole migration mutates nothing and still
// records as applied.
func TestDemoRepairMigrationRunsInATransaction(t *testing.T) {
	name, raw := demoRepairMigrationFile(t)
	if strings.Contains(raw, "+goose NO TRANSACTION") {
		t.Errorf("%s carries `-- +goose NO TRANSACTION`: each statement then autocommits, the transaction-local GUC is discarded, and every statement matches zero rows", name)
	}
	// Keeps the guard above from passing against a file with nothing to protect.
	for _, section := range []string{"Up", "Down"} {
		if len(countVerbs(demoRepairStatements(t, section))) == 0 {
			t.Errorf("%s %s: no DML statements", name, section)
		}
	}
}

// A Down that re-inserts the right NUMBER of rows but the wrong ones is a broken
// inverse, and the count assertion below cannot see it. withdrawnDemoEntities is
// transcribed from db/seed.dev.sql as it stood before the trim, so comparing
// against it is comparing against the seed the Down claims to restore.
func TestDemoRepairMigrationDownRestoresTheWithdrawnRowsVerbatim(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	ensureTenant(t, pool, demoTenantID, "Okafor & Partners", "firm")

	// ON CONFLICT DO NOTHING means a row already present wins, so a stale one
	// would be read back instead of the Down's own — say so rather than fail
	// with a confusing value mismatch.
	if n := mustCount(t, pool,
		`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = ANY($2)`,
		demoTenantID, withdrawnDemoTINs()); n != 0 {
		t.Fatalf("precondition: %d of the 17 withdrawn clients are already present, so ON CONFLICT DO NOTHING would mask what the Down inserts", n)
	}

	// Read back inside the migrator's own transaction: the Down's set_config is
	// what makes these rows selectable at all, and rolling back leaves nothing.
	tx := migratorTx(t, ctx)
	execStatements(t, ctx, tx, demoRepairStatements(t, "Down"))

	got := map[string]entityRow{}
	rows, err := tx.Query(ctx,
		`SELECT name, tin, sector, status FROM business_entities WHERE tenant_id = $1 AND tin = ANY($2)`,
		demoTenantID, withdrawnDemoTINs())
	if err != nil {
		t.Fatalf("read back the re-inserted rows: %v", err)
	}
	for rows.Next() {
		var r entityRow
		if err := rows.Scan(&r.name, &r.tin, &r.sector, &r.status); err != nil {
			t.Fatalf("scan re-inserted row: %v", err)
		}
		got[r.tin] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate re-inserted rows: %v", err)
	}
	rows.Close()

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("roll back the Down body: %v", err)
	}

	for _, want := range withdrawnDemoEntities {
		switch have, ok := got[want.tin]; {
		case !ok:
			t.Errorf("Down did not re-insert %s (%s)", want.tin, want.name)
		case have != want:
			t.Errorf("Down re-inserted %s with the wrong values\ngot:  %+v\nwant: %+v", want.tin, have, want)
		}
	}
	if len(got) != len(withdrawnDemoEntities) {
		t.Errorf("Down re-inserted %d rows, want %d", len(got), len(withdrawnDemoEntities))
	}
}

// demoRepairMigrationVersion is the migration's goose version id, taken from the
// filename rather than hardcoded.
func demoRepairMigrationVersion(t *testing.T) int64 {
	t.Helper()
	name, _ := demoRepairMigrationFile(t)
	v, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("parse goose version out of %q: %v", name, err)
	}
	return v
}

// Everything else here executes the migration's statements directly, which means
// nothing observes goose PARSING the file. `-- +goose NO TRANSACTION` is only
// caught by a string scan, and a string scan cannot tell that the marker's effect
// is "mutates nothing while recording MIGRATE OK" — the exact silent no-op this
// migration exists to avoid.
//
// So: clear this one migration's goose_db_version row and let provider.Up
// re-apply it the way a deploy does. Deleting the row rather than DownTo() keeps
// the blast radius to this migration alone — DownTo would also roll back any
// migration added after it.
func TestDemoRepairMigrationAppliesThroughGoose(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	seedLegacyDemoPortfolio(t, pool)

	firmEntity := entityIDByTIN(t, pool, demoTenantID, "10012345-0001")
	firmDoc := seedRepairDocument(t, pool, demoTenantID)
	firmInvoice := seedRepairInvoice(t, pool, demoTenantID, firmEntity, "DEMO-2026-GOOSE-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, firmInvoice, firmDoc)

	inHouseEntity := entityIDByTIN(t, pool, honeywellTenantID, curatedHoneywellEntity.tin)
	inHouseDoc := seedRepairDocument(t, pool, honeywellTenantID)
	inHouseInvoice := seedRepairInvoice(t, pool, honeywellTenantID, inHouseEntity, "DEMO-2026-GOOSE-IH-"+randomSuffix(t))
	linkInvoiceToDocument(t, pool, inHouseInvoice, inHouseDoc)

	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}

	version := demoRepairMigrationVersion(t)
	if _, err := pool.Exec(ctx, `DELETE FROM goose_db_version WHERE version_id = $1`, version); err != nil {
		t.Fatalf("mark migration %d unapplied: %v", version, err)
	}
	// Leave goose's ledger consistent even if the Up below fails.
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore goose state after the test: %v", err)
		}
	})

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-apply the repair migration through goose: %v", err)
	}

	if n := mustCount(t, pool,
		`SELECT count(*) FROM goose_db_version WHERE version_id = $1 AND is_applied`, version); n != 1 {
		t.Errorf("goose recorded %d applied rows for version %d, want 1", n, version)
	}
	if got := fetchDemoBusinessEntities(t, pool, demoTenantID); !reflect.DeepEqual(got, sortedEntityRows(curatedDemoEntities)) {
		t.Errorf("through goose the firm holds %d entities, want the curated 10 — a migration that records MIGRATE OK while mutating nothing looks exactly like this\ngot: %+v", len(got), got)
	}
	for _, tc := range []struct {
		label   string
		invoice string
	}{
		{"firm tenant", firmInvoice},
		{"in-house tenant", inHouseInvoice},
	} {
		if link := fetchDocumentLink(t, pool, tc.invoice); link.documentID != "" || link.sourceRows != nil {
			t.Errorf("%s: through goose the demo invoice is still linked (%+v), want both columns NULL", tc.label, link)
		}
	}
}

// The CI reversibility round-trip runs every Down against a database bootstrapped
// from empty, where the demo tenants do not exist.
func TestDemoRepairMigrationDownRunsCleanOnAnEmptyDatabase(t *testing.T) {
	superDSN, _ := requireProvisionDSNs(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()
	down := demoRepairStatements(t, "Down")

	// Positive control, against the real RLS-bound tables: with the demo tenant
	// present the Down re-inserts the 17. Rolled back — nothing persists.
	t.Run("tenant_present_reinserts_the_withdrawn_clients", func(t *testing.T) {
		ensureTenant(t, pool, demoTenantID, "Okafor & Partners", "firm")
		present := mustCount(t, pool,
			`SELECT count(*) FROM business_entities WHERE tenant_id = $1 AND tin = ANY($2)`,
			demoTenantID, withdrawnDemoTINs())

		tx := migratorTx(t, ctx)
		affected := execStatements(t, ctx, tx, down)
		if want := int64(17 - present); affected != want {
			t.Errorf("Down inserted %d rows, want %d (17 withdrawn clients minus the %d already present)", affected, want, present)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatalf("roll back the Down body: %v", err)
		}
	})

	// Temp tables shadow the real ones for the duration of the transaction, so
	// the Down body runs against a `tenants` that holds nothing — the same state
	// the CI reset -> up round-trip presents it with.
	t.Run("tenant_absent_inserts_nothing", func(t *testing.T) {
		tx := migratorTx(t, ctx)
		for _, table := range []string{"tenants", "business_entities"} {
			if _, err := tx.Exec(ctx,
				`CREATE TEMP TABLE `+table+` (LIKE public.`+table+` INCLUDING ALL) ON COMMIT DROP`); err != nil {
				t.Fatalf("shadow %s with an empty temp table: %v", table, err)
			}
		}
		if affected := execStatements(t, ctx, tx, down); affected != 0 {
			t.Errorf("Down inserted %d rows against a database with no demo tenant, want 0", affected)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatalf("roll back the Down body: %v", err)
		}
	})
}
