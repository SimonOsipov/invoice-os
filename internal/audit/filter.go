// filter.go: Filter's seven fields rendered as SQL. Kept apart from reader.go because
// Query and the count statement must share ONE built predicate set — a filter applied to
// the page but not the count makes total a lie with every test still green.
package audit

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// searchTargets holds the two A-8 fold-in lookups: the subjects whose display name
// matches q, and the companies whose name does. Both come back as column predicates,
// which is the only way free text can reach an actor or a company at all.
type searchTargets struct {
	subjects  []string
	companies []string
}

// resolveSearchTargets runs the two fold-in lookups on tx. RLS on tx is the only scope —
// no tenant predicate, the same rule as actor.Resolve.
func resolveSearchTargets(ctx context.Context, tx pgx.Tx, q string) (searchTargets, error) {
	return searchTargets{}, nil
}

// filterPredicates renders f's seven filter fields as AND-joined fragments over the `a`
// alias, and returns them with their bind arguments. Pure: no ctx, no tx. The cursor is
// deliberately absent — it belongs to the page statement alone, so total cannot shrink as
// the caller pages.
func filterPredicates(f Filter, s searchTargets) (string, []any, error) {
	return "", nil, nil
}

// escapeLike neutralises the LIKE/ILIKE metacharacters in a user-supplied string so it
// matches literally under an explicit ESCAPE '\' clause. Order matters: backslash first.
// Copied from internal/invoice, which cannot be imported here — it imports this package.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}
