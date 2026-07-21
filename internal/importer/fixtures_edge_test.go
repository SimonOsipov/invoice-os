// M4-11-04: DB-backed edge-case fixture verification -- runs each
// committed edge_*.csv through the real import->validate pipeline (mirrors
// fixtures_green_test.go's pattern) and asserts it trips exactly its one
// intended failure, leaving the other invoices unaffected.
//
// A fixture that doesn't trip its intended failure (or trips an extra one)
// is a fixturegen bug, not a test bug: these tests must not be weakened,
// and the committed CSVs must not be hand-edited.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// importEdgeFixture is importGreenFixture's twin for an edge fixture, but
// returns err instead of t.Fatal-ing on it -- AC#1/#3 expect Import to fail
// (ErrValidation) and need the error value.
func importEdgeFixture(t *testing.T, path, tenantLabel, entityName string) (res BatchResult, err error, entityID string, super *pgxpool.Pool) {
	t.Helper()
	super, app := dbTestPools(t)

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read fixture %s: %v", path, readErr)
	}
	header, rows, _, decErr := Decode(bytes.NewReader(data), "csv")
	if decErr != nil {
		t.Fatalf("Decode %s: %v", path, decErr)
	}

	tenantID := seedTenant(t, super, tenantLabel)
	entityID, _ = createEntityViaRealPortfolioStore(t, super, app, tenantID, entityName, tinFixFIRSTIN)

	srv := startInProcess04ForImporter(t, app)
	validator := invoice.NewValidator(srv.URL, impvS2SToken, nil)
	svc := newTestServiceWithGate(app, invoice.NewGate(invoice.NewStore(app), validator))

	c := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID,
	})

	res, err = svc.Import(c, entityID, stdMapping, header, rows, false)
	return res, err, entityID, super
}

// assertNothingWrittenForEntity asserts zero import_batches/invoices rows
// for entityID, read back via the superuser pool.
func assertNothingWrittenForEntity(t *testing.T, super *pgxpool.Pool, entityID, label string) {
	t.Helper()
	ctx := context.Background()
	var batchCount, invoiceCount int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, entityID).Scan(&batchCount); err != nil {
		t.Fatalf("%s: count import_batches: %v", label, err)
	}
	if err := super.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE entity_id = $1`, entityID).Scan(&invoiceCount); err != nil {
		t.Fatalf("%s: count invoices: %v", label, err)
	}
	if batchCount != 0 {
		t.Errorf("%s: import_batches rows for entity = %d, want 0 (a pre-write validation error must persist nothing)", label, batchCount)
	}
	if invoiceCount != 0 {
		t.Errorf("%s: invoices rows for entity = %d, want 0 (a pre-write validation error must persist nothing)", label, invoiceCount)
	}
}

// assertPreWriteRejected asserts the shared "rejected before any write"
// shape: err is ErrValidation, res is zero-value, and nothing was written.
// reason/label name the fixture and why, for failure messages.
func assertPreWriteRejected(t *testing.T, path, tenantLabel, entityName, label, reason string) {
	t.Helper()
	res, err, entityID, super := importEdgeFixture(t, path, tenantLabel, entityName)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Import %s: err = %v, want errors.Is(err, ErrValidation) -- %s", label, err, reason)
	}
	if res.ID != "" || res.Status != "" {
		t.Errorf("Import %s: res = %+v, want the zero-value BatchResult on a pre-write validation error", label, res)
	}
	assertNothingWrittenForEntity(t, super, entityID, label)
}

// --- AC#1: edge_missing_columns ---------------------------------------------

// TestFixtures_MissingColumnsRejectedPreWrite: edge_missing_columns.csv
// drops the Total column, but stdMapping still maps total -> "Total";
// resolveMapping must reject that before any write.
func TestFixtures_MissingColumnsRejectedPreWrite(t *testing.T) {
	assertPreWriteRejected(t, "../../testdata/invoices/edge_missing_columns.csv",
		"M4-11-04 missing-columns tenant", "M4-11-04 missing-columns entity",
		"edge_missing_columns.csv",
		"the file's header has no \"Total\" column, but stdMapping maps field \"total\" to header \"Total\"")
}

// --- AC#2: edge_in_file_dupes -----------------------------------------------

// TestFixtures_InFileDupesQuarantined: edge_in_file_dupes.csv has one
// invoice (INV-SYN-00001) with a 4th row differing only on Issue Date;
// import must complete, quarantine exactly that group, and report a
// RowError naming the conflicting field.
func TestFixtures_InFileDupesQuarantined(t *testing.T) {
	res, err, _, _ := importEdgeFixture(t, "../../testdata/invoices/edge_in_file_dupes.csv",
		"M4-11-04 in-file-dupes tenant", "M4-11-04 in-file-dupes entity")
	if err != nil {
		t.Fatalf("Import edge_in_file_dupes.csv: %v", err)
	}
	if res.Status != "completed" {
		t.Errorf("Status = %q, want %q -- an in-file dupe quarantines its OWN group, it does not abort the run", res.Status, "completed")
	}
	if res.QuarantinedInvoices != 1 {
		t.Errorf("QuarantinedInvoices = %d, want 1 (Errors: %+v)", res.QuarantinedInvoices, res.Errors)
	}
	if res.ReadyInvoices != 4 {
		t.Errorf("ReadyInvoices = %d, want 4 (5 groups total, exactly 1 quarantined)", res.ReadyInvoices)
	}

	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "rows disagree on issue_date") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("res.Errors = %+v, want an entry whose message contains %q", res.Errors, "rows disagree on issue_date")
	}
}

// --- AC#3: edge_bad_encoding -------------------------------------------------

// TestFixtures_BadEncodingRejected: edge_bad_encoding.csv is UTF-16LE
// without a BOM, so Decode's sniff falls through to the windows-1252
// tolerant fallback, which mangles the header row past recognition --
// resolveMapping must reject that before any write, like AC#1.
func TestFixtures_BadEncodingRejected(t *testing.T) {
	assertPreWriteRejected(t, "../../testdata/invoices/edge_bad_encoding.csv",
		"M4-11-04 bad-encoding tenant", "M4-11-04 bad-encoding entity",
		"edge_bad_encoding.csv",
		"UTF-16LE-without-BOM content tolerantly decoded as windows-1252 must mangle the header row past recognition")
}

// --- AC#4: edge_bad_tin ------------------------------------------------------

// TestFixtures_BadTinImportsCleanValidateRejects: edge_bad_tin.csv mutates
// INV-SYN-00001's TIN to fail ^[0-9]{8}-[0-9]{4}$; a buyer TIN is never
// import-time-checked, only caught by the 04 gate at validate time, so
// Import must complete with zero quarantine and report buyer-tin-format on
// exactly that invoice.
func TestFixtures_BadTinImportsCleanValidateRejects(t *testing.T) {
	res, err, _, _ := importEdgeFixture(t, "../../testdata/invoices/edge_bad_tin.csv",
		"M4-11-04 bad-tin tenant", "M4-11-04 bad-tin entity")
	if err != nil {
		t.Fatalf("Import edge_bad_tin.csv: %v", err)
	}
	if res.QuarantinedInvoices != 0 {
		t.Errorf("QuarantinedInvoices = %d, want 0 -- a malformed buyer TIN is a VALIDATE-time rule "+
			"violation, not an import-time structural error (Errors: %+v)", res.QuarantinedInvoices, res.Errors)
	}
	if res.InvoicesWithViolations != 1 {
		t.Errorf("InvoicesWithViolations = %d, want 1 (exactly the one TIN-mutated invoice; the other 19 clean)", res.InvoicesWithViolations)
	}

	keys := violationKeys(res)
	if !containsKey(keys, "buyer-tin-format") {
		t.Errorf("violationKeys(res) = %v, want it to contain %q", keys, "buyer-tin-format")
	}

	// Pin attribution, not just count: InvoicesWithViolations==1 alone
	// would also pass if the violation landed on the wrong invoice.
	assertViolationsPinnedTo(t, res, "INV-SYN-00001")
}

// --- AC#5: edge_vat_math_wrong -----------------------------------------------

// TestFixtures_VatMathWrongImportsCleanValidateRejects: edge_vat_math_wrong.csv
// mutates INV-SYN-00001's VAT to 0.00 (subtotal/lines still reconcile);
// Import must complete with zero quarantine. The violated rule-key set
// across the whole batch must be EXACTLY {"vat-standard-rate"}, not merely
// contain it -- any extra key or invoice means a real fixturegen/rule-engine
// defect.
func TestFixtures_VatMathWrongImportsCleanValidateRejects(t *testing.T) {
	res, err, _, _ := importEdgeFixture(t, "../../testdata/invoices/edge_vat_math_wrong.csv",
		"M4-11-04 vat-math-wrong tenant", "M4-11-04 vat-math-wrong entity")
	if err != nil {
		t.Fatalf("Import edge_vat_math_wrong.csv: %v", err)
	}
	if res.QuarantinedInvoices != 0 {
		t.Errorf("QuarantinedInvoices = %d, want 0 -- wrong VAT math is a VALIDATE-time rule violation, "+
			"not an import-time structural error (Errors: %+v)", res.QuarantinedInvoices, res.Errors)
	}
	if res.InvoicesWithViolations != 1 {
		t.Errorf("InvoicesWithViolations = %d, want 1 (exactly the one VAT-mutated invoice; the other 19 clean)", res.InvoicesWithViolations)
	}

	uniq := map[string]bool{}
	for _, k := range violationKeys(res) {
		uniq[k] = true
	}
	if !uniq["vat-standard-rate"] {
		t.Errorf("violated rule-key set = %v, want it to contain %q", sortedKeySet(uniq), "vat-standard-rate")
	}
	if len(uniq) != 1 {
		t.Errorf("violated rule-key set across the batch = %v, want EXACTLY {%q} -- an extra key means "+
			"this fixture trips a SECOND rule AC#5 does not expect", sortedKeySet(uniq), "vat-standard-rate")
		for _, iv := range res.InvoiceViolations {
			for _, v := range iv.Violations {
				if v.RuleKey != "vat-standard-rate" {
					t.Errorf("  invoice %s (id=%s): unexpected rule %q (severity=%s): %s",
						iv.InvoiceNumber, iv.InvoiceID, v.RuleKey, v.Severity, v.Message)
				}
			}
		}
	}

	// Pin attribution, not just the rule-key set -- see AC#4's note above.
	assertViolationsPinnedTo(t, res, "INV-SYN-00001")
}

// assertViolationsPinnedTo asserts exactly one InvoiceViolations entry,
// naming invoiceNumber.
func assertViolationsPinnedTo(t *testing.T, res BatchResult, invoiceNumber string) {
	t.Helper()
	if len(res.InvoiceViolations) != 1 || res.InvoiceViolations[0].InvoiceNumber != invoiceNumber {
		t.Errorf("InvoiceViolations = %+v, want exactly one entry for invoice_number %q", res.InvoiceViolations, invoiceNumber)
	}
}

// containsKey reports whether keys contains want.
func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

// sortedKeySet renders a rule-key set as a sorted slice for stable failure
// messages.
func sortedKeySet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- AC#6: oversized ----------------------------------------------------------

// TestFixtures_OversizedRejected413: a multipart body over maxUploadBytes
// (built from real green_500.csv content, repeated) must 413 before
// CreateHandler calls the injected imp closure. No DB needed -- the cap
// fires in ParseMultipartForm.
func TestFixtures_OversizedRejected413(t *testing.T) {
	data, err := os.ReadFile("../../testdata/invoices/green_500.csv")
	if err != nil {
		t.Fatalf("read green_500.csv: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("green_500.csv is empty, cannot inflate to exceed maxUploadBytes")
	}

	var big bytes.Buffer
	for big.Len() <= maxUploadBytes {
		big.Write(data)
	}
	if big.Len() <= maxUploadBytes {
		t.Fatalf("inflated body is %d bytes, want > %d (maxUploadBytes)", big.Len(), maxUploadBytes)
	}

	called := false
	stubImp := func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
		called = true
		return BatchResult{}, nil
	}

	mappingJSON, err := json.Marshal(stdMapping)
	if err != nil {
		t.Fatalf("marshal mapping: %v", err)
	}
	body, contentType := buildMultipartBody(t, uuid.NewString(), string(mappingJSON), "green_500_inflated.csv", "", big.Bytes())
	id := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
	rec, resp := doImportCreate(t, stubImp, &id, "", contentType, body)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a body over the 10 MiB cap (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
	if called {
		t.Error("stubImp was invoked -- the upload cap must reject the request BEFORE the importer ever runs")
	}
}
