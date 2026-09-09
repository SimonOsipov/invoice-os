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
//	RB-08       TestImportDocumentReadback_RealGateLeavesZeroLineDraftWithLineItemsRequired
//
// RB-07 is not a Go test: CI checks out refs/pull/N/merge with fetch-depth 1, so origin/main is
// unresolvable and the spreadsheet-path diff can't be observed. Verified as PR evidence instead.
//
// AC #6 (Core AC 8's extraction half) was not met by EXTR-06 and is no longer open: EXTR-17
// wired the deployed text reader, and EXTR-24-06 connected the read lines to the invoice, so a
// document import does now write line_items -- see the RealGateNoLongerReportsLineItemsRequired
// and AGridCleanInvoiceCanStillBeRuleBlocked specs below.
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

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
		t.Errorf("len(LineItems) = %d, want 0", len(got.LineItems))
	}
	if got := countLineItems(t, super, invID); got != 0 {
		t.Errorf("line_items rows for the written invoice = %d, want 0", got)
	}
}

// --- RB-08 -------------------------------------------------------------

// RB-08: the REAL invoice.Gate (never fakeGate) over a document-sourced, zero-line draft
// leaves it draft and reports line-items-required -- Core AC 8's invoice half, extending
// TestGate_ValidateZeroLineItemsStaysDraftWithLineItemsRequired (internal/invoice/gate_test.go:
// 371) to a fixture ImportDocument actually produced (docCleanValues seeds no line rows, so
// this fixture is zero-line by construction) rather than a hand-built one.
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

// --- line-item grouping reaches the store (retiring D-13) ------------------------

// rbLineCellsRaw reads every line_items numeric-shaped column as text for invoiceID, so the
// discrimination check below can prove no stored cell carries a HEADER amount, not just that
// the hydrated Go values look right.
func rbLineCellsRaw(t *testing.T, super *pgxpool.Pool, invoiceID string) []string {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT quantity::text, unit_price::text, line_total::text, line_tax::text
		   FROM line_items WHERE invoice_id = $1 ORDER BY line_no`, invoiceID)
	if err != nil {
		t.Fatalf("read line_items raw cells: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var q, u, lt, tax *string
		if err := rows.Scan(&q, &u, &lt, &tax); err != nil {
			t.Fatalf("scan line_items raw cells: %v", err)
		}
		for _, v := range []*string{q, u, lt, tax} {
			if v != nil {
				out = append(out, *v)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate line_items raw cells: %v", err)
	}
	return out
}

// AC-1/AC-2: a document-imported invoice's line_items rows come from the reader, in numeric
// index order -- proven against the REAL Service.ImportDocument and REAL invoice.Store, not a
// hand-built CreateInput. Discrimination: every seeded line cell is a value that appears
// NOWHERE among the header fields, and no stored line_items row carries a header amount.
func TestImportDocumentReadback_WrittenInvoiceCarriesTheReadLines(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-READLINES tenant")
	entityID := seedEntity(t, super, tenantID, "RB-READLINES entity")
	documentID := seedDocument(t, super, tenantID)

	values := docCleanValues("RB-READLINES-INV") // header subtotal/vat/total: 1000.00/75.00/1075.00
	values["line_items[1].description"] = sxPtr("Line One Widget")
	values["line_items[1].quantity"] = sxPtr("2")
	values["line_items[1].unit_price"] = sxPtr("11.11")
	values["line_items[1].line_total"] = sxPtr("22.22")
	values["line_items[2].description"] = sxPtr("Line Two Gadget")
	values["line_items[2].quantity"] = sxPtr("3")
	values["line_items[2].unit_price"] = sxPtr("33.33")
	values["line_items[2].line_total"] = sxPtr("99.99")
	values["line_items[3].description"] = sxPtr("Line Three Gizmo")
	values["line_items[3].quantity"] = sxPtr("4")
	values["line_items[3].unit_price"] = sxPtr("44.44")
	values["line_items[3].line_total"] = sxPtr("177.76")
	docSeedExtraction(t, super, tenantID, documentID, values)
	if got, want := docCountExtractionFields(t, super, documentID), len(values); got != want {
		t.Fatalf("seeded %d extraction_field_results row(s), want %d -- the fixture did not land, so the read-back below would be vacuous", got, want)
	}

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "RB-READLINES-INV")

	istore := invoice.NewStore(app)
	got, err := istore.Get(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.LineItems) != 3 {
		t.Fatalf("len(LineItems) = %d, want 3", len(got.LineItems))
	}

	wantDescs := []string{"Line One Widget", "Line Two Gadget", "Line Three Gizmo"}
	wantQty := []string{"2.000", "3.000", "4.000"} // line_items.quantity is numeric(14,3)
	wantPrice := []string{"11.11", "33.33", "44.44"}
	wantTotal := []string{"22.22", "99.99", "177.76"}
	for i, li := range got.LineItems {
		if li.LineNo != i+1 {
			t.Errorf("LineItems[%d].LineNo = %d, want %d", i, li.LineNo, i+1)
		}
		if li.Description == nil || *li.Description != wantDescs[i] {
			t.Errorf("LineItems[%d].Description = %v, want %q", i, li.Description, wantDescs[i])
		}
		if li.Quantity == nil || *li.Quantity != wantQty[i] {
			t.Errorf("LineItems[%d].Quantity = %v, want %q", i, li.Quantity, wantQty[i])
		}
		if li.UnitPrice == nil || *li.UnitPrice != wantPrice[i] {
			t.Errorf("LineItems[%d].UnitPrice = %v, want %q", i, li.UnitPrice, wantPrice[i])
		}
		if li.LineTotal == nil || *li.LineTotal != wantTotal[i] {
			t.Errorf("LineItems[%d].LineTotal = %v, want %q", i, li.LineTotal, wantTotal[i])
		}
	}

	if n := countLineItems(t, super, invID); n != 3 {
		t.Errorf("raw line_items rows = %d, want 3", n)
	}

	// Discrimination: no stored cell equals a header amount -- the lines came from the reader,
	// not from the header row leaking into line_items by accident.
	for _, headerVal := range []string{"1000.00", "75.00", "1075.00"} {
		for _, cell := range rbLineCellsRaw(t, super, invID) {
			if cell == headerVal {
				t.Errorf("a stored line_items cell = %q, which is a header amount -- lines must never carry header values", headerVal)
			}
		}
	}
}

// AC-1: per-line VAT (line_tax) reaches the stored row and invoice.SubmissionCanonical, using
// a value distinct from the header vat so a mapper that accidentally read the header field
// instead cannot pass.
func TestImportDocumentReadback_PerLineVatReachesTheStoredRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-LINETAX tenant")
	entityID := seedEntity(t, super, tenantID, "RB-LINETAX entity")
	documentID := seedDocument(t, super, tenantID)

	values := docCleanValues("RB-LINETAX-INV") // header vat: 75.00
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].line_tax"] = sxPtr("12.34") // deliberately not the header vat
	docSeedExtraction(t, super, tenantID, documentID, values)

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "RB-LINETAX-INV")

	istore := invoice.NewStore(app)
	got, err := istore.Get(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.LineItems) != 1 {
		t.Fatalf("len(LineItems) = %d, want 1", len(got.LineItems))
	}
	if got.LineItems[0].LineTax == nil || *got.LineItems[0].LineTax != "12.34" {
		t.Fatalf("LineItems[0].LineTax = %v, want %q -- the assertions below then prove nothing", got.LineItems[0].LineTax, "12.34")
	}

	canonical := invoice.SubmissionCanonical(got)
	if len(canonical.Lines) != 1 {
		t.Fatalf("len(SubmissionCanonical.Lines) = %d, want 1", len(canonical.Lines))
	}
	if canonical.Lines[0].LineTax == nil || *canonical.Lines[0].LineTax != "12.34" {
		t.Errorf("SubmissionCanonical.Lines[0].LineTax = %v, want %q", canonical.Lines[0].LineTax, "12.34")
	}
}

// AC-6: a document-imported invoice whose table read cleanly is NOT blocked by
// line-items-required once the REAL invoice.Gate runs over it. Attribution control:
// TestImportDocumentReadback_RealGateLeavesZeroLineDraftWithLineItemsRequired (above, KEPT
// GREEN) proves the same rule still fires on a line-less fixture, so a pass here is caused by
// the seeded lines, not by an unrelated change to the gate.
func TestImportDocumentReadback_RealGateNoLongerReportsLineItemsRequired(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-GATELINES tenant")
	entityID := seedEntity(t, super, tenantID, "RB-GATELINES entity")
	documentID := seedDocument(t, super, tenantID)

	values := docCleanValues("RB-GATELINES-INV") // header subtotal: 1000.00
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].quantity"] = sxPtr("2")
	values["line_items[1].unit_price"] = sxPtr("100.00")
	values["line_items[1].line_total"] = sxPtr("200.00")
	values["line_items[2].description"] = sxPtr("Gadget")
	values["line_items[2].quantity"] = sxPtr("1")
	values["line_items[2].unit_price"] = sxPtr("300.00")
	values["line_items[2].line_total"] = sxPtr("300.00")
	values["line_items[3].description"] = sxPtr("Widget XL")
	values["line_items[3].quantity"] = sxPtr("1")
	values["line_items[3].unit_price"] = sxPtr("500.00")
	values["line_items[3].line_total"] = sxPtr("500.00") // 200+300+500 = 1000.00, the header subtotal
	docSeedExtraction(t, super, tenantID, documentID, values)
	if got, want := docCountExtractionFields(t, super, documentID), len(values); got != want {
		t.Fatalf("seeded %d extraction_field_results row(s), want %d -- the fixture did not land, so the gate assertion below would be vacuous", got, want)
	}

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "RB-GATELINES-INV")

	// Positive companion for the absence assertion below: the rule can only be silent for the
	// right reason if the rows it looks for are actually there.
	if n := countLineItems(t, super, invID); n != 3 {
		t.Fatalf("the imported invoice holds %d line_items row(s), want 3 -- the gate assertion below would pass for the wrong reason", n)
	}

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	gate := invoice.NewGate(invoice.NewStore(app), validator)

	got, _, err := gate.Validate(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Validate: want a normal (nil-error) outcome, got err: %v", err)
	}

	var vs []invoice.Violation
	if err := json.Unmarshal(got.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", got.Violations, err)
	}
	for _, v := range vs {
		if v.RuleKey == "line-items-required" {
			t.Errorf("violations = %+v, still names line-items-required despite 3 seeded line rows", vs)
		}
	}
}

// --- Adversarial line-item coverage (QA, task-991 Mode B) ---------------------------------

// TestImportDocumentReadback_LineNoFollowsNumericIndexAcrossTen is AC-2's written-invoice half.
// The three specs above all seed indices 1..3, where a lexicographic sort and a numeric one
// agree; only an index past 9 separates them. docSeedExtraction seeds the non-header names in
// sorted-name order, so "line_items[10].*" is written to extraction_field_results FIRST -- the
// arrival order and the required order genuinely disagree here.
func TestImportDocumentReadback_LineNoFollowsNumericIndexAcrossTen(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-TENIDX tenant")
	entityID := seedEntity(t, super, tenantID, "RB-TENIDX entity")
	documentID := seedDocument(t, super, tenantID)

	values := docCleanValues("RB-TENIDX-INV")
	values["line_items[10].description"] = sxPtr("Index Ten")
	values["line_items[2].description"] = sxPtr("Index Two")
	values["line_items[1].description"] = sxPtr("Index One")
	docSeedExtraction(t, super, tenantID, documentID, values)
	if got, want := docCountExtractionFields(t, super, documentID), len(values); got != want {
		t.Fatalf("seeded %d extraction_field_results row(s), want %d -- the fixture did not land", got, want)
	}

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "RB-TENIDX-INV")

	got, err := invoice.NewStore(app).Get(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.LineItems) != 3 {
		t.Fatalf("len(LineItems) = %d, want 3", len(got.LineItems))
	}
	for i, want := range []string{"Index One", "Index Two", "Index Ten"} {
		if got.LineItems[i].LineNo != i+1 {
			t.Errorf("LineItems[%d].LineNo = %d, want %d", i, got.LineItems[i].LineNo, i+1)
		}
		if got.LineItems[i].Description == nil || *got.LineItems[i].Description != want {
			t.Errorf("line_no %d reads %v, want %q -- a lexicographic sort puts index 10 second", i+1, got.LineItems[i].Description, want)
		}
	}
}

// TestImportDocumentReadback_ANullOnlyLineIsStillWritten: the store half of
// TestDocumentCreateInput_ALineWhoseEveryCellIsNullStillProducesAnEntry. An all-NULL
// LineItemInput is a legal line_items row, so the ordinal a reviewer saw survives the write.
func TestImportDocumentReadback_ANullOnlyLineIsStillWritten(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-NULLLINE tenant")
	entityID := seedEntity(t, super, tenantID, "RB-NULLLINE entity")
	documentID := seedDocument(t, super, tenantID)

	values := docCleanValues("RB-NULLLINE-INV")
	values["line_items[1].description"] = sxPtr("Real Row")
	values["line_items[2].description"] = nil
	values["line_items[3].description"] = sxPtr("Third Row")
	docSeedExtraction(t, super, tenantID, documentID, values)

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "RB-NULLLINE-INV")

	got, err := invoice.NewStore(app).Get(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.LineItems) != 3 {
		t.Fatalf("len(LineItems) = %d, want 3 -- an all-NULL line keeps its ordinal", len(got.LineItems))
	}
	if got.LineItems[1].Description != nil {
		t.Errorf("LineItems[1].Description = %v, want nil", got.LineItems[1].Description)
	}
	if got.LineItems[2].Description == nil || *got.LineItems[2].Description != "Third Row" {
		t.Errorf("LineItems[2].Description = %v, want %q", got.LineItems[2].Description, "Third Row")
	}
}

// TestImportDocumentReadback_LinesWithoutAnInvoiceNumberWriteNothing: the persisted half of
// document.go's "grouping runs after the quarantine branch" note. A table that read cleanly but
// no invoice number quarantines whole -- no invoice, and so no line_items row either.
func TestImportDocumentReadback_LinesWithoutAnInvoiceNumberWriteNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "RB-NONUM tenant")
	entityID := seedEntity(t, super, tenantID, "RB-NONUM entity")
	documentID := seedDocument(t, super, tenantID)

	values := docCleanValues("RB-NONUM-INV")
	delete(values, "invoice_number")
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].unit_price"] = sxPtr("10.00")
	values["line_items[2].description"] = sxPtr("Gadget")
	docSeedExtraction(t, super, tenantID, documentID, values)
	if got, want := docCountExtractionFields(t, super, documentID), len(values); got != want {
		t.Fatalf("seeded %d extraction_field_results row(s), want %d -- the fixture did not land", got, want)
	}

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: want a quarantined domain outcome, got err: %v", err)
	}
	if res.RowsInvalid != 1 || len(res.Errors) == 0 {
		t.Fatalf("RowsInvalid = %d with %d error(s), want 1 and at least one RowError", res.RowsInvalid, len(res.Errors))
	}
	if res.Errors[0].Field != "invoice_number" {
		t.Errorf("RowError.Field = %q, want %q", res.Errors[0].Field, "invoice_number")
	}
	if n := countLineItemsForEntity(t, super, entityID); n != 0 {
		t.Errorf("line_items for the entity = %d, want 0 -- a quarantined document writes no lines", n)
	}
}

// --- The newly-reachable sum rule, characterised against the REAL gate (QA) -------------------
//
// line-items-sum-subtotal (evaluator line_sum, tolerance 0.005) never evaluated real extracted
// data before EXTR-24-06 made lines reach the invoice. These characterise that it now does, on
// the surfaces that already report a document-import outcome -- legible only once Re-validate
// runs (ImportDocument stamps no rule_set_version), never on import itself.

// ac1Fixture seeds a 3-line invoice whose qty*unit_price folds to 1000.00 against a printed
// subtotal of 1200.00 -- shared by the AC-1/AC-3 pair so both read the identical shortfall.
func ac1Fixture(t *testing.T, super *pgxpool.Pool, tenantID, documentID, invoiceNumber string) {
	t.Helper()
	values := docCleanValues(invoiceNumber)
	values["subtotal"] = sxPtr("1200.00")
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].quantity"] = sxPtr("2")
	values["line_items[1].unit_price"] = sxPtr("100.00")
	values["line_items[1].line_total"] = sxPtr("200.00")
	values["line_items[2].description"] = sxPtr("Gadget")
	values["line_items[2].quantity"] = sxPtr("3")
	values["line_items[2].unit_price"] = sxPtr("100.00")
	values["line_items[2].line_total"] = sxPtr("300.00")
	values["line_items[3].description"] = sxPtr("Widget XL")
	values["line_items[3].quantity"] = sxPtr("1")
	values["line_items[3].unit_price"] = sxPtr("500.00")
	values["line_items[3].line_total"] = sxPtr("500.00") // 200+300+500 = 1000.00, printed subtotal 1200.00
	docSeedExtraction(t, super, tenantID, documentID, values)
	if got, want := docCountExtractionFields(t, super, documentID), len(values); got != want {
		t.Fatalf("seeded %d extraction_field_results row(s), want %d -- the fixture did not land", got, want)
	}
}

// AC-1: a document-imported invoice whose read lines do not sum to the printed subtotal is left
// draft by the REAL gate and carries line-items-sum-subtotal, not line-items-required.
// Mutation: `LineItems: lineItems` -> `LineItems: nil` in documentCreateInput (document.go) --
// line-items-required returns and this rule goes silent.
func TestImportDocumentReadback_LinesThatMissTheSubtotalBlockOnTheSumRule(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AC1 tenant")
	entityID := seedEntity(t, super, tenantID, "AC1 entity")
	documentID := seedDocument(t, super, tenantID)
	ac1Fixture(t, super, tenantID, documentID, "AC1-INV")

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "AC1-INV")

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
	var haveSum, haveRequired bool
	for _, v := range vs {
		switch v.RuleKey {
		case "line-items-sum-subtotal":
			haveSum = true
		case "line-items-required":
			haveRequired = true
		}
	}
	if !haveSum {
		t.Errorf("violations = %+v, want one naming line-items-sum-subtotal", vs)
	}
	if haveRequired {
		t.Errorf("violations = %+v, must not name line-items-required -- 3 lines were seeded", vs)
	}
}

// AC-3: the line-items-sum-subtotal violation names the shortfall as exact decimal strings --
// Expected is the folded line sum (1000), Actual the printed subtotal (1200): evaluators_math.go
// hoists `declared` once and reuses it for both the comparison and Actual (Stage 2 correction
// C3), and decimal.String() trims trailing zeros, so this is "1000"/"1200", never "1000.00".
// Mutation: swap withExpected(sum.String())/withActual(declared.String()) at evaluators_math.go:275.
func TestImportDocumentReadback_TheSumViolationNamesTheShortfall(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AC3 tenant")
	entityID := seedEntity(t, super, tenantID, "AC3 entity")
	documentID := seedDocument(t, super, tenantID)
	ac1Fixture(t, super, tenantID, documentID, "AC3-INV")

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "AC3-INV")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	gate := invoice.NewGate(invoice.NewStore(app), validator)

	got, _, err := gate.Validate(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}

	var vs []invoice.Violation
	if err := json.Unmarshal(got.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", got.Violations, err)
	}
	var found *invoice.Violation
	for i := range vs {
		if vs[i].RuleKey == "line-items-sum-subtotal" {
			found = &vs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("violations = %+v, want one naming line-items-sum-subtotal", vs)
	}
	if found.Path != "subtotal" {
		t.Errorf("Path = %q, want %q", found.Path, "subtotal")
	}
	if found.Expected == nil || *found.Expected != "1000" {
		t.Errorf("Expected = %v, want %q -- the folded line sum, decimal-trimmed", found.Expected, "1000")
	}
	if found.Actual == nil || *found.Actual != "1200" {
		t.Errorf("Actual = %v, want %q -- the printed subtotal", found.Actual, "1200")
	}
}

// ac2Fixture seeds the two-line divergent fixture: every per-row residual sits exactly on the
// reconciler's 0.01 boundary (so the grid reads clean -- see TestReconcileLines_
// TheDivergentFixtureRaisesNoRowFlag and lineItems.test.ts's sibling), while the fold at the
// rule's tighter 0.005 tolerance is 0.02 off (10.00*3 + 5.00*2 = 40.00 against printed 40.02).
func ac2Fixture(t *testing.T, super *pgxpool.Pool, tenantID, documentID, invoiceNumber string) {
	t.Helper()
	values := docCleanValues(invoiceNumber)
	values["subtotal"] = sxPtr("40.02")
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].quantity"] = sxPtr("3")
	values["line_items[1].unit_price"] = sxPtr("10.00")
	values["line_items[1].line_total"] = sxPtr("30.01")
	values["line_items[2].description"] = sxPtr("Gadget")
	values["line_items[2].quantity"] = sxPtr("2")
	values["line_items[2].unit_price"] = sxPtr("5.00")
	values["line_items[2].line_total"] = sxPtr("10.01")
	ac2AssertStillDivergent(t, values)
	docSeedExtraction(t, super, tenantID, documentID, values)
	if got, want := docCountExtractionFields(t, super, documentID), len(values); got != want {
		t.Fatalf("seeded %d extraction_field_results row(s), want %d -- the fixture did not land", got, want)
	}
}

var ac2ReconcileToleranceRe = regexp.MustCompile(`const\s+reconcileTolerance\s*=\s*"([^"]+)"`)

// ac2AssertStillDivergent re-derives the divergence from the numbers ac2Fixture actually seeds,
// against the reconciler's LIVE constant -- so editing either the fixture or reconcileTolerance
// reddens here instead of silently falsifying the "grid-clean" half, which is asserted in
// packages this one may not import (document_deps_test.go's SX-09 fence).
func ac2AssertStillDivergent(t *testing.T, values map[string]*string) {
	t.Helper()

	src, err := os.ReadFile("../extraction/reconcile.go")
	if err != nil {
		t.Fatalf("read ../extraction/reconcile.go: %v", err)
	}
	m := ac2ReconcileToleranceRe.FindSubmatch(src)
	if m == nil {
		t.Fatal("reconcileTolerance not found in ../extraction/reconcile.go")
	}
	tol, err := decimal.NewFromString(string(m[1]))
	if err != nil {
		t.Fatalf("parse reconcileTolerance %q: %v", m[1], err)
	}

	dec := func(key string) decimal.Decimal {
		t.Helper()
		p := values[key]
		if p == nil {
			t.Fatalf("fixture has no %s", key)
		}
		d, err := decimal.NewFromString(*p)
		if err != nil {
			t.Fatalf("parse %s = %q: %v", key, *p, err)
		}
		return d
	}

	printedSubtotal := dec("subtotal")
	lineTotalSum, foldSum := decimal.Zero, decimal.Zero
	for i := 1; ; i++ {
		q := fmt.Sprintf("line_items[%d].quantity", i)
		if values[q] == nil {
			if i == 1 {
				t.Fatal("fixture seeded no line rows")
			}
			break
		}
		lineTotal := dec(fmt.Sprintf("line_items[%d].line_total", i))
		fold := dec(q).Mul(dec(fmt.Sprintf("line_items[%d].unit_price", i)))
		if residual := fold.Sub(lineTotal).Abs(); !residual.Equal(tol) {
			t.Errorf("row %d residual = %s, want exactly %s -- the row must sit ON the reconciler boundary, so no row flags", i, residual, tol)
		}
		lineTotalSum = lineTotalSum.Add(lineTotal)
		foldSum = foldSum.Add(fold)
	}

	if !lineTotalSum.Equal(printedSubtotal) {
		t.Errorf("sum of line_total = %s, printed subtotal = %s -- they must agree exactly, or the grid's sum sentence is not clean", lineTotalSum, printedSubtotal)
	}
	if foldSum.Equal(printedSubtotal) {
		t.Errorf("fold of quantity x unit_price = %s equals the printed subtotal -- the blocking rule would have nothing to report", foldSum)
	}
}

// AC-2: a grid-clean invoice can still be rule-blocked. Every row reads 'ok' and the grid's own
// sum sentence agrees (see the reconcile/lineItems siblings), yet the REAL gate blocks the same
// invoice on line-items-sum-subtotal -- the new, confusing, user-visible outcome this story
// makes reachable. Asserting only the block would miss the point; the grid-clean half is proven
// by this test's siblings over the identical numbers.
// Mutation: raise the seeded line-items-sum-subtotal tolerance above 0.02 (DB-level, verified
// and reverted by hand -- Out of Scope forbids a committed change to any seeded tolerance).
func TestImportDocumentReadback_AGridCleanInvoiceCanStillBeRuleBlocked(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AC2 tenant")
	entityID := seedEntity(t, super, tenantID, "AC2 entity")
	documentID := seedDocument(t, super, tenantID)
	ac2Fixture(t, super, tenantID, documentID, "AC2-INV")

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "AC2-INV")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	gate := invoice.NewGate(invoice.NewStore(app), validator)

	got, _, err := gate.Validate(sxIdentity(ctx, tenantID), invID)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Status != invoice.StatusDraft {
		t.Errorf("status = %q, want %q -- a grid-clean invoice can still be blocked by the sum rule", got.Status, invoice.StatusDraft)
	}

	var vs []invoice.Violation
	if err := json.Unmarshal(got.Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", got.Violations, err)
	}
	var haveSum, haveRequired bool
	var expected, actual string
	for _, v := range vs {
		switch v.RuleKey {
		case "line-items-sum-subtotal":
			haveSum = true
			if v.Expected != nil {
				expected = *v.Expected
			}
			if v.Actual != nil {
				actual = *v.Actual
			}
		case "line-items-required":
			haveRequired = true
		}
	}
	if !haveSum {
		t.Fatalf("violations = %+v, want one naming line-items-sum-subtotal", vs)
	}
	if expected != "40" || actual != "40.02" {
		t.Errorf("Expected/Actual = %q/%q, want \"40\"/\"40.02\"", expected, actual)
	}
	if haveRequired {
		t.Errorf("violations = %+v, must not name line-items-required -- 2 lines were seeded", vs)
	}
}

// AC-4: the blocked invoice is returned by the review screen's own batch query -- Store.List
// filtered by ImportBatchIDs, RLS-scoped. Moved here from internal/invoice (Correction 4): that
// package cannot import internal/importer (cycle), so a real ImportDocument-produced batch id is
// only reachable from this package.
// Mutation: drop the `import_batch_id = ANY(...)` clause at store.go -- the tenant's OTHER,
// unrelated batch then leaks into this one's result and the total/len assertion fails. (RLS is
// a separate, orthogonal guard: the cross-tenant companion below stays 0/0 regardless of this
// clause, since RLS scopes by tenant independently of any WHERE filter.)
func TestImportDocumentReadback_TheBlockedInvoiceIsReturnedByItsBatchQuery(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AC4A tenant")
	otherTenantID := seedTenant(t, super, "AC4A other tenant")
	entityID := seedEntity(t, super, tenantID, "AC4A entity")
	documentID := seedDocument(t, super, tenantID)
	ac2Fixture(t, super, tenantID, documentID, "AC4A-INV")

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "AC4A-INV")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	gate := invoice.NewGate(invoice.NewStore(app), validator)
	if _, _, err := gate.Validate(sxIdentity(ctx, tenantID), invID); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// A second, unrelated batch for the SAME tenant -- without this, dropping the
	// import_batch_id filter would still leave total=1 (the tenant's only invoice) and the
	// mutation below would pass unnoticed.
	otherDocumentID := rbSeedDocumentWithMeta(t, super, tenantID, "ac4a-other.pdf", 1, strings.Repeat("c", 64))
	docSeedExtraction(t, super, tenantID, otherDocumentID, docCleanValues("AC4A-OTHER-INV"))
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, otherDocumentID); err != nil {
		t.Fatalf("ImportDocument (second batch): %v", err)
	}

	istore := invoice.NewStore(app)
	items, total, err := istore.List(sxIdentity(ctx, tenantID), invoice.ListFilter{ImportBatchIDs: []string{res.ID}, Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("List(ImportBatchIDs: [batch]).total/len = %d/%d, want 1/1 -- the tenant's OTHER batch must not leak in", total, len(items))
	}
	var vs []invoice.Violation
	if err := json.Unmarshal(items[0].Violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", items[0].Violations, err)
	}
	var found bool
	for _, v := range vs {
		if v.RuleKey == "line-items-sum-subtotal" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("returned invoice's violations = %+v, want one naming line-items-sum-subtotal", vs)
	}

	crossItems, crossTotal, err := istore.List(sxIdentity(ctx, otherTenantID), invoice.ListFilter{ImportBatchIDs: []string{res.ID}, Limit: 50})
	if err != nil {
		t.Fatalf("List (other tenant, same batch id): %v", err)
	}
	if crossTotal != 0 || len(crossItems) != 0 {
		t.Errorf("List (other tenant, same batch id).total/len = %d/%d, want 0/0", crossTotal, len(crossItems))
	}
}

// AC-4: violationSummary's rule-key rail counts the sum rule for the batch.
// Mutation: seed the violation with an empty rule_key -- ViolationSummary's
// `nullif(v->>'rule_key', ”) IS NOT NULL` guard drops it (verified by hand against this test's
// own seeded row; TestViolationSummary_EmptyOrMissingRuleKeyExcludedByNullifGuard pins the SQL
// clause itself in isolation).
func TestImportDocumentReadback_ViolationSummaryCountsTheSumRuleForTheBatch(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "AC4B tenant")
	entityID := seedEntity(t, super, tenantID, "AC4B entity")
	documentID := seedDocument(t, super, tenantID)
	ac2Fixture(t, super, tenantID, documentID, "AC4B-INV")

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	invID := invoiceIDByNumber(t, super, entityID, "AC4B-INV")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	gate := invoice.NewGate(invoice.NewStore(app), validator)
	if _, _, err := gate.Validate(sxIdentity(ctx, tenantID), invID); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	istore := invoice.NewStore(app)
	summary, err := istore.ViolationSummary(sxIdentity(ctx, tenantID), []string{res.ID})
	if err != nil {
		t.Fatalf("ViolationSummary: %v", err)
	}
	if len(summary) == 0 {
		t.Fatal("ViolationSummary returned no rows; the count check below would be vacuous")
	}
	var found bool
	for _, rc := range summary {
		if rc.RuleKey == "line-items-sum-subtotal" {
			found = true
			if rc.Invoices != 1 {
				t.Errorf("RuleCount.Invoices for line-items-sum-subtotal = %d, want 1", rc.Invoices)
			}
		}
	}
	if !found {
		t.Errorf("summary = %+v, want a RuleCount naming line-items-sum-subtotal", summary)
	}
}
