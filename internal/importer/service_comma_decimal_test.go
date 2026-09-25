package importer

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const commaDecimalMsg = "uses a comma as the decimal mark"

func commaDecimalImport(t *testing.T, label string, rows [][]string, dryRun bool) (super *pgxpool.Pool, entityID string, res BatchResult) {
	t.Helper()
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, label+" tenant")
	entityID = seedEntity(t, super, tenantID, label+" entity")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	res, err := newTestService(app).Import(c, entityID, "", "", 1, stdMapping, stdHeader, rows, dryRun)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return super, entityID, res
}

// assertOneCommaError requires exactly one RowError with the given rows, field and the comma reason.
func assertOneCommaError(t *testing.T, errs []RowError, wantRows []int, wantField string) {
	t.Helper()
	if len(errs) != 1 {
		t.Fatalf("len(Errors) = %d, want 1: %+v", len(errs), errs)
	}
	re := errs[0]
	if got := rowNumbersOf(re); !intSliceEqual(got, wantRows) {
		t.Errorf("RowError rows = %v, want %v", got, wantRows)
	}
	if re.Field != wantField {
		t.Errorf("RowError.Field = %q, want %q", re.Field, wantField)
	}
	if !strings.Contains(re.Message, commaDecimalMsg) {
		t.Errorf("RowError.Message = %q, want it to contain %q", re.Message, commaDecimalMsg)
	}
}

func readNumericText(t *testing.T, super *pgxpool.Pool, query, id string) *string {
	t.Helper()
	var s *string
	if err := super.QueryRow(context.Background(), query, id).Scan(&s); err != nil {
		t.Fatalf("read back %q: %v", query, err)
	}
	return s
}

func TestIsCommaDecimal_Shapes(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"1.234,56", true},
		{"12,50", true},
		{"1,5", true},
		{"1,2345", true},
		{"1,234,56", true},
		{"1234,", true},
		{" 12,50 ", true},
		{"-1,50", true},
		{"1,234", false},
		{"1,234.56", false},
		{"1,234,567.89", false},
		{"1234.56", false},
		{"", false},
		{"   ", false},
		{"N/A", false},
		{"1234", false},
	}
	for _, tc := range cases {
		if got := isCommaDecimal(tc.raw); got != tc.want {
			t.Errorf("isCommaDecimal(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestServiceImport_CommaDecimalSubtotalQuarantinedNotSaved(t *testing.T) {
	rows := [][]string{
		mkRow("INV-EU1", "2026-01-10", "T1", "B1", "NGN", "1.234,56", "0.00", "1.234,56", "Item", "1", "1234.56"), // sheet 2
		mkRow("INV-OK", "2026-01-11", "T2", "B2", "NGN", "80.00", "8.00", "88.00", "Item", "1", "80.00"),          // sheet 3
	}
	super, entityID, res := commaDecimalImport(t, "COMMA-EU1", rows, false)

	if got := countInvoicesByNumber(t, super, entityID, "INV-EU1"); got != 0 {
		t.Errorf("INV-EU1 persisted = %d, want 0 (comma-decimal subtotal must never be saved)", got)
	}
	if got := countInvoicesByNumber(t, super, entityID, "INV-OK"); got != 1 {
		t.Errorf("INV-OK persisted = %d, want 1", got)
	}
	assertOneCommaError(t, res.Errors, []int{2}, "subtotal")
}

func TestServiceImport_CommaDecimalLineUnitPriceQuarantinesWholeGroup(t *testing.T) {
	rows := [][]string{
		mkRow("INV-EU2", "2026-01-10", "T1", "B1", "NGN", "22.50", "0.00", "22.50", "A", "1", "10.00"), // sheet 2
		mkRow("INV-EU2", "2026-01-10", "T1", "B1", "NGN", "22.50", "0.00", "22.50", "B", "1", "12,50"), // sheet 3
	}
	super, entityID, res := commaDecimalImport(t, "COMMA-EU2", rows, false)

	if got := countInvoicesByNumber(t, super, entityID, "INV-EU2"); got != 0 {
		t.Errorf("INV-EU2 persisted = %d, want 0 (one comma-decimal line holds back the whole invoice)", got)
	}
	assertOneCommaError(t, res.Errors, []int{2, 3}, "line_unit_price")
}

func TestServiceImport_CommaDecimalVATOneDigitQuarantined(t *testing.T) {
	rows := [][]string{
		mkRow("INV-EUV", "2026-01-10", "T1", "B1", "NGN", "20.00", "1,5", "21.50", "Item", "1", "20.00"),
	}
	super, entityID, res := commaDecimalImport(t, "COMMA-VAT", rows, false)

	if got := countInvoicesByNumber(t, super, entityID, "INV-EUV"); got != 0 {
		t.Errorf("INV-EUV persisted = %d, want 0", got)
	}
	assertOneCommaError(t, res.Errors, []int{2}, "vat")
}

// The sibling non-numeric check must not be what rejects the cell.
func TestServiceImport_CommaDecimalMessageIsNotTheNonNumericMessage(t *testing.T) {
	rows := [][]string{
		mkRow("INV-EUM", "2026-01-10", "T1", "B1", "NGN", "1.234,56", "0.00", "1.234,56", "Item", "1", "1234.56"),
	}
	_, _, res := commaDecimalImport(t, "COMMA-MSG", rows, false)

	if len(res.Errors) == 0 {
		t.Fatalf("Errors is empty, want the comma-decimal RowError")
	}
	for _, re := range res.Errors {
		if strings.Contains(re.Message, "is not a valid number") {
			t.Errorf("RowError.Message = %q, must not be the non-numeric reason", re.Message)
		}
		if !strings.Contains(re.Message, commaDecimalMsg) {
			t.Errorf("RowError.Message = %q, want it to contain %q", re.Message, commaDecimalMsg)
		}
	}
}

func TestServiceImport_GroupedAndPlainAmountsImportUnchanged(t *testing.T) {
	rows := [][]string{
		mkRow("INV-G1", "2026-01-10", "T1", "B1", "NGN", "1,234.56", "0.00", "1,234.56", "Item", "1", "1,234.56"),
		mkRow("INV-G2", "2026-01-10", "T1", "B1", "NGN", "1,234", "0.00", "1,234", "Item", "1", "1234"),
		mkRow("INV-G3", "2026-01-10", "T1", "B1", "NGN", "1234.56", "0.00", "1234.56", "Item", "1", "1234.56"),
		mkRow("INV-G4", "2026-01-10", "T1", "B1", "NGN", "100.00", "  ", "100.00", "Item", "1", "100.00"),
	}
	super, entityID, res := commaDecimalImport(t, "COMMA-UNCHANGED", rows, false)

	if res.ReadyInvoices != 4 || res.QuarantinedInvoices != 0 || len(res.Errors) != 0 {
		t.Fatalf("(Ready=%d Quarantined=%d Errors=%+v), want (4,0,[])", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}
	if res.RowsTotal != 4 || res.RowsValid != 4 || res.RowsInvalid != 0 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (4,4,0)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	for num, want := range map[string]string{"INV-G1": "1234.56", "INV-G2": "1234.00", "INV-G3": "1234.56"} {
		got := invoiceSubtotal(t, super, invoiceIDByNumber(t, super, entityID, num))
		if got == nil || *got != want {
			t.Errorf("%s subtotal = %v, want %q", num, got, want)
		}
	}
	g1 := invoiceIDByNumber(t, super, entityID, "INV-G1")
	if got := readNumericText(t, super, `SELECT unit_price::text FROM line_items WHERE invoice_id = $1`, g1); got == nil || *got != "1234.56" {
		t.Errorf("INV-G1 unit_price = %v, want \"1234.56\"", got)
	}
	g4 := invoiceIDByNumber(t, super, entityID, "INV-G4")
	if got := readNumericText(t, super, `SELECT vat::text FROM invoices WHERE id = $1`, g4); got != nil {
		t.Errorf("INV-G4 vat = %q, want NULL (blank cell)", *got)
	}
}

func commaDecimalMixedFixture() [][]string {
	return [][]string{
		mkRow("INV-MX1", "2026-01-10", "T1", "B1", "NGN", "1.234,56", "0.00", "1.234,56", "Item", "1", "1234.56"), // sheet 2
		mkRow("INV-MX2", "2026-01-10", "T1", "B1", "NGN", "22.50", "0.00", "22.50", "A", "1", "10.00"),            // sheet 3
		mkRow("INV-MX2", "2026-01-10", "T1", "B1", "NGN", "22.50", "0.00", "22.50", "B", "1", "12,50"),            // sheet 4
		mkRow("INV-MX3", "2026-01-11", "T2", "B2", "NGN", "80.00", "8.00", "88.00", "Item", "1", "80.00"),         // sheet 5
		mkRow("INV-MX4", "2026-01-12", "T3", "B3", "NGN", "1,234.56", "0.00", "1,234.56", "Item", "1", "1234.56"), // sheet 6
	}
}

type rowErrorKey struct {
	Rows    string
	Field   string
	Message string
}

func rowErrorKeys(errs []RowError) []rowErrorKey {
	out := make([]rowErrorKey, 0, len(errs))
	for _, e := range errs {
		out = append(out, rowErrorKey{Rows: fmt.Sprint(rowNumbersOf(e)), Field: e.Field, Message: e.Message})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rows < out[j].Rows })
	return out
}

func TestServiceImport_CommaDecimalDryRunMatchesRealVerdict(t *testing.T) {
	super, dryEntity, dryRes := commaDecimalImport(t, "COMMA-PARITY-DRY", commaDecimalMixedFixture(), true)
	if got := countImportBatchesForEntity(t, super, dryEntity); got != 0 {
		t.Errorf("dry-run wrote %d import_batches rows, want 0", got)
	}
	if got := countInvoicesForEntity(t, super, dryEntity); got != 0 {
		t.Errorf("dry-run wrote %d invoices rows, want 0", got)
	}
	_, _, realRes := commaDecimalImport(t, "COMMA-PARITY-REAL", commaDecimalMixedFixture(), false)

	// Parity alone passes when neither path quarantines, so pin the verdict too.
	for name, res := range map[string]BatchResult{"dry-run": dryRes, "real": realRes} {
		if res.RowsTotal != 5 || res.RowsValid != 2 || res.RowsInvalid != 3 || res.ReadyInvoices != 2 || res.QuarantinedInvoices != 2 {
			t.Errorf("%s counters (total=%d valid=%d invalid=%d ready=%d quarantined=%d), want (5,2,3,2,2)",
				name, res.RowsTotal, res.RowsValid, res.RowsInvalid, res.ReadyInvoices, res.QuarantinedInvoices)
		}
	}
	if dryRes.RowsTotal != realRes.RowsTotal || dryRes.RowsValid != realRes.RowsValid || dryRes.RowsInvalid != realRes.RowsInvalid ||
		dryRes.ReadyInvoices != realRes.ReadyInvoices || dryRes.QuarantinedInvoices != realRes.QuarantinedInvoices {
		t.Errorf("dry-run counters %+v differ from real %+v", dryRes, realRes)
	}

	dryKeys, realKeys := rowErrorKeys(dryRes.Errors), rowErrorKeys(realRes.Errors)
	if len(dryKeys) != 2 {
		t.Fatalf("dry-run Errors = %+v, want 2 comma-decimal errors", dryRes.Errors)
	}
	for _, k := range dryKeys {
		if !strings.Contains(k.Message, commaDecimalMsg) {
			t.Errorf("dry-run error %+v, want the comma reason", k)
		}
	}
	if !reflect.DeepEqual(dryKeys, realKeys) {
		t.Errorf("error sets differ:\n dry-run %+v\n real    %+v", dryKeys, realKeys)
	}
}

func TestServiceImport_CommaDecimalPartialSuccessCounters(t *testing.T) {
	rows := [][]string{
		mkRow("INV-P1", "2026-01-10", "T1", "B1", "NGN", "10.00", "0.00", "10.00", "Item", "1", "10.00"), // sheet 2
		mkRow("INV-P2", "2026-01-10", "T1", "B1", "NGN", "22.50", "0.00", "22.50", "A", "1", "12,50"),    // sheet 3
		mkRow("INV-P2", "2026-01-10", "T1", "B1", "NGN", "22.50", "0.00", "22.50", "B", "1", "10.00"),    // sheet 4
		mkRow("INV-P3", "2026-01-11", "T2", "B2", "NGN", "80.00", "8.00", "88.00", "Item", "1", "80.00"), // sheet 5
	}
	super, entityID, res := commaDecimalImport(t, "COMMA-PARTIAL", rows, false)

	if res.Status != "completed" {
		t.Errorf("Status = %q, want %q", res.Status, "completed")
	}
	if res.ReadyInvoices != 2 || res.QuarantinedInvoices != 1 {
		t.Errorf("(Ready=%d Quarantined=%d), want (2,1)", res.ReadyInvoices, res.QuarantinedInvoices)
	}
	if res.RowsTotal != 4 || res.RowsInvalid != 2 || res.RowsValid != res.RowsTotal-2 {
		t.Errorf("counts = (total=%d valid=%d invalid=%d), want (4,2,2)", res.RowsTotal, res.RowsValid, res.RowsInvalid)
	}
	for num, want := range map[string]int{"INV-P1": 1, "INV-P2": 0, "INV-P3": 1} {
		if got := countInvoicesByNumber(t, super, entityID, num); got != want {
			t.Errorf("%s persisted = %d, want %d", num, got, want)
		}
	}
}

// "1250" and "12,50" normalize to the same string, so only a raw-cell check on every row sees it.
func TestServiceImport_CommaDecimalInLaterRowOfHeaderFieldQuarantined(t *testing.T) {
	rows := [][]string{
		mkRow("INV-EU3", "2026-01-10", "T1", "B1", "NGN", "1250", "0.00", "1250.00", "A", "1", "625.00"),  // sheet 2
		mkRow("INV-EU3", "2026-01-10", "T1", "B1", "NGN", "12,50", "0.00", "1250.00", "B", "1", "625.00"), // sheet 3
	}
	super, entityID, res := commaDecimalImport(t, "COMMA-EU3", rows, false)

	if got := countInvoicesByNumber(t, super, entityID, "INV-EU3"); got != 0 {
		t.Errorf("INV-EU3 persisted = %d, want 0", got)
	}
	assertOneCommaError(t, res.Errors, []int{2, 3}, "subtotal")
}

func TestServiceImport_CommaDecimalBeatsHeaderConflictReason(t *testing.T) {
	rows := [][]string{
		mkRow("INV-EU4", "2026-01-10", "T1", "B1", "NGN", "1.234,56", "0.00", "1234.56", "A", "1", "1000.00"), // sheet 2
		mkRow("INV-EU4", "2026-01-10", "T1", "B1", "NGN", "1234.56", "0.00", "1234.56", "B", "1", "234.56"),   // sheet 3
	}
	_, _, res := commaDecimalImport(t, "COMMA-EU4", rows, false)

	if len(res.Errors) == 0 {
		t.Fatalf("Errors is empty, want one RowError for INV-EU4")
	}
	for _, re := range res.Errors {
		if strings.Contains(re.Message, "rows disagree") {
			t.Errorf("RowError.Message = %q, want the comma reason, not the header-conflict reason", re.Message)
		}
	}
	assertOneCommaError(t, res.Errors, []int{2, 3}, "subtotal")
}
