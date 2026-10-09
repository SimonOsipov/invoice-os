package invoice

import (
	"sort"

	"github.com/shopspring/decimal"
)

// TaxSubtotal is one tax category's totals, derived from the lines (ENGI-02 D6).
type TaxSubtotal struct {
	TaxCategory   string  `json:"tax_category"`
	TaxPercent    *string `json:"tax_percent"`
	TaxableAmount *string `json:"taxable_amount"`
	TaxAmount     *string `json:"tax_amount"`
}

type taxGroupKey struct {
	category   string
	hasPercent bool
	percent    string
}

// taxSum is a running decimal sum that goes absent on a NULL or non-decimal member.
type taxSum struct {
	total  decimal.Decimal
	absent bool
}

func (s *taxSum) add(v *string) {
	if s.absent {
		return
	}
	if v == nil {
		s.absent = true
		return
	}
	d, err := decimal.NewFromString(*v)
	if err != nil {
		s.absent = true
		return
	}
	s.total = s.total.Add(d)
}

func (s taxSum) text() *string {
	if s.absent {
		return nil
	}
	v := s.total.StringFixed(2)
	return &v
}

// taxSubtotals groups categorised lines by exact (category, percent) text, in
// the line_no order of each group's first line. Uncategorised lines join no group.
func taxSubtotals(lines []LineItem) []TaxSubtotal {
	sorted := append([]LineItem(nil), lines...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].LineNo < sorted[b].LineNo })

	type group struct {
		sub            TaxSubtotal
		taxable, taxes taxSum
	}
	var order []*group
	byKey := map[taxGroupKey]*group{}
	for _, l := range sorted {
		if l.TaxCategory == nil {
			continue
		}
		key := taxGroupKey{category: *l.TaxCategory}
		if l.TaxPercent != nil {
			key.hasPercent, key.percent = true, *l.TaxPercent
		}
		g, ok := byKey[key]
		if !ok {
			g = &group{sub: TaxSubtotal{TaxCategory: *l.TaxCategory}}
			if l.TaxPercent != nil {
				p := *l.TaxPercent
				g.sub.TaxPercent = &p
			}
			byKey[key] = g
			order = append(order, g)
		}
		g.taxable.add(l.LineTotal)
		g.taxes.add(l.LineTax)
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]TaxSubtotal, len(order))
	for i, g := range order {
		out[i] = g.sub
		out[i].TaxableAmount = g.taxable.text()
		out[i].TaxAmount = g.taxes.text()
	}
	return out
}
