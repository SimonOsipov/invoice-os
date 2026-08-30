// document_readback_test.go: EXTR-06-07 (task-767, FINAL subtask) -- proves a document-sourced
// invoice reads back correctly through invoice.Store.SourceDocument and that a document-sourced
// zero-line draft is caught by the real invoice.Gate. Deliberately package importer, not
// invoice: internal/importer already imports internal/invoice (no cycle), so the write-side
// fixture can be the genuine product of Service.ImportDocument rather than a hand-rebuilt
// CreateInput that would trust documentCreateInput's shape instead of proving it. See task-767's
// "Lead correction to the Architecture validation".
//
// Reuses dbTestPools/seedTenant/seedEntity/seedDocument/newTestService/sxIdentity/sxPtr,
// docCleanValues/docSeedExtraction, invoiceIDByNumber/countLineItems (document_service_db_test.go
// and friends, same package), and readAuditForInvoice/startInProcess04ForImporter/impvS2SToken
// (spine_integration_test.go/service_gate_test.go).
//
// Spec-to-test map (Test Specs table, EXTR-06-07 / task-767):
//
//	RB-01/RB-03 TestImportDocumentReadback_SourceRowsNullOtherInvoiceRowsEmptyArray
//	RB-02       TestImportDocumentReadback_DocumentRecordCarriesRightValues
//	RB-04       TestImportDocumentReadback_InvoicesCreatedCountsAtTwoCheckpoints
//	RB-05       TestImportDocumentReadback_AuditActorIsCallerSubjectNotWorkerLiteral
//	RB-06       TestImportDocumentReadback_WrittenInvoiceIsDraftWithZeroLineItems
//	RB-07       TestImporterSpreadsheetPath_UntouchedByDocumentPath
//	RB-08       TestImportDocumentReadback_RealGateLeavesZeroLineDraftWithLineItemsRequired
//
// AC #6 (Core AC 8's extraction half) is NOT met by this story -- D-19
// (.ralph/EXTR-06-finalized.md), now owned by EXTR-17 The Pipeline Runs End To End: nothing wires
// Reconcile's output into the worker yet, and extraction_field_results carries no line-item
// values, so no `line_items=missing` reason row is ever written. There is no oracle for that
// absence, so asserting it would be vacuous; this comment is the story's evidence for AC #6
// instead of a test row.
package importer

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// rbSeedDocumentWithMeta inserts one documents row with caller-chosen filename/size/hash, so
// RB-02 can assert exact values rather than mere presence -- seedDocument (service_source_rows_
// test.go) hardcodes all three.
func rbSeedDocumentWithMeta(t *testing.T, super *pgxpool.Pool, tenantID, filename string, sizeBytes int64, contentHash string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes, filename)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, "rb/"+tenantID+"/"+uuid.NewString(), contentHash, sizeBytes, filename,
	).Scan(&id); err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	return id
}

// --- RB-01 / RB-03 ---------------------------------------------------------

// RB-01/RB-03: opposite requirements on the SAME marshalled SourceDocument body --
// source_rows is null (never recorded on the document path; SourceRows has no omitempty and is
// never coerced), other_invoice_rows is [] (explicitly coerced, source_document.go:90). Asserted
// on one marshal so neither claim can drift from what the other actually saw.
func TestImportDocumentReadback_SourceRowsNullOtherInvoiceRowsEmptyArray(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-01 tenant")
	entityID := seedEntity(t, super, tenantID, "RB-01 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("RB-01-INV"))

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	invID := invoiceIDByNumber(t, super, entityID, "RB-01-INV")
	istore := invoice.NewStore(app)
	got, err := istore.SourceDocument(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal SourceDocument: %v", err)
	}
	if !strings.Contains(string(body), `"source_rows":null`) {
		t.Errorf("body = %s, want \"source_rows\":null (a document-sourced invoice never records sheet rows)", body)
	}
	if !strings.Contains(string(body), `"other_invoice_rows":[]`) {
		t.Errorf("body = %s, want \"other_invoice_rows\":[]", body)
	}
	if strings.Contains(string(body), `"other_invoice_rows":null`) {
		t.Errorf("body = %s, must not contain \"other_invoice_rows\":null", body)
	}
}

// --- RB-02 -------------------------------------------------------------

// RB-02: the document object carries the RIGHT id, filename, size, content hash and
// uploaded_at -- values, not mere presence.
func TestImportDocumentReadback_DocumentRecordCarriesRightValues(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-02 tenant")
	entityID := seedEntity(t, super, tenantID, "RB-02 entity")
	filename := "invoice-rb-02.pdf"
	contentHash := strings.Repeat("b", 64)
	documentID := rbSeedDocumentWithMeta(t, super, tenantID, filename, 54321, contentHash)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("RB-02-INV"))

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	invID := invoiceIDByNumber(t, super, entityID, "RB-02-INV")
	istore := invoice.NewStore(app)
	got, err := istore.SourceDocument(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("SourceDocument: %v", err)
	}
	if got.Document == nil {
		t.Fatal("Document = nil, want a populated record for a document-sourced invoice")
	}
	if got.Document.ID != documentID {
		t.Errorf("Document.ID = %q, want %q", got.Document.ID, documentID)
	}
	if got.Document.Filename == nil || *got.Document.Filename != filename {
		t.Errorf("Document.Filename = %v, want %q", got.Document.Filename, filename)
	}
	if got.Document.SizeBytes != 54321 {
		t.Errorf("Document.SizeBytes = %d, want 54321", got.Document.SizeBytes)
	}
	if got.Document.ContentHash != contentHash {
		t.Errorf("Document.ContentHash = %q, want %q", got.Document.ContentHash, contentHash)
	}
	if got.Document.UploadedAt.IsZero() {
		t.Error("Document.UploadedAt is zero, want the recorded created_at")
	}
}

// --- RB-04 -------------------------------------------------------------

// RB-04: invoices_created counts invoices actually linked to the document, checked at TWO
// points (1 after the first link, 2 after the second) -- a single final-state assertion of 2
// would pass vacuously against a hardcoded literal. The subquery is tenant-scoped, self-
// inclusive and carries no `id <> $2` filter (task-767 Implementation Notes item 4), so two
// ImportDocument runs against the SAME document (each with its own newest-succeeded job) are
// enough: no entity_id column on documents means the fixture doesn't need two entities.
func TestImportDocumentReadback_InvoicesCreatedCountsAtTwoCheckpoints(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-04 tenant")
	entityID := seedEntity(t, super, tenantID, "RB-04 entity")
	documentID := seedDocument(t, super, tenantID)
	svc := newTestService(app)
	istore := invoice.NewStore(app)

	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("RB-04-A"))
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument (first): %v", err)
	}
	invA := invoiceIDByNumber(t, super, entityID, "RB-04-A")
	gotA, err := istore.SourceDocument(sxIdentity(ctx, tenantID), invA)
	if err != nil {
		t.Fatalf("SourceDocument (checkpoint 1): %v", err)
	}
	if gotA.Document == nil {
		t.Fatal("Document = nil at checkpoint 1, want a populated record")
	}
	if gotA.Document.InvoicesCreated != 1 {
		t.Errorf("InvoicesCreated after the first link = %d, want 1", gotA.Document.InvoicesCreated)
	}

	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("RB-04-B"))
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument (second): %v", err)
	}
	invB := invoiceIDByNumber(t, super, entityID, "RB-04-B")
	gotB, err := istore.SourceDocument(sxIdentity(ctx, tenantID), invB)
	if err != nil {
		t.Fatalf("SourceDocument (checkpoint 2): %v", err)
	}
	if gotB.Document == nil {
		t.Fatal("Document = nil at checkpoint 2, want a populated record")
	}
	if gotB.Document.InvoicesCreated != 2 {
		t.Errorf("InvoicesCreated after the second link = %d, want 2", gotB.Document.InvoicesCreated)
	}
}

// --- RB-05 -------------------------------------------------------------

// RB-05: the invoice.created audit_log row's actor is the caller's subject (D-2) -- paired
// with the negative check (never a worker/system literal) so this can't pass on an unrelated
// non-empty string.
func TestImportDocumentReadback_AuditActorIsCallerSubjectNotWorkerLiteral(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-05 tenant")
	entityID := seedEntity(t, super, tenantID, "RB-05 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("RB-05-INV"))

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	invID := invoiceIDByNumber(t, super, entityID, "RB-05-INV")
	rows := readAuditForInvoice(t, app, tenantID, invID)

	var found bool
	for _, r := range rows {
		if r.event != "invoice.created" {
			continue
		}
		found = true
		if r.actor != memberSubject {
			t.Errorf("invoice.created actor = %q, want the caller's subject %q", r.actor, memberSubject)
		}
		if r.actor == "extraction-worker" || r.actor == "system" {
			t.Errorf("invoice.created actor = %q, must not be a worker/system literal (D-2)", r.actor)
		}
	}
	if !found {
		t.Fatal("no invoice.created audit_log row found for this invoice")
	}
}

// --- RB-06 -------------------------------------------------------------

// RB-06: the written invoice is draft with zero line_items -- checked both through the real
// invoice.Store.Get (the API surface Core AC 1 cares about) and a raw row count (a hydration
// bug in Get could otherwise hide a real row).
func TestImportDocumentReadback_WrittenInvoiceIsDraftWithZeroLineItems(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-06 tenant")
	entityID := seedEntity(t, super, tenantID, "RB-06 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("RB-06-INV"))

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	invID := invoiceIDByNumber(t, super, entityID, "RB-06-INV")
	istore := invoice.NewStore(app)
	got, err := istore.Get(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != invoice.StatusDraft {
		t.Errorf("status = %q, want %q", got.Status, invoice.StatusDraft)
	}
	if len(got.LineItems) != 0 {
		t.Errorf("len(LineItems) = %d, want 0 (D-13: nothing extracted feeds line items yet)", len(got.LineItems))
	}
	if got := countLineItems(t, super, invID); got != 0 {
		t.Errorf("line_items rows for the written invoice = %d, want 0", got)
	}
}

// --- RB-08 -------------------------------------------------------------

// RB-08: the REAL invoice.Gate (never fakeGate) over a document-sourced, zero-line draft
// leaves it draft and reports line-items-required -- Core AC 8's invoice half, extending
// TestGate_ValidateZeroLineItemsStaysDraftWithLineItemsRequired (internal/invoice/gate_test.go:
// 371) to a fixture ImportDocument actually produced (D-13 forces zero line items here) rather
// than a hand-built one.
func TestImportDocumentReadback_RealGateLeavesZeroLineDraftWithLineItemsRequired(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-08 tenant")
	entityID := seedEntity(t, super, tenantID, "RB-08 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("RB-08-INV"))

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "RB-08-INV")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	gate := invoice.NewGate(invoice.NewStore(app), validator)

	got, _, err := gate.Validate(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Validate: want a normal (nil-error) BLOCKED outcome, got err: %v", err)
	}
	if got.Status != invoice.StatusDraft {
		t.Errorf("status = %q, want %q", got.Status, invoice.StatusDraft)
	}

	var vs []invoice.Violation
	if err := json.Unmarshal(got.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", got.Violations, err)
	}
	var found bool
	for _, v := range vs {
		if v.RuleKey == "line-items-required" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("violations = %+v, want one naming line-items-required", vs)
	}
}

// --- RB-07 -------------------------------------------------------------

// rbRepoRoot resolves the worktree root the git commands below must run from -- duplicated
// (not imported) from document_deps_test.go's sxDepsRepoRoot, same rationale: unrelated fence,
// different test file.
func rbRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// RB-07: the spreadsheet path's own files are untouched since origin/main. Precondition (an
// unresolvable ref makes an empty diff indistinguishable from a clean one) and control needle
// (document.go must show as added, or the diff read nothing) both guard the absence check below
// from proving nothing (task-767 Implementation Notes). service.go is the one exception: it
// carries an authorized comment-only diff (naming Import/ImportDocument as sibling entrypoints,
// subtask 04's QA), so it is checked for "no non-comment line changed", not byte-identity.
func TestImporterSpreadsheetPath_UntouchedByDocumentPath(t *testing.T) {
	root := rbRepoRoot(t)

	mergeBase := exec.Command("git", "merge-base", "--is-ancestor", "origin/main", "HEAD")
	mergeBase.Dir = root
	if err := mergeBase.Run(); err != nil {
		t.Fatalf("git merge-base --is-ancestor origin/main HEAD: %v -- origin/main is not an ancestor of HEAD, so an empty diff below would be indistinguishable from a clean one", err)
	}

	nameOnly := exec.Command("git", "diff", "--name-only", "origin/main...HEAD", "--", "internal/importer")
	nameOnly.Dir = root
	out, err := nameOnly.Output()
	if err != nil {
		t.Fatalf("git diff --name-only origin/main...HEAD -- internal/importer: %v", err)
	}
	changed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			changed[line] = true
		}
	}

	if !changed["internal/importer/document.go"] {
		t.Fatalf("diff = %v, want it to list internal/importer/document.go as added (control needle -- an empty/wrong diff proves nothing)", out)
	}

	spreadsheetFiles := []string{
		"internal/importer/service_test.go",
		"internal/importer/service_dup_test.go",
		"internal/importer/store.go",
		"internal/importer/store_test.go",
		"internal/importer/dup_parity_test.go",
		"internal/importer/decode.go",
	}
	for _, f := range spreadsheetFiles {
		if changed[f] {
			t.Errorf("diff lists %q as changed, want the spreadsheet path byte-unchanged since origin/main", f)
		}
	}

	serviceDiffCmd := exec.Command("git", "diff", "origin/main...HEAD", "--", "internal/importer/service.go")
	serviceDiffCmd.Dir = root
	serviceDiff, err := serviceDiffCmd.Output()
	if err != nil {
		t.Fatalf("git diff origin/main...HEAD -- internal/importer/service.go: %v", err)
	}
	for _, line := range strings.Split(string(serviceDiff), "\n") {
		if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		content := strings.TrimSpace(line[1:])
		if content == "" {
			continue
		}
		if !strings.HasPrefix(content, "//") {
			t.Errorf("service.go diff has a non-comment changed line: %q, want the diff to touch comments only", line)
		}
	}
}
