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
	"sort"
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
// row set entirely (distinct from present-but-nil, which is a NULL value column). Header names
// in mapperFieldNames order seed first, then any remaining key (line_items[N].<role> names)
// in sorted order -- mapperFieldNames alone would silently drop every line key a caller passed.
func docSeedExtraction(t *testing.T, super *pgxpool.Pool, tenantID, documentID string, values map[string]*string) {
	t.Helper()
	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", time.Now().UTC())
	now := time.Now().UTC()
	seeded := make(map[string]bool, len(values))
	var i int
	for _, name := range mapperFieldNames {
		v, ok := values[name]
		if !ok {
			continue
		}
		seedExtractionField(t, super, tenantID, job, name, v, nil, 0, now.Add(time.Duration(i)*time.Millisecond))
		seeded[name] = true
		i++
	}
	rest := make([]string, 0, len(values)-len(seeded))
	for name := range values {
		if !seeded[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		seedExtractionField(t, super, tenantID, job, name, values[name], nil, 0, now.Add(time.Duration(i)*time.Millisecond))
		i++
	}
}

// docCountExtractionFields is the seed floor every line-carrying fixture needs: it counts
// extraction_field_results rows across documentID's job(s), so a future narrowing of
// docSeedExtraction back to header-only names empties a fixture LOUDLY instead of passing
// vacuously.
func docCountExtractionFields(t *testing.T, super *pgxpool.Pool, documentID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM extraction_field_results r
		   JOIN extraction_jobs j ON j.id = r.extraction_job_id
		  WHERE j.document_id = $1`, documentID,
	).Scan(&n); err != nil {
		t.Fatalf("count extraction_field_results for document %s: %v", documentID, err)
	}
	return n
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
	// Retargeted by EXTR-15-05: DOC-03 seeds ten fields with a NULL number, so it takes the
	// read-document branch. DOC-14 below covers the poor-scan branch.
	if res.Errors[0].Message != ac2Message(t) {
		t.Errorf("Errors[0].Message = %q, want the mapper's read-document message %q", res.Errors[0].Message, ac2Message(t))
	}
	assertReadDocumentMessage(t, res.Errors[0].Message)

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

// DOC-08: the written invoice has status draft and count(line_items) = 0 -- docCleanValues
// seeds no line rows, so this fixture is zero-line by construction, not by any code path that
// drops lines.
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

// --- DOC-14 (EXTR-15-05 PS-7) --------------------------------------------

// docSeedPoorScanExtraction seeds the exact rank-0 field set the extraction worker writes for
// a document with no text layer: one document_text_layer row, NULL value, reason unreadable.
// docSeedExtraction cannot express it -- that helper only walks mapperFieldNames.
func docSeedPoorScanExtraction(t *testing.T, super *pgxpool.Pool, tenantID, documentID string) {
	t.Helper()
	now := time.Now().UTC()
	job := seedExtractionJob(t, super, tenantID, documentID, "succeeded", now)
	seedExtractionField(t, super, tenantID, job, "document_text_layer", nil, sxPtr("unreadable"), 0, now)
}

// DOC-14: the poor scan reaches the batch-review screen as a scan-quality complaint, not as a
// machine string about the tenant's data. End-to-end through the real ImportDocument, so a
// mapper fix that never reaches the wire fails here.
func TestServiceImportDocument_PoorScanQuarantineCarriesTheScanQualityMessage(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DOC-14 tenant")
	entityID := seedEntity(t, super, tenantID, "DOC-14 entity")
	documentID := seedDocument(t, super, tenantID)
	docSeedPoorScanExtraction(t, super, tenantID, documentID)

	svc := newTestService(app)
	res, err := svc.ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v, want nil -- a poor scan is a domain outcome, not a returned error (D-9)", err)
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
	if res.Errors[0].RuleKey != "" {
		t.Errorf("Errors[0].RuleKey = %q, want empty -- a rule key would render this as already-imported", res.Errors[0].RuleKey)
	}
	assertPoorScanMessage(t, res.Errors[0].Message)
	if res.Errors[0].Message == ac2Message(t) {
		t.Errorf("Errors[0].Message = %q, which is the read-document message -- the poor scan is reported as if the document had been read", res.Errors[0].Message)
	}

	_, status, rowsTotal, rowsValid, rowsInvalid := docBatchRowByEntity(t, super, entityID)
	if status != "completed" || rowsTotal != 1 || rowsValid != 0 || rowsInvalid != 1 {
		t.Errorf("batch row = (%s, total=%d, valid=%d, invalid=%d), want (completed, 1, 0, 1)", status, rowsTotal, rowsValid, rowsInvalid)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices for entity = %d, want 0", got)
	}
}

// --- EXTR-27-02 (task-1014): a no-number reading is readable and fileable ------------------

// docNoNumberValues is the reading a no-number document quarantines with: docCleanValues("")
// but invoice_number itself absent (unreadable), not the empty string -- the real shape.
func docNoNumberValues() map[string]*string {
	values := docCleanValues("")
	values["invoice_number"] = nil
	return values
}

// countInvoicesCitingDocument counts invoices whose source_document_id cites documentID.
func countInvoicesCitingDocument(t *testing.T, super *pgxpool.Pool, documentID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoices WHERE source_document_id = $1`, documentID,
	).Scan(&n); err != nil {
		t.Fatalf("count invoices citing document %s: %v", documentID, err)
	}
	return n
}

// extractionJobIDForDocument reads back the newest extraction_jobs id for documentID, so
// CarriedReading's own ExtractionJobID can be pinned against the real row, not just non-empty.
func extractionJobIDForDocument(t *testing.T, super *pgxpool.Pool, documentID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM extraction_jobs WHERE document_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, documentID,
	).Scan(&id); err != nil {
		t.Fatalf("read extraction_jobs id for document %s: %v", documentID, err)
	}
	return id
}

// --- CR-01 -----------------------------------------------------------------------------

// CR-01: a no-number reading carries every header value plus its lines, in index order, and a
// header-only reading still gives a non-nil empty LineItems slice that marshals to [].
func TestServiceCarriedReading_ANoNumberReadingCarriesEveryValue(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "CR-01 tenant")
	documentID := docSeedDocument(t, super, tenantID)
	values := docNoNumberValues()
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].quantity"] = sxPtr("2")
	values["line_items[1].unit_price"] = sxPtr("10.00")
	values["line_items[1].line_total"] = sxPtr("20.00")
	values["line_items[1].line_tax"] = sxPtr("1.50")
	values["line_items[2].description"] = sxPtr("Gadget")
	docSeedExtraction(t, super, tenantID, documentID, values)
	wantJobID := extractionJobIDForDocument(t, super, documentID)

	svc := newTestServiceWithGate(app, &fakeGate{})
	callCtx := sxIdentity(ctx, tenantID)
	reading, err := svc.CarriedReading(callCtx, documentID)
	if err != nil {
		t.Fatalf("CarriedReading: %v", err)
	}
	if reading == nil {
		t.Fatal("reading = nil, want a carried reading")
	}
	if reading.DocumentID != documentID {
		t.Errorf("DocumentID = %q, want %q", reading.DocumentID, documentID)
	}
	if reading.ExtractionJobID != wantJobID {
		t.Errorf("ExtractionJobID = %q, want %q", reading.ExtractionJobID, wantJobID)
	}
	if reading.IssueDate == nil || *reading.IssueDate != "2026-03-01" {
		t.Errorf("IssueDate = %v, want %q", reading.IssueDate, "2026-03-01")
	}
	if reading.BuyerTIN == nil || *reading.BuyerTIN != "87654321-0001" {
		t.Errorf("BuyerTIN = %v, want %q", reading.BuyerTIN, "87654321-0001")
	}
	if reading.BuyerName == nil || *reading.BuyerName != "Clean Buyer Ltd" {
		t.Errorf("BuyerName = %v, want %q", reading.BuyerName, "Clean Buyer Ltd")
	}
	if reading.Currency == nil || *reading.Currency != "NGN" {
		t.Errorf("Currency = %v, want %q", reading.Currency, "NGN")
	}
	if reading.Subtotal == nil || *reading.Subtotal != "1000.00" {
		t.Errorf("Subtotal = %v, want %q", reading.Subtotal, "1000.00")
	}
	if reading.VAT == nil || *reading.VAT != "75.00" {
		t.Errorf("VAT = %v, want %q", reading.VAT, "75.00")
	}
	if reading.Total == nil || *reading.Total != "1075.00" {
		t.Errorf("Total = %v, want %q", reading.Total, "1075.00")
	}
	if len(reading.LineItems) != 2 {
		t.Fatalf("len(LineItems) = %d, want 2", len(reading.LineItems))
	}
	if reading.LineItems[0].Description == nil || *reading.LineItems[0].Description != "Widget" {
		t.Errorf("LineItems[0].Description = %v, want %q", reading.LineItems[0].Description, "Widget")
	}
	if reading.LineItems[0].LineTax == nil || *reading.LineItems[0].LineTax != "1.50" {
		t.Errorf("LineItems[0].LineTax = %v, want %q", reading.LineItems[0].LineTax, "1.50")
	}
	if reading.LineItems[1].Description == nil || *reading.LineItems[1].Description != "Gadget" {
		t.Errorf("LineItems[1].Description = %v, want %q", reading.LineItems[1].Description, "Gadget")
	}

	// Second leg: a header-only reading (no lines) still gives a non-nil, empty LineItems slice.
	documentID2 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID2, docNoNumberValues())
	reading2, err := svc.CarriedReading(callCtx, documentID2)
	if err != nil {
		t.Fatalf("CarriedReading (header-only): %v", err)
	}
	if reading2 == nil {
		t.Fatal("reading2 = nil, want a carried reading")
	}
	if reading2.LineItems == nil || len(reading2.LineItems) != 0 {
		t.Errorf("LineItems = %v, want a non-nil empty slice", reading2.LineItems)
	}
	b, jerr := json.Marshal(reading2)
	if jerr != nil {
		t.Fatalf("marshal reading: %v", jerr)
	}
	if !strings.Contains(string(b), `"line_items":[]`) {
		t.Errorf("marshalled reading = %s, want a literal [] for line_items, not null", b)
	}
}

// --- CR-02 -----------------------------------------------------------------------------

// CR-02: every "not carriable" shape reads (nil, nil) -- never an error, never a real reading.
// The two-documents control catches a predicate ignoring source_document_id.
func TestServiceCarriedReading_NothingToCarryIsNil(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	svc := newTestServiceWithGate(app, &fakeGate{})

	tenantID := seedTenant(t, super, "CR-02 tenant")
	entityID := seedEntity(t, super, tenantID, "CR-02 entity")
	callCtx := sxIdentity(ctx, tenantID)

	// Positive control: proves this suite's fixtures are not universally nil.
	controlDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, controlDoc, docNoNumberValues())
	if reading, err := svc.CarriedReading(callCtx, controlDoc); err != nil || reading == nil {
		t.Fatalf("control (no-number reading): reading=%v err=%v, want a non-nil reading", reading, err)
	}

	noJobDoc := docSeedDocument(t, super, tenantID)

	poorScanDoc := docSeedDocument(t, super, tenantID)
	docSeedPoorScanExtraction(t, super, tenantID, poorScanDoc)

	numberedDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, numberedDoc, docCleanValues("CR-02-N1"))

	badDateDoc := docSeedDocument(t, super, tenantID)
	badDateValues := docNoNumberValues()
	badDateValues["issue_date"] = sxPtr("13/02/2026")
	docSeedExtraction(t, super, tenantID, badDateDoc, badDateValues)

	filedDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, filedDoc, docNoNumberValues())
	if _, err := invoice.NewStore(app).Create(callCtx, invoice.CreateInput{
		EntityID: entityID, InvoiceNumber: "CR-02-FILED", SourceDocumentID: &filedDoc,
	}); err != nil {
		t.Fatalf("seed a filed invoice for %s: %v", filedDoc, err)
	}

	cases := []struct {
		name string
		doc  string
	}{
		{"no job", noJobDoc},
		{"poor scan", poorScanDoc},
		{"numbered", numberedDoc},
		{"bad date", badDateDoc},
		{"already filed", filedDoc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reading, err := svc.CarriedReading(callCtx, tc.doc)
			if err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if reading != nil {
				t.Errorf("reading = %+v, want nil", reading)
			}
		})
	}

	// Cross-tenant: tenant B reading tenant A's no-number document.
	tenantB := seedTenant(t, super, "CR-02 tenant B")
	if reading, err := svc.CarriedReading(sxIdentity(ctx, tenantB), controlDoc); err != nil || reading != nil {
		t.Errorf("cross-tenant read: reading=%v err=%v, want (nil, nil)", reading, err)
	}

	// Two-documents control: doc1 filed, doc2 not -- catches a predicate ignoring source_document_id.
	doc1 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc1, docNoNumberValues())
	if _, err := invoice.NewStore(app).Create(callCtx, invoice.CreateInput{
		EntityID: entityID, InvoiceNumber: "CR-02-DOC1", SourceDocumentID: &doc1,
	}); err != nil {
		t.Fatalf("seed doc1's filed invoice: %v", err)
	}
	doc2 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc2, docNoNumberValues())

	if reading, err := svc.CarriedReading(callCtx, doc1); err != nil || reading != nil {
		t.Errorf("doc1 (filed): reading=%v err=%v, want (nil, nil)", reading, err)
	}
	if reading, err := svc.CarriedReading(callCtx, doc2); err != nil || reading == nil {
		t.Errorf("doc2 (not filed): reading=%v err=%v, want a non-nil reading", reading, err)
	}
}

// --- CR-03 -----------------------------------------------------------------------------

// CR-03: a genuinely failed read (a cancelled context) propagates as an error, never swallowed
// into (nil, nil) -- that would turn a suspended member's 403 into a null reading.
func TestServiceCarriedReading_AFailedReadIsAnErrorNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "CR-03 tenant")
	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docNoNumberValues())

	svc := newTestServiceWithGate(app, &fakeGate{})
	cancelled, cancel := context.WithCancel(sxIdentity(ctx, tenantID))
	cancel()

	reading, err := svc.CarriedReading(cancelled, documentID)
	if err == nil {
		t.Fatal("err = nil, want the cancelled-context failure to propagate")
	}
	if reading != nil {
		t.Errorf("reading = %+v, want nil on a failed read", reading)
	}
}

// --- SN-01 -----------------------------------------------------------------------------

// SN-01: SupplyInvoiceNumber files every carried value under the supplied number, mints no
// batch, runs the gate once, and audits the supply -- then the document is no longer carriable.
func TestServiceSupplyInvoiceNumber_FilesEveryCarriedValue(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SN-01 tenant")
	entityID := seedEntity(t, super, tenantID, "SN-01 entity")
	documentID := docSeedDocument(t, super, tenantID)
	values := docNoNumberValues()
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].unit_price"] = sxPtr("10.00")
	docSeedExtraction(t, super, tenantID, documentID, values)

	g := &fakeGate{}
	svc := newTestServiceWithGate(app, g)
	callCtx := sxIdentity(ctx, tenantID)

	inv, err := svc.SupplyInvoiceNumber(callCtx, entityID, documentID, "SN-01-INV")
	if err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v", err)
	}
	if inv.InvoiceNumber != "SN-01-INV" {
		t.Errorf("InvoiceNumber = %q, want %q", inv.InvoiceNumber, "SN-01-INV")
	}
	if got := countInvoicesByNumber(t, super, entityID, "SN-01-INV"); got != 1 {
		t.Fatalf("invoices SN-01-INV = %d, want 1", got)
	}
	gotDoc, gotBatch := docInvoiceLinks(t, super, inv.ID)
	if gotDoc != documentID {
		t.Errorf("source_document_id = %q, want %q", gotDoc, documentID)
	}
	if gotBatch != "" {
		t.Errorf("import_batch_id = %q, want empty (no batch minted)", gotBatch)
	}
	if len(inv.LineItems) != 1 || inv.LineItems[0].Description == nil || *inv.LineItems[0].Description != "Widget" {
		t.Errorf("LineItems = %+v, want one item named %q", inv.LineItems, "Widget")
	}
	if g.validateBatchCalls != 1 {
		t.Errorf("gate.ValidateBatch calls = %d, want 1", g.validateBatchCalls)
	}
	if len(g.validateBatchInvs) != 1 || g.validateBatchInvs[0].ID != inv.ID {
		t.Errorf("gate.ValidateBatch invs = %+v, want exactly [%s]", g.validateBatchInvs, inv.ID)
	}

	rows := readAuditForInvoice(t, app, tenantID, inv.ID)
	var found bool
	for _, r := range rows {
		if r.event != "invoice.created" {
			continue
		}
		found = true
		var payload map[string]any
		if jerr := json.Unmarshal(r.payload, &payload); jerr != nil {
			t.Fatalf("unmarshal audit payload: %v", jerr)
		}
		if payload["invoice_number_supplied"] != true {
			t.Errorf("payload[invoice_number_supplied] = %v, want true", payload["invoice_number_supplied"])
		}
		if payload["document_id"] != documentID {
			t.Errorf("payload[document_id] = %v, want %q", payload["document_id"], documentID)
		}
		if r.actor != memberSubject {
			t.Errorf("actor = %q, want %q", r.actor, memberSubject)
		}
	}
	if !found {
		t.Fatal("no invoice.created audit row found for the supplied invoice")
	}

	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("import_batches for entity = %d, want 0 (no batch minted)", got)
	}
	if reading, rerr := svc.CarriedReading(callCtx, documentID); rerr != nil || reading != nil {
		t.Errorf("CarriedReading after filing: reading=%v err=%v, want (nil, nil)", reading, rerr)
	}
}

// --- SN-02 -----------------------------------------------------------------------------

// SN-02: a number already on the entity's register refuses with no write and no gate call --
// and the reading survives the collision, so a fresh number still files (no lost work).
func TestServiceSupplyInvoiceNumber_ATakenNumberWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SN-02 tenant")
	entityID := seedEntity(t, super, tenantID, "SN-02 entity")
	callCtx := sxIdentity(ctx, tenantID)

	if _, err := invoice.NewStore(app).Create(callCtx, invoice.CreateInput{EntityID: entityID, InvoiceNumber: "N1"}); err != nil {
		t.Fatalf("seed invoice N1: %v", err)
	}

	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docNoNumberValues())

	g := &fakeGate{}
	svc := newTestServiceWithGate(app, g)

	if _, err := svc.SupplyInvoiceNumber(callCtx, entityID, documentID, "N1"); !errors.Is(err, invoice.ErrDuplicateNumber) {
		t.Fatalf("SupplyInvoiceNumber(N1) err = %v, want invoice.ErrDuplicateNumber", err)
	}
	if got := countInvoicesCitingDocument(t, super, documentID); got != 0 {
		t.Errorf("invoices citing document = %d, want 0", got)
	}
	if g.validateBatchCalls != 0 {
		t.Errorf("gate.ValidateBatch calls = %d, want 0", g.validateBatchCalls)
	}

	// No lost work: the reading is still carriable, and a fresh number files it.
	if reading, err := svc.CarriedReading(callCtx, documentID); err != nil || reading == nil {
		t.Fatalf("CarriedReading after a collision: reading=%v err=%v, want a non-nil reading", reading, err)
	}
	inv, err := svc.SupplyInvoiceNumber(callCtx, entityID, documentID, "N2")
	if err != nil {
		t.Fatalf("SupplyInvoiceNumber(N2): %v", err)
	}
	if inv.InvoiceNumber != "N2" {
		t.Errorf("InvoiceNumber = %q, want %q", inv.InvoiceNumber, "N2")
	}
}

// --- SN-03 -----------------------------------------------------------------------------

// SN-03: an untrimmed number collides with its trimmed twin -- Store.Create's own unique
// constraint sees the trimmed value either way. Control: an untrimmed but genuinely new number
// still files trimmed.
func TestServiceSupplyInvoiceNumber_ASpacedNumberCollidesWithItsTrimmedTwin(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SN-03 tenant")
	entityID := seedEntity(t, super, tenantID, "SN-03 entity")
	callCtx := sxIdentity(ctx, tenantID)

	if _, err := invoice.NewStore(app).Create(callCtx, invoice.CreateInput{EntityID: entityID, InvoiceNumber: "N1"}); err != nil {
		t.Fatalf("seed invoice N1: %v", err)
	}

	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docNoNumberValues())

	svc := newTestServiceWithGate(app, &fakeGate{})
	if _, err := svc.SupplyInvoiceNumber(callCtx, entityID, documentID, "  N1  "); !errors.Is(err, invoice.ErrDuplicateNumber) {
		t.Fatalf("SupplyInvoiceNumber(\"  N1  \") err = %v, want invoice.ErrDuplicateNumber", err)
	}
	if got := countInvoicesCitingDocument(t, super, documentID); got != 0 {
		t.Errorf("invoices citing document = %d, want 0", got)
	}

	inv, err := svc.SupplyInvoiceNumber(callCtx, entityID, documentID, "  N9  ")
	if err != nil {
		t.Fatalf("SupplyInvoiceNumber(\"  N9  \"): %v", err)
	}
	if inv.InvoiceNumber != "N9" {
		t.Errorf("InvoiceNumber = %q, want %q (trimmed)", inv.InvoiceNumber, "N9")
	}
}

// --- SN-04 -----------------------------------------------------------------------------

// SN-04: every shape SupplyInvoiceNumber must refuse -- poor scan/numbered/all-null (not
// carried), no job (not found), and a blank-after-trim number (validation) -- writes nothing
// and never touches the gate.
func TestServiceSupplyInvoiceNumber_RefusesWhatCannotBeCarried(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SN-04 tenant")
	entityID := seedEntity(t, super, tenantID, "SN-04 entity")
	callCtx := sxIdentity(ctx, tenantID)

	poorScanDoc := docSeedDocument(t, super, tenantID)
	docSeedPoorScanExtraction(t, super, tenantID, poorScanDoc)

	numberedDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, numberedDoc, docCleanValues("SN-04-N1"))

	allNullDoc := docSeedDocument(t, super, tenantID)
	allNullValues := docNoNumberValues()
	for k := range allNullValues {
		if k != "invoice_number" {
			allNullValues[k] = nil
		}
	}
	docSeedExtraction(t, super, tenantID, allNullDoc, allNullValues)

	noJobDoc := docSeedDocument(t, super, tenantID)

	blankNumberDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, blankNumberDoc, docNoNumberValues())

	g := &fakeGate{}
	svc := newTestServiceWithGate(app, g)

	cases := []struct {
		name    string
		doc     string
		number  string
		wantErr error
	}{
		{"poor scan", poorScanDoc, "X1", ErrReadingNotCarried},
		{"numbered", numberedDoc, "X2", ErrReadingNotCarried},
		{"all-null", allNullDoc, "X3", ErrReadingNotCarried},
		{"no job", noJobDoc, "X4", ErrNotFound},
		{"blank number", blankNumberDoc, "   ", ErrValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.SupplyInvoiceNumber(callCtx, entityID, tc.doc, tc.number); !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if got := countInvoicesCitingDocument(t, super, tc.doc); got != 0 {
				t.Errorf("invoices citing document = %d, want 0", got)
			}
		})
	}
	if g.validateBatchCalls != 0 {
		t.Errorf("gate.ValidateBatch calls = %d, want 0", g.validateBatchCalls)
	}
}

// --- SN-05 (carry-once) -----------------------------------------------------------------

// SN-05: a document files once. A second supply, even with a different number, refuses --
// the first-filed invoice is the only one that ever cites the document.
func TestServiceSupplyInvoiceNumber_ADocumentFilesOnce(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SN-05 tenant")
	entityID := seedEntity(t, super, tenantID, "SN-05 entity")
	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docNoNumberValues())

	svc := newTestServiceWithGate(app, &fakeGate{})
	callCtx := sxIdentity(ctx, tenantID)

	if _, err := svc.SupplyInvoiceNumber(callCtx, entityID, documentID, "N1"); err != nil {
		t.Fatalf("first supply: %v", err)
	}
	if _, err := svc.SupplyInvoiceNumber(callCtx, entityID, documentID, "N2"); !errors.Is(err, ErrDocumentAlreadyFiled) {
		t.Errorf("second supply err = %v, want ErrDocumentAlreadyFiled", err)
	}
	if got := countInvoicesCitingDocument(t, super, documentID); got != 1 {
		t.Errorf("invoices citing document = %d, want 1", got)
	}
}

// --- SN-06 -----------------------------------------------------------------------------

// SN-06: a gate outage during the post-file re-validate does not roll back the filed invoice --
// the draft keeps its Re-validate.
func TestServiceSupplyInvoiceNumber_AGateOutageKeepsTheInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "SN-06 tenant")
	entityID := seedEntity(t, super, tenantID, "SN-06 entity")
	documentID := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, docNoNumberValues())

	g := &fakeGate{validateBatchErr: invoice.ErrUpstream}
	svc := newTestServiceWithGate(app, g)

	inv, err := svc.SupplyInvoiceNumber(sxIdentity(ctx, tenantID), entityID, documentID, "SN-06-INV")
	if err != nil {
		t.Fatalf("SupplyInvoiceNumber during a gate outage: %v", err)
	}
	if inv.InvoiceNumber != "SN-06-INV" {
		t.Errorf("InvoiceNumber = %q, want %q", inv.InvoiceNumber, "SN-06-INV")
	}
	if inv.Status != invoice.StatusDraft {
		t.Errorf("Status = %q, want %q", inv.Status, invoice.StatusDraft)
	}
	if got := countInvoicesCitingDocument(t, super, documentID); got != 1 {
		t.Errorf("invoices citing document = %d, want 1", got)
	}
}
