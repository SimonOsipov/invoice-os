// Suite for the extraction_jobs.ai_text_truncated migration: the flag a tenant job
// records when the AI read cut text. One migrator transaction, rolled back, the shape of
// extraction_jobs_document_type_migration_test.go.
package db_test

import (
	"context"
	"io/fs"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	aiTextTruncatedMigrationGlob = "*_extraction_jobs_ai_text_truncated.sql"
	aiTextTruncatedColumn        = "ai_text_truncated"
)

func aiTextTruncatedMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, aiTextTruncatedMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", aiTextTruncatedMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1 -- scaffold it with `make migrate-create name=extraction_jobs_ai_text_truncated`",
			len(matches), aiTextTruncatedMigrationGlob, matches)
	}
	return matches[0]
}

func aiTextTruncatedSection(t *testing.T, section string) string {
	t.Helper()
	return auditEntityStripComments(auditEntitySectionOf(t, aiTextTruncatedMigrationName(t), section))
}

func aiTextTruncatedColumnPresent(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'extraction_jobs' AND column_name = $1`,
		aiTextTruncatedColumn).Scan(&present); err != nil {
		t.Fatalf("check extraction_jobs.%s presence: %v", aiTextTruncatedColumn, err)
	}
	return present
}

// aiTextTruncatedValue reads the flag; a read of a missing column fails here with the SQL error.
func aiTextTruncatedValue(t *testing.T, ctx context.Context, tx pgx.Tx, jobID string) bool {
	t.Helper()
	var got bool
	if err := tx.QueryRow(ctx, `SELECT ai_text_truncated FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
		t.Fatalf("read ai_text_truncated of job %s: %v", jobID, err)
	}
	return got
}

// A job row older than the Up reads false, and the column is boolean NOT NULL DEFAULT false.
func TestExtractionJobsAITextTruncated_IsNotNullDefaultFalse(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx) // rolled back on cleanup

	// Run the file's own Down first when the column is already there, so the job below
	// predates the Up whether or not the dev database has it applied.
	if aiTextTruncatedColumnPresent(t, ctx, tx) {
		if _, err := tx.Exec(ctx, aiTextTruncatedSection(t, "Down")); err != nil {
			t.Fatalf("Down body failed: %v", err)
		}
	}
	jobID := seedDocumentTypeJob(t, ctx, tx)
	if _, err := tx.Exec(ctx, aiTextTruncatedSection(t, "Up")); err != nil {
		t.Fatalf("Up body failed: %v", err)
	}

	var dataType, nullable string
	var def *string
	if err := tx.QueryRow(ctx,
		`SELECT data_type, is_nullable, column_default FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'extraction_jobs' AND column_name = $1`,
		aiTextTruncatedColumn).Scan(&dataType, &nullable, &def); err != nil {
		t.Fatalf("read extraction_jobs.%s from information_schema: %v", aiTextTruncatedColumn, err)
	}
	if dataType != "boolean" || nullable != "NO" || def == nil || *def != "false" {
		t.Errorf("extraction_jobs.%s is %s nullable=%s default=%v, want boolean, NO, false", aiTextTruncatedColumn, dataType, nullable, def)
	}
	if aiTextTruncatedValue(t, ctx, tx, jobID) {
		t.Errorf("a job older than the Up reads ai_text_truncated true, want false")
	}
}

// The shipped Down drops only its column, and the Up re-adds it with existing rows false.
func TestExtractionJobsAITextTruncated_DownAndUp(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx) // rolled back on cleanup

	if !aiTextTruncatedColumnPresent(t, ctx, tx) {
		t.Fatalf("column extraction_jobs.%s does not exist (is the migration applied? run `make migrate-up`)", aiTextTruncatedColumn)
	}
	jobID := seedDocumentTypeJob(t, ctx, tx)
	if _, err := tx.Exec(ctx, `UPDATE extraction_jobs SET ai_text_truncated = true WHERE id = $1`, jobID); err != nil {
		t.Fatalf("set the flag on job %s: %v", jobID, err)
	}
	before := extractionJobsColumns(t, ctx, tx)

	if _, err := tx.Exec(ctx, aiTextTruncatedSection(t, "Down")); err != nil {
		t.Fatalf("Down body failed: %v", err)
	}
	if aiTextTruncatedColumnPresent(t, ctx, tx) {
		t.Errorf("extraction_jobs.%s survived the migration's own Down", aiTextTruncatedColumn)
	}
	want := slices.DeleteFunc(slices.Clone(before), func(c string) bool { return c == aiTextTruncatedColumn })
	if afterDown := extractionJobsColumns(t, ctx, tx); !slices.Equal(afterDown, want) {
		t.Errorf("columns after Down = %v, want %v -- the Down took a column it does not own", afterDown, want)
	}

	if _, err := tx.Exec(ctx, aiTextTruncatedSection(t, "Up")); err != nil {
		t.Fatalf("Up body failed after its own Down: %v", err)
	}
	if !aiTextTruncatedColumnPresent(t, ctx, tx) {
		t.Fatalf("extraction_jobs.%s is absent after the Up body replayed", aiTextTruncatedColumn)
	}
	if aiTextTruncatedValue(t, ctx, tx, jobID) {
		t.Errorf("the existing job reads ai_text_truncated true after Down/Up, want false")
	}
	afterUp := slices.Sorted(slices.Values(extractionJobsColumns(t, ctx, tx)))
	if wantSet := slices.Sorted(slices.Values(before)); !slices.Equal(afterUp, wantSet) {
		t.Errorf("columns after the Down/Up round trip = %v, want the original set %v", afterUp, wantSet)
	}
}
