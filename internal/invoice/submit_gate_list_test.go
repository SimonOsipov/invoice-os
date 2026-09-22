// BUG-12-01 (task-868): the list wire must carry the SAME submitGate answer the
// detail wire already publishes.
//
// Every assertion here reads RAW wire bytes rather than a decoded struct: the keys
// under test do not exist yet, and a decode would turn an absent key into a silent
// zero -- the fail-open shape this story exists to remove.
//
// Run: DEV_DB_PORT=5437 make -s test-invoice
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- helpers ----------------------------------------------------------------

// listSubmitPair drives the REAL ListHandler (the cmd/invoice/main.go wiring) and
// returns invID's submit pair UNDECODED. An invoice missing from the page is a
// Fatal: a silent "not found" would make every comparison below vacuous.
func listSubmitPair(t *testing.T, store *Store, ctx context.Context, invID, label string) (canSubmit, reason string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices?limit=200", nil)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	ListHandler(store.List, store.RowFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: LIST status = %d, want 200 (body=%s)", label, rec.Code, rec.Body.String())
	}
	for _, row := range listRowsRaw(t, rec) {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("%s: decode row id: %v", label, err)
		}
		if id == invID {
			return submitFlagsOf(t, row, label+" (the LIST row)")
		}
	}
	t.Fatalf("%s: invoice %s is not on the page: %s", label, invID, rec.Body.String())
	return "", ""
}

// detailSubmitPair is listSubmitPair's twin on GET /v1/invoices/{id}.
func detailSubmitPair(t *testing.T, store *Store, ctx context.Context, invID, label string) (canSubmit, reason string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+invID, nil)
	r.SetPathValue("id", invID)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	GetHandler(store.Get, store.CallerRole, store.ApprovalFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: GET status = %d, want 200 (body=%s)", label, rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: decode detail body %q: %v", label, rec.Body.String(), err)
	}
	return submitFlagsOf(t, body, label+" (the DETAIL response)")
}

// --- AC-2: one gate, two wires ----------------------------------------------

// TestListAndDetail_SubmitGateCannotDisagree is the spec that proves ONE predicate
// feeds TWO wires. A second copy of submitGate's ladder written for the list -- in
// Go, in SQL or in TypeScript -- passes every other spec in this file and fails here
// on the first rung whose wording or order drifted.
//
// TWO tenants, and they cannot be merged. TransmitClearTx's gate is
// `EXISTS (SELECT 1 FROM approval_policy_versions WHERE is_active)`, which is
// TENANT-wide under RLS, so one active policy makes every unarmed row in that tenant
// read blocked and the seven-status walk stops testing the status rungs.
//
// The armed-state claim itself lives in
// TestListHandler_SubmitFlagsFlipWithTheApprovalRun; this spec only requires the
// two wires to agree, at every status.
func TestListAndDetail_SubmitGateCannotDisagree(t *testing.T) {
	super, app := dbTestPools(t)

	// Leg 1: a tenant with NO active policy version. TransmitClearTx short-circuits
	// every id to clear, isolating the role and status rungs.
	walkTenant := seedTenant(t, super, "BUG-12-01-WALK")
	walkEntity := seedEntity(t, super, walkTenant, "BUG-12-01 Walk Corp")
	walkSubject := uuid.NewString()
	seedMembership(t, super, walkTenant, walkSubject, "admin")
	walkCtx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: walkSubject, Role: "authenticated", TenantID: walkTenant,
	})

	// wantCan/wantReason are hand-written, not derived from submitGate: an oracle that
	// called the predicate under test would agree with any ladder, including a broken one.
	walk := []struct {
		status     Status
		wantCan    string
		wantReason string
	}{
		{StatusDraft, "false", jsonOf(t, "Only validated invoices can be submitted — re-validate this invoice first.")},
		{StatusValidated, "true", "null"},
		{StatusQueued, "false", "null"},
		{StatusSubmitted, "false", "null"},
		{StatusAccepted, "false", "null"},
		{StatusRejected, "false", jsonOf(t, "Only validated invoices can be submitted — edit this invoice and re-validate it first.")},
		{StatusFailed, "false", "null"},
	}
	walkIDs := make([]string, len(walk))
	for i, c := range walk {
		walkIDs[i] = seedInvoiceAtStatus(t, super, walkTenant, walkEntity, "bug-12-01-walk-"+string(c.status), c.status)
	}

	// The vacuity guard the sibling approve spec uses: an agreement of two falses,
	// or of two nulls, proves nothing.
	sawTrue := false
	sawSentence := false

	store := NewStore(app)
	for i, c := range walk {
		t.Run(string(c.status), func(t *testing.T) {
			label := string(c.status)
			listCan, listReason := listSubmitPair(t, store, walkCtx, walkIDs[i], label)
			detailCan, detailReason := detailSubmitPair(t, store, walkCtx, walkIDs[i], label)

			if listCan != detailCan {
				t.Errorf("can_submit: list = %s, detail = %s -- ONE submitGate call feeds both wires, so they cannot differ", listCan, detailCan)
			}
			if listReason != detailReason {
				t.Errorf("submit_blocked_reason:\n  list   = %s\n  detail = %s\n-- the same submitGate call must produce both", listReason, detailReason)
			}
			if listCan != c.wantCan {
				t.Errorf("can_submit = %s, want %s for a %s invoice read by an admin in a policy-free tenant", listCan, c.wantCan, c.status)
			}
			if listReason != c.wantReason {
				t.Errorf("submit_blocked_reason = %s, want %s", listReason, c.wantReason)
			}
			if listCan == "true" {
				sawTrue = true
			}
			if listCan == "false" && listReason != "null" {
				sawSentence = true
			}
		})
	}

	// Leg 2: MF-2 row 5, the production defect state -- an active policy with an open
	// run, in its OWN tenant. seedApprovalFactsFixture publishes the policy and
	// armInvoice opens the run through Store.ApplyValidation, the real arming path.
	fx := seedApprovalFactsFixture(t, super, "BUG-12-01-ARMED", true)
	fx.armInvoice(t, super, app, "bug-12-01-armed")

	reallyArmed := false
	t.Run("armed_validated_really_armed", func(t *testing.T) {
		armedStore := NewStore(app)
		af, err := armedStore.ApprovalFacts(fx.ctx, fx.invID)
		if err != nil {
			t.Fatalf("ApprovalFacts: %v", err)
		}
		if af.TransmitClear {
			t.Fatal("ApprovalFacts.TransmitClear = true for an OPEN run under an active policy -- the fixture did not arm, so the armed leg below proves nothing")
		}
		reallyArmed = true
	})

	t.Run("armed_validated", func(t *testing.T) {
		if !reallyArmed {
			t.Fatal("the armed fixture never reached the defect state -- see armed_validated_really_armed")
		}
		armedStore := NewStore(app)
		listCan, listReason := listSubmitPair(t, armedStore, fx.ctx, fx.invID, "armed")
		detailCan, detailReason := detailSubmitPair(t, armedStore, fx.ctx, fx.invID, "armed")

		if listCan != detailCan {
			t.Errorf("can_submit: list = %s, detail = %s -- the armed row is the state the two surfaces disagree on in production", listCan, detailCan)
		}
		if listReason != detailReason {
			t.Errorf("submit_blocked_reason:\n  list   = %s\n  detail = %s", listReason, detailReason)
		}
		if wantCan := "false"; listCan != wantCan {
			t.Errorf("can_submit = %s, want %s (open run under an active policy)", listCan, wantCan)
		}
		if want := jsonOf(t, awaitingApprovalReason); listReason != want {
			t.Errorf("submit_blocked_reason = %s, want %s", listReason, want)
		}
	})

	if !sawTrue {
		t.Error("no walk leg read can_submit true -- an agreement of two falses proves nothing")
	}
	if !sawSentence {
		t.Error("no walk leg read a refusal SENTENCE -- an agreement of two nulls proves nothing")
	}
}

// --- AC-3: the armed run reaches the list wire -------------------------------

// TestListHandler_SubmitFlagsFlipWithTheApprovalRun: one invoice, open run then approved run;
// the list row and the detail body flip together and agree at each leg.
//
// The flip assertion runs FIRST. Two identical readings mean the fixture never armed,
// and every assertion below it would pass on a wire that ignored the run state entirely.
func TestListHandler_SubmitFlagsFlipWithTheApprovalRun(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "BUG-15-01-FLIP", true)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID) // open
	store := NewStore(app)

	openList, openReason := listSubmitPair(t, store, fx.ctx, fx.invID, "open")
	openDetail, openDetailReason := detailSubmitPair(t, store, fx.ctx, fx.invID, "open")

	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	clearList, clearReason := listSubmitPair(t, store, fx.ctx, fx.invID, "approved")
	clearDetail, clearDetailReason := detailSubmitPair(t, store, fx.ctx, fx.invID, "approved")

	if openList == clearList && openReason == clearReason {
		t.Fatalf("the list submit pair is %s/%s at BOTH run states -- the fixture never armed, so this spec is vacuous", openList, openReason)
	}
	if openList != "false" {
		t.Errorf("open run: list can_submit = %s, want false", openList)
	}
	if want := jsonOf(t, awaitingApprovalReason); openReason != want {
		t.Errorf("open run: list submit_blocked_reason = %s, want %s", openReason, want)
	}
	if openList != openDetail || openReason != openDetailReason {
		t.Errorf("open run: list = %s/%s, detail = %s/%s -- one gate, two wires", openList, openReason, openDetail, openDetailReason)
	}
	if clearList != "true" || clearReason != "null" {
		t.Errorf("approved run: list can_submit/submit_blocked_reason = %s/%s, want true/null", clearList, clearReason)
	}
	if clearList != clearDetail || clearReason != clearDetailReason {
		t.Errorf("approved run: list = %s/%s, detail = %s/%s -- one gate, two wires", clearList, clearReason, clearDetail, clearDetailReason)
	}
}
