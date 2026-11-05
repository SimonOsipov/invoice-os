package approval

// Store-adjacent specs for ArmTx: the tx-scoped write half of the arming engine —
// resolve the first matching policy, materialise it against one invoice,
// and write approval_runs + approval_run_steps + one audit row. Every spec here starts
// RED against an undefined ArmTx/ArmResult (task-480's Test-first stage).
//
// Separate from engine_test.go, whose header asserts it is a pure test that never calls
// dbTestPools and so cannot skip — these DB-backed specs must not land there. The
// adversarial coverage beyond these nine acceptance criteria lives in
// engine_adversarial_test.go.
//
// Run locally with `DEV_DB_PORT=5433 make test-approvals`; in CI the rls job's gate
// step fails the build on any skip.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- fixtures ------------------------------------------------------------------------

// setInvoiceTotal sets an invoice's total as decimal text. seedInvoice
// (schema_constraints_test.go:195) inserts only tenant_id/entity_id/invoice_number and
// cannot set it — invoices.total is nullable with no default — so every
// amount-condition spec needs this to give evalCondition a real value to compare.
func setInvoiceTotal(t *testing.T, super *pgxpool.Pool, invoiceID, total string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE invoices SET total = $2::numeric WHERE id = $1`, invoiceID, total)
	if err != nil {
		t.Fatalf("set invoice %s total to %s: %v", invoiceID, total, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set invoice %s total affected %d rows, want 1", invoiceID, tag.RowsAffected())
	}
}

// arm runs ArmTx inside a fresh tenant-scoped transaction — db.WithinTenantTx's own
// commit-on-nil/rollback-on-error behaviour and nothing layered on top. Every spec below
// reads the committed (or rolled-back) result back through the superuser pool.
func arm(t *testing.T, pool *pgxpool.Pool, tenantID, invoiceID, fingerprint, actor string) (ArmResult, error) {
	t.Helper()
	var res ArmResult
	err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		res, err = ArmTx(context.Background(), tx, tenantID, invoiceID, fingerprint, actor)
		return err
	})
	return res, err
}

// storedApprovalRun is one approval_runs row, read back as the superuser.
type storedApprovalRun struct {
	ID                 string
	TenantID           string
	InvoiceID          string
	PolicyVersionID    string
	State              string
	ContentFingerprint string
	OpenedAt           time.Time
	ClosedAt           *time.Time
	ClosedBy           *string
}

// oneApprovalRun reads the single approval_runs row for invoiceID, Fataling unless
// exactly one exists — every arm spec below expects exactly one run per invoice.
func oneApprovalRun(t *testing.T, super *pgxpool.Pool, invoiceID string) storedApprovalRun {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id, tenant_id, invoice_id, policy_version_id, state, content_fingerprint,
		        opened_at, closed_at, closed_by
		   FROM approval_runs WHERE invoice_id = $1`, invoiceID)
	if err != nil {
		t.Fatalf("read approval_runs for invoice %s: %v", invoiceID, err)
	}
	defer rows.Close()
	var out []storedApprovalRun
	for rows.Next() {
		var r storedApprovalRun
		if err := rows.Scan(&r.ID, &r.TenantID, &r.InvoiceID, &r.PolicyVersionID, &r.State,
			&r.ContentFingerprint, &r.OpenedAt, &r.ClosedAt, &r.ClosedBy); err != nil {
			t.Fatalf("scan approval_runs for invoice %s: %v", invoiceID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_runs for invoice %s: %v", invoiceID, err)
	}
	if len(out) != 1 {
		t.Fatalf("approval_runs rows for invoice %s = %d, want exactly 1: %+v", invoiceID, len(out), out)
	}
	return out[0]
}

// storedRunStep is one approval_run_steps row, read back in ord order.
type storedRunStep struct {
	Ord             int
	Kind            string
	WorkflowRoleKey *string
	SLAHours        *int
	NotifyTarget    *string
	NotifyChannel   *string
	State           string
	DueAt           *time.Time
	SatisfiedAt     *time.Time
	SatisfiedBy     *string
}

// runStepsOf reads every approval_run_steps row of runID, in ord order (D32).
func runStepsOf(t *testing.T, super *pgxpool.Pool, runID string) []storedRunStep {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT ord, kind, workflow_role_key, sla_hours, notify_target, notify_channel,
		        state, due_at, satisfied_at, satisfied_by
		   FROM approval_run_steps WHERE run_id = $1 ORDER BY ord`, runID)
	if err != nil {
		t.Fatalf("read approval_run_steps for run %s: %v", runID, err)
	}
	defer rows.Close()
	out := []storedRunStep{}
	for rows.Next() {
		var s storedRunStep
		if err := rows.Scan(&s.Ord, &s.Kind, &s.WorkflowRoleKey, &s.SLAHours,
			&s.NotifyTarget, &s.NotifyChannel, &s.State, &s.DueAt, &s.SatisfiedAt, &s.SatisfiedBy); err != nil {
			t.Fatalf("scan approval_run_steps for run %s: %v", runID, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read approval_run_steps for run %s: %v", runID, err)
	}
	return out
}

// --- AC-1: one run row, one step row per materialised step ---------------------------

// TestArm_WritesOneRowPerMaterialisedStep is AC-1's direct pin: a root lane of three
// approval steps plus one notify writes exactly one run row and exactly four step rows,
// ord 0..3 in tree order.
func TestArm_WritesOneRowPerMaterialisedStep(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-one-row-per-step")
	entityID := seedBusinessEntity(t, super, tenantID, "Arm Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "one-row-per-step-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Four-step policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("fin_dir"), SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "approval", WorkflowRoleKey: ptr("compliance"), SLAHours: ptr(12),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 3, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-writes-one-row", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.RunID == "" {
		t.Error("RunID is empty, want a written run's id")
	}
	if res.Steps != 4 {
		t.Errorf("Steps = %d, want 4", res.Steps)
	}
	if res.Closed {
		t.Error("Closed = true, want false — an approval step remains pending")
	}

	if n := rowCount(t, super, "approval_runs", tenantID); n != 1 {
		t.Errorf("approval_runs rows = %d, want exactly 1", n)
	}
	if n := rowCount(t, super, "approval_run_steps", tenantID); n != 4 {
		t.Errorf("approval_run_steps rows = %d, want exactly 4", n)
	}

	run := oneApprovalRun(t, super, invoiceID)
	if run.ID != res.RunID {
		t.Errorf("stored run id = %q, want ArmResult.RunID %q", run.ID, res.RunID)
	}

	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 4 {
		t.Fatalf("runStepsOf = %d rows, want 4", len(steps))
	}
	wantKinds := []string{"approval", "approval", "approval", "notify"}
	for i, want := range wantKinds {
		if steps[i].Ord != i {
			t.Errorf("steps[%d].Ord = %d, want %d", i, steps[i].Ord, i)
		}
		if steps[i].Kind != want {
			t.Errorf("steps[%d].Kind = %q, want %q", i, steps[i].Kind, want)
		}
	}
	// The trailing (last-ord) row's own columns, not just kind/ord: a ragged unnest
	// array pads its short columns with NULL starting at the END of the row set, so a
	// dropped array element surfaces here first.
	last := steps[3]
	if last.NotifyTarget == nil || *last.NotifyTarget != "Tax Team" {
		t.Errorf("steps[3].NotifyTarget = %v, want \"Tax Team\"", last.NotifyTarget)
	}
	if last.NotifyChannel == nil || *last.NotifyChannel != "In-app" {
		t.Errorf("steps[3].NotifyChannel = %v, want \"In-app\"", last.NotifyChannel)
	}
}

// --- AC-2: the one write-nothing arm ---------------------------------------------------

// TestArm_NoActivePolicyWritesNothing: a tenant whose only version is sealed but not
// active writes nothing at all — no run, no step, no audit row. Control: a second
// tenant WITH an active version writes rows, so the zero counts above are not a store
// that refuses everything.
func TestArm_NoActivePolicyWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-no-active-policy")
	entityID := seedBusinessEntity(t, super, tenantID, "No Active Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "no-active-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Sealed not active")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	sealApprovalPolicyVersion(t, super, versionID) // sealed, but never activated

	res, err := arm(t, app, tenantID, invoiceID, "fp-no-active", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx with no active version: %v, want nil error", err)
	}
	want := ArmResult{}
	if res != want {
		t.Errorf("ArmTx with no active version = %+v, want the zero ArmResult", res)
	}
	for _, table := range []string{"approval_runs", "approval_run_steps"} {
		if n := rowCount(t, super, table, tenantID); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 0 {
		t.Errorf("invoice.approval_armed audit rows = %d, want 0", n)
	}

	// Control: a second tenant WITH an active version writes rows.
	controlTenant := policyTenant(t, super, "APPR-06 arm-no-active-control")
	controlEntity := seedBusinessEntity(t, super, controlTenant, "Control Corp")
	controlInvoice := seedInvoice(t, super, controlTenant, controlEntity, "control-invoice-1")
	controlPolicy := seedApprovalPolicy(t, super, controlTenant, "Active control policy")
	controlVersion := seedApprovalPolicyVersionN(t, super, controlTenant, controlPolicy, 1)
	seedApprovalPolicyStepInLane(t, super, controlTenant, controlVersion, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, controlVersion)

	if _, err := arm(t, app, controlTenant, controlInvoice, "fp-control", "test-actor"); err != nil {
		t.Fatalf("control: ArmTx with an active version: %v, want success", err)
	}
	if n := rowCount(t, super, "approval_runs", controlTenant); n != 1 {
		t.Errorf("control: approval_runs rows = %d, want 1", n)
	}
}

// --- AC-3, AC-5: an active-but-empty version still arms a closed run -----------------

// TestArm_ActiveButEmptyPolicyArmsClosedApprovedRun: the state TestPublish_EmptyPolicyAllowed
// (policy_publish_test.go:266) proves publishable — zero steps — still arms a run,
// written closed 'approved' by 'system', with one audit row. Also falsifies the
// superseded write-nothing plan (D29).
func TestArm_ActiveButEmptyPolicyArmsClosedApprovedRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-empty-active-policy")
	entityID := seedBusinessEntity(t, super, tenantID, "Empty Active Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "empty-active-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Empty policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	activateApprovalPolicyVersion(t, super, versionID) // zero steps, sealed + active

	res, err := arm(t, app, tenantID, invoiceID, "fp-empty-active", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx against an empty active version: %v, want success", err)
	}
	if res.RunID == "" {
		t.Error("RunID is empty, want a written run's id")
	}
	if res.Steps != 0 {
		t.Errorf("Steps = %d, want 0", res.Steps)
	}
	if !res.Closed {
		t.Error("Closed = false, want true — an active version with zero steps has no pending approval")
	}
	if n := rowCount(t, super, "approval_run_steps", tenantID); n != 0 {
		t.Errorf("approval_run_steps rows = %d, want 0", n)
	}

	run := oneApprovalRun(t, super, invoiceID)
	if run.State != "approved" {
		t.Errorf("run.State = %q, want approved", run.State)
	}
	if run.ClosedAt == nil {
		t.Error("run.ClosedAt is NULL, want non-NULL")
	}
	if run.ClosedBy == nil || *run.ClosedBy != "system" {
		t.Errorf("run.ClosedBy = %v, want \"system\"", run.ClosedBy)
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 1 {
		t.Errorf("invoice.approval_armed audit rows = %d, want 1", n)
	}

	// Control: a second tenant with one approval step arms open/pending/closed_by NULL.
	controlTenant := policyTenant(t, super, "APPR-06 arm-empty-active-control")
	controlEntity := seedBusinessEntity(t, super, controlTenant, "Control Corp")
	controlInvoice := seedInvoice(t, super, controlTenant, controlEntity, "control-invoice-1")
	controlPolicy := seedApprovalPolicy(t, super, controlTenant, "One-step control policy")
	controlVersion := seedApprovalPolicyVersionN(t, super, controlTenant, controlPolicy, 1)
	seedApprovalPolicyStepInLane(t, super, controlTenant, controlVersion, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, controlVersion)

	if _, err := arm(t, app, controlTenant, controlInvoice, "fp-control", "test-actor"); err != nil {
		t.Fatalf("control: ArmTx: %v, want success", err)
	}
	controlRun := oneApprovalRun(t, super, controlInvoice)
	if controlRun.State != "open" || controlRun.ClosedAt != nil || controlRun.ClosedBy != nil {
		t.Errorf("control run = %+v, want state open, closed_at NULL, closed_by NULL", controlRun)
	}
}

// --- AC-4: the mechanical "no second pass" guard --------------------------------------

// TestArm_NeverUpdatesTheRunAfterInsert is the mechanical guard for the closure
// predicate: an insert-then-update implementation would pass every state assertion
// above and fail only here. Two legs — one pending approval step, and zero steps
// closed on arm — both must write exactly one statement against approval_runs,
// beginning INSERT, and never an UPDATE approval_runs.
func TestArm_NeverUpdatesTheRunAfterInsert(t *testing.T) {
	super, _ := dbTestPools(t)
	traced, rec := tracedAppPool(t)

	tenantOneStep := policyTenant(t, super, "APPR-06 arm-no-update-one-step")
	entityOne := seedBusinessEntity(t, super, tenantOneStep, "One Step Corp")
	invoiceOne := seedInvoice(t, super, tenantOneStep, entityOne, "one-step-invoice-1")
	policyOne := seedApprovalPolicy(t, super, tenantOneStep, "One approval step")
	versionOne := seedApprovalPolicyVersionN(t, super, tenantOneStep, policyOne, 1)
	seedApprovalPolicyStepInLane(t, super, tenantOneStep, versionOne, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionOne)

	tenantZeroStep := policyTenant(t, super, "APPR-06 arm-no-update-zero-step")
	entityZero := seedBusinessEntity(t, super, tenantZeroStep, "Zero Step Corp")
	invoiceZero := seedInvoice(t, super, tenantZeroStep, entityZero, "zero-step-invoice-1")
	policyZero := seedApprovalPolicy(t, super, tenantZeroStep, "Zero-step policy")
	versionZero := seedApprovalPolicyVersionN(t, super, tenantZeroStep, policyZero, 1)
	activateApprovalPolicyVersion(t, super, versionZero)

	for _, leg := range []struct {
		name      string
		tenantID  string
		invoiceID string
	}{
		{"one pending approval step", tenantOneStep, invoiceOne},
		{"zero steps, closed on arm", tenantZeroStep, invoiceZero},
	} {
		t.Run(leg.name, func(t *testing.T) {
			rec.reset()
			if _, err := arm(t, traced, leg.tenantID, leg.invoiceID, "fp-no-update", "test-actor"); err != nil {
				t.Fatalf("ArmTx: %v", err)
			}
			runStatements := rec.mentioning("approval_runs")
			if len(runStatements) != 1 {
				t.Fatalf("statements mentioning approval_runs = %d, want exactly 1: %v", len(runStatements), runStatements)
			}
			if got := strings.TrimSpace(runStatements[0]); !strings.HasPrefix(got, "INSERT") {
				t.Errorf("the one approval_runs statement = %q, want it to begin INSERT", got)
			}
			if updates := rec.mentioning("UPDATE approval_runs"); len(updates) != 0 {
				t.Errorf("UPDATE approval_runs statements = %v, want none — an insert-then-update implementation must fail here", updates)
			}
		})
	}
}

// --- AC-6: content_fingerprint is the caller's literal, nothing computed -------------

// TestArm_RunCarriesTheSuppliedFingerprint: the stored content_fingerprint equals the
// fingerprint argument verbatim. Control: a different literal on a sibling invoice is
// stored verbatim too, proving ArmTx computes nothing itself.
func TestArm_RunCarriesTheSuppliedFingerprint(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-fingerprint")
	entityID := seedBusinessEntity(t, super, tenantID, "Fingerprint Corp")
	invoiceA := seedInvoice(t, super, tenantID, entityID, "fingerprint-invoice-a")
	invoiceB := seedInvoice(t, super, tenantID, entityID, "fingerprint-invoice-b")

	policyID := seedApprovalPolicy(t, super, tenantID, "Fingerprint policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	if _, err := arm(t, app, tenantID, invoiceA, "caller-supplied-fingerprint-a", "test-actor"); err != nil {
		t.Fatalf("ArmTx invoice A: %v", err)
	}
	runA := oneApprovalRun(t, super, invoiceA)
	if runA.ContentFingerprint != "caller-supplied-fingerprint-a" {
		t.Errorf("run A content_fingerprint = %q, want the literal verbatim", runA.ContentFingerprint)
	}

	if _, err := arm(t, app, tenantID, invoiceB, "a-completely-different-literal", "test-actor"); err != nil {
		t.Fatalf("ArmTx invoice B: %v", err)
	}
	runB := oneApprovalRun(t, super, invoiceB)
	if runB.ContentFingerprint != "a-completely-different-literal" {
		t.Errorf("run B content_fingerprint = %q, want the literal verbatim", runB.ContentFingerprint)
	}
}

// --- AC-7: due_at is SLA-derived, gated on approval + sla_hours > 0 -------------------

// TestArm_DueAtNullWhenNoDeadline: three approval steps at sla_hours NULL, 0 and 48 —
// the first two have due_at NULL, the third's due_at is within a second of
// opened_at + 48h.
func TestArm_DueAtNullWhenNoDeadline(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-due-at-null")
	entityID := seedBusinessEntity(t, super, tenantID, "Due At Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "due-at-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Deadline mix policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("role-null-sla"), SLAHours: nil,
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("role-zero-sla"), SLAHours: ptr(0),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "approval", WorkflowRoleKey: ptr("role-48h-sla"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-due-at", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.Steps != 3 {
		t.Fatalf("Steps = %d, want 3", res.Steps)
	}

	run := oneApprovalRun(t, super, invoiceID)
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 3 {
		t.Fatalf("runStepsOf = %d rows, want 3", len(steps))
	}
	if steps[0].DueAt != nil {
		t.Errorf("steps[0] (sla NULL) due_at = %v, want NULL", steps[0].DueAt)
	}
	// The stored sla_hours column itself, not just due_at: NULL and 0 both yield
	// due_at IS NULL, so a bulk-insert bug substituting 0 for a nil sla would pass the
	// due_at checks above and be caught only here.
	if steps[0].SLAHours != nil {
		t.Errorf("steps[0] sla_hours = %v, want NULL, not 0 — NULL and 0 are distinct stored rows", *steps[0].SLAHours)
	}
	if steps[1].DueAt != nil {
		t.Errorf("steps[1] (sla 0) due_at = %v, want NULL", steps[1].DueAt)
	}
	if steps[1].SLAHours == nil || *steps[1].SLAHours != 0 {
		t.Errorf("steps[1] sla_hours = %v, want 0, not NULL", steps[1].SLAHours)
	}
	if steps[2].DueAt == nil {
		t.Fatalf("steps[2] (sla 48) due_at is NULL, want opened_at + 48h")
	}
	want := run.OpenedAt.Add(48 * time.Hour)
	if diff := steps[2].DueAt.Sub(want); diff < -time.Second || diff > time.Second {
		t.Errorf("steps[2].DueAt = %s, want within 1s of opened_at+48h (%s)", steps[2].DueAt, want)
	}
}

// --- AC-7, AC-8: notify/autoapprove never get a due_at, the D-A regression lock ------

// TestArm_NotifyAndAutoapproveStepsNeverGetADueAt: a notify at sla_hours=7 and an
// autoapprove at sla_hours=12 — both seedable, the exact shape
// TestPutDraft_NullableColumnsRoundTripInOneBatch (policy_draft_test.go:406)
// round-trips — plus one approval at sla_hours=48. The superseded ungated
// `CASE WHEN sla > 0` (D-A) passes every other spec and fails only this one: it would
// leak due_at onto the notify and autoapprove rows because materialise copies
// SLAHours through for every kind.
func TestArm_NotifyAndAutoapproveStepsNeverGetADueAt(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-notify-autoapprove-no-due-at")
	entityID := seedBusinessEntity(t, super, tenantID, "No Due At Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "no-due-at-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Notify+autoapprove+approval policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "notify", SLAHours: ptr(7),
		NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "autoapprove", SLAHours: ptr(12),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-notify-autoapprove", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.Steps != 3 {
		t.Fatalf("Steps = %d, want 3", res.Steps)
	}

	run := oneApprovalRun(t, super, invoiceID)
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 3 {
		t.Fatalf("runStepsOf = %d rows, want 3", len(steps))
	}
	if steps[0].Kind != "notify" || steps[0].DueAt != nil {
		t.Errorf("notify step = %+v, want kind notify and due_at NULL despite sla_hours=7", steps[0])
	}
	if steps[1].Kind != "autoapprove" || steps[1].DueAt != nil {
		t.Errorf("autoapprove step = %+v, want kind autoapprove and due_at NULL despite sla_hours=12", steps[1])
	}
	if steps[2].Kind != "approval" || steps[2].DueAt == nil {
		t.Fatalf("approval step = %+v, want kind approval and a non-NULL due_at", steps[2])
	}
	want := run.OpenedAt.Add(48 * time.Hour)
	if diff := steps[2].DueAt.Sub(want); diff < -time.Second || diff > time.Second {
		t.Errorf("approval step DueAt = %s, want within 1s of opened_at+48h (%s)", steps[2].DueAt, want)
	}
}

// --- AC-8: a notify step is written skipped, carrying target and channel ------------

// TestArm_NotifyStepWrittenSkippedWithTargetAndChannel: root lane holds one approval
// step and one notify step — the notify row is state='skipped' with target and
// channel set, and the run stays open because the approval step is pending.
func TestArm_NotifyStepWrittenSkippedWithTargetAndChannel(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-notify-skipped")
	entityID := seedBusinessEntity(t, super, tenantID, "Notify Skipped Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "notify-skipped-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Approval plus notify policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-notify-target-channel", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.Closed {
		t.Error("Closed = true, want false — the approval step remains pending")
	}

	run := oneApprovalRun(t, super, invoiceID)
	if run.State != "open" {
		t.Errorf("run.State = %q, want open", run.State)
	}
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 2 {
		t.Fatalf("runStepsOf = %d rows, want 2", len(steps))
	}
	if steps[0].State != "pending" {
		t.Errorf("approval step state = %q, want pending", steps[0].State)
	}
	notify := steps[1]
	if notify.Kind != "notify" || notify.State != "skipped" {
		t.Fatalf("notify step = %+v, want kind notify, state skipped", notify)
	}
	if notify.NotifyTarget == nil || *notify.NotifyTarget != "Tax Team" {
		t.Errorf("notify_target = %v, want \"Tax Team\"", notify.NotifyTarget)
	}
	if notify.NotifyChannel == nil || *notify.NotifyChannel != "In-app" {
		t.Errorf("notify_channel = %v, want \"In-app\"", notify.NotifyChannel)
	}
}

// --- AC-9: one audit row, summary-only payload ----------------------------------------

// TestArm_WritesOneAuditRowWithSummaryPayload: exactly one invoice.approval_armed row
// whose payload keys are exactly {id, run_id, policy_version_id, steps} — id is the
// INVOICE id. Copies internal/reconciliation/audit_test.go:158-168, including its
// anti-vacuity guard first: an empty {} would satisfy the allowlist loop vacuously.
func TestArm_WritesOneAuditRowWithSummaryPayload(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-audit-summary-payload")
	entityID := seedBusinessEntity(t, super, tenantID, "Audit Summary Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "audit-summary-invoice-1")

	policyID := seedApprovalPolicy(t, super, tenantID, "Audit summary policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(48),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-audit-summary", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.Steps != 2 {
		t.Fatalf("res.Steps = %d, want 2 — the steps assertion below is vacuous otherwise", res.Steps)
	}

	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 1 {
		t.Fatalf("invoice.approval_armed audit rows = %d, want exactly 1", n)
	}

	var payload []byte
	if err := super.QueryRow(context.Background(),
		`SELECT payload FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.approval_armed'`,
		tenantID).Scan(&payload); err != nil {
		t.Fatalf("read the audit row: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// Anti-vacuity guard FIRST: an empty {} would trivially (and vacuously) satisfy the
	// key-set check below without proving anything was actually written
	// (internal/reconciliation/audit_test.go:158-168).
	if len(body) == 0 {
		t.Fatal("payload is empty {} — want id/run_id/policy_version_id/steps populated")
	}

	allowed := map[string]bool{"id": true, "run_id": true, "policy_version_id": true, "policy_id": true, "position": true, "matched_scopes": true, "steps": true, "invoice_number": true}
	for k := range body {
		if !allowed[k] {
			t.Errorf("payload key %q is not in the summary-only allowlist {id, run_id, policy_version_id, policy_id, position, matched_scopes, steps, invoice_number} — no invoice content", k)
		}
	}
	for k := range allowed {
		if _, ok := body[k]; !ok {
			t.Errorf("payload is missing key %q", k)
		}
	}

	if got, ok := body["id"].(string); !ok || got != invoiceID {
		t.Errorf("payload id = %v, want the invoice id %q", body["id"], invoiceID)
	}
	if got, ok := body["run_id"].(string); !ok || got != res.RunID {
		t.Errorf("payload run_id = %v, want %q", body["run_id"], res.RunID)
	}
	if got, ok := body["policy_version_id"].(string); !ok || got != versionID {
		t.Errorf("payload policy_version_id = %v, want %q", body["policy_version_id"], versionID)
	}
	if gotSteps, ok := body["steps"].(float64); !ok || int(gotSteps) != res.Steps {
		t.Errorf("payload steps = %v, want %d", body["steps"], res.Steps)
	}
}

// --- APPR-06-05 AC-1,3,4,5: autoapprove settles every step and closes the run -------

// TestArm_AutoApproveSettlesEveryStepAndClosesTheRun pins polH1's own autoapprove
// shape (engine_test.go's polH1, at ₦750,000,000 — the same tree and total as
// TestMaterialise_LinearPassMatchesSimulate's "polH1 @750,000,000" case): both
// approval steps sit BEFORE the autoapprove in emission order, so this is D15's other
// direction from TestArm_OrdDensityAcrossMixedKinds, where the autoapprove sits after
// a root approval reached through a different lane.
func TestArm_AutoApproveSettlesEveryStepAndClosesTheRun(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-autoapprove-settles-run")
	entityID := seedBusinessEntity(t, super, tenantID, "Autoapprove Settles Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "autoapprove-settles-invoice-1")
	setInvoiceTotal(t, super, invoiceID, "750000000.00")

	policyID := seedApprovalPolicy(t, super, tenantID, "polH1 shape")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("line_mgr"), SLAHours: ptr(48),
	})
	condA := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500000000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condA, Branch: ptr("then"), Ord: 0,
		Kind: "approval", WorkflowRoleKey: ptr("fin_dir"), SLAHours: ptr(48),
	})
	condB := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000000000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condB, Branch: ptr("then"), Ord: 0,
		Kind: "approval", WorkflowRoleKey: ptr("cfo"), SLAHours: ptr(72),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		ParentStepID: &condB, Branch: ptr("else"), Ord: 0,
		Kind: "autoapprove",
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 3, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-autoapprove-settles", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	// Guard first: every index below is vacuous unless exactly the four materialised
	// steps landed (₦750m takes condA's then and condB's else).
	if res.Steps != 4 {
		t.Fatalf("Steps = %d, want 4", res.Steps)
	}
	if !res.Closed {
		t.Error("Closed = false, want true")
	}

	run := oneApprovalRun(t, super, invoiceID)
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 4 {
		t.Fatalf("runStepsOf = %d rows, want 4", len(steps))
	}
	lineMgr, finDir, auto, notify := steps[0], steps[1], steps[2], steps[3]

	if lineMgr.Kind != "approval" || lineMgr.State != "skipped" {
		t.Errorf("steps[0] (line_mgr) = %+v, want kind approval, state skipped", lineMgr)
	}
	if finDir.Kind != "approval" || finDir.State != "skipped" {
		t.Errorf("steps[1] (fin_dir) = %+v, want kind approval, state skipped", finDir)
	}
	if auto.Kind != "autoapprove" || auto.State != "satisfied" {
		t.Fatalf("steps[2] (autoapprove) = %+v, want kind autoapprove, state satisfied", auto)
	}
	if auto.SatisfiedAt == nil {
		t.Error("autoapprove step satisfied_at is NULL, want non-NULL")
	}
	if auto.SatisfiedBy == nil || *auto.SatisfiedBy != "system" {
		t.Errorf("autoapprove step satisfied_by = %v, want \"system\"", auto.SatisfiedBy)
	}
	if notify.Kind != "notify" || notify.State != "skipped" {
		t.Errorf("steps[3] (notify) = %+v, want kind notify, state skipped", notify)
	}
	if notify.NotifyTarget == nil || *notify.NotifyTarget != "Tax Team" {
		t.Errorf("notify_target = %v, want \"Tax Team\"", notify.NotifyTarget)
	}
	if notify.NotifyChannel == nil || *notify.NotifyChannel != "In-app" {
		t.Errorf("notify_channel = %v, want \"In-app\"", notify.NotifyChannel)
	}

	if run.State != "approved" {
		t.Errorf("run.State = %q, want approved", run.State)
	}
	if run.ClosedAt == nil {
		t.Error("run.ClosedAt is NULL, want non-NULL")
	}
	if run.ClosedBy == nil || *run.ClosedBy != "system" {
		t.Errorf("run.ClosedBy = %v, want \"system\"", run.ClosedBy)
	}
}

// --- APPR-06-05 AC-2: closure is one rule, not two branches --------------------------

// TestArm_ClosureIsOneRuleNotTwoBranches (D34) pins that the run's closed/approved
// state comes from ONE predicate reading step states, never from a second branch keyed
// on `auto` directly. Three legs, each its own tenant (keeps the arming resolve to one active version): (a) an autoapprove policy closes the
// run; (b) a notify-only policy closes it too, even though auto is false — an
// `if auto { close }` implementation fails ONLY this leg; (c) one staffed approval step
// leaves the run open — the control that rules out a `len(steps)==0` implementation,
// which would also pass leg (b) by accident.
func TestArm_ClosureIsOneRuleNotTwoBranches(t *testing.T) {
	super, app := dbTestPools(t)

	assertClosure := func(t *testing.T, invoiceID, wantState string, wantClosed bool) {
		t.Helper()
		run := oneApprovalRun(t, super, invoiceID)
		if run.State != wantState {
			t.Errorf("run.State = %q, want %q", run.State, wantState)
		}
		if wantClosed {
			if run.ClosedAt == nil {
				t.Error("run.ClosedAt is NULL, want non-NULL")
			}
			if run.ClosedBy == nil || *run.ClosedBy != "system" {
				t.Errorf("run.ClosedBy = %v, want \"system\"", run.ClosedBy)
			}
		} else {
			if run.ClosedAt != nil {
				t.Errorf("run.ClosedAt = %v, want NULL", run.ClosedAt)
			}
			if run.ClosedBy != nil {
				t.Errorf("run.ClosedBy = %v, want NULL", run.ClosedBy)
			}
		}
	}

	t.Run("leg a: an autoapprove policy closes the run", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-06 closure-leg-a-autoapprove")
		entityID := seedBusinessEntity(t, super, tenantID, "Closure Leg A Corp")
		invoiceID := seedInvoice(t, super, tenantID, entityID, "closure-leg-a-invoice")
		setInvoiceTotal(t, super, invoiceID, "750000000.00")

		policyID := seedApprovalPolicy(t, super, tenantID, "Leg A policy")
		versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("line_mgr"), SLAHours: ptr(48),
		})
		condA := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: 1, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500000000.00"),
		})
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			ParentStepID: &condA, Branch: ptr("then"), Ord: 0,
			Kind: "approval", WorkflowRoleKey: ptr("fin_dir"), SLAHours: ptr(48),
		})
		condB := seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: 2, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("1000000000.00"),
		})
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			ParentStepID: &condB, Branch: ptr("then"), Ord: 0,
			Kind: "approval", WorkflowRoleKey: ptr("cfo"), SLAHours: ptr(72),
		})
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			ParentStepID: &condB, Branch: ptr("else"), Ord: 0,
			Kind: "autoapprove",
		})
		activateApprovalPolicyVersion(t, super, versionID)

		if _, err := arm(t, app, tenantID, invoiceID, "fp-closure-leg-a", "test-actor"); err != nil {
			t.Fatalf("ArmTx: %v", err)
		}
		assertClosure(t, invoiceID, "approved", true)
	})

	t.Run("leg b: a notify-only policy closes the run even though auto is false", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-06 closure-leg-b-notify-only")
		entityID := seedBusinessEntity(t, super, tenantID, "Closure Leg B Corp")
		invoiceID := seedInvoice(t, super, tenantID, entityID, "closure-leg-b-invoice")

		policyID := seedApprovalPolicy(t, super, tenantID, "Leg B policy")
		versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: 0, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app"),
		})
		activateApprovalPolicyVersion(t, super, versionID)

		if _, err := arm(t, app, tenantID, invoiceID, "fp-closure-leg-b", "test-actor"); err != nil {
			t.Fatalf("ArmTx: %v", err)
		}
		assertClosure(t, invoiceID, "approved", true)
	})

	t.Run("leg c: one staffed approval step leaves the run open", func(t *testing.T) {
		tenantID := policyTenant(t, super, "APPR-06 closure-leg-c-staffed")
		entityID := seedBusinessEntity(t, super, tenantID, "Closure Leg C Corp")
		invoiceID := seedInvoice(t, super, tenantID, entityID, "closure-leg-c-invoice")

		roleID := seedWorkflowRole(t, super, tenantID, "leg-c-role", "Leg C Role")
		adminID := uuid.NewString()
		seedMembership(t, super, tenantID, adminID, "admin", "active")
		staffWorkflowRole(t, super, tenantID, roleID, adminID, 0)

		policyID := seedApprovalPolicy(t, super, tenantID, "Leg C policy")
		versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("leg-c-role"), SLAHours: ptr(48),
		})
		activateApprovalPolicyVersion(t, super, versionID)

		if _, err := arm(t, app, tenantID, invoiceID, "fp-closure-leg-c", "test-actor"); err != nil {
			t.Fatalf("ArmTx: %v", err)
		}
		assertClosure(t, invoiceID, "open", false)
	})
}

// --- APPR-06-05 AC-6,7: unsignable roles still arm pending, arming computes no blockedness

// TestArm_UnsignableRolesStillArmPending: four workflow roles in one tenant —
// ceo_none (zero holders), cfo_susp (sole holder suspended), prep_only (sole holder
// active but role='preparer'), fin_dir_ok (sole holder active admin, Q1's signable
// control) — named by one version's four root approval steps, in that order. The
// fixture read-back BEFORE arming proves holder counts 0/1/1/1 and each holder's
// stored role+status, so the four legs cannot silently collapse into one shape and
// pass for the wrong reason. All four must arm identically — pending, run open, zero
// approval_decisions rows — because arming computes no blockedness (AC-7): the
// signable role's step is written exactly like the three unsignable ones.
func TestArm_UnsignableRolesStillArmPending(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-unsignable-roles-pending")
	entityID := seedBusinessEntity(t, super, tenantID, "Unsignable Roles Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "unsignable-roles-invoice-1")

	seedWorkflowRole(t, super, tenantID, "ceo_none", "CEO (zero holders)")

	cfoSuspID := seedWorkflowRole(t, super, tenantID, "cfo_susp", "CFO (suspended holder)")
	cfoUserID := uuid.NewString()
	seedMembership(t, super, tenantID, cfoUserID, "admin", "suspended")
	staffWorkflowRole(t, super, tenantID, cfoSuspID, cfoUserID, 0)

	prepOnlyID := seedWorkflowRole(t, super, tenantID, "prep_only", "Preparer-only holder")
	prepUserID := uuid.NewString()
	seedMembership(t, super, tenantID, prepUserID, "preparer", "active")
	staffWorkflowRole(t, super, tenantID, prepOnlyID, prepUserID, 0)

	finDirOkID := seedWorkflowRole(t, super, tenantID, "fin_dir_ok", "Fin Dir (signable control)")
	finDirUserID := uuid.NewString()
	seedMembership(t, super, tenantID, finDirUserID, "admin", "active")
	staffWorkflowRole(t, super, tenantID, finDirOkID, finDirUserID, 0)

	// Fixture assertion BEFORE arming: holder counts 0/1/1/1 and each holder's stored
	// role+status, read back as one query ordered by role key.
	type holder struct{ RoleKey, Role, Status string }
	rows, err := super.Query(context.Background(),
		`SELECT r.key, m.role, m.status
		   FROM workflow_roles r
		   JOIN workflow_role_members wrm ON wrm.workflow_role_id = r.id
		   JOIN memberships m ON m.tenant_id = wrm.tenant_id AND m.user_id = wrm.user_id
		  WHERE r.tenant_id = $1
		  ORDER BY r.key`, tenantID)
	if err != nil {
		t.Fatalf("read back fixture holders: %v", err)
	}
	var holders []holder
	for rows.Next() {
		var h holder
		if err := rows.Scan(&h.RoleKey, &h.Role, &h.Status); err != nil {
			t.Fatalf("scan holder: %v", err)
		}
		holders = append(holders, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read back fixture holders: %v", err)
	}
	wantHolders := []holder{
		{"cfo_susp", "admin", "suspended"},
		{"fin_dir_ok", "admin", "active"},
		{"prep_only", "preparer", "active"},
	}
	if !reflect.DeepEqual(holders, wantHolders) {
		t.Fatalf("fixture holders = %+v, want %+v (ceo_none has none; the other three exactly one each)", holders, wantHolders)
	}

	policyID := seedApprovalPolicy(t, super, tenantID, "Unsignable roles policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("ceo_none"), SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 1, Kind: "approval", WorkflowRoleKey: ptr("cfo_susp"), SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 2, Kind: "approval", WorkflowRoleKey: ptr("prep_only"), SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 3, Kind: "approval", WorkflowRoleKey: ptr("fin_dir_ok"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-unsignable-roles", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.Steps != 4 {
		t.Fatalf("Steps = %d, want 4", res.Steps)
	}
	if res.Closed {
		t.Error("Closed = true, want false — arming computes no blockedness, all four steps are pending")
	}

	run := oneApprovalRun(t, super, invoiceID)
	if run.State != "open" {
		t.Errorf("run.State = %q, want open", run.State)
	}
	if run.ClosedBy != nil {
		t.Errorf("run.ClosedBy = %v, want NULL", run.ClosedBy)
	}

	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 4 {
		t.Fatalf("runStepsOf = %d rows, want 4", len(steps))
	}
	wantKeys := []string{"ceo_none", "cfo_susp", "prep_only", "fin_dir_ok"}
	for i, want := range wantKeys {
		if steps[i].State != "pending" {
			t.Errorf("steps[%d] (%s) state = %q, want pending — the signable role must be written identically to the unsignable ones", i, want, steps[i].State)
		}
		if steps[i].WorkflowRoleKey == nil || *steps[i].WorkflowRoleKey != want {
			t.Errorf("steps[%d].WorkflowRoleKey = %v, want %q", i, steps[i].WorkflowRoleKey, want)
		}
	}

	if n := rowCount(t, super, "approval_decisions", tenantID); n != 0 {
		t.Errorf("approval_decisions rows = %d, want 0", n)
	}
}

// --- APPR-06-05 AC-6: a soft-deleted role still arms pending -------------------------

// TestArm_SoftDeletedRoleStillArmsPending: a role sealed into a policy step while
// live, then soft-deleted before the arm — the step is still pending, its
// workflow_role_key byte-identical to the seeded key, the run stays open.
// TestApprovalSteps_DeletedRoleKeyResolvesToBlocked already pins that the key
// "resolves to blocked at read time, not here"; this is the arm-time half of that
// posture — arming itself does no role lookup at all.
func TestArm_SoftDeletedRoleStillArmsPending(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-06 arm-soft-deleted-role-pending")
	entityID := seedBusinessEntity(t, super, tenantID, "Soft Deleted Role Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "soft-deleted-role-invoice-1")

	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")

	policyID := seedApprovalPolicy(t, super, tenantID, "Soft-deleted role policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("tax-reviewer"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID) // sealed + activated while the role is still live

	softDeleteWorkflowRole(t, super, roleID)

	res, err := arm(t, app, tenantID, invoiceID, "fp-soft-deleted-role", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	if res.Steps != 1 {
		t.Fatalf("Steps = %d, want 1", res.Steps)
	}
	if res.Closed {
		t.Error("Closed = true, want false — the approval step is pending")
	}

	run := oneApprovalRun(t, super, invoiceID)
	if run.State != "open" {
		t.Errorf("run.State = %q, want open", run.State)
	}
	steps := runStepsOf(t, super, run.ID)
	if len(steps) != 1 {
		t.Fatalf("runStepsOf = %d rows, want 1", len(steps))
	}
	if steps[0].State != "pending" {
		t.Errorf("step state = %q, want pending", steps[0].State)
	}
	if steps[0].WorkflowRoleKey == nil || *steps[0].WorkflowRoleKey != "tax-reviewer" {
		t.Errorf("step workflow_role_key = %v, want \"tax-reviewer\" byte-identical to the seeded key", steps[0].WorkflowRoleKey)
	}

	// Re-read the role's own deleted_at, so this case cannot silently become "role
	// still live".
	after := roleRow(t, super, roleID)
	if after.DeletedAt == nil {
		t.Fatal("workflow_roles.deleted_at is NULL after softDeleteWorkflowRole, want non-NULL")
	}
}

// --- APSC-03-01: ArmTx arms the first matching policy -------------------------------

// scopeSpec is a version's scope columns; zero value is a catch-all. Band bounds are decimal text.
type scopeSpec struct {
	DocTypes, EntityIDs, RoleIDs []string
	BandLower, BandUpper         *string
	BandCurrency                 *string
}

// seedScopedPolicyDraft inserts a live policy at an explicit position and an unsealed
// version carrying the scopes. Scopes can only be written before the seal.
func seedScopedPolicyDraft(t *testing.T, super *pgxpool.Pool, tenantID, name string, position int, s scopeSpec) (policyID, versionID string) {
	t.Helper()
	ctx := context.Background()
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policies (tenant_id, name, position) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, name, position).Scan(&policyID); err != nil {
		t.Fatalf("seed policy %q at position %d: %v", name, position, err)
	}
	orEmpty := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policy_versions
		     (tenant_id, policy_id, version, scope_document_types, scope_entity_ids, scope_workflow_role_ids,
		      scope_amount_lower, scope_amount_upper, scope_amount_currency)
		 VALUES ($1, $2, 1, $3::text[], $4::text[]::uuid[], $5::text[]::uuid[],
		         $6::text::numeric, $7::text::numeric, $8)
		 RETURNING id`,
		tenantID, policyID, orEmpty(s.DocTypes), orEmpty(s.EntityIDs), orEmpty(s.RoleIDs),
		s.BandLower, s.BandUpper, s.BandCurrency).Scan(&versionID); err != nil {
		t.Fatalf("seed version of %q: %v", name, err)
	}
	return policyID, versionID
}

// seedScopedPolicy is seedScopedPolicyDraft plus one approval step naming roleKey, sealed and active.
func seedScopedPolicy(t *testing.T, super *pgxpool.Pool, tenantID, name string, position int, s scopeSpec, roleKey string) (policyID, versionID string) {
	t.Helper()
	policyID, versionID = seedScopedPolicyDraft(t, super, tenantID, name, position, s)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr(roleKey), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionID)
	return policyID, versionID
}

// seedCreditNote seeds an original and a credit note on it; invoices_original_matches_type
// needs document_type and original_invoice_id set in one UPDATE.
func seedCreditNote(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string) string {
	t.Helper()
	original := seedInvoice(t, super, tenantID, entityID, number+"-original")
	note := seedInvoice(t, super, tenantID, entityID, number)
	tag, err := super.Exec(context.Background(),
		`UPDATE invoices SET document_type = 'credit_note', original_invoice_id = $2 WHERE id = $1`, note, original)
	if err != nil {
		t.Fatalf("make %s a credit note: %v", number, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("make %s a credit note affected %d rows, want 1", number, tag.RowsAffected())
	}
	return note
}

// setInvoiceMoney sets currency and total; nil writes NULL.
func setInvoiceMoney(t *testing.T, super *pgxpool.Pool, invoiceID string, currency, total *string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE invoices SET currency = $2, total = $3::text::numeric WHERE id = $1`, invoiceID, currency, total)
	if err != nil {
		t.Fatalf("set invoice %s money: %v", invoiceID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set invoice %s money affected %d rows, want 1", invoiceID, tag.RowsAffected())
	}
}

// armedVersion arms the invoice and returns the version the run names; "" means nothing armed.
func armedVersion(t *testing.T, super, app *pgxpool.Pool, tenantID, invoiceID string) string {
	t.Helper()
	res, err := arm(t, app, tenantID, invoiceID, "fp-"+invoiceID, "test-actor")
	if err != nil {
		t.Fatalf("ArmTx(%s): %v", invoiceID, err)
	}
	if res.RunID == "" {
		return ""
	}
	return oneApprovalRun(t, super, invoiceID).PolicyVersionID
}

func pendingRoleKeys(t *testing.T, super *pgxpool.Pool, runID string) []string {
	t.Helper()
	keys := []string{}
	for _, s := range runStepsOf(t, super, runID) {
		if s.State == "pending" && s.WorkflowRoleKey != nil {
			keys = append(keys, *s.WorkflowRoleKey)
		}
	}
	return keys
}

func TestArm_LowerPositionWinsOverThePolicyPublishedLast(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 lower-position-wins")
	entityID := seedBusinessEntity(t, super, tenantID, "Arm Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "lower-position-1")

	_, versionA := seedScopedPolicy(t, super, tenantID, "A first in order", 1, scopeSpec{}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B published last", 2, scopeSpec{}, "cfo")

	if got := armedVersion(t, super, app, tenantID, invoiceID); got != versionA {
		t.Fatalf("armed version = %s, want A's %s (position 1); B is %s", got, versionA, versionB)
	}
	if got := pendingRoleKeys(t, super, oneApprovalRun(t, super, invoiceID).ID); !reflect.DeepEqual(got, []string{"fin_mgr"}) {
		t.Errorf("pending steps = %v, want [fin_mgr]", got)
	}
}

func TestArm_CreditNoteArmsTheDocumentTypePolicy(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 credit-note-policy")
	entityID := seedBusinessEntity(t, super, tenantID, "Arm Corp")
	note := seedCreditNote(t, super, tenantID, entityID, "doc-type-note-1")
	invoice := seedInvoice(t, super, tenantID, entityID, "doc-type-invoice-1")

	_, versionA := seedScopedPolicy(t, super, tenantID, "A credit notes", 1, scopeSpec{DocTypes: []string{"credit_note"}}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")

	if got := armedVersion(t, super, app, tenantID, note); got != versionA {
		t.Errorf("credit note armed %s, want A's %s", got, versionA)
	}
	if got := armedVersion(t, super, app, tenantID, invoice); got != versionB {
		t.Errorf("commercial invoice armed %s, want B's %s", got, versionB)
	}
}

func TestArm_ClientScopeArmsOnlyItsClient(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 client-scope")
	e1 := seedBusinessEntity(t, super, tenantID, "Client One")
	e2 := seedBusinessEntity(t, super, tenantID, "Client Two")
	inE1 := seedInvoice(t, super, tenantID, e1, "client-e1-1")
	inE2 := seedInvoice(t, super, tenantID, e2, "client-e2-1")

	_, versionA := seedScopedPolicy(t, super, tenantID, "A client one", 1, scopeSpec{EntityIDs: []string{e1}}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")

	if got := armedVersion(t, super, app, tenantID, inE1); got != versionA {
		t.Errorf("invoice in E1 armed %s, want A's %s", got, versionA)
	}
	if got := armedVersion(t, super, app, tenantID, inE2); got != versionB {
		t.Errorf("invoice in E2 armed %s, want B's %s", got, versionB)
	}
}

func TestArm_AmountBandNeedsItsCurrency(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 amount-band-currency")
	entityID := seedBusinessEntity(t, super, tenantID, "Band Corp")

	_, versionA := seedScopedPolicy(t, super, tenantID, "A NGN band", 1,
		scopeSpec{BandLower: ptr("5000000.00"), BandUpper: ptr("50000000.00"), BandCurrency: ptr("NGN")}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")

	for _, c := range []struct {
		name            string
		currency, total *string
		want            string
	}{
		{"NGN at the lower bound", ptr("NGN"), ptr("5000000.00"), versionA},
		{"NGN at the upper bound", ptr("NGN"), ptr("50000000.00"), versionA},
		{"USD with a total inside the numbers", ptr("USD"), ptr("10000000.00"), versionB},
		{"NGN above the upper bound", ptr("NGN"), ptr("50000000.01"), versionB},
		{"NGN with NULL total", ptr("NGN"), nil, versionB},
		{"NULL currency", nil, ptr("10000000.00"), versionB},
	} {
		inv := seedInvoice(t, super, tenantID, entityID, "band-"+uuid.NewString()[:8])
		setInvoiceMoney(t, super, inv, c.currency, c.total)
		if got := armedVersion(t, super, app, tenantID, inv); got != c.want {
			t.Errorf("%s: armed %s, want %s", c.name, got, c.want)
		}
	}
}

func TestArm_CreatorRoleScopeMatchesTheCreatorsLiveRole(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 creator-role")
	entityID := seedBusinessEntity(t, super, tenantID, "Role Corp")
	roleID := seedWorkflowRole(t, super, tenantID, "preparers", "Preparers")
	member := seedMemberOf(t, super, tenantID, memberSpec{role: "preparer", roleID: roleID})
	outsider := seedMemberOf(t, super, tenantID, memberSpec{role: "preparer"})

	_, versionA := seedScopedPolicy(t, super, tenantID, "A preparers", 1, scopeSpec{RoleIDs: []string{roleID}}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")

	created := func(number string, actor *string) string {
		inv := seedInvoice(t, super, tenantID, entityID, number)
		if actor != nil {
			seedHistory(t, super, tenantID, inv, nil, *actor)
		}
		return inv
	}
	for _, c := range []struct {
		name string
		inv  string
		want string
	}{
		{"created by a role member", created("creator-member", &member), versionA},
		{"created by extraction-worker", created("creator-worker", ptr("extraction-worker")), versionB},
		{"created by system", created("creator-system", ptr("system")), versionB},
		{"created by backfill-source-rows", created("creator-backfill", ptr("backfill-source-rows")), versionB},
		{"created by a member of no role", created("creator-outsider", &outsider), versionB},
		{"no creation history row", created("creator-none", nil), versionB},
	} {
		if got := armedVersion(t, super, app, tenantID, c.inv); got != c.want {
			t.Errorf("%s: armed %s, want %s", c.name, got, c.want)
		}
	}

	softDeleteWorkflowRole(t, super, roleID)
	after := created("creator-member-after-delete", &member)
	if got := armedVersion(t, super, app, tenantID, after); got != versionB {
		t.Errorf("created by a member of a soft-deleted role: armed %s, want B's %s", got, versionB)
	}
}

func TestArm_EverySetScopeMustMatch(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 every-scope")
	e1 := seedBusinessEntity(t, super, tenantID, "Client One")
	e2 := seedBusinessEntity(t, super, tenantID, "Client Two")
	noteE1 := seedCreditNote(t, super, tenantID, e1, "every-note-e1")
	noteE2 := seedCreditNote(t, super, tenantID, e2, "every-note-e2")

	_, versionA := seedScopedPolicy(t, super, tenantID, "A credit notes of E1", 1,
		scopeSpec{DocTypes: []string{"credit_note"}, EntityIDs: []string{e1}}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")

	if got := armedVersion(t, super, app, tenantID, noteE1); got != versionA {
		t.Errorf("credit note in E1 armed %s, want A's %s", got, versionA)
	}
	if got := armedVersion(t, super, app, tenantID, noteE2); got != versionB {
		t.Errorf("credit note in E2 armed %s, want B's %s (the client scope fails A)", got, versionB)
	}
}

func TestArm_SoftDeletedPolicysActiveVersionNeverArms(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 soft-deleted-policy")
	entityID := seedBusinessEntity(t, super, tenantID, "Arm Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "soft-deleted-policy-1")

	_, versionB := seedScopedPolicy(t, super, tenantID, "B live catch-all", 2, scopeSpec{}, "cfo")
	policyA, versionA := seedScopedPolicy(t, super, tenantID, "A deleted catch-all", 1, scopeSpec{}, "fin_mgr")
	softDeleteApprovalPolicy(t, super, policyA)

	var active bool
	if err := super.QueryRow(context.Background(),
		`SELECT is_active FROM approval_policy_versions WHERE id = $1`, versionA).Scan(&active); err != nil || !active {
		t.Fatalf("fixture: A's version is_active = %v (err %v), want true under a deleted policy", active, err)
	}
	if got := armedVersion(t, super, app, tenantID, invoiceID); got != versionB {
		t.Errorf("armed %s, want B's %s (a deleted policy never arms)", got, versionB)
	}
}

func TestArm_NoMatchingPolicyWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 no-match")
	entityID := seedBusinessEntity(t, super, tenantID, "Arm Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "no-match-invoice-1")
	note := seedCreditNote(t, super, tenantID, entityID, "no-match-note-1")

	_, versionA := seedScopedPolicy(t, super, tenantID, "A credit notes only", 1, scopeSpec{DocTypes: []string{"credit_note"}}, "fin_mgr")

	res, err := arm(t, app, tenantID, invoiceID, "fp-no-match", "test-actor")
	if err != nil {
		t.Fatalf("ArmTx with no matching policy: %v, want nil error", err)
	}
	if res != (ArmResult{}) {
		t.Errorf("ArmTx = %+v, want the zero ArmResult", res)
	}
	for _, table := range []string{"approval_runs", "approval_run_steps"} {
		if n := rowCount(t, super, table, tenantID); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
	if n := auditCount(t, super, tenantID, "invoice.approval_armed"); n != 0 {
		t.Errorf("invoice.approval_armed rows = %d, want 0", n)
	}

	// Control: the credit note in the same tenant arms A.
	if got := armedVersion(t, super, app, tenantID, note); got != versionA {
		t.Errorf("control credit note armed %s, want A's %s", got, versionA)
	}
}

func TestArm_BandSelectsAndTheFlowConditionStillBranches(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 band-and-condition")
	entityID := seedBusinessEntity(t, super, tenantID, "Band Corp")

	_, versionA := seedScopedPolicyDraft(t, super, tenantID, "A band with a condition", 1,
		scopeSpec{BandLower: ptr("0.00"), BandUpper: ptr("1000000.00"), BandCurrency: ptr("NGN")})
	cond := seedApprovalPolicyStepInLane(t, super, tenantID, versionA, seedStepSpec{
		Ord: 0, Kind: "condition", CondOp: ptr(">"), CondAmount: ptr("500000.00"),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionA, seedStepSpec{
		ParentStepID: &cond, Branch: ptr("then"), Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("cfo"), SLAHours: ptr(24),
	})
	seedApprovalPolicyStepInLane(t, super, tenantID, versionA, seedStepSpec{
		ParentStepID: &cond, Branch: ptr("else"), Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("fin_mgr"), SLAHours: ptr(24),
	})
	activateApprovalPolicyVersion(t, super, versionA)
	seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "fin_dir")

	for _, c := range []struct{ total, wantRole string }{{"750000.00", "cfo"}, {"250000.00", "fin_mgr"}} {
		inv := seedInvoice(t, super, tenantID, entityID, "band-cond-"+c.total)
		setInvoiceMoney(t, super, inv, ptr("NGN"), ptr(c.total))
		if got := armedVersion(t, super, app, tenantID, inv); got != versionA {
			t.Errorf("NGN %s armed %s, want A's %s", c.total, got, versionA)
			continue
		}
		if got := pendingRoleKeys(t, super, oneApprovalRun(t, super, inv).ID); !reflect.DeepEqual(got, []string{c.wantRole}) {
			t.Errorf("NGN %s pending steps = %v, want [%s]", c.total, got, c.wantRole)
		}
	}
}

func TestArm_ReadsTheCreatorOnlyWhenARoleScopeIsInForce(t *testing.T) {
	super, _ := dbTestPools(t)
	traced, rec := tracedAppPool(t)

	roleless := policyTenant(t, super, "APSC-03-01 creator-read-none")
	e0 := seedBusinessEntity(t, super, roleless, "Corp")
	inv0 := seedInvoice(t, super, roleless, e0, "creator-read-none-1")
	seedScopedPolicy(t, super, roleless, "type scope", 1, scopeSpec{DocTypes: []string{"commercial_invoice"}}, "fin_mgr")
	seedScopedPolicy(t, super, roleless, "catch-all", 2, scopeSpec{}, "cfo")

	roled := policyTenant(t, super, "APSC-03-01 creator-read-role")
	e1 := seedBusinessEntity(t, super, roled, "Corp")
	inv1 := seedInvoice(t, super, roled, e1, "creator-read-role-1")
	roleID := seedWorkflowRole(t, super, roled, "preparers", "Preparers")
	creator := seedMemberOf(t, super, roled, memberSpec{role: "preparer", roleID: roleID})
	seedHistory(t, super, roled, inv1, nil, creator)
	seedScopedPolicy(t, super, roled, "role scope", 1, scopeSpec{RoleIDs: []string{roleID}}, "fin_mgr")
	seedScopedPolicy(t, super, roled, "catch-all", 2, scopeSpec{}, "cfo")

	for _, c := range []struct {
		name            string
		tenant, invoice string
		wantHistory     int
	}{{"no role scope in force", roleless, inv0, 0}, {"a role scope in force", roled, inv1, 1}} {
		t.Run(c.name, func(t *testing.T) {
			rec.reset()
			res, err := arm(t, traced, c.tenant, c.invoice, "fp-creator-read", "test-actor")
			if err != nil || res.RunID == "" {
				t.Fatalf("ArmTx = %+v, %v; want a run (a policy matches)", res, err)
			}
			if got := rec.mentioning("invoice_status_history"); len(got) != c.wantHistory {
				t.Errorf("statements reading invoice_status_history = %d, want %d: %v", len(got), c.wantHistory, got)
			}
		})
	}
}

// abortedTx runs fn in a tenant tx that a caught division by zero has already aborted,
// and returns fn's error; the tx rolls back.
func abortedTx(t *testing.T, app *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) error {
	t.Helper()
	var got error
	_ = db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `SELECT 1/0`); err == nil {
			t.Error("SELECT 1/0 succeeded, the tx is not aborted")
		}
		got = fn(tx)
		return got
	})
	return got
}

func TestArm_InForceReadErrorIsReturned(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 in-force-read-error")
	entityID := seedBusinessEntity(t, super, tenantID, "Arm Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "in-force-error-1")
	seedScopedPolicy(t, super, tenantID, "catch-all", 1, scopeSpec{}, "fin_mgr")

	var res ArmResult
	err := abortedTx(t, app, tenantID, func(tx pgx.Tx) error {
		var err error
		res, err = ArmTx(context.Background(), tx, tenantID, invoiceID, "fp-error", "test-actor")
		return err
	})
	if err == nil {
		t.Fatalf("ArmTx in an aborted tx = %+v, nil; want the error returned, not read as no match", res)
	}
	if code := pgCode(err); code != "25P02" {
		t.Errorf("SQLSTATE = %q, want 25P02", code)
	}
}

func TestReadCreatorRoleIDs_ErrorIsReturned(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-01 creator-read-error")
	entityID := seedBusinessEntity(t, super, tenantID, "Arm Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "creator-error-1")
	roleID := seedWorkflowRole(t, super, tenantID, "preparers", "Preparers")
	creator := seedMemberOf(t, super, tenantID, memberSpec{role: "preparer", roleID: roleID})
	seedHistory(t, super, tenantID, invoiceID, nil, creator)

	var roles map[string]bool
	err := abortedTx(t, app, tenantID, func(tx pgx.Tx) error {
		var err error
		roles, err = readCreatorRoleIDsTx(context.Background(), tx, invoiceID)
		return err
	})
	if err == nil {
		t.Fatalf("readCreatorRoleIDsTx in an aborted tx = %v, nil; want an error", roles)
	}
	if code := pgCode(err); code != "25P02" {
		t.Errorf("SQLSTATE = %q, want 25P02", code)
	}
	if roles != nil {
		t.Errorf("roles = %v on error, want nil", roles)
	}

	// Control: a live tx returns the creator's role.
	var live map[string]bool
	if err := db.WithinTenantTx(context.Background(), app, tenantID, func(tx pgx.Tx) error {
		var err error
		live, err = readCreatorRoleIDsTx(context.Background(), tx, invoiceID)
		return err
	}); err != nil {
		t.Fatalf("readCreatorRoleIDsTx: %v", err)
	}
	if want := map[string]bool{roleID: true}; !reflect.DeepEqual(live, want) {
		t.Errorf("creator roles = %v, want %v", live, want)
	}
}

// armedPayload arms the invoice and returns the raw payload of its approval_armed row.
func armedPayload(t *testing.T, super, app *pgxpool.Pool, tenantID, invoiceID string) []byte {
	t.Helper()
	if _, err := arm(t, app, tenantID, invoiceID, "fp-"+invoiceID, "test-actor"); err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	var payload []byte
	if err := super.QueryRow(context.Background(),
		`SELECT payload FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.approval_armed'`,
		tenantID).Scan(&payload); err != nil {
		t.Fatalf("read the audit row: %v", err)
	}
	return payload
}

func TestArm_AuditNamesThePolicyItsPositionAndTheMatchedScopes(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-02 audit-names-policy")
	entityID := seedBusinessEntity(t, super, tenantID, "Audit Corp")
	other := seedBusinessEntity(t, super, tenantID, "Other Corp")
	note := seedCreditNote(t, super, tenantID, entityID, "audit-names-note-1")
	setInvoiceMoney(t, super, note, ptr("NGN"), ptr("500.00"))

	seedScopedPolicy(t, super, tenantID, "X matches nothing", 1, scopeSpec{EntityIDs: []string{other}}, "cfo")
	policyA, versionA := seedScopedPolicy(t, super, tenantID, "A credit notes in band", 2, scopeSpec{
		DocTypes: []string{"credit_note"}, BandLower: ptr("0.00"), BandUpper: ptr("1000000.00"), BandCurrency: ptr("NGN"),
	}, "fin_mgr")
	seedScopedPolicy(t, super, tenantID, "B catch-all", 3, scopeSpec{}, "cfo")

	var body struct {
		PolicyID        string   `json:"policy_id"`
		Position        int      `json:"position"`
		MatchedScopes   []string `json:"matched_scopes"`
		PolicyVersionID string   `json:"policy_version_id"`
	}
	if err := json.Unmarshal(armedPayload(t, super, app, tenantID, note), &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if body.PolicyID != policyA || body.Position != 2 || body.PolicyVersionID != versionA {
		t.Errorf("payload policy/position/version = %s/%d/%s, want %s/2/%s", body.PolicyID, body.Position, body.PolicyVersionID, policyA, versionA)
	}
	if want := []string{"document_type", "amount_band"}; !reflect.DeepEqual(body.MatchedScopes, want) {
		t.Errorf("matched_scopes = %v, want %v", body.MatchedScopes, want)
	}
}

func TestArm_AuditCatchAllMatchNamesNoScope(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-02 audit-catch-all")
	entityID := seedBusinessEntity(t, super, tenantID, "Audit Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "audit-catch-all-1")
	seedScopedPolicy(t, super, tenantID, "A catch-all", 1, scopeSpec{}, "fin_mgr")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(armedPayload(t, super, app, tenantID, invoiceID), &raw); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got := string(raw["matched_scopes"]); got != "[]" {
		t.Errorf("matched_scopes = %q, want []", got)
	}
}

// --- APSC-03 QA: adversarial coverage ------------------------------------------------

func TestArm_DebitNoteArmsItsOwnDocumentTypePolicy(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-QA debit-note")
	entityID := seedBusinessEntity(t, super, tenantID, "Note Corp")
	debit := seedCreditNote(t, super, tenantID, entityID, "qa-debit")
	if _, err := super.Exec(context.Background(), `UPDATE invoices SET document_type = 'debit_note' WHERE id = $1`, debit); err != nil {
		t.Fatalf("make the note a debit note: %v", err)
	}
	credit := seedCreditNote(t, super, tenantID, entityID, "qa-credit")
	invoice := seedInvoice(t, super, tenantID, entityID, "qa-invoice")

	_, vDebit := seedScopedPolicy(t, super, tenantID, "A debit notes", 1, scopeSpec{DocTypes: []string{"debit_note"}}, "fin_mgr")
	_, vCredit := seedScopedPolicy(t, super, tenantID, "B credit notes", 2, scopeSpec{DocTypes: []string{"credit_note"}}, "cfo")
	_, vRest := seedScopedPolicy(t, super, tenantID, "C catch-all", 3, scopeSpec{}, "fin_dir")

	for _, c := range []struct{ name, inv, want string }{
		{"debit note", debit, vDebit}, {"credit note", credit, vCredit}, {"commercial invoice", invoice, vRest},
	} {
		if got := armedVersion(t, super, app, tenantID, c.inv); got != c.want {
			t.Errorf("%s armed %s, want %s", c.name, got, c.want)
		}
	}
}

func TestArm_NaNTotalNeverMatchesABand(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-QA nan-total")
	entityID := seedBusinessEntity(t, super, tenantID, "Nan Corp")
	inv := seedInvoice(t, super, tenantID, entityID, "qa-nan")
	setInvoiceMoney(t, super, inv, ptr("NGN"), ptr("NaN"))

	seedScopedPolicy(t, super, tenantID, "A band", 1, scopeSpec{BandLower: ptr("0.00"), BandCurrency: ptr("NGN")}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")

	if got := armedVersion(t, super, app, tenantID, inv); got != versionB {
		t.Errorf("NaN total armed %q, want the catch-all %s and no error", got, versionB)
	}
}

func TestArm_InactiveVersionOfALowerPolicyNeverArms(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-QA inactive-version")
	entityID := seedBusinessEntity(t, super, tenantID, "Inactive Corp")
	inv := seedInvoice(t, super, tenantID, entityID, "qa-inactive")

	_, versionA := seedScopedPolicy(t, super, tenantID, "A switched off", 1, scopeSpec{}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")
	if _, err := super.Exec(context.Background(), `UPDATE approval_policy_versions SET is_active = false WHERE id = $1`, versionA); err != nil {
		t.Fatalf("deactivate A: %v", err)
	}
	if got := armedVersion(t, super, app, tenantID, inv); got != versionB {
		t.Errorf("armed %s, want B's %s (A's only version is inactive)", got, versionB)
	}
}

func TestArm_NoPolicyInForceIssuesOnlyTheInForceRead(t *testing.T) {
	super, _ := dbTestPools(t)
	traced, rec := tracedAppPool(t)
	tenantID := policyTenant(t, super, "APSC-03-QA ungoverned")
	entityID := seedBusinessEntity(t, super, tenantID, "Ungoverned Corp")
	inv := seedInvoice(t, super, tenantID, entityID, "qa-ungoverned")

	rec.reset()
	if res, err := arm(t, traced, tenantID, inv, "fp-ungoverned", "test-actor"); err != nil || res != (ArmResult{}) {
		t.Fatalf("ArmTx = %+v, %v; want the zero result", res, err)
	}
	if got := rec.mentioning("invoices"); len(got) != 0 {
		t.Errorf("an ungoverned tenant read the document: %v, want none", got)
	}
}

func TestArm_OnlyTheCreationRowNamesTheCreator(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-QA creation-row")
	entityID := seedBusinessEntity(t, super, tenantID, "Creator Corp")
	roleID := seedWorkflowRole(t, super, tenantID, "preparers", "Preparers")
	member := seedMemberOf(t, super, tenantID, memberSpec{role: "preparer", roleID: roleID})
	outsider := seedMemberOf(t, super, tenantID, memberSpec{role: "preparer"})

	_, versionA := seedScopedPolicy(t, super, tenantID, "A preparers", 1, scopeSpec{RoleIDs: []string{roleID}}, "fin_mgr")
	_, versionB := seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")

	// Created by an outsider; the role member only moved it along.
	byOutsider := seedInvoice(t, super, tenantID, entityID, "qa-created-by-outsider")
	seedHistory(t, super, tenantID, byOutsider, nil, outsider)
	seedHistory(t, super, tenantID, byOutsider, ptr("draft"), member)
	// Created by the role member; an outsider moved it along.
	byMember := seedInvoice(t, super, tenantID, entityID, "qa-created-by-member")
	seedHistory(t, super, tenantID, byMember, nil, member)
	seedHistory(t, super, tenantID, byMember, ptr("draft"), outsider)

	if got := armedVersion(t, super, app, tenantID, byOutsider); got != versionB {
		t.Errorf("created by an outsider, moved by a role member: armed %s, want B's %s", got, versionB)
	}
	if got := armedVersion(t, super, app, tenantID, byMember); got != versionA {
		t.Errorf("created by a role member, moved by an outsider: armed %s, want A's %s", got, versionA)
	}
}

func TestArm_CreatorWithADeletedAndALiveRoleMatchesTheLiveOne(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-QA live-and-deleted-role")
	entityID := seedBusinessEntity(t, super, tenantID, "Roles Corp")
	liveRole := seedWorkflowRole(t, super, tenantID, "live-role", "Live")
	goneRole := seedWorkflowRole(t, super, tenantID, "gone-role", "Gone")
	member := seedMemberOf(t, super, tenantID, memberSpec{role: "preparer", roleID: liveRole})
	staffWorkflowRole(t, super, tenantID, goneRole, member, 0)
	softDeleteWorkflowRole(t, super, goneRole)
	inv := seedInvoice(t, super, tenantID, entityID, "qa-two-roles")
	seedHistory(t, super, tenantID, inv, nil, member)

	_, versionGone := seedScopedPolicy(t, super, tenantID, "A deleted role", 1, scopeSpec{RoleIDs: []string{goneRole}}, "fin_mgr")
	_, versionLive := seedScopedPolicy(t, super, tenantID, "B live role", 2, scopeSpec{RoleIDs: []string{liveRole}}, "cfo")

	if got := armedVersion(t, super, app, tenantID, inv); got != versionLive {
		t.Errorf("armed %s, want the live role's policy %s, not the deleted role's %s", got, versionLive, versionGone)
	}
}

// Another tenant's policies and roles are invisible under RLS, so they never decide this tenant's document.
func TestArm_AnotherTenantsPolicyAndRoleNeverMatch(t *testing.T) {
	super, app := dbTestPools(t)
	tenantA := policyTenant(t, super, "APSC-03-QA cross-tenant A")
	tenantB := policyTenant(t, super, "APSC-03-QA cross-tenant B")
	entityA := seedBusinessEntity(t, super, tenantA, "Tenant A Corp")
	entityB := seedBusinessEntity(t, super, tenantB, "Tenant B Corp")
	docA := seedInvoice(t, super, tenantA, entityA, "qa-cross-a")
	docB := seedInvoice(t, super, tenantB, entityB, "qa-cross-b")

	// B's catch-all sits at position 1 and its role holds A's document creator.
	roleB := seedWorkflowRole(t, super, tenantB, "b-role", "B role")
	creator := seedMemberOf(t, super, tenantB, memberSpec{role: "preparer", roleID: roleB})
	seedHistory(t, super, tenantA, docA, nil, creator)
	_, versionBAll := seedScopedPolicy(t, super, tenantB, "B catch-all", 1, scopeSpec{}, "cfo")

	// A holds only a policy scoped to B's role and B's client.
	seedScopedPolicy(t, super, tenantA, "A scoped to B's ids", 1, scopeSpec{RoleIDs: []string{roleB}}, "fin_mgr")
	seedScopedPolicy(t, super, tenantA, "A scoped to B's client", 2, scopeSpec{EntityIDs: []string{entityB}}, "fin_mgr")

	res, err := arm(t, app, tenantA, docA, "fp-cross-a", "test-actor")
	if err != nil || res.RunID != "" {
		t.Fatalf("tenant A ArmTx = %+v, %v; want no run: no policy of A matches, B's catch-all is not A's", res, err)
	}
	if n := rowCount(t, super, "approval_runs", tenantA); n != 0 {
		t.Errorf("tenant A approval_runs = %d, want 0", n)
	}
	// Control: B's own document arms B's catch-all.
	if got := armedVersion(t, super, app, tenantB, docB); got != versionBAll {
		t.Errorf("tenant B armed %q, want its catch-all %s", got, versionBAll)
	}
}

// With the creator read blocked, ArmTx returns the error instead of reading "no roles" and
// falling to the catch-all.
func TestArm_CreatorRoleReadErrorIsReturnedByArmTx(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-QA creator-read-blocked")
	entityID := seedBusinessEntity(t, super, tenantID, "Blocked Corp")
	roleID := seedWorkflowRole(t, super, tenantID, "preparers", "Preparers")
	creator := seedMemberOf(t, super, tenantID, memberSpec{role: "preparer", roleID: roleID})
	inv := seedInvoice(t, super, tenantID, entityID, "qa-blocked-1")
	seedHistory(t, super, tenantID, inv, nil, creator)
	seedScopedPolicy(t, super, tenantID, "A preparers", 1, scopeSpec{RoleIDs: []string{roleID}}, "fin_mgr")
	seedScopedPolicy(t, super, tenantID, "B catch-all", 2, scopeSpec{}, "cfo")

	ctx := context.Background()
	holder, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the lock holder: %v", err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `LOCK TABLE invoice_status_history IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock invoice_status_history: %v", err)
	}

	var res ArmResult
	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '300ms'`); err != nil {
			return err
		}
		var err error
		res, err = ArmTx(ctx, tx, tenantID, inv, "fp-blocked", "test-actor")
		return err
	})
	if err == nil {
		t.Fatalf("ArmTx = %+v, nil; want the creator read's lock timeout returned", res)
	}
	if code := pgCode(err); code != "55P03" {
		t.Errorf("SQLSTATE = %q, want 55P03", code)
	}
	if n := rowCount(t, super, "approval_runs", tenantID); n != 0 {
		t.Errorf("approval_runs = %d after the error, want 0", n)
	}
}

func TestArm_AuditNamesEveryScopeInTheFixedOrder(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APSC-03-QA audit-four-scopes")
	entityID := seedBusinessEntity(t, super, tenantID, "Four Corp")
	roleID := seedWorkflowRole(t, super, tenantID, "preparers", "Preparers")
	creator := seedMemberOf(t, super, tenantID, memberSpec{role: "preparer", roleID: roleID})
	note := seedCreditNote(t, super, tenantID, entityID, "qa-four")
	setInvoiceMoney(t, super, note, ptr("NGN"), ptr("500.00"))
	seedHistory(t, super, tenantID, note, nil, creator)

	seedScopedPolicy(t, super, tenantID, "A all four", 1, scopeSpec{
		DocTypes: []string{"credit_note"}, EntityIDs: []string{entityID}, RoleIDs: []string{roleID},
		BandLower: ptr("0.00"), BandUpper: ptr("1000.00"), BandCurrency: ptr("NGN"),
	}, "fin_mgr")

	var body struct {
		MatchedScopes []string `json:"matched_scopes"`
	}
	if err := json.Unmarshal(armedPayload(t, super, app, tenantID, note), &body); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	want := []string{"document_type", "client", "amount_band", "creator_role"}
	if !reflect.DeepEqual(body.MatchedScopes, want) {
		t.Errorf("matched_scopes = %v, want %v", body.MatchedScopes, want)
	}
}
