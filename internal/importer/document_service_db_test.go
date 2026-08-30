// document_service_db_test.go: DB-backed RED specs for Service.ImportDocument (EXTR-06-03,
// task-763), authored against the stub in document.go before the real implementation exists --
// Mode A. Reuses dbTestPools/seedTenant/seedEntity/seedDocument/newTestService/sxIdentity and
// the countInvoicesBy*/invoiceIDByNumber/countLineItems read-back helpers already defined in
// store_test.go/service_test.go/document_db_test.go (same package).
//
// Spec-to-test map (Test Specs table, EXTR-06-03 / task-763):
//
//	DOC-01 TestServiceImportDocument_CleanExtractionWritesOneInvoiceReturnsCompletedCounts
//	DOC-02 TestServiceImportDocument_NoSettledExtractionReturnsErrNotFoundMintsNoBatch
//	DOC-03 TestServiceImportDocument_UnreadableInvoiceNumberQuarantinesBatchNoInvoiceWritten
//	DOC-04 TestServiceImportDocument_JunkSubtotalQuarantinedAsDomainOutcomeNotReturnedError
//	DOC-05 TestServiceImportDocument_OperationalCreateFailureFinalizesBatchFailedReturnsRawError
//	DOC-06 TestServiceImportDocument_CleanPathErrorsAndInvoiceViolationsMarshalToEmptyArrayNotNull
//	DOC-07 TestServiceImportDocument_RuleSetVersionMarshalsToNullNotZeroDefault
//	DOC-08 TestServiceImportDocument_WrittenInvoiceIsDraftWithZeroLineItems
//	DOC-09 TestServiceImportDocument_RowsValidPlusRowsInvalidEqualsRowsTotalAcrossFourOutcomes
//	DOC-10 TestRLS_ImportDocumentCrossTenantDocumentReturnsErrNotFoundWritesNothing
//	DOC-11 TestServiceImportDocument_TwoDocumentsInOneEntityEachGetOwnBatchAndSourceDocumentID
//
// Two additions beyond the Test Specs table (task-763's own executor brief):
//
//	DOC-12 TestServiceImportDocument_MintFailurePropagatesRawErrorClosestInducibleForExistingNumbersGap
//	       -- see that test's own doc comment: the true post-mint ExistingNumbers gap could not
//	       be honestly induced without a fault-injection seam; this exercises the closest
//	       safely-inducible condition instead (a mint-time failure) and says so plainly.
//	DOC-13 TestServiceImportDocument_MapperQuarantinePathInvoiceViolationsMarshalsToEmptyArrayNotNull
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- fixture builders --------------------------------------------------

// docCleanValues is the ten decided-field values a "clean" settled extraction carries, keyed
// exactly to mapperFieldNames (document.go) -- documentCreateInput's whole contract.
func docCleanValues(invoiceNumber string) map[string]*string {
	return map[string]*string{
		"invoice_number": sxPtr(invoiceNumber),
		"issue_date":     sxPtr("2026-03-01"),
		"supplier_tin":   sxPtr("12345678-0001"),
		"supplier_name":  sxPtr("Ignored Supplier"),
		"buyer_tin":      sxPtr("87654321-0001"),
		"buyer_name":     sxPtr("Clean Buyer Ltd"),
		"currency":       sxPtr("NGN"),
		"subtotal":       sxPtr("1000.00"),
		"vat":            sxPtr("75.00"),
		"total":          sxPtr("1075.00"),
	}
}

// docSeedExtraction seeds one succeeded extraction_jobs row for documentID plus one rank-0
// field per (name, value) present in values -- a name absent from values is left out of the
// row set entirely (distinct from present-but-nil, which is a NULL value column).
func docSeedExtraction(t *testing.T, super *pgxpool.Pool, tenantID, documentID string, values map[string]*string) {
	t.Helper()
	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	now := time.Now().UTC()
	for i, name := range mapperFieldNames {
		v, ok := values[name]
		if !ok {
			continue
		}
		seedExtractionField(t, super, tenantID, job, name, v, nil, 0, now.Add(time.Duration(i)*time.Millisecond))
	}
}

// docInvoiceLinks reads back one invoice's source_document_id/import_batch_id.
func docInvoiceLinks(t *testing.T, super *pgxpool.Pool, invoiceID string) (sourceDocumentID, importBatchID string) {
	t.Helper()
	var docID, batchID *string
	if err := super.QueryRow(context.Background(),
		`SELECT source_document_id::text, import_batch_id::text FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&docID, &batchID); err != nil {
		t.Fatalf("read source_document_id/import_batch_id: %v", err)
	}
	if docID != nil {
		sourceDocumentID = *docID
	}
	if batchID != nil {
		importBatchID = *batchID
	}
	return sourceDocumentID, importBatchID
}

// docBatchRowByEntity reads back the one import_batches row for entityID -- valid whenever a
// fixture mints exactly one batch, which is every DOC-* spec below except DOC-11.
func docBatchRowByEntity(t *testing.T, super *pgxpool.Pool, entityID string) (id, status string, rowsTotal, rowsValid, rowsInvalid int) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT id, status, rows_total, rows_valid, rows_invalid FROM import_batches WHERE entity_id = $1`,
		entityID,
	).Scan(&id, &status, &rowsTotal, &rowsValid, &rowsInvalid); err != nil {
		t.Fatalf("read back the batch for entity %s: %v (want exactly one import_batches row)", entityID, err)
	}
	return id, status, rowsTotal, rowsValid, rowsInvalid
}

// docSeedDocument inserts one documents row with a content_hash unique to THIS call --
// seedDocument/sxSeedDocument (store_test.go/document_db_test.go) both hardcode the same
// content_hash for every row, so calling either twice under one tenant trips
// documents_tenant_content_hash_uq (DOC-11 needs two documents in one tenant).
func docSeedDocument(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	hash := strings.ReplaceAll(uuid.NewString(), "-", "") + strings.ReplaceAll(uuid.NewString(), "-", "")
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO documents (tenant_id, storage_key, content_hash, size_bytes)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, "doc/"+tenantID+"/"+uuid.NewString(), hash, int64(11),
	).Scan(&id); err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	return id
}

// --- DOC-01 --------------------------------------------------------------

// DOC-01: a clean extraction writes exactly one invoice, carrying the right source_document_id
// and import_batch_id, and returns completed/1/1/0/1/0.
func TestServiceImportDocument_CleanExtractionWritesOneInvoiceReturnsCompletedCounts(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-01 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-01 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("DOC-01-INV"))

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	if res.Status != "completed" || res.RowsTotal != 1 || res.RowsValid != 1 ||
		res.RowsInvalid != 0 || res.ReadyInvoices != 1 || res.QuarantinedInvoices != 0 {
		t.Errorf("BatchResult = %+v, want completed/1/1/0/1/0", res)
	}
	if res.ID == "" {
		t.Fatal("res.ID = \"\", want the minted batch id")
	}

	if got := countInvoicesByNumber(t, super, entityID, "DOC-01-INV"); got != 1 {
		t.Fatalf("invoices DOC-01-INV = %d, want 1", got)
	}
	invID := invoiceIDByNumber(t, super, entityID, "DOC-01-INV")
	gotDocID, gotBatchID := docInvoiceLinks(t, super, invID)
	if gotDocID != documentID {
		t.Errorf("source_document_id = %q, want %q", gotDocID, documentID)
	}
	if gotBatchID != res.ID {
		t.Errorf("import_batch_id = %q, want the minted batch %q", gotBatchID, res.ID)
	}
}

// --- DOC-02 --------------------------------------------------------------

// DOC-02: a document with only a failed job returns ErrNotFound and mints no batch.
func TestServiceImportDocument_NoSettledExtractionReturnsErrNotFoundMintsNoBatch(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-02 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-02 entity")
	documentID := seedDocument(t, super, tenantID)
	seedExtractionJob(t, super, tenantID, documentID, "failed", time.Now().UTC())

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ImportDocument err = %v, want ErrNotFound", err)
	}
	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("import_batches for entity = %d, want 0 (no batch minted before the read, D-10)", got)
	}
}

// --- DOC-03 --------------------------------------------------------------

// DOC-03: an unreadable invoice_number still mints the batch, finalizes it completed, reports
// one structural error and writes no invoice -- never a returned error (D-9).
func TestServiceImportDocument_UnreadableInvoiceNumberQuarantinesBatchNoInvoiceWritten(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-03 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-03 entity")
	documentID := seedDocument(t, super, tenantID)
	values := docCleanValues("unused")
	values["invoice_number"] = nil
	docSeedExtraction(t, super, tenantID, documentID, values)

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
	if res.Errors[0].Message != "invoice_number is missing or blank" {
		t.Errorf("Errors[0].Message = %q, want the shipped documentCreateInput message", res.Errors[0].Message)
	}

	if got := countImportBatchesForEntity(t, super, entityID); got != 1 {
		t.Fatalf("import_batches for entity = %d, want 1 (minted even on a mapper error, D-17)", got)
	}
	if _, status, _, _, _ := docBatchRowByEntity(t, super, entityID); status != "completed" {
		t.Errorf("batch status = %q, want completed (a quarantine is a completed run, never failed)", status)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices for entity = %d, want 0", got)
	}
}

// --- DOC-04 --------------------------------------------------------------

// DOC-04: subtotal = "not-a-number" comes back as a quarantined domain outcome (22P02 ->
// invoice.ErrValidation), not a returned error.
func TestServiceImportDocument_JunkSubtotalQuarantinedAsDomainOutcomeNotReturnedError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-04 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-04 entity")
	documentID := seedDocument(t, super, tenantID)
	values := docCleanValues("DOC-04-INV")
	values["subtotal"] = sxPtr("not-a-number")
	docSeedExtraction(t, super, tenantID, documentID, values)

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v, want nil -- a 22P02 on the numeric cast is a domain outcome, never a returned error", err)
	}
	if res.Status != "completed" || res.ReadyInvoices != 0 || res.QuarantinedInvoices != 1 {
		t.Errorf("BatchResult = %+v, want completed/ready=0/quarantined=1", res)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1", len(res.Errors))
	}
	if res.Errors[0].Message == "" {
		t.Error("Errors[0].Message = \"\", want a human-readable domain message")
	}

	if got := countInvoicesByNumber(t, super, entityID, "DOC-04-INV"); got != 0 {
		t.Errorf("invoices DOC-04-INV persisted = %d, want 0 (Create's tx rolled back on 22P02)", got)
	}
	if _, status, _, _, _ := docBatchRowByEntity(t, super, entityID); status != "completed" {
		t.Errorf("batch status = %q, want completed", status)
	}
}

// --- DOC-05 --------------------------------------------------------------

// DOC-05: an operational (non-domain) Create failure finalizes the batch failed and returns
// the raw error. Fixture: auth.Identity{Subject: ""} (never a cancelled context, D-18) --
// mirrors the shipped TestServiceImport_OperationalCreateFailureAbortsRunNotQuarantined
// (service_test.go:1316). The empty Subject takes db.WithinRequestTenantTxOpts's non-UUID
// branch and skips the membership gate everywhere, so nothing before Create blocks; Create's
// own invoice_status_history INSERT then trips the actor CHECK (23514) raw, with ctx still
// live, so the best-effort Finalize('failed') can run and be read back.
func TestServiceImportDocument_OperationalCreateFailureFinalizesBatchFailedReturnsRawError(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-05 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-05 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("DOC-05-INV"))

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: "", Role: "authenticated", TenantID: tenantID})

	res, err := svc.ImportDocument(c, entityID, documentID)
	if err == nil {
		t.Fatal("ImportDocument err = nil, want the raw operational error (invoice_status_history actor CHECK) to propagate")
	}
	if errors.Is(err, invoice.ErrValidation) || errors.Is(err, invoice.ErrDuplicateNumber) {
		t.Errorf("err = %v, want a NON-domain error", err)
	}
	if res.ID != "" {
		t.Errorf("res.ID = %q on an aborted run, want empty (BatchResult{} on error)", res.ID)
	}

	if got := countInvoicesByNumber(t, super, entityID, "DOC-05-INV"); got != 0 {
		t.Errorf("invoices DOC-05-INV persisted = %d, want 0", got)
	}
	if _, status, _, _, _ := docBatchRowByEntity(t, super, entityID); status != "failed" {
		t.Errorf("batch status = %q, want failed (never left processing, never laundered completed)", status)
	}
}

// --- DOC-06 --------------------------------------------------------------

// DOC-06: on the clean path, Errors and InvoiceViolations marshal to [], never null.
func TestServiceImportDocument_CleanPathErrorsAndInvoiceViolationsMarshalToEmptyArrayNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-06 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-06 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("DOC-06-INV"))

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	errJSON, jerr := json.Marshal(res.Errors)
	if jerr != nil {
		t.Fatalf("marshal Errors: %v", jerr)
	}
	if string(errJSON) != "[]" {
		t.Errorf("Errors marshalled = %s, want [] (never null)", errJSON)
	}

	violJSON, jerr := json.Marshal(res.InvoiceViolations)
	if jerr != nil {
		t.Fatalf("marshal InvoiceViolations: %v", jerr)
	}
	if string(violJSON) != "[]" {
		t.Errorf("InvoiceViolations marshalled = %s, want [] (never null)", violJSON)
	}
}

// --- DOC-07 --------------------------------------------------------------

// DOC-07: RuleSetVersion marshals to null, never a 0 default -- nothing was evaluated (AC #6),
// and neither violation counter moved off zero.
func TestServiceImportDocument_RuleSetVersionMarshalsToNullNotZeroDefault(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-07 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-07 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("DOC-07-INV"))

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("res.Status = %q, want completed (the clean-path precondition for this spec)", res.Status)
	}
	if res.RuleSetVersion != nil {
		t.Errorf("RuleSetVersion = %v, want nil -- ImportDocument runs no gate (AC #6)", *res.RuleSetVersion)
	}
	if res.InvoicesClean != 0 || res.InvoicesWithViolations != 0 {
		t.Errorf("InvoicesClean=%d InvoicesWithViolations=%d, want 0/0 (AC #6)", res.InvoicesClean, res.InvoicesWithViolations)
	}

	wire := importResponse{RuleSetVersion: res.RuleSetVersion}
	b, jerr := json.Marshal(wire)
	if jerr != nil {
		t.Fatalf("marshal importResponse: %v", jerr)
	}
	if !strings.Contains(string(b), `"rule_set_version":null`) {
		t.Errorf("marshalled importResponse = %s, want a literal null for rule_set_version, not a 0 default", b)
	}
}

// --- DOC-08 --------------------------------------------------------------

// DOC-08: the written invoice has status draft and count(line_items) = 0 (D-13: nothing
// extracted feeds LineItems yet).
func TestServiceImportDocument_WrittenInvoiceIsDraftWithZeroLineItems(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-08 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-08 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("DOC-08-INV"))

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	if got := countInvoicesByNumber(t, super, entityID, "DOC-08-INV"); got != 1 {
		t.Fatalf("invoices DOC-08-INV = %d, want 1", got)
	}
	invID := invoiceIDByNumber(t, super, entityID, "DOC-08-INV")

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
		t.Fatalf("read invoice status: %v", err)
	}
	if status != string(invoice.StatusDraft) {
		t.Errorf("status = %q, want %q", status, invoice.StatusDraft)
	}
	if got := countLineItems(t, super, invID); got != 0 {
		t.Errorf("line_items for the written invoice = %d, want 0", got)
	}
}

// --- DOC-09 --------------------------------------------------------------

// DOC-09: rows_valid + rows_invalid == rows_total on the PERSISTED import_batches row (not
// just the returned BatchResult, which the operational-failure path deliberately zeroes),
// across the four outcomes DOC-01/03/04/05 above produce: clean, mapper-error, domain-error,
// operational-failure.
func TestServiceImportDocument_RowsValidPlusRowsInvalidEqualsRowsTotalAcrossFourOutcomes(t *testing.T) {
	super, app := dbTestPools(t)
	svc := newTestService(app)

	cases := []struct {
		name            string
		mutate          func(values map[string]*string)
		useEmptySubject bool
	}{
		{name: "clean", mutate: func(map[string]*string) {}},
		{name: "mapper-error", mutate: func(v map[string]*string) { v["invoice_number"] = nil }},
		{name: "domain-error", mutate: func(v map[string]*string) { v["subtotal"] = sxPtr("not-a-number") }},
		{name: "operational-failure", mutate: func(map[string]*string) {}, useEmptySubject: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tenantID := seedTenant(t, super, "DOC-09 "+tc.name)
			entityID := seedEntity(t, super, tenantID, "DOC-09 entity "+tc.name)
			documentID := seedDocument(t, super, tenantID)
			values := docCleanValues("DOC-09-" + tc.name)
			tc.mutate(values)
			docSeedExtraction(t, super, tenantID, documentID, values)

			callCtx := sxIdentity(ctx, tenantID)
			if tc.useEmptySubject {
				callCtx = auth.WithIdentity(ctx, auth.Identity{Subject: "", Role: "authenticated", TenantID: tenantID})
			}
			// The returned error is exercised by DOC-03/04/05 above -- this spec only checks
			// the persisted row's arithmetic.
			_, _ = svc.ImportDocument(callCtx, entityID, documentID)

			_, _, total, valid, invalid := docBatchRowByEntity(t, super, entityID)
			if valid+invalid != total {
				t.Errorf("rows_valid(%d) + rows_invalid(%d) = %d, want rows_total %d", valid, invalid, valid+invalid, total)
			}
		})
	}
}

// --- DOC-10 --------------------------------------------------------------

// DOC-10: a cross-tenant document_id returns ErrNotFound and writes nothing -- RLS refusal.
func TestRLS_ImportDocumentCrossTenantDocumentReturnsErrNotFoundWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "DOC-10 tenant A")
	documentID := seedDocument(t, super, tenantA)
	docSeedExtraction(t, super, tenantA, documentID, docCleanValues("DOC-10-INV"))

	tenantB := seedTenant(t, super, "DOC-10 tenant B")
	entityB := seedEntity(t, super, tenantB, "DOC-10 entity B")

	svc := newTestService(app)
	if _, err := svc.ImportDocument(sxIdentity(ctx, tenantB), entityB, documentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ImportDocument (cross-tenant document) err = %v, want ErrNotFound", err)
	}
	if got := countImportBatchesForEntity(t, super, entityB); got != 0 {
		t.Errorf("import_batches for tenant B's entity = %d, want 0", got)
	}
	if got := countInvoicesForEntity(t, super, entityB); got != 0 {
		t.Errorf("invoices for tenant B's entity = %d, want 0", got)
	}
}

// --- DOC-11 --------------------------------------------------------------

// DOC-11: two different documents in one entity, different numbers, both write -- each with
// its own source_document_id and its own batch, never a shared or mixed-up link.
func TestServiceImportDocument_TwoDocumentsInOneEntityEachGetOwnBatchAndSourceDocumentID(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-11 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-11 entity")

	doc1 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc1, docCleanValues("DOC-11-A"))
	doc2 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc2, docCleanValues("DOC-11-B"))

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)
	res1, err := svc.ImportDocument(callCtx, entityID, doc1)
	if err != nil {
		t.Fatalf("ImportDocument(doc1): %v", err)
	}
	res2, err := svc.ImportDocument(callCtx, entityID, doc2)
	if err != nil {
		t.Fatalf("ImportDocument(doc2): %v", err)
	}
	if res1.ID == "" || res2.ID == "" || res1.ID == res2.ID {
		t.Fatalf("batch ids = (%q, %q), want two distinct non-empty ids", res1.ID, res2.ID)
	}

	inv1 := invoiceIDByNumber(t, super, entityID, "DOC-11-A")
	inv2 := invoiceIDByNumber(t, super, entityID, "DOC-11-B")

	if gotDoc, gotBatch := docInvoiceLinks(t, super, inv1); gotDoc != doc1 || gotBatch != res1.ID {
		t.Errorf("invoice A links = (doc=%q batch=%q), want (doc=%q batch=%q)", gotDoc, gotBatch, doc1, res1.ID)
	}
	if gotDoc, gotBatch := docInvoiceLinks(t, super, inv2); gotDoc != doc2 || gotBatch != res2.ID {
		t.Errorf("invoice B links = (doc=%q batch=%q), want (doc=%q batch=%q)", gotDoc, gotBatch, doc2, res2.ID)
	}
}

// --- DOC-12 ----------------------------------------------------------------

// DOC-12: the architecture's flagged gap is step 5 (ExistingNumbers) failing operationally
// AFTER the batch is already minted -- .ralph/EXTR-06-finalized.md's Implementation Notes,
// "GAP: design specifies no error branch here". That EXACT condition cannot be honestly
// induced through the real ImportDocument(ctx, entityID, documentID) entrypoint in this
// codebase: entityID is bound identically into BOTH CreateBatch's INSERT (entity_id, FK to
// business_entities) and ExistingNumbers's SELECT (WHERE entity_id = $1), so any entityID
// malformed enough to fail ExistingNumbers ALSO fails CreateBatch first -- before the batch
// exists. ExistingNumbers itself carries no domain sentinel of its own (a non-matching
// entity_id is a legitimate empty result, not an error), and the one real operational-failure
// fixture this suite has (DOC-05's empty-Subject identity) trips an actor CHECK on a table
// ExistingNumbers's own pure SELECT never touches. Service also holds a concrete *Store, not
// an interface -- no fault-injection seam exists (the same limitation
// TestServiceImport_OperationalCreateFailureAbortsRunNotQuarantined's own doc comment records
// for Create).
//
// This spec exercises the CLOSEST safely-inducible condition instead: a malformed entity_id
// fails at the MINT step itself (CreateBatch), the earliest point an operational-shaped
// failure can occur. It proves ImportDocument propagates a raw, non-ErrNotFound error with no
// batch left behind when the mint itself fails. It does NOT close the true post-mint
// ExistingNumbers gap -- that remains genuinely unverified pending either a fault-injection
// seam or an accepted design change.
func TestServiceImportDocument_MintFailurePropagatesRawErrorClosestInducibleForExistingNumbersGap(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-12 tenant")
	documentID := seedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docCleanValues("DOC-12-INV"))

	svc := newTestService(app)
	_, err := svc.ImportDocument(sxIdentity(ctx, tenantID), "not-a-uuid", documentID)
	if err == nil {
		t.Fatal("ImportDocument(malformed entity_id) err = nil, want the raw mint failure to propagate")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want ErrValidation (CreateBatch's own 22P02 mapping)", err)
	}

	var n int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE document_id = $1`, documentID).Scan(&n); err != nil {
		t.Fatalf("count import_batches by document_id: %v", err)
	}
	if n != 0 {
		t.Errorf("import_batches for this document = %d, want 0 (the mint itself never succeeded)", n)
	}
}

// --- DOC-13 ------------------------------------------------------------------

// DOC-13: the nil-slice coercion, on the path most likely to skip it -- the mapper's step-4
// early return (a RowError) never reaches Create or the gate, so InvoiceViolations is never
// appended to on this path. If ImportDocument's own BatchResult construction doesn't
// explicitly coerce a nil []InvoiceViolations to an empty one here, it marshals to null, not
// [] -- the exact bug class M4-16 already shipped once (a bare `var s []T` with no omittempty
// marshals nil to null).
func TestServiceImportDocument_MapperQuarantinePathInvoiceViolationsMarshalsToEmptyArrayNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-13 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-13 entity")
	documentID := seedDocument(t, super, tenantID)
	values := docCleanValues("unused")
	values["invoice_number"] = nil
	docSeedExtraction(t, super, tenantID, documentID, values)

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1 (the mapper's structural RowError)", len(res.Errors))
	}

	violJSON, jerr := json.Marshal(res.InvoiceViolations)
	if jerr != nil {
		t.Fatalf("marshal InvoiceViolations: %v", jerr)
	}
	if string(violJSON) != "[]" {
		t.Errorf("InvoiceViolations marshalled = %s, want [] (never null) on the mapper-error early return", violJSON)
	}
}
