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
