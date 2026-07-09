// M3-01-05 (task-28): tests for the `invitations` tenant-owned table, written BEFORE
// the migration exists (RED against SQLSTATE 42P01 undefined_table). The table the
// Executor will add:
//
//	invitations: id uuid PK DEFAULT gen_random_uuid(), tenant_id uuid NOT NULL
//	    REFERENCES tenants(id) ON DELETE CASCADE, role text NOT NULL REFERENCES
//	    roles(name), invitee_email text NOT NULL CHECK (char_length(invitee_email) > 0),
//	    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted',
//	    'revoked')), created_at timestamptz NOT NULL DEFAULT now() — FORCE RLS, policy
//	    `tenant_isolation` copied from the tenants/business_entities/memberships
//	    template (docs/migrations.md §6, §8; no tenant_enumerate policy). Partial
//	    UNIQUE (tenant_id, invitee_email) WHERE status = 'pending'. GRANT SELECT,
//	    INSERT, UPDATE TO invoice_app (NO DELETE — revoked status is the removal path).
//	    `roles` already exists (rows: admin, preparer, reviewer).
//
// Each case attacks the same guarantees M2-07/BE-RLS/MEM-RLS prove for the tenants/
// business_entities/memberships shape, transplanted onto invitations, plus the
// invitations-specific constraints the Test Spec calls out: the partial
// (tenant_id, invitee_email) WHERE status='pending' UNIQUE index (INV-UNIQ-04) and the
// status CHECK (INV-STATUS-05).
//
// Rows are seeded per-test (seedInvitation below), NOT in the shared harness.seed() in
// rls_harness_test.go — that runs in TestMain before every test in the package, so a
// missing invitations table would break the ENTIRE suite instead of failing only these
// INV-RLS cases.
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS`
// (.github/workflows/ci.yml) and `make test-rls` both pick these up automatically.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_Invitations -v ./internal/platform/db/...
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedInvitation inserts one invitations row for tenantID/role/invitee_email as the
// superuser (BYPASSRLS, so seeding needs no tenant context) and returns its id plus a
// cleanup func. Scoped per-test — see the package doc comment above for why this must
// NOT move into the shared harness.seed().
func seedInvitation(t *testing.T, tenantID, role, invitee string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO invitations (id, tenant_id, role, invitee_email) VALUES ($1, $2, $3, $4)`,
		id, tenantID, role, invitee,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed invitations: undefined_table (42P01) — invitations migration not applied yet: %v", err)
		}
		t.Fatalf("seed invitations: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invitations WHERE id = $1`, id)
	}
}

// INV-RLS-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees
// only A's invitation row; B's is invisible (filtered out, not an error).
func TestRLS_InvitationsCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	_, cleanupA := seedInvitation(t, h.tenantA, "preparer", "a-invitee@example.com")
	defer cleanupA()
	_, cleanupB := seedInvitation(t, h.tenantB, "preparer", "b-invitee@example.com")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM invitations WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM invitations WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// INV-RLS-02: a cross-tenant INSERT (row named for tenant B while scoped to A) is
// refused with a WITH CHECK violation, SQLSTATE 42501.
func TestRLS_InvitationsCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invitations (tenant_id, role, invitee_email) VALUES ($1, 'preparer', 'x@e.io')`,
			h.tenantB,
		)
		return e
	})
	assertRLSViolation(t, err)
}

// INV-RLS-03: a missing app.current_tenant GUC fails closed — with no context set, the
// isolation predicate is false for every row and the connection sees nothing.
func TestRLS_InvitationsMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM invitations`); n != 0 {
		t.Errorf("invitations visible with no tenant set = %d, want 0", n)
	}
}

// INV-UNIQ-04: (tenant_id, invitee_email) is UNIQUE only while status = 'pending' (a
// partial index). A second pending invitation for the same email in the same tenant is
// refused (23505 unique_violation), but a third row for the SAME email in the SAME
// tenant with status = 'revoked' succeeds — proving the index's WHERE clause, not a
// plain UNIQUE(tenant_id, invitee_email), is what's enforced.
func TestRLS_InvitationsPendingEmailUniquePartial(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	cleanupIDs := func(ids ...string) {
		for _, id := range ids {
			if id == "" {
				continue
			}
			_, _ = h.super.Exec(context.Background(), `DELETE FROM invitations WHERE id = $1`, id)
		}
	}

	var firstID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invitations (tenant_id, role, invitee_email) VALUES ($1, 'preparer', 'alice@e.io') RETURNING id`,
			h.tenantA,
		).Scan(&firstID)
	})
	if err != nil {
		t.Fatalf("insert first pending invitation: %v", err)
	}
	defer cleanupIDs(firstID)

	// A second pending invitation for the same (tenant, email) is refused.
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invitations (tenant_id, role, invitee_email) VALUES ($1, 'preparer', 'alice@e.io')`,
			h.tenantA,
		)
		return e
	})
	if err == nil {
		t.Fatal("duplicate pending (tenant_id, invitee_email) succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate pending (tenant_id, invitee_email): SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}

	// A THIRD row for the SAME email in the SAME tenant, explicitly status='revoked',
	// succeeds — the partial index only covers status='pending'.
	var revokedID string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invitations (tenant_id, role, invitee_email, status) VALUES ($1, 'preparer', 'alice@e.io', 'revoked') RETURNING id`,
			h.tenantA,
		).Scan(&revokedID)
	})
	if err != nil {
		t.Fatalf("insert revoked row for same (tenant, email) (want success, partial index only covers pending): %v", err)
	}
	cleanupIDs(revokedID)
}

// INV-STATUS-05: `status` and `invitee_email` both carry CHECK constraints. An
// unrecognized status value is refused (23514 check_violation, the status IN (...)
// check), and an empty invitee_email is refused (23514, the char_length(...) > 0
// check).
func TestRLS_InvitationsStatusAndEmailCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	// A bogus status is rejected.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invitations (tenant_id, role, invitee_email, status) VALUES ($1, 'preparer', 'bogus-status@e.io', 'bogus')`,
			h.tenantA,
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with status = 'bogus' succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with status = 'bogus': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}

	// An empty invitee_email is rejected.
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invitations (tenant_id, role, invitee_email) VALUES ($1, 'preparer', '')`,
			h.tenantA,
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with invitee_email = '' succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with invitee_email = '': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
}

// INV-RLS-06: a positive own-tenant INSERT succeeds — proves RLS's WITH CHECK, the
// tenants(id) FK, and the roles(name) FK all coexist for a same-tenant write, and the
// row becomes visible to its own tenant with the default status of 'pending'.
func TestRLS_InvitationsOwnTenantInsertSucceeds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var (
		id     string
		before int
	)
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		before = mustCount(t, tx, `SELECT count(*) FROM invitations WHERE tenant_id = $1`, h.tenantA)
		return tx.QueryRow(ctx,
			`INSERT INTO invitations (tenant_id, role, invitee_email) VALUES ($1, 'preparer', 'bob@e.io') RETURNING id`,
			h.tenantA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invitations WHERE id = $1`, id)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if after := mustCount(t, tx, `SELECT count(*) FROM invitations WHERE tenant_id = $1`, h.tenantA); after != before+1 {
			t.Errorf("count after own-tenant insert = %d, want %d", after, before+1)
		}
		var status string
		if e := tx.QueryRow(ctx, `SELECT status FROM invitations WHERE id = $1`, id).Scan(&status); e != nil {
			return e
		}
		if status != "pending" {
			t.Errorf("status default = %q, want %q", status, "pending")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify own-tenant insert: %v", err)
	}
}

// INV-RLS-07 (F2): reassigning an OWN, visible row to another tenant is refused. This
// is the case that catches a per-table policy copy-paste regression where the
// USING/WITH CHECK clause was narrowed to only validate fresh INSERTs and stopped
// re-checking an UPDATE's target tenant_id.
func TestRLS_InvitationsOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	_, cleanup := seedInvitation(t, h.tenantA, "preparer", "reassign@example.com")
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE invitations SET tenant_id = $1 WHERE tenant_id = $2`, h.tenantB, h.tenantA)
		return e
	})
	assertRLSViolation(t, err)
}
