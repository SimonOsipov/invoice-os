// document_dup_test.go: proves Core AC 4, 5, 6 for the document path (EXTR-06-04, task-764) --
// the duplicate guarantee under real concurrency. Mode B: subtask 03 already wired
// storeDuplicateRowError into ImportDocument's Create-error branch, so most specs here are
// green on arrival, not red. Each one's doc comment records whether it failed for real or was
// proven non-vacuous by a live mutation (recorded, then reverted -- never left in place).
//
// Spec-to-test map (Test Specs table, EXTR-06-04 / task-764):
//
//	DUP-D1 TestDuplicateGuarantee_UniqueIndexNotDeferrable
//	DUP-D2/D3/D4/D8 TestServiceImportDocument_ConcurrentDuplicateRaceWinnerWritesLosersEnrichedNoOrphans
//	DUP-D5 TestServiceImportDocument_SequentialReimportResolvesInvoiceIDToWinner
//	DUP-D6 TestServiceImportDocument_DuplicateRowErrorMarshalsWithNoRowOrRowsKey
//	DUP-D7 TestServiceImportDocument_SameNumberDifferentEntitiesBothWrite
//
// D2/D3/D4/D8 share one test function: they all read the SAME race (one entity, one number,
// 4 racers), and splitting them would either re-run the race 4 times (more flakiness surface,
// not less) or require passing race state across functions -- the shipped precedent
// (TestServiceImport_ConcurrentDuplicateLoserReportedAsFirstClassViolation, service_dup_test.go
// :224) combines the same four concerns into one test for the same reason.
package importer

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// --- DUP-D1 ----------------------------------------------------------------

// DUP-D1: invoices_tenant_entity_number_uq carries no pg_constraint row. dup_parity_test.go's
// TestPredicateParity_IndexShapeIsUniqueNonPartialOnExactColumns already pins UNIQUE / exact
// columns / non-partial via pg_get_indexdef; this test adds the one thing that one doesn't
// check: not-deferrable. A bare `CREATE UNIQUE INDEX` (migrations/20260714103137_invoices.sql
// :81-82, verified) carries no pg_constraint row at all -- only a CONSTRAINT can be marked
// deferrable in Postgres, so this absence IS the not-deferrable guarantee.
//
// GREEN ON ARRIVAL. Non-vacuity proven by live mutation (reverted, never committed): inside a
// throwaway Go test (internal/importer/zz_mutation_check_test.go, written, run, then deleted
// -- not part of this commit), a rolled-back tx ran `ALTER TABLE invoices ADD CONSTRAINT
// zz_mutation_test_c UNIQUE USING INDEX invoices_tenant_entity_number_uq DEFERRABLE` and the
// count went 0 -> 1 (`pg_constraint WHERE conname = 'zz_mutation_test_c' AND condeferrable`).
// The same mutation also RENAMES the index (Postgres ties a constraint-backed index's name to
// its constraint), so in a real (uncommitted) mutation this test's own regclass cast would
// error out rather than just flip the count -- still a hard failure either way.
func TestDuplicateGuarantee_UniqueIndexNotDeferrable(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	var n int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint WHERE conindid = 'invoices_tenant_entity_number_uq'::regclass`,
	).Scan(&n); err != nil {
		t.Fatalf("query pg_constraint by conindid: %v (a wrap-in-constraint mutation renames the index, so this errors when the guarantee breaks)", err)
	}
	if n != 0 {
		t.Errorf("pg_constraint rows backed by invoices_tenant_entity_number_uq = %d, want 0 -- deferrability is a constraint-only property; a bare index can never be deferred", n)
	}
}

// --- DUP-D2 / DUP-D3 / DUP-D4 / DUP-D8 --------------------------------------

// countStatusHistoryForEntity counts every invoice_status_history row belonging to any invoice
// under entityID -- no existing helper does this (countLineItemsForEntity, perf_test.go, is the
// closest analogue).
func countStatusHistoryForEntity(t *testing.T, super *pgxpool.Pool, entityID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM invoice_status_history h JOIN invoices i ON i.id = h.invoice_id WHERE i.entity_id = $1`,
		entityID,
	).Scan(&n); err != nil {
		t.Fatalf("count invoice_status_history for entity: %v", err)
	}
	return n
}

// DUP-D2/D3/D4/D8: 4 goroutines, one close(start), ONE shared entityID, 4 documents each
// carrying the same invoice_number -> exactly 1 winner, 3 losers, count(invoices)=1, every
// loser's RowError enriched (rule_key/severity/field, never bare), no racer errors or reports
// failed, and no orphaned line_items/invoice_status_history rows survive the losers.
//
// The t.Fatal guard below asserts all four racers used the SAME entityID BEFORE any outcome is
// read -- a split-entity race collides on nothing on (tenant_id, entity_id, invoice_number),
// so all 4 would win and every assertion below would still incidentally hold while proving
// nothing about the guarantee. entityID here is a single variable read by every goroutine (not
// re-resolved per-goroutine), so the guard is structural, not just a runtime check.
//
// GREEN ON ARRIVAL (document.go:201-227 already calls storeDuplicateRowError on
// invoice.ErrDuplicateNumber). Non-vacuity proven by mutation: internal/importer/document.go
// :209-210 temporarily changed from
//
//	if errors.Is(createErr, invoice.ErrDuplicateNumber) {
//	    quarantineErr = storeDuplicateRowError(nil, existing[in.InvoiceNumber])
//	} else {
//	    quarantineErr = RowError{Message: msg}
//	}
//
// to unconditionally `quarantineErr = RowError{Message: msg}` (deleting the enrichment
// branch) -> re-ran this test -> FAILED on the RuleKey/Severity/Field assertions for every
// loser (empty string != "no-duplicate-invoice-number") -> reverted.
func TestServiceImportDocument_ConcurrentDuplicateRaceWinnerWritesLosersEnrichedNoOrphans(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-D2 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-D2 entity")

	const racers = 4
	const raceNumber = "INV-EXTR06-RACE"

	racerEntityIDs := make([]string, racers)
	documentIDs := make([]string, racers)
	for i := 0; i < racers; i++ {
		racerEntityIDs[i] = entityID // one shared entityID, read by every goroutine below
		documentIDs[i] = docSeedDocument(t, super, tenantID)
		docSeedExtraction(t, super, tenantID, documentIDs[i], docCleanValues(raceNumber))
	}

	// Guard: every racer shares one entity id, checked BEFORE any outcome is read (D-19 /
	// task-764's own instruction) -- catches a future edit that splits racers across entities,
	// which would make the race collide on nothing and pass 4-for-4 while proving nothing.
	for i := 1; i < racers; i++ {
		if racerEntityIDs[i] != racerEntityIDs[0] {
			t.Fatalf("racer[%d] entityID = %q, want %q (all racers must share one entity, or the unique key never collides)", i, racerEntityIDs[i], racerEntityIDs[0])
		}
	}

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	results := make([]BatchResult, racers)
	errs := make([]error, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.ImportDocument(callCtx, racerEntityIDs[i], documentIDs[i])
		}(i)
	}
	close(start)
	wg.Wait()

	// DUP-D4: no racer returns a non-nil error, none reports status "failed".
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer[%d] ImportDocument unexpected error: %v (a racing-INSERT loser must be quarantined, never returned as an error)", i, err)
		}
		if results[i].Status != "completed" {
			t.Errorf("racer[%d].Status = %q, want %q", i, results[i].Status, "completed")
		}
	}

	winners, losers := 0, 0
	for i := 0; i < racers; i++ {
		r := results[i]
		switch {
		case r.ReadyInvoices == 1 && r.QuarantinedInvoices == 0:
			winners++
		case r.ReadyInvoices == 0 && r.QuarantinedInvoices == 1:
			losers++
			if len(r.Errors) != 1 {
				t.Fatalf("racer[%d] quarantined but len(Errors)=%d, want 1: %+v", i, len(r.Errors), r.Errors)
			}
			// DUP-D3: the enriched shape. InvoiceID is deliberately NOT asserted here --
			// under real concurrency the winner's row is uncommitted when the loser's
			// ExistingNumbers runs, so InvoiceID is legitimately empty on this path.
			re := r.Errors[0]
			if re.RuleKey != ruleKeyDuplicateInvoiceNumber {
				t.Errorf("racer[%d] (loser) RowError.RuleKey = %q, want %q", i, re.RuleKey, ruleKeyDuplicateInvoiceNumber)
			}
			if re.Severity != "error" {
				t.Errorf("racer[%d] (loser) RowError.Severity = %q, want %q", i, re.Severity, "error")
			}
			if re.Field != "invoice_number" {
				t.Errorf("racer[%d] (loser) RowError.Field = %q, want %q", i, re.Field, "invoice_number")
			}
		default:
			t.Errorf("racer[%d] unexpected verdict (ReadyInvoices=%d QuarantinedInvoices=%d), want exactly one of (1,0)/(0,1)", i, r.ReadyInvoices, r.QuarantinedInvoices)
		}
	}
	if winners != 1 || losers != racers-1 {
		t.Fatalf("aggregate racer outcome (winners=%d losers=%d), want (1,%d)", winners, losers, racers-1)
	}

	// DUP-D2: exactly one row lands despite racers concurrent commit attempts.
	if got := countInvoicesByNumber(t, super, entityID, raceNumber); got != 1 {
		t.Errorf("stored %s rows = %d, want exactly 1 despite %d concurrent commit attempts", raceNumber, got, racers)
	}

	// DUP-D8: the losers leave no partial invoice (checked above), no orphan line_items and no
	// orphan invoice_status_history row. The document path never writes line_items at all
	// (Core AC 8, document.go:104-106), so the entity-wide count must be exactly 0; the
	// winner's own Create writes exactly one status-history row (internal/invoice/store.go
	// :261-266, inside the SAME tx as the invoices INSERT it follows -- a loser's 23505 aborts
	// that tx before this INSERT runs, so a loser can never leave one behind).
	if got := countLineItemsForEntity(t, super, entityID); got != 0 {
		t.Errorf("line_items for entity = %d, want 0 (the document path never writes line items)", got)
	}
	if got := countStatusHistoryForEntity(t, super, entityID); got != 1 {
		t.Errorf("invoice_status_history rows for entity = %d, want exactly 1 (only the winner transitions)", got)
	}
}

// --- DUP-D5 ------------------------------------------------------------------

// DUP-D5: a sequential (non-racing) second import of the same number on the same entity
// reports the same enriched shape AND resolves InvoiceID to the first import's invoice id via
// the fast path (ExistingNumbers's upfront precheck resolves it before Create even runs, D-12
// in .ralph/EXTR-06-finalized.md).
//
// GREEN ON ARRIVAL. Non-vacuity proven by mutation: internal/importer/document.go:210
// temporarily changed from `storeDuplicateRowError(nil, existing[in.InvoiceNumber])` to
// `storeDuplicateRowError(nil, "")` (dropping the resolved id) -> re-ran this test -> FAILED
// on the InvoiceID assertion (got "" want the winner's id) -> reverted.
func TestServiceImportDocument_SequentialReimportResolvesInvoiceIDToWinner(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-D5 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-D5 entity")
	const number = "INV-EXTR06-SEQ"

	doc1 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc1, docCleanValues(number))
	doc2 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc2, docCleanValues(number))

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	first, err := svc.ImportDocument(callCtx, entityID, doc1)
	if err != nil {
		t.Fatalf("first ImportDocument: %v", err)
	}
	if first.ReadyInvoices != 1 || first.QuarantinedInvoices != 0 {
		t.Fatalf("first result = %+v, want the winner (ReadyInvoices=1 QuarantinedInvoices=0)", first)
	}
	winnerID := invoiceIDByNumber(t, super, entityID, number)

	second, err := svc.ImportDocument(callCtx, entityID, doc2)
	if err != nil {
		t.Fatalf("second ImportDocument: %v", err)
	}
	if second.ReadyInvoices != 0 || second.QuarantinedInvoices != 1 {
		t.Fatalf("second result = %+v, want the loser (ReadyInvoices=0 QuarantinedInvoices=1)", second)
	}
	if len(second.Errors) != 1 {
		t.Fatalf("second.Errors = %+v, want exactly 1 entry", second.Errors)
	}

	re := second.Errors[0]
	if re.RuleKey != ruleKeyDuplicateInvoiceNumber || re.Severity != "error" || re.Field != "invoice_number" {
		t.Errorf("second RowError = %+v, want the enriched shape (rule_key=%q severity=error field=invoice_number)", re, ruleKeyDuplicateInvoiceNumber)
	}
	if re.InvoiceID != winnerID {
		t.Errorf("second RowError.InvoiceID = %q, want the winner's id %q (fast path via ExistingNumbers)", re.InvoiceID, winnerID)
	}

	if got := countInvoicesByNumber(t, super, entityID, number); got != 1 {
		t.Errorf("stored %s rows = %d, want 1", number, got)
	}
}

// --- DUP-D6 ------------------------------------------------------------------

// DUP-D6: the document duplicate RowError marshals with neither a "row" nor a "rows" key --
// there are no sheet rows to cite for a document import, and inventing one (Row/Rows) would be
// a false claim. Uses the RowError DUP-D5 already proved is the real duplicate shape.
//
// GREEN ON ARRIVAL (storeDuplicateRowError always calls sheetRows(nil), which returns a
// non-nil EMPTY []int{}; `Rows []int json:"rows,omitempty"` omits an empty slice regardless of
// nil-ness, and Row int is never set on this path). Non-vacuity proven by mutation:
// internal/importer/service.go's sheetRows temporarily changed from
// `out := make([]int, len(rowIdxs))` to `out := make([]int, len(rowIdxs)+1)` (handing back a
// fabricated one-element slice for a nil/empty input) -> re-ran this test -> FAILED (the "rows"
// key appeared in the marshalled JSON) -> reverted.
func TestServiceImportDocument_DuplicateRowErrorMarshalsWithNoRowOrRowsKey(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-D6 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-D6 entity")
	const number = "INV-EXTR06-JSON"

	doc1 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc1, docCleanValues(number))
	doc2 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, doc2, docCleanValues(number))

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	if _, err := svc.ImportDocument(callCtx, entityID, doc1); err != nil {
		t.Fatalf("first ImportDocument: %v", err)
	}
	second, err := svc.ImportDocument(callCtx, entityID, doc2)
	if err != nil {
		t.Fatalf("second ImportDocument: %v", err)
	}
	if len(second.Errors) != 1 {
		t.Fatalf("second.Errors = %+v, want exactly 1 entry", second.Errors)
	}

	raw, err := json.Marshal(second.Errors[0])
	if err != nil {
		t.Fatalf("json.Marshal(RowError): %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal into map: %v", err)
	}
	if _, ok := m["row"]; ok {
		t.Errorf("marshalled RowError carries a %q key: %s, want it absent", "row", raw)
	}
	if _, ok := m["rows"]; ok {
		t.Errorf("marshalled RowError carries a %q key: %s, want it absent", "rows", raw)
	}
	// Control needle: rule_key MUST be present, or an empty/failed parse would pass vacuously.
	if _, ok := m["rule_key"]; !ok {
		t.Fatalf("marshalled RowError has no %q key at all: %s -- the map parse itself is broken", "rule_key", raw)
	}
}

// --- DUP-D7 ------------------------------------------------------------------

// DUP-D7: the same number on two DIFFERENT entities of one tenant both write -- the guarantee
// is per-entity (invoices_tenant_entity_number_uq spans tenant_id, entity_id, invoice_number),
// not per-tenant, even though the story's prose could be misread that way.
//
// GREEN ON ARRIVAL (ImportDocument never prechecks tenant-wide; ExistingNumbers itself is
// entity_id-scoped, store.go's ExistingNumbers query; the real guard is
// invoices_tenant_entity_number_uq, which is scoped to entity_id). ExistingNumbers never
// gates Create, so mutating it cannot change this test's outcome -- the load-bearing thing is
// the DB index. Non-vacuity proven by a live schema mutation (not a Go source edit): a
// throwaway test (zz_mutation_d7_test.go, written/run/deleted -- not part of this commit)
// committed `CREATE UNIQUE INDEX zz_mutation_tenant_number_uq ON invoices (tenant_id,
// invoice_number)` (a broader, TENANT-wide key, simulating "a writer that prechecks
// tenant-wide" -- the Test Specs table's own RED reason) -> re-ran this test -> FAILED
// (entityB's write hit 23505 against entityA's row and was quarantined instead of written;
// entityB stored rows = 0, want 1) -> dropped the temporary index, re-ran, back to PASS.
func TestServiceImportDocument_SameNumberDifferentEntitiesBothWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-D7 tenant")
	entityA := seedEntity(t, super, tenantID, "DUP-D7 entity A")
	entityB := seedEntity(t, super, tenantID, "DUP-D7 entity B")
	const number = "INV-EXTR06-CROSSENTITY"

	docA := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, docA, docCleanValues(number))
	docB := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, docB, docCleanValues(number))

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	resA, err := svc.ImportDocument(callCtx, entityA, docA)
	if err != nil {
		t.Fatalf("ImportDocument(entityA): %v", err)
	}
	resB, err := svc.ImportDocument(callCtx, entityB, docB)
	if err != nil {
		t.Fatalf("ImportDocument(entityB): %v", err)
	}

	for name, r := range map[string]BatchResult{"entityA": resA, "entityB": resB} {
		if r.Status != "completed" || r.ReadyInvoices != 1 || r.QuarantinedInvoices != 0 {
			t.Errorf("%s result = %+v, want completed with ReadyInvoices=1 QuarantinedInvoices=0", name, r)
		}
	}

	if got := countInvoicesByNumber(t, super, entityA, number); got != 1 {
		t.Errorf("entityA stored %s rows = %d, want 1", number, got)
	}
	if got := countInvoicesByNumber(t, super, entityB, number); got != 1 {
		t.Errorf("entityB stored %s rows = %d, want 1", number, got)
	}
}
