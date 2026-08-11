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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// seedApprovalPolicyVersion inserts one draft (unsealed, inactive) version, the first version.
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

// --- soft delete + the one-draft invariant -----------------------------------

// TestApprovalPolicy_DeletedAtColumnExists: invoice_app stamps deleted_at and the row stays
// readable — a soft delete is an UPDATE on a column the table-wide UPDATE grant already covers.
func TestApprovalPolicy_DeletedAtColumnExists(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-05 soft-delete")
	policyID := seedApprovalPolicy(t, super, tenantID, "Soft-delete policy")

	var affected int64
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, policyID)
		affected = tag.RowsAffected()
		return err
	}); err != nil {
		t.Fatalf("soft delete as invoice_app: %v", err)
	}
	if affected != 1 {
		t.Fatalf("soft delete affected %d rows, want 1", affected)
	}

	// Read back as the app role: nothing at the schema level hides a soft-deleted row, so the
	// filter that eventually hides it is the query's job, not RLS's.
	var stamped bool
	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT deleted_at IS NOT NULL FROM approval_policies WHERE id = $1`, policyID).Scan(&stamped)
	}); err != nil {
		t.Fatalf("read back the soft-deleted policy as invoice_app: %v", err)
	}
	if !stamped {
		t.Error("deleted_at IS NULL after the soft delete, want a timestamp")
	}
}

// TestApprovalPolicy_OneDraftPerPolicy: a second unsealed version for the same policy is refused
// by approval_policy_versions_one_draft, which is what makes the draft PUT's "resolve by NOT
// sealed" unambiguous. version = 2 and the constraint-NAME assertion are both load-bearing:
// reusing version 1 trips the pre-existing (tenant_id, policy_id, version) key, which raises the
// same 23505 and leaves the new index unexercised.
func TestApprovalPolicy_OneDraftPerPolicy(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-05 one-draft")
	policyID := seedApprovalPolicy(t, super, tenantID, "One-draft policy")
	seedApprovalPolicyVersion(t, super, tenantID, policyID)

	// Guard: (tenant_id, policy_id, 2) is unoccupied, so the old key cannot be what refuses
	// the insert below.
	var clash int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1 AND policy_id = $2 AND version = 2`,
		tenantID, policyID).Scan(&clash); err != nil {
		t.Fatalf("count existing version-2 rows: %v", err)
	}
	if clash != 0 {
		t.Fatalf("version 2 already exists for this policy (%d rows) — approval_policy_versions_tenant_policy_version_uq, not the new index, would refuse the insert", clash)
	}

	_, err := super.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 2)`,
		tenantID, policyID)
	if err == nil {
		t.Fatal("second unsealed version for the same policy succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("second unsealed version: SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_policy_versions_one_draft" {
		t.Errorf("constraint = %q, want %q", name, "approval_policy_versions_one_draft")
	}

	var unsealed int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM approval_policy_versions WHERE tenant_id = $1 AND policy_id = $2 AND NOT sealed`,
		tenantID, policyID).Scan(&unsealed); err != nil {
		t.Fatalf("count unsealed versions: %v", err)
	}
	if unsealed != 1 {
		t.Errorf("unsealed version count = %d after the refused insert, want 1", unsealed)
	}
}

// TestApprovalPolicy_OneDraftIndexAllowsManySealed.
//
// POSITIVE CONTROL: the index binds only unsealed rows, so sealing the draft frees the slot and
// a policy accumulates many sealed versions beside at most one draft. Green before the migration
// too, and the only spec that catches an index that lost its WHERE clause.
func TestApprovalPolicy_OneDraftIndexAllowsManySealed(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-05 many-sealed")
	policyID := seedApprovalPolicy(t, super, tenantID, "Many-sealed policy")
	v1 := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	// Registered after seedTenant's own cleanup so LIFO runs it first: once a version is sealed
	// the plain tenant cascade raises 23001 and would leak this tenant forever.
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	sealApprovalPolicyVersion(t, super, v1)

	var v2 string
	if err := super.QueryRow(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 2) RETURNING id`,
		tenantID, policyID).Scan(&v2); err != nil {
		t.Fatalf("draft version 2 after sealing version 1: %v, want success", err)
	}
	sealApprovalPolicyVersion(t, super, v2)

	if _, err := super.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 3)`,
		tenantID, policyID); err != nil {
		t.Fatalf("draft version 3 after sealing version 2: %v, want success", err)
	}

	var sealed, unsealed int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE sealed), count(*) FILTER (WHERE NOT sealed)
		   FROM approval_policy_versions WHERE tenant_id = $1 AND policy_id = $2`,
		tenantID, policyID).Scan(&sealed, &unsealed); err != nil {
		t.Fatalf("count versions by seal state: %v", err)
	}
	if sealed != 2 || unsealed != 1 {
		t.Errorf("version counts = %d sealed / %d unsealed, want 2 / 1", sealed, unsealed)
	}
}

// TestApprovalPolicy_HardDeleteStillUngranted.
//
// POSITIVE CONTROL: invoice_app holds no DELETE grant on approval_policies, so a hard delete
// fails the privilege check before RLS is even consulted. Green before the migration too — it
// pins the reason deleted_at has to exist at all, and goes red the day someone grants DELETE.
func TestApprovalPolicy_HardDeleteStillUngranted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-05 hard-delete")
	policyID := seedApprovalPolicy(t, super, tenantID, "Hard-delete policy")

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `DELETE FROM approval_policies WHERE id = $1`, policyID)
		return execErr
	})
	assertSQLState(t, err, "42501")

	var exists bool
	if err := super.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM approval_policies WHERE id = $1)`, policyID).Scan(&exists); err != nil {
		t.Fatalf("check policy survival: %v", err)
	}
	if !exists {
		t.Error("policy no longer exists after the refused DELETE, want present")
	}
}

// --- QA adversarial: tenant scope, per-policy keying, soft-delete interaction ---

// TestApprovalPolicy_SoftDeleteIsTenantScoped: a soft delete is an UPDATE, so RLS — not a grant —
// is the only thing between tenant B and tenant A's policy. Tenant B's UPDATE must find no row
// rather than stamp one.
func TestApprovalPolicy_SoftDeleteIsTenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantA := seedTenant(t, super, "APPR-05 soft-delete scope A")
	tenantB := seedTenant(t, super, "APPR-05 soft-delete scope B")
	policyA := seedApprovalPolicy(t, super, tenantA, "Tenant A policy")

	var visible, affected int64
	if err := db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM approval_policies WHERE id = $1`, policyA).Scan(&visible); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, policyA)
		affected = tag.RowsAffected()
		return err
	}); err != nil {
		t.Fatalf("tenant B attempts tenant A's policy: %v", err)
	}
	if visible != 0 {
		t.Errorf("tenant A's policy is visible to tenant B (%d rows), want 0", visible)
	}
	if affected != 0 {
		t.Errorf("tenant B soft-deleted %d of tenant A's policies, want 0", affected)
	}

	var stamped bool
	if err := super.QueryRow(ctx,
		`SELECT deleted_at IS NOT NULL FROM approval_policies WHERE id = $1`, policyA).Scan(&stamped); err != nil {
		t.Fatalf("read back tenant A's policy: %v", err)
	}
	if stamped {
		t.Error("tenant A's policy carries a deleted_at after tenant B's UPDATE, want NULL")
	}

	// Control: the identical statement under tenant A's own context does stamp the row, so the
	// zero above is RLS and not a broken UPDATE.
	var owned int64
	if err := db.WithinTenantTx(ctx, app, tenantA, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, policyA)
		owned = tag.RowsAffected()
		return err
	}); err != nil {
		t.Fatalf("control: tenant A soft-deletes its own policy: %v", err)
	}
	if owned != 1 {
		t.Errorf("control: tenant A's own soft delete affected %d rows, want 1", owned)
	}
}

// TestApprovalPolicy_OneDraftIsPerPolicyNotPerTenant: the index is keyed (tenant_id, policy_id),
// unlike approval_policy_versions_one_active which is (tenant_id) alone. Draft resolution in the
// CRUD path is per policy, so an index that borrowed the one_active shape would cap a whole
// tenant at one draft and break it. The closing refusal stops this passing vacuously against a
// database with no one_draft index at all.
func TestApprovalPolicy_OneDraftIsPerPolicyNotPerTenant(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-05 per-policy draft")
	policyOne := seedApprovalPolicy(t, super, tenantID, "First policy")
	policyTwo := seedApprovalPolicy(t, super, tenantID, "Second policy")

	seedApprovalPolicyVersion(t, super, tenantID, policyOne)
	seedApprovalPolicyVersion(t, super, tenantID, policyTwo)

	var drafts, policiesWithDraft int
	if err := super.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT policy_id) FROM approval_policy_versions
		   WHERE tenant_id = $1 AND NOT sealed`, tenantID).Scan(&drafts, &policiesWithDraft); err != nil {
		t.Fatalf("count drafts: %v", err)
	}
	if drafts != 2 || policiesWithDraft != 2 {
		t.Errorf("tenant holds %d drafts across %d policies, want 2 across 2", drafts, policiesWithDraft)
	}

	// Same tenant, same policy: still refused. Version 3 is unoccupied, so the pre-existing
	// (tenant_id, policy_id, version) key cannot be what fires.
	_, err := super.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 3)`,
		tenantID, policyOne)
	if err == nil {
		t.Fatal("a second draft on the same policy succeeded, want 23505 — the index is not enforcing per policy")
	}
	if name := pgConstraint(err); name != "approval_policy_versions_one_draft" {
		t.Errorf("constraint = %q, want %q", name, "approval_policy_versions_one_draft")
	}
}

// TestApprovalPolicy_SoftDeleteDoesNotFreeTheDraftSlot: deleted_at and one_draft are independent.
// A soft-deleted policy keeps its draft and still refuses a second one — the CRUD path cannot
// treat a soft delete as a way to start over.
func TestApprovalPolicy_SoftDeleteDoesNotFreeTheDraftSlot(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-05 deleted draft slot")
	policyID := seedApprovalPolicy(t, super, tenantID, "Soft-deleted policy with a draft")
	draftID := seedApprovalPolicyVersion(t, super, tenantID, policyID)

	if err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, policyID)
		return execErr
	}); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	_, err := super.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 2)`,
		tenantID, policyID)
	if err == nil {
		t.Fatal("a second draft on a soft-deleted policy succeeded, want 23505")
	}
	if name := pgConstraint(err); name != "approval_policy_versions_one_draft" {
		t.Errorf("constraint = %q, want %q", name, "approval_policy_versions_one_draft")
	}

	var survives, stamped bool
	if err := super.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM approval_policy_versions WHERE id = $1),
		        (SELECT deleted_at IS NOT NULL FROM approval_policies WHERE id = $2)`,
		draftID, policyID).Scan(&survives, &stamped); err != nil {
		t.Fatalf("read back draft and policy: %v", err)
	}
	if !survives {
		t.Error("the draft vanished when its policy was soft-deleted, want it retained")
	}
	if !stamped {
		t.Error("deleted_at is NULL after the soft delete, want a timestamp")
	}
}

// TestApprovalPolicy_DeletedAtIsNullableWithNoDefault: nothing writes deleted_at implicitly. A
// DEFAULT now() or a NOT NULL would soft-delete every policy the moment it is created.
func TestApprovalPolicy_DeletedAtIsNullableWithNoDefault(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	var dataType, isNullable string
	var columnDefault *string
	if err := super.QueryRow(ctx,
		`SELECT data_type, is_nullable, column_default FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'approval_policies' AND column_name = 'deleted_at'`,
	).Scan(&dataType, &isNullable, &columnDefault); err != nil {
		t.Fatalf("read deleted_at from the catalog: %v", err)
	}
	if dataType != "timestamp with time zone" {
		t.Errorf("deleted_at type = %q, want %q", dataType, "timestamp with time zone")
	}
	if isNullable != "YES" {
		t.Errorf("deleted_at is_nullable = %q, want YES", isNullable)
	}
	if columnDefault != nil {
		t.Errorf("deleted_at column_default = %q, want none", *columnDefault)
	}

	tenantID := seedTenant(t, super, "APPR-05 deleted_at default")
	policyID := seedApprovalPolicy(t, super, tenantID, "Freshly created policy")
	seedApprovalPolicyVersion(t, super, tenantID, policyID)

	var stamped bool
	if err := super.QueryRow(ctx,
		`SELECT deleted_at IS NOT NULL FROM approval_policies WHERE id = $1`, policyID).Scan(&stamped); err != nil {
		t.Fatalf("read back the fresh policy: %v", err)
	}
	if stamped {
		t.Error("a freshly created policy already carries a deleted_at, want NULL")
	}
}
