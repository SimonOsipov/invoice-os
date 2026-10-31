// Suite for the extraction_jobs_debit_note migration: the document_type CHECK grows to eight names.
// The Down restores the seven with NOT VALID, because FORCE RLS hides other tenants' rows from the
// migrator and it cannot clear them.
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
	debitNoteMigrationGlob = "*_extraction_jobs_debit_note.sql"

	// The newest migration on the base branch. Ours must sort after it.
	debitNotePredecessor = "20261030144857_rule_set_v6.sql"
)

func debitNoteMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, debitNoteMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", debitNoteMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1 -- scaffold it with `make migrate-create name=extraction_jobs_debit_note`",
			len(matches), debitNoteMigrationGlob, matches)
	}
	if matches[0] <= debitNotePredecessor {
		t.Fatalf("%s sorts at or before %s", matches[0], debitNotePredecessor)
	}
	return matches[0]
}

func debitNoteSection(t *testing.T, section string) string {
	t.Helper()
	return auditEntityStripComments(auditEntitySectionOf(t, debitNoteMigrationName(t), section))
}

// documentTypeCheckValidated reports whether the named CHECK is validated against existing rows.
func documentTypeCheckValidated(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var validated bool
	if err := tx.QueryRow(ctx,
		`SELECT convalidated FROM pg_constraint WHERE conrelid = 'public.extraction_jobs'::regclass AND conname = $1`,
		documentTypeConstraint).Scan(&validated); err != nil {
		t.Fatalf("read %s: %v", documentTypeConstraint, err)
	}
	return validated
}

// debitNoteWrite runs one UPDATE in a savepoint, so a refusal does not abort the transaction.
func debitNoteWrite(t *testing.T, ctx context.Context, tx pgx.Tx, jobID string, v any) error {
	t.Helper()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("open savepoint: %v", err)
	}
	if _, err := sp.Exec(ctx, `UPDATE extraction_jobs SET document_type = $1 WHERE id = $2`, v, jobID); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		t.Fatalf("release savepoint: %v", err)
	}
	return nil
}

// A debit note row exists when the Down runs: the Down must succeed, keep the row, and refuse a new one.
func TestExtractionJobsDebitNote_DownKeepsARowAndRefusesANewOne(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx)
	up, down := debitNoteSection(t, "Up"), debitNoteSection(t, "Down")

	jobID := seedDocumentTypeJob(t, ctx, tx) // leaves the tenant bound
	// Replaying the Up makes the test independent of whether the dev DB is migrated.
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("Up body failed: %v", err)
	}
	if err := debitNoteWrite(t, ctx, tx, jobID, "debit note"); err != nil {
		t.Fatalf("the Up body's CHECK refuses 'debit note': %v", err)
	}

	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("Down body failed with a 'debit note' row present: %v", err)
	}

	var got *string
	if err := tx.QueryRow(ctx, `SELECT document_type FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
		t.Fatalf("read the job back: %v", err)
	}
	if got == nil || *got != "debit note" {
		t.Errorf("after Down the job holds %v, want 'debit note' (the Down must not rewrite or delete it)", got)
	}
	if !documentTypeCheckPresent(t, ctx, tx) {
		t.Fatalf("%s is absent after the Down", documentTypeConstraint)
	}
	if documentTypeCheckValidated(t, ctx, tx) {
		t.Errorf("%s is validated after the Down; the kept 'debit note' row means it must be NOT VALID", documentTypeConstraint)
	}

	if err := debitNoteWrite(t, ctx, tx, jobID, "debit note"); pgCode(err) != "23514" || pgConstraint(err) != documentTypeConstraint {
		t.Errorf("a new 'debit note' write after Down: %v; want 23514 on %s", err, documentTypeConstraint)
	}
	// Control: the restored seven still write.
	if err := debitNoteWrite(t, ctx, tx, jobID, "credit note"); err != nil {
		t.Errorf("the Down's CHECK refuses 'credit note': %v", err)
	}
}

// Up, Down, Up in one rolled-back transaction: the table's shape does not move.
func TestExtractionJobsDebitNote_MigrationRoundTrips(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx)
	up, down := debitNoteSection(t, "Up"), debitNoteSection(t, "Down")

	before := extractionJobsColumns(t, ctx, tx)
	if !extractionJobsForceRLS(t, ctx, tx) {
		t.Fatalf("extraction_jobs does not have FORCE row security before the round trip; the assertions after it would be vacuous")
	}
	assertShape := func(step string) {
		t.Helper()
		if got := extractionJobsColumns(t, ctx, tx); !slices.Equal(got, before) {
			t.Errorf("columns after %s = %v, want %v", step, got, before)
		}
		if !extractionJobsForceRLS(t, ctx, tx) {
			t.Errorf("extraction_jobs lost FORCE row security after %s", step)
		}
		if !documentTypeCheckPresent(t, ctx, tx) {
			t.Errorf("%s is absent after %s", documentTypeConstraint, step)
		}
	}

	for _, step := range []struct{ name, body string }{{"Up", up}, {"Down", down}, {"the second Up", up}} {
		if _, err := tx.Exec(ctx, step.body); err != nil {
			t.Fatalf("%s body failed: %v", step.name, err)
		}
		assertShape(step.name)
	}

	// After the final Up the CHECK is the eight-name one and is validated.
	jobID := seedDocumentTypeJob(t, ctx, tx)
	if err := debitNoteWrite(t, ctx, tx, jobID, "debit note"); err != nil {
		t.Errorf("after Up, Down, Up the CHECK refuses 'debit note': %v", err)
	}
	if !documentTypeCheckValidated(t, ctx, tx) {
		t.Errorf("%s is NOT VALID after the second Up", documentTypeConstraint)
	}
}
