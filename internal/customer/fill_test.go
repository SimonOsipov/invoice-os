package customer_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

func seedEntity(t *testing.T, super *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`, tenantID, name).Scan(&id); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	return id
}

func fillCreate(ctx context.Context, t *testing.T, store *invoice.Store, entityID, number, tin, name, email string) {
	t.Helper()
	if _, err := store.Create(ctx, invoice.CreateInput{
		EntityID: entityID, InvoiceNumber: number, BuyerTIN: &tin, BuyerName: &name, BuyerEmail: &email,
	}); err != nil {
		t.Fatalf("Create(%s): %v", number, err)
	}
}

func fillRecord(t *testing.T, super *pgxpool.Pool, tenantID, tin string) (name, email string) {
	t.Helper()
	var n, e *string
	if err := super.QueryRow(context.Background(),
		`SELECT name, email FROM customers WHERE tenant_id = $1 AND tin = $2`, tenantID, tin).Scan(&n, &e); err != nil {
		t.Fatalf("no customers row for tenant %s tin %s: %v", tenantID, tin, err)
	}
	return strVal(n), strVal(e)
}

func TestFill_SameTINTwoTenantsTwoRecords(t *testing.T) {
	super, app := dbTestPools(t)
	store := invoice.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "fill two A")
	tenantB, _, ctxB := seedTenantWithAdmin(t, super, "fill two B")
	entityA, entityB := seedEntity(t, super, tenantA, "A entity"), seedEntity(t, super, tenantB, "B entity")

	fillCreate(ctxA, t, store, entityA, "FILL-A1", tin1, "A Ltd", "a@x.ng")
	fillCreate(ctxB, t, store, entityB, "FILL-B1", tin1, "B Ltd", "b@x.ng")
	for tenant, want := range map[string]string{tenantA: "A Ltd", tenantB: "B Ltd"} {
		var n int
		if err := super.QueryRow(context.Background(), `SELECT count(*) FROM customers WHERE tenant_id = $1`, tenant).Scan(&n); err != nil || n != 1 {
			t.Fatalf("records in tenant %s = %d (err %v), want 1", tenant, n, err)
		}
		if name, _ := fillRecord(t, super, tenant, tin1); name != want {
			t.Errorf("tenant %s record name = %s, want %s", tenant, name, want)
		}
	}

	fillCreate(ctxA, t, store, entityA, "FILL-A2", tin1, "A Two Ltd", "a2@x.ng")
	if name, email := fillRecord(t, super, tenantA, tin1); name != "A Two Ltd" || email != "a2@x.ng" {
		t.Errorf("A's record after its second save = %s/%s, want A Two Ltd/a2@x.ng", name, email)
	}
	if name, email := fillRecord(t, super, tenantB, tin1); name != "B Ltd" || email != "b@x.ng" {
		t.Errorf("B's record after A's second save = %s/%s, want B Ltd/b@x.ng (unchanged)", name, email)
	}
}

func TestFill_WritesNoAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	store := invoice.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "fill audit")
	entityA := seedEntity(t, super, tenantA, "A entity")

	fillCreate(ctxA, t, store, entityA, "FILL-AU1", tin1, "First Ltd", "a@x.ng")
	fillCreate(ctxA, t, store, entityA, "FILL-AU2", tin1, "Second Ltd", "b@x.ng")
	if name, email := fillRecord(t, super, tenantA, tin1); name != "Second Ltd" || email != "b@x.ng" {
		t.Fatalf("control: record = %s/%s, want Second Ltd/b@x.ng (both saves filled)", name, email)
	}
	if n := len(auditRows(t, super, tenantA, "invoice.created")); n != 2 {
		t.Errorf("control: invoice.created rows = %d, want 2", n)
	}
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event LIKE 'customers.%'`, tenantA).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if n != 0 {
		t.Errorf("customers.* audit rows after two filling saves = %d, want 0", n)
	}
}
