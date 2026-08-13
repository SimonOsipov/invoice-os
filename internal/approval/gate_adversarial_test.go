package approval

// Adversarial coverage for the transmit gate, added in QA over gate_test.go's
// acceptance specs. Three of these kill mutants that the acceptance suite let live:
// AXIS 2's wr.deleted_at IS NULL, RowFactsTx's Overdue clock read, and both seams'
// kind = "approval" predicate.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- non-canonical ids: the two paths key the map differently ------------------------

// noPolicyTenant is a tenant with a business entity and no policy at all, so
// TransmitClearTx takes its short-circuit.
func noPolicyTenant(t *testing.T, super *pgxpool.Pool, name string) (tenantID, entityID string) {
	t.Helper()
	tenantID = policyTenant(t, super, name)
	return tenantID, seedBusinessEntity(t, super, tenantID, name+" Corp")
}

// TestTransmitClearTx_NonCanonicalIDKeysDivergeBetweenThePaths pins a DEFECT, not a
// contract: the short-circuit keys the map with the CALLER's id string while the run read
// keys it with Postgres's canonical lowercase. uuid.Parse at the door (handlers.go:1152)
// validates without normalising, so an uppercase id reaches here intact. It fails CLOSED
// -- an approved invoice is wrongly refused, never wrongly cleared. Normalise at the door
// (subtasks 03/04/08) and this test flips to "both paths agree".
func TestTransmitClearTx_NonCanonicalIDKeysDivergeBetweenThePaths(t *testing.T) {
	super, app := dbTestPools(t)

	// Path A: an active policy, so the run read runs and Postgres supplies the key.
	f := newApproveFixture(t, super, app, "APPR-08-01 noncanonical-sql-path", "noncanonical-role")
	closeApprovalRunFor(t, super, f.runID, "approved", "fixture-approver")
	upper := strings.ToUpper(f.invoiceID)
	if upper == f.invoiceID {
		t.Fatalf("seedInvoice returned an id with no lowercase hex (%s) -- the fixture proves nothing", f.invoiceID)
	}

	sqlPath := transmitClear(t, app, f.tenantID, []string{upper})
	if _, ok := sqlPath[upper]; ok {
		t.Errorf("the run read keyed the map with the caller's uppercase id -- the defect is fixed, flip this test to assert both paths agree")
	}
	if !sqlPath[f.invoiceID] {
		t.Errorf("sqlPath[canonical] = %t, want true -- Postgres accepted the uppercase id and answered under the canonical key",
			sqlPath[f.invoiceID])
	}

	// Path B: no active policy, so the short-circuit keys the map with the caller's id.
	tenantB, entityB := noPolicyTenant(t, super, "APPR-08-01 noncanonical-short-circuit")
	invoiceB := seedInvoice(t, super, tenantB, entityB, "noncanonical-short-circuit-invoice")
	setInvoiceStatus(t, super, invoiceB, "validated")
	upperB := strings.ToUpper(invoiceB)

	shortCircuit := transmitClear(t, app, tenantB, []string{upperB})
	if !shortCircuit[upperB] {
		t.Errorf("shortCircuit[uppercase] = %t, want true -- the short-circuit echoes the caller's key", shortCircuit[upperB])
	}
	if _, ok := shortCircuit[invoiceB]; ok {
		t.Errorf("the short-circuit also produced the canonical key -- the two paths now agree, flip this test")
	}
}

// TestRowFactsTx_NonCanonicalIDIsAbsentFromTheMap: RowFactsTx has one path, so a
// non-canonical id is merely absent rather than divergent -- a caller that keys by its own
// request string reads "no run" for an armed invoice.
func TestRowFactsTx_NonCanonicalIDIsAbsentFromTheMap(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 rows-noncanonical", "rows-noncanonical-role", "Rows Noncanonical")
	invoiceID := g.validatedInvoice(t, super, "rows-noncanonical-invoice")
	g.armOne(t, app, invoiceID)

	facts := rowFacts(t, app, g.tenantID, []string{strings.ToUpper(invoiceID)})
	if _, ok := facts[strings.ToUpper(invoiceID)]; ok {
		t.Errorf("RowFactsTx keyed the map with the caller's uppercase id -- normalisation landed, update this test")
	}
	rf, ok := facts[invoiceID]
	if !ok {
		t.Fatalf("the canonical key is absent too (%d entries) -- Postgres did not accept the uppercase id at all", len(facts))
	}
	if rf.RunState != "open" {
		t.Errorf("RunState = %q, want open", rf.RunState)
	}
}

// --- empty and nil id sets -----------------------------------------------------------

// TestTransmitClearTx_EmptyAndNilIDsAnswerEmptyAtConstantCost: no len(ids) == 0
// short-circuit by design, so both statements still run and the count stays constant.
func TestTransmitClearTx_EmptyAndNilIDsAnswerEmptyAtConstantCost(t *testing.T) {
	super, _ := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 empty-ids", "empty-ids-role", "Empty Ids Role")

	for _, c := range []struct {
		name string
		ids  []string
	}{
		{"empty", []string{}},
		{"nil", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			tracedApp, rec := tracedAppPool(t)
			rec.reset()
			clear := transmitClear(t, tracedApp, g.tenantID, c.ids)
			if len(clear) != 0 {
				t.Errorf("len(clear) = %d, want 0", len(clear))
			}
			for _, table := range []string{"FROM approval_policy_versions", "FROM invoices"} {
				if got := len(rec.mentioning(table)); got != 1 {
					t.Errorf("statements mentioning %q = %d, want exactly 1", table, got)
				}
			}
		})
	}
}

func TestRowFactsTx_EmptyAndNilIDsAnswerEmptyAtConstantCost(t *testing.T) {
	super, _ := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 rows-empty-ids", "rows-empty-role", "Rows Empty Role")

	for _, c := range []struct {
		name string
		ids  []string
	}{
		{"empty", []string{}},
		{"nil", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			tracedApp, rec := tracedAppPool(t)
			rec.reset()
			facts := rowFacts(t, tracedApp, g.tenantID, c.ids)
			if len(facts) != 0 {
				t.Errorf("len(facts) = %d, want 0", len(facts))
			}
			for _, table := range []string{
				"FROM approval_runs", "FROM approval_run_steps", "FROM workflow_roles",
				"FROM workflow_role_members", "FROM memberships",
			} {
				if got := len(rec.mentioning(table)); got != 1 {
					t.Errorf("statements mentioning %q = %d, want exactly 1", table, got)
				}
			}
		})
	}
}

// --- unknown and cross-tenant ids ----------------------------------------------------

// TestTransmitClearTx_UnknownAndCrossTenantIDsAreAbsentNeverTrue: one batch mixing a
// caller's own approved invoice, a stranger tenant's, and an id that exists nowhere.
func TestTransmitClearTx_UnknownAndCrossTenantIDsAreAbsentNeverTrue(t *testing.T) {
	super, app := dbTestPools(t)

	a := newGateTenant(t, super, "APPR-08-01 mixed-batch-a", "mixed-a-role", "Mixed A Role")
	aInvoice := a.validatedInvoice(t, super, "mixed-batch-a-invoice")
	closeApprovalRunFor(t, super, a.armOne(t, app, aInvoice), "approved", "reviewer-a")

	b := newGateTenant(t, super, "APPR-08-01 mixed-batch-b", "mixed-b-role", "Mixed B Role")
	bInvoice := b.validatedInvoice(t, super, "mixed-batch-b-invoice")
	closeApprovalRunFor(t, super, b.armOne(t, app, bInvoice), "approved", "reviewer-b")

	unknown := uuid.NewString()

	clear := transmitClear(t, app, b.tenantID, []string{aInvoice, bInvoice, unknown})
	for _, c := range []struct {
		name, id string
	}{
		{"tenant A's invoice", aInvoice},
		{"an id that exists nowhere", unknown},
	} {
		if got, ok := clear[c.id]; ok {
			t.Errorf("%s is present in the map (= %t), want absent -- absence is what makes it fail closed", c.name, got)
		}
	}
	if !clear[bInvoice] {
		t.Errorf("clear[B's own approved invoice] = %t, want true -- the control proving the run read ran", clear[bInvoice])
	}
	if len(clear) != 1 {
		t.Errorf("len(clear) = %d, want 1", len(clear))
	}
}

func TestRowFactsTx_UnknownIDIsAbsentFromTheMap(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 rows-unknown-id", "rows-unknown-role", "Rows Unknown Role")
	armed := g.validatedInvoice(t, super, "rows-unknown-armed-invoice")
	g.armOne(t, app, armed)

	facts := rowFacts(t, app, g.tenantID, []string{armed, uuid.NewString()})
	if len(facts) != 1 {
		t.Errorf("len(facts) = %d, want 1 -- an id that exists nowhere gets no entry", len(facts))
	}
	if _, ok := facts[armed]; !ok {
		t.Errorf("the armed invoice is absent, want present -- the control proving the read ran")
	}
}

// --- AXIS 2's soft-delete predicate --------------------------------------------------

// TestGateFactsTx_SoftDeletedRoleIsNotHeld covers AXIS 2's wr.deleted_at IS NULL, which
// no acceptance spec exercised: dropping it left every gate_test.go spec green while the
// gate and decideTx disagreed about a deleted role.
func TestGateFactsTx_SoftDeletedRoleIsNotHeld(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-08-01 soft-deleted-role", "soft-deleted-role")
	subject := activeApprover(t, super, f.tenantID)
	staffWorkflowRole(t, super, f.tenantID, f.roleID, subject, 0)

	before, err := gateFacts(t, app, f.tenantID, f.invoiceID, subject)
	if err != nil {
		t.Fatalf("GateFactsTx before the delete: %v", err)
	}
	if !before.CallerHoldsRole {
		t.Fatalf("CallerHoldsRole before the delete = false -- the fixture never reached the state under test")
	}

	softDeleteWorkflowRole(t, super, f.roleID)

	after, err := gateFacts(t, app, f.tenantID, f.invoiceID, subject)
	if err != nil {
		t.Fatalf("GateFactsTx after the delete: %v", err)
	}
	if after.CallerHoldsRole {
		t.Errorf("CallerHoldsRole = true, want false -- AXIS 2 requires wr.deleted_at IS NULL")
	}
	if after.RunState != "open" || !ordEq(after.PendingStepOrd, ptr(0)) {
		t.Errorf("RunState = %q, PendingStepOrd = %s, want open/0 -- only the role holding changes",
			after.RunState, ordText(after.PendingStepOrd))
	}

	if _, derr := approve(t, app, f.tenantID, subject, f.invoiceID, nil); !errors.Is(derr, ErrNotRoleHolder) {
		t.Errorf("Decide = %v, want ErrNotRoleHolder -- the gate and decideTx must refuse on the same rung", derr)
	}
}

// --- the Overdue clock ---------------------------------------------------------------

// TestRowFactsTx_OverdueBoundary covers the DueAt.Before(time.Now()) half of the rule.
// gate_test.go's settled/pending pair only covers the step-state half, so an Overdue that
// ignored the clock entirely stayed green.
func TestRowFactsTx_OverdueBoundary(t *testing.T) {
	super, app := dbTestPools(t)
	g := newGateTenant(t, super, "APPR-08-01 rows-overdue-boundary", "overdue-boundary-role", "Overdue Boundary")

	// Captured before the read, so "now" is strictly in the read's past however fast the
	// test runs -- the boundary is observable without a sleep.
	now := time.Now()
	for _, c := range []struct {
		name  string
		dueAt time.Time
		want  bool
	}{
		{"48h in the future", now.Add(48 * time.Hour), false},
		{"exactly now", now, true},
		{"1ms past", now.Add(-time.Millisecond), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			invoiceID := g.validatedInvoice(t, super, "overdue-boundary-"+c.name)
			runID := g.armOne(t, app, invoiceID)
			backdateRunStepDueAt(t, super, runStepID(t, super, runID, 0), c.dueAt)

			facts := rowFacts(t, app, g.tenantID, []string{invoiceID})
			rf, ok := facts[invoiceID]
			if !ok {
				t.Fatalf("invoice absent from the map (%d entries), want one entry", len(facts))
			}
			if rf.DueAt == nil {
				t.Fatalf("DueAt = nil -- the pending step carries a due_at")
			}
			if rf.Overdue != c.want {
				t.Errorf("Overdue = %t, want %t for a due_at %s", rf.Overdue, c.want, c.name)
			}
		})
	}
}

// --- the kind = "approval" predicate -------------------------------------------------

// notifyThenApprovalFixture arms a two-step version -- a notify at ord 0 and a staffed
// approval at ord 1 -- then force-writes the notify back to pending. ArmTx always writes
// notify steps skipped, but no CHECK ties kind to state, so the row is legal.
type notifyThenApprovalFixture struct {
	tenantID, invoiceID, subject string
}

func newNotifyThenApprovalFixture(t *testing.T, super, app *pgxpool.Pool, name, roleKey string) notifyThenApprovalFixture {
	t.Helper()
	tenantID := policyTenant(t, super, name)
	entityID := seedBusinessEntity(t, super, tenantID, name+" Corp")

	roleID := seedWorkflowRole(t, super, tenantID, roleKey, "Notify Then Approval Role")
	subject := activeApprover(t, super, tenantID)
	staffWorkflowRole(t, super, tenantID, roleID, subject, 0)

	policyID := seedApprovalPolicy(t, super, tenantID, name+" policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "notify", NotifyTarget: ptr("ops@example.com"), NotifyChannel: ptr("email"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr(roleKey), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	invoiceID := seedInvoice(t, super, tenantID, entityID, name+" invoice")
	setInvoiceStatus(t, super, invoiceID, "validated")
	res, err := arm(t, app, tenantID, invoiceID, "fp-"+invoiceID, "fixture-arm")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	setRunStepState(t, super, runStepID(t, super, res.RunID, 0), "pending")

	return notifyThenApprovalFixture{tenantID: tenantID, invoiceID: invoiceID, subject: subject}
}

// TestGateFactsTx_PendingNotifyStepIsNotTheGatesStep: without kind = "approval" the lower
// notify ord wins and CallerHoldsRole collapses to false, which no acceptance spec saw.
func TestGateFactsTx_PendingNotifyStepIsNotTheGatesStep(t *testing.T) {
	super, app := dbTestPools(t)
	f := newNotifyThenApprovalFixture(t, super, app, "APPR-08-01 gate-pending-notify", "gate-notify-role")

	gf, err := gateFacts(t, app, f.tenantID, f.invoiceID, f.subject)
	if err != nil {
		t.Fatalf("GateFactsTx: %v", err)
	}
	if !ordEq(gf.PendingStepOrd, ptr(1)) {
		t.Errorf("PendingStepOrd = %s, want 1 -- ord 0 is a pending notify, which the gate never reads", ordText(gf.PendingStepOrd))
	}
	if !gf.CallerHoldsRole {
		t.Errorf("CallerHoldsRole = false, want true -- the subject holds the ord-1 approval step's role")
	}
}

func TestRowFactsTx_PendingNotifyStepIsNotTheRowsStep(t *testing.T) {
	super, app := dbTestPools(t)
	f := newNotifyThenApprovalFixture(t, super, app, "APPR-08-01 rows-pending-notify", "rows-notify-role")

	facts := rowFacts(t, app, f.tenantID, []string{f.invoiceID})
	rf, ok := facts[f.invoiceID]
	if !ok {
		t.Fatalf("invoice absent from the map (%d entries), want one entry", len(facts))
	}
	if !ordEq(rf.PendingOrd, ptr(1)) {
		t.Errorf("PendingOrd = %s, want 1 -- ord 0 is a pending notify", ordText(rf.PendingOrd))
	}
	if rf.PendingRoleTitle == nil || *rf.PendingRoleTitle != "Notify Then Approval Role" {
		t.Errorf("PendingRoleTitle = %s, want the ord-1 approval step's role title", titleText(rf.PendingRoleTitle))
	}
	if rf.DueAt == nil {
		t.Errorf("DueAt = nil, want the ord-1 approval step's due_at -- a notify carries none")
	}
}

// --- RowFacts JSON shape (AC-7) ------------------------------------------------------

// TestRowFacts_JSONTagsCarryNoOmitempty: 08-08 ships RowFacts on the wire, where an
// omitted key and a null one are different answers to "is there a run".
func TestRowFacts_JSONTagsCarryNoOmitempty(t *testing.T) {
	blob, err := json.Marshal(RowFacts{})
	if err != nil {
		t.Fatalf("marshal RowFacts: %v", err)
	}
	for _, key := range []string{
		`"run_state"`, `"pending_ord"`, `"pending_role_title"`,
		`"pending_holder_warn"`, `"due_at"`, `"overdue"`,
	} {
		if !strings.Contains(string(blob), key) {
			t.Errorf("the zero RowFacts omits %s -- an omitempty tag crept in", key)
		}
	}
}
