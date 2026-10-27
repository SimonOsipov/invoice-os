package db_test

import (
	"context"
	"errors"
	"strings"
	"sync"
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

// ldHold makes a take the lock for task.
func ldHold(t *testing.T, a *db.Leader, task string) {
	t.Helper()
	fn, runs := ldRuns()
	if err := a.Lead(task, fn)(context.Background()); err != nil || *runs != 1 {
		t.Fatalf("holder: runs=%d err=%v, want 1 run", *runs, err)
	}
}

// BUG-52: shutdown cancels a Lead that waits on a held lock; it returns nil at once, task unrun.
func TestLeader_CancelledWhileWaitingReturnsNilWithoutRunning(t *testing.T) {
	dsn := requireHarness(t).appURL
	task := "test-" + uuid.NewString()
	a, b := db.NewLeader(dsn), db.NewLeader(dsn)
	t.Cleanup(func() { ldStop(t, a); ldStop(t, b) })
	ldHold(t, a, task)

	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	res := make(chan error, 1)
	go func() { res <- b.Lead(task, func(context.Context) error { ran.Store(true); return nil })(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("cancelled wait returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Lead still waiting 2s after its ctx was cancelled")
	}
	if ran.Load() {
		t.Fatal("task ran without the lock")
	}
}

// BUG-52: a task waiting on a held lock must not block another task of the same Leader.
func TestLeader_WaitingTaskDoesNotBlockAnotherTask(t *testing.T) {
	dsn := requireHarness(t).appURL
	held, free := "test-"+uuid.NewString(), "test-"+uuid.NewString()
	a, b := db.NewLeader(dsn), db.NewLeader(dsn)
	t.Cleanup(func() { ldStop(t, a); ldStop(t, b) })
	ldHold(t, a, held)

	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan struct{})
	go func() {
		defer close(waiting)
		_ = b.Lead(held, func(context.Context) error { return nil })(ctx)
	}()
	t.Cleanup(func() { cancel(); <-waiting })
	time.Sleep(500 * time.Millisecond)

	fn, runs := ldRuns()
	start := time.Now()
	if err := b.Lead(free, fn)(context.Background()); err != nil || *runs != 1 {
		t.Fatalf("b on a free task: runs=%d err=%v, want 1 run", *runs, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a free task waited %s behind a task that waits on a held lock", d)
	}
}

// BUG-52: a connect failure is returned at once, not retried.
func TestLeader_ClaimErrorIsReturnedNotRetried(t *testing.T) {
	l := db.NewLeader("postgres://nobody:x@127.0.0.1:1/none?sslmode=disable")
	t.Cleanup(func() { ldStop(t, l) })
	fn, runs := ldRuns()
	start := time.Now()
	err := l.Lead("test-"+uuid.NewString(), fn)(context.Background())
	if err == nil {
		t.Fatal("Lead on an unreachable database returned nil")
	}
	if *runs != 0 {
		t.Fatalf("task ran %d times without a lock", *runs)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("a claim error took %s to return: it was retried", d)
	}
}

// BUG-52: two waiters on one released lock run the task once between them.
func TestLeader_TwoWaitersRunTheTaskOnce(t *testing.T) {
	dsn := requireHarness(t).appURL
	task := "test-" + uuid.NewString()
	a, b, c := db.NewLeader(dsn), db.NewLeader(dsn), db.NewLeader(dsn)
	t.Cleanup(func() { ldStop(t, a); ldStop(t, b); ldStop(t, c) })
	ldHold(t, a, task)

	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	fn := func(context.Context) error { runs.Add(1); return nil }
	var wg sync.WaitGroup
	for _, l := range []*db.Leader{b, c} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = l.Lead(task, fn)(ctx) }()
	}
	t.Cleanup(func() { cancel(); wg.Wait() })

	time.Sleep(time.Second)
	if n := runs.Load(); n != 0 {
		t.Fatalf("%d waiters ran while a held the lock", n)
	}
	ldStop(t, a)
	deadline := time.Now().Add(15 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if runs.Load() == 0 {
		t.Fatal("no waiter took the lock within 15s of a stopping")
	}
	time.Sleep(6 * time.Second) // longer than claimRetry
	if n := runs.Load(); n != 1 {
		t.Fatalf("task ran %d times across two waiters, want 1", n)
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

// BUG-67: a claim whose ctx is done must not close the shared connection and drop the other locks.
func TestLeader_CancelledClaimKeepsOtherLocks(t *testing.T) {
	// peerTakes reports whether a second Leader runs task, which only happens if the holder lost it.
	peerTakes := func(t *testing.T, dsn, task string) bool {
		t.Helper()
		peer := db.NewLeader(dsn)
		t.Cleanup(func() { ldStop(t, peer) })
		fn, runs := ldRuns()
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := peer.Lead(task, fn)(ctx); err != nil {
			t.Fatalf("peer: %v", err)
		}
		return *runs > 0
	}
	setup := func(t *testing.T) (a *db.Leader, dsn, held, other string) {
		dsn = requireHarness(t).appURL
		held, other = "test-"+uuid.NewString(), "test-"+uuid.NewString()
		a = db.NewLeader(dsn)
		t.Cleanup(func() { ldStop(t, a) })
		ldHold(t, a, held)
		return
	}

	t.Run("ctx already cancelled", func(t *testing.T) {
		a, dsn, held, other := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fn, runs := ldRuns()
		err := a.Lead(other, fn)(ctx)
		if !errors.Is(err, context.Canceled) || *runs != 0 {
			t.Errorf("cancelled claim: err=%v runs=%d, want the ctx error and no run", err, *runs)
		}
		if peerTakes(t, dsn, held) {
			t.Fatal("a peer took the lock a still holds: a cancelled claim closed the leader connection")
		}
	})

	t.Run("ctx cancelled while the claim waits on the mutex", func(t *testing.T) {
		a, dsn, held, other := setup(t)
		release := db.ExportLeaderHoldMu(a)
		ctx, cancel := context.WithCancel(context.Background())
		fn, runs := ldRuns()
		res := make(chan error, 1)
		go func() { res <- a.Lead(other, fn)(ctx) }()
		time.Sleep(200 * time.Millisecond)
		cancel()
		release()
		var err error
		select {
		case err = <-res:
		case <-time.After(5 * time.Second):
			t.Fatal("Lead did not return after its ctx was cancelled")
		}
		if !errors.Is(err, context.Canceled) || *runs != 0 {
			t.Errorf("cancelled claim: err=%v runs=%d, want the ctx error and no run", err, *runs)
		}
		if peerTakes(t, dsn, held) {
			t.Fatal("a peer took the lock a still holds: a cancelled claim closed the leader connection")
		}
	})
}

// BUG-67 AC 2: a dead connection with a live ctx is replaced and every task re-claimed.
func TestLeader_DeadConnectionIsReplacedAndLocksReclaimed(t *testing.T) {
	dsn := requireHarness(t).appURL
	held, other := "test-"+uuid.NewString(), "test-"+uuid.NewString()
	a, peer := db.NewLeader(dsn), db.NewLeader(dsn)
	t.Cleanup(func() { ldStop(t, a); ldStop(t, peer) })
	ldHold(t, a, held)
	if ok, err := db.ExportLeaderTerminate(context.Background(), a); err != nil || !ok {
		t.Fatalf("terminate: ok=%v err=%v", ok, err)
	}
	fnP, runsP := ldRuns()
	if err := peer.Lead(held, fnP)(context.Background()); err != nil || *runsP != 1 {
		t.Fatalf("peer after a's connection died: runs=%d err=%v, want 1", *runsP, err)
	}
	skip, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	fnA, runsA := ldRuns()
	if err := a.Lead(held, fnA)(skip); err != nil || *runsA != 0 {
		t.Fatalf("a on the lost task: runs=%d err=%v, want 0: held must be cleared", *runsA, err)
	}
	if err := a.Lead(other, fnA)(context.Background()); err != nil || *runsA != 1 {
		t.Fatalf("a on a free task: runs=%d err=%v, want 1 on the new connection", *runsA, err)
	}
}

// BUG-67 AC 4: claims cancelled at random points, mid-Ping or mid-query, keep the held lock.
func TestLeader_ClaimsCancelledMidFlightKeepTheHeldLock(t *testing.T) {
	dsn := requireHarness(t).appURL
	held := "test-" + uuid.NewString()
	a, peer := db.NewLeader(dsn), db.NewLeader(dsn)
	t.Cleanup(func() { ldStop(t, a); ldStop(t, peer) })
	ldHold(t, a, held)
	var wg sync.WaitGroup
	for i := range 300 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%30)*100*time.Microsecond)
			defer cancel()
			fn, _ := ldRuns()
			_ = a.Lead("test-"+uuid.NewString(), fn)(ctx)
		}()
	}
	wg.Wait()
	fn, runs := ldRuns()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := peer.Lead(held, fn)(ctx); err != nil || *runs != 0 {
		t.Fatalf("peer: runs=%d err=%v, want 0: a still holds the lock", *runs, err)
	}
}
