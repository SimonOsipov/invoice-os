// M4-06-01 -- QA Mode-A RED specs: an against-store `(entity,
// invoice_number)` collision must report as a first-class, RULE-SHAPED
// violation (RuleKey "no-duplicate-invoice-number", Severity "error", a
// human-readable Message) in BatchResult.Errors, instead of the pre-M4-06
// bare RowError{Message:"already imported"} shape. This holds at BOTH emit
// sites -- the upfront ExistingNumbers precheck (service.go's `if
// existing[num]` branch) and the racing-INSERT backstop (the
// domainCreateErrorMessage/invoice.ErrDuplicateNumber branch) -- and must
// NOT leak onto the invoice.ErrValidation create-error path, which stays
// bare (DUP-05, a regression guard). errors[] and invoice_violations[]
// never mix (DUP-07): a duplicate is always structural (errors[]), never a
// rule-engine violation (invoice_violations[]).
//
// Written BEFORE the real implementation exists, against service.go's
// M4-06-01 compile scaffold: RowError gained RuleKey/Severity fields
// (additive), and storeDuplicateRowError exists but is NOT wired into
// either emit site -- it deliberately still returns the OLD bare shape. So
// every assertion below fails on the MISSING RuleKey/Severity (a real
// value mismatch), never on a compile error, except DUP-05 which is
// expected to already be green (nothing on the ErrValidation path was
// touched by the scaffold).
//
// Spec-to-test map (Test Specs table, M4-06-01):
//
//	DUP-01 TestStoreDuplicateRowError_ReturnsRuleShapedViolation (unit, non-DB)
//	DUP-02 TestServiceImport_StoreDuplicateReportedAsFirstClassViolation
//	DUP-03 TestServiceImport_DryRunAndRealBothReportIdenticalEnrichedDuplicate
//	DUP-04 TestServiceImport_ConcurrentDuplicateLoserReportedAsFirstClassViolation
//	DUP-05 TestServiceImport_ValidationCreateErrorRowErrorStaysBareNotRuleShaped (regression guard)
//	DUP-06 TestServiceImport_MultipleStoreDuplicatesEachEnrichedIndependently
//	DUP-07 TestServiceImport_DuplicateNeverMixesWithContentViolation
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -v -run 'TestStoreDuplicateRowError|TestServiceImport_.*(Duplicate|ValidationCreateError)' ./internal/importer/...
package importer

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- DUP-01 (unit, non-DB) --------------------------------------------

// DUP-01: storeDuplicateRowError([]int{0,1}) returns a rule-shaped
// RowError: Rows converted to 1-based sheet rows [2,3] ([sheetRows]'s
// existing contract), Field "invoice_number", RuleKey
// "no-duplicate-invoice-number", Severity "error", non-empty Message. RED
// against the scaffold: the stub returns the bare shape, so RuleKey/
// Severity are both "".
func TestStoreDuplicateRowError_ReturnsRuleShapedViolation(t *testing.T) {
	got := storeDuplicateRowError([]int{0, 1})

	wantRows := []int{2, 3}
	if !intSliceEqual(got.Rows, wantRows) {
		t.Errorf("Rows = %v, want %v", got.Rows, wantRows)
	}
	if got.Field != "invoice_number" {
		t.Errorf("Field = %q, want %q", got.Field, "invoice_number")
	}
	if got.RuleKey != ruleKeyDuplicateInvoiceNumber {
		t.Errorf("RuleKey = %q, want %q", got.RuleKey, ruleKeyDuplicateInvoiceNumber)
	}
	if got.Severity != "error" {
		t.Errorf("Severity = %q, want %q", got.Severity, "error")
	}
	if got.Message == "" {
		t.Errorf("Message is empty, want a non-empty human-readable message (e.g. %q)", msgDuplicateInvoiceNumber)
	}
}

// --- DUP-02 --------------------------------------------------------------

// DUP-02: a stored INV-DUP2 pre-seeded, then imported alongside a clean
// INV-CLEAN2B (mirrors IMP-SVC-03's shape/fixture) -- the Errors entry for
// INV-DUP2 must carry Rows (sheet rows), Field "invoice_number", RuleKey
// "no-duplicate-invoice-number", Severity "error", a non-empty Message;
// counts stay (ReadyInvoices=1, QuarantinedInvoices=1); superuser
// read-back: INV-DUP2 count stays 1 (no duplicate inserted), INV-CLEAN2B
// present. RED against the scaffold: RuleKey/Severity are both "".
func TestServiceImport_StoreDuplicateReportedAsFirstClassViolation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-02 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-02 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-DUP2")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-DUP2", "2026-01-10", "TIN-E", "Dup Co", "NGN", "150.00", "15.00", "165.00", "DupItem1", "1", "150.00"),      // sheet 2
		mkRow("INV-DUP2", "2026-01-10", "TIN-E", "Dup Co", "NGN", "150.00", "15.00", "165.00", "DupItem2", "1", "150.00"),      // sheet 3
		mkRow("INV-CLEAN2B", "2026-01-12", "TIN-F", "Clean2 Co", "NGN", "80.00", "8.00", "88.00", "CleanItem", "1", "80.00"),   // sheet 4
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	re, found := findRowErrorWithRows(res.Errors, []int{2, 3})
	if !found {
		t.Fatalf("no RowError with Rows==[2,3] in %+v", res.Errors)
	}
	if re.Field != "invoice_number" {
		t.Errorf("Field = %q, want %q", re.Field, "invoice_number")
	}
	if re.RuleKey != ruleKeyDuplicateInvoiceNumber {
		t.Errorf("RuleKey = %q, want %q -- a store-level duplicate must be a first-class rule violation (M4-06-01 AC)", re.RuleKey, ruleKeyDuplicateInvoiceNumber)
	}
	if re.Severity != "error" {
		t.Errorf("Severity = %q, want %q", re.Severity, "error")
	}
	if re.Message == "" {
		t.Errorf("Message is empty, want %q", msgDuplicateInvoiceNumber)
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP2"); got != 1 {
		t.Errorf("INV-DUP2 rows = %d, want exactly 1 (the pre-seeded one, no duplicate inserted)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-CLEAN2B"); got != 1 {
		t.Errorf("INV-CLEAN2B rows = %d, want 1 (committed)", got)
	}
}

// --- DUP-03 --------------------------------------------------------------

// dup03Fixture is DUP-03's file: an against-store duplicate (INV-DUP3, 2
// rows) plus a clean singleton (INV-CLEAN3B). A fresh slice per call
// (mirrors IMP-SVC-06/07's mixedFileFixture convention) so dry-run and real
// runs each get an untouched copy.
func dup03Fixture() [][]string {
	return [][]string{
		mkRow("INV-DUP3", "2026-01-10", "TIN-E", "Dup Co", "NGN", "150.00", "15.00", "165.00", "DupItem1", "1", "150.00"), // sheet 2
		mkRow("INV-DUP3", "2026-01-10", "TIN-E", "Dup Co", "NGN", "150.00", "15.00", "165.00", "DupItem2", "1", "150.00"), // sheet 3
		mkRow("INV-CLEAN3B", "2026-01-12", "TIN-F", "Clean Co", "NGN", "80.00", "8.00", "88.00", "CleanItem", "1", "80.00"), // sheet 4
	}
}

// DUP-03: the SAME file run dryRun=true then dryRun=false -- BOTH results'
// Errors contain the IDENTICAL enriched INV-DUP3 entry (RuleKey/Severity/
// Field/Message all match between dry and real); the dry-run persists
// nothing (superuser invoice count unchanged after it). RED against the
// scaffold: both entries carry empty RuleKey/Severity (identical to each
// other, but not to the wanted enriched shape).
func TestServiceImport_DryRunAndRealBothReportIdenticalEnrichedDuplicate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-03 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-03 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-DUP3")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	beforeCount := countInvoicesForEntity(t, super, entityID)

	dryRes, err := svc.Import(c, entityID, stdMapping, stdHeader, dup03Fixture(), true)
	if err != nil {
		t.Fatalf("Import (dry-run): %v", err)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != beforeCount {
		t.Fatalf("dry-run persisted invoices: before=%d after=%d, want unchanged", beforeCount, got)
	}

	realRes, err := svc.Import(c, entityID, stdMapping, stdHeader, dup03Fixture(), false)
	if err != nil {
		t.Fatalf("Import (real): %v", err)
	}

	dryRE, foundDry := findRowErrorWithRows(dryRes.Errors, []int{2, 3})
	realRE, foundReal := findRowErrorWithRows(realRes.Errors, []int{2, 3})
	if !foundDry || !foundReal {
		t.Fatalf("expected a RowError with Rows==[2,3] in BOTH results; dry found=%v real found=%v (dry=%+v real=%+v)", foundDry, foundReal, dryRes.Errors, realRes.Errors)
	}

	for name, re := range map[string]RowError{"dry-run": dryRE, "real": realRE} {
		if re.RuleKey != ruleKeyDuplicateInvoiceNumber {
			t.Errorf("%s RuleKey = %q, want %q", name, re.RuleKey, ruleKeyDuplicateInvoiceNumber)
		}
		if re.Severity != "error" {
			t.Errorf("%s Severity = %q, want %q", name, re.Severity, "error")
		}
		if re.Field != "invoice_number" {
			t.Errorf("%s Field = %q, want %q", name, re.Field, "invoice_number")
		}
	}
	if dryRE.RuleKey != realRE.RuleKey || dryRE.Severity != realRE.Severity || dryRE.Field != realRE.Field || dryRE.Message != realRE.Message {
		t.Errorf("dry-run and real duplicate RowErrors differ: dry=%+v real=%+v, want byte-identical enriched entries", dryRE, realRE)
	}
}

// --- DUP-04 --------------------------------------------------------------

// DUP-04: reproduces IMP-SVC-12's concurrent-race mechanism (racers
// concurrent Import calls for the SAME never-before-seen invoice_number,
// all racing past the ExistingNumbers precheck simultaneously so exactly
// one wins the Create and the rest hit the racing-INSERT backstop) -- every
// LOSER's RowError must be the ENRICHED form (RuleKey/Severity/Field set),
// not the bare string, not an empty Field. RED against the scaffold: every
// loser's RowError carries empty RuleKey/Severity (the backstop emit site
// is not wired to storeDuplicateRowError either).
func TestServiceImport_ConcurrentDuplicateLoserReportedAsFirstClassViolation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-04 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-04 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const racers = 4
	results := make([]BatchResult, racers)
	errs := make([]error, racers)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			raceRow := [][]string{
				mkRow("INV-DUP4-RACE", "2026-01-10", "TIN-RACE", "Racer", "NGN", "100.00", "0.00", "100.00", fmt.Sprintf("RaceItem%d", i), "1", "100.00"),
			}
			results[i], errs[i] = svc.Import(c, entityID, stdMapping, stdHeader, raceRow, false)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Import[%d] unexpected top-level error: %v (per-group Create errors must be captured as RowErrors, not returned)", i, err)
		}
	}

	winners, losers := 0, 0
	for i := 0; i < racers; i++ {
		r := results[i]
		if r.Status != "completed" {
			t.Errorf("racer[%d].Status = %q, want %q (a losing race is still a completed run, never failed)", i, r.Status, "completed")
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
			if re.RuleKey != ruleKeyDuplicateInvoiceNumber {
				t.Errorf("racer[%d] (loser) RowError.RuleKey = %q, want %q -- the race backstop must ALSO enrich, not just the precheck", i, re.RuleKey, ruleKeyDuplicateInvoiceNumber)
			}
			if re.Severity != "error" {
				t.Errorf("racer[%d] (loser) RowError.Severity = %q, want %q", i, re.Severity, "error")
			}
			if re.Field == "" {
				t.Errorf("racer[%d] (loser) RowError.Field is empty, want non-empty (not the bare pre-M4-06 shape)", i)
			}
		default:
			t.Errorf("racer[%d] unexpected verdict (ReadyInvoices=%d QuarantinedInvoices=%d), want exactly one of (1,0)/(0,1)", i, r.ReadyInvoices, r.QuarantinedInvoices)
		}
	}
	if winners != 1 || losers != racers-1 {
		t.Fatalf("aggregate racer outcome (winners=%d losers=%d), want (1,%d) -- exactly one of the %d concurrent imports must win", winners, losers, racers-1, racers)
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP4-RACE"); got != 1 {
		t.Errorf("stored INV-DUP4-RACE rows = %d, want exactly 1 despite %d concurrent commit attempts", got, racers)
	}
}

// --- DUP-05 (regression guard) --------------------------------------------

// DUP-05: a group that fails Store.Create with invoice.ErrValidation
// (mirrors IMP-SVC-15's "N/A" non-numeric total, which trips Postgres's
// 22P02 on the ::numeric cast) yields a RowError with EMPTY RuleKey and
// EMPTY Severity -- ONLY the duplicate path is rule-shaped; a genuinely
// invalid VALUE is a structural error, not a rule violation. This is a
// regression guard, EXPECTED GREEN from the start at Mode-A time: the
// scaffold does not touch the ErrValidation branch at all (only
// storeDuplicateRowError/the two duplicate emit sites are in scope for
// M4-06-01), so RuleKey/Severity are already "" on this path today.
func TestServiceImport_ValidationCreateErrorRowErrorStaysBareNotRuleShaped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-05 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-05 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-DUP5-BADTOTAL", "2026-01-10", "T1", "B1", "NGN", "500.00", "0.00", "N/A", "BadItem", "1", "500.00"), // sheet 2
		mkRow("INV-DUP5-CLEAN", "2026-01-11", "T2", "B2", "NGN", "80.00", "8.00", "88.00", "CleanItem", "1", "80.00"),  // sheet 3
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
	}
	re := res.Errors[0]
	if re.RuleKey != "" {
		t.Errorf("RuleKey = %q, want empty -- only the duplicate path is rule-shaped; an ErrValidation create-error must stay bare", re.RuleKey)
	}
	if re.Severity != "" {
		t.Errorf("Severity = %q, want empty", re.Severity)
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP5-BADTOTAL"); got != 0 {
		t.Errorf("INV-DUP5-BADTOTAL persisted = %d, want 0", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP5-CLEAN"); got != 1 {
		t.Errorf("INV-DUP5-CLEAN persisted = %d, want 1", got)
	}
}

// --- DUP-06 --------------------------------------------------------------

// DUP-06: two independent pre-seeded duplicates (INV-DUP6-A, INV-DUP6-B) in
// one file alongside a clean INV-DUP6-C -- TWO enriched duplicate Errors
// entries, each with its OWN Rows and the SAME RuleKey, and INV-DUP6-C
// commits. RED against the scaffold: both entries carry empty RuleKey/
// Severity.
func TestServiceImport_MultipleStoreDuplicatesEachEnrichedIndependently(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-06 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-06 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-DUP6-A")
	seedInvoice(t, super, tenantID, entityID, "INV-DUP6-B")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-DUP6-A", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "ItemA", "1", "10.00"), // sheet 2
		mkRow("INV-DUP6-B", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "ItemB", "1", "20.00"), // sheet 3
		mkRow("INV-DUP6-C", "2026-01-12", "T3", "B3", "NGN", "30.00", "3.00", "33.00", "ItemC", "1", "30.00"), // sheet 4
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 2 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,2)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 2 {
		t.Fatalf("len(Errors) = %d, want 2: %+v", len(res.Errors), res.Errors)
	}

	reA, foundA := findRowErrorWithRows(res.Errors, []int{2})
	reB, foundB := findRowErrorWithRows(res.Errors, []int{3})
	if !foundA || !foundB {
		t.Fatalf("expected distinct RowErrors for Rows==[2] (INV-DUP6-A) and Rows==[3] (INV-DUP6-B), got %+v", res.Errors)
	}
	for name, re := range map[string]RowError{"INV-DUP6-A": reA, "INV-DUP6-B": reB} {
		if re.RuleKey != ruleKeyDuplicateInvoiceNumber {
			t.Errorf("%s RuleKey = %q, want %q", name, re.RuleKey, ruleKeyDuplicateInvoiceNumber)
		}
		if re.Severity != "error" {
			t.Errorf("%s Severity = %q, want %q", name, re.Severity, "error")
		}
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP6-C"); got != 1 {
		t.Errorf("INV-DUP6-C rows = %d, want 1 (committed)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP6-A"); got != 1 {
		t.Errorf("INV-DUP6-A rows = %d, want exactly 1 (the pre-seeded one, no duplicate inserted)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP6-B"); got != 1 {
		t.Errorf("INV-DUP6-B rows = %d, want exactly 1 (the pre-seeded one, no duplicate inserted)", got)
	}
}

// --- DUP-07 --------------------------------------------------------------

// contentViolationGate is DUP-07's recording/fake gate double. Unlike
// fakeGate (service_gate_test.go), which returns one STATIC BatchOutcome
// regardless of which invoices are passed, this one must flag whichever
// created invoice carries targetNumber with a content (rule-engine)
// violation and report every OTHER created invoice clean -- the target
// invoice's ID is unknown until Create actually runs, so a static fixture
// cannot express it; ValidateBatch computes ByID from the invs it is
// handed.
type contentViolationGate struct {
	targetNumber string
}

func (g *contentViolationGate) Evaluate(ctx context.Context, items []invoice.EvalItem) (invoice.EvalResult, error) {
	return invoice.EvalResult{ByRef: map[string][]invoice.Violation{}}, nil
}

func (g *contentViolationGate) ValidateBatch(ctx context.Context, invs []invoice.Invoice) (invoice.BatchOutcome, error) {
	byID := map[string][]invoice.Violation{}
	clean, withViolations := 0, 0
	for _, inv := range invs {
		if inv.InvoiceNumber == g.targetNumber {
			byID[inv.ID] = []invoice.Violation{{
				RuleKey:  "dup-07-synthetic-content-rule",
				Severity: "warning",
				Message:  "synthetic content violation for DUP-07 (never a duplicate)",
			}}
			withViolations++
			continue
		}
		clean++
	}
	return invoice.BatchOutcome{
		RuleSetVersion:   1,
		RuleSetVersionID: uuid.NewString(),
		Clean:            clean,
		WithViolations:   withViolations,
		ByID:             byID,
	}, nil
}

// DUP-07: one pre-seeded stored duplicate (INV-DUP7, quarantined) PLUS a
// separate READY group (INV-DUP7-CONTENT) the recording gate flags with a
// content violation -- the duplicate appears ONLY in res.Errors (carrying
// RuleKey "no-duplicate-invoice-number"), the content violation appears
// ONLY in res.InvoiceViolations -- never mixed ([errors-shape] /
// [import-report-shape]). RED against the scaffold: the duplicate's
// RowError.RuleKey is empty (not yet asserting the enrichment), though the
// never-mixed structural assertions may already hold.
func TestServiceImport_DuplicateNeverMixesWithContentViolation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "DUP-07 tenant")
	entityID := seedEntity(t, super, tenantID, "DUP-07 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-DUP7")

	svc := newTestServiceWithGate(app, &contentViolationGate{targetNumber: "INV-DUP7-CONTENT"})
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-DUP7", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "DupItem", "1", "10.00"),             // sheet 2
		mkRow("INV-DUP7-CONTENT", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "ContentItem", "1", "20.00"), // sheet 3
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want exactly 1 (the duplicate only): %+v", len(res.Errors), res.Errors)
	}
	dupRE := res.Errors[0]
	if dupRE.RuleKey != ruleKeyDuplicateInvoiceNumber {
		t.Errorf("duplicate RowError.RuleKey = %q, want %q", dupRE.RuleKey, ruleKeyDuplicateInvoiceNumber)
	}
	for _, re := range res.Errors {
		if re.RuleKey == "dup-07-synthetic-content-rule" {
			t.Errorf("content violation leaked into res.Errors (structural errors and rule violations must never mix): %+v", re)
		}
	}

	if len(res.InvoiceViolations) != 1 {
		t.Fatalf("len(InvoiceViolations) = %d, want exactly 1 (the content-flagged invoice only): %+v", len(res.InvoiceViolations), res.InvoiceViolations)
	}
	iv := res.InvoiceViolations[0]
	if iv.InvoiceNumber != "INV-DUP7-CONTENT" {
		t.Errorf("InvoiceViolations[0].InvoiceNumber = %q, want %q", iv.InvoiceNumber, "INV-DUP7-CONTENT")
	}
	for _, v := range iv.Violations {
		if v.RuleKey == ruleKeyDuplicateInvoiceNumber {
			t.Errorf("duplicate rule leaked into InvoiceViolations (structural errors and rule violations must never mix): %+v", iv.Violations)
		}
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP7"); got != 1 {
		t.Errorf("INV-DUP7 rows = %d, want exactly 1 (the pre-seeded one, no duplicate inserted)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-DUP7-CONTENT"); got != 1 {
		t.Errorf("INV-DUP7-CONTENT rows = %d, want 1 (committed, even though it carries a rule violation)", got)
	}
}
