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
	mappingCheckInstructions = "State whether the following statement about this spreadsheet is true: the named column holds the named invoice field. The spreadsheet's first rows are shown; one row is one invoice line, and invoice-level values repeat on every line of the same invoice. Judge from the column's header and its values; a header may use any name, abbreviation or language for its field. The fields are: invoice_number: the invoice's own number, not an order, PO, customer, account or payment reference. issue_date: the date the invoice was issued, not a due, delivery or payment date. buyer_tin: the buyer's (customer's) Tax Identification Number, not the seller's own TIN or an RC number. buyer_name: the buyer's name, not a customer code, address, email or phone. currency: the currency code. subtotal: the invoice's amount before VAT, not a line amount. vat: the invoice's VAT amount, not a VAT rate or a line's tax. total: the invoice's total including VAT, not a line amount, amount paid, balance due or amount after withholding tax. line_description: the line's item or service description. line_quantity: the line's quantity. line_unit_price: the line's price per unit, not a line total."
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
