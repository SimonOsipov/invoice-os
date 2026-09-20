package invoice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// A batch deleted after the import leaves invoices.import_batch_id NULL (ON
// DELETE SET NULL), so the header row is no longer knowable. The read must
// still succeed with an explicit nil rather than fail on the LEFT JOIN.
func TestStoreSourceDocument_HeaderRowIsNilAfterTheBatchIsDeleted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AIR-06-04 QA deleted-batch tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-06-04 QA deleted-batch entity")
	documentID := seedDocument(t, super, tenantID)
	batchID := seedImportBatch(t, super, tenantID, entityID)
	if _, err := super.Exec(ctx, `UPDATE import_batches SET header_row = 3 WHERE id = $1`, batchID); err != nil {
		t.Fatalf("set header_row: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{
		EntityID: entityID, InvoiceNumber: "AIR-06-04-QA-DEL", ImportBatchID: &batchID,
		SourceDocumentID: &documentID, SourceRows: []int{4},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Floor: the nil after the delete must be the delete's doing, not a reader
	// that never reads the column.
	before, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument (before delete): %v", err)
	}
	if before.HeaderRow == nil || *before.HeaderRow != 3 {
		t.Fatalf("HeaderRow before delete = %v, want 3", before.HeaderRow)
	}

	if _, err := super.Exec(ctx, `DELETE FROM import_batches WHERE id = $1`, batchID); err != nil {
		t.Fatalf("delete batch: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument (after delete): %v", err)
	}
	if got.HeaderRow != nil {
		t.Errorf("HeaderRow after delete = %d, want nil", *got.HeaderRow)
	}
	if got.Document == nil {
		t.Error("Document = nil, want the document to survive the batch delete")
	}
	if want := []int{4}; !intSliceEqual(got.SourceRows, want) {
		t.Errorf("SourceRows = %v, want %v", got.SourceRows, want)
	}
}

// An explicit 1 is a recorded value, not the absent case: the wire must carry
// 1, not null, or the SPA cannot tell a row-1 import from an unrecorded one.
func TestStoreSourceDocument_HeaderRowOfOneIsRecordedNotNil(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AIR-06-04 QA row-one tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-06-04 QA row-one entity")
	batchID := seedImportBatch(t, super, tenantID, entityID)
	if _, err := super.Exec(ctx, `UPDATE import_batches SET header_row = 1 WHERE id = $1`, batchID); err != nil {
		t.Fatalf("set header_row: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AIR-06-04-QA-ONE", ImportBatchID: &batchID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.HeaderRow == nil {
		t.Fatal("HeaderRow = nil, want 1")
	}
	if *got.HeaderRow != 1 {
		t.Errorf("HeaderRow = %d, want 1", *got.HeaderRow)
	}
}

// A header row far past the previewer's row cap round-trips unclamped: the
// reader reports what the import recorded.
func TestStoreSourceDocument_LargeHeaderRowRoundTrips(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AIR-06-04 QA large-row tenant")
	entityID := seedEntity(t, super, tenantID, "AIR-06-04 QA large-row entity")
	batchID := seedImportBatch(t, super, tenantID, entityID)
	if _, err := super.Exec(ctx, `UPDATE import_batches SET header_row = 100000 WHERE id = $1`, batchID); err != nil {
		t.Fatalf("set header_row: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "AIR-06-04-QA-BIG", ImportBatchID: &batchID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.HeaderRow == nil || *got.HeaderRow != 100000 {
		t.Errorf("HeaderRow = %v, want 100000", got.HeaderRow)
	}
}

// The LEFT JOIN reaches a second table, so it is a second RLS surface. Two
// refusals: tenant B cannot read tenant A's invoice at all, and an invoice
// doctored to point at another tenant's batch reports nil rather than that
// tenant's value.
func TestRLS_SourceDocumentHeaderRowDoesNotCrossTenants(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "AIR-06-04 QA rls tenant A")
	tenantB := seedTenant(t, super, "AIR-06-04 QA rls tenant B")
	entityA := seedEntity(t, super, tenantA, "AIR-06-04 QA rls entity A")
	entityB := seedEntity(t, super, tenantB, "AIR-06-04 QA rls entity B")

	batchA := seedImportBatch(t, super, tenantA, entityA)
	if _, err := super.Exec(ctx, `UPDATE import_batches SET header_row = 5 WHERE id = $1`, batchA); err != nil {
		t.Fatalf("set tenant A header_row: %v", err)
	}
	batchB := seedImportBatch(t, super, tenantB, entityB)
	if _, err := super.Exec(ctx, `UPDATE import_batches SET header_row = 9 WHERE id = $1`, batchB); err != nil {
		t.Fatalf("set tenant B header_row: %v", err)
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA})
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB})

	invA, err := store.Create(cA, CreateInput{EntityID: entityA, InvoiceNumber: "AIR-06-04-QA-RLS-A", ImportBatchID: &batchA})
	if err != nil {
		t.Fatalf("Create (tenant A): %v", err)
	}

	// Floor: tenant A reads its own batch's value, so a nil below means refusal.
	own, err := store.SourceDocument(cA, invA.ID)
	if err != nil {
		t.Fatalf("SourceDocument (tenant A, own invoice): %v", err)
	}
	if own.HeaderRow == nil || *own.HeaderRow != 5 {
		t.Fatalf("tenant A HeaderRow = %v, want 5", own.HeaderRow)
	}

	got, err := store.SourceDocument(cB, invA.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SourceDocument (tenant B for tenant A's invoice) err = %v, want ErrNotFound", err)
	}
	if got.HeaderRow != nil {
		t.Errorf("HeaderRow on a refused read = %d, want nil", *got.HeaderRow)
	}
	if got.InvoiceID != "" || got.SourceRows != nil || got.Document != nil {
		t.Errorf("SourceDocument on error = %+v, want the zero value", got)
	}

	// Doctored: tenant A's invoice pointed at tenant B's batch. Only the
	// superuser can write this; RLS must still hide B's 9 from A.
	if _, err := super.Exec(ctx, `UPDATE invoices SET import_batch_id = $1 WHERE id = $2`, batchB, invA.ID); err != nil {
		t.Fatalf("doctor import_batch_id: %v", err)
	}
	crossed, err := store.SourceDocument(cA, invA.ID)
	if err != nil {
		t.Fatalf("SourceDocument (tenant A, doctored pointer): %v", err)
	}
	if crossed.HeaderRow != nil {
		t.Errorf("tenant A HeaderRow through tenant B's batch = %d, want nil", *crossed.HeaderRow)
	}
	if crossed.InvoiceID != invA.ID {
		t.Errorf("InvoiceID = %q, want %q -- the invoice itself must stay readable", crossed.InvoiceID, invA.ID)
	}
}

// The handler renders whatever the store reports, including a value the
// previewer treats as the default. Guards the wire against an omitempty creeping in.
func TestSourceDocumentHandler_HeaderRowOfOneRendersAsOne(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	one := 1
	get := func(ctx context.Context, gotID string) (SourceDocument, error) {
		return SourceDocument{InvoiceID: gotID, HeaderRow: &one}, nil
	}
	rec := doSourceDocument(t, get, &id, uuid.NewString())
	if raw := rec.Body.String(); !strings.Contains(raw, `"header_row":1`) {
		t.Errorf("body = %s, want it to contain \"header_row\":1", raw)
	}
}
