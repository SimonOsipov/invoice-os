// M4-11-03 (task-195): DB-backed green-clean verification. The import->
// validate pipeline (M4-03/04/06) already ships, and M4-11-01/02 already
// generated and committed testdata/invoices/green_500.csv and
// green_second.csv as SUPPOSEDLY clean fixtures. Nothing before this file
// ever actually ran either committed CSV through the real pipeline: closest
// is tools/fixturegen/committed_test.go, which only checks header/row-count
// shape, not Service.Import. These tests are that missing proof: read the
// committed bytes -> Decode -> seed a tenant+entity via the REAL
// portfolio.Store.Create path (createEntityViaRealPortfolioStore,
// service_tin_test.go) -> Service.Import wired to a REAL in-process rule-set
// 04 (startInProcess04ForImporter, service_gate_test.go) on the ACTIVE v2
// rule set -> assert zero quarantine and zero violations end to end.
//
// If a file is NOT clean on this real path, that is a fixturegen bug
// (M4-11-01), not a test bug -- these tests must not be weakened to make a
// dirty fixture pass, and the committed CSVs must not be hand-edited
// (M4-11-02's no-hand-edit guard).
package importer

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// importGreenFixture reads path (relative to this package dir), decodes it
// as CSV, seeds a FRESH tenant + entity through the real portfolio.Store
// path (createEntityViaRealPortfolioStore, service_tin_test.go -- a
// Luhn-valid canonical FIRS TIN, exactly what production writes on
// POST /v1/entities), and imports it for real (dryRun=false) through a
// Service wired to a REAL in-process rule-set 04 on the active v2 rule set
// (startInProcess04ForImporter, service_gate_test.go). One fresh
// tenant+entity per call, so green_500 and green_second (and repeat calls
// for the no-store-duplicate check) never share state.
//
// Also returns entityID and the superuser pool so callers can read back
// the actually-persisted invoices/line_items rows out-of-band (M4-11-03
// QA adversarial addition, fixtures_green_adversarial_test.go) -- proof
// that BatchResult's self-reported counters correspond to real writes,
// not merely a self-consistent but silently no-op'd import.
func importGreenFixture(t *testing.T, path, tenantLabel, entityName string) (res BatchResult, entityID string, super *pgxpool.Pool) {
	t.Helper()
	super, app := dbTestPools(t)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	header, rows, _, err := Decode(bytes.NewReader(data), "csv")
	if err != nil {
		t.Fatalf("Decode %s: %v", path, err)
	}

	tenantID := seedTenant(t, super, tenantLabel)
	// tinFixFIRSTIN (service_tin_test.go): a Luhn-valid hyphenated FIRS TIN,
	// so portfolio's ValidateTIN accepts it and canonicalizes it exactly as
	// production does for every API-created entity.
	entityID, _ = createEntityViaRealPortfolioStore(t, super, app, tenantID, entityName, tinFixFIRSTIN)

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	svc := newTestServiceWithGate(app, invoice.NewGate(invoice.NewStore(app), validator))

	c := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID,
	})

	res, err = svc.Import(c, entityID, stdMapping, header, rows, false)
	if err != nil {
		t.Fatalf("Import %s: %v", path, err)
	}
	return res, entityID, super
}

// assertCleanImport pins the AC-1/AC-2 clean invariant: nothing quarantined,
// nothing invalid, nothing violates, and the file's invoice-group count came
// through as ReadyInvoices.
func assertCleanImport(t *testing.T, label string, res BatchResult, wantReadyInvoices int) {
	t.Helper()
	if res.RowsInvalid != 0 {
		t.Errorf("%s: RowsInvalid = %d, want 0 (Errors: %+v)", label, res.RowsInvalid, res.Errors)
	}
	if res.QuarantinedInvoices != 0 {
		t.Errorf("%s: QuarantinedInvoices = %d, want 0 (Errors: %+v)", label, res.QuarantinedInvoices, res.Errors)
	}
	if res.InvoicesWithViolations != 0 {
		t.Errorf("%s: InvoicesWithViolations = %d, want 0 (InvoiceViolations: %+v)", label, res.InvoicesWithViolations, res.InvoiceViolations)
	}
	if len(res.InvoiceViolations) != 0 {
		t.Errorf("%s: len(InvoiceViolations) = %d, want 0: %+v", label, len(res.InvoiceViolations), res.InvoiceViolations)
	}
	if res.ReadyInvoices != wantReadyInvoices {
		t.Errorf("%s: ReadyInvoices = %d, want %d", label, res.ReadyInvoices, wantReadyInvoices)
	}
	if res.Status != "completed" {
		t.Errorf("%s: Status = %q, want completed", label, res.Status)
	}
}

// TestFixtures_Green500ImportsAndValidatesClean is task-195 AC-1: the
// committed green_500.csv (500 distinct invoice_number groups, 3 line items
// each -- tools/fixturegen generateGreen(seed,500)) must import and validate
// fully clean through the real pipeline.
func TestFixtures_Green500ImportsAndValidatesClean(t *testing.T) {
	res, _, _ := importGreenFixture(t, "../../testdata/invoices/green_500.csv",
		"M4-11-03 green_500 tenant", "M4-11-03 green_500 entity")
	assertCleanImport(t, "green_500.csv", res, 500)
}

// TestFixtures_GreenSecondImportsAndValidatesClean is task-195 AC-2: the
// committed green_second.csv (generateGreen(43,250): 250 distinct
// invoice_number groups under a DIFFERENT seed, so its content genuinely
// differs from green_500.csv rather than being a truncation of it) must
// import and validate fully clean under its own, distinct fresh entity.
func TestFixtures_GreenSecondImportsAndValidatesClean(t *testing.T) {
	res, _, _ := importGreenFixture(t, "../../testdata/invoices/green_second.csv",
		"M4-11-03 green_second tenant", "M4-11-03 green_second entity")
	assertCleanImport(t, "green_second.csv", res, 250)
}

// TestFixtures_GreenNoStoreDuplicateOnFreshEntity is task-195 AC-3: the
// M4-06 against-store duplicate check (ruleKeyDuplicateInvoiceNumber,
// service.go) must not false-fire against a FRESH, empty per-entity store on
// a file's first import -- every one of green_500.csv's invoice numbers is
// new to this entity, so ExistingNumbers must report none of them as
// already-imported. Checked in both places the rule can surface: the
// rule-shaped RowError in res.Errors (the store-duplicate path) and
// violationKeys(res) (the 04 rule-engine path).
//
// The two loops below are absence-only (no key/RuleKey matches
// ruleKeyDuplicateInvoiceNumber), which on their own would PASS just as
// happily on a totally broken import (0 groups ever created, nothing ever
// evaluated) as on a genuine "checked 500 opportunities and none false-
// fired" run (QA Mode-B mutation-verified, M4-11-03: a hand-built
// zero-ReadyInvoices BatchResult clears both loops trivially). The
// assertCleanImport call below closes that gap by pinning
// ReadyInvoices==500 (so the duplicate check demonstrably had 500 fresh
// numbers to evaluate, not zero) alongside the rest of the clean invariant,
// before the two loops name the specific rule this AC is about.
func TestFixtures_GreenNoStoreDuplicateOnFreshEntity(t *testing.T) {
	res, _, _ := importGreenFixture(t, "../../testdata/invoices/green_500.csv",
		"M4-11-03 no-dup tenant", "M4-11-03 no-dup entity")
	assertCleanImport(t, "green_500.csv (no-dup)", res, 500)

	for _, key := range violationKeys(res) {
		if key == ruleKeyDuplicateInvoiceNumber {
			t.Fatalf("violationKeys(res) contains %q on a fresh entity's first import -- "+
				"the store-duplicate rule false-fired against an empty store", ruleKeyDuplicateInvoiceNumber)
		}
	}
	for _, e := range res.Errors {
		if e.RuleKey == ruleKeyDuplicateInvoiceNumber {
			t.Fatalf("res.Errors contains a %q RowError on a fresh entity's first import: %+v",
				ruleKeyDuplicateInvoiceNumber, e)
		}
	}
}
