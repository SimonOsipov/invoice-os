package customer_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/customer"
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

func fillCreate(ctx context.Context, t *testing.T, store *invoice.Store, entityID, number, tin, name, email string) string {
	t.Helper()
	inv, err := store.Create(ctx, invoice.CreateInput{
		EntityID: entityID, InvoiceNumber: number, BuyerTIN: &tin, BuyerName: &name, BuyerEmail: &email,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", number, err)
	}
	return inv.ID
}

func fillEditEmail(ctx context.Context, t *testing.T, store *invoice.Store, invoiceID, email string) {
	t.Helper()
	if _, err := store.Edit(ctx, invoiceID, invoice.EditInput{UpdateInput: invoice.UpdateInput{BuyerEmail: &email}}); err != nil {
		t.Fatalf("Edit(%s): %v", invoiceID, err)
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

	a1 := fillCreate(ctxA, t, store, entityA, "FILL-A3", tin1, "A Three Ltd", "a3@x.ng")
	fillEditEmail(ctxA, t, store, a1, "a-edit@x.ng")
	if _, email := fillRecord(t, super, tenantA, tin1); email != "a-edit@x.ng" {
		t.Fatalf("control: A's record email after its edit = %s, want a-edit@x.ng", email)
	}
	if name, email := fillRecord(t, super, tenantB, tin1); name != "B Ltd" || email != "b@x.ng" {
		t.Errorf("B's record after A's edit = %s/%s, want B Ltd/b@x.ng (unchanged)", name, email)
	}

	var aRecord string
	if err := super.QueryRow(context.Background(), `SELECT id FROM customers WHERE tenant_id = $1 AND tin = $2`, tenantA, tin1).Scan(&aRecord); err != nil {
		t.Fatalf("read A's record id: %v", err)
	}
	fixed := "a-fixed@x.ng"
	if _, err := customer.NewStore(app).Update(ctxA, aRecord, customer.UpdateInput{Email: &fixed}); err != nil {
		t.Fatalf("A corrects its record: %v", err)
	}
	fillCreate(ctxB, t, store, entityB, "FILL-B2", tin1, "B Two Ltd", "b2@x.ng")
	if name, email := fillRecord(t, super, tenantB, tin1); name != "B Two Ltd" || email != "b2@x.ng" {
		t.Errorf("B's record after A's correction and B's save = %s/%s, want B Two Ltd/b2@x.ng (A's correction does not guard B)", name, email)
	}
	if _, email := fillRecord(t, super, tenantA, tin1); email != fixed {
		t.Errorf("A's record email = %s, want %s", email, fixed)
	}
}

func TestFill_WritesNoAuditRow(t *testing.T) {
	super, app := dbTestPools(t)
	store := invoice.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "fill audit")
	entityA := seedEntity(t, super, tenantA, "A entity")

	fillCreate(ctxA, t, store, entityA, "FILL-AU1", tin1, "First Ltd", "a@x.ng")
	second := fillCreate(ctxA, t, store, entityA, "FILL-AU2", tin1, "Second Ltd", "b@x.ng")
	fillEditEmail(ctxA, t, store, second, "c@x.ng")
	if name, email := fillRecord(t, super, tenantA, tin1); name != "Second Ltd" || email != "c@x.ng" {
		t.Fatalf("control: record = %s/%s, want Second Ltd/c@x.ng (both saves and the edit filled)", name, email)
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
		t.Errorf("customers.* audit rows after two filling saves and an edit = %d, want 0", n)
	}
}
