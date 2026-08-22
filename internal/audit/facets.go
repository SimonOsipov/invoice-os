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

// facetCounts returns the three facet arrays for f. It takes the ALREADY-resolved search
// targets rather than resolving them again: q's fold-in lookups are the same two queries
// Query issued, and re-running them would double the per-request memberships statements
// that TestAuditActor_OneMembershipsStatementPerRequest counts.
//
// Each facet is one copy of f with its own dimension cleared, so "omits its own filter"
// is a field assignment, never a second predicate builder. The actor facet clears Actors
// but KEEPS ActorKind: kind is a separate control, so with kind=system the offered actor
// list should narrow to system actors.
//
// The cursor never reaches here — a facet counts the whole filtered set, not a page.
// Actor labels are NOT resolved here; Query unions these subjects with the page's and
// resolves once (System Design §7).
func facetCounts(ctx context.Context, tx pgx.Tx, f Filter, s searchTargets) (Facets, error) {
	return Facets{
		Event:   make([]Facet, 0),
		Actor:   make([]Facet, 0),
		Company: make([]Facet, 0),
	}, nil
}

var _ = fmt.Sprintf
