// source_document_read_test.go: GET /v1/invoices/{id}/source-document -- RED
// specs authored before Store.SourceDocument / SourceDocumentHandler exist.
// DB specs reuse seedTenant/seedEntity/seedDocument (source_document_test.go)
// and intSliceEqual/mustCount/auditCount (source_rows_test.go, store_test.go).
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

var (
	sourceDocumentUUIDRe   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	sourceDocumentSHA256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// doSourceDocument mirrors doInvoiceHistory's shape for the new route.
func doSourceDocument(t *testing.T, get func(ctx context.Context, id string) (SourceDocument, error), id *auth.Identity, invoiceID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID+"/source-document", nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	SourceDocumentHandler(get, nil).ServeHTTP(rec, r)
	return rec
}

// insertDocumentAuditRow writes one audit_log row directly as the superuser
// (bypasses internal/document on purpose). tenant_id must be explicit: its
// column default reads app.current_tenant, unset on a superuser connection.
func insertDocumentAuditRow(t *testing.T, super *pgxpool.Pool, tenantID, event, actor, documentID string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO audit_log (tenant_id, actor, event, payload) VALUES ($1, $2, $3, jsonb_build_object('id', $4::text))`,
		tenantID, actor, event, documentID,
	); err != nil {
		t.Fatalf("insert audit_log %s: %v", event, err)
	}
}

// T1 (AC-1): an imported invoice's Document/SourceRows are fully populated,
// every identifier well-formed (DOC-01 retro rule), not merely non-empty.
func TestStoreSourceDocument_ReturnsDocumentAndRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T1 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T1 entity")
	documentID := seedDocument(t, super, tenantID)
	if _, err := super.Exec(ctx, `UPDATE documents SET filename = $1 WHERE id = $2`, "sales-jan.csv", documentID); err != nil {
		t.Fatalf("set filename: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{
		EntityID: entityID, InvoiceNumber: "DOC-02-02-T1",
		SourceDocumentID: &documentID, SourceRows: []int{2, 3, 4},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}

	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record for an imported invoice")
	}
	if !sourceDocumentUUIDRe.MatchString(got.Document.ID) {
		t.Errorf("Document.ID = %q, not a well-formed uuid", got.Document.ID)
	}
	if got.Document.Filename == nil || *got.Document.Filename == "" {
		t.Errorf("Document.Filename = %v, want a non-empty filename", got.Document.Filename)
	}
	if got.Document.SizeBytes <= 0 {
		t.Errorf("Document.SizeBytes = %d, want > 0", got.Document.SizeBytes)
	}
	if !sourceDocumentSHA256Re.MatchString(got.Document.ContentHash) {
		t.Errorf("Document.ContentHash = %q, want to match %s", got.Document.ContentHash, sourceDocumentSHA256Re.String())
	}
	if got.Document.UploadedAt.IsZero() {
		t.Error("Document.UploadedAt is zero, want the recorded created_at")
	}
	if len(got.SourceRows) == 0 {
		t.Fatal("SourceRows is empty, want the stored [2,3,4]")
	}
	if want := []int{2, 3, 4}; !intSliceEqual(got.SourceRows, want) {
		t.Errorf("SourceRows = %v, want %v", got.SourceRows, want)
	}
}

// T2 (AC-2): a manually created invoice returns both keys present but nil --
// never a 404.
func TestStoreSourceDocument_ManualInvoiceHasNilDocument(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T2 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T2 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document != nil {
		t.Errorf("Document = %+v, want nil for a manually created invoice", got.Document)
	}
	if got.SourceRows != nil {
		t.Errorf("SourceRows = %v, want nil for a manually created invoice", got.SourceRows)
	}
	if got.InvoiceID != inv.ID {
		t.Errorf("InvoiceID = %q, want %q", got.InvoiceID, inv.ID)
	}
}

// T3 (AC-5): a random, well-formed uuid that names no row.
func TestStoreSourceDocument_UnknownIDIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T3 tenant")
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	_, err := store.SourceDocument(c, uuid.NewString())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SourceDocument(random uuid) err = %v, want ErrNotFound", err)
	}
}

// T4 (AC-5): a malformed id is 22P02 -> ErrValidation, never ErrNotFound.
func TestStoreSourceDocument_MalformedIDIsValidation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T4 tenant")
	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	_, err := store.SourceDocument(c, "not-a-uuid")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("SourceDocument(%q) err = %v, want ErrValidation", "not-a-uuid", err)
	}
}

// T5 (AC-5): cross-tenant is ErrNotFound AND the returned struct leaks no
// field (RLS 0-rows, not a partially-populated read).
func TestRLS_SourceDocumentCrossTenantIsNotFound(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "DOC-02-02 T5 tenant A")
	tenantB := seedTenant(t, super, "DOC-02-02 T5 tenant B")
	entityB := seedEntity(t, super, tenantB, "DOC-02-02 T5 entity B")

	store := NewStore(app)
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB})
	invB, err := store.Create(cB, CreateInput{EntityID: entityB, InvoiceNumber: "DOC-02-02-T5"})
	if err != nil {
		t.Fatalf("Create (as tenant B): %v", err)
	}

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA})
	got, err := store.SourceDocument(cA, invB.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SourceDocument (tenant A for tenant B's invoice) err = %v, want ErrNotFound", err)
	}
	if got.InvoiceID != "" || got.SourceRows != nil || got.Document != nil {
		t.Errorf("SourceDocument on error = %+v, want the zero value (no field leaks)", got)
	}
}

// T6 (AC-3, [uploader-from-audit]): the uploader is the document.created
// actor, not a later document.reused actor on the same document.
func TestStoreSourceDocument_UploaderIsDocumentCreatedActor(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T6 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T6 entity")
	documentID := seedDocument(t, super, tenantID)

	creator := uuid.NewString()
	reuser := uuid.NewString()
	insertDocumentAuditRow(t, super, tenantID, "document.created", creator, documentID)
	insertDocumentAuditRow(t, super, tenantID, "document.reused", reuser, documentID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T6", SourceDocumentID: &documentID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record")
	}
	if got.Document.UploadedBy == nil || *got.Document.UploadedBy == "" {
		t.Fatalf("UploadedBy = %v, want the document.created actor %q", got.Document.UploadedBy, creator)
	}
	if *got.Document.UploadedBy != creator {
		t.Errorf("UploadedBy = %q, want %q (the document.created actor)", *got.Document.UploadedBy, creator)
	}
	if *got.Document.UploadedBy == reuser {
		t.Errorf("UploadedBy = %q, must not be the document.reused actor", *got.Document.UploadedBy)
	}
}

// T7 (AC-3): seedDocument alone writes no audit row -- UploadedBy is nil,
// but Document itself must not be (the nil under test is the uploader).
func TestStoreSourceDocument_NoAuditRowLeavesUploaderNil(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T7 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T7 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T7", SourceDocumentID: &documentID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record (the nil under test is the uploader, not the document)")
	}
	if got.Document.UploadedBy != nil {
		t.Errorf("UploadedBy = %q, want nil when no document.created row exists", *got.Document.UploadedBy)
	}
}

// T8 (AC-4, design §4): one document backing 3 invoices.
func TestStoreSourceDocument_InvoicesCreatedCountsSiblings(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T8 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T8 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	var last Invoice
	for i, rows := range [][]int{{2}, {5}, {8}} {
		inv, err := store.Create(c, CreateInput{
			EntityID: entityID, InvoiceNumber: fmt.Sprintf("DOC-02-02-T8-%d", i),
			SourceDocumentID: &documentID, SourceRows: rows,
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		last = inv
	}

	got, err := store.SourceDocument(c, last.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record")
	}
	if got.Document.InvoicesCreated != 3 {
		t.Errorf("InvoicesCreated = %d, want 3", got.Document.InvoicesCreated)
	}
}

// T9 (AC-4): siblings' first rows, self excluded, ascending.
func TestStoreSourceDocument_OtherInvoiceRowsExcludesSelfAndIsSorted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T9 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T9 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T9-A", SourceDocumentID: &documentID, SourceRows: []int{2, 3}}); err != nil {
		t.Fatalf("Create A: %v", err)
	}
	invB, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T9-B", SourceDocumentID: &documentID, SourceRows: []int{9}})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if _, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T9-C", SourceDocumentID: &documentID, SourceRows: []int{5, 6, 7}}); err != nil {
		t.Fatalf("Create C: %v", err)
	}

	got, err := store.SourceDocument(c, invB.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record")
	}
	if want := []int{2, 5}; !intSliceEqual(got.Document.OtherInvoiceRows, want) {
		t.Errorf("OtherInvoiceRows = %v, want %v (ascending, self excluded)", got.Document.OtherInvoiceRows, want)
	}
	if len(got.Document.OtherInvoiceRows) != 2 {
		t.Errorf("len(OtherInvoiceRows) = %d, want 2", len(got.Document.OtherInvoiceRows))
	}
}

// T10 (AC-4): the store-side nil -> []int{} coercion, isolated from the
// handler/JSON layer (T11 covers the wire encoding).
func TestStoreSourceDocument_OtherInvoiceRowsIsEmptySliceNotNil(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T10 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T10 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T10", SourceDocumentID: &documentID, SourceRows: []int{2}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record")
	}
	if got.Document.OtherInvoiceRows == nil {
		t.Error("OtherInvoiceRows = nil, want a non-nil empty slice (a nil []T without omitempty marshals to JSON null)")
	}
	if len(got.Document.OtherInvoiceRows) != 0 {
		t.Errorf("len(OtherInvoiceRows) = %d, want 0", len(got.Document.OtherInvoiceRows))
	}
}

// T11 (AC-4): through the real store and handler, the wire body has a bare
// [], never null -- the exact M4-16 defect class.
func TestSourceDocumentHandler_OtherInvoiceRowsIsEmptyArrayNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T11 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T11 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	identity := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, identity)
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T11", SourceDocumentID: &documentID, SourceRows: []int{2}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doSourceDocument(t, store.SourceDocument, &identity, inv.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if !bytes.Contains(raw, []byte(`"other_invoice_rows":[]`)) {
		t.Errorf("body = %s, want it to contain \"other_invoice_rows\":[]", raw)
	}
	if bytes.Contains(raw, []byte(`"other_invoice_rows":null`)) {
		t.Errorf("body = %s, must not contain \"other_invoice_rows\":null", raw)
	}
}

// T12 (AC-5): no identity -> 401 before the store closure ever runs; the
// body's key set is exactly {"error"}.
func TestSourceDocumentHandler_UnauthenticatedIs401(t *testing.T) {
	invoiceID := uuid.NewString()
	get := func(ctx context.Context, id string) (SourceDocument, error) {
		t.Fatal("SourceDocument must not run without an identity")
		return SourceDocument{}, nil
	}
	rec := doSourceDocument(t, get, nil, invoiceID)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if len(body) != 1 {
		t.Fatalf("body = %s, want exactly one top-level key", rec.Body.String())
	}
	rawMsg, ok := body["error"]
	if !ok {
		t.Fatalf("body = %s, want the sole key to be \"error\"", rec.Body.String())
	}
	var msg string
	if err := json.Unmarshal(rawMsg, &msg); err != nil {
		t.Fatalf("decode error value: %v", err)
	}
	if msg == "" {
		t.Error("error message is empty, want non-empty")
	}
}

// T13 (AC-5): cross-tenant and unknown-id are byte-identical 404s through
// the real store+handler -- no existence oracle.
func TestRLS_SourceDocumentHandlerCrossTenantIs404AndIdenticalToUnknown(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "DOC-02-02 T13 tenant A")
	tenantB := seedTenant(t, super, "DOC-02-02 T13 tenant B")
	entityB := seedEntity(t, super, tenantB, "DOC-02-02 T13 entity B")

	store := NewStore(app)
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB})
	invB, err := store.Create(cB, CreateInput{EntityID: entityB, InvoiceNumber: "DOC-02-02-T13"})
	if err != nil {
		t.Fatalf("Create (as tenant B): %v", err)
	}

	identityA := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA}
	recCross := doSourceDocument(t, store.SourceDocument, &identityA, invB.ID)
	recUnknown := doSourceDocument(t, store.SourceDocument, &identityA, uuid.NewString())

	if recCross.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant status = %d, want 404 (body=%s)", recCross.Code, recCross.Body.String())
	}
	if recUnknown.Code != http.StatusNotFound {
		t.Fatalf("unknown-id status = %d, want 404 (body=%s)", recUnknown.Code, recUnknown.Body.String())
	}
	bodyCross := recCross.Body.Bytes()
	bodyUnknown := recUnknown.Body.Bytes()
	if len(bodyCross) == 0 || len(bodyUnknown) == 0 {
		t.Fatalf("bodies must be non-empty: cross=%q unknown=%q", bodyCross, bodyUnknown)
	}
	if !bytes.Equal(bodyCross, bodyUnknown) {
		t.Errorf("cross-tenant body = %s, unknown-id body = %s, want byte-identical", bodyCross, bodyUnknown)
	}
}

// T14 (AC-2): raw-byte check -- a decode can't tell an omitted key from a
// null one, so this asserts on the wire bytes directly.
func TestSourceDocumentHandler_NullsRenderExplicitly(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	get := func(ctx context.Context, gotID string) (SourceDocument, error) {
		return SourceDocument{InvoiceID: gotID}, nil
	}
	rec := doSourceDocument(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if !bytes.Contains(raw, []byte(`"document":null`)) {
		t.Errorf("body = %s, want it to contain \"document\":null", raw)
	}
	if !bytes.Contains(raw, []byte(`"source_rows":null`)) {
		t.Errorf("body = %s, want it to contain \"source_rows\":null", raw)
	}
}

// T15 ([metadata-read-not-audited]): a successful read writes no audit row.
// The err==nil && Document!=nil guard is a vacuity floor -- an error path
// also writes nothing and would pass trivially otherwise.
func TestSourceDocumentRead_WritesNoAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T15 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02-02 T15 entity")
	documentID := seedDocument(t, super, tenantID)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "DOC-02-02-T15", SourceDocumentID: &documentID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	before := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenantID)

	got, err := store.SourceDocument(c, inv.ID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record -- an error path also writes no audit row and would pass vacuously")
	}

	after := mustCount(t, super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, tenantID)
	if after != before {
		t.Errorf("audit_log rows = %d after the read, want %d (unchanged) -- the metadata read must write no audit row", after, before)
	}
}

// T16 ([uploader-index-on-audit-log]): the corrected (tenant_id, id) partial
// index is actually used by the planner for the uploader subquery -- node
// type (Bitmap vs Index Scan) is deliberately NOT asserted, only the index
// name and the absence of a full Seq Scan.
func TestSourceDocumentUploaderLookupUsesIndex(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02-02 T16 tenant")
	targetDocID := uuid.NewString()

	for i := 0; i < 300; i++ {
		insertDocumentAuditRow(t, super, tenantID, "document.created", uuid.NewString(), uuid.NewString())
	}
	insertDocumentAuditRow(t, super, tenantID, "document.created", uuid.NewString(), targetDocID)

	if _, err := super.Exec(ctx, `ANALYZE audit_log`); err != nil {
		t.Fatalf("ANALYZE audit_log: %v", err)
	}

	var planLines []string
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`EXPLAIN (COSTS OFF) SELECT a.actor FROM audit_log a
			   WHERE a.event = 'document.created' AND a.payload->>'id' = $1
			   ORDER BY a.id ASC LIMIT 1`, targetDocID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			planLines = append(planLines, line)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("EXPLAIN uploader subquery: %v", err)
	}

	plan := strings.Join(planLines, "\n")
	if plan == "" || !strings.Contains(plan, "audit_log") {
		t.Fatalf("plan = %q, want a non-empty plan mentioning audit_log (vacuity floor)", plan)
	}
	if !strings.Contains(plan, "audit_log_document_created_idx") {
		t.Errorf("plan = %s, want it to use audit_log_document_created_idx", plan)
	}
	if strings.Contains(plan, "Seq Scan on audit_log") {
		t.Errorf("plan = %s, must not Seq Scan audit_log", plan)
	}
}
