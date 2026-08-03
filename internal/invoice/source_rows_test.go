// source_rows_test.go: invoices.source_rows, the sheet-row range an import
// captured for one invoice. CreateInput gains the field; Invoice and
// invoiceColumns deliberately do not (TestInvoiceColumns_OmitsSourceRows).
//
// Written RED, before invoices_source_rows.sql or CreateInput.SourceRows
// exist -- see the DOC-02-01 story / task-357 Test Specs table.
package invoice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// pgConstraint extracts the violated CONSTRAINT NAME from err. Several
// negative specs below could be satisfied by EITHER of the two new CHECKs
// (e.g. SourceRows=[1] with no document violates both), so a bare SQLSTATE
// 23514 assertion would pass for the wrong reason.
func pgConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// sourceRowsOf reads the column directly via the superuser pool: it is NOT
// on Invoice/invoiceColumns.
func sourceRowsOf(t *testing.T, super *pgxpool.Pool, invoiceID string) []int {
	t.Helper()
	var out []int
	if err := super.QueryRow(context.Background(),
		`SELECT source_rows FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&out); err != nil {
		t.Fatalf("read invoices.source_rows: %v", err)
	}
	return out
}

// sourceRowsIsNull asserts the boolean directly -- a '{}' also scans into a
// non-nil empty []int, so scanning into a slice cannot distinguish NULL from
// empty the way SQL's own IS NULL can.
func sourceRowsIsNull(t *testing.T, super *pgxpool.Pool, invoiceID string) bool {
	t.Helper()
	var isNull bool
	if err := super.QueryRow(context.Background(),
		`SELECT source_rows IS NULL FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&isNull); err != nil {
		t.Fatalf("read invoices.source_rows IS NULL: %v", err)
	}
	return isNull
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestStoreCreate_PersistsSourceRows (AC-2): the sheet rows an import
// supplies land on the row, in order.
func TestStoreCreate_PersistsSourceRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 source-rows tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 source-rows entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:         entityID,
		InvoiceNumber:    "DOC-02-01-ROWS-1",
		SourceDocumentID: &documentID,
		SourceRows:       []int{2, 3, 4},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := sourceRowsOf(t, super, inv.ID)
	if len(got) != 3 {
		t.Fatalf("source_rows length = %d, want 3 (got %v)", len(got), got)
	}
	if want := []int{2, 3, 4}; !intSliceEqual(got, want) {
		t.Errorf("source_rows = %v, want %v", got, want)
	}
}

// TestStoreCreate_NilSourceRowsIsNull (AC-3): an unset SourceRows must leave
// the column SQL NULL, never '{}' -- asserted via IS NULL, not a slice scan.
func TestStoreCreate_NilSourceRowsIsNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 nil-rows tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 nil-rows entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:         entityID,
		InvoiceNumber:    "DOC-02-01-ROWS-2",
		SourceDocumentID: &documentID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !sourceRowsIsNull(t, super, inv.ID) {
		t.Error("source_rows IS NULL = false, want true for an unset SourceRows (must not default to '{}')")
	}
}

// TestStoreCreate_EmptyNonNilSourceRowsRejected (AC-3): a non-nil but empty
// SourceRows is structurally unreachable from the importer (sheetRows never
// returns it), but the CHECK must still refuse it if ever supplied -- this
// is the exact defect the corrected migration closes (array_length('{}',1)
// is NULL, and a NULL CHECK is satisfied).
func TestStoreCreate_EmptyNonNilSourceRowsRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 empty-rows tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 empty-rows entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const invoiceNumber = "DOC-02-01-ROWS-3"
	_, err := store.Create(c, CreateInput{
		EntityID:         entityID,
		InvoiceNumber:    invoiceNumber,
		SourceDocumentID: &documentID,
		SourceRows:       []int{},
	})
	if pgCode(err) != "23514" {
		t.Fatalf("Create(SourceRows=[]int{}) err = %v (code %q), want 23514", err, pgCode(err))
	}
	if got := pgConstraint(err); got != "invoices_source_rows_are_sheet_rows" {
		t.Errorf("ConstraintName = %q, want invoices_source_rows_are_sheet_rows", got)
	}

	var n int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM invoices WHERE entity_id = $1 AND invoice_number = $2`, entityID, invoiceNumber,
	).Scan(&n); err != nil {
		t.Fatalf("count invoices: %v", err)
	}
	if n != 0 {
		t.Errorf("invoices row count for %q = %d, want 0 (rejected create must not partially write)", invoiceNumber, n)
	}
}

// TestStoreCreate_SourceRowsWithoutDocumentRejected (AC-3): a range without
// a document is an orphan claim about a file we do not hold.
func TestStoreCreate_SourceRowsWithoutDocumentRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 no-doc-rows tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 no-doc-rows entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	_, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "DOC-02-01-ROWS-4",
		SourceRows:    []int{2},
	})
	if pgCode(err) != "23514" {
		t.Fatalf("Create(SourceRows without document) err = %v (code %q), want 23514", err, pgCode(err))
	}
	if got := pgConstraint(err); got != "invoices_source_rows_requires_document" {
		t.Errorf("ConstraintName = %q, want invoices_source_rows_requires_document", got)
	}
}

// TestStoreCreate_SourceRowBelowTwoRejected (AC-3): header is sheet row 1,
// so a data row is always >= 2. A valid document is present, so this must
// fail on the sheet-rows CHECK, not the requires-document one.
func TestStoreCreate_SourceRowBelowTwoRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 below-two tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 below-two entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	_, err := store.Create(c, CreateInput{
		EntityID:         entityID,
		InvoiceNumber:    "DOC-02-01-ROWS-5",
		SourceDocumentID: &documentID,
		SourceRows:       []int{1},
	})
	if pgCode(err) != "23514" {
		t.Fatalf("Create(SourceRows=[1]) err = %v (code %q), want 23514", err, pgCode(err))
	}
	if got := pgConstraint(err); got != "invoices_source_rows_are_sheet_rows" {
		t.Errorf("ConstraintName = %q, want invoices_source_rows_are_sheet_rows", got)
	}
}

// TestStoreCreate_NullElementSourceRowsRejected (AC-3): pgx cannot express a
// NULL array element from a Go []int, so this bypasses Store.Create and
// writes the literal '{2,NULL}' directly as the superuser -- proving the
// CHECK itself (not merely the Go-side type) refuses a NULL element (2 <=
// ALL over a NULL element is NULL, and a bare ALL would let it through).
func TestStoreCreate_NullElementSourceRowsRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-01 null-elem tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-01 null-elem entity")
	documentID := seedDocument(t, super, tenantID)

	_, err := super.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, source_document_id, source_rows)
		 VALUES ($1, $2, $3, $4, '{2,NULL}')`,
		tenantID, entityID, "DOC-02-01-ROWS-6", documentID,
	)
	if pgCode(err) != "23514" {
		t.Fatalf("direct insert with source_rows='{2,NULL}' err = %v (code %q), want 23514", err, pgCode(err))
	}
	if got := pgConstraint(err); got != "invoices_source_rows_are_sheet_rows" {
		t.Errorf("ConstraintName = %q, want invoices_source_rows_are_sheet_rows", got)
	}
}

// TestInvoiceColumns_OmitsSourceRows (AC-4): mirrors
// TestInvoiceColumns_OmitsSourceDocumentID (source_document_test.go), same
// vacuity guard -- import_batch_id must still be present so the negative
// assertion above it cannot pass vacuously.
func TestInvoiceColumns_OmitsSourceRows(t *testing.T) {
	if strings.Contains(invoiceColumns, "source_rows") {
		t.Errorf("invoiceColumns gained source_rows — it widens Invoice and the MBS fingerprint payload:\n%s", invoiceColumns)
	}
	if strings.Contains(invoiceColumns, "source_document_id") {
		t.Errorf("invoiceColumns gained source_document_id — it widens Invoice and the MBS fingerprint payload:\n%s", invoiceColumns)
	}
	if !strings.Contains(invoiceColumns, "import_batch_id") {
		t.Fatal("invoiceColumns no longer mentions import_batch_id — the check above passed vacuously")
	}
}

// TestRLS_InvoicesSourceRowsCrossTenantRefused (AC-6): source_rows inherits
// invoices' existing tenant_isolation policy -- a cross-tenant SELECT
// returns zero rows, and a re-read under the OWNING tenant's own GUC proves
// the zero-rows result was RLS, not a missing/blank row.
func TestRLS_InvoicesSourceRowsCrossTenantRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "DOC-02-01 rls tenant A")
	tenantB := seedTenant(t, super, "DOC-02-01 rls tenant B")
	entityB := seedEntity(t, super, tenantB, "DOC-02-01 rls B entity")
	documentB := seedDocument(t, super, tenantB)

	store := NewStore(app)
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	invB, err := store.Create(cB, CreateInput{
		EntityID:         entityB,
		InvoiceNumber:    "DOC-02-01-RLS-B",
		SourceDocumentID: &documentB,
		SourceRows:       []int{2, 3},
	})
	if err != nil {
		t.Fatalf("Create (as tenant B): %v", err)
	}

	// Negative: tenant A's GUC sees zero rows for B's invoice id.
	err = db.WithinTenantTx(ctx, app, tenantA, func(tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE id = $1 AND source_rows IS NOT NULL`, invB.ID).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Errorf("rows visible to tenant A for tenant B's invoice = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant A visibility check): %v", err)
	}

	// Positive: re-read under B's OWN GUC -- proves the zero-rows result
	// above was RLS, not the row/value being absent.
	err = db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		var got []int
		if e := tx.QueryRow(ctx, `SELECT source_rows FROM invoices WHERE id = $1`, invB.ID).Scan(&got); e != nil {
			return e
		}
		if want := []int{2, 3}; !intSliceEqual(got, want) {
			t.Errorf("source_rows visible to tenant B (own GUC) = %v, want %v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant B visibility check): %v", err)
	}
}
