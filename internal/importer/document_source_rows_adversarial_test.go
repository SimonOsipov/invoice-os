// document_source_rows_adversarial_test.go: QA adversarial coverage for task-765
// (EXTR-06-05, Core AC 2/3) beyond the Mode A specs in document_source_rows_test.go.
//
//	QA-01 TestServiceImportDocument_AllFieldsUnreadableWithReasonCodesQuarantinesNoInvoiceNoPanic
//	      -- every mapper field present with value NULL + reason_code "unreadable", distinct
//	      from SR-07's literally-zero-rows fixture (this one exercises the reason_code column
//	      and a fully-populated but all-nil values map).
//	QA-02 TestServiceImportDocument_QuarantinePathWritesNoInvoiceRowAtAll -- a quarantined
//	      document mints a batch but the invoices table gets no row for it at all, so there is
//	      no row to carry a bad source_rows in the first place.
//	QA-03 TestInvoices_NoDocumentSourcedInvoiceInTenantHasNonNullSourceRows -- tenant-wide
//	      invariant, both a clean import (writes an invoice) and a quarantine (writes none) in
//	      the same tenant, asserted directly against the DB.
package importer

import (
	"context"
	"testing"
	"time"
)

// QA-01: a succeeded job with a field row for every mapper name, each value NULL and reason
// "unreadable" -- structurally different from SR-07 (zero field rows). documentCreateInput
// must still quarantine on invoice_number without panicking.
func TestServiceImportDocument_AllFieldsUnreadableWithReasonCodesQuarantinesNoInvoiceNoPanic(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "QA-01 tenant")
	entityID := seedEntity(t, super, tenantID, "QA-01 entity")
	documentID := docSeedDocument(t, super, tenantID)

	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	now := time.Now().UTC()
	for i, name := range mapperFieldNames {
		seedExtractionField(t, super, tenantID, job, name, nil, sxPtr("unreadable"), 0, now.Add(time.Duration(i)*time.Millisecond))
	}

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v, want nil -- a mapper RowError must never surface as a returned error (D-9)", err)
	}
	if res.Status != "completed" || res.ReadyInvoices != 0 || res.QuarantinedInvoices != 1 {
		t.Errorf("BatchResult = %+v, want completed/ready=0/quarantined=1", res)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(res.Errors))
	}
	if res.Errors[0].Field != "invoice_number" {
		t.Errorf("Errors[0].Field = %q, want %q", res.Errors[0].Field, "invoice_number")
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices for entity = %d, want 0 -- an all-unreadable extraction must never write a garbage invoice", got)
	}
}

// QA-02: the quarantine path (DOC-03's fixture) leaves zero invoices rows referencing this
// document at all -- not one row with a bad source_rows, no row.
func TestServiceImportDocument_QuarantinePathWritesNoInvoiceRowAtAll(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "QA-02 tenant")
	entityID := seedEntity(t, super, tenantID, "QA-02 entity")
	documentID := docSeedDocument(t, super, tenantID)
	values := docCleanValues("unused")
	values["invoice_number"] = nil
	docSeedExtraction(t, super, tenantID, documentID, values)

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	if res.QuarantinedInvoices != 1 {
		t.Fatalf("QuarantinedInvoices = %d, want 1", res.QuarantinedInvoices)
	}

	var n int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM invoices WHERE source_document_id = $1`, documentID,
	).Scan(&n); err != nil {
		t.Fatalf("count invoices by source_document_id: %v", err)
	}
	if n != 0 {
		t.Errorf("invoices referencing this document = %d, want 0 -- a quarantine must leave no row, not a row with a bad source_rows", n)
	}
}

// QA-03: tenant-wide invariant -- across a clean import (writes one invoice) and a quarantine
// (writes none) in the same tenant, no document-sourced invoice anywhere in the tenant carries
// a non-NULL source_rows.
func TestInvoices_NoDocumentSourcedInvoiceInTenantHasNonNullSourceRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "QA-03 tenant")
	entityID := seedEntity(t, super, tenantID, "QA-03 entity")
	cleanDocID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, cleanDocID, docCleanValues("QA-03-INV"))
	quarantineDocID := docSeedDocument(t, super, tenantID)
	badValues := docCleanValues("unused")
	badValues["invoice_number"] = nil
	docSeedExtraction(t, super, tenantID, quarantineDocID, badValues)

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, cleanDocID); err != nil {
		t.Fatalf("ImportDocument (clean): %v", err)
	}
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, quarantineDocID); err != nil {
		t.Fatalf("ImportDocument (quarantine): %v", err)
	}

	if got := countInvoicesForEntity(t, super, entityID); got != 1 {
		t.Fatalf("invoices for entity = %d, want 1 (only the clean import writes a row)", got)
	}

	var n int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM invoices
		 WHERE tenant_id = $1 AND source_document_id IS NOT NULL AND source_rows IS NOT NULL`,
		tenantID,
	).Scan(&n); err != nil {
		t.Fatalf("count non-NULL source_rows for tenant: %v", err)
	}
	if n != 0 {
		t.Errorf("document-sourced invoices with non-NULL source_rows = %d, want 0", n)
	}
}
