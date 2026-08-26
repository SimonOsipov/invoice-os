// AUDIT-04-08: Core AC 2's isolation claim at the store's own transaction — the page, the
// facets and a replayed foreign cursor.
//
// Helpers use an rls* prefix. These are GREEN BY CONSTRUCTION: RLS already works, so they are
// regression guards, not red-then-green tests. What earns them their place is that dropping
// the tenant GUC turns them red — every case therefore carries a control proving the other
// tenant's rows really exist, so an absence can never be read as isolation when it is really
// an empty fixture.
package audit_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// rlsInsert writes one row with an explicit actor. pageInsert hardcodes "page-actor" and
// pageRow carries no actor field; widening it would touch four other files, and AC #2 needs a
// tenant-B-only actor to prove the actor facet is scoped.
func rlsInsert(t *testing.T, f *fixture, p pageFixture, actor, event, payload string, ageSeconds int) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
			VALUES ($1, $2, $3, $4::jsonb, now() - ($5 * interval '1 second'))
			RETURNING id`, p.tenant, actor, event, payload, ageSeconds).Scan(&id)
	}); err != nil {
		t.Fatalf("insert audit row: %v", err)
	}
	return id
}

// rlsFacetValues is a facet list's non-nil values.
func rlsFacetValues(buckets []audit.Facet) []string {
	out := make([]string, 0, len(buckets))
	for _, b := range buckets {
		if b.Value != nil {
			out = append(out, *b.Value)
		}
	}
	return out
}

func rlsContains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// TestRLS_AuditReadIsTenantScoped is AC #1. The reader issues no tenant predicate of its own —
// app.current_tenant plus FORCE RLS is the whole of the isolation — so if the policy stopped
// applying this read would return tenant B's rows with nothing else changing.
func TestRLS_AuditReadIsTenantScoped(t *testing.T) {
	f := requireFixture(t)
	a := pageSeedTenant(t, f)
	b := pageSeedTenant(t, f)

	pageInsert(t, f, a, pageSeries("invoice.created", 3, 30))
	for i := 0; i < 5; i++ {
		rlsInsert(t, f, b, "rls-actor-b", "rls.b.only.event", `{}`, 40+i)
	}

	// The control FIRST: tenant B really holds those five rows. Without it, A seeing none of
	// them proves nothing — a failed insert looks exactly like perfect isolation.
	own := pageQuery(t, f, b, audit.Filter{Limit: 50})
	if own.Total != 5 {
		t.Fatalf("tenant B reads back %d of its own rows, want 5; the isolation claim below "+
			"cannot be made against an empty fixture", own.Total)
	}

	got := pageQuery(t, f, a, audit.Filter{Limit: 50})
	if got.Total != 3 {
		t.Errorf("tenant A's total = %d, want 3 — its own rows and no others", got.Total)
	}
	if len(got.Events) == 0 {
		t.Fatalf("tenant A's read returned nothing; a test that sees no rows cannot tell "+
			"isolation from breakage (total = %d)", got.Total)
	}
	for _, e := range got.Events {
		if e.Event == "rls.b.only.event" {
			t.Errorf("tenant A's page carries %q, an event only tenant B ever wrote", e.Event)
		}
		if e.Actor == "rls-actor-b" {
			t.Errorf("tenant A's page carries actor %q, which only tenant B ever wrote", e.Actor)
		}
	}
}

// TestRLS_AuditFacetsAreTenantScoped is AC #2. The facets are three separate statements
// sharing filterPredicates with the page, so a predicate proven on the page is NOT proven
// here — the facet-scoping bug found in AUDIT-04-06 was exactly that.
func TestRLS_AuditFacetsAreTenantScoped(t *testing.T) {
	f := requireFixture(t)
	a := pageSeedTenant(t, f)
	b := pageSeedTenant(t, f)

	aEntity := pageSeedEntity(t, f, a, "RLS A Ltd")
	bEntity := pageSeedEntity(t, f, b, "RLS B Ltd")

	// portfolio.entity.created carries the entity in payload.id, so the BEFORE INSERT
	// resolver attributes each row to its own company — which is what gives the company
	// facet something tenant-specific to leak.
	rlsInsert(t, f, a, "rls-actor-a", "portfolio.entity.created", `{"id":"`+aEntity+`"}`, 30)
	rlsInsert(t, f, b, "rls-actor-b", "portfolio.entity.created", `{"id":"`+bEntity+`"}`, 31)
	rlsInsert(t, f, b, "rls-actor-b", "rls.b.only.event", `{}`, 32)

	// The control: B's own facets carry all three of its values, so their absence from A's
	// facets below is isolation and not an empty fixture.
	own := pageQuery(t, f, b, audit.Filter{Limit: 50})
	for _, tc := range []struct {
		dimension string
		values    []string
		want      string
	}{
		{"event", rlsFacetValues(own.Facets.Event), "rls.b.only.event"},
		{"actor", rlsFacetValues(own.Facets.Actor), "rls-actor-b"},
		{"company", rlsFacetValues(own.Facets.Company), bEntity},
	} {
		if !rlsContains(tc.values, tc.want) {
			t.Fatalf("tenant B's own %s facet does not carry %q (got %v); this test cannot make "+
				"its claim", tc.dimension, tc.want, tc.values)
		}
	}

	got := pageQuery(t, f, a, audit.Filter{Limit: 50})
	for _, tc := range []struct {
		dimension string
		values    []string
		leaked    string
		mine      string
	}{
		{"event", rlsFacetValues(got.Facets.Event), "rls.b.only.event", "portfolio.entity.created"},
		{"actor", rlsFacetValues(got.Facets.Actor), "rls-actor-b", "rls-actor-a"},
		{"company", rlsFacetValues(got.Facets.Company), bEntity, aEntity},
	} {
		if rlsContains(tc.values, tc.leaked) {
			t.Errorf("tenant A's %s facet carries %q, which only tenant B ever wrote: %v",
				tc.dimension, tc.leaked, tc.values)
		}
		if !rlsContains(tc.values, tc.mine) {
			t.Errorf("tenant A's %s facet is missing its own value %q (got %v); an empty facet "+
				"would satisfy the leak check above vacuously", tc.dimension, tc.mine, tc.values)
		}
	}
}

// TestRLS_AuditForeignCursorYieldsAnEmptyPageNotALeak is AC #3, and it proves TWO different
// things that are easy to conflate.
//
// ISOLATION is structural: DecodeCursor is pure syntax and accepts any tenant's cursor by
// design, and RLS then scopes the query to the CALLER — so tenant B's rows can never appear.
//
// EMPTINESS is a property of this fixture, not of the system. Tenant B's rows are seeded
// OLDER than every one of tenant A's, so a cursor at B's position leaves nothing of A's
// behind it. With the ages reversed, A would correctly see its OWN older rows and the page
// would not be empty — that is right behaviour, not a leak.
func TestRLS_AuditForeignCursorYieldsAnEmptyPageNotALeak(t *testing.T) {
	f := requireFixture(t)
	a := pageSeedTenant(t, f)
	b := pageSeedTenant(t, f)

	pageInsert(t, f, a, pageSeries("invoice.created", 3, 30))          // newest
	bIDs := pageInsert(t, f, b, pageSeries("invoice.updated", 4, 900)) // oldest

	// Limit 2 of B's 4 rows, so has_more is true and next_cursor is a real mid-list position.
	bPage := pageQuery(t, f, b, audit.Filter{Limit: 2})
	if !bPage.Page.HasMore {
		t.Fatalf("tenant B's 2-row page reports has_more = false over %d rows; there is no "+
			"cursor to replay", bPage.Total)
	}
	cursor := pageCursorOf(t, bPage)

	// The control: the cursor is a WORKING position, not a dud. Replayed by its own tenant it
	// returns B's remaining older rows. Without this, A's empty page below could just as well
	// mean the cursor was garbage.
	bRest := pageQuery(t, f, b, audit.Filter{Limit: 50, Cursor: cursor})
	if len(bRest.Events) == 0 {
		t.Fatalf("tenant B replaying its own cursor got nothing back; the cursor is not a " +
			"usable position and this test cannot make its claim")
	}

	got := pageQuery(t, f, a, audit.Filter{Limit: 50, Cursor: cursor})
	if len(got.Events) != 0 {
		t.Errorf("tenant A replaying tenant B's cursor got %d rows, want 0 — B's rows are all "+
			"older than A's, so nothing of A's lies behind that position", len(got.Events))
	}

	// The claim that actually matters, asserted separately because it holds regardless of the
	// ages above.
	foreign := make(map[int64]struct{}, len(bIDs))
	for _, id := range bIDs {
		foreign[id] = struct{}{}
	}
	for _, id := range pageIDs(t, got) {
		if _, isB := foreign[id]; isB {
			t.Errorf("tenant A's page carries row %d, which belongs to tenant B", id)
		}
	}

	// And A is not simply empty: unfiltered it reads its own rows back.
	unbounded := pageQuery(t, f, a, audit.Filter{Limit: 50})
	if unbounded.Total == 0 {
		t.Fatalf("tenant A has no rows at all; the empty page above proves nothing")
	}
}

// TestRLS_AuditSearchNumberFoldInIsTenantScoped is AUDIT-11-09's new read path under the
// isolation claim. The number fence resolves free text against `invoices` — a table the
// audit reader never touched before AUDIT-11 — and writes no tenant predicate of its own:
// app.current_tenant plus FORCE RLS is the whole of the scope, exactly as for the
// memberships and business_entities fold-ins.
//
// Two claims, because a leak here has two shapes. Rows: tenant B's number must never reach
// tenant A's page. Existence: a caller must not be able to probe another tenant's numbering
// by comparing a real foreign number against one nobody holds — so the two responses are
// asserted indistinguishable, not merely both empty.
//
// Measured, so the strength of this case is not overstated: disabling RLS on `invoices`
// entirely leaves it GREEN. The mutation is effective at the fold-in (tenant A then resolves
// tenant B's invoice id), but audit_log's own policy fences the page independently, so a
// foreign id matches no visible row. audit_log RLS is the load-bearing fence here and
// invoices RLS is defence in depth. What this case does catch is the arm being widened,
// re-pointed at a column audit_log does not scope, or dropped (its controls go red).
func TestRLS_AuditSearchNumberFoldInIsTenantScoped(t *testing.T) {
	f := requireFixture(t)
	a := pageSeedTenant(t, f)
	b := pageSeedTenant(t, f)

	const foreign, mine, nobody = "INV-SECRET-77", "INV-OWN-1", "INV-NOBODY-99"
	aEntity := pageSeedEntity(t, f, a, "RLS Number A Ltd")
	bEntity := pageSeedEntity(t, f, b, "RLS Number B Ltd")
	aInvoice := filtSeedNumberedInvoice(t, f, a, aEntity, mine)
	bInvoice := filtSeedNumberedInvoice(t, f, b, bEntity, foreign)

	filtInsert(t, f, a, []filtRow{
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, aInvoice), ageSeconds: 10},
	})
	filtInsert(t, f, b, []filtRow{
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, bInvoice), ageSeconds: 20},
		{event: "invoice.updated", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q,"invoice_number":%q}`, bInvoice, foreign), ageSeconds: 10},
	})

	// Controls FIRST. B's rows really exist and really are reachable by that number, and A's
	// own search works — otherwise A seeing nothing proves nothing.
	if own := pageQuery(t, f, b, audit.Filter{Limit: 20, Q: foreign}); own.Total != 2 {
		t.Fatalf("tenant B reads back %d of its own rows for %q, want 2; the isolation claim below "+
			"cannot be made against an empty fixture", own.Total, foreign)
	}
	if got := pageQuery(t, f, a, audit.Filter{Limit: 20, Q: mine}); got.Total != 1 {
		t.Fatalf("tenant A's search for its own number %q returned total %d, want 1; search is not "+
			"working for A at all", mine, got.Total)
	}

	leak := pageQuery(t, f, a, audit.Filter{Limit: 20, Q: foreign})
	if leak.Total != 0 || len(leak.Events) != 0 {
		t.Errorf("tenant A's search for tenant B's invoice number %q matched %d rows (total %d), "+
			"want none — the fold-in reads invoices under RLS alone", foreign, len(leak.Events), leak.Total)
	}
	for _, e := range leak.Events {
		if e.EntityID != nil && *e.EntityID == bEntity {
			t.Errorf("tenant A's page carries a row scoped to tenant B's company %s", bEntity)
		}
	}

	// The existence half: a real foreign number and a number nobody holds must be
	// indistinguishable, or the fold-in becomes a probe of another tenant's numbering.
	blank := pageQuery(t, f, a, audit.Filter{Limit: 20, Q: nobody})
	if leak.Total != blank.Total || len(leak.Events) != len(blank.Events) ||
		leak.LogIsEmpty != blank.LogIsEmpty ||
		len(leak.Facets.Event) != len(blank.Facets.Event) ||
		len(leak.Facets.Actor) != len(blank.Facets.Actor) ||
		len(leak.Facets.Company) != len(blank.Facets.Company) {
		t.Errorf("tenant A can tell a real foreign number from an unused one: %q gives "+
			"total=%d events=%d empty=%v facets=%d/%d/%d, %q gives total=%d events=%d empty=%v "+
			"facets=%d/%d/%d — that difference is a probe of tenant B's numbering",
			foreign, leak.Total, len(leak.Events), leak.LogIsEmpty,
			len(leak.Facets.Event), len(leak.Facets.Actor), len(leak.Facets.Company),
			nobody, blank.Total, len(blank.Events), blank.LogIsEmpty,
			len(blank.Facets.Event), len(blank.Facets.Actor), len(blank.Facets.Company))
	}
}
