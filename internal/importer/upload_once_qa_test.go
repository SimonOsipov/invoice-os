// upload_once_qa_test.go: QA Mode B coverage on top of the [upload-once]
// contract specs — the dry-run path's own obligations (it needs the document
// like a real import does, and fails closed the same way), the unknown-part
// rule on POST /v1/imports, and the format-resolution parity case that only
// SanitizeFilename's oversized-extension fallback reaches.
package importer

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestImport_DryRunRequiresDocumentID: a dry run decodes the bytes, so it needs
// the id exactly as a real import does. The dry_run query param is parsed AFTER
// the document is opened, so the 400 here is the same one a real import gets —
// pinned because the cheap reading ("a dry run writes nothing, so it needs
// nothing") would make the endpoint answer 200 on a request with no source.
func TestImport_DryRunRequiresDocumentID(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("q.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
		t.Fatal("Import must not run for a dry run with no document_id")
		return BatchResult{}, nil
	}

	body, ct := buildImportForm(t, uuid.NewString(), mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), "")
	rec, raw, resp := doImportUpload(t, imp, open.fn(), &id, "?dry_run=true", ct, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
	}
	if !strings.Contains(resp.Error, "document_id") {
		t.Errorf("error = %q, want it to name document_id", resp.Error)
	}
	if len(open.ids) != 0 {
		t.Errorf("open calls = %d, want 0", len(open.ids))
	}
}

// TestImport_DryRunStorageUnreachableIs500: the dry-run half of [fail-closed].
// A dry run persists nothing either way, so the only observable difference is
// the status — and a 200 here would tell the caller their file previews clean
// when the server never read a byte of it.
func TestImport_DryRunStorageUnreachableIs500(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	objs := newMemObjects()
	docSvc := document.NewService(document.NewStore(app), objs)

	tenantID := seedTenant(t, super, "uo dry-run fail-closed tenant")
	entityID := seedEntity(t, super, tenantID, "uo dry-run fail-closed entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	doc := storeDocumentAs(t, docSvc, tenantID, "q.csv", "text/csv", csvBody(t, uoHeader, uoRows("UO-DRYDOWN", 2)))
	objs.getErr = errOpenBoom

	body, ct := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), doc.ID)
	rec, raw, resp := doImportUpload(t, svc.Import, docSvc.Open, &id, "?dry_run=true", ct, body)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a dry run whose bytes cannot be fetched (body=%s)", rec.Code, raw)
	}
	if strings.Contains(resp.Error, errOpenBoom.Error()) {
		t.Errorf("500 body leaks the internal error: %q", resp.Error)
	}
	if n := countImportBatchesForEntity(t, super, entityID); n != 0 {
		t.Errorf("import_batches rows = %d, want 0", n)
	}
}

// TestImport_DryRunPersistsNothingButDecodesTheDocument: the positive control
// for the two above — with the store reachable, a dry run reaches Import with
// the decoded rows and the document id, and writes nothing.
func TestImport_DryRunPersistsNothingButDecodesTheDocument(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantID := seedTenant(t, super, "uo dry-run decode tenant")
	entityID := seedEntity(t, super, tenantID, "uo dry-run decode entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	rows := uoRows("UO-DRYOK", 3)
	doc := storeDocumentAs(t, docSvc, tenantID, "q.csv", "text/csv", csvBody(t, uoHeader, rows))

	body, ct := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), doc.ID)
	rec, raw, resp := doImportUpload(t, svc.Import, docSvc.Open, &id, "?dry_run=true", ct, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if resp.RowsTotal != len(rows) {
		t.Errorf("rows_total = %d, want %d — the dry run must have decoded the STORED bytes", resp.RowsTotal, len(rows))
	}
	if resp.ID != "" {
		t.Errorf("id = %q, want it omitted on a dry run", resp.ID)
	}
	if n := countImportBatchesForEntity(t, super, entityID); n != 0 {
		t.Errorf("import_batches rows = %d, want 0", n)
	}
	if n := countInvoicesForEntity(t, super, entityID); n != 0 {
		t.Errorf("invoices rows = %d, want 0", n)
	}
}

// TestImport_UnknownPartsIgnoredNotRejected: only "file" is refused on this
// endpoint, and only because it is the retired one. Any other unexpected part
// is ignored — the same rule PreviewHandler follows
// (TestPreviewHandler_ExtraUnexpectedPartIgnored). Asserted at a few bytes
// rather than the 11 MiB the cap probe uses, so a regression here does not have
// to be inferred from a size test.
func TestImport_UnknownPartsIgnoredNotRejected(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("q.csv", "text/csv", csvBody(t, []string{"Inv No"}, [][]string{{"INV-1"}}))

	var ran bool
	imp := func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, h []string, r [][]string, dryRun bool) (BatchResult, error) {
		ran = true
		return BatchResult{ID: uuid.NewString(), Status: "completed", RowsTotal: len(r), RowsValid: len(r)}, nil
	}

	body, ct := buildImportForm(t, uuid.NewString(),
		mustMappingJSON(t, map[string]string{"invoice_number": "Inv No"}), open.doc.ID,
		importPart{field: "note", content: []byte("a stray text field")},
		importPart{field: "attachment", filename: "extra.csv", content: []byte("Inv No\nINV-9\n")})

	rec, raw, _ := doImportUpload(t, imp, open.fn(), &id, "", ct, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 — an unknown part must be ignored, not rejected (body=%s)", rec.Code, raw)
	}
	if !ran {
		t.Error("Import never ran")
	}
	if len(open.ids) != 1 {
		t.Errorf("open calls = %d, want 1", len(open.ids))
	}
}

// TestPreviewThenImport_OversizedExtensionResolvesSameFormat: the branch
// SanitizeFilename's maxKeptExt bound sends to the plain 255-rune cut. Preview
// reads the RAW part filename and import reads the SANITIZED row, so the two
// resolve different strings here — a 344-rune name with a 41-rune "extension"
// versus a 255-rune prefix with no dot at all. Neither resolves a format, so
// both must 400, and the bytes must still be stored and named
// ([error-body-carries-document-id]) even though nothing can parse them.
func TestPreviewThenImport_OversizedExtensionResolvesSameFormat(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})
	docSvc := document.NewService(document.NewStore(app), newMemObjects())

	tenantID := seedTenant(t, super, "uo oversized-ext tenant")
	entityID := seedEntity(t, super, tenantID, "uo oversized-ext entity")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	// No explicit part Content-Type, so the multipart default is
	// application/octet-stream and only the name could carry a format.
	name := strings.Repeat("a", 300) + "." + strings.Repeat("x", 40)
	content := csvBody(t, uoHeader, uoRows("UO-EXT", 2))

	pBody, previewCT := buildMultipartBody(t, "", "", name, "", content)
	pRec, pRaw, preview := doPreviewUpload(t, docSvc.Store, &id, previewCT, pBody)
	if pRec.Code != http.StatusBadRequest {
		t.Fatalf("preview status = %d, want 400 for an unresolvable format (body=%s)", pRec.Code, pRaw)
	}
	if preview.DocumentID == "" {
		t.Fatal("preview 400 carries no document_id — the stored bytes are unretrievable")
	}

	iBody, importCT := buildImportForm(t, entityID, mustMappingJSON(t, uoMapping), preview.DocumentID)
	iRec, iRaw, _ := doImportUpload(t, svc.Import, docSvc.Open, &id, "", importCT, iBody)

	if iRec.Code != http.StatusBadRequest {
		t.Fatalf("import status = %d, want 400 — preview rejected this same upload (body=%s)", iRec.Code, iRaw)
	}

	// The stored name took SanitizeFilename's fallback cut: 255 runes, and the
	// oversized "extension" did not ride through.
	var storedName *string
	if err := super.QueryRow(context.Background(),
		`SELECT filename FROM documents WHERE id = $1`, preview.DocumentID,
	).Scan(&storedName); err != nil {
		t.Fatalf("read documents.filename: %v", err)
	}
	if storedName == nil {
		t.Fatal("documents.filename is NULL, want the truncated name")
	}
	if n := utf8.RuneCountInString(*storedName); n != 255 {
		t.Errorf("documents.filename is %d runes, want 255", n)
	}
	if strings.Contains(*storedName, ".") {
		t.Errorf("documents.filename kept a dot: a %d-rune extension must not survive the cap", 41)
	}

	if n := countImportBatchesForEntity(t, super, entityID); n != 0 {
		t.Errorf("import_batches rows = %d, want 0", n)
	}
}
