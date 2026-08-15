package approval

// The transmit gate under a real Postgres: TransmitClear and the three tx-scoped reads
// that feed it.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate step
// fails the build on any skip.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- call wrappers -----------------------------------------------------------------

// transmitClear runs TransmitClearTx inside a fresh tenant-scoped transaction --
// arm()'s shape (arm_test.go:52), since these seams take a tx, not a pool.
func transmitClear(t *testing.T, pool *pgxpool.Pool, tenantID string, ids []string) map[string]bool {
	t.Helper()
	var clear map[string]bool
	if err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		clear, err = TransmitClearTx(context.Background(), tx, ids)
		return err
	}); err != nil {
		t.Fatalf("TransmitClearTx: %v", err)
	}
	return clear
}

func gateFacts(t *testing.T, pool *pgxpool.Pool, tenantID, invoiceID, subject string) (GateFacts, error) {
	t.Helper()
	var gf GateFacts
	err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		gf, err = GateFactsTx(context.Background(), tx, invoiceID, subject)
		return err
	})
	return gf, err
}

// heldRoleKeys runs HeldRoleKeysTx inside a fresh tenant-scoped transaction -- gateFacts'
// shape, since these seams take a tx, not a pool.
func heldRoleKeys(t *testing.T, pool *pgxpool.Pool, tenantID string, keys []string, subject string) map[string]bool {
	t.Helper()
	var held map[string]bool
	if err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		held, err = HeldRoleKeysTx(context.Background(), tx, keys, subject)
		return err
	}); err != nil {
		t.Fatalf("HeldRoleKeysTx: %v", err)
	}
	return held
}

func rowFacts(t *testing.T, pool *pgxpool.Pool, tenantID string, ids []string) map[string]RowFacts {
	t.Helper()
	var facts map[string]RowFacts
	if err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		facts, err = RowFactsTx(context.Background(), tx, ids)
		return err
	}); err != nil {
		t.Fatalf("RowFactsTx: %v", err)
	}
	return facts
}

// --- fixtures ----------------------------------------------------------------------

// gateTenant is newApproveFixture's policy half without an invoice: one sealed+active
// version holding a single approval step on roleKey, so a spec can arm several invoices
// off one version (approval_policy_versions_one_active caps a tenant at one).
type gateTenant struct {
	tenantID, entityID, roleID, versionID string
}

func newGateTenant(t *testing.T, super *pgxpool.Pool, name, roleKey, roleTitle string) gateTenant {
	t.Helper()
	var g gateTenant
	g.tenantID = policyTenant(t, super, name)
	g.entityID = seedBusinessEntity(t, super, g.tenantID, name+" Corp")
	g.roleID = seedWorkflowRole(t, super, g.tenantID, roleKey, roleTitle)

	policyID := seedApprovalPolicy(t, super, g.tenantID, name+" policy")
	g.versionID = seedApprovalPolicyVersionN(t, super, g.tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, g.tenantID, g.versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleKey), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, g.versionID)
	return g
}

// validatedInvoice seeds one invoice already at 'validated' -- seedInvoice defaults to
// 'draft', and both doors the gate guards only ever see a validated invoice.
func (g gateTenant) validatedInvoice(t *testing.T, super *pgxpool.Pool, number string) string {
	t.Helper()
	id := seedInvoice(t, super, g.tenantID, g.entityID, number)
	setInvoiceStatus(t, super, id, "validated")
	return id
}

// armOne arms one invoice off the tenant's active version and returns the run id.
func (g gateTenant) armOne(t *testing.T, app *pgxpool.Pool, invoiceID string) string {
	t.Helper()
	res, err := arm(t, app, g.tenantID, invoiceID, "fp-"+invoiceID, "fixture-arm")
	if err != nil {
		t.Fatalf("arm %s: %v", invoiceID, err)
	}
	if res.RunID == "" {
		t.Fatalf("arm %s returned no run -- the fixture's version is not active", invoiceID)
	}
	return res.RunID
}

// setRunStepState force-writes one step's state, for the fixtures that need a run left
// open with nothing pending (decision_test.go:934's raw UPDATE, hoisted).
func setRunStepState(t *testing.T, super *pgxpool.Pool, stepID, state string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_run_steps SET state = $2,
		        satisfied_at = CASE WHEN $2 = 'satisfied' THEN now() END,
		        satisfied_by = CASE WHEN $2 = 'satisfied' THEN 'fixture' END
		  WHERE id = $1`, stepID, state)
	if err != nil {
		t.Fatalf("set run step %s state to %s: %v", stepID, state, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set run step %s state affected %d rows, want 1", stepID, tag.RowsAffected())
	}
}

// stampRunOpenedAt forces a run's opened_at, so "the newest run wins" is observable
// rather than at the mercy of now() resolution (stampCreatedAt's precedent).
func stampRunOpenedAt(t *testing.T, super *pgxpool.Pool, runID string, at time.Time) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET opened_at = $2 WHERE id = $1`, runID, at)
	if err != nil {
		t.Fatalf("stamp opened_at on run %s: %v", runID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("stamp opened_at on run %s affected %d rows, want 1", runID, tag.RowsAffected())
	}
}

// activeApprover seeds one active reviewer and returns its user id.
func activeApprover(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	userID := uuid.NewString()
	seedMembership(t, super, tenantID, userID, "reviewer", "active")
	return userID
}

func ordText(p *int) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%d", *p)
}

func ordEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func titleText(p *string) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprintf("%q", *p)
}

// --- AC-2: the predicate -----------------------------------------------------------

// TestTransmitClear_TruthTable: only (active policy, no approved run) is blocked.
func TestTransmitClear_TruthTable(t *testing.T) {
	cases := []struct {
		policyActive, approvedRun, want bool
	}{
		{false, false, true},
		{false, true, true},
		{true, false, false},
		{true, true, true},
	}
	for _, c := range cases {
		if got := TransmitClear(c.policyActive, c.approvedRun); got != c.want {
			t.Errorf("TransmitClear(policyActive=%t, approvedRun=%t) = %t, want %t",
				c.policyActive, c.approvedRun, got, c.want)
		}
	}
}

// --- AC-3: TransmitClearTx ---------------------------------------------------------

// TestTransmitClearTx_NoActivePolicyClearsEveryInvoice: with no active version the
// second statement must never run -- a tenant that has not published a policy pays one
// statement, and every id is clear.
func TestTransmitClearTx_NoActivePolicyClearsEveryInvoice(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-08-01 no-active-policy")
	entityID := seedBusinessEntity(t, super, tenantID, "No Active Policy Corp")
	policyID := seedApprovalPolicy(t, super, tenantID, "Unpublished policy")
	// A DRAFT version, never activated: the filter under test is is_active, not
	// "the tenant has a version".
	seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		id := seedInvoice(t, super, tenantID, entityID, fmt.Sprintf("no-policy-invoice-%d", i))
		setInvoiceStatus(t, super, id, "validated")
		ids = append(ids, id)
	}

	tracedApp, rec := tracedAppPool(t)
	rec.reset()
	clear := transmitClear(t, tracedApp, tenantID, ids)

	for i, id := range ids {
		if !clear[id] {
			t.Errorf("clear[invoice %d] = %t, want true -- no active policy clears everything", i, clear[id])
		}
	}
	if got := len(rec.mentioning("FROM approval_policy_versions")); got != 1 {
		t.Errorf("statements mentioning %q = %d, want exactly 1", "FROM approval_policy_versions", got)
	}
	if got := len(rec.mentioning("FROM invoices")); got != 0 {
		t.Errorf("statements mentioning %q = %d, want 0 -- the short-circuit must skip the run read", "FROM invoices", got)
	}
}

// TestTransmitClearTx_ActivePolicyBlocksAnInvoiceWithNoRun: the seed's shape (validated
// with no approval_runs row) is blocked, and is PRESENT in the map -- absence means
// "invisible under RLS", a different fact.
func TestTransmitClearTx_ActivePolicyBlocksAnInvoiceWithNoRun(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 no-run-blocked", "no-run-role", "No Run Role")
	invoiceID := g.validatedInvoice(t, super, "no-run-invoice")

	clear := transmitClear(t, app, g.tenantID, []string{invoiceID})
	got, ok := clear[invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries) -- an RLS-visible invoice must be present and false", len(clear))
	}
	if got {
		t.Errorf("clear = true, want false -- an active policy blocks an invoice with no run")
	}
}

// TestTransmitClearTx_ActivePolicyClearsAnApprovedRun (AC-2's true disjunct).
func TestTransmitClearTx_ActivePolicyClearsAnApprovedRun(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 approved-clear", "approved-clear-role")
	closeApprovalRunFor(t, super, f.runID, "approved", "fixture-approver")

	clear := transmitClear(t, app, f.tenantID, []string{f.invoiceID})
	if !clear[f.invoiceID] {
		t.Errorf("clear = %t, want true -- an approved run clears the gate", clear[f.invoiceID])
	}
}

func TestTransmitClearTx_OpenRunIsNotClear(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 open-not-clear", "open-not-clear-role")

	clear := transmitClear(t, app, f.tenantID, []string{f.invoiceID})
	got, ok := clear[f.invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries) -- an RLS-visible invoice must be present and false", len(clear))
	}
	if got {
		t.Errorf("clear = true, want false -- an open run has not approved anything yet")
	}
}

// TestTransmitClearTx_CancelledAndRejectedRunsAreNotClear: neither closed-but-unapproved
// state clears. The approved third invoice is the positive control -- without it an
// implementation that maps everything false would pass.
func TestTransmitClearTx_CancelledAndRejectedRunsAreNotClear(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 closed-not-clear", "closed-role", "Closed Role")

	cancelled := g.validatedInvoice(t, super, "cancelled-run-invoice")
	closeApprovalRunFor(t, super, g.armOne(t, app, cancelled), "cancelled", "editor")

	rejected := g.validatedInvoice(t, super, "rejected-run-invoice")
	closeApprovalRunFor(t, super, g.armOne(t, app, rejected), "rejected", "reviewer-1")

	approved := g.validatedInvoice(t, super, "approved-run-invoice")
	closeApprovalRunFor(t, super, g.armOne(t, app, approved), "approved", "reviewer-1")

	clear := transmitClear(t, app, g.tenantID, []string{cancelled, rejected, approved})
	for _, id := range []string{cancelled, rejected} {
		got, ok := clear[id]
		if !ok {
			t.Errorf("invoice %s absent from the map -- an RLS-visible invoice must be present and false", id)
			continue
		}
		if got {
			t.Errorf("clear[%s] = true, want false", id)
		}
	}
	if !clear[approved] {
		t.Errorf("clear[approved] = %t, want true -- the control that stops an all-false implementation passing", clear[approved])
	}
}

// TestTransmitClearTx_ConstantInBatchSize (AC-3): two statements at 50 ids, not 51.
func TestTransmitClearTx_ConstantInBatchSize(t *testing.T) {
	super, _ := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 batch-constant", "batch-role", "Batch Role")

	ids := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		ids = append(ids, g.validatedInvoice(t, super, fmt.Sprintf("batch-invoice-%d", i)))
	}

	tracedApp, rec := tracedAppPool(t)
	rec.reset()
	clear := transmitClear(t, tracedApp, g.tenantID, ids)

	if len(clear) != 50 {
		t.Errorf("len(clear) = %d, want 50 -- every visible id answers", len(clear))
	}
	for _, table := range []string{"FROM approval_policy_versions", "FROM invoices"} {
		if got := len(rec.mentioning(table)); got != 1 {
			t.Errorf("statements mentioning %q = %d, want exactly 1 (50 ids must not inflate this)", table, got)
		}
	}
}

// TestTransmitClearTx_ValidatedInvoiceNeverHoldsTwoLiveRuns: an approve/edit/re-validate
// cycle leaves EXISTS-approved and latest-run agreeing, because the edit's
// CancelLiveRunTx cancelled the stale approved run.
func TestTransmitClearTx_ValidatedInvoiceNeverHoldsTwoLiveRuns(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 two-live-runs", "two-live-runs-role")
	closeApprovalRunFor(t, super, f.runID, "approved", "fixture-approver")

	if _, err := cancel(t, app, f.tenantID, f.invoiceID, "editor"); err != nil {
		t.Fatalf("CancelLiveRunTx (the edit): %v", err)
	}
	if _, err := arm(t, app, f.tenantID, f.invoiceID, "fp-rearmed", "re-validate"); err != nil {
		t.Fatalf("re-arm: %v", err)
	}

	var approvedRuns int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'approved'`,
		f.invoiceID).Scan(&approvedRuns); err != nil {
		t.Fatalf("count approved runs: %v", err)
	}
	if approvedRuns != 0 {
		t.Fatalf("approved runs after the edit = %d, want 0 -- the fixture never reached the state under test", approvedRuns)
	}

	clear := transmitClear(t, app, f.tenantID, []string{f.invoiceID})
	got, ok := clear[f.invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries) -- an RLS-visible invoice must be present and false", len(clear))
	}
	if got {
		t.Errorf("clear = true, want false -- the stale approved run was cancelled by the edit")
	}
}

// TestTransmitClearTx_IsTenantScopedByRLS (AC-6): tenant B holds its OWN active version,
// so the second statement really runs -- without it the short-circuit would map A's id
// true and the spec would prove nothing about RLS. B's own approved invoice is the
// positive control that the statement returned rows at all.
func TestTransmitClearTx_IsTenantScopedByRLS(t *testing.T) {
	super, app := dbTestPools(t)

	a := newGateTenant(t, super, "APPR-08-01 rls-clear-tenant-a", "rls-a-role", "RLS A Role")
	aInvoice := a.validatedInvoice(t, super, "rls-a-invoice")
	closeApprovalRunFor(t, super, a.armOne(t, app, aInvoice), "approved", "reviewer-a")

	b := newGateTenant(t, super, "APPR-08-01 rls-clear-tenant-b", "rls-b-role", "RLS B Role")
	bInvoice := b.validatedInvoice(t, super, "rls-b-invoice")
	closeApprovalRunFor(t, super, b.armOne(t, app, bInvoice), "approved", "reviewer-b")

	clear := transmitClear(t, app, b.tenantID, []string{aInvoice, bInvoice})
	if _, ok := clear[aInvoice]; ok {
		t.Errorf("tenant A's invoice is in tenant B's map (= %t) -- RLS must hide it entirely, and absence is fail-closed", clear[aInvoice])
	}
	if !clear[bInvoice] {
		t.Errorf("clear[B's own approved invoice] = %t, want true -- the control proving the run statement ran", clear[bInvoice])
	}
}

// --- AC-4: GateFactsTx -------------------------------------------------------------

// TestGateFactsTx_MirrorsDecideTxRefusalLadder: each rung of decideTx's ladder that
// GateFacts can observe, plus the clear case. AXIS 1 is deliberately absent -- the gate's
// first rung is internal/invoice's own callerRole, and AXIS 2 already implies it.
func TestGateFactsTx_MirrorsDecideTxRefusalLadder(t *testing.T) {
	super, app := dbTestPools(t)

	cases := []struct {
		name           string
		setup          func(t *testing.T) (tenantID, invoiceID, subject string)
		wantRunState   string
		wantPendingOrd *int
		wantHoldsRole  bool
		wantDecideErr  error // nil = Decide succeeds
	}{
		{
			name: "no run at all",
			setup: func(t *testing.T) (string, string, string) {
				g := newGateTenant(t, super, "APPR-08-01 ladder-no-run", "ladder-no-run-role", "Ladder No Run")
				return g.tenantID, g.validatedInvoice(t, super, "ladder-no-run-invoice"), activeApprover(t, super, g.tenantID)
			},
			wantRunState: "", wantPendingOrd: nil, wantHoldsRole: false,
			wantDecideErr: ErrRunNotFound,
		},
		{
			name: "run closed",
			setup: func(t *testing.T) (string, string, string) {
				f := newApproveFixture(t, super, app, "APPR-08-01 ladder-closed", "ladder-closed-role")
				subject := activeApprover(t, super, f.tenantID)
				staffWorkflowRole(t, super, f.tenantID, f.roleID, subject, 0)
				closeApprovalRunFor(t, super, f.runID, "cancelled", "editor")
				return f.tenantID, f.invoiceID, subject
			},
			wantRunState: "cancelled", wantPendingOrd: ptr(0), wantHoldsRole: true,
			wantDecideErr: ErrRunClosed,
		},
		{
			name: "nothing pending",
			setup: func(t *testing.T) (string, string, string) {
				f := newApproveFixture(t, super, app, "APPR-08-01 ladder-nothing-pending", "ladder-pending-role")
				subject := activeApprover(t, super, f.tenantID)
				staffWorkflowRole(t, super, f.tenantID, f.roleID, subject, 0)
				setRunStepState(t, super, f.stepID, "satisfied")
				return f.tenantID, f.invoiceID, subject
			},
			wantRunState: "open", wantPendingOrd: nil, wantHoldsRole: false,
			wantDecideErr: ErrRunClosed,
		},
		{
			name: "caller does not hold the step's role",
			setup: func(t *testing.T) (string, string, string) {
				f := newApproveFixture(t, super, app, "APPR-08-01 ladder-not-holder", "ladder-holder-role")
				return f.tenantID, f.invoiceID, activeApprover(t, super, f.tenantID)
			},
			wantRunState: "open", wantPendingOrd: ptr(0), wantHoldsRole: false,
			wantDecideErr: ErrNotRoleHolder,
		},
		{
			name: "clear",
			setup: func(t *testing.T) (string, string, string) {
				f := newApproveFixture(t, super, app, "APPR-08-01 ladder-clear", "ladder-clear-role")
				subject := activeApprover(t, super, f.tenantID)
				staffWorkflowRole(t, super, f.tenantID, f.roleID, subject, 0)
				return f.tenantID, f.invoiceID, subject
			},
			wantRunState: "open", wantPendingOrd: ptr(0), wantHoldsRole: true,
			wantDecideErr: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tenantID, invoiceID, subject := c.setup(t)

			gf, err := gateFacts(t, app, tenantID, invoiceID, subject)
			if err != nil {
				t.Fatalf("GateFactsTx: %v, want nil -- a refused rung is fields, never an error", err)
			}
			if gf.RunState != c.wantRunState {
				t.Errorf("RunState = %q, want %q", gf.RunState, c.wantRunState)
			}
			if !ordEq(gf.PendingStepOrd, c.wantPendingOrd) {
				t.Errorf("PendingStepOrd = %s, want %s", ordText(gf.PendingStepOrd), ordText(c.wantPendingOrd))
			}
			if gf.CallerHoldsRole != c.wantHoldsRole {
				t.Errorf("CallerHoldsRole = %t, want %t", gf.CallerHoldsRole, c.wantHoldsRole)
			}
			// Every fixture publishes a version, so this is the control that keeps the
			// all-zero "no run at all" rung from passing vacuously.
			if !gf.PolicyActive {
				t.Errorf("PolicyActive = false, want true -- every fixture here has a sealed, active version")
			}

			// Decide runs second: on the clear case it closes the run.
			_, derr := approve(t, app, tenantID, subject, invoiceID, nil)
			if c.wantDecideErr == nil {
				if derr != nil {
					t.Errorf("Decide = %v, want success -- GateFacts says the gate is clear", derr)
				}
				return
			}
			if !errors.Is(derr, c.wantDecideErr) {
				t.Errorf("Decide = %v, want %v -- GateFacts and decideTx must refuse on the same rung", derr, c.wantDecideErr)
			}
		})
	}
}

// TestGateFactsTx_HoldsRoleRequiresActiveApprover (AC-4): AXIS 2's m.status/m.role
// predicates bite. The staffed active reviewer is the polarity control.
func TestGateFactsTx_HoldsRoleRequiresActiveApprover(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 holds-role-axis-two", "axis-two-role")

	suspendedAdmin := uuid.NewString()
	seedMembership(t, super, f.tenantID, suspendedAdmin, "admin", "suspended")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, suspendedAdmin, 0)

	activePreparer := uuid.NewString()
	seedMembership(t, super, f.tenantID, activePreparer, "preparer", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, activePreparer, 1)

	activeReviewer := activeApprover(t, super, f.tenantID)
	staffWorkflowRole(t, super, f.tenantID, f.roleID, activeReviewer, 2)

	for _, c := range []struct {
		name, subject string
		want          bool
	}{
		{"suspended admin", suspendedAdmin, false},
		{"active preparer", activePreparer, false},
		{"active reviewer", activeReviewer, true},
	} {
		gf, err := gateFacts(t, app, f.tenantID, f.invoiceID, c.subject)
		if err != nil {
			t.Fatalf("GateFactsTx as %s: %v", c.name, err)
		}
		if gf.CallerHoldsRole != c.want {
			t.Errorf("CallerHoldsRole for the %s = %t, want %t", c.name, gf.CallerHoldsRole, c.want)
		}
	}
}

func TestGateFactsTx_HoldsRoleTrueForStaffedActiveApprover(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 holds-role-true", "holds-role-true-role")
	subject := activeApprover(t, super, f.tenantID)
	staffWorkflowRole(t, super, f.tenantID, f.roleID, subject, 0)

	gf, err := gateFacts(t, app, f.tenantID, f.invoiceID, subject)
	if err != nil {
		t.Fatalf("GateFactsTx: %v", err)
	}
	if !gf.CallerHoldsRole {
		t.Errorf("CallerHoldsRole = false, want true -- a staffed active reviewer holds the step's role")
	}
	if gf.RunState != "open" {
		t.Errorf("RunState = %q, want open", gf.RunState)
	}
	if !ordEq(gf.PendingStepOrd, ptr(0)) {
		t.Errorf("PendingStepOrd = %s, want 0", ordText(gf.PendingStepOrd))
	}
}

// TestGateFactsTx_NoRunIsEmptyStateNotAnError: an unarmed invoice is a fact, not
// ErrRunNotFound. The armed sibling is the control that "" is a read, not a zero value.
func TestGateFactsTx_NoRunIsEmptyStateNotAnError(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 no-run-facts", "no-run-facts-role", "No Run Facts")
	subject := activeApprover(t, super, g.tenantID)

	unarmed := g.validatedInvoice(t, super, "no-run-facts-invoice")
	gf, err := gateFacts(t, app, g.tenantID, unarmed, subject)
	if err != nil {
		t.Fatalf("GateFactsTx on an invoice with no run: %v, want nil", err)
	}
	if gf.RunState != "" {
		t.Errorf("RunState = %q, want empty", gf.RunState)
	}
	if gf.PendingStepOrd != nil {
		t.Errorf("PendingStepOrd = %s, want nil", ordText(gf.PendingStepOrd))
	}
	if gf.ApprovedRun {
		t.Errorf("ApprovedRun = true, want false -- no runs at all means no approved run")
	}
	if !gf.PolicyActive {
		t.Errorf("PolicyActive = false, want true -- the fixture's version is sealed and active")
	}

	armed := g.validatedInvoice(t, super, "no-run-facts-armed-invoice")
	g.armOne(t, app, armed)
	sibling, err := gateFacts(t, app, g.tenantID, armed, subject)
	if err != nil {
		t.Fatalf("GateFactsTx on the armed sibling: %v", err)
	}
	if sibling.RunState != "open" {
		t.Errorf("RunState of the armed sibling = %q, want open -- the control proving empty is read, not defaulted", sibling.RunState)
	}
}

// TestGateFactsTx_FourStatements (AC-3): one per table on the fully-populated path.
func TestGateFactsTx_FourStatements(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 gate-statement-count", "gate-count-role")
	subject := activeApprover(t, super, f.tenantID)
	staffWorkflowRole(t, super, f.tenantID, f.roleID, subject, 0)

	tracedApp, rec := tracedAppPool(t)
	rec.reset()
	gf, err := gateFacts(t, tracedApp, f.tenantID, f.invoiceID, subject)
	if err != nil {
		t.Fatalf("GateFactsTx: %v", err)
	}
	if !gf.CallerHoldsRole {
		t.Fatalf("CallerHoldsRole = false -- the fixture never reached the four-statement path")
	}

	for _, table := range []string{
		"FROM approval_policy_versions", "FROM approval_runs",
		"FROM approval_run_steps", "FROM workflow_roles",
	} {
		if got := len(rec.mentioning(table)); got != 1 {
			t.Errorf("statements mentioning %q = %d, want exactly 1", table, got)
		}
	}
}

// TestGateFactsTx_ApprovedRunIsExistsNotLatestRun: ApprovedRun is EXISTS over every run,
// RunState is the newest one -- the two disjuncts are read by different rules on purpose.
func TestGateFactsTx_ApprovedRunIsExistsNotLatestRun(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 exists-not-latest", "exists-role", "Exists Role")
	invoiceID := g.validatedInvoice(t, super, "exists-not-latest-invoice")

	approvedRun := seedApprovalRun(t, super, g.tenantID, invoiceID, g.versionID)
	closeApprovalRunFor(t, super, approvedRun, "approved", "reviewer-1")
	stampRunOpenedAt(t, super, approvedRun, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))

	cancelledRun := seedApprovalRun(t, super, g.tenantID, invoiceID, g.versionID)
	closeApprovalRunFor(t, super, cancelledRun, "cancelled", "editor")
	stampRunOpenedAt(t, super, cancelledRun, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))

	gf, err := gateFacts(t, app, g.tenantID, invoiceID, activeApprover(t, super, g.tenantID))
	if err != nil {
		t.Fatalf("GateFactsTx: %v", err)
	}
	if !gf.ApprovedRun {
		t.Errorf("ApprovedRun = false, want true -- an older approved run still exists")
	}
	if gf.RunState != "cancelled" {
		t.Errorf("RunState = %q, want cancelled -- the newest run wins the header", gf.RunState)
	}
}

// TestGateFactsTx_NullRoleKeyOnThePendingStepIsNotHolding: decideTx's own roleKey != nil
// guard -- AXIS 2 never runs and nobody holds the step. RowFactsTx deliberately differs
// (TestRowFactsTx_NullRoleKeyLeavesTitleAndWarnUnset): it SKIPS a NULL key rather than
// resolving it to "Role no longer exists".
func TestGateFactsTx_NullRoleKeyOnThePendingStepIsNotHolding(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-08-01 gate-null-role-key")
	entityID := seedBusinessEntity(t, super, tenantID, "Gate Null Role Key Corp")
	policyID := seedApprovalPolicy(t, super, tenantID, "Null role key policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: nil, SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "gate-null-role-key-invoice")
	setInvoiceStatus(t, super, invoiceID, "validated")
	if _, err := arm(t, app, tenantID, invoiceID, "fp-null-role-key", "fixture-arm"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin", "active")

	tracedApp, rec := tracedAppPool(t)
	rec.reset()
	gf, err := gateFacts(t, tracedApp, tenantID, invoiceID, subject)
	if err != nil {
		t.Fatalf("GateFactsTx: %v", err)
	}
	if gf.RunState != "open" {
		t.Errorf("RunState = %q, want open", gf.RunState)
	}
	if !ordEq(gf.PendingStepOrd, ptr(0)) {
		t.Errorf("PendingStepOrd = %s, want 0 -- the step is pending, it just names no role", ordText(gf.PendingStepOrd))
	}
	if gf.CallerHoldsRole {
		t.Errorf("CallerHoldsRole = true, want false -- a NULL role key is held by nobody")
	}
	if got := len(rec.mentioning("FROM workflow_roles")); got != 0 {
		t.Errorf("statements mentioning %q = %d, want 0 -- AXIS 2 is guarded by roleKey != nil", "FROM workflow_roles", got)
	}

	if _, derr := approve(t, app, tenantID, subject, invoiceID, nil); !errors.Is(derr, ErrNotRoleHolder) {
		t.Errorf("Decide = %v, want ErrNotRoleHolder -- decideTx leaves holds false on a NULL key", derr)
	}
}

// --- APPR-12-09: HeldRoleKeysTx, the widened AXIS-2 read ----------------------------
//
// GateFactsTx's AXIS-2 EXISTS query moves here, widened from `wr.key = $1` to
// `wr.key = ANY($1::text[])`, so ONE list request can resolve the whole page's pending
// role keys at once. GateFactsTx is re-pointed at it and keeps every rung it had
// (TestGateFactsTx_* above, and TestGateFactsTx_SoftDeletedRoleIsNotHeld in
// gate_adversarial_test.go, are that re-pointing's green-before-and-after guard).
//
// Absence in the returned map and an explicit false are the SAME answer -- `held[key]`
// reads false for both -- so nothing below pins which one the implementation picks.

// TestHeldRoleKeysTx_OneStatementRegardlessOfKeyAndHolderCount (A09-9): eight role keys,
// two active holders each, ONE statement. A per-key loop answers IDENTICALLY on this map
// and costs eight round trips per list request, so the statement count is the only oracle
// that separates the two -- the same argument
// TestRowFactsTx_FiveStatementsRegardlessOfRowAndRoleCount makes for RowFactsTx.
//
// The map assertions are the control: a read that returned nothing at all would pay one
// statement too.
func TestHeldRoleKeysTx_OneStatementRegardlessOfKeyAndHolderCount(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-12-09 held-keys-statement-count")

	subject := activeApprover(t, super, tenantID)
	keys := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("held-count-role-%d", i)
		keys = append(keys, key)
		roleID := seedWorkflowRole(t, super, tenantID, key, key)
		// Two OTHER active holders per role, so the batch spans 16 membership rows and a
		// per-holder fan-out would show up in the count too.
		for h := 0; h < 2; h++ {
			staffWorkflowRole(t, super, tenantID, roleID, activeApprover(t, super, tenantID), h)
		}
		// The caller holds the first four only, so the answer is not uniform.
		if i < 4 {
			staffWorkflowRole(t, super, tenantID, roleID, subject, 2)
		}
	}

	tracedApp, rec := tracedAppPool(t)
	rec.reset()
	held := heldRoleKeys(t, tracedApp, tenantID, keys, subject)

	for i, key := range keys {
		want := i < 4
		if held[key] != want {
			t.Errorf("held[%q] = %t, want %t -- the caller is staffed to the first four roles only", key, held[key], want)
		}
	}
	// AXIS 2 is ONE statement over three tables -- workflow_roles in the FROM, the other
	// two JOINed -- so each clause must appear exactly once, not once per key.
	for _, clause := range []string{"FROM workflow_roles", "JOIN workflow_role_members", "JOIN memberships"} {
		if got := len(rec.mentioning(clause)); got != 1 {
			t.Errorf("statements mentioning %q = %d, want exactly 1 (eight keys across sixteen holders must not inflate this)", clause, got)
		}
	}
}

// TestHeldRoleKeysTx_MixedKeySetAnswersPerKey (A09-9b) is the failure mode the widening
// INVENTS, and no shipped spec can reach it: with one key per call, "the caller holds this
// key" and "the caller holds SOMETHING in the batch" are the same sentence. With a key
// SET they are not, and an EXISTS-shaped answer mapped back over the REQUESTED keys rather
// than the RETURNED rows marks every key in the batch held.
//
// Four keys, four different reasons, in one call:
//
//	live + staffed          -> true   (the control: an all-false read fails here)
//	live + staffed to someone else -> false
//	SOFT-DELETED + staffed  -> false  (wr.deleted_at IS NULL, carried through the widening)
//	no such role at all     -> false  (absent, never an error)
func TestHeldRoleKeysTx_MixedKeySetAnswersPerKey(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-12-09 held-keys-mixed-set")
	subject := activeApprover(t, super, tenantID)

	staffWorkflowRole(t, super, tenantID, seedWorkflowRole(t, super, tenantID, "mixed-held", "Mixed Held"), subject, 0)
	staffWorkflowRole(t, super, tenantID, seedWorkflowRole(t, super, tenantID, "mixed-other", "Mixed Other"),
		activeApprover(t, super, tenantID), 0)

	doomed := seedWorkflowRole(t, super, tenantID, "mixed-deleted", "Mixed Deleted")
	staffWorkflowRole(t, super, tenantID, doomed, subject, 0)
	softDeleteWorkflowRole(t, super, doomed)

	keys := []string{"mixed-held", "mixed-other", "mixed-deleted", "mixed-nonexistent"}
	held := heldRoleKeys(t, app, tenantID, keys, subject)
	for key, want := range map[string]bool{
		"mixed-held": true, "mixed-other": false, "mixed-deleted": false, "mixed-nonexistent": false,
	} {
		if held[key] != want {
			t.Errorf("held[%q] = %t, want %t", key, held[key], want)
		}
	}
}

// --- AC-5: RowFactsTx --------------------------------------------------------------

func TestRowFactsTx_LatestRunPerInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 rows-latest-run", "rows-latest-role", "Rows Latest Role")
	invoiceID := g.validatedInvoice(t, super, "rows-latest-run-invoice")

	older := seedApprovalRun(t, super, g.tenantID, invoiceID, g.versionID)
	closeApprovalRunFor(t, super, older, "cancelled", "editor")
	stampRunOpenedAt(t, super, older, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))

	newer := seedApprovalRun(t, super, g.tenantID, invoiceID, g.versionID) // defaults to open
	stampRunOpenedAt(t, super, newer, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))

	facts := rowFacts(t, app, g.tenantID, []string{invoiceID})
	rf, ok := facts[invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries), want one entry", len(facts))
	}
	if rf.RunState != "open" {
		t.Errorf("RunState = %q, want open -- the newer run wins", rf.RunState)
	}
}

// TestRowFactsTx_PendingStepAndTitle: the first PENDING approval step, not the first
// step -- ord 0 is a skipped notify. Warn false here is the polarity control for
// TestRowFactsTx_UnstaffedRoleSetsWarn.
func TestRowFactsTx_PendingStepAndTitle(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-08-01 rows-pending-title")
	entityID := seedBusinessEntity(t, super, tenantID, "Rows Pending Title Corp")

	roleID := seedWorkflowRole(t, super, tenantID, "cfo", "CFO")
	holder := activeApprover(t, super, tenantID)
	staffWorkflowRole(t, super, tenantID, roleID, holder, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, "CFO policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "notify", NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("email"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("cfo"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "rows-pending-title-invoice")
	setInvoiceStatus(t, super, invoiceID, "validated")
	if _, err := arm(t, app, tenantID, invoiceID, "fp-pending-title", "fixture-arm"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	facts := rowFacts(t, app, tenantID, []string{invoiceID})
	rf, ok := facts[invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries), want one entry", len(facts))
	}
	if !ordEq(rf.PendingOrd, ptr(1)) {
		t.Errorf("PendingOrd = %s, want 1 -- ord 0 is a skipped notify", ordText(rf.PendingOrd))
	}
	if rf.PendingRoleTitle == nil || *rf.PendingRoleTitle != "CFO" {
		t.Errorf("PendingRoleTitle = %s, want %q -- the live title, not the key", titleText(rf.PendingRoleTitle), "CFO")
	}
	if rf.PendingHolderWarn {
		t.Errorf("PendingHolderWarn = true, want false -- the role holds an active reviewer")
	}
	if rf.DueAt == nil {
		t.Errorf("DueAt = nil, want the pending step's due_at (sla_hours 48)")
	}
}

// TestRowFactsTx_UnstaffedRoleSetsWarn: newApproveFixture staffs nobody, so the run
// panel's own "nobody assigned" warn is what a list row must show. The title assertion
// proves role resolution ran rather than warn defaulting true.
func TestRowFactsTx_UnstaffedRoleSetsWarn(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 rows-unstaffed-warn", "unstaffed-warn-role")

	facts := rowFacts(t, app, f.tenantID, []string{f.invoiceID})
	rf, ok := facts[f.invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries), want one entry", len(facts))
	}
	if !rf.PendingHolderWarn {
		t.Errorf("PendingHolderWarn = false, want true -- the role has no active approver")
	}
	if rf.PendingRoleTitle == nil || *rf.PendingRoleTitle != "unstaffed-warn-role" {
		t.Errorf("PendingRoleTitle = %s, want %q", titleText(rf.PendingRoleTitle), "unstaffed-warn-role")
	}
}

// TestRowFactsTx_OverdueTracksStepStateNotJustDueAt: a settled step past its due_at is
// not overdue -- the pending-only read is what enforces it, so DueAt is nil too.
func TestRowFactsTx_OverdueTracksStepStateNotJustDueAt(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 rows-overdue-state", "overdue-role", "Overdue Role")

	settledInvoice := g.validatedInvoice(t, super, "rows-overdue-settled-invoice")
	settledRun := g.armOne(t, app, settledInvoice)
	pendingInvoice := g.validatedInvoice(t, super, "rows-overdue-pending-invoice")
	pendingRun := g.armOne(t, app, pendingInvoice)

	past := time.Now().Add(-72 * time.Hour)
	settledStep := runStepID(t, super, settledRun, 0)
	backdateRunStepDueAt(t, super, settledStep, past)
	setRunStepState(t, super, settledStep, "satisfied")
	backdateRunStepDueAt(t, super, runStepID(t, super, pendingRun, 0), past)

	facts := rowFacts(t, app, g.tenantID, []string{settledInvoice, pendingInvoice})

	settled, ok := facts[settledInvoice]
	if !ok {
		t.Fatalf("the settled invoice is absent from the map (%d entries) -- its run still exists", len(facts))
	}
	if settled.Overdue {
		t.Errorf("Overdue for the settled step = true, want false -- overdue tracks state, not just due_at")
	}
	if settled.DueAt != nil {
		t.Errorf("DueAt for the settled step = %v, want nil -- nothing is pending", settled.DueAt)
	}

	pending, ok := facts[pendingInvoice]
	if !ok {
		t.Fatalf("the pending invoice is absent from the map (%d entries)", len(facts))
	}
	if !pending.Overdue {
		t.Errorf("Overdue for the pending step = false, want true -- its due_at is 72h past")
	}
}

// TestRowFactsTx_FiveStatementsRegardlessOfRowAndRoleCount (AC-3): 30 invoices whose
// first pending step spans four distinct roles -- a per-row or per-role query inflates
// this, and its RESULTS would be identical.
func TestRowFactsTx_FiveStatementsRegardlessOfRowAndRoleCount(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-08-01 rows-statement-count")
	entityID := seedBusinessEntity(t, super, tenantID, "Rows Statement Count Corp")

	roleKeys := []string{"row-role-a", "row-role-b", "row-role-c", "row-role-d"}
	for _, key := range roleKeys {
		roleID := seedWorkflowRole(t, super, tenantID, key, key)
		for i := 0; i < 2; i++ {
			staffWorkflowRole(t, super, tenantID, roleID, activeApprover(t, super, tenantID), i)
		}
	}

	policyID := seedApprovalPolicy(t, super, tenantID, "Rows statement count policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	for i, key := range roleKeys {
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: i, Kind: "approval", WorkflowRoleKey: ptr(key), SLAHours: ptr(24),
		})
	}
	activateApprovalPolicyVersion(t, super, versionID)

	ids := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		invoiceID := seedInvoice(t, super, tenantID, entityID, fmt.Sprintf("rows-count-invoice-%d", i))
		setInvoiceStatus(t, super, invoiceID, "validated")
		res, err := arm(t, app, tenantID, invoiceID, fmt.Sprintf("fp-rows-count-%d", i), "fixture-arm")
		if err != nil {
			t.Fatalf("arm invoice %d: %v", i, err)
		}
		// Satisfying a prefix moves the FIRST pending step's ord, so the batch spans all
		// four role keys instead of only the version's ord-0 role.
		for ord := 0; ord < i%4; ord++ {
			setRunStepState(t, super, runStepID(t, super, res.RunID, ord), "satisfied")
		}
		ids = append(ids, invoiceID)
	}

	tracedApp, rec := tracedAppPool(t)
	rec.reset()
	facts := rowFacts(t, tracedApp, tenantID, ids)

	if len(facts) != 30 {
		t.Errorf("len(facts) = %d, want 30", len(facts))
	}
	seen := map[string]bool{}
	for _, rf := range facts {
		if rf.PendingRoleTitle != nil {
			seen[*rf.PendingRoleTitle] = true
		}
	}
	if len(seen) != 4 {
		t.Errorf("distinct pending role titles = %d (%v), want 4 -- the fixture never reached the multi-role path", len(seen), seen)
	}
	for _, table := range []string{
		"FROM approval_runs", "FROM approval_run_steps", "FROM workflow_roles",
		"FROM workflow_role_members", "FROM memberships",
	} {
		if got := len(rec.mentioning(table)); got != 1 {
			t.Errorf("statements mentioning %q = %d, want exactly 1 (30 rows across four roles must not inflate this)", table, got)
		}
	}
}

func TestRowFactsTx_InvoiceWithNoRunIsAbsentFromTheMap(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 rows-absent", "rows-absent-role", "Rows Absent Role")

	armed := g.validatedInvoice(t, super, "rows-absent-armed-invoice")
	g.armOne(t, app, armed)
	unarmedA := g.validatedInvoice(t, super, "rows-absent-unarmed-a")
	unarmedB := g.validatedInvoice(t, super, "rows-absent-unarmed-b")

	facts := rowFacts(t, app, g.tenantID, []string{armed, unarmedA, unarmedB})
	if len(facts) != 1 {
		t.Errorf("len(facts) = %d, want 1 -- an invoice with no run gets no entry", len(facts))
	}
	if _, ok := facts[armed]; !ok {
		t.Errorf("the armed invoice is absent from the map, want present")
	}
}

// TestRowFactsTx_DeletedRoleTitleAndWarnMatchTheRunPanel: a list row and the run panel
// read the same helpers, so they cannot disagree about a soft-deleted role.
func TestRowFactsTx_DeletedRoleTitleAndWarnMatchTheRunPanel(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 rows-deleted-role", "doomed-role")
	softDeleteWorkflowRole(t, super, f.roleID)

	facts := rowFacts(t, app, f.tenantID, []string{f.invoiceID})
	rf, ok := facts[f.invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries), want one entry", len(facts))
	}

	ctx, _ := callerCtx(t, super, f.tenantID, "preparer", "active")
	run, err := NewStore(app, stubFingerprinter, nil).ApprovalRun(ctx, f.invoiceID)
	if err != nil {
		t.Fatalf("ApprovalRun: %v", err)
	}
	if len(run.Steps) != 1 || run.Steps[0].WorkflowRoleTitle == nil || run.Steps[0].Holder == nil {
		t.Fatalf("run panel step = %+v, want one step carrying a resolved title and holder", run.Steps)
	}
	panel := run.Steps[0]

	if rf.PendingRoleTitle == nil || *rf.PendingRoleTitle != "Deleted role" {
		t.Errorf("PendingRoleTitle = %s, want %q", titleText(rf.PendingRoleTitle), "Deleted role")
	}
	if !rf.PendingHolderWarn {
		t.Errorf("PendingHolderWarn = false, want true -- the role no longer exists")
	}
	if rf.PendingRoleTitle != nil && *rf.PendingRoleTitle != *panel.WorkflowRoleTitle {
		t.Errorf("PendingRoleTitle = %s, panel step title = %q -- a row and the panel must agree",
			titleText(rf.PendingRoleTitle), *panel.WorkflowRoleTitle)
	}
	if rf.PendingHolderWarn != panel.Holder.Warn {
		t.Errorf("PendingHolderWarn = %t, panel step Holder.Warn = %t -- a row and the panel must agree",
			rf.PendingHolderWarn, panel.Holder.Warn)
	}
}

// TestRowFactsTx_NullRoleKeyLeavesTitleAndWarnUnset: a NULL key is SKIPPED, never run
// through resolveHolder -- that would wrongly render "Role no longer exists".
// GateFactsTx deliberately differs (TestGateFactsTx_NullRoleKeyOnThePendingStepIsNotHolding).
func TestRowFactsTx_NullRoleKeyLeavesTitleAndWarnUnset(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-08-01 rows-null-role-key")
	entityID := seedBusinessEntity(t, super, tenantID, "Rows Null Role Key Corp")
	policyID := seedApprovalPolicy(t, super, tenantID, "Rows null role key policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: nil, SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "rows-null-role-key-invoice")
	setInvoiceStatus(t, super, invoiceID, "validated")
	if _, err := arm(t, app, tenantID, invoiceID, "fp-rows-null-role-key", "fixture-arm"); err != nil {
		t.Fatalf("arm: %v", err)
	}

	facts := rowFacts(t, app, tenantID, []string{invoiceID})
	rf, ok := facts[invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries), want one entry", len(facts))
	}
	if rf.RunState != "open" {
		t.Errorf("RunState = %q, want open", rf.RunState)
	}
	if !ordEq(rf.PendingOrd, ptr(0)) {
		t.Errorf("PendingOrd = %s, want 0 -- the step is pending, it just names no role", ordText(rf.PendingOrd))
	}
	if rf.PendingRoleTitle != nil {
		t.Errorf("PendingRoleTitle = %s, want nil -- never %q for a step that names no role",
			titleText(rf.PendingRoleTitle), "Role no longer exists")
	}
	if rf.PendingHolderWarn {
		t.Errorf("PendingHolderWarn = true, want false -- a step that names no role warns about nothing")
	}
}

// TestRowFactsTx_IsTenantScopedByRLS (AC-6). B's own armed invoice is the control that
// the read returned rows at all.
func TestRowFactsTx_IsTenantScopedByRLS(t *testing.T) {
	super, app := dbTestPools(t)

	a := newGateTenant(t, super, "APPR-08-01 rls-rows-tenant-a", "rls-rows-a-role", "RLS Rows A")
	aInvoice := a.validatedInvoice(t, super, "rls-rows-a-invoice")
	a.armOne(t, app, aInvoice)

	b := newGateTenant(t, super, "APPR-08-01 rls-rows-tenant-b", "rls-rows-b-role", "RLS Rows B")
	bInvoice := b.validatedInvoice(t, super, "rls-rows-b-invoice")
	b.armOne(t, app, bInvoice)

	if facts := rowFacts(t, app, b.tenantID, []string{aInvoice}); len(facts) != 0 {
		t.Errorf("len(facts) over tenant A's invoice as tenant B = %d, want 0", len(facts))
	}

	mixed := rowFacts(t, app, b.tenantID, []string{aInvoice, bInvoice})
	if _, ok := mixed[aInvoice]; ok {
		t.Errorf("tenant A's invoice is in tenant B's map, want absent")
	}
	if _, ok := mixed[bInvoice]; !ok {
		t.Errorf("tenant B's own armed invoice is absent from its map -- the control proving the read ran")
	}
}

// --- AC-6: the static guard ---------------------------------------------------------

// TestGateFile_NoTenantIdPredicate: RLS is the only tenant scope. Static, so it also runs
// in CI's `go` job.
func TestGateFile_NoTenantIdPredicate(t *testing.T) {
	path := filepath.Join(repoRoot(t), "internal", "approval", "gate.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(raw)
	// Without this the scan would pass vacuously against an empty or renamed file.
	// HeldRoleKeysTx (APPR-12-09, A09-10) is on the list because AXIS 2 MOVES into it:
	// after the extraction it is the only copy of that query in this file, so a scan that
	// no longer saw it would be scanning the wrong thing.
	for _, sym := range []string{"func TransmitClearTx", "func GateFactsTx", "func RowFactsTx", "func HeldRoleKeysTx"} {
		if !strings.Contains(src, sym) {
			t.Fatalf("%s does not declare %q -- the scan below would prove nothing", path, sym)
		}
	}
	if strings.Contains(src, "tenant_id") {
		t.Errorf("gate.go mentions tenant_id -- RLS is the only tenant scope (store.go:27-30)")
	}
}
