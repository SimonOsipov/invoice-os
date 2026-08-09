package approval

// Constraint-level specs for the policy-config schema that carry AC-named test functions
// rather than the TestRLS_ prefix internal/platform/db reserves (APPR-03 design doc §10).
// Written before the migration exists, so each seed call fails with an explicit 42P01 until
// it lands.
//
// Run: `DEV_DB_PORT=5433 make test-approvals`.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// failIfUndefinedApprovalSchema turns the pre-migration failure into an explicit fatal
// instead of a raw driver error, mirroring failIfUndefinedWorkflowRoles
// (internal/platform/db/workflow_roles_rls_test.go). Returns true when it fired.
func failIfUndefinedApprovalSchema(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the approval policy-config migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedApprovalPolicy inserts one policy as the superuser and returns its id. No cleanup
// func: callers seed under a tenant from seedTenant, whose own cleanup cascades.
func seedApprovalPolicy(t *testing.T, super *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	var id string
	err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, name,
	).Scan(&id)
	if failIfUndefinedApprovalSchema(t, "seed approval_policies", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed approval_policies: %v", err)
	}
	return id
}

// seedApprovalPolicyVersion inserts one draft (unsealed, inactive) version, version=1.
func seedApprovalPolicyVersion(t *testing.T, super *pgxpool.Pool, tenantID, policyID string) string {
	t.Helper()
	var id string
	err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1) RETURNING id`,
		tenantID, policyID,
	).Scan(&id)
	if failIfUndefinedApprovalSchema(t, "seed approval_policy_versions", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed approval_policy_versions: %v", err)
	}
	return id
}

// seedApprovalPolicyStep inserts one step under versionID. parentStepID nil means
// top-level.
func seedApprovalPolicyStep(t *testing.T, super *pgxpool.Pool, tenantID, versionID string, parentStepID *string, kind string, ord int) string {
	t.Helper()
	var id string
	err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_steps (tenant_id, version_id, parent_step_id, ord, kind)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, versionID, parentStepID, ord, kind,
	).Scan(&id)
	if failIfUndefinedApprovalSchema(t, "seed approval_policy_steps", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed approval_policy_steps: %v", err)
	}
	return id
}

// TestApprovalPolicy_NestedConditionRejected (AC-3, PC-15): a step whose parent_step_id is
// non-null may not carry kind='condition'. Positive controls: the same parent with
// kind='approval' succeeds, and a top-level (null parent) condition succeeds. This does
// NOT prove depth is capped — see TestApprovalPolicy_ThreeDeepApprovalChainIsAcceptedTodayKnownGap.
func TestApprovalPolicy_NestedConditionRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-03 nested-condition")
	policyID := seedApprovalPolicy(t, super, tenantID, "Nested condition policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	parentID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)

	_, err := super.Exec(context.Background(),
		`INSERT INTO approval_policy_steps (tenant_id, version_id, parent_step_id, ord, kind) VALUES ($1, $2, $3, 0, 'condition')`,
		tenantID, versionID, parentID)
	if err == nil {
		t.Fatal("child step with kind='condition' succeeded, want check_violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("condition child: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_policy_steps_depth_cap" {
		t.Errorf("constraint = %q, want %q", name, "approval_policy_steps_depth_cap")
	}

	if _, err := super.Exec(context.Background(),
		`INSERT INTO approval_policy_steps (tenant_id, version_id, parent_step_id, ord, kind) VALUES ($1, $2, $3, 1, 'approval')`,
		tenantID, versionID, parentID); err != nil {
		t.Fatalf("control: child step with kind='approval': want success, got: %v", err)
	}

	if _, err := super.Exec(context.Background(),
		`INSERT INTO approval_policy_steps (tenant_id, version_id, parent_step_id, ord, kind) VALUES ($1, $2, NULL, 2, 'condition')`,
		tenantID, versionID); err != nil {
		t.Fatalf("control: top-level condition: want success, got: %v", err)
	}
}

// TestApprovalSteps_DeletedRoleKeyResolvesToBlocked (AC-2, PC-16): workflow_role_key carries
// no FK (see design doc §5), so a step keeps storing the key verbatim after its role is
// soft-deleted — the lookup that resolves it to "blocked" happens at read time, not here.
func TestApprovalSteps_DeletedRoleKeyResolvesToBlocked(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-03 deleted-role-key")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	policyID := seedApprovalPolicy(t, super, tenantID, "Deleted role key policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)

	var stepID string
	err := super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind, workflow_role_key)
		 VALUES ($1, $2, 0, 'approval', $3) RETURNING id`,
		tenantID, versionID, "tax-reviewer").Scan(&stepID)
	if failIfUndefinedApprovalSchema(t, "seed approval_policy_steps with workflow_role_key", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed approval_policy_steps with workflow_role_key: %v", err)
	}

	liveRoleCount := func() int {
		var n int
		if e := super.QueryRow(context.Background(),
			`SELECT count(*) FROM workflow_roles WHERE tenant_id = $1 AND key = $2 AND deleted_at IS NULL`,
			tenantID, "tax-reviewer").Scan(&n); e != nil {
			t.Fatalf("count live workflow_roles: %v", e)
		}
		return n
	}

	if n := liveRoleCount(); n != 1 {
		t.Fatalf("live role count before the soft delete = %d, want 1", n)
	}

	softDeleteWorkflowRole(t, super, roleID)

	if n := liveRoleCount(); n != 0 {
		t.Errorf("live role count after the soft delete = %d, want 0", n)
	}

	var storedKey string
	if err := super.QueryRow(context.Background(),
		`SELECT workflow_role_key FROM approval_policy_steps WHERE id = $1`, stepID).Scan(&storedKey); err != nil {
		t.Fatalf("read back step workflow_role_key: %v", err)
	}
	if storedKey != "tax-reviewer" {
		t.Errorf("step workflow_role_key after the role's soft delete = %q, want unchanged %q", storedKey, "tax-reviewer")
	}
}

// seedBusinessEntity inserts one business entity as the superuser and returns its id.
func seedBusinessEntity(t *testing.T, super *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	var id string
	err := super.QueryRow(context.Background(),
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, name,
	).Scan(&id)
	if failIfUndefinedApprovalSchema(t, "seed business_entities", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	return id
}

// seedInvoice inserts one invoice as the superuser and returns its id. Every column but
// tenant_id/entity_id/invoice_number is nullable or defaulted.
func seedInvoice(t *testing.T, super *pgxpool.Pool, tenantID, entityID, invoiceNumber string) string {
	t.Helper()
	var id string
	err := super.QueryRow(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, entityID, invoiceNumber,
	).Scan(&id)
	if failIfUndefinedApprovalSchema(t, "seed invoices", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed invoices: %v", err)
	}
	return id
}

// seedApprovalRun inserts one run as the superuser and returns its id. content_fingerprint
// is an opaque literal — no spec in this file compares fingerprints. state defaults to 'open'.
func seedApprovalRun(t *testing.T, super *pgxpool.Pool, tenantID, invoiceID, versionID string) string {
	t.Helper()
	var id string
	err := super.QueryRow(context.Background(),
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, invoiceID, versionID, "fp-"+uuid.NewString(),
	).Scan(&id)
	if failIfUndefinedApprovalSchema(t, "seed approval_runs", err) {
		return ""
	}
	if err != nil {
		t.Fatalf("seed approval_runs: %v", err)
	}
	return id
}

// TestApprovalRuns_SecondOpenRunRejected (AC-7, RN-18): a second run with state='open'
// for the same (tenant_id, invoice_id) is refused by approval_runs_one_open. Positive
// controls: once the first run closes, a second open run on the same invoice succeeds;
// an open run on a DIFFERENT invoice never collides.
func TestApprovalRuns_SecondOpenRunRejected(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-03 second-open-run")
	entityID := seedBusinessEntity(t, super, tenantID, "Second-open-run Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "RN-18-invoice-1")
	otherInvoiceID := seedInvoice(t, super, tenantID, entityID, "RN-18-invoice-2")
	policyID := seedApprovalPolicy(t, super, tenantID, "Second-open-run policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)

	runID := seedApprovalRun(t, super, tenantID, invoiceID, versionID)

	_, err := super.Exec(context.Background(),
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, $4)`,
		tenantID, invoiceID, versionID, "fp-"+uuid.NewString())
	if err == nil {
		t.Fatal("second open run for the same (tenant_id, invoice_id) succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("second open run: SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_runs_one_open" {
		t.Errorf("constraint = %q, want %q", name, "approval_runs_one_open")
	}

	// Positive control: once the first run closes, a second open run on the same
	// invoice succeeds — the partial index only binds state='open'.
	if _, err := super.Exec(context.Background(),
		`UPDATE approval_runs SET state = 'approved', closed_at = now(), closed_by = 'rn-18-test' WHERE id = $1`,
		runID); err != nil {
		t.Fatalf("close the first run: %v", err)
	}
	if _, err := super.Exec(context.Background(),
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, $4)`,
		tenantID, invoiceID, versionID, "fp-"+uuid.NewString()); err != nil {
		t.Fatalf("control: open run after the first closed: want success, got: %v", err)
	}

	// Positive control: an open run on a DIFFERENT invoice never collides.
	if _, err := super.Exec(context.Background(),
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, $4)`,
		tenantID, otherInvoiceID, versionID, "fp-"+uuid.NewString()); err != nil {
		t.Fatalf("control: open run on a different invoice: want success, got: %v", err)
	}
}

// assertCheckViolation fails t unless err is a 23514 check_violation on the named
// constraint.
func assertCheckViolation(t *testing.T, err error, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want check_violation on %s, got success", constraint)
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("%s: SQLSTATE = %q, want 23514 (check_violation): %v", constraint, code, err)
	}
	if name := pgConstraint(err); name != constraint {
		t.Errorf("constraint = %q, want %q", name, constraint)
	}
}

// TestApprovalDecisions_ActorCheckRejectsEmptyString: approval_decisions_actor_check
// (char_length(actor) > 0) is asserted nowhere in the RN-01..RN-18 battery — a dropped
// CHECK would let an empty actor silently break the "who decided" audit guarantee AC-9
// depends on.
func TestApprovalDecisions_ActorCheckRejectsEmptyString(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-03 empty-actor")
	entityID := seedBusinessEntity(t, super, tenantID, "Empty-actor Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "actor-check-invoice-1")
	policyID := seedApprovalPolicy(t, super, tenantID, "Empty-actor policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	runID := seedApprovalRun(t, super, tenantID, invoiceID, versionID)

	var stepID string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind) VALUES ($1, $2, 0, 'approval') RETURNING id`,
		tenantID, runID).Scan(&stepID); err != nil {
		t.Fatalf("seed approval_run_steps: %v", err)
	}

	_, err := super.Exec(context.Background(),
		`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor) VALUES ($1, $2, $3, 'approved', '')`,
		tenantID, runID, stepID)
	assertCheckViolation(t, err, "approval_decisions_actor_check")

	// Positive control: a non-empty actor succeeds.
	if _, err := super.Exec(context.Background(),
		`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor) VALUES ($1, $2, $3, 'approved', 'qa-tester')`,
		tenantID, runID, stepID); err != nil {
		t.Fatalf("control: non-empty actor: want success, got: %v", err)
	}
}

// TestApprovalRunLedger_EnumChecksRejectInvalidValues: the state/kind/decision CHECKs on
// all three runtime ledger tables are asserted nowhere in the RN-01..RN-18 battery — a
// dropped or mistyped CHECK would let an out-of-vocabulary value persist silently.
func TestApprovalRunLedger_EnumChecksRejectInvalidValues(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-03 enum-checks")
	entityID := seedBusinessEntity(t, super, tenantID, "Enum-checks Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "enum-check-invoice-1")
	policyID := seedApprovalPolicy(t, super, tenantID, "Enum-checks policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)

	t.Run("approval_runs.state", func(t *testing.T) {
		_, err := super.Exec(context.Background(),
			`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint, state) VALUES ($1, $2, $3, $4, 'bogus')`,
			tenantID, invoiceID, versionID, "fp-"+uuid.NewString())
		assertCheckViolation(t, err, "approval_runs_state_check")
	})

	runID := seedApprovalRun(t, super, tenantID, invoiceID, versionID)

	t.Run("approval_run_steps.kind", func(t *testing.T) {
		_, err := super.Exec(context.Background(),
			`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind) VALUES ($1, $2, 0, 'bogus')`,
			tenantID, runID)
		assertCheckViolation(t, err, "approval_run_steps_kind_check")
	})

	t.Run("approval_run_steps.state", func(t *testing.T) {
		_, err := super.Exec(context.Background(),
			`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind, state) VALUES ($1, $2, 1, 'approval', 'bogus')`,
			tenantID, runID)
		assertCheckViolation(t, err, "approval_run_steps_state_check")
	})

	var stepID string
	if err := super.QueryRow(context.Background(),
		`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind) VALUES ($1, $2, 2, 'approval') RETURNING id`,
		tenantID, runID).Scan(&stepID); err != nil {
		t.Fatalf("seed approval_run_steps: %v", err)
	}

	t.Run("approval_decisions.decision", func(t *testing.T) {
		_, err := super.Exec(context.Background(),
			`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor) VALUES ($1, $2, $3, 'bogus', 'qa-tester')`,
			tenantID, runID, stepID)
		assertCheckViolation(t, err, "approval_decisions_decision_check")
	})
}

// TestApprovalPolicy_ThreeDeepApprovalChainIsAcceptedTodayKnownGap is a KNOWN-GAP TRIPWIRE
// (PC-17), not a feature test: approval_policy_steps_depth_cap only rejects a CHILD whose
// kind is 'condition', so a chain of nested approval steps is legal to arbitrary depth
// today (design doc §2.3 residual). Pins the exact boundary so closing the gap later (a
// trigger, handed to APPR-05) flips this RED as a deliberate decision, not a silent change.
func TestApprovalPolicy_ThreeDeepApprovalChainIsAcceptedTodayKnownGap(t *testing.T) {
	super, _ := dbTestPools(t)
	tenantID := seedTenant(t, super, "APPR-03 three-deep-chain")
	policyID := seedApprovalPolicy(t, super, tenantID, "Three-deep chain policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)

	root := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)
	mid := seedApprovalPolicyStep(t, super, tenantID, versionID, &root, "approval", 0)
	leaf := seedApprovalPolicyStep(t, super, tenantID, versionID, &mid, "approval", 0)

	var parentOfLeaf string
	if err := super.QueryRow(context.Background(),
		`SELECT parent_step_id::text FROM approval_policy_steps WHERE id = $1`, leaf).Scan(&parentOfLeaf); err != nil {
		t.Fatalf("read back the leaf's parent: %v", err)
	}
	if parentOfLeaf != mid {
		t.Errorf("leaf's parent_step_id = %q, want %q — the three-deep chain did not persist as inserted", parentOfLeaf, mid)
	}
}
