package dashboard

import "sort"

// Category is one of the three readiness-score bars.
type Category string

const (
	CategoryFieldCompleteness Category = "field_completeness"
	CategoryTaxAccuracy       Category = "tax_accuracy"
	CategoryIdentifiers       Category = "identifiers"
)

// ruleCategories assigns every rule key of every dated rule-set version to exactly one bar.
var ruleCategories = map[string]Category{
	"supplier-tin-required":         CategoryFieldCompleteness,
	"supplier-name-required":        CategoryFieldCompleteness,
	"invoice-number-required":       CategoryFieldCompleteness,
	"issue-date-required":           CategoryFieldCompleteness,
	"currency-required":             CategoryFieldCompleteness,
	"subtotal-required":             CategoryFieldCompleteness,
	"total-required":                CategoryFieldCompleteness,
	"line-items-required":           CategoryFieldCompleteness,
	"buyer-tin-required":            CategoryFieldCompleteness,
	"buyer-city-required":           CategoryFieldCompleteness,
	"buyer-country-required":        CategoryFieldCompleteness,
	"buyer-email-required":          CategoryFieldCompleteness,
	"buyer-lga-required":            CategoryFieldCompleteness,
	"buyer-name-required":           CategoryFieldCompleteness,
	"buyer-postal-zone-required":    CategoryFieldCompleteness,
	"buyer-state-required":          CategoryFieldCompleteness,
	"buyer-street-required":         CategoryFieldCompleteness,
	"invoice-kind-required":         CategoryFieldCompleteness,
	"line-amount-required":          CategoryFieldCompleteness,
	"line-base-quantity-required":   CategoryFieldCompleteness,
	"line-classification-required":  CategoryFieldCompleteness,
	"line-description-required":     CategoryFieldCompleteness,
	"line-item-id-required":         CategoryFieldCompleteness,
	"line-price-required":           CategoryFieldCompleteness,
	"line-price-unit-required":      CategoryFieldCompleteness,
	"line-quantity-required":        CategoryFieldCompleteness,
	"line-tax-category-required":    CategoryFieldCompleteness,
	"line-tax-percent-required":     CategoryFieldCompleteness,
	"line-tax-required":             CategoryFieldCompleteness,
	"supplier-city-required":        CategoryFieldCompleteness,
	"supplier-country-required":     CategoryFieldCompleteness,
	"supplier-email-required":       CategoryFieldCompleteness,
	"supplier-lga-required":         CategoryFieldCompleteness,
	"supplier-postal-zone-required": CategoryFieldCompleteness,
	"supplier-state-required":       CategoryFieldCompleteness,
	"supplier-street-required":      CategoryFieldCompleteness,
	"tax-currency-required":         CategoryFieldCompleteness,

	"vat-required":                    CategoryTaxAccuracy,
	"vat-non-negative":                CategoryTaxAccuracy,
	"vat-standard-rate":               CategoryTaxAccuracy,
	"line-items-sum-subtotal":         CategoryTaxAccuracy,
	"subtotal-non-negative":           CategoryTaxAccuracy,
	"total-non-negative":              CategoryTaxAccuracy,
	"line-cost-non-negative":          CategoryTaxAccuracy,
	"no-duplicate-line-items":         CategoryTaxAccuracy,
	"tax-percent-matches-category":    CategoryTaxAccuracy,
	"vat-equals-tax-subtotals":        CategoryTaxAccuracy,
	"vat-standard-rate-uncategorised": CategoryTaxAccuracy,

	"supplier-tin-format":       CategoryIdentifiers,
	"buyer-tin-format":          CategoryIdentifiers,
	"currency-allowed":          CategoryIdentifiers,
	"buyer-country-allowed":     CategoryIdentifiers,
	"buyer-email-length":        CategoryIdentifiers,
	"buyer-lga-allowed":         CategoryIdentifiers,
	"buyer-name-length":         CategoryIdentifiers,
	"buyer-state-allowed":       CategoryIdentifiers,
	"invoice-kind-allowed":      CategoryIdentifiers,
	"line-hs-code-allowed":      CategoryIdentifiers,
	"line-price-unit-allowed":   CategoryIdentifiers,
	"line-service-code-allowed": CategoryIdentifiers,
	"line-tax-category-allowed": CategoryIdentifiers,
	"supplier-country-allowed":  CategoryIdentifiers,
	"supplier-email-length":     CategoryIdentifiers,
	"supplier-lga-allowed":      CategoryIdentifiers,
	"supplier-name-length":      CategoryIdentifiers,
	"supplier-state-allowed":    CategoryIdentifiers,
	"tax-currency-allowed":      CategoryIdentifiers,
}

// categoryKeys returns cat's rule keys, sorted ascending -- this is the
// text[] value bound to the Q1 aggregation query's per-category parameter.
func categoryKeys(cat Category) []string {
	var keys []string
	for key, c := range ruleCategories {
		if c == cat {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
