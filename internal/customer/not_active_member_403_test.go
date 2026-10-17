package customer_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The seam's refusal must reach the wire as 403, not fall through to a 500 or a 401.
func TestCustomers_NotActiveMemberIs403(t *testing.T) {
	super, app := dbTestPools(t)
	h := newMux(customer.NewStore(app))
	tenantA := seedTenant(t, super, "customer not active")
	_, ctxActive := seedMember(t, super, tenantA, "admin", "active")
	_, ctxSuspended := seedMember(t, super, tenantA, "admin", "suspended")
	ctxNoRow := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	id := seedCustomer(t, super, tenantA, seed{TIN: tin1, Email: ptr("old@x.ng")})

	rec := do(t, h, ctxActive, http.MethodGet, "/v1/customers", "")
	wantStatus(t, rec, http.StatusOK)

	for name, ctx := range map[string]context.Context{"suspended": ctxSuspended, "no membership row": ctxNoRow} {
		for _, tc := range []struct{ method, target, body string }{
			{http.MethodGet, "/v1/customers", ""},
			{http.MethodPatch, "/v1/customers/" + id, `{"email":"new@x.ng"}`},
			{http.MethodPost, "/v1/customers", `{"tin":"` + tin2 + `","email":"new@x.ng"}`},
			{http.MethodPost, "/v1/customers/" + id + "/archive", ""},
			{http.MethodPost, "/v1/customers/" + id + "/restore", ""},
		} {
			rec := do(t, h, ctx, tc.method, tc.target, tc.body)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s = %d, want 403 (body %q)", tc.method, tc.target, name, rec.Code, rec.Body.String())
				continue
			}
			if got := errorBody(t, rec); got != db.NotActiveMemberMessage {
				t.Errorf("%s as %s error = %q, want db.NotActiveMemberMessage %q", tc.method, name, got, db.NotActiveMemberMessage)
			}
		}
	}

	if got := strVal(readRow(t, super, id).Email); got != "old@x.ng" {
		t.Errorf("email after refused PATCHes = %q, want unchanged old@x.ng", got)
	}
	if n := len(auditRows(t, super, tenantA, updatedEvent)); n != 0 {
		t.Errorf("audit rows after refused PATCHes = %d, want 0", n)
	}
	if st := rowStatus(t, super, id); st != "active" {
		t.Errorf("status after refused archive/restore = %q, want active", st)
	}
	if n := countTIN(t, super, tenantA, tin2); n != 0 {
		t.Errorf("rows for the refused POST's TIN = %d, want 0", n)
	}
}
