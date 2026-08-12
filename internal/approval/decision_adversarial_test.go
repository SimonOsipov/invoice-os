package approval

// task-489 (APPR-07-04, Mode B / QA phase): adversarial coverage beyond the Test Specs
// table decision_test.go transcribes -- gaps a mutation pass over decision.go surfaced
// that no AC-derived test observes. Same fixtures/helpers as decision_test.go; no new
// harness.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate
// step fails the build on any skip.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// TestApprove_TenantBindingIsCorrectAndCrossTenantInvisible proves commitDecisionTx's
// GUC-derived tenant_id (decision.go:200-207, not a Go-bound parameter) lands the
// decision under the RIGHT tenant, not merely a non-erroring one: read the row back as
// superuser and check its tenant_id, then confirm a second tenant's own RLS-scoped app
// connection sees zero rows for the same run_id.
func TestApprove_TenantBindingIsCorrectAndCrossTenantInvisible(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 tenant-binding", "tenant-binding-role")
	otherTenantID := policyTenant(t, super, "APPR-07 tenant-binding OTHER")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	if _, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil); err != nil {
		t.Fatalf("Decide(approved): %v, want success", err)
	}

	var tenantID string
	if err := super.QueryRow(context.Background(),
		`SELECT tenant_id FROM approval_decisions WHERE run_id = $1`, f.runID).Scan(&tenantID); err != nil {
		t.Fatalf("read back decision tenant_id: %v", err)
	}
	if tenantID != f.tenantID {
		t.Errorf("approval_decisions.tenant_id = %q, want %q", tenantID, f.tenantID)
	}

	var n int
	err := db.WithinTenantTx(context.Background(), app, otherTenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM approval_decisions WHERE run_id = $1`, f.runID).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count decisions as the other tenant: %v", err)
	}
	if n != 0 {
		t.Errorf("approval_decisions rows visible under a different tenant's RLS scope = %d, want 0", n)
	}
}

// TestApprove_NonApproverOracleIdenticalAcrossFourInvoiceStates extends
// TestApprove_PermissionCheckPrecedesRowLock's two states (unknown id, real forbidden
// id) to all four the story calls out: unknown, cross-tenant, real-and-awaiting, and
// real-but-not-validated. requireApprover runs before any of invoices/approval_runs is
// read, so none of these four can differ for a non-approver.
func TestApprove_NonApproverOracleIdenticalAcrossFourInvoiceStates(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 four-state-oracle", "four-state-oracle-role")
	other := newApproveFixture(t, super, app, "APPR-07 four-state-oracle OTHER", "four-state-oracle-other-role")

	notValidatedID := seedInvoice(t, super, f.tenantID, f.entityID, "four-state-oracle-not-validated")
	setInvoiceStatus(t, super, notValidatedID, "draft")

	preparerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, preparerID, "preparer", "active")

	traced, rec := tracedAppPool(t)
	store := NewStore(traced, stubFingerprinter, nil)
	callAs := func(t *testing.T, invoiceID string) error {
		t.Helper()
		rec.reset()
		c := auth.WithIdentity(context.Background(),
			auth.Identity{Subject: preparerID, Role: "authenticated", TenantID: f.tenantID})
		_, err := store.Decide(c, invoiceID, "approved", nil)
		if got := rec.mentioning("invoices"); len(got) != 0 {
			t.Errorf("Decide against %s issued %d statement(s) mentioning \"invoices\": %v", invoiceID, len(got), got)
		}
		return err
	}

	cases := []struct{ name, id string }{
		{"unknown id", uuid.NewString()},
		{"cross-tenant id (a real, open run in another tenant)", other.invoiceID},
		{"real id, awaiting this caller's approval", f.invoiceID},
		{"real id, not validated", notValidatedID},
	}
	var want error
	for i, tc := range cases {
		got := callAs(t, tc.id)
		if !errors.Is(got, ErrNotPermitted) {
			t.Errorf("%s: err = %v, want ErrNotPermitted", tc.name, got)
		}
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Errorf("%s: err = %v, not identical to %q's %v -- a non-approver must not distinguish these", tc.name, got, cases[0].name, want)
		}
	}
	assertNothingWritten(t, super, f)
	assertNothingWritten(t, super, other)
}

// TestApprove_ApproverGetsIdenticalRunNotFoundAcrossUnknownCrossTenantMalformedAndNoRun
// mirrors ApprovalRun's read-path guarantee (TestApprovalRun_UnknownCrossTenantAndMalformedIdsAgree)
// on Decide's write path, and closes the "role key staffed in a DIFFERENT tenant"
// adversarial case: the caller is a real, staffed, active admin -- but in tenant B, on a
// workflow role sharing tenant A's exact key string. AXIS 2 carries no tenant_id
// predicate of its own (store.go:27-30); this pins that RLS alone still isolates it.
func TestApprove_ApproverGetsIdenticalRunNotFoundAcrossUnknownCrossTenantMalformedAndNoRun(t *testing.T) {
	super, app := dbTestPools(t)
	fA := newApproveFixture(t, super, app, "APPR-07 approver-notfound-oracle A", "approver-notfound-oracle-role")

	tenantB := policyTenant(t, super, "APPR-07 approver-notfound-oracle B")
	entityB := seedBusinessEntity(t, super, tenantB, "Approver NotFound Oracle B Corp")
	roleBID := seedWorkflowRole(t, super, tenantB, "approver-notfound-oracle-role", "approver-notfound-oracle-role")
	adminID := uuid.NewString()
	seedMembership(t, super, tenantB, adminID, "admin", "active")
	staffWorkflowRole(t, super, tenantB, roleBID, adminID, 0)

	noRunInvoiceID := seedInvoice(t, super, tenantB, entityB, "approver-notfound-oracle-no-run")
	setInvoiceStatus(t, super, noRunInvoiceID, "validated")

	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: adminID, Role: "authenticated", TenantID: tenantB})
	store := NewStore(app, stubFingerprinter, nil)

	cases := []struct{ name, id string }{
		{"unknown id", uuid.NewString()},
		{"malformed id", "not-a-uuid"},
		{"cross-tenant id (tenant A's real, open, awaiting-approval invoice)", fA.invoiceID},
		{"real id in caller's own tenant, no run", noRunInvoiceID},
	}
	var want error
	for i, tc := range cases {
		_, got := store.Decide(c, tc.id, "approved", nil)
		if !errors.Is(got, ErrRunNotFound) {
			t.Errorf("%s: err = %v, want ErrRunNotFound", tc.name, got)
		}
		if code := pgCode(got); code != "" {
			t.Errorf("%s: surfaced a raw Postgres error (SQLSTATE %s)", tc.name, code)
		}
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Errorf("%s: err = %v, not identical to %q's %v", tc.name, got, cases[0].name, want)
		}
	}
	// Control: tenant A's own run is untouched -- this is a filter, not a store that
	// refuses everyone.
	assertNothingWritten(t, super, fA)
}

// TestApprove_CallerWithNoMembershipRowIsNotPermitted: requireApprover's Scan hits
// pgx.ErrNoRows for a subject the memberships table has never heard of, not merely a
// wrong-role or suspended row -- fails closed the same as every other refusal.
func TestApprove_CallerWithNoMembershipRowIsNotPermitted(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 no-membership-row", "no-membership-row-role")

	ghostID := uuid.NewString() // no seedMembership call at all
	_, err := approve(t, app, f.tenantID, ghostID, f.invoiceID, nil)
	if !errors.Is(err, ErrNotPermitted) {
		t.Errorf("Decide(approved) as a subject with no membership row at all: err = %v, want ErrNotPermitted", err)
	}
	assertNothingWritten(t, super, f)
}

// TestApprove_OtherHolderStaffedInRoleDoesNotAuthorizeUnstaffedCaller: AXIS 2 must
// check the CALLER's OWN staffing row (wrm.user_id = caller.Subject), not merely "does
// SOME eligible member hold this role" -- an EXISTS with the wrm.user_id predicate
// dropped would let any active admin/reviewer through on a stranger's staffing alone.
// Mutation-tested: dropping "AND wrm.user_id = $2" from decision.go's AXIS-2 query is
// NOT caught by any Test-Specs-table case, since every one of those either stages the
// SAME caller as the sole staffed holder or stages nobody at all.
func TestApprove_OtherHolderStaffedInRoleDoesNotAuthorizeUnstaffedCaller(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 other-holder-not-caller", "other-holder-not-caller-role")

	otherAdminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, otherAdminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, otherAdminID, 0)

	callerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, callerID, "reviewer", "active") // never staffed

	_, err := approve(t, app, f.tenantID, callerID, f.invoiceID, nil)
	if !errors.Is(err, ErrNotRoleHolder) {
		t.Errorf("Decide(approved) as an active reviewer NOT staffed into the role, while ANOTHER admin IS: err = %v, want ErrNotRoleHolder", err)
	}
	assertNothingWritten(t, super, f)
}

// TestApprove_PendingNotifyStepAloneDoesNotResolveAsApprovalStep: ArmTx never leaves a
// notify step 'pending' (always materialised 'skipped' -- confirmed against
// engine.go:174-187 for TestApprove_SkipsNotifyStepsWhenAdvancing), so this forces the
// shape directly. decideTx's resolving SELECT filters on kind='approval'; a step of any
// other kind, however its state reads, must never be picked up as the current step.
func TestApprove_PendingNotifyStepAloneDoesNotResolveAsApprovalStep(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 pending-notify-not-approval", "pending-notify-not-approval-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	if _, err := super.Exec(context.Background(),
		`UPDATE approval_run_steps SET kind = 'notify' WHERE id = $1`, f.stepID); err != nil {
		t.Fatalf("force step kind to notify: %v", err)
	}

	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil)
	if !errors.Is(err, ErrRunClosed) {
		t.Errorf("Decide(approved) against a run whose only pending step is kind=notify: err = %v, want ErrRunClosed (no approval step to resolve)", err)
	}
	if n := len(decisionsForRun(t, super, f.runID)); n != 0 {
		t.Errorf("approval_decisions rows = %d, want 0", n)
	}
}

// TestApprove_ConcurrentDecisionsOnAMultiStepRunResolveDeterministically: unlike
// TestApprove_ConcurrentSingleDecision (a 1-step run, where closing on the first
// decision means the run's own FOR UPDATE is doing double duty), this run has TWO
// pending approval steps and stays open after the first decision. Two goroutines race
// Decide against it; decideTx's approval_runs FOR UPDATE (held for the whole tx, not
// just the step SELECT) must still fully serialise them, so the loser's step
// resolution always re-reads post-commit and lands on step 1, never re-decides step 0.
func TestApprove_ConcurrentDecisionsOnAMultiStepRunResolveDeterministically(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 concurrent-multi-step")
	entityID := seedBusinessEntity(t, super, tenantID, "Concurrent Multi Step Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "concurrent-multi-step-invoice-1")
	setInvoiceStatus(t, super, invoiceID, "validated")

	roleA, roleB := "concurrent-multi-step-role-a", "concurrent-multi-step-role-b"
	roleAID := seedWorkflowRole(t, super, tenantID, roleA, roleA)
	roleBID := seedWorkflowRole(t, super, tenantID, roleB, roleB)
	adminID := uuid.NewString()
	seedMembership(t, super, tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, tenantID, roleAID, adminID, 0)
	staffWorkflowRole(t, super, tenantID, roleBID, adminID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Concurrent multi-step policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleA), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr(roleB), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-concurrent-multi-step", "fixture-arm")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}

	const n = 2
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = approve(t, app, tenantID, adminID, invoiceID, nil)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Decide returned %v, want nil (both steps staffed and pending)", i, err)
		}
	}

	steps := runStepsOf(t, super, res.RunID)
	if len(steps) != 2 {
		t.Fatalf("runStepsOf = %d rows, want 2", len(steps))
	}
	if steps[0].State != "satisfied" || steps[1].State != "satisfied" {
		t.Errorf("step states = %+v, want both satisfied", steps)
	}
	decisions := decisionsForRun(t, super, res.RunID)
	if len(decisions) != 2 {
		t.Fatalf("approval_decisions rows = %d, want exactly 2 (one per step, no duplicate)", len(decisions))
	}
	seenSteps := map[string]bool{}
	for _, d := range decisions {
		if seenSteps[d.RunStepID] {
			t.Errorf("approval_decisions has more than one row for run_step_id %s -- the same step was decided twice", d.RunStepID)
		}
		seenSteps[d.RunStepID] = true
	}
	storedRun := oneApprovalRun(t, super, invoiceID)
	if storedRun.State != "approved" {
		t.Errorf("stored run.state = %q, want approved", storedRun.State)
	}
}

// --- task-491 (APPR-07-06, Mode B / QA phase): the approve-reason bound belongs in
// Decide, not only at the HTTP edge -----------------------------------------------
//
// decision.go:47-56 bounds a REJECT reason inside Decide itself (byte-counted,
// maxRejectReasonLen). handlers.go's DecideHandler adds the SAME 1000-byte bound for
// BOTH decisions, but only at the wire layer -- Decide's own approve path never checks
// it. approval_decisions.reason is an unbounded `text` column (migrations/
// 20260809232011_approval_runs.sql:86, no CHECK, no length constraint), so any future
// caller of Store.Decide that is not DecideHandler -- a batch-approve, a CLI, a worker
// -- can write an approve reason of arbitrary length straight past every guard this
// story ships. This is a domain-layer gap, not merely a wire-layer one: every other
// bound this package enforces (hasNUL, name length, role-key length) lives at the
// store, not only at its HTTP callers.
//
// FAILS TODAY: Decide's approve branch has no length check at all. Reported as a
// defect, not fixed here (QA does not implement store changes) -- see task-491's
// implementation_notes for the verdict and the column-type evidence.

// TestApprove_ReasonBoundIsEnforcedAtTheDomainLayerNotOnlyAtTheHTTPEdge mirrors
// TestReject_ReasonOverByteBoundaryRefused exactly, for approve instead of reject, and
// calls Decide directly -- bypassing DecideHandler entirely, the same way a future
// non-HTTP caller would.
func TestApprove_ReasonBoundIsEnforcedAtTheDomainLayerNotOnlyAtTheHTTPEdge(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 approve-reason-domain-bound", "approve-reason-domain-bound-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("a", 1001)
	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("Decide(approved) with a 1001-byte reason, called directly (not through DecideHandler): err = %v, want ErrValidation -- "+
			"the bound must live in Decide itself, not only at the HTTP edge", err)
	}
	assertNothingWritten(t, super, f)
}

// TestApprove_UnboundedReasonReachesTheUnboundedTextColumn is the same gap's other
// half: proves the column itself places no ceiling, so a caller that gets past
// Decide's missing check writes exactly what it sent -- the unbounded write this bound
// exists to prevent. Uses a MUCH larger reason than the 1001-byte case above so the
// finding does not read as an off-by-one; strengthens the case that the fix belongs at
// the domain layer, not the schema.
func TestApprove_UnboundedReasonReachesTheUnboundedTextColumn(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 approve-reason-unbounded-column", "approve-reason-unbounded-column-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("a", 50_000)
	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if err != nil {
		t.Fatalf("Decide(approved) with a 50,000-byte reason: %v, want success today -- documenting the unbounded column, not asserting it is correct", err)
	}
	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 || decisions[0].Reason == nil {
		t.Fatalf("decisions = %+v, want exactly one row with a non-NULL reason", decisions)
	}
	if got := len(*decisions[0].Reason); got != 50_000 {
		t.Errorf("stored reason length = %d, want the full 50,000 bytes -- approval_decisions.reason (text, no CHECK) placed no ceiling of its own", got)
	}
}

// --- the story's own named-but-never-written spec: a NUL byte in reason ------------
//
// The design doc's Test Specs table names TestDecide_NULByteInReasonIsRefusedNotStored
// ("a reason containing \x00 -> Decide -> refused with nothing written (22021 never
// reaches the client as a 500)"), but no file in this package ever defined it. Every
// OTHER text field this package accepts (policy names, workflow_role_key, notify
// target/channel) is screened with hasNUL (policy.go:251-255) before it reaches SQL;
// Decide's reason parameter is not. Postgres's text type raises SQLSTATE 22021 for an
// embedded NUL, which decisionStatusForErr's default case maps to a bare 500 -- turning
// a client input mistake into a fabricated server error, the exact class of leak
// pgCode(err) != "" checks elsewhere in this package (e.g.
// TestApprove_ApproverGetsIdenticalRunNotFoundAcrossUnknownCrossTenantMalformedAndNoRun)
// already guard against.
//
// FAILS TODAY for both decisions. Reported as a defect, not fixed here.

// TestDecide_NULByteInReasonIsRefusedNotStored transcribes the story's own spec name
// verbatim. Table over both decisions: reject already has ITS OWN trim+bound check
// (decision.go:47-56) that a NUL survives untouched (a NUL is not whitespace), and
// approve has no reason check at all before this subtask's HTTP-only bound -- neither
// path screens for NUL specifically.
func TestDecide_NULByteInReasonIsRefusedNotStored(t *testing.T) {
	cases := []struct {
		name     string
		decision string
	}{
		{"approved", "approved"},
		{"rejected", "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			super, app := dbTestPools(t)
			f := newApproveFixture(t, super, app, "APPR-07 nul-byte-reason-"+tc.name, "nul-byte-reason-"+tc.name+"-role")
			adminID := uuid.NewString()
			seedMembership(t, super, f.tenantID, adminID, "admin", "active")
			staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

			reason := "bad\x00reason"
			var err error
			if tc.decision == "approved" {
				_, err = approve(t, app, f.tenantID, adminID, f.invoiceID, &reason)
			} else {
				_, err = reject(t, app, f.tenantID, adminID, f.invoiceID, &reason)
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("Decide(%s) with a NUL byte in reason: err = %v, want ErrValidation", tc.decision, err)
			}
			if code := pgCode(err); code != "" {
				t.Errorf("Decide(%s) with a NUL byte in reason surfaced a raw Postgres error (SQLSTATE %s) instead of a clean sentinel -- "+
					"this is exactly what decisionStatusForErr's default case turns into an opaque 500", tc.decision, code)
			}
			assertNothingWritten(t, super, f)
		})
	}
}
