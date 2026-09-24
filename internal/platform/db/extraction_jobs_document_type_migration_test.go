// Suite for the extraction_jobs.document_type migration: the non-invoice type the worker read a
// document as. One migrator transaction, rolled back, the shape of
// import_batches_header_row_migration_test.go.
package db_test

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	documentTypeMigrationGlob = "*_extraction_jobs_document_type.sql"
	documentTypeColumn        = "document_type"
	documentTypeConstraint    = "extraction_jobs_document_type_check"

	// The newest migration before this one. Ours must sort after it.
	documentTypePredecessor = "20260919155430_import_batches_header_row.sql"
)

// The worker's option names minus "tax invoice", which is never stored.
var documentTypeStored = []string{
	"receipt", "proforma", "quotation", "credit note",
	"delivery note", "statement", "purchase order",
}

// Near misses the CHECK must refuse: the invoice itself, an unknown name, case, and empty.
var documentTypeRefused = []string{"tax invoice", "other", "Receipt", ""}

func documentTypeMigrationName(t *testing.T) string {
	t.Helper()
	matches, err := fs.Glob(migrations.FS, documentTypeMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", documentTypeMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d file(s) matching %s (%v), want exactly 1 -- scaffold it with `make migrate-create name=extraction_jobs_document_type`",
			len(matches), documentTypeMigrationGlob, matches)
	}
	return matches[0]
}

// documentTypeSection strips comments, so a commented-out statement neither satisfies nor trips a scan.
func documentTypeSection(t *testing.T, section string) string {
	t.Helper()
	return auditEntityStripComments(auditEntitySectionOf(t, documentTypeMigrationName(t), section))
}

func documentTypeColumnPresent(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'extraction_jobs' AND column_name = $1`,
		documentTypeColumn).Scan(&present); err != nil {
		t.Fatalf("check extraction_jobs.%s presence: %v", documentTypeColumn, err)
	}
	return present
}

func documentTypeCheckPresent(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) = 1 FROM pg_constraint
		   WHERE conrelid = 'public.extraction_jobs'::regclass AND conname = $1`,
		documentTypeConstraint).Scan(&present); err != nil {
		t.Fatalf("check %s presence: %v", documentTypeConstraint, err)
	}
	return present
}

// seedDocumentTypeJob inserts one tenant, document and job on tx and leaves that tenant set:
// extraction_jobs is FORCE RLS, so the migrator sees the row only under its tenant.
func seedDocumentTypeJob(t *testing.T, ctx context.Context, tx pgx.Tx) string {
	t.Helper()
	tenantID := uuid.NewString()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'document_type check')`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	var documentID, jobID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, repeat('a', 64), 1) RETURNING id`,
		tenantID, "document-type/"+tenantID).Scan(&documentID); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO extraction_jobs (tenant_id, document_id, extractor, extractor_version, state)
		 VALUES ($1, $2, 'mock', 'v1', 'succeeded') RETURNING id`,
		tenantID, documentID).Scan(&jobID); err != nil {
		t.Fatalf("seed extraction job: %v", err)
	}
	return jobID
}

// TestExtractionJobsDocumentType_MigrationFileIsOrderedAndComplete (AC-1, no DB).
func TestExtractionJobsDocumentType_MigrationFileIsOrderedAndComplete(t *testing.T) {
	name := documentTypeMigrationName(t)
	if name <= documentTypePredecessor {
		t.Errorf("%s sorts at or before %s", name, documentTypePredecessor)
	}

	up := documentTypeSection(t, "Up")
	down := documentTypeSection(t, "Down")
	upper := strings.ToUpper(up)

	if !strings.Contains(upper, "ADD COLUMN DOCUMENT_TYPE TEXT") {
		t.Errorf("%s Up never contains %q:\n%s", name, "ADD COLUMN document_type text", up)
	}
	if n := strings.Count(upper, "ADD COLUMN"); n != 1 {
		t.Errorf("%s Up adds %d column(s), want exactly 1:\n%s", name, n, up)
	}
	if n := strings.Count(upper, "CHECK"); n != 1 {
		t.Errorf("%s Up declares %d CHECK(s), want exactly 1:\n%s", name, n, up)
	}
	// The absences mean nothing until the Up is shown to hold the column at all.
	if strings.Contains(up, documentTypeColumn) {
		for _, needle := range []string{"DEFAULT", "NOT NULL", "GRANT", "POLICY"} {
			if strings.Contains(upper, needle) {
				t.Errorf("%s Up contains %s -- the column is nullable with no default, and the table's grant and policy already cover it:\n%s", name, needle, up)
			}
		}
	}
	if !strings.Contains(strings.ToUpper(down), "DROP COLUMN") || !strings.Contains(down, documentTypeColumn) {
		t.Errorf("%s Down does not drop %s:\n%s", name, documentTypeColumn, down)
	}
	for _, needle := range []string{"GRANT", "POLICY"} {
		if strings.Contains(strings.ToUpper(down), needle) {
			t.Errorf("%s Down contains %s:\n%s", name, needle, down)
		}
	}
}

// TestExtractionJobsDocumentType_TheCheckAdmitsTheSevenAndRefusesTheRest (AC-2).
func TestExtractionJobsDocumentType_TheCheckAdmitsTheSevenAndRefusesTheRest(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx) // rolled back on cleanup
	jobID := seedDocumentTypeJob(t, ctx, tx)

	if !documentTypeColumnPresent(t, ctx, tx) {
		t.Fatalf("column extraction_jobs.%s does not exist (is the migration applied? run `make migrate-up`)", documentTypeColumn)
	}

	// Each write runs in a savepoint so a refusal does not abort the rest.
	write := func(v any) (int64, error) {
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatalf("open savepoint: %v", err)
		}
		tag, err := sp.Exec(ctx, `UPDATE extraction_jobs SET document_type = $1 WHERE id = $2`, v, jobID)
		if err != nil {
			_ = sp.Rollback(ctx)
			return 0, err
		}
		if err := sp.Commit(ctx); err != nil {
			t.Fatalf("release savepoint: %v", err)
		}
		return tag.RowsAffected(), nil
	}
	readBack := func() *string {
		var got *string
		if err := tx.QueryRow(ctx, `SELECT document_type FROM extraction_jobs WHERE id = $1`, jobID).Scan(&got); err != nil {
			t.Fatalf("read the seeded job back: %v", err)
		}
		return got
	}

	if n, err := write((*string)(nil)); err != nil || n != 1 {
		t.Errorf("SET document_type = NULL: rows %d, err %v; want 1 row, no error", n, err)
	} else if got := readBack(); got != nil {
		t.Errorf("after SET NULL the job holds %q, want NULL", *got)
	}
	for _, v := range documentTypeStored {
		n, err := write(v)
		if err != nil || n != 1 {
			t.Errorf("SET document_type = %q: rows %d, err %v; want 1 row, no error", v, n, err)
			continue
		}
		if got := readBack(); got == nil || *got != v {
			t.Errorf("after SET %q the job holds %v, want %q", v, got, v)
		}
	}

	for _, v := range documentTypeRefused {
		_, err := write(v)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) {
			t.Errorf("SET document_type = %q: err %v, want SQLSTATE 23514 from %s", v, err, documentTypeConstraint)
			continue
		}
		if pgErr.Code != "23514" || pgErr.ConstraintName != documentTypeConstraint {
			t.Errorf("SET document_type = %q: SQLSTATE %s constraint %q, want 23514 from %s",
				v, pgErr.Code, pgErr.ConstraintName, documentTypeConstraint)
		}
	}
}

// TestExtractionJobsDocumentType_MigrationRoundTrips (AC-3): the shipped bodies themselves.
func TestExtractionJobsDocumentType_MigrationRoundTrips(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx) // rolled back on cleanup

	if !documentTypeColumnPresent(t, ctx, tx) {
		t.Fatalf("column extraction_jobs.%s does not exist (is the migration applied? run `make migrate-up`)", documentTypeColumn)
	}
	if !documentTypeCheckPresent(t, ctx, tx) {
		t.Fatalf("%s is absent before the round trip; the assertions after the Down would be vacuous", documentTypeConstraint)
	}
	before := extractionJobsColumns(t, ctx, tx)
	if !extractionJobsForceRLS(t, ctx, tx) {
		t.Fatalf("extraction_jobs does not have FORCE row security before the round trip; the assertion after the Down would be vacuous")
	}

	if _, err := tx.Exec(ctx, documentTypeSection(t, "Down")); err != nil {
		t.Fatalf("Down body failed: %v", err)
	}
	if documentTypeColumnPresent(t, ctx, tx) {
		t.Errorf("extraction_jobs.%s survived the migration's own Down", documentTypeColumn)
	}
	if documentTypeCheckPresent(t, ctx, tx) {
		t.Errorf("%s survived the Down", documentTypeConstraint)
	}
	want := slices.DeleteFunc(slices.Clone(before), func(c string) bool { return c == documentTypeColumn })
	if afterDown := extractionJobsColumns(t, ctx, tx); !slices.Equal(afterDown, want) {
		t.Errorf("columns after Down = %v, want %v -- the Down took a column it does not own", afterDown, want)
	}
	if !extractionJobsForceRLS(t, ctx, tx) {
		t.Errorf("extraction_jobs lost FORCE row security across the Down")
	}

	if _, err := tx.Exec(ctx, documentTypeSection(t, "Up")); err != nil {
		t.Fatalf("Up body failed after its own Down: %v", err)
	}
	if !documentTypeColumnPresent(t, ctx, tx) {
		t.Errorf("extraction_jobs.%s is absent after the Up body replayed", documentTypeColumn)
	}
	if !documentTypeCheckPresent(t, ctx, tx) {
		t.Errorf("%s is absent after the Up body replayed", documentTypeConstraint)
	}
	afterUp := slices.Sorted(slices.Values(extractionJobsColumns(t, ctx, tx)))
	if wantSet := slices.Sorted(slices.Values(before)); !slices.Equal(afterUp, wantSet) {
		t.Errorf("columns after the Down/Up round trip = %v, want the original set %v", afterUp, wantSet)
	}
}
