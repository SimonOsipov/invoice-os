package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func staffIdentity(t *testing.T, tenant bool) auth.Identity {
	t.Helper()
	subject := uuid.NewString()
	_, cleanup := seedMembership(t, h.tenantA, subject, "admin")
	t.Cleanup(cleanup)
	id := auth.Identity{Subject: subject, Role: "authenticated", Staff: true, RulesRole: true}
	if tenant {
		id.TenantID = h.tenantA
	}
	return id
}

// unreachablePool is lazy: any statement or connection attempt on it fails, so a refusal that
// returns first proves the seam touched no database.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://nobody:nothing@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestRLS_WithinStaffTxRefusesWithoutAStaffCaller(t *testing.T) {
	requireHarness(t)
	staff := staffIdentity(t, false)
	ctxs := map[string]context.Context{
		"plain context":                     context.Background(),
		"identity set, no rules-role check": auth.WithIdentity(context.Background(), staff),
		"tenant-less caller, no check":      auth.WithTenantlessCaller(context.Background(), staff),
	}
	for name, ctx := range ctxs {
		for poolName, pool := range map[string]*pgxpool.Pool{"app pool": h.app, "unreachable pool": unreachablePool(t)} {
			t.Run(name+"/"+poolName, func(t *testing.T) {
				ran := 0
				err := db.WithinStaffTx(ctx, pool, func(pgx.Tx) error { ran++; return nil })
				if !errors.Is(err, db.ErrNotStaff) {
					t.Errorf("err = %v, want db.ErrNotStaff", err)
				}
				if ran != 0 {
					t.Errorf("fn ran %d times, want 0", ran)
				}
			})
		}
	}

	// Control: with a checked staff caller the unreachable pool is what fails, so the refusals above came before any connection.
	err := db.WithinStaffTx(db.StaffCtxForTest(t, staff), unreachablePool(t), func(pgx.Tx) error { return nil })
	if err == nil || errors.Is(err, db.ErrNotStaff) {
		t.Errorf("control: staff caller on an unreachable pool got %v, want a connection error", err)
	}
}

func TestRLS_WithinStaffTxSetsNoTenant(t *testing.T) {
	requireHarness(t)
	const q = `SELECT coalesce(current_setting('app.current_tenant', true), '')`

	// Control: the query sees a tenant when the tenant seam sets one.
	var seen string
	if err := db.WithinTenantTx(context.Background(), h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), q).Scan(&seen)
	}); err != nil || seen != h.tenantA {
		t.Fatalf("control: tenant seam read %q (err %v), want %q", seen, err, h.tenantA)
	}

	for _, tenant := range []bool{true, false} {
		ctx := db.StaffCtxForTest(t, staffIdentity(t, tenant))
		ran := 0
		err := db.WithinStaffTx(ctx, h.app, func(tx pgx.Tx) error {
			ran++
			return tx.QueryRow(ctx, q).Scan(&seen)
		})
		if err != nil || ran != 1 {
			t.Fatalf("tenant=%v: err %v, fn ran %d times, want nil and 1", tenant, err, ran)
		}
		if seen != "" {
			t.Errorf("tenant=%v: app.current_tenant = %q inside the staff tx, want empty", tenant, seen)
		}
	}
}

func TestRLS_WithinStaffTxCommitsAndRollsBack(t *testing.T) {
	requireHarness(t)
	id := staffIdentity(t, false)
	actor := uuid.MustParse(id.Subject)
	ctx := db.StaffCtxForTest(t, id)
	committed, rolledBack := "engi11.05.commit."+actor.String(), "engi11.05.rollback."+actor.String()
	rows := func(event string) int {
		return mustCount(t, h.super, `SELECT count(*) FROM public.staff_audit_log WHERE actor = $1 AND event = $2`, actor, event)
	}

	if err := db.WithinStaffTx(ctx, h.app, func(tx pgx.Tx) error {
		return audit.RecordStaff(ctx, tx, actor, uuid.New(), committed, map[string]string{"k": "v"})
	}); err != nil {
		t.Fatalf("committing tx: %v", err)
	}

	boom := errors.New("fn failed")
	inserted := false
	err := db.WithinStaffTx(ctx, h.app, func(tx pgx.Tx) error {
		if err := audit.RecordStaff(ctx, tx, actor, uuid.New(), rolledBack, nil); err != nil {
			return err
		}
		inserted = true
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the error fn returned", err)
	}
	if !inserted {
		t.Fatal("the rolled-back insert never ran, so the zero count below proves nothing")
	}

	if n := rows(committed); n != 1 {
		t.Errorf("committed rows = %d, want 1", n)
	}
	if n := rows(rolledBack); n != 0 {
		t.Errorf("rolled-back rows = %d, want 0", n)
	}
}
