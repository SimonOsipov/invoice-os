// M4-11-04 (task-196): DB-backed edge-case fixture verification. M4-11-01/02
// generated and committed testdata/invoices/edge_*.csv as fixtures that are
// each SUPPOSED to trip exactly one intended failure -- one structural
// (pre-write) rejection, or one clean-import-but-VALIDATE-rejects rule
// violation, or (for oversized) the HTTP upload cap -- with the OTHER 19 (or
// 4) invoices in the same file staying unaffected. Nothing before this file
// ever ran a committed edge CSV through the real pipeline: closest is
// tools/fixturegen's own gen_test.go/gen_adversarial_test.go, which only
// check the BYTES the generator produced (header shape, mutated cell values),
// never Service.Import or CreateHandler. These tests are that missing proof,
// mirroring fixtures_green_test.go's (M4-11-03) real-pipeline pattern: read
// the committed bytes -> Decode -> seed a tenant+entity via the REAL
// portfolio.Store.Create path (createEntityViaRealPortfolioStore,
// service_tin_test.go) -> Service.Import wired to a REAL in-process rule-set
// 04 (startInProcess04ForImporter, service_gate_test.go) on the ACTIVE v2
// rule set.
//
// If a fixture does NOT trip its intended failure -- or trips an EXTRA one --
// that is a fixturegen bug (M4-11-01), not a test bug: these tests must not
// be weakened to make a fixture pass, and the committed CSVs must not be
// hand-edited (M4-11-02's no-hand-edit guard).
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

// importEdgeFixture is importGreenFixture's (fixtures_green_test.go) twin for
// an edge fixture: read path -> Decode -> a fresh tenant + entity through the
// REAL portfolio.Store path -> Service.Import (dryRun=false) through a REAL
// in-process rule-set 04. Unlike importGreenFixture, it RETURNS err instead
// of t.Fatal-ing on it: AC#1/#3 below expect Import to fail (ErrValidation),
// and the caller needs that error value to assert on.
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

// assertNothingWrittenForEntity pins AC#1/#3's "nothing written" invariant --
// zero import_batches/invoices rows for entityID, read back via the
// superuser pool exactly like TestCreateHandler_DryRun200NothingPersisted
// (handlers_test.go) checks a dry run's non-write.
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

// assertPreWriteRejected is importEdgeFixture's twin for AC#1/#3's shared
// "rejected before any write" shape: err must be ErrValidation, res must be
// the zero-value BatchResult, and nothing may have been written for the
// seeded entity. reason names WHY this particular fixture is expected to
// fail (appended to the errors.Is failure message); label is the short
// fixture name used in every failure message, including
// assertNothingWrittenForEntity's own label parameter.
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

// TestFixtures_MissingColumnsRejectedPreWrite is task-196 AC#1: the committed
// edge_missing_columns.csv (tools/fixturegen's buildEdgeMissingColumns) drops
// the "Total" column entirely, but stdMapping -- the full 11-key canonical
// mapping every other test in this package imports against -- still maps
// total -> "Total". resolveMapping (service.go) must reject that BEFORE any
// write: the mapped header string "Total" is absent from the file's header
// row.
func TestFixtures_MissingColumnsRejectedPreWrite(t *testing.T) {
	assertPreWriteRejected(t, "../../testdata/invoices/edge_missing_columns.csv",
		"M4-11-04 missing-columns tenant", "M4-11-04 missing-columns entity",
		"edge_missing_columns.csv",
		"the file's header has no \"Total\" column, but stdMapping maps field \"total\" to header \"Total\"")
}

// --- AC#2: edge_in_file_dupes -----------------------------------------------

// TestFixtures_InFileDupesQuarantined is task-196 AC#2: the committed
// edge_in_file_dupes.csv (tools/fixturegen's buildEdgeInFileDupes) has 5
// invoice_number groups, one of which (INV-SYN-00001) carries a 4th row that
// repeats every header field verbatim EXCEPT Issue Date -- an in-file header
// conflict ([dedup]). Import must complete (this is not a pre-write
// rejection: 4 of the 5 groups are perfectly ready), quarantine exactly that
// one group, and report a RowError whose message names the conflicting
// field.
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

// TestFixtures_BadEncodingRejected is task-196 AC#3: the committed
// edge_bad_encoding.csv (tools/fixturegen's buildEdgeBadEncoding) is UTF-16LE
// WITHOUT a byte-order-mark, with its first buyer name forced non-ASCII so
// the raw bytes are provably not valid UTF-8 (gen.go's own doc comment).
// Decode's BOM/charset sniff therefore falls past both the UTF-16LE-BOM
// branch (no BOM present) and the utf8.Valid branch (fails), landing on the
// windows-1252 tolerant fallback -- which decodes UTF-16LE's embedded NUL
// bytes as literal control characters, mangling the header row so no
// stdMapping header string is found. resolveMapping must reject that BEFORE
// any write, exactly like AC#1.
func TestFixtures_BadEncodingRejected(t *testing.T) {
	assertPreWriteRejected(t, "../../testdata/invoices/edge_bad_encoding.csv",
		"M4-11-04 bad-encoding tenant", "M4-11-04 bad-encoding entity",
		"edge_bad_encoding.csv",
		"UTF-16LE-without-BOM content tolerantly decoded as windows-1252 must mangle the header row past recognition")
}

// --- AC#4: edge_bad_tin ------------------------------------------------------

// TestFixtures_BadTinImportsCleanValidateRejects is task-196 AC#4: the
// committed edge_bad_tin.csv (tools/fixturegen's buildEdgeBadTin) mutates
// exactly INV-SYN-00001's Buyer TIN to "1234567-8901" (7 digits before the
// dash, failing the seeded buyer-tin-format rule's ^[0-9]{8}-[0-9]{4}$
// shape); the other 19 invoices keep well-formed TINs. A buyer TIN is never
// import-time-checked (it is not one of the 5 numeric header fields, nor
// does classify parse it) -- it can only be caught by the REAL 04 gate at
// validate time. So Import must complete with ZERO quarantine (this is not a
// structural defect) and report buyer-tin-format as a violation on exactly
// that one invoice.
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

	// QA (M4-11-04 verify): InvoicesWithViolations==1 alone would also pass
	// if the WRONG invoice carried the violation (e.g. a rule-key regression
	// that misattributes buyer-tin-format to a clean row's group instead of
	// buildEdgeBadTin's actual mutated INV-SYN-00001) -- pin the attribution,
	// not just the count.
	assertViolationsPinnedTo(t, res, "INV-SYN-00001")
}

// --- AC#5: edge_vat_math_wrong -----------------------------------------------

// TestFixtures_VatMathWrongImportsCleanValidateRejects is task-196 AC#5: the
// committed edge_vat_math_wrong.csv (tools/fixturegen's buildEdgeVatMathWrong)
// mutates exactly INV-SYN-00001's VAT to 0.00 while its subtotal/line items
// still reconcile normally -- failing the seeded vat-standard-rate rule
// (rate=0.075, tolerance=0.005) -- and recomputes Total = Subtotal (v2 has no
// separate total=subtotal+vat rule, so that is not a SECOND intended defect);
// the other 19 invoices keep normal reconciled VAT. Like AC#4, this is a
// VALIDATE-time-only defect: Import must complete with zero quarantine.
//
// This is the LOAD-BEARING single-rule oracle: the violated rule-key set
// across the WHOLE batch must be EXACTLY {"vat-standard-rate"} -- not merely
// "contains" it. If any other invoice or any other rule also fires (e.g. the
// generator's Total=Subtotal recompute accidentally tripping a rule this AC
// does not expect), that is a real fixturegen/rule-engine defect, and the
// failure message below names the offending invoice + rule precisely so it
// is diagnosable without re-running anything.
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

	// QA (M4-11-04 verify): the single-rule-key check above is batch-wide and
	// would also pass if the vat-standard-rate violation landed on the WRONG
	// invoice -- pin the attribution to buildEdgeVatMathWrong's actual
	// mutated INV-SYN-00001, not just the rule-key set.
	assertViolationsPinnedTo(t, res, "INV-SYN-00001")
}

// assertViolationsPinnedTo pins the AC#4/AC#5 "attribution, not just count"
// invariant: exactly one InvoiceViolations entry, and it names
// invoiceNumber -- not just any invoice carrying a violation.
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

// sortedKeySet renders a rule-key set as a sorted slice, for a stable and
// readable failure message.
func sortedKeySet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- AC#6: oversized ----------------------------------------------------------

// TestFixtures_OversizedRejected413 is task-196 AC#6: a multipart body whose
// "file" part alone exceeds maxUploadBytes (10 MiB, [upload-cap]) must 413
// before CreateHandler ever calls the injected imp closure -- proved here
// with REAL invoice CSV content (green_500.csv, repeated until the body
// clears the cap) rather than junk bytes, so this exercises exactly the
// shape a firm uploading a genuinely oversized spreadsheet would hit. No DB
// needed: the cap fires in ParseMultipartForm, before Decode or Service.Import
// are ever reached.
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
