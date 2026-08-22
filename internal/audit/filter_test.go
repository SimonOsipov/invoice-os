// AUDIT-04-03: the claims about generated SQL text that no behavioural test can make.
// No DB. Helpers use an fsql* prefix; filt* belongs to filter_db_test.go.
package audit_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// fsqlBuild renders one Filter's predicates, failing on an unexpected error.
func fsqlBuild(t *testing.T, f audit.Filter, subjects, companies []string) string {
	t.Helper()
	where, _, err := audit.FilterSQLForTest(f, subjects, companies)
	if err != nil {
		t.Fatalf("FilterSQLForTest: unexpected error %v", err)
	}
	return where
}

// TestAuditFilter_CompanyPredicateNeverOrsInWorkspaceRows is AC #3. The swallowing
// predicate `entity_id = $1 OR entity_id IS NULL` is what CompanyFilter's closed
// construction exists to make unrepresentable; this asserts the SQL agrees.
func TestAuditFilter_CompanyPredicateNeverOrsInWorkspaceRows(t *testing.T) {
	entity := uuid.NewString()

	cases := []struct {
		mode    string
		company audit.CompanyFilter
		want    []string
		absent  []string
	}{
		{
			mode:    "workspace",
			company: audit.WorkspaceOnly(),
			want:    []string{"a.entity_id IS NULL"},
			absent:  []string{"a.entity_id = $"},
		},
		{
			mode:    "named",
			company: audit.NamedCompany(entity),
			want:    []string{"a.entity_id = $"},
			absent:  []string{"a.entity_id IS NULL"},
		},
		{
			mode:    "all",
			company: audit.AllCompanies(),
			want:    nil,
			absent:  []string{"a.entity_id IS NULL", "a.entity_id = $"},
		},
	}

	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			where := fsqlBuild(t, audit.Filter{Limit: 50, Company: c.company}, nil, nil)

			for _, want := range c.want {
				if !strings.Contains(where, want) {
					t.Errorf("company=%s produced %q, want it to contain %q", c.mode, where, want)
				}
			}
			for _, absent := range c.absent {
				if strings.Contains(where, absent) {
					t.Errorf("company=%s produced %q, want it NOT to contain %q", c.mode, where, absent)
				}
			}
			// The forbidden predicate itself, asserted against every mode rather than
			// only the two that could plausibly emit it.
			for _, forbidden := range []string{"OR a.entity_id IS NULL", "OR entity_id IS NULL"} {
				if strings.Contains(where, forbidden) {
					t.Errorf("company=%s produced %q, which contains the swallowing predicate %q",
						c.mode, where, forbidden)
				}
			}
		})
	}
}

// TestAuditFilter_NamedCompanyBindsTheIDRatherThanInliningIt keeps the named-company
// fragment parameterised: an inlined id would satisfy the AC-3 text assertions above
// while opening an injection path.
func TestAuditFilter_NamedCompanyBindsTheIDRatherThanInliningIt(t *testing.T) {
	entity := uuid.NewString()
	where, args, err := audit.FilterSQLForTest(audit.Filter{Limit: 50, Company: audit.NamedCompany(entity)}, nil, nil)
	if err != nil {
		t.Fatalf("FilterSQLForTest: %v", err)
	}
	if strings.Contains(where, entity) {
		t.Errorf("the entity id is inlined in %q, want it bound as a parameter", where)
	}
	found := false
	for _, a := range args {
		if s, ok := a.(string); ok && s == entity {
			found = true
		}
	}
	if !found {
		t.Errorf("args = %v, want the entity id %s among them", args, entity)
	}
}

// TestAuditFilter_UnknownActorKindIsRefused fences the fail-open hazard: ActorKind is a
// free string, nothing validates it before AUDIT-04-07's handler, and a builder that
// silently emits no fragment for an unrecognised value degrades to "no filter" — which
// TestAuditFilter_EmptyValueAppliesNoFilter would then read as correct.
func TestAuditFilter_UnknownActorKindIsRefused(t *testing.T) {
	for _, kind := range []string{"person", "People", "system ", "robot"} {
		_, _, err := audit.FilterSQLForTest(audit.Filter{Limit: 50, ActorKind: kind}, nil, nil)
		if err == nil {
			t.Errorf("ActorKind = %q was accepted, want an error — an unrecognised kind must not "+
				"silently become 'no filter'", kind)
		}
	}
	for _, kind := range []string{"", "system", "people"} {
		if _, _, err := audit.FilterSQLForTest(audit.Filter{Limit: 50, ActorKind: kind}, nil, nil); err != nil {
			t.Errorf("ActorKind = %q was refused (%v), want it accepted", kind, err)
		}
	}
}

// TestAuditFilter_ActorKindEmitsNoBindParameter pins the fragment as a literal chosen by
// a closed switch. A caller-supplied value reaching the SQL text would be an injection
// path; a bind parameter here would mean the value was concatenated somewhere.
func TestAuditFilter_ActorKindEmitsNoBindParameter(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{"system", "a.actor = 'system'"},
		{"people", "a.actor <> 'system'"},
	} {
		where, args, err := audit.FilterSQLForTest(audit.Filter{Limit: 50, ActorKind: tc.kind}, nil, nil)
		if err != nil {
			t.Fatalf("ActorKind=%s: %v", tc.kind, err)
		}
		if !strings.Contains(where, tc.want) {
			t.Errorf("ActorKind=%s produced %q, want it to contain %q", tc.kind, where, tc.want)
		}
		if len(args) != 0 {
			t.Errorf("ActorKind=%s bound %d args (%v), want 0 — the fragment is a literal", tc.kind, len(args), args)
		}
	}
}

// TestAuditFilter_SearchFragmentIsParenthesised is the lesson internal/invoice/store.go
// records at :663-666. Fragments join with AND, so an unparenthesised OR-group binds as
// (everything AND first) OR rest — every other filter evaporates and the query goes
// tenant-wide with a plausible-looking total.
func TestAuditFilter_SearchFragmentIsParenthesised(t *testing.T) {
	f := audit.Filter{
		Limit:   50,
		Q:       "acme",
		Events:  []string{"invoice.created"},
		Company: audit.NamedCompany(uuid.NewString()),
	}
	where := fsqlBuild(t, f, []string{uuid.NewString()}, []string{uuid.NewString()})

	or := strings.Index(where, " OR ")
	if or == -1 {
		t.Fatalf("where = %q, want a search fragment containing OR", where)
	}
	// Every OR in the built predicate must sit inside a parenthesised group: walk the
	// text and require depth > 0 at each OR.
	depth := 0
	for i := 0; i < len(where); i++ {
		switch where[i] {
		case '(':
			depth++
		case ')':
			depth--
		case 'O':
			if strings.HasPrefix(where[i:], "OR ") && i > 0 && where[i-1] == ' ' && depth == 0 {
				t.Errorf("where = %q has an OR at paren depth 0 (offset %d) — it will swallow every "+
					"other filter", where, i)
			}
		}
	}
}

// TestAuditFilter_EmptyFilterEmitsNoPredicate is AC #6's SQL-level half: an unset field
// must emit no text at all, not a guarded "$1 IS NULL OR ..." form. The guarded form is
// what stops the cursor's row-value comparison folding into the Index Cond.
func TestAuditFilter_EmptyFilterEmitsNoPredicate(t *testing.T) {
	where, args, err := audit.FilterSQLForTest(audit.Filter{Limit: 50}, nil, nil)
	if err != nil {
		t.Fatalf("FilterSQLForTest: %v", err)
	}
	if strings.TrimSpace(where) != "" {
		t.Errorf("an unset Filter produced %q, want the empty string", where)
	}
	if len(args) != 0 {
		t.Errorf("an unset Filter bound %d args (%v), want 0", len(args), args)
	}
}

// TestAuditFilter_CursorIsNeverPartOfThePredicates keeps the cursor out of the shared
// predicate set. It is a position, not a filter: total is built from these predicates and
// must not shrink as the caller pages.
func TestAuditFilter_CursorIsNeverPartOfThePredicates(t *testing.T) {
	where := fsqlBuild(t, audit.Filter{Limit: 50, Cursor: &audit.Cursor{ID: 42}}, nil, nil)
	for _, forbidden := range []string{"created_at, a.id", "a.id) <", "ROW("} {
		if strings.Contains(where, forbidden) {
			t.Errorf("where = %q contains the cursor fragment %q; the cursor belongs to the page "+
				"statement alone, or total counts only the rows after it", where, forbidden)
		}
	}
}
