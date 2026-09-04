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
//
// QA pass (task-764): the winners==1/losers==racers-1 assertion holds identically whether the
// racers truly overlap or run fully serialized -- Create's unique-constraint 23505 fires on the
// 2nd..Nth attempt to write one number either way, and this test never asserted anything that
// would tell the two apart (loser InvoiceID is deliberately unchecked, see DUP-D3's own doc
// comment). TestServiceImportDocument_ConcurrentDuplicateRaceWinnerWritesLosersEnrichedNoOrphans
// now also times each racer's ImportDocument call and asserts at least one pair of intervals
// truly overlaps -- this is the one assertion in the file that actually distinguishes "raced"
// from "serialized-and-happened-to-match." Also added: TestServiceImportDocument_
// EightConcurrentRacersYieldExactlyOneWinnerSevenLosers (wider N), TestServiceImportDocument_
// MixedCollisionRaceIsolatesPerEntityOutcomes (same document/different entities interleaved with
// a same-entity collision), TestServiceImportDocument_UnreadableNumberQuarantineStaysDistinctFrom
// DuplicateQuarantineUnderRace (two quarantine reasons in one race stay distinguishable).
package importer

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

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

// countAuditLogForEntity counts invoice.created audit_log rows attributed to entityID -- the
// event's own resolver (migrations/20260829195203_audit_log_entity_for_extraction.sql) joins
// invoices on the id in payload to fill audit_log.entity_id, so this is entity-scoped even
// though the INSERT itself never names entity_id directly. audit.Record (internal/invoice/
// store.go, Create) runs LAST inside Create's tx, after the invoices INSERT -- a loser's 23505
// aborts that tx before audit.Record is ever reached, so a loser can leave no row here either.
func countAuditLogForEntity(t *testing.T, super *pgxpool.Pool, entityID string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE entity_id = $1 AND event = 'invoice.created'`,
		entityID,
	).Scan(&n); err != nil {
		t.Fatalf("count audit_log for entity: %v", err)
	}
	return n
}

// intervalsOverlap reports whether [aStart,aEnd) and [bStart,bEnd) share any instant.
func intervalsOverlap(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
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
	callStart := make([]time.Time, racers)
	callEnd := make([]time.Time, racers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			callStart[i] = time.Now()
			results[i], errs[i] = svc.ImportDocument(callCtx, racerEntityIDs[i], documentIDs[i])
			callEnd[i] = time.Now()
		}(i)
	}
	close(start)
	wg.Wait()

	// QA pass (task-764): winners==1/losers==racers-1 alone holds even under complete
	// serialization (Create's 23505 fires on the 2nd..Nth write attempt regardless of overlap),
	// so it does NOT by itself prove concurrency -- only that the constraint works. This is the
	// one assertion that does: at least one pair of racers' ImportDocument calls must have truly
	// overlapped in wall-clock time, or the goroutines never actually raced.
	overlapped := false
	for i := 0; i < racers && !overlapped; i++ {
		for j := i + 1; j < racers; j++ {
			if intervalsOverlap(callStart[i], callEnd[i], callStart[j], callEnd[j]) {
				overlapped = true
				break
			}
		}
	}
	if !overlapped {
		t.Errorf("no two racers' ImportDocument calls overlapped in time (starts=%v ends=%v) -- the goroutines ran fully serialized, so this run proves only that the constraint works, not that it works under concurrency (Core AC 6)", callStart, callEnd)
	}

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
	// QA pass (task-764): a loser's 23505 aborts Create's tx before audit.Record runs (it is
	// the LAST statement in that tx), so no loser can leave an orphaned audit_log row either.
	if got := countAuditLogForEntity(t, super, entityID); got != 1 {
		t.Errorf("audit_log invoice.created rows for entity = %d, want exactly 1 (only the winner's Create reaches audit.Record)", got)
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

// --- QA pass: wider N -------------------------------------------------------

// QA pass (task-764): the same DUP-D2 race at N=8 -- if the 1-winner/(N-1)-loser split were an
// artifact of racers=4 specifically, a wider N would expose it.
func TestServiceImportDocument_EightConcurrentRacersYieldExactlyOneWinnerSevenLosers(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-QA-8 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-QA-8 entity")

	const racers = 8
	const raceNumber = "INV-EXTR06-RACE8"

	documentIDs := make([]string, racers)
	for i := 0; i < racers; i++ {
		documentIDs[i] = docSeedDocument(t, super, tenantID)
		docSeedExtraction(t, super, tenantID, documentIDs[i], docCleanValues(raceNumber))
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
			results[i], errs[i] = svc.ImportDocument(callCtx, entityID, documentIDs[i])
		}(i)
	}
	close(start)
	wg.Wait()

	winners, losers := 0, 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer[%d] unexpected error: %v", i, err)
		}
		r := results[i]
		if r.Status != "completed" {
			t.Errorf("racer[%d].Status = %q, want %q", i, r.Status, "completed")
		}
		switch {
		case r.ReadyInvoices == 1 && r.QuarantinedInvoices == 0:
			winners++
		case r.ReadyInvoices == 0 && r.QuarantinedInvoices == 1:
			losers++
			if len(r.Errors) != 1 {
				t.Fatalf("racer[%d] quarantined but len(Errors)=%d, want 1: %+v", i, len(r.Errors), r.Errors)
			}
			re := r.Errors[0]
			if re.RuleKey != ruleKeyDuplicateInvoiceNumber || re.Severity != "error" || re.Field != "invoice_number" {
				t.Errorf("racer[%d] (loser) RowError = %+v, want the enriched duplicate shape (rule_key=%q severity=error field=invoice_number)", i, re, ruleKeyDuplicateInvoiceNumber)
			}
		default:
			t.Errorf("racer[%d] unexpected verdict (ReadyInvoices=%d QuarantinedInvoices=%d), want exactly one of (1,0)/(0,1)", i, r.ReadyInvoices, r.QuarantinedInvoices)
		}
	}
	if winners != 1 || losers != racers-1 {
		t.Fatalf("aggregate racer outcome (winners=%d losers=%d), want (1,%d)", winners, losers, racers-1)
	}
	if got := countInvoicesByNumber(t, super, entityID, raceNumber); got != 1 {
		t.Errorf("stored %s rows = %d, want exactly 1 despite %d concurrent commit attempts", raceNumber, got, racers)
	}
}

// --- QA pass: mixed collision ------------------------------------------

// QA pass (task-764): interleaves a NON-colliding pair (one document imported into two
// different entities -- no key collision, since the guarantee is entity-scoped, mirrors DUP-D7
// via document sharing instead of number sharing) with a colliding pair (two documents, same
// entity, same number) in ONE concurrent race released by the same close(start). Proves the two
// outcomes stay isolated under one shared release: the colliding pair's 1-winner/1-loser split
// does not leak into the non-colliding pair's both-succeed outcome, or vice versa.
func TestServiceImportDocument_MixedCollisionRaceIsolatesPerEntityOutcomes(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-QA-MIX tenant")
	entityA := seedEntity(t, super, tenantID, "DUP-QA-MIX entity A")
	entityB := seedEntity(t, super, tenantID, "DUP-QA-MIX entity B")

	const sharedNumber = "INV-EXTR06-MIXSHARED"
	const collideNumber = "INV-EXTR06-MIXCOLLIDE"

	sharedDoc := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, sharedDoc, docCleanValues(sharedNumber))

	collideDoc1 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, collideDoc1, docCleanValues(collideNumber))
	collideDoc2 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, collideDoc2, docCleanValues(collideNumber))

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	type mixRacer struct {
		entityID, documentID string
	}
	// 0/1 share a document across two entities (no collision); 2/3 share an entity and number
	// (collision). All four release from the same close(start) below.
	racers := []mixRacer{
		{entityA, sharedDoc},
		{entityB, sharedDoc},
		{entityA, collideDoc1},
		{entityA, collideDoc2},
	}

	results := make([]BatchResult, len(racers))
	errs := make([]error, len(racers))
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(len(racers))
	for i, r := range racers {
		go func(i int, r mixRacer) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.ImportDocument(callCtx, r.entityID, r.documentID)
		}(i, r)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer[%d] unexpected error: %v", i, err)
		}
	}

	// Racers 0/1: different entities sharing one document -- no collision, both must win.
	for i, name := range map[int]string{0: "entityA (shared doc)", 1: "entityB (shared doc)"} {
		r := results[i]
		if r.Status != "completed" || r.ReadyInvoices != 1 || r.QuarantinedInvoices != 0 {
			t.Errorf("racer[%d] (%s) = %+v, want completed/ready=1/quarantined=0 (no cross-entity collision)", i, name, r)
		}
	}

	// Racers 2/3: same entity, same number -- exactly one winner, one loser.
	winners, losers := 0, 0
	for _, i := range []int{2, 3} {
		r := results[i]
		switch {
		case r.ReadyInvoices == 1 && r.QuarantinedInvoices == 0:
			winners++
		case r.ReadyInvoices == 0 && r.QuarantinedInvoices == 1:
			losers++
			if len(r.Errors) != 1 {
				t.Fatalf("racer[%d] quarantined but len(Errors)=%d, want 1", i, len(r.Errors))
			}
			re := r.Errors[0]
			if re.RuleKey != ruleKeyDuplicateInvoiceNumber || re.Severity != "error" || re.Field != "invoice_number" {
				t.Errorf("racer[%d] (loser) RowError = %+v, want the enriched duplicate shape", i, re)
			}
		default:
			t.Errorf("racer[%d] unexpected verdict (ReadyInvoices=%d QuarantinedInvoices=%d)", i, r.ReadyInvoices, r.QuarantinedInvoices)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("collision-pair outcome (winners=%d losers=%d), want (1,1)", winners, losers)
	}

	if got := countInvoicesByNumber(t, super, entityA, sharedNumber); got != 1 {
		t.Errorf("entityA stored %s rows = %d, want 1", sharedNumber, got)
	}
	if got := countInvoicesByNumber(t, super, entityB, sharedNumber); got != 1 {
		t.Errorf("entityB stored %s rows = %d, want 1", sharedNumber, got)
	}
	if got := countInvoicesByNumber(t, super, entityA, collideNumber); got != 1 {
		t.Errorf("entityA stored %s rows = %d, want exactly 1 despite 2 concurrent commit attempts", collideNumber, got)
	}
}

// --- QA pass: distinguishable quarantine reasons under race -----------------

// QA pass (task-764): one entity, 3 racers -- two collide on a valid number (DUP-D2 shape,
// scaled down), the third carries an unreadable invoice_number (mapper quarantine, the
// pre-Create path documentCreateInput's own early return takes). All three release from the
// same close(start). The mapper-quarantined racer must never be mistaken for a duplicate loser:
// its RowError carries no RuleKey/Severity (documentCreateInput sets only Field/Message) and a
// different Message, while a duplicate loser's RowError always carries
// ruleKeyDuplicateInvoiceNumber/"error".
func TestServiceImportDocument_UnreadableNumberQuarantineStaysDistinctFromDuplicateQuarantineUnderRace(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-QA-DISTINCT tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-QA-DISTINCT entity")

	const dupNumber = "INV-EXTR06-DISTINCTDUP"

	dupDoc1 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, dupDoc1, docCleanValues(dupNumber))
	dupDoc2 := docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, dupDoc2, docCleanValues(dupNumber))

	unreadableDoc := docSeedDocument(t, super, tenantID)
	unreadableValues := docCleanValues("unused")
	unreadableValues["invoice_number"] = nil
	docSeedExtraction(t, super, tenantID, unreadableDoc, unreadableValues)

	svc := newTestService(app)
	callCtx := sxIdentity(ctx, tenantID)

	documentIDs := []string{dupDoc1, dupDoc2, unreadableDoc}
	results := make([]BatchResult, 3)
	errs := make([]error, 3)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	for i, docID := range documentIDs {
		go func(i int, docID string) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.ImportDocument(callCtx, entityID, docID)
		}(i, docID)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer[%d] unexpected error: %v", i, err)
		}
	}

	// Racers 0/1: exactly one winner, one duplicate loser with the enriched shape.
	winners, losers := 0, 0
	for _, i := range []int{0, 1} {
		r := results[i]
		switch {
		case r.ReadyInvoices == 1 && r.QuarantinedInvoices == 0:
			winners++
		case r.ReadyInvoices == 0 && r.QuarantinedInvoices == 1:
			losers++
			if len(r.Errors) != 1 {
				t.Fatalf("racer[%d] quarantined but len(Errors)=%d, want 1", i, len(r.Errors))
			}
			re := r.Errors[0]
			if re.RuleKey != ruleKeyDuplicateInvoiceNumber {
				t.Errorf("racer[%d] (duplicate loser) RuleKey = %q, want %q", i, re.RuleKey, ruleKeyDuplicateInvoiceNumber)
			}
			if re.Severity != "error" {
				t.Errorf("racer[%d] (duplicate loser) Severity = %q, want %q", i, re.Severity, "error")
			}
		default:
			t.Errorf("racer[%d] unexpected verdict (ReadyInvoices=%d QuarantinedInvoices=%d)", i, r.ReadyInvoices, r.QuarantinedInvoices)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("duplicate-pair outcome (winners=%d losers=%d), want (1,1)", winners, losers)
	}

	// Racer 2: mapper-quarantined for an UNRELATED reason -- must never carry the duplicate
	// guard's RuleKey/Severity/Message.
	mapperResult := results[2]
	if mapperResult.Status != "completed" || mapperResult.ReadyInvoices != 0 || mapperResult.QuarantinedInvoices != 1 {
		t.Fatalf("racer[2] (unreadable number) = %+v, want completed/ready=0/quarantined=1", mapperResult)
	}
	if len(mapperResult.Errors) != 1 {
		t.Fatalf("racer[2] len(Errors) = %d, want 1", len(mapperResult.Errors))
	}
	mre := mapperResult.Errors[0]
	if mre.RuleKey != "" {
		t.Errorf("racer[2] (mapper quarantine) RuleKey = %q, want empty -- a mapper error must never be mistaken for the duplicate guard", mre.RuleKey)
	}
	if mre.Severity != "" {
		t.Errorf("racer[2] (mapper quarantine) Severity = %q, want empty", mre.Severity)
	}
	if mre.Field != "invoice_number" {
		t.Errorf("racer[2] (mapper quarantine) Field = %q, want %q", mre.Field, "invoice_number")
	}
	// Retargeted by EXTR-15-05: racer 2's extraction is ten fields READ with a blank number,
	// so it takes the read-document branch, never the poor-scan one. The literal moved into
	// document_map_test.go's assertReadDocumentMessage.
	if mre.Message != ac2Message(t) {
		t.Errorf("racer[2] (mapper quarantine) Message = %q, want the mapper's own read-document message %q", mre.Message, ac2Message(t))
	}
	assertReadDocumentMessage(t, mre.Message)
	if mre.Message == msgDuplicateInvoiceNumber {
		t.Fatalf("racer[2] (mapper quarantine) Message equals the duplicate guard's message -- the two quarantine reasons are indistinguishable")
	}

	if got := countInvoicesByNumber(t, super, entityID, dupNumber); got != 1 {
		t.Errorf("stored %s rows = %d, want exactly 1", dupNumber, got)
	}
}
