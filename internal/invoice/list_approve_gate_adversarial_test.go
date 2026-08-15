// APPR-12-09 QA (task-526): adversarial coverage for the list wire's approve gate, over
// the REAL Store -- no stub between the wire and Postgres.
//
// list_approve_gate_test.go's A09-1/2/3 drive ListHandler through gateStub, so they pin
// the HANDLER's use of approvalGate but say nothing about how Store.RowFacts resolves the
// gate inputs. A09-4 uses the real store but asks one question (cross-tenant). These two
// close what is left:
//
//	NULL role key   -- the one uncovered path the implementation notes named. The DETAIL
//	                   half is pinned (TestGateFactsTx_NullRoleKeyOnThePendingStepIsNotHolding,
//	                   internal/approval); the LIST half was fail-closed only by
//	                   CONSTRUCTION -- one guarded writer in Store.RowFacts, one
//	                   absent-reads-false reader in ListHandler -- with nothing joining them.
//	list vs detail  -- the two wires read their gate inputs from DIFFERENT store methods
//	                   (Store.RowFacts + ListGateFacts vs Store.ApprovalFacts +
//	                   Store.CallerRole). A09-3 hands both halves the SAME stubbed facts, so
//	                   a disagreement created by the two store paths cannot reach it.
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// listPageByID drives the REAL Store through the REAL ListHandler -- main.go's own wiring
// -- and returns the page keyed by invoice id, raw.
func listPageByID(t *testing.T, store *Store, ctx context.Context) map[string]map[string]json.RawMessage {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices?limit=200", nil)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	ListHandler(store.List, store.RowFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	byID := map[string]map[string]json.RawMessage{}
	for _, row := range listRowsRaw(t, rec) {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("decode row id: %v", err)
		}
		byID[id] = row
	}
	return byID
}

// detailRowRaw drives the REAL Store through the REAL GetHandler for one invoice.
func detailRowRaw(t *testing.T, store *Store, ctx context.Context, invID string) map[string]json.RawMessage {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+invID, nil)
	r.SetPathValue("id", invID)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	GetHandler(store.Get, store.CallerRole, store.ApprovalFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode detail body %q: %v", rec.Body.String(), err)
	}
	return out
}

// armOneInvoice creates an invoice and promotes it through the REAL ApplyValidation, which
// is what writes approval_runs AND its pending approval_run_steps row.
func armOneInvoice(t *testing.T, super *pgxpool.Pool, store *Store, ctx context.Context, entityID, number string) string {
	t.Helper()
	inv, err := store.Create(ctx, CreateInput{EntityID: entityID, InvoiceNumber: number})
	if err != nil {
		t.Fatalf("Create(%s): %v", number, err)
	}
	got, err := store.ApplyValidation(ctx, inv.ID, []Violation{}, seedRuleSetVersionID(t, super), contentFingerprint(inv, inv.LineItems))
	if err != nil {
		t.Fatalf("ApplyValidation(%s): %v", number, err)
	}
	if got.Status != StatusValidated {
		t.Fatalf("%s status = %q, want %q -- the fixture never armed", number, got.Status, StatusValidated)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM approval_run_steps s JOIN approval_runs r ON r.id = s.run_id
		  WHERE r.invoice_id = $1 AND s.kind = 'approval' AND s.state = 'pending'`, inv.ID); n != 1 {
		t.Fatalf("%s has %d pending approval steps, want 1", number, n)
	}
	return inv.ID
}

// --- the NULL workflow_role_key on the pending step, at the LIST wire ---------

// TestListHandler_RealStore_NullRoleKeyOnThePendingStepCannotApprove joins the two halves
// the implementation left unjoined.
//
// A policy step may name NO workflow role (approval_policy_steps.workflow_role_key is
// nullable, and approval.RowFactsTx skips such a key rather than resolving it). The caller
// here is an ACTIVE ADMIN, so rungs 1 and 2 pass and the run really is open and pending:
// the only thing standing between them and Approve is that nobody holds a role that does
// not exist. Rung 5 is therefore the ONLY correct answer, and asserting the sentence --
// not merely `can_approve == false` -- is what separates it from rung 3 ("no approval
// run"), which a fail-closed-for-the-wrong-reason implementation would emit.
//
// The `approval` object is asserted alongside, so the refusal cannot be explained away by
// an unarmed fixture.
func TestListHandler_RealStore_NullRoleKeyOnThePendingStepCannotApprove(t *testing.T) {
	super, app := dbTestPools(t)

	label := "APPR-12-09-QA null-role-key"
	tenantID := seedTenant(t, super, label+" tenant")
	entityID := seedEntity(t, super, tenantID, label+" entity")
	policyID := seedApprovalPolicyFor(t, super, tenantID, label+" policy")
	versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	seedApprovalStepFor(t, super, tenantID, versionID, approvalStepSpecFor{
		Ord: 0, Kind: "approval", WorkflowRoleKey: nil,
	})
	activateApprovalPolicyVersionFor(t, super, versionID)

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	ctx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: subject, Role: "authenticated", TenantID: tenantID,
	})

	store := NewStore(app, WithApprovalsEnforced(true))
	invID := armOneInvoice(t, super, store, ctx, entityID, "appr-12-09-qa-null-key")
	if n := mustCount(t, super,
		`SELECT count(*) FROM approval_run_steps s JOIN approval_runs r ON r.id = s.run_id
		  WHERE r.invoice_id = $1 AND s.kind = 'approval' AND s.workflow_role_key IS NULL`, invID); n != 1 {
		t.Fatalf("the pending step's workflow_role_key is not NULL (%d NULL steps) -- the state under test was never reached", n)
	}

	row, ok := listPageByID(t, store, ctx)[invID]
	if !ok {
		t.Fatalf("the armed invoice %s is not on its own page", invID)
	}

	// The run IS open and pending: without this the refusal below could be rung 3's.
	var approvalObj struct {
		RunState         string  `json:"run_state"`
		PendingOrd       *int    `json:"pending_ord"`
		PendingRoleTitle *string `json:"pending_role_title"`
	}
	if err := json.Unmarshal(row["approval"], &approvalObj); err != nil {
		t.Fatalf("decode approval %q: %v", string(row["approval"]), err)
	}
	if approvalObj.RunState != "open" || approvalObj.PendingOrd == nil || *approvalObj.PendingOrd != 0 {
		t.Fatalf("approval = %s, want an open run pending at ord 0 -- the refusal below would be about the run, not the role",
			string(row["approval"]))
	}
	if approvalObj.PendingRoleTitle != nil {
		t.Errorf("pending_role_title = %q, want null -- RowFactsTx skips a NULL key rather than resolving it", *approvalObj.PendingRoleTitle)
	}

	gotCan, gotReason := approveFlagsOf(t, row, "the NULL-role-key row")
	if gotCan != "false" {
		t.Errorf("can_approve = %s, want false -- a step naming no workflow role is held by nobody", gotCan)
	}
	if want := jsonOf(t, reasonNotRoleHolder); gotReason != want {
		t.Errorf("approve_blocked_reason = %s,\nwant %s\n-- rung 5 specifically: rung 3's sentence would mean the gate never saw the open run", gotReason, want)
	}

	// The detail wire's own answer for the same invoice, byte for byte. GateFactsTx has
	// pinned this rung since APPR-08-01; the list must not have invented a second answer.
	detail := detailRowRaw(t, store, ctx, invID)
	detailCan, detailReason := approveFlagsOf(t, detail, "the NULL-role-key DETAIL response")
	if detailCan != gotCan || detailReason != gotReason {
		t.Errorf("list = (%s, %s), detail = (%s, %s) -- ONE gate feeds both wires", gotCan, gotReason, detailCan, detailReason)
	}
}

// --- one page, four rungs, both wires ----------------------------------------

// TestListAndDetail_RealStore_ApproveGateAgreesRowByRow puts four invoices at four
// DIFFERENT rungs on ONE page and compares every row against GetHandler's answer for the
// same invoice, over the real store.
//
// Two things only this shape can catch. First, the per-row derivation: ONE Store.RowFacts
// call resolves the whole page, so an implementation that resolved the gate per PAGE
// rather than per ROW -- caller role is page-wide, and it is the only page-wide input --
// would answer identically for all four. Second, the two wires' gate inputs come from
// different store methods, so their agreement is a claim about Store.RowFacts and
// Store.ApprovalFacts/Store.CallerRole agreeing, which no stub-driven spec can make.
//
// The four expected sentences are asserted verbatim AND checked distinct, so "they agree"
// can never be four copies of one answer.
func TestListAndDetail_RealStore_ApproveGateAgreesRowByRow(t *testing.T) {
	super, app := dbTestPools(t)

	// staffed=true: an active admin membership plus the finance-lead role the one-step
	// policy names, staffed with this subject. fx.invID starts as a validated invoice with
	// NO run -- kept as the rung-3 row before armInvoice would replace it.
	fx := seedApprovalFactsFixture(t, super, "APPR-12-09-QA-AGREE", true)
	bareID := fx.invID
	draftID := seedInvoiceAtStatus(t, super, fx.tenantID, fx.entityID, "APPR-12-09-QA-AGREE draft", StatusDraft)

	store := NewStore(app, WithApprovalsEnforced(true))
	armedID := armOneInvoice(t, super, store, fx.ctx, fx.entityID, "appr-12-09-qa-agree-armed")
	closedID := armOneInvoice(t, super, store, fx.ctx, fx.entityID, "appr-12-09-qa-agree-closed")

	var closedRunID string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM approval_runs WHERE invoice_id = $1 ORDER BY opened_at DESC LIMIT 1`, closedID,
	).Scan(&closedRunID); err != nil {
		t.Fatalf("read the run to close: %v", err)
	}
	closeApprovalRunFor(t, super, closedRunID, "approved", "fixture")

	want := []struct {
		label  string
		invID  string
		can    string
		reason string
	}{
		{"armed, staffed, pending -- the allowed rung", armedID, "true", "null"},
		{"validated with no run -- rung 3", bareID, "false", jsonOf(t, reasonNoRun)},
		{"draft -- rung 2", draftID, "false", jsonOf(t, reasonNotValidated)},
		{"run already approved -- rung 4", closedID, "false", jsonOf(t, reasonRunClosed)},
	}

	// The four answers must be four DIFFERENT answers, or agreement proves nothing.
	seen := map[string]string{}
	for _, w := range want {
		if prev, dup := seen[w.can+w.reason]; dup {
			t.Fatalf("the %q and %q legs expect the SAME answer -- the page no longer spans distinct rungs", prev, w.label)
		}
		seen[w.can+w.reason] = w.label
	}

	page := listPageByID(t, store, fx.ctx)
	for _, w := range want {
		row, ok := page[w.invID]
		if !ok {
			t.Fatalf("%s (%s) is missing from the page -- its leg cannot run", w.label, w.invID)
		}
		gotCan, gotReason := approveFlagsOf(t, row, w.label)
		if gotCan != w.can {
			t.Errorf("%s: can_approve = %s, want %s", w.label, gotCan, w.can)
		}
		if gotReason != w.reason {
			t.Errorf("%s: approve_blocked_reason = %s,\nwant %s", w.label, gotReason, w.reason)
		}

		detail := detailRowRaw(t, store, fx.ctx, w.invID)
		detailCan, detailReason := approveFlagsOf(t, detail, w.label+" (DETAIL)")
		if detailCan != gotCan {
			t.Errorf("%s: can_approve list = %s, detail = %s -- the two store paths must resolve the same gate", w.label, gotCan, detailCan)
		}
		if detailReason != gotReason {
			t.Errorf("%s: approve_blocked_reason\n  list   = %s\n  detail = %s", w.label, gotReason, detailReason)
		}
	}
}
