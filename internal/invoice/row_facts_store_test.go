// task-499 (APPR-08-08): DB-backed specs for (*Store).RowFacts -- the list's
// row-approval read -- and for the end-to-end guarantee that APPROVALS_ENFORCED never
// gates it.
//
// Each case carries a POSITIVE leg asserting a real, populated answer: an "is empty" or
// "the two are equal" assertion alone passes for free against a method that returns
// nothing, which is exactly the vacuous-oracle hole APPR-08-07's QA found in this
// subtask's two predecessors.
//
// Spec-to-test map (task-499 Test Specs table):
//
//	AC-6 TestStoreRowFacts_DoesNotConsultApprovalsEnforced
//	AC-6 TestListHandler_ApprovalFactsIgnoreTheEnforcementFlag
//	RLS  TestStoreRowFacts_IsTenantScopedByRLS
//
// Fixtures are reused wholesale: seedApprovalFactsFixture / armInvoice
// (approval_facts_test.go), tenantCtx (awaiting_approval_test.go), seedTenant /
// dbTestPools (store_test.go), listRowsRaw (row_approval_envelope_test.go).
//
// Run: DATABASE_URL=... DATABASE_SUPERUSER_URL=... go test -p 1 -count=1 ./internal/invoice/...
package invoice

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/approval"
)

// wantArmedRowFacts is what an armInvoice'd fixture must answer: an open run whose
// single kind='approval' step is pending at ord 0 on a live, staffed role.
// seedOneStepActivePolicyTenant sets no sla_hours, so due_at is NULL and overdue false.
func wantArmedRowFacts() approval.RowFacts {
	ord := 0
	title := "Finance Lead"
	return approval.RowFacts{
		RunState: "open", PendingOrd: &ord, PendingRoleTitle: &title,
		PendingHolderWarn: false, DueAt: nil, Overdue: false,
	}
}

// --- AC-6: the flag gates enforcement, not visibility -----------------------

// TestStoreRowFacts_DoesNotConsultApprovalsEnforced: the ONE way this method differs
// from (*Store).ApprovalFacts, which folds the flag into TransmitClear. Two stores over
// the same armed fixture, flag on and off, must answer identically
// (docs/approvals.md section 11).
//
// Three legs, and the equality one alone is worthless without the other two:
//
//	POPULATED -- the flag-ON map really carries the armed invoice's facts, so a method
//	  that answered {} for both would not pass by returning nothing.
//	LIVE -- WithApprovalsEnforced(true) is actually in force on that same store, proved
//	  through ApprovalFacts, whose TransmitClear the flag DOES fold. Without this leg,
//	  wrapping the whole read in "if !s.approvalsEnforced { ... }" passes: nothing else
//	  in the package reads the flag on a store built for this test.
//	EQUAL -- the two maps are deeply equal.
func TestStoreRowFacts_DoesNotConsultApprovalsEnforced(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-08-FLAGPAIR", true)
	fx.armInvoice(t, super, app, "appr-08-08-flagpair")

	on := NewStore(app, WithApprovalsEnforced(true))
	off := NewStore(app, WithApprovalsEnforced(false))

	gotOn, err := on.RowFacts(fx.ctx, []string{fx.invID})
	if err != nil {
		t.Fatalf("RowFacts (flag on): %v", err)
	}
	gotOff, err := off.RowFacts(fx.ctx, []string{fx.invID})
	if err != nil {
		t.Fatalf("RowFacts (flag off): %v", err)
	}

	// POPULATED.
	factsOn, ok := gotOn[fx.invID]
	if !ok {
		t.Fatalf("RowFacts (flag on) = %+v, want an entry for the armed invoice %s -- an empty map would make the equality check below vacuous", gotOn, fx.invID)
	}
	if !reflect.DeepEqual(factsOn, wantArmedRowFacts()) {
		t.Errorf("RowFacts (flag on)[%s] = %+v, want %+v", fx.invID, factsOn, wantArmedRowFacts())
	}

	// LIVE: the flag-ON store really has the flag on.
	af, err := on.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts (flag on): %v", err)
	}
	if af.TransmitClear {
		t.Fatal("ApprovalFacts(flag on).TransmitClear = true for an OPEN run under an active policy -- WithApprovalsEnforced(true) is not in force on this store, so the equality check below proves nothing")
	}

	// EQUAL.
	if !reflect.DeepEqual(gotOn, gotOff) {
		t.Errorf("RowFacts differ by APPROVALS_ENFORCED:\n  on  = %+v\n  off = %+v\nthe flag gates enforcement, never visibility (docs/approvals.md section 11)", gotOn, gotOff)
	}
}

// TestStoreRowFacts_IsTenantScopedByRLS: RLS is the only tenant scope on this read
// (approval.RowFactsTx carries no tenant_id predicate, by design --
// TestGateFile_NoTenantIdPredicate). Tenant B asking about tenant A's armed invoice
// gets an EMPTY map and no error: absent-from-the-map reads as "no run", which is the
// fail-closed answer, and an error would be an existence oracle.
//
// The tenant-A leg is what makes the tenant-B leg mean anything: it proves the invoice
// really is armed and really is readable, so the empty answer is scoping rather than
// "nothing was ever seeded". APPR-08-07's QA found this subtask's predecessor could not
// have detected a leak for exactly the missing-control reason.
func TestStoreRowFacts_IsTenantScopedByRLS(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-08-RLS", true)
	fx.armInvoice(t, super, app, "appr-08-08-rls")

	store := NewStore(app, WithApprovalsEnforced(true))

	// Control: tenant A sees its own armed invoice.
	own, err := store.RowFacts(fx.ctx, []string{fx.invID})
	if err != nil {
		t.Fatalf("RowFacts (owning tenant): %v", err)
	}
	if got, ok := own[fx.invID]; !ok {
		t.Fatalf("RowFacts (owning tenant) = %+v, want an entry for %s -- without this the cross-tenant leg below cannot tell scoping from an unarmed fixture", own, fx.invID)
	} else if got.RunState != "open" {
		t.Fatalf("RowFacts (owning tenant)[%s].RunState = %q, want \"open\"", fx.invID, got.RunState)
	}

	// The leak this test exists for: a foreign tenant asking about A's id by value.
	otherTenant := seedTenant(t, super, "APPR-08-08-RLS other tenant")
	leaked, err := store.RowFacts(tenantCtx(otherTenant), []string{fx.invID})
	if err != nil {
		t.Fatalf("RowFacts (foreign tenant): %v (want an empty map, never an error -- an error is an existence oracle)", err)
	}
	if len(leaked) != 0 {
		t.Errorf("RowFacts (foreign tenant) = %+v, want an empty map -- tenant %s must learn nothing about tenant %s's invoice", leaked, otherTenant, fx.tenantID)
	}
}

// TestListHandler_ApprovalFactsIgnoreTheEnforcementFlag: the same AC-6 guarantee end to
// end, DB row through to wire byte -- the REAL Store.List and Store.RowFacts wired into
// the REAL ListHandler, exactly as cmd/invoice/main.go wires them, on a flag-on and a
// flag-off store. The two rows' approval objects must be byte-identical.
//
// A store-level equality can hold while the HANDLER forks on the flag (an
// "if s.approvalsEnforced" in the seam wiring, a nulled envelope on a flag-off
// deployment); this is the leg that would catch that.
func TestListHandler_ApprovalFactsIgnoreTheEnforcementFlag(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-08-WIREFLAG", true)
	fx.armInvoice(t, super, app, "appr-08-08-wireflag")

	approvalOf := func(t *testing.T, store *Store) json.RawMessage {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/invoices?limit=200", nil)
		r = r.WithContext(fx.ctx)
		rec := httptest.NewRecorder()
		ListHandler(store.List, store.RowFacts, nil).ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		for _, row := range listRowsRaw(t, rec) {
			var idField string
			if err := json.Unmarshal(row["id"], &idField); err != nil {
				t.Fatalf("decode row id: %v", err)
			}
			if idField != fx.invID {
				continue
			}
			raw, ok := row["approval"]
			if !ok {
				t.Fatalf("the armed invoice's row has no \"approval\" key: %s", rec.Body.String())
			}
			return raw
		}
		t.Fatalf("the armed invoice %s is not on the page: %s", fx.invID, rec.Body.String())
		return nil
	}

	on := NewStore(app, WithApprovalsEnforced(true))
	off := NewStore(app, WithApprovalsEnforced(false))

	rawOn := approvalOf(t, on)
	rawOff := approvalOf(t, off)

	// POPULATED: an explicit null on both sides would make the comparison vacuous.
	var gotOn approval.RowFacts
	if err := json.Unmarshal(rawOn, &gotOn); err != nil {
		t.Fatalf("decode approval (flag on) %q: %v", string(rawOn), err)
	}
	if !reflect.DeepEqual(gotOn, wantArmedRowFacts()) {
		t.Errorf("approval (flag on) = %+v, want %+v", gotOn, wantArmedRowFacts())
	}

	if string(rawOn) != string(rawOff) {
		t.Errorf("the armed invoice's approval object differs by APPROVALS_ENFORCED:\n  on  = %s\n  off = %s", rawOn, rawOff)
	}
}
