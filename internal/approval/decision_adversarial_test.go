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
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
// decision.go:51-68 now bounds BOTH decisions' reasons inside Decide itself
// (byte-counted, maxRejectReasonLen) -- the fix for the defect these two tests
// originally found and pinned RED (task-491's first QA pass: Decide's approve branch
// had no length check at all, so a non-HTTP caller of the exported Store.Decide -- a
// batch-approve, a CLI, a worker -- could write a reason of arbitrary length straight
// past every guard the HTTP-only bound provided). approval_decisions.reason is still an
// unbounded `text` column (migrations/20260809232011_approval_runs.sql:86, no CHECK) --
// the ceiling is enforced in Go, not the schema, same as every other bound this package
// keeps at the store layer (hasNUL, name length, role-key length).

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

// TestApprove_VeryLongReasonIsRefusedAndWritesNothing is the fixed version of what was
// TestApprove_UnboundedReasonReachesTheUnboundedTextColumn: that test documented the
// defect (a 50,000-byte approve reason wrote through in full) and necessarily flipped
// to FAIL once Decide's domain-layer bound (decision.go:63-68) shipped -- no threshold
// can satisfy both "50,000 bytes writes through" and "1001 bytes is refused". Rewritten
// in place rather than left red or deleted, since QA authored it and the behaviour it
// pinned was the bug, not a spec worth preserving. Uses a MUCH larger reason than the
// 1001-byte case above so the coverage isn't a duplicate off-by-one check: it proves
// the bound holds even for a wildly oversized input, not merely at the boundary, and
// that nothing partial lands in approval_decisions on the refused write.
func TestApprove_VeryLongReasonIsRefusedAndWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 approve-reason-very-long-refused", "approve-reason-very-long-refused-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("a", 50_000)
	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("Decide(approved) with a 50,000-byte reason: err = %v, want ErrValidation -- "+
			"the domain-layer bound must hold well past the boundary, not just at 1001 bytes", err)
	}
	assertNothingWritten(t, super, f)
}

// TestApprove_MultiByteReasonOverByteBoundaryRefusedAtTheDomainLayer mirrors
// TestReject_MultiByteReasonOverByteBoundaryRefused (decision_test.go), for approve
// instead of reject, calling Decide directly. Proves the fix's new approve-branch bound
// (decision.go:63-68, `len(*reason) > maxRejectReasonLen`) is byte-counted, not
// rune-counted, the same way the pre-existing HTTP-edge check already was -- the
// re-verification pass this test exists for: mutating that len() call to
// utf8.RuneCountInString must still redden a DOMAIN-layer caller, not just
// TestApprovalHandler_ApproveReasonBoundIsByteCountedNotRuneCounted's HTTP-edge one.
func TestApprove_MultiByteReasonOverByteBoundaryRefusedAtTheDomainLayer(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 approve-reason-multibyte-domain", "approve-reason-multibyte-domain-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("€", 334) // 1002 bytes, 334 runes
	if n := utf8.RuneCountInString(reason); n >= maxRejectReasonLen {
		t.Fatalf("fixture reason has %d runes, want under %d -- the point of this test is the byte/rune gap", n, maxRejectReasonLen)
	}
	if len(reason) <= maxRejectReasonLen {
		t.Fatalf("fixture reason has %d bytes, want over %d", len(reason), maxRejectReasonLen)
	}

	_, err := approve(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if !errors.Is(err, ErrValidation) {
		t.Errorf("Decide(approved) with a %d-byte/%d-rune reason, called directly: err = %v, want ErrValidation (byte-counted, not rune-counted)",
			len(reason), utf8.RuneCountInString(reason), err)
	}
	assertNothingWritten(t, super, f)
}

// --- the story's own named-but-never-written spec: a NUL byte in reason ------------
//
// The design doc's Test Specs table names TestDecide_NULByteInReasonIsRefusedNotStored
// ("a reason containing \x00 -> Decide -> refused with nothing written (22021 never
// reaches the client as a 500)"), but no file in this package defined it until this QA
// pass. Every OTHER text field this package accepts (policy names, workflow_role_key,
// notify target/channel) was already screened with hasNUL (policy.go:251-255) before
// reaching SQL; Decide's reason parameter now is too (decision.go:44-49), fixing the
// gap this test originally found RED: a NUL byte used to raise a raw Postgres 22021 that
// decisionStatusForErr's default case turned into a bare 500. decisionStatusForErr now
// maps ErrValidation -> 400 "invalid reason" (handlers.go:238-241), so the fabricated
// 500 this test's own doc comment used to describe no longer happens.

// TestDecide_NULByteInReasonIsRefusedNotStored transcribes the story's own spec name
// verbatim. Table over both decisions: reject already had ITS OWN trim+bound check
// (decision.go:53-62) that a NUL survives untouched (a NUL is not whitespace), and
// approve had no reason check at all before this subtask's HTTP-only bound -- hasNUL
// now screens both, ahead of either decision's own branch.
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

// --- task-492 (APPR-07-07, adversarial suite): the 6 genuinely-new specs from the
// Stage-1/2 validation appendix (task-492's implementation_plan), plus one boundary
// positive control for approve's reason bound. 5 of task-492's own 11 planned specs
// duplicate shipped coverage 1:1 (TestApprove_CrossTenantRunIsNotFound,
// TestApprovalRun_CrossTenantReadIsNotFound,
// TestApprove_ForbiddenAndUnknownAreIndistinguishableForANonApprover,
// TestApprove_SubjectWithNoMembershipRefused,
// TestApprovalRun_HolderOrderFollowsStaffingOrdNotRosterOrder) and are deliberately not
// rebuilt here -- see the appendix for the 1:1 mapping. The NUL-byte spec
// (TestDecide_NULByteInReasonIsRefusedNotStored, above) was verified against hasNUL's
// position-independent check (policy.go:255) and already covers both decision branches;
// no further NUL-position variant adds coverage.

// TestDecisions_AppendOnlyGrantMatrix (AC-5): approval_decisions holds only SELECT,
// INSERT for invoice_app (migrations/20260809232011_approval_runs.sql:114) -- the
// durability claim the story's ledger rests on, distinct from approval_policies'
// grant matrix (schema_constraints_test.go's TestApprovalPolicy_HardDeleteStillUngranted
// is the only other 42501 probe in the package, and it never touches this table).
func TestDecisions_AppendOnlyGrantMatrix(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 decisions-grant-matrix", "decisions-grant-matrix-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	if _, err := approve(t, app, f.tenantID, adminID, f.invoiceID, nil); err != nil {
		t.Fatalf("seed a real decision: Decide(approved): %v, want success", err)
	}
	var decisionID string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM approval_decisions WHERE run_id = $1`, f.runID).Scan(&decisionID); err != nil {
		t.Fatalf("read back decision id: %v", err)
	}

	updErr := db.WithinTenantTx(context.Background(), app, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE approval_decisions SET actor = 'tampered' WHERE id = $1`, decisionID)
		return err
	})
	assertSQLState(t, updErr, "42501")

	delErr := db.WithinTenantTx(context.Background(), app, f.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`DELETE FROM approval_decisions WHERE id = $1`, decisionID)
		return err
	})
	assertSQLState(t, delErr, "42501")

	// The row survives both refused mutations byte-identical.
	after := decisionsForRun(t, super, f.runID)
	if len(after) != 1 || after[0].Actor != adminID {
		t.Errorf("approval_decisions after the refused UPDATE/DELETE = %+v, want the original row untouched", after)
	}
}

// TestApprove_ConcurrentApproveAndRejectYieldOneOutcome (AC-6): every existing
// concurrency test races two calls of the SAME decision value
// (TestApprove_ConcurrentSingleDecision, TestApprove_ConcurrentDecisionsOnAMultiStepRunResolveDeterministically,
// both approve-v-approve in decision_test.go). Nothing races one approve against one
// reject on the same run. The resolving SELECT (decision.go:159-172) is shared,
// decision-value-agnostic code, so the loser here is ErrRunClosed regardless of which
// value it carried -- this proves the winner's decision value and the run's terminal
// state never disagree, whichever of the two actually wins the row lock.
func TestApprove_ConcurrentApproveAndRejectYieldOneOutcome(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 concurrent-approve-reject", "concurrent-approve-reject-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, errs[0] = approve(t, app, f.tenantID, adminID, f.invoiceID, nil)
	}()
	go func() {
		defer wg.Done()
		<-start
		reason := "racing reject"
		_, errs[1] = reject(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	}()
	close(start)
	wg.Wait()

	approveWon, rejectWon := errs[0] == nil, errs[1] == nil
	if approveWon == rejectWon {
		t.Fatalf("errs = %v, want exactly one of approve/reject to succeed", errs)
	}
	loserErr := errs[1]
	if rejectWon {
		loserErr = errs[0]
	}
	if !errors.Is(loserErr, ErrRunClosed) {
		t.Errorf("loser's err = %v, want ErrRunClosed", loserErr)
	}

	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 {
		t.Fatalf("approval_decisions rows for the run = %d, want exactly 1: %+v", len(decisions), decisions)
	}
	wantDecision := "approved"
	if rejectWon {
		wantDecision = "rejected"
	}
	if decisions[0].Decision != wantDecision {
		t.Errorf("stored decision = %q, want %q (matching whichever of approve/reject actually won)", decisions[0].Decision, wantDecision)
	}

	run := oneApprovalRun(t, super, f.invoiceID)
	if run.State != wantDecision {
		t.Errorf("run.State = %q, want %q -- must match the single decision that was written", run.State, wantDecision)
	}
}

// demoteAndCancel mirrors internal/invoice/store.go's edit-triggered demotion
// (store.go:1327,1537: validated -> draft, then CancelLiveRunTx, same tx, same
// invoices -> approval_* lock order as decideTx) using only package-local pieces --
// internal/approval must not import internal/invoice.
func demoteAndCancel(t *testing.T, pool *pgxpool.Pool, tenantID, invoiceID, actor string) (bool, error) {
	t.Helper()
	var cancelled bool
	err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(),
			`UPDATE invoices SET status = 'draft' WHERE id = $1 AND status = 'validated'`, invoiceID); err != nil {
			return err
		}
		var err error
		cancelled, err = CancelLiveRunTx(context.Background(), tx, invoiceID, actor)
		return err
	})
	return cancelled, err
}

// TestApprove_ConcurrentDecideAndEditDoNotStrandTheRun (AC-6): the mechanism the story
// names (internal/invoice/store.go:1327,1537) demotes a validated invoice to draft and
// calls CancelLiveRunTx in the SAME tx -- reachable entirely inside this package via
// decideTx and the exported CancelLiveRunTx. Uses reject, not approve: CancelLiveRunTx
// deliberately treats 'approved' as still-live (engine.go:258-260's own doc: cancelling
// a decided-but-since-edited run is intended), so racing approve here would make "a
// cancelled run carrying a decision" a LEGITIMATE outcome, not a defect. 'rejected' is
// the one state CancelLiveRunTx never touches, so this is the shape that actually
// distinguishes a real stranding bug from working-as-designed.
func TestApprove_ConcurrentDecideAndEditDoNotStrandTheRun(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 concurrent-decide-edit", "concurrent-decide-edit-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	var rejectErr, cancelErr error
	var cancelled bool
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		reason := "racing edit"
		_, rejectErr = reject(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	}()
	go func() {
		defer wg.Done()
		<-start
		cancelled, cancelErr = demoteAndCancel(t, app, f.tenantID, f.invoiceID, "editor-actor")
	}()
	close(start)
	wg.Wait()

	if cancelErr != nil {
		t.Fatalf("demoteAndCancel: %v, want nil", cancelErr)
	}

	run := oneApprovalRun(t, super, f.invoiceID)
	decisions := decisionsForRun(t, super, f.runID)

	switch {
	case rejectErr == nil:
		// reject won the invoices-row lock: the run closes rejected before the edit
		// ever reaches it, so CancelLiveRunTx must find it already excluded.
		if run.State != "rejected" {
			t.Errorf("reject won the race but run.State = %q, want rejected", run.State)
		}
		if len(decisions) != 1 {
			t.Errorf("reject won the race but approval_decisions rows = %d, want 1", len(decisions))
		}
		if cancelled {
			t.Error("CancelLiveRunTx reported cancelled=true against an already-rejected run, want false -- a rejected run must never be cancelled")
		}
	case errors.Is(rejectErr, ErrNotAwaitingApproval):
		// the edit won the invoices-row lock: the invoice reads 'draft' by the time
		// Decide re-checks it, refusing before ever touching the run.
		if run.State != "cancelled" {
			t.Errorf("the edit won the race but run.State = %q, want cancelled", run.State)
		}
		if len(decisions) != 0 {
			t.Errorf("the edit won the race but approval_decisions rows = %d, want 0 -- a cancelled run must never carry a decision", len(decisions))
		}
		if !cancelled {
			t.Error("CancelLiveRunTx reported cancelled=false against a still-open run, want true")
		}
	default:
		t.Fatalf("reject returned %v, want nil or ErrNotAwaitingApproval", rejectErr)
	}

	// Either branch, the run must never be left open beside a non-validated invoice --
	// exactly reconciliation's approval_run_orphaned finding
	// (internal/reconciliation/reconciliation.go:168-172).
	if run.State == "open" {
		t.Error("run.State = open after both goroutines completed, want rejected or cancelled")
	}
}

// TestApprove_TwoOpenRunsCannotExist (AC-6): the only existing coverage of
// approval_runs_one_open (schema_constraints_test.go's TestApprovalRuns_SecondOpenRunRejected)
// is a raw-SQL direct-INSERT probe of the constraint alone. This walks the real
// lifecycle instead -- reject (via Decide) -> demote -> re-validate -> re-arm (via
// ArmTx) -- and confirms at most one OPEN run exists at every step, not just at the
// two static endpoints.
func TestApprove_TwoOpenRunsCannotExist(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 two-open-runs-lifecycle", "two-open-runs-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	openRunCount := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'open'`, f.invoiceID).Scan(&n); err != nil {
			t.Fatalf("count open runs: %v", err)
		}
		return n
	}
	if n := openRunCount(t); n != 1 {
		t.Fatalf("open runs before reject = %d, want 1", n)
	}

	reason := "not this one"
	if _, err := reject(t, app, f.tenantID, adminID, f.invoiceID, &reason); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if n := openRunCount(t); n != 0 {
		t.Errorf("open runs after reject = %d, want 0", n)
	}
	rejectedRun := oneApprovalRun(t, super, f.invoiceID)
	if rejectedRun.State != "rejected" {
		t.Fatalf("run.State after reject = %q, want rejected", rejectedRun.State)
	}

	// Demote + re-validate: stubDemoter (decision_test.go) never touches
	// invoices.status -- the honest oracle for that edge is
	// internal/invoice/reject_demotion_test.go -- so this simulates the two real
	// transitions directly, matching TestApprove_ConcurrentDecideAndEditDoNotStrandTheRun's
	// same rationale above.
	setInvoiceStatus(t, super, f.invoiceID, "draft")
	if n := openRunCount(t); n != 0 {
		t.Errorf("open runs while draft = %d, want 0", n)
	}
	setInvoiceStatus(t, super, f.invoiceID, "validated")
	if n := openRunCount(t); n != 0 {
		t.Errorf("open runs after re-validate, before re-arm = %d, want 0", n)
	}

	res, err := arm(t, app, f.tenantID, f.invoiceID, "fp-two-open-runs-rearm", "rearm-actor")
	if err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	if res.RunID == rejectedRun.ID {
		t.Fatalf("re-arm produced the SAME run id as the rejected run, want a new one")
	}
	if n := openRunCount(t); n != 1 {
		t.Errorf("open runs after re-arm = %d, want exactly 1", n)
	}

	var total int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, f.invoiceID).Scan(&total); err != nil {
		t.Fatalf("count total runs: %v", err)
	}
	if total != 2 {
		t.Errorf("total approval_runs for invoice = %d, want exactly 2 (rejected + re-armed)", total)
	}

	var newState string
	if err := super.QueryRow(context.Background(),
		`SELECT state FROM approval_runs WHERE id = $1`, res.RunID).Scan(&newState); err != nil {
		t.Fatalf("read back re-armed run state: %v", err)
	}
	if newState != "open" {
		t.Errorf("re-armed run.state = %q, want open", newState)
	}
}

// TestApprove_HolderInAnotherTenantDoesNotSatisfy (AC-2): the highest-value AXIS-2 gap
// -- every shipped cross-tenant test is refused earlier, at the invoices lookup
// (staging the collision as "decide tenant A's invoice as a tenant-B caller"), so
// nothing exercises AXIS 2's own RLS-only isolation (decision.go:174-197 carries no
// explicit tenant_id predicate of its own; RLS on workflow_roles/workflow_role_members
// is the only thing scoping it). This stages the caller as a genuine active admin OF
// THE RUN'S OWN TENANT -- AXIS 1 and the invoices/approval_runs lookups all pass -- but
// staffed into a role sharing the exact same key STRING only in a DIFFERENT tenant.
// Reachable because memberships_tenant_user_uq is (tenant_id, user_id), not user_id
// alone: one person can hold separate membership+staffing rows in two tenants.
func TestApprove_HolderInAnotherTenantDoesNotSatisfy(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 holder-other-tenant", "same-key-axis2")
	otherTenantID := policyTenant(t, super, "APPR-07 holder-other-tenant OTHER")
	otherRoleID := seedWorkflowRole(t, super, otherTenantID, "same-key-axis2", "same-key-axis2")

	callerID := uuid.NewString()
	seedMembership(t, super, f.tenantID, callerID, "admin", "active") // real, active admin OF THE TARGET tenant
	// workflow_role_members_tenant_user_fk requires a (tenant_id, user_id) membership
	// row in the SAME tenant as the staffing row -- memberships_tenant_user_uq is
	// (tenant_id, user_id), not user_id alone, so this is a second, separate row for
	// the same person.
	seedMembership(t, super, otherTenantID, callerID, "admin", "active")
	staffWorkflowRole(t, super, otherTenantID, otherRoleID, callerID, 0) // staffed in the OTHER tenant's same-key role only

	_, err := approve(t, app, f.tenantID, callerID, f.invoiceID, nil)
	if !errors.Is(err, ErrNotRoleHolder) {
		t.Errorf("Decide(approved) as an admin staffed into a SAME-KEY role in a DIFFERENT tenant: err = %v, want ErrNotRoleHolder", err)
	}
	assertNothingWritten(t, super, f)

	// Positive control: the SAME caller, SAME invoice, staffed into the TARGET
	// tenant's own role instead, now succeeds -- proving the refusal above was
	// really about tenant-scoping the staffing row, not something else broken in
	// this fixture.
	staffWorkflowRole(t, super, f.tenantID, f.roleID, callerID, 0)
	run, err := approve(t, app, f.tenantID, callerID, f.invoiceID, nil)
	if err != nil {
		t.Fatalf("Decide(approved) after staffing the caller into the TARGET tenant's own role: %v, want success", err)
	}
	if run.State != "approved" {
		t.Errorf("run.State = %q, want approved", run.State)
	}
}

// TestApprovalRunSteps_TenantIsolationDirectRLSCheck mirrors
// TestApprove_TenantBindingIsCorrectAndCrossTenantInvisible's shape for
// approval_run_steps: approval_runs and approval_decisions each have a direct RLS proof
// of their own; approval_run_steps shares the identical verbatim tenant_isolation
// policy (migrations/20260809232011_approval_runs.sql:107-112, "the verbatim M2-06
// template") but was, until now, only ever exercised transitively -- every existing
// test that would reach it is already refused earlier, at the run or invoice lookup.
func TestApprovalRunSteps_TenantIsolationDirectRLSCheck(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 run-steps-direct-rls", "run-steps-direct-rls-role")
	otherTenantID := policyTenant(t, super, "APPR-07 run-steps-direct-rls OTHER")

	var tenantID string
	if err := super.QueryRow(context.Background(),
		`SELECT tenant_id FROM approval_run_steps WHERE id = $1`, f.stepID).Scan(&tenantID); err != nil {
		t.Fatalf("read back step tenant_id: %v", err)
	}
	if tenantID != f.tenantID {
		t.Errorf("approval_run_steps.tenant_id = %q, want %q", tenantID, f.tenantID)
	}

	var n int
	err := db.WithinTenantTx(context.Background(), app, otherTenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM approval_run_steps WHERE id = $1`, f.stepID).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count steps as the other tenant: %v", err)
	}
	if n != 0 {
		t.Errorf("approval_run_steps rows visible under a different tenant's RLS scope = %d, want 0", n)
	}

	// Positive control: the step's own tenant sees it fine -- otherwise "0 rows" could
	// vacuously pass because the step never existed at all.
	var m int
	err = db.WithinTenantTx(context.Background(), app, f.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM approval_run_steps WHERE id = $1`, f.stepID).Scan(&m)
	})
	if err != nil {
		t.Fatalf("count steps as the step's own tenant: %v", err)
	}
	if m != 1 {
		t.Errorf("approval_run_steps rows visible under the step's own tenant = %d, want 1", m)
	}
}

// TestApprove_ReasonAtByteBoundarySucceeds mirrors TestReject_ReasonAtByteBoundarySucceeds
// (decision_test.go) for approve. The existing approve-reason coverage above
// (TestApprove_ReasonBoundIsEnforcedAtTheDomainLayerNotOnlyAtTheHTTPEdge,
// TestApprove_VeryLongReasonIsRefusedAndWritesNothing,
// TestApprove_MultiByteReasonOverByteBoundaryRefusedAtTheDomainLayer) only ever asserts
// REFUSAL -- nothing proves the bound is a genuine boundary rather than an
// always-refuse: a mutant that made approve's check fire even AT 1000 bytes would pass
// every one of those.
func TestApprove_ReasonAtByteBoundarySucceeds(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-07 approve-reason-at-boundary", "approve-reason-at-boundary-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := strings.Repeat("a", 1000)
	run, err := approve(t, app, f.tenantID, adminID, f.invoiceID, &reason)
	if err != nil {
		t.Fatalf("Decide(approved) with a 1000-byte reason: %v, want success", err)
	}
	if run.State != "approved" {
		t.Errorf("run.State = %q, want approved -- a reason exactly at the 1000-byte bound must be legal", run.State)
	}

	decisions := decisionsForRun(t, super, f.runID)
	if len(decisions) != 1 {
		t.Fatalf("approval_decisions rows for the run = %d, want exactly 1", len(decisions))
	}
	if decisions[0].Reason == nil || *decisions[0].Reason != reason {
		t.Errorf("stored decision reason mismatch, want the full 1000-byte reason written through unchanged")
	}
}
