package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Refused on top of documentTypeRefused: whitespace, case, and separator variants of a stored name.
var documentTypeNearMisses = []string{
	"receipt ", " receipt", "RECEIPT", "Credit Note", "credit_note", "purchase-order", "tax_invoice",
}

// The shipped Up, replayed over a row that predates it: the row reads NULL, and the file's own
// CHECK admits the seven and refuses every near miss. The live-schema test cannot see the file.
func TestExtractionJobsDocumentType_TheReplayedUpAdmitsTheSevenAndRefusesNearMisses(t *testing.T) {
	ctx := t.Context()
	tx := migratorTx(t, ctx)

	if !documentTypeColumnPresent(t, ctx, tx) || !documentTypeCheckPresent(t, ctx, tx) {
		t.Fatalf("extraction_jobs.%s or %s absent before the replay (run `make migrate-up`)", documentTypeColumn, documentTypeConstraint)
	}
	if _, err := tx.Exec(ctx, documentTypeSection(t, "Down")); err != nil {
		t.Fatalf("Down body: %v", err)
	}
	if documentTypeColumnPresent(t, ctx, tx) {
		t.Fatalf("extraction_jobs.%s survived the Down; the replay below would not be the file's Up", documentTypeColumn)
	}
	jobID := seedDocumentTypeJob(t, ctx, tx)
	if _, err := tx.Exec(ctx, documentTypeSection(t, "Up")); err != nil {
		t.Fatalf("Up body over an existing row: %v", err)
	}

	var existing *string
	if err := tx.QueryRow(ctx, `SELECT document_type FROM extraction_jobs WHERE id = $1`, jobID).Scan(&existing); err != nil {
		t.Fatalf("read the pre-existing job: %v", err)
	}
	if existing != nil {
		t.Errorf("a job written before the Up reads %q, want NULL (no backfill, no default)", *existing)
	}

	write := func(v any) error {
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatalf("open savepoint: %v", err)
		}
		tag, err := sp.Exec(ctx, `UPDATE extraction_jobs SET document_type = $1 WHERE id = $2`, v, jobID)
		if err != nil {
			_ = sp.Rollback(ctx)
			return err
		}
		if tag.RowsAffected() != 1 {
			t.Errorf("SET document_type = %v affected %d rows, want 1", v, tag.RowsAffected())
		}
		if err := sp.Commit(ctx); err != nil {
			t.Fatalf("release savepoint: %v", err)
		}
		return nil
	}

	if err := write((*string)(nil)); err != nil {
		t.Errorf("replayed Up refuses NULL: %v", err)
	}
	if len(documentTypeStored) != 7 {
		t.Fatalf("documentTypeStored holds %d names, want 7", len(documentTypeStored))
	}
	for _, v := range documentTypeStored {
		if err := write(v); err != nil {
			t.Errorf("replayed Up refuses %q: %v", v, err)
		}
	}
	for _, v := range append(append([]string{}, documentTypeRefused...), documentTypeNearMisses...) {
		err := write(v)
		if pgCode(err) != "23514" || pgConstraint(err) != documentTypeConstraint {
			t.Errorf("SET document_type = %q after the replayed Up: %v; want 23514 on %s", v, err, documentTypeConstraint)
		}
	}
}

// invoice_app writes the column in its own tenant with no new grant, and not in another's.
func TestRLS_ExtractionJobsDocumentTypeIsTenantScoped(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	docA, cleanupDoc := seedDocument(t, h.tenantA, "document-type/a.pdf")
	defer cleanupDoc()
	jobA, cleanupJob := seedExtractionJob(t, h.tenantA, docA)
	defer cleanupJob()

	update := func(tenant, v string) (int64, error) {
		var n int64
		err := db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, `UPDATE extraction_jobs SET document_type = $1 WHERE id = $2`, v, jobA)
			n = tag.RowsAffected()
			return e
		})
		return n, err
	}

	if n, err := update(h.tenantA, "receipt"); err != nil || n != 1 {
		t.Fatalf("tenant A SET document_type = 'receipt': rows %d, err %v; want 1 row (positive control)", n, err)
	}
	if _, err := update(h.tenantA, "tax invoice"); pgCode(err) != "23514" || pgConstraint(err) != documentTypeConstraint {
		t.Errorf("tenant A SET document_type = 'tax invoice': %v; want 23514 on %s", err, documentTypeConstraint)
	}

	if n, err := update(h.tenantB, "proforma"); err != nil || n != 0 {
		t.Errorf("tenant B UPDATE of A's job: rows %d, err %v; want 0 rows, no error", n, err)
	}
	var crossRead *string
	err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT document_type FROM extraction_jobs WHERE id = $1`, jobA).Scan(&crossRead)
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("tenant B read of A's document_type: %v, %v; want pgx.ErrNoRows", crossRead, err)
	}

	var after *string
	if err := h.super.QueryRow(ctx, `SELECT document_type FROM extraction_jobs WHERE id = $1`, jobA).Scan(&after); err != nil {
		t.Fatalf("read back document_type: %v", err)
	}
	if after == nil || *after != "receipt" {
		t.Errorf("document_type after tenant B's UPDATE = %v, want \"receipt\"", after)
	}
}
