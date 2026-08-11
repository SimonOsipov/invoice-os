// RLS, grant and constraint suite for the three run-ledger tables (approval_runs,
// approval_run_steps, approval_decisions), written before the migration exists so
// every case fails with an explicit 42P01 until it lands.
//
// Rows are seeded per-test below, never in the shared harness.seed() — a missing table
// must fail only these cases, not every test in the package.
//
// Run: `DEV_DB_PORT=5433 make test-rls`. requireHarness skips without the four per-role
// DSNs, and a skip is itself a failure under scripts/ci/rls-test-gate.sh.
package db_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// failIfUndefinedApprovalRun turns the pre-migration failure into an explicit message
// instead of a misleading SQLSTATE mismatch, copied from failIfUndefinedApprovalPolicy
// (approval_policy_rls_test.go). Returns true when it fired.
func failIfUndefinedApprovalRun(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the approval run-ledger migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// seedApprovalRun stages one run for tenantID as the superuser and returns its id plus a
// cleanup func. content_fingerprint is an opaque literal — no spec in this file compares
// fingerprints. state/opened_at/created_at all DEFAULT.
func seedApprovalRun(t *testing.T, tenantID, invoiceID, versionID string) (id string, cleanup func()) {
	t.Helper()
	err := h.super.QueryRow(context.Background(),
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, invoiceID, versionID, "fp-"+uuid.NewString(),
	).Scan(&id)
	if failIfUndefinedApprovalRun(t, "seed approval_runs", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed approval_runs: %v", err)
	}
	cleanup = func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_runs WHERE id = $1`, id)
	}
	return
}

// seedApprovalRunStep stages one step under runID.
func seedApprovalRunStep(t *testing.T, tenantID, runID string, ord int, kind string) (id string, cleanup func()) {
	t.Helper()
	err := h.super.QueryRow(context.Background(),
		`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, runID, ord, kind,
	).Scan(&id)
	if failIfUndefinedApprovalRun(t, "seed approval_run_steps", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed approval_run_steps: %v", err)
	}
	cleanup = func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_run_steps WHERE id = $1`, id)
	}
	return
}

// seedApprovalNotifyRunStep stages one kind='notify' step carrying both notify columns.
func seedApprovalNotifyRunStep(t *testing.T, tenantID, runID, target, channel string) (id string, cleanup func()) {
	t.Helper()
	err := h.super.QueryRow(context.Background(),
		`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind, notify_target, notify_channel)
		 VALUES ($1, $2, 0, 'notify', $3, $4) RETURNING id`,
		tenantID, runID, target, channel,
	).Scan(&id)
	if failIfUndefinedApprovalRun(t, "seed approval_run_steps (notify)", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed approval_run_steps (notify): %v", err)
	}
	cleanup = func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_run_steps WHERE id = $1`, id)
	}
	return
}

// seedApprovalDecision stages one decision under runID/runStepID. actor must be
// non-empty (CHECK char_length(actor) > 0).
func seedApprovalDecision(t *testing.T, tenantID, runID, runStepID, decision, actor string) (id string, cleanup func()) {
	t.Helper()
	err := h.super.QueryRow(context.Background(),
		`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, runID, runStepID, decision, actor,
	).Scan(&id)
	if failIfUndefinedApprovalRun(t, "seed approval_decisions", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed approval_decisions: %v", err)
	}
	cleanup = func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_decisions WHERE id = $1`, id)
	}
	return
}

// RN-01: each of the three tables is born ENABLE + FORCE RLS carrying exactly one
// policy, tenant_isolation, bound to no role (so it also catches the migrator/owner)
// with cmd=ALL and no tenant_enumerate — nothing in this epic reads approvals
// cross-tenant.
func TestRLS_ApprovalRunTablesForceRLSAndPoliciesDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, table := range []string{"approval_runs", "approval_run_steps", "approval_decisions"} {
		var enabled, forced bool
		err := h.super.QueryRow(ctx,
			`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`,
			table,
		).Scan(&enabled, &forced)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("no pg_class row for public.%s — the approval run-ledger migration is not applied yet", table)
		}
		if err != nil {
			t.Fatalf("read pg_class for %s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s relrowsecurity/relforcerowsecurity = %v/%v, want true/true (ENABLE alone would let "+
				"the migrator/owner bypass the policy)", table, enabled, forced)
		}

		type policy struct {
			roles []string
			cmd   string
			qual  string
		}
		got := map[string]policy{}
		rows, err := h.super.Query(ctx,
			`SELECT policyname, roles::text[], cmd, coalesce(qual, '')
			   FROM pg_policies WHERE schemaname = 'public' AND tablename = $1`, table)
		if err != nil {
			t.Fatalf("query pg_policies for %s: %v", table, err)
		}
		for rows.Next() {
			var name string
			var p policy
			if e := rows.Scan(&name, &p.roles, &p.cmd, &p.qual); e != nil {
				rows.Close()
				t.Fatalf("scan pg_policies row for %s: %v", table, e)
			}
			got[name] = p
		}
		rows.Close()
		if e := rows.Err(); e != nil {
			t.Fatalf("iterate pg_policies rows for %s: %v", table, e)
		}

		if _, leaked := got["tenant_enumerate"]; leaked {
			t.Errorf("%s carries a tenant_enumerate policy — nothing in this epic reads approvals cross-tenant", table)
		}
		if len(got) != 1 {
			t.Errorf("policies on %s = %d (%v), want exactly 1 (tenant_isolation)", table, len(got), got)
		}
		iso, ok := got["tenant_isolation"]
		if !ok {
			t.Fatalf("no tenant_isolation policy on %s — the approval run-ledger migration is not applied yet", table)
		}
		if strings.Join(iso.roles, ",") != "public" {
			t.Errorf("%s tenant_isolation roles = %v, want [public] (no TO clause — it must bind every role, "+
				"including the migrator that owns the table)", table, iso.roles)
		}
		if iso.cmd != "ALL" {
			t.Errorf("%s tenant_isolation cmd = %q, want %q (its USING must double as the INSERT/UPDATE WITH CHECK)",
				table, iso.cmd, "ALL")
		}
		if !strings.Contains(iso.qual, "app.current_tenant") {
			t.Errorf("%s tenant_isolation qual = %q, want a comparison against the app.current_tenant GUC",
				table, iso.qual)
		}
	}
}

// RN-02: cross-tenant SELECT is filtered, not an error: A sees only its own run.
func TestRLS_ApprovalRunsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-02 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-02-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-02-policy-a"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-02 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-02-B")
	defer cleanupInvoiceB()
	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-02-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	runB, cleanupRunB := seedApprovalRun(t, h.tenantB, invoiceB, versionB)
	defer cleanupRunB()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM approval_runs WHERE id = $1`, runA); n != 1 {
			t.Errorf("A's own run visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM approval_runs WHERE id = $1`, runB); n != 0 {
			t.Errorf("B's run visible to A = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// RN-03: a cross-tenant INSERT is refused by the policy's WITH CHECK before the FK to
// invoice_id/policy_version_id is even reached, so the attack row needs no real ids.
func TestRLS_ApprovalRunsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	fingerprint := "fp-" + uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_runs WHERE content_fingerprint = $1`, fingerprint)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, $4)`,
			h.tenantB, uuid.NewString(), uuid.NewString(), fingerprint)
		return e
	})
	if failIfUndefinedApprovalRun(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("cross-tenant INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_runs WHERE content_fingerprint = $1`, fingerprint); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}
}

// RN-04: reassigning an own, visible run to another tenant is refused — the case that
// catches a policy that stopped re-checking UPDATEs.
func TestRLS_ApprovalRunsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-04 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-04-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-04-policy"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE approval_runs SET tenant_id = $1 WHERE id = $2`, h.tenantB, runA)
		return e
	})
	assertRLSViolation(t, err)

	var got string
	if e := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM approval_runs WHERE id = $1`, runA).Scan(&got); e != nil {
		t.Fatalf("read back tenant_id as superuser: %v", e)
	}
	if got != h.tenantA {
		t.Errorf("tenant_id after the refused UPDATE = %q, want unchanged %q", got, h.tenantA)
	}
}

// RN-05: an unset app.current_tenant leaves the predicate NULL for every row — the app
// connection sees nothing and raises no error.
func TestRLS_ApprovalRunsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-05 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-05-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-05-policy-a"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-05 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-05-B")
	defer cleanupInvoiceB()
	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-05-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	runB, cleanupRunB := seedApprovalRun(t, h.tenantB, invoiceB, versionB)
	defer cleanupRunB()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	n, err := scanCount(ctx, tx, `SELECT count(*) FROM approval_runs WHERE id IN ($1, $2)`, runA, runB)
	if err != nil {
		t.Fatalf("SELECT with no tenant context: %v", err)
	}
	if n != 0 {
		t.Errorf("runs visible with no tenant set = %d, want 0", n)
	}
}

// RN-06: FORCE binds the table owner too — invoice_migrator inserting with no tenant
// context is refused by the policy, not merely unprivileged. The attack row needs no
// real invoice_id/policy_version_id, same reasoning as RN-03.
func TestRLS_ApprovalRunsOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	fingerprint := "fp-" + uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_runs WHERE content_fingerprint = $1`, fingerprint)
	}()

	_, err := h.mig.Exec(ctx,
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, $4)`,
		h.tenantA, uuid.NewString(), uuid.NewString(), fingerprint)
	if failIfUndefinedApprovalRun(t, "owner INSERT with no tenant context", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("owner INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_runs WHERE content_fingerprint = $1`, fingerprint); n != 0 {
		t.Errorf("rows after the refused owner INSERT = %d, want 0", n)
	}
}

// RN-07: as SUPERUSER, a run for tenant A naming tenant B's invoice_id is refused — the
// invoice FK is not composite on (tenant_id, id).
func TestRLS_ApprovalRunsCrossTenantInvoiceRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-07-policy-a"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-07 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-07-B")
	defer cleanupInvoiceB()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, $4)`,
		h.tenantA, invoiceB, versionA, "fp-"+uuid.NewString())
	if err == nil {
		t.Fatal("run for tenant A naming tenant B's invoice_id succeeded, want 23503 — the " +
			"invoice FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant invoice_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_runs_tenant_invoice_fk" {
		t.Errorf("constraint = %q, want %q", name, "approval_runs_tenant_invoice_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, invoiceB); n != 0 {
		t.Errorf("run rows after the refused INSERT = %d, want 0", n)
	}
}

// RN-08: as SUPERUSER, a run naming another tenant's policy_version_id is refused — the
// version FK is not composite on (tenant_id, id).
func TestRLS_ApprovalRunsCrossTenantVersionRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-08 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-08-A")
	defer cleanupInvoiceA()

	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-08-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_runs (tenant_id, invoice_id, policy_version_id, content_fingerprint) VALUES ($1, $2, $3, $4)`,
		h.tenantA, invoiceA, versionB, "fp-"+uuid.NewString())
	if err == nil {
		t.Fatal("run for tenant A naming tenant B's policy_version_id succeeded, want 23503 — the " +
			"version FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant policy_version_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_runs_tenant_version_fk" {
		t.Errorf("constraint = %q, want %q", name, "approval_runs_tenant_version_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_runs WHERE policy_version_id = $1`, versionB); n != 0 {
		t.Errorf("run rows after the refused INSERT = %d, want 0", n)
	}
}

// RN-09: the version FK is ON DELETE RESTRICT. Deleting a governing version that a run
// still references is refused — an explicit RESTRICT raises 23001 restrict_violation,
// checked immediately at the DELETE, not 23503 foreign_key_violation (what an implicit
// NO ACTION FK raises) — the SQLSTATE is the only thing that distinguishes them
// (TestRLS_InvoicesEntityDeleteRestricted, invoices_rls_test.go).
func TestRLS_ApprovalRunsVersionDeleteRestricted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// Cleanup order matters: the run (RESTRICT on policy_version_id) must be removed
	// BEFORE the version, so its cleanup is deferred LAST — deferred funcs run LIFO.
	// Deferring right after each seed (not batched at the end) also means a seed
	// Fatal here still cleans up everything seeded so far.
	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-09 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-09-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-09-policy"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()

	_, err := h.super.Exec(ctx, `DELETE FROM approval_policy_versions WHERE id = $1`, versionA)
	if err == nil {
		t.Fatal("delete the governing version with a live run succeeded, want restrict_violation " +
			"(SQLSTATE 23001, ON DELETE RESTRICT)")
	}
	if code := pgCode(err); code != "23001" {
		t.Fatalf("delete governing version with a live run: SQLSTATE = %q, want 23001 (restrict_violation): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_runs WHERE id = $1`, runA); n != 1 {
		t.Errorf("run rows after the refused version delete = %d, want 1 (row must survive)", n)
	}
}

// QA adversarial addition (mutation-testing APPR-03-07): RN-09 pins RESTRICT on
// approval_runs_tenant_version_fk, but nothing pinned it on approval_runs_tenant_invoice_fk —
// flipping that one to NO ACTION left the whole suite green. Same shape as RN-09, invoice side.
func TestRLS_ApprovalRunsInvoiceDeleteRestricted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-09b A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-09b-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-09b-policy"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()

	_, err := h.super.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceA)
	if err == nil {
		t.Fatal("delete the referenced invoice with a live run succeeded, want restrict_violation " +
			"(SQLSTATE 23001, ON DELETE RESTRICT)")
	}
	if code := pgCode(err); code != "23001" {
		t.Fatalf("delete referenced invoice with a live run: SQLSTATE = %q, want 23001 (restrict_violation): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_runs WHERE id = $1`, runA); n != 1 {
		t.Errorf("run rows after the refused invoice delete = %d, want 1 (row must survive)", n)
	}
}

// RN-10: as SUPERUSER, a run step for A naming B's run_id is refused — the run FK is
// not composite on (tenant_id, id). B's run must be real, not a garbage uuid: a garbage
// id would ALSO fail a hypothetical single-column FK, defeating the point.
func TestRLS_ApprovalRunStepsCrossTenantRunRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-10 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-10-B")
	defer cleanupInvoiceB()
	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-10-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	runB, cleanupRunB := seedApprovalRun(t, h.tenantB, invoiceB, versionB)
	defer cleanupRunB()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind) VALUES ($1, $2, 0, 'approval')`,
		h.tenantA, runB)
	if err == nil {
		t.Fatal("step for tenant A naming tenant B's run_id succeeded, want 23503 — the " +
			"run FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant run_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_run_steps_tenant_run_fk" {
		t.Errorf("constraint = %q, want %q", name, "approval_run_steps_tenant_run_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_run_steps WHERE run_id = $1`, runB); n != 0 {
		t.Errorf("step rows after the refused INSERT = %d, want 0", n)
	}
}

// APPR-06-01: the notify columns are ordinary row-level data, not exempt from the
// table's existing tenant_isolation predicate just because they were added after the
// policy was created — A cannot see B's notify_target/notify_channel by row id.
func TestRLS_ApprovalRunStepsNotifyColumnsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-NC1 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-NC1-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-nc1-policy-a"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()
	stepA, cleanupStepA := seedApprovalNotifyRunStep(t, h.tenantA, runA, "A Tax Team", "Email")
	defer cleanupStepA()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-NC1 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-NC1-B")
	defer cleanupInvoiceB()
	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-nc1-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	runB, cleanupRunB := seedApprovalRun(t, h.tenantB, invoiceB, versionB)
	defer cleanupRunB()
	stepB, cleanupStepB := seedApprovalNotifyRunStep(t, h.tenantB, runB, "B Tax Team", "Slack")
	defer cleanupStepB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var target, channel string
		if e := tx.QueryRow(ctx,
			`SELECT notify_target, notify_channel FROM approval_run_steps WHERE id = $1`, stepA,
		).Scan(&target, &channel); e != nil {
			return e
		}
		if target != "A Tax Team" || channel != "Email" {
			t.Errorf("A's own notify columns = (%q, %q), want (%q, %q)", target, channel, "A Tax Team", "Email")
		}

		if n := mustCount(t, tx, `SELECT count(*) FROM approval_run_steps WHERE id = $1`, stepB); n != 0 {
			t.Errorf("B's notify step visible to A by id = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// APPR-06-01: a cross-tenant write naming another tenant's notify columns is refused two
// ways — an INSERT with tenant_id set to B hits the WITH CHECK before the run_id FK is
// even reached (RN-03's shape), and an UPDATE targeting B's step from A's tx matches zero
// rows because the row is invisible (IB-RLS-03's shape, import_batches_rls_test.go).
func TestRLS_ApprovalRunStepsNotifyColumnsCrossTenantWriteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-NC2 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-NC2-B")
	defer cleanupInvoiceB()
	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-nc2-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	runB, cleanupRunB := seedApprovalRun(t, h.tenantB, invoiceB, versionB)
	defer cleanupRunB()
	stepB, cleanupStepB := seedApprovalNotifyRunStep(t, h.tenantB, runB, "B Tax Team", "Slack")
	defer cleanupStepB()

	insertErr := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO approval_run_steps (tenant_id, run_id, ord, kind, notify_target, notify_channel)
			 VALUES ($1, $2, 0, 'notify', $3, $4)`,
			h.tenantB, runB, "hijacked target", "hijacked channel")
		return e
	})
	assertRLSViolation(t, insertErr)
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_run_steps WHERE notify_target = $1`, "hijacked target"); n != 0 {
		t.Errorf("rows after the refused cross-tenant notify INSERT = %d, want 0", n)
	}

	updateErr := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE approval_run_steps SET notify_target = 'overwritten' WHERE id = $1`, stepB)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 0 {
			t.Errorf("cross-tenant notify_target UPDATE affected %d rows, want 0", ct.RowsAffected())
		}
		return nil
	})
	if updateErr != nil {
		t.Fatalf("WithinTenantTx (UPDATE): %v", updateErr)
	}

	var got string
	if e := h.super.QueryRow(ctx, `SELECT notify_target FROM approval_run_steps WHERE id = $1`, stepB).Scan(&got); e != nil {
		t.Fatalf("read back notify_target as superuser: %v", e)
	}
	if got != "B Tax Team" {
		t.Errorf("notify_target after the refused cross-tenant UPDATE = %q, want unchanged %q", got, "B Tax Team")
	}
}

// RN-11: cross-tenant SELECT is filtered, not an error: A sees only its own decision.
func TestRLS_ApprovalDecisionsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-11 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-11-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-11-policy-a"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()
	stepA, cleanupStepA := seedApprovalRunStep(t, h.tenantA, runA, 0, "approval")
	defer cleanupStepA()
	decisionA, cleanupDecisionA := seedApprovalDecision(t, h.tenantA, runA, stepA, "approved", "rn-11-actor")
	defer cleanupDecisionA()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-11 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-11-B")
	defer cleanupInvoiceB()
	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-11-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	runB, cleanupRunB := seedApprovalRun(t, h.tenantB, invoiceB, versionB)
	defer cleanupRunB()
	stepB, cleanupStepB := seedApprovalRunStep(t, h.tenantB, runB, 0, "approval")
	defer cleanupStepB()
	decisionB, cleanupDecisionB := seedApprovalDecision(t, h.tenantB, runB, stepB, "approved", "rn-11-actor")
	defer cleanupDecisionB()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM approval_decisions WHERE id = $1`, decisionA); n != 1 {
			t.Errorf("A's own decision visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM approval_decisions WHERE id = $1`, decisionB); n != 0 {
			t.Errorf("B's decision visible to A = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// RN-12: a cross-tenant INSERT is refused by the policy's WITH CHECK before the FK to
// run_id/run_step_id is even reached, so the attack row needs no real ids.
func TestRLS_ApprovalDecisionsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	actor := "rn-12-" + uuid.NewString()
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_decisions WHERE actor = $1`, actor)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor) VALUES ($1, $2, $3, 'approved', $4)`,
			h.tenantB, uuid.NewString(), uuid.NewString(), actor)
		return e
	})
	if failIfUndefinedApprovalRun(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("cross-tenant INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_decisions WHERE actor = $1`, actor); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}
}

// RN-13: as SUPERUSER, a decision naming B's run_step_id is refused — the run_step FK
// is not composite on (tenant_id, id). A's own run satisfies the run_id FK so only the
// run_step_id leg is wrong-tenant.
func TestRLS_ApprovalDecisionsCrossTenantRunStepRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-13 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-13-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-13-policy-a"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-13 B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-13-B")
	defer cleanupInvoiceB()
	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-13-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	runB, cleanupRunB := seedApprovalRun(t, h.tenantB, invoiceB, versionB)
	defer cleanupRunB()
	stepB, cleanupStepB := seedApprovalRunStep(t, h.tenantB, runB, 0, "approval")
	defer cleanupStepB()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor) VALUES ($1, $2, $3, 'approved', 'rn-13-actor')`,
		h.tenantA, runA, stepB)
	if err == nil {
		t.Fatal("decision for tenant A naming tenant B's run_step_id succeeded, want 23503 — the " +
			"run_step FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant run_step_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_decisions_tenant_run_step_fk" {
		t.Errorf("constraint = %q, want %q", name, "approval_decisions_tenant_run_step_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_decisions WHERE run_step_id = $1`, stepB); n != 0 {
		t.Errorf("decision rows after the refused INSERT = %d, want 0", n)
	}
}

// QA adversarial addition (mutation-testing APPR-03-07): RN-13 only exercises the
// run_step_id leg of approval_decisions's two composite FKs — its run_id leg stays valid
// for tenant A throughout, so flipping approval_decisions_tenant_run_fk to single-column
// left the whole suite green. Mirrors RN-13 with the roles reversed: run_id is wrong-tenant,
// run_step_id is a valid tenant-A row.
func TestRLS_ApprovalDecisionsCrossTenantRunRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-13b A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-13b-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-13b-policy-a"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()
	stepA, cleanupStepA := seedApprovalRunStep(t, h.tenantA, runA, 0, "approval")
	defer cleanupStepA()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "RN-13b B Corp")
	defer cleanupEntityB()
	invoiceB, cleanupInvoiceB := seedInvoice(t, h.tenantB, entityB, "RN-13b-B")
	defer cleanupInvoiceB()
	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("rn-13b-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	runB, cleanupRunB := seedApprovalRun(t, h.tenantB, invoiceB, versionB)
	defer cleanupRunB()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_decisions (tenant_id, run_id, run_step_id, decision, actor) VALUES ($1, $2, $3, 'approved', 'rn-13b-actor')`,
		h.tenantA, runB, stepA)
	if err == nil {
		t.Fatal("decision for tenant A naming tenant B's run_id succeeded, want 23503 — the " +
			"run FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant run_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_decisions_tenant_run_fk" {
		t.Errorf("constraint = %q, want %q", name, "approval_decisions_tenant_run_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_decisions WHERE run_step_id = $1`, stepA); n != 0 {
		t.Errorf("decision rows after the refused INSERT = %d, want 0", n)
	}
}

// attemptWithSavepoint runs sql inside a SAVEPOINT nested in tx (pgx v5's pseudo-nested
// tx.Begin() issues SAVEPOINT / RELEASE SAVEPOINT / ROLLBACK TO SAVEPOINT), returning the
// exec error. Only the savepoint is undone on error, so tx stays usable for whatever
// assertions follow — copied from internal/validation/rule_immutability_test.go's
// attemptWithSavepoint, needed here because RN-14 must assert two refused mutations plus
// a positive-control readback inside the SAME transaction.
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

// RN-14 (the AC-9 retention test): app-role UPDATE and DELETE on its own, visible
// decision are both refused at the GRANT layer (42501, "permission denied" — not RLS).
// The positive control runs in the SAME transaction as both refusals so the readback
// cannot go vacuous (a separate, un-scoped connection would just see nothing).
func TestRLS_ApprovalDecisionsAppUpdateAndDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "RN-14 A Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "RN-14-A")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("rn-14-policy"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()
	stepA, cleanupStepA := seedApprovalRunStep(t, h.tenantA, runA, 0, "approval")
	defer cleanupStepA()
	decisionA, cleanupDecisionA := seedApprovalDecision(t, h.tenantA, runA, stepA, "approved", "rn-14-actor")
	defer cleanupDecisionA()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		updateErr := attemptWithSavepoint(t, ctx, tx,
			`UPDATE approval_decisions SET reason = 'changed my mind' WHERE id = $1`, decisionA)
		if updateErr == nil {
			t.Error("app-role UPDATE of its own approval_decisions row succeeded, want permission denied " +
				"(SQLSTATE 42501) — invoice_app must hold SELECT + INSERT only")
		} else if code := pgCode(updateErr); code != "42501" {
			t.Errorf("app-role UPDATE on approval_decisions: SQLSTATE = %q, want 42501 (insufficient_privilege): %v",
				code, updateErr)
		} else if msg := pgMessage(updateErr); !strings.Contains(msg, "permission denied") {
			t.Errorf("UPDATE refusal message = %q, want the grant layer (\"permission denied\") — an RLS "+
				"violation here would mean an UPDATE grant exists and only the policy stopped it", msg)
		}

		deleteErr := attemptWithSavepoint(t, ctx, tx, `DELETE FROM approval_decisions WHERE id = $1`, decisionA)
		if deleteErr == nil {
			t.Error("app-role DELETE of its own approval_decisions row succeeded, want permission denied " +
				"(SQLSTATE 42501) — this grant IS the AC-9 retention mechanism")
		} else if code := pgCode(deleteErr); code != "42501" {
			t.Errorf("app-role DELETE on approval_decisions: SQLSTATE = %q, want 42501 (insufficient_privilege): %v",
				code, deleteErr)
		} else if msg := pgMessage(deleteErr); !strings.Contains(msg, "permission denied") {
			t.Errorf("DELETE refusal message = %q, want the grant layer (\"permission denied\")", msg)
		}

		// Positive control: same row, same role, same tx — still SELECT-able and present.
		if n := mustCount(t, tx, `SELECT count(*) FROM approval_decisions WHERE id = $1`, decisionA); n != 1 {
			t.Errorf("decision row visible to its own tenant after the refused UPDATE/DELETE = %d, want 1", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}

	var reason *string
	if e := h.super.QueryRow(ctx, `SELECT reason FROM approval_decisions WHERE id = $1`, decisionA).Scan(&reason); e != nil {
		t.Fatalf("read back reason as superuser: %v", e)
	}
	if reason != nil {
		t.Errorf("reason after the refused UPDATE = %q, want unchanged NULL", *reason)
	}
}

// RN-15: the grant matrix, per §4 of the design doc exactly — invoice_tenant_reader
// gets nothing on any of the three tables, and only approval_decisions withholds
// UPDATE from invoice_app.
func TestRLS_ApprovalRunTablesGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	type grantCase struct {
		table, role, priv string
		want              bool
	}
	privs := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES"}

	var cases []grantCase
	for _, table := range []string{"approval_runs", "approval_run_steps", "approval_decisions"} {
		appUpdate := table != "approval_decisions" // decisions is SELECT, INSERT only — the AC-9 retention grant
		for _, priv := range privs {
			want := priv == "SELECT" || priv == "INSERT" || (priv == "UPDATE" && appUpdate)
			cases = append(cases, grantCase{table, "invoice_app", priv, want})
			cases = append(cases, grantCase{table, "invoice_tenant_reader", priv, false})
		}
	}

	for _, c := range cases {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, $2, $3)`, c.role, "public."+c.table, c.priv,
		).Scan(&got)
		if failIfUndefinedApprovalRun(t, "has_table_privilege("+c.role+", "+c.table+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, %s, %q): %v", c.role, c.table, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, %s, %q) = %v, want %v — see §4's grant matrix",
				c.role, c.table, c.priv, got, c.want)
		}
	}
}

// RN-16: <table>_tenant_id_id_uq must be a real UNIQUE CONSTRAINT on (tenant_id, id) for
// all three tables — a composite FK can reference a constraint only, so a bare unique
// index would leave the downstream FKs unbuildable. Filtering by conname keeps this
// immune to PG18 emitting NOT-NULL constraints as contype='n'.
func TestRLS_ApprovalRunTablesTenantIdIdUniqueConstraintsExist(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, table := range []string{"approval_runs", "approval_run_steps", "approval_decisions"} {
		conname := table + "_tenant_id_id_uq"
		var (
			contype string
			cols    []string
		)
		err := h.super.QueryRow(ctx,
			`SELECT c.contype::text,
			        (SELECT array_agg(a.attname::text ORDER BY k.ord)
			           FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
			           JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum)
			   FROM pg_constraint c
			   JOIN pg_class t ON t.oid = c.conrelid
			   JOIN pg_namespace n ON n.oid = t.relnamespace
			  WHERE n.nspname = 'public' AND t.relname = $1 AND c.conname = $2`,
			table, conname,
		).Scan(&contype, &cols)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("no pg_constraint row named %s — the approval run-ledger migration is not applied yet, "+
				"or the uniqueness was declared as a bare index", conname)
		}
		if err != nil {
			t.Fatalf("read pg_constraint for %s: %v", conname, err)
		}
		if contype != "u" {
			t.Errorf("%s contype = %q, want %q (UNIQUE constraint)", conname, contype, "u")
		}
		if got := strings.Join(cols, ","); got != "tenant_id,id" {
			t.Errorf("%s columns = %q, want %q (the composite FK target)", conname, got, "tenant_id,id")
		}
	}
}

// RN-17: a throwaway tenant holding run -> run step -> decision: deleting the tenant
// must cascade cleanly through all three tables rather than raising an FK error — every
// runtime child FK is CASCADE, not RESTRICT (only the invoice/version legs are).
func TestRLS_ApprovalRunLedgerTenantDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'approval run-ledger throwaway tenant')`, tenantID); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	}()

	entityID, _ := seedBusinessEntity(t, tenantID, "RN-17 throwaway Corp")
	invoiceID, _ := seedInvoice(t, tenantID, entityID, "RN-17-throwaway")
	policyID, _ := seedApprovalPolicy(t, tenantID, apName("rn-17-policy"))
	versionID, _ := seedApprovalPolicyVersion(t, tenantID, policyID)
	runID, _ := seedApprovalRun(t, tenantID, invoiceID, versionID)
	stepID, _ := seedApprovalRunStep(t, tenantID, runID, 0, "approval")
	decisionID, _ := seedApprovalDecision(t, tenantID, runID, stepID, "approved", "rn-17-actor")

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete the tenant: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_runs WHERE id = $1`, runID); n != 0 {
		t.Errorf("run rows after the tenant delete = %d, want 0", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_run_steps WHERE id = $1`, stepID); n != 0 {
		t.Errorf("run step rows after the tenant delete = %d, want 0", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_decisions WHERE id = $1`, decisionID); n != 0 {
		t.Errorf("decision rows after the tenant delete = %d, want 0", n)
	}
}

// QA adversarial addition (mutation-testing APPR-03-07): RN-17 only exercises the
// tenant-level cascade, and every one of these tables ALSO carries its own direct
// tenant_id -> tenants CASCADE, so a run/run-step/decision row is removed by that path
// regardless of what approval_run_steps_tenant_run_fk or approval_decisions_tenant_run_fk
// say — flipping either to RESTRICT left RN-17 (and the whole suite) green. This deletes
// the run directly, the way TestRLS_ApprovalPolicyVersionsDeleteCascadesToSteps does for
// the config side, so the composite FK's own action is what gets exercised.
func TestRLS_ApprovalRunsDeleteCascadesToRunStepsAndDecisions(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "run-delete-cascade Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "run-delete-cascade")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("run-delete-cascade-policy"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runID, _ := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	stepID, _ := seedApprovalRunStep(t, h.tenantA, runID, 0, "approval")
	decisionID, _ := seedApprovalDecision(t, h.tenantA, runID, stepID, "approved", "run-delete-cascade-actor")

	if _, err := h.super.Exec(ctx, `DELETE FROM approval_runs WHERE id = $1`, runID); err != nil {
		t.Fatalf("delete the run directly (not via tenant): %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_run_steps WHERE id = $1`, stepID); n != 0 {
		t.Errorf("run step rows after the run delete = %d, want 0 — approval_run_steps_tenant_run_fk "+
			"is not cascading", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_decisions WHERE id = $1`, decisionID); n != 0 {
		t.Errorf("decision rows after the run delete = %d, want 0 — approval_decisions_tenant_run_fk "+
			"is not cascading", n)
	}
}

// QA adversarial addition (mutation-testing APPR-03-07): same gap as above, for
// approval_decisions_tenant_run_step_fk — deletes the run step directly so the tenant-level
// direct cascade can't mask a RESTRICT flip on the composite FK.
func TestRLS_ApprovalRunStepsDeleteCascadesToDecisions(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "runstep-delete-cascade Corp")
	defer cleanupEntityA()
	invoiceA, cleanupInvoiceA := seedInvoice(t, h.tenantA, entityA, "runstep-delete-cascade")
	defer cleanupInvoiceA()
	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("runstep-delete-cascade-policy"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()
	runA, cleanupRunA := seedApprovalRun(t, h.tenantA, invoiceA, versionA)
	defer cleanupRunA()
	stepID, _ := seedApprovalRunStep(t, h.tenantA, runA, 0, "approval")
	decisionID, _ := seedApprovalDecision(t, h.tenantA, runA, stepID, "approved", "runstep-delete-cascade-actor")

	if _, err := h.super.Exec(ctx, `DELETE FROM approval_run_steps WHERE id = $1`, stepID); err != nil {
		t.Fatalf("delete the run step directly (not via tenant): %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_decisions WHERE id = $1`, decisionID); n != 0 {
		t.Errorf("decision rows after the run step delete = %d, want 0 — approval_decisions_tenant_run_step_fk "+
			"is not cascading", n)
	}
}

// approvalRunStepsNotifyMigrationVersion is the notify-columns migration's goose
// version id, taken from the embedded filename rather than hardcoded.
func approvalRunStepsNotifyMigrationVersion(t *testing.T) int64 {
	t.Helper()
	const glob = "*_approval_run_steps_notify.sql"
	matches, err := fs.Glob(migrations.FS, glob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", glob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1", len(matches), glob, matches)
	}
	v, err := strconv.ParseInt(strings.SplitN(matches[0], "_", 2)[0], 10, 64)
	if err != nil {
		t.Fatalf("parse goose version out of %q: %v", matches[0], err)
	}
	return v
}

// approvalRunStepsColumns reads approval_run_steps' current column set, ordinal-position
// order.
func approvalRunStepsColumns(t *testing.T, ctx context.Context) []string {
	t.Helper()
	rows, err := h.super.Query(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = 'approval_run_steps'
		 ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("query approval_run_steps columns: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate approval_run_steps columns: %v", err)
	}
	return out
}

// AC-2, AC-4: the notify-columns migration's own Down drops EXACTLY notify_target and
// notify_channel, and re-applying Up restores them. Driven through goose (ApplyVersion
// on the real provider, reading migrations.FS) rather than a hand-copied ALTER TABLE, so
// this proves the shipped Down body itself. CI's migrate-reset + migrate-up round-trip
// cannot catch an over-broad Down (e.g. one that also dropped sla_hours): the re-up
// rebuilds the whole table from CREATE TABLE, so the extra drop would never surface —
// this is the only oracle for "drops both columns and nothing else". Transcribed from
// TestRLS_MembershipsDownDropsExactlyThreeColumns (memberships_rls_test.go).
func TestRLS_ApprovalRunStepsNotifyDownDropsExactlyTwoColumns(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	version := approvalRunStepsNotifyMigrationVersion(t)

	migDSN := os.Getenv("DATABASE_MIGRATION_URL")
	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	// Registered before the restore cleanup below so LIFO runs restore first, close
	// last — a plain `defer` here would close the connection before t.Cleanup runs.
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}

	// Restore the column set regardless of how the assertions below turn out — later
	// tests in this package depend on notify_target/notify_channel existing.
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore approval_run_steps schema after the Down/Up round-trip: %v", err)
		}
	})

	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back the notify-columns migration: %v", err)
	}

	wantDown := []string{
		"id", "tenant_id", "run_id", "ord", "kind", "workflow_role_key",
		"sla_hours", "due_at", "state", "satisfied_at", "satisfied_by", "created_at",
	}
	if got := approvalRunStepsColumns(t, ctx); !reflect.DeepEqual(got, wantDown) {
		t.Fatalf("columns after Down = %v, want %v", got, wantDown)
	}

	if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
		t.Fatalf("re-apply the notify-columns migration: %v", err)
	}

	wantUp := append(append([]string{}, wantDown...), "notify_target", "notify_channel")
	if got := approvalRunStepsColumns(t, ctx); !reflect.DeepEqual(got, wantUp) {
		t.Fatalf("columns after re-applying Up = %v, want %v", got, wantUp)
	}
}
