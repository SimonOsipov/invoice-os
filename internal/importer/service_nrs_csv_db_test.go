package importer

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// The CSV and mapping the deployed import contract sends (e2e/api/contract-import.spec.ts).
const nrsContractHeader = "Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price,HS Code,Tax Category,Tax Rate,Line Tax,Line Net,State,Due Date"

var nrsContractMapping = map[string]string{
	"invoice_number": "Invoice No", "issue_date": "Issue Date", "buyer_tin": "Buyer TIN", "buyer_name": "Buyer",
	"currency": "Currency", "subtotal": "Subtotal", "vat": "VAT", "total": "Total", "line_description": "Item",
	"line_quantity": "Qty", "line_unit_price": "Unit Price", "line_hsn_code": "HS Code", "line_tax_category": "Tax Category",
	"line_tax_percent": "Tax Rate", "line_tax": "Line Tax", "line_total": "Line Net", "buyer_state": "State", "due_date": "Due Date",
}

func nrsContractCSV(num, secondLineTax string) string {
	head := num + ",2026-01-15,87654321-0002,NRS Import Buyer,NGN,150.00,11.25,161.25"
	l1 := head + ",Item 1,1,100.00,8471.30,STANDARD_VAT,7.5,7.50,100.00,NG-FC,2026-02-28"
	l2 := head + ",Item 2,1,50.00,8471.30,STANDARD_VAT,7.5," + secondLineTax + ",50.00,NG-FC,2026-02-28"
	return nrsContractHeader + "\n" + l1 + "\n" + l2
}

// strOf reads a JSON string value; anything else (a number, null) shows as its Go form.
func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func decodeContractCSV(t *testing.T, csv string) ([]string, [][]string) {
	t.Helper()
	header, rows, _, err := Decode(strings.NewReader(csv), "csv")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return header, rows
}

func TestImportNRS_TheContractCSVRoundTripsThroughTheLiveRuleSet(t *testing.T) {
	g := newNRSGateRun(t, nil)
	csv := nrsContractCSV("CSV-1", "3.75")
	header, rows := decodeContractCSV(t, csv)

	dry, err := g.svc.Import(g.ctx, g.entityID, "", "", 1, nrsContractMapping, header, rows, true, "")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.ReadyInvoices != 1 || len(dry.Errors) != 0 {
		t.Fatalf("dry run ready = %d, errors = %+v, want 1 and none", dry.ReadyInvoices, dry.Errors)
	}

	if dry.InvoicesClean != 1 || dry.InvoicesWithViolations != 0 {
		t.Errorf("live rules: clean = %d, with violations = %d (%+v), want 1 and 0", dry.InvoicesClean, dry.InvoicesWithViolations, dry.InvoiceViolations)
	}
	real, err := g.svc.Import(g.ctx, g.entityID, "", "", 1, nrsContractMapping, header, rows, false, "")
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if real.ReadyInvoices != 1 || len(real.Errors) != 0 {
		t.Fatalf("real run ready = %d, errors = %+v, want 1 and none", real.ReadyInvoices, real.Errors)
	}

	_, app := dbTestPools(t)
	id := invoiceIDByNumber(t, g.super, g.entityID, "CSV-1")
	inv, err := invoice.NewStore(app).Get(g.ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	raw, _ := json.Marshal(inv)
	var wire struct {
		BuyerState *string          `json:"buyer_state"`
		DueDate    *string          `json:"due_date"`
		LineItems  []map[string]any `json:"line_items"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode wire: %v", err)
	}
	checkWants(t, []nrsWant{
		{"buyer_state", sv(wire.BuyerState), "NG-FC"},
		{"due_date", sv(wire.DueDate), "2026-02-28T00:00:00Z"},
	})
	if len(wire.LineItems) != 2 {
		t.Fatalf("line_items = %d, want 2", len(wire.LineItems))
	}
	var got [][5]string
	for _, l := range wire.LineItems {
		got = append(got, [5]string{strOf(l["hsn_code"]), strOf(l["tax_category"]), strOf(l["tax_percent"]), strOf(l["line_total"]), strOf(l["line_tax"])})
	}
	want := [][5]string{
		{"8471.30", "STANDARD_VAT", "7.50", "100.00", "7.50"},
		{"8471.30", "STANDARD_VAT", "7.50", "50.00", "3.75"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
}

func TestImportNRS_TheContractCSVWithAnExponentFailsTheInvoiceOnBothRows(t *testing.T) {
	g := newNRSGateRun(t, nil)
	header, rows := decodeContractCSV(t, nrsContractCSV("CSV-EXP", "1e400"))
	res, err := g.svc.Import(g.ctx, g.entityID, "", "", 1, nrsContractMapping, header, rows, true, "")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %+v, want exactly one", res.Errors)
	}
	e := res.Errors[0]
	if e.Field != "line_tax" || e.Message != "line_tax is not a valid number" || !reflect.DeepEqual(e.Rows, []int{2, 3}) {
		t.Errorf("error = %+v, want line_tax on rows [2 3]", e)
	}
	if res.ReadyInvoices != 0 {
		t.Errorf("ready = %d, want 0", res.ReadyInvoices)
	}
}
