package approval

// Store-adjacent specs for ArmTx: the tx-scoped write half of the arming engine —
// resolve the tenant's one active sealed version, materialise it against one invoice,
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
	"strings"
	"testing"
	"time"

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

	allowed := map[string]bool{"id": true, "run_id": true, "policy_version_id": true, "steps": true}
	for k := range body {
		if !allowed[k] {
			t.Errorf("payload key %q is not in the summary-only allowlist {id, run_id, policy_version_id, steps} — no invoice content", k)
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
