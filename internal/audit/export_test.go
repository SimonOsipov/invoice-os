package audit

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// FilterSQLForTest exposes the pure predicate builder to package audit_test. AC #3 is a
// claim about generated SQL text, which no behavioural test can make. This file compiles
// only under `go test`, so the production surface is unchanged.
func FilterSQLForTest(f Filter, subjects, companies, invoices []string) (string, []any, error) {
	f.scope.admin = true
	return filterPredicates(f, searchTargets{subjects: subjects, companies: companies, invoices: invoices})
}

// ScopedFilterSQLForTest renders f's predicates under the caller scope string, as an admin.
func ScopedFilterSQLForTest(f Filter, scope string) (string, []any, error) {
	f.scope = scopeFromGUC(scope)
	f.scope.admin = true
	return filterPredicates(f, searchTargets{})
}

// AccountScopedFilterSQLForTest is ScopedFilterSQLForTest with the caller's admin flag and subject.
func AccountScopedFilterSQLForTest(f Filter, scope string, admin bool, self string) (string, []any, error) {
	f.scope = scopeFromGUC(scope)
	f.scope.admin = admin
	f.scope.self = self
	return filterPredicates(f, searchTargets{})
}

// QueryAsAdminForTest is Query under an admin caller scope; a bare Query is non-admin and fails closed.
func QueryAsAdminForTest(ctx context.Context, tx pgx.Tx, f Filter) (Response, error) {
	f.scope.admin = true
	return Query(ctx, tx, f)
}

// FirmWideEventsForTest lists the names in firmWideEvents.
func FirmWideEventsForTest() []string {
	out := make([]string, 0, len(firmWideEvents))
	for e := range firmWideEvents {
		out = append(out, e)
	}
	return out
}

// FacetSQLForTest exposes the three built facet statements — event, actor, company, in
// that order — for the same reason: a facet's ORDER BY is invisible to a behavioural test,
// because Postgres returns a small GROUP BY in a stable order whether or not one is
// written. Measured: deleting every facet ORDER BY left the whole DB suite green.
func FacetSQLForTest(f Filter, subjects, companies, invoices []string) ([]string, error) {
	event, actor, company, err := facetStatements(f, searchTargets{subjects: subjects, companies: companies, invoices: invoices})
	if err != nil {
		return nil, err
	}
	return []string{event.sql, actor.sql, company.sql}, nil
}

// SetRecordTenantForTest swaps the customer-write seam and restores it on cleanup.
func SetRecordTenantForTest(t testing.TB, fn func(context.Context, pgx.Tx, string, string, any) error) {
	t.Helper()
	orig := recordTenant
	recordTenant = fn
	t.Cleanup(func() { recordTenant = orig })
}
