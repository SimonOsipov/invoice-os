package approval

// task-487 QA phase: adversarial coverage for Store.ApprovalRun beyond the 14 AC-derived
// specs in read_model_db_test.go -- the multi-run "current run" case, an RLS positive
// control, a schema-permitted cross-run decision reference, and holder/reason/empty-run
// edge cases the AC table doesn't name individually.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate step
// fails the build on any skip.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- fixtures ---------------------------------------------------------------

// setMembershipEmail gives a seeded membership an email, leaving display_name NULL --
// setMembershipDisplayName's counterpart, for the holderName fallback-ladder tests.
func setMembershipEmail(t *testing.T, super *pgxpool.Pool, userID, email string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE memberships SET email = $2 WHERE user_id = $1`, userID, email)
	if err != nil {
		t.Fatalf("set membership email for %s: %v", userID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set membership email for %s affected %d rows, want 1", userID, tag.RowsAffected())
	}
}

// backdateRunOpenedAt rewrites one run's opened_at, so multi-run "current run" tests don't
// depend on the wall-clock gap between two arm() calls.
func backdateRunOpenedAt(t *testing.T, super *pgxpool.Pool, runID string, at time.Time) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET opened_at = $2 WHERE id = $1`, runID, at)
	if err != nil {
		t.Fatalf("backdate opened_at for run %s: %v", runID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("backdate opened_at for run %s affected %d rows, want 1", runID, tag.RowsAffected())
	}
}

// --- multi-run: the cancelled run must not shadow the current one --------------------

// TestApprovalRun_CancelledRunDoesNotShadowTheCurrentRun: edit-voids-approval cancels a
// run and a later validation re-arms a new one on the SAME invoice -- ApprovalRun must
// read the current (open) run, never the cancelled one, even though both share an
// invoice_id and the query has no other filter to tell them apart.
func TestApprovalRun_CancelledRunDoesNotShadowTheCurrentRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 multi-run-current")
	entityID := seedBusinessEntity(t, super, tenantID, "Multi Run Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "multi-run-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Multi-run policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	firstRes, err := arm(t, app, tenantID, invoiceID, "fp-multi-run-1", "test-actor")
	if err != nil {
		t.Fatalf("arm (first): %v", err)
	}
	// Pushed into the past explicitly rather than relying on the wall-clock gap between
	// this arm and the next -- two transactions run back-to-back could in principle land
	// in the same timestamptz tick.
	backdateRunOpenedAt(t, super, firstRes.RunID, time.Now().Add(-time.Hour))

	if _, err := cancel(t, app, tenantID, invoiceID, "test-actor"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	secondRes, err := arm(t, app, tenantID, invoiceID, "fp-multi-run-2", "test-actor")
	if err != nil {
		t.Fatalf("arm (second): %v", err)
	}
	if secondRes.RunID == firstRes.RunID {
		t.Fatal("second arm reused the first run's id -- the fixture is broken, not the assertion below")
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if run.RunID != secondRes.RunID {
		t.Errorf("run.RunID = %q, want the current run %q -- the cancelled run must not shadow it", run.RunID, secondRes.RunID)
	}
	if run.State != "open" {
		t.Errorf("run.State = %q, want \"open\"", run.State)
	}
}

// TestApprovalRun_TiedOpenedAtStillReturnsExactlyOneRun: two runs on the same invoice
// sharing the identical opened_at instant (forced -- two separate arm() transactions
// cannot produce this on their own) -- the query carries no secondary ORDER BY, so which
// one wins is implementation-defined, but ApprovalRun must still return exactly one
// complete, self-consistent run rather than erroring or mixing rows from both.
func TestApprovalRun_TiedOpenedAtStillReturnsExactlyOneRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 tied-opened-at")
	entityID := seedBusinessEntity(t, super, tenantID, "Tied Opened At Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "tied-opened-at-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Tied opened_at policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	firstRes, err := arm(t, app, tenantID, invoiceID, "fp-tied-1", "test-actor")
	if err != nil {
		t.Fatalf("arm (first): %v", err)
	}
	if _, err := cancel(t, app, tenantID, invoiceID, "test-actor"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	secondRes, err := arm(t, app, tenantID, invoiceID, "fp-tied-2", "test-actor")
	if err != nil {
		t.Fatalf("arm (second): %v", err)
	}

	tied := time.Now()
	backdateRunOpenedAt(t, super, firstRes.RunID, tied)
	backdateRunOpenedAt(t, super, secondRes.RunID, tied)

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v (want a run picked, not an error)", err)
	}
	if run.RunID != firstRes.RunID && run.RunID != secondRes.RunID {
		t.Fatalf("run.RunID = %q, want one of %q or %q", run.RunID, firstRes.RunID, secondRes.RunID)
	}
	if len(run.Steps) != 1 {
		t.Errorf("len(run.Steps) = %d, want 1 (whichever run was picked, its own single step)", len(run.Steps))
	}
}

// --- RLS: prove the isolation depends on the role, not just the invoice id -----------

// TestApprovalRun_SuperuserPoolBypassesRLS_ProvesTheAppPoolIsolationIsReal: a positive
// control paired with the app-pool refusal read_model_db_test.go already pins
// (TestApprovalRun_UnknownCrossTenantAndMalformedIdsAgree). Postgres superusers always
// bypass row security, FORCE ROW LEVEL SECURITY notwithstanding -- so running the exact
// same call over the superuser pool must leak tenant A's run to tenant B's context. If it
// didn't, the earlier refusal would be coming from something other than RLS (e.g. an
// accidental filter on invoiceID) and would prove nothing about tenant isolation.
func TestApprovalRun_SuperuserPoolBypassesRLS_ProvesTheAppPoolIsolationIsReal(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := policyTenant(t, super, "APPR-07 rls-control A")
	entityA := seedBusinessEntity(t, super, tenantA, "RLS Control A Corp")
	invoiceA := seedInvoice(t, super, tenantA, entityA, "rls-control-invoice-a")
	policyA := seedApprovalPolicy(t, super, tenantA, "RLS control A policy")
	versionA := seedApprovalPolicyVersionN(t, super, tenantA, policyA, 1)
	seedApprovalPolicyStepInLane(t, super, tenantA, versionA, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionA)
	if _, err := arm(t, app, tenantA, invoiceA, "fp-rls-control-a", "test-actor"); err != nil {
		t.Fatalf("arm tenant A: %v", err)
	}

	tenantB := policyTenant(t, super, "APPR-07 rls-control B")
	cB, _ := callerCtx(t, super, tenantB, "preparer", "active")

	// Negative control: the app pool (invoice_app, RLS-bound) refuses tenant A's data to
	// tenant B's context -- the same guarantee TestApprovalRun_UnknownCrossTenantAndMalformedIdsAgree
	// already pins, repeated here so the positive control below has a same-test baseline.
	if _, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(cB, invoiceA); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("app pool: ApprovalRun(tenant B ctx, tenant A invoice) = %v, want ErrRunNotFound", err)
	}

	// Positive control: the identical call over the superuser pool succeeds and returns
	// tenant A's run -- proving the refusal above rests on RLS + the invoice_app role.
	run, err := NewStore(super, stubFingerprinter, nil).ApprovalRun(cB, invoiceA)
	if err != nil {
		t.Fatalf("superuser pool: ApprovalRun(tenant B ctx, tenant A invoice) = %v, want success (a superuser always bypasses RLS)", err)
	}
	if run.RunID == "" {
		t.Error("superuser pool: run.RunID is empty, want tenant A's run to have come through")
	}
}

// --- decision ledger: a schema-permitted but never-written cross-run reference -------

// TestApprovalRun_DecisionReferencingStepFromAnotherRunDefaultsOrdToZero: the FK on
// approval_decisions.run_step_id checks (tenant_id, id) against approval_run_steps, not
// that the step's own run_id matches the decision's run_id -- so the schema permits a
// decision whose run_step_id names a step from a DIFFERENT run in the same tenant, even
// though nothing in this codebase writes such a row today. ApprovalRun's stepOrd map is
// built only from the CURRENT run's own steps, so a step id outside it is a plain Go map
// miss: Ord silently reads 0 (the zero value) rather than erroring. Pinned here as
// documented behavior, not asserted as correct -- see the QA report for the finding.
func TestApprovalRun_DecisionReferencingStepFromAnotherRunDefaultsOrdToZero(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 decision-cross-run-step")
	entityID := seedBusinessEntity(t, super, tenantID, "Cross Run Step Corp")
	invoiceA := seedInvoice(t, super, tenantID, entityID, "cross-run-step-invoice-a")
	invoiceB := seedInvoice(t, super, tenantID, entityID, "cross-run-step-invoice-b")

	policyID := seedApprovalPolicy(t, super, tenantID, "Cross run step policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	resA, err := arm(t, app, tenantID, invoiceA, "fp-cross-run-step-a", "test-actor")
	if err != nil {
		t.Fatalf("arm invoice A: %v", err)
	}
	resB, err := arm(t, app, tenantID, invoiceB, "fp-cross-run-step-b", "test-actor")
	if err != nil {
		t.Fatalf("arm invoice B: %v", err)
	}
	stepFromB := runStepID(t, super, resB.RunID, 0)
	seedApprovalDecision(t, super, tenantID, resA.RunID, stepFromB, "approved", "qa-tester", nil)

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceA)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Decisions) != 1 {
		t.Fatalf("len(run.Decisions) = %d, want 1", len(run.Decisions))
	}
	d := run.Decisions[0]
	if d.RunStepID != stepFromB {
		t.Errorf("RunStepID = %q, want %q", d.RunStepID, stepFromB)
	}
	if d.Ord != 0 {
		t.Errorf("Ord = %d, want 0 (the map-miss zero value for a step outside run A)", d.Ord)
	}
}

// TestApprovalRun_DecisionReasonDistinguishesNullFromEmptyString: NULL and "" are
// different stored values in a nullable text column -- the ledger's *string must keep
// them apart rather than collapsing "" to nil or vice versa.
func TestApprovalRun_DecisionReasonDistinguishesNullFromEmptyString(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 reason-null-vs-empty")
	entityID := seedBusinessEntity(t, super, tenantID, "Reason Null Vs Empty Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "reason-null-vs-empty-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Reason null-vs-empty policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("fin_dir"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-reason-null-vs-empty", "test-actor")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	nullStep := runStepID(t, super, res.RunID, 0)
	emptyStep := runStepID(t, super, res.RunID, 1)
	seedApprovalDecision(t, super, tenantID, res.RunID, nullStep, "approved", "qa-tester", nil)
	seedApprovalDecision(t, super, tenantID, res.RunID, emptyStep, "approved", "qa-tester", ptr(""))

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Decisions) != 2 {
		t.Fatalf("len(run.Decisions) = %d, want 2", len(run.Decisions))
	}
	var gotNull, gotEmpty bool
	for _, d := range run.Decisions {
		switch d.RunStepID {
		case nullStep:
			gotNull = true
			if d.Reason != nil {
				t.Errorf("null-reason decision: Reason = %v, want nil", *d.Reason)
			}
		case emptyStep:
			gotEmpty = true
			if d.Reason == nil {
				t.Error("empty-string-reason decision: Reason = nil, want a pointer to \"\"")
			} else if *d.Reason != "" {
				t.Errorf("empty-string-reason decision: Reason = %q, want \"\"", *d.Reason)
			}
		}
	}
	if !gotNull || !gotEmpty {
		t.Fatalf("did not find both decisions by run_step_id: gotNull=%v gotEmpty=%v", gotNull, gotEmpty)
	}
}

// --- holder resolution: the email fallback rung ---------------------------------------

// TestApprovalRun_HolderNameFallsBackToEmailWhenDisplayNameIsNull: display_name NULL,
// email set -- holderName's fallback ladder (display_name ?? email ?? user_id) must land
// on the email rung, not skip straight to the raw user id.
func TestApprovalRun_HolderNameFallsBackToEmailWhenDisplayNameIsNull(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 holder-email-fallback")
	entityID := seedBusinessEntity(t, super, tenantID, "Holder Email Fallback Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "holder-email-fallback-invoice-1")

	roleID := seedWorkflowRole(t, super, tenantID, "email-fallback-role", "Email Fallback Role")
	holderUserID := uuid.NewString()
	seedMembership(t, super, tenantID, holderUserID, "reviewer", "active")
	setMembershipEmail(t, super, holderUserID, "halima@example.com")
	staffWorkflowRole(t, super, tenantID, roleID, holderUserID, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "Email fallback policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("email-fallback-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-email-fallback", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("len(run.Steps) = %d, want 1", len(run.Steps))
	}
	holder := run.Steps[0].Holder
	if holder == nil {
		t.Fatal("Holder is nil, want a resolved value")
	}
	if holder.Text != "halima@example.com" {
		t.Errorf("Holder.Text = %q, want the email fallback %q", holder.Text, "halima@example.com")
	}
}

// --- ORDER BY really is load-bearing, not incidentally matching insertion order ------

// TestApprovalRun_StepsReturnInOrdOrderRegardlessOfPhysicalInsertOrder: ArmTx always
// inserts steps in ord order, so physical (heap) order happens to match ord order for
// every other test in this file -- a dropped ORDER BY ord would still pass them. Steps
// are seeded directly here in the REVERSE of their ord, decoupling physical order from
// ord so the query's own ORDER BY is what the assertion actually exercises.
func TestApprovalRun_StepsReturnInOrdOrderRegardlessOfPhysicalInsertOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 steps-ord-not-insert-order")
	entityID := seedBusinessEntity(t, super, tenantID, "Steps Ord Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "steps-ord-not-insert-order-invoice-1")
	policyID := seedApprovalPolicy(t, super, tenantID, "Steps ord policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	runID := seedApprovalRun(t, super, tenantID, invoiceID, versionID)

	for ord := 2; ord >= 0; ord-- { // physically inserted 2, 1, 0 -- ord ascends the opposite way
		if _, err := super.Exec(context.Background(),
			`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind) VALUES ($1, $2, $3, 'approval')`,
			tenantID, runID, ord); err != nil {
			t.Fatalf("seed step ord=%d: %v", ord, err)
		}
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 3 {
		t.Fatalf("len(run.Steps) = %d, want 3", len(run.Steps))
	}
	for i, want := range []int{0, 1, 2} {
		if run.Steps[i].Ord != want {
			t.Errorf("Steps[%d].Ord = %d, want %d -- steps must come back in ord order, not physical insert order", i, run.Steps[i].Ord, want)
		}
	}
}

// TestApprovalRun_HolderResolutionUsesStaffingOrdNotPhysicalInsertOrder: staffWorkflowRole
// in every other test happens to insert in ord order too, so a dropped ORDER BY on the
// holder-members query would still pass them. Staffed here in the REVERSE of ord --
// resolveHolder must still pick the ord=0 holder as primary (roles.ts:109-110's "first
// holder", not "first inserted").
func TestApprovalRun_HolderResolutionUsesStaffingOrdNotPhysicalInsertOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 holder-ord-not-insert-order")
	entityID := seedBusinessEntity(t, super, tenantID, "Holder Ord Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "holder-ord-not-insert-order-invoice-1")

	roleID := seedWorkflowRole(t, super, tenantID, "ord-not-insert-role", "Ord Not Insert Role")
	// Both suspended, so resolution() falls to resBlocked and picks holders[0] -- the ord=0
	// holder must win even though it is staffed SECOND, physically.
	firstOrdHolder := uuid.NewString() // ord 0, staffed second
	seedMembership(t, super, tenantID, firstOrdHolder, "reviewer", "suspended")
	setMembershipDisplayName(t, super, firstOrdHolder, "Ada Okafor")
	secondOrdHolder := uuid.NewString() // ord 1, staffed first
	seedMembership(t, super, tenantID, secondOrdHolder, "admin", "suspended")
	setMembershipDisplayName(t, super, secondOrdHolder, "Bola Adeyemi")

	staffWorkflowRole(t, super, tenantID, roleID, secondOrdHolder, 1) // inserted first, ord 1
	staffWorkflowRole(t, super, tenantID, roleID, firstOrdHolder, 0)  // inserted second, ord 0

	policyID := seedApprovalPolicy(t, super, tenantID, "Ord not insert order policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("ord-not-insert-role"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-holder-ord-not-insert", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 1 {
		t.Fatalf("len(run.Steps) = %d, want 1", len(run.Steps))
	}
	holder := run.Steps[0].Holder
	if holder == nil {
		t.Fatal("Holder is nil, want a resolved value")
	}
	if holder.Text != "Ada Okafor +1" {
		t.Errorf("Holder.Text = %q, want %q -- the ord=0 holder must be primary regardless of insertion order", holder.Text, "Ada Okafor +1")
	}
}

// --- AC-8: statement count really is constant, not just claimed in prose -------------

// TestApprovalRun_SixStatementsRegardlessOfStepAndRoleCount (AC-8): five approval steps
// across three distinct roles, each staffed with two holders, plus one decision -- a
// per-step or per-role query (an N+1) would inflate the count past the six the
// implementation notes claim. Uses tracedAppPool/sqlRecorder (workflow_roles_test.go),
// the only way to see an N+1 whose RESULTS are identical to the batched form.
func TestApprovalRun_SixStatementsRegardlessOfStepAndRoleCount(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 statement-count")
	entityID := seedBusinessEntity(t, super, tenantID, "Statement Count Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "statement-count-invoice-1")

	for _, key := range []string{"role-a", "role-b", "role-c"} {
		roleID := seedWorkflowRole(t, super, tenantID, key, key)
		for i := 0; i < 2; i++ {
			userID := uuid.NewString()
			seedMembership(t, super, tenantID, userID, "reviewer", "active")
			staffWorkflowRole(t, super, tenantID, roleID, userID, i)
		}
	}

	policyID := seedApprovalPolicy(t, super, tenantID, "Statement count policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	for i, key := range []string{"role-a", "role-b", "role-c", "role-a", "role-b"} {
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: i, Kind: "approval", WorkflowRoleKey: ptr(key), SLAHours: ptr(24),
		})
	}
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-statement-count", "test-actor")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	stepID := runStepID(t, super, res.RunID, 0)
	seedApprovalDecision(t, super, tenantID, res.RunID, stepID, "approved", "qa-tester", nil)

	tracedApp, rec := tracedAppPool(t)
	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	rec.reset()
	run, err := NewStore(tracedApp, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 5 {
		t.Fatalf("len(run.Steps) = %d, want 5", len(run.Steps))
	}

	for _, table := range []string{
		"FROM approval_runs", "FROM approval_run_steps", "FROM workflow_roles",
		"FROM workflow_role_members", "FROM memberships", "FROM approval_decisions",
	} {
		if got := len(rec.mentioning(table)); got != 1 {
			t.Errorf("statements mentioning %q = %d, want exactly 1 (five steps across three roles must not inflate this)", table, got)
		}
	}
	// Counted apart so the six above stay the STORE's budget: the request seam's
	// batched gate adds one more memberships read (db.WithinRequestTenantTxOpts).
	if got := len(rec.seamMentioning("FROM memberships")); got != 1 {
		t.Errorf("the seam gate issued %d memberships statement(s), want exactly 1 -- the six counts above are scoped, not blind", got)
	}
}

// --- zero-step run: the []-never-nil rule holds even with nothing to report ----------

// TestApprovalRun_ZeroStepRunReadsEmptyNonNilSlices: an empty policy closes on arm with
// no steps at all -- run.Steps and run.Decisions must both come back as non-nil, empty
// slices (the MarshalJSON []-never-null rule depends on the Go value already being
// non-nil, not just on the marshaller's own nil-check).
func TestApprovalRun_ZeroStepRunReadsEmptyNonNilSlices(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-07 zero-step-run")
	entityID := seedBusinessEntity(t, super, tenantID, "Zero Step Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "zero-step-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Zero-step policy (closes on arm)")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	activateApprovalPolicyVersion(t, super, versionID) // zero steps, sealed + active

	if _, err := arm(t, app, tenantID, invoiceID, "fp-zero-step", "test-actor"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	c, _ := callerCtx(t, super, tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(c, invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if run.Steps == nil {
		t.Error("run.Steps is nil, want a non-nil empty slice")
	}
	if len(run.Steps) != 0 {
		t.Errorf("len(run.Steps) = %d, want 0", len(run.Steps))
	}
	if run.Decisions == nil {
		t.Error("run.Decisions is nil, want a non-nil empty slice")
	}
	if len(run.Decisions) != 0 {
		t.Errorf("len(run.Decisions) = %d, want 0", len(run.Decisions))
	}
}
