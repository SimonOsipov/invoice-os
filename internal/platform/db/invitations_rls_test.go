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
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const invitationsTokenMigrationGlob = "*_invitations_token_expiry_inviter.sql"

// tokenHash returns n random bytes, so every seeded pending row has its own hash.
func tokenHash(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	return b
}

// pgViolation asserts err is a PgError with the given SQLSTATE and constraint name.
func pgViolation(t *testing.T, what string, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: want %s on %s, got %v", what, code, constraint, err)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("%s: want %s on %s, got %s on %q (%s)", what, code, constraint, pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}

// seedInvitation inserts one invitations row for tenantID/role/invitee_email as the
// superuser (BYPASSRLS, so seeding needs no tenant context) and returns its id plus a
// cleanup func. Scoped per-test — see the package doc comment above for why this must
// NOT move into the shared harness.seed().
func seedInvitation(t *testing.T, tenantID, role, invitee string) (id string, cleanup func()) {
	t.Helper()
	return seedInvitationWithHash(t, tenantID, role, invitee, tokenHash(t, 32))
}

// seedInvitationWithHash is seedInvitation with a caller-chosen token hash.
func seedInvitationWithHash(t *testing.T, tenantID, role, invitee string, hash []byte) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO invitations (id, tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
		 VALUES ($1, $2, $3, $4, $5, now() + interval '7 days', $6)`,
		id, tenantID, role, invitee, hash, uuid.NewString(),
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
			`INSERT INTO invitations (tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
			 VALUES ($1, 'preparer', 'x@e.io', $2, now() + interval '7 days', $3)`,
			h.tenantB, tokenHash(t, 32), uuid.NewString(),
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

	rowA, cleanupA := seedInvitation(t, h.tenantA, "preparer", "no-context-a@example.com")
	defer cleanupA()
	rowB, cleanupB := seedInvitation(t, h.tenantB, "preparer", "no-context-b@example.com")
	defer cleanupB()

	// Control: the zero below means nothing unless the rows are there to hide.
	if n := mustCount(t, h.super, `SELECT count(*) FROM invitations WHERE id IN ($1, $2)`, rowA, rowB); n != 2 {
		t.Fatalf("superuser sees %d of the two seeded rows, want 2", n)
	}

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM invitations WHERE id IN ($1, $2)`, rowA, rowB); n != 0 {
		t.Errorf("seeded invitations rows visible with no tenant set = %d, want 0", n)
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
			`INSERT INTO invitations (tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
			 VALUES ($1, 'preparer', 'alice@e.io', $2, now() + interval '7 days', $3) RETURNING id`,
			h.tenantA, tokenHash(t, 32), uuid.NewString(),
		).Scan(&firstID)
	})
	if err != nil {
		t.Fatalf("insert first pending invitation: %v", err)
	}
	defer cleanupIDs(firstID)

	// A second pending invitation for the same (tenant, email) is refused.
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invitations (tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
			 VALUES ($1, 'preparer', 'alice@e.io', $2, now() + interval '7 days', $3)`,
			h.tenantA, tokenHash(t, 32), uuid.NewString(),
		)
		return e
	})
	if err == nil {
		t.Fatal("duplicate pending (tenant_id, invitee_email) succeeded, want unique_violation (SQLSTATE 23505)")
	}
	pgViolation(t, "duplicate pending (tenant_id, invitee_email)", err, "23505", "invitations_tenant_invitee_pending_uq")

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
			`INSERT INTO invitations (tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
			 VALUES ($1, 'preparer', '', $2, now() + interval '7 days', $3)`,
			h.tenantA, tokenHash(t, 32), uuid.NewString(),
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with invitee_email = '' succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	pgViolation(t, "insert with invitee_email = ''", err, "23514", "invitations_invitee_email_check")
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
			`INSERT INTO invitations (tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
			 VALUES ($1, 'preparer', 'bob@e.io', $2, now() + interval '7 days', $3) RETURNING id`,
			h.tenantA, tokenHash(t, 32), uuid.NewString(),
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

// INV-RLS-08 (F5, QA-added): invoice_app has NO DELETE grant on invitations — revoking
// an invite is an UPDATE to status = 'revoked', not a row deletion (see the migration
// header). Even a same-tenant DELETE on a row the app can otherwise see/update must be
// refused, and refused at the GRANT level (SQLSTATE 42501 insufficient_privilege) rather
// than by RLS, since there is no privilege for the policy to even evaluate against.
// This is the one guarantee the Test Spec's INV-RLS-0x cases don't cover: none of them
// exercise DELETE, so a future migration that widens the GRANT to include DELETE would
// slip through unnoticed without this case.
func TestRLS_InvitationsDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	id, cleanup := seedInvitation(t, h.tenantA, "preparer", "delete-refused@example.com")
	defer cleanup()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM invitations WHERE tenant_id = $1`, h.tenantA)
		return e
	})
	if err == nil {
		t.Fatal("app-role DELETE on invitations succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role DELETE on invitations: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	// The row must still exist — a permission-denied DELETE has no effect.
	if n := mustCount(t, h.super, `SELECT count(*) FROM invitations WHERE id = $1`, id); n != 1 {
		t.Errorf("row count after refused DELETE = %d, want 1 (row must survive)", n)
	}
}

// inTenantRollback runs fn as the app role scoped to tenant, then always rolls back, so a
// case can insert freely without cleanup. A refused statement aborts the tx: one per attempt.
func inTenantRollback(t *testing.T, tenant string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	return fn(ctx, tx)
}

const insertPending = `INSERT INTO invitations (tenant_id, role, invitee_email, token_hash, expires_at, invited_by)
	 VALUES ($1, 'preparer', $2, $3, $4, $5)`

// RESEND-05-01 AC 1: the four columns, their types and nullability, and the send_status
// default and CHECK.
func TestRLS_InvitationsTokenColumnsExist(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	type col struct {
		dataType, nullable string
		deflt              *string
	}
	rows, err := h.super.Query(ctx, `
		SELECT column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_name = 'invitations'
		  AND column_name IN ('token_hash','expires_at','invited_by','send_status')`)
	if err != nil {
		t.Fatalf("read information_schema.columns: %v", err)
	}
	got := map[string]col{}
	for rows.Next() {
		var name string
		var c col
		if err := rows.Scan(&name, &c.dataType, &c.nullable, &c.deflt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[name] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("invitations has %d of the 4 new columns (token_hash, expires_at, invited_by, send_status): %v", len(got), got)
	}
	for name, want := range map[string]col{
		"token_hash":  {dataType: "bytea", nullable: "YES"},
		"expires_at":  {dataType: "timestamp with time zone", nullable: "YES"},
		"invited_by":  {dataType: "uuid", nullable: "YES"},
		"send_status": {dataType: "text", nullable: "NO"},
	} {
		c := got[name]
		if c.dataType != want.dataType || c.nullable != want.nullable {
			t.Errorf("%s = %s nullable=%s, want %s nullable=%s", name, c.dataType, c.nullable, want.dataType, want.nullable)
		}
		if name != "send_status" && c.deflt != nil {
			t.Errorf("%s has default %q, want none", name, *c.deflt)
		}
	}
	if d := got["send_status"].deflt; d == nil || *d != "'sending'::text" {
		t.Errorf("send_status default = %v, want 'sending'::text", d)
	}

	// Every accepted value stores; the default is 'sending'; anything else is refused.
	for _, v := range []string{"sending", "sent", "failed"} {
		err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `INSERT INTO invitations (tenant_id, role, invitee_email, status, send_status)
				VALUES ($1, 'preparer', 'send-status@e.io', 'revoked', $2)`, h.tenantA, v)
			return e
		})
		if err != nil {
			t.Errorf("send_status = %q refused: %v", v, err)
		}
	}
	var dflt string
	err = inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO invitations (tenant_id, role, invitee_email, status)
			VALUES ($1, 'preparer', 'send-default@e.io', 'revoked') RETURNING send_status`, h.tenantA).Scan(&dflt)
	})
	if err != nil || dflt != "sending" {
		t.Errorf("send_status of a row that names none = %q (err %v), want sending", dflt, err)
	}
	err = inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO invitations (tenant_id, role, invitee_email, token_hash, expires_at, invited_by, send_status)
			VALUES ($1, 'preparer', 'send-bogus@e.io', $2, now() + interval '7 days', $3, 'bogus')`,
			h.tenantA, tokenHash(t, 32), uuid.NewString())
		return e
	})
	pgViolation(t, "send_status = 'bogus'", err, "23514", "invitations_send_status_check")
}

// RESEND-05-01 AC 2: a pending row missing any of the three is refused; a complete one stores.
func TestRLS_InvitationsPendingRowNeedsItsToken(t *testing.T) {
	h := requireHarness(t)
	exp, by := time.Now().Add(7*24*time.Hour), uuid.NewString()

	if err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, insertPending, h.tenantA, "complete@e.io", tokenHash(t, 32), exp, by)
		return e
	}); err != nil {
		t.Fatalf("a pending row with all three of token_hash, expires_at, invited_by was refused: %v", err)
	}

	for _, c := range []struct {
		missing string
		hash    []byte
		exp     *time.Time
		by      *string
	}{
		{"token_hash", nil, &exp, &by},
		{"expires_at", tokenHash(t, 32), nil, &by},
		{"invited_by", tokenHash(t, 32), &exp, nil},
	} {
		err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, insertPending, h.tenantA, "missing-"+c.missing+"@e.io", c.hash, c.exp, c.by)
			return e
		})
		pgViolation(t, "pending row missing "+c.missing, err, "23514", "invitations_pending_has_token")
	}
}

// RESEND-05-01 AC 3: only a pending row needs the token fields.
func TestRLS_InvitationsNonPendingRowNeedsNoToken(t *testing.T) {
	h := requireHarness(t)

	var n int
	var hashIsNull bool
	err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `INSERT INTO invitations (tenant_id, role, invitee_email, status)
			VALUES ($1, 'preparer', 'revoked-bare@e.io', 'revoked') RETURNING id`, h.tenantA).Scan(&id); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT count(*), bool_and(token_hash IS NULL) FROM invitations WHERE id = $1`, id).Scan(&n, &hashIsNull)
	})
	if err != nil {
		t.Fatalf("a revoked row with no token fields was refused: %v", err)
	}
	if n != 1 || !hashIsNull {
		t.Errorf("stored revoked row: count=%d token_hash null=%v, want 1 and true", n, hashIsNull)
	}
}

// RESEND-05-01 AC 4 (boundary): a token hash is exactly 32 bytes.
func TestRLS_InvitationsTokenHashIsThirtyTwoBytes(t *testing.T) {
	h := requireHarness(t)
	exp, by := time.Now().Add(7*24*time.Hour), uuid.NewString()

	for _, n := range []int{31, 33} {
		err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, insertPending, h.tenantA, "len@e.io", tokenHash(t, n), exp, by)
			return e
		})
		pgViolation(t, fmt.Sprintf("token_hash of %d bytes", n), err, "23514", "invitations_token_hash_len")
	}
	if err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, insertPending, h.tenantA, "len@e.io", tokenHash(t, 32), exp, by)
		return e
	}); err != nil {
		t.Fatalf("a 32-byte token_hash was refused: %v", err)
	}
}

// RESEND-05-01 AC 5: one token identifies one invite, inside a tenant and across tenants.
func TestRLS_InvitationsTokenHashIsUnique(t *testing.T) {
	h := requireHarness(t)
	exp, by := time.Now().Add(7*24*time.Hour), uuid.NewString()

	hash := tokenHash(t, 32)
	_, cleanup := seedInvitationWithHash(t, h.tenantA, "preparer", "uniq-first@example.com", hash)
	defer cleanup()

	// Control: a different hash for another address stores, so only the collision is refused.
	if err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, insertPending, h.tenantA, "uniq-other@example.com", tokenHash(t, 32), exp, by)
		return e
	}); err != nil {
		t.Fatalf("a pending row with a fresh token_hash was refused: %v", err)
	}

	err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, insertPending, h.tenantA, "uniq-second@example.com", hash, exp, by)
		return e
	})
	pgViolation(t, "same token_hash, same tenant", err, "23505", "invitations_token_hash_uq")

	// Superuser bypasses RLS, so the refusal can only come from the index.
	_, err = h.super.Exec(context.Background(), insertPending, h.tenantB, "uniq-b@example.com", hash, exp, by)
	pgViolation(t, "same token_hash, other tenant", err, "23505", "invitations_token_hash_uq")
}

// RESEND-05-01 AC 6: RLS covers the new columns, for reads and writes.
func TestRLS_InvitationsTokenColumnsCrossTenantRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	hash := tokenHash(t, 32)
	idA, cleanup := seedInvitationWithHash(t, h.tenantA, "preparer", "cross-token@example.com", hash)
	defer cleanup()
	var wantExp time.Time
	if err := h.super.QueryRow(ctx, `SELECT expires_at FROM invitations WHERE id = $1`, idA).Scan(&wantExp); err != nil {
		t.Fatalf("read seeded expires_at: %v", err)
	}

	const byHash = `SELECT count(*) FROM invitations WHERE token_hash = $1`
	// Control: the owner sees the row by hash, so the zeros below mean something.
	if err := inTenantRollback(t, h.tenantA, func(ctx context.Context, tx pgx.Tx) error {
		if n := mustCount(t, tx, byHash, hash); n != 1 {
			t.Errorf("tenant A sees %d rows by its own token_hash, want 1", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var updated int64
	if err := inTenantRollback(t, h.tenantB, func(ctx context.Context, tx pgx.Tx) error {
		if n := mustCount(t, tx, byHash, hash); n != 0 {
			t.Errorf("tenant B sees %d of A's rows by token_hash, want 0", n)
		}
		tag, e := tx.Exec(ctx, `UPDATE invitations SET token_hash = $1, expires_at = now() WHERE id = $2`, tokenHash(t, 32), idA)
		updated = tag.RowsAffected()
		return e
	}); err != nil {
		t.Fatalf("scoped to B: %v", err)
	}
	if updated != 0 {
		t.Errorf("B's UPDATE of A's row affected %d rows, want 0", updated)
	}

	var gotHash []byte
	var gotExp time.Time
	if err := h.super.QueryRow(ctx, `SELECT token_hash, expires_at FROM invitations WHERE id = $1`, idA).Scan(&gotHash, &gotExp); err != nil {
		t.Fatalf("re-read A's row: %v", err)
	}
	if string(gotHash) != string(hash) || !gotExp.Equal(wantExp) {
		t.Errorf("A's row after B's UPDATE: token_hash changed=%v expires_at %v, want unchanged %v", string(gotHash) != string(hash), gotExp, wantExp)
	}

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if n := mustCount(t, tx, byHash, hash); n != 0 {
		t.Errorf("with no tenant set the app role sees %d rows by token_hash, want 0", n)
	}
}

// RESEND-05-01 AC 7: the Up body applies over a pending row that has no token and leaves it
// as it was. The Down body strips the columns first, which recreates the legacy row shape.
func TestRLS_InvitationsTokenMigrationKeepsALegacyPendingRow(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, invitationsTokenMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s in migrations.FS: %v", invitationsTokenMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations.FS holds %d files matching %s (%v), want exactly 1 -- the migration is absent",
			len(matches), invitationsTokenMigrationGlob, matches)
	}
	down := auditEntitySectionOf(t, matches[0], "Down")
	up := auditEntitySectionOf(t, matches[0], "Up")

	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("Down body: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, h.tenantA); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO invitations (tenant_id, role, invitee_email)
		VALUES ($1, 'preparer', 'legacy@example.com') RETURNING id`, h.tenantA).Scan(&id); err != nil {
		t.Fatalf("insert legacy pending row (columns should be gone after Down): %v", err)
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("Up body over a legacy pending row: %v", err)
	}

	var status, email string
	var hashNull, expNull, byNull bool
	if err := tx.QueryRow(ctx, `SELECT status, invitee_email, token_hash IS NULL, expires_at IS NULL, invited_by IS NULL
		FROM invitations WHERE id = $1`, id).Scan(&status, &email, &hashNull, &expNull, &byNull); err != nil {
		t.Fatalf("read legacy row after Up (it must still be visible to its tenant): %v", err)
	}
	if status != "pending" || email != "legacy@example.com" || !hashNull || !expNull || !byNull {
		t.Errorf("legacy row after Up: status=%q email=%q token_hash null=%v expires_at null=%v invited_by null=%v, want it unchanged",
			status, email, hashNull, expNull, byNull)
	}
}
