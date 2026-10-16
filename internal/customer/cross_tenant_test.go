package customer_test

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/customer"
)

func TestCustomers_CrossTenantIDIs404(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	h := newMux(store)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer cross A")
	tenantB, _, _ := seedTenantWithAdmin(t, super, "customer cross B")
	aID := seedCustomer(t, super, tenantA, seed{TIN: tin1, Email: ptr("a@x.ng")})
	bID := seedCustomer(t, super, tenantB, seed{TIN: tin1, Email: ptr("b@x.ng"), Edited: []string{"state"}})

	rec := do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+bID, `{"email":"hax@x.ng"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH B's id as A = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
	if got := errorBody(t, rec); got != "not found" {
		t.Errorf("error = %q, want not found", got)
	}
	if _, err := store.Update(ctxA, bID, customer.UpdateInput{Email: ptr("hax@x.ng")}); !errors.Is(err, customer.ErrNotFound) {
		t.Errorf("Store.Update(B's id) as A err = %v, want ErrNotFound", err)
	}

	r := readRow(t, super, bID)
	if strVal(r.Email) != "b@x.ng" || !reflect.DeepEqual(r.Edited, []string{"state"}) {
		t.Errorf("B's row = email %q edited %v, want b@x.ng [state] (untouched)", strVal(r.Email), r.Edited)
	}
	for name, tenant := range map[string]string{"A": tenantA, "B": tenantB} {
		if n := len(auditRows(t, super, tenant, updatedEvent)); n != 0 {
			t.Errorf("audit rows in tenant %s after the cross-tenant PATCH = %d, want 0", name, n)
		}
	}

	// A's own record is correctable, and its audit row lands in A only.
	rec = do(t, h, ctxA, http.MethodPatch, "/v1/customers/"+aID, `{"email":"a2@x.ng"}`)
	wantStatus(t, rec, http.StatusOK)
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 1 {
		t.Errorf("control: audit rows in tenant A = %d, want 1", n)
	}
	if n := len(auditRows(t, super, tenantB, updatedEvent)); n != 0 {
		t.Errorf("audit rows in tenant B after A's own correction = %d, want 0", n)
	}
}

func TestLookupByTIN_NeverReturnsAnotherTenantsRecord(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer lookup A")
	tenantB, _, ctxB := seedTenantWithAdmin(t, super, "customer lookup B")
	bID := seedCustomer(t, super, tenantB, seed{TIN: tin1})
	seedCustomer(t, super, tenantA, seed{TIN: tin2})

	got, err := lookup(t, app, ctxB, tin1)
	if err != nil || got == nil || got.ID != bID {
		t.Fatalf("control: LookupByTIN as B = %v, %v, want B's record %s", got, err, bID)
	}
	got, err = lookup(t, app, ctxA, tin1)
	if err != nil || got != nil {
		t.Errorf("LookupByTIN as A for B's TIN = %v, %v, want nil, nil", got, err)
	}
}

func TestStoreList_TINFilterNeverCrossesTenants(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer filter A")
	tenantB, _, _ := seedTenantWithAdmin(t, super, "customer filter B")
	aID := seedCustomer(t, super, tenantA, seed{TIN: tin1})
	seedCustomer(t, super, tenantB, seed{TIN: tin1})
	seedCustomer(t, super, tenantB, seed{TIN: tin2})

	got, total, err := store.List(ctxA, customer.ListFilter{TIN: tin1, Limit: 50})
	if err != nil || total != 1 || !reflect.DeepEqual(ids(got), []string{aID}) {
		t.Errorf("List tin=%s as A = ids %v total %d err %v, want only A's [%s] total 1", tin1, ids(got), total, err, aID)
	}
	got, total, err = store.List(ctxA, customer.ListFilter{TIN: tin2, Limit: 50})
	if err != nil || len(got) != 0 || total != 0 {
		t.Errorf("List tin=%s (held only by B) as A = %v total %d err %v, want none", tin2, tins(got), total, err)
	}
}

func TestRLS_StatusFilterNeverCrossesTenants(t *testing.T) {
	super, app := dbTestPools(t)
	store := customer.NewStore(app)
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer status cross A")
	tenantB, _, _ := seedTenantWithAdmin(t, super, "customer status cross B")
	aID := seedCustomer(t, super, tenantA, seed{TIN: tin1, Status: "archived"})
	bID := seedCustomer(t, super, tenantB, seed{TIN: tin1, Status: "archived"})

	got, total, err := store.List(ctxA, customer.ListFilter{Status: "archived", Limit: 50})
	if err != nil {
		t.Fatalf("List archived as A: %v", err)
	}
	if total != 1 || !reflect.DeepEqual(ids(got), []string{aID}) {
		t.Errorf("List archived as A = ids %v total %d, want [%s] total 1", ids(got), total, aID)
	}
	if st := rowStatus(t, super, bID); st != "archived" {
		t.Errorf("B's status = %q, want archived (untouched)", st)
	}
}

func TestRLS_CustomerCreateNeverCrossesTenants(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA, _, ctxA := seedTenantWithAdmin(t, super, "customer create cross A")
	tenantB, _, _ := seedTenantWithAdmin(t, super, "customer create cross B")
	bID := seedCustomer(t, super, tenantB, seed{TIN: tin1, Name: ptr("B Ltd"), Edited: []string{"state"}})
	before := rowJSON(t, super, bID)

	rec := do(t, h, ctxA, http.MethodPost, "/v1/customers", `{"tin":"`+tin1+`","name":"A Ltd"}`)
	wantStatus(t, rec, http.StatusCreated)

	got := decodeCustomer(t, rec.Body.Bytes())
	if got.ID == bID {
		t.Errorf("A's create returned B's id %s", bID)
	}
	if n := countTIN(t, super, tenantA, tin1); n != 1 {
		t.Fatalf("A's rows for %s = %d, want 1", tin1, n)
	}
	if tenant := rowTenant(t, super, idFor(t, super, tenantA, tin1)); tenant != tenantA {
		t.Errorf("A's row tenant_id = %q, want %q", tenant, tenantA)
	}
	if after := rowJSON(t, super, bID); after != before {
		t.Errorf("B's row after A's create = %s, want unchanged %s", after, before)
	}
	if n := len(auditRows(t, super, tenantB, createdEvent)); n != 0 {
		t.Errorf("created audit rows in tenant B = %d, want 0", n)
	}
	if n := len(auditRows(t, super, tenantA, createdEvent)); n != 1 {
		t.Errorf("control: created audit rows in tenant A = %d, want 1", n)
	}
}
