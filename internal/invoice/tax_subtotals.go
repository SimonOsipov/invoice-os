package invoice

// TaxSubtotal is one tax category's totals, derived from the lines (ENGI-02 D6).
type TaxSubtotal struct {
	TaxCategory   string  `json:"tax_category"`
	TaxPercent    *string `json:"tax_percent"`
	TaxableAmount *string `json:"taxable_amount"`
	TaxAmount     *string `json:"tax_amount"`
}

// taxSubtotals is a Mode A stub; the implementation lands with ENGI-02-03.
func taxSubtotals(lines []LineItem) []TaxSubtotal {
	return nil
}
