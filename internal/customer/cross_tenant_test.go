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
