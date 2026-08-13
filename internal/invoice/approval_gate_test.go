// APPR-08-06: approvalGate -- the detail page's approve/reject availability -- and its
// four wire keys. Behavioural specs pin the five-rung ladder; structural guards pin key
// presence, wire order and occurrence count.
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// The five refusal sentences, restated as test-local literals so a silent edit to
// handlers.go cannot rewrite its own oracle (the wantAwaitingApprovalReason
// precedent, submit_gate_approval_test.go). Em dashes are U+2014.
const (
	wantApproveRoleReason      = "Only an admin or a reviewer can approve or reject an invoice — ask an approver on your team."
	wantApproveStatusReason    = "Only a validated invoice can be approved or rejected."
	wantApproveNoRunReason     = "This invoice has no approval run to decide on."
	wantApproveClosedRunReason = "This invoice's approval run is already closed."
	wantApproveNotHolderReason = "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."
)

// ordPtr is the pending-step-ordinal pointer idiom. Not named ptr: that name is
// taken by payload_engine_test.go's *string helper.
func ordPtr(n int) *int { return &n }

// liveRunFacts is the ONLY fixture that clears every rung -- an open run with a
// pending step, held by the caller. Every refusal fixture below is this one with a
// single field spoiled, so each spec names exactly the rung it exercises.
func liveRunFacts() ApprovalFacts {
	return ApprovalFacts{TransmitClear: false, RunState: "open", PendingStepOrd: ordPtr(0), CallerHoldsRole: true}
}

// assertApprovalGate pins BOTH halves of one gate answer. wantReason "" means the
// gate must return a nil pointer -- never merely "not the sentence I named".
func assertApprovalGate(t *testing.T, gotCan bool, gotReason *string, wantCan bool, wantReason string) {
	t.Helper()
	if gotCan != wantCan {
		t.Errorf("can = %v, want %v", gotCan, wantCan)
	}
	switch {
	case wantReason == "" && gotReason != nil:
		t.Errorf("reason = %q, want an explicit nil -- an allowed gate names no refusal", *gotReason)
	case wantReason != "" && gotReason == nil:
		t.Errorf("reason = nil, want %q", wantReason)
	case wantReason != "" && *gotReason != wantReason:
		t.Errorf("reason = %q, want %q", *gotReason, wantReason)
	}
}

// --- AC-4: the five rungs, in decideTx's own order ---------------------------

// TestApprovalGate_LadderOrderMatchesDecide: role is rung 1, ahead of status. A
// preparer on a draft invoice whose run they hold reads the ROLE sentence, because
// decideTx runs requireApprover (AXIS 1) before it reads the row at all -- a
// status-first gate would name a refusal the endpoint never reaches.
func TestApprovalGate_LadderOrderMatchesDecide(t *testing.T) {
	can, reason := approvalGate(StatusDraft, "preparer", liveRunFacts())
	assertApprovalGate(t, can, reason, false, wantApproveRoleReason)
}

// TestApprovalGate_EveryNonApproverRoleIsRefused: "" is the value callerRole falls
// back to on an error, so it must refuse like any other non-approver.
func TestApprovalGate_EveryNonApproverRoleIsRefused(t *testing.T) {
	for _, role := range []string{"preparer", "", "authenticated", "owner"} {
		t.Run("role_"+role, func(t *testing.T) {
			can, reason := approvalGate(StatusValidated, role, liveRunFacts())
			assertApprovalGate(t, can, reason, false, wantApproveRoleReason)
		})
	}
}

// TestApprovalGate_NotValidated: rung 2 is a LITERAL status compare, mirroring
// decideTx's own `status != "validated"` -- not a legalTransitions lookup. Every
// status but validated refuses, whatever the run says.
func TestApprovalGate_NotValidated(t *testing.T) {
	if len(allStatuses) != 7 {
		t.Fatalf("allStatuses covers %d statuses, want 7 -- a new status needs a row here", len(allStatuses))
	}
	for _, s := range allStatuses {
		if s == StatusValidated {
			continue
		}
		t.Run(string(s), func(t *testing.T) {
			can, reason := approvalGate(s, "admin", liveRunFacts())
			assertApprovalGate(t, can, reason, false, wantApproveStatusReason)
		})
	}
}

// TestApprovalGate_NoRun: rung 3. The zero ApprovalFacts is also what GetHandler
// substitutes on a seam error, so this is the fail-closed answer too.
func TestApprovalGate_NoRun(t *testing.T) {
	t.Run("zero_facts", func(t *testing.T) {
		can, reason := approvalGate(StatusValidated, "admin", ApprovalFacts{})
		assertApprovalGate(t, can, reason, false, wantApproveNoRunReason)
	})
	// Rung 3 precedes rung 5: a role-holding caller with no run still reads the
	// no-run sentence, never the staffed-to-the-role one.
	t.Run("holds_role_but_no_run", func(t *testing.T) {
		can, reason := approvalGate(StatusValidated, "admin", ApprovalFacts{TransmitClear: true, CallerHoldsRole: true})
		assertApprovalGate(t, can, reason, false, wantApproveNoRunReason)
	})
}

// TestApprovalGate_ClosedRun: rung 4a on the NAIVE closed run -- a single-step run
// whose only step is settled, so no pending step survives it. The adversarial shape
// is TestApprovalGate_DeadRunWithALaterPendingStep below.
func TestApprovalGate_ClosedRun(t *testing.T) {
	for _, state := range []string{"approved", "rejected", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			can, reason := approvalGate(StatusValidated, "admin", ApprovalFacts{RunState: state})
			assertApprovalGate(t, can, reason, false, wantApproveClosedRunReason)
		})
	}
}

// TestApprovalGate_DeadRunWithALaterPendingStep is the rung-4a mutant killer.
// CancelLiveRunTx writes approval_runs ONLY, and a reject settles just the CURRENT
// step, so a dead run legitimately reports a pending LATER step AND a role-holding
// caller -- pinned upstream by internal/approval's TestGateFactsTx_MirrorsDecideTxRefusalLadder
// ("cancelled", ord 0, holds true, ErrRunClosed). Drop `RunState != "open"` from
// rung 4 and this fixture walks straight to allowed, rendering an enabled Approve
// button on a dead run. TestApprovalGate_ClosedRun above does NOT catch that.
func TestApprovalGate_DeadRunWithALaterPendingStep(t *testing.T) {
	for _, state := range []string{"cancelled", "rejected"} {
		t.Run(state, func(t *testing.T) {
			f := liveRunFacts()
			f.RunState = state
			can, reason := approvalGate(StatusValidated, "admin", f)
			assertApprovalGate(t, can, reason, false, wantApproveClosedRunReason)
		})
	}
}

// TestApprovalGate_RejectedRunDemotedToDraft is the rung-2 mutant killer. Reject's
// demoter walks the invoice back to draft while the run stays 'rejected' with its
// later steps pending, so this shape is reachable in production. Both rung 2 and
// rung 4 would refuse it -- the oracle is WHICH sentence, so dropping rung 2 fails
// here on the message even though `can` stays false.
func TestApprovalGate_RejectedRunDemotedToDraft(t *testing.T) {
	f := liveRunFacts()
	f.RunState = "rejected"
	can, reason := approvalGate(StatusDraft, "admin", f)
	assertApprovalGate(t, can, reason, false, wantApproveStatusReason)
}

// TestApprovalGate_OpenRunWithNoPendingStep: rung 4b. decideTx maps it to
// ErrRunClosed, the same sentinel as 4a, so it shares 4a's sentence.
func TestApprovalGate_OpenRunWithNoPendingStep(t *testing.T) {
	f := liveRunFacts()
	f.PendingStepOrd = nil
	can, reason := approvalGate(StatusValidated, "admin", f)
	assertApprovalGate(t, can, reason, false, wantApproveClosedRunReason)
}

// TestApprovalGate_NotRoleHolder: rung 5, the last one.
func TestApprovalGate_NotRoleHolder(t *testing.T) {
	f := liveRunFacts()
	f.CallerHoldsRole = false
	can, reason := approvalGate(StatusValidated, "admin", f)
	assertApprovalGate(t, can, reason, false, wantApproveNotHolderReason)
}

// TestApprovalGate_AllowedWhenEveryRungPasses is the permissive control: a gate that
// always refused would pass every spec above for free. ord 0 is a REAL first step,
// so the pending-step rung must read the pointer's presence, not its value.
func TestApprovalGate_AllowedWhenEveryRungPasses(t *testing.T) {
	for _, role := range []string{"admin", "reviewer"} {
		for _, ord := range []int{0, 3} {
			t.Run(role+"_ord"+strconv.Itoa(ord), func(t *testing.T) {
				f := liveRunFacts()
				f.PendingStepOrd = ordPtr(ord)
				can, reason := approvalGate(StatusValidated, role, f)
				assertApprovalGate(t, can, reason, true, "")
			})
		}
	}
}

// TestApprovalGate_IgnoresTransmitClear (AC #5): APPROVALS_ENFORCED reaches
// ApprovalFacts through TransmitClear and nowhere else (store.go), so a gate that
// consulted it would silently flag-gate an UNFLAGGED endpoint. Same fixture, both
// values, identical answer -- and allowed in both, so a gate stuck on false cannot
// pass this for free.
func TestApprovalGate_IgnoresTransmitClear(t *testing.T) {
	f := liveRunFacts()
	f.TransmitClear = false
	canOff, reasonOff := approvalGate(StatusValidated, "admin", f)
	f.TransmitClear = true
	canOn, reasonOn := approvalGate(StatusValidated, "admin", f)

	assertApprovalGate(t, canOff, reasonOff, true, "")
	assertApprovalGate(t, canOn, reasonOn, true, "")
	if canOff != canOn {
		t.Errorf("can = %v with TransmitClear false and %v with it true, want identical -- the four flags are not flag-gated", canOff, canOn)
	}
	if !sameReason(reasonOff, reasonOn) {
		t.Errorf("reason differs across TransmitClear: %v vs %v, want identical", derefReason(reasonOff), derefReason(reasonOn))
	}
}

// --- AC-3: one gate call, two pairs, on the wire ------------------------------

// approveGateBody is submitGateBody's idiom for APPR-08-06's four keys.
type approveGateBody struct {
	CanApprove           bool    `json:"can_approve"`
	ApproveBlockedReason *string `json:"approve_blocked_reason"`
	CanReject            bool    `json:"can_reject"`
	RejectBlockedReason  *string `json:"reject_blocked_reason"`
}

func decodeApproveBody(t *testing.T, rec *httptest.ResponseRecorder) approveGateBody {
	t.Helper()
	var out approveGateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

func sameReason(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func derefReason(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// factsStub is fixedApprovalStub's idiom for a hand-built ApprovalFacts.
func factsStub(f ApprovalFacts) func(ctx context.Context, id string) (ApprovalFacts, error) {
	return func(ctx context.Context, id string) (ApprovalFacts, error) { return f, nil }
}

// approvalLadderRow is the ladder restated at the WIRE, one row per rung plus the
// allowed one -- the fixture table TestApprovalGate_ApproveAndRejectNeverDiverge and
// TestGetHandler_ApproveReasonsExplicitNullWhenAllowed both read.
var approvalLadderRows = []struct {
	name       string
	status     Status
	role       string
	facts      ApprovalFacts
	wantCan    bool
	wantReason string
}{
	{"role_rung", StatusValidated, "preparer", ApprovalFacts{RunState: "open", PendingStepOrd: ordPtr(0), CallerHoldsRole: true}, false, wantApproveRoleReason},
	{"status_rung", StatusQueued, "admin", ApprovalFacts{RunState: "open", PendingStepOrd: ordPtr(0), CallerHoldsRole: true}, false, wantApproveStatusReason},
	{"no_run_rung", StatusValidated, "admin", ApprovalFacts{TransmitClear: true}, false, wantApproveNoRunReason},
	{"closed_run_rung", StatusValidated, "admin", ApprovalFacts{RunState: "approved"}, false, wantApproveClosedRunReason},
	{"dead_run_later_pending_step", StatusValidated, "reviewer", ApprovalFacts{RunState: "cancelled", PendingStepOrd: ordPtr(0), CallerHoldsRole: true}, false, wantApproveClosedRunReason},
	{"open_run_no_pending_step", StatusValidated, "admin", ApprovalFacts{RunState: "open", CallerHoldsRole: true}, false, wantApproveClosedRunReason},
	{"not_role_holder", StatusValidated, "reviewer", ApprovalFacts{RunState: "open", PendingStepOrd: ordPtr(0)}, false, wantApproveNotHolderReason},
	{"every_rung_passes", StatusValidated, "admin", ApprovalFacts{RunState: "open", PendingStepOrd: ordPtr(0), CallerHoldsRole: true}, true, ""},
}

// TestApprovalGate_ApproveAndRejectNeverDiverge (AC #3): both pairs carry the
// ladder's answer, and carry the SAME one. Two gate calls, or a reject arm that
// grew a rung of its own, would show up here -- reject's extra rule is
// DecideHandler's body check, never an availability rung.
func TestApprovalGate_ApproveAndRejectNeverDiverge(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	for _, tt := range approvalLadderRows {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := doInvoiceGetGated(t, invoiceAtStatusStub(tt.status), fixedRoleStub(tt.role, nil), factsStub(tt.facts), &id, uuid.NewString())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			got := decodeApproveBody(t, rec)
			t.Run("approve", func(t *testing.T) {
				assertApprovalGate(t, got.CanApprove, got.ApproveBlockedReason, tt.wantCan, tt.wantReason)
			})
			t.Run("reject", func(t *testing.T) {
				assertApprovalGate(t, got.CanReject, got.RejectBlockedReason, tt.wantCan, tt.wantReason)
			})
			if got.CanApprove != got.CanReject {
				t.Errorf("can_approve = %v but can_reject = %v, want identical -- ONE approvalGate call feeds both pairs", got.CanApprove, got.CanReject)
			}
			if !sameReason(got.ApproveBlockedReason, got.RejectBlockedReason) {
				t.Errorf("approve_blocked_reason = %q but reject_blocked_reason = %q, want identical", derefReason(got.ApproveBlockedReason), derefReason(got.RejectBlockedReason))
			}
		})
	}
}

// TestGetHandler_ApproveReasonsExplicitNullWhenAllowed is AC #6's unreachable half.
// doInvoiceGet hardwires clearApprovalStub, whose RunState is "", so the run-absent
// rung fires on every one of its callers and no reason is ever null there; only a
// gate-PASSING fixture through doInvoiceGetGated reaches the null.
func TestGetHandler_ApproveReasonsExplicitNullWhenAllowed(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	rec, _ := doInvoiceGetGated(t, invoiceAtStatusStub(StatusValidated), fixedRoleStub("admin", nil), factsStub(liveRunFacts()), &id, uuid.NewString())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"can_approve":true`, `"approve_blocked_reason":null`, `"can_reject":true`, `"reject_blocked_reason":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s, want the literal %s -- an allowed gate renders explicit null, never an omitted key and never a string", body, want)
		}
	}
}

// TestGetHandler_ApproveFlagsTrackTheInjectedFacts is AC #7's derivation oracle.
// TestGetHandler_ActionFlagsAreDerivedNotHardcoded's lever is legalTransitions,
// which approvalGate never reads (rung 2 is a literal, mirroring decideTx's own),
// so it cannot reach these flags. This one holds status AND role fixed and varies
// ONLY the seam's facts: a hardcoded answer cannot follow.
func TestGetHandler_ApproveFlagsTrackTheInjectedFacts(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()

	flagsFor := func(t *testing.T, f ApprovalFacts) approveGateBody {
		t.Helper()
		rec, _ := doInvoiceGetGated(t, invoiceAtStatusStub(StatusValidated), fixedRoleStub("admin", nil), factsStub(f), &id, invoiceID)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		return decodeApproveBody(t, rec)
	}

	live := flagsFor(t, liveRunFacts())
	if !live.CanApprove {
		t.Error("can_approve = false on an open run with a pending step held by the caller, want true")
	}
	if live.ApproveBlockedReason != nil {
		t.Errorf("approve_blocked_reason = %q on an allowed gate, want null", *live.ApproveBlockedReason)
	}

	dead := liveRunFacts()
	dead.RunState = "cancelled"
	closed := flagsFor(t, dead)
	if closed.CanApprove {
		t.Error("can_approve = true on a cancelled run, want false -- the flag must follow the injected facts")
	}
	switch {
	case closed.ApproveBlockedReason == nil:
		t.Errorf("approve_blocked_reason = null on a cancelled run, want %q", wantApproveClosedRunReason)
	case *closed.ApproveBlockedReason != wantApproveClosedRunReason:
		t.Errorf("approve_blocked_reason = %q, want %q", *closed.ApproveBlockedReason, wantApproveClosedRunReason)
	}
}

// --- AC-2: the wire shape ------------------------------------------------------

// TestGetHandler_ApproveFlagsAreLastInWireOrder (AC #2/#4): writeJSON marshals with
// json.NewEncoder, so declaration order IS wire order -- appending the four LAST is
// what keeps every pre-existing key's position untouched.
func TestGetHandler_ApproveFlagsAreLastInWireOrder(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	rec, _ := doInvoiceGetGated(t, invoiceAtStatusStub(StatusValidated), fixedRoleStub("admin", nil), factsStub(liveRunFacts()), &id, uuid.NewString())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := topLevelKeyOrder(t, rec.Body.Bytes())
	want := []string{"can_approve", "approve_blocked_reason", "can_reject", "reject_blocked_reason"}
	if len(got) < len(want) {
		t.Fatalf("top-level key order = %v, want at least %d keys", got, len(want))
	}
	if tail := got[len(got)-len(want):]; !reflect.DeepEqual(tail, want) {
		t.Errorf("last %d wire keys = %v, want %v (full order = %v)", len(want), tail, want, got)
	}
}

// TestGetHandler_ApproveKeysAppearExactlyOnce: the embed-boundary guard, the
// TestGetHandler_CanSubmitKeysStillExactlyOnce precedent. encoding/json's
// ambiguous-field rule silently DROPS both entries of a same-depth duplicate tag, so
// a name collision with Invoice would delete the key rather than repeat it. Run on
// both role arms, since the key set must not vary by role.
func TestGetHandler_ApproveKeysAppearExactlyOnce(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	for _, role := range []string{"admin", "preparer"} {
		t.Run(role, func(t *testing.T) {
			rec, _ := doInvoiceGetGated(t, invoiceAtStatusStub(StatusValidated), fixedRoleStub(role, nil), factsStub(liveRunFacts()), &id, uuid.NewString())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, k := range []string{`"can_approve":`, `"approve_blocked_reason":`, `"can_reject":`, `"reject_blocked_reason":`} {
				if got := strings.Count(body, k); got != 1 {
					t.Errorf("body has %d occurrences of %s, want exactly 1 (body=%s)", got, k, body)
				}
			}
		})
	}
}

// --- DB-backed: the real Store.ApprovalFacts behind the read side --------------

// TestGetHandler_ApproveFlagsIgnoreTheEnforcementFlag (AC #5) end to end. The store
// half is already pinned by TestStoreApprovalFacts_ReadsRunFactsEvenWithTheFlagOff;
// this is the HANDLER half -- the same armed invoice, read through the REAL
// Store.ApprovalFacts under both flag states, must produce byte-identical approve
// flags. The armed fixture clears every rung, so both arms read true: a gate stuck
// on false cannot pass this by agreeing with itself.
func TestGetHandler_ApproveFlagsIgnoreTheEnforcementFlag(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-06-FLAGPARITY", true)
	fx.armInvoice(t, super, app, "appr-08-06-flagparity")

	flagsFor := func(t *testing.T, enforced bool) (approveGateBody, string) {
		t.Helper()
		store := NewStore(app, WithApprovalsEnforced(enforced))
		r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+fx.invID, nil)
		r.SetPathValue("id", fx.invID)
		r = r.WithContext(fx.ctx)
		rec := httptest.NewRecorder()
		GetHandler(store.Get, store.CallerRole, store.ApprovalFacts, nil).ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("enforced=%v: status = %d, want 200 (body=%s)", enforced, rec.Code, rec.Body.String())
		}
		return decodeApproveBody(t, rec), rec.Body.String()
	}

	off, offBody := flagsFor(t, false)
	on, onBody := flagsFor(t, true)

	if !off.CanApprove {
		t.Errorf("APPROVALS_ENFORCED off: can_approve = false, want true -- the caller is a staffed admin on a validated invoice with an open run (body=%s)", offBody)
	}
	if !on.CanApprove {
		t.Errorf("APPROVALS_ENFORCED on: can_approve = false, want true (body=%s)", onBody)
	}
	if off.CanApprove != on.CanApprove || off.CanReject != on.CanReject {
		t.Errorf("flags differ across APPROVALS_ENFORCED: off=%+v on=%+v, want identical -- the decision endpoint is unflagged", off, on)
	}
	if !sameReason(off.ApproveBlockedReason, on.ApproveBlockedReason) || !sameReason(off.RejectBlockedReason, on.RejectBlockedReason) {
		t.Errorf("reasons differ across APPROVALS_ENFORCED: off=%q/%q on=%q/%q, want identical",
			derefReason(off.ApproveBlockedReason), derefReason(off.RejectBlockedReason),
			derefReason(on.ApproveBlockedReason), derefReason(on.RejectBlockedReason))
	}
	// can_submit is the control: it IS flag-folded, so a handler that had simply
	// stopped reading the flag would show up here rather than pass silently.
	if !strings.Contains(offBody, `"can_submit":true`) {
		t.Errorf("APPROVALS_ENFORCED off: want the literal \"can_submit\":true (body=%s)", offBody)
	}
	if !strings.Contains(onBody, `"can_submit":false`) {
		t.Errorf("APPROVALS_ENFORCED on: want the literal \"can_submit\":false on an open run (body=%s)", onBody)
	}
}
