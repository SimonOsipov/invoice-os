package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// lineQueryTx records the id count of every selectLineItemsSQL statement.
type lineQueryTx struct {
	pgx.Tx
	sizes []int
}

func (r *lineQueryTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if sql == selectLineItemsSQL {
		r.sizes = append(r.sizes, len(args[0].([]string)))
	}
	return r.Tx.Query(ctx, sql, args...)
}

func TestSelectLineItems_ExportsLegacyAmountsAsStoredText(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-lineitems-amounts")
	entity := mustCreateEntity(t, tx, tenant, "Amounts Co", "90000010-0001")
	inv := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-LI-AMT"})
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total, line_tax, tax_percent, base_quantity)
		VALUES ($1, $2, 1, 'Widget', 2.5, 10, 25, 1.88, 7.5, 3)`, tenant, inv); err != nil {
		t.Fatalf("insert line: %v", err)
	}

	actingAs(t, tx, tenant)
	rows := runSelectLineItems(t, tx, []string{inv})
	if len(rows) != 2 {
		t.Fatalf("line_items.csv has %d records, want 2", len(rows))
	}
	// Four distinct legacy values and the two NRS numerics: a swapped or dropped column shows.
	want := map[string]string{
		"quantity": "2.500", "unit_price": "10.00", "line_total": "25.00", "line_tax": "1.88",
		"tax_percent": "7.50", "base_quantity": "3.000", "line_no": "1", "description": "Widget",
	}
	for col, v := range want {
		if got := rows[1][lineItemsIndex(t, col)]; got != v {
			t.Errorf("%s = %q, want %q", col, got, v)
		}
	}
}

func TestSelectLineItems_FreeTextRoundTripsThroughTheBundle(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-lineitems-hostile")
	entity := mustCreateEntity(t, tx, tenant, "Hostile Co", "90000011-0001")
	from := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
	inv := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-LI-HOSTILE", createdAt: from})

	hostile := map[string]string{
		"description":  "a,b \"quoted\"\nsecond line, with comma",
		"tax_category": "=SUM(A1)", "hsn_code": "+1", "isic_code": "-2", "product_category": "@x",
		"service_category": "tab\there", "sellers_item_identification": ",,\"\"", "price_unit": " lead and trail ",
	}
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO line_items (tenant_id, invoice_id, line_no, description, tax_category, hsn_code, isic_code,
		       product_category, service_category, sellers_item_identification, price_unit)
		VALUES ($1, $2, 1, $3, $4, $5, $6, $7, $8, $9, $10)`, tenant, inv,
		hostile["description"], hostile["tax_category"], hostile["hsn_code"], hostile["isic_code"],
		hostile["product_category"], hostile["service_category"], hostile["sellers_item_identification"], hostile["price_unit"]); err != nil {
		t.Fatalf("insert line: %v", err)
	}
	if _, err := tx.Exec(context.Background(), `
		UPDATE invoices SET supplier_street = $2, buyer_city = $3, supplier_email = $4 WHERE id = $1`,
		inv, "1 Main St,\n\"Annex\"", "=HYPERLINK(\"x\")", "a,b@x"); err != nil {
		t.Fatalf("update invoice: %v", err)
	}

	actingAs(t, tx, tenant)
	bundle := mustAssembleBundle(t, tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)},
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: 10, now: time.Now()})
	zr := mustReadZip(t, bundle)

	lines := parseCSV(t, mustReadZipEntry(t, zr, "line_items.csv"))
	if len(lines) != 2 {
		t.Fatalf("line_items.csv has %d records, want 2", len(lines))
	}
	if len(lines[1]) != len(lineItemsCSVHeader) {
		t.Fatalf("row has %d fields, want %d", len(lines[1]), len(lineItemsCSVHeader))
	}
	for col, v := range hostile {
		if got := lines[1][lineItemsIndex(t, col)]; got != v {
			t.Errorf("line %s = %q, want %q", col, got, v)
		}
	}

	invs := parseCSV(t, mustReadZipEntry(t, zr, "invoices.csv"))
	if len(invs) != 2 || len(invs[1]) != len(invoicesCSVHeader) {
		t.Fatalf("invoices.csv records = %d (fields %d), want 2 x %d", len(invs), len(invs[len(invs)-1]), len(invoicesCSVHeader))
	}
	for col, v := range map[string]string{"supplier_street": "1 Main St,\n\"Annex\"", "buyer_city": "=HYPERLINK(\"x\")", "supplier_email": "a,b@x"} {
		if got := invs[1][headerIndex(t, col)]; got != v {
			t.Errorf("invoice %s = %q, want %q", col, got, v)
		}
	}
}

func TestRLS_SelectLineItemsMixedTenantIDsReturnOnlyTheActingTenantsLines(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-lineitems-mix-a")
	tenantB := mustCreateTenant(t, tx, "archive-lineitems-mix-b")
	entityA := mustCreateEntity(t, tx, tenantA, "Mix A Co", "90000012-0001")
	entityB := mustCreateEntity(t, tx, tenantB, "Mix B Co", "90000012-0002")
	invA := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantA, entityID: entityA, invoiceNumber: "INV-LI-MIX-A"})
	invB := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantB, entityID: entityB, invoiceNumber: "INV-LI-MIX-B"})
	for i := 1; i <= 2; i++ {
		mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenantA, invoiceID: invA, lineNo: i})
	}
	mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenantB, invoiceID: invB, lineNo: 1})

	both := []string{invA, invB}
	for _, c := range []struct {
		name, tenant, wantInv string
		wantLines             int
	}{{"A", tenantA, invA, 2}, {"B", tenantB, invB, 1}} {
		actingAs(t, tx, c.tenant)
		rows := runSelectLineItems(t, tx, both)
		if len(rows)-1 != c.wantLines {
			t.Errorf("tenant %s: %d data rows, want %d", c.name, len(rows)-1, c.wantLines)
		}
		for _, r := range rows[1:] {
			if r[0] != c.wantInv {
				t.Errorf("tenant %s read a line of invoice %s", c.name, r[0])
			}
		}
		counts, err := countChildren(context.Background(), tx, both)
		if err != nil {
			t.Fatalf("countChildren: %v", err)
		}
		if counts.LineItems != c.wantLines {
			t.Errorf("tenant %s: countChildren LineItems = %d, want %d", c.name, counts.LineItems, c.wantLines)
		}
	}

	actingAsRoleOnly(t, tx)
	if _, err := tx.Exec(context.Background(), `SELECT set_config('app.current_tenant', '', true)`); err != nil {
		t.Fatalf("clear GUC: %v", err)
	}
	if rows := runSelectLineItems(t, tx, both); len(rows) != 1 {
		t.Errorf("no tenant GUC: %d records, want 1 (header only)", len(rows))
	}
	if counts, err := countChildren(context.Background(), tx, both); err != nil || counts.LineItems != 0 {
		t.Errorf("no tenant GUC: countChildren LineItems = %d (err %v), want 0", counts.LineItems, err)
	}
}

// TestLineItems_ChunkBoundariesAgreeAcrossDownloadAndPreview: 1001 invoices x 2 lines.
// created_at ascends while ids descend, so a chunk boundary cannot hide behind id order.
func TestLineItems_ChunkBoundariesAgreeAcrossDownloadAndPreview(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-lineitems-boundaries")
	entity := mustCreateEntity(t, tx, tenant, "Boundary Co", "90000013-0001")
	base := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)

	const total = 1001
	ids := make([]string, total)
	for i := range ids {
		ids[i] = mustCreateInvoice(t, tx, invoiceFixture{
			id: fmt.Sprintf("00000000-0000-4000-8000-%012x", total-i), tenantID: tenant, entityID: entity,
			invoiceNumber: fmt.Sprintf("INV-LI-B-%04d", i), createdAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO line_items (tenant_id, invoice_id, line_no)
		SELECT $1, i, n FROM unnest($2::uuid[]) AS i, generate_series(1, 2) AS n`, tenant, ids); err != nil {
		t.Fatalf("insert lines: %v", err)
	}
	actingAs(t, tx, tenant)

	for _, n := range []int{1, 499, 500, 501, 1000, 1001} {
		rec := &lineQueryTx{Tx: tx}
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		if err := selectLineItems(context.Background(), rec, ids[:n], w); err != nil {
			t.Fatalf("n=%d: selectLineItems: %v", n, err)
		}
		w.Flush()
		rows := parseCSV(t, buf.Bytes())[1:]

		if len(rows) != 2*n {
			t.Errorf("n=%d: %d data rows, want %d", n, len(rows), 2*n)
		}
		if want := (n + 499) / 500; len(rec.sizes) != want {
			t.Errorf("n=%d: %d line queries, want %d", n, len(rec.sizes), want)
		}
		for _, s := range rec.sizes {
			if s > 500 {
				t.Errorf("n=%d: a line query carried %d ids, want at most 500", n, s)
			}
		}
		// Each invoice is one contiguous run of line_no 1, 2.
		seen := map[string]bool{}
		for i := 0; i+1 < len(rows); i += 2 {
			a, b := rows[i], rows[i+1]
			if a[0] != b[0] || a[2] != "1" || b[2] != "2" {
				t.Fatalf("n=%d: rows %d-%d are (%s,%s) and (%s,%s), want one invoice's lines 1 and 2", n, i, i+1, a[0], a[2], b[0], b[2])
			}
			if seen[a[0]] {
				t.Errorf("n=%d: invoice %s appears in two runs", n, a[0])
			}
			seen[a[0]] = true
		}
		if len(seen) != n {
			t.Errorf("n=%d: %d distinct invoices, want %d", n, len(seen), n)
		}
		// The whole file is ordered by invoice_id, line_no.
		if !sort.SliceIsSorted(rows, func(i, j int) bool {
			if rows[i][0] != rows[j][0] {
				return rows[i][0] < rows[j][0]
			}
			return rows[i][2] < rows[j][2]
		}) {
			t.Errorf("n=%d: line_items.csv is not ordered by invoice_id, line_no", n)
		}
	}

	r := Request{EntityID: entity, From: base, To: base.Add(time.Hour)}
	bundle := mustAssembleBundle(t, tx, r, assembleOpts{tenantID: tenant, subject: "system", maxInvoices: 5000, now: time.Now()})
	zr := mustReadZip(t, bundle)
	got, err := preview(context.Background(), tx, r, previewOpts{maxInvoices: 5000})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	csvRows, manifestRows := csvDataRecordCount(t, zr, "line_items.csv"), manifestOracleCounts(t, zr).LineItems
	if csvRows != 2*total || manifestRows != 2*total || got.Counts.LineItems != 2*total {
		t.Errorf("line_items rows: csv %d, manifest %d, preview %d, want %d each", csvRows, manifestRows, got.Counts.LineItems, 2*total)
	}
}

func TestAssemble_LineItemsManifestEntryMatchesTheZipEntry(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-lineitems-manifest")
	entity := mustCreateEntity(t, tx, tenant, "Manifest Lines Co", "90000014-0001")
	from := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	inv := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-LI-MAN", createdAt: from})
	for i := 1; i <= 3; i++ {
		mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenant, invoiceID: inv, lineNo: i})
	}

	actingAs(t, tx, tenant)
	bundle := mustAssembleBundle(t, tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)},
		assembleOpts{tenantID: tenant, subject: "system", maxInvoices: 10, now: time.Now()})
	zr := mustReadZip(t, bundle)

	names := entryNames(zr)
	if names[len(names)-1] != "manifest.json" || names[0] != "invoices.csv" || names[1] != "line_items.csv" {
		t.Errorf("entries = %v, want invoices.csv, line_items.csv first and manifest.json last", names)
	}
	var doc manifestDoc
	if err := json.Unmarshal(mustReadZipEntry(t, zr, "manifest.json"), &doc); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var entry *manifestEntry
	for i := range doc.Entries {
		if doc.Entries[i].Name == "line_items.csv" {
			entry = &doc.Entries[i]
		}
	}
	if entry == nil {
		t.Fatal("manifest lists no line_items.csv entry")
	}
	raw := mustReadZipEntry(t, zr, "line_items.csv")
	sum := sha256.Sum256(raw)
	if entry.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("manifest sha256 = %s, want %x", entry.SHA256, sum)
	}
	if entry.Bytes != int64(len(raw)) {
		t.Errorf("manifest bytes = %d, want %d", entry.Bytes, len(raw))
	}
	if entry.Rows == nil || *entry.Rows != 3 || doc.Counts.LineItems != 3 {
		t.Errorf("manifest rows = %v, counts.line_items = %d, want 3 and 3", entry.Rows, doc.Counts.LineItems)
	}
	if doc.Format != "ascomply-evidence-bundle/1" {
		t.Errorf("manifest format = %q, want ascomply-evidence-bundle/1 (D13)", doc.Format)
	}
}

func TestSelectLineItems_OrderedByInvoiceAcrossChunks(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-lineitems-chunk-order")
	entity := mustCreateEntity(t, tx, tenant, "Chunk Order Co", "90000015-0001")
	base := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)

	const total = 501
	ids := make([]string, total)
	wantLines := 0
	for i := range ids {
		ids[i] = mustCreateInvoice(t, tx, invoiceFixture{
			id: fmt.Sprintf("00000000-0000-4000-8000-%012d", total-i), tenantID: tenant, entityID: entity,
			invoiceNumber: fmt.Sprintf("INV-LI-O-%04d", i), createdAt: base.Add(time.Duration(i) * time.Second),
		})
		for n := 1; n <= 1+i%3/2; n++ {
			mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenant, invoiceID: ids[i], lineNo: n})
			wantLines++
		}
	}
	actingAs(t, tx, tenant)

	before := append([]string(nil), ids...)
	rows := runSelectLineItems(t, tx, ids)[1:]
	if !slices.Equal(ids, before) {
		t.Error("selectLineItems reordered the caller's ids slice")
	}
	if len(rows) != wantLines {
		t.Fatalf("%d data rows, want %d", len(rows), wantLines)
	}
	seen := map[string]bool{}
	for i, r := range rows {
		key := r[0] + "/" + r[2]
		if seen[key] {
			t.Errorf("row %d: line %s repeated", i, key)
		}
		seen[key] = true
		if i > 0 {
			p := rows[i-1]
			if r[0] < p[0] || (r[0] == p[0] && r[2] <= p[2]) {
				t.Fatalf("row %d (%s, line %s) follows (%s, line %s): not ordered by invoice_id, line_no", i, r[0], r[2], p[0], p[2])
			}
		}
	}
}
