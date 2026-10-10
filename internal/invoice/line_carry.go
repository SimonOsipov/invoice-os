package invoice

import (
	"strings"

	"github.com/shopspring/decimal"
)

// carryNRSLines gives each entry of a line replace the NRS fields it omits. An entry with
// an id takes them from that stored line; an entry with none and no NRS field of its own takes
// them from the first unclaimed stored line with equal legacy content. before is the locked,
// RLS-filtered line set, so an id outside it (other invoice, other tenant, replaced, malformed,
// repeated) is ErrUnknownLineID with no existence oracle.
func carryNRSLines(in []LineItemInput, before []LineItem) ([]LineItemInput, error) {
	out := append([]LineItemInput(nil), in...)
	byID := make(map[string]int, len(before))
	for i, b := range before {
		byID[strings.ToLower(b.ID)] = i
	}
	claimed := make([]bool, len(before))

	for i := range out {
		if out[i].ID == nil {
			continue
		}
		j, ok := byID[strings.ToLower(*out[i].ID)]
		if !ok || claimed[j] {
			return nil, ErrUnknownLineID
		}
		claimed[j] = true
		fillNRS(&out[i], before[j])
	}

	for i := range out {
		if out[i].ID != nil || hasNRS(out[i]) {
			continue
		}
		for j, b := range before {
			if !claimed[j] && sameLegacyContent(out[i], b) {
				claimed[j] = true
				fillNRS(&out[i], b)
				break
			}
		}
	}
	return out, nil
}

func hasNRS(l LineItemInput) bool {
	return l.TaxCategory != nil || l.HSNCode != nil || l.ISICCode != nil ||
		l.ProductCategory != nil || l.ServiceCategory != nil ||
		l.SellersItemIdentification != nil || l.PriceUnit != nil ||
		l.TaxPercent != nil || l.BaseQuantity != nil
}

// fillNRS sets each nil NRS field of l to the stored line's value.
func fillNRS(l *LineItemInput, s LineItem) {
	for _, f := range [...]struct {
		dst **string
		src *string
	}{
		{&l.TaxCategory, s.TaxCategory}, {&l.HSNCode, s.HSNCode}, {&l.ISICCode, s.ISICCode},
		{&l.ProductCategory, s.ProductCategory}, {&l.ServiceCategory, s.ServiceCategory},
		{&l.SellersItemIdentification, s.SellersItemIdentification}, {&l.PriceUnit, s.PriceUnit},
		{&l.TaxPercent, s.TaxPercent}, {&l.BaseQuantity, s.BaseQuantity},
	} {
		if *f.dst == nil {
			*f.dst = f.src
		}
	}
}

// sameLegacyContent: description exact, numerics by decimal value ("100" equals "100.00").
func sameLegacyContent(l LineItemInput, s LineItem) bool {
	return strPtrEqual(l.Description, s.Description) &&
		sameDecimal(l.Quantity, s.Quantity) && sameDecimal(l.UnitPrice, s.UnitPrice) &&
		sameDecimal(l.LineTotal, s.LineTotal) && sameDecimal(l.LineTax, s.LineTax)
}

func sameDecimal(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, errX := decimal.NewFromString(*a)
	y, errY := decimal.NewFromString(*b)
	if errX != nil || errY != nil {
		return *a == *b
	}
	return x.Equal(y)
}
