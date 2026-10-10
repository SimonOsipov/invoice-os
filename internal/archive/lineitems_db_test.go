package archive

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// lineItemFixture is mustCreateLineItem's input; nil columns insert NULL.
type lineItemFixture struct {
	tenantID, invoiceID string
	lineNo              int
	description         *string
	// nrs holds the 9 NRS columns by name; numeric ones are passed as text.
	nrs map[string]string
}

var lineItemNRSColumns = []string{
	"tax_category", "hsn_code", "isic_code", "product_category", "service_category",
	"sellers_item_identification", "price_unit", "tax_percent", "base_quantity",
}

func mustCreateLineItem(t *testing.T, tx pgx.Tx, f lineItemFixture) {
	t.Helper()
	args := []any{f.tenantID, f.invoiceID, f.lineNo, f.description}
	for _, c := range lineItemNRSColumns {
		if v, ok := f.nrs[c]; ok {
			args = append(args, v)
		} else {
			args = append(args, nil)
		}
	}
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO line_items (tenant_id, invoice_id, line_no, description,
		       tax_category, hsn_code, isic_code, product_category, service_category,
		       sellers_item_identification, price_unit, tax_percent, base_quantity)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::text::numeric, $13::text::numeric)`, args...); err != nil {
		t.Fatalf("insert line_items fixture: %v", err)
	}
}

func lineItemsIndex(t *testing.T, column string) int {
	t.Helper()
	for i, h := range lineItemsCSVHeader {
		if h == column {
			return i
		}
	}
	t.Fatalf("lineItemsCSVHeader has no column %q", column)
	return -1
}

func runSelectLineItems(t *testing.T, tx pgx.Tx, ids []string) [][]string {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := selectLineItems(context.Background(), tx, ids, w); err != nil {
		t.Fatalf("selectLineItems: unexpected error: %v", err)
	}
	w.Flush()
	return parseCSV(t, buf.Bytes())
}

func TestSelectLineItems_ExportsEveryLineInOrder(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-lineitems-order")
	entity := mustCreateEntity(t, tx, tenant, "Line Items Co", "90000001-0001")
	invX := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-LI-X"})
	invY := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-LI-Y"})

	desc := "Widget"
	nrs := map[string]string{
		"tax_category": "VAT", "hsn_code": "8471", "isic_code": "4651", "product_category": "goods",
		"service_category": "svc", "sellers_item_identification": "SKU-9", "price_unit": "NGN per kg",
		"tax_percent": "7.50", "base_quantity": "1.000",
	}
	// Line 2 is planted before line 1 so the order cannot be insertion order.
	mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenant, invoiceID: invX, lineNo: 2, description: &desc, nrs: nrs})
	mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenant, invoiceID: invX, lineNo: 1, description: &desc})
	mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenant, invoiceID: invY, lineNo: 1, description: &desc})

	actingAs(t, tx, tenant)
	rows := runSelectLineItems(t, tx, []string{invX, invY})
	if len(rows) != 4 {
		t.Fatalf("line_items.csv has %d records, want 4 (header + 3 lines)", len(rows))
	}
	data := rows[1:]
	for i := 1; i < len(data); i++ {
		prev, cur := data[i-1], data[i]
		if prev[0] > cur[0] || (prev[0] == cur[0] && prev[2] >= cur[2]) {
			t.Errorf("rows %d and %d out of order: (%s, %s) before (%s, %s)", i-1, i, prev[0], prev[2], cur[0], cur[2])
		}
	}

	numbers := map[string]string{invX: "INV-LI-X", invY: "INV-LI-Y"}
	var line2 []string
	for _, r := range data {
		if got := r[lineItemsIndex(t, "invoice_number")]; got != numbers[r[0]] {
			t.Errorf("invoice_number for %s = %q, want %q", r[0], got, numbers[r[0]])
		}
		if r[0] == invX && r[2] == "2" {
			line2 = r
		}
	}
	if line2 == nil {
		t.Fatal("no row for invoice X line 2")
	}
	want := map[string]string{"description": "Widget"}
	for k, v := range nrs {
		want[k] = v
	}
	for col, v := range want {
		if got := line2[lineItemsIndex(t, col)]; got != v {
			t.Errorf("line 2 %s = %q, want %q", col, got, v)
		}
	}
}

func TestSelectLineItems_NullFieldsWriteEmptyCells(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-lineitems-null")
	entity := mustCreateEntity(t, tx, tenant, "Null Lines Co", "90000002-0001")
	inv := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-LI-NULL"})
	desc := "Only description"
	mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenant, invoiceID: inv, lineNo: 1, description: &desc})

	actingAs(t, tx, tenant)
	rows := runSelectLineItems(t, tx, []string{inv})
	if len(rows) != 2 {
		t.Fatalf("line_items.csv has %d records, want 2", len(rows))
	}
	row := rows[1]
	if len(row) != 17 {
		t.Fatalf("row length = %d, want 17", len(row))
	}
	empty := 0
	for i, h := range lineItemsCSVHeader {
		switch h {
		case "invoice_id", "invoice_number", "line_no", "description":
			continue
		}
		if row[i] != "" {
			t.Errorf("%s = %q, want an empty cell", h, row[i])
		}
		empty++
	}
	if empty != 13 {
		t.Errorf("checked %d empty cells, want 13", empty)
	}
}

func TestSelectLineItems_HeaderOnlyForNoIDs(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	actingAs(t, tx, mustCreateTenant(t, tx, "archive-lineitems-none"))
	if rows := runSelectLineItems(t, tx, nil); len(rows) != 1 {
		t.Errorf("line_items.csv has %d records for no ids, want 1 (header only)", len(rows))
	}
}

func TestRLS_SelectLineItemsWithAnotherTenantsInvoiceIDsWritesHeaderOnly(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-lineitems-rls-a")
	tenantB := mustCreateTenant(t, tx, "archive-lineitems-rls-b")
	entityA := mustCreateEntity(t, tx, tenantA, "RLS A Co", "90000003-0001")
	invA := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantA, entityID: entityA, invoiceNumber: "INV-LI-RLS-A"})
	desc := "A's line"
	mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenantA, invoiceID: invA, lineNo: 1, description: &desc})

	var planted int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM line_items WHERE invoice_id = $1`, invA).Scan(&planted); err != nil || planted != 1 {
		t.Fatalf("control needle: planted %d lines (err %v), want 1", planted, err)
	}

	actingAs(t, tx, tenantB) // invoice_app role, tenant B GUC
	rows := runSelectLineItems(t, tx, []string{invA})
	if len(rows) != 1 {
		t.Errorf("line_items.csv under tenant B has %d records for tenant A's invoice, want 1 (header only)", len(rows))
	}
}

func TestSelectLineItems_ChunksPast500Invoices(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-lineitems-chunks")
	entity := mustCreateEntity(t, tx, tenant, "Chunk Lines Co", "90000004-0001")

	ids := make([]string, 501)
	for i := range ids {
		ids[i] = mustCreateInvoice(t, tx, invoiceFixture{
			id: uuid.NewString(), tenantID: tenant, entityID: entity, invoiceNumber: fmt.Sprintf("INV-LI-CH-%03d", i),
			createdAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second),
		})
		mustCreateLineItem(t, tx, lineItemFixture{tenantID: tenant, invoiceID: ids[i], lineNo: 1})
	}

	actingAs(t, tx, tenant)
	rows := runSelectLineItems(t, tx, ids)
	if len(rows) != 502 {
		t.Fatalf("line_items.csv has %d records, want 502 (header + 501)", len(rows))
	}
	seen := map[string]int{}
	for _, r := range rows[1:] {
		seen[r[0]]++
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Errorf("invoice %s appears %d times, want exactly once", id, seen[id])
		}
	}
}
