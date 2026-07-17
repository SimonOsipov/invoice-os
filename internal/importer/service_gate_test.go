// task-114 / M4-04-07 ("Importer: batch pre-check (real + dry-run) +
// additive report fields") -- Mode A RED specs for IMPV-01..13/16, against
// service.go's QA Mode-A structural scaffold (see that file's own comment
// block: BatchResult/NewService/the `gate` interface exist in their final
// shape, but Import() never calls s.gate and never populates the four new
// BatchResult fields -- every assertion below fails on a REAL value/status/
// count mismatch, never on a compile error). IMPV-14/15 (the HTTP layer)
// live in handlers_gate_test.go instead, next to the other handler tests.
//
// Spec-to-test map (task-114's Test Specs table + the Stage-1 addendum's
// IMPV-16):
//
//	IMPV-01 TestServiceImport_MixedFileCleanValidatedVATWrongStaysDraft
//	IMPV-02 TestServiceImport_MixedFileCountersAndRuleSetVersion
//	IMPV-03 TestServiceImport_MixedFileErrorsStayStructuralOnlyNoRuleFailureLeaks
//	IMPV-04 TestServiceImport_ConflictPlusVATWrongPlusCleanOutcomesNeverMix
//	IMPV-05 TestServiceImport_ConflictMixCountersMatchM403Exactly
//	IMPV-06 TestServiceImport_DryRunSameViolationsAsRealRunWillProduce
//	IMPV-07 TestServiceImport_DryRunWritesZeroRowsBothTables
//	IMPV-08 TestServiceImport_GateValidateBatchCalledExactlyOnceWithAllCreated
//	IMPV-09 TestServiceImport_QuarantinedInvoiceNeverReachesGate
//	IMPV-10 TestServiceImport_ApplyValidationDBFaultAbortsRunNotLaunderedIntoRowErrors
//	IMPV-11 TestServiceImport_ValidatorErrUpstreamAbortsRun
//	IMPV-12 TestServiceImport_TinLessEntityCleanFileStaysDraftWithSupplierTinRequired
//	IMPV-13 TestServiceImport_NoLineRowsMappedStaysDraftWithLineItemsRequired (see
//	        its own doc comment -- a FLAGGED, reported-not-reinterpreted spec
//	        ambiguity: this looks architecturally unreachable via the real
//	        import path as currently designed)
//	IMPV-16 TestServiceImport_AllQuarantinedBatchNullVersionNeverCallsGate
//
// IMPV-01..07/12/13/16 are DB-backed (dbTestPools). IMPV-01..07/12 stand up
// a REAL in-process 04 (internal/validation's Store.LoadActiveRuleSetGlobal
// + NewDefaultEngine + BatchValidateHandler behind S2SMiddleware, on an
// httptest.Server) against the live v2 rule set on the shared dev DB --
// mirroring internal/invoice/gate_test.go's startInProcess04 exactly
// (declared fresh here, package-private, since that helper is unexported in
// a different package). IMPV-08..11 are filed "unit" in task-114's own Test
// Specs table but still need a real DB for Store.Create's actual invoice
// writes (Service.inv is a concrete *invoice.Store, per Stage-1 F3, mirrors
// internal/invoice/gate_test.go's own GAPI-11 precedent/flag) -- only the
// GATE dependency is faked (fakeGate below), which is the whole point of
// the [Stage-1 addendum F3] seam: no real DB fault can reach ApplyValidation
// from here (M4-03's own empty-Subject trick aborts at Create, before
// ApplyValidation is ever reached), so IMPV-10/11 inject the fault via the
// fake gate instead.
//
// Run:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -v -run 'TestServiceImport_.*IMPV|MixedFile|ConflictMix|ConflictPlus|DryRun|GateValidateBatch|QuarantinedInvoiceNeverReaches|ApplyValidationDBFault|ValidatorErrUpstream|TinLessEntityCleanFile|NoLineRowsMapped|AllQuarantined' ./internal/importer/...
//
// (or simply `go test -count=1 ./internal/importer/...` to run everything.)
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

// impvS2SToken is an arbitrary in-process peer secret shared between the
// fake 04 server (S2SMiddleware) and the Validator pointed at it below --
// not a real secret, never read from env, scoped to this file's tests only
// (mirrors internal/invoice/gate_test.go's gapiS2SToken).
const impvS2SToken = "impv-qa-test-s2s-token"

// startInProcess04ForImporter stands up a REAL 04 batch-validate handler
// (real DB-backed rule-set load, real engine, real peer-auth middleware) on
// an httptest.Server -- the importer-package twin of
// internal/invoice/gate_test.go's startInProcess04 (unexported there, so
// re-declared here rather than imported).
func startInProcess04ForImporter(t *testing.T, app *pgxpool.Pool) *httptest.Server {
	t.Helper()
	vstore := validation.NewStore(app)
	eng := validation.NewDefaultEngine()
	handler := validation.S2SMiddleware(impvS2SToken)(validation.BatchValidateHandler(vstore.LoadActiveRuleSetGlobal, eng, nil))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// newTestServiceWithGate is newTestService's gate-integrated twin: every
// task-114/M4-04-07 spec needs a non-nil gate (real or fake), unlike the
// pre-existing M4-03 specs newTestService still serves.
func newTestServiceWithGate(app *pgxpool.Pool, g gate) *Service {
	return NewService(NewStore(app), invoice.NewStore(app), g)
}

// seedEntityWithTIN inserts one business_entities row WITH a tin (the
// positive complement to store_test.go's seedEntity, which leaves tin
// NULL) -- needed for IMPV-01..07's "otherwise clean" fixtures, since a
// nil supplier tin fires supplier-tin-required regardless of anything else
// (that is IMPV-12's OWN point, and must not leak into the other specs).
// Mirrors store_adversarial_test.go's inline TIN-seeding, factored into a
// reusable helper since this file needs it repeatedly.
func seedEntityWithTIN(t *testing.T, super *pgxpool.Pool, tenantID, name, tin string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, name, tin,
	).Scan(&id); err != nil {
		t.Fatalf("seed business_entities with tin: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

// readInvoiceVerdict reads back one invoice's gate-relevant columns via the
// superuser pool (bypasses RLS, out-of-band verification -- same convention
// as this package's invoiceSupplier/invoiceSubtotal helpers).
func readInvoiceVerdict(t *testing.T, super *pgxpool.Pool, invoiceID string) (status string, violations json.RawMessage, ruleSetVersionID *string) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT status, violations, rule_set_version_id FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&status, &violations, &ruleSetVersionID); err != nil {
		t.Fatalf("read invoice verdict for %s: %v", invoiceID, err)
	}
	return status, violations, ruleSetVersionID
}

// impvHasViolation reports whether vs contains ruleKey (this package's own
// copy of internal/invoice/gate_test.go's unexported hasViolation).
func impvHasViolation(vs []invoice.Violation, ruleKey string) bool {
	for _, v := range vs {
		if v.RuleKey == ruleKey {
			return true
		}
	}
	return false
}

// --- fakeGate: the Stage-1 addendum F3 seam ---------------------------

// fakeGate is a test double satisfying the importer-local `gate` interface
// (service.go) -- lets IMPV-08..11 assert call counts/items and inject an
// operational fault without a real 04 round trip or a real DB fault. Both
// methods record every call (count + last-seen args), which is enough for
// every IMPV-08..11 assertion; no test here calls either method more than
// once so "last-seen" and "the one call" are the same thing.
type fakeGate struct {
	evaluateCalls  int
	evaluateItems  []invoice.EvalItem
	evaluateResult invoice.EvalResult
	evaluateErr    error

	validateBatchCalls  int
	validateBatchInvs   []invoice.Invoice
	validateBatchResult invoice.BatchOutcome
	validateBatchErr    error
}

func (f *fakeGate) Evaluate(ctx context.Context, items []invoice.EvalItem) (invoice.EvalResult, error) {
	f.evaluateCalls++
	f.evaluateItems = items
	return f.evaluateResult, f.evaluateErr
}

func (f *fakeGate) ValidateBatch(ctx context.Context, invs []invoice.Invoice) (invoice.BatchOutcome, error) {
	f.validateBatchCalls++
	f.validateBatchInvs = invs
	return f.validateBatchResult, f.validateBatchErr
}

// --- fixtures -----------------------------------------------------------

// impvCleanFileFixture is IMPV-01's file: 2 clean invoices (verified clean
// against the real v2 rule set -- the identical content
// internal/invoice/gate_test.go's gapiValidInvoiceInput/GAPI-12 already
// proves produces zero violations, reused here rather than re-derived: 250
// = 2x100 + 1x50 [line-items-sum-subtotal], 18.75 = 7.5% of 250
// [vat-standard-rate]) plus 1 VAT-wrong invoice (VAT deliberately 1.00
// instead of 18.75, every OTHER field untouched, exactly GAPI-12's
// technique -- v1/v2 have no total==subtotal+vat cross-check, so this fires
// ONLY vat-standard-rate).
//
// F5 CONFIRMED AVOIDED: every money value here (250.00, 18.75, 268.75,
// 100.00, 7.50, 107.50, 1.00, 50.00) carries no leading zero, so dry-run's
// raw-string wire value and the real path's Postgres-normalized value are
// byte-identical -- IMPV-06 (which asserts dry-run == real) exercises the
// real [dry-run-evaluates] semantic, not the Stage-1 F5 named
// incompleteness (leading-zero money is a KNOWN, ACCEPTED divergence this
// fixture deliberately does not trip).
func impvCleanFileFixture() [][]string {
	return [][]string{
		mkRow("IMPV-CLEAN-1", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "18.75", "268.75", "Item1", "2", "100.00"), // sheet 2
		mkRow("IMPV-CLEAN-1", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "18.75", "268.75", "Item2", "1", "50.00"),  // sheet 3
		mkRow("IMPV-CLEAN-2", "2026-07-02", "87654321-0002", "Beta Ltd", "NGN", "100.00", "7.50", "107.50", "Item1", "1", "100.00"),  // sheet 4
		mkRow("IMPV-VATWRONG", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "1.00", "268.75", "Item1", "2", "100.00"), // sheet 5
		mkRow("IMPV-VATWRONG", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "1.00", "268.75", "Item2", "1", "50.00"),  // sheet 6
	}
}

// impvConflictMixFixture is IMPV-04/05's file: 1 header-conflict group
// (disagrees on total across its 2 rows, same technique as
// service_test.go's mixedFileFixture INV-CONFLICT) + 1 VAT-wrong group
// (IMPV-CLEAN-1's numbers, VAT wrong) + 1 clean group (IMPV-CLEAN-2's
// numbers).
func impvConflictMixFixture() [][]string {
	return [][]string{
		mkRow("IMPV4-CONFLICT", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"), // sheet 2
		mkRow("IMPV4-CONFLICT", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "10.00", "1.00", "99.00", "Item2", "1", "10.00"), // sheet 3 -- total disagrees -> conflict
		mkRow("IMPV4-VATWRONG", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "1.00", "268.75", "Item1", "2", "100.00"), // sheet 4
		mkRow("IMPV4-VATWRONG", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "1.00", "268.75", "Item2", "1", "50.00"),  // sheet 5
		mkRow("IMPV4-CLEAN", "2026-07-02", "87654321-0002", "Beta Ltd", "NGN", "100.00", "7.50", "107.50", "Item1", "1", "100.00"),    // sheet 6
	}
}

// impvAllQuarantinedFixture is IMPV-16's file: every row is ungroupable
// (blank invoice_number), so zero groups ever become READY -- the
// [Stage-1 addendum F2] "was anything evaluated at all" edge case.
func impvAllQuarantinedFixture() [][]string {
	return [][]string{
		mkRow("", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "100.00", "7.50", "107.50", "Item1", "1", "100.00"), // sheet 2
		mkRow("", "2026-07-02", "87654321-0002", "Beta Ltd", "NGN", "100.00", "7.50", "107.50", "Item1", "1", "100.00"), // sheet 3
	}
}

// impvNoLineHeader / impvNoLineMapping are IMPV-13's mapping: every
// line_* canonical field is deliberately OMITTED (see that test's own doc
// comment for the flagged spec ambiguity this fixture probes).
var impvNoLineHeader = []string{"Inv No", "Date", "Buyer TIN", "Buyer", "Currency", "Subtotal", "VAT", "Total"}

var impvNoLineMapping = map[string]string{
	"invoice_number": "Inv No", "issue_date": "Date", "buyer_tin": "Buyer TIN", "buyer_name": "Buyer",
	"currency": "Currency", "subtotal": "Subtotal", "vat": "VAT", "total": "Total",
}

// --- shared run helpers ---------------------------------------------------

// runIMPVCleanFile runs impvCleanFileFixture() through a REAL Service wired
// to a REAL in-process 04 (real v2 rule set), real or dry-run per dryRun.
func runIMPVCleanFile(t *testing.T, dryRun bool) (res BatchResult, super, app *pgxpool.Pool, entityID string) {
	t.Helper()
	super, app = dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "IMPV clean-file tenant")
	entityID = seedEntityWithTIN(t, super, tenantID, "IMPV clean-file entity", "12345678-0001")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	realGate := invoice.NewGate(invoice.NewStore(app), validator)
	svc := newTestServiceWithGate(app, realGate)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	var err error
	res, err = svc.Import(c, entityID, stdMapping, stdHeader, impvCleanFileFixture(), dryRun)
	if err != nil {
		t.Fatalf("Import (dryRun=%v): %v", dryRun, err)
	}
	return res, super, app, entityID
}

// runIMPVConflictMix runs impvConflictMixFixture() through a REAL Service
// wired to a REAL in-process 04, real import only (IMPV-04/05 are both
// real-only per the Test Specs table).
func runIMPVConflictMix(t *testing.T) (res BatchResult, super, app *pgxpool.Pool, entityID string) {
	t.Helper()
	super, app = dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "IMPV conflict-mix tenant")
	entityID = seedEntityWithTIN(t, super, tenantID, "IMPV conflict-mix entity", "12345678-0001")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	realGate := invoice.NewGate(invoice.NewStore(app), validator)
	svc := newTestServiceWithGate(app, realGate)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	var err error
	res, err = svc.Import(c, entityID, stdMapping, stdHeader, impvConflictMixFixture(), false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return res, super, app, entityID
}

// --- IMPV-01/02/03: real import, the clean+VAT-wrong file -----------------

// TestServiceImport_MixedFileCleanValidatedVATWrongStaysDraft (IMPV-01): the
// 2 clean invoices land validated (violations [], version stamped); the
// VAT-wrong one stays draft carrying vat-standard-rate -- one gate, same as
// the manual path (Core AC #2/#4). RED against the scaffold: Import() never
// calls s.gate at all, so every invoice stays draft with violations still
// '[]' (the column DEFAULT) -- the clean invoices' status assertion fails
// (got draft, want validated), and the VAT-wrong invoice's violation
// assertion fails (got none, want vat-standard-rate).
func TestServiceImport_MixedFileCleanValidatedVATWrongStaysDraft(t *testing.T) {
	_, super, _, entityID := runIMPVCleanFile(t, false)

	for _, num := range []string{"IMPV-CLEAN-1", "IMPV-CLEAN-2"} {
		id := invoiceIDByNumber(t, super, entityID, num)
		status, violations, rsvID := readInvoiceVerdict(t, super, id)
		if status != "validated" {
			t.Errorf("%s status = %q, want %q -- a clean invoice must promote (one gate, same as the manual path)", num, status, "validated")
		}
		if string(violations) != "[]" {
			t.Errorf("%s violations = %s, want []", num, violations)
		}
		if rsvID == nil {
			t.Errorf("%s rule_set_version_id = nil, want stamped", num)
		}
	}

	id := invoiceIDByNumber(t, super, entityID, "IMPV-VATWRONG")
	status, violations, rsvID := readInvoiceVerdict(t, super, id)
	if status != "draft" {
		t.Errorf("IMPV-VATWRONG status = %q, want %q -- a blocking violation must not promote", status, "draft")
	}
	var vs []invoice.Violation
	if err := json.Unmarshal(violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", violations, err)
	}
	if !impvHasViolation(vs, "vat-standard-rate") {
		t.Errorf("IMPV-VATWRONG violations = %+v, want one naming vat-standard-rate", vs)
	}
	if rsvID == nil {
		t.Error("IMPV-VATWRONG rule_set_version_id = nil, want stamped even on a blocking verdict")
	}
}

// TestServiceImport_MixedFileCountersAndRuleSetVersion (IMPV-02):
// invoices_clean==2, invoices_with_violations==1, rule_set_version==2
// (deref'd -- [Stage-1 F7]). RED against the scaffold: all three new
// BatchResult fields stay at their Go zero value (0, 0, nil) since Import()
// never populates them.
func TestServiceImport_MixedFileCountersAndRuleSetVersion(t *testing.T) {
	res, _, _, _ := runIMPVCleanFile(t, false)

	if res.InvoicesClean != 2 {
		t.Errorf("InvoicesClean = %d, want 2", res.InvoicesClean)
	}
	if res.InvoicesWithViolations != 1 {
		t.Errorf("InvoicesWithViolations = %d, want 1", res.InvoicesWithViolations)
	}
	if res.RuleSetVersion == nil {
		t.Fatal("RuleSetVersion = nil, want a pointer to 2 -- something WAS evaluated on this non-empty batch")
	}
	if *res.RuleSetVersion != 2 {
		t.Errorf("*RuleSetVersion = %d, want 2", *res.RuleSetVersion)
	}
	// Cheap invariant, no dedicated spec ID ([Stage-1 F7] folds it into
	// IMPV-02): on the real path every ready invoice is either clean or
	// violating, never neither/both.
	if res.InvoicesClean+res.InvoicesWithViolations != res.ReadyInvoices {
		t.Errorf("InvoicesClean(%d)+InvoicesWithViolations(%d) = %d, want ReadyInvoices %d",
			res.InvoicesClean, res.InvoicesWithViolations, res.InvoicesClean+res.InvoicesWithViolations, res.ReadyInvoices)
	}
}

// TestServiceImport_MixedFileErrorsStayStructuralOnlyNoRuleFailureLeaks
// (IMPV-03): errors is empty -- a rule failure is not a structural error
// ([import-report-shape], Core AC #5).
//
// QA MODE-A NOTE (flagged, per the return norms this story has already set
// with GAPI-16/VB-04/VB-06/VB-11): this assertion is ALREADY satisfied by
// the M4-03-only classify/Create logic, which the scaffold leaves entirely
// untouched -- Import() never routes a rule outcome through errorsList on
// any path, scaffolded or real, so this test PASSES even before the
// executor implements anything. It is a genuine, non-vacuous invariant
// (Core AC #5: the two outcomes never mix), just not one Mode A can turn RED
// here -- there is no "the two outcomes mix" bug in today's code to trigger.
func TestServiceImport_MixedFileErrorsStayStructuralOnlyNoRuleFailureLeaks(t *testing.T) {
	res, _, _, _ := runIMPVCleanFile(t, false)

	if len(res.Errors) != 0 {
		t.Errorf("Errors = %+v, want empty -- a rule failure (vat-standard-rate) is not a structural error ([import-report-shape], Core AC#5)", res.Errors)
	}
}

// --- IMPV-04/05: real import, conflict + VAT-wrong + clean -----------------

// TestServiceImport_ConflictPlusVATWrongPlusCleanOutcomesNeverMix (IMPV-04):
// errors has EXACTLY the conflict; invoice_violations has EXACTLY the
// VAT-wrong one; quarantined_invoices==1; invoices_clean==1 -- the two
// outcomes never mix. RED against the scaffold: InvoiceViolations/
// InvoicesClean stay at their zero value.
func TestServiceImport_ConflictPlusVATWrongPlusCleanOutcomesNeverMix(t *testing.T) {
	res, _, _, _ := runIMPVConflictMix(t)

	if len(res.Errors) != 1 {
		t.Fatalf("Errors = %+v, want exactly 1 (the header conflict)", res.Errors)
	}
	if _, found := findRowErrorWithRows(res.Errors, []int{2, 3}); !found {
		t.Errorf("no RowError with Rows==[2,3] (IMPV4-CONFLICT) in %+v", res.Errors)
	}
	if res.QuarantinedInvoices != 1 {
		t.Errorf("QuarantinedInvoices = %d, want 1 (the conflict only -- a rule violation does not quarantine)", res.QuarantinedInvoices)
	}
	if res.InvoicesClean != 1 {
		t.Errorf("InvoicesClean = %d, want 1 (IMPV4-CLEAN)", res.InvoicesClean)
	}

	if len(res.InvoiceViolations) != 1 {
		t.Fatalf("InvoiceViolations = %+v, want exactly 1 (IMPV4-VATWRONG) -- the conflict must NOT also appear here", res.InvoiceViolations)
	}
	iv := res.InvoiceViolations[0]
	if iv.InvoiceNumber != "IMPV4-VATWRONG" {
		t.Errorf("InvoiceViolations[0].InvoiceNumber = %q, want %q", iv.InvoiceNumber, "IMPV4-VATWRONG")
	}
	if !impvHasViolation(iv.Violations, "vat-standard-rate") {
		t.Errorf("InvoiceViolations[0].Violations = %+v, want one naming vat-standard-rate", iv.Violations)
	}
}

// TestServiceImport_ConflictMixCountersMatchM403Exactly (IMPV-05):
// rows_valid+rows_invalid==rows_total; the five M4-03 counters match their
// M4-03-only values exactly (Core AC #5, M4-03 IMP-SVC-13).
//
// QA MODE-A NOTE (flagged, same class as IMPV-03): these counters are
// M4-03-only and unaffected by the gate scaffold, so this test PASSES
// immediately -- a genuine regression guard for "counters unchanged", not a
// novel red at Mode A time.
func TestServiceImport_ConflictMixCountersMatchM403Exactly(t *testing.T) {
	res, _, _, _ := runIMPVConflictMix(t)

	if res.RowsValid+res.RowsInvalid != res.RowsTotal {
		t.Errorf("RowsValid(%d)+RowsInvalid(%d) = %d, want RowsTotal %d", res.RowsValid, res.RowsInvalid, res.RowsValid+res.RowsInvalid, res.RowsTotal)
	}
	if res.RowsTotal != 5 || res.RowsValid != 3 || res.RowsInvalid != 2 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (5,3,2)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	if res.ReadyInvoices != 2 || res.QuarantinedInvoices != 1 {
		t.Errorf("(ReadyInvoices=%d QuarantinedInvoices=%d), want (2,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
}

// --- IMPV-06/07: dry-run, the SAME clean+VAT-wrong file ---------------------

// TestServiceImport_DryRunSameViolationsAsRealRunWillProduce (IMPV-06): the
// IMPV-01 file, dry-run -- the same invoice_violations the real run then
// produces; invoices_clean==2. RED against the scaffold: InvoicesClean/
// InvoiceViolations stay at their zero value on dry-run too.
func TestServiceImport_DryRunSameViolationsAsRealRunWillProduce(t *testing.T) {
	res, _, _, _ := runIMPVCleanFile(t, true)

	if res.InvoicesClean != 2 {
		t.Errorf("dry-run InvoicesClean = %d, want 2 -- the same verdict the real run then produces", res.InvoicesClean)
	}
	if len(res.InvoiceViolations) != 1 {
		t.Fatalf("dry-run InvoiceViolations = %+v, want exactly 1 (IMPV-VATWRONG)", res.InvoiceViolations)
	}
	iv := res.InvoiceViolations[0]
	if iv.InvoiceNumber != "IMPV-VATWRONG" {
		t.Errorf("InvoiceViolations[0].InvoiceNumber = %q, want %q", iv.InvoiceNumber, "IMPV-VATWRONG")
	}
	if iv.InvoiceID != "" {
		t.Errorf("dry-run InvoiceViolations[0].InvoiceID = %q, want empty -- no id exists yet pre-Create ([import-report-shape])", iv.InvoiceID)
	}
	if !impvHasViolation(iv.Violations, "vat-standard-rate") {
		t.Errorf("dry-run InvoiceViolations[0].Violations = %+v, want one naming vat-standard-rate", iv.Violations)
	}
}

// TestServiceImport_DryRunWritesZeroRowsBothTables (IMPV-07): 0
// import_batches rows, 0 invoices rows after a dry-run.
//
// QA MODE-A NOTE (flagged, same class as IMPV-03/05): per the Stage-1
// addendum's own "Re-verified against shipped code" section, dry-run
// returns BEFORE CreateBatch (service.go, unchanged by this scaffold), so
// this ALREADY PASSES today and will keep passing after real
// implementation too (inserting a Gate.Evaluate call ahead of that early
// return still writes nothing). Kept as its own spec regardless, per the
// AC's explicit "IMPV-07 must assert ZERO rows in BOTH tables" wording --
// an always-green regression pin for [import-report-shape]'s "nothing new
// is persisted" claim.
func TestServiceImport_DryRunWritesZeroRowsBothTables(t *testing.T) {
	_, super, _, entityID := runIMPVCleanFile(t, true)

	if got := countImportBatchesForEntity(t, super, entityID); got != 0 {
		t.Errorf("dry-run wrote %d import_batches rows, want 0", got)
	}
	if got := countInvoicesForEntity(t, super, entityID); got != 0 {
		t.Errorf("dry-run wrote %d invoices rows, want 0", got)
	}
}

// --- IMPV-08/09: the gate seam's call-count/item-set specs ------------------

// TestServiceImport_GateValidateBatchCalledExactlyOnceWithAllCreated
// (IMPV-08): a 3-invoice file, real Import, gate.ValidateBatch called
// EXACTLY once with all 3 ([batch-of-one]). RED against the scaffold:
// Import() never calls s.gate.ValidateBatch at all, so
// fakeGate.validateBatchCalls stays 0.
func TestServiceImport_GateValidateBatchCalledExactlyOnceWithAllCreated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "IMPV-08 tenant")
	entityID := seedEntity(t, super, tenantID, "IMPV-08 entity")

	rows := [][]string{
		mkRow("IMPV8-A", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
		mkRow("IMPV8-B", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
		mkRow("IMPV8-C", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	fg := &fakeGate{}
	svc := newTestServiceWithGate(app, fg)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	if _, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if fg.validateBatchCalls != 1 {
		t.Errorf("gate.ValidateBatch called %d times, want exactly 1 ([batch-of-one]: one round trip for the whole batch, not one per invoice)", fg.validateBatchCalls)
	}
	if fg.validateBatchCalls == 1 && len(fg.validateBatchInvs) != 3 {
		t.Errorf("the one ValidateBatch call carried %d invoices, want 3", len(fg.validateBatchInvs))
	}
}

// TestServiceImport_QuarantinedInvoiceNeverReachesGate (IMPV-09): a file
// with 1 quarantined + 2 ready, real Import, gate.ValidateBatch receives
// EXACTLY 2 invoices -- the quarantined one was never created, so it can
// never reach the gate. RED against the scaffold: fakeGate.validateBatchCalls
// stays 0 (Import never calls it at all).
func TestServiceImport_QuarantinedInvoiceNeverReachesGate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "IMPV-09 tenant")
	entityID := seedEntity(t, super, tenantID, "IMPV-09 entity")

	rows := [][]string{
		mkRow("IMPV9-CONFLICT", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
		mkRow("IMPV9-CONFLICT", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "99.00", "Item2", "1", "10.00"), // total disagrees -> quarantined
		mkRow("IMPV9-READY1", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
		mkRow("IMPV9-READY2", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	fg := &fakeGate{}
	svc := newTestServiceWithGate(app, fg)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.QuarantinedInvoices != 1 {
		t.Fatalf("QuarantinedInvoices = %d, want 1 (test setup sanity: IMPV9-CONFLICT must actually quarantine)", res.QuarantinedInvoices)
	}

	if fg.validateBatchCalls != 1 {
		t.Fatalf("gate.ValidateBatch called %d times, want exactly 1", fg.validateBatchCalls)
	}
	if len(fg.validateBatchInvs) != 2 {
		t.Errorf("gate.ValidateBatch received %d invoices, want 2 -- the quarantined invoice was never created, so it must never reach the gate", len(fg.validateBatchInvs))
	}
	for _, inv := range fg.validateBatchInvs {
		if inv.InvoiceNumber == "IMPV9-CONFLICT" {
			t.Errorf("gate.ValidateBatch received IMPV9-CONFLICT, which was quarantined at classify time and never created")
		}
	}
}

// --- IMPV-10/11: abort-on-operational-fault, never laundered ---------------

// errIMPVFakeDBFault is IMPV-10's injected operational fault -- a stand-in
// for the DB fault ApplyValidation can return mid-batch, which no in-request
// lever can trigger for real once Create has already succeeded ([Stage-1
// F3]: Store.Create writes its own history row with the same actor and
// aborts FIRST on an empty Subject, so that trick cannot reach
// ApplyValidation at all).
var errIMPVFakeDBFault = errors.New("qa fake: db fault during ApplyValidation")

// TestServiceImport_ApplyValidationDBFaultAbortsRunNotLaunderedIntoRowErrors
// (IMPV-10): ApplyValidation returns a DB fault mid-batch -> the run aborts,
// Finalize(failed), the raw error propagates (handler 500s) -- never
// laundered into a fake RowError ([create-error-classification]). RED
// against the scaffold: Import() never calls s.gate.ValidateBatch at all, so
// the fake's injected error never surfaces -- Import "succeeds" normally
// instead (err == nil, want non-nil).
func TestServiceImport_ApplyValidationDBFaultAbortsRunNotLaunderedIntoRowErrors(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "IMPV-10 tenant")
	entityID := seedEntity(t, super, tenantID, "IMPV-10 entity")

	rows := [][]string{
		mkRow("IMPV10-INV", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	fg := &fakeGate{validateBatchErr: fmt.Errorf("apply validation to invoice %s: %w", "fake-id", errIMPVFakeDBFault)}
	svc := newTestServiceWithGate(app, fg)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err == nil {
		t.Fatal("Import: err = nil, want the raw ApplyValidation fault to propagate (never laundered into a fake RowError) [create-error-classification]")
	}
	if !errors.Is(err, errIMPVFakeDBFault) {
		t.Errorf("err = %v, want it to wrap errIMPVFakeDBFault", err)
	}
	if errors.Is(err, invoice.ErrValidation) || errors.Is(err, invoice.ErrDuplicateNumber) {
		t.Errorf("err = %v, want a NON-domain error (a DB fault is operational, not bad input)", err)
	}
	if res.ID != "" {
		t.Errorf("res.ID = %q on an aborted run, want empty (BatchResult{} on error)", res.ID)
	}
	if len(res.Errors) != 0 {
		t.Errorf("res.Errors = %+v, want empty -- never laundered into a fake RowError", res.Errors)
	}

	if got := countInvoicesByNumber(t, super, entityID, "IMPV10-INV"); got != 1 {
		t.Errorf("IMPV10-INV persisted = %d, want 1 -- Create already committed before ValidateBatch ran (mid-batch, not mid-Create)", got)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM import_batches WHERE entity_id = $1`, entityID).Scan(&status); err != nil {
		t.Fatalf("read back the batch's terminal status: %v", err)
	}
	if status != "failed" {
		t.Errorf("import_batches.status = %q, want %q (a truthful failed batch, never a laundered 'completed')", status, "failed")
	}
}

// TestServiceImport_ValidatorErrUpstreamAbortsRun (IMPV-11): the validator
// returns ErrUpstream -> the run aborts, Finalize(failed), 500 -- an
// unreachable 04 is an outage, not "everything is clean". RED against the
// scaffold: same shape as IMPV-10, err stays nil.
func TestServiceImport_ValidatorErrUpstreamAbortsRun(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "IMPV-11 tenant")
	entityID := seedEntity(t, super, tenantID, "IMPV-11 entity")

	rows := [][]string{
		mkRow("IMPV11-INV", "2026-07-01", "T1", "B1", "NGN", "10.00", "1.00", "11.00", "Item1", "1", "10.00"),
	}

	fg := &fakeGate{validateBatchErr: fmt.Errorf("%w: fake 04 outage", invoice.ErrUpstream)}
	svc := newTestServiceWithGate(app, fg)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false)
	if err == nil {
		t.Fatal("Import: err = nil, want ErrUpstream to propagate -- an unreachable 04 is an outage, not \"everything is clean\"")
	}
	if !errors.Is(err, invoice.ErrUpstream) {
		t.Errorf("err = %v, want it to wrap invoice.ErrUpstream", err)
	}
	if res.ID != "" {
		t.Errorf("res.ID = %q on an aborted run, want empty", res.ID)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM import_batches WHERE entity_id = $1`, entityID).Scan(&status); err != nil {
		t.Fatalf("read back the batch's terminal status: %v", err)
	}
	if status != "failed" {
		t.Errorf("import_batches.status = %q, want %q", status, "failed")
	}
}

// --- IMPV-12/13: the two named-violation regression specs -----------------

// TestServiceImport_TinLessEntityCleanFileStaysDraftWithSupplierTinRequired
// (IMPV-12): a tin-less entity (seedEntity, no tin -- mirrors
// service_test.go's IMP-SVC-11 fixture), otherwise clean file, real Import
// -> the draft stays draft carrying supplier-tin-required -- M4-03's
// [entity] "intended signal" now actually fires. RED against the scaffold:
// violations stays '[]' (the column default) since ApplyValidation is never
// called.
func TestServiceImport_TinLessEntityCleanFileStaysDraftWithSupplierTinRequired(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "IMPV-12 tenant")
	entityID := seedEntity(t, super, tenantID, "IMPV-12 entity") // no tin, default

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	realGate := invoice.NewGate(invoice.NewStore(app), validator)
	svc := newTestServiceWithGate(app, realGate)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("IMPV12-CLEAN", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "100.00", "7.50", "107.50", "Item1", "1", "100.00"),
	}
	if _, err := svc.Import(c, entityID, stdMapping, stdHeader, rows, false); err != nil {
		t.Fatalf("Import: %v", err)
	}

	id := invoiceIDByNumber(t, super, entityID, "IMPV12-CLEAN")
	status, violations, _ := readInvoiceVerdict(t, super, id)
	if status != "draft" {
		t.Errorf("status = %q, want %q", status, "draft")
	}
	var vs []invoice.Violation
	if err := json.Unmarshal(violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", violations, err)
	}
	if !impvHasViolation(vs, "supplier-tin-required") {
		t.Errorf("violations = %+v, want one naming supplier-tin-required -- M4-03's [entity] intended signal now actually fires", vs)
	}
}

// TestServiceImport_NoLineRowsMappedStaysDraftWithLineItemsRequired
// (IMPV-13): "a file whose invoice has no line rows mapped ... stays draft
// with line-items-required" (task-114's Test Specs table, verbatim).
//
// FLAGGED SPEC AMBIGUITY -- reported, not reinterpreted, per this story's
// own established Mode A norm (its own QA return history already caught a
// spec contradiction and an undiscriminating test by doing exactly this
// rather than silently guessing):
//
// buildCreateInput (service.go, UNCHANGED by this subtask's own plan -- its
// real-path group->line-item mapping is not in task-114's Files to modify)
// appends EXACTLY ONE invoice.LineItemInput per ROW in the group,
// UNCONDITIONALLY, regardless of whether line_description/line_quantity/
// line_unit_price are mapped at all:
//
//	for _, ri := range g.rowIdxs {
//		row := rows[ri]
//		in.LineItems = append(in.LineItems, invoice.LineItemInput{...})
//	}
//
// A READY group requires >= 1 row by construction (a group is built FROM
// rows; there is no way to register an invoice_number with zero rows), so
// len(CreateInput.LineItems) can NEVER be 0 on the real import path as
// currently designed. MBSPayload's `len(inv.LineItems) > 0` guard
// (payload.go) will therefore always see >= 1 (possibly content-empty) line
// item for any real-path invoice, and line-items-required -- a
// PRESENCE-only check on the line_items ARRAY, confirmed by
// internal/invoice/gate_test.go's own GAPI-14 (which fires it ONLY via
// CreateInput.LineItems being entirely OMITTED, i.e. a nil slice, not via
// per-item content) -- cannot fire on any group that has at least one row.
//
// This test is authored as literally as IMPV-13's own wording allows (a
// mapping that omits every line_* canonical field for a single-row group),
// so it is runnable and traceable. It currently reds only because the
// scaffold never calls the gate at all (violations stays '[]') -- NOT
// because of the architectural gap above. Wiring the gate alone will NOT
// make this test pass once implemented for real: reaching
// line-items-required via the real import path, if it is reachable at all,
// needs either a different fixture this analysis missed, or a design
// decision outside this subtask's stated scope (e.g. buildCreateInput
// skipping a LineItemInput for an all-blank line row -- which would also
// change LineNo/[D10]'s "1..N by array position" semantics for the
// remaining lines). Escalating for an architect/executor ruling rather than
// silently reshaping the fixture or the AC.
func TestServiceImport_NoLineRowsMappedStaysDraftWithLineItemsRequired(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "IMPV-13 tenant")
	entityID := seedEntityWithTIN(t, super, tenantID, "IMPV-13 entity", "12345678-0001")

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	realGate := invoice.NewGate(invoice.NewStore(app), validator)
	svc := newTestServiceWithGate(app, realGate)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		{"IMPV13-NOLINES", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "100.00", "7.50", "107.50"},
	}
	if _, err := svc.Import(c, entityID, impvNoLineMapping, impvNoLineHeader, rows, false); err != nil {
		t.Fatalf("Import: %v", err)
	}

	id := invoiceIDByNumber(t, super, entityID, "IMPV13-NOLINES")
	status, violations, _ := readInvoiceVerdict(t, super, id)
	if status != "draft" {
		t.Errorf("status = %q, want %q", status, "draft")
	}
	var vs []invoice.Violation
	if err := json.Unmarshal(violations, &vs); err != nil {
		t.Fatalf("unmarshal violations %s: %v", violations, err)
	}
	if !impvHasViolation(vs, "line-items-required") {
		t.Errorf("violations = %+v, want one naming line-items-required (IMPV-13) -- see this test's doc comment: "+
			"possibly unreachable via the real import path as currently designed; flagged for an architect ruling", vs)
	}
}

// --- IMPV-16: the empty-batch null-not-zero guard --------------------------

// TestServiceImport_AllQuarantinedBatchNullVersionNeverCallsGate (IMPV-16,
// the Stage-1 addendum's new spec, F2): a file where every group is
// quarantined -> rule_set_version is NULL, not 0; invoices_clean==0;
// invoices_with_violations==0; the gate is NEVER called (0 round trips),
// on BOTH real and dry-run.
//
// QA MODE-A NOTE (flagged): every one of these assertions ALREADY holds
// against the scaffold, since Import() never calls s.gate under ANY
// circumstance (not just the empty-batch one) -- so this test is
// VACUOUSLY green today, exactly as internal/validation's VB-13 was
// predicted and documented to be for a structurally analogous reason
// ("empty invoice passes vacuously ... mis-rooting cannot be caught
// there"). Its real discriminating power only exists ONCE the executor
// wires real, non-empty-batch gate calls (per IMPV-02/06/08, which DO fail
// today): the executor cannot satisfy those without ALSO guarding the
// empty-batch case on len(created)==0 / len(readyGroups)==0 rather than on
// Evaluate/ValidateBatch's returned RuleSetVersion value, or this test
// regresses the moment that guard is missing. Kept as its own spec per the
// Stage-1 addendum's explicit new-spec directive, not folded away.
func TestServiceImport_AllQuarantinedBatchNullVersionNeverCallsGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dryRun bool
	}{
		{"real", false},
		{"dry_run", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			ctx := context.Background()
			tenantID := seedTenant(t, super, "IMPV-16 "+tc.name+" tenant")
			entityID := seedEntityWithTIN(t, super, tenantID, "IMPV-16 "+tc.name+" entity", "12345678-0001")

			fg := &fakeGate{}
			svc := newTestServiceWithGate(app, fg)
			c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

			res, err := svc.Import(c, entityID, stdMapping, stdHeader, impvAllQuarantinedFixture(), tc.dryRun)
			if err != nil {
				t.Fatalf("Import (dryRun=%v): %v", tc.dryRun, err)
			}
			if res.RuleSetVersion != nil {
				t.Errorf("RuleSetVersion = %d, want nil (null) -- nothing was evaluated, a returned 0 would be a false version stamp", *res.RuleSetVersion)
			}
			if res.InvoicesClean != 0 {
				t.Errorf("InvoicesClean = %d, want 0", res.InvoicesClean)
			}
			if res.InvoicesWithViolations != 0 {
				t.Errorf("InvoicesWithViolations = %d, want 0", res.InvoicesWithViolations)
			}
			if fg.evaluateCalls != 0 || fg.validateBatchCalls != 0 {
				t.Errorf("gate calls = (Evaluate=%d ValidateBatch=%d), want (0,0) -- an all-quarantined batch must never round-trip to 04", fg.evaluateCalls, fg.validateBatchCalls)
			}
		})
	}
}

