package invoice

import "strings"

// SupplierContact holds the company record's NRS contact and address.
type SupplierContact struct {
	Email, Telephone, Street, City, PostalZone, Country, State, LGA *string
}

// DefaultSupplierContact fills each nil supplier contact field of in from c.
// A non-nil caller value, "" included, wins; a nil or blank company value is no default.
func DefaultSupplierContact(in *CreateInput, c SupplierContact) {
	fill := func(dst **string, src *string) {
		if *dst != nil || src == nil {
			return
		}
		if v := strings.TrimSpace(*src); v != "" {
			*dst = &v
		}
	}
	fill(&in.SupplierEmail, c.Email)
	fill(&in.SupplierTelephone, c.Telephone)
	fill(&in.SupplierStreet, c.Street)
	fill(&in.SupplierCity, c.City)
	fill(&in.SupplierPostalZone, c.PostalZone)
	fill(&in.SupplierCountry, c.Country)
	fill(&in.SupplierState, c.State)
	fill(&in.SupplierLGA, c.LGA)
}
