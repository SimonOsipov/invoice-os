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

// failureKindVocabulary is internal/extraction/audit.go's five kinds. Spelled here rather than
// imported: internal/platform/db must not depend on a caller package.
// TestExtractionJobs_FailureKindCheckMirrorsTheConsts is the half that reads them from source.
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
