package invoice

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/shopspring/decimal"
)

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

var (
	plainDecimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	vatRate      = decimal.New(75, -3)
	moneyBound   = decimal.New(1, 12)
	qtyBound     = decimal.New(1, 11)
)

// parsePlain reads plain decimal text only; "1e5", blanks and padding are refused.
func parsePlain(s string) (decimal.Decimal, error) {
	if !plainDecimal.MatchString(s) {
		return decimal.Zero, fmt.Errorf("%w: %q is not a plain decimal", ErrValidation, s)
	}
	return decimal.NewFromString(s)
}

// noteQuantity and noteUnitPrice hold the column bounds: a value that would round or overflow is refused.
func noteQuantity(s string) (decimal.Decimal, error) {
	q, err := parsePlain(s)
	if err != nil {
		return q, err
	}
	if !q.IsPositive() || !q.Round(3).Equal(q) || q.GreaterThanOrEqual(qtyBound) {
		return q, fmt.Errorf("%w: quantity must be above 0, at most 3 decimals, below 10^11", ErrValidation)
	}
	return q, nil
}

func noteUnitPrice(s string) (decimal.Decimal, error) {
	p, err := parsePlain(s)
	if err != nil {
		return p, err
	}
	if p.IsNegative() || !p.Round(2).Equal(p) || p.GreaterThanOrEqual(moneyBound) {
		return p, fmt.Errorf("%w: unit price must be 0 or more, at most 2 decimals, below 10^12", ErrValidation)
	}
	return p, nil
}

// noteAmounts ports draftTotals and lineTaxKobo (invoiceDraft.ts); the vectors in
// TestNoteAmounts_Vectors pin both sides.
func noteAmounts(lines []noteMoneyLine) (noteMoney, error) {
	n := len(lines)
	out := noteMoney{LineTotals: make([]string, n), LineTaxes: make([]*string, n)}
	totals := make([]decimal.Decimal, n)
	exactSum := decimal.Zero
	categorised := false
	for i, l := range lines {
		q, err := noteQuantity(l.Quantity)
		if err != nil {
			return noteMoney{}, err
		}
		p, err := noteUnitPrice(l.UnitPrice)
		if err != nil {
			return noteMoney{}, err
		}
		product := q.Mul(p)
		totals[i] = product.Round(2)
		if totals[i].GreaterThanOrEqual(moneyBound) {
			return noteMoney{}, fmt.Errorf("%w: a line total reaches 10^12", ErrValidation)
		}
		out.LineTotals[i] = totals[i].StringFixed(2)
		exactSum = exactSum.Add(product)
		if l.TaxCategory != nil && *l.TaxCategory != "" {
			categorised = true
		}
	}
	subtotal := exactSum.Round(2)
	vat := subtotal.Mul(vatRate).Round(2)
	if categorised {
		vat = decimal.Zero
		for i, t := range lineTaxes(lines, totals) {
			if t != nil {
				vat = vat.Add(*t)
				s := t.StringFixed(2)
				out.LineTaxes[i] = &s
			}
		}
	}
	total := subtotal.Add(vat)
	if total.GreaterThanOrEqual(moneyBound) {
		return noteMoney{}, fmt.Errorf("%w: the total reaches 10^12", ErrValidation)
	}
	out.Subtotal, out.VAT, out.Total = subtotal.StringFixed(2), vat.StringFixed(2), total.StringFixed(2)
	return out, nil
}

// lineTaxes groups categorised lines by (category, percent to 2dp). A group's tax is the rounded
// sum; floors go to the lines and leftover kobo to the largest remainders, ties to the lower line.
// A line with no usable percent joins no group and keeps a nil tax.
func lineTaxes(lines []noteMoneyLine, totals []decimal.Decimal) []*decimal.Decimal {
	taxes := make([]*decimal.Decimal, len(lines))
	type group struct{ members []int }
	groups := map[string]*group{}
	var order []string
	pct := make([]decimal.Decimal, len(lines))
	for i, l := range lines {
		if l.TaxCategory == nil || *l.TaxCategory == "" || l.TaxPercent == nil {
			continue
		}
		p, err := parsePlain(*l.TaxPercent)
		if err != nil {
			continue
		}
		pct[i] = p.Round(2)
		key := *l.TaxCategory + "|" + pct[i].String()
		if groups[key] == nil {
			groups[key] = &group{}
			order = append(order, key)
		}
		groups[key].members = append(groups[key].members, i)
	}
	for _, key := range order {
		m := groups[key].members
		exact := make([]decimal.Decimal, len(m))
		floors := make([]decimal.Decimal, len(m))
		sum, floorSum := decimal.Zero, decimal.Zero
		for j, i := range m {
			exact[j] = totals[i].Mul(pct[i]).Shift(-2)
			floors[j] = exact[j].RoundFloor(2)
			sum = sum.Add(exact[j])
			floorSum = floorSum.Add(floors[j])
		}
		leftover := int(sum.Round(2).Sub(floorSum).Shift(2).IntPart())
		byRemainder := make([]int, len(m))
		for j := range byRemainder {
			byRemainder[j] = j
		}
		sort.SliceStable(byRemainder, func(a, b int) bool {
			ra, rb := exact[byRemainder[a]].Sub(floors[byRemainder[a]]), exact[byRemainder[b]].Sub(floors[byRemainder[b]])
			return ra.GreaterThan(rb)
		})
		for _, j := range byRemainder {
			if leftover <= 0 {
				break
			}
			floors[j] = floors[j].Add(decimal.New(1, -2))
			leftover--
		}
		for j, i := range m {
			t := floors[j]
			taxes[i] = &t
		}
	}
	return taxes
}
