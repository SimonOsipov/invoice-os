// task-482 (APPR-06-06, Mode A): RED specs for approval.ArmTx wired into
// Store.ApplyValidation's promoting branch (store.go:1743-1748) -- not yet wired, so every
// arming assertion below fails; ApplyValidation itself is untouched. Fixtures port
// internal/approval's unexported seedApprovalPolicy/seedApprovalPolicyVersionN/
// seedApprovalPolicyStepInLane/activateApprovalPolicyVersion (policy_crud_test.go,
// policy_draft_test.go), unreachable from this package.
package invoice

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- fixtures: approval-policy seeding, ported from internal/approval ------

func seedApprovalPolicyFor(t *testing.T, super *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, name,
	).Scan(&id); err != nil {
		t.Fatalf("seed approval_policies: %v", err)
	}
	t.Cleanup(func() { teardownApprovalFixtureFor(t, super, tenantID) })
	return id
}

// teardownApprovalFixtureFor unwinds a tenant's approval rows before seedTenant's
// own cleanup deletes the tenant. Both guards this file trips would otherwise abort
// that delete SILENTLY (it discards its error) and leak the tenant and its invoices:
// approval_policy_versions_seal_guard refuses to delete a sealed version, and
// approval_runs' composite FK to invoices is ON DELETE RESTRICT. session_replication_role
// suppresses both; safe here because this drops rows rather than asserting on them.
// Ported from internal/approval's teardownSealedApprovalFixture.
func teardownApprovalFixtureFor(t *testing.T, super *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Errorf("teardown approval fixture %s: begin tx: %v", tenantID, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		t.Errorf("teardown approval fixture %s: set session_replication_role: %v", tenantID, err)
		return
	}
	for _, table := range []string{
		"approval_decisions", "approval_run_steps", "approval_runs",
		"approval_policy_steps", "approval_policy_versions", "approval_policies",
	} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id = $1`, tenantID); err != nil {
			t.Errorf("teardown approval fixture %s: delete %s: %v", tenantID, table, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("teardown approval fixture %s: commit: %v", tenantID, err)
	}
}

// seedApprovalPolicyVersionFor inserts version 1, unsealed and inactive.
func seedApprovalPolicyVersionFor(t *testing.T, super *pgxpool.Pool, tenantID, policyID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1) RETURNING id`,
		tenantID, policyID,
	).Scan(&id); err != nil {
		t.Fatalf("seed approval_policy_versions: %v", err)
	}
	return id
}

// approvalStepSpecFor is one root-level approval_policy_steps row (no branch/condition --
// this file's fixtures only ever need a single approval step).
type approvalStepSpecFor struct {
	Ord             int
	Kind            string
	WorkflowRoleKey *string
	SLAHours        *int
}

// seedApprovalStepFor must run BEFORE activateApprovalPolicyVersionFor --
// approval_policy_steps_content_lock refuses INSERTs into a sealed version.
func seedApprovalStepFor(t *testing.T, super *pgxpool.Pool, tenantID, versionID string, spec approvalStepSpecFor) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind, workflow_role_key, sla_hours)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		tenantID, versionID, spec.Ord, spec.Kind, spec.WorkflowRoleKey, spec.SLAHours,
	).Scan(&id); err != nil {
		t.Fatalf("seed approval_policy_steps: %v", err)
	}
	return id
}

// activateApprovalPolicyVersionFor seals AND activates in one statement --
// approval_policy_versions_active_is_sealed refuses is_active without sealed.
func activateApprovalPolicyVersionFor(t *testing.T, super *pgxpool.Pool, versionID string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions
		    SET sealed = true, is_active = true, published_at = now(), published_by = 'fixture'
		  WHERE id = $1`, versionID)
	if err != nil {
		t.Fatalf("activate approval_policy_versions %s: %v", versionID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("activate approval_policy_versions %s affected %d rows, want 1", versionID, tag.RowsAffected())
	}
}

// sealApprovalPolicyVersionFor seals WITHOUT activating -- the second leg of
// TestApplyValidation_NoActivePolicyLeavesTheTrailUnchanged (a sealed, non-active version).
func sealApprovalPolicyVersionFor(t *testing.T, super *pgxpool.Pool, versionID string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_policy_versions SET sealed = true WHERE id = $1`, versionID)
	if err != nil {
		t.Fatalf("seal approval_policy_versions %s: %v", versionID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("seal approval_policy_versions %s affected %d rows, want 1", versionID, tag.RowsAffected())
	}
}

// seedApprovalRunFor inserts one approval_runs row directly (state defaults to
// 'open') -- reimplements internal/approval's own seedApprovalRun
// (schema_constraints_test.go:213) here because that helper is unexported and
// unreachable from this package. task-483 (APPR-06-07).
func seedApprovalRunFor(t *testing.T, super *pgxpool.Pool, tenantID, invoiceID, versionID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, invoiceID, versionID, "fp-"+uuid.NewString(),
	).Scan(&id); err != nil {
		t.Fatalf("seed approval_runs: %v", err)
	}
	return id
}

// closeApprovalRunFor force-closes a run directly, for fixtures that need one already
// closed BEFORE the call under test runs (e.g. a zero-step 'approved' run) --
// bypasses both ArmTx's own closure branch and CancelLiveRunTx, each under test
// elsewhere. task-483 (APPR-06-07).
func closeApprovalRunFor(t *testing.T, super *pgxpool.Pool, runID, state, closedBy string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET state = $1, closed_at = now(), closed_by = $2 WHERE id = $3`,
		state, closedBy, runID)
	if err != nil {
		t.Fatalf("close approval_runs %s: %v", runID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("close approval_runs %s affected %d rows, want 1", runID, tag.RowsAffected())
	}
}

// approvalStepInLaneSpecFor is one approval_policy_steps row, including the
// condition-lane columns approvalStepSpecFor omits -- ported from internal/approval's
// seedStepSpec/seedApprovalPolicyStepInLane (policy_crud_test.go), unreachable from
// this package. task-483 (APPR-06-07).
type approvalStepInLaneSpecFor struct {
	ParentStepID    *string
	Branch          *string // nil at root; "then" or "else" inside a lane
	Ord             int
	Kind            string
	WorkflowRoleKey *string
	SLAHours        *int
	CondOp          *string
	CondAmount      *string
}

// seedApprovalStepInLaneFor must run BEFORE activateApprovalPolicyVersionFor -- same
// ordering rule as seedApprovalStepFor.
func seedApprovalStepInLaneFor(t *testing.T, super *pgxpool.Pool, tenantID, versionID string, spec approvalStepInLaneSpecFor) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_steps
		        (tenant_id, version_id, parent_step_id, branch, ord, kind, workflow_role_key, sla_hours, cond_op, cond_amount)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::text::numeric)
		 RETURNING id`,
		tenantID, versionID, spec.ParentStepID, spec.Branch, spec.Ord, spec.Kind,
		spec.WorkflowRoleKey, spec.SLAHours, spec.CondOp, spec.CondAmount,
	).Scan(&id); err != nil {
		t.Fatalf("seed approval_policy_steps (kind %s, ord %d): %v", spec.Kind, spec.Ord, err)
	}
	return id
}

// seedOneStepActivePolicyTenant is the shared fixture: a fresh tenant with one active
// version naming a single staffed approval role -- what every AC below arms against.
func seedOneStepActivePolicyTenant(t *testing.T, super *pgxpool.Pool, label string) (tenantID, entityID, versionID string) {
	t.Helper()
	tenantID = seedTenant(t, super, label+" tenant")
	entityID = seedEntity(t, super, tenantID, label+" entity")
	policyID := seedApprovalPolicyFor(t, super, tenantID, label+" policy")
	versionID = seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
	seedApprovalStepFor(t, super, tenantID, versionID, approvalStepSpecFor{
		Ord: 0, Kind: "approval", WorkflowRoleKey: strPtr("finance-lead"),
	})
	activateApprovalPolicyVersionFor(t, super, versionID)
	return tenantID, entityID, versionID
}

// --- AC-1: a clean promotion arms a run -------------------------------------

// TestApplyValidation_PromotionArmsARun: a tenant with an active sealed policy naming a
// staffed role, a clean draft -> ApplyValidation -> validated AND exactly one approval_runs
// row (open, content_fingerprint == evaluatedFingerprint, policy_version_id == the active
// version) with exactly one approval_run_steps row (kind approval, state pending). Fails
// today: zero runs (ArmTx is not called).
func TestApplyValidation_PromotionArmsARun(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "ARM-01")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ARM-01"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ruleSetVersionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	got, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID, fp)
	if err != nil {
		t.Fatalf("ApplyValidation (clean, active policy): want success, got: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("ApplyValidation returned status = %q, want %q", got.Status, StatusValidated)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, inv.ID); n != 1 {
		t.Fatalf("approval_runs rows for invoice = %d, want exactly 1 -- ArmTx did not run", n)
	}

	var runID, state, runFingerprint, policyVersionID string
	if err := super.QueryRow(ctx,
		`SELECT id, state, content_fingerprint, policy_version_id FROM approval_runs WHERE invoice_id = $1`,
		inv.ID,
	).Scan(&runID, &state, &runFingerprint, &policyVersionID); err != nil {
		t.Fatalf("read approval_runs row: %v", err)
	}
	if state != "open" {
		t.Errorf("approval_runs.state = %q, want %q", state, "open")
	}
	if runFingerprint != fp {
		t.Errorf("approval_runs.content_fingerprint = %q, want the evaluatedFingerprint %q", runFingerprint, fp)
	}
	if policyVersionID != versionID {
		t.Errorf("approval_runs.policy_version_id = %q, want the active version %q", policyVersionID, versionID)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM approval_run_steps WHERE run_id = $1`, runID); n != 1 {
		t.Fatalf("approval_run_steps rows for run = %d, want exactly 1", n)
	}
	var stepKind, stepState string
	if err := super.QueryRow(ctx,
		`SELECT kind, state FROM approval_run_steps WHERE run_id = $1`, runID,
	).Scan(&stepKind, &stepState); err != nil {
		t.Fatalf("read approval_run_steps row: %v", err)
	}
	if stepKind != "approval" {
		t.Errorf("approval_run_steps.kind = %q, want %q", stepKind, "approval")
	}
	if stepState != "pending" {
		t.Errorf("approval_run_steps.state = %q, want %q", stepState, "pending")
	}
}

// TestApplyValidation_ArmedRowSitsBetweenTransitionedAndValidated: the three audit rows
// this call writes, read ORDER BY id (never created_at -- all three share one transaction
// timestamp), are exactly invoice.transitioned, invoice.approval_armed, invoice.validated;
// all three carry the caller's subject as actor. Fails today: only two rows.
func TestApplyValidation_ArmedRowSitsBetweenTransitionedAndValidated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID, entityID, _ := seedOneStepActivePolicyTenant(t, super, "ARM-02")
	subject := memberSubject
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ARM-02"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ruleSetVersionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	if _, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID, fp); err != nil {
		t.Fatalf("ApplyValidation (clean, active policy): want success, got: %v", err)
	}

	rows, err := super.Query(ctx,
		`SELECT event, actor FROM audit_log
		  WHERE tenant_id = $1 AND event IN ('invoice.transitioned','invoice.approval_armed','invoice.validated')
		  ORDER BY id`, tenantID)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()

	var events []string
	for rows.Next() {
		var event, actor string
		if err := rows.Scan(&event, &actor); err != nil {
			t.Fatalf("scan audit_log row: %v", err)
		}
		events = append(events, event)
		if actor != subject {
			t.Errorf("audit_log %s actor = %q, want the caller's subject %q", event, actor, subject)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log: %v", err)
	}

	want := []string{"invoice.transitioned", "invoice.approval_armed", "invoice.validated"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("audit_log events ORDER BY id = %v, want %v", events, want)
	}
}

// --- AC-3: a blocked verdict arms nothing -----------------------------------

// TestApplyValidation_BlockedVerdictArmsNothing: same tenant/active policy, a draft
// carrying one severity:"error" violation -> stays draft, approval_runs empty, no
// invoice.approval_armed row, invoice.validated present with outcome "blocked". A
// positive control in the SAME tenant (a clean draft) DOES arm, so this cannot pass
// vacuously against a build where nothing ever arms. Fails today: the control leg fails.
func TestApplyValidation_BlockedVerdictArmsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID, entityID, _ := seedOneStepActivePolicyTenant(t, super, "ARM-03")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	ruleSetVersionID := seedRuleSetVersionID(t, super)

	control, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ARM-03-control"})
	if err != nil {
		t.Fatalf("Create (control): %v", err)
	}
	controlFP := contentFingerprint(control, control.LineItems)
	gotControl, err := store.ApplyValidation(c, control.ID, []Violation{}, ruleSetVersionID, controlFP)
	if err != nil {
		t.Fatalf("ApplyValidation (control, clean): want success, got: %v", err)
	}
	if gotControl.Status != StatusValidated {
		t.Errorf("control invoice status = %q, want %q", gotControl.Status, StatusValidated)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, control.ID); n != 1 {
		t.Fatalf("control invoice approval_runs rows = %d, want exactly 1 -- the positive control did not arm, "+
			"so this test cannot tell 'nothing arms at all' from 'blocked correctly arms nothing'", n)
	}

	blocked, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ARM-03-blocked"})
	if err != nil {
		t.Fatalf("Create (blocked): %v", err)
	}
	blockedFP := contentFingerprint(blocked, blocked.LineItems)
	vs := []Violation{{RuleKey: "ARM-03-rule", Severity: "error", Message: "blocking violation"}}

	gotBlocked, err := store.ApplyValidation(c, blocked.ID, vs, ruleSetVersionID, blockedFP)
	if err != nil {
		t.Fatalf("ApplyValidation (blocked): want a nil-error blocked outcome, got: %v", err)
	}
	if gotBlocked.Status != StatusDraft {
		t.Errorf("blocked invoice status = %q, want %q (a blocked verdict does not promote)", gotBlocked.Status, StatusDraft)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, blocked.ID); n != 0 {
		t.Errorf("blocked invoice approval_runs rows = %d, want 0 -- a blocked verdict must arm nothing", n)
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.approval_armed' AND payload->>'id' = $2`,
		tenantID, blocked.ID,
	); n != 0 {
		t.Errorf("invoice.approval_armed rows for the blocked invoice = %d, want 0", n)
	}

	var outcome string
	if err := super.QueryRow(ctx,
		`SELECT payload->>'outcome' FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.validated' AND payload->>'id' = $2`,
		tenantID, blocked.ID,
	).Scan(&outcome); err != nil {
		t.Fatalf("read invoice.validated outcome for the blocked invoice: %v", err)
	}
	if outcome != "blocked" {
		t.Errorf("invoice.validated outcome for the blocked invoice = %q, want %q", outcome, "blocked")
	}
}

// --- AC-2: a failing arm rolls back the whole promotion ----------------------

// TestArm_FailureRollsBackPromotion: a tenant with an active one-step policy over a clean
// draft that already carries an OPEN run (pre-inserted -- the exact state task-483's
// cancel exists to prevent) -> ApplyValidation -> ArmTx's own INSERT collides on
// approval_runs_one_open (23505) and the WHOLE promotion rolls back: status stays draft,
// violations/rule_set_version_id unchanged, no new history row, no new audit row of any of
// the three events, and the pre-inserted run is untouched. Does NOT use the long-actor
// lever (TestApplyValidation_LongActorRollsBackWholeTx's 256-char actor): that one raises
// 23514 inside transitionTx and never reaches ArmTx (D-B in task-482's plan). Fails today:
// the promotion commits and no error is returned.
func TestArm_FailureRollsBackPromotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, "ARM-04")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "ARM-04"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ruleSetVersionID := seedRuleSetVersionID(t, super)
	fp := contentFingerprint(inv, inv.LineItems)

	var preRunID string
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint, state)
		 VALUES ($1, $2, $3, 'pre-existing-open-run', 'open') RETURNING id`,
		tenantID, inv.ID, versionID,
	).Scan(&preRunID); err != nil {
		t.Fatalf("pre-insert open approval_runs row: %v", err)
	}

	before := snapshotInvoiceGateState(t, super, inv.ID)
	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")
	beforeValidated := auditCount(t, app, tenantID, "invoice.validated")
	beforeArmed := auditCount(t, app, tenantID, "invoice.approval_armed")

	_, err = store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID, fp)
	if err == nil {
		t.Fatal("ApplyValidation with a pre-existing open run succeeded, want a 23505 on approval_runs_one_open")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("ApplyValidation with a pre-existing open run: pgCode = %q, want 23505 (unique_violation): %v", code, err)
	}

	assertGateSnapshotUnchanged(t, before, snapshotInvoiceGateState(t, super, inv.ID), "ARM-04")
	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d (the whole tx rolled back)", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned {
		t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d", n, beforeTransitioned)
	}
	if n := auditCount(t, app, tenantID, "invoice.validated"); n != beforeValidated {
		t.Errorf("audit_log invoice.validated rows = %d, want unchanged %d", n, beforeValidated)
	}
	if n := auditCount(t, app, tenantID, "invoice.approval_armed"); n != beforeArmed {
		t.Errorf("audit_log invoice.approval_armed rows = %d, want unchanged %d", n, beforeArmed)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, inv.ID); n != 1 {
		t.Errorf("approval_runs rows for invoice = %d, want exactly 1 (only the pre-inserted row)", n)
	}
	var state string
	if err := super.QueryRow(ctx, `SELECT state FROM approval_runs WHERE id = $1`, preRunID).Scan(&state); err != nil {
		t.Fatalf("read pre-inserted approval_runs row: %v", err)
	}
	if state != "open" {
		t.Errorf("pre-inserted approval_runs.state = %q, want unchanged %q", state, "open")
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_run_steps WHERE run_id = $1`, preRunID); n != 0 {
		t.Errorf("approval_run_steps rows for the pre-inserted run = %d, want 0", n)
	}
}

// --- AC-4: no active policy leaves the existing trail unchanged --------------

// TestApplyValidation_NoActivePolicyLeavesTheTrailUnchanged: (a) a tenant with no policy at
// all, and (b) a tenant whose only version is sealed but NOT active -- both promote
// cleanly, gain exactly the two audit rows shipped today (invoice.transitioned,
// invoice.validated, no invoice.approval_armed), and write zero approval_runs/
// approval_run_steps rows. This is the regression lock for "behaves exactly as today" and
// the reason the other promoting GATE/outcome-loop tests stay green. Passes today by
// construction (ArmTx is not called at all yet); it fails the moment arming ever writes
// unconditionally.
func TestApplyValidation_NoActivePolicyLeavesTheTrailUnchanged(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	ruleSetVersionID := seedRuleSetVersionID(t, super)

	assertUnarmed := func(t *testing.T, label, tenantID, entityID string) {
		t.Helper()
		c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
		inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: label})
		if err != nil {
			t.Fatalf("%s: Create: %v", label, err)
		}
		fp := contentFingerprint(inv, inv.LineItems)

		got, err := store.ApplyValidation(c, inv.ID, []Violation{}, ruleSetVersionID, fp)
		if err != nil {
			t.Fatalf("%s: ApplyValidation (clean): want success, got: %v", label, err)
		}
		if got.Status != StatusValidated {
			t.Errorf("%s: status = %q, want %q", label, got.Status, StatusValidated)
		}

		if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != 1 {
			t.Errorf("%s: invoice.transitioned rows = %d, want 1", label, n)
		}
		if n := auditCount(t, app, tenantID, "invoice.validated"); n != 1 {
			t.Errorf("%s: invoice.validated rows = %d, want 1", label, n)
		}
		if n := auditCount(t, app, tenantID, "invoice.approval_armed"); n != 0 {
			t.Errorf("%s: invoice.approval_armed rows = %d, want 0 -- no active policy means arming must not write", label, n)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM approval_runs WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("%s: approval_runs rows = %d, want 0", label, n)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM approval_run_steps WHERE tenant_id = $1`, tenantID); n != 0 {
			t.Errorf("%s: approval_run_steps rows = %d, want 0", label, n)
		}
	}

	t.Run("no policy at all", func(t *testing.T) {
		tenantID := seedTenant(t, super, "ARM-05a tenant")
		entityID := seedEntity(t, super, tenantID, "ARM-05a entity")
		assertUnarmed(t, "ARM-05a", tenantID, entityID)
	})

	t.Run("sealed but not active", func(t *testing.T) {
		tenantID := seedTenant(t, super, "ARM-05b tenant")
		entityID := seedEntity(t, super, tenantID, "ARM-05b entity")
		policyID := seedApprovalPolicyFor(t, super, tenantID, "ARM-05b policy")
		versionID := seedApprovalPolicyVersionFor(t, super, tenantID, policyID)
		sealApprovalPolicyVersionFor(t, super, versionID)
		assertUnarmed(t, "ARM-05b", tenantID, entityID)
	})
}
