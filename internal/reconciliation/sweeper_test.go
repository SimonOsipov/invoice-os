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
	"os"
	"sync/atomic"
	"testing"
	"time"
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
