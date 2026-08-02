// source_document_test.go: invoices.source_document_id — the durable pointer
// an imported invoice carries. CreateInput gains the field; Invoice and
// invoiceColumns deliberately do not.
package invoice

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// seedDocument inserts one documents row as the superuser and returns its id.
func seedDocument(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, "t/"+tenantID+"/"+uuid.NewString(), strings.Repeat("a", 64), int64(11),
	).Scan(&id); err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	return id
}

// sourceDocumentOf reads the column directly: it is NOT on Invoice, by design.
func sourceDocumentOf(t *testing.T, super *pgxpool.Pool, invoiceID string) *string {
	t.Helper()
	var out *string
	if err := super.QueryRow(context.Background(),
		`SELECT source_document_id::text FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&out); err != nil {
		t.Fatalf("read invoices.source_document_id: %v", err)
	}
	return out
}

// TestStoreCreate_PersistsSourceDocumentID: the pointer an import supplies
// lands on the row.
func TestStoreCreate_PersistsSourceDocumentID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-06 source-doc tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-06 source-doc entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{
		EntityID:         entityID,
		InvoiceNumber:    "DOC-06-SRC-1",
		SourceDocumentID: &documentID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := sourceDocumentOf(t, super, inv.ID); got == nil || *got != documentID {
		t.Errorf("source_document_id = %v, want %q", got, documentID)
	}
}

// TestManualCreate_LeavesSourceDocumentNull: a create that names no document
// leaves the column NULL — the pointer must never be defaulted or invented.
func TestManualCreate_LeavesSourceDocumentNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-06 manual-create tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-06 manual-create entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-06-SRC-2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := sourceDocumentOf(t, super, inv.ID); got != nil {
		t.Errorf("source_document_id = %q, want NULL for a manually created invoice", *got)
	}
}

// TestInvoiceColumns_OmitsSourceDocumentID (G5): invoiceColumns feeds a
// POSITIONAL scanInvoice and projects into the gate's MBS payload via
// invoiceFromCreateInput. Only CreateInput carries the new field.
func TestInvoiceColumns_OmitsSourceDocumentID(t *testing.T) {
	if strings.Contains(invoiceColumns, "source_document_id") {
		t.Errorf("invoiceColumns gained source_document_id — it widens Invoice and the MBS fingerprint payload:\n%s", invoiceColumns)
	}
	if !strings.Contains(invoiceColumns, "import_batch_id") {
		t.Fatal("invoiceColumns no longer mentions import_batch_id — the check above passed vacuously")
	}
}
