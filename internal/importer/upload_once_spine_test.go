// upload_once_spine_test.go: the DB-backed half of [upload-once] — the
// document pointer reaching import_batches and invoices, the fail-closed
// import, the two 404s, and the preview/import decode parity. Real
// document.Service over the real documents table with an in-memory object
// store; the fake-driven handler specs live in handlers_upload_once_test.go.
//
// Run with DATABASE_URL + DATABASE_SUPERUSER_URL set (make test-rls).
package importer

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- in-memory object store -------------------------------------------------

// memObjects is document.ObjectStore over a map. getErr makes storage
// unreachable for reads while the documents row stays perfectly valid — the
// exact shape of Core AC 2.
type memObjects struct {
	mu      sync.Mutex
	objects map[string][]byte
	getErr  error
}

func newMemObjects() *memObjects { return &memObjects{objects: map[string][]byte{}} }

func (m *memObjects) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = b
	return nil
}

func (m *memObjects) Get(ctx context.Context, key, rangeHeader string) (document.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return document.Object{}, m.getErr
	}
	b, ok := m.objects[key]
	if !ok {
		return document.Object{}, document.ErrNotFound
	}
	return document.Object{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b))}, nil
}

// --- fixtures ---------------------------------------------------------------

var uoHeader = []string{"Inv No", "Date", "Buyer", "Subtotal", "VAT", "Total"}

var uoMapping = map[string]string{
	"invoice_number": "Inv No", "issue_date": "Date", "buyer_name": "Buyer",
	"subtotal": "Subtotal", "vat": "VAT", "total": "Total",
}

func uoRows(prefix string, n int) [][]string {
	rows := make([][]string, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, []string{prefix + "-" + string(rune('0'+i)), "2026-01-15", "Acme Ltd", "100.00", "19.00", "119.00"})
	}
	return rows
}

// storeDocumentAs writes one document under tenantID and returns the row.
func storeDocumentAs(t *testing.T, svc *document.Service, tenantID, filename, contentType string, content []byte) document.Document {
	t.Helper()
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
	})
	doc, _, err := svc.Store(ctx, filename, contentType, int64(len(content)), bytes.NewReader(content))
	if err != nil {
		t.Fatalf("store document: %v", err)
	}
	return doc
}

// --- super-pool read-backs (RLS-bypassing, G5: never through invoice.Invoice) --

func batchColumn(t *testing.T, super *pgxpool.Pool, column, batchID string) *string {
	t.Helper()
	var out *string
	if err := super.QueryRow(context.Background(),
		`SELECT `+column+`::text FROM import_batches WHERE id = $1`, batchID,
	).Scan(&out); err != nil {
		t.Fatalf("read import_batches.%s: %v", column, err)
	}
	return out
}

func countInvoicesWithSourceDocument(t *testing.T, super *pgxpool.Pool, entityID, documentID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE entity_id = $1 AND source_document_id = $2`, entityID, documentID,
	).Scan(&n); err != nil {
		t.Fatalf("count invoices by source_document_id: %v", err)
	}
	return n
}

func countInvoicesWithoutSourceDocument(t *testing.T, super *pgxpool.Pool, entityID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE entity_id = $1 AND source_document_id IS NULL`, entityID,
	).Scan(&n); err != nil {
		t.Fatalf("count invoices with a null source_document_id: %v", err)
	}
	return n
}

// --- AC-6/AC-7: the pointer lands, the filename comes off the row -----------

// TestImport_BatchCarriesDocumentID and TestImport_FilenameComesFromDocumentRow
// are one run: the request carries no filename anywhere, so a batch that ends
// up named q.csv can only have read it off the document row — and a transposed
// (filename, documentID) argument pair fails both halves at once.
func TestImport_BatchCarriesDocumentID(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantID := seedTenant(t, super, "uo batch pointer tenant")
	entityID := seedEntity(t, super, tenantID, "uo batch pointer entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	rows := uoRows("UO-BATCH", 1)
	doc := storeDocumentAs(t, docSvc, tenantID, "q.csv", "text/csv", csvBody(t, uoHeader, rows))

	body, ct := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), doc.ID)
	rec, raw, resp := doImportUpload(t, svc.Import, docSvc.Open, &id, "", ct, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
	}
	if got := batchColumn(t, super, "document_id", resp.ID); got == nil || *got != doc.ID {
		t.Errorf("import_batches.document_id = %v, want %q", got, doc.ID)
	}
	if got := batchColumn(t, super, "filename", resp.ID); got == nil || *got != "q.csv" {
		t.Errorf("import_batches.filename = %v, want %q sourced from the document row", got, "q.csv")
	}
}

// TestImport_InvoicesCarrySourceDocumentID: every invoice an import creates
// points at the document. Read by direct SQL — source_document_id must NOT join
// invoiceColumns/invoice.Invoice (it feeds a positional scan and the gate's MBS
// payload).
func TestImport_InvoicesCarrySourceDocumentID(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantID := seedTenant(t, super, "uo invoice pointer tenant")
	entityID := seedEntity(t, super, tenantID, "uo invoice pointer entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	rows := uoRows("UO-INV", 3)
	doc := storeDocumentAs(t, docSvc, tenantID, "three.csv", "text/csv", csvBody(t, uoHeader, rows))

	body, ct := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), doc.ID)
	rec, raw, resp := doImportUpload(t, svc.Import, docSvc.Open, &id, "", ct, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
	}
	if resp.ReadyInvoices != 3 {
		t.Fatalf("ready_invoices = %d, want 3 — nothing to assert the pointer on otherwise", resp.ReadyInvoices)
	}
	if n := countInvoicesForEntity(t, super, entityID); n != 3 {
		t.Fatalf("invoices persisted = %d, want 3", n)
	}
	if n := countInvoicesWithSourceDocument(t, super, entityID, doc.ID); n != 3 {
		t.Errorf("invoices with source_document_id = %q: %d, want 3", doc.ID, n)
	}
	if n := countInvoicesWithoutSourceDocument(t, super, entityID); n != 0 {
		t.Errorf("invoices with a NULL source_document_id = %d, want 0", n)
	}
}

// TestCreateBatch_EmptyDocumentIDPersistsNull: the column is nullable and an
// empty id persists as SQL NULL, mirroring filename's nullif treatment — an
// empty string would be a uuid cast error, and callers with no document (a
// direct store-level create) must stay writable.
func TestCreateBatch_EmptyDocumentIDPersistsNull(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID := seedTenant(t, super, "uo createbatch null tenant")
	entityID := seedEntity(t, super, tenantID, "uo createbatch null entity")
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: memberSubject, Role: "authenticated", TenantID: tenantID,
	})

	id, err := NewStore(app).CreateBatch(ctx, entityID, "x.csv", "")
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if got := batchColumn(t, super, "document_id", id); got != nil {
		t.Errorf("import_batches.document_id = %q, want NULL for an empty document id", *got)
	}
}

// --- AC-4: fail closed when the bytes cannot be fetched ---------------------

// TestImport_StorageUnreachableWritesNothing (G3 — the fetch lives in
// CreateHandler, not Service.Import, so this cannot be a service-level spec):
// the row resolves, the object does not, and nothing is written.
func TestImport_StorageUnreachableWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	objs := newMemObjects()
	docSvc := document.NewService(document.NewStore(app), objs)

	tenantID := seedTenant(t, super, "uo fail-closed tenant")
	entityID := seedEntity(t, super, tenantID, "uo fail-closed entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	doc := storeDocumentAs(t, docSvc, tenantID, "q.csv", "text/csv", csvBody(t, uoHeader, uoRows("UO-DOWN", 2)))
	objs.getErr = errOpenBoom

	body, ct := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), doc.ID)
	rec, raw, resp := doImportUpload(t, svc.Import, docSvc.Open, &id, "", ct, body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when object storage is unreachable (body=%s)", rec.Code, raw)
	}
	if strings.Contains(resp.Error, errOpenBoom.Error()) {
		t.Errorf("500 body leaks the internal error: %q", resp.Error)
	}
	if n := countImportBatchesForEntity(t, super, entityID); n != 0 {
		t.Errorf("import_batches rows = %d, want 0 — the fetch must precede every write", n)
	}
	if n := countInvoicesForEntity(t, super, entityID); n != 0 {
		t.Errorf("invoices rows = %d, want 0", n)
	}
}

// --- AC-5/G2: document sentinels do not travel through statusForErr ---------

// TestImport_CrossTenantDocumentIDIs404: errors.Is(document.ErrNotFound,
// importer.ErrNotFound) is false, so routing the open error through
// importer.statusForErr 500s this case. It is a 404.
func TestImport_CrossTenantDocumentIDIs404(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	ownerTenant := seedTenant(t, super, "uo cross-tenant owner")
	callerTenant := seedTenant(t, super, "uo cross-tenant caller")
	entityID := seedEntity(t, super, callerTenant, "uo cross-tenant entity")
	caller := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: callerTenant}

	doc := storeDocumentAs(t, docSvc, ownerTenant, "theirs.csv", "text/csv", csvBody(t, uoHeader, uoRows("UO-XT", 1)))

	body, ct := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), doc.ID)
	rec, raw, resp := doImportUpload(t, svc.Import, docSvc.Open, &caller, "", ct, body)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for another tenant's document id (body=%s)", rec.Code, raw)
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message")
	}
	if n := countImportBatchesForEntity(t, super, entityID); n != 0 {
		t.Errorf("import_batches rows = %d, want 0", n)
	}
}

// TestImport_NotFoundBodiesAreByteIdentical: a refused id and an absent one
// must be indistinguishable, or the endpoint is an existence oracle over
// another tenant's documents.
func TestImport_NotFoundBodiesAreByteIdentical(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	ownerTenant := seedTenant(t, super, "uo oracle owner")
	callerTenant := seedTenant(t, super, "uo oracle caller")
	entityID := seedEntity(t, super, callerTenant, "uo oracle entity")
	caller := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: callerTenant}

	doc := storeDocumentAs(t, docSvc, ownerTenant, "theirs.csv", "text/csv", csvBody(t, uoHeader, uoRows("UO-ORACLE", 1)))
	mapping := mustMappingJSON(t, uoMapping)

	crossBody, crossCT := buildImportForm(t, entityID, mapping, doc.ID)
	crossRec, crossRaw, _ := doImportUpload(t, svc.Import, docSvc.Open, &caller, "", crossCT, crossBody)

	absentBody, absentCT := buildImportForm(t, entityID, mapping, uuid.NewString())
	absentRec, absentRaw, _ := doImportUpload(t, svc.Import, docSvc.Open, &caller, "", absentCT, absentBody)

	if crossRec.Code != http.StatusNotFound || absentRec.Code != http.StatusNotFound {
		t.Fatalf("statuses = cross-tenant %d / absent %d, want 404 for both (bodies=%s | %s)",
			crossRec.Code, absentRec.Code, crossRaw, absentRaw)
	}
	if !bytes.Equal(crossRaw, absentRaw) {
		t.Errorf("bodies differ:\n cross-tenant: %s\n absent:       %s", crossRaw, absentRaw)
	}
}

// --- AC-9/G4: preview and import resolve the same file ----------------------

// TestPreviewThenImportDecodeIdenticalBytes ([preview-reuses-decode]): preview
// stores, import reads back the SAME object, and both decode to the same facts
// and the same row count.
func TestPreviewThenImportDecodeIdenticalBytes(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantID := seedTenant(t, super, "uo parity tenant")
	entityID := seedEntity(t, super, tenantID, "uo parity entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	rows := uoRows("UO-PARITY", 3)
	content := csvBody(t, uoHeader, rows)

	pBody, previewCT := buildMultipartBody(t, "", "", "parity.csv", "text/csv", content)
	pRec, pRaw, preview := doPreviewUpload(t, docSvc.Store, &id, previewCT, pBody)
	if pRec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200 (body=%s)", pRec.Code, pRaw)
	}
	if len(preview.Columns) != len(uoHeader) {
		t.Fatalf("preview columns = %v, want %v", preview.Columns, uoHeader)
	}

	iBody, importCT := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), preview.DocumentID)
	iRec, iRaw, imported := doImportUpload(t, svc.Import, docSvc.Open, &id, "", importCT, iBody)
	if iRec.Code != http.StatusCreated {
		t.Fatalf("import status = %d, want 201 (body=%s)", iRec.Code, iRaw)
	}

	if imported.RowsTotal != preview.RowsTotal {
		t.Errorf("rows_total: import %d != preview %d", imported.RowsTotal, preview.RowsTotal)
	}
	if imported.Format != preview.Format {
		t.Errorf("format: import %q != preview %q", imported.Format, preview.Format)
	}
	if (imported.Delimiter == nil) != (preview.Delimiter == nil) ||
		(imported.Delimiter != nil && *imported.Delimiter != *preview.Delimiter) {
		t.Errorf("delimiter: import %v != preview %v", imported.Delimiter, preview.Delimiter)
	}
	if (imported.Encoding == nil) != (preview.Encoding == nil) ||
		(imported.Encoding != nil && *imported.Encoding != *preview.Encoding) {
		t.Errorf("encoding: import %v != preview %v", imported.Encoding, preview.Encoding)
	}
	// A different header would have failed resolveMapping, not produced rows.
	if imported.ReadyInvoices != len(rows) {
		t.Errorf("ready_invoices = %d, want %d", imported.ReadyInvoices, len(rows))
	}
}

// TestPreviewThenImport_LongFilenameResolvesSameFormat (G4): SanitizeFilename
// truncates to 255 runes, which cuts the extension off a long name. Preview
// sees the raw name and resolves csv; import sees the stored one. They must
// still agree — the upload is the same upload.
func TestPreviewThenImport_LongFilenameResolvesSameFormat(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantID := seedTenant(t, super, "uo long-name tenant")
	entityID := seedEntity(t, super, tenantID, "uo long-name entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	rows := uoRows("UO-LONG", 2)
	// No explicit part Content-Type: the multipart default is
	// application/octet-stream, so only the filename can carry the format.
	longName := strings.Repeat("a", 300) + ".csv"
	pBody, previewCT := buildMultipartBody(t, "", "", longName, "", csvBody(t, uoHeader, rows))
	pRec, pRaw, preview := doPreviewUpload(t, docSvc.Store, &id, previewCT, pBody)
	if pRec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200 (body=%s)", pRec.Code, pRaw)
	}
	if preview.Format != "csv" {
		t.Fatalf("preview format = %q, want csv", preview.Format)
	}

	iBody, importCT := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), preview.DocumentID)
	iRec, iRaw, imported := doImportUpload(t, svc.Import, docSvc.Open, &id, "", importCT, iBody)

	if iRec.Code != http.StatusCreated {
		t.Fatalf("import status = %d, want 201 — preview accepted this same upload as csv (body=%s)", iRec.Code, iRaw)
	}
	if imported.Format != preview.Format {
		t.Errorf("format: import %q != preview %q", imported.Format, preview.Format)
	}
	if imported.RowsTotal != preview.RowsTotal {
		t.Errorf("rows_total: import %d != preview %d", imported.RowsTotal, preview.RowsTotal)
	}
}

// TestPreviewThenImport_UnsanitizableFilenameResolvesSameFormat (G4): a name
// that sanitizes to "" is stored as SQL NULL, so import reads a nil *string for
// both the name and — if it were absent — the declared type. Dereferencing them
// is the whole spec.
func TestPreviewThenImport_UnsanitizableFilenameResolvesSameFormat(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantID := seedTenant(t, super, "uo blank-name tenant")
	entityID := seedEntity(t, super, tenantID, "uo blank-name entity")
	id := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}

	rows := uoRows("UO-BLANK", 2)
	pBody, previewCT := buildMultipartBody(t, "", "", "   ", "text/csv", csvBody(t, uoHeader, rows))
	pRec, pRaw, preview := doPreviewUpload(t, docSvc.Store, &id, previewCT, pBody)
	if pRec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200 (body=%s)", pRec.Code, pRaw)
	}

	iBody, importCT := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), preview.DocumentID)
	iRec, iRaw, imported := doImportUpload(t, svc.Import, docSvc.Open, &id, "", importCT, iBody)

	if iRec.Code != http.StatusCreated {
		t.Fatalf("import status = %d, want 201 (body=%s)", iRec.Code, iRaw)
	}
	if imported.Format != preview.Format {
		t.Errorf("format: import %q != preview %q", imported.Format, preview.Format)
	}
	// The name sanitized away, so the batch has nothing to record.
	if got := batchColumn(t, super, "filename", imported.ID); got != nil {
		t.Errorf("import_batches.filename = %q, want NULL when the document's name is NULL", *got)
	}
}
