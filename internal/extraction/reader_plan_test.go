// reader_plan_test.go: the reader's statement is served by extraction_jobs_tenant_document_idx
// over a realistic multi-tenant corpus, and reader.go names no River table. Shares
// store_db_test.go's harness.
package extraction_test

import (
	"context"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	// 4,000 corpus rows sit ~20x above the measured seq/index flip point, which lies between
	// 100 and 200 rows on this table.
	rpTenants       = 20
	rpJobsPerTenant = 200

	// rpHotJobs must track maxJobsPerDocument, which is unexported and absent from
	// export_test.go. rpCorpus asserts the two agree via rdDeclaredCap.
	rpHotJobs = 50

	rpIndex = "extraction_jobs_tenant_document_idx"
	rpTable = "extraction_jobs"
)

// rpFixture is the corpus plus the statement the reader actually issued. The SQL is captured off
// the pgx tracer, never retyped: a hand-written copy pins a dead literal, and a plan over it
// would stay green through any change to the shipped query.
type rpFixture struct {
	ctxA   context.Context
	hotDoc string
	sql    string
}

// rpCorpus builds 20 tenants x 200 jobs plus one hot document carrying the LIMIT cap, ANALYZEs,
// asserts the shape, and captures the reader's statement. Per test with t.Cleanup, not
// sync.Once: a leaked corpus moves the plan for every other test in the binary.
func rpCorpus(t *testing.T) rpFixture {
	t.Helper()
	ctx := t.Context()
	h := stRequire(t)

	if got := rdDeclaredCap(t); got != rpHotJobs {
		t.Fatalf("reader.go declares %s = %d but this file seeds %d hot jobs; the LIMIT arg below "+
			"would no longer be the shipped one", rdCapName, got, rpHotJobs)
	}

	// Registered before rdTenant, so LIFO runs it LAST — after every corpus tenant is gone.
	// Otherwise the statistics stay pinned at 4,000 rows for the other tests in the binary.
	t.Cleanup(func() { rpAnalyze(t) })

	ctxA, readTenant, hotDoc := rdTenant(t, ctx, "active")

	tenantIDs := []string{readTenant}
	for i := 1; i < rpTenants; i++ {
		id := uuid.NewString()
		if _, err := h.super.Exec(ctx,
			`INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, "extr-07-plan "+id[:8]); err != nil {
			t.Fatalf("seed corpus tenant %d: %v", i, err)
		}
		tenantIDs = append(tenantIDs, id)
	}
	// invoice_app holds no DELETE on either table, so teardown is the superuser's. One DELETE
	// suffices: the tenant_id CASCADE removes the extraction_jobs rows before the composite
	// RESTRICT FK to documents is checked.
	extra := tenantIDs[1:]
	t.Cleanup(func() {
		if _, err := h.super.Exec(context.Background(),
			`DELETE FROM tenants WHERE id = ANY($1)`, extra); err != nil {
			t.Errorf("teardown %d corpus tenants: %v", len(extra), err)
		}
	})

	start := time.Now()
	for i, id := range tenantIDs {
		// Set-based: one statement per tenant. The composite FK (tenant_id, document_id) needs a
		// documents parent per distinct document_id, which here is one per job; 4,000 single-row
		// inserts from Go would not be free.
		if _, err := h.super.Exec(ctx,
			`WITH d AS (
			     INSERT INTO documents (id, tenant_id, storage_key, content_hash, size_bytes)
			     SELECT gen_random_uuid(), $1, 'extr-07-plan/' || gen_random_uuid() || '/' || g,
			            md5(gen_random_uuid()::text) || md5(gen_random_uuid()::text), 1024
			       FROM generate_series(1, $2) g
			     RETURNING id
			 )
			 INSERT INTO extraction_jobs
			     (tenant_id, document_id, state, extractor, extractor_version, created_at)
			 SELECT $1, d.id, $3, $4, $5, now() FROM d`,
			id, rpJobsPerTenant, rdStates[0], stExtractor, stExtractorVersion); err != nil {
			t.Fatalf("seed %d jobs for corpus tenant %d: %v", rpJobsPerTenant, i, err)
		}
	}
	// The hot document: the shape the LIMIT actually serves. A 1-job document plans as a plain
	// Index Scan, which is not what this reader exists to do.
	if _, err := h.super.Exec(ctx,
		`INSERT INTO extraction_jobs
		     (tenant_id, document_id, state, extractor, extractor_version, created_at)
		 SELECT $1, $2, $3, $4, $5, now() - (g * interval '1 second')
		   FROM generate_series(1, $6) g`,
		readTenant, hotDoc, rdStates[0], stExtractor, stExtractorVersion, rpHotJobs); err != nil {
		t.Fatalf("seed %d jobs on the hot document: %v", rpHotJobs, err)
	}
	build := time.Since(start)

	// Required for determinism, not to make the assertions pass: autovacuum firing mid-run
	// would otherwise move the plan under the test.
	rpAnalyze(t)
	t.Logf("corpus: %d tenants x %d jobs + %d hot = %d rows in %s",
		rpTenants, rpJobsPerTenant, rpHotJobs, rpTenants*rpJobsPerTenant+rpHotJobs, build.Round(time.Millisecond))

	rpAssertCorpusShape(t, tenantIDs, hotDoc)

	return rpFixture{ctxA: ctxA, hotDoc: hotDoc, sql: rpCapturedSQL(t, ctxA, hotDoc)}
}

func rpAnalyze(t *testing.T) {
	t.Helper()
	if _, err := stRequire(t).super.Exec(context.Background(), "ANALYZE "+rpTable); err != nil {
		t.Errorf("ANALYZE %s: %v", rpTable, err)
	}
}

// rpAssertCorpusShape runs before any EXPLAIN. Every plan assertion in this file is a claim
// about selectivity, so a corpus that silently changed shape would turn each of them into a
// different claim. Counted as the SUPERUSER: an app-pool count is RLS-filtered to one tenant.
func rpAssertCorpusShape(t *testing.T, tenantIDs []string, hotDoc string) {
	t.Helper()
	ctx := context.Background()
	h := stRequire(t)

	var rows, tenants, docs int
	if err := h.super.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT tenant_id), count(DISTINCT document_id)
		   FROM extraction_jobs WHERE tenant_id = ANY($1)`, tenantIDs).
		Scan(&rows, &tenants, &docs); err != nil {
		t.Fatalf("read back corpus shape: %v", err)
	}

	if want := rpTenants*rpJobsPerTenant + rpHotJobs; rows != want {
		t.Fatalf("corpus holds %d job(s), want %d", rows, want)
	}
	if tenants != rpTenants {
		t.Fatalf("corpus spans %d tenant(s), want %d — a single-tenant corpus makes the tenant "+
			"lead of the index unfalsifiable", tenants, rpTenants)
	}
	if want := rpTenants*rpJobsPerTenant + 1; docs != want {
		t.Fatalf("corpus spans %d document(s), want %d", docs, want)
	}

	var hot int
	if err := h.super.QueryRow(ctx,
		`SELECT count(*) FROM extraction_jobs WHERE document_id = $1`, hotDoc).Scan(&hot); err != nil {
		t.Fatalf("count the hot document: %v", err)
	}
	if hot != rpHotJobs {
		t.Fatalf("the hot document carries %d job(s), want %d — the EXPLAIN below would measure "+
			"the one-row plan shape instead", hot, rpHotJobs)
	}

	// The corpus must dominate the table, or the plan is a property of somebody else's rows.
	var total int
	if err := h.super.QueryRow(ctx, `SELECT count(*) FROM extraction_jobs`).Scan(&total); err != nil {
		t.Fatalf("count extraction_jobs: %v", err)
	}
	if total != rows {
		t.Fatalf("extraction_jobs holds %d row(s) but the corpus is %d; a leftover fixture is "+
			"shaping this plan", total, rows)
	}
}

// rpCapturedSQL runs one real read and returns the statement pgx saw on the wire. It doubles as
// the count-first floor: a read returning nothing would also produce a plan.
func rpCapturedSQL(t *testing.T, ctxA context.Context, documentID string) string {
	t.Helper()
	tr := &rdQueryTracer{}
	r := &extraction.Reader{Pool: rdTracedPool(t, tr)}

	out, err := r.JobsForDocument(ctxA, documentID)
	if err != nil {
		t.Fatalf("JobsForDocument over the corpus: %v", err)
	}
	if len(out.Jobs) != rpHotJobs {
		t.Fatalf("the read returned %d row(s), want %d; a plan over a query that returns nothing "+
			"proves nothing", len(out.Jobs), rpHotJobs)
	}

	n, seen := tr.matching(rpTable)
	if n != 1 {
		t.Fatalf("the read issued %d statement(s) naming %s, want 1; the pool saw %v", n, rpTable, seen)
	}
	for _, sql := range seen {
		if strings.Contains(sql, rpTable) {
			return sql
		}
	}
	t.Fatalf("no captured statement names %s; the pool saw %v", rpTable, seen)
	return ""
}

// rpExplain plans the captured statement as invoice_app with app.current_tenant set, through
// the production seam. COSTS OFF, and enable_seqscan is never touched: forcing the plan would
// make every assertion below a tautology.
func rpExplain(t *testing.T, f rpFixture) string {
	t.Helper()
	ctx := f.ctxA
	var lines []string
	if err := db.WithinRequestTenantTx(ctx, stRequire(t).app, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+f.sql, f.hotDoc, rpHotJobs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("EXPLAIN the captured statement: %v", err)
	}
	plan := strings.Join(lines, "\n")
	if plan == "" || !strings.Contains(plan, rpTable) {
		t.Fatalf("plan = %q, want a non-empty plan naming %s", plan, rpTable)
	}
	return plan
}

// rpScanNode is one scan node and the Index Cond lines belonging to it ALONE. Concatenating
// across nodes (internal/audit's planCondLines) accepts a BitmapAnd whose two cond lines
// together name both columns; pinning the node to the exact index name is what rejects it.
type rpScanNode struct {
	target string
	conds  []string
}

func rpScanNodes(plan string) []rpScanNode {
	var out []rpScanNode
	for _, line := range strings.Split(plan, "\n") {
		if target, ok := rpScanTarget(line); ok {
			out = append(out, rpScanNode{target: target})
			continue
		}
		trimmed := strings.TrimSpace(line)
		if len(out) > 0 && strings.HasPrefix(trimmed, "Index Cond:") {
			last := &out[len(out)-1]
			last.conds = append(last.conds, trimmed)
		}
	}
	return out
}

// rpScanTarget reports the relation or index a plan line scans. "Scan using X on Y" names the
// index, "Scan on X" the relation; "using" is matched first because the former line carries
// both markers.
func rpScanTarget(line string) (string, bool) {
	for _, marker := range []string{"Scan using ", "Scan on "} {
		if i := strings.Index(line, marker); i >= 0 {
			return strings.Fields(line[i+len(marker):])[0], true
		}
	}
	return "", false
}

// AC-2 and AC-3: the RLS policy's tenant predicate and the reader's document predicate are
// served by ONE index lookup, not by a scan plus a filter. Node type is deliberately unasserted
// — a 1-job document plans as "Index Scan using", the 50-job document as a Bitmap pair.
//
// The name and no-Seq-Scan checks are coarse: no reader.go edit reaches either, and both go red
// only on a migration that drops or renames the index. The Index Cond check is the falsifiable
// one — uuid_eq is proleakproof, so FORCE RLS holds neither predicate back from it.
func TestRLS_ExtractionReaderPlanPushesTheTenantPredicateIntoTheIndexCond(t *testing.T) {
	f := rpCorpus(t)
	plan := rpExplain(t, f)

	if !strings.Contains(plan, rpIndex) {
		t.Errorf("plan = %s\nwant it to use %s", plan, rpIndex)
	}
	if strings.Contains(plan, "Seq Scan on "+rpTable) {
		t.Errorf("plan = %s\nmust not Seq Scan %s", plan, rpTable)
	}

	nodes := rpScanNodes(plan)
	if len(nodes) == 0 {
		t.Fatalf("plan = %s\nno scan node parsed out of it", plan)
	}

	var onIndex int
	for _, n := range nodes {
		if n.target != rpIndex {
			continue
		}
		onIndex++
		for _, cond := range n.conds {
			// ONE line naming both. Two lines that each name one are the BitmapAnd shape a
			// document_id-only index produces, and that is a failure, not a pass.
			if strings.Contains(cond, "tenant_id") && strings.Contains(cond, "document_id") {
				return
			}
		}
	}
	if onIndex == 0 {
		t.Fatalf("plan = %s\nno node scans %s, so the tenant predicate cannot be in its Index Cond",
			plan, rpIndex)
	}
	t.Errorf("plan = %s\nno single Index Cond line under %s names both tenant_id and document_id; "+
		"one of them is a post-scan Filter, or the cond is split across two indexes", plan, rpIndex)
}

// rpRiverNeedles are the queue tables the read path must not touch. idempotency_keys rides
// along: it is the submission spine's write-side table and has no business on a poll.
var rpRiverNeedles = []string{"river_job", "river_leader", "river_queue", "idempotency_keys"}

// AC-4, the concrete half of "does not degrade the queue". File-scoped to reader.go on purpose:
// store.go and worker.go legitimately import River, so a package-level check would fail.
func TestExtractionReader_TouchesNoRiverTable(t *testing.T) {
	// Floor one: an absence proved over an empty needle list is not a proof.
	if len(rpRiverNeedles) == 0 {
		t.Fatal("rpRiverNeedles is empty, so this scan looked for nothing")
	}

	file, fset := mxParse(t, rdReaderSource)

	var lits, named int
	ast.Inspect(file, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		unq, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		lits++
		if strings.Contains(unq, rpTable) {
			named++
		}
		for _, needle := range rpRiverNeedles {
			if strings.Contains(unq, needle) {
				t.Errorf("%s: %s names %s; the read path must not touch the queue",
					fset.Position(bl.Pos()), rdReaderSource, needle)
			}
		}
		return true
	})

	// Floor two: a parse yielding no literals, or only struct tags, makes the loop above vacuous.
	if lits == 0 {
		t.Fatalf("%s holds no string literal, so this scan examined nothing", rdReaderSource)
	}
	if named == 0 {
		t.Fatalf("%s holds no string literal naming %s, so the query this scan is about was never "+
			"examined", rdReaderSource, rpTable)
	}

	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if strings.Contains(path, "riverqueue") {
			t.Errorf("%s: %s imports %s", fset.Position(imp.Pos()), rdReaderSource, path)
		}
	}
}

// The seam's TOTAL wire cost, not the per-table count TestRLS_ExtractionReaderIssuesOneQueryPerRead
// already holds — a query added against a DIFFERENT table leaves that one at 1. The gate's
// set_config and membership SELECT ride a pgx.Batch, which fires only a BatchTracer; BeginTx and
// Commit go through Conn.Exec and do reach this tracer.
func TestRLS_ExtractionReaderIssuesNoStatementBeyondBeginSelectCommit(t *testing.T) {
	ctx := t.Context()
	tr := &rdQueryTracer{}
	r := &extraction.Reader{Pool: rdTracedPool(t, tr)}

	ctxA, tenantID, documentID := rdTenant(t, ctx, "active")
	base := time.Now().UTC().Truncate(time.Microsecond)
	const seeded = 3
	for i := range seeded {
		rdSeedJob(t, ctx, tenantID, documentID, rdStates[i%len(rdStates)], base.Add(time.Duration(i)*time.Second), nil)
	}

	out, err := r.JobsForDocument(ctxA, documentID)
	if err != nil {
		t.Fatalf("JobsForDocument: %v", err)
	}
	// The rows make the count mean something: a read that returned nothing would issue the
	// same three statements.
	if len(out.Jobs) != seeded {
		t.Fatalf("got %d row(s) %v, want %d", len(out.Jobs), rdIDs(out.Jobs), seeded)
	}

	_, seen := tr.matching(rpTable)
	if len(seen) != 3 {
		t.Fatalf("one read of %d row(s) issued %d traced statement(s), want 3 (begin, the SELECT, "+
			"commit); the pool saw %v", len(out.Jobs), len(seen), seen)
	}
	if seen[0] != "begin" || seen[2] != "commit" {
		t.Errorf("the traced statements were %v, want begin and commit around one SELECT", seen)
	}
	if !strings.Contains(seen[1], rpTable) {
		t.Errorf("the statement between begin and commit was %q, want the read of %s", seen[1], rpTable)
	}
}
