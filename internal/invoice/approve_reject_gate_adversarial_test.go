// APPR-08-06 QA (Stage 4, Mode B): adversarial coverage on top of the Mode A specs.
// Those sample the ladder one rung at a time; these take the FULL cross product, drive
// the four keys through the real store on a real armed invoice, and pin the claim the
// wire actually makes -- that can_approve agrees with what POST
// /v1/invoices/{id}/approvals would answer.
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- The full oracle ----------------------------------------------------------

// decideRung restates ONE of decideTx's six refusal branches. Written as six rows
// against approvalGate's five statements on purpose: 4a and 4b share a sentinel and
// therefore a sentence, but they are separate branches upstream, so an oracle that
// merged them could not tell a collapsed rung from a faithful one.
type decideRung struct {
	name   string
	fails  func(s Status, role string, f ApprovalFacts) bool
	reason string
}

// decideLadder is transcribed from decideTx's sentinel table (internal/approval/
// decision.go), NOT from approvalGate -- an oracle copied off the code under test
// proves only that the code equals itself. isApprover is restated as its literal
// membership set for the same reason.
var decideLadder = []decideRung{
	{"axis1_role", func(s Status, role string, f ApprovalFacts) bool {
		return role != "admin" && role != "reviewer"
	}, wantApproveRoleReason},
	{"status", func(s Status, role string, f ApprovalFacts) bool {
		return s != StatusValidated
	}, wantApproveStatusReason},
	{"run_absent", func(s Status, role string, f ApprovalFacts) bool {
		return f.RunState == ""
	}, wantApproveNoRunReason},
	{"run_not_open", func(s Status, role string, f ApprovalFacts) bool {
		return f.RunState != "open"
	}, wantApproveClosedRunReason},
	{"no_pending_step", func(s Status, role string, f ApprovalFacts) bool {
		return f.PendingStepOrd == nil
	}, wantApproveClosedRunReason},
	{"not_role_holder", func(s Status, role string, f ApprovalFacts) bool {
		return !f.CallerHoldsRole
	}, wantApproveNotHolderReason},
}

// expectApproval walks decideLadder and answers with the FIRST rung that refuses.
func expectApproval(s Status, role string, f ApprovalFacts) (bool, string, string) {
	for _, r := range decideLadder {
		if r.fails(s, role, f) {
			return false, r.reason, r.name
		}
	}
	return true, "", "allowed"
}

// approvalRunStates is every value approval_runs.state can hold, plus "" for the
// no-run answer ApprovalFacts reports when the read finds nothing -- which is also
// GetHandler's fail-closed substitute on a seam error.
var approvalRunStates = []string{"", "open", "approved", "rejected", "cancelled"}

// TestApprovalGate_FullOracle is the exhaustive twin of the Mode A rung specs: every
// status x every run state x pending-step present/absent x role-holder true/false x
// four roles, 560 rows, each checked against decideLadder rather than against a
// sampled fixture. A mutant that survives one hand-picked shape (rung 4's two halves
// are the known pair) cannot survive the cross product.
func TestApprovalGate_FullOracle(t *testing.T) {
	if len(allStatuses) != 7 {
		t.Fatalf("allStatuses covers %d statuses, want 7 -- a new status needs no edit here, but the count guard must be retuned deliberately", len(allStatuses))
	}
	rows, allowed := 0, 0
	for _, s := range allStatuses {
		for _, state := range approvalRunStates {
			for _, ord := range []*int{nil, ordPtr(0), ordPtr(4)} {
				for _, holds := range []bool{true, false} {
					for _, role := range []string{"admin", "reviewer", "preparer", ""} {
						f := ApprovalFacts{RunState: state, PendingStepOrd: ord, CallerHoldsRole: holds}
						wantCan, wantReason, rung := expectApproval(s, role, f)
						name := fmt.Sprintf("%s_%s_ord%v_holds%v_%s", s, orNone(state), ordName(ord), holds, orNone(role))
						t.Run(name, func(t *testing.T) {
							can, reason := approvalGate(s, role, f)
							assertApprovalGate(t, can, reason, wantCan, wantReason)
							if can != wantCan {
								t.Errorf("first refusing rung should be %q", rung)
							}
						})
						rows++
						if wantCan {
							allowed++
						}
					}
				}
			}
		}
	}
	// A table whose expectations are all-false would pass under a gate hardwired to
	// refuse, so pin that both answers are actually exercised.
	if allowed == 0 || allowed == rows {
		t.Fatalf("oracle produced %d allowed of %d rows -- a table with only one answer is not an oracle", allowed, rows)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func ordName(p *int) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}

// TestApprovalGate_TransmitClearIsInertAcrossTheWholeOracle widens
// TestApprovalGate_IgnoresTransmitClear (AC #5) from one allowed fixture to the whole
// cross product: the flag reaches ApprovalFacts through TransmitClear alone, so
// flipping it must not move any of the 560 answers -- not just the passing one.
func TestApprovalGate_TransmitClearIsInertAcrossTheWholeOracle(t *testing.T) {
	for _, s := range allStatuses {
		for _, state := range approvalRunStates {
			for _, ord := range []*int{nil, ordPtr(0)} {
				for _, holds := range []bool{true, false} {
					for _, role := range []string{"admin", "reviewer", "preparer", ""} {
						off := ApprovalFacts{TransmitClear: false, RunState: state, PendingStepOrd: ord, CallerHoldsRole: holds}
						on := off
						on.TransmitClear = true
						canOff, reasonOff := approvalGate(s, role, off)
						canOn, reasonOn := approvalGate(s, role, on)
						if canOff != canOn || !sameReason(reasonOff, reasonOn) {
							t.Errorf("%s/%s/ord=%s/holds=%v/%s: flag off -> (%v,%q), on -> (%v,%q), want identical",
								s, orNone(state), ordName(ord), holds, orNone(role),
								canOff, derefReason(reasonOff), canOn, derefReason(reasonOn))
						}
					}
				}
			}
		}
	}
}

// --- The seam failing ---------------------------------------------------------

// TestGetHandler_ApproveFlagsFailClosedOnASeamError: an ApprovalFacts read that errors
// must leave all FOUR keys refusing WITH a reason, on a 200. The zero ApprovalFacts
// GetHandler substitutes has RunState "", so the honest answer is the no-run sentence
// -- never a null reason beside a false flag, which would render a disabled button the
// SPA has no copy for, and never a 5xx over a read the page does not depend on.
func TestGetHandler_ApproveFlagsFailClosedOnASeamError(t *testing.T) {
	seamErrs := map[string]error{
		"generic":      errors.New("approval facts: boom"),
		"no_tenant":    db.ErrNoTenant,
		"ctx_canceled": context.Canceled,
	}
	for name, seamErr := range seamErrs {
		for _, s := range allStatuses {
			t.Run(name+"_"+string(s), func(t *testing.T) {
				id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
				facts := func(ctx context.Context, gotID string) (ApprovalFacts, error) {
					// A seam that errors AND returns a permissive value is the trap:
					// GetHandler must discard the value, not merge it.
					return liveRunFacts(), seamErr
				}
				rec, _ := doInvoiceGetGated(t, invoiceAtStatusStub(s), fixedRoleStub("admin", nil), facts, &id, uuid.NewString())
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 -- a failed approval read must not fail the page (body=%s)", rec.Code, rec.Body.String())
				}
				got := decodeApproveBody(t, rec)
				want := wantApproveNoRunReason
				if s != StatusValidated {
					want = wantApproveStatusReason
				}
				assertApprovalGate(t, got.CanApprove, got.ApproveBlockedReason, false, want)
				assertApprovalGate(t, got.CanReject, got.RejectBlockedReason, false, want)
			})
		}
	}
}

// --- DB-backed: the real store behind the four keys ---------------------------

// approveFlagsVia runs the REAL Store through the REAL GetHandler and returns the four
// keys off the serialized body -- no stub anywhere between the wire and Postgres.
func approveFlagsVia(t *testing.T, app *pgxpool.Pool, fx approvalFactsFixture, enforced bool) approveGateBody {
	t.Helper()
	store := NewStore(app, WithApprovalsEnforced(enforced))
	r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+fx.invID, nil)
	r.SetPathValue("id", fx.invID)
	r = r.WithContext(fx.ctx)
	rec := httptest.NewRecorder()
	GetHandler(store.Get, store.CallerRole, store.ApprovalFacts, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var out approveGateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

// sentenceForSentinel pairs each of decideTx's five sentinels with the sentence
// approvalGate emits for the same refusal. It is the agreement contract itself: a
// wire that refuses for a reason the endpoint would not give is exactly the drift
// [gates-on-the-wire] exists to prevent.
var sentenceForSentinel = []struct {
	err      error
	sentence string
}{
	{approval.ErrNotPermitted, wantApproveRoleReason},
	{approval.ErrNotAwaitingApproval, wantApproveStatusReason},
	{approval.ErrRunNotFound, wantApproveNoRunReason},
	{approval.ErrRunClosed, wantApproveClosedRunReason},
	{approval.ErrNotRoleHolder, wantApproveNotHolderReason},
}

// assertDecideAgrees is the oracle: the wire's answer must predict the endpoint's.
func assertDecideAgrees(t *testing.T, got approveGateBody, decideErr error) {
	t.Helper()
	if got.CanApprove {
		if decideErr != nil {
			t.Errorf("can_approve = true but Decide answered %v -- the wire offered a button the endpoint refuses", decideErr)
		}
		return
	}
	if decideErr == nil {
		t.Fatal("can_approve = false but Decide succeeded -- the wire hid an action the caller was entitled to")
	}
	if got.ApproveBlockedReason == nil {
		t.Fatalf("can_approve = false with a null reason, while Decide answered %v", decideErr)
	}
	for _, row := range sentenceForSentinel {
		if errors.Is(decideErr, row.err) {
			if *got.ApproveBlockedReason != row.sentence {
				t.Errorf("Decide answered %v but the wire said %q, want %q", row.err, *got.ApproveBlockedReason, row.sentence)
			}
			return
		}
	}
	t.Errorf("Decide answered an unmapped error %v; the wire said %q", decideErr, *got.ApproveBlockedReason)
}

// approvalStoreFor is the REAL decision store, wired exactly as cmd/invoice wires it.
func approvalStoreFor(app *pgxpool.Pool) *approval.Store {
	return approval.NewStore(app, FingerprintTx, DemoteApprovalRejectedTx)
}

// TestGetHandler_ApproveFlagAgreesWithTheDecisionEndpoint is the claim the four keys
// actually make. Every row arms a real invoice through Store.ApplyValidation, spoils
// exactly one precondition, reads can_approve off the serialized body, and THEN calls
// the real approval.Store.Decide -- the domain half of POST
// /v1/invoices/{id}/approvals. A true flag must be followed by a success and a false
// flag by the sentinel whose sentence the wire just published.
func TestGetHandler_ApproveFlagAgreesWithTheDecisionEndpoint(t *testing.T) {
	rows := []struct {
		name string
		// The request seam refuses a non-active caller before the store reads
		// anything (db.WithinRequestTenantTxOpts), so this row has no flags to
		// agree on -- both doors refuse under one sentinel instead.
		refusedBySeam bool
		spoil         func(t *testing.T, super *pgxpool.Pool, fx approvalFactsFixture)
	}{
		{"staffed_admin_allowed", false, func(t *testing.T, super *pgxpool.Pool, fx approvalFactsFixture) {}},
		{"membership_downgraded_to_preparer", false, func(t *testing.T, super *pgxpool.Pool, fx approvalFactsFixture) {
			mustExec(t, super, `UPDATE memberships SET role = 'preparer' WHERE tenant_id = $1 AND user_id = $2`, fx.tenantID, fx.subject)
		}},
		{"membership_suspended", true, func(t *testing.T, super *pgxpool.Pool, fx approvalFactsFixture) {
			mustExec(t, super, `UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, fx.tenantID, fx.subject)
		}},
		{"workflow_role_soft_deleted", false, func(t *testing.T, super *pgxpool.Pool, fx approvalFactsFixture) {
			mustExec(t, super, `UPDATE workflow_roles SET deleted_at = now() WHERE tenant_id = $1 AND key = 'finance-lead'`, fx.tenantID)
		}},
		{"unstaffed_from_the_role", false, func(t *testing.T, super *pgxpool.Pool, fx approvalFactsFixture) {
			mustExec(t, super, `DELETE FROM workflow_role_members WHERE tenant_id = $1 AND user_id = $2`, fx.tenantID, fx.subject)
		}},
	}
	for i, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			super, app := dbTestPools(t)

			fx := seedApprovalFactsFixture(t, super, fmt.Sprintf("APPR-08-06-AGREE-%d", i), true)
			fx.armInvoice(t, super, app, fmt.Sprintf("appr-08-06-agree-%d", i))
			row.spoil(t, super, fx)

			if row.refusedBySeam {
				store := NewStore(app, WithApprovalsEnforced(true))
				r := httptest.NewRequest(http.MethodGet, "/v1/invoices/"+fx.invID, nil)
				r.SetPathValue("id", fx.invID)
				r = r.WithContext(fx.ctx)
				rec := httptest.NewRecorder()
				GetHandler(store.Get, store.CallerRole, store.ApprovalFacts, nil).ServeHTTP(rec, r)
				// The seam refuses the read, and the refusal reaches the wire as the
				// 403 the mapper produces -- the same body every other handler gives.
				assertNotActiveMember403(t, rec)
				if _, err := approvalStoreFor(app).Decide(fx.ctx, fx.invID, "approved", nil); !errors.Is(err, db.ErrNotActiveMember) {
					t.Errorf("Decide err = %v, want db.ErrNotActiveMember -- both doors refuse under one sentinel", err)
				}
				return
			}

			got := approveFlagsVia(t, app, fx, true)
			_, decideErr := approvalStoreFor(app).Decide(fx.ctx, fx.invID, "approved", nil)
			assertDecideAgrees(t, got, decideErr)

			// can_reject shares the computation, so it must share the verdict.
			if got.CanReject != got.CanApprove || !sameReason(got.ApproveBlockedReason, got.RejectBlockedReason) {
				t.Errorf("approve pair = (%v,%q) but reject pair = (%v,%q), want identical",
					got.CanApprove, derefReason(got.ApproveBlockedReason), got.CanReject, derefReason(got.RejectBlockedReason))
			}
		})
	}
}

// TestGetHandler_ApproveFlagClosesAfterTheRunIsDecided is the same agreement claim
// across a state CHANGE rather than a spoiled fixture: the very caller who was offered
// the button must stop being offered it once the run closes, and the second Decide
// must refuse with the sentinel matching the sentence the wire now publishes.
func TestGetHandler_ApproveFlagClosesAfterTheRunIsDecided(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-06-CLOSES", true)
	fx.armInvoice(t, super, app, "appr-08-06-closes")

	before := approveFlagsVia(t, app, fx, true)
	if !before.CanApprove {
		t.Fatalf("can_approve = false for a staffed admin on a freshly armed invoice, want true (reason=%q)", derefReason(before.ApproveBlockedReason))
	}
	if before.ApproveBlockedReason != nil {
		t.Errorf("approve_blocked_reason = %q on an allowed gate, want null", *before.ApproveBlockedReason)
	}

	if _, err := approvalStoreFor(app).Decide(fx.ctx, fx.invID, "approved", nil); err != nil {
		t.Fatalf("Decide(approved) on the invoice the wire said was approvable: %v", err)
	}

	after := approveFlagsVia(t, app, fx, true)
	if after.CanApprove {
		t.Error("can_approve = true after the run was approved, want false")
	}
	_, secondErr := approvalStoreFor(app).Decide(fx.ctx, fx.invID, "approved", nil)
	if secondErr == nil {
		t.Fatal("a second Decide succeeded on a closed run -- this test's oracle assumes it cannot")
	}
	assertDecideAgrees(t, after, secondErr)
}

// TestGetHandler_ApproveFlagsUnflaggedOnEveryFixture is AC #5 against the real store on
// the shapes that REFUSE, not just the one that passes: APPROVALS_ENFORCED must move
// neither the flags nor the reasons, whatever rung is doing the refusing.
func TestGetHandler_ApproveFlagsUnflaggedOnEveryFixture(t *testing.T) {
	for _, staffed := range []bool{true, false} {
		t.Run(fmt.Sprintf("staffed_%v", staffed), func(t *testing.T) {
			super, app := dbTestPools(t)

			fx := seedApprovalFactsFixture(t, super, fmt.Sprintf("APPR-08-06-UNFLAGGED-%v", staffed), staffed)
			fx.armInvoice(t, super, app, fmt.Sprintf("appr-08-06-unflagged-%v", staffed))

			off := approveFlagsVia(t, app, fx, false)
			on := approveFlagsVia(t, app, fx, true)
			if off.CanApprove != on.CanApprove || off.CanReject != on.CanReject {
				t.Errorf("flags differ across APPROVALS_ENFORCED: off=%+v on=%+v", off, on)
			}
			if !sameReason(off.ApproveBlockedReason, on.ApproveBlockedReason) || !sameReason(off.RejectBlockedReason, on.RejectBlockedReason) {
				t.Errorf("reasons differ across APPROVALS_ENFORCED: off=%q/%q on=%q/%q",
					derefReason(off.ApproveBlockedReason), derefReason(off.RejectBlockedReason),
					derefReason(on.ApproveBlockedReason), derefReason(on.RejectBlockedReason))
			}
			// staffed=false seeds no membership at all, so the role rung refuses; the
			// staffed arm clears every rung. Pinning which one each is keeps this from
			// passing on two identically-broken answers.
			if want := staffed; on.CanApprove != want {
				t.Errorf("can_approve = %v with staffed=%v, want %v (reason=%q)", on.CanApprove, staffed, want, derefReason(on.ApproveBlockedReason))
			}
		})
	}
}

func mustExec(t *testing.T, super *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	tag, err := super.Exec(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatalf("exec %q affected 0 rows -- the fixture was not spoiled, so this row proves nothing", sql)
	}
}
