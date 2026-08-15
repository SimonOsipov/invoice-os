// APPR-12-09 (task-526): can_approve / approve_blocked_reason on the LIST wire, from the
// SAME approvalGate the detail wire calls (handlers.go).
//
// RED until the two keys ship. Every assertion here reads RAW response bytes: presence,
// absence and explicit null are three different answers, and decoding collapses the first
// two.
//
// Spec-to-test map (task-526 Test Specs table):
//
//	A09-1 TestListHandler_ApproverOnThePendingRoleCanApprove
//	A09-2 TestListHandler_ApproveRefusalLadderVerbatim
//	A09-3 TestListAndDetail_ApproveGateCannotDisagree
//	A09-4 TestListHandler_RealStore_CrossTenantCannotApprove (DB-backed)
//
// ONE line in this file changes when the implementation lands: gateStub's return, marked
// below. The per-spec inputs already carry the caller role and the held-role map.
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- the five refusal sentences, verbatim -----------------------------------
//
// Copied byte-for-byte from approvalGate's rungs (handlers.go). Em dash is U+2014 in
// rungs 1 and 5; the apostrophes in rungs 4 and 5 are ASCII. approveReasonsAreVerbatim
// below reads them back out of handlers.go, so a paraphrase here fails loudly instead of
// pinning the wrong copy.
const (
	reasonNotAnApprover = "Only an admin or a reviewer can approve or reject an invoice — ask an approver on your team."
	reasonNotValidated  = "Only a validated invoice can be approved or rejected."
	reasonNoRun         = "This invoice has no approval run to decide on."
	reasonRunClosed     = "This invoice's approval run is already closed."
	reasonNotRoleHolder = "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."
)

// --- helpers ----------------------------------------------------------------

// listGateInputs is what a spec below wants the list's gate seam to answer for one page.
//
// EXECUTOR: when Store.RowFacts widens to (map[string]approval.RowFacts, ListGateFacts,
// error) and ListHandler's rowFacts param widens with it, widen gateStub's return to
//
//	return in.facts, ListGateFacts{CallerRole: in.callerRole, HoldsPendingRole: in.holdsPendingRole}, nil
//
// and NOTHING else in this file moves. The two fields are already populated per spec; they
// have nowhere to go on today's signature, which is exactly why every spec here is RED.
type listGateInputs struct {
	facts            map[string]approval.RowFacts
	callerRole       string
	holdsPendingRole map[string]bool
}

func gateStub(in listGateInputs) func(ctx context.Context, ids []string) (map[string]approval.RowFacts, error) {
	return func(ctx context.Context, ids []string) (map[string]approval.RowFacts, error) {
		return in.facts, nil
	}
}

// openRunFacts is one row's standing with an open run pending at ord on a named role.
func openRunFacts(ord int) approval.RowFacts {
	title := "Finance Lead"
	return approval.RowFacts{RunState: "open", PendingOrd: &ord, PendingRoleTitle: &title}
}

// jsonOf renders want the way encoding/json would, so a raw-byte comparison against the
// wire needs no hand-escaping.
func jsonOf(t *testing.T, want any) string {
	t.Helper()
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal %v: %v", want, err)
	}
	return string(b)
}

// approveFlagsOf pulls one row's two keys out UNDECODED. An absent key is a Fatal, never a
// silent zero: "the server did not say" is the fail-open shape [gates-on-the-wire] exists
// to remove, and it is the exact failure this whole file is RED on today.
func approveFlagsOf(t *testing.T, row map[string]json.RawMessage, label string) (canApprove, reason string) {
	t.Helper()
	raw, ok := row["can_approve"]
	if !ok {
		t.Fatalf("%s has no \"can_approve\" key -- every list row must carry it, with no omitempty (keys present: %v)", label, sortedKeys(row))
	}
	rawReason, ok := row["approve_blocked_reason"]
	if !ok {
		t.Fatalf("%s has no \"approve_blocked_reason\" key -- every list row must carry it, with no omitempty (keys present: %v)", label, sortedKeys(row))
	}
	return string(raw), string(rawReason)
}

func sortedKeys(row map[string]json.RawMessage) []string {
	out := make([]string, 0, len(row))
	for k := range row {
		out = append(out, k)
	}
	return out
}

// --- A09-1: the allowed case reaches the wire -------------------------------

// TestListHandler_ApproverOnThePendingRoleCanApprove: an approver who holds the pending
// step's workflow role gets can_approve true and a NULL reason on the list row -- the
// same answer GetHandler already gives for the same invoice (A09-3 pins that they cannot
// diverge; this pins that the allowed rung is reachable at all).
//
// The second, unarmed row is the polarity control: without it an implementation that
// hardcoded true would pass.
func TestListHandler_ApproverOnThePendingRoleCanApprove(t *testing.T) {
	id := listIdentity()
	armed, bare := uuid.NewString(), uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{
			{ID: armed, Status: StatusValidated},
			{ID: bare, Status: StatusValidated},
		}, 2, nil
	}
	facts := gateStub(listGateInputs{
		facts:            map[string]approval.RowFacts{armed: openRunFacts(0)},
		callerRole:       "reviewer",
		holdsPendingRole: map[string]bool{armed: true},
	})

	rec := doInvoiceListWithFacts(t, list, facts, &id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := listRowsRaw(t, rec)
	if len(rows) != 2 {
		t.Fatalf("len(invoices) = %d, want 2 (body=%s)", len(rows), rec.Body.String())
	}

	gotCan, gotReason := approveFlagsOf(t, rows[0], "the armed row")
	if gotCan != "true" {
		t.Errorf("row 0 can_approve = %s, want true -- a reviewer holding the pending step's role may approve", gotCan)
	}
	if gotReason != "null" {
		t.Errorf("row 0 approve_blocked_reason = %s, want null -- an allowed gate states no reason", gotReason)
	}

	// CONTROL: the same caller, an invoice with no run. Without this leg a hardcoded
	// `true` passes the two assertions above.
	bareCan, bareReason := approveFlagsOf(t, rows[1], "the unarmed row")
	if bareCan != "false" {
		t.Errorf("row 1 can_approve = %s, want false -- an invoice with no run has nothing to decide on", bareCan)
	}
	if want := jsonOf(t, reasonNoRun); bareReason != want {
		t.Errorf("row 1 approve_blocked_reason = %s, want %s", bareReason, want)
	}
}

// --- A09-2: the refusal ladder, rung by rung --------------------------------

// TestListHandler_ApproveRefusalLadderVerbatim walks approvalGate's five rungs on the LIST
// wire, asserting each sentence VERBATIM. The copy is the backend's
// ([gates-on-the-wire]); a paraphrase on this wire is drift the SPA would render.
//
// Every case is one row on its own page, so the rung under test is the one that answers.
func TestListHandler_ApproveRefusalLadderVerbatim(t *testing.T) {
	for _, c := range []struct {
		name       string
		status     Status
		in         listGateInputs
		wantReason string
	}{
		{
			// Rung 1 also covers the zero ListGateFacts: an unresolved caller role is ""
			// and "" is not an approver, so a failed gate read denies.
			name:       "rung 1: not an approver",
			status:     StatusValidated,
			in:         listGateInputs{callerRole: "preparer"},
			wantReason: reasonNotAnApprover,
		},
		{
			name:       "rung 2: not validated",
			status:     StatusDraft,
			in:         listGateInputs{callerRole: "admin"},
			wantReason: reasonNotValidated,
		},
		{
			name:       "rung 3: no approval run",
			status:     StatusValidated,
			in:         listGateInputs{callerRole: "admin"},
			wantReason: reasonNoRun,
		},
		{
			name:       "rung 4: the run is closed",
			status:     StatusValidated,
			in:         listGateInputs{callerRole: "admin"},
			wantReason: reasonRunClosed,
		},
		{
			// The OTHER half of rung 4's conjunction: an open run with nothing pending.
			// Rung 4 is a conjunction on purpose (handlers.go), and a page can carry a
			// dead run whose later steps are still pending.
			name:       "rung 4: open run, nothing pending",
			status:     StatusValidated,
			in:         listGateInputs{callerRole: "admin"},
			wantReason: reasonRunClosed,
		},
		{
			name:       "rung 5: not staffed to the pending step's role",
			status:     StatusValidated,
			in:         listGateInputs{callerRole: "admin"},
			wantReason: reasonNotRoleHolder,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := listIdentity()
			invID := uuid.NewString()

			in := c.in
			in.facts = map[string]approval.RowFacts{}
			in.holdsPendingRole = map[string]bool{}
			switch c.name {
			case "rung 4: the run is closed":
				in.facts[invID] = approval.RowFacts{RunState: "cancelled"}
			case "rung 4: open run, nothing pending":
				in.facts[invID] = approval.RowFacts{RunState: "open"} // PendingOrd nil
			case "rung 5: not staffed to the pending step's role":
				in.facts[invID] = openRunFacts(0)
				in.holdsPendingRole[invID] = false
			case "rung 1: not an approver", "rung 2: not validated":
				// Staffed and armed, so the EARLIER rung is what answers -- a ladder that
				// checked role or status last would emit rung 5's sentence instead.
				in.facts[invID] = openRunFacts(0)
				in.holdsPendingRole[invID] = true
			}

			list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
				return []Invoice{{ID: invID, Status: c.status}}, 1, nil
			}
			rec := doInvoiceListWithFacts(t, list, gateStub(in), &id, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			rows := listRowsRaw(t, rec)
			if len(rows) != 1 {
				t.Fatalf("len(invoices) = %d, want 1", len(rows))
			}

			gotCan, gotReason := approveFlagsOf(t, rows[0], "the row")
			if gotCan != "false" {
				t.Errorf("can_approve = %s, want false on a refused rung", gotCan)
			}
			if want := jsonOf(t, c.wantReason); gotReason != want {
				t.Errorf("approve_blocked_reason = %s,\nwant %s\n-- the copy is approvalGate's (handlers.go), never re-authored on this wire", gotReason, want)
			}
		})
	}
}

// TestListHandler_ApproveReasonsAreHandlersGoOwnCopy is the vacuity control for the five
// constants above: it reads them back out of handlers.go. A constant here that drifted
// from approvalGate's literal would otherwise pin the wrong sentence on both wires and
// the ladder spec would still pass.
func TestListHandler_ApproveReasonsAreHandlersGoOwnCopy(t *testing.T) {
	raw, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("read handlers.go: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "func approvalGate(") {
		t.Fatalf("handlers.go does not declare approvalGate -- the scan below would prove nothing")
	}
	for _, reason := range []string{
		reasonNotAnApprover, reasonNotValidated, reasonNoRun, reasonRunClosed, reasonNotRoleHolder,
	} {
		if !strings.Contains(src, reason) {
			t.Errorf("handlers.go does not contain the literal %q -- this file pins a sentence approvalGate does not emit", reason)
		}
	}
}

// --- A09-3: detail and list cannot disagree ---------------------------------

// TestListAndDetail_ApproveGateCannotDisagree: the SAME invoice, the SAME caller, driven
// through BOTH handlers. Both can_approve values and both reason strings must be
// byte-identical.
//
// This is the spec that proves ONE predicate feeds TWO wires. A second copy of the ladder
// written for the list -- in Go, in SQL or in TypeScript -- passes every other spec in this
// file and fails here on the first rung whose wording or order drifted (AC-2).
func TestListAndDetail_ApproveGateCannotDisagree(t *testing.T) {
	for _, c := range []struct {
		name   string
		status Status
		role   string
		facts  approval.RowFacts
		holds  bool
	}{
		{"allowed", StatusValidated, "reviewer", openRunFacts(0), true},
		{"not an approver", StatusValidated, "preparer", openRunFacts(0), true},
		{"not validated", StatusDraft, "admin", openRunFacts(0), true},
		{"no run", StatusValidated, "admin", approval.RowFacts{}, false},
		{"run closed", StatusValidated, "admin", approval.RowFacts{RunState: "approved"}, false},
		{"not the role holder", StatusValidated, "admin", openRunFacts(1), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := listIdentity()
			invID := uuid.NewString()
			inv := Invoice{ID: invID, Status: c.status}

			// The detail wire's inputs, built from the SAME three facts the list gets.
			get := func(ctx context.Context, gotID string) (Invoice, error) { return inv, nil }
			callerRole := func(ctx context.Context) (string, error) { return c.role, nil }
			detailFacts := func(ctx context.Context, gotID string) (ApprovalFacts, error) {
				return ApprovalFacts{
					TransmitClear:   true,
					RunState:        c.facts.RunState,
					PendingStepOrd:  c.facts.PendingOrd,
					CallerHoldsRole: c.holds,
				}, nil
			}

			r := httptest.NewRequest("GET", "/v1/invoices/"+invID, nil)
			r.SetPathValue("id", invID)
			r = r.WithContext(auth.WithIdentity(r.Context(), id))
			detailRec := httptest.NewRecorder()
			GetHandler(get, callerRole, detailFacts, nil).ServeHTTP(detailRec, r)
			if detailRec.Code != http.StatusOK {
				t.Fatalf("GET status = %d, want 200 (body=%s)", detailRec.Code, detailRec.Body.String())
			}
			var detail map[string]json.RawMessage
			if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
				t.Fatalf("decode detail body %q: %v", detailRec.Body.String(), err)
			}
			detailCan, detailReason := approveFlagsOf(t, detail, "the DETAIL response")

			list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
				return []Invoice{inv}, 1, nil
			}
			listFacts := gateStub(listGateInputs{
				facts:            map[string]approval.RowFacts{invID: c.facts},
				callerRole:       c.role,
				holdsPendingRole: map[string]bool{invID: c.holds},
			})
			listRec := doInvoiceListWithFacts(t, list, listFacts, &id, "")
			if listRec.Code != http.StatusOK {
				t.Fatalf("LIST status = %d, want 200 (body=%s)", listRec.Code, listRec.Body.String())
			}
			rows := listRowsRaw(t, listRec)
			if len(rows) != 1 {
				t.Fatalf("len(invoices) = %d, want 1", len(rows))
			}
			listCan, listReason := approveFlagsOf(t, rows[0], "the LIST row")

			if listCan != detailCan {
				t.Errorf("can_approve: list = %s, detail = %s -- ONE gate feeds both wires, so they cannot differ", listCan, detailCan)
			}
			if listReason != detailReason {
				t.Errorf("approve_blocked_reason:\n  list   = %s\n  detail = %s\n-- the same approvalGate call must produce both", listReason, detailReason)
			}
			// The both-sides-empty vacuity guard: a pair of absent keys would have
			// Fataled in approveFlagsOf, but a pair of nulls would not.
			if c.name == "allowed" && listCan != "true" {
				t.Errorf("can_approve on the allowed case = %s, want true -- an agreement of two falses proves nothing", listCan)
			}
			if c.name != "allowed" && listReason == "null" {
				t.Errorf("approve_blocked_reason on the %q case = null, want a sentence -- agreement must not be two nulls", c.name)
			}
		})
	}
}

// --- A09-4: cross-tenant refusal on the widened read ------------------------

// TestListHandler_RealStore_CrossTenantCannotApprove drives the REAL Store.List and
// Store.RowFacts through the REAL ListHandler -- the same method-value wiring
// cmd/invoice/main.go uses -- and asks whether a caller can approve across a tenant
// boundary.
//
// Defence in depth, not a new-table RLS obligation: the widened read reaches
// workflow_roles/workflow_role_members/memberships, all three of which this same request
// already reads through RowFactsTx. What it pins is that the caller-role and held-role
// resolution are BOTH scoped by RLS: both tenants staff a role under the IDENTICAL key
// "finance-lead" (seedOneStepActivePolicyTenant), so a read that lost its tenant scope
// would answer "holds" for tenant A's staffing while serving tenant B's page.
//
// The three legs matter together:
//
//	CONTROL   -- tenant A's own caller CAN approve tenant A's armed invoice (an all-false
//	             implementation would pass the other two legs for free).
//	ABSENT    -- tenant A's invoice never appears on tenant B's page at all.
//	REFUSED   -- A's subject, admitted to B as an admin but staffed to NO role there, is
//	             refused on rung 5 for B's own armed invoice. Rung 5 specifically: an
//	             earlier rung answering would mean the held-role read was never reached.
func TestListHandler_RealStore_CrossTenantCannotApprove(t *testing.T) {
	super, app := dbTestPools(t)

	fxA := seedApprovalFactsFixture(t, super, "APPR-12-09-XT-A", true)
	bareA := fxA.invID // validated, never armed: the fixture's own second row
	fxA.armInvoice(t, super, app, "appr-12-09-xt-a")

	fxB := seedApprovalFactsFixture(t, super, "APPR-12-09-XT-B", true)
	fxB.armInvoice(t, super, app, "appr-12-09-xt-b")

	// A's subject joins B as an admin but is staffed to NO workflow role there, so rung 1
	// passes and rung 5 is the one under test.
	seedMembership(t, super, fxB.tenantID, fxA.subject, "admin")
	crossCtx := auth.WithIdentity(context.Background(), auth.Identity{
		Subject: fxA.subject, Role: "authenticated", TenantID: fxB.tenantID,
	})

	store := NewStore(app, WithApprovalsEnforced(true))
	page := func(t *testing.T, ctx context.Context) (*httptest.ResponseRecorder, map[string]map[string]json.RawMessage) {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/invoices?limit=200", nil)
		r = r.WithContext(ctx)
		rec := httptest.NewRecorder()
		ListHandler(store.List, store.RowFacts, nil).ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		byID := map[string]map[string]json.RawMessage{}
		for _, row := range listRowsRaw(t, rec) {
			var invID string
			if err := json.Unmarshal(row["id"], &invID); err != nil {
				t.Fatalf("decode row id: %v", err)
			}
			byID[invID] = row
		}
		return rec, byID
	}

	// CONTROL.
	_, own := page(t, fxA.ctx)
	armedRow, ok := own[fxA.invID]
	if !ok {
		t.Fatalf("tenant A's own armed invoice %s is not on its own page -- the control cannot run", fxA.invID)
	}
	ownCan, ownReason := approveFlagsOf(t, armedRow, "tenant A's own armed row")
	if ownCan != "true" {
		t.Errorf("tenant A's own armed row can_approve = %s, want true -- a staffed active admin on an open, pending step may approve", ownCan)
	}
	if ownReason != "null" {
		t.Errorf("tenant A's own armed row approve_blocked_reason = %s, want null", ownReason)
	}
	// Same page, same caller, an invoice with no run: the derivation is PER ROW.
	if bareRow, ok := own[bareA]; ok {
		bareCan, bareReason := approveFlagsOf(t, bareRow, "tenant A's unarmed row")
		if bareCan != "false" {
			t.Errorf("tenant A's unarmed row can_approve = %s, want false", bareCan)
		}
		if want := jsonOf(t, reasonNoRun); bareReason != want {
			t.Errorf("tenant A's unarmed row approve_blocked_reason = %s, want %s", bareReason, want)
		}
	} else {
		t.Errorf("tenant A's unarmed invoice %s is missing from its own page -- the per-row leg cannot run", bareA)
	}

	// ABSENT + REFUSED.
	rec, foreign := page(t, crossCtx)
	if _, ok := foreign[fxA.invID]; ok {
		t.Errorf("tenant A's armed invoice %s is on tenant B's page -- RLS must hide it entirely", fxA.invID)
	}
	if strings.Contains(rec.Body.String(), fxA.invID) {
		t.Errorf("tenant B's body mentions tenant A's invoice id %s", fxA.invID)
	}
	crossRow, ok := foreign[fxB.invID]
	if !ok {
		t.Fatalf("tenant B's own armed invoice %s is not on its page -- without it the refusal below could be an empty page (body=%s)", fxB.invID, rec.Body.String())
	}
	crossCan, crossReason := approveFlagsOf(t, crossRow, "tenant B's armed row read by tenant A's subject")
	if crossCan != "false" {
		t.Errorf("can_approve = %s, want false -- staffing in tenant A must never make a holder of tenant B's identically-keyed role", crossCan)
	}
	if want := jsonOf(t, reasonNotRoleHolder); crossReason != want {
		t.Errorf("approve_blocked_reason = %s,\nwant %s\n-- rung 5 specifically: an earlier rung means the held-role read never ran, so this spec would prove nothing", crossReason, want)
	}
}
