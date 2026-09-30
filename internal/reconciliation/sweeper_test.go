// M5-06-06 (task-248): the Sweeper ticker BackgroundWorker + ConfigFromEnv. Pure suite —
// no DATABASE_* env, no requireHarness — a fake sweepFn/clock stands in for a real
// Reconciler.SweepOnce, so these run unconditionally under a bare `go test ./...`.
//
// RED against the sweep.go/sweeper.go stubs: Sweeper.Start/Stop are no-ops (sweepFn is
// never invoked), so every TestSweeper* case's "wait for at least one call" positive
// control times out and t.Fatalf's — never a compile or setup error. ConfigFromEnv always
// returns the zero Config + nil error, so TestConfigFromEnvDefaults mismatches the
// documented defaults and TestConfigFromEnvMalformed's "want a non-nil error" fails.
//
// Every TestSweeper* case pairs its "no further/no concurrent calls" negative assertion
// with a "the ticker DID call sweepFn at least once/enough times" positive control — a
// stub that never ticks at all would otherwise make the negative half pass vacuously.
//
// Spec-to-test map (M5-06 story, [M5-06-06] Test Specs table):
//
//	AC-1 TestSweeperTicksInvokeSweep
//	AC-1 TestSweeperStopHalts
//	AC-2 TestSweeperSingleFlight
//	AC-3 TestConfigFromEnvDefaults
//	AC-3 TestConfigFromEnvMalformed
package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/SimonOsipov/invoice-os/internal/platform/sentrytest"
)

// AC-1: Start drives sweepFn once per Interval; at a 10ms interval, waiting ~50ms must
// observe at least 3 calls.
func TestSweeperTicksInvokeSweep(t *testing.T) {
	var calls atomic.Int64
	s := &Sweeper{
		Interval: 10 * time.Millisecond,
		sweepFn: func(context.Context) error {
			calls.Add(1)
			return nil
		},
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for calls.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("sweepFn call count = %d after 500ms at a 10ms tick interval, want >= 3 — "+
				"Start must drive sweepFn on a ticker", calls.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	afterStop := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != afterStop {
		t.Errorf("sweepFn call count grew from %d to %d after Stop returned, want no further calls", afterStop, got)
	}
}

// AC-1: once Stop returns, no further tick may start a new sweepFn call. Paired with a
// positive control (wait for >= 1 call before Stop) so a stub that never ticks at all
// cannot make "frozen at 0" pass vacuously.
func TestSweeperStopHalts(t *testing.T) {
	var calls atomic.Int64
	s := &Sweeper{
		Interval: 10 * time.Millisecond,
		sweepFn: func(context.Context) error {
			calls.Add(1)
			return nil
		},
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for calls.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("sweepFn was never called within 500ms at a 10ms tick interval — Start must be " +
				"driving the ticker before Stop's halt behaviour can be proven")
		}
		time.Sleep(2 * time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	frozen := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != frozen {
		t.Errorf("sweepFn call count grew from %d to %d in the 30ms after Stop returned, want frozen "+
			"(no further ticks once Stop halts the loop)", frozen, got)
	}
}

// AC-2: a slow sweepFn (40ms) at a fast tick interval (10ms) must never run two
// executions concurrently — single-flight. Paired with a positive control (wait for a full
// execution to complete) so a stub that never ticks at all cannot make "max concurrency
// observed = 0" pass vacuously.
func TestSweeperSingleFlight(t *testing.T) {
	var (
		inFlight  atomic.Int64
		maxSeen   atomic.Int64
		totalRuns atomic.Int64
	)
	s := &Sweeper{
		Interval: 10 * time.Millisecond,
		sweepFn: func(context.Context) error {
			n := inFlight.Add(1)
			for {
				old := maxSeen.Load()
				if n <= old || maxSeen.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			inFlight.Add(-1)
			totalRuns.Add(1)
			return nil
		},
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for totalRuns.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("sweepFn never completed a single execution within 1s — Start must drive the " +
				"ticker before single-flight can be proven")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Let a couple more tick windows pass while the first (slow) execution is still
	// in flight — this is the window a non-single-flight implementation would overlap.
	time.Sleep(60 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Stop(stopCtx)

	if got := maxSeen.Load(); got > 1 {
		t.Errorf("max concurrent sweepFn executions observed = %d, want 1 (single-flight: a slow "+
			"sweep must never be overlapped by the next tick)", got)
	}
}

// AC-3: every RECONCILE_* var unset -> the documented defaults (interval 5m, grace 15m,
// ceiling 20, maxAge 24h).
func TestConfigFromEnvDefaults(t *testing.T) {
	for _, key := range []string{envReconcileInterval, envPollOverdueGrace, envHopCeiling, envMaxPendingAge} {
		// t.Setenv FIRST so its cleanup restores whatever the runner had, THEN Unsetenv --
		// mirrors submission.TestMockConfigFromEnv's "unset" subtest
		// (internal/submission/mock_adapter_test.go:3552-3562).
		t.Setenv(key, "sentinel")
		os.Unsetenv(key)
	}

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() with every RECONCILE_* unset returned unexpected error: %v", err)
	}
	want := Config{
		Interval:         defaultReconcileInterval,
		PollOverdueGrace: defaultPollOverdueGrace,
		MaxPendingAge:    defaultMaxPendingAge,
		HopCeiling:       defaultHopCeiling,
	}
	if cfg != want {
		t.Errorf("ConfigFromEnv() with every RECONCILE_* unset = %+v, want the documented defaults %+v "+
			"(interval 5m, grace 15m, ceiling 20, maxAge 24h)", cfg, want)
	}
}

// AC-3: a malformed duration is a non-nil error, never a silently-defaulted or
// partially-populated Config.
func TestConfigFromEnvMalformed(t *testing.T) {
	t.Setenv(envReconcileInterval, "nope")

	cfg, err := ConfigFromEnv()
	if err == nil {
		t.Fatalf("ConfigFromEnv() with %s=%q = %+v, <nil> error — want a non-nil error on a malformed duration",
			envReconcileInterval, "nope", cfg)
	}
	if cfg != (Config{}) {
		t.Errorf("ConfigFromEnv() error path returned %+v, want the zero Config", cfg)
	}
}

// scriptedSweeper runs one script step per tick, then parks the next tick until its
// context ends. parked closes once the parking tick starts, so every scripted step,
// including its capture, has finished by then.
func scriptedSweeper(script ...func() error) (*Sweeper, <-chan struct{}) {
	var n atomic.Int64
	parked := make(chan struct{})
	return &Sweeper{
		Interval: 5 * time.Millisecond,
		sweepFn: func(ctx context.Context) error {
			i := int(n.Add(1)) - 1
			if i < len(script) {
				return script[i]()
			}
			if i == len(script) {
				close(parked)
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}, parked
}

func failWith(msg string) func() error { return func() error { return errors.New(msg) } }

func succeed() error { return nil }

func startAndWaitParked(t *testing.T, s *Sweeper, parked <-chan struct{}) {
	t.Helper()
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
	select {
	case <-parked:
	case <-time.After(2 * time.Second):
		t.Fatal("the scripted ticks never finished within 2s")
	}
}

func stopSweeper(t *testing.T, s *Sweeper) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
}

func eventText(e *sentry.Event) string {
	text := e.Message
	for _, ex := range e.Exception {
		text += " " + ex.Value
	}
	return text
}

func assertEventCount(t *testing.T, rec *sentrytest.Recorder, want sentrytest.Labels, n int) {
	t.Helper()
	events := rec.Events()
	if len(events) != n {
		t.Fatalf("recorded %d events, want %d", len(events), n)
	}
	for _, e := range events {
		sentrytest.AssertLabels(t, e, want)
	}
}

func TestSweeper_FailedSweepOpensOneIssue(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	s, parked := scriptedSweeper(failWith("enumerate: db down"))
	startAndWaitParked(t, s, parked)

	e := rec.One(t, want)
	if got := eventText(e); !strings.Contains(got, "db down") {
		t.Errorf("event text = %q, want it to carry the sweep error %q", got, "enumerate: db down")
	}
}

func TestSweeper_RepeatedFailureOpensOneIssue(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	s, parked := scriptedSweeper(failWith("db down"), failWith("db down"), failWith("db down"))
	startAndWaitParked(t, s, parked)

	rec.One(t, want)
}

func TestSweeper_FailureAfterRecoveryOpensAnother(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	s, parked := scriptedSweeper(failWith("first"), succeed, failWith("second"))
	startAndWaitParked(t, s, parked)

	assertEventCount(t, rec, want, 2)
}

func TestSweeper_PanickingSweepIsRecoveredAndReported(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	var tick2 atomic.Bool
	s, parked := scriptedSweeper(
		func() error { panic("sweep boom") },
		func() error { tick2.Store(true); return nil },
	)
	startAndWaitParked(t, s, parked)

	e := rec.One(t, want)
	if e.Level != sentry.LevelFatal {
		t.Errorf("event level = %q, want %q", e.Level, sentry.LevelFatal)
	}
	if !tick2.Load() {
		t.Error("tick 2 never ran after the panic")
	}
	stopSweeper(t, s)
}

// Guard: nothing is bound to capture the panic; the loop must still tick.
func TestSweeper_PanicWithSentryOffKeepsTicking(t *testing.T) {
	sentry.CurrentHub().BindClient(nil)
	var tick2 atomic.Bool
	s, parked := scriptedSweeper(
		func() error { panic("sweep boom") },
		func() error { tick2.Store(true); return nil },
	)
	startAndWaitParked(t, s, parked)

	if !tick2.Load() {
		t.Error("tick 2 never ran after the panic")
	}
	stopSweeper(t, s)
}

// Guard for the cancel half: the parked third tick returns ctx.Err() after Stop.
func TestSweeper_ShutdownCancelOpensNothing(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	s, parked := scriptedSweeper(failWith("db down"), succeed)
	startAndWaitParked(t, s, parked)

	assertEventCount(t, rec, want, 1)
	stopSweeper(t, s)
	assertEventCount(t, rec, want, 1)
}

// Guard for the success half: tick 3 is held open so the recorder is read after ticks 1 and 2 only.
func TestSweeper_SuccessOpensNothing(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	entered, release := make(chan struct{}), make(chan struct{})
	s, parked := scriptedSweeper(succeed, succeed, func() error {
		close(entered)
		<-release
		return errors.New("late failure")
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("tick 3 never started within 2s")
	}
	rec.None(t)

	close(release)
	select {
	case <-parked:
	case <-time.After(2 * time.Second):
		t.Fatal("tick 4 never started within 2s")
	}
	rec.One(t, want)
}

// A panic and an error belong to one run of failures; a success ends the run.
func TestSweeper_PanicAndErrorShareOneRunOfFailures(t *testing.T) {
	boom := func() error { panic("sweep boom") }
	cases := []struct {
		name   string
		script []func() error
		want   int
	}{
		{"panic then error", []func() error{boom, failWith("later")}, 1},
		{"error then panic", []func() error{failWith("first"), boom}, 1},
		{"panic then panic", []func() error{boom, boom}, 1},
		{"panic, success, error", []func() error{boom, succeed, failWith("later")}, 2},
		{"error, success, panic", []func() error{failWith("first"), succeed, boom}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, rec, want := sentrytest.Boot(t, "reconciliation")
			s, parked := scriptedSweeper(c.script...)
			startAndWaitParked(t, s, parked)

			assertEventCount(t, rec, want, c.want)
			stopSweeper(t, s)
		})
	}
}

// A crash is a crash even when shutdown has begun; only errors are suppressed on cancel.
func TestSweeper_PanicDuringShutdownIsStillReported(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	entered := make(chan struct{})
	s := &Sweeper{
		Interval: 5 * time.Millisecond,
		sweepFn: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			panic("boom while stopping")
		},
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep never started within 2s")
	}
	stopSweeper(t, s)

	e := rec.One(t, want)
	if e.Level != sentry.LevelFatal {
		t.Errorf("event level = %q, want %q", e.Level, sentry.LevelFatal)
	}
}

// Only shutdown suppresses an error: a context error while the run is live still counts.
func TestSweeper_ContextErrorWhileRunningCounts(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	s, parked := scriptedSweeper(
		func() error { return fmt.Errorf("sweep: tenant query: %w", context.DeadlineExceeded) },
	)
	startAndWaitParked(t, s, parked)

	e := rec.One(t, want)
	if got := eventText(e); !strings.Contains(got, "deadline exceeded") {
		t.Errorf("event text = %q, want it to carry the context error", got)
	}
}

// Truth table of one sweep: the returned flag and the events sent, for every input.
func TestSweeper_RunSweepTransitionTable(t *testing.T) {
	boom := func(context.Context) error { panic("sweep boom") }
	fail := func(context.Context) error { return errors.New("db down") }
	ok := func(context.Context) error { return nil }
	cases := []struct {
		name       string
		fn         func(context.Context) error
		cancelled  bool
		failing    bool
		wantNext   bool
		wantEvents int
	}{
		{"success re-arms", ok, false, true, false, 0},
		{"success stays armed", ok, false, false, false, 0},
		{"first error reports", fail, false, false, true, 1},
		{"repeated error is silent", fail, false, true, true, 0},
		{"first panic reports", boom, false, false, true, 1},
		{"repeated panic is silent", boom, false, true, true, 0},
		{"error on shutdown leaves an armed flag armed", fail, true, false, false, 0},
		{"error on shutdown leaves a failing flag failing", fail, true, true, true, 0},
		{"success on shutdown re-arms", ok, true, true, false, 0},
		{"first panic on shutdown reports", boom, true, false, true, 1},
		{"repeated panic on shutdown is silent", boom, true, true, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, rec, want := sentrytest.Boot(t, "reconciliation")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.cancelled {
				cancel()
			}
			s := &Sweeper{sweepFn: c.fn}

			if got := s.runSweep(ctx, c.failing); got != c.wantNext {
				t.Errorf("runSweep returned %v, want %v", got, c.wantNext)
			}
			assertEventCount(t, rec, want, c.wantEvents)
		})
	}
}

// The joined SweepOnce error is the event text: tenant ids stay, quoted values and upstream reasons go.
func TestSweeper_EventNamesTenantsWithoutCustomerText(t *testing.T) {
	_, rec, want := sentrytest.Boot(t, "reconciliation")
	const tenantA, tenantB = "0b6a7d0e-1111-4c1e-9a5a-3f1e2d4c5b6a", "7c1f2e3d-2222-4d2f-8b6b-4a2f3e5d6c7b"
	joined := errors.Join(
		fmt.Errorf("reconciliation: tenant %s: %w", tenantA,
			fmt.Errorf("audit: record event %q: invoice number %q rejected", "reconciliation.auto_fixed", "INV-2026-SECRET")),
		fmt.Errorf("reconciliation: tenant %s: %w", tenantB,
			errors.New("submit returned 422: buyer TIN 12345678-0001 unknown")),
	)
	s, parked := scriptedSweeper(func() error { return joined })
	startAndWaitParked(t, s, parked)

	got := eventText(rec.One(t, want))
	for _, id := range []string{tenantA, tenantB} {
		if !strings.Contains(got, id) {
			t.Errorf("event text %q does not name tenant %s", got, id)
		}
	}
	for _, leak := range []string{"INV-2026-SECRET", "12345678-0001"} {
		if strings.Contains(got, leak) {
			t.Errorf("event text %q leaks %q", got, leak)
		}
	}
}
