// Test-first (RED) suite for EXTR-15-01: extraction_jobs.failure_kind. The column carries the
// stage that failed onto the row, where both extraction read DTOs can serve it -- until now it
// existed only inside the audit payload, which no reader can query.
//
// Everything runs on one migrator transaction, rolled back: the migrations job carries no
// DATABASE_READER_URL, so requireHarness would skip and rls-test-gate.sh fails a step on any
// skip (TestCIRunFiltersReachEveryTestInThePackage pins the -run filter that reaches this file).
package db_test

import (
	"context"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	failureKindMigrationGlob = "*_extraction_jobs_failure_kind.sql"
	failureKindColumn        = "failure_kind"

	// The newest migration on main at aaee0c3d. This story's file must sort after it: a
	// reverse-timestamp merge froze production deploys for nine days once, and a PR
	// environment gets a virgin database, so the deploy gate cannot catch one.
	failureKindPredecessor = "20260903055829_audit_log_entity_for_anchor_learned.sql"
)

// failureKindVocabulary is what EXTR-15-01's migration introduced, not the live vocabulary:
// failureKindMigrationGlob pins that one file, so this list is a historical record and does NOT
// move when a later migration widens the CHECK (EXTR-19-03 adds a sixth kind in its own file).
// TestExtractionJobs_FailureKindCheckMirrorsTheConsts is the live source-vs-DB bijection.
var failureKindVocabulary = []string{
	"document_unavailable",
	"extract_failed",
	"page_rows_not_written",
	"pages_not_rendered",
	"text_not_read",
}

func failureKindMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, failureKindMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", failureKindMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1",
			len(matches), failureKindMigrationGlob, matches)
	}
	return matches[0]
}

func failureKindSection(t *testing.T, section string) string {
	t.Helper()
	return auditEntitySectionOf(t, failureKindMigrationName(t), section)
}

func failureKindColumnPresent(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'extraction_jobs' AND column_name = $1`,
		failureKindColumn).Scan(&present); err != nil {
		t.Fatalf("check extraction_jobs.%s presence: %v", failureKindColumn, err)
	}
	return present
}

// extractionJobsColumns reads the table's shape in ordinal order, floored: a walk that read
// nothing would make every comparison below vacuous.
func extractionJobsColumns(t *testing.T, ctx context.Context, tx pgx.Tx) []string {
	t.Helper()
	rows, err := tx.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'extraction_jobs'
		   ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("read extraction_jobs columns: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read extraction_jobs columns: %v", err)
	}
	if len(out) < 10 {
		t.Fatalf("extraction_jobs reports %d column(s) (%v); the comparisons above would be vacuous", len(out), out)
	}
	return out
}

// FK-3 half one (AC-2, no DB): the file exists, sorts after its predecessor, and its Up names
// every kind the extraction vocabulary declares. Needs no database, so a misordered timestamp
// is caught on every CI job rather than only the ones with Postgres attached.
func TestExtractionFailureKind_MigrationFileIsOrderedAndComplete(t *testing.T) {
	name := failureKindMigrationName(t)
	if name <= failureKindPredecessor {
		t.Errorf("%s sorts at or before %s; goose applies in filename order and a PR environment gets a virgin database, so the gate cannot catch a reverse stamp",
			name, failureKindPredecessor)
	}

	up := failureKindSection(t, "Up")
	down := failureKindSection(t, "Down")

	if !strings.Contains(up, failureKindColumn) {
		t.Fatalf("%s Up never names %s, so the assertions below are vacuous:\n%s", name, failureKindColumn, up)
	}

	// Set equality against the whole vocabulary, read out of the quoted literals: five values
	// is satisfied by five values that are not the five audit.go declares.
	seen := map[string]bool{}
	var got []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(up, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		got = append(got, m[1])
	}
	slices.Sort(got)
	if !slices.Equal(got, failureKindVocabulary) {
		t.Errorf("%s Up quotes %v, want exactly %v -- a CHECK written against the old four refuses a kind the worker already produces",
			name, got, failureKindVocabulary)
	}

	if !strings.Contains(strings.ToUpper(down), "DROP COLUMN") || !strings.Contains(down, failureKindColumn) {
		t.Errorf("%s Down does not drop %s:\n%s", name, failureKindColumn, down)
	}
}

// FK-3 half two (AC-2): the shipped Down body itself removes the column and the shipped Up
// body puts it back. CI's migrate-reset + migrate-up round trip cannot catch an over-broad
// Down -- the re-up restores whatever it took with it.
func TestExtractionFailureKind_DownDropsExactlyThatColumn(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx) // rolled back on cleanup

	if !failureKindColumnPresent(t, ctx, tx) {
		t.Fatalf("column extraction_jobs.%s does not exist yet", failureKindColumn)
	}
	before := extractionJobsColumns(t, ctx, tx)

	if _, err := tx.Exec(ctx, failureKindSection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
	if failureKindColumnPresent(t, ctx, tx) {
		t.Errorf("extraction_jobs.%s survived the migration's own Down", failureKindColumn)
	}
	afterDown := extractionJobsColumns(t, ctx, tx)
	want := slices.DeleteFunc(slices.Clone(before), func(c string) bool { return c == failureKindColumn })
	if !slices.Equal(afterDown, want) {
		t.Errorf("columns after Down = %v, want %v -- the Down took a column it does not own", afterDown, want)
	}

	if _, err := tx.Exec(ctx, failureKindSection(t, "Up")); err != nil {
		t.Fatalf("Up body failed after its own Down: %v", err)
	}
	if !failureKindColumnPresent(t, ctx, tx) {
		t.Fatalf("extraction_jobs.%s is absent after the Up body replayed", failureKindColumn)
	}
	if afterUp := extractionJobsColumns(t, ctx, tx); !slices.Equal(afterUp, before) {
		t.Errorf("columns after the Down/Up round trip = %v, want the original %v", afterUp, before)
	}
}

// ---- EXTR-19-03: the sixth kind's own migration -----------------------------------------

const (
	// The kind EXTR-19-03 adds. A boxless document whose layout identity could not be stored
	// is its own stage, so it is its own kind.
	layoutNotWrittenKind = "layout_not_written"

	// Found-needle control for the scan below: in the CHECK since the column existed.
	failureKindShippedNeedle = "extract_failed"

	// Anti-control: internal/submission's vocabulary, on invoices.failure_kind. A scan anchored
	// on the column alone would select 20260805075045_invoices_failure_kind.sql the day that
	// file sorts last, and pin the wrong list without a single assertion changing.
	failureKindForeignNeedle = "payload_not_built"

	// The last migration to declare this vocabulary before EXTR-19-03.
	layoutNotWrittenPredecessor = "20260904154655_extraction_jobs_failure_kind.sql"
)

// failureKindInList captures the quoted values of an `extraction_jobs` `failure_kind IN (...)`
// list, or nil when the section declares none. Anchored on the TABLE as well as the column.
var failureKindInListRE = regexp.MustCompile(`(?is)failure_kind\s+IN\s*\(([^)]*)\)`)

func failureKindInList(section string) []string {
	if !strings.Contains(section, "extraction_jobs") {
		return nil
	}
	m := failureKindInListRE.FindStringSubmatch(section)
	if m == nil {
		return nil
	}
	var out []string
	for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out
}

// latestFailureKindDeclaration returns the lexicographically last migration whose Up section
// declares the extraction_jobs failure_kind vocabulary, and that vocabulary. Filenames lead with
// the goose stamp, so lexical order is apply order and the last declaration is the live one.
// Up-scoped on purpose: EXTR-19-03's own Down re-states the five-value list.
func latestFailureKindDeclaration(t *testing.T) (string, []string) {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob *.sql in migrations.FS: %v", err)
	}
	// Population floor: a mis-resolved FS returning nothing would report every absence below
	// clean. 58 files shipped when this was written.
	if len(names) <= 50 {
		t.Fatalf("the migrations scan read %d .sql file(s), want more than 50", len(names))
	}
	slices.Sort(names)

	var gotName string
	var got []string
	var candidates []string
	for _, name := range names {
		kinds := failureKindInList(auditEntitySectionOf(t, name, "Up"))
		if kinds == nil {
			continue
		}
		candidates = append(candidates, name)
		gotName, got = name, kinds
	}
	if len(candidates) == 0 {
		t.Fatalf("no migration Up declares an extraction_jobs failure_kind IN-list; the assertions below would be vacuous")
	}
	// The anchor matches real shipped SQL, not only this test's fixtures.
	if !slices.Contains(candidates, layoutNotWrittenPredecessor) {
		t.Fatalf("the scan did not select %s among %v; its CHECK is the one this vocabulary came from",
			layoutNotWrittenPredecessor, candidates)
	}
	if !slices.Contains(got, failureKindShippedNeedle) {
		t.Fatalf("%s declares %v, which does not include %q -- the scan read something that is not this CHECK",
			gotName, got, failureKindShippedNeedle)
	}
	if slices.Contains(got, failureKindForeignNeedle) {
		t.Fatalf("%s declares %v, which includes %q -- the scan selected the invoices CHECK",
			gotName, got, failureKindForeignNeedle)
	}
	return gotName, got
}

// failureKindCheckValues reads the live CHECK's IN-list off extraction_jobs, on tx.
func failureKindCheckValues(t *testing.T, ctx context.Context, tx pgx.Tx) []string {
	t.Helper()
	var def string
	if err := tx.QueryRow(ctx,
		`SELECT pg_get_constraintdef(c.oid)
		   FROM pg_constraint c
		   JOIN pg_class rel ON rel.oid = c.conrelid
		   JOIN pg_namespace n ON n.oid = rel.relnamespace
		  WHERE n.nspname = 'public' AND rel.relname = 'extraction_jobs'
		    AND c.contype = 'c' AND pg_get_constraintdef(c.oid) LIKE '%failure_kind%'`).Scan(&def); err != nil {
		t.Fatalf("read the failure_kind CHECK on extraction_jobs: %v", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(def, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("the CHECK %q quotes no value; every comparison against it would be vacuous", def)
	}
	slices.Sort(out)
	return out
}

// AC-3/AC-4 (no DB): the vocabulary is read from the WHOLE migrations directory, so a later
// migration that widens the CHECK is seen. The Go twin of documentRun.test.ts's TS15-1 guard,
// which was pinned to one filename and would have stayed green through this story.
func TestExtractionFailureKind_LayoutNotWrittenIsTheLatestDeclaration(t *testing.T) {
	name, got := latestFailureKindDeclaration(t)

	if !slices.Contains(got, layoutNotWrittenKind) {
		t.Fatalf("the latest declaration is %s, whose Up declares %v -- no migration in migrations/ admits %q",
			name, got, layoutNotWrittenKind)
	}
	if name <= layoutNotWrittenPredecessor {
		t.Errorf("the latest declaration is %s, which sorts at or before %s; goose applies in filename order",
			name, layoutNotWrittenPredecessor)
	}
	want := []string{
		"document_unavailable", "extract_failed", layoutNotWrittenKind,
		"page_rows_not_written", "pages_not_rendered", "text_not_read",
	}
	sorted := slices.Clone(got)
	slices.Sort(sorted)
	if !slices.Equal(sorted, want) {
		t.Errorf("%s Up declares %v, want exactly %v", name, sorted, want)
	}
}

// AC-6: the migration round-trips, and its Down clears the sixth value off every row before it
// narrows the CHECK back. Down runs with NO tenant context, the way goose runs it: extraction_jobs
// is FORCE ROW LEVEL SECURITY and owned by invoice_migrator, so a clearing UPDATE written without
// that in mind touches zero rows while the narrowing ADD CONSTRAINT still validates all of them.
func TestExtractionFailureKind_LayoutNotWrittenMigrationRoundTrips(t *testing.T) {
	ctx := t.Context()
	name, kinds := latestFailureKindDeclaration(t)
	if !slices.Contains(kinds, layoutNotWrittenKind) {
		t.Fatalf("the latest declaration is %s, whose Up declares %v -- EXTR-19-03's migration is not in migrations/, so there is no round trip to run",
			name, kinds)
	}
	up := auditEntitySectionOf(t, name, "Up")
	down := auditEntitySectionOf(t, name, "Down")

	tx := migratorTx(t, ctx) // rolled back on cleanup

	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("%s Up failed: %v", name, err)
	}
	if got := failureKindCheckValues(t, ctx, tx); !slices.Contains(got, layoutNotWrittenKind) {
		t.Fatalf("after Up the CHECK admits %v, which does not include %q", got, layoutNotWrittenKind)
	}

	// A row holding the sixth value, or the Down's clearing step is asserted over nothing.
	tenantID, jobID := uuid.NewString(), ""
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'extr-19-03 round trip')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	var documentID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, repeat('a', 64), 1) RETURNING id`,
		tenantID, "extr-19-03/"+tenantID).Scan(&documentID); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO extraction_jobs (tenant_id, document_id, extractor, extractor_version, state, failure_kind)
		 VALUES ($1, $2, 'mock', 'v1', 'dead_lettered', $3) RETURNING id`,
		tenantID, documentID, layoutNotWrittenKind).Scan(&jobID); err != nil {
		t.Fatalf("seed a job holding %q (the widened CHECK refused it): %v", layoutNotWrittenKind, err)
	}

	// goose holds no tenant context. Clearing it here is what makes the Down's UPDATE face the
	// same row visibility it faces in production.
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', '', true)`); err != nil {
		t.Fatalf("clear tenant context: %v", err)
	}
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("%s Down failed with no tenant context set, which is how goose runs it: %v", name, err)
	}

	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		t.Fatalf("restore tenant context: %v", err)
	}
	var kind *string
	if err := tx.QueryRow(ctx, `SELECT failure_kind FROM extraction_jobs WHERE id = $1`, jobID).Scan(&kind); err != nil {
		t.Fatalf("read the seeded job back: %v", err)
	}
	if kind != nil {
		t.Errorf("after Down the job still holds failure_kind %q, want NULL", *kind)
	}

	afterDown := failureKindCheckValues(t, ctx, tx)
	if slices.Contains(afterDown, layoutNotWrittenKind) {
		t.Errorf("after Down the CHECK still admits %v, want the five-value list back", afterDown)
	}
	if len(afterDown) != 5 {
		t.Errorf("after Down the CHECK admits %d value(s) (%v), want 5", len(afterDown), afterDown)
	}

	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("%s Up failed on the replay after its own Down: %v", name, err)
	}
	afterUp := failureKindCheckValues(t, ctx, tx)
	if len(afterUp) != 6 || !slices.Contains(afterUp, layoutNotWrittenKind) {
		t.Errorf("after the Up replay the CHECK admits %v, want the six-value list", afterUp)
	}
}

// AC-4 controls for the selector above, on fixtures rather than the directory: the real scan
// selects one file today, so nothing there exercises Up-scoping or the "pick the last" branch.
func TestExtractionFailureKind_DeclarationScanIsScopedAndPicksTheLast(t *testing.T) {
	const sixUpFiveDown = `-- +goose Up
ALTER TABLE extraction_jobs ADD CONSTRAINT c CHECK (failure_kind IS NULL OR failure_kind IN (
    'document_unavailable', 'pages_not_rendered', 'page_rows_not_written',
    'extract_failed', 'text_not_read', 'layout_not_written'));

-- +goose Down
ALTER TABLE extraction_jobs ADD CONSTRAINT c CHECK (failure_kind IS NULL OR failure_kind IN (
    'document_unavailable', 'pages_not_rendered', 'page_rows_not_written',
    'extract_failed', 'text_not_read'));
`
	up, ok := auditEntityUpOf(sixUpFiveDown)
	if !ok {
		t.Fatalf("the fixture carries both goose markers; the splitter did not find them")
	}
	if got := failureKindInList(up); len(got) != 6 || !slices.Contains(got, layoutNotWrittenKind) {
		t.Errorf("the Up section yields %v, want the six-value list -- the Down's five-value list won", got)
	}
	if got := failureKindInList(sixUpFiveDown); len(got) != 6 {
		t.Errorf("an unscoped read yields %d value(s) (%v); the fixture cannot discriminate", len(got), got)
	}

	// The table anchor: the invoices CHECK has the identical shape and must never be selected,
	// however late it sorts.
	const invoicesUp = `ALTER TABLE invoices ADD COLUMN failure_kind text
    CHECK (failure_kind IS NULL OR failure_kind IN
           ('payload_not_built', 'never_acknowledged', 'acknowledged_no_verdict'));`
	if got := failureKindInList(invoicesUp); got != nil {
		t.Errorf("the invoices CHECK yielded %v, want nil -- a later invoices widening would pin the wrong vocabulary", got)
	}

	// The "pick the last" branch: two declarations, and the later stamp wins.
	const fiveOnly = `-- +goose Up
ALTER TABLE extraction_jobs ADD COLUMN failure_kind text CHECK (failure_kind IS NULL OR failure_kind IN (
    'document_unavailable', 'pages_not_rendered', 'page_rows_not_written',
    'extract_failed', 'text_not_read'));

-- +goose Down
ALTER TABLE extraction_jobs DROP COLUMN failure_kind;
`
	files := []struct{ name, sql string }{
		{"20260904154655_a.sql", fiveOnly},
		{"20260905120000_b.sql", sixUpFiveDown},
	}
	var lastName string
	var last []string
	for _, f := range files {
		body, ok := auditEntityUpOf(f.sql)
		if !ok {
			t.Fatalf("fixture %s carries both goose markers; the splitter did not find them", f.name)
		}
		if kinds := failureKindInList(body); kinds != nil {
			lastName, last = f.name, kinds
		}
	}
	if lastName != "20260905120000_b.sql" || len(last) != 6 {
		t.Errorf("the scan settled on %s with %v, want the later file's six-value list", lastName, last)
	}
}
