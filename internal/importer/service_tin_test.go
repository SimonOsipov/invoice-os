// M4-04-08 (RALPH fix cycle): the regression suite for the FALSE
// supplier-tin-format violation that dev-env.yml run 29575083819 caught
// (e2e/api/perf.spec.ts: invoices_clean "Expected 450, Received 0").
//
// WHY THIS FILE EXISTS -- the test gap is the real defect. EVERY pre-existing
// fixture creates its entity by RAW SQL INSERT of an already-hyphenated
// literal: seedEntityWithTIN (service_gate_test.go), db/demo-reset.sql,
// e2e/api/contract-validation.spec.ts's hand-built JSON. NOTHING exercised
// portfolio.Store.Create, the ONLY thing that actually writes an entity TIN in
// production -- and which CANONICALIZES it (hyphen-stripped) via ValidateTIN.
// So the whole Go suite was structurally blind to the fact that an
// API-created entity's TIN cannot satisfy supplier-tin-format
// (^[0-9]{8}-[0-9]{4}$). Only the DEPLOYED perf spec, whose createEntity()
// posts through the real API, ever hit it.
//
// Proof of the blindness, in one line: seedEntityWithTIN's own fixture TIN
// "12345678-0001" is not even Luhn-valid -- ValidateTIN would REJECT it. The
// raw INSERT is what let it through.
//
// These tests close the gap by going through the REAL portfolio.Store.Create.
//
// The internal test package (package importer) importing internal/portfolio
// mirrors service_gate_test.go, which already imports internal/validation --
// another separately-deployed service (cmd/validation) -- to drive the real
// engine in-process. Neither adds a PRODUCTION import edge: `go list -deps
// ./internal/importer` and `./cmd/invoice` cover non-test files only, so the
// service boundary that internal/importer/store.go:55 protects ("a per-package
// copy, not a cross-package import") stays intact. TestImporter_...
// DoesNotImportPortfolioInProduction below PINS that.
package importer

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/portfolio"
)

// tinFixFIRSTIN is a Luhn-VALID hyphenated 8+4 FIRS TIN, so portfolio's
// ValidateTIN ACCEPTS it (the point of these tests is what happens AFTER
// acceptance). It canonicalizes to "100123450007" -- 12 bare digits, which is
// exactly what supplier-tin-format rejects without mbsSupplierTIN.
const tinFixFIRSTIN = "10012345-0007"

// tinFixJTBTIN is a Luhn-valid 10-digit JTB TIN -- the shape tin.go accepts
// alongside the FIRS shapes, and which has NO hyphen to restore.
const tinFixJTBTIN = "1001230000"

// createEntityViaRealPortfolioStore creates ONE business_entities row through
// the REAL portfolio.Store.Create -- ValidateTIN, Luhn, canonicalization and
// all -- i.e. the exact path POST /v1/entities takes in production, and the
// path every pre-existing importer fixture bypasses. Returns the entity id and
// the CANONICAL tin as actually persisted.
func createEntityViaRealPortfolioStore(t *testing.T, super, app *pgxpool.Pool, tenantID, name, rawTIN string) (entityID, canonicalTIN string) {
	t.Helper()
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID,
	})
	ent, err := portfolio.NewStore(app).Create(ctx, portfolio.CreateInput{Name: name, TIN: rawTIN})
	if err != nil {
		t.Fatalf("portfolio.Store.Create(tin=%q): %v", rawTIN, err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, ent.ID)
	})
	if ent.TIN == nil {
		t.Fatalf("portfolio.Store.Create returned a nil TIN for input %q", rawTIN)
	}
	return ent.ID, *ent.TIN
}

// tinFixCleanFile is one otherwise-clean invoice (2 lines): 2x100.00 + 1x50.00
// = 250.00 subtotal [line-items-sum-subtotal], VAT 18.75 = 7.5% of 250.00
// [vat-standard-rate], buyer TIN hyphenated and Luhn-valid [buyer-tin-format].
// Same verified-clean numbers as impvCleanFileFixture. Supplier tin/name are
// NOT CSV columns -- they come from the entity ([supplier-from-entity]), which
// is precisely what these tests exercise. So the ONLY thing that can make this
// file violate is the supplier TIN's spelling.
func tinFixCleanFile() [][]string {
	return [][]string{
		mkRow("TINFIX-CLEAN-1", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "18.75", "268.75", "Item1", "2", "100.00"),
		mkRow("TINFIX-CLEAN-1", "2026-07-01", "87654321-0002", "Beta Ltd", "NGN", "250.00", "18.75", "268.75", "Item2", "1", "50.00"),
	}
}

// importCleanFileForEntity runs tinFixCleanFile() for entityID through a REAL
// Service wired to a REAL in-process 04 on the REAL active rule set.
func importCleanFileForEntity(t *testing.T, app *pgxpool.Pool, tenantID, entityID string) BatchResult {
	t.Helper()
	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	svc := newTestServiceWithGate(app, invoice.NewGate(invoice.NewStore(app), validator))
	c := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID,
	})
	res, err := svc.Import(c, entityID, stdMapping, stdHeader, tinFixCleanFile(), false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return res
}

// violationKeys flattens a BatchResult's reported violations to rule keys.
func violationKeys(res BatchResult) []string {
	var keys []string
	for _, iv := range res.InvoiceViolations {
		for _, v := range iv.Violations {
			keys = append(keys, v.RuleKey)
		}
	}
	return keys
}

// TestServiceImport_APICreatedEntityCleanFileHasNoFalseTinFormatViolation is
// THE regression test for the shipped bug, and the one the whole suite lacked:
// an entity created the way production creates one (portfolio.Store.Create ->
// canonical 12-digit TIN) importing an otherwise-clean invoice must report
// ZERO violations. Before the fix, supplier-tin-format fires on every invoice
// of every API-created entity -- the deployed perf spec's invoices_clean 450
// -> 0.
//
// This is the SAME shape as the false-clean class M4-04 hunts, mirrored: a
// false VIOLATION rejecting a firm's valid invoices.
func TestServiceImport_APICreatedEntityCleanFileHasNoFalseTinFormatViolation(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "TINFIX api-created entity tenant")

	entityID, canonicalTIN := createEntityViaRealPortfolioStore(
		t, super, app, tenantID, "TINFIX API-created Supplier Co", tinFixFIRSTIN)

	// Pin the PREMISE, so a future tin.go change that stops canonicalizing
	// turns this into a loud, self-explaining failure rather than a silently
	// vacuous pass: the stored TIN really is the hyphen-free 12-digit form
	// that supplier-tin-format rejects.
	if canonicalTIN != "100123450007" {
		t.Fatalf("premise broken: portfolio.Store.Create persisted tin %q, want the canonical %q "+
			"(if tin.go no longer canonicalizes, this test's reason to exist has changed)",
			canonicalTIN, "100123450007")
	}

	res := importCleanFileForEntity(t, app, tenantID, entityID)

	if got := violationKeys(res); len(got) != 0 {
		t.Errorf("clean invoice for an API-created entity reported violations %v, want none "+
			"(supplier-tin-format firing here is the FALSE violation of M4-04-08: the entity TIN is "+
			"known-valid -- it passed ValidateTIN+Luhn -- and WE stripped its hyphen on write)", got)
	}
	if res.InvoicesClean != 1 {
		t.Errorf("InvoicesClean = %d, want 1 -- this is the deployed perf spec's assertion in miniature "+
			"(dev-env.yml 29575083819: Expected 450, Received 0)", res.InvoicesClean)
	}
	if res.InvoicesWithViolations != 0 {
		t.Errorf("InvoicesWithViolations = %d, want 0", res.InvoicesWithViolations)
	}
}

// TestServiceImport_APICreatedJTBEntityReportsGenuineTinFormatViolation pins
// the DELIBERATE non-fix for the 10-digit JTB shape. A JTB TIN has no hyphen
// to restore, and mbsSupplierTIN must NOT invent an 8+4 split -- that would
// fabricate a FIRS TIN out of a JTB one. It therefore cannot satisfy
// supplier-tin-format (^[0-9]{8}-[0-9]{4}$), and the resulting violation is
// GENUINE: a real signal about a real MBS-vs-JTB mismatch, not a formatting
// bug. M4-04-08 FLAGS this for a product decision rather than papering over
// it.
//
// This test exists so that decision is made DELIBERATELY: if the product
// answer is "JTB TINs must pass", this test is the thing that must change,
// and it says so out loud.
func TestServiceImport_APICreatedJTBEntityReportsGenuineTinFormatViolation(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "TINFIX jtb entity tenant")

	entityID, canonicalTIN := createEntityViaRealPortfolioStore(
		t, super, app, tenantID, "TINFIX JTB Supplier Co", tinFixJTBTIN)

	// A JTB TIN is already canonical: 10 digits, nothing stripped.
	if canonicalTIN != tinFixJTBTIN {
		t.Fatalf("premise broken: JTB tin persisted as %q, want %q unchanged", canonicalTIN, tinFixJTBTIN)
	}

	res := importCleanFileForEntity(t, app, tenantID, entityID)

	if got := violationKeys(res); len(got) != 1 || got[0] != "supplier-tin-format" {
		t.Errorf("violations for a JTB-TIN entity = %v, want exactly [supplier-tin-format] -- "+
			"a 10-digit JTB TIN genuinely cannot satisfy the MBS 8+4 rule. If mbsSupplierTIN ever "+
			"hyphenates a JTB TIN it would fabricate a FIRS TIN and this must fail.", got)
	}
	if res.InvoicesClean != 0 {
		t.Errorf("InvoicesClean = %d, want 0 (the JTB violation is real and blocking)", res.InvoicesClean)
	}
}

// TestMBSSupplierTIN_IsTheExactInverseOfValidateTIN is the DRIFT GUARD, and
// the reason mbsSupplierTIN may safely live in internal/importer as a local
// helper instead of beside tin.go: it round-trips through the REAL
// portfolio.ValidateTIN, so any change to tin.go's canonicalization
// (tinShapePattern gaining a shape, Replace's count changing) reds THIS test
// rather than silently re-arming the false-violation trap in production.
//
// Pure and DB-free: ValidateTIN is a pure function, so this runs in every
// `go test ./...` even without DATABASE_URL -- unlike the DB-backed specs
// above, which skip. That matters: the guard must fire in the cheapest suite.
func TestMBSSupplierTIN_IsTheExactInverseOfValidateTIN(t *testing.T) {
	// Every FIRS spelling ValidateTIN accepts must map back to the ONE MBS
	// wire spelling. tin.go's own doc: the hyphenated and bare-12 spellings
	// "persist identically" -- they ARE the same TIN, so both must render as
	// NNNNNNNN-NNNN. Rendering the bare-12 input is therefore NOT fabricating
	// a format: it is spelling the single canonical identity the MBS way.
	for _, spelling := range []string{"10012345-0007", "100123450007"} {
		canonical, err := portfolio.ValidateTIN(spelling)
		if err != nil {
			t.Fatalf("ValidateTIN(%q): %v (fixture must be a VALID TIN)", spelling, err)
		}
		got := mbsSupplierTIN(&canonical)
		if got == nil || *got != "10012345-0007" {
			t.Errorf("mbsSupplierTIN(ValidateTIN(%q)=%q) = %v, want %q -- the inverse has drifted from tin.go",
				spelling, canonical, derefOrNil(got), "10012345-0007")
		}
	}

	// A JTB TIN survives untouched -- see the JTB test above.
	canonicalJTB, err := portfolio.ValidateTIN(tinFixJTBTIN)
	if err != nil {
		t.Fatalf("ValidateTIN(%q): %v", tinFixJTBTIN, err)
	}
	if got := mbsSupplierTIN(&canonicalJTB); got == nil || *got != tinFixJTBTIN {
		t.Errorf("mbsSupplierTIN(JTB %q) = %v, want it UNCHANGED -- hyphenating a JTB TIN "+
			"fabricates a FIRS TIN", tinFixJTBTIN, derefOrNil(got))
	}
}

// TestMBSSupplierTIN_LeavesUncanonicalizedValuesAlone pins that the helper
// only ever restores what WE stripped. An already-hyphenated row (every
// raw-seeded fixture, db/demo-reset.sql's literals) and a nil TIN must pass
// through untouched -- store-invalid-faithfully: we do not rewrite values we
// did not canonicalize.
func TestMBSSupplierTIN_LeavesUncanonicalizedValuesAlone(t *testing.T) {
	if got := mbsSupplierTIN(nil); got != nil {
		t.Errorf("mbsSupplierTIN(nil) = %v, want nil (a TIN-less entity must still fire supplier-tin-required)", *got)
	}
	for _, raw := range []string{
		"10223456-0022", // db/demo-reset.sql's already-hyphenated literal
		"12345678-0001", // seedEntityWithTIN's raw-seeded literal
		"",              // an empty string is not canonical -- never rewritten
		"BADTIN",        // stored-invalid content must keep violating, faithfully
		"1234567890123", // 13 digits: not a shape we produce
	} {
		v := raw
		got := mbsSupplierTIN(&v)
		if got == nil || *got != raw {
			t.Errorf("mbsSupplierTIN(%q) = %v, want it unchanged", raw, derefOrNil(got))
		}
	}
}

// derefOrNil renders a *string for a failure message without panicking on nil.
func derefOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// TestImporter_DoesNotImportPortfolioInProduction pins the SERVICE BOUNDARY
// this file's portfolio import must not breach. internal/importer ships in
// cmd/invoice; internal/portfolio ships in cmd/portfolio -- two separately
// deployed services. store.go:55 states the convention verbatim: "a
// per-package copy, not a cross-package import". The tests above import
// portfolio as an out-of-band oracle (exactly as service_gate_test.go imports
// internal/validation to drive the real engine), which adds NO production
// edge -- `go list -deps` covers non-test files only. This test proves that
// claim instead of asserting it in a comment.
//
// Precedent: internal/invoice/validator_test.go's
// TestValidatorClient_DoesNotImportValidationPackage (VC-14).
func TestImporter_DoesNotImportPortfolioInProduction(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	for _, pkg := range []string{"./internal/importer", "./cmd/invoice"} {
		cmd := exec.Command("go", "list", "-deps", pkg)
		cmd.Dir = strings.TrimSpace(string(root))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.TrimSpace(line) == "github.com/SimonOsipov/invoice-os/internal/portfolio" {
				t.Errorf("%s imports internal/portfolio in PRODUCTION -- forbidden: importer (cmd/invoice) "+
					"and portfolio (cmd/portfolio) are separate services. mbsSupplierTIN is a local helper "+
					"precisely so this edge never exists; the portfolio import here is TEST-ONLY.", pkg)
			}
		}
	}
}
