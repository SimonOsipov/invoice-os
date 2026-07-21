// M4-11-03: DB-backed green-clean verification -- runs the committed
// green_500.csv/green_second.csv through the real import->validate
// pipeline (Decode -> a real portfolio-store entity -> Service.Import
// against the real in-process rule-set 04) and asserts zero quarantine and
// zero violations end to end.
//
// A dirty fixture here is a fixturegen bug, not a test bug: these tests
// must not be weakened, and the committed CSVs must not be hand-edited.
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

// importGreenFixture reads path, decodes it as CSV, seeds a fresh tenant +
// entity via the real portfolio.Store path, and imports it (dryRun=false)
// through a Service wired to a real in-process rule-set 04. One fresh
// tenant+entity per call, so callers never share state. Also returns
// entityID and the superuser pool so callers can read back persisted rows
// out-of-band.
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

// assertCleanImport asserts nothing quarantined, nothing invalid, nothing
// violates, and ReadyInvoices matches the file's invoice-group count.
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

// TestFixtures_Green500ImportsAndValidatesClean: green_500.csv (500
// invoices, 3 lines each) must import and validate fully clean.
func TestFixtures_Green500ImportsAndValidatesClean(t *testing.T) {
	res, _, _ := importGreenFixture(t, "../../testdata/invoices/green_500.csv",
		"M4-11-03 green_500 tenant", "M4-11-03 green_500 entity")
	assertCleanImport(t, "green_500.csv", res, 500)
}

// TestFixtures_GreenSecondImportsAndValidatesClean: green_second.csv (250
// invoices, distinct seed from green_500) must import and validate fully
// clean.
func TestFixtures_GreenSecondImportsAndValidatesClean(t *testing.T) {
	res, _, _ := importGreenFixture(t, "../../testdata/invoices/green_second.csv",
		"M4-11-03 green_second tenant", "M4-11-03 green_second entity")
	assertCleanImport(t, "green_second.csv", res, 250)
}

// TestFixtures_GreenNoStoreDuplicateOnFreshEntity: the against-store
// duplicate check (ruleKeyDuplicateInvoiceNumber) must not false-fire
// against a fresh, empty entity store -- checked both via res.Errors and
// violationKeys(res). The absence-only loops below would also pass on a
// totally broken (zero-group) import, so assertCleanImport's
// ReadyInvoices==500 check guards that the duplicate check actually had
// 500 numbers to evaluate.
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
