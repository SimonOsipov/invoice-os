// M4-03-01 (task-102): red AC tests for the write-side import_batch_id
// change, authored BEFORE Store.Create's INSERT is wired to bind it
// (Stage 2.5 — CreateInput.ImportBatchID exists as a field, per invoice.go,
// but store.go's INSERT does not yet reference it). Reuses the
// dbTestPools/seedTenant/seedEntity/mustCount harness from store_test.go
// (same package, same file).
//
// Spec-to-test map (Test Specs table, M4-03-01 story / task-102):
//
//	INV-IMPBATCH-01 TestStoreCreate_ImportBatchIDPersistsAndRoundTrips
//	INV-IMPBATCH-02 TestStoreCreate_NilImportBatchIDPersistsNull
//	INV-IMPBATCH-03 TestStoreCreate_NonExistentImportBatchIDRejected
//
// Run: `make test-rls` (or `make test-audit`), or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run 'TestStoreCreate_.*ImportBatch' -v ./internal/invoice/...
package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedImportBatch inserts one import_batches row for tenantID/entityID as
// the superuser (BYPASSRLS) and registers its own cleanup — mirrors
// seedEntity's idiom. Status/counters/errors are left at their column
// DEFAULTs ('pending'/0/0/0/'[]'); INV-IMPBATCH-01..03 only need a valid FK
// target, not a particular batch state.
func seedImportBatch(t *testing.T, super *pgxpool.Pool, tenantID, entityID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO import_batches (tenant_id, entity_id) VALUES ($1, $2) RETURNING id`,
		tenantID, entityID,
	).Scan(&id); err != nil {
		t.Fatalf("seed import_batches: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM import_batches WHERE id = $1`, id)
	})
	return id
}

// INV-IMPBATCH-01: Create with a valid seeded import_batch_id persists that
// FK; a re-read (Get) returns it on Invoice.ImportBatchID.
//
// RED (Stage 2.5, field declared but unwired): Store.Create does not yet bind
// ImportBatchID into the invoices INSERT, so the column persists NULL and
// the assertions below fail on value, not on a compile/setup error — the
// correct behavioral RED for this spec.
func TestStoreCreate_ImportBatchIDPersistsAndRoundTrips(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-IMPBATCH-01 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-IMPBATCH-01 entity")
	batchID := seedImportBatch(t, super, tenantID, entityID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "INV-IMPBATCH-01",
		ImportBatchID: &batchID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.ImportBatchID == nil || *inv.ImportBatchID != batchID {
		t.Fatalf("Create returned import_batch_id = %v, want %q", inv.ImportBatchID, batchID)
	}

	var persisted *string
	if err := super.QueryRow(ctx,
		`SELECT import_batch_id FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&persisted); err != nil {
		t.Fatalf("read back invoice: %v", err)
	}
	if persisted == nil || *persisted != batchID {
		t.Errorf("invoices.import_batch_id read back = %v, want %q", persisted, batchID)
	}

	got, err := store.Get(c, inv.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ImportBatchID == nil || *got.ImportBatchID != batchID {
		t.Errorf("Get: ImportBatchID = %v, want %q", got.ImportBatchID, batchID)
	}
}

// INV-IMPBATCH-02: Create with ImportBatchID nil persists SQL NULL —
// existing behaviour unchanged. This is a keep-green regression guard: it
// may already pass while the field is unwired (an unreferenced column
// defaults to NULL either way), and MUST still pass once Stage 3 wires the
// INSERT.
func TestStoreCreate_NilImportBatchIDPersistsNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-IMPBATCH-02 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-IMPBATCH-02 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "INV-IMPBATCH-02",
		ImportBatchID: nil,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.ImportBatchID != nil {
		t.Errorf("Create returned import_batch_id = %q, want nil", *inv.ImportBatchID)
	}

	var persisted *string
	if err := super.QueryRow(ctx,
		`SELECT import_batch_id FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&persisted); err != nil {
		t.Fatalf("read back invoice: %v", err)
	}
	if persisted != nil {
		t.Errorf("invoices.import_batch_id read back = %q, want NULL", *persisted)
	}
}

// INV-IMPBATCH-03: a non-existent import_batch_id is rejected by the FK
// (23503 -> ErrValidation), same mapping as the existing entity-id path
// (INV-STORE-13); no row commits (atomic rollback).
//
// RED (Stage 2.5, field declared but unwired): the unreferenced field means
// Create ignores the bogus id entirely and SUCCEEDS instead of failing — the
// correct behavioral RED for this spec (not a compile error).
func TestStoreCreate_NonExistentImportBatchIDRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-IMPBATCH-03 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-IMPBATCH-03 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	bogusBatchID := uuid.NewString()
	_, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "INV-IMPBATCH-03",
		ImportBatchID: &bogusBatchID,
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create with a non-existent import_batch_id err = %v, want ErrValidation (23503 foreign_key_violation)", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("invoices rows for tenant after rejected Create = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM line_items WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("line_items rows for tenant after rejected Create = %d, want 0", n)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.created'`, tenantID); n != 0 {
		t.Errorf("audit_log invoice.created rows for tenant after rejected Create = %d, want 0", n)
	}
}
