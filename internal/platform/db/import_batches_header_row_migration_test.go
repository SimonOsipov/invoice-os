// Suite for the import_batches.header_row migration: the 1-based file row an import read its
// column names from. One migrator transaction, rolled back, the shape
// extraction_jobs_layout_tokens_migration_test.go established.
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
	headerRowMigrationGlob = "*_import_batches_header_row.sql"
	headerRowColumn        = "header_row"
	headerRowConstraint    = "import_batches_header_row_check"

	// The newest migration on this branch. Ours must sort after it.
	headerRowPredecessor = "20260914202716_import_mappings.sql"
)

func headerRowMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, headerRowMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", headerRowMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1 -- scaffold it with `make migrate-create name=import_batches_header_row`",
			len(matches), headerRowMigrationGlob, matches)
	}
	return matches[0]
}

func headerRowSection(t *testing.T, section string) string {
	t.Helper()
	return auditEntitySectionOf(t, headerRowMigrationName(t), section)
}

func headerRowColumnPresent(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'import_batches' AND column_name = $1`,
		headerRowColumn).Scan(&present); err != nil {
		t.Fatalf("check import_batches.%s presence: %v", headerRowColumn, err)
	}
	return present
}

func headerRowCheckPresent(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM pg_constraint
		   WHERE conrelid = 'public.import_batches'::regclass AND conname = $1`,
		headerRowConstraint).Scan(&present); err != nil {
		t.Fatalf("check %s presence: %v", headerRowConstraint, err)
	}
	return present
}

// importBatchesColumns reads the table's shape in ordinal order, floored: a walk that read
// nothing would make every comparison below vacuous.
func importBatchesColumns(t *testing.T, ctx context.Context, tx pgx.Tx) []string {
	t.Helper()
	rows, err := tx.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'import_batches'
		   ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("read import_batches columns: %v", err)
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
		t.Fatalf("read import_batches columns: %v", err)
	}
	if len(out) < 5 {
		t.Fatalf("import_batches reports only %d column(s) (%v) -- the walk itself is broken", len(out), out)
	}
	return out
}

func importBatchesForceRLS(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var forced bool
	if err := tx.QueryRow(ctx,
		`SELECT relforcerowsecurity FROM pg_class WHERE oid = 'public.import_batches'::regclass`).Scan(&forced); err != nil {
		t.Fatalf("read relforcerowsecurity for import_batches: %v", err)
	}
	return forced
}

// TestImportBatchesHeaderRow_MigrationFileIsOrderedAndComplete (AC-6, no DB): the file exists,
// sorts after its predecessor, and its Up/Down declare the expected shape.
func TestImportBatchesHeaderRow_MigrationFileIsOrderedAndComplete(t *testing.T) {
	name := headerRowMigrationName(t)
	if name <= headerRowPredecessor {
		t.Errorf("%s sorts at or before %s", name, headerRowPredecessor)
	}

	up := headerRowSection(t, "Up")
	down := headerRowSection(t, "Down")

	if !strings.Contains(up, headerRowColumn) {
		t.Fatalf("%s Up never names %s, so the assertions below are vacuous:\n%s", name, headerRowColumn, up)
	}
	for _, needle := range []string{"ADD COLUMN header_row integer", "header_row >= 1"} {
		if !strings.Contains(up, needle) {
			t.Errorf("%s Up never contains %q:\n%s", name, needle, up)
		}
	}
	for _, needle := range []string{"GRANT", "POLICY"} {
		if strings.Contains(strings.ToUpper(up), needle) {
			t.Errorf("%s Up contains %s -- a new column on an already-FORCE-RLS table needs neither:\n%s", name, needle, up)
		}
	}
	if !strings.Contains(strings.ToUpper(down), "DROP COLUMN") || !strings.Contains(down, headerRowColumn) {
		t.Errorf("%s Down does not drop %s:\n%s", name, headerRowColumn, down)
	}
}

// TestImportBatchesHeaderRow_MigrationRoundTrips (AC-6): the shipped bodies themselves.
func TestImportBatchesHeaderRow_MigrationRoundTrips(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx) // rolled back on cleanup

	if !headerRowColumnPresent(t, ctx, tx) {
		t.Fatalf("column import_batches.%s does not exist yet", headerRowColumn)
	}
	if !headerRowCheckPresent(t, ctx, tx) {
		t.Errorf("%s is absent; a nullable integer column with no CHECK admits 0", headerRowConstraint)
	}
	before := importBatchesColumns(t, ctx, tx)
	forceBefore := importBatchesForceRLS(t, ctx, tx)
	if !forceBefore {
		t.Fatalf("import_batches does not have FORCE row security before the round trip; the assertion after the Down would be vacuous")
	}

	if _, err := tx.Exec(ctx, headerRowSection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
	if headerRowColumnPresent(t, ctx, tx) {
		t.Errorf("import_batches.%s survived the migration's own Down", headerRowColumn)
	}
	if headerRowCheckPresent(t, ctx, tx) {
		t.Errorf("%s survived the Down that dropped its column", headerRowConstraint)
	}
	afterDown := importBatchesColumns(t, ctx, tx)
	want := slices.DeleteFunc(slices.Clone(before), func(c string) bool { return c == headerRowColumn })
	if !slices.Equal(afterDown, want) {
		t.Errorf("columns after Down = %v, want %v -- the Down took a column it does not own", afterDown, want)
	}
	if !importBatchesForceRLS(t, ctx, tx) {
		t.Errorf("import_batches lost FORCE row security across the Down")
	}

	if _, err := tx.Exec(ctx, headerRowSection(t, "Up")); err != nil {
		t.Fatalf("Up body failed after its own Down: %v", err)
	}
	if !headerRowColumnPresent(t, ctx, tx) {
		t.Fatalf("import_batches.%s is absent after the Up body replayed", headerRowColumn)
	}
	if !headerRowCheckPresent(t, ctx, tx) {
		t.Errorf("%s is absent after the Up body replayed", headerRowConstraint)
	}
	if !importBatchesForceRLS(t, ctx, tx) {
		t.Errorf("import_batches lost FORCE row security across the Down/Up round trip")
	}
	afterUp := slices.Sorted(slices.Values(importBatchesColumns(t, ctx, tx)))
	if wantSet := slices.Sorted(slices.Values(before)); !slices.Equal(afterUp, wantSet) {
		t.Errorf("columns after the Down/Up round trip = %v, want the original set %v", afterUp, wantSet)
	}
}
