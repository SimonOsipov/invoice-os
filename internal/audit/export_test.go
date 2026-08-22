package audit

// FilterSQLForTest exposes the pure predicate builder to package audit_test. AC #3 is a
// claim about generated SQL text, which no behavioural test can make. This file compiles
// only under `go test`, so the production surface is unchanged.
func FilterSQLForTest(f Filter, subjects, companies []string) (string, []any, error) {
	return filterPredicates(f, searchTargets{subjects: subjects, companies: companies})
}

// FacetSQLForTest exposes the three built facet statements — event, actor, company, in
// that order — for the same reason: a facet's ORDER BY is invisible to a behavioural test,
// because Postgres returns a small GROUP BY in a stable order whether or not one is
// written. Measured: deleting every facet ORDER BY left the whole DB suite green.
func FacetSQLForTest(f Filter, subjects, companies []string) ([]string, error) {
	event, actor, company, err := facetStatements(f, searchTargets{subjects: subjects, companies: companies})
	if err != nil {
		return nil, err
	}
	return []string{event.sql, actor.sql, company.sql}, nil
}
