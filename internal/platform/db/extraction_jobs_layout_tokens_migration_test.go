// Suite for EXTR-19-06's migration: extraction_jobs.layout_tokens, the page-1 token text a
// boxless job retains. One migrator transaction, rolled back, the shape
// extraction_failure_kind_migration_test.go established.
package db_test

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	layoutTokensMigrationGlob = "*_extraction_jobs_layout_tokens.sql"
	layoutTokensColumn        = "layout_tokens"
	layoutTokensConstraint    = "extraction_jobs_layout_tokens_check"

	// The newest migration on this branch. Ours must sort after it: goose applies in filename
	// order and a PR environment gets a virgin database, so no deploy gate catches a reverse
	// stamp.
	layoutTokensPredecessor = "20260905173456_extraction_jobs_layout_not_written.sql"
)

func layoutTokensMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, layoutTokensMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", layoutTokensMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1 -- EXTR-19-06 scaffolds it with `make migrate-create name=extraction_jobs_layout_tokens`",
			len(matches), layoutTokensMigrationGlob, matches)
	}
	return matches[0]
}

func layoutTokensSection(t *testing.T, section string) string {
	t.Helper()
	return auditEntitySectionOf(t, layoutTokensMigrationName(t), section)
}

func layoutTokensColumnPresent(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'extraction_jobs' AND column_name = $1`,
		layoutTokensColumn).Scan(&present); err != nil {
		t.Fatalf("check extraction_jobs.%s presence: %v", layoutTokensColumn, err)
	}
	return present
}

func layoutTokensCheckPresent(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM pg_constraint
		   WHERE conrelid = 'public.extraction_jobs'::regclass AND conname = $1`,
		layoutTokensConstraint).Scan(&present); err != nil {
		t.Fatalf("check %s presence: %v", layoutTokensConstraint, err)
	}
	return present
}

// LT-12 (AC-10, no DB): the file exists, sorts after its predecessor, and its Up declares both
// conjuncts. A size-only CHECK admits the JSON null literal a nil slice marshals to.
func TestExtractionJobsLayoutTokens_MigrationFileIsOrderedAndComplete(t *testing.T) {
	name := layoutTokensMigrationName(t)
	if name <= layoutTokensPredecessor {
		t.Errorf("%s sorts at or before %s", name, layoutTokensPredecessor)
	}

	up := layoutTokensSection(t, "Up")
	down := layoutTokensSection(t, "Down")

	if !strings.Contains(up, layoutTokensColumn) {
		t.Fatalf("%s Up never names %s, so the assertions below are vacuous:\n%s", name, layoutTokensColumn, up)
	}
	for _, needle := range []string{"jsonb_typeof", "'array'", "char_length", "262144"} {
		if !strings.Contains(up, needle) {
			t.Errorf("%s Up never names %s:\n%s", name, needle, up)
		}
	}
	if !strings.Contains(strings.ToUpper(down), "DROP COLUMN") || !strings.Contains(down, layoutTokensColumn) {
		t.Errorf("%s Down does not drop %s:\n%s", name, layoutTokensColumn, down)
	}
}

// LT-13 (AC-10): the shipped bodies themselves. CI's migrate-reset + migrate-up round trip
// cannot catch an over-broad Down -- the re-up restores whatever it took with it.
func TestExtractionJobsLayoutTokens_MigrationRoundTrips(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx) // rolled back on cleanup

	if !layoutTokensColumnPresent(t, ctx, tx) {
		t.Fatalf("column extraction_jobs.%s does not exist yet", layoutTokensColumn)
	}
	if !layoutTokensCheckPresent(t, ctx, tx) {
		t.Errorf("%s is absent; a nullable jsonb column with no CHECK admits an object and a 300 KiB array", layoutTokensConstraint)
	}
	before := extractionJobsColumns(t, ctx, tx)
	forceBefore := extractionJobsForceRLS(t, ctx, tx)
	if !forceBefore {
		t.Fatalf("extraction_jobs does not have FORCE row security before the round trip; the assertion after the Down would be vacuous")
	}

	if _, err := tx.Exec(ctx, layoutTokensSection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
	if layoutTokensColumnPresent(t, ctx, tx) {
		t.Errorf("extraction_jobs.%s survived the migration's own Down", layoutTokensColumn)
	}
	if layoutTokensCheckPresent(t, ctx, tx) {
		t.Errorf("%s survived the Down that dropped its column", layoutTokensConstraint)
	}
	afterDown := extractionJobsColumns(t, ctx, tx)
	want := slices.DeleteFunc(slices.Clone(before), func(c string) bool { return c == layoutTokensColumn })
	if !slices.Equal(afterDown, want) {
		t.Errorf("columns after Down = %v, want %v -- the Down took a column it does not own", afterDown, want)
	}
	// Pure DDL: RLS constrains DML, not ALTER TABLE, so this Down needs no NO FORCE/FORCE
	// toggle and must not leave one off.
	if !extractionJobsForceRLS(t, ctx, tx) {
		t.Errorf("extraction_jobs lost FORCE row security across the Down")
	}

	if _, err := tx.Exec(ctx, layoutTokensSection(t, "Up")); err != nil {
		t.Fatalf("Up body failed after its own Down: %v", err)
	}
	if !layoutTokensColumnPresent(t, ctx, tx) {
		t.Fatalf("extraction_jobs.%s is absent after the Up body replayed", layoutTokensColumn)
	}
	if !layoutTokensCheckPresent(t, ctx, tx) {
		t.Errorf("%s is absent after the Up body replayed", layoutTokensConstraint)
	}
	if !extractionJobsForceRLS(t, ctx, tx) {
		t.Errorf("extraction_jobs lost FORCE row security across the Down/Up round trip")
	}
	// Set equality, not ordinal: a DROP + re-ADD moves the column to the end of the ordinal
	// list, which says nothing about whether the Down took a column it does not own.
	afterUp := slices.Sorted(slices.Values(extractionJobsColumns(t, ctx, tx)))
	if wantSet := slices.Sorted(slices.Values(before)); !slices.Equal(afterUp, wantSet) {
		t.Errorf("columns after the Down/Up round trip = %v, want the original set %v", afterUp, wantSet)
	}
}

func extractionJobsForceRLS(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var forced bool
	if err := tx.QueryRow(ctx,
		`SELECT relforcerowsecurity FROM pg_class WHERE oid = 'public.extraction_jobs'::regclass`).Scan(&forced); err != nil {
		t.Fatalf("read relforcerowsecurity for extraction_jobs: %v", err)
	}
	return forced
}
