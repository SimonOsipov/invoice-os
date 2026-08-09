// RLS, grant and constraint suite for workflow_roles + workflow_role_members, written
// before the migration exists so every case fails with an explicit 42P01 until it lands.
//
// Rows are seeded per-test below, never in the shared harness.seed() — a missing table
// must fail only these cases, not every test in the package.
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

// failIfUndefinedWorkflowRoles turns the pre-migration failure into an explicit message
// instead of a misleading "want 23505, got 42P01". Returns true when it fired.
func failIfUndefinedWorkflowRoles(t *testing.T, what string, err error) bool {
	t.Helper()
	if pgCode(err) == "42P01" {
		t.Fatalf("%s: undefined_table (42P01) — the workflow_roles migration is not applied yet: %v", what, err)
		return true
	}
	return false
}

// wrKey returns a per-case unique role key so reruns never collide on
// workflow_roles_tenant_key_uq.
func wrKey(prefix string) string { return prefix + "-" + uuid.NewString() }

// seedWorkflowRole stages one role for tenantID as the superuser (BYPASSRLS, so seeding
// needs neither tenant context nor a grant) and returns its id plus a cleanup func.
func seedWorkflowRole(t *testing.T, tenantID, key string) (id string, cleanup func()) {
	t.Helper()
	id = uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO workflow_roles (id, tenant_id, key, title) VALUES ($1, $2, $3, $4)`,
		id, tenantID, key, "Seeded "+key,
	); err != nil {
		if pgCode(err) == "42P01" {
			t.Fatalf("seed workflow_roles: undefined_table (42P01) — migration not applied yet: %v", err)
		}
		t.Fatalf("seed workflow_roles: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM workflow_roles WHERE id = $1`, id)
	}
}

// seedWorkflowRoleMember stages one staffing row as the superuser. The caller must seed the
// (tenantID, userID) membership first — the composite FK to memberships requires it.
func seedWorkflowRoleMember(t *testing.T, tenantID, roleID, userID string, ord int) (id string, cleanup func()) {
	t.Helper()
	id = uuid.NewString()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO workflow_role_members (id, tenant_id, workflow_role_id, user_id, ord)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, roleID, userID, ord,
	); err != nil {
		if pgCode(err) == "42P01" {
			t.Fatalf("seed workflow_role_members: undefined_table (42P01) — migration not applied yet: %v", err)
		}
		t.Fatalf("seed workflow_role_members: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM workflow_role_members WHERE id = $1`, id)
	}
}

// QA-added beyond the plan's 22: AC-3/AC-4's catalog half. A policy written
// `TO invoice_app` instead of bare would pass every behavioural case here (it is strictly
// more restrictive) while leaving the migrator with no applicable policy at all, and a
// stray tenant_enumerate would be caught only indirectly.
func TestRLS_WorkflowRolesForceRLSAndPoliciesDeclared(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, table := range []string{"workflow_roles", "workflow_role_members"} {
		var enabled, forced bool
		err := h.super.QueryRow(ctx,
			`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass('public.' || $1)`,
			table,
		).Scan(&enabled, &forced)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("no pg_class row for public.%s — the workflow_roles migration is not applied yet", table)
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
			t.Errorf("%s carries a tenant_enumerate policy — no cross-tenant consumer of workflow roles exists", table)
		}
		if len(got) != 1 {
			t.Errorf("policies on %s = %d (%v), want exactly 1 (tenant_isolation)", table, len(got), got)
		}
		iso, ok := got["tenant_isolation"]
		if !ok {
			t.Fatalf("no tenant_isolation policy on %s — the workflow_roles migration is not applied yet", table)
		}
		if strings.Join(iso.roles, ",") != "public" {
			t.Errorf("%s tenant_isolation roles = %v, want [public] (no TO clause — it must bind every role, "+
				"including the migrator that owns the table)", table, iso.roles)
		}
		if iso.cmd != "ALL" {
			t.Errorf("%s tenant_isolation cmd = %q, want %q (its USING must double as the INSERT/UPDATE "+
				"WITH CHECK)", table, iso.cmd, "ALL")
		}
		if !strings.Contains(iso.qual, "app.current_tenant") {
			t.Errorf("%s tenant_isolation qual = %q, want a comparison against the app.current_tenant GUC",
				table, iso.qual)
		}
	}
}

// Cross-tenant SELECT is filtered, not an error: A sees its own role and never B's.
func TestRLS_WorkflowRolesCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupA := seedWorkflowRole(t, h.tenantA, wrKey("tax-reviewer"))
	defer cleanupA()
	roleB, cleanupB := seedWorkflowRole(t, h.tenantB, wrKey("tax-reviewer"))
	defer cleanupB()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM workflow_roles WHERE id = $1`, roleA); n != 1 {
			t.Errorf("A's own role visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM workflow_roles WHERE id = $1`, roleB); n != 0 {
			t.Errorf("B's role visible to A = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// A cross-tenant INSERT is refused by the policy's WITH CHECK. The message check is
// load-bearing: a missing INSERT grant answers with the same 42501 and would prove nothing
// about isolation.
func TestRLS_WorkflowRolesCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	key := wrKey("cross-insert")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM workflow_roles WHERE key = $1`, key)
	}()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, 'Cross')`, h.tenantB, key)
		return e
	})
	if failIfUndefinedWorkflowRoles(t, "cross-tenant INSERT", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("cross-tenant INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_roles WHERE key = $1`, key); n != 0 {
		t.Errorf("rows after the refused cross-tenant INSERT = %d, want 0", n)
	}
}

// Reassigning an own, visible row to another tenant is refused — this is the case that
// catches a per-table policy copy-paste that stopped re-checking UPDATEs.
func TestRLS_WorkflowRolesOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanup := seedWorkflowRole(t, h.tenantA, wrKey("reassign"))
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE workflow_roles SET tenant_id = $1 WHERE id = $2`, h.tenantB, roleA)
		return e
	})
	assertRLSViolation(t, err)

	var got string
	if e := h.super.QueryRow(ctx, `SELECT tenant_id::text FROM workflow_roles WHERE id = $1`, roleA).Scan(&got); e != nil {
		t.Fatalf("read back tenant_id as superuser: %v", e)
	}
	if got != h.tenantA {
		t.Errorf("tenant_id after the refused UPDATE = %q, want unchanged %q", got, h.tenantA)
	}
}

// An unset app.current_tenant leaves the predicate NULL for every row: the app connection
// sees nothing and raises no error.
func TestRLS_WorkflowRolesMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupA := seedWorkflowRole(t, h.tenantA, wrKey("no-guc-a"))
	defer cleanupA()
	roleB, cleanupB := seedWorkflowRole(t, h.tenantB, wrKey("no-guc-b"))
	defer cleanupB()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	n, err := scanCount(ctx, tx, `SELECT count(*) FROM workflow_roles WHERE id IN ($1, $2)`, roleA, roleB)
	if err != nil {
		t.Fatalf("SELECT with no tenant context: %v", err)
	}
	if n != 0 {
		t.Errorf("roles visible with no tenant set = %d, want 0", n)
	}
}

// FORCE binds the table owner too: invoice_migrator inserting with no tenant context is
// refused by the policy, not merely unprivileged.
func TestRLS_WorkflowRolesOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	key := wrKey("owner")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM workflow_roles WHERE key = $1`, key)
	}()

	_, err := h.mig.Exec(ctx,
		`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, 'Owner')`, h.tenantA, key)
	if failIfUndefinedWorkflowRoles(t, "owner INSERT with no tenant context", err) {
		return
	}
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("owner INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_roles WHERE key = $1`, key); n != 0 {
		t.Errorf("rows after the refused owner INSERT = %d, want 0", n)
	}
}

// The catalog half of least privilege, asked as the SUPERUSER — a missing privilege and a
// policy refusal are both 42501 and indistinguishable on the wire, so the absent grants can
// only be proven here. UPDATE is granted for the rename, the soft delete, and the staffing
// path's SELECT ... FOR UPDATE lock.
func TestRLS_WorkflowRolesGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "UPDATE", true},
		{"invoice_app", "DELETE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_app", "REFERENCES", false},
		{"invoice_tenant_reader", "SELECT", false},
		{"invoice_tenant_reader", "INSERT", false},
		{"invoice_tenant_reader", "UPDATE", false},
		{"invoice_tenant_reader", "DELETE", false},
		{"invoice_tenant_reader", "TRUNCATE", false},
		{"invoice_tenant_reader", "REFERENCES", false},
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.workflow_roles', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedWorkflowRoles(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, workflow_roles, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, workflow_roles, %q) = %v, want %v — the grant is exactly "+
				"`GRANT SELECT, INSERT, UPDATE ON workflow_roles TO invoice_app`, and nothing to "+
				"invoice_tenant_reader", c.role, c.priv, got, c.want)
		}
	}
}

// Deleting a role is soft (an UPDATE of deleted_at), so there is no DELETE grant. The
// follow-up SELECT is the positive half: the same row is reachable by the same role, so the
// refusal is about DELETE and not about the row being invisible.
func TestRLS_WorkflowRolesAppDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanup := seedWorkflowRole(t, h.tenantA, wrKey("no-delete"))
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM workflow_roles WHERE id = $1`, roleA)
		return e
	})
	if err == nil {
		t.Fatal("app-role DELETE on its own workflow_roles row succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("own-tenant DELETE: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}
	if msg := pgMessage(err); !strings.Contains(msg, "permission denied") {
		t.Errorf("DELETE refusal message = %q, want the grant layer (\"permission denied\") — an RLS violation "+
			"here would mean a DELETE grant exists and only the policy stopped it", msg)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_roles WHERE id = $1`, roleA); n != 1 {
		t.Errorf("role rows after the refused DELETE = %d, want 1", n)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM workflow_roles WHERE id = $1`, roleA); n != 1 {
			t.Errorf("own row visible to its own tenant after the refused DELETE = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("re-read own row: %v", err)
	}
}

// SELECT ... FOR UPDATE requires the UPDATE privilege in Postgres, and the staffing path
// takes exactly this lock on the role row — so the UPDATE grant is load-bearing beyond
// rename and soft delete.
func TestRLS_WorkflowRolesSelectForUpdateAllowedForApp(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanup := seedWorkflowRole(t, h.tenantA, wrKey("lock"))
	defer cleanup()

	var got string
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM workflow_roles WHERE id = $1 FOR UPDATE`, roleA).Scan(&got)
	}); err != nil {
		t.Fatalf("app-role SELECT ... FOR UPDATE: want success, got: %v", err)
	}
	if got != roleA {
		t.Errorf("locked row id = %q, want %q", got, roleA)
	}
}

// The key UNIQUE deliberately spans soft-deleted rows (it is not partial on
// deleted_at IS NULL): a re-minted key must never inherit a sealed policy step.
func TestRLS_WorkflowRolesKeyUniquePerTenantSpansDeleted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	key := wrKey("tax-reviewer")
	roleA, cleanup := seedWorkflowRole(t, h.tenantA, key)
	defer cleanup()

	if _, err := h.super.Exec(ctx, `UPDATE workflow_roles SET deleted_at = now() WHERE id = $1`, roleA); err != nil {
		t.Fatalf("soft-delete the seeded role: %v", err)
	}

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, 'Re-minted')`, h.tenantA, key)
		return e
	})
	if err == nil {
		t.Fatal("re-minting a soft-deleted key succeeded, want 23505 (unique_violation)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("re-mint a soft-deleted key: SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "workflow_roles_tenant_key_uq" {
		t.Errorf("constraint = %q, want %q — a different index rejected it", name, "workflow_roles_tenant_key_uq")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_roles WHERE key = $1`, key); n != 1 {
		t.Errorf("rows holding the key after the refused re-mint = %d, want 1", n)
	}
}

// The key UNIQUE is scoped per tenant: the same key under another tenant is a different
// role. A (key)-only unique index would make one tenant's vocabulary global.
func TestRLS_WorkflowRolesSameKeyDifferentTenantsAllowed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	key := wrKey("approver")
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM workflow_roles WHERE key = $1`, key)
	}()

	for _, tenant := range []string{h.tenantA, h.tenantB} {
		err := db.WithinTenantTx(ctx, h.app, tenant, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx,
				`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, 'Approver')`, tenant, key)
			return e
		})
		if failIfUndefinedWorkflowRoles(t, "INSERT the shared key for tenant "+tenant, err) {
			return
		}
		if err != nil {
			t.Fatalf("INSERT the shared key for tenant %s: want success, got: %v", tenant, err)
		}
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_roles WHERE key = $1`, key); n != 2 {
		t.Errorf("rows holding the shared key = %d, want 2 (one per tenant)", n)
	}
}

// workflow_roles_tenant_id_id_uq must be a real UNIQUE CONSTRAINT on (tenant_id, id): a
// composite FK can reference a constraint only, so a bare unique index would leave the
// staffing table's role FK unbuildable. Filtering by conname keeps this immune to PG18
// emitting NOT-NULL constraints as contype='n' rows.
func TestRLS_WorkflowRolesTenantIdIdUniqueConstraintExists(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

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
		  WHERE n.nspname = 'public' AND t.relname = 'workflow_roles'
		    AND c.conname = 'workflow_roles_tenant_id_id_uq'`,
	).Scan(&contype, &cols)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("no pg_constraint row named workflow_roles_tenant_id_id_uq — the workflow_roles migration is " +
			"not applied yet, or the uniqueness was declared as a bare index")
	}
	if err != nil {
		t.Fatalf("read pg_constraint for workflow_roles_tenant_id_id_uq: %v", err)
	}
	if contype != "u" {
		t.Errorf("workflow_roles_tenant_id_id_uq contype = %q, want %q (UNIQUE constraint)", contype, "u")
	}
	if got := strings.Join(cols, ","); got != "tenant_id,id" {
		t.Errorf("workflow_roles_tenant_id_id_uq columns = %q, want %q (the composite FK target)", got, "tenant_id,id")
	}
}

// The positive write path: naming only tenant_id/key/title leaves id and created_at to
// their defaults, description to the empty string, and deleted_at NULL.
func TestRLS_WorkflowRolesOwnTenantInsertSucceeds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO workflow_roles (tenant_id, key, title) VALUES ($1, $2, 'Tax Reviewer') RETURNING id`,
			h.tenantA, wrKey("defaults"),
		).Scan(&id)
	})
	if failIfUndefinedWorkflowRoles(t, "own-tenant INSERT", err) {
		return
	}
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM workflow_roles WHERE id = $1`, id)
	}()
	if _, e := uuid.Parse(id); e != nil {
		t.Errorf("RETURNING id = %q, want a defaulted uuid: %v", id, e)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			description    string
			deletedAt      *string
			createdAtFresh bool
		)
		if e := tx.QueryRow(ctx,
			`SELECT description, deleted_at::text, created_at > now() - interval '1 hour'
			   FROM workflow_roles WHERE id = $1`, id,
		).Scan(&description, &deletedAt, &createdAtFresh); e != nil {
			return e
		}
		if description != "" {
			t.Errorf("description with none supplied = %q, want the '' DEFAULT", description)
		}
		if deletedAt != nil {
			t.Errorf("deleted_at on a fresh row = %q, want NULL", *deletedAt)
		}
		if !createdAtFresh {
			t.Error("created_at is not within the last hour — the now() DEFAULT is not load-bearing")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify insert defaults: %v", err)
	}
}

// Cross-tenant SELECT on the staffing table is filtered, not an error.
func TestRLS_WorkflowRoleMembersCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupRoleA := seedWorkflowRole(t, h.tenantA, wrKey("staffed-a"))
	defer cleanupRoleA()
	userA := uuid.NewString()
	_, cleanupMemA := seedMembership(t, h.tenantA, userA, "reviewer")
	defer cleanupMemA()
	memberA, cleanupA := seedWorkflowRoleMember(t, h.tenantA, roleA, userA, 0)
	defer cleanupA()

	roleB, cleanupRoleB := seedWorkflowRole(t, h.tenantB, wrKey("staffed-b"))
	defer cleanupRoleB()
	userB := uuid.NewString()
	_, cleanupMemB := seedMembership(t, h.tenantB, userB, "reviewer")
	defer cleanupMemB()
	memberB, cleanupB := seedWorkflowRoleMember(t, h.tenantB, roleB, userB, 0)
	defer cleanupB()

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM workflow_role_members WHERE id = $1`, memberA); n != 1 {
			t.Errorf("A's own staffing row visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM workflow_role_members WHERE id = $1`, memberB); n != 0 {
			t.Errorf("B's staffing row visible to A = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// A staffing row named for tenant B while scoped to A is refused by the policy's WITH
// CHECK. B's role and membership are seeded, so neither FK is what refuses.
func TestRLS_WorkflowRoleMembersCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleB, cleanupRoleB := seedWorkflowRole(t, h.tenantB, wrKey("cross-staffing"))
	defer cleanupRoleB()
	userB := uuid.NewString()
	_, cleanupMemB := seedMembership(t, h.tenantB, userB, "reviewer")
	defer cleanupMemB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, 0)`,
			h.tenantB, roleB, userB)
		return e
	})
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("cross-tenant INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM workflow_role_members WHERE workflow_role_id = $1`, roleB); n != 0 {
		t.Errorf("staffing rows after the refused cross-tenant INSERT = %d, want 0", n)
	}
}

// An unset app.current_tenant fails closed on the staffing table too.
func TestRLS_WorkflowRoleMembersMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupRoleA := seedWorkflowRole(t, h.tenantA, wrKey("no-guc-staff-a"))
	defer cleanupRoleA()
	userA := uuid.NewString()
	_, cleanupMemA := seedMembership(t, h.tenantA, userA, "reviewer")
	defer cleanupMemA()
	memberA, cleanupA := seedWorkflowRoleMember(t, h.tenantA, roleA, userA, 0)
	defer cleanupA()

	roleB, cleanupRoleB := seedWorkflowRole(t, h.tenantB, wrKey("no-guc-staff-b"))
	defer cleanupRoleB()
	userB := uuid.NewString()
	_, cleanupMemB := seedMembership(t, h.tenantB, userB, "reviewer")
	defer cleanupMemB()
	memberB, cleanupB := seedWorkflowRoleMember(t, h.tenantB, roleB, userB, 0)
	defer cleanupB()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	n, err := scanCount(ctx, tx,
		`SELECT count(*) FROM workflow_role_members WHERE id IN ($1, $2)`, memberA, memberB)
	if err != nil {
		t.Fatalf("SELECT with no tenant context: %v", err)
	}
	if n != 0 {
		t.Errorf("staffing rows visible with no tenant set = %d, want 0", n)
	}
}

// FORCE binds the owner on the staffing table too.
func TestRLS_WorkflowRoleMembersOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupRole := seedWorkflowRole(t, h.tenantA, wrKey("owner-staffing"))
	defer cleanupRole()
	userA := uuid.NewString()
	_, cleanupMem := seedMembership(t, h.tenantA, userA, "reviewer")
	defer cleanupMem()

	_, err := h.mig.Exec(ctx,
		`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, 0)`,
		h.tenantA, roleA, userA)
	assertRLSViolation(t, err)
	if msg := pgMessage(err); !strings.Contains(msg, "row-level security") {
		t.Errorf("owner INSERT refusal message = %q, want an RLS WITH CHECK violation", msg)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM workflow_role_members WHERE workflow_role_id = $1`, roleA); n != 0 {
		t.Errorf("rows after the refused owner INSERT = %d, want 0", n)
	}
}

// The staffing table's grant matrix. No UPDATE: an ord change arrives as a whole-set
// replace, so nothing mutates a staffing row in place — and a FOR UPDATE lock here would
// therefore be 42501.
func TestRLS_WorkflowRoleMembersGrantMatrix(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	for _, c := range []struct {
		role string
		priv string
		want bool
	}{
		{"invoice_app", "SELECT", true},
		{"invoice_app", "INSERT", true},
		{"invoice_app", "DELETE", true},
		{"invoice_app", "UPDATE", false},
		{"invoice_app", "TRUNCATE", false},
		{"invoice_app", "REFERENCES", false},
		{"invoice_tenant_reader", "SELECT", false},
		{"invoice_tenant_reader", "INSERT", false},
		{"invoice_tenant_reader", "UPDATE", false},
		{"invoice_tenant_reader", "DELETE", false},
		{"invoice_tenant_reader", "TRUNCATE", false},
		{"invoice_tenant_reader", "REFERENCES", false},
	} {
		var got bool
		err := h.super.QueryRow(ctx,
			`SELECT has_table_privilege($1, 'public.workflow_role_members', $2)`, c.role, c.priv,
		).Scan(&got)
		if failIfUndefinedWorkflowRoles(t, "has_table_privilege("+c.role+", "+c.priv+")", err) {
			return
		}
		if err != nil {
			t.Fatalf("has_table_privilege(%q, workflow_role_members, %q): %v", c.role, c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(%q, workflow_role_members, %q) = %v, want %v — the grant is exactly "+
				"`GRANT SELECT, INSERT, DELETE ON workflow_role_members TO invoice_app`, and nothing to "+
				"invoice_tenant_reader", c.role, c.priv, got, c.want)
		}
	}
}

// Staffing a user who holds a membership in B only must be refused, and by
// workflow_role_members_tenant_user_fk specifically — a single-column
// REFERENCES memberships(user_id) would accept this, since FK checks run RLS-bypassed.
// Attempted as the superuser on purpose, so no policy can be what refuses.
func TestRLS_WorkflowRoleMembersCrossTenantUserRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupRole := seedWorkflowRole(t, h.tenantA, wrKey("cross-user"))
	defer cleanupRole()
	foreignUser := uuid.NewString()
	_, cleanupMem := seedMembership(t, h.tenantB, foreignUser, "reviewer")
	defer cleanupMem()

	_, err := h.super.Exec(ctx,
		`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, 0)`,
		h.tenantA, roleA, foreignUser)
	if err == nil {
		t.Fatal("staffing tenant A's role with a user who is a member of tenant B only succeeded, want 23503 — " +
			"the memberships FK is not composite on (tenant_id, user_id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant user_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "workflow_role_members_tenant_user_fk" {
		t.Errorf("constraint = %q, want %q", name, "workflow_role_members_tenant_user_fk")
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_role_members WHERE user_id = $1`, foreignUser); n != 0 {
		t.Errorf("staffing rows after the refused INSERT = %d, want 0", n)
	}
}

// A hard DELETE of the role row (superuser only — the app has no DELETE grant there) takes
// its staffing rows with it.
func TestRLS_WorkflowRoleMembersRoleHardDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupRole := seedWorkflowRole(t, h.tenantA, wrKey("cascade-role"))
	defer cleanupRole()

	var memberIDs []string
	for ord := 0; ord < 2; ord++ {
		userID := uuid.NewString()
		_, cleanupMem := seedMembership(t, h.tenantA, userID, "reviewer")
		defer cleanupMem()
		id, cleanupMember := seedWorkflowRoleMember(t, h.tenantA, roleA, userID, ord)
		defer cleanupMember()
		memberIDs = append(memberIDs, id)
	}

	if _, err := h.super.Exec(ctx, `DELETE FROM workflow_roles WHERE id = $1`, roleA); err != nil {
		t.Fatalf("hard-delete the role row: %v", err)
	}
	for _, id := range memberIDs {
		if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_role_members WHERE id = $1`, id); n != 0 {
			t.Errorf("staffing row %s after the role delete = %d, want 0 (the role FK is ON DELETE CASCADE)", id, n)
		}
	}
}

// Three FKs share tenant_id across these two tables plus memberships; deleting the tenant
// must still cascade cleanly rather than raising an FK error.
func TestRLS_WorkflowRoleMembersTenantDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'workflow-roles throwaway tenant')`, tenantID); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	}()

	userID := uuid.NewString()
	seedMembership(t, tenantID, userID, "reviewer")
	roleID, _ := seedWorkflowRole(t, tenantID, wrKey("cascade-tenant"))
	memberID, _ := seedWorkflowRoleMember(t, tenantID, roleID, userID, 0)

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete the tenant: %v", err)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_roles WHERE id = $1`, roleID); n != 0 {
		t.Errorf("role rows after the tenant delete = %d, want 0", n)
	}
	if n := mustCount(t, h.super, `SELECT count(*) FROM workflow_role_members WHERE id = $1`, memberID); n != 0 {
		t.Errorf("staffing rows after the tenant delete = %d, want 0", n)
	}
}

// The same user may not be staffed into the same role twice, whatever ord says.
func TestRLS_WorkflowRoleMembersDuplicateUserRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupRole := seedWorkflowRole(t, h.tenantA, wrKey("dup"))
	defer cleanupRole()
	userID := uuid.NewString()
	_, cleanupMem := seedMembership(t, h.tenantA, userID, "reviewer")
	defer cleanupMem()
	_, cleanupMember := seedWorkflowRoleMember(t, h.tenantA, roleA, userID, 0)
	defer cleanupMember()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, 1)`,
			h.tenantA, roleA, userID)
		return e
	})
	if err == nil {
		t.Fatal("staffing the same user into the same role twice succeeded, want 23505 (unique_violation)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate (role, user): SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "workflow_role_members_tenant_role_user_uq" {
		t.Errorf("constraint = %q, want %q — a different index rejected it", name,
			"workflow_role_members_tenant_role_user_uq")
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM workflow_role_members WHERE workflow_role_id = $1`, roleA); n != 1 {
		t.Errorf("staffing rows for the role after the refused duplicate = %d, want 1", n)
	}
}

// ord is NOT NULL with no DEFAULT on purpose: a silent DEFAULT 0 would collapse a whole
// submitted order to position 0 undetected. The ord-supplied leg is the positive control.
func TestRLS_WorkflowRoleMembersOrdNotNull(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupRole := seedWorkflowRole(t, h.tenantA, wrKey("ord"))
	defer cleanupRole()
	userID := uuid.NewString()
	_, cleanupMem := seedMembership(t, h.tenantA, userID, "reviewer")
	defer cleanupMem()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id) VALUES ($1, $2, $3)`,
			h.tenantA, roleA, userID)
		return e
	})
	if err == nil {
		t.Fatal("INSERT omitting ord succeeded, want not_null_violation (SQLSTATE 23502) — ord must carry no DEFAULT")
	}
	if code := pgCode(err); code != "23502" {
		t.Fatalf("INSERT omitting ord: SQLSTATE = %q, want 23502 (not_null_violation): %v", code, err)
	}
	if msg := pgMessage(err); !strings.Contains(msg, `"ord"`) {
		t.Errorf("refusal message = %q, want it to name the ord column", msg)
	}

	var id string
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord)
			 VALUES ($1, $2, $3, 0) RETURNING id`, h.tenantA, roleA, userID).Scan(&id)
	}); err != nil {
		t.Fatalf("INSERT with ord supplied: want success, got: %v", err)
	}
	_, _ = h.super.Exec(ctx, `DELETE FROM workflow_role_members WHERE id = $1`, id)
}

// The ROLE leg of the composite FK, the half CrossTenantUserRefused does not reach: tenant
// A naming B's role id is refused by workflow_role_members_tenant_role_fk. A single-column
// REFERENCES workflow_roles(id) would accept it — the id exists — and referential checks run
// RLS-bypassed, so nothing else in the schema would stop A staffing seats onto B's role.
// Attempted as the superuser so no policy can be what refuses.
func TestRLS_WorkflowRoleMembersCrossTenantRoleRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleB, cleanupRole := seedWorkflowRole(t, h.tenantB, wrKey("cross-role"))
	defer cleanupRole()
	ownUser := uuid.NewString()
	_, cleanupMem := seedMembership(t, h.tenantA, ownUser, "reviewer")
	defer cleanupMem()

	_, err := h.super.Exec(ctx,
		`INSERT INTO workflow_role_members (tenant_id, workflow_role_id, user_id, ord) VALUES ($1, $2, $3, 0)`,
		h.tenantA, roleB, ownUser)
	if err == nil {
		t.Fatal("staffing tenant A's own member onto tenant B's role id succeeded, want 23503 — the " +
			"workflow_roles FK is not composite on (tenant_id, id)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("cross-tenant workflow_role_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}
	if name := pgConstraint(err); name != "workflow_role_members_tenant_role_fk" {
		t.Errorf("constraint = %q, want %q", name, "workflow_role_members_tenant_role_fk")
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM workflow_role_members WHERE workflow_role_id = $1`, roleB); n != 0 {
		t.Errorf("staffing rows after the refused INSERT = %d, want 0", n)
	}
}

// Postgres requires the UPDATE privilege for SELECT ... FOR UPDATE, and the staffing table
// has none — so the whole-set-replace path must take its lock on the ROLE row, never on a
// staffing row. Mirror of TestRLS_WorkflowRolesSelectForUpdateAllowedForApp: a stray GRANT
// UPDATE added to "make reordering easier" turns this refusal green and makes in-place ord
// mutation possible.
func TestRLS_WorkflowRoleMembersSelectForUpdateRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	roleA, cleanupRole := seedWorkflowRole(t, h.tenantA, wrKey("no-lock"))
	defer cleanupRole()
	userID := uuid.NewString()
	_, cleanupMem := seedMembership(t, h.tenantA, userID, "reviewer")
	defer cleanupMem()
	memberA, cleanupMember := seedWorkflowRoleMember(t, h.tenantA, roleA, userID, 0)
	defer cleanupMember()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var id string
		return tx.QueryRow(ctx,
			`SELECT id::text FROM workflow_role_members WHERE id = $1 FOR UPDATE`, memberA).Scan(&id)
	})
	if err == nil {
		t.Fatal("app-role SELECT ... FOR UPDATE on workflow_role_members succeeded, want permission denied " +
			"(SQLSTATE 42501) — the table carries no UPDATE grant")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("FOR UPDATE on the staffing table: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}
	if msg := pgMessage(err); !strings.Contains(msg, "permission denied") {
		t.Errorf("refusal message = %q, want the grant layer (\"permission denied\")", msg)
	}

	// Positive control: the same row read plainly by the same role succeeds, so the refusal
	// is about the lock's UPDATE requirement and not about visibility.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM workflow_role_members WHERE id = $1`, memberA); n != 1 {
			t.Errorf("plain SELECT of the same staffing row = %d, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("plain SELECT: %v", err)
	}
}

// tenant_isolation must not filter on deleted_at. The key-mint scan reads keys WITHOUT a
// deleted_at filter precisely because the UNIQUE spans soft-deleted rows; a policy narrowed
// to `AND deleted_at IS NULL` would blind that scan while KeyUniquePerTenantSpansDeleted
// stayed green — unique-index checks bypass RLS, so its 23505 fires whether the sealed row
// is visible or not.
func TestRLS_WorkflowRolesSoftDeletedRowStaysVisibleToItsTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	key := wrKey("sealed")
	roleA, cleanup := seedWorkflowRole(t, h.tenantA, key)
	defer cleanup()
	if _, err := h.super.Exec(ctx, `UPDATE workflow_roles SET deleted_at = now() WHERE id = $1`, roleA); err != nil {
		t.Fatalf("soft-delete the seeded role: %v", err)
	}

	if err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var gotKey string
		var isDeleted bool
		if e := tx.QueryRow(ctx,
			`SELECT key, deleted_at IS NOT NULL FROM workflow_roles WHERE id = $1`, roleA,
		).Scan(&gotKey, &isDeleted); e != nil {
			return e
		}
		if gotKey != key {
			t.Errorf("key of the soft-deleted role = %q, want %q", gotKey, key)
		}
		if !isDeleted {
			t.Error("deleted_at on the soft-deleted role is NULL, want it set")
		}
		return nil
	}); err != nil {
		t.Fatalf("read the soft-deleted role as its own tenant: %v — tenant_isolation must not filter deleted_at, "+
			"or the key-mint scan cannot see a sealed key it must not re-issue", err)
	}

	// Still isolated: sealed or not, B never sees it.
	if err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM workflow_roles WHERE id = $1`, roleA); n != 0 {
			t.Errorf("A's soft-deleted role visible to B = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithinTenantTx as B: %v", err)
	}
}
