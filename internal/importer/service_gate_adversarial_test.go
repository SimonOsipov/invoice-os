// task-114 / M4-04-07 -- Stage 4 QA (Mode B): adversarial coverage added on
// top of the executor's green IMPV-01..16 (service_gate_test.go/
// handlers_gate_test.go), without modifying any of them. Three gaps the
// orchestrator's audit surfaced:
//
//   - TestServiceImport_DryRunErrUpstreamAbortsRunNoWrites: the executor's
//     own flagged deviation -- dry-run's Evaluate call erroring (an
//     unreachable 04 during a PREVIEW) had NO IMPV spec. IMPV-11 only covers
//     the REAL path's ValidateBatch error. This pins the dry-run twin: still
//     an unconditional abort, and -- unlike the real path -- there is no
//     batch to even Finalize(failed), since dry-run never reaches
//     CreateBatch.
//
//   - TestServiceImport_ReadyInvoicesCountMatchesGateReceivedCount: pins the
//     readyCount/`created` desync risk the executor itself flagged in
//     service.go's Import -- both are incremented in the SAME branch today
//     (right after a successful Store.Create), so nothing currently drifts,
//     but nothing enforces that beyond code review either. This asserts the
//     two independently-observable projections of "how many invoices were
//     actually created" agree, so a future edit that increments one without
//     the other fails loudly here instead of silently reporting a wrong
//     ReadyInvoices count or sending the gate a mismatched batch.
//
//   - TestServiceImport_LeadingZeroSubtotalDryRunOverReportsNeverUnderReportsRealClean:
//     empirically proves Stage-1 F5's named-but-deliberately-NOT-fixed
//     direction-of-error claim against the REAL v2 rule set: a leading-zero
//     subtotal makes the dry-run preview strictly MORE pessimistic than the
//     real run that follows it, never the reverse. The dangerous direction
//     (dry-run reads clean when the real run would not) is the one thing
//     this story explicitly forbids ([dry-run-evaluates], Core AC#4) -- this
//     test is the regression guard for that specific promise, not just a
//     symmetry check.
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -v -run 'TestServiceImport_DryRunErrUpstream|TestServiceImport_ReadyInvoicesCountMatchesGateReceivedCount|TestServiceImport_LeadingZeroSubtotal' ./internal/importer/...
package importer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestServiceImport_DryRunErrUpstreamAbortsRunNoWrites covers the executor's
// own flagged gap (deviation #2): dry-run's Gate.Evaluate call returning
// invoice.ErrUpstream had no IMPV spec. An unreachable 04 during a PREVIEW is
// still an outage, not "everything is clean" -- the same principle IMPV-11
// pins for the real path, but dry-run's shape differs: there is no batch id
// minted yet (CreateBatch never runs before the dry-run branch returns), so
// there is nothing to walk back to `failed`. The abort must still be
// unconditional and the write count must still be exactly zero.
func TestServiceImport_DryRunErrUpstreamAbortsRunNoWrites(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "QA dry-run ErrUpstream tenant")
	entityID := seedEntity(t, super, tenantID, "QA dry-run ErrUpstream entity")

	rows := [][]string{
		mkRow("QA-DRYUP-INV", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	fg := &fakeGate{evaluateErr: fmt.Errorf("%w: fake 04 outage", invoice.ErrUpstream)}
	svc := newTestServiceWithGate(app, fg)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, true) // dryRun=true
	if err == nil {
		t.Fatal("Import (dryRun=true): err = nil, want ErrUpstream to propagate -- an unreachable 04 during a PREVIEW is still an outage, not \"everything is clean\"")
	}
	if !errors.Is(err, invoice.ErrUpstream) {
		t.Errorf("err = %v, want it to wrap invoice.ErrUpstream", err)
	}
	if res.RuleSetVersion != nil || res.InvoicesClean != 0 || res.InvoicesWithViolations != 0 || len(res.InvoiceViolations) != 0 {
		t.Errorf("res = %+v on an aborted dry-run, want the zero-value BatchResult -- never a laundered partial preview", res)
	}

	if fg.evaluateCalls != 1 {
		t.Errorf("gate.Evaluate called %d times, want exactly 1 (it WAS attempted, then failed)", fg.evaluateCalls)
	}
	if fg.validateBatchCalls != 0 {
		t.Errorf("gate.ValidateBatch called %d times, want 0 -- dry-run never calls the writing half of the gate", fg.validateBatchCalls)
	}

	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("import_batches rows = %d, want 0 -- dry-run's Evaluate failure happens BEFORE CreateBatch is ever reached, so there is no batch to even Finalize(failed)", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("invoices rows = %d, want 0", got)
	}
}

// TestServiceImport_ReadyInvoicesCountMatchesGateReceivedCount pins the
// readyCount/`created` desync risk flagged in service.go's Import: today
// `readyCount++` and `created = append(...)` happen in the SAME branch,
// right after a successful Store.Create, so they cannot currently drift --
// but nothing beyond code review enforces that they stay that way. This
// asserts the two independently-observable projections of "how many
// invoices were actually created and handed to the gate" agree:
// BatchResult.ReadyInvoices (the M4-03 counter, reported to the caller) and
// the length of the slice the gate's ValidateBatch actually received (what
// 04 was told to judge). A future edit that increments one without the
// other -- e.g. a new early-continue in the Create loop that updates
// readyCount but forgets to append to `created`, or vice versa -- fails
// this test directly instead of silently reporting a ReadyInvoices count
// that disagrees with what was actually validated.
func TestServiceImport_ReadyInvoicesCountMatchesGateReceivedCount(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "QA readyCount tenant")
	entityID := seedEntity(t, super, tenantID, "QA readyCount entity")

	rows := [][]string{
		mkRow("QA-RC-CONFLICT", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
		mkRow("QA-RC-CONFLICT", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "99.00", "Item2", "1", "10.00"), // total disagrees -> quarantined at classify, never reaches Create
		mkRow("QA-RC-READY1", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
		mkRow("QA-RC-READY2", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
		mkRow("QA-RC-READY3", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	fg := &fakeGate{}
	svc := newTestServiceWithGate(app, fg)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 3 {
		t.Fatalf("test setup sanity: ReadyInvoices = %d, want 3 (QA-RC-CONFLICT must actually quarantine, the other three must actually create)", res.ReadyInvoices)
	}
	if fg.validateBatchCalls != 1 {
		t.Fatalf("gate.ValidateBatch called %d times, want exactly 1", fg.validateBatchCalls)
	}

	if res.ReadyInvoices != len(fg.validateBatchInvs) {
		t.Errorf("res.ReadyInvoices = %d, len(gate-received invoices) = %d -- these must never desync: every invoice Store.Create succeeds on must be BOTH counted in ReadyInvoices AND sent to the gate ([batch-of-one], IMPV-09's premise)", res.ReadyInvoices, len(fg.validateBatchInvs))
	}
}

// TestServiceImport_LeadingZeroSubtotalDryRunOverReportsNeverUnderReportsRealClean
// empirically verifies Stage-1 F5's direction-of-error claim against the
// REAL v2 rule set (not the fake gate): money written with a leading zero
// ("0100.00") survives classify (decimalNumberRe accepts it, so the group is
// READY) but fails jsonNumberRe, so the dry-run mapper carries it as a raw
// JSON STRING while the real path's Postgres round trip
// ('0100.00'::numeric::text -> "100.00") normalizes it to a bare number
// (confirmed live against the dev DB: SELECT '0100.00'::text::numeric::text
// = "100.00"). 04's toFloat (internal/validation/evaluators.go) explicitly
// documents that a string reports ok=false, and EVERY evaluator that calls
// it (rangeEval, taxMathEval, lineSumEval) maps that to a VIOLATION, never a
// silent pass -- confirmed by reading each evaluator's own source, not
// assumed.
//
// This test proves the consequence end-to-end: on ONE fixed invoice, the
// dry-run preview reports it as NOT clean while the real run that follows
// (same invoice, same entity) both reports AND stores it as clean. The
// direction is the whole point -- a dry-run that read CLEANER than the real
// run subsequently proves would be laundering a genuine failure into "this
// looks fine" in an advisory preview, which is exactly what
// [dry-run-evaluates]/Core AC#4 forbid. This fixture cannot produce that
// direction (every affected evaluator only adds violations on non-numeric
// data, never removes them), and this test is the regression guard pinning
// that it stays that way.
func TestServiceImport_LeadingZeroSubtotalDryRunOverReportsNeverUnderReportsRealClean(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "QA leading-zero tenant")
	entityID := seedEntityWithTIN(t, super, tenantID, "QA leading-zero entity", "12345678-0001")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	realGate := invoice.NewGate(invoice.NewStore(app), validator)
	svc := newTestServiceWithGate(app, realGate)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// subtotal carries a LEADING ZERO. vat/total/unit_price carry none, so
	// they round-trip identically on both paths -- the ONLY divergence this
	// fixture can produce is the one F5 names. 7.50 is exactly 7.5% of
	// 100.00 ([vat-standard-rate] would otherwise also fire on the REAL run
	// too, confounding the direction-of-error signal).
	rows := [][]string{
		mkRow("QA-LEADZERO", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "0100.00", "7.50", "107.50", "Item1", "1", "100.00"),
	}

	// Dry-run FIRST (writes nothing, so it cannot shadow the real run's
	// ExistingNumbers check that follows on the same entity/invoice_number).
	dryRes, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, true)
	if err != nil {
		t.Fatalf("Import (dryRun=true): %v", err)
	}
	if dryRes.InvoicesClean != 0 {
		t.Errorf("dry-run InvoicesClean = %d, want 0 -- the leading-zero subtotal must read as a raw string and violate on the PREVIEW (subtotal-non-negative / vat-standard-rate / line-items-sum-subtotal all resolve \"subtotal\" via toFloat)", dryRes.InvoicesClean)
	}
	if len(dryRes.InvoiceViolations) != 1 {
		t.Fatalf("dry-run InvoiceViolations = %+v, want exactly 1 entry (QA-LEADZERO)", dryRes.InvoiceViolations)
	}
	if len(dryRes.InvoiceViolations[0].Violations) == 0 {
		t.Fatalf("dry-run InvoiceViolations[0].Violations is empty, want >= 1 -- the known incompleteness [Stage-1 F5] must manifest as an OVER-report here, or this fixture no longer exercises it")
	}

	// Real SECOND: proves the invoice is genuinely, actually clean once
	// Postgres has normalized the leading zero away.
	realRes, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import (real): %v", err)
	}
	if realRes.InvoicesClean != 1 {
		t.Fatalf("real InvoicesClean = %d, want 1 -- test setup sanity: Postgres must normalize \"0100.00\" to \"100.00\" so the REAL run is genuinely clean, proving the dry-run divergence above is dry-run-only and not a real defect in this invoice", realRes.InvoicesClean)
	}
	if len(realRes.InvoiceViolations) != 0 {
		t.Errorf("real InvoiceViolations = %+v, want empty -- confirms the real run is genuinely clean", realRes.InvoiceViolations)
	}

	id := invoiceIDByNumber(t, super, entityID, "QA-LEADZERO")
	status, violations, rsvID := readInvoiceVerdict(t, super, id)
	if status != "validated" {
		t.Errorf("stored status = %q, want %q -- the real run's Postgres-normalized subtotal is genuinely valid and must promote", status, "validated")
	}
	if string(violations) != "[]" {
		t.Errorf("stored violations = %s, want [] -- the DB must agree with realRes, not with the dry-run preview's false positives", violations)
	}
	if rsvID == nil {
		t.Error("stored rule_set_version_id = nil, want stamped")
	}

	// THE DIRECTION, asserted explicitly: dry-run must never read CLEANER
	// than (or even merely as clean as, on THIS invoice which the real run
	// just proved passes) the real run. Going the other way -- dry-run
	// reporting clean while the real run then finds a problem -- is the
	// laundering [dry-run-evaluates] forbids.
	if dryRes.InvoicesClean > realRes.InvoicesClean {
		t.Fatalf("dry-run InvoicesClean(%d) > real InvoicesClean(%d) -- dry-run must never report CLEANER than the real run: that would launder a real failure into an advisory \"this looks fine\"", dryRes.InvoicesClean, realRes.InvoicesClean)
	}
}
