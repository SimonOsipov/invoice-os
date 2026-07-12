package portfolio

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// TestPortfolioCrossTenantIntegrationFlow (M3-10-01, Core AC 1; story M3-10
// "Cross-Tenant Integration & Golden Test Suite"): one coherent, interleaved
// two-tenant scenario driven end-to-end through portfolio.Store, run under
// real RLS. This complements -- does NOT re-implement -- the existing
// per-method *_CrossTenantNotFound tests above: those each isolate a single
// method; this test chains create -> read -> list -> update -> offboard ->
// onboard as tenant A while repeatedly checking tenant B's mirror entity
// stays invisible, unmutated, and un-audited throughout.
//
// Tenant B's entity is seeded once via seedEntity (the existing cross-tenant
// precedent, e.g. TestStoreGetByID_CrossTenantNotFound at line ~977).
// Tenant A gets NO pre-seeded row -- its entity is produced live by the
// flow's own Store.Create call, and every subsequent step (GetByID/List/
// Update/SetStatus) operates on that same id, per story Decision
// [A-ARCH-6].
func TestPortfolioCrossTenantIntegrationFlow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	// --- seed: two firm tenants, B's mirror entity only ------------------
	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, 'portfolio cross-tenant flow A', 'firm'), ($2, 'portfolio cross-tenant flow B', 'firm')`,
		tenantA, tenantB); err != nil {
		t.Fatalf("seed tenants: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1, $2)`, tenantA, tenantB)
	})

	// B's TIN is set (not nil) so the final "byte-for-byte unchanged" re-read
	// is a meaningful assertion, not just a name check.
	const bTIN = "123456780006"
	bEntityID := seedEntity(t, super, tenantB, "B Corp", strPtr(bTIN))

	store := NewStore(app)
	subjA := uuid.NewString()
	ctxA := auth.WithIdentity(ctx, auth.Identity{Subject: subjA, Role: "authenticated", TenantID: tenantA})
	// ctxB is created up front (not just at the final re-read) so the
	// reverse-direction isolation check in step 2b can use it too --
	// isolation must be symmetric, not just proven A-sees-none-of-B.
	ctxB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})

	// --- 1. Create: A's own entity, produced live (not pre-seeded) -------
	const aTIN = "1234567897"
	aEntity, err := store.Create(ctxA, CreateInput{Name: "A Corp", TIN: aTIN})
	if err != nil {
		t.Fatalf("Create (as A): %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, aEntity.ID)
	})
	if aEntity.Status != "active" {
		t.Fatalf("Create: status = %q, want %q", aEntity.Status, "active")
	}
	if aEntity.TIN == nil || *aEntity.TIN != aTIN {
		t.Fatalf("Create: tin = %v, want %q", aEntity.TIN, aTIN)
	}

	// --- 2. GetByID: A's own entity resolves; B's id is invisible to A ---
	got, err := store.GetByID(ctxA, aEntity.ID)
	if err != nil {
		t.Fatalf("GetByID(A's own entity): %v", err)
	}
	if got.ID != aEntity.ID || got.Name != aEntity.Name || got.Status != aEntity.Status {
		t.Errorf("GetByID(A's own entity) = %+v, want id=%s name=%s status=%s", got, aEntity.ID, aEntity.Name, aEntity.Status)
	}

	if _, err := store.GetByID(ctxA, bEntityID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID(B's id) as tenant A err = %v, want ErrNotFound", err)
	}

	// --- 2b. Reverse direction: as tenant B, A's (live, non-pre-seeded)
	// entity is equally invisible -- RLS isolation is symmetric, not just
	// A-can't-see-B. QA adversarial addition (M3-10-01): no existing test
	// exercises this direction against a live-created (not seeded) row.
	if _, err := store.GetByID(ctxB, aEntity.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID(A's id) as tenant B err = %v, want ErrNotFound", err)
	}

	// --- 3. List: exactly A's one entity, never B's ----------------------
	items, total, err := store.List(ctxA, ListFilter{Limit: 50})
	if err != nil {
		t.Fatalf("List (as A): %v", err)
	}
	if total != 1 {
		t.Fatalf("List (as A): total = %d, want 1", total)
	}
	if len(items) != 1 || items[0].ID != aEntity.ID {
		t.Fatalf("List (as A) = %+v, want exactly A's entity %s", items, aEntity.ID)
	}
	for _, e := range items {
		if e.ID == bEntityID {
			t.Errorf("List (as A) leaked tenant B's entity: %+v", e)
		}
	}

	// --- 4. Update: A's own entity succeeds; B's id is invisible to A ----
	updated, err := store.Update(ctxA, aEntity.ID, UpdateInput{Name: strPtr("A Corp Renamed")})
	if err != nil {
		t.Fatalf("Update(A's own entity): %v", err)
	}
	if updated.ID != aEntity.ID || updated.Name != "A Corp Renamed" {
		t.Errorf("Update(A's own entity) = %+v, want id=%s name=%q", updated, aEntity.ID, "A Corp Renamed")
	}

	if _, err := store.Update(ctxA, bEntityID, UpdateInput{Name: strPtr("hax")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(B's id) as tenant A err = %v, want ErrNotFound", err)
	}

	// --- 5. SetStatus: offboard then onboard A's own entity; B's id stays
	// invisible to A (RLS -- must be ErrNotFound, NOT ErrRedundantTransition,
	// since A can never see B's current status to begin with).
	offboarded, err := store.SetStatus(ctxA, aEntity.ID, "archived")
	if err != nil {
		t.Fatalf("SetStatus(A's own entity, offboard): %v", err)
	}
	if offboarded.Status != "archived" {
		t.Errorf("SetStatus(offboard) status = %q, want %q", offboarded.Status, "archived")
	}

	// --- 5b. List(status=active) while A's own entity is archived: the
	// status filter and the tenant filter must compose. QA adversarial
	// addition (M3-10-01): tenant B's mirror entity is untouched and still
	// "active", so a non-empty result here can ONLY come from B leaking
	// through -- A has nothing of its own left to match this filter.
	activeStatus := "active"
	activeItems, activeTotal, err := store.List(ctxA, ListFilter{Status: &activeStatus, Limit: 50})
	if err != nil {
		t.Fatalf("List(as A, status=active) while A's entity is archived: %v", err)
	}
	if activeTotal != 0 || len(activeItems) != 0 {
		t.Fatalf("List(as A, status=active) while A's own entity is archived = total=%d items=%+v, want 0/[] (non-empty here can only mean tenant B's active entity leaked through)", activeTotal, activeItems)
	}

	onboarded, err := store.SetStatus(ctxA, aEntity.ID, "active")
	if err != nil {
		t.Fatalf("SetStatus(A's own entity, onboard): %v", err)
	}
	if onboarded.Status != "active" {
		t.Errorf("SetStatus(onboard) status = %q, want %q", onboarded.Status, "active")
	}

	if _, err := store.SetStatus(ctxA, bEntityID, "archived"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus(B's id) as tenant A err = %v, want ErrNotFound", err)
	}

	// --- 6. Audit: A's four mutations, in commit order, all actor==subjA;
	// zero rows leaked under tenant B for any of A's actions.
	wantEvents := []string{
		"portfolio.entity.created",
		"portfolio.entity.updated",
		"portfolio.entity.offboarded",
		"portfolio.entity.onboarded",
	}
	events, actors := auditEventsForEntity(t, app, tenantA, aEntity.ID)
	if len(events) != len(wantEvents) {
		t.Fatalf("auditEventsForEntity(A, aEntity.ID) events = %v, want %v (length mismatch)", events, wantEvents)
	}
	for i, want := range wantEvents {
		if events[i] != want {
			t.Errorf("auditEventsForEntity(A, aEntity.ID) event[%d] = %q, want %q (commit order)", i, events[i], want)
		}
		if actors[i] != subjA {
			t.Errorf("auditEventsForEntity(A, aEntity.ID) actor[%d] = %q, want %q", i, actors[i], subjA)
		}
	}

	// QA adversarial addition (M3-10-01): check ALL four of A's mutation
	// events, not just created/updated -- offboarded/onboarded must not leak
	// into tenant B's audit either.
	for _, event := range wantEvents {
		if n := auditCount(t, app, tenantB, event); n != 0 {
			t.Errorf("audit_log rows for %s under tenant B = %d, want 0 (A's action must not leak into B's audit)", event, n)
		}
	}

	// --- 7. B's mirror entity is byte-for-byte unchanged, re-read as B ---
	gotB, err := store.GetByID(ctxB, bEntityID)
	if err != nil {
		t.Fatalf("GetByID(B's entity, as B) after the entire A-driven flow: %v", err)
	}
	if gotB.Name != "B Corp" {
		t.Errorf("B's entity name = %q, want unchanged %q", gotB.Name, "B Corp")
	}
	if gotB.TIN == nil || *gotB.TIN != bTIN {
		t.Errorf("B's entity tin = %v, want unchanged %q", gotB.TIN, bTIN)
	}
	if gotB.Status != "active" {
		t.Errorf("B's entity status = %q, want unchanged %q", gotB.Status, "active")
	}
}
