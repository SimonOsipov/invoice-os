// backfill_qa_test.go: QA adversarial coverage for BackfillSourceRows (DOC-02-08,
// task-364 Stage 4) -- closes the 3 gaps the executor reported against its own
// code, pins the duplicate-detection/coverage-scope split (Stage 3 deviation),
// and adds edge cases the pre-authored red specs (backfill_test.go) didn't
// reach: a symmetric-trim collision, a genuine concurrent writer, a real PATCH
// line-item replacement, an XLSX gap row, a document shared across entities,
// an empty tenant, and a malformed tenant id.
package importer

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// renameInvoiceNumber simulates a post-import invoice_number edit -- direct
// SQL, since invoice_number isn't part of invoice.EditInput.
func renameInvoiceNumber(t *testing.T, super *pgxpool.Pool, invoiceID, newNumber string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET invoice_number = $1 WHERE id = $2`, newNumber, invoiceID,
	); err != nil {
		t.Fatalf("rename invoice_number: %v", err)
	}
}

// --- Part 1, gap 1: symmetric trim ------------------------------------------

// TestBackfill_SymmetricTrimDoesNotMisattributeWhitespaceVariants: two
// invoices whose raw numbers differ ONLY by whitespace. Exact matching keeps
// them apart; TrimSpace(cell)==TrimSpace(number) would collapse both onto the
// same key and mismatch both invoices' matched-row counts against their
// stored line-item counts, refusing what should recover cleanly.
func TestBackfill_SymmetricTrimDoesNotMisattributeWhitespaceVariants(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "symmetric-trim tenant")
	entityID := seedEntity(t, super, tenantID, "symmetric-trim entity")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	const plain, padded = "INV-1", " INV-1 "
	rows := [][]string{
		mkRow(plain, "2026-01-10", "TIN-1", "Buyer A", "NGN", "100.00", "10.00", "110.00", "Item A", "1", "100.00"),
		mkRow(padded, "2026-01-11", "TIN-2", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Item B", "1", "200.00"),
	}
	doc := storeDocumentAs(t, docSvc, tenantID, "symmetric.csv", "text/csv", csvBody(t, stdHeader, rows))

	svc := newTestService(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(c, entityID, "", doc.ID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	invPlain := invoiceIDByNumber(t, super, entityID, plain)
	invPadded := invoiceIDByNumber(t, super, entityID, padded)
	wantPlain := sourceRowsOf(t, super, invPlain)
	wantPadded := sourceRowsOf(t, super, invPadded)
	nullSourceRows(t, super, invPlain, invPadded)

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesWritten != 2 {
		t.Fatalf("InvoicesWritten = %d, want 2 -- exact matching must recover both distinctly", res.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, invPlain); !intSliceEqual(got, wantPlain) {
		t.Errorf("plain invoice source_rows = %v, want %v", got, wantPlain)
	}
	if got := sourceRowsOf(t, super, invPadded); !intSliceEqual(got, wantPadded) {
		t.Errorf("padded invoice source_rows = %v, want %v", got, wantPadded)
	}
}

// --- Part 1, gap 2: the AND source_rows IS NULL guard is a concurrency guard -

// waitForBlockedBackend polls pg_stat_activity until some OTHER backend is
// genuinely waiting on a lock, or fails the test. A bounded poll, not a fixed
// sleep, so the concurrency test below cannot pass without truly forcing the
// interleaving it claims to test.
func waitForBlockedBackend(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid()`,
		).Scan(&n); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("BackfillSourceRows's UPDATE never blocked -- the concurrency scenario was not exercised")
}

// TestBackfill_ConcurrentPopulationSurvivesTheGuard pins `AND source_rows IS
// NULL` as a concurrency guard, not redundancy with the needsRows filter. A
// writer takes a FOR UPDATE lock on the target row BEFORE BackfillSourceRows
// even starts, so its own SELECT still reads NULL (the invoice becomes a
// target); only once BackfillSourceRows's UPDATE is provably blocked on that
// lock does the writer populate the row and commit. Postgres re-checks an
// UPDATE's WHERE clause on unblock, so the guard must see the row is no
// longer NULL and refuse to overwrite it. Deleting the guard would let the
// backfill's stale `matched` value clobber the concurrent writer's row.
func TestBackfill_ConcurrentPopulationSurvivesTheGuard(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "concurrent-guard")
	nullSourceRows(t, super, fx.invA)

	ctx := context.Background()
	writer, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent writer tx: %v", err)
	}
	defer func() { _ = writer.Rollback(ctx) }()
	var locked string
	if err := writer.QueryRow(ctx, `SELECT id FROM invoices WHERE id = $1 FOR UPDATE`, fx.invA).Scan(&locked); err != nil {
		t.Fatalf("lock invoice row: %v", err)
	}

	type outcome struct {
		res BackfillResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := BackfillSourceRows(ctx, app, fx.open, fx.tenantID, false)
		done <- outcome{res, err}
	}()

	waitForBlockedBackend(t, super)

	if _, err := writer.Exec(ctx, `UPDATE invoices SET source_rows = '{99}' WHERE id = $1`, fx.invA); err != nil {
		t.Fatalf("concurrent writer UPDATE: %v", err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent writer: %v", err)
	}

	out := <-done
	if out.err != nil {
		t.Fatalf("BackfillSourceRows: %v", out.err)
	}
	if out.res.InvoicesWritten != 0 {
		t.Errorf("InvoicesWritten = %d, want 0 -- another writer populated the row before this UPDATE landed", out.res.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, fx.invA); !intSliceEqual(got, []int{99}) {
		t.Errorf("invoice A source_rows = %v, want [99] (the concurrent writer's value) -- the guard must refuse to clobber it", got)
	}
}

// --- Part 3: the duplicate/coverage scope split (Stage 3 deviation) --------

// TestBackfill_PopulatedSiblingWithEditedNumberDoesNotBlockRecovery: invB is
// already populated (not a target) and its number is renamed to something
// absent from the file, simulating a post-import edit. Coverage/tie inference
// is scoped to the NULL subset, so invB's now-unmatchable number must not
// block invA's recovery -- the same per-invoice blast radius principle
// TestBackfill_LineItemCountMismatchWritesNothing already pins for the
// line-count check.
func TestBackfill_PopulatedSiblingWithEditedNumberDoesNotBlockRecovery(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "split-scope")
	nullSourceRows(t, super, fx.invA)
	renameInvoiceNumber(t, super, fx.invB, "RENAMED-AFTER-IMPORT")

	res, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.DocumentsAmbiguous != 0 {
		t.Errorf("DocumentsAmbiguous = %d, want 0 -- invB's edited number must not block invA", res.DocumentsAmbiguous)
	}
	if res.InvoicesWritten != 1 {
		t.Errorf("InvoicesWritten = %d, want 1", res.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, fx.invA); !intSliceEqual(got, fx.rowsA) {
		t.Errorf("invoice A source_rows = %v, want %v", got, fx.rowsA)
	}
	if got := sourceRowsOf(t, super, fx.invB); !intSliceEqual(got, fx.rowsB) {
		t.Errorf("invoice B source_rows = %v, want %v (untouched)", got, fx.rowsB)
	}
}

// TestBackfill_DuplicateWithAnAlreadyPopulatedSiblingIsAmbiguous: invB is
// already populated (not a target) but shares invA's raw invoice_number, via
// the same two-entities-one-document construction the pre-authored duplicate
// spec uses (a single entity's unique index forbids the collision). Duplicate
// detection scans EVERY invoice on the document, so invA must still refuse --
// an already-populated sibling is just as blocking here as another target
// would be, because the row->invoice mapping is ambiguous either way.
func TestBackfill_DuplicateWithAnAlreadyPopulatedSiblingIsAmbiguous(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "dup-populated tenant")
	entityA := seedEntity(t, super, tenantID, "dup-populated entity A")
	entityB := seedEntity(t, super, tenantID, "dup-populated entity B")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	rows := [][]string{
		mkRow("DUP-POP-1", "2026-01-10", "TIN-1", "Buyer", "NGN", "100.00", "10.00", "110.00", "Item", "1", "100.00"),
	}
	doc := storeDocumentAs(t, docSvc, tenantID, "dup-pop.csv", "text/csv", csvBody(t, stdHeader, rows))

	svc := newTestService(app)
	cA := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	cB := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(cA, entityA, "", doc.ID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("import into entity A: %v", err)
	}
	if _, err := svc.Import(cB, entityB, "", doc.ID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("import into entity B: %v", err)
	}
	invA := invoiceIDByNumber(t, super, entityA, "DUP-POP-1")
	invB := invoiceIDByNumber(t, super, entityB, "DUP-POP-1")
	wantB := sourceRowsOf(t, super, invB)
	nullSourceRows(t, super, invA) // invB stays populated -- not a target

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.DocumentsAmbiguous != 1 {
		t.Errorf("DocumentsAmbiguous = %d, want 1", res.DocumentsAmbiguous)
	}
	if got := sourceRowsOf(t, super, invA); got != nil {
		t.Errorf("entity A invoice source_rows = %v, want SQL NULL -- invB's populated duplicate must still refuse it", got)
	}
	if got := sourceRowsOf(t, super, invB); !intSliceEqual(got, wantB) {
		t.Errorf("entity B invoice source_rows = %v, want %v (untouched)", got, wantB)
	}
}

// --- Part 4: adversarial coverage -------------------------------------------

// TestBackfill_SingleInvoiceSingleRowDocument: the minimal case -- one
// invoice, one row, one target.
func TestBackfill_SingleInvoiceSingleRowDocument(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "solo tenant")
	entityID := seedEntity(t, super, tenantID, "solo entity")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	rows := [][]string{mkRow("SOLO-1", "2026-01-10", "TIN-1", "Buyer", "NGN", "100.00", "10.00", "110.00", "Item", "1", "100.00")}
	doc := storeDocumentAs(t, docSvc, tenantID, "solo.csv", "text/csv", csvBody(t, stdHeader, rows))

	svc := newTestService(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(c, entityID, "", doc.ID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	inv := invoiceIDByNumber(t, super, entityID, "SOLO-1")
	want := sourceRowsOf(t, super, inv)
	if len(want) == 0 {
		t.Fatal("importer recorded empty source_rows before the test starts")
	}
	nullSourceRows(t, super, inv)

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesWritten != 1 {
		t.Errorf("InvoicesWritten = %d, want 1", res.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, inv); !intSliceEqual(got, want) {
		t.Errorf("source_rows = %v, want %v", got, want)
	}
}

// TestBackfill_PATCHReplacedLineItemsIsARealMismatch: invoice A's line items
// are replaced via the real invoice.Store.Edit path (replaceLinesTx: DELETE +
// re-INSERT the whole set), not a raw extra-row INSERT -- the mismatch this
// produces is the one the algorithm's step 5 exists for, exercised through
// the actual PATCH mechanism rather than synthesized.
func TestBackfill_PATCHReplacedLineItemsIsARealMismatch(t *testing.T) {
	super, app := dbTestPools(t)
	fx := newBFTwoInvoiceFixture(t, super, app, "patch-mismatch")
	nullSourceRows(t, super, fx.invA, fx.invB)

	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: fx.tenantID})
	descA1, descA2 := "Replaced 1", "Replaced 2"
	priceA1, priceA2 := "50.00", "60.00"
	if _, err := invoice.NewStore(app).Edit(c, fx.invA, invoice.EditInput{LineItems: &[]invoice.LineItemInput{
		{Description: &descA1, UnitPrice: &priceA1},
		{Description: &descA2, UnitPrice: &priceA2},
	}}); err != nil {
		t.Fatalf("Edit (replace line items via PATCH): %v", err)
	}

	res, err := BackfillSourceRows(context.Background(), app, fx.open, fx.tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesAmbiguous != 1 {
		t.Errorf("InvoicesAmbiguous = %d, want 1", res.InvoicesAmbiguous)
	}
	if got := sourceRowsOf(t, super, fx.invA); got != nil {
		t.Errorf("invoice A source_rows = %v, want SQL NULL -- the PATCH-replaced line set no longer agrees with its 1 matched row", got)
	}
	if got := sourceRowsOf(t, super, fx.invB); !intSliceEqual(got, fx.rowsB) {
		t.Errorf("invoice B source_rows = %v, want %v -- its unedited sibling must still recover", got, fx.rowsB)
	}
}

// TestBackfill_XLSXGapRowDoesNotShiftRecoveredRowNumbers: excelize
// materializes an untouched row as an empty []string, consuming a rows[]
// index without becoming an invoice -- the row after the gap must still
// recover its correct (post-gap) sheet row.
func TestBackfill_XLSXGapRowDoesNotShiftRecoveredRowNumbers(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "xlsx-gap tenant")
	entityID := seedEntity(t, super, tenantID, "xlsx-gap entity")

	rows := [][]string{
		mkRow("GAP-1", "2026-01-10", "TIN-1", "Buyer A", "NGN", "100.00", "10.00", "110.00", "Item A", "1", "100.00"),
		{}, // untouched -- excelize materializes this as a gap (sheet row 3)
		mkRow("GAP-2", "2026-01-11", "TIN-2", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Item B", "1", "200.00"),
	}
	xlsxBytes := xlsxBody(t, stdHeader, rows)
	_, decodedRows, _, err := Decode(bytes.NewReader(xlsxBytes), "xlsx")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(decodedRows) != 3 {
		t.Fatalf("len(decodedRows) = %d, want 3 (including the gap)", len(decodedRows))
	}

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	doc := storeDocumentAs(t, docSvc, tenantID, "gap.xlsx", xlsxContentType, xlsxBytes)

	svc := newTestService(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(c, entityID, "", doc.ID, stdMapping, stdHeader, decodedRows, false); err != nil {
		t.Fatalf("import fixture: %v", err)
	}

	inv1 := invoiceIDByNumber(t, super, entityID, "GAP-1")
	inv2 := invoiceIDByNumber(t, super, entityID, "GAP-2")
	want1 := sourceRowsOf(t, super, inv1)
	want2 := sourceRowsOf(t, super, inv2)
	if len(want1) == 0 || len(want2) == 0 {
		t.Fatalf("importer recorded empty source_rows before the test starts: 1=%v 2=%v", want1, want2)
	}
	nullSourceRows(t, super, inv1, inv2)

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesWritten != 2 {
		t.Errorf("InvoicesWritten = %d, want 2", res.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, inv1); !intSliceEqual(got, want1) {
		t.Errorf("GAP-1 source_rows = %v, want %v", got, want1)
	}
	if got := sourceRowsOf(t, super, inv2); !intSliceEqual(got, want2) {
		t.Errorf("GAP-2 source_rows = %v, want %v (must skip past the gap row correctly)", got, want2)
	}
}

// TestBackfill_TenantWithNoCandidatesIsANoop: a freshly seeded tenant with no
// invoices at all -- open must never be called, and the result is the zero
// value with no error.
func TestBackfill_TenantWithNoCandidatesIsANoop(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "empty tenant")

	open := func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error) {
		t.Fatal("open must never be reached for a tenant with no candidate documents")
		return document.Document{}, document.Object{}, nil
	}

	res, err := BackfillSourceRows(context.Background(), app, open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.DocumentsScanned != 0 || res.InvoicesWritten != 0 || res.InvoicesRecoverable != 0 {
		t.Errorf("result = %+v, want the zero value", res)
	}
	if len(res.Notes) != 0 {
		t.Errorf("Notes = %v, want empty", res.Notes)
	}
}

// TestBackfill_SameDocumentAcrossEntitiesRecoversEachIndependently: one
// document backs invoices in two entities under one tenant, with distinct
// (non-duplicate) numbers -- documents are tenant-scoped, not entity-scoped,
// so both recover independently and correctly. The non-ambiguous counterpart
// to TestBackfill_DuplicateInvoiceNumberInOneDocumentIsAmbiguous.
func TestBackfill_SameDocumentAcrossEntitiesRecoversEachIndependently(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "cross-entity tenant")
	entityA := seedEntity(t, super, tenantID, "cross-entity A")
	entityB := seedEntity(t, super, tenantID, "cross-entity B")

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	rows := [][]string{
		mkRow("ENT-A-1", "2026-01-10", "TIN-A", "Buyer A", "NGN", "100.00", "10.00", "110.00", "Item", "1", "100.00"),
		mkRow("ENT-B-1", "2026-01-11", "TIN-B", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Item", "1", "200.00"),
	}
	doc := storeDocumentAs(t, docSvc, tenantID, "cross-entity.csv", "text/csv", csvBody(t, stdHeader, rows))

	invA := seedInvoiceForDocument(t, super, tenantID, entityA, doc.ID, "ENT-A-1")
	addExtraLineItem(t, super, tenantID, invA) // gives it exactly 1 line_items row
	invB := seedInvoiceForDocument(t, super, tenantID, entityB, doc.ID, "ENT-B-1")
	addExtraLineItem(t, super, tenantID, invB)

	res, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false)
	if err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if res.InvoicesWritten != 2 {
		t.Errorf("InvoicesWritten = %d, want 2", res.InvoicesWritten)
	}
	if got := sourceRowsOf(t, super, invA); !intSliceEqual(got, []int{2}) {
		t.Errorf("entity A invoice source_rows = %v, want [2]", got)
	}
	if got := sourceRowsOf(t, super, invB); !intSliceEqual(got, []int{3}) {
		t.Errorf("entity B invoice source_rows = %v, want [3]", got)
	}
}

// TestBackfill_MalformedTenantIDFailsClosedNoPanic: db.WithinTenantTx
// validates the tenant id as a uuid and fails closed -- open must never be
// reached.
func TestBackfill_MalformedTenantIDFailsClosedNoPanic(t *testing.T) {
	_, app := dbTestPools(t)

	open := func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error) {
		t.Fatal("open must never be reached for a malformed tenant id")
		return document.Document{}, document.Object{}, nil
	}

	res, err := BackfillSourceRows(context.Background(), app, open, "not-a-uuid", false)
	if !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("err = %v, want db.ErrNoTenant", err)
	}
	if res.DocumentsScanned != 0 || res.InvoicesWritten != 0 {
		t.Errorf("result = %+v, want the zero value", res)
	}
}

// --- Part 2: row numbering follows Decode order, never physical lines -----

// multilineCSVBytes builds header + row1 via csv.Writer, then a raw blank
// physical line (encoding/csv drops it -- no rows[] entry), then row2 (whose
// last cell embeds a literal newline, so csv.Writer quotes it and it decodes
// back as ONE record spanning two physical lines).
func multilineCSVBytes(t *testing.T, header, row1, row2 []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := w.Write(row1); err != nil {
		t.Fatalf("write row1: %v", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	buf.WriteString("\n")
	w2 := csv.NewWriter(&buf)
	if err := w2.Write(row2); err != nil {
		t.Fatalf("write row2: %v", err)
	}
	w2.Flush()
	if err := w2.Error(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return buf.Bytes()
}

// TestBackfill_RowNumberingMatchesImporterAcrossBlankLinesAndQuotedNewlines: a
// blank physical line (dropped by encoding/csv) followed by a row whose last
// cell embeds a literal newline (one logical record spanning two physical
// lines) -- BackfillSourceRows must recover the SAME sheet row the importer
// itself recorded for the identical bytes, proving both read Decode's row
// order, never physical line numbers.
func TestBackfill_RowNumberingMatchesImporterAcrossBlankLinesAndQuotedNewlines(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "row-numbering tenant")
	entityID := seedEntity(t, super, tenantID, "row-numbering entity")

	row1 := mkRow("INV-FIRST", "2026-01-10", "TIN-1", "Buyer A", "NGN", "100.00", "10.00", "110.00", "Item A", "1", "100.00")
	row2 := mkRow("INV-MULTI", "2026-01-12", "TIN-2", "Buyer B", "NGN", "200.00", "20.00", "220.00", "Multi\nLine", "1", "200.00")
	content := multilineCSVBytes(t, stdHeader, row1, row2)

	_, decodedRows, _, err := Decode(bytes.NewReader(content), "csv")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(decodedRows) != 2 {
		t.Fatalf("len(decodedRows) = %d, want 2 -- the blank line must not become a row", len(decodedRows))
	}

	docSvc := document.NewService(document.NewStore(app), newMemObjects())
	doc := storeDocumentAs(t, docSvc, tenantID, "multiline.csv", "text/csv", content)

	svc := newTestService(app)
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	if _, err := svc.Import(c, entityID, "", doc.ID, stdMapping, stdHeader, decodedRows, false); err != nil {
		t.Fatalf("import fixture: %v", err)
	}

	invMulti := invoiceIDByNumber(t, super, entityID, "INV-MULTI")
	want := sourceRowsOf(t, super, invMulti)
	if len(want) == 0 {
		t.Fatal("importer recorded empty source_rows for INV-MULTI before the test starts")
	}
	nullSourceRows(t, super, invMulti)

	if _, err := BackfillSourceRows(context.Background(), app, docSvc.Open, tenantID, false); err != nil {
		t.Fatalf("BackfillSourceRows: %v", err)
	}
	if got := sourceRowsOf(t, super, invMulti); !intSliceEqual(got, want) {
		t.Errorf("source_rows = %v, want %v (the importer's own recorded range)", got, want)
	}
}
