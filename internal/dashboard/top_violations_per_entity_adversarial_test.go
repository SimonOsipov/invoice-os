// QA adversarial coverage on top of top_violations_per_entity_test.go's
// MTV-01..07 (task-427). Those specs prove per-entity splitting, cross-entity
// summing, and a 2-way root tie; this file closes gaps in the new per-entity
// grouping code path they don't touch: an entity with zero invoices (so it
// never appears in Clients at all -- does the byEntity map lookup handle
// that silently?), a 3-way root tie fed by mixed single/multi-entity
// contributions, and a fanout wide enough to stress the Go-side sort past
// the 3-entity cases already covered.
package dashboard

import (
	"fmt"
	"testing"
)

// An entity with zero invoices never gets a Clients row (DASH-12) -- this
// confirms that holds when other entities carry TopViolations too, and that
// the entity contributes nothing to the root sum.
func TestStoreRollup_TopViolationsPerEntityEntityWithZeroInvoicesAbsentFromClientsAndRoot(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "zero-invoice entity tenant")
	e1 := seedEntity(t, super, tenantID, "has violations")
	e2 := seedEntity(t, super, tenantID, "clean invoices")
	seedEntity(t, super, tenantID, "zero invoices") // never referenced again

	seedInvoiceWithViolations(t, super, tenantID, e1, "ZERO-e1", "draft",
		`[{"rule_key":"only-rule","severity":"error","message":"m"}]`)
	seedInvoice(t, super, tenantID, e2, "ZERO-e2-clean")

	got := rollupFor(t, app, tenantID)

	if len(got.Clients) != 2 {
		t.Fatalf("Clients = %+v, want exactly 2 rows (zero-invoice entity must be absent)", got.Clients)
	}
	assertTopViolations(t, clientFor(t, got, e1).TopViolations, []RuleCount{{RuleKey: "only-rule", Invoices: 1}})
	assertTopViolations(t, clientFor(t, got, e2).TopViolations, []RuleCount{})

	want := []RuleCount{{RuleKey: "only-rule", Invoices: 1}}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}

// A 3-way tie at the root, fed by mixed contribution shapes (one rule from a
// single entity, one summed across two entities, one summed across three) --
// MTV-03/the existing root tie-break test only exercise a 2-way tie. All
// three must land at 3 and sort rule_key ASC.
func TestStoreRollup_TopViolationsPerEntityThreeWayTieAtRootAcrossMixedContributions(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "3-way tie tenant")
	e1 := seedEntity(t, super, tenantID, "tie entity 1")
	e2 := seedEntity(t, super, tenantID, "tie entity 2")
	e3 := seedEntity(t, super, tenantID, "tie entity 3")

	broken := func(rule string) string {
		return fmt.Sprintf(`[{"rule_key":%q,"severity":"error","message":"m"}]`, rule)
	}
	// rule-alpha: all 3 from e1 alone.
	for i := 0; i < 3; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e1, fmt.Sprintf("TIE3-alpha-%d", i), "draft", broken("rule-alpha"))
	}
	// rule-mid: 2 from e2, 1 from e3.
	for i := 0; i < 2; i++ {
		seedInvoiceWithViolations(t, super, tenantID, e2, fmt.Sprintf("TIE3-mid-e2-%d", i), "draft", broken("rule-mid"))
	}
	seedInvoiceWithViolations(t, super, tenantID, e3, "TIE3-mid-e3", "draft", broken("rule-mid"))
	// rule-zulu: 1 from each of e1, e2, e3.
	seedInvoiceWithViolations(t, super, tenantID, e1, "TIE3-zulu-e1", "draft", broken("rule-zulu"))
	seedInvoiceWithViolations(t, super, tenantID, e2, "TIE3-zulu-e2", "draft", broken("rule-zulu"))
	seedInvoiceWithViolations(t, super, tenantID, e3, "TIE3-zulu-e3", "draft", broken("rule-zulu"))

	got := rollupFor(t, app, tenantID)

	want := []RuleCount{
		{RuleKey: "rule-alpha", Invoices: 3},
		{RuleKey: "rule-mid", Invoices: 3},
		{RuleKey: "rule-zulu", Invoices: 3},
	}
	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}

// Fanout across 8 entities, each contributing one rule at a distinct count,
// stresses the Go-side sort past the 3-entity cases the RED suite covers --
// wide enough that a bug in the sort (e.g. only comparing the first N
// elements, or an unstable partial sort) would show up as a misordering
// somewhere past the first couple of entries.
func TestStoreRollup_TopViolationsPerEntityWideFanoutAcrossManyEntitiesSortsCorrectly(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "wide fanout tenant")

	// counts strictly descending, one rule per entity, invoices DESC then
	// rule_key ASC gives a fully determined expected order.
	specs := []struct {
		rule  string
		count int
	}{
		{"rule-h", 8}, {"rule-g", 7}, {"rule-f", 6}, {"rule-e", 5},
		{"rule-d", 4}, {"rule-c", 3}, {"rule-b", 2}, {"rule-a", 1},
	}
	want := make([]RuleCount, 0, len(specs))
	for _, s := range specs {
		e := seedEntity(t, super, tenantID, "fanout entity "+s.rule)
		for i := 0; i < s.count; i++ {
			seedInvoiceWithViolations(t, super, tenantID, e, fmt.Sprintf("WIDE-%s-%d", s.rule, i), "draft",
				fmt.Sprintf(`[{"rule_key":%q,"severity":"error","message":"m"}]`, s.rule))
		}
		want = append(want, RuleCount{RuleKey: s.rule, Invoices: s.count})
	}

	got := rollupFor(t, app, tenantID)

	assertTopViolations(t, got.TopViolations, want)
	assertTopViolations(t, got.Totals.TopViolations, want)
}
