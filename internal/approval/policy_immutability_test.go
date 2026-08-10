package approval

// PIL-01..14: the five-mechanism seal lock (design doc §6) — content lock, TRUNCATE lock,
// parent seal guard, active-implies-sealed CHECK, one-active-per-tenant index. Written before
// APPR-03-04's migration exists, so ten specs are RED (the op simply succeeds where an error
// was expected — not 42703, `sealed` already shipped in APPR-03-02) and four are POSITIVE
// CONTROLS, exempt from the RED gate and marked as such, each citing the APPR-03-07 mutation
// that proves it can fail.
//
// Owner-refusal attacks run as invoice_migrator (migratorPool) inside an outer tx + SAVEPOINT,
// unconditionally rolled back (attemptWithSavepoint) — db.WithinTenantTx would COMMIT a
// currently-succeeding attack, e.g. PIL-10's TRUNCATE, against the shared dev DB. Every attack
// tx sets app.current_tenant first: these three tables are FORCE RLS, so a missing GUC makes
// RLS — not the trigger — the thing that blocks the row.
//
// Run: `DEV_DB_PORT=5433 make test-approvals`.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- harness -----------------------------------------------------------------

// migratorPool returns the migrator-role (table owner) pool built from DATABASE_MIGRATION_URL,
// or skips when unset. A BEFORE trigger fires for the table owner too, which neither `super`
// (bypasses RLS, not the owner) nor `app` (blocked by grants before any trigger runs) can prove
// — copied in shape from internal/validation/rule_immutability_test.go:82-141, looped over all
// three approval tables instead of two.
func migratorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	migURL := os.Getenv("DATABASE_MIGRATION_URL")
	if migURL == "" {
		t.Skip("owner-proof migrator-role test skipped: set DATABASE_MIGRATION_URL " +
			"(e.g. postgres://invoice_migrator:migrator@localhost:5433/invoice_os?sslmode=disable)")
	}
	ctx := context.Background()

	m, err := pgxpool.New(ctx, migURL)
	if err != nil {
		t.Fatalf("connect migrator: %v", err)
	}
	t.Cleanup(m.Close)
	if err := m.Ping(ctx); err != nil {
		t.Fatalf("ping migrator (is the DB up and bootstrapped?): %v", err)
	}

	// Self-assert this pool is really the non-superuser, table-owning migrator — not a
	// mistakenly-supplied superuser DSN, which would make every owner-proof assertion below
	// pass vacuously (a BEFORE trigger fires for a superuser too, but a superuser also bypasses
	// RLS/grants, silently duplicating `super`'s coverage instead of proving the trigger binds
	// the actual NOSUPERUSER table owner).
	var currentUser string
	if err := m.QueryRow(ctx, `SELECT current_user`).Scan(&currentUser); err != nil {
		t.Fatalf("read current_user on the migrator pool: %v", err)
	}
	if currentUser != "invoice_migrator" {
		t.Fatalf("migrator pool's current_user = %q, want %q — DATABASE_MIGRATION_URL is not "+
			"connecting as the table-owning migrator role", currentUser, "invoice_migrator")
	}

	var isSuperuser bool
	if err := m.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&isSuperuser); err != nil {
		t.Fatalf("read rolsuper for %s: %v", currentUser, err)
	}
	if isSuperuser {
		t.Fatalf("migrator pool's role %q is a SUPERUSER — DATABASE_MIGRATION_URL must point at "+
			"the NOSUPERUSER invoice_migrator role, or the owner-proof assertions in this file "+
			"are meaningless", currentUser)
	}

	for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_policy_steps"} {
		var owner string
		if err := m.QueryRow(ctx,
			`SELECT tableowner FROM pg_tables WHERE schemaname = 'public' AND tablename = $1`, table,
		).Scan(&owner); err != nil {
			t.Fatalf("read tableowner for %s: %v", table, err)
		}
		if owner != currentUser {
			t.Fatalf("table %s is owned by %q, want %q (the migrator pool) — the owner-proof "+
				"assertions in this file require the migrator to actually OWN the tables it "+
				"attacks", table, owner, currentUser)
		}
	}

	return m
}

// attemptWithSavepoint runs sql inside a SAVEPOINT nested in tx and returns the exec error
// without poisoning tx for whatever assertions follow — copied from
// internal/validation/rule_immutability_test.go:187-216 (unexported, cross-package import is
// not possible).
func attemptWithSavepoint(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	t.Helper()
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("open savepoint: %v", err)
	}
	_, execErr := sp.Exec(ctx, sql, args...)
	if execErr != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			t.Fatalf("rollback savepoint after op error (%v): %v", execErr, rbErr)
		}
		return execErr
	}
	if commitErr := sp.Commit(ctx); commitErr != nil {
		t.Fatalf("release savepoint after op success: %v", commitErr)
	}
	return nil
}

// assertSQLState fatals unless err wraps a *pgconn.PgError with code want. Pre-migration every
// RED-gated op below actually succeeds, so this fatals on "non-Postgres err <nil>" — that IS
// the RED reason (design doc §12, [qa-r2-f3-red-reason-is-no-exception-not-42703]).
func assertSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s, got non-Postgres err %v", want, err)
	}
	if pgErr.Code != want {
		t.Fatalf("want SQLSTATE %s, got %s (%s)", want, pgErr.Code, pgErr.Message)
	}
}

// sealApprovalPolicyVersion seals a version as the superuser (autocommit fixture write, mirrors
// seedApprovalPolicy et al.). Every caller must register teardownSealedApprovalFixture via
// t.Cleanup AFTER seedTenant's own, so LIFO order runs it first (design doc §6.4).
func sealApprovalPolicyVersion(t *testing.T, super *pgxpool.Pool, versionID string) {
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

// teardownSealedApprovalFixture removes a tenant holding a sealed version. Once APPR-03-04
// ships, `DELETE FROM tenants` cascades into a sealed version and raises 23001 (mechanism (d)),
// so seedTenant's own discarded-error cleanup would silently leak the tenant forever. This
// bypasses trigger firing AND FK/CASCADE enforcement for the children (session_replication_role
// = 'replica', tx-scoped) and deletes them bottom-up explicitly, then deletes the tenant
// OUTSIDE that override on a normal connection — inside it the CASCADE would not fire and the
// children would orphan instead of being removed (measured on PG 18.4, design doc §6.4).
func teardownSealedApprovalFixture(t *testing.T, super *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()

	tx, err := super.Begin(ctx)
	if err != nil {
		t.Errorf("teardown sealed fixture %s: begin tx: %v", tenantID, err)
		return
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		t.Errorf("teardown sealed fixture %s: set session_replication_role: %v", tenantID, err)
		_ = tx.Rollback(ctx)
		return
	}
	for _, table := range []string{"approval_policy_steps", "approval_policy_versions", "approval_policies"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id = $1`, tenantID); err != nil {
			t.Errorf("teardown sealed fixture %s: delete %s: %v", tenantID, table, err)
			_ = tx.Rollback(ctx)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("teardown sealed fixture %s: commit: %v", tenantID, err)
		return
	}

	if _, err := super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Errorf("teardown sealed fixture %s: delete tenant: %v", tenantID, err)
	}
}

// stepSnapshot reads a step's full row as jsonb text, for byte-identical-afterwards assertions.
func stepSnapshot(t *testing.T, super *pgxpool.Pool, stepID string) string {
	t.Helper()
	var snap string
	if err := super.QueryRow(context.Background(),
		`SELECT to_jsonb(s)::text FROM approval_policy_steps s WHERE id = $1`, stepID).Scan(&snap); err != nil {
		t.Fatalf("snapshot step %s: %v", stepID, err)
	}
	return snap
}

// setTenantGUC is the mandatory first statement of every attack tx below: these three tables
// are FORCE RLS, so skipping this makes RLS — not the trigger — the thing that blocks the row
// (design doc §5 of this subtask's implementation plan).
func setTenantGUC(t *testing.T, ctx context.Context, tx pgx.Tx, tenantID string) {
	t.Helper()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		t.Fatalf("set tenant GUC: %v", err)
	}
}

// --- mechanism (b): the content lock -----------------------------------------

// TestPIL01_InsertUnderSealedVersionRejectedAsOwner (PIL-01): migrator, tenant GUC set, INSERT
// INTO approval_policy_steps under a sealed version -> 23001.
func TestPIL01_InsertUnderSealedVersionRejectedAsOwner(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-01")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-01 insert policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	opErr := attemptWithSavepoint(t, ctx, tx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind) VALUES ($1, $2, 0, 'approval')`,
		tenantID, versionID)
	assertSQLState(t, opErr, "23001")

	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM approval_policy_steps WHERE version_id = $1`, versionID).Scan(&count); err != nil {
		t.Fatalf("count steps under the sealed version: %v", err)
	}
	if count != 0 {
		t.Errorf("step count under the sealed version = %d after a rejected INSERT, want 0", count)
	}
}

// TestPIL02_ContentUpdateOnSealedStepRejected (PIL-02): UPDATE of a content column
// (workflow_role_key) on a sealed step -> 23001; the row is byte-identical afterwards.
func TestPIL02_ContentUpdateOnSealedStepRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-02")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-02 content update policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)
	if _, err := super.Exec(ctx, `UPDATE approval_policy_steps SET workflow_role_key = $1 WHERE id = $2`,
		"tax-reviewer", stepID); err != nil {
		t.Fatalf("seed step workflow_role_key: %v", err)
	}
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	before := stepSnapshot(t, super, stepID)

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	opErr := attemptWithSavepoint(t, ctx, tx,
		`UPDATE approval_policy_steps SET workflow_role_key = 'pil-02-mutated' WHERE id = $1`, stepID)
	assertSQLState(t, opErr, "23001")

	var afterSnap string
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(s)::text FROM approval_policy_steps s WHERE id = $1`, stepID).Scan(&afterSnap); err != nil {
		t.Fatalf("read step after rejected content UPDATE: %v", err)
	}
	if afterSnap != before {
		t.Errorf("step snapshot after rejected content UPDATE = %s, want unchanged %s", afterSnap, before)
	}
}

// TestPIL03_DeleteOfSealedStepRejected (PIL-03): direct DELETE FROM approval_policy_steps of a
// sealed step -> 23001; the row survives.
func TestPIL03_DeleteOfSealedStepRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-03")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-03 delete policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	opErr := attemptWithSavepoint(t, ctx, tx, `DELETE FROM approval_policy_steps WHERE id = $1`, stepID)
	assertSQLState(t, opErr, "23001")

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM approval_policy_steps WHERE id = $1)`, stepID).Scan(&exists); err != nil {
		t.Fatalf("check step survival: %v", err)
	}
	if !exists {
		t.Error("step no longer exists after rejected direct DELETE, want present")
	}
}

// TestPIL04_ReparentIntoSealedRejected (PIL-04): a step under an unsealed draft, then
// UPDATE ... SET version_id = <sealed> -> 23001. The sole case covering the NEW-side check —
// checking only OLD's version would let this through.
func TestPIL04_ReparentIntoSealedRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-04")

	draftPolicy := seedApprovalPolicy(t, super, tenantID, "PIL-04 draft policy")
	draftID := seedApprovalPolicyVersion(t, super, tenantID, draftPolicy)
	stepID := seedApprovalPolicyStep(t, super, tenantID, draftID, nil, "approval", 0)

	sealedPolicy := seedApprovalPolicy(t, super, tenantID, "PIL-04 sealed policy")
	sealedID := seedApprovalPolicyVersion(t, super, tenantID, sealedPolicy)
	sealApprovalPolicyVersion(t, super, sealedID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	opErr := attemptWithSavepoint(t, ctx, tx,
		`UPDATE approval_policy_steps SET version_id = $1 WHERE id = $2`, sealedID, stepID)
	assertSQLState(t, opErr, "23001")

	var currentParent string
	if err := tx.QueryRow(ctx, `SELECT version_id::text FROM approval_policy_steps WHERE id = $1`, stepID).Scan(&currentParent); err != nil {
		t.Fatalf("read step's version_id after rejected reparent: %v", err)
	}
	if currentParent != draftID {
		t.Errorf("step's version_id = %s after rejected reparent, want still %s (the unsealed draft)", currentParent, draftID)
	}
}

// --- mechanism (d): the parent seal guard ------------------------------------

// TestPIL05_UnsealRejected (PIL-05): UPDATE approval_policy_versions SET sealed = false on a
// sealed row -> 23001; sealed still true.
func TestPIL05_UnsealRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-05")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-05 unseal policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE approval_policy_versions SET sealed = false WHERE id = $1`, versionID)
	assertSQLState(t, opErr, "23001")

	var stillSealed bool
	if err := tx.QueryRow(ctx, `SELECT sealed FROM approval_policy_versions WHERE id = $1`, versionID).Scan(&stillSealed); err != nil {
		t.Fatalf("read sealed after rejected unseal: %v", err)
	}
	if !stillSealed {
		t.Error("sealed = false after a rejected unseal attempt, want still true")
	}
}

// TestPIL06_SealTransitionsAllowed (PIL-06).
//
// POSITIVE CONTROL: sealing false->true, re-sealing true->true (no-op), and activating a sealed
// version are all legitimate writes that must never be blocked — green from the moment this is
// written, since nothing pre-lock stops a legal transition. Non-vacuity: APPR-03-07 mutation
// #19.
func TestPIL06_SealTransitionsAllowed(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-06")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-06 seal transitions policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	seal := func(step string) {
		t.Helper()
		if err := db.WithinTenantTx(ctx, migrator, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE approval_policy_versions SET sealed = true WHERE id = $1`, versionID)
			return err
		}); err != nil {
			t.Fatalf("%s: %v, want success", step, err)
		}
	}
	seal("seal false->true")
	seal("seal true->true (no-op)")

	var sealed bool
	if err := super.QueryRow(ctx, `SELECT sealed FROM approval_policy_versions WHERE id = $1`, versionID).Scan(&sealed); err != nil {
		t.Fatalf("read sealed: %v", err)
	}
	if !sealed {
		t.Error("sealed = false after two legitimate seal transitions, want true")
	}

	if err := db.WithinTenantTx(ctx, migrator, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, versionID)
		return err
	}); err != nil {
		t.Fatalf("activate a sealed version: %v, want success", err)
	}
	var active bool
	if err := super.QueryRow(ctx, `SELECT is_active FROM approval_policy_versions WHERE id = $1`, versionID).Scan(&active); err != nil {
		t.Fatalf("read is_active: %v", err)
	}
	if !active {
		t.Error("is_active = false after activating a sealed version, want true")
	}
}

// TestPIL07_NoOpUpdateOnSealedStepAllowed (PIL-07).
//
// POSITIVE CONTROL: `ord = ord` on a sealed step touches no column (IS DISTINCT FROM sees
// nothing changed), so the content lock's per-column check must let it through — proves the
// guard is not a blanket UPDATE ban. Non-vacuity: APPR-03-07 mutation #17.
func TestPIL07_NoOpUpdateOnSealedStepAllowed(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-07")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-07 no-op update policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	before := stepSnapshot(t, super, stepID)
	if err := db.WithinTenantTx(ctx, migrator, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE approval_policy_steps SET ord = ord WHERE id = $1`, stepID)
		return err
	}); err != nil {
		t.Fatalf("no-op UPDATE on a sealed step: %v, want success", err)
	}
	after := stepSnapshot(t, super, stepID)
	if after != before {
		t.Errorf("step snapshot after the no-op UPDATE = %s, want unchanged %s", after, before)
	}
}

// TestPIL08_PrimaryKeyChangeOnSealedStepRejected (PIL-08): UPDATE ... SET id = gen_random_uuid()
// on a sealed step -> 23001. The step is deliberately a LEAF (no children):
// approval_policy_steps_tenant_parent_fk has no ON UPDATE CASCADE, so a childed step would hit
// 23503 instead of the documented clean 23001.
func TestPIL08_PrimaryKeyChangeOnSealedStepRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-08")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-08 pk change policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	opErr := attemptWithSavepoint(t, ctx, tx, `UPDATE approval_policy_steps SET id = gen_random_uuid() WHERE id = $1`, stepID)
	assertSQLState(t, opErr, "23001")

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM approval_policy_steps WHERE id = $1)`, stepID).Scan(&exists); err != nil {
		t.Fatalf("check step survival at its original id: %v", err)
	}
	if !exists {
		t.Error("step no longer exists at its original id after a rejected PK change, want present")
	}
}

// TestPIL09_CascadeParentDeleteOfSealedVersionRejected (PIL-09): DELETE FROM
// approval_policy_versions WHERE id = <sealed> -> 23001 AND the child step row survives. The F1
// trap: a subquery-based guard reads NULL once the cascade has already removed the parent and
// never raises, so both halves below must hold, not just the SQLSTATE.
func TestPIL09_CascadeParentDeleteOfSealedVersionRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-09")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-09 cascade policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	opErr := attemptWithSavepoint(t, ctx, tx, `DELETE FROM approval_policy_versions WHERE id = $1`, versionID)
	assertSQLState(t, opErr, "23001")

	var versionExists, stepExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM approval_policy_versions WHERE id = $1)`, versionID).Scan(&versionExists); err != nil {
		t.Fatalf("check version survival: %v", err)
	}
	if !versionExists {
		t.Error("version no longer exists after a rejected DELETE, want present")
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM approval_policy_steps WHERE id = $1)`, stepID).Scan(&stepExists); err != nil {
		t.Fatalf("check child step survival: %v", err)
	}
	if !stepExists {
		t.Error("child step no longer exists after a rejected parent DELETE, want present — the cascade must never have run")
	}
}

// --- mechanism (c): the TRUNCATE lock ----------------------------------------

// TestPIL10_TruncateStepsRejected (PIL-10): migrator TRUNCATE approval_policy_steps -> 23001.
// Row triggers never fire on TRUNCATE, so only the statement trigger can stop it. TRUNCATE
// wipes the WHOLE table, every tenant — the always-rolled-back tx is what makes authoring this
// RED-phase safe against the shared dev DB.
func TestPIL10_TruncateStepsRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-10")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-10 truncate policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	var preCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM approval_policy_steps`).Scan(&preCount); err != nil {
		t.Fatalf("count approval_policy_steps before TRUNCATE: %v", err)
	}
	if preCount == 0 {
		t.Fatal("count(approval_policy_steps) = 0 before TRUNCATE, want > 0 — nothing to protect")
	}

	opErr := attemptWithSavepoint(t, ctx, tx, `TRUNCATE approval_policy_steps`)
	assertSQLState(t, opErr, "23001")

	var postCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM approval_policy_steps`).Scan(&postCount); err != nil {
		t.Fatalf("count approval_policy_steps after rejected TRUNCATE: %v", err)
	}
	if postCount != preCount {
		t.Errorf("count(approval_policy_steps) after rejected TRUNCATE = %d, want %d (unchanged)", postCount, preCount)
	}

	var stepExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM approval_policy_steps WHERE id = $1)`, stepID).Scan(&stepExists); err != nil {
		t.Fatalf("check seeded step survival: %v", err)
	}
	if !stepExists {
		t.Error("seeded step no longer exists after a rejected TRUNCATE, want present")
	}
}

// --- mechanism (e): active-implies-sealed CHECK + one-active-per-tenant index --

// TestPIL11_ActiveImpliesSealed (PIL-11): INSERT ... (is_active, sealed) VALUES (true, false)
// -> 23514 on approval_policy_versions_active_is_sealed; UPDATE ... SET is_active = true on an
// unsealed row -> 23514 too. Control: activating an already-sealed version succeeds. Neither
// probe activates a version before the control runs, so this stays isolated from PIL-12's
// one-active index — it is never confounded by it.
func TestPIL11_ActiveImpliesSealed(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-11")

	draftPolicy := seedApprovalPolicy(t, super, tenantID, "PIL-11 draft policy")
	draftID := seedApprovalPolicyVersion(t, super, tenantID, draftPolicy)

	sealedPolicy := seedApprovalPolicy(t, super, tenantID, "PIL-11 sealed policy")
	sealedID := seedApprovalPolicyVersion(t, super, tenantID, sealedPolicy)
	sealApprovalPolicyVersion(t, super, sealedID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	insertErr := attemptWithSavepoint(t, ctx, tx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version, is_active, sealed) VALUES ($1, $2, 2, true, false)`,
		tenantID, draftPolicy)
	assertSQLState(t, insertErr, "23514")

	updateErr := attemptWithSavepoint(t, ctx, tx, `UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, draftID)
	assertSQLState(t, updateErr, "23514")

	var draftActive bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM approval_policy_versions WHERE id = $1`, draftID).Scan(&draftActive); err != nil {
		t.Fatalf("read draft is_active: %v", err)
	}
	if draftActive {
		t.Error("draft is_active = true after a rejected activation, want false")
	}

	// control: activating an already-sealed version must still succeed.
	if _, err := tx.Exec(ctx, `UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, sealedID); err != nil {
		t.Fatalf("control: activate sealed version: %v, want success", err)
	}
	var sealedActive bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM approval_policy_versions WHERE id = $1`, sealedID).Scan(&sealedActive); err != nil {
		t.Fatalf("read sealed is_active: %v", err)
	}
	if !sealedActive {
		t.Error("control: sealed version is_active = false after activation, want true")
	}
}

// TestPIL12_OneActiveVersionPerTenant (PIL-12): a second is_active = true version for the same
// tenant -> 23505 on approval_policy_versions_one_active. Control: a second TENANT's active
// version succeeds — the index is per-tenant, unlike rule_set_versions_one_active.
func TestPIL12_OneActiveVersionPerTenant(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "APPR-03 pil-12 A")
	policyA1 := seedApprovalPolicy(t, super, tenantA, "PIL-12 A active policy")
	activeA := seedApprovalPolicyVersion(t, super, tenantA, policyA1)
	sealApprovalPolicyVersion(t, super, activeA)
	if _, err := super.Exec(ctx, `UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, activeA); err != nil {
		t.Fatalf("seed active version: %v", err)
	}
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantA) })
	policyA2 := seedApprovalPolicy(t, super, tenantA, "PIL-12 A second policy")

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantA)

	// sealed=true isolates this from PIL-11's active_is_sealed CHECK — only the one-active
	// index is under test here.
	opErr := attemptWithSavepoint(t, ctx, tx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version, is_active, sealed) VALUES ($1, $2, 1, true, true)`,
		tenantA, policyA2)
	assertSQLState(t, opErr, "23505")
	if name := pgConstraint(opErr); name != "approval_policy_versions_one_active" {
		t.Errorf("constraint = %q, want approval_policy_versions_one_active", name)
	}

	tenantB := seedTenant(t, super, "APPR-03 pil-12 B")
	policyB := seedApprovalPolicy(t, super, tenantB, "PIL-12 B active policy")
	activeB := seedApprovalPolicyVersion(t, super, tenantB, policyB)
	sealApprovalPolicyVersion(t, super, activeB)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantB) })
	if _, err := super.Exec(ctx, `UPDATE approval_policy_versions SET is_active = true WHERE id = $1`, activeB); err != nil {
		t.Fatalf("control: activate tenant B's version: %v, want success", err)
	}
}

// --- non-vacuity + regression controls ---------------------------------------

// TestPIL13_DraftVersionStaysFullyMutable (PIL-13).
//
// POSITIVE CONTROL: under an UNSEALED version, step INSERT / content UPDATE / DELETE and the
// version's own delete all succeed — an over-broad trigger that locked drafts too would pass
// every RED-gated spec above and fail only here. Non-vacuity: APPR-03-07 mutation #18.
func TestPIL13_DraftVersionStaysFullyMutable(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-13")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-13 draft policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)

	if err := db.WithinTenantTx(ctx, migrator, tenantID, func(tx pgx.Tx) error {
		var stepID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind) VALUES ($1, $2, 0, 'approval') RETURNING id`,
			tenantID, versionID).Scan(&stepID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE approval_policy_steps SET ord = 1 WHERE id = $1`, stepID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM approval_policy_steps WHERE id = $1`, stepID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM approval_policy_versions WHERE id = $1`, versionID)
		return err
	}); err != nil {
		t.Fatalf("draft version full mutability: %v, want every op to succeed", err)
	}

	var exists bool
	if err := super.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM approval_policy_versions WHERE id = $1)`, versionID).Scan(&exists); err != nil {
		t.Fatalf("check version deletion: %v", err)
	}
	if exists {
		t.Error("draft version still exists after a clean delete, want gone")
	}
}

// TestApprovalSteps_RoleDeleteLeavesSealedVersionByteIdentical (PIL-14, AC-2).
//
// POSITIVE CONTROL: workflow_role_key carries no FK to workflow_roles (design doc §5), so
// soft-deleting and then hard-deleting the role a sealed step names must never touch the step
// row. This can never go RED — the FK it guards against has never existed. Non-vacuity:
// APPR-03-07 mutation #15, a reintroduced `... ON DELETE SET NULL` FK.
func TestApprovalSteps_RoleDeleteLeavesSealedVersionByteIdentical(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-14")
	roleID := seedWorkflowRole(t, super, tenantID, "tax-reviewer", "Tax Reviewer")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-14 role-delete policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "approval", 0)
	if _, err := super.Exec(ctx, `UPDATE approval_policy_steps SET workflow_role_key = $1 WHERE id = $2`,
		"tax-reviewer", stepID); err != nil {
		t.Fatalf("seed step workflow_role_key: %v", err)
	}
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	before := stepSnapshot(t, super, stepID)

	softDeleteWorkflowRole(t, super, roleID)
	afterSoftDelete := stepSnapshot(t, super, stepID)
	if afterSoftDelete != before {
		t.Errorf("step snapshot after role soft-delete = %s, want unchanged %s", afterSoftDelete, before)
	}

	if _, err := super.Exec(ctx, `DELETE FROM workflow_roles WHERE id = $1`, roleID); err != nil {
		t.Fatalf("hard-delete the role: %v, want success — workflow_role_key carries no FK", err)
	}
	afterHardDelete := stepSnapshot(t, super, stepID)
	if afterHardDelete != before {
		t.Errorf("step snapshot after role hard-delete = %s, want unchanged %s", afterHardDelete, before)
	}
}

// TestPIL15_CondAmountUpdateOnSealedStepRejected (PIL-15): UPDATE of cond_amount on a sealed
// step -> 23001; the row is byte-identical afterwards. QA gap-fill for APPR-03-04: mutating
// cond_amount out of the content lock's 14-column comparison left every other PIL spec green.
func TestPIL15_CondAmountUpdateOnSealedStepRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-15")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-15 cond_amount update policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "condition", 0)
	if _, err := super.Exec(ctx, `UPDATE approval_policy_steps SET cond_op = '>', cond_amount = 100.00 WHERE id = $1`,
		stepID); err != nil {
		t.Fatalf("seed step cond_amount: %v", err)
	}
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	before := stepSnapshot(t, super, stepID)

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	opErr := attemptWithSavepoint(t, ctx, tx,
		`UPDATE approval_policy_steps SET cond_amount = 999.00 WHERE id = $1`, stepID)
	assertSQLState(t, opErr, "23001")

	var afterSnap string
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(s)::text FROM approval_policy_steps s WHERE id = $1`, stepID).Scan(&afterSnap); err != nil {
		t.Fatalf("read step after rejected content UPDATE: %v", err)
	}
	if afterSnap != before {
		t.Errorf("step snapshot after rejected content UPDATE = %s, want unchanged %s", afterSnap, before)
	}
}

// TestPIL16_RemainingContentColumnsUpdateOnSealedStepRejected: QA gap-fill for APPR-03-04.
// Mutation-testing (APPR-03-07) found that of the content lock's 14 compared columns, only
// four (id, version_id, workflow_role_key, cond_amount) had per-column regression coverage
// (PIL-08/04/02/15) — dropping any of the other ten from the trigger's comparison list left
// every existing PIL spec green. Table-driven UPDATE of each on a sealed step -> 23001.
func TestPIL16_RemainingContentColumnsUpdateOnSealedStepRejected(t *testing.T) {
	migrator := migratorPool(t)
	super, _ := dbTestPools(t)
	ctx := context.Background()
	tenantID := seedTenant(t, super, "APPR-03 pil-16")
	policyID := seedApprovalPolicy(t, super, tenantID, "PIL-16 remaining columns policy")
	versionID := seedApprovalPolicyVersion(t, super, tenantID, policyID)
	stepID := seedApprovalPolicyStep(t, super, tenantID, versionID, nil, "condition", 0)
	if _, err := super.Exec(ctx,
		`UPDATE approval_policy_steps SET branch = 'then', sla_hours = 4, cond_op = '>',
		    cond_amount = 100.00, notify_target = 'ops@example.com', notify_channel = 'email'
		 WHERE id = $1`, stepID); err != nil {
		t.Fatalf("seed step content columns: %v", err)
	}
	sealApprovalPolicyVersion(t, super, versionID)
	t.Cleanup(func() { teardownSealedApprovalFixture(t, super, tenantID) })

	before := stepSnapshot(t, super, stepID)

	tx, err := migrator.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migrator tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setTenantGUC(t, ctx, tx, tenantID)

	// tenant_id/parent_step_id target fresh, unrelated UUIDs: if the lock ever stops
	// comparing them, the UPDATE either succeeds outright or trips an unrelated FK
	// (23503) instead of the lock's own 23001 — assertSQLState treats both as a failure,
	// so this still catches the regression without needing a second valid row to reparent onto.
	cases := []struct {
		name string
		sql  string
	}{
		{"tenant_id", `UPDATE approval_policy_steps SET tenant_id = gen_random_uuid() WHERE id = $1`},
		{"parent_step_id", `UPDATE approval_policy_steps SET parent_step_id = gen_random_uuid() WHERE id = $1`},
		{"branch", `UPDATE approval_policy_steps SET branch = 'else' WHERE id = $1`},
		{"ord", `UPDATE approval_policy_steps SET ord = 99 WHERE id = $1`},
		{"kind", `UPDATE approval_policy_steps SET kind = 'approval' WHERE id = $1`},
		{"sla_hours", `UPDATE approval_policy_steps SET sla_hours = 8 WHERE id = $1`},
		{"cond_op", `UPDATE approval_policy_steps SET cond_op = '>=' WHERE id = $1`},
		{"notify_target", `UPDATE approval_policy_steps SET notify_target = 'ops2@example.com' WHERE id = $1`},
		{"notify_channel", `UPDATE approval_policy_steps SET notify_channel = 'slack' WHERE id = $1`},
		{"created_at", `UPDATE approval_policy_steps SET created_at = created_at + interval '1 hour' WHERE id = $1`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opErr := attemptWithSavepoint(t, ctx, tx, c.sql, stepID)
			assertSQLState(t, opErr, "23001")
		})
	}

	var afterSnap string
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(s)::text FROM approval_policy_steps s WHERE id = $1`, stepID).Scan(&afterSnap); err != nil {
		t.Fatalf("read step after rejected updates: %v", err)
	}
	if afterSnap != before {
		t.Errorf("step snapshot after rejected content updates = %s, want unchanged %s", afterSnap, before)
	}
}
