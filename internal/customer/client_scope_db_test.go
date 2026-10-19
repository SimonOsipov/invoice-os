package customer_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

func csMember(t *testing.T, super *pgxpool.Pool, tenant, role string, clients ...string) context.Context {
	t.Helper()
	ctx := context.Background()
	user := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status, all_clients) VALUES ($1, $2, $3, 'active', false)`,
		tenant, user, role); err != nil {
		t.Fatalf("seed %s: %v", role, err)
	}
	for _, e := range clients {
		if _, err := super.Exec(ctx,
			`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, tenant, user, e); err != nil {
			t.Fatalf("seed membership_clients: %v", err)
		}
	}
	return auth.WithIdentity(ctx, auth.Identity{Subject: user, Role: "authenticated", TenantID: tenant})
}

func TestClientScope_BuyerDirectoryIsSharedWithEveryMember(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "CS buyer directory")
	entA := seedEntity(t, super, tenant, "Client A")
	seedEntity(t, super, tenant, "Client B")
	want := map[string]bool{}
	for _, tin := range []string{tin1, tin2, tin3} {
		want[seedCustomer(t, super, tenant, seed{TIN: tin})] = true
	}
	store := customer.NewStore(app)

	for name, ctx := range map[string]context.Context{
		"admin":      csMember(t, super, tenant, "admin"),
		"limited":    csMember(t, super, tenant, "reviewer", entA),
		"unassigned": csMember(t, super, tenant, "preparer"),
	} {
		items, total, err := store.List(ctx, customer.ListFilter{Limit: 50})
		if err != nil {
			t.Fatalf("%s List: %v", name, err)
		}
		if total != 3 || len(items) != 3 {
			t.Fatalf("%s List = %d items, total %d, want 3 and 3", name, len(items), total)
		}
		for _, c := range items {
			if !want[c.ID] {
				t.Errorf("%s List returned unexpected customer %s", name, c.ID)
			}
		}
	}
}
