// AUDIT-04-05: the claims about generated facet SQL that no behavioural test can make.
// No DB.
//
// These exist because a measured mutation survived the whole DB suite: deleting every
// facet ORDER BY left `make test-audit` green, since Postgres returns a small GROUP BY in
// a stable order whether or not one is written and only reshuffles once the plan flips to
// a hash aggregate. The byte-identity comparisons in facets_db_test.go would then flake in
// CI rather than fail here.
package audit_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
)

// fctsqlNames labels the three statements FacetSQLForTest returns, in its order.
var fctsqlNames = []string{"event", "actor", "company"}

// fctsqlBuild renders the three facet statements, failing on an unexpected error.
func fctsqlBuild(t *testing.T, f audit.Filter) []string {
	t.Helper()
	got, err := audit.FacetSQLForTest(f, nil, nil)
	if err != nil {
		t.Fatalf("FacetSQLForTest: unexpected error %v", err)
	}
	if len(got) != len(fctsqlNames) {
		t.Fatalf("FacetSQLForTest returned %d statements, want %d", len(got), len(fctsqlNames))
	}
	return got
}

// TestAuditFacetSQL_EveryFacetIsOrdered is the fence the DB suite cannot provide. Every
// facet must order by count and then break ties on a value, or the buckets reshuffle
// between requests and a picker becomes unreadable.
func TestAuditFacetSQL_EveryFacetIsOrdered(t *testing.T) {
	for i, sql := range fctsqlBuild(t, audit.Filter{Limit: 50}) {
		name := fctsqlNames[i]
		idx := strings.Index(sql, "ORDER BY count(*) DESC")
		if idx == -1 {
			t.Errorf("the %s facet has no `ORDER BY count(*) DESC`:\n%s", name, sql)
			continue
		}
		// A count-only ORDER BY still reshuffles equal buckets, which is exactly the
		// corpus shape facets_db_test.go compares byte-for-byte.
		if !strings.Contains(sql[idx:], "DESC, ") {
			t.Errorf("the %s facet orders by count with no value tiebreak; equal counts would "+
				"reshuffle:\n%s", name, sql)
		}
	}
}

// TestAuditFacetSQL_EachFacetOmitsItsOwnFilterAndKeepsTheRest is AC #1 and AC #2 asserted
// on the SQL rather than on counts. A behavioural test can only see that the numbers did
// not move; this sees WHY, and cannot be satisfied by a fixture where the omitted filter
// happened to exclude nothing.
func TestAuditFacetSQL_EachFacetOmitsItsOwnFilterAndKeepsTheRest(t *testing.T) {
	sqls := fctsqlBuild(t, audit.Filter{
		Limit:     50,
		From:      time.Now().Add(-24 * time.Hour),
		Events:    []string{"invoice.created"},
		Actors:    []string{uuid.NewString()},
		ActorKind: "people",
		Company:   audit.NamedCompany(uuid.NewString()),
	})

	cases := []struct {
		name   string
		sql    string
		absent string
		want   []string
	}{
		{
			name:   "event",
			sql:    sqls[0],
			absent: "a.event = ANY(",
			want:   []string{"a.actor = ANY(", "a.entity_id = $", "a.created_at >=", "a.actor <> 'system'"},
		},
		{
			name:   "actor",
			sql:    sqls[1],
			absent: "a.actor = ANY(",
			// ActorKind survives here on purpose: it is a separate control, so kind=system
			// must narrow the actors the picker offers (task-624, judgment call 1).
			want: []string{"a.event = ANY(", "a.entity_id = $", "a.created_at >=", "a.actor <> 'system'"},
		},
		{
			name:   "company",
			sql:    sqls[2],
			absent: "a.entity_id = $",
			want:   []string{"a.event = ANY(", "a.actor = ANY(", "a.created_at >=", "a.actor <> 'system'"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if strings.Contains(c.sql, c.absent) {
				t.Errorf("the %s facet applies its own filter %q; selecting a value would empty its "+
					"own picker:\n%s", c.name, c.absent, c.sql)
			}
			for _, want := range c.want {
				if !strings.Contains(c.sql, want) {
					t.Errorf("the %s facet is missing %q; a facet outside the other filters offers "+
						"values that return nothing:\n%s", c.name, want, c.sql)
				}
			}
		})
	}
}

// TestAuditFacetSQL_CompanyFacetDropsTheWorkspaceFilterToo pins the other half of the
// company facet's own dimension. WorkspaceOnly renders as `a.entity_id IS NULL`, a
// different fragment from the named-company one, so omitting one does not omit the other.
func TestAuditFacetSQL_CompanyFacetDropsTheWorkspaceFilterToo(t *testing.T) {
	sqls := fctsqlBuild(t, audit.Filter{Limit: 50, Company: audit.WorkspaceOnly()})

	// The predicate, not the GROUP BY / join text, which also mentions the column.
	if strings.Contains(sqls[2], "WHERE a.entity_id IS NULL") ||
		strings.Contains(sqls[2], "AND a.entity_id IS NULL") {
		t.Errorf("the company facet applies the workspace filter; the companies would vanish from "+
			"the picker the moment workspace was selected:\n%s", sqls[2])
	}
	// The other two facets keep it, because it is not their own filter.
	for i := 0; i < 2; i++ {
		if !strings.Contains(sqls[i], "a.entity_id IS NULL") {
			t.Errorf("the %s facet dropped the workspace filter, which is not its own:\n%s",
				fctsqlNames[i], sqls[i])
		}
	}
}

// TestAuditFacetSQL_NoCursorReachesAFacet keeps the keyset position out. A facet counts the
// whole filtered set; a cursor in one would make the numbers shrink as the reader paged.
func TestAuditFacetSQL_NoCursorReachesAFacet(t *testing.T) {
	sqls := fctsqlBuild(t, audit.Filter{Limit: 50, Cursor: &audit.Cursor{ID: 42}})
	for i, sql := range sqls {
		for _, forbidden := range []string{"a.id) <", "(a.created_at, a.id)", "ROW("} {
			if strings.Contains(sql, forbidden) {
				t.Errorf("the %s facet contains the cursor fragment %q; its counts would shrink as "+
					"the reader paged:\n%s", fctsqlNames[i], forbidden, sql)
			}
		}
		if strings.Contains(sql, "LIMIT") {
			t.Errorf("the %s facet carries a LIMIT; a facet counts the whole filtered set, not a "+
				"page:\n%s", fctsqlNames[i], sql)
		}
	}
}

// TestAuditFacetSQL_UnknownActorKindIsRefused keeps the fail-closed rule from filter.go
// reaching the facets: an unrecognised kind must error rather than degrade to "no filter"
// on three more statements.
func TestAuditFacetSQL_UnknownActorKindIsRefused(t *testing.T) {
	if _, err := audit.FacetSQLForTest(audit.Filter{Limit: 50, ActorKind: "robot"}, nil, nil); err == nil {
		t.Error("an unknown actor kind was accepted by the facet builder, want an error")
	}
}
