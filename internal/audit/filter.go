// filter.go: Filter's seven fields rendered as SQL. Kept apart from reader.go because
// Query and the count statement must share ONE built predicate set — a filter applied to
// the page but not the count makes total a lie with every test still green.
package audit

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// searchTargets holds the two fold-in lookups: the subjects whose display name matches q,
// and the companies whose name does. Both come back as column predicates, which is the
// only way free text can reach an actor or a company under FORCE RLS.
type searchTargets struct {
	subjects  []string
	companies []string
}

// resolveSearchTargets runs the two fold-in lookups on tx. RLS on tx is the only scope —
// no tenant predicate, the same rule as actor.Resolve. Neither list is capped: a cap
// would silently drop matches, and both tables are small per tenant.
func resolveSearchTargets(ctx context.Context, tx pgx.Tx, q string) (searchTargets, error) {
	if q == "" {
		return searchTargets{}, nil
	}
	like := escapeLike(q)

	var out searchTargets
	// memberships.display_name is nullable and unindexed, so this is a Seq Scan by
	// construction. It only searches display_name, not the email actor.Resolve falls
	// back to — an actor rendered by their email is not reachable by the name shown.
	rows, err := tx.Query(ctx,
		`SELECT user_id::text FROM memberships WHERE display_name ILIKE '%' || $1 || '%' ESCAPE '\'`, like)
	if err != nil {
		return searchTargets{}, fmt.Errorf("audit: resolve search subjects: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return searchTargets{}, fmt.Errorf("audit: scan search subject: %w", err)
		}
		out.subjects = append(out.subjects, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return searchTargets{}, fmt.Errorf("audit: read search subjects: %w", err)
	}

	rows, err = tx.Query(ctx,
		`SELECT id::text FROM business_entities WHERE name ILIKE '%' || $1 || '%' ESCAPE '\'`, like)
	if err != nil {
		return searchTargets{}, fmt.Errorf("audit: resolve search companies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return searchTargets{}, fmt.Errorf("audit: scan search company: %w", err)
		}
		out.companies = append(out.companies, id)
	}
	if err := rows.Err(); err != nil {
		return searchTargets{}, fmt.Errorf("audit: read search companies: %w", err)
	}
	return out, nil
}

// filterPredicates renders f's seven filter fields as AND-joined fragments over the `a`
// alias, with their bind arguments. Pure: no ctx, no tx.
//
// The cursor is deliberately absent — it belongs to the page statement alone, so total
// cannot shrink as the caller pages (TestAuditFilter_CursorIsNeverPartOfThePredicates).
// Every fragment is `a.`-qualified because the count statement aliases audit_log without
// the business_entities join; a fragment that reached for be.name would break the count
// at runtime, not compile time.
func filterPredicates(f Filter, s searchTargets) (string, []any, error) {
	var conditions []string
	var args []any

	bind := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if !f.From.IsZero() {
		conditions = append(conditions, "a.created_at >= "+bind(f.From))
	}
	if !f.To.IsZero() {
		conditions = append(conditions, "a.created_at <= "+bind(f.To))
	}
	if len(f.Events) > 0 {
		conditions = append(conditions, "a.event = ANY("+bind(f.Events)+")")
	}
	if len(f.Actors) > 0 {
		conditions = append(conditions, "a.actor = ANY("+bind(f.Actors)+")")
	}

	// A literal with no bind parameter, chosen by a closed switch: the caller's string
	// never reaches the SQL. An unrecognised kind is refused rather than dropped —
	// silently emitting nothing would degrade to "no filter", which is what
	// TestAuditFilter_EmptyValueAppliesNoFilter asserts is correct for an EMPTY value.
	switch f.ActorKind {
	case "":
	case "system":
		conditions = append(conditions, "a.actor = 'system'")
	case "people":
		conditions = append(conditions, "a.actor <> 'system'")
	default:
		return "", nil, fmt.Errorf("audit: unknown actor kind %q, want \"\", \"system\" or \"people\"", f.ActorKind)
	}

	switch f.Company.Mode() {
	case ModeAllCompanies:
	case ModeNamedCompany:
		conditions = append(conditions, "a.entity_id = "+bind(f.Company.ID())+"::uuid")
	case ModeWorkspaceOnly:
		// Never `entity_id = $1 OR entity_id IS NULL`: that predicate swallows every
		// company's rows into the workspace view (TestAuditFilter_CompanyPredicateNeverOrsInWorkspaceRows).
		conditions = append(conditions, "a.entity_id IS NULL")
	default:
		return "", nil, fmt.Errorf("audit: unknown company mode %d", f.Company.Mode())
	}

	if f.Q != "" {
		conditions = append(conditions, searchFragment(f.Q, s, bind))
	}

	if len(conditions) == 0 {
		return "", nil, nil
	}
	return strings.Join(conditions, " AND "), args, nil
}

// searchFragment is q's four routes as one parenthesised OR-group. The parentheses are
// load-bearing: conditions join with AND, so a bare `x OR y` would bind as
// `(everything AND x) OR y` and every other filter would silently evaporate, leaving a
// tenant-wide result with a plausible-looking total.
//
// The payload route matches VALUES, via jsonb_each_text, not payload::text. payload::text
// renders the key names too, and `id` alone appears as a key on nearly every row — a
// measured 50,000 of 50,000. The two fold-in arms are omitted when their lookup found
// nothing; the two text arms always appear, so the group can never collapse to nothing
// and fall back to the unfiltered set.
func searchFragment(q string, s searchTargets, bind func(any) string) string {
	like := bind(escapeLike(q))
	arms := []string{
		`a.event ILIKE '%' || ` + like + ` || '%' ESCAPE '\'`,
		`EXISTS (SELECT 1 FROM jsonb_each_text(a.payload) kv
		          WHERE kv.value ILIKE '%' || ` + like + ` || '%' ESCAPE '\')`,
	}
	if len(s.subjects) > 0 {
		arms = append(arms, "a.actor = ANY("+bind(s.subjects)+")")
	}
	if len(s.companies) > 0 {
		arms = append(arms, "a.entity_id = ANY("+bind(s.companies)+"::uuid[])")
	}
	return "(" + strings.Join(arms, " OR ") + ")"
}

// escapeLike neutralises the LIKE/ILIKE metacharacters in a user-supplied string so it
// matches literally under an explicit ESCAPE '\' clause. Order matters: backslash first,
// or the backslashes this introduces get escaped again. Copied from internal/invoice,
// which cannot be imported here — it imports this package.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}
