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

// TestStoreCreate_CrossTenantImportBatchIDFKBypassesRLS (QA adversarial
// coverage, task-102 Stage 4): pins the ACTUAL (permissive) behavior of the
// $13 FK reference when the caller supplies another tenant's import_batches
// id.
//
// import_batches carries the same FORCE-RLS tenant_isolation policy as every
// other table here (migrations/20260714100953_import_batches.sql), so tenant
// A's app-role connection cannot SELECT/UPDATE tenant B's batch row directly.
// BUT Postgres foreign-key constraint enforcement is documented to run
// internally, NOT subject to RLS (the referencing side's FK trigger checks
// referential existence with row security effectively off) -- so the
// invoices.import_batch_id -> import_batches(id) FK on Store.Create's INSERT
// validates against ALL tenants' batches, not just the caller's.
//
// Result verified empirically below: tenant A's Create SUCCEEDS and commits
// an invoice owned by tenant A whose import_batch_id points at tenant B's
// batch -- a genuine cross-tenant reference leak at the Store.Create level.
// This is NOT a violation of any M4-03-01 acceptance criterion (AC#1/#3 only
// require "a re-read returns it" / "a non-existent id is FK-rejected" --
// both true here; the id is NOT non-existent, just foreign-tenant) and
// Store.Create is deliberately not hardened against it per this task's
// instructions (no AC requires a same-tenant check at the store layer).
//
// The production path is safe ANYWAY: M4-03-04's CreateBatch (task-105, the
// importer service) always mints a NEW same-tenant batch row before calling
// Create, so no caller ever has a foreign tenant's batch id in hand to pass
// through. This test exists purely to PIN the store-level permissiveness so
// a future caller (or a refactor of M4-03-04) doesn't accidentally introduce
// a real leak by feeding Store.Create a caller-supplied (rather than
// self-minted) batch id without a tenant-ownership check.
func TestStoreCreate_CrossTenantImportBatchIDFKBypassesRLS(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-IMPBATCH-XTENANT tenant A")
	entityA := seedEntity(t, super, tenantA, "INV-IMPBATCH-XTENANT A entity")

	tenantB := seedTenant(t, super, "INV-IMPBATCH-XTENANT tenant B")
	entityB := seedEntity(t, super, tenantB, "INV-IMPBATCH-XTENANT B entity")
	batchB := seedImportBatch(t, super, tenantB, entityB)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	inv, err := store.Create(cA, CreateInput{
		EntityID:      entityA,
		InvoiceNumber: "INV-IMPBATCH-XTENANT-A",
		ImportBatchID: &batchB,
	})

	// ACTUAL (verified) behavior: this succeeds. If a future change to
	// Store.Create (or to the import_batches/invoices FK) starts rejecting a
	// foreign-tenant batch id, this assertion will fail loudly -- update it
	// deliberately, don't just delete it, since that would re-introduce the
	// silent-leak risk this test exists to catch.
	if err != nil {
		t.Fatalf("Create (tenant A, import_batch_id = tenant B's batch) err = %v, want nil (current Store.Create behavior: the FK check is not RLS-scoped, so a foreign-tenant batch id is accepted)", err)
	}
	if inv.ImportBatchID == nil || *inv.ImportBatchID != batchB {
		t.Fatalf("Create returned import_batch_id = %v, want %q (tenant B's batch, linked despite tenant A being the caller)", inv.ImportBatchID, batchB)
	}

	// Confirm the leak is genuinely committed, not just reflected in the
	// in-memory return value.
	var persisted *string
	if err := super.QueryRow(ctx,
		`SELECT import_batch_id FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&persisted); err != nil {
		t.Fatalf("read back invoice: %v", err)
	}
	if persisted == nil || *persisted != batchB {
		t.Errorf("invoices.import_batch_id read back = %v, want %q (committed cross-tenant reference)", persisted, batchB)
	}

	// The invoice itself is still correctly scoped to tenant A (RLS on
	// invoices.tenant_id is untouched by this FK-bypass finding) -- only the
	// import_batch_id FK target crosses the tenant boundary.
	var tenantOfInvoice string
	if err := super.QueryRow(ctx,
		`SELECT tenant_id FROM invoices WHERE id = $1`, inv.ID,
	).Scan(&tenantOfInvoice); err != nil {
		t.Fatalf("read back invoice tenant_id: %v", err)
	}
	if tenantOfInvoice != tenantA {
		t.Errorf("invoices.tenant_id = %q, want %q (tenant A, the caller)", tenantOfInvoice, tenantA)
	}
}

// TestStoreCreate_EmptyStringImportBatchIDRejected (QA adversarial coverage,
// task-102 Stage 4): an empty-string ImportBatchID (distinct from nil) is
// NOT a valid uuid literal, so Postgres's uuid parser raises 22P02
// (invalid_text_representation) on the $13 bind -- same SQLSTATE bucket
// store.go's header comment already documents for a malformed entity_id/
// import_batch_id uuid, mapped to ErrValidation. Pins that this is a clean
// validation error, not a panic and not silently coerced to NULL (an empty
// string is NOT the same wire value as a nil pointer -- CreateInput.
// ImportBatchID being a *string means a caller could construct
// &"" by mistake, e.g. from an unchecked strings.TrimSpace of a CSV cell).
func TestStoreCreate_EmptyStringImportBatchIDRejected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-IMPBATCH-EMPTY tenant")
	entityID := seedEntity(t, super, tenantID, "INV-IMPBATCH-EMPTY entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	empty := ""
	_, err := store.Create(c, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "INV-IMPBATCH-EMPTY",
		ImportBatchID: &empty,
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create with ImportBatchID = &\"\" err = %v, want ErrValidation (22P02 invalid_text_representation)", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID); n != 0 {
		t.Errorf("invoices rows for tenant after rejected Create = %d, want 0", n)
	}
}
