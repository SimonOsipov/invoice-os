package validation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// v5Start is rule-set v5's start date; invoices issued on it are judged by v5.
const v5Start = "2027-01-01"

// v5Lists are the real NRS codes the v5 fixtures use, keyed by NRS list name.
var v5Lists = map[string][]string{
	"currencies":             {"NGN", "USD"},
	"countries":              {"NG"},
	"states":                 {"NG-LA", "NG-FC"},
	"lgas":                   {"NG-LA-AGE", "NG-FC-AML"},
	"invoice-quantity-codes": {"EA", "C62"},
	"hs-codes":               {"8471.30", "1006.30"},
	"services-codes":         {"6201", "5610"},
	"tax-categories":         {"STANDARD_VAT", "ZERO_VAT", "EXEMPTED", "STAMP_DUTY"},
}

// seedV5Lists inserts the v5 code lists as the superuser. Cleanup deletes only the rows this
// call inserted, so synced rows survive.
func seedV5Lists(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	type row struct{ list, code string }
	var inserted []row
	for list, codes := range v5Lists {
		for _, code := range codes {
			tag, err := super.Exec(ctx,
				`INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, '[{}]') ON CONFLICT DO NOTHING`, list, code)
			if err != nil {
				t.Fatalf("seed nrs_codes %s/%s: %v", list, code, err)
			}
			if tag.RowsAffected() == 1 {
				inserted = append(inserted, row{list, code})
			}
		}
	}
	t.Cleanup(func() {
		for _, r := range inserted {
			_, _ = super.Exec(context.Background(), `DELETE FROM nrs_codes WHERE list = $1 AND code = $2`, r.list, r.code)
		}
	})
}

// v5Line builds one categorised goods line. Numbers are float64: the real path decodes JSON to float64 and
// the CEL presence rules test type(x.f) == double.
func v5Line(id, category string, amount, percent, tax float64) map[string]any {
	return map[string]any{
		"id": id, "description": "Laptop", "quantity": 1.0, "unit_price": amount, "line_total": amount,
		"sellers_item_identification": "SKU-" + id, "base_quantity": 1.0, "price_unit": "EA",
		"hsn_code": "8471.30", "product_category": "Computers",
		"tax_category": category, "tax_percent": percent, "line_tax": tax,
	}
}

// v5StandardLines is STANDARD_VAT 1000/75, ZERO_VAT 500/0, EXEMPTED 200/0: subtotal 1700, VAT 75.
func v5StandardLines() []map[string]any {
	return []map[string]any{
		v5Line("1", "STANDARD_VAT", 1000, 7.5, 75),
		v5Line("2", "ZERO_VAT", 500, 0, 0),
		v5Line("3", "EXEMPTED", 200, 0, 0),
	}
}

func v5Party(tin, name, lga, state string) map[string]any {
	return map[string]any{
		"tin": tin, "name": name, "email": "accounts@example.ng", "street": "1 Marina Road",
		"city": "Lagos", "postal_zone": "100001", "lga": lga, "state": state, "country": "NG",
	}
}

// v5Invoice builds a payload the way MBSPayload does: header totals come from the lines, and
// tax_subtotals is derived by (tax_category, tax_percent) in first-line order, omitted when no
// line has a category. kind B2C carries no buyer block.
func v5Invoice(kind string, lines []map[string]any) Payload {
	var subtotal, vat float64
	els := make([]any, len(lines))
	type key struct {
		cat string
		pct float64
	}
	var order []key
	taxable, taxes := map[key]float64{}, map[key]float64{}
	for i, l := range lines {
		els[i] = l
		subtotal += l["line_total"].(float64)
		if tax, ok := l["line_tax"].(float64); ok {
			vat += tax
		}
		cat, ok := l["tax_category"].(string)
		if !ok {
			continue
		}
		pct, _ := l["tax_percent"].(float64)
		k := key{cat, pct}
		if _, seen := taxable[k]; !seen {
			order = append(order, k)
		}
		taxable[k] += l["line_total"].(float64)
		taxes[k] += l["line_tax"].(float64)
	}
	inv := map[string]any{
		"invoice_number": "INV-2027-000123", "issue_date": v5Start, "currency": "NGN",
		"tax_currency_code": "NGN", "invoice_kind": kind,
		"supplier": v5Party("12345678-0001", "Acme Nigeria Ltd", "NG-LA-AGE", "NG-LA"),
		"subtotal": subtotal, "vat": vat, "total": subtotal + vat, "line_items": els,
	}
	if kind != "B2C" {
		inv["buyer"] = v5Party("87654321-0002", "Buyer Ltd", "NG-FC-AML", "NG-FC")
	}
	if len(order) > 0 {
		subs := make([]any, len(order))
		for i, k := range order {
			subs[i] = map[string]any{
				"tax_category": k.cat, "tax_percent": k.pct, "taxable_amount": taxable[k], "tax_amount": taxes[k],
			}
		}
		inv["tax_subtotals"] = subs
	}
	return Payload{"invoice": inv}
}

func v5B2BPayload() Payload { return v5Invoice("B2B", v5StandardLines()) }
func v5B2CPayload() Payload { return v5Invoice("B2C", v5StandardLines()) }
