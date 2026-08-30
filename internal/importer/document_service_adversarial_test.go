// document_service_adversarial_test.go: post-implementation QA coverage for
// Service.ImportDocument (EXTR-06-03, task-763) beyond the 13 DOC-* specs authored red in
// document_service_db_test.go -- a sequential duplicate-number collision (D-12's own resolved
// id, never exercised by any DOC-* spec), exact persisted rows_valid/rows_invalid values on both
// quarantine paths (DOC-09 only checks their SUM, which is swap-invariant), every non-succeeded
// extraction_jobs state (D-10), same-document re-import (same and different entity), and the
// written invoice's source_rows column.
package importer

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- duplicate-id resolution (D-12) -----------------------------------------

// A sequential (non-racing) duplicate: doc1 imports cleanly, doc2 carries the identical
// invoice_number. ExistingNumbers's upfront precheck must resolve doc1's stored invoice id, and
// the quarantined RowError must carry it -- an empty InvoiceID here means the precheck map never
// reaches storeDuplicateRowError, defeating D-12's whole reason for keeping the fast path (EXTR-15
// needs that id to link the review screen back to the invoice already on file).
func TestServiceImportDocument_SequentialDuplicateNumberCarriesCollidingInvoiceID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-DOC tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-DOC entity")
	doc1 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc1, docCleanValues("DUP-DOC-INV"))
	doc2 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc2, docCleanValues("DUP-DOC-INV"))

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	res1, err := svc.ImportDocument(callCtx, entityID, doc1)
	if err != nil {
		t.Fatalf("ImportDocument(doc1): %v", err)
	}
	if res1.ReadyInvoices != 1 {
		t.Fatalf("first import = %+v, want a clean write", res1)
	}
	firstInvoiceID := invoiceIDByNumber(t, super, entityID, "DUP-DOC-INV")
	if firstInvoiceID == "" {
		t.Fatal("firstInvoiceID = \"\", fixture setup did not actually write an invoice")
	}

	res2, err := svc.ImportDocument(callCtx, entityID, doc2)
	if err != nil {
		t.Fatalf("ImportDocument(doc2): %v, want nil -- a duplicate number is a domain outcome, not a returned error", err)
	}
	if res2.Status != "completed" || res2.QuarantinedInvoices != 1 || res2.ReadyInvoices != 0 {
		t.Fatalf("second import = %+v, want a quarantined duplicate", res2)
	}
	if len(res2.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(res2.Errors))
	}
	if res2.Errors[0].RuleKey != ruleKeyDuplicateInvoiceNumber {
		t.Errorf("RuleKey = %q, want %q", res2.Errors[0].RuleKey, ruleKeyDuplicateInvoiceNumber)
	}
	if res2.Errors[0].InvoiceID != firstInvoiceID {
		t.Errorf("InvoiceID = %q, want the FIRST invoice's id %q -- the ExistingNumbers fast-path resolution is not reaching the quarantine error", res2.Errors[0].InvoiceID, firstInvoiceID)
	}

	if got := countInvoicesByNumber(t, super, entityID, "DUP-DOC-INV"); got != 1 {
		t.Errorf("invoices DUP-DOC-INV = %d, want 1 (the second Create must roll back on 23505)", got)
	}
}

// --- exact rows_valid/rows_invalid on the quarantine paths -----------------

// DOC-09 (document_service_db_test.go) only checks rows_valid + rows_invalid == rows_total on
// the persisted row -- an invariant that is SWAP-INVARIANT (0+1 and 1+0 both sum to 1) and so
// cannot see rows_valid/rows_invalid transposed. This asserts the exact persisted values on both
// quarantine paths: a quarantined row is invalid, never valid.
func TestServiceImportDocument_QuarantinePathPersistsExactRowsValidInvalid(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(values map[string]*string)
	}{
		{name: "mapper-error", mutate: func(v map[string]*string) { v["invoice_number"] = nil }},
		{name: "domain-error", mutate: func(v map[string]*string) { v["subtotal"] = sxPtr("not-a-number") }},
	}
	super, app := dbTestPools(t)
	svc := newTestService(app)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tenantID := seedTenant(t, super, "QROW "+tc.name)
			entityID := seedEntity(t, super, tenantID, "QROW entity "+tc.name)
			documentID := seedDocument(t, super, tenantID)
			values := docCleanValues("QROW-" + tc.name)
			tc.mutate(values)
			docSeedExtraction(t, super, tenantID, documentID, values)

			if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
				t.Fatalf("ImportDocument: %v", err)
			}

			_, _, _, valid, invalid := docBatchRowByEntity(t, super, entityID)
			if valid != 0 || invalid != 1 {
				t.Errorf("persisted rows_valid=%d rows_invalid=%d, want 0/1", valid, invalid)
			}
		})
	}
}

// --- D-10 under every non-succeeded job state -------------------------------

// A document whose newest job is queued, extracting, failed, or dead_lettered must return
// ErrNotFound and mint no import_batches row, for every non-succeeded state -- not just the one
// state (failed) DOC-02 covers.
func TestServiceImportDocument_EveryNonSucceededJobStateReturnsErrNotFoundMintsNoBatch(t *testing.T) {
	states := []string{"queued", "extracting", "failed", "dead_lettered"}
	super, app := dbTestPools(t)
	svc := newTestService(app)

	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			tenantID := seedTenant(t, super, "D10 "+state)
			entityID := seedEntity(t, super, tenantID, "D10 entity "+state)
			documentID := seedDocument(t, super, tenantID)
			seedExtractionJob(t, super, tenantID, documentID, state, time.Now().UTC())

			var seeded int
			if err := super.QueryRow(ctx,
				`SELECT count(*) FROM extraction_jobs WHERE document_id = $1 AND state = $2`, documentID, state,
			).Scan(&seeded); err != nil {
				t.Fatalf("count seeded extraction_jobs: %v", err)
			}
			if seeded != 1 {
				t.Fatalf("seeded extraction_jobs (state %s) = %d, want 1 -- fixture setup is broken", state, seeded)
			}

			if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("ImportDocument (job state %s) err = %v, want ErrNotFound", state, err)
			}
			if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
				t.Errorf("import_batches for entity (job state %s) = %d, want 0", state, got)
			}
		})
	}
}

// --- re-importing the same document -----------------------------------------

// PINNED: ImportDocument carries no dedup-by-document guard. A second call against the same
// document under the SAME entity mints its own batch and re-derives the identical
// invoice_number from the same settled extraction, so it collides and quarantines as a
// duplicate -- it never silently no-ops and never writes a second invoice.
func TestServiceImportDocument_SameDocumentSameEntityTwiceQuarantinesAsDuplicate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "REIMPORT tenant")
	entityID := seedEntity(t, super, tenantID, "REIMPORT entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("REIMPORT-INV"))

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	res1, err := svc.ImportDocument(callCtx, entityID, documentID)
	if err != nil {
		t.Fatalf("first ImportDocument: %v", err)
	}
	if res1.ReadyInvoices != 1 {
		t.Fatalf("first import = %+v, want a clean write", res1)
	}

	res2, err := svc.ImportDocument(callCtx, entityID, documentID)
	if err != nil {
		t.Fatalf("second ImportDocument: %v, want nil -- a re-import collision is a domain outcome", err)
	}
	if res2.ID == res1.ID {
		t.Fatal("second batch id == first, want its own distinct batch -- no dedup-by-document short-circuit exists")
	}
	if res2.Status != "completed" || res2.QuarantinedInvoices != 1 || res2.ReadyInvoices != 0 {
		t.Errorf("second import = %+v, want a quarantined duplicate (completed/quarantined=1/ready=0)", res2)
	}
	if got := countInvoicesByNumber(t, super, entityID, "REIMPORT-INV"); got != 1 {
		t.Errorf("invoices REIMPORT-INV = %d, want 1 (the second write must not duplicate the row)", got)
	}
	if got := countImportBatchesForEntity(t, super, entityID); got != 2 {
		t.Errorf("import_batches for entity = %d, want 2 (each call mints its own batch, even the quarantined one)", got)
	}
}

// PINNED: the (tenant, entity, invoice_number) unique guard is scoped PER ENTITY. The same
// document imported under a DIFFERENT entity is not a duplicate at all -- both writes succeed,
// each producing its own invoice, both citing the same source_document_id.
func TestServiceImportDocument_SameDocumentDifferentEntitiesBothWriteIndependently(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "XENT tenant")
	entityA := seedEntity(t, super, tenantID, "XENT entity A")
	entityB := seedEntity(t, super, tenantID, "XENT entity B")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("XENT-INV"))

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	resA, err := svc.ImportDocument(callCtx, entityA, documentID)
	if err != nil {
		t.Fatalf("ImportDocument(entityA): %v", err)
	}
	resB, err := svc.ImportDocument(callCtx, entityB, documentID)
	if err != nil {
		t.Fatalf("ImportDocument(entityB): %v", err)
	}
	if resA.QuarantinedInvoices != 0 || resB.QuarantinedInvoices != 0 {
		t.Fatalf("resA=%+v resB=%+v, want both clean (no cross-entity dedup)", resA, resB)
	}

	if got := countInvoicesForEntity(t, super, entityA); got != 1 {
		t.Errorf("invoices for entity A = %d, want 1", got)
	}
	if got := countInvoicesForEntity(t, super, entityB); got != 1 {
		t.Errorf("invoices for entity B = %d, want 1", got)
	}
	invA := invoiceIDByNumber(t, super, entityA, "XENT-INV")
	invB := invoiceIDByNumber(t, super, entityB, "XENT-INV")
	if gotDoc, _ := docInvoiceLinks(t, super, invA); gotDoc != documentID {
		t.Errorf("entity A invoice source_document_id = %q, want %q", gotDoc, documentID)
	}
	if gotDoc, _ := docInvoiceLinks(t, super, invB); gotDoc != documentID {
		t.Errorf("entity B invoice source_document_id = %q, want %q", gotDoc, documentID)
	}
}

// --- source_rows is genuinely NULL, never '{}' ------------------------------

// invoices_source_rows_are_sheet_rows rejects BOTH '{}' (cardinality < 1) and any element < 2,
// so NULL is the only legal value documentCreateInput can leave here (D-13: nothing extracted
// feeds line-level rows yet). DOC-01 already checks source_document_id but not source_rows.
func TestServiceImportDocument_WrittenInvoiceSourceRowsIsNullNotEmptyArray(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SRC-ROWS tenant")
	entityID := seedEntity(t, super, tenantID, "SRC-ROWS entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("SRC-ROWS-INV"))

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	invID := invoiceIDByNumber(t, super, entityID, "SRC-ROWS-INV")
	var sourceDocID *string
	var sourceRows []int
	if err := super.QueryRow(ctx,
		`SELECT source_document_id::text, source_rows FROM invoices WHERE id = $1`, invID,
	).Scan(&sourceDocID, &sourceRows); err != nil {
		t.Fatalf("read source_document_id/source_rows: %v", err)
	}
	if sourceDocID == nil || *sourceDocID != documentID {
		t.Errorf("source_document_id = %v, want %q", sourceDocID, documentID)
	}
	if sourceRows != nil {
		t.Errorf("source_rows = %v, want NULL (the CHECK rejects '{}' and any element < 2)", sourceRows)
	}
}
