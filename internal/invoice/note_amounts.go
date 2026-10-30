package invoice

// noteMoneyLine is one chosen line's money input. TaxCategory and TaxPercent come from the original line.
type noteMoneyLine struct {
	Quantity, UnitPrice     string
	TaxCategory, TaxPercent *string
}

// noteMoney is what a note stores: per-line totals and taxes, then the header amounts.
type noteMoney struct {
	LineTotals           []string
	LineTaxes            []*string
	Subtotal, VAT, Total string
}

// noteAmounts is a compile stub; the executor replaces it.
func noteAmounts(lines []noteMoneyLine) (noteMoney, error) {
	return noteMoney{}, ErrValidation
}
