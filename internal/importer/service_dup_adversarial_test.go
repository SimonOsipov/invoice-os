// M4-06-01 QA Mode-B: adversarial coverage added on top of the executor's
// DUP-01..07 suite (service_dup_test.go), which already proves the Go-level
// shape/semantics of an against-store duplicate's enriched RowError. This
// file targets what DUP-01..07 does NOT cover:
//
//   - the HTTP wire shape end-to-end (M4-08 consumes raw JSON, not Go
//     structs) -- a struct field renders correctly in Go but nobody has
//     proven `rule_key`/`severity` actually reach the response body with the
//     right JSON key spelling.
//   - omitempty actually OMITS the keys (not just leaves them "") for a bare
//     structural RowError, so a consumer can tell "named rule" from
//     "structural" by KEY PRESENCE, not by testing for an empty string.
//   - the race backstop's per-loser counter bookkeeping (RowsTotal/RowsValid/
//     RowsInvalid), which DUP-04 never asserts -- only ReadyInvoices/
//     QuarantinedInvoices and the error shape.
//   - that a quarantined duplicate is structurally EXCLUDED from the set
//     ValidateBatch ever sees (not merely "the canned gate output happens to
//     match") -- verified via fakeGate's recorded call args, not its
//     configured return value.
//   - a non-contiguous, >2-row colliding group still cites every one of its
//     sheet rows via sheetRows, sorted.
//   - the true-negative case: a file with zero against-store collisions
//     never emits a rule_key=="no-duplicate-invoice-number" entry, even
//     alongside an unrelated structural quarantine in the same file.
//
// Spec-to-test map:
//
//	ADV-DUP-01 TestRowError_OmitemptyDistinguishesRuleShapedFromStructural (unit, non-DB)
//	ADV-DUP-02 TestCreateHandler_StoreDuplicateWireShapeAndStructuralOmitsRuleFields
//	ADV-DUP-03 TestServiceImport_RaceBackstopLoserCountersIncrementByExactlyOne
//	ADV-DUP-04 TestServiceImport_StoreDuplicateNeverReachesGateEvaluation
//	ADV-DUP-05 TestServiceImport_NonContiguousMultiRowDuplicateGroupCitesAllRowsSorted
//	ADV-DUP-06 TestServiceImport_NoStoreDuplicateNoRuleKeyEntriesEvenAlongsideStructuralQuarantine
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -v -run 'TestRowError_Omitempty|TestCreateHandler_StoreDuplicate|TestServiceImport_(RaceBackstop|StoreDuplicateNeverReaches|NonContiguousMultiRow|NoStoreDuplicate)' ./internal/importer/...
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- ADV-DUP-01 (unit, non-DB) ------------------------------------------

// ADV-DUP-01: marshal a rule-shaped RowError (storeDuplicateRowError) and a
// BARE structural one (mirrors what headerConflictField/bestEffortBadNumericField
// actually emit -- RuleKey/Severity left at their Go zero value "") side by
// side, decoding each into a generic key-presence map rather than back into
// RowError -- decoding into RowError would hide the very thing under test,
// since json.Unmarshal leaves a missing field at its zero value ("")
// INDISTINGUISHABLE from a present-but-empty one. omitempty is only proven by
// checking the KEY is absent, not that the decoded value is "".
func TestRowError_OmitemptyDistinguishesRuleShapedFromStructural(t *testing.T) {
	ruleShaped := storeDuplicateRowError([]int{0, 1})
	bareStructural := RowError{
		Rows:    sheetRows([]int{4, 6}),
		Field:   "total",
		Message: "rows disagree on total",
	}

	for name, re := range map[string]RowError{"rule-shaped": ruleShaped, "bare-structural": bareStructural} {
		b, err := json.Marshal(re)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		var generic map[string]json.RawMessage
		if err := json.Unmarshal(b, &generic); err != nil {
			t.Fatalf("%s: unmarshal into generic map: %v", name, err)
		}

		_, hasRuleKey := generic["rule_key"]
		_, hasSeverity := generic["severity"]

		switch name {
		case "rule-shaped":
			if !hasRuleKey {
				t.Errorf("%s: %s missing key %q entirely, want present", name, string(b), "rule_key")
			}
			if !hasSeverity {
				t.Errorf("%s: %s missing key %q entirely, want present", name, string(b), "severity")
			}
		case "bare-structural":
			if hasRuleKey {
				t.Errorf("%s: %s carries key %q, want ABSENT (omitempty) -- a consumer must be able to tell structural from rule-shaped by key presence alone", name, string(b), "rule_key")
			}
			if hasSeverity {
				t.Errorf("%s: %s carries key %q, want ABSENT (omitempty)", name, string(b), "severity")
			}
		}
	}
}

// --- ADV-DUP-02 -----------------------------------------------------------

// ADV-DUP-02: drives the REAL HTTP handler (POST /v1/imports) with a 3-group
// file -- an against-store duplicate (INV-HTTP-DUP, pre-seeded), a
// structurally-quarantined group (INV-HTTP-BADTOTAL, non-numeric total ->
// bestEffortBadNumericField catches it in classify, never reaches
// storeDuplicateRowError), and a clean group (INV-HTTP-CLEAN) -- and asserts
// the raw response BYTES literally contain `"rule_key":"no-duplicate-invoice-number"`,
// `"severity":"error"`, `"field":"invoice_number"` for the duplicate entry
// (proving JSON serialization end-to-end, not just the Go struct M4-08 will
// never see), while the structural entry's generic-decoded map has NEITHER
// key present at all.
func TestCreateHandler_StoreDuplicateWireShapeAndStructuralOmitsRuleFields(t *testing.T) {
	super, app := dbTestPools(t)
	svc := NewService(NewStore(app), invoice.NewStore(app), &fakeGate{})

	tenantID := seedTenant(t, super, "ADV-DUP-02 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-DUP-02 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-HTTP-DUP")
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}

	rows := [][]string{
		mkRow("INV-HTTP-DUP", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "DupItem", "1", "10.00"),     // sheet 2
		mkRow("INV-HTTP-BADTOTAL", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "N/A", "BadItem", "1", "20.00"),  // sheet 3 -- non-numeric total
		mkRow("INV-HTTP-CLEAN", "2026-01-12", "T3", "B3", "NGN", "30.00", "3.00", "33.00", "CleanItem", "1", "30.00"), // sheet 4
	}

	mappingJSON, err := json.Marshal(stdMapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType := buildMultipartBody(t, entityID, string(mappingJSON), "data.csv", "", csvBody(t, stdHeader, rows))
	rec, resp := doImportCreate(t, svc.Import, &id, "", contentType, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.ReadyInvoices != 1 || resp.QuarantinedInvoices != 2 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,2)", resp.ReadyInvoices, resp.QuarantinedInvoices)
	}

	raw := rec.Body.String()
	for _, want := range []string{
		`"rule_key":"no-duplicate-invoice-number"`,
		`"severity":"error"`,
		`"field":"invoice_number"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("response body does not literally contain %s -- wire shape must carry it verbatim for M4-08 to consume: %s", want, raw)
		}
	}

	// Decode into a generic key-presence view to prove the STRUCTURAL entry
	// (INV-HTTP-BADTOTAL) omits rule_key/severity entirely -- not merely
	// renders them "".
	var generic struct {
		Errors []map[string]json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &generic); err != nil {
		t.Fatalf("decode generic response: %v", err)
	}
	if len(generic.Errors) != 2 {
		t.Fatalf("generic errors[] length = %d, want 2: %s", len(generic.Errors), raw)
	}
	var foundStructural bool
	for _, e := range generic.Errors {
		var field string
		if raw, ok := e["field"]; ok {
			_ = json.Unmarshal(raw, &field)
		}
		if field != "total" {
			continue
		}
		foundStructural = true
		if _, ok := e["rule_key"]; ok {
			t.Errorf("structural (field=%q) errors[] entry carries key %q, want ABSENT: %v", field, "rule_key", e)
		}
		if _, ok := e["severity"]; ok {
			t.Errorf("structural (field=%q) errors[] entry carries key %q, want ABSENT: %v", field, "severity", e)
		}
	}
	if !foundStructural {
		t.Fatalf("no structural (field==%q) entry found in errors[]: %s", "total", raw)
	}
}

// --- ADV-DUP-03 -----------------------------------------------------------

// ADV-DUP-03: reproduces DUP-04's race-backstop mechanism (N concurrent
// Import calls for the SAME never-before-seen invoice_number, so exactly one
// wins and the rest hit the racing-INSERT backstop), but asserts what DUP-04
// never checks -- the LOSER's per-run RowsTotal/RowsValid/RowsInvalid
// counters increment by EXACTLY the one row it contributed (no double
// counting from the redundant errors.Is(createErr, ErrDuplicateNumber) check
// inside BOTH domainCreateErrorMessage and the outer branch, service.go) --
// plus that the aggregate error-entry count across every losing result is
// EXACTLY racers-1 (never more: proves the backstop appends once per group,
// not once per classification branch it happens to satisfy).
func TestServiceImport_RaceBackstopLoserCountersIncrementByExactlyOne(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-DUP-03 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-DUP-03 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	const racers = 5
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
				mkRow("INV-ADV3-RACE", "2026-01-10", "TIN-RACE", "Racer", "NGN", "100.00", "0.00", "100.00", fmt.Sprintf("RaceItem%d", i), "1", "100.00"),
			}
			results[i], errs[i] = svc.Import(c, entityID, stdMapping, stdHeader, raceRow, false)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Import[%d] unexpected top-level error: %v", i, err)
		}
	}

	totalLoserErrorEntries := 0
	losers := 0
	for i := 0; i < racers; i++ {
		r := results[i]
		if r.ReadyInvoices == 1 {
			continue // the winner -- not under test here
		}
		losers++
		if r.RowsTotal != 1 || r.RowsValid != 0 || r.RowsInvalid != 1 {
			t.Errorf("racer[%d] (loser) counters = (total=%d valid=%d invalid=%d), want (1,0,1) -- exactly one row, exactly once", i, r.RowsTotal, r.RowsValid, r.RowsInvalid)
		}
		if r.QuarantinedInvoices != 1 {
			t.Errorf("racer[%d] (loser) QuarantinedInvoices = %d, want 1", i, r.QuarantinedInvoices)
		}
		if len(r.Errors) != 1 {
			t.Errorf("racer[%d] (loser) len(Errors) = %d, want exactly 1 (no double-append across the two errors.Is(ErrDuplicateNumber) checks in service.go): %+v", i, len(r.Errors), r.Errors)
		}
		totalLoserErrorEntries += len(r.Errors)
	}
	if losers != racers-1 {
		t.Fatalf("losers = %d, want %d", losers, racers-1)
	}
	if totalLoserErrorEntries != racers-1 {
		t.Errorf("aggregate error entries across all losers = %d, want exactly %d (one per loser, no extras)", totalLoserErrorEntries, racers-1)
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-ADV3-RACE"); got != 1 {
		t.Errorf("stored INV-ADV3-RACE rows = %d, want exactly 1", got)
	}
}

// --- ADV-DUP-04 -----------------------------------------------------------

// ADV-DUP-04: a pre-seeded against-store duplicate (INV-ADV4-DUP) alongside
// one clean ready group (INV-ADV4-CLEAN) -- asserts, via fakeGate's RECORDED
// call args (not its configured return value, which would pass vacuously
// regardless of what was actually sent -- see contentViolationGate's own doc
// in service_dup_test.go for why a static canned result can't prove this),
// that ValidateBatch is invoked with EXACTLY the one non-duplicate invoice:
// the quarantined duplicate never becomes part of `created` and therefore
// never reaches gate evaluation at all -- it is not merely absent from the
// reported clean/violation counts, it was structurally never a candidate.
// Also ties the duplicate's Severity string directly to
// invoice.HasBlockingViolation's own blocking vocabulary, proving "error" is
// not just a message string that happens to read as blocking but the EXACT
// value the promotion predicate treats as such.
func TestServiceImport_StoreDuplicateNeverReachesGateEvaluation(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-DUP-04 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-DUP-04 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-ADV4-DUP")

	fg := &fakeGate{
		validateBatchResult: invoice.BatchOutcome{
			RuleSetVersion:   1,
			RuleSetVersionID: uuid.NewString(),
			Clean:            1,
			WithViolations:   0,
			ByID:             map[string][]invoice.Violation{},
		},
	}
	svc := newTestServiceWithGate(app, fg)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-ADV4-DUP", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "DupItem", "1", "10.00"),     // sheet 2
		mkRow("INV-ADV4-CLEAN", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "CleanItem", "1", "20.00"), // sheet 3
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (1,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}

	if fg.validateBatchCalls != 1 {
		t.Fatalf("ValidateBatch calls = %d, want 1", fg.validateBatchCalls)
	}
	if len(fg.validateBatchInvs) != 1 {
		t.Fatalf("ValidateBatch was called with %d invoices, want exactly 1 (the duplicate must never be a candidate for evaluation): %+v", len(fg.validateBatchInvs), fg.validateBatchInvs)
	}
	if got := fg.validateBatchInvs[0].InvoiceNumber; got != "INV-ADV4-CLEAN" {
		t.Errorf("the single invoice passed to ValidateBatch is %q, want %q -- the duplicate must not have reached gate evaluation", got, "INV-ADV4-CLEAN")
	}

	if res.InvoicesClean != 1 || res.InvoicesWithViolations != 0 {
		t.Errorf("(InvoicesClean=%d InvoicesWithViolations=%d), want (1,0)", res.InvoicesClean, res.InvoicesWithViolations)
	}
	if len(res.InvoiceViolations) != 0 {
		t.Errorf("InvoiceViolations = %+v, want empty", res.InvoiceViolations)
	}

	re, found := findRowErrorWithRows(res.Errors, []int{2})
	if !found {
		t.Fatalf("no RowError with Rows==[2] in %+v", res.Errors)
	}
	if re.Severity != "error" {
		t.Fatalf("duplicate Severity = %q, want %q", re.Severity, "error")
	}
	if !invoice.HasBlockingViolation([]invoice.Violation{{Severity: re.Severity}}) {
		t.Errorf("RowError.Severity %q is not recognized as blocking by invoice.HasBlockingViolation -- the duplicate's severity vocabulary must agree with the promotion predicate's own", re.Severity)
	}
}

// --- ADV-DUP-05 -----------------------------------------------------------

// ADV-DUP-05: a pre-seeded duplicate whose group spans FOUR non-contiguous
// sheet rows (interleaved with three OTHER, distinct invoice numbers) --
// the enriched entry's Rows must list ALL FOUR sheet rows, sorted ascending
// (sheetRows' contract), not merely the first/last, and not in file-visit
// order.
func TestServiceImport_NonContiguousMultiRowDuplicateGroupCitesAllRowsSorted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-DUP-05 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-DUP-05 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-ADV5-DUP")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-ADV5-DUP", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "DupA", "1", "10.00"),      // sheet 2
		mkRow("INV-ADV5-OTHER1", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "Other1", "1", "20.00"), // sheet 3
		mkRow("INV-ADV5-DUP", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "DupB", "1", "10.00"),      // sheet 4
		mkRow("INV-ADV5-OTHER2", "2026-01-12", "T3", "B3", "NGN", "30.00", "3.00", "33.00", "Other2", "1", "30.00"), // sheet 5
		mkRow("INV-ADV5-DUP", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "DupC", "1", "10.00"),      // sheet 6
		mkRow("INV-ADV5-OTHER3", "2026-01-13", "T4", "B4", "NGN", "40.00", "4.00", "44.00", "Other3", "1", "40.00"), // sheet 7
		mkRow("INV-ADV5-DUP", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "DupD", "1", "10.00"),      // sheet 8
	}

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 3 || res.QuarantinedInvoices != 1 {
		t.Fatalf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (3,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(res.Errors), res.Errors)
	}
	re := res.Errors[0]
	wantRows := []int{2, 4, 6, 8}
	if !intSliceEqual(re.Rows, wantRows) {
		t.Errorf("Rows = %v, want %v (all four non-contiguous sheet rows, sorted ascending)", re.Rows, wantRows)
	}
	if re.RuleKey != ruleKeyDuplicateInvoiceNumber {
		t.Errorf("RuleKey = %q, want %q", re.RuleKey, ruleKeyDuplicateInvoiceNumber)
	}
	if re.Severity != "error" {
		t.Errorf("Severity = %q, want %q", re.Severity, "error")
	}

	if got := countInvoicesByNumber(t, super, entityID, "INV-ADV5-DUP"); got != 1 {
		t.Errorf("INV-ADV5-DUP rows = %d, want exactly 1 (the pre-seeded one)", got)
	}
	for _, n := range []string{"INV-ADV5-OTHER1", "INV-ADV5-OTHER2", "INV-ADV5-OTHER3"} {
		if got := countInvoicesByNumber(t, super, entityID, n); got != 1 {
			t.Errorf("%s rows = %d, want 1 (committed)", n, got)
		}
	}
}

// --- ADV-DUP-06 -----------------------------------------------------------

// ADV-DUP-06: a file with NO against-store collision at all -- one clean
// group plus one UNRELATED structural quarantine (a header-field conflict,
// exercising a totally different classify branch) -- must produce ZERO
// errors[] entries carrying RuleKey=="no-duplicate-invoice-number" (false
// positives). Guards against the rule key ever being applied indiscriminately
// to whatever the first/only errors[] entry happens to be.
func TestServiceImport_NoStoreDuplicateNoRuleKeyEntriesEvenAlongsideStructuralQuarantine(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ADV-DUP-06 tenant")
	entityID := seedEntity(t, super, tenantID, "ADV-DUP-06 entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-ADV6-CLEAN", "2026-01-10", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),    // sheet 2
		mkRow("INV-ADV6-CONFLICT", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "22.00", "ItemA", "1", "20.00"), // sheet 3
		mkRow("INV-ADV6-CONFLICT", "2026-01-11", "T2", "B2", "NGN", "20.00", "2.00", "99.00", "ItemB", "1", "20.00"), // sheet 4 -- total conflict, no duplicate involved
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
	for _, re := range res.Errors {
		if re.RuleKey == ruleKeyDuplicateInvoiceNumber {
			t.Errorf("false positive: RowError %+v carries RuleKey %q, but this file has NO against-store collision", re, ruleKeyDuplicateInvoiceNumber)
		}
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-ADV6-CLEAN"); got != 1 {
		t.Errorf("INV-ADV6-CLEAN rows = %d, want 1 (committed)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-ADV6-CONFLICT"); got != 0 {
		t.Errorf("INV-ADV6-CONFLICT rows = %d, want 0 (quarantined for header conflict, not duplicate)", got)
	}
}
