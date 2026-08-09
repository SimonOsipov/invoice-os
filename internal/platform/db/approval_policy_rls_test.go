// RLS, grant and constraint suite for the three policy-config tables (approval_policies,
// approval_policy_versions, approval_policy_steps), written before the migration exists so
// every case fails with an explicit 42P01 until it lands.
//
// Rows are seeded per-test below, never in the shared harness.seed() — a missing table must
// fail only these cases, not every test in the package.
//
// Run: `DEV_DB_PORT=5433 make test-rls`. requireHarness skips without the four per-role
// DSNs, and a skip is itself a failure under scripts/ci/rls-test-gate.sh.
package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// failIfUndefinedApprovalPolicy turns the pre-migration failure into an explicit message
// instead of a misleading SQLSTATE mismatch, copied from failIfUndefinedWorkflowRoles
// (workflow_roles_rls_test.go). Returns true when it fired.
func failIfUndefinedApprovalPolicy(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the approval policy-config migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// apName returns a per-case unique policy name, so reruns never collide.
func apName(prefix string) string { return prefix + "-" + uuid.NewString() }

// seedApprovalPolicy stages one policy for tenantID as the superuser (BYPASSRLS, so seeding
// needs neither tenant context nor a grant) and returns its id plus a cleanup func.
func seedApprovalPolicy(t *testing.T, tenantID, name string) (id string, cleanup func()) {
	t.Helper()
	err := h.super.QueryRow(context.Background(),
		`INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, name,
	).Scan(&id)
	if failIfUndefinedApprovalPolicy(t, "seed approval_policies", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed approval_policies: %v", err)
	}
	cleanup = func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_policies WHERE id = $1`, id)
	}
	return
}

// seedApprovalPolicyVersion stages one draft (unsealed, inactive) version, version=1, for
// policyID.
func seedApprovalPolicyVersion(t *testing.T, tenantID, policyID string) (id string, cleanup func()) {
	t.Helper()
	err := h.super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1) RETURNING id`,
		tenantID, policyID,
	).Scan(&id)
	if failIfUndefinedApprovalPolicy(t, "seed approval_policy_versions", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed approval_policy_versions: %v", err)
	}
	cleanup = func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_policy_versions WHERE id = $1`, id)
	}
	return
}

// seedApprovalPolicyStep stages one top-level step (parent_step_id and branch both NULL)
// under versionID.
func seedApprovalPolicyStep(t *testing.T, tenantID, versionID string, ord int, kind string) (id string, cleanup func()) {
	t.Helper()
	err := h.super.QueryRow(context.Background(),
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind) VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, versionID, ord, kind,
	).Scan(&id)
	if failIfUndefinedApprovalPolicy(t, "seed approval_policy_steps", err) {
		return
	}
	if err != nil {
		t.Fatalf("seed approval_policy_steps: %v", err)
	}
	cleanup = func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_policy_steps WHERE id = $1`, id)
	}
	return
}

// PC-01: each of the three tables is born ENABLE + FORCE RLS carrying exactly one policy,
// tenant_isolation, bound to no role (so it also catches the migrator/owner) with cmd=ALL
// and no tenant_enumerate — nothing in this epic reads approvals cross-tenant.
func TestRLS_ApprovalPolicyTablesForceRLSAndPoliciesDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_policy_steps"} {
		var enabled, forced bool
		err := h.super.QueryRow(ctx,
			`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`,
			table,
		).Scan(&enabled, &forced)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("no pg_class row for public.%s — the approval policy-config migration is not applied yet", table)
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
			t.Fatalf("no tenant_isolation policy on %s — the approval policy-config migration is not applied yet", table)
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

// PC-02: cross-tenant SELECT is filtered, not an error: A sees only its own policy.
func TestRLS_ApprovalPoliciesCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanupA := seedApprovalPolicy(t, h.tenantA, apName("policy-a"))
	defer cleanupA()
	policyB, cleanupB := seedApprovalPolicy(t, h.tenantB, apName("policy-b"))
	defer cleanupB()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM approval_policies WHERE id = $1`, policyA); n != 1 {
			t.Errorf("A's own policy visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM approval_policies WHERE id = $1`, policyB); n != 0 {
			t.Errorf("B's policy visible to A = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// PC-03: a cross-tenant INSERT is refused by the policy's WITH CHECK. The message check is
// load-bearing: a missing INSERT grant answers with the same 42501 and would prove nothing
// about isolation.
func TestRLS_ApprovalPoliciesCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	name := apName("cross-insert")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_policies WHERE name = $1`, name)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2)`, h.tenantB, name)
		return e
	})
	if failIfUndefinedApprovalPolicy(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("cross-tenant INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policies WHERE name = $1`, name); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}
}

// PC-04: reassigning an own, visible row to another tenant is refused — the case that
// catches a policy that stopped re-checking UPDATEs.
func TestRLS_ApprovalPoliciesOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanup := seedApprovalPolicy(t, h.tenantA, apName("reassign"))
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE approval_policies SET tenant_id = $1 WHERE id = $2`, h.tenantB, policyA)
		return e
	})
	assertRLSViolation(t, err)

	var got string
	if e := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM approval_policies WHERE id = $1`, policyA).Scan(&got); e != nil {
		t.Fatalf("read back tenant_id as superuser: %v", e)
	}
	if got != h.tenantA {
		t.Errorf("tenant_id after the refused UPDATE = %q, want unchanged %q", got, h.tenantA)
	}
}

// PC-05: an unset app.current_tenant leaves the predicate NULL for every row — the app
// connection sees nothing and raises no error.
func TestRLS_ApprovalPoliciesMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanupA := seedApprovalPolicy(t, h.tenantA, apName("no-guc-a"))
	defer cleanupA()
	policyB, cleanupB := seedApprovalPolicy(t, h.tenantB, apName("no-guc-b"))
	defer cleanupB()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	n, err := scanCount(ctx, tx, `SELECT count(*) FROM approval_policies WHERE id IN ($1, $2)`, policyA, policyB)
	if err != nil {
		t.Fatalf("SELECT with no tenant context: %v", err)
	}
	if n != 0 {
		t.Errorf("policies visible with no tenant set = %d, want 0", n)
	}
}

// PC-06: FORCE binds the table owner too — invoice_migrator inserting with no tenant
// context is refused by the policy, not merely unprivileged.
func TestRLS_ApprovalPoliciesOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	name := apName("owner")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_policies WHERE name = $1`, name)
	}()

	_, err := h.mig.Exec(ctx, `INSERT INTO approval_policies (tenant_id, name) VALUES ($1, $2)`, h.tenantA, name)
	if failIfUndefinedApprovalPolicy(t, "owner INSERT with no tenant context", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("owner INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policies WHERE name = $1`, name); n != 0 {
		t.Errorf("rows after the refused owner INSERT = %d, want 0", n)
	}
}

// PC-07: scope is pinned to the one Q7 value; anything else is a check_violation. Positive
// control: omitting scope defaults to it.
func TestRLS_ApprovalPoliciesScopeCheckRejects(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policies (tenant_id, name, scope) VALUES ($1, $2, 'Capex')`,
		h.tenantA, apName("bad-scope"))
	if failIfUndefinedApprovalPolicy(t, "scope CHECK provocation", err) {
		return
	}
	if err == nil {
		t.Fatal("insert with scope = 'Capex' succeeded, want check_violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with scope = 'Capex': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if name := pgConstraint(err); !strings.Contains(name, "scope") {
		t.Errorf("constraint = %q, want one naming scope", name)
	}

	policyID, cleanup := seedApprovalPolicy(t, h.tenantA, apName("default-scope"))
	defer cleanup()
	var scope string
	if e := h.super.QueryRow(ctx, `SELECT scope FROM approval_policies WHERE id = $1`, policyID).Scan(&scope); e != nil {
		t.Fatalf("read back scope: %v", e)
	}
	if scope != "All invoices" {
		t.Errorf("scope with none supplied = %q, want %q (DEFAULT not load-bearing)", scope, "All invoices")
	}
}

// PC-08: as SUPERUSER (so no policy can be what refuses), a version for tenant A naming
// tenant B's policy_id is refused — the policy FK is not composite on (tenant_id, id).
func TestRLS_ApprovalPolicyVersionsCrossTenantPolicyRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyB, cleanup := seedApprovalPolicy(t, h.tenantB, apName("cross-policy"))
	defer cleanup()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1)`, h.tenantA, policyB)
	if err == nil {
		t.Fatal("version for tenant A naming tenant B's policy_id succeeded, want 23503 — the " +
			"policy FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant policy_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_policy_versions_tenant_policy_fk" {
		t.Errorf("constraint = %q, want %q", name, "approval_policy_versions_tenant_policy_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_versions WHERE policy_id = $1`, policyB); n != 0 {
		t.Errorf("version rows after the refused INSERT = %d, want 0", n)
	}
}

// PC-09 (AC-2, name fixed): as SUPERUSER, a step for tenant A naming tenant B's version_id
// is refused. A single-column REFERENCES approval_policy_versions(id) would ACCEPT it — FK
// checks run RLS-bypassed.
func TestRLS_ApprovalPolicyStepsCrossTenantVersionRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyB, cleanupPolicy := seedApprovalPolicy(t, h.tenantB, apName("cross-version-policy"))
	defer cleanupPolicy()
	versionB, cleanupVersion := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersion()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind) VALUES ($1, $2, 0, 'approval')`,
		h.tenantA, versionB)
	if err == nil {
		t.Fatal("step for tenant A naming tenant B's version_id succeeded, want 23503 — the " +
			"version FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant version_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_policy_steps_tenant_version_fk" {
		t.Errorf("constraint = %q, want %q", name, "approval_policy_steps_tenant_version_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_steps WHERE version_id = $1`, versionB); n != 0 {
		t.Errorf("step rows after the refused INSERT = %d, want 0", n)
	}
}

// PC-10: as SUPERUSER, a step for tenant A naming tenant B's step as parent_step_id is
// refused — the parent self-FK is not composite on (tenant_id, id) either.
func TestRLS_ApprovalPolicyStepsCrossTenantParentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanupPolicyA := seedApprovalPolicy(t, h.tenantA, apName("cross-parent-policy-a"))
	defer cleanupPolicyA()
	versionA, cleanupVersionA := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersionA()

	policyB, cleanupPolicyB := seedApprovalPolicy(t, h.tenantB, apName("cross-parent-policy-b"))
	defer cleanupPolicyB()
	versionB, cleanupVersionB := seedApprovalPolicyVersion(t, h.tenantB, policyB)
	defer cleanupVersionB()
	stepB, cleanupStepB := seedApprovalPolicyStep(t, h.tenantB, versionB, 0, "approval")
	defer cleanupStepB()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, parent_step_id, ord, kind)
		 VALUES ($1, $2, $3, 0, 'approval')`,
		h.tenantA, versionA, stepB)
	if err == nil {
		t.Fatal("step for tenant A naming tenant B's step as parent_step_id succeeded, want 23503 — the " +
			"parent FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant parent_step_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_policy_steps_tenant_parent_fk" {
		t.Errorf("constraint = %q, want %q", name, "approval_policy_steps_tenant_parent_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_steps WHERE parent_step_id = $1`, stepB); n != 0 {
		t.Errorf("step rows after the refused INSERT = %d, want 0", n)
	}
}

// PC-11: two top-level steps (parent_step_id and branch both NULL) under the same version
// at the same ord collide — a plain UNIQUE (NULLs distinct) would accept both, since NULL
// never equals NULL. Positive control: a differing ord succeeds.
func TestRLS_ApprovalPolicyStepsSlotUniqueBindsTopLevelSteps(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanupPolicy := seedApprovalPolicy(t, h.tenantA, apName("slot-unique"))
	defer cleanupPolicy()
	versionA, cleanupVersion := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersion()
	_, cleanupFirst := seedApprovalPolicyStep(t, h.tenantA, versionA, 0, "approval")
	defer cleanupFirst()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind) VALUES ($1, $2, 0, 'approval')`,
			h.tenantA, versionA)
		return e
	})
	if err == nil {
		t.Fatal("second top-level step at the same (version_id, ord) succeeded, want 23505 (unique_violation)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate top-level slot: SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_policy_steps_slot_uq" {
		t.Errorf("constraint = %q, want %q — a plain UNIQUE (NULLs distinct) would have accepted this",
			name, "approval_policy_steps_slot_uq")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_steps WHERE version_id = $1`, versionA); n != 1 {
		t.Errorf("step rows for the version after the refused duplicate = %d, want 1", n)
	}

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind) VALUES ($1, $2, 1, 'approval')`,
			h.tenantA, versionA)
		return e
	})
	if err != nil {
		t.Fatalf("control: step at a differing ord: want success, got: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_steps WHERE version_id = $1`, versionA); n != 2 {
		t.Errorf("step rows for the version after the control insert = %d, want 2", n)
	}
}

// PC-12: the grant matrix, per §4 of the design doc exactly — invoice_tenant_reader gets
// nothing on any of the three tables, and only approval_policy_steps carries DELETE.
func TestRLS_ApprovalPolicyTablesGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	type grantCase struct {
		table, role, priv string
		want              bool
	}
	privs := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES"}

	var cases []grantCase
	for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_policy_steps"} {
		appDelete := table == "approval_policy_steps" // draft-tree editing needs DELETE; the others never do
		for _, priv := range privs {
			want := priv == "SELECT" || priv == "INSERT" || priv == "UPDATE" || (priv == "DELETE" && appDelete)
			cases = append(cases, grantCase{table, "invoice_app", priv, want})
			cases = append(cases, grantCase{table, "invoice_tenant_reader", priv, false})
		}
	}

	for _, c := range cases {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, $2, $3)`, c.role, "public."+c.table, c.priv,
		).Scan(&got)
		if failIfUndefinedApprovalPolicy(t, "has_table_privilege("+c.role+", "+c.table+", "+c.priv+")", err) {
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

// PC-13: <table>_tenant_id_id_uq must be a real UNIQUE CONSTRAINT on (tenant_id, id) for all
// three tables — a composite FK can reference a constraint only, so a bare unique index
// would leave the downstream FKs unbuildable. Filtering by conname keeps this immune to
// PG18 emitting NOT-NULL constraints as contype='n'.
func TestRLS_ApprovalPolicyTablesTenantIdIdUniqueConstraintsExist(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, table := range []string{"approval_policies", "approval_policy_versions", "approval_policy_steps"} {
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
			t.Fatalf("no pg_constraint row named %s — the approval policy-config migration is not applied yet, "+
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

// PC-14: a throwaway tenant holding policy -> unsealed version -> step: deleting the tenant
// must cascade cleanly through all three tables rather than raising an FK error.
func TestRLS_ApprovalPolicyConfigTenantDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'approval-policy-config throwaway tenant')`, tenantID); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	}()

	policyID, _ := seedApprovalPolicy(t, tenantID, apName("cascade-tenant"))
	versionID, _ := seedApprovalPolicyVersion(t, tenantID, policyID)
	stepID, _ := seedApprovalPolicyStep(t, tenantID, versionID, 0, "approval")

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete the tenant: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policies WHERE id = $1`, policyID); n != 0 {
		t.Errorf("policy rows after the tenant delete = %d, want 0", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_versions WHERE id = $1`, versionID); n != 0 {
		t.Errorf("version rows after the tenant delete = %d, want 0", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_steps WHERE id = $1`, stepID); n != 0 {
		t.Errorf("step rows after the tenant delete = %d, want 0", n)
	}
}

// Adversarial additions (QA, not in the architect's PC-01..17 table): AC-1 enumerates
// three more CHECKs on approval_policy_steps — kind, branch, cond_op — that no PC spec
// provokes. APPR-03-02's Green Gate is just "PC-01...PC-17 green", so an enum typo in
// the migration (e.g. kind missing 'notify') would ship silently green without these.

// kind is pinned to the four Q-declared values; a fifth is a check_violation. Positive
// control: 'autoapprove' succeeds — the one kind value no PC spec exercises.
func TestRLS_ApprovalPolicyStepsKindCheckRejects(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanupPolicy := seedApprovalPolicy(t, h.tenantA, apName("kind-check-policy"))
	defer cleanupPolicy()
	versionA, cleanupVersion := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersion()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind) VALUES ($1, $2, 0, 'bogus')`,
		h.tenantA, versionA)
	if failIfUndefinedApprovalPolicy(t, "kind CHECK provocation", err) {
		return
	}
	if err == nil {
		t.Fatal("insert with kind = 'bogus' succeeded, want check_violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with kind = 'bogus': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if name := pgConstraint(err); !strings.Contains(name, "kind") {
		t.Errorf("constraint = %q, want one naming kind", name)
	}

	_, cleanupOK := seedApprovalPolicyStep(t, h.tenantA, versionA, 0, "autoapprove")
	defer cleanupOK()
}

// branch is pinned to 'then'/'else'; anything else is a check_violation. Positive
// control: 'then' succeeds, no CHECK ties it to parent_step_id being non-null.
func TestRLS_ApprovalPolicyStepsBranchCheckRejects(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanupPolicy := seedApprovalPolicy(t, h.tenantA, apName("branch-check-policy"))
	defer cleanupPolicy()
	versionA, cleanupVersion := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersion()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, branch, ord, kind) VALUES ($1, $2, 'maybe', 0, 'approval')`,
		h.tenantA, versionA)
	if failIfUndefinedApprovalPolicy(t, "branch CHECK provocation", err) {
		return
	}
	if err == nil {
		t.Fatal("insert with branch = 'maybe' succeeded, want check_violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with branch = 'maybe': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if name := pgConstraint(err); !strings.Contains(name, "branch") {
		t.Errorf("constraint = %q, want one naming branch", name)
	}

	if _, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, branch, ord, kind) VALUES ($1, $2, 'then', 0, 'approval')`,
		h.tenantA, versionA); err != nil {
		t.Fatalf("control: branch = 'then': want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_policy_steps WHERE version_id = $1`, versionA)
	}()
}

// cond_op is pinned to the four comparison operators; anything else is a
// check_violation. Positive control: '>=' succeeds.
func TestRLS_ApprovalPolicyStepsCondOpCheckRejects(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyA, cleanupPolicy := seedApprovalPolicy(t, h.tenantA, apName("cond-op-check-policy"))
	defer cleanupPolicy()
	versionA, cleanupVersion := seedApprovalPolicyVersion(t, h.tenantA, policyA)
	defer cleanupVersion()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind, cond_op) VALUES ($1, $2, 0, 'condition', '=')`,
		h.tenantA, versionA)
	if failIfUndefinedApprovalPolicy(t, "cond_op CHECK provocation", err) {
		return
	}
	if err == nil {
		t.Fatal("insert with cond_op = '=' succeeded, want check_violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with cond_op = '=': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
	if name := pgConstraint(err); !strings.Contains(name, "cond_op") {
		t.Errorf("constraint = %q, want one naming cond_op", name)
	}

	if _, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, ord, kind, cond_op) VALUES ($1, $2, 0, 'condition', '>=')`,
		h.tenantA, versionA); err != nil {
		t.Fatalf("control: cond_op = '>=': want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_policy_steps WHERE version_id = $1`, versionA)
	}()
}

// QA adversarial addition: no PC spec exercises approval_policy_versions_tenant_policy_version_uq,
// so a regression that dropped it or widened it to span tenants would ship silently green.
func TestRLS_ApprovalPolicyVersionsVersionUniquePerPolicy(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyID, cleanup := seedApprovalPolicy(t, h.tenantA, apName("version-unique"))
	defer cleanup()

	if _, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1)`,
		h.tenantA, policyID); err != nil {
		t.Fatalf("seed version 1: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM approval_policy_versions WHERE policy_id = $1`, policyID)
	}()

	_, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 1)`,
		h.tenantA, policyID)
	if err == nil {
		t.Fatal("duplicate version 1 for the same policy succeeded, want 23505")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate version: SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "approval_policy_versions_tenant_policy_version_uq" {
		t.Errorf("constraint = %q, want %q", name, "approval_policy_versions_tenant_policy_version_uq")
	}

	// Positive control: a different version number for the same policy succeeds.
	if _, err := h.super.Exec(ctx,
		`INSERT INTO approval_policy_versions (tenant_id, policy_id, version) VALUES ($1, $2, 2)`,
		h.tenantA, policyID); err != nil {
		t.Fatalf("control: version 2 for the same policy: want success, got: %v", err)
	}
}

// QA adversarial addition: PC-14 only exercises the tenant_id CASCADE (deleting a tenant),
// which every one of the three tables carries independently. It never exercises the
// non-tenant composite FKs' own ON DELETE CASCADE direction, so flipping any of the three
// to RESTRICT would ship silently green. Each covers one composite FK in isolation.

func TestRLS_ApprovalPolicyPoliciesDeleteCascadesToVersions(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyID, _ := seedApprovalPolicy(t, h.tenantA, apName("policy-delete-cascade"))
	versionID, _ := seedApprovalPolicyVersion(t, h.tenantA, policyID)

	if _, err := h.super.Exec(ctx, `DELETE FROM approval_policies WHERE id = $1`, policyID); err != nil {
		t.Fatalf("delete the policy directly (not via tenant): %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_versions WHERE id = $1`, versionID); n != 0 {
		t.Errorf("version rows after the policy delete = %d, want 0 — approval_policy_versions_tenant_policy_fk "+
			"is not cascading", n)
	}
}

func TestRLS_ApprovalPolicyVersionsDeleteCascadesToSteps(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyID, cleanupPolicy := seedApprovalPolicy(t, h.tenantA, apName("version-delete-cascade"))
	defer cleanupPolicy()
	versionID, _ := seedApprovalPolicyVersion(t, h.tenantA, policyID)
	stepID, _ := seedApprovalPolicyStep(t, h.tenantA, versionID, 0, "approval")

	if _, err := h.super.Exec(ctx, `DELETE FROM approval_policy_versions WHERE id = $1`, versionID); err != nil {
		t.Fatalf("delete the version directly (not via tenant): %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_steps WHERE id = $1`, stepID); n != 0 {
		t.Errorf("step rows after the version delete = %d, want 0 — approval_policy_steps_tenant_version_fk "+
			"is not cascading", n)
	}
}

func TestRLS_ApprovalPolicyStepsParentDeleteCascadesToChildSteps(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	policyID, cleanupPolicy := seedApprovalPolicy(t, h.tenantA, apName("parent-delete-cascade"))
	defer cleanupPolicy()
	versionID, cleanupVersion := seedApprovalPolicyVersion(t, h.tenantA, policyID)
	defer cleanupVersion()
	parentID, _ := seedApprovalPolicyStep(t, h.tenantA, versionID, 0, "approval")

	var childID string
	if err := h.super.QueryRow(ctx,
		`INSERT INTO approval_policy_steps (tenant_id, version_id, parent_step_id, branch, ord, kind)
		 VALUES ($1, $2, $3, 'then', 0, 'approval') RETURNING id`,
		h.tenantA, versionID, parentID,
	).Scan(&childID); err != nil {
		t.Fatalf("seed child step: %v", err)
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM approval_policy_steps WHERE id = $1`, parentID); err != nil {
		t.Fatalf("delete the parent step: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM approval_policy_steps WHERE id = $1`, childID); n != 0 {
		t.Errorf("child step rows after the parent delete = %d, want 0 — approval_policy_steps_tenant_parent_fk "+
			"is not cascading", n)
	}
}
