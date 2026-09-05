// EXTR-15-10 (task-855, Mode A): RED specs for import_batches.document_id
// reaching Store.GetBatch. The column is written at INSERT (store.go
// CreateBatch) and read back nowhere.
//
// Batch has no DocumentID field yet, so a direct field reference would be a
// COMPILE error and would take this package's whole suite down with it. The
// specs reach the field through the two reflective helpers below and fail on
// an explicit not-implemented t.Fatalf instead. handlers_test.go's
// TestGetHandler_BodyCarriesTheBatchDocumentID reuses batchWithDocumentID.
package importer

import (
	"context"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// batchDocumentID reads Batch.DocumentID reflectively. ok is false ONLY when
// the field is absent; a nil *string reads (nil, true), which is what
// distinguishes "not implemented" from "implemented, this batch has none".
func batchDocumentID(t *testing.T, b Batch) (value *string, ok bool) {
	t.Helper()
	f := reflect.ValueOf(b).FieldByName("DocumentID")
	if !f.IsValid() {
		return nil, false
	}
	if f.Type().String() != "*string" {
		t.Fatalf("Batch.DocumentID is %s, want *string", f.Type())
	}
	if f.IsNil() {
		return nil, true
	}
	s := f.Elem().String()
	return &s, true
}

// batchWithDocumentID returns b with DocumentID set, or fails the calling test
// with the not-implemented message when the field does not exist.
func batchWithDocumentID(t *testing.T, b Batch, documentID string) Batch {
	t.Helper()
	f := reflect.ValueOf(&b).Elem().FieldByName("DocumentID")
	if !f.IsValid() {
		t.Fatalf("not implemented -- importer.Batch must carry DocumentID *string (EXTR-15-10 AC-1)")
	}
	if f.Type().String() != "*string" {
		t.Fatalf("Batch.DocumentID is %s, want *string", f.Type())
	}
	f.Set(reflect.ValueOf(&documentID))
	return b
}

func derefString(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// BD-1 (AC-1): a batch created with a document id reads that id back through
// GetBatch, and a batch created without one reads back nil.
//
// Both halves matter. Only the first catches a SELECT that never names
// b.document_id; only the second catches a scan that hard-codes a value or
// coerces SQL NULL to "". A SELECT that names the column without a matching
// Scan destination is caught by pgx's own arity error at "GetBatch:" below.
func TestGetBatch_CarriesTheStoredDocumentID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "EXTR-15-10 document_id tenant")
	entityID := seedEntity(t, super, tenantID, "EXTR-15-10 document_id entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	withDoc, err := store.CreateBatch(c, entityID, "scan.pdf", documentID)
	if err != nil {
		t.Fatalf("CreateBatch (with document): %v", err)
	}
	got, err := store.GetBatch(c, withDoc)
	if err != nil {
		t.Fatalf("GetBatch (with document): %v", err)
	}
	gotDoc, ok := batchDocumentID(t, got)
	if !ok {
		t.Fatalf("not implemented -- importer.Batch must carry DocumentID *string and GetBatch's SELECT must name b.document_id (EXTR-15-10 AC-1)")
	}
	if gotDoc == nil || *gotDoc != documentID {
		t.Errorf("DocumentID = %s, want %q", derefString(gotDoc), documentID)
	}

	// CreateBatch maps "" through nullif to SQL NULL.
	noDoc, err := store.CreateBatch(c, entityID, "ledger.csv", "")
	if err != nil {
		t.Fatalf("CreateBatch (no document): %v", err)
	}
	gotNone, err := store.GetBatch(c, noDoc)
	if err != nil {
		t.Fatalf("GetBatch (no document): %v", err)
	}
	noneDoc, ok := batchDocumentID(t, gotNone)
	if !ok {
		t.Fatalf("not implemented -- importer.Batch must carry DocumentID *string (EXTR-15-10 AC-1)")
	}
	if noneDoc != nil {
		t.Errorf("DocumentID = %q for a batch with no stored document, want nil", *noneDoc)
	}
}
