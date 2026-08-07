// Unit-level coverage for ruleCategories/categoryKeys that doesn't need the
// DB -- complements the MCAT-0N guard tests in categories_test.go.
package dashboard

import (
	"strings"
	"testing"
)

// Every value must be one of the three known categories -- catches an
// empty/whitespace Category silently dropping a key from every result.
func TestCategories_AllValuesAreKnownCategories(t *testing.T) {
	known := map[Category]bool{
		CategoryFieldCompleteness: true,
		CategoryTaxAccuracy:       true,
		CategoryIdentifiers:       true,
	}
	for key, cat := range ruleCategories {
		if !known[cat] {
			t.Errorf("ruleCategories[%q] = %q, not a known category", key, cat)
		}
	}
}

// An unrecognized Category must return an empty result, not panic.
func TestCategories_UnknownCategoryReturnsEmpty(t *testing.T) {
	for _, cat := range []Category{"bogus-category", ""} {
		if got := categoryKeys(cat); len(got) != 0 {
			t.Errorf("categoryKeys(%q) = %v, want empty", cat, got)
		}
	}
}

// Rule keys are lowercase kebab-case (the DB's convention) -- a case-
// differing key is a distinct map entry, so a stray typo wouldn't be caught.
func TestCategories_KeysAreLowercase(t *testing.T) {
	for key := range ruleCategories {
		if key != strings.ToLower(key) {
			t.Errorf("ruleCategories has non-lowercase key %q", key)
		}
	}
}
