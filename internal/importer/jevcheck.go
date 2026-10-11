package importer

import (
	"context"
	"fmt"
	"slices"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// MappingChecker is the slice of *jev.Client this package uses; nil is off.
type MappingChecker interface {
	Enabled() bool
	Ask(ctx context.Context, req jev.Request) (jev.Response, error)
}

// A noul at or below this doubts the placement.
// ceiling: measured on 48 synthetic layouts (CHECK-00, 2026-09-24); re-measure on real spreadsheets before a production key
const mappingCheckThreshold = 0.10

// internal/jevmeasure/wording.go's mapping text, byte for byte (TestMappingCheck_TheQuestionIsTheMeasuredQuestion).
const (
	mappingCheckInstructions = "State whether the following statement about this spreadsheet is true: the named column holds the named invoice field. The spreadsheet's first rows are shown; one row is one invoice line, and invoice-level values repeat on every line of the same invoice. Judge from the column's header and its values; a header may use any name, abbreviation or language for its field. The fields are: invoice_number: the invoice's own number, not an order, PO, customer, account or payment reference. issue_date: the date the invoice was issued, not a due, delivery or payment date. buyer_tin: the buyer's (customer's) Tax Identification Number, not the seller's own TIN or an RC number. buyer_name: the buyer's name, not a customer code, address, email or phone. currency: the currency code. subtotal: the invoice's amount before VAT, not a line amount. vat: the invoice's VAT amount, not a VAT rate or a line's tax. total: the invoice's total including VAT, not a line amount, amount paid, balance due or amount after withholding tax. line_description: the line's item or service description. line_quantity: the line's quantity. line_unit_price: the line's price per unit, not a line total. invoice_kind: the buyer type, B2B, B2G or B2C, not a document type such as invoice or credit note. tax_currency_code: the currency code the tax is stated in. due_date: the date payment is due, not the issue date. issue_time: the time of day the invoice was issued. tax_point_date: the tax point or supply date. payment_status: whether the invoice is paid or pending, not an amount. buyer_email: the buyer's (customer's) email address, not the seller's. buyer_telephone: the buyer's (customer's) telephone number, not the seller's. buyer_street: the buyer's (customer's) street address, not the seller's. buyer_city: the buyer's (customer's) city, not the seller's. buyer_postal_zone: the buyer's (customer's) postal code, not the seller's. buyer_country: the buyer's (customer's) country, not the seller's. buyer_state: the buyer's (customer's) state, not the seller's. buyer_lga: the buyer's (customer's) local government area, not the seller's. line_total: the line's amount before tax, only from a column whose header names the line or item, such as Line Amount, Item Amount or Line Total. Otherwise null: any other amount column is the invoice's subtotal or total, or stays unmapped. line_tax: the line's tax amount, only from a column whose header names the line or item, such as Line Tax, Item Tax or Tax on Line. Otherwise null: any other VAT or tax amount column is the invoice's vat. line_tax_category: the line's tax category, such as standard VAT, zero-rated or exempt. line_hsn_code: the line's HS code for goods. line_isic_code: the line's service (ISIC) code. line_product_category: the line's product category, not a description or a code. line_service_category: the line's service category, not a description or a code. line_sellers_item_identification: the seller's own item code or SKU, not an HS or service code. line_price_unit: the line's unit of measure, not a price. line_tax_percent: the line's tax rate in percent, not an amount. line_base_quantity: the quantity the unit price is for."
	mappingCheckTrue         = "The column holds the named field as defined above, whatever its header is called."
	mappingCheckFalse        = "The column holds something other than the named field as defined above: another field, a rate or percentage where an amount is defined, a line amount where an invoice amount is defined, the seller's details where the buyer's are defined, or data that is not an invoice field."
)

func mappingCheckQuestion(field, header string) jev.Question {
	return jev.Question{
		Type:         jev.TypeNoul,
		Instructions: mappingCheckInstructions + fmt.Sprintf(" Field: %s. Column header: %s.", field, header),
		True:         mappingCheckTrue,
		False:        mappingCheckFalse,
	}
}

// checkPlacements returns the doubted fields, sorted; never nil. Any skip returns an empty slice.
func checkPlacements(ctx context.Context, c MappingChecker, window [][]string, placements map[string]string) []string {
	doubted := []string{}
	if c == nil || !c.Enabled() || len(placements) == 0 || len(window) == 0 {
		return doubted
	}
	qs := make(map[string]jev.Question, len(placements))
	for field, header := range placements {
		qs[field] = mappingCheckQuestion(field, header)
	}
	resp, err := c.Ask(ctx, jev.Request{Purpose: jev.PurposeMappingCheck, State: mappingPromptText(window), Questions: qs})
	if err != nil {
		return doubted
	}
	for field := range placements {
		a, ok := resp.Answers[field]
		if !ok || a.Type != jev.TypeNoul {
			return []string{}
		}
		// NaN compares false, so it doubts nothing.
		if a.Noul <= mappingCheckThreshold {
			doubted = append(doubted, field)
		}
	}
	slices.Sort(doubted)
	return doubted
}
