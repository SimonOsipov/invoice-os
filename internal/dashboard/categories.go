package dashboard

import "sort"

// Category is one of the three readiness-score bars.
type Category string

const (
	CategoryFieldCompleteness Category = "field_completeness"
	CategoryTaxAccuracy       Category = "tax_accuracy"
	CategoryIdentifiers       Category = "identifiers"
)

// ruleCategories assigns every active v4 rule key to exactly one bar.
var ruleCategories = map[string]Category{
	"supplier-tin-required":   CategoryFieldCompleteness,
	"supplier-name-required":  CategoryFieldCompleteness,
	"invoice-number-required": CategoryFieldCompleteness,
	"issue-date-required":     CategoryFieldCompleteness,
	"currency-required":       CategoryFieldCompleteness,
	"subtotal-required":       CategoryFieldCompleteness,
	"total-required":          CategoryFieldCompleteness,
	"line-items-required":     CategoryFieldCompleteness,
	"buyer-tin-required":      CategoryFieldCompleteness,

	"vat-required":            CategoryTaxAccuracy,
	"vat-non-negative":        CategoryTaxAccuracy,
	"vat-standard-rate":       CategoryTaxAccuracy,
	"line-items-sum-subtotal": CategoryTaxAccuracy,
	"subtotal-non-negative":   CategoryTaxAccuracy,
	"total-non-negative":      CategoryTaxAccuracy,
	"line-cost-non-negative":  CategoryTaxAccuracy,
	"no-duplicate-line-items": CategoryTaxAccuracy,

	"supplier-tin-format": CategoryIdentifiers,
	"buyer-tin-format":    CategoryIdentifiers,
	"currency-allowed":    CategoryIdentifiers,
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
