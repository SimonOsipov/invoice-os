// persona-handoff-fix regression fix ([entity-id-restored]): CI caught a
// filter-AFTER-paginate bug (e2e/topology/import-wizard.spec.ts:208) -- the SPA
// scoped Invoices/Reports/Customers to the active client by fetching a
// tenant-wide, LIMIT-50 page (listInvoices, no entity param) and filtering it
// IN THE BROWSER (filterByActiveEntity, frontend/app/src/lib/invoices.ts). On
// the shared, never-reset dev DB, an entity's own invoices routinely fall
// outside the newest-50 tenant-wide window, so its Invoices list rendered
// EMPTY even though the entity has invoices. The fix threads f.EntityID
// (invoice.go's ListFilter, [D8]) into Store.List's WHERE clause, narrowing
// the row set BEFORE Limit/Offset are ever applied -- in SQL, not the browser.
//
// The test below is deliberately built so a filter-AFTER-paginate
// reimplementation of this fix would fail it: a naive fix that re-fetches the
// tenant-wide page and filters client-side (or even server-side, but after the
// LIMIT) would return ZERO rows for entityA here, exactly reproducing the
// CI-caught bug -- a fixture where the filtered entity's own row count
// trivially fits under Limit would NOT catch that class of regression.
package invoice

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"

	"github.com/google/uuid"
)

// seedInvoiceAt is seedInvoice plus an explicit created_at overwrite -- same
// superuser force-write idiom as needs_attention_adversarial_test.go's own
// seedInvoiceWithViolationsAtHelper, needed here for the same reason: several
// inserts issued back-to-back in one test are not reliably distinct at
// Postgres timestamptz's real-clock resolution, and this test's whole point
// (entityB's rows must sort strictly newer than entityA's) cannot depend on
// scheduling accidents.
func seedInvoiceAt(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string, createdAt time.Time) string {
	t.Helper()
	id := seedInvoice(t, super, tenantID, entityID, number)
	if _, err := super.Exec(context.Background(),
		`UPDATE invoices SET created_at = $1 WHERE id = $2`, createdAt, id,
	); err != nil {
		t.Fatalf("force-seed invoice created_at: %v", err)
	}
	return id
}

// TestStoreList_EntityIDNarrowsBeforeLimit is the regression proof (see file
// header). One tenant, two entities: entityA gets 2 invoices at the OLDEST
// timestamps; entityB gets 5 invoices at strictly newer ones, so entityB's
// rows alone fill any tenant-wide window up to Limit:5. A first sanity check
// confirms the fixture actually exercises the trap (a Limit:3 unfiltered
// window is entirely entityB); the real assertion is that
// List(EntityID: entityA, Limit: 3) still returns BOTH of entityA's invoices
// and the FILTERED total (2), not 0 items / total 7.
func TestStoreList_EntityIDNarrowsBeforeLimit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ENTITY-FILTER tenant")
	entityA := seedEntity(t, super, tenantID, "ENTITY-FILTER entity A")
	entityB := seedEntity(t, super, tenantID, "ENTITY-FILTER entity B")

	base := time.Now().UTC()
	at := func(offsetHours int) time.Time { return base.Add(time.Duration(offsetHours) * time.Hour) }

	for i := 0; i < 2; i++ {
		seedInvoiceAt(t, super, tenantID, entityA, fmt.Sprintf("ENTITY-FILTER-A-%d", i), at(i))
	}
	for i := 0; i < 5; i++ {
		// Offset by +10 so every one of entityB's rows sorts strictly newer
		// than either of entityA's two.
		seedInvoiceAt(t, super, tenantID, entityB, fmt.Sprintf("ENTITY-FILTER-B-%d", i), at(10+i))
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	// Sanity: an unfiltered Limit:3 tenant-wide window is entirely entityB's
	// rows. If this fails, the fixture no longer exercises the trap this test
	// exists to catch.
	unfiltered, unfilteredTotal, err := store.List(c, ListFilter{Limit: 3, Offset: 0})
	if err != nil {
		t.Fatalf("List (unfiltered sanity): %v", err)
	}
	if unfilteredTotal != 7 {
		t.Fatalf("List (unfiltered) total = %d, want 7 (2 entityA + 5 entityB)", unfilteredTotal)
	}
	for _, inv := range unfiltered {
		if inv.EntityID != entityB {
			t.Fatalf("fixture assumption broken: expected the newest-3 tenant-wide window to be entirely entityB, got entity %s -- this test no longer exercises the filter-after-paginate trap", inv.EntityID)
		}
	}

	// The real assertion: entityA's own invoices are NOT in that newest-3
	// tenant-wide window, yet List(EntityID: entityA, Limit: 3) must still
	// return both of them -- proving the WHERE narrows before LIMIT runs, not
	// after.
	items, total, err := store.List(c, ListFilter{EntityID: entityA, Limit: 3, Offset: 0})
	if err != nil {
		t.Fatalf("List (EntityID: entityA): %v", err)
	}
	if total != 2 {
		t.Fatalf("List (EntityID: entityA).total = %d, want 2 (entityA's own filtered count, not 7 tenant-wide)", total)
	}
	if len(items) != 2 {
		t.Fatalf("List (EntityID: entityA, limit=3) returned %d items, want 2 -- a filter-AFTER-paginate bug would return 0 here (entityA's rows aren't in the newest-3 tenant-wide window)", len(items))
	}
	for _, inv := range items {
		if inv.EntityID != entityA {
			t.Errorf("List (EntityID: entityA) leaked another entity's invoice: %+v", inv)
		}
	}

	// A real, but entirely unseeded, entity must resolve to a filtered EMPTY
	// result -- never a silent fall-through to tenant-wide.
	entityC := seedEntity(t, super, tenantID, "ENTITY-FILTER entity C (empty)")
	emptyItems, emptyTotal, err := store.List(c, ListFilter{EntityID: entityC, Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List (EntityID: entityC, empty): %v", err)
	}
	if emptyTotal != 0 {
		t.Errorf("List (EntityID: entityC) total = %d, want 0", emptyTotal)
	}
	if len(emptyItems) != 0 {
		t.Errorf("List (EntityID: entityC) len = %d, want 0", len(emptyItems))
	}
}

// TestStoreList_EntityIDAndNeedsAttentionAND: the two ListFilter predicates
// AND together, not OR/override each other. entityA gets one needs-attention
// (rejected) invoice and one clean (validated) invoice; entityB gets one
// needs-attention (rejected) invoice of its own. Filtering by entityA AND
// NeedsAttention:true must return exactly entityA's rejected invoice -- never
// entityB's (which would mean EntityID was dropped once NeedsAttention was
// added), and never entityA's clean one (which would mean NeedsAttention was
// dropped once EntityID was added).
func TestStoreList_EntityIDAndNeedsAttentionAND(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "ENTITY-FILTER AND tenant")
	entityA := seedEntity(t, super, tenantID, "ENTITY-FILTER AND entity A")
	entityB := seedEntity(t, super, tenantID, "ENTITY-FILTER AND entity B")

	aRejected := seedInvoiceWithViolations(t, super, tenantID, entityA, "ENTITY-FILTER-AND-A-rejected", string(StatusRejected), `[]`)
	_ = seedInvoiceWithViolations(t, super, tenantID, entityA, "ENTITY-FILTER-AND-A-clean", string(StatusValidated), `[]`)
	_ = seedInvoiceWithViolations(t, super, tenantID, entityB, "ENTITY-FILTER-AND-B-rejected", string(StatusRejected), `[]`)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, total, err := store.List(c, ListFilter{EntityID: entityA, NeedsAttention: true, Limit: 50})
	if err != nil {
		t.Fatalf("List (EntityID: entityA, NeedsAttention: true): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (EntityID: entityA, NeedsAttention: true).total = %d, want 1", total)
	}
	if len(items) != 1 || items[0].ID != aRejected {
		t.Fatalf("List (EntityID: entityA, NeedsAttention: true) items = %+v, want exactly [%s]", items, aRejected)
	}
}
