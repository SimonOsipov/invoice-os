// AUDIT-04-05: the three facet counts against a real Postgres — each computed within the
// OTHER filters, the company facet's workspace bucket, and the arithmetic that ties the
// buckets to total.
//
// Helpers use an fct* prefix; plan/trigger/scoped/reader/page/filt/fsql/act are taken.
// Every case reads facets off audit.Query rather than the unexported counter, because
// Query is what assembles page + total + facets into the one response the endpoint
// returns (A-2) — a facet that is right in isolation and wrong in the envelope is still
// a bug on the wire.
package audit_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- helpers --------------------------------------------------------------------------

// fctCounts folds a facet array to value -> count. The company facet's workspace bucket
// has a nil Value and lands under the "<workspace>" key, which no entity id can collide
// with.
func fctCounts(buckets []audit.Facet) map[string]int {
	out := make(map[string]int, len(buckets))
	for _, b := range buckets {
		if b.Value == nil {
			out["<workspace>"] += b.Count
			continue
		}
		out[*b.Value] += b.Count
	}
	return out
}

// fctSum totals a facet array.
func fctSum(buckets []audit.Facet) int {
	n := 0
	for _, b := range buckets {
		n += b.Count
	}
	return n
}

// fctJSON renders a value the way the endpoint would, for the byte-identity claims.
func fctJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// fctRequireNonEmpty fails when a facet array is empty. Every comparison below is between
// two facet computations, and two empty arrays compare equal — this is what stops a facet
// that returns nothing from passing as a facet that returns the right thing.
func fctRequireNonEmpty(t *testing.T, label string, buckets []audit.Facet) {
	t.Helper()
	if len(buckets) == 0 {
		t.Fatalf("%s facet is empty; the comparison it feeds would be vacuous", label)
	}
}

// fctDeleteMembership removes a member while their audit rows stay — the departure that
// Core AC 7 is about.
func fctDeleteMembership(t *testing.T, f *fixture, p pageFixture, userID string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
			p.tenant, userID)
		return e
	}); err != nil {
		t.Fatalf("delete membership %s: %v", userID, err)
	}
}

// --- AC #1: every OTHER filter moves the numbers ----------------------------------------

// TestAuditFacets_CountsShiftWithEveryOtherFilter is AC #1 across all four dimensions it
// names, not just the date. The event facet is the probe: narrowing on date, actor,
// company or search must pull its counts down, because a facet computed OUTSIDE the other
// filters answers the same numbers every time and the UI then offers a value that returns
// nothing.
func TestAuditFacets_CountsShiftWithEveryOtherFilter(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	base := pageQuery(t, f, c.p, audit.Filter{Limit: 5})
	fctRequireNonEmpty(t, "baseline event", base.Facets.Event)
	baseCounts := fctCounts(base.Facets.Event)

	cases := []struct {
		dimension string
		filter    audit.Filter
	}{
		{"date", audit.Filter{Limit: 5, From: time.Now().Add(-7 * 24 * time.Hour)}},
		{"actor", audit.Filter{Limit: 5, Actors: []string{c.actors[0]}}},
		{"company", audit.Filter{Limit: 5, Company: audit.NamedCompany(c.entityA)}},
		{"search", audit.Filter{Limit: 5, Q: "alpha"}},
	}
	if len(cases) != 4 {
		t.Fatalf("case table has %d entries, want the four dimensions AC #1 names", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.dimension, func(t *testing.T) {
			got := pageQuery(t, f, c.p, tc.filter)
			fctRequireNonEmpty(t, tc.dimension+" event", got.Facets.Event)
			narrowed := fctCounts(got.Facets.Event)

			strictlyLower := false
			for event, want := range baseCounts {
				have := narrowed[event] // an absent event reads as 0, which is still <=
				if have > want {
					t.Errorf("%s: event %q counts %d under the narrowed filter but %d unfiltered; "+
						"a facet within the other filters can only shrink", tc.dimension, event, have, want)
				}
				if have < want {
					strictlyLower = true
				}
			}
			if !strictlyLower {
				t.Errorf("%s: narrowing left every event count unchanged (%v vs %v) — the event "+
					"facet is being computed outside the other filters", tc.dimension, narrowed, baseCounts)
			}
			// A narrowed set that matched nothing would satisfy "every count <=" trivially.
			if fctSum(got.Facets.Event) == 0 {
				t.Errorf("%s: the narrowed event facet sums to 0; the fixture does not exercise "+
					"this dimension", tc.dimension)
			}
		})
	}
}

// --- AC #2: a facet ignores its OWN filter ----------------------------------------------

// TestAuditFacets_EventFacetIgnoresTheEventFilterItself is AC #2. Selecting a value must
// leave that facet's own numbers alone, or the list collapses to the one chosen value and
// the reader can no longer see what else there is to switch to.
func TestAuditFacets_EventFacetIgnoresTheEventFilterItself(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	before := pageQuery(t, f, c.p, audit.Filter{Limit: 5})
	after := pageQuery(t, f, c.p, audit.Filter{Limit: 5, Events: []string{c.events[0]}})

	fctRequireNonEmpty(t, "unselected event", before.Facets.Event)
	fctRequireNonEmpty(t, "selected event", after.Facets.Event)
	if len(before.Facets.Event) != len(c.events) {
		t.Fatalf("the unselected event facet has %d buckets, want the corpus's %d events — the "+
			"comparison below would not be over the whole set", len(before.Facets.Event), len(c.events))
	}

	if got, want := fctJSON(t, after.Facets.Event), fctJSON(t, before.Facets.Event); got != want {
		t.Errorf("selecting event %q changed the event facet\n got: %s\nwant: %s", c.events[0], got, want)
	}
	// The selection must still have bitten somewhere, or this asserts nothing.
	if after.Total >= before.Total {
		t.Errorf("total = %d with event %q selected and %d without; the selection did not filter",
			after.Total, c.events[0], before.Total)
	}
}

// TestAuditFacets_ActorFacetIgnoresActorsButHonoursActorKind is the reading recorded on
// task-624: "its own filter" is the one the facet DRIVES. The actor facet populates the
// actor multi-select, so Actors is its own; ActorKind is a separate control, and with
// kind=system the offered actors must narrow to the system ones.
func TestAuditFacets_ActorFacetIgnoresActorsButHonoursActorKind(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	base := pageQuery(t, f, c.p, audit.Filter{Limit: 5})
	fctRequireNonEmpty(t, "baseline actor", base.Facets.Actor)
	if len(base.Facets.Actor) != len(c.actors) {
		t.Fatalf("the baseline actor facet has %d buckets, want the corpus's %d actors",
			len(base.Facets.Actor), len(c.actors))
	}

	t.Run("ignores its own Actors filter", func(t *testing.T) {
		got := pageQuery(t, f, c.p, audit.Filter{Limit: 5, Actors: []string{c.actors[0]}})
		if a, b := fctJSON(t, got.Facets.Actor), fctJSON(t, base.Facets.Actor); a != b {
			t.Errorf("selecting one actor changed the actor facet\n got: %s\nwant: %s", a, b)
		}
		if got.Total >= base.Total {
			t.Errorf("total = %d with one actor selected and %d without; the selection did not filter",
				got.Total, base.Total)
		}
	})

	t.Run("honours ActorKind", func(t *testing.T) {
		sys := pageQuery(t, f, c.p, audit.Filter{Limit: 5, ActorKind: "system"})
		fctRequireNonEmpty(t, "system actor", sys.Facets.Actor)
		for _, b := range sys.Facets.Actor {
			if b.Value == nil || *b.Value != "system" {
				t.Errorf("kind=system offered actor %v, want only \"system\"", b.Value)
			}
		}

		people := pageQuery(t, f, c.p, audit.Filter{Limit: 5, ActorKind: "people"})
		fctRequireNonEmpty(t, "people actor", people.Facets.Actor)
		for _, b := range people.Facets.Actor {
			if b.Value != nil && *b.Value == "system" {
				t.Errorf("kind=people offered the system actor, want it excluded")
			}
		}
		if len(sys.Facets.Actor)+len(people.Facets.Actor) != len(base.Facets.Actor) {
			t.Errorf("system(%d) + people(%d) actor buckets != unfiltered(%d); the two kinds must "+
				"partition the actors", len(sys.Facets.Actor), len(people.Facets.Actor),
				len(base.Facets.Actor))
		}
	})
}

// --- AC #3: the facet reads the log, not the roster -------------------------------------

// TestAuditFacets_DepartedMemberStillAppearsAsAnActorFacet is Core AC 7. The actor facet
// is GROUP BY actor on audit_log, so a subject appears because their ROWS exist. A facet
// built by joining memberships would drop them the day they leave and their history would
// become unfilterable — the test deletes the membership AFTER seeding for exactly that
// reason.
func TestAuditFacets_DepartedMemberStillAppearsAsAnActorFacet(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)

	departed := uuid.NewString()
	staying := uuid.NewString()
	filtSeedMembership(t, f, p, departed, "Departed Person")
	filtSeedMembership(t, f, p, staying, "Staying Person")
	filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: departed, payload: "{}", ageSeconds: 40},
		{event: "invoice.created", actor: departed, payload: "{}", ageSeconds: 30},
		{event: "invoice.updated", actor: staying, payload: "{}", ageSeconds: 20},
	})

	// The facet names them while the membership stands, so the post-deletion assertion is
	// about the deletion and not about the fixture.
	before := pageQuery(t, f, p, audit.Filter{Limit: 10})
	fctRequireNonEmpty(t, "pre-deletion actor", before.Facets.Actor)
	if got := fctCounts(before.Facets.Actor)[departed]; got != 2 {
		t.Fatalf("before the deletion the departed subject counts %d, want 2", got)
	}

	fctDeleteMembership(t, f, p, departed)

	after := pageQuery(t, f, p, audit.Filter{Limit: 10})
	fctRequireNonEmpty(t, "post-deletion actor", after.Facets.Actor)
	counts := fctCounts(after.Facets.Actor)
	if got := counts[departed]; got != 2 {
		t.Errorf("after the deletion the departed subject counts %d, want 2 — their rows are still "+
			"in the log, so they must stay a filterable actor (facet: %v)", got, counts)
	}
	if got := counts[staying]; got != 1 {
		t.Errorf("the remaining member counts %d, want 1", got)
	}

	var found *audit.Facet
	for i := range after.Facets.Actor {
		if b := after.Facets.Actor[i]; b.Value != nil && *b.Value == departed {
			found = &after.Facets.Actor[i]
		}
	}
	if found == nil {
		t.Fatalf("the departed subject is absent from the actor facet %v", counts)
	}
	if found.Kind != "raw" {
		t.Errorf("the departed subject's facet kind = %q, want \"raw\" — nothing can name them now",
			found.Kind)
	}
	if found.Name == nil || *found.Name != departed {
		t.Errorf("the departed subject's facet name = %v, want the subject itself so the bucket "+
			"still renders", found.Name)
	}
}

// --- AC #4: the workspace bucket --------------------------------------------------------

// TestAuditFacets_CompanyFacetCarriesTheWorkspaceBucketAndSumsToTotal is AC #4. GROUP BY
// entity_id yields the NULL group for free, and that group IS the workspace-level count
// (contract §4) — the workspace UNION unattributed partition, not company_scope.
func TestAuditFacets_CompanyFacetCarriesTheWorkspaceBucketAndSumsToTotal(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	got := pageQuery(t, f, c.p, audit.Filter{Limit: 5})
	fctRequireNonEmpty(t, "company", got.Facets.Company)

	nulls := 0
	byID := map[string]audit.Facet{}
	for _, b := range got.Facets.Company {
		if b.Value == nil {
			nulls++
			if b.Name != nil {
				t.Errorf("the workspace bucket carries name %q, want nil", *b.Name)
			}
			continue
		}
		byID[*b.Value] = b
	}
	if nulls != 1 {
		t.Errorf("the company facet has %d nil-value buckets, want exactly 1 (the workspace bucket)",
			nulls)
	}
	if len(got.Facets.Company) != 3 {
		t.Errorf("the company facet has %d buckets, want 3 (two companies plus workspace)",
			len(got.Facets.Company))
	}
	for id, name := range map[string]string{c.entityA: "Acme Holdings", c.entityB: "Borealis Ltd"} {
		b, ok := byID[id]
		if !ok {
			t.Errorf("company %s is absent from the facet", id)
			continue
		}
		if b.Name == nil || *b.Name != name {
			t.Errorf("company %s carries name %v, want %q", id, b.Name, name)
		}
		if b.Count == 0 {
			t.Errorf("company %s counts 0", id)
		}
	}
	if got.Total == 0 {
		t.Fatalf("total is 0; the sum assertion below would be 0 == 0")
	}
	if sum := fctSum(got.Facets.Company); sum != got.Total {
		t.Errorf("the company buckets sum to %d but total is %d; with no company selected every "+
			"row belongs to exactly one bucket", sum, got.Total)
	}
}

// --- AC #5: empty is [], never null -----------------------------------------------------

// TestAuditFacets_EmptyFacetsMarshalToEmptyArrays is AC #5 and D-9. The filter is a FUTURE
// From: no facet omits the date, so all three are genuinely empty. A filter on event,
// actor or company would leave that facet populated, because a facet omits its own filter.
func TestAuditFacets_EmptyFacetsMarshalToEmptyArrays(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	empty := pageQuery(t, f, c.p, audit.Filter{Limit: 5, From: time.Now().Add(time.Hour)})
	if empty.Total != 0 {
		t.Fatalf("a future From matched %d rows, want 0", empty.Total)
	}
	for _, tc := range []struct {
		name    string
		buckets []audit.Facet
	}{
		{"event", empty.Facets.Event},
		{"actor", empty.Facets.Actor},
		{"company", empty.Facets.Company},
	} {
		if len(tc.buckets) != 0 {
			t.Errorf("the %s facet has %d buckets under a filter matching nothing",
				tc.name, len(tc.buckets))
		}
	}
	if got, want := fctJSON(t, empty.Facets), `{"event":[],"actor":[],"company":[]}`; got != want {
		t.Errorf("empty facets marshal to %s, want %s — a null here is a client crash, not an "+
			"empty list", got, want)
	}

	// The anti-vacuity half: "always empty" would satisfy every assertion above.
	full := pageQuery(t, f, c.p, audit.Filter{Limit: 5})
	fctRequireNonEmpty(t, "unfiltered event", full.Facets.Event)
	fctRequireNonEmpty(t, "unfiltered actor", full.Facets.Actor)
	fctRequireNonEmpty(t, "unfiltered company", full.Facets.Company)
}

// --- AC #6: the buckets tie to the total ------------------------------------------------

// TestAuditFacets_AgreeWithTheTotalUnderTheSameFilters is AC #6. No combination here sets
// Company: the company facet OMITS Company, so with one selected its buckets sum to more
// than total by construction. The case below asserts that rather than pretending it is a
// defect.
func TestAuditFacets_AgreeWithTheTotalUnderTheSameFilters(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	combos := []struct {
		name   string
		filter audit.Filter
	}{
		{"unfiltered", audit.Filter{Limit: 5}},
		{"one event", audit.Filter{Limit: 5, Events: []string{c.events[0]}}},
		{"one actor", audit.Filter{Limit: 5, Actors: []string{c.actors[0]}}},
		{"last 7 days", audit.Filter{Limit: 5, From: time.Now().Add(-7 * 24 * time.Hour)}},
		{"search", audit.Filter{Limit: 5, Q: "alpha"}},
		{"event plus kind", audit.Filter{Limit: 5, Events: []string{c.events[0]}, ActorKind: "people"}},
	}
	if len(combos) < 5 {
		t.Fatalf("the combination table has %d entries, want at least 5", len(combos))
	}

	for _, combo := range combos {
		t.Run(combo.name, func(t *testing.T) {
			got := pageQuery(t, f, c.p, combo.filter)
			if got.Total == 0 {
				t.Fatalf("total is 0; this combination would pass on 0 == 0")
			}
			if sum := fctSum(got.Facets.Company); sum != got.Total {
				t.Errorf("the company buckets sum to %d but total is %d", sum, got.Total)
			}
			// The event and actor facets omit their own dimension, so they tie to total
			// only when that dimension is unset.
			if len(combo.filter.Events) == 0 {
				if sum := fctSum(got.Facets.Event); sum != got.Total {
					t.Errorf("the event buckets sum to %d but total is %d", sum, got.Total)
				}
			}
			if len(combo.filter.Actors) == 0 {
				if sum := fctSum(got.Facets.Actor); sum != got.Total {
					t.Errorf("the actor buckets sum to %d but total is %d", sum, got.Total)
				}
			}
		})
	}
}

// TestAuditFacets_CompanyFacetSumExceedsTotalWhenACompanyIsSelected is the direct evidence
// that a facet omits its own filter — the claim AC #1 and AC #6 together reach for. With a
// company selected, total counts that company's rows while the facet still counts every
// company's, so the sum must be STRICTLY greater.
func TestAuditFacets_CompanyFacetSumExceedsTotalWhenACompanyIsSelected(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	all := pageQuery(t, f, c.p, audit.Filter{Limit: 5})
	one := pageQuery(t, f, c.p, audit.Filter{Limit: 5, Company: audit.NamedCompany(c.entityA)})

	if one.Total == 0 {
		t.Fatalf("selecting company %s matched no rows; the fixture cannot make this claim", c.entityA)
	}
	if one.Total >= all.Total {
		t.Fatalf("selecting a company left total at %d of %d; the filter excluded nothing",
			one.Total, all.Total)
	}
	sum := fctSum(one.Facets.Company)
	if sum <= one.Total {
		t.Errorf("with a company selected the company buckets sum to %d and total is %d; the sum "+
			"must exceed it, or the facet is applying its own filter and every other company has "+
			"vanished from the picker", sum, one.Total)
	}
	if sum != all.Total {
		t.Errorf("the company buckets sum to %d with a company selected but %d without; the facet "+
			"must count the same set either way", sum, all.Total)
	}
}

// --- ordering ---------------------------------------------------------------------------

// TestAuditFacets_BucketOrderIsCountThenValue pins the order. Two facet computations are
// compared byte-for-byte above, so an unordered GROUP BY would make those cases flake
// rather than fail — and a picker whose entries reshuffle between requests is unusable.
func TestAuditFacets_BucketOrderIsCountThenValue(t *testing.T) {
	f := requireFixture(t)
	c := filtBuildCorpus(t, f)

	// The corpus ties on count across all four events, so the value tiebreak decides.
	tied := pageQuery(t, f, c.p, audit.Filter{Limit: 5})
	fctRequireNonEmpty(t, "event", tied.Facets.Event)
	seen := map[int]bool{}
	for _, b := range tied.Facets.Event {
		seen[b.Count] = true
	}
	if len(seen) != 1 {
		t.Fatalf("the corpus event counts are %v, want a single tied count so the value tiebreak "+
			"is the thing under test", seen)
	}
	for i := 1; i < len(tied.Facets.Event); i++ {
		prev, cur := tied.Facets.Event[i-1], tied.Facets.Event[i]
		if prev.Value == nil || cur.Value == nil {
			t.Fatalf("the event facet has a nil value at index %d", i)
		}
		if *prev.Value > *cur.Value {
			t.Errorf("tied event buckets run %q then %q, want ascending by value",
				*prev.Value, *cur.Value)
		}
	}

	// Narrowing skews the counts apart, so the count ordering decides.
	skewed := pageQuery(t, f, c.p, audit.Filter{Limit: 5, From: time.Now().Add(-7 * 24 * time.Hour)})
	fctRequireNonEmpty(t, "narrowed event", skewed.Facets.Event)
	distinct := map[int]bool{}
	for _, b := range skewed.Facets.Event {
		distinct[b.Count] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("the narrowed event counts are %v, want at least two distinct so the count "+
			"ordering is the thing under test", distinct)
	}
	for i := 1; i < len(skewed.Facets.Event); i++ {
		if prev, cur := skewed.Facets.Event[i-1], skewed.Facets.Event[i]; prev.Count < cur.Count {
			t.Errorf("event buckets run count %d then %d, want descending", prev.Count, cur.Count)
		}
	}

	// Repeating a request must return the identical order.
	again := pageQuery(t, f, c.p, audit.Filter{Limit: 5})
	if a, b := fctJSON(t, again.Facets), fctJSON(t, tied.Facets); a != b {
		t.Errorf("two identical requests returned different facets\n first: %s\nsecond: %s", b, a)
	}
}
