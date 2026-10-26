package db_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// ldRuns returns a task that counts its runs, and the counter.
func ldRuns() (func(context.Context) error, *int) {
	n := 0
	return func(context.Context) error { n++; return nil }, &n
}

func ldStop(t *testing.T, l *db.Leader) {
	t.Helper()
	if err := l.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// AC 3 (GKE-05): two worker instances run a task once between them, and the second takes over
// when the first stops or loses its connection.
func TestLeader_OneInstanceRunsEachTask(t *testing.T) {
	dsn := requireHarness(t).appURL
	ctx := context.Background()
	task, other := "test-"+uuid.NewString(), "test-"+uuid.NewString()
	a, b := db.NewLeader(dsn), db.NewLeader(dsn)
	t.Cleanup(func() { ldStop(t, a); ldStop(t, b) })

	fnA, runsA := ldRuns()
	fnB, runsB := ldRuns()
	for range 3 {
		if err := a.Lead(task, fnA)(ctx); err != nil {
			t.Fatalf("a: %v", err)
		}
		skipCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		err := b.Lead(task, fnB)(skipCtx)
		cancel()
		if err != nil {
			t.Fatalf("b: %v", err)
		}
	}
	if *runsA != 3 || *runsB != 0 {
		t.Fatalf("runs a=%d b=%d, want 3 and 0: the first claimant leads every tick", *runsA, *runsB)
	}

	fnOther, runsOther := ldRuns()
	if err := b.Lead(other, fnOther)(ctx); err != nil || *runsOther != 1 {
		t.Fatalf("b on another task: runs=%d err=%v, want 1 run: locks are per task", *runsOther, err)
	}

	// The leader's connection dies: its locks go with it, and b takes the task.
	if _, err := db.ExportLeaderTerminate(ctx, a); err != nil {
		t.Fatalf("terminate a's connection: %v", err)
	}
	if err := b.Lead(task, fnB)(ctx); err != nil || *runsB != 1 {
		t.Fatalf("b after a's connection died: runs=%d err=%v, want 1 run", *runsB, err)
	}
	skipCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := a.Lead(task, fnA)(skipCtx); err != nil || *runsA != 3 {
		t.Fatalf("a after reconnecting: runs=%d err=%v, want still 3: b leads now", *runsA, err)
	}

	// b stops: a takes over on its next tick.
	ldStop(t, b)
	if err := a.Lead(task, fnA)(ctx); err != nil || *runsA != 4 {
		t.Fatalf("a after b stopped: runs=%d err=%v, want 4", *runsA, err)
	}
}

// BUG-52: a Lead that misses the lock must retry soon, not wait a full task interval.
func TestLeader_MissedLockRetriesAfterTheHolderStops(t *testing.T) {
	dsn := requireHarness(t).appURL
	task := "test-" + uuid.NewString()
	a, b := db.NewLeader(dsn), db.NewLeader(dsn)
	t.Cleanup(func() { ldStop(t, a); ldStop(t, b) })

	fnA, runsA := ldRuns()
	if err := a.Lead(task, fnA)(context.Background()); err != nil || *runsA != 1 {
		t.Fatalf("a: runs=%d err=%v, want 1 run: a takes the lock", *runsA, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	var ran atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = b.Lead(task, func(context.Context) error { ran.Store(true); return nil })(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })

	time.Sleep(time.Second)
	if ran.Load() {
		t.Fatal("b ran while a held the lock")
	}
	ldStop(t, a)
	deadline := time.After(15 * time.Second)
	for !ran.Load() {
		select {
		case <-done:
			if !ran.Load() {
				t.Fatal("b's Lead returned without running its task after a stopped: a missed lock is never retried")
			}
		case <-deadline:
			t.Fatal("b did not run within 15s of a stopping: a missed lock is not retried soon")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// The worker's DATABASE_URL carries pool_max_conns (db-migrations.md, Connection budget);
// Postgres refuses it as a startup parameter.
func TestLeader_ConnectsWithAPoolParameterInTheDSN(t *testing.T) {
	dsn := requireHarness(t).appURL
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	l := db.NewLeader(dsn + sep + "pool_max_conns=16")
	t.Cleanup(func() { ldStop(t, l) })
	fn, runs := ldRuns()
	if err := l.Lead("test-"+uuid.NewString(), fn)(context.Background()); err != nil || *runs != 1 {
		t.Fatalf("Lead with pool_max_conns in the DSN: runs=%d err=%v, want 1 run", *runs, err)
	}
}
