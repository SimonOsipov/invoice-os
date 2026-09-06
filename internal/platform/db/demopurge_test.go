// Suite for the demo-tenant purge primitive (demopurge.go).
//
// The four proof obligations that discharge the ratified condition ("delete, as
// long as you don't mess up with the real production data, only the demo") are
// TestPurgeAllowlist*, TestPurgeHasNoUnscopedDeleteStatement,
// TestPurgeLeavesANonDemoTenantUntouched +
// TestPurgeWitnessAssertionIsSensitiveToTheTenantFilter, and
// TestPurgeReplicaWindowHoldsExactlyOneStatement.
//
// Env-gated on DATABASE_SUPERUSER_URL via requireSuperuserDSN
// (bootstrap_test.go). No t.Parallel: every DB-backed case shares the demo
// tenants' rows.
package db_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// witnessTenantID is the throwaway non-demo tenant the survival obligation
// plants under. Deliberately unlike the four seeded ids so a failure message
// cannot be misread.
const witnessTenantID = "9d9d9d9d-9d9d-9d9d-9d9d-9d9d9d9d9d9d"

// tenantPredicate is the predicate every statement demopurge.go emits must
// carry, spelled exactly as the scan and the implementation share it.
const tenantPredicate = "WHERE tenant_id = ANY($1)"

// restrictViolation is what audit_log_append_only() raises, and what a purge
// running in the wrong table order raises against a RESTRICT foreign key.
const restrictViolation = "23001"

// lockNotAvailable is what lock_timeout raises when the purge cannot take a row
// lock a concurrent session is holding.
const lockNotAvailable = "55P03"

// ---- shared helpers --------------------------------------------------------

// execSetup runs one fixture statement; a failure here is broken setup, never a
// red spec, so it fails the test immediately and says so.
func execSetup(t *testing.T, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, sql string, args ...any) {
	t.Helper()
	if _, err := q.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("test setup: exec %q: %v", sql, err)
	}
}

// allPurgeableTables is purgeTables + purgeExcludedTables — every table that
// carries a tenant_id, whether the purge deletes from it or not.
func allPurgeableTables() []string {
	out := append([]string{}, db.PurgeTablesForTest...)
	return append(out, db.PurgeExcludedTablesForTest...)
}

// countFor counts rows of table owned by tenants. The predicate compares
// tenant_id::text so the test helper never depends on how the implementation
// binds its uuid[] parameter.
func countFor(t *testing.T, pool *pgxpool.Pool, table string, tenants []string) int {
	t.Helper()
	return mustCount(t, pool, `SELECT count(*) FROM `+table+` WHERE tenant_id::text = ANY($1)`, tenants)
}

// snapshotTable returns every row of table for tenantID as sorted JSON text —
// a whole-row value comparison that needs no per-table column list.
func snapshotTable(t *testing.T, pool *pgxpool.Pool, table, tenantID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT to_jsonb(x)::text FROM `+table+` x WHERE tenant_id::text = $1 ORDER BY 1`, tenantID)
	if err != nil {
		t.Fatalf("test setup: snapshot %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("test setup: scan %s snapshot: %v", table, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("test setup: snapshot %s: %v", table, err)
	}
	return out
}

// createWitnessTenant inserts the throwaway tenant and removes it, and anything
// planted under it, on cleanup.
func createWitnessTenant(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	execSetup(t, pool,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'Purge witness (test)', 'firm')
		 ON CONFLICT (id) DO NOTHING`, witnessTenantID)
	t.Cleanup(func() { dropTenantRows(t, pool, witnessTenantID) })
}

// dropTenantRows removes every tenant-owned row of tenantID and the tenant
// itself. One replica-scoped transaction: foreign-key enforcement and the
// append-only/content-lock triggers are off, so cleanup needs no delete order.
// Refuses to run against a demo tenant — cleanup must never do the purge's job.
func dropTenantRows(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	t.Helper()
	for _, demo := range db.DemoTenants {
		if demo == tenantID {
			t.Fatalf("dropTenantRows called with demo tenant %s — cleanup must not wipe seeded state", tenantID)
		}
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("cleanup: begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	execSetup(t, tx, `SET LOCAL session_replication_role = 'replica'`)
	for _, tbl := range allPurgeableTables() {
		execSetup(t, tx, `DELETE FROM `+tbl+` WHERE tenant_id::text = $1`, tenantID)
	}
	execSetup(t, tx, `DELETE FROM tenants WHERE id::text = $1`, tenantID)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("cleanup: commit: %v", err)
	}
}

// reseedOnCleanup restores db/seed.dev.sql's fixtures after a test that purged
// the demo tenants, so the rest of the package's run starts from the baseline.
func reseedOnCleanup(t *testing.T, superDSN string) {
	t.Helper()
	t.Cleanup(func() {
		if err := db.Seed(context.Background(), superDSN, dbsql.FS); err != nil {
			t.Fatalf("cleanup: reseed: %v", err)
		}
	})
}

// plantedRow is one fixture row and the statement that removes it again.
type plantedRow struct {
	table string
	col   string
	val   any
}

// plantWitnessRows inserts at least one row into EVERY purged table under
// tenantID, building the excluded foreign-key parents (memberships,
// approval_policies, approval_policy_versions) it needs. It asserts its own
// coverage, so adding a table to purgeTables without extending this helper
// fails here rather than leaving a survival assertion silently partial. Every
// planted row is removed by id on cleanup, so planting under a demo tenant
// leaves nothing behind.
func plantWitnessRows(t *testing.T, pool *pgxpool.Pool, tenantID string) map[string]int {
	t.Helper()
	ctx := context.Background()

	var undo []plantedRow
	t.Cleanup(func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("cleanup: begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		execSetup(t, tx, `SET LOCAL session_replication_role = 'replica'`)
		for i := len(undo) - 1; i >= 0; i-- {
			r := undo[i]
			execSetup(t, tx,
				`DELETE FROM `+r.table+` WHERE tenant_id::text = $1 AND `+r.col+`::text = $2`,
				tenantID, r.val)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("cleanup: commit: %v", err)
		}
	})

	planted := map[string]int{}
	// parent plants an excluded-table row the witness needs; it is undone but
	// never counted, since the purge must leave those tables alone.
	parent := func(table, col string, val any, sql string, args ...any) {
		execSetup(t, pool, sql, args...)
		undo = append(undo, plantedRow{table: table, col: col, val: val})
	}
	plant := func(table, col string, val any, sql string, args ...any) {
		parent(table, col, val, sql, args...)
		planted[table]++
	}

	userID := uuid.NewString()
	policyID := uuid.NewString()
	versionID := uuid.NewString()
	entityID := uuid.NewString()
	documentID := uuid.NewString()
	extractionJobID := uuid.NewString()
	fieldResultID := uuid.NewString()
	correctionID := uuid.NewString()
	pageImageID := uuid.NewString()
	anchorRuleID := uuid.NewString()
	batchID := uuid.NewString()
	invoiceID := uuid.NewString()
	jobID := uuid.NewString()
	exchangeID := uuid.NewString()
	runID := uuid.NewString()
	stepID := uuid.NewString()
	decisionID := uuid.NewString()
	roleID := uuid.NewString()
	roleMemberID := uuid.NewString()
	memberID := uuid.NewString()
	inviteID := uuid.NewString()
	lineID := uuid.NewString()
	historyID := uuid.NewString()
	idemKey := "purge-witness-" + uuid.NewString()
	auditEvent := "purge.witness." + uuid.NewString()[:8]
	contentHash := strings.Repeat("a", 64)

	parent("memberships", "id", memberID,
		`INSERT INTO memberships (id, tenant_id, user_id, role, status) VALUES ($1,$2,$3,'admin','active')`,
		memberID, tenantID, userID)
	parent("approval_policies", "id", policyID,
		`INSERT INTO approval_policies (id, tenant_id, name) VALUES ($1,$2,'Purge witness policy')`,
		policyID, tenantID)
	parent("approval_policy_versions", "id", versionID,
		`INSERT INTO approval_policy_versions (id, tenant_id, policy_id, version, sealed, is_active)
		 VALUES ($1,$2,$3,1,true,false)`,
		versionID, tenantID, policyID)

	plant("business_entities", "id", entityID,
		`INSERT INTO business_entities (id, tenant_id, name, tin, sector, status)
		 VALUES ($1,$2,'Purge Witness Ltd','90000000-0001','Testing','active')`,
		entityID, tenantID)
	plant("documents", "id", documentID,
		`INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes, filename, declared_content_type)
		 VALUES ($1,$2,'purge-witness/doc.csv',$3,64,'doc.csv','text/csv')`,
		documentID, tenantID, contentHash)
	plant("extraction_jobs", "id", extractionJobID,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1,$2,$3,'mock','v1')`,
		extractionJobID, tenantID, documentID)
	plant("extraction_field_results", "id", fieldResultID,
		`INSERT INTO extraction_field_results (id, tenant_id, extraction_job_id, field_name, value)
		 VALUES ($1,$2,$3,'total_amount','100.00')`,
		fieldResultID, tenantID, extractionJobID)
	plant("extraction_field_corrections", "id", correctionID,
		`INSERT INTO extraction_field_corrections
		     (id, tenant_id, extraction_job_id, field_name, value, method, actor)
		 VALUES ($1,$2,$3,'total_amount','212.50','typed',$4)`,
		correctionID, tenantID, extractionJobID, userID)
	plant("extraction_page_images", "id", pageImageID,
		`INSERT INTO extraction_page_images
		     (id, tenant_id, document_id, page_number, width_px, height_px, storage_key)
		 VALUES ($1,$2,$3,1,1275,1651,$4)`,
		pageImageID, tenantID, documentID, "tenants/"+tenantID+"/pages/purge-witness/v1/p0001.png")
	// Hangs off no document: the rule belongs to a computed layout fingerprint.
	plant("extraction_anchor_rules", "id", anchorRuleID,
		`INSERT INTO extraction_anchor_rules (id, tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
		 VALUES ($1,$2,$3,$4,$5::jsonb,1)`,
		anchorRuleID, tenantID, "v1:"+contentHash, earField, earRuleBody)
	plant("import_batches", "id", batchID,
		`INSERT INTO import_batches (id, tenant_id, entity_id, status, rows_total, rows_valid, rows_invalid, filename, document_id)
		 VALUES ($1,$2,$3,'completed',1,1,0,'doc.csv',$4)`,
		batchID, tenantID, entityID, documentID)
	plant("invoices", "id", invoiceID,
		`INSERT INTO invoices (id, tenant_id, entity_id, import_batch_id, invoice_number, status, currency, total)
		 VALUES ($1,$2,$3,$4,'PURGE-WITNESS-1','draft','NGN',100.00)`,
		invoiceID, tenantID, entityID, batchID)
	plant("line_items", "id", lineID,
		`INSERT INTO line_items (id, tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total)
		 VALUES ($1,$2,$3,1,'witness line',1.000,100.00,100.00)`,
		lineID, tenantID, invoiceID)
	plant("invoice_status_history", "id", historyID,
		`INSERT INTO invoice_status_history (id, tenant_id, invoice_id, from_status, to_status, actor)
		 VALUES ($1,$2,$3,NULL,'draft','purge-witness')`,
		historyID, tenantID, invoiceID)
	plant("submission_jobs", "id", jobID,
		`INSERT INTO submission_jobs (id, tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state)
		 VALUES ($1,$2,$3,$4,'mock','v1','accepted')`,
		jobID, tenantID, invoiceID, "purge-witness-job-"+jobID)
	plant("app_exchange", "id", exchangeID,
		`INSERT INTO app_exchange (id, tenant_id, submission_job_id, invoice_id, operation, outcome, attempt, adapter, adapter_version)
		 VALUES ($1,$2,$3,$4,'submit','sent',1,'mock','v1')`,
		exchangeID, tenantID, jobID, invoiceID)
	plant("approval_runs", "id", runID,
		`INSERT INTO approval_runs (id, tenant_id, invoice_id, policy_version_id, state, content_fingerprint)
		 VALUES ($1,$2,$3,$4,'open','purge-witness-fingerprint')`,
		runID, tenantID, invoiceID, versionID)
	plant("approval_run_steps", "id", stepID,
		`INSERT INTO approval_run_steps (id, tenant_id, run_id, ord, kind, state)
		 VALUES ($1,$2,$3,1,'approval','pending')`,
		stepID, tenantID, runID)
	plant("approval_decisions", "id", decisionID,
		`INSERT INTO approval_decisions (id, tenant_id, run_id, run_step_id, decision, actor)
		 VALUES ($1,$2,$3,$4,'approved','purge-witness')`,
		decisionID, tenantID, runID, stepID)
	plant("idempotency_keys", "key", idemKey,
		`INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1,$2)`,
		tenantID, idemKey)
	plant("submission_rate_limits", "tenant_id", tenantID,
		`INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1, 60)
		 ON CONFLICT (tenant_id) DO UPDATE SET max_per_minute = EXCLUDED.max_per_minute`,
		tenantID)
	plant("invitations", "id", inviteID,
		`INSERT INTO invitations (id, tenant_id, role, invitee_email, status)
		 VALUES ($1,$2,'admin','purge-witness@example.test','pending')`,
		inviteID, tenantID)
	plant("workflow_roles", "id", roleID,
		`INSERT INTO workflow_roles (id, tenant_id, key, title) VALUES ($1,$2,$3,'Purge Witness Role')`,
		roleID, tenantID, "purge_witness_"+roleID[:8])
	plant("workflow_role_members", "id", roleMemberID,
		`INSERT INTO workflow_role_members (id, tenant_id, workflow_role_id, user_id, ord)
		 VALUES ($1,$2,$3,$4,1)`,
		roleMemberID, tenantID, roleID, userID)
	plant("audit_log", "event", auditEvent,
		`INSERT INTO audit_log (tenant_id, actor, event, payload) VALUES ($1,'purge-witness',$2,'{}'::jsonb)`,
		tenantID, auditEvent)

	var missing []string
	for _, tbl := range db.PurgeTablesForTest {
		if planted[tbl] == 0 {
			missing = append(missing, tbl)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("plantWitnessRows planted no row in %v — a survival/removal assertion over those tables would be vacuous; extend this helper alongside purgeTables", missing)
	}
	if len(planted) != len(db.PurgeTablesForTest) {
		t.Fatalf("plantWitnessRows planted into %d tables, want %d (purgeTables)", len(planted), len(db.PurgeTablesForTest))
	}
	return planted
}

// ---- Obligation 1: the allowlist cannot point at a real tenant -------------

// seedTenantUUIDPattern extracts the quoted uuids from seed.dev.sql's
// INSERT INTO tenants statement.
var seedTenantUUIDPattern = regexp.MustCompile(`'([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})'`)

// seedFileTenants returns the tenant ids db/seed.dev.sql inserts into tenants.
func seedFileTenants(t *testing.T) []string {
	t.Helper()
	b, err := fs.ReadFile(dbsql.FS, "seed.dev.sql")
	if err != nil {
		t.Fatalf("read embedded seed.dev.sql: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "INSERT INTO tenants")
	if start == -1 {
		t.Fatal("db/seed.dev.sql has no INSERT INTO tenants — this oracle reads nothing")
	}
	end := strings.Index(src[start:], ";")
	if end == -1 {
		t.Fatal("db/seed.dev.sql's INSERT INTO tenants is unterminated")
	}
	var ids []string
	for _, m := range seedTenantUUIDPattern.FindAllStringSubmatch(src[start:start+end], -1) {
		ids = append(ids, strings.ToLower(m[1]))
	}
	if len(ids) == 0 {
		t.Fatal("extracted 0 tenant uuids from db/seed.dev.sql — the extractor stopped matching, which reads exactly like an empty seed")
	}
	return ids
}

func sortedLower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	sort.Strings(out)
	return out
}

// TestPurgeAllowlistMatchesSeedFileTenants (obligation 1): DemoTenants is a
// literal, so nothing keeps it in step with db/seed.dev.sql except this test.
// It compares as SETS, which fails in both directions: a tenant added to the
// seed and not to the allowlist, and a tenant in the allowlist the seed never
// creates.
func TestPurgeAllowlistMatchesSeedFileTenants(t *testing.T) {
	seeded := sortedLower(seedFileTenants(t))
	allowed := sortedLower(db.DemoTenants)
	if !reflect.DeepEqual(seeded, allowed) {
		t.Fatalf("db.DemoTenants and db/seed.dev.sql's tenants disagree — the purge allowlist must be exactly the seeded set, and a divergence here means a deploy may delete a tenant nobody meant to seed, or skip one it did\nseed.dev.sql: %v\nDemoTenants:  %v", seeded, allowed)
	}
}

// TestPurgeAllowlistIsExactlyFourSeedTenants (obligation 1): pins the length
// independently, so widening the purge takes two separate literal edits.
func TestPurgeAllowlistIsExactlyFourSeedTenants(t *testing.T) {
	if len(db.DemoTenants) != 4 {
		t.Fatalf("len(db.DemoTenants) = %d, want 4 — widening the purge allowlist must require editing this pin as well as the list itself", len(db.DemoTenants))
	}
	seen := map[string]bool{}
	for _, id := range db.DemoTenants {
		if seen[strings.ToLower(id)] {
			t.Errorf("db.DemoTenants repeats %s — a duplicate hides a missing tenant behind a length that still reads as 4", id)
		}
		seen[strings.ToLower(id)] = true
	}
}

// TestSeedFileWarnsThatItsTenantsArePurgeable (AC-13): the comment block
// directly above INSERT INTO tenants must say a tenant added there is one a
// boot-time purge may delete. Comment only, so
// TestSeedFileHasNoDestructiveStatements (which strips -- first) cannot trip.
func TestSeedFileWarnsThatItsTenantsArePurgeable(t *testing.T) {
	b, err := fs.ReadFile(dbsql.FS, "seed.dev.sql")
	if err != nil {
		t.Fatalf("read embedded seed.dev.sql: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	insertAt := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "INSERT INTO tenants") {
			insertAt = i
			break
		}
	}
	if insertAt == -1 {
		t.Fatal("db/seed.dev.sql has no INSERT INTO tenants line")
	}
	var warned bool
	for i := insertAt - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "--") {
			break
		}
		if strings.Contains(strings.ToLower(trimmed), "purge") {
			warned = true
			break
		}
	}
	if !warned {
		t.Fatal("the comment block directly above db/seed.dev.sql's INSERT INTO tenants never mentions the purge — someone adding a tenant there has no way to know a deploy may delete its rows")
	}
}

// ---- Obligation 2: no unscoped statement, by construction ------------------

// deleteLiteralsIn returns every string literal in src that mentions DELETE
// FROM. It parses the AST rather than grepping so a mention inside a comment is
// never counted.
func deleteLiteralsIn(t *testing.T, filename, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if strings.Contains(strings.ToUpper(v), "DELETE FROM") {
			out = append(out, v)
		}
		return true
	})
	return out
}

// unscopedDeletes filters deleteLiteralsIn's output down to the literals that
// do not carry the tenant predicate.
func unscopedDeletes(lits []string) []string {
	var out []string
	for _, l := range lits {
		if !strings.Contains(l, tenantPredicate) {
			out = append(out, l)
		}
	}
	return out
}

// TestPurgeHasNoUnscopedDeleteStatement (obligation 2): every DELETE the
// package can emit must carry WHERE tenant_id = ANY($1) in the SAME string
// literal, so the tenant predicate cannot be dropped by editing one half of a
// concatenation. The control-needle subtest runs the same scanner over a
// fixture holding a planted unscoped delete: a scanner that quietly stops
// matching returns zero hits, and zero hits reads exactly like a clean file.
func TestPurgeHasNoUnscopedDeleteStatement(t *testing.T) {
	const filename = "demopurge.go"
	src, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	lits := deleteLiteralsIn(t, filename, string(src))
	if len(lits) == 0 {
		t.Errorf("%s holds no string literal containing DELETE FROM — the purge emits no statement at all, so scanning it proves nothing about scoping (obligation 2)", filename)
	}
	for _, l := range unscopedDeletes(lits) {
		t.Errorf("%s holds an unscoped DELETE literal %q — every statement the purge can emit must carry %q in the same literal", filename, l, tenantPredicate)
	}

	t.Run("control needle", func(t *testing.T) {
		const fixture = `package p

const unscoped = "DELETE FROM invoices;"
const scoped = "DELETE FROM invoices WHERE tenant_id = ANY($1)"
`
		lits := deleteLiteralsIn(t, "fixture.go", fixture)
		if len(lits) != 2 {
			t.Fatalf("scanner collected %d DELETE literals from the fixture, want 2 — it has stopped seeing statements it should see", len(lits))
		}
		got := unscopedDeletes(lits)
		want := []string{"DELETE FROM invoices;"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("scanner reported %v as unscoped, want %v — it cannot find a planted violation, so a clean report from it means nothing", got, want)
		}
	})
}

// TestPurgeKeepsDocumentsAndAuditLogTogether (AC-6): documents and audit_log
// are a coupled pair. internal/invoice/source_document.go reads the uploader
// from the document.created audit row and nowhere else, so purging documents
// alone leaves stale audit rows the (tenant_id, content_hash) dedupe then
// re-attributes to a re-uploaded file, permanently.
func TestPurgeKeepsDocumentsAndAuditLogTogether(t *testing.T) {
	var hasDocuments, hasAuditLog bool
	for _, tbl := range db.PurgeTablesForTest {
		switch tbl {
		case "documents":
			hasDocuments = true
		case "audit_log":
			hasAuditLog = true
		}
	}
	if hasDocuments != hasAuditLog {
		t.Fatalf("purgeTables has documents=%v audit_log=%v — they must be purged together or not at all: the previewer reads a document's uploader from its document.created audit row, so purging one and keeping the other permanently mis-attributes the next re-upload", hasDocuments, hasAuditLog)
	}
}

// ---- Anti-drift: the two lists together must cover the live schema ---------

// rlsFixtureTable is the RLS harness's transient table (rls_harness_test.go
// creates it in TestMain and drops it on teardown). It carries tenant_id but is
// no part of the schema, so it is no part of the purge's business.
const rlsFixtureTable = "rls_fixture"

// TestPurgeTableListCoversEveryTenantOwnedTable (AC-2): a table added by a
// future migration turns a silent leak into a red test. documents was missed by
// resetTables exactly this way once already.
func TestPurgeTableListCoversEveryTenantOwnedTable(t *testing.T) {
	pool := bootstrapSuperuserPool(t, requireSuperuserDSN(t))
	rows, err := pool.Query(context.Background(), `
		SELECT c.relname
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE a.attname = 'tenant_id'
		   AND a.attnum > 0 AND NOT a.attisdropped
		   AND c.relkind = 'r' AND n.nspname = 'public'`)
	if err != nil {
		t.Fatalf("read tenant_id-bearing tables from pg_attribute: %v", err)
	}
	defer rows.Close()
	var live []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan relname: %v", err)
		}
		if name == rlsFixtureTable {
			continue
		}
		live = append(live, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read tenant_id-bearing tables: %v", err)
	}
	if len(live) == 0 {
		t.Fatal("pg_attribute reported 0 tables carrying tenant_id — the oracle read nothing, so agreeing with it proves nothing")
	}

	declared := map[string]int{}
	for _, tbl := range allPurgeableTables() {
		declared[tbl]++
	}
	for tbl, n := range declared {
		if n > 1 {
			t.Errorf("%s appears %d times across purgeTables and purgeExcludedTables — a table is purged or excluded, never both", tbl, n)
		}
	}
	inLive := map[string]bool{}
	for _, tbl := range live {
		inLive[tbl] = true
	}
	for _, tbl := range live {
		if declared[tbl] == 0 {
			t.Errorf("table %s carries tenant_id but appears in neither purgeTables nor purgeExcludedTables — a demo tenant's rows there survive every purge, silently", tbl)
		}
	}
	for tbl := range declared {
		if !inLive[tbl] {
			t.Errorf("table %s is declared in purgeTables/purgeExcludedTables but carries no tenant_id in the live schema", tbl)
		}
	}
}

// ---- AC-1: the purge removes a demo tenant's rows everywhere ---------------

// TestPurgeCountsEveryPopulatedTableUnderItsOwnName pins the leaf-first order.
// purgeTables is walked in order and ByTable takes each statement's own
// RowsAffected, so a parent listed before its ON DELETE CASCADE child takes the
// child's rows silently and reports the child as having lost nothing. Every
// table still ends empty, which is why the removal test above cannot see it.
// TestPurgeResultOmitsTablesThatLostNothing asserts the same equality but only
// over tables db.Seed populates, and the seed writes no extraction row.
func TestPurgeCountsEveryPopulatedTableUnderItsOwnName(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	plantWitnessRows(t, pool, demoTenantID)

	before := map[string]int{}
	for _, tbl := range db.PurgeTablesForTest {
		before[tbl] = countFor(t, pool, tbl, db.DemoTenants)
		if before[tbl] == 0 {
			t.Fatalf("test setup: %s holds 0 demo-tenant rows before the purge — its count below would be vacuous", tbl)
		}
	}

	res, err := db.PurgeDemoTenants(ctx, superDSN)
	if err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}

	for _, tbl := range db.PurgeTablesForTest {
		if got := res.ByTable[tbl]; got != int64(before[tbl]) {
			t.Errorf("ByTable[%s] = %d, want %d — %s is listed after a table whose ON DELETE CASCADE "+
				"already took its rows, so its own DELETE matched nothing", tbl, got, before[tbl], tbl)
		}
	}
}

// TestPurgeRemovesEveryTenantOwnedRowForADemoTenant (AC-1): a row is planted in
// every purged table first, so a table the purge forgets cannot hide behind a
// count that was already zero.
func TestPurgeRemovesEveryTenantOwnedRowForADemoTenant(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	plantWitnessRows(t, pool, demoTenantID)

	for _, tbl := range db.PurgeTablesForTest {
		if n := countFor(t, pool, tbl, db.DemoTenants); n == 0 {
			t.Fatalf("test setup: %s holds 0 demo-tenant rows before the purge — asserting it is empty afterwards would be vacuous", tbl)
		}
	}

	if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}

	for _, tbl := range db.PurgeTablesForTest {
		if n := countFor(t, pool, tbl, db.DemoTenants); n != 0 {
			t.Errorf("%s still holds %d row(s) for the demo tenants after the purge, want 0", tbl, n)
		}
	}
	for _, tbl := range db.PurgeExcludedTablesForTest {
		if tbl == "memberships" {
			if n := countFor(t, pool, tbl, db.DemoTenants); n == 0 {
				t.Errorf("%s is excluded from the purge but holds 0 demo-tenant rows afterwards — it was deleted anyway", tbl)
			}
		}
	}
}

// ---- Obligation 3: a non-demo tenant survives, witnessed in every table ----

// TestPurgeLeavesANonDemoTenantUntouched (obligation 3, AC-2): the throwaway
// tenant holds a row in all twenty-one purged tables; every one must survive the
// purge with identical column values.
func TestPurgeLeavesANonDemoTenantUntouched(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	createWitnessTenant(t, pool)
	plantWitnessRows(t, pool, witnessTenantID)

	before := map[string][]string{}
	for _, tbl := range db.PurgeTablesForTest {
		before[tbl] = snapshotTable(t, pool, tbl, witnessTenantID)
		if len(before[tbl]) == 0 {
			t.Fatalf("test setup: the witness tenant holds no %s row before the purge — its survival cannot be witnessed", tbl)
		}
	}

	if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}

	for _, tbl := range db.PurgeTablesForTest {
		after := snapshotTable(t, pool, tbl, witnessTenantID)
		if !reflect.DeepEqual(before[tbl], after) {
			t.Errorf("the non-demo witness tenant's %s rows changed across the purge — the allowlist did not hold\nbefore: %v\nafter:  %v", tbl, before[tbl], after)
		}
	}
}

// TestPurgeWitnessAssertionIsSensitiveToTheTenantFilter (obligation 3): the
// same witness, purged through the unexported seam with its tenant ADDED to the
// list, inside a rolled-back transaction. Every witness row must be gone —
// mutation, not inspection, so the survival assertion above cannot be vacuous.
func TestPurgeWitnessAssertionIsSensitiveToTheTenantFilter(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	createWitnessTenant(t, pool)
	plantWitnessRows(t, pool, witnessTenantID)

	conn, err := pgx.Connect(ctx, superDSN)
	if err != nil {
		t.Fatalf("test setup: connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("test setup: begin: %v", err)
	}
	// Rolled back unconditionally: the mutation must never reach disk.
	defer func() { _ = tx.Rollback(ctx) }()

	for _, tbl := range db.PurgeTablesForTest {
		n := mustCount(t, tx, `SELECT count(*) FROM `+tbl+` WHERE tenant_id::text = $1`, witnessTenantID)
		if n == 0 {
			t.Fatalf("test setup: the witness tenant holds no %s row, so its disappearance cannot be observed", tbl)
		}
	}

	widened := append(append([]string{}, db.DemoTenants...), witnessTenantID)
	if _, err := db.PurgeWithinForTest(ctx, tx, widened); err != nil {
		t.Fatalf("purgeWithin over the widened tenant list: %v", err)
	}

	for _, tbl := range db.PurgeTablesForTest {
		n := mustCount(t, tx, `SELECT count(*) FROM `+tbl+` WHERE tenant_id::text = $1`, witnessTenantID)
		if n != 0 {
			t.Errorf("%s still holds %d witness row(s) after purging a tenant list that INCLUDES the witness — the survival assertion in TestPurgeLeavesANonDemoTenantUntouched proves nothing, because this purge cannot reach that tenant's rows at all", tbl, n)
		}
	}
}

// ---- Obligation 4: the replica window is exactly one statement -------------

// stmtTracer records the SQL of every STANDALONE statement pgx sends on a
// connection. It is a QueryTracer only, so it sees nothing a SendBatch carries —
// request_gate_db_test.go's seamTracer is the one that does.
type stmtTracer struct {
	mu    sync.Mutex
	stmts []string
}

func (tr *stmtTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.stmts = append(tr.stmts, d.SQL)
	return ctx
}

func (tr *stmtTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tr *stmtTracer) recorded() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]string{}, tr.stmts...)
}

// replicationRoleSet reports the value a SET session_replication_role statement
// assigns, matching on normalised whitespace so the implementation's exact
// spelling is not pinned.
func replicationRoleSet(sql string) (string, bool) {
	l := strings.ToLower(strings.Join(strings.Fields(sql), " "))
	if !strings.HasPrefix(l, "set ") || !strings.Contains(l, "session_replication_role") {
		return "", false
	}
	i := strings.Index(l, "=")
	if i == -1 {
		return "", false
	}
	return strings.Trim(strings.TrimSpace(l[i+1:]), "'\";"), true
}

func isSetStatement(sql string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(sql)), "set ")
}

// tracedPurgeConn opens a pinned connection carrying tr and returns it. The
// seam is the only place a tracer can be attached — PurgeDemoTenants dials its
// own connection.
func tracedPurgeConn(t *testing.T, dsn string, tr *stmtTracer) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("test setup: parse superuser dsn: %v", err)
	}
	cfg.Tracer = tr
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("test setup: connect with tracer: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// TestPurgeReplicaWindowHoldsExactlyOneStatement (obligation 4, AC-3): the
// foreign-key bypass is transaction-wide while it is on, so the window must
// open once, hold only the tenant-scoped audit_log delete, and close again.
func TestPurgeReplicaWindowHoldsExactlyOneStatement(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	ctx := context.Background()

	tr := &stmtTracer{}
	conn := tracedPurgeConn(t, superDSN, tr)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("test setup: begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := db.PurgeWithinForTest(ctx, tx, db.DemoTenants); err != nil {
		t.Fatalf("purgeWithin: %v", err)
	}

	stmts := tr.recorded()
	if len(stmts) == 0 {
		t.Fatal("the tracer recorded no statements at all — it is not attached to the connection the purge ran on")
	}

	replicaCount, replicaAt, closeAt := 0, -1, -1
	for i, s := range stmts {
		v, ok := replicationRoleSet(s)
		if !ok {
			continue
		}
		switch v {
		case "replica":
			replicaCount++
			if replicaAt == -1 {
				replicaAt = i
			}
		case "origin":
			if replicaAt != -1 && closeAt == -1 {
				closeAt = i
			}
		}
	}
	if replicaCount != 1 {
		t.Fatalf("the purge switched session_replication_role to 'replica' %d time(s), want exactly 1 — the bypass suppresses referential integrity transaction-wide, so a second window (or none) changes what the other twenty deletes are checked against\ntraced: %v", replicaCount, stmts)
	}
	if closeAt == -1 {
		t.Fatalf("session_replication_role is never set back to 'origin' after the replica window opens\ntraced: %v", stmts)
	}

	var inWindow []string
	for _, s := range stmts[replicaAt+1 : closeAt] {
		if !isSetStatement(s) {
			inWindow = append(inWindow, s)
		}
	}
	if len(inWindow) != 1 {
		t.Fatalf("%d non-SET statement(s) ran inside the replica window, want exactly 1: %v", len(inWindow), inWindow)
	}
	if !strings.Contains(inWindow[0], "audit_log") {
		t.Errorf("the statement inside the replica window is %q, want the audit_log delete — no other table needs the bypass", inWindow[0])
	}
	if !strings.Contains(inWindow[0], tenantPredicate) {
		t.Errorf("the statement inside the replica window is %q and carries no %q — the one statement that runs with integrity checks off must still be tenant-scoped", inWindow[0], tenantPredicate)
	}
}

// TestPurgeRestoresOriginBeforeCommit (AC-3): the bypass must be re-armed while
// the transaction is still open, not left to the connection's close.
func TestPurgeRestoresOriginBeforeCommit(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, superDSN)
	if err != nil {
		t.Fatalf("test setup: connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("test setup: begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := db.PurgeWithinForTest(ctx, tx, db.DemoTenants); err != nil {
		t.Fatalf("purgeWithin: %v", err)
	}

	var role string
	if err := tx.QueryRow(ctx, `SELECT current_setting('session_replication_role')`).Scan(&role); err != nil {
		t.Fatalf("read session_replication_role: %v", err)
	}
	if role != "origin" {
		t.Errorf("session_replication_role = %q before COMMIT, want \"origin\" — the transaction would commit with referential integrity still suppressed", role)
	}
}

// TestPurgeDeleteOrderRunsUnderFullForeignKeyEnforcement (AC-3): the twenty
// non-audit_log deletes must run under 'origin', so a future reordering of
// purgeTables fails loudly instead of silently orphaning rows.
func TestPurgeDeleteOrderRunsUnderFullForeignKeyEnforcement(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	t.Run("a BEFORE DELETE probe on line_items observes origin", func(t *testing.T) {
		reseedOnCleanup(t, superDSN)
		plantWitnessRows(t, pool, demoTenantID)

		execSetup(t, pool, `CREATE TABLE IF NOT EXISTS demo_purge_probe (observed_role text NOT NULL)`)
		execSetup(t, pool, `
			CREATE OR REPLACE FUNCTION demo_purge_probe_fn() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			    INSERT INTO demo_purge_probe (observed_role)
			    VALUES (current_setting('session_replication_role'));
			    RETURN OLD;
			END $$`)
		execSetup(t, pool, `
			CREATE TRIGGER demo_purge_probe_trg BEFORE DELETE ON line_items
			FOR EACH ROW EXECUTE FUNCTION demo_purge_probe_fn()`)
		t.Cleanup(func() {
			execSetup(t, pool, `DROP TRIGGER IF EXISTS demo_purge_probe_trg ON line_items`)
			execSetup(t, pool, `DROP FUNCTION IF EXISTS demo_purge_probe_fn()`)
			execSetup(t, pool, `DROP TABLE IF EXISTS demo_purge_probe`)
		})

		if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
			t.Fatalf("PurgeDemoTenants: %v", err)
		}

		fired := mustCount(t, pool, `SELECT count(*) FROM demo_purge_probe`)
		if fired == 0 {
			t.Fatalf("the BEFORE DELETE probe on line_items never fired — an ORIGIN trigger does not fire under session_replication_role='replica', so the non-audit_log deletes ran with referential integrity suppressed")
		}
		wrong := mustCount(t, pool, `SELECT count(*) FROM demo_purge_probe WHERE observed_role <> 'origin'`)
		if wrong != 0 {
			t.Errorf("%d of %d probe observations saw session_replication_role other than 'origin'", wrong, fired)
		}
	})

	t.Run("a reordered purge raises restrict_violation instead of orphaning rows", func(t *testing.T) {
		reseedOnCleanup(t, superDSN)
		plantWitnessRows(t, pool, demoTenantID)

		order := db.PurgeTablesForTest
		i, j := indexOfTable(t, order, "submission_jobs"), indexOfTable(t, order, "invoices")
		// In-place swap: PurgeTablesForTest shares purgeTables' backing array,
		// so this is the order the purge itself executes.
		order[i], order[j] = order[j], order[i]
		t.Cleanup(func() { order[i], order[j] = order[j], order[i] })

		before := countFor(t, pool, "invoices", db.DemoTenants)
		if before == 0 {
			t.Fatal("test setup: no demo invoices to orphan, so the reorder cannot be detected")
		}

		_, err := db.PurgeDemoTenants(ctx, superDSN)
		if err == nil {
			t.Fatal("a purge that deletes invoices before submission_jobs returned nil — the RESTRICT foreign key was not enforced, so a future reorder would silently orphan rows")
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != restrictViolation {
			t.Fatalf("reordered purge failed with %v, want SQLSTATE %s (restrict_violation)", err, restrictViolation)
		}
		if after := countFor(t, pool, "invoices", db.DemoTenants); after != before {
			t.Errorf("demo invoices = %d after the failed purge, want %d — the transaction did not roll back", after, before)
		}
	})
}

func indexOfTable(t *testing.T, tables []string, name string) int {
	t.Helper()
	for i, tbl := range tables {
		if tbl == name {
			return i
		}
	}
	t.Fatalf("purgeTables has no %s entry", name)
	return -1
}

// TestPurgeAuditLogRequiresTheReplicaBypass pins the measured premise the
// narrowed window rests on: audit_log's append-only trigger refuses a DELETE
// under 'origin' even for a superuser, and 'replica' is what lifts it.
func TestPurgeAuditLogRequiresTheReplicaBypass(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	event := "purge.premise." + uuid.NewString()[:8]
	execSetup(t, pool,
		`INSERT INTO audit_log (tenant_id, actor, event, payload) VALUES ($1,'purge-premise',$2,'{}'::jsonb)`,
		demoTenantID, event)

	t.Run("origin refuses the delete", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("test setup: begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		execSetup(t, tx, `SET LOCAL session_replication_role = 'origin'`)
		_, err = tx.Exec(ctx, `DELETE FROM audit_log WHERE event = $1`, event)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != restrictViolation {
			t.Fatalf("DELETE FROM audit_log under 'origin' returned %v, want SQLSTATE %s — the bypass would be unnecessary", err, restrictViolation)
		}
	})

	t.Run("replica permits it", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("test setup: begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		execSetup(t, tx, `SET LOCAL session_replication_role = 'replica'`)
		tag, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE event = $1`, event)
		if err != nil {
			t.Fatalf("DELETE FROM audit_log under 'replica': %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("DELETE under 'replica' removed %d row(s), want 1", tag.RowsAffected())
		}
	})

	t.Cleanup(func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("cleanup: begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		execSetup(t, tx, `SET LOCAL session_replication_role = 'replica'`)
		execSetup(t, tx, `DELETE FROM audit_log WHERE event = $1`, event)
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("cleanup: commit: %v", err)
		}
	})
}

// ---- AC-1/AC-4/AC-5: restore, idempotence, counts, bounded waiting ---------

// workflowRoleIDs returns the demo tenants' workflow_roles ids, sorted. Those
// ids are seeded as literals, so they must survive a purge-and-reseed byte for
// byte — approval policies reference roles by key, and a regenerated id set is
// the shape that breaks a demo silently.
func workflowRoleIDs(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT id::text FROM workflow_roles WHERE tenant_id::text = ANY($1) ORDER BY 1`, db.DemoTenants)
	if err != nil {
		t.Fatalf("read workflow_roles ids: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan workflow_roles id: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read workflow_roles ids: %v", err)
	}
	return out
}

// TestPurgeThenSeedRestoresTheSeededBaselineExactly (AC-1): the purge is only
// safe on a demo because the next Seed puts the demo back. audit_log is the one
// table it cannot restore — db/seed.dev.sql writes no audit rows — so that half
// is asserted separately rather than folded into a count comparison it would
// pass trivially.
func TestPurgeThenSeedRestoresTheSeededBaselineExactly(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("test setup: Seed (establish baseline): %v", err)
	}

	baseline := map[string]int{}
	for _, tbl := range db.PurgeTablesForTest {
		baseline[tbl] = countFor(t, pool, tbl, db.DemoTenants)
	}
	// The seed does not populate every purged table; these it does, and a zero
	// here would make the comparison below vacuous.
	for _, tbl := range []string{
		"business_entities", "invoices", "line_items", "invoice_status_history",
		"submission_jobs", "app_exchange", "workflow_roles", "workflow_role_members",
	} {
		if baseline[tbl] == 0 {
			t.Fatalf("test setup: the seeded baseline holds 0 %s rows for the demo tenants — comparing that count after a purge would prove nothing", tbl)
		}
	}
	rolesBefore := workflowRoleIDs(t, pool)

	if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed after purge: %v", err)
	}

	for _, tbl := range db.PurgeTablesForTest {
		if tbl == "audit_log" {
			continue
		}
		if got := countFor(t, pool, tbl, db.DemoTenants); got != baseline[tbl] {
			t.Errorf("%s holds %d demo-tenant row(s) after purge+Seed, want the baseline %d", tbl, got, baseline[tbl])
		}
	}
	if n := countFor(t, pool, "audit_log", db.DemoTenants); n != 0 {
		t.Errorf("audit_log holds %d demo-tenant row(s) after purge+Seed, want 0 — the seed writes no audit rows, so this is the half of the purge a reseed deliberately does not restore", n)
	}
	if rolesAfter := workflowRoleIDs(t, pool); !reflect.DeepEqual(rolesBefore, rolesAfter) {
		t.Errorf("workflow_roles ids changed across purge+Seed — they are seeded as literals precisely so they survive\nbefore: %v\nafter:  %v", rolesBefore, rolesAfter)
	}
}

// TestPurgeIsIdempotent (AC-4): a redeploy runs the purge again on an
// already-purged database.
func TestPurgeIsIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("test setup: Seed: %v", err)
	}

	first, err := db.PurgeDemoTenants(ctx, superDSN)
	if err != nil {
		t.Fatalf("first PurgeDemoTenants: %v", err)
	}
	if first.Rows == 0 {
		t.Fatal("the first purge deleted 0 rows, so 'the second deletes none' would prove nothing")
	}

	second, err := db.PurgeDemoTenants(ctx, superDSN)
	if err != nil {
		t.Fatalf("second PurgeDemoTenants: %v", err)
	}
	if second.Rows != 0 {
		t.Errorf("second PurgeDemoTenants deleted %d row(s), want 0", second.Rows)
	}
	if len(second.ByTable) != 0 {
		t.Errorf("second PurgeDemoTenants reported ByTable = %v, want empty — a table that deleted nothing must not be named", second.ByTable)
	}
}

// TestPurgeResultCountsMatchRowsDeleted (AC-5): the database is drained first,
// then a known number of rows is planted across exactly three foreign-key-free
// tables, so ByTable is checked against a number the test chose.
func TestPurgeResultCountsMatchRowsDeleted(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
		t.Fatalf("PurgeDemoTenants (drain): %v", err)
	}

	tenantA, tenantB := db.DemoTenants[0], db.DemoTenants[1]
	execSetup(t, pool, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1,'purge-count-1')`, tenantA)
	execSetup(t, pool, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1,'purge-count-2')`, tenantB)
	execSetup(t, pool, `INSERT INTO submission_rate_limits (tenant_id, max_per_minute) VALUES ($1,60)
		ON CONFLICT (tenant_id) DO UPDATE SET max_per_minute = EXCLUDED.max_per_minute`, tenantA)
	for i := 0; i < 3; i++ {
		execSetup(t, pool,
			`INSERT INTO invitations (tenant_id, role, invitee_email, status)
			 VALUES ($1,'admin',$2,'pending')`,
			tenantA, "purge-count-"+strconv.Itoa(i)+"@example.test")
	}

	res, err := db.PurgeDemoTenants(ctx, superDSN)
	if err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}
	want := map[string]int64{"idempotency_keys": 2, "submission_rate_limits": 1, "invitations": 3}
	if !reflect.DeepEqual(res.ByTable, want) {
		t.Errorf("PurgeResult.ByTable = %v, want %v — it must name exactly the tables that lost rows, with their counts", res.ByTable, want)
	}
	if res.Rows != 6 {
		t.Errorf("PurgeResult.Rows = %d, want 6 (the sum of ByTable)", res.Rows)
	}
}

// TestPurgeBoundedByStatementTimeout (AC-3): a demo purge runs at gateway boot,
// so it must fail rather than hold the boot open behind someone else's lock.
func TestPurgeBoundedByStatementTimeout(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	t.Run("the transaction sets both guards", func(t *testing.T) {
		conn, err := pgx.Connect(ctx, superDSN)
		if err != nil {
			t.Fatalf("test setup: connect: %v", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("test setup: begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if _, err := db.PurgeWithinForTest(ctx, tx, db.DemoTenants); err != nil {
			t.Fatalf("purgeWithin: %v", err)
		}
		var lockMS, stmtMS int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT setting::int FROM pg_settings WHERE name = 'lock_timeout'),
			       (SELECT setting::int FROM pg_settings WHERE name = 'statement_timeout')`).Scan(&lockMS, &stmtMS); err != nil {
			t.Fatalf("read timeout settings: %v", err)
		}
		if lockMS != 15000 {
			t.Errorf("lock_timeout = %dms inside the purge transaction, want 15000", lockMS)
		}
		if stmtMS != 60000 {
			t.Errorf("statement_timeout = %dms inside the purge transaction, want 60000", stmtMS)
		}
	})

	t.Run("a held row lock fails the purge instead of blocking the boot", func(t *testing.T) {
		reseedOnCleanup(t, superDSN)
		plantWitnessRows(t, pool, demoTenantID)

		holder, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("test setup: begin lock holder: %v", err)
		}
		defer func() { _ = holder.Rollback(ctx) }()
		var lockedID string
		if err := holder.QueryRow(ctx,
			`SELECT id::text FROM invoices WHERE tenant_id::text = ANY($1) LIMIT 1 FOR UPDATE`,
			db.DemoTenants).Scan(&lockedID); err != nil {
			t.Fatalf("test setup: lock a demo invoice row: %v", err)
		}

		before := countFor(t, pool, "invoices", db.DemoTenants)
		started := time.Now()
		_, err = db.PurgeDemoTenants(ctx, superDSN)
		elapsed := time.Since(started)

		if err == nil {
			t.Fatal("PurgeDemoTenants succeeded while another session held a row lock on a demo invoice — it cannot have waited on the lock at all")
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != lockNotAvailable {
			t.Fatalf("PurgeDemoTenants failed with %v, want SQLSTATE %s (lock_not_available) from lock_timeout", err, lockNotAvailable)
		}
		if elapsed > 40*time.Second {
			t.Errorf("PurgeDemoTenants took %s to give up, want the 15s lock_timeout to bound it", elapsed)
		}
		if after := countFor(t, pool, "invoices", db.DemoTenants); after != before {
			t.Errorf("demo invoices = %d after the blocked purge, want %d — the transaction did not roll back", after, before)
		}
	})
}

// LT-11 (AC-9). The boot-time purge deletes the row too: demopurge.go's table list is
// column-blind, and TestPurgeTableListCoversEveryTenantOwnedTable is what keeps it complete.
func TestPurgeClearsAJobHoldingLayoutTokens(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ctx := context.Background()

	reseedOnCleanup(t, superDSN)
	jobID := ltSeedJobWithTokens(t, pool, demoTenantID, "purge-layout-tokens-"+uuid.NewString())

	if _, err := db.PurgeDemoTenants(ctx, superDSN); err != nil {
		t.Fatalf("PurgeDemoTenants: %v", err)
	}
	if n := mustCount(t, pool, `SELECT count(*) FROM extraction_jobs WHERE id = $1`, jobID); n != 0 {
		t.Errorf("the job holding layout_tokens survived the purge (%d row(s)), want 0", n)
	}
}
