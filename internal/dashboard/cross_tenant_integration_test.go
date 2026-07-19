package dashboard

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestRLS_DashboardRollupCrossTenantIsolated (DASH-14, Test Specs table,
// M4-07-01 story / task-155): tenants A and B each have their own entity
// and invoices; B has 5 broken drafts. Store.Rollup(ctxA) must not surface
// any trace of B's data (no B row in Clients, none of B's invoices folded
// into Totals) and, symmetrically, Store.Rollup(ctxB) must not surface any
// trace of A's data -- proven in BOTH directions in one test, mirroring
// internal/invoice/cross_tenant_integration_test.go's
// TestRLS_InvoicesStoreChildWritesTenantScoped shape (positive visibility
// under one's own tenant AND negative invisibility of the other's).
func TestRLS_DashboardRollupCrossTenantIsolated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "DASH-14 tenant A")
	tenantB := seedTenant(t, super, "DASH-14 tenant B")
	entityA := seedEntity(t, super, tenantA, "DASH-14 A Corp")
	entityB := seedEntity(t, super, tenantB, "DASH-14 B Corp")

	seedInvoiceAtStatus(t, super, tenantA, entityA, "DASH-14-A1", "accepted")

	broken := `[{"rule_key":"x","severity":"error","message":"x"}]`
	for i := 0; i < 5; i++ {
		seedInvoiceWithViolations(t, super, tenantB, entityB, fmt.Sprintf("DASH-14-B%d", i), "draft", broken)
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})

	// Direction 1: tenant A must see only its own row, none of B's.
	gotA, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup(as tenant A): %v", err)
	}
	for _, c := range gotA.Clients {
		if c.EntityID == entityB {
			t.Errorf("tenant A's Clients contains B's entity %s", entityB)
		}
	}
	if len(gotA.Clients) != 1 || gotA.Clients[0].EntityID != entityA {
		t.Fatalf("tenant A's Clients = %+v, want exactly A's own row (entity %s)", gotA.Clients, entityA)
	}
	if gotA.Totals.NeedsAttention != 0 {
		t.Errorf("tenant A's Totals.NeedsAttention = %d, want 0 (must not see B's 5 broken drafts)", gotA.Totals.NeedsAttention)
	}
	if gotA.Totals.Counts.Draft != 0 {
		t.Errorf("tenant A's Totals.Counts.Draft = %d, want 0 (must not see B's 5 draft invoices)", gotA.Totals.Counts.Draft)
	}

	// Direction 2: tenant B must see only its own row, none of A's.
	gotB, err := store.Rollup(cB)
	if err != nil {
		t.Fatalf("Rollup(as tenant B): %v", err)
	}
	for _, c := range gotB.Clients {
		if c.EntityID == entityA {
			t.Errorf("tenant B's Clients contains A's entity %s", entityA)
		}
	}
	if len(gotB.Clients) != 1 || gotB.Clients[0].EntityID != entityB {
		t.Fatalf("tenant B's Clients = %+v, want exactly B's own row (entity %s)", gotB.Clients, entityB)
	}
	if gotB.Totals.NeedsAttention != 5 {
		t.Errorf("tenant B's Totals.NeedsAttention = %d, want 5 (its own 5 broken drafts)", gotB.Totals.NeedsAttention)
	}
	if gotB.Totals.Counts.Accepted != 0 {
		t.Errorf("tenant B's Totals.Counts.Accepted = %d, want 0 (must not see A's accepted invoice)", gotB.Totals.Counts.Accepted)
	}
}

// TestRLS_DashboardRollupCrossTenantSortPoisonRefused: tenant B's entity is
// deliberately constructed to sort to position 0 by BOTH of Store.Rollup's
// sort keys if isolation ever leaked -- a name that sorts alphabetically
// before every one of A's entity names AND the highest needs_attention in
// the whole dataset. If RLS ever failed open, this row would be
// unmistakable: it would land as A's Clients[0]. It must not appear
// anywhere in A's Clients at all. DASH-14 proves plain non-visibility; this
// proves non-visibility survives the specific case an ordering bug would
// most likely surface it.
func TestRLS_DashboardRollupCrossTenantSortPoisonRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "DASH-adversarial poison tenant A")
	tenantB := seedTenant(t, super, "DASH-adversarial poison tenant B")

	// A's own entities -- named and counted so a leak would visibly displace
	// them from their expected positions.
	aEntity1 := seedEntity(t, super, tenantA, "Mno Corp")
	aEntity2 := seedEntity(t, super, tenantA, "Zyx Corp")
	seedInvoice(t, super, tenantA, aEntity1, "POISON-A1")
	seedInvoice(t, super, tenantA, aEntity2, "POISON-A2")

	// B's poison entity: alphabetically first AND the most exceptions of any
	// entity in the dataset.
	poisonEntity := seedEntity(t, super, tenantB, "AAAAA Poison Corp")
	broken := `[{"rule_key":"x","severity":"error","message":"y"}]`
	for i := 0; i < 10; i++ {
		seedInvoiceWithViolations(t, super, tenantB, poisonEntity, fmt.Sprintf("POISON-B%d", i), "draft", broken)
	}

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	got, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup(as tenant A): %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("tenant A's Clients = %d rows, want 2 (its own two entities only)", len(got.Clients))
	}
	for _, c := range got.Clients {
		if c.EntityID == poisonEntity {
			t.Fatalf("tenant A's Clients contains tenant B's poison entity %s (%s) -- cross-tenant isolation leaked at the sort boundary", poisonEntity, c.EntityName)
		}
	}
	if got.Clients[0].EntityID == poisonEntity || got.Clients[0].EntityName == "AAAAA Poison Corp" {
		t.Fatalf("tenant A's Clients[0] = %+v, is tenant B's poison row", got.Clients[0])
	}
	if got.Totals.NeedsAttention != 0 {
		t.Errorf("tenant A's Totals.NeedsAttention = %d, want 0 (must not fold in B's 10 broken drafts)", got.Totals.NeedsAttention)
	}
}

// TestRLS_DashboardRollupUnknownTenantSeesNothing: an identity carrying a
// syntactically-valid tenant id that was never seeded (no `tenants` row, no
// business_entities, no invoices) must see a fully-empty rollup -- not an
// error, and NOT another tenant's data -- even while a DIFFERENT, real
// tenant in the same database has data. Proves RLS's tenant_isolation
// policy is strict equality against app.current_tenant, not a fallback that
// could ever expose "any known tenant" when the caller's own tenant id has
// no matching rows. db.WithinTenantTx (internal/platform/db/db.go) only
// requires tenantID to parse as a UUID -- it does not require a matching row
// in `tenants` -- so this is a real, reachable code path (e.g. a stale or
// forged JWT tenant claim), not a hypothetical.
func TestRLS_DashboardRollupUnknownTenantSeesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	// A real tenant WITH data, to prove the empty result isn't just "the DB
	// happens to be empty."
	tenantWithData := seedTenant(t, super, "DASH-adversarial has-data tenant")
	entity := seedEntity(t, super, tenantWithData, "Has Data Corp")
	broken := `[{"rule_key":"x","severity":"error","message":"y"}]`
	seedInvoiceWithViolations(t, super, tenantWithData, entity, "UNKNOWN-1", "draft", broken)

	unknownTenantID := uuid.NewString() // never inserted into `tenants`, never referenced by any row

	store := NewStore(app)
	cUnknown := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: unknownTenantID})

	got, err := store.Rollup(cUnknown)
	if err != nil {
		t.Fatalf("Rollup(unknown tenant id): %v", err)
	}
	if len(got.Clients) != 0 {
		t.Fatalf("Clients = %d rows, want 0 (an unregistered tenant id must never surface another tenant's rows)", len(got.Clients))
	}
	if got.Totals.Counts != (Counts{}) || got.Totals.NeedsAttention != 0 {
		t.Errorf("Totals = %+v, want all-zero", got.Totals)
	}
}

// TestRLS_DashboardTopViolationsCrossTenantIsolated (DASH-27, Test Specs
// table, M4-07-02 story / task-156): tenants A and B; only B has invoices
// carrying severity:"error" violations on rule "X". Store.Rollup(ctxA)'s
// TopViolations must be empty and contain no trace of rule X; symmetrically,
// Store.Rollup(ctxB) DOES report rule X -- both directions asserted in one
// test, same shape as TestRLS_DashboardRollupCrossTenantIsolated (DASH-14)
// above. TopViolations relies on the SAME bare `FROM invoices i` RLS scoping
// as the per-entity query (no `WHERE tenant_id` anywhere in this package,
// AC-6/story AC-7) -- this proves that holds for the per-rule query too, not
// just the per-entity one DASH-14 already covers.
func TestRLS_DashboardTopViolationsCrossTenantIsolated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "DASH-27 tenant A")
	tenantB := seedTenant(t, super, "DASH-27 tenant B")
	entityA := seedEntity(t, super, tenantA, "DASH-27 A Corp")
	entityB := seedEntity(t, super, tenantB, "DASH-27 B Corp")

	// A has an invoice, but none carrying an error-severity violation on
	// rule X (or any rule at all) -- proves A's empty TopViolations isn't
	// just "A has no invoices."
	seedInvoiceAtStatus(t, super, tenantA, entityA, "DASH-27-A1", "accepted")

	ruleX := `[{"rule_key":"X","severity":"error","message":"z"}]`
	seedInvoiceWithViolations(t, super, tenantB, entityB, "DASH-27-B1", "draft", ruleX)
	seedInvoiceWithViolations(t, super, tenantB, entityB, "DASH-27-B2", "draft", ruleX)

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})

	// Direction 1: tenant A must see no trace of rule X.
	gotA, err := store.Rollup(cA)
	if err != nil {
		t.Fatalf("Rollup(as tenant A): %v", err)
	}
	if len(gotA.TopViolations) != 0 {
		t.Errorf("tenant A's TopViolations = %+v, want empty (must not see B's rule X violations)", gotA.TopViolations)
	}
	for _, rc := range gotA.TopViolations {
		if rc.RuleKey == "X" {
			t.Errorf("tenant A's TopViolations contains rule X: %+v", gotA.TopViolations)
		}
	}

	// Direction 2: tenant B must see its own rule X, correctly counted.
	gotB, err := store.Rollup(cB)
	if err != nil {
		t.Fatalf("Rollup(as tenant B): %v", err)
	}
	if len(gotB.TopViolations) != 1 || gotB.TopViolations[0].RuleKey != "X" || gotB.TopViolations[0].Invoices != 2 {
		t.Fatalf("tenant B's TopViolations = %+v, want exactly [{X,2}]", gotB.TopViolations)
	}
}
