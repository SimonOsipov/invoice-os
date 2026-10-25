// DEL-04-02: the request seam refuses every non-READ ONLY transaction of a workspace
// scheduled for deletion, after the membership gate. Named TestRLS_ so ci.yml's
// -run TestRLS step reaches each case.
package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// lockTenant schedules the tenant through the superuser; the trigger derives the due time.
func lockTenant(t *testing.T, tenantID string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = now(), deletion_scheduled_by = $2 WHERE id = $1`,
		tenantID, uuid.NewString()); err != nil {
		t.Fatalf("lock tenant: %v", err)
	}
	t.Cleanup(func() { unlockTenant(t, tenantID) })
}

func unlockTenant(t *testing.T, tenantID string) {
	t.Helper()
	if _, err := h.super.Exec(context.Background(),
		`UPDATE tenants SET deletion_scheduled_at = NULL, deletion_scheduled_by = NULL WHERE id = $1`,
		tenantID); err != nil {
		t.Fatalf("unlock tenant: %v", err)
	}
}

func TestRLS_RequestSeamRefusesAReadWriteTxOfALockedWorkspace(t *testing.T) {
	h := requireHarness(t)
	user := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, user, "admin", "active")
	ctx := requestCtx(user, h.tenantA)

	// Control: unlocked, the same call runs, so the refusals below are the lock's.
	ran := false
	if err := db.WithinRequestTenantTx(ctx, h.app, func(pgx.Tx) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("control: unlocked workspace refused its active admin: ran=%v err=%v", ran, err)
	}

	lockTenant(t, h.tenantA)

	ran = false
	err := db.WithinRequestTenantTx(ctx, h.app, func(pgx.Tx) error { ran = true; return nil })
	if !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Errorf("WithinRequestTenantTx err = %v, want db.ErrWorkspaceLocked", err)
	}
	if ran {
		t.Error("WithinRequestTenantTx: the closure ran in a locked workspace")
	}

	for name, opts := range map[string]pgx.TxOptions{
		"zero":            {},
		"explicit write":  {AccessMode: pgx.ReadWrite},
		"repeatable read": {IsoLevel: pgx.RepeatableRead},
	} {
		ran = false
		err := db.WithinRequestTenantTxOpts(ctx, h.app, opts, func(pgx.Tx) error { ran = true; return nil })
		if !errors.Is(err, db.ErrWorkspaceLocked) {
			t.Errorf("%s: err = %v, want db.ErrWorkspaceLocked", name, err)
		}
		if ran {
			t.Errorf("%s: the closure ran in a locked workspace", name)
		}
	}
}

func TestRLS_RequestSeamAdmitsAReadOnlyTxOfALockedWorkspace(t *testing.T) {
	h := requireHarness(t)
	user := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, user, "admin", "active")
	lockTenant(t, h.tenantA)
	ctx := requestCtx(user, h.tenantA)
	ro := pgx.TxOptions{AccessMode: pgx.ReadOnly}

	// Control: the lock is live for a default-options tx, so the READ ONLY pass below is the exemption.
	if err := db.WithinRequestTenantTx(ctx, h.app, func(pgx.Tx) error { return nil }); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Fatalf("control: default-options tx of a locked workspace err = %v, want db.ErrWorkspaceLocked", err)
	}

	var want string
	if err := h.super.QueryRow(context.Background(), `SELECT name FROM tenants WHERE id = $1`, h.tenantA).Scan(&want); err != nil {
		t.Fatalf("read tenant name: %v", err)
	}

	var got string
	ran := false
	err := db.WithinRequestTenantTxOpts(ctx, h.app, ro, func(tx pgx.Tx) error {
		ran = true
		return tx.QueryRow(context.Background(), `SELECT name FROM tenants`).Scan(&got)
	})
	if err != nil {
		t.Fatalf("READ ONLY tx of a locked workspace: err = %v, want nil", err)
	}
	if !ran || got != want {
		t.Errorf("READ ONLY closure ran=%v read %q, want it to run and read %q", ran, got, want)
	}

	// A write inside the admitted READ ONLY tx is Postgres's refusal, not the seam's.
	err = db.WithinRequestTenantTxOpts(ctx, h.app, ro, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO audit_log (tenant_id, actor, event, payload) VALUES ($1, 'del-04-02', 'lock.test', '{}'::jsonb)`, h.tenantA)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Errorf("write inside READ ONLY: err = %v, want SQLSTATE 25006", err)
	}
}

func TestRLS_RequestSeamPutsTheMembershipGateBeforeTheLock(t *testing.T) {
	h := requireHarness(t)
	active, suspended, removed := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, active, "admin", "active")
	seedMembershipWithStatus(t, h.tenantA, suspended, "admin", "suspended")
	seedMembershipWithStatus(t, h.tenantA, removed, "admin", "removed")
	lockTenant(t, h.tenantA)

	noop := func(pgx.Tx) error { return nil }
	// Control: the lock is live for an active member, so the two below prove ordering.
	if err := db.WithinRequestTenantTx(requestCtx(active, h.tenantA), h.app, noop); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Fatalf("control: active member of a locked workspace err = %v, want db.ErrWorkspaceLocked", err)
	}
	for name, user := range map[string]string{"suspended": suspended, "removed": removed} {
		ran := false
		err := db.WithinRequestTenantTx(requestCtx(user, h.tenantA), h.app, func(pgx.Tx) error { ran = true; return nil })
		if !errors.Is(err, db.ErrNotActiveMember) {
			t.Errorf("%s member of a locked workspace err = %v, want db.ErrNotActiveMember", name, err)
		}
		if errors.Is(err, db.ErrWorkspaceLocked) {
			t.Errorf("%s member got the lock refusal; the membership gate must win", name)
		}
		if ran {
			t.Errorf("%s member: the closure ran", name)
		}
	}
}

func TestRLS_RequestSeamReadsTheLockPerTransaction(t *testing.T) {
	h := requireHarness(t)
	user := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, user, "admin", "active")
	ctx := requestCtx(user, h.tenantA)
	noop := func(pgx.Tx) error { return nil }

	lockTenant(t, h.tenantA)
	if err := db.WithinRequestTenantTx(ctx, h.app, noop); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Fatalf("locked: err = %v, want db.ErrWorkspaceLocked", err)
	}

	unlockTenant(t, h.tenantA)
	ran := false
	if err := db.WithinRequestTenantTx(ctx, h.app, func(pgx.Tx) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("after the lock cleared on the same pool: ran=%v err=%v, want the closure to run", ran, err)
	}

	lockTenant(t, h.tenantA)
	if err := db.WithinRequestTenantTx(ctx, h.app, noop); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Errorf("locked again: err = %v, want db.ErrWorkspaceLocked", err)
	}
}

func TestRLS_RequestSeamLockIsScopedToTheCurrentTenant(t *testing.T) {
	h := requireHarness(t)
	inA, inB := uuid.NewString(), uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, inA, "admin", "active")
	seedMembershipWithStatus(t, h.tenantB, inB, "admin", "active")
	lockTenant(t, h.tenantB)

	// Control: B's own caller is refused, so the lock is live.
	if err := db.WithinRequestTenantTx(requestCtx(inB, h.tenantB), h.app, func(pgx.Tx) error { return nil }); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Fatalf("control: caller of locked tenant B err = %v, want db.ErrWorkspaceLocked", err)
	}
	ran := false
	if err := db.WithinRequestTenantTx(requestCtx(inA, h.tenantA), h.app, func(pgx.Tx) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("caller of unlocked tenant A while B is locked: ran=%v err=%v, want the closure to run", ran, err)
	}
}

func TestRLS_WorkspaceLockedReadsOnlyTheBoundTenant(t *testing.T) {
	h := requireHarness(t)
	lockTenant(t, h.tenantA)
	locked := func(tenantID string) bool {
		t.Helper()
		var got bool
		if err := db.WithinTenantTx(context.Background(), h.app, tenantID, func(tx pgx.Tx) error {
			var err error
			got, err = db.WorkspaceLocked(context.Background(), tx)
			return err
		}); err != nil {
			t.Fatalf("WorkspaceLocked in %s: %v", tenantID, err)
		}
		return got
	}
	if !locked(h.tenantA) {
		t.Error("WorkspaceLocked(tenant A, locked) = false, want true")
	}
	if locked(h.tenantB) {
		t.Error("WorkspaceLocked(tenant B, unlocked) = true while only A is locked, want false")
	}
	if locked(uuid.NewString()) {
		t.Error("WorkspaceLocked(absent tenant) = true, want false")
	}
}

func TestRLS_WorkerSeamIsNotRefusedByTheLock(t *testing.T) {
	h := requireHarness(t)
	lockTenant(t, h.tenantA)
	// audit_log is append-only even for the superuser, so a fixture table stands in for the write.
	payload := "del-04-02-worker-" + uuid.NewString()
	insert := func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO rls_fixture (tenant_id, payload) VALUES ($1, $2)`, h.tenantA, payload)
		return err
	}
	t.Cleanup(func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM rls_fixture WHERE payload = $1`, payload)
	})
	count := func() int {
		var n int
		if err := h.super.QueryRow(context.Background(),
			`SELECT count(*) FROM rls_fixture WHERE payload = $1`, payload).Scan(&n); err != nil {
			t.Fatalf("count fixture rows: %v", err)
		}
		return n
	}

	// Control: the lock is live for the request seam, so the passes below are the exemption.
	user := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, user, "admin", "active")
	if err := db.WithinRequestTenantTx(requestCtx(user, h.tenantA), h.app, insert); !errors.Is(err, db.ErrWorkspaceLocked) {
		t.Fatalf("control: request seam err = %v, want db.ErrWorkspaceLocked", err)
	}

	if err := db.WithinTenantTx(context.Background(), h.app, h.tenantA, insert); err != nil {
		t.Fatalf("WithinTenantTx on a locked workspace: %v, want nil", err)
	}
	if err := db.WithinRequestTenantTx(requestCtx("extraction-worker", h.tenantA), h.app, insert); err != nil {
		t.Fatalf("non-UUID subject through WithinRequestTenantTx on a locked workspace: %v, want nil", err)
	}
	if n := count(); n != 2 {
		t.Errorf("rows written by the worker seams = %d, want 2", n)
	}
}

// The lock rides statement 2 of the existing batch: one batch, two statements, the lock column
// named in the second, no standalone lookup. TestRLS_RequestSeamIssuesOneRoundTripForTheGate
// covers the unlocked shape and stays unchanged.
func TestRLS_RequestSeamReadsTheLockInTheGateBatch(t *testing.T) {
	h := requireHarness(t)
	user := uuid.NewString()
	seedMembershipWithStatus(t, h.tenantA, user, "admin", "active")
	lockTenant(t, h.tenantA)

	for name, opts := range map[string]pgx.TxOptions{
		"refused":  {},
		"admitted": {AccessMode: pgx.ReadOnly},
	} {
		pool, tr := tracedSeamPool(t)
		_ = db.WithinRequestTenantTxOpts(requestCtx(user, h.tenantA), pool, opts, func(pgx.Tx) error { return nil })

		starts := tr.starts()
		if len(starts) != 1 || len(starts[0]) != 2 {
			t.Fatalf("%s: batches = %q, want exactly one batch of two statements", name, starts)
		}
		if !containsFold(starts[0][1], "deletion_due_at") {
			t.Errorf("%s: batch statement 2 = %q, want it to read deletion_due_at", name, starts[0][1])
		}
		if n := countContaining(tr.standaloneStmts(), "deletion_due_at") + countContaining(tr.standaloneStmts(), "set_config"); n != 0 {
			t.Errorf("%s: the seam sent %d standalone lock/set_config statement(s), want 0", name, n)
		}
	}
}

func containsFold(s, sub string) bool { return countContaining([]string{s}, sub) > 0 }
