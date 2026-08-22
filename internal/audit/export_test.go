package audit

// FilterSQLForTest exposes the pure predicate builder to package audit_test. AC #3 is a
// claim about generated SQL text, which no behavioural test can make. This file compiles
// only under `go test`, so the production surface is unchanged.
func FilterSQLForTest(f Filter, subjects, companies []string) (string, []any, error) {
	return filterPredicates(f, searchTargets{subjects: subjects, companies: companies})
}
