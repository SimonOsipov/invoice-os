// APPR-08-05 QA (Stage 4, Mode B): adversarial coverage on top of the Mode A specs.
// Those pin submitGate and Store.ApprovalFacts at their two ends; these cross the
// two -- the full status x verdict matrix through the real GetHandler, role
// precedence at the handler rather than the gate, the run states no acceptance
// criterion names, and the one path that wires the REAL store into the REAL
// handler.
package invoice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- The full matrix through GetHandler --------------------------------------

// TestGetHandler_ApprovalArmAcrossEveryStatusAndVerdict is the wire-level twin of
// TestSubmitGate_ApprovalArmOnlyFiresOnValidated: 7 statuses x both verdicts, read
// off the serialized body rather than the gate's return. The oracle is hardcoded --
// derived from shippedSubmitGateOracle only in the one row that changes, so a
// regression in getResponse's plumbing (the wrong field wired to can_submit, a
// reason dropped on the way out) cannot hide behind a green gate test.
func TestGetHandler_ApprovalArmAcrossEveryStatusAndVerdict(t *testing.T) {
	if len(shippedSubmitGateOracle) != len(allStatuses) {
		t.Fatalf("oracle covers %d statuses, want all %d", len(shippedSubmitGateOracle), len(allStatuses))
	}
	for _, s := range allStatuses {
		for _, clear := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s_clear_%v", s, clear), func(t *testing.T) {
				want := shippedSubmitGateOracle[s]
				if s == StatusValidated && !clear {
					want = submitGateWant{false, wantAwaitingApprovalReason}
				}
				id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
				rec, resp := doInvoiceGetGated(t, invoiceAtStatusStub(s), fixedRoleStub("admin", nil), fixedApprovalStub(clear, nil), &id, uuid.NewString())
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
				}
				assertSubmitGateRow(t, want, resp.CanSubmit, resp.SubmitBlockedReason)
			})
		}
	}
}

// TestGetHandler_ApprovalSentenceOnlyReachesTheWireOnValidated guards the COPY, not
// the flag. awaitingApprovalReason promises "it can be submitted once an approver
// approves it", which is only true where a submit is otherwise possible. A run in
// state cancelled or rejected also reads TransmitClear false
// (TestStoreApprovalFacts_CancelledRunIsNotClear below), and both of those demote
// the invoice out of validated -- so the sentence must be unreachable on every
// other status, or the copy would tell a draft's reader to go find an approver.
func TestGetHandler_ApprovalSentenceOnlyReachesTheWireOnValidated(t *testing.T) {
	for _, s := range allStatuses {
		t.Run(string(s), func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			_, resp := doInvoiceGetGated(t, invoiceAtStatusStub(s), fixedRoleStub("admin", nil), fixedApprovalStub(false, nil), &id, uuid.NewString())
			carries := resp.SubmitBlockedReason != nil && *resp.SubmitBlockedReason == wantAwaitingApprovalReason
			if want := s == StatusValidated; carries != want {
				t.Errorf("status %q with a blocked approval verdict carries the awaiting-approval sentence = %v, want %v (reason=%v)", s, carries, want, ptrStr(resp.SubmitBlockedReason))
			}
		})
	}
}

// TestGetHandler_PreparerAndAdminOnTheSameGatedInvoice: role precedence at the
// WIRE, on one invoice and one approval verdict, so the two answers are directly
// comparable. TestSubmitGate_RoleStillWinsOverApproval proves the ordering inside
// the gate; this proves GetHandler passes the caller's own role through rather
// than a fixed one, and that the preparer never learns a run is open.
func TestGetHandler_PreparerAndAdminOnTheSameGatedInvoice(t *testing.T) {
	invoiceID := uuid.NewString()
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{ID: invoiceID, Status: StatusValidated}, nil
	}
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	_, preparer := doInvoiceGetGated(t, get, fixedRoleStub("preparer", nil), fixedApprovalStub(false, nil), &id, invoiceID)
	if preparer.CanSubmit {
		t.Error("preparer can_submit = true on a gated validated invoice, want false")
	}
	switch {
	case preparer.SubmitBlockedReason == nil:
		t.Errorf("preparer submit_blocked_reason = null, want the role sentence %q", wantNotApproverTransmitReason)
	case *preparer.SubmitBlockedReason != wantNotApproverTransmitReason:
		t.Errorf("preparer submit_blocked_reason = %q, want the role sentence -- role is the FIRST rung and the door answers 403 on role before any row is read", *preparer.SubmitBlockedReason)
	}

	_, admin := doInvoiceGetGated(t, get, fixedRoleStub("admin", nil), fixedApprovalStub(false, nil), &id, invoiceID)
	if admin.CanSubmit {
		t.Error("admin can_submit = true on a gated validated invoice, want false")
	}
	switch {
	case admin.SubmitBlockedReason == nil:
		t.Errorf("admin submit_blocked_reason = null, want %q", wantAwaitingApprovalReason)
	case *admin.SubmitBlockedReason != wantAwaitingApprovalReason:
		t.Errorf("admin submit_blocked_reason = %q, want %q", *admin.SubmitBlockedReason, wantAwaitingApprovalReason)
	}
}

// TestGetHandler_ApprovalSeamErrNoTenantStillAnswers200: db.ErrNoTenant is the one
// seam error with a plausible 401 reading, since it is exactly what an
// unauthenticated ctx produces. GetHandler has already 401'd on identity by the time
// the seam runs, so reaching here with ErrNoTenant means an internal fault, and the
// answer is the same fail-closed 200 every other seam error gets -- never a 401
// re-litigated after a row was already fetched, never a 5xx.
func TestGetHandler_ApprovalSeamErrNoTenantStillAnswers200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	rec, resp := doInvoiceGetGated(t, invoiceAtStatusStub(StatusValidated), fixedRoleStub("admin", nil), fixedApprovalStub(true, db.ErrNoTenant), &id, uuid.NewString())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- ErrNoTenant from the approval seam is a fault, not a re-authentication (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.CanSubmit {
		t.Error("can_submit = true after ErrNoTenant from the seam, want false")
	}
	switch {
	case resp.SubmitBlockedReason == nil:
		t.Errorf("submit_blocked_reason = null, want %q", wantAwaitingApprovalReason)
	case *resp.SubmitBlockedReason != wantAwaitingApprovalReason:
		t.Errorf("submit_blocked_reason = %q, want %q", *resp.SubmitBlockedReason, wantAwaitingApprovalReason)
	}
}

// TestGetHandler_NonCanonicalIdSpellingsReachTheSeamAsCanonical widens
// TestGetHandler_ApprovalSeamKeyedOnTheFetchedRowId from one spelling to the five a
// client can actually send. Every one must reach the seam as Store.Get's canonical
// text: an id the seam cannot match reads "no run", which is TransmitClear FALSE --
// so this trap fails closed rather than open, and would otherwise show up as an
// unexplainable disabled button instead of a leak.
func TestGetHandler_NonCanonicalIdSpellingsReachTheSeamAsCanonical(t *testing.T) {
	canonical := uuid.NewString()
	// Every spelling must survive httptest.NewRequest, so no raw spaces: a padded id
	// cannot be expressed as a URL at all and never reaches a handler.
	spellings := map[string]string{
		"braced_upper":     "{" + strings.ToUpper(canonical) + "}",
		"braced":           "{" + canonical + "}",
		"upper":            strings.ToUpper(canonical),
		"urn":              "urn:uuid:" + canonical,
		"percent_encoded":  "%7B" + canonical + "%7D",
		"no_hyphens_upper": strings.ToUpper(strings.ReplaceAll(canonical, "-", "")),
	}
	for name, requested := range spellings {
		t.Run(name, func(t *testing.T) {
			if requested == canonical {
				t.Fatalf("spelling %q is identical to the canonical id -- this row exercises nothing", name)
			}
			get := func(ctx context.Context, gotID string) (Invoice, error) {
				return Invoice{ID: canonical, Status: StatusValidated}, nil
			}
			var seen []string
			facts := func(ctx context.Context, gotID string) (ApprovalFacts, error) {
				seen = append(seen, gotID)
				return ApprovalFacts{TransmitClear: true}, nil
			}
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			rec, _ := doInvoiceGetGated(t, get, fixedRoleStub("admin", nil), facts, &id, requested)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if len(seen) != 1 {
				t.Fatalf("approvalFacts called %d times, want exactly 1", len(seen))
			}
			if seen[0] != canonical {
				t.Errorf("approvalFacts received %q, want the FETCHED row id %q", seen[0], canonical)
			}
		})
	}
}

func ptrStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// --- Run states no acceptance criterion names --------------------------------

// TestStoreApprovalFacts_ClosedRunStatesAreNotClear: approval.TransmitClear reads
// ApprovedRun, which is EXISTS over state = 'approved' -- so cancelled and rejected
// are NOT clear, and neither is silently promoted to clear by the newest-run
// RunState read sitting next to it. Both states are reachable: a reject and a
// re-validation each close the live run. TestGetHandler_ApprovalSentenceOnly-
// ReachesTheWireOnValidated is the other half -- both demote out of validated, so
// the false verdict never surfaces as the awaiting-approval sentence.
func TestStoreApprovalFacts_ClosedRunStatesAreNotClear(t *testing.T) {
	for _, state := range []string{"cancelled", "rejected"} {
		t.Run(state, func(t *testing.T) {
			super, app := dbTestPools(t)

			fx := seedApprovalFactsFixture(t, super, "APPR-08-05-"+strings.ToUpper(state), false)
			runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
			closeApprovalRunFor(t, super, runID, state, "fixture")

			store := NewStore(app, WithApprovalsEnforced(true))

			got, err := store.ApprovalFacts(fx.ctx, fx.invID)
			if err != nil {
				t.Fatalf("ApprovalFacts: %v", err)
			}
			if got.TransmitClear {
				t.Errorf("TransmitClear = true with a %s run under an active policy, want false -- only an approved run clears", state)
			}
			if got.RunState != state {
				t.Errorf("RunState = %q, want %q", got.RunState, state)
			}
			if got.PendingStepOrd != nil {
				t.Errorf("PendingStepOrd = %d on a %s run, want nil", *got.PendingStepOrd, state)
			}
		})
	}
}

// TestStoreApprovalFacts_SeesOnlyCommittedDecisions is the concurrency question
// stated deterministically: a decision held open in another transaction must not be
// visible (no dirty read), and must be visible the moment it commits (no caching,
// no snapshot held across calls). Read committed is Postgres's default and each
// ApprovalFacts call is its own transaction, so a GET racing a decision resolves to
// one side or the other -- never a torn answer.
func TestStoreApprovalFacts_SeesOnlyCommittedDecisions(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-RACE", false)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	store := NewStore(app, WithApprovalsEnforced(true))

	before, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts before: %v", err)
	}
	if before.TransmitClear || before.RunState != "open" {
		t.Fatalf("before the decision: TransmitClear=%v RunState=%q, want false/open", before.TransmitClear, before.RunState)
	}

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the decision tx: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err := tx.Exec(ctx,
		`UPDATE approval_runs SET state = 'approved', closed_at = now(), closed_by = $1 WHERE id = $2`,
		"fixture", runID,
	); err != nil {
		t.Fatalf("uncommitted approve: %v", err)
	}

	// A plain SELECT takes no row lock, so this must not block and must not see the
	// uncommitted row.
	during, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts during the open decision tx: %v", err)
	}
	if during.TransmitClear {
		t.Error("TransmitClear = true while the approval is still uncommitted -- a dirty read")
	}
	if during.RunState != "open" {
		t.Errorf("RunState = %q while the approval is still uncommitted, want %q", during.RunState, "open")
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the decision: %v", err)
	}
	committed = true

	after, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts after the commit: %v", err)
	}
	if !after.TransmitClear {
		t.Error("TransmitClear = false after the approval committed -- the read is cached or holds a stale snapshot")
	}
	if after.RunState != "approved" {
		t.Errorf("RunState = %q after the commit, want %q", after.RunState, "approved")
	}
}

// --- The real store through the real handler ---------------------------------

// TestGetHandler_RealStoreRealApprovalFacts_EndToEnd is the ONE path with no stub
// between the wire and the database. Every other DB-backed GetHandler spec injects
// clearApprovalStub, so before this test Store.ApprovalFacts and GetHandler's third
// seam were each covered at their own end and joined only by an AST assertion in
// cmd/invoice (TestInvoiceMain_WiresApprovalFactsIntoGetHandler) -- which proves the
// wiring is WRITTEN, not that it answers. Here store.Get, store.CallerRole and
// store.ApprovalFacts are all real, over an invoice armed through Store.ApplyValidation,
// and the assertion is the serialized body.
//
// Both flag positions run against the SAME armed invoice: the flag-ON answer is the
// feature, and the flag-OFF answer is the release-safety claim (docs/approvals.md
// section 11) that the whole subtask rests on.
func TestGetHandler_RealStoreRealApprovalFacts_EndToEnd(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-E2E", true)
	fx.armInvoice(t, super, app, "appr-08-05-e2e")

	getFor := func(t *testing.T, enforced bool) submitGateBody {
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
		var resp submitGateBody
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
		return resp
	}

	// The caller is an active admin, so the role rung passes and the status rung
	// passes (armInvoice asserts validated) -- the approval rung is what decides.
	t.Run("flag_on_blocks_with_the_shared_sentence", func(t *testing.T) {
		resp := getFor(t, true)
		if resp.CanSubmit {
			t.Error("can_submit = true for an admin on an armed validated invoice with the flag ON, want false")
		}
		switch {
		case resp.SubmitBlockedReason == nil:
			t.Errorf("submit_blocked_reason = null, want %q", wantAwaitingApprovalReason)
		case *resp.SubmitBlockedReason != wantAwaitingApprovalReason:
			t.Errorf("submit_blocked_reason = %q, want %q", *resp.SubmitBlockedReason, wantAwaitingApprovalReason)
		}
	})

	t.Run("flag_off_is_inert", func(t *testing.T) {
		resp := getFor(t, false)
		if !resp.CanSubmit {
			t.Error("can_submit = false with APPROVALS_ENFORCED off, want true -- the flag-off wire must read exactly as it did before the gate landed")
		}
		if resp.SubmitBlockedReason != nil {
			t.Errorf("submit_blocked_reason = %q with the flag off, want null", *resp.SubmitBlockedReason)
		}
	})
}
