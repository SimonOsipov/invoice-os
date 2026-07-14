package invoice

import (
	"context"
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
