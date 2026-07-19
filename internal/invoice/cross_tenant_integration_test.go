package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestRLS_InvoicesStoreChildWritesTenantScoped (Test Specs, M4-02-01 story /
// task-96): Store.Create's child writes -- line_items and the genesis
// invoice_status_history row -- must carry the CALLER's tenant_id, not
// merely be reachable via the parent invoice_id FK. A Store that forgot to
// stamp tenant_id on a child INSERT (relying solely on the invoice_id join
// to imply tenancy) would still pass every INV-STORE-0x positive-path check
// in store_test.go (those only look at child rows through an invoice_id
// filter) while silently producing an RLS-orphaned or even
// cross-tenant-visible child row. This test proves the positive case
// (visible under tenant A's own GUC) AND the negative case (invisible under
// tenant B's GUC) for both child tables in one pass.
func TestRLS_InvoicesStoreChildWritesTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-CHILD-SCOPE tenant A")
	tenantB := seedTenant(t, super, "INV-CHILD-SCOPE tenant B")
	entityA := seedEntity(t, super, tenantA, "INV-CHILD-SCOPE A Corp")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	desc := "line 1"
	inv, err := store.Create(cA, CreateInput{
		EntityID:      entityA,
		InvoiceNumber: "INV-CHILD-SCOPE-A",
		LineItems:     []LineItemInput{{Description: &desc}},
	})
	if err != nil {
		t.Fatalf("Create (as tenant A): %v", err)
	}

	// Positive: both child rows are visible under tenant A's own GUC context.
	err = db.WithinTenantTx(ctx, app, tenantA, func(tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM line_items WHERE invoice_id = $1`, inv.ID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			t.Errorf("line_items visible to tenant A for its own invoice = %d, want 1", n)
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			t.Errorf("invoice_status_history visible to tenant A for its own invoice = %d, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant A visibility check): %v", err)
	}

	// Negative: neither child row is visible under tenant B's GUC context --
	// proving the child rows carry tenant A's tenant_id (RLS-scoped), not
	// left NULL/unscoped or accidentally stamped with B's.
	err = db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM line_items WHERE invoice_id = $1`, inv.ID).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Errorf("line_items visible to tenant B for tenant A's invoice = %d, want 0", n)
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Errorf("invoice_status_history visible to tenant B for tenant A's invoice = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant B visibility check): %v", err)
	}
}

// TestRLS_InvoicesTransitionCrossTenantRefused (Test Specs, M4-02-02 story /
// task-97): Transition cannot mutate -- or even see -- another tenant's
// invoice. RLS's `SELECT status ... FOR UPDATE` 0-rows for a cross-tenant id,
// which Transition maps to ErrNotFound, and tenant B's status +
// invoice_status_history are left completely untouched. Distinct from
// TestTransition_NotFoundAndCrossTenant (transition_test.go, INV-SM-04's
// plain not-found case): this asserts the RLS isolation boundary
// specifically, re-reading B's row under B's OWN GUC (db.WithinTenantTx)
// afterward -- not just via the superuser bypass -- so a Store that somehow
// wrote a change invisible to a superuser-count assertion would still be
// caught here, mirroring TestRLS_InvoicesStoreChildWritesTenantScoped above.
func TestRLS_InvoicesTransitionCrossTenantRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-SM-RLS tenant A")
	tenantB := seedTenant(t, super, "INV-SM-RLS tenant B")
	entityB := seedEntity(t, super, tenantB, "INV-SM-RLS B entity")

	store := NewStore(app)
	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	invB, err := store.Create(cB, CreateInput{EntityID: entityB, InvoiceNumber: "INV-SM-RLS-B"})
	if err != nil {
		t.Fatalf("Create (as tenant B): %v", err)
	}

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	if _, err := store.Transition(cA, invB.ID, StatusValidated); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Transition(tenant B's invoice) as tenant A err = %v, want ErrNotFound", err)
	}

	// Re-read under tenant B's OWN GUC (not the superuser bypass) -- proves
	// the refusal left B's row genuinely untouched from B's own vantage, not
	// merely invisible to A.
	err = db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		var status string
		if e := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invB.ID).Scan(&status); e != nil {
			return e
		}
		if status != string(StatusDraft) {
			t.Errorf("tenant B's invoice status after refused cross-tenant Transition = %q, want unchanged %q", status, StatusDraft)
		}
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invB.ID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			t.Errorf("invoice_status_history rows for tenant B's invoice = %d, want 1 (genesis row only, no new row from the refused transition)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant B visibility check): %v", err)
	}
}

// TestRLS_InvoiceHistory_ReturnsOrderedTransitions (Test Specs #6, task-160/
// M4-22-01, Core AC #1/#2/#3): Store.History returns every
// invoice_status_history row for the caller's own invoice, ordered
// changed_at ASC, id ASC -- the genesis (NULL->draft) row first, then each
// subsequent transition in the order it happened. Builds the fixture
// through the REAL Store.Create/Store.Transition (both already shipped),
// not a superuser seed, so the genesis row's own actor/timestamp are the
// real ones a caller would see.
func TestRLS_InvoiceHistory_ReturnsOrderedTransitions(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-HIST-01 tenant A")
	entityA := seedEntity(t, super, tenantA, "INV-HIST-01 entity")

	store := NewStore(app)
	subject := uuid.NewString()
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantA})

	inv, err := store.Create(cA, CreateInput{EntityID: entityA, InvoiceNumber: "INV-HIST-01"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(cA, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition(draft->validated): %v", err)
	}

	got, err := store.History(cA, inv.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("History returned %d rows, want 2 (genesis + one transition)", len(got))
	}

	if got[0].FromStatus != nil {
		t.Errorf("got[0].FromStatus = %q, want nil (the genesis row has no predecessor)", *got[0].FromStatus)
	}
	if got[0].ToStatus != StatusDraft {
		t.Errorf("got[0].ToStatus = %q, want %q", got[0].ToStatus, StatusDraft)
	}
	if got[0].Actor != subject {
		t.Errorf("got[0].Actor = %q, want %q", got[0].Actor, subject)
	}

	if got[1].FromStatus == nil || *got[1].FromStatus != StatusDraft {
		t.Errorf("got[1].FromStatus = %v, want a pointer to %q", got[1].FromStatus, StatusDraft)
	}
	if got[1].ToStatus != StatusValidated {
		t.Errorf("got[1].ToStatus = %q, want %q", got[1].ToStatus, StatusValidated)
	}
	if got[1].Actor != subject {
		t.Errorf("got[1].Actor = %q, want %q", got[1].Actor, subject)
	}

	if got[1].ChangedAt.Before(got[0].ChangedAt) {
		t.Errorf("got[1].ChangedAt (%v) is before got[0].ChangedAt (%v), want non-decreasing changed_at ASC order", got[1].ChangedAt, got[0].ChangedAt)
	}
}

// TestRLS_InvoiceHistory_CrossTenantReturnsNothing (Test Specs #7 as
// corrected by Stage 1 GAP 2, AC #5): an id belonging to another tenant
// must resolve to ErrNotFound, exactly like a genuinely nonexistent id --
// indistinguishable, zero rows leaked. This is the highest-value spec in
// the set: Store.History is necessarily a MULTI-row tx.Query (unlike Get's
// single-row tx.QueryRow), so Query()/Next() never yields pgx.ErrNoRows on
// an RLS-filtered zero-row result -- a naive implementation that only
// checks errors.Is(err, pgx.ErrNoRows) would silently return (nil, nil) ->
// HTTP 200 [] here instead of ErrNotFound. The superuser read-back proves
// real history rows exist for invoiceA (so this is not vacuously "nothing
// to leak in the first place").
func TestRLS_InvoiceHistory_CrossTenantReturnsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-HIST-02 tenant A")
	tenantB := seedTenant(t, super, "INV-HIST-02 tenant B")
	entityA := seedEntity(t, super, tenantA, "INV-HIST-02 A entity")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	invA, err := store.Create(cA, CreateInput{EntityID: entityA, InvoiceNumber: "INV-HIST-02-A"})
	if err != nil {
		t.Fatalf("Create (as tenant A): %v", err)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invA.ID); n == 0 {
		t.Fatal("setup: no invoice_status_history rows exist for tenant A's invoice -- the cross-tenant refusal below would be vacuous")
	}

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB})
	got, err := store.History(cB, invA.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("History(tenant A's invoice) as tenant B err = %v, want ErrNotFound (not a 200 empty array)", err)
	}
	if len(got) != 0 {
		t.Errorf("History(tenant A's invoice) as tenant B returned %d rows, want 0", len(got))
	}
}

// TestRLS_InvoiceHistory_UnsetGUCFailsClosed (Test Specs #8, defense in
// depth): Store.History wraps db.WithinRequestTenantTx like every other
// Store method (store.go's package doc), which resolves app.current_tenant
// from the caller's Identity in ctx and returns db.ErrNoTenant -- issuing
// no SQL at all -- when no identity is present. Proven non-vacuous the same
// way as TestRLS_InvoiceHistory_CrossTenantReturnsNothing above: a
// superuser read-back confirms real history rows exist for the invoice
// before the no-identity call, so "zero rows returned" is a genuine
// fail-closed refusal, never an unscoped all-tenants query.
func TestRLS_InvoiceHistory_UnsetGUCFailsClosed(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "INV-HIST-03 tenant A")
	entityA := seedEntity(t, super, tenantA, "INV-HIST-03 entity")

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	inv, err := store.Create(cA, CreateInput{EntityID: entityA, InvoiceNumber: "INV-HIST-03"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n == 0 {
		t.Fatal("setup: no invoice_status_history rows exist for the invoice -- the fail-closed assertion below would be vacuous")
	}

	got, err := store.History(ctx, inv.ID) // bare ctx: no identity, so app.current_tenant is never set
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("History(no identity in context) err = %v, want db.ErrNoTenant (fail-closed, never an unscoped all-tenants query)", err)
	}
	if len(got) != 0 {
		t.Errorf("History(no identity in context) returned %d rows, want 0", len(got))
	}
}
