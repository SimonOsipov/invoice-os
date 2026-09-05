// source_document_create_test.go: source_document_id on the POST /v1/invoices
// wire. CreateHandler over a real Store -- store.go already binds the column
// (TestStoreCreate_PersistsSourceDocumentID), so only a handler-level test can
// see whether the wire key reaches it. Inline DATABASE_URL gate via
// dbTestPools, this package's convention.
package invoice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// createViaHandler returns the raw body too: byte-equality is the oracle in
// TestRLS_CreateRefusesAnotherTenantsSourceDocument.
func createViaHandler(t *testing.T, store *Store, tenantID, rawBody string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/invoices", strings.NewReader(rawBody))
	r = r.WithContext(auth.WithIdentity(context.Background(), auth.Identity{
		Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
	}))
	rec := httptest.NewRecorder()
	CreateHandler(store.Create, nil).ServeHTTP(rec, r)
	return rec, rec.Body.String()
}

func createBodyWithDocument(entityID, invoiceNumber, sourceDocumentID string) string {
	return fmt.Sprintf(`{"entity_id":%q,"invoice_number":%q,"source_document_id":%q}`,
		entityID, invoiceNumber, sourceDocumentID)
}

// Superuser, so the count is not itself scoped by the RLS this file tests
// through; tenant-scoped, so debris from an earlier run cannot answer it.
func invoiceCountByNumber(t *testing.T, super *pgxpool.Pool, tenantID, invoiceNumber string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, tenantID, invoiceNumber,
	).Scan(&n); err != nil {
		t.Fatalf("count invoices by number: %v", err)
	}
	return n
}

func documentCountByID(t *testing.T, super *pgxpool.Pool, documentID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM documents WHERE id = $1`, documentID,
	).Scan(&n); err != nil {
		t.Fatalf("count documents by id: %v", err)
	}
	return n
}

// TestCreateHandler_PersistsOwnTenantSourceDocument (SD-2, AC-2/AC-6): a
// well-formed uuid naming the caller's own document lands on the column and
// comes back out of the source-document read. SourceRows stays nil: a
// hand-typed invoice has no sheet rows.
func TestCreateHandler_PersistsOwnTenantSourceDocument(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "EXTR-15-06 SD-2 tenant")
	entityID := seedEntity(t, super, tenantID, "EXTR-15-06 SD-2 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	rec, body := createViaHandler(t, store, tenantID,
		createBodyWithDocument(entityID, "EXTR-15-06-SD2", documentID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, body)
	}

	var created invoiceBody
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if created.ID == "" {
		t.Fatalf("response carries no invoice id: %s", body)
	}

	if got := sourceDocumentOf(t, super, created.ID); got == nil || *got != documentID {
		t.Fatalf("invoices.source_document_id = %v, want %q", got, documentID)
	}

	c := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
	})
	sd, err := store.SourceDocument(c, created.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if sd.Document == nil {
		t.Fatalf("SourceDocument.Document = nil, want the document the create named")
	}
	if sd.Document.ID != documentID {
		t.Errorf("SourceDocument.Document.ID = %q, want %q", sd.Document.ID, documentID)
	}
	if sd.SourceRows != nil {
		t.Errorf("SourceRows = %v, want nil for a hand-typed invoice (AC-6)", sd.SourceRows)
	}
}

// TestRLS_CreateRefusesAnotherTenantsSourceDocument (SD-4, AC-4): tenant A
// naming tenant B's document is refused with a body byte-equal to a
// never-existed uuid's. No code is added for this -- invoices_tenant_source_
// document_fk is composite, so the INSERT itself raises 23503 inside Create's
// tx and store.go maps it to ErrValidation.
func TestRLS_CreateRefusesAnotherTenantsSourceDocument(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "EXTR-15-06 SD-4 tenant A")
	tenantB := seedTenant(t, super, "EXTR-15-06 SD-4 tenant B")
	entityA := seedEntity(t, super, tenantA, "EXTR-15-06 SD-4 entity A")
	documentB := seedDocument(t, super, tenantB)

	store := NewStore(app)

	crossRec, crossBody := createViaHandler(t, store, tenantA,
		createBodyWithDocument(entityA, "EXTR-15-06-SD4-CROSS", documentB))
	missingRec, missingBody := createViaHandler(t, store, tenantA,
		createBodyWithDocument(entityA, "EXTR-15-06-SD4-MISSING", uuid.NewString()))

	if crossRec.Code != http.StatusBadRequest {
		t.Fatalf("cross-tenant document: status = %d, want 400 (body=%s)", crossRec.Code, crossBody)
	}
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("never-existed document: status = %d, want 400 (body=%s)", missingRec.Code, missingBody)
	}
	if crossBody != missingBody {
		t.Errorf("existence oracle: a REAL cross-tenant document id answers %q and a never-existed one %q -- the two must be byte-equal",
			crossBody, missingBody)
	}
}

// TestRLS_RefusedCreateWithASourceDocumentLeavesNoRow (SD-5, AC-5): the
// refusal is the INSERT failing inside Create's tx, so nothing survives it.
// Its own test rather than a tail of SD-4 above: behind that test's status
// Fatal these counts would never run, and an unexecuted assertion is not a
// spec.
func TestRLS_RefusedCreateWithASourceDocumentLeavesNoRow(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "EXTR-15-06 SD-5 tenant A")
	tenantB := seedTenant(t, super, "EXTR-15-06 SD-5 tenant B")
	entityA := seedEntity(t, super, tenantA, "EXTR-15-06 SD-5 entity A")
	documentB := seedDocument(t, super, tenantB)

	store := NewStore(app)

	const crossNumber = "EXTR-15-06-SD5-CROSS"
	const missingNumber = "EXTR-15-06-SD5-MISSING"
	const controlNumber = "EXTR-15-06-SD5-CONTROL"

	// The control runs first: without it the two zeros below would also be
	// reported by a counter reading the wrong table, column or tenant.
	controlRec, controlBody := createViaHandler(t, store, tenantA,
		fmt.Sprintf(`{"entity_id":%q,"invoice_number":%q}`, entityA, controlNumber))
	if controlRec.Code != http.StatusCreated {
		t.Fatalf("control create: status = %d, want 201 (body=%s)", controlRec.Code, controlBody)
	}
	if n := invoiceCountByNumber(t, super, tenantA, controlNumber); n != 1 {
		t.Fatalf("count(invoices for tenant A where invoice_number=%q) = %d, want 1 -- this counter would report 0 for anything",
			controlNumber, n)
	}

	createViaHandler(t, store, tenantA, createBodyWithDocument(entityA, crossNumber, documentB))
	createViaHandler(t, store, tenantA, createBodyWithDocument(entityA, missingNumber, uuid.NewString()))

	if n := invoiceCountByNumber(t, super, tenantA, crossNumber); n != 0 {
		t.Errorf("count(invoices where invoice_number=%q) = %d, want 0 after a cross-tenant document was refused", crossNumber, n)
	}
	if n := invoiceCountByNumber(t, super, tenantA, missingNumber); n != 0 {
		t.Errorf("count(invoices where invoice_number=%q) = %d, want 0 after a never-existed document was refused", missingNumber, n)
	}
}

// TestRLS_CreateSourceDocumentRefusalIsTheFKNotAbsence (SD-6, AC-4
// falsification): the same id, once while tenant B's documents row EXISTS and
// once after it is deleted. Identical bodies prove the refusal comes from the
// composite FK and not from "no such row anywhere" -- which a spec asserting
// only "cross-tenant is a 400" would pass either way. The two legs use
// different invoice numbers so a duplicate-number 409 can never stand in for
// the refusal under test.
func TestRLS_CreateSourceDocumentRefusalIsTheFKNotAbsence(t *testing.T) {
	super, app := dbTestPools(t)

	tenantA := seedTenant(t, super, "EXTR-15-06 SD-6 tenant A")
	tenantB := seedTenant(t, super, "EXTR-15-06 SD-6 tenant B")
	entityA := seedEntity(t, super, tenantA, "EXTR-15-06 SD-6 entity A")
	documentB := seedDocument(t, super, tenantB)

	store := NewStore(app)

	if n := documentCountByID(t, super, documentB); n != 1 {
		t.Fatalf("count(documents where id=%q) = %d before the first leg, want 1", documentB, n)
	}
	presentRec, presentBody := createViaHandler(t, store, tenantA,
		createBodyWithDocument(entityA, "EXTR-15-06-SD6-PRESENT", documentB))

	if _, err := super.Exec(context.Background(), `DELETE FROM documents WHERE id = $1`, documentB); err != nil {
		t.Fatalf("delete tenant B's document: %v", err)
	}
	if n := documentCountByID(t, super, documentB); n != 0 {
		t.Fatalf("count(documents where id=%q) = %d after the delete, want 0 -- the second leg would repeat the first", documentB, n)
	}
	absentRec, absentBody := createViaHandler(t, store, tenantA,
		createBodyWithDocument(entityA, "EXTR-15-06-SD6-ABSENT", documentB))

	if presentRec.Code != http.StatusBadRequest {
		t.Fatalf("row present: status = %d, want 400 (body=%s)", presentRec.Code, presentBody)
	}
	if absentRec.Code != http.StatusBadRequest {
		t.Fatalf("row deleted: status = %d, want 400 (body=%s)", absentRec.Code, absentBody)
	}
	if presentBody != absentBody {
		t.Errorf("the same id answers %q while tenant B's row exists and %q after it is deleted -- the refusal is reading existence, not the FK",
			presentBody, absentBody)
	}
}
