// facets.go: the three facet counts (AUDIT-04-05). Each facet omits its OWN filter and
// applies the rest, so the numbers a control shows are the numbers picking it would give.
// Split from filter.go because these are three GROUP BY statements, not predicates; they
// reuse filterPredicates rather than deriving SQL a second time.
package audit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// facetStmt is one built facet statement. Building is kept apart from executing so the
// generated SQL can be asserted without a database: the bucket ORDER BY is invisible to
// every behavioural test, because Postgres returns a small GROUP BY in a stable order
// anyway and only reshuffles once the plan flips to a hash aggregate.
// TestAuditFacetSQL_EveryFacetIsOrdered is what actually holds it.
type facetStmt struct {
	sql  string
	args []any
}

// facetStatements builds the three statements for f: event, actor, company, in that order.
// Each is one copy of f with its own dimension cleared, so "omits its own filter" is a
// field assignment and never a second predicate builder.
//
// The actor facet clears Actors but KEEPS ActorKind: kind is a separate control, so with
// kind=system the offered actor list narrows to system actors. The cursor never reaches
// here — a facet counts the whole filtered set, not a page.
func facetStatements(f Filter, s searchTargets) (event, actor, company facetStmt, err error) {
	eventFilter, actorFilter, companyFilter := f, f, f
	eventFilter.Events = nil
	actorFilter.Actors = nil
	companyFilter.Company = AllCompanies()

	if event, err = facetColumnStmt(eventFilter, s, "a.event"); err != nil {
		return facetStmt{}, facetStmt{}, facetStmt{}, fmt.Errorf("audit: event facet: %w", err)
	}
	if actor, err = facetColumnStmt(actorFilter, s, "a.actor"); err != nil {
		return facetStmt{}, facetStmt{}, facetStmt{}, fmt.Errorf("audit: actor facet: %w", err)
	}
	if company, err = facetCompanyStmt(companyFilter, s); err != nil {
		return facetStmt{}, facetStmt{}, facetStmt{}, fmt.Errorf("audit: company facet: %w", err)
	}
	return event, actor, company, nil
}

// facetColumnStmt counts one NOT NULL text column of audit_log. No join: event and actor
// are stored values, not references, which is what keeps a departed member a filterable
// actor (Core AC 7) — the bucket exists because their ROWS do.
//
// column is concatenated into the SQL. Both call sites are literals in this file and the
// function is unexported, so no caller value reaches the statement; every value that does
// is bound by filterPredicates.
func facetColumnStmt(f Filter, s searchTargets, column string) (facetStmt, error) {
	where, args, err := filterPredicates(f, s)
	if err != nil {
		return facetStmt{}, err
	}
	if where != "" {
		where = " WHERE " + where
	}
	return facetStmt{
		sql: `SELECT ` + column + `, count(*) FROM audit_log a` + where +
			` GROUP BY ` + column +
			` ORDER BY count(*) DESC, ` + column + ` ASC`,
		args: args,
	}, nil
}

// facetCompanyStmt counts by entity_id, joining business_entities for the display name.
// The NULL group comes free and IS the workspace-level bucket (contract §4) — the
// workspace UNION unattributed partition, not company_scope='workspace' (D-23/D-28). A
// company deleted since the row was written keeps its bucket with a nil name, the same
// shape the page rows use.
func facetCompanyStmt(f Filter, s searchTargets) (facetStmt, error) {
	where, args, err := filterPredicates(f, s)
	if err != nil {
		return facetStmt{}, err
	}
	if where != "" {
		where = " WHERE " + where
	}
	return facetStmt{
		sql: `SELECT a.entity_id, be.name, count(*)
	          FROM audit_log a
	          LEFT JOIN business_entities be ON be.id = a.entity_id` + where + `
	         GROUP BY a.entity_id, be.name
	         ORDER BY count(*) DESC, be.name ASC NULLS LAST, a.entity_id ASC NULLS LAST`,
		args: args,
	}, nil
}

// facetCounts returns the three facet arrays for f. It takes the ALREADY-resolved search
// targets rather than resolving them again: q's fold-in lookups are the same two queries
// Query issued, and re-running them would double the per-request memberships statements
// TestAuditActor_OneMembershipsStatementPerRequest counts.
//
// Actor labels are NOT resolved here; Query unions these subjects with the page's and
// resolves once (System Design §7), which is why the actor buckets come back with Name
// and Kind unset.
func facetCounts(ctx context.Context, tx pgx.Tx, f Filter, s searchTargets) (Facets, error) {
	eventStmt, actorStmt, companyStmt, err := facetStatements(f, s)
	if err != nil {
		return Facets{}, err
	}

	event, err := facetScanColumn(ctx, tx, eventStmt)
	if err != nil {
		return Facets{}, fmt.Errorf("audit: event facet: %w", err)
	}
	actor, err := facetScanColumn(ctx, tx, actorStmt)
	if err != nil {
		return Facets{}, fmt.Errorf("audit: actor facet: %w", err)
	}
	company, err := facetScanCompany(ctx, tx, companyStmt)
	if err != nil {
		return Facets{}, fmt.Errorf("audit: company facet: %w", err)
	}
	return Facets{Event: event, Actor: actor, Company: company}, nil
}

// facetScanColumn reads a two-column (value, count) facet. The column is NOT NULL, so each
// row scans into a fresh string whose address becomes the bucket's Value.
func facetScanColumn(ctx context.Context, tx pgx.Tx, st facetStmt) ([]Facet, error) {
	rows, err := tx.Query(ctx, st.sql, st.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Facet, 0, 8)
	for rows.Next() {
		var b Facet
		var value string
		if err := rows.Scan(&value, &b.Count); err != nil {
			return nil, err
		}
		b.Value = &value
		out = append(out, b)
	}
	return out, rows.Err()
}

// facetScanCompany reads the three-column (entity_id, name, count) facet. Both entity_id
// and name are nullable and scan straight into the *string fields.
func facetScanCompany(ctx context.Context, tx pgx.Tx, st facetStmt) ([]Facet, error) {
	rows, err := tx.Query(ctx, st.sql, st.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Facet, 0, 8)
	for rows.Next() {
		var b Facet
		if err := rows.Scan(&b.Value, &b.Name, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
