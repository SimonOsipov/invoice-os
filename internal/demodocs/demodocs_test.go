// Pure Go, no DB, no t.Skip gate — the file-building half of the package.
// The DB-backed half lives in store_test.go behind the usual per-role DSNs.
package demodocs

import (
	"encoding/csv"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func row(entity, invoiceID, number, item string) lineRow {
	return lineRow{
		entityName: entity, invoiceID: invoiceID, invoiceNumber: number,
		issueDate: "2026-06-02", buyerTIN: "20011122-0001", buyerName: "Zenith Freight",
		currency: "NGN", subtotal: "500000.00", vat: "37500.00", total: "537500.00",
		itemDesc: item, qty: "1", unitPrice: "500000.00",
	}
}

// The header is the importer's own auto-recognised column set, and the first
// data row must land on sheet row 2 — importer.sheetRow and the
// invoices_source_rows_are_sheet_rows CHECK both start there.
func TestBuildCSV_HeaderThenRowsInOrder(t *testing.T) {
	body, _ := buildCSV([]lineRow{
		row("Adeyemi", "inv-1", "DEMO-2026-1001", "Consulting"),
		row("Adeyemi", "inv-2", "DEMO-2026-1002", "Fabric rolls"),
	})

	records, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("generated file is not valid CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3 (header + 2 rows)", len(records))
	}
	if got := strings.Join(records[0], ","); got != csvHeader {
		t.Errorf("header = %q, want %q", got, csvHeader)
	}
	if records[1][0] != "DEMO-2026-1001" || records[2][0] != "DEMO-2026-1002" {
		t.Errorf("row order = %q/%q, want DEMO-2026-1001 then -1002", records[1][0], records[2][0])
	}
	// Every data row carries all 11 columns; a short row would make the
	// previewer's column count disagree with its header.
	for i, r := range records {
		if len(r) != 11 {
			t.Errorf("record %d has %d fields, want 11", i, len(r))
		}
	}
}

// A multi-line-item invoice occupies consecutive sheet rows, and the numbers
// are the file's own — the whole promise of Core AC #3.
func TestBuildCSV_SheetRowsAreTheFilesOwnRowNumbers(t *testing.T) {
	_, sheetRows := buildCSV([]lineRow{
		row("Adeyemi", "inv-1", "DEMO-2026-1001", "Consulting"),     // sheet row 2
		row("Adeyemi", "inv-1", "DEMO-2026-1001", "Implementation"), // sheet row 3
		row("Adeyemi", "inv-2", "DEMO-2026-1002", "Fabric rolls"),   // sheet row 4
	})

	if want := []int{2, 3}; !reflect.DeepEqual(sheetRows["inv-1"], want) {
		t.Errorf("sheetRows[inv-1] = %v, want %v", sheetRows["inv-1"], want)
	}
	if want := []int{4}; !reflect.DeepEqual(sheetRows["inv-2"], want) {
		t.Errorf("sheetRows[inv-2] = %v, want %v", sheetRows["inv-2"], want)
	}
}

// The sheet row must index the row it actually wrote. Asserting the mapping
// against the parsed file — rather than against a literal — is what catches an
// off-by-one that a hand-written expectation would simply agree with.
func TestBuildCSV_SheetRowIndexesTheRowItDescribes(t *testing.T) {
	rows := []lineRow{
		row("Adeyemi", "inv-1", "DEMO-2026-1001", "Consulting"),
		row("Adeyemi", "inv-1", "DEMO-2026-1001", "Implementation"),
		row("Adeyemi", "inv-2", "DEMO-2026-1002", "Fabric rolls"),
	}
	body, sheetRows := buildCSV(rows)
	records, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, r := range rows {
		for _, sr := range sheetRows[r.invoiceID] {
			if sr < firstDataSheetRow || sr > len(records) {
				t.Fatalf("sheet row %d for %s is outside the file (%d records)", sr, r.invoiceID, len(records))
			}
			// records is 0-indexed and includes the header, so sheet row N is
			// records[N-1].
			if got := records[sr-1][0]; got != r.invoiceNumber {
				t.Errorf("sheet row %d holds invoice %q, but is mapped to %q", sr, got, r.invoiceNumber)
			}
		}
	}
}

// Buyer names in the seed contain commas ("Adeyemi & Sons Trading Ltd" does
// not, but a real one will). encoding/csv quotes them; a hand-rolled
// strings.Join would silently shift every later column.
func TestBuildCSV_QuotesFieldsContainingSeparators(t *testing.T) {
	r := row("Adeyemi", "inv-1", "DEMO-2026-1001", "Consulting, onsite")
	r.buyerName = `Zenith Freight & Logistics, Ltd`
	body, _ := buildCSV([]lineRow{r})

	records, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := records[1][3]; got != r.buyerName {
		t.Errorf("buyer round-tripped as %q, want %q", got, r.buyerName)
	}
	if got := records[1][8]; got != r.itemDesc {
		t.Errorf("item round-tripped as %q, want %q", got, r.itemDesc)
	}
}

// Identical input must produce identical bytes: the content-hash dedup is the
// only thing making a re-run idempotent rather than duplicating every document.
func TestBuildCSV_IsDeterministic(t *testing.T) {
	rows := []lineRow{
		row("Adeyemi", "inv-1", "DEMO-2026-1001", "Consulting"),
		row("Adeyemi", "inv-2", "DEMO-2026-1002", "Fabric rolls"),
	}
	first, _ := buildCSV(rows)
	second, _ := buildCSV(rows)
	if string(first) != string(second) {
		t.Error("buildCSV is not deterministic; the content-hash dedup would store a new document every run")
	}
}

func TestGroupByEntity_SplitsOnEntityBoundary(t *testing.T) {
	groups := groupByEntity([]lineRow{
		row("Adeyemi", "inv-1", "DEMO-2026-1001", "a"),
		row("Adeyemi", "inv-2", "DEMO-2026-1002", "b"),
		row("Chukwu", "inv-3", "DEMO-2026-2001", "c"),
	})

	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	if groups[0].entityName != "Adeyemi" || len(groups[0].rows) != 2 {
		t.Errorf("group 0 = %q with %d rows, want Adeyemi with 2", groups[0].entityName, len(groups[0].rows))
	}
	if groups[1].entityName != "Chukwu" || len(groups[1].rows) != 1 {
		t.Errorf("group 1 = %q with %d rows, want Chukwu with 1", groups[1].entityName, len(groups[1].rows))
	}
}

// pendingRows orders by entity name, so a repeat of an earlier name cannot
// occur — but if the ORDER BY is ever dropped, two files for one supplier is
// the correct outcome, not one file with interleaved rows.
func TestGroupByEntity_DoesNotMergeNonAdjacentRepeats(t *testing.T) {
	groups := groupByEntity([]lineRow{
		row("Adeyemi", "inv-1", "DEMO-2026-1001", "a"),
		row("Chukwu", "inv-2", "DEMO-2026-2001", "b"),
		row("Adeyemi", "inv-3", "DEMO-2026-1002", "c"),
	})
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3 (adjacent-run grouping, not a set)", len(groups))
	}
}

func TestGroupByEntity_EmptyInputYieldsNoGroups(t *testing.T) {
	if got := groupByEntity(nil); len(got) != 0 {
		t.Errorf("groupByEntity(nil) = %v, want no groups", got)
	}
}

func TestFilenameFor(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Adeyemi & Sons Trading Ltd", "adeyemi-sons-trading-ltd-invoices.csv"},
		{"Okonkwo Textiles Nigeria Ltd", "okonkwo-textiles-nigeria-ltd-invoices.csv"},
		{"  ", "supplier-invoices.csv"},
		{"???", "supplier-invoices.csv"},
	} {
		if got := filenameFor(tc.in); got != tc.want {
			t.Errorf("filenameFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The allowlist is the safety boundary — it is what makes running this against
// production reach demo data only. Pin it to db/seed.dev.sql rather than to a
// literal here: a tenant added to the seed without being added to the list
// would silently never get documents, and a uuid added here that the seed does
// not create is a tenant this package has no business touching.
func TestDemoTenants_AreExactlyTheSeedFilesTenants(t *testing.T) {
	seed, err := os.ReadFile("../../db/seed.dev.sql")
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	block := regexp.MustCompile(`(?s)INSERT INTO tenants \([^)]*\) VALUES(.*?);`).FindSubmatch(seed)
	if block == nil {
		t.Fatal("no INSERT INTO tenants block in db/seed.dev.sql — this test no longer pins anything")
	}
	found := regexp.MustCompile(`'([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})'`).
		FindAllSubmatch(block[1], -1)
	if len(found) == 0 {
		t.Fatal("no tenant uuids matched inside the seed's tenants block")
	}

	var fromSeed []string
	for _, m := range found {
		fromSeed = append(fromSeed, string(m[1]))
	}
	got := append([]string(nil), DemoTenants...)
	sort.Strings(fromSeed)
	sort.Strings(got)

	if !reflect.DeepEqual(got, fromSeed) {
		t.Errorf("DemoTenants = %v\nseed tenants = %v\nthe allowlist must match db/seed.dev.sql exactly", got, fromSeed)
	}
}
