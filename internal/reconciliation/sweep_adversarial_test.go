// QA Mode B adversarial coverage for M5-06-05/06 (task-247/248), added on top of the
// RED-authored AC tests in sweep_test.go / sweeper_test.go. These target properties the
// architect's Test Specs table names but does not fully exercise: that a single poisoned
// tenant's per-tenant rollback (sweep.go's `if err != nil { log }` continue-on-error path,
// SweepOnce comment lines 158-162) can never starve every OTHER tenant in the same sweep —
// TestRLS_SweepReArmFailureRollsBack only ever proves rollback for ONE tenant in isolation,
// never alongside a healthy sibling in the SAME SweepOnce call — plus Sweeper lifecycle edge
// cases (Stop-before-Start, double-Stop, Start's promptness, extended post-Stop quiescence)
// and ConfigFromEnv's malformed-value handling across all four env vars (sweeper_test.go's
// own TestConfigFromEnvMalformed only exercises RECONCILE_INTERVAL).
package reconciliation

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// CRITICAL: a poisoned tenant must not starve its siblings in the SAME sweep. Two tenants
// each carry one lost_poll; afterHeal returns a sentinel error for tenant A ONLY (invoked
// AFTER a genuine ReArmPoll/recordAutoFixAudit already succeeded, exactly like
// TestRLS_SweepReArmFailureRollsBack), tenant B's afterHeal call returns nil. A single
// SweepOnce call must roll back A's tenant tx (0 river_job, 0 auto_fixed) while B's tenant
// tx commits in FULL (1 river_job, 1 auto_fixed) — proving the per-tenant `continue on
// error` loop in SweepOnce (sweep.go) truly isolates one bad tenant from the rest, not just
// that a lone poisoned tenant rolls back with nothing else in the sweep to starve.
func TestRLS_SweepPoisonTenantDoesNotStarveOthers(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()
	overdue := time.Now().Add(-1 * time.Hour)

	tenantA, _, invoiceA, cleanupA := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupA()
	jobA, cleanupJobA := rcSeedJob(t, h, tenantA, invoiceA, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJobA()
	defer rcCleanupPollJobsFor(h, jobA)
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantA)
	}()

	tenantB, _, invoiceB, cleanupB := rcSeedInvoice(t, h, rcInvoiceOpts{status: "submitted"})
	defer cleanupB()
	jobB, cleanupJobB := rcSeedJob(t, h, tenantB, invoiceB, rcJobOpts{state: "pending", attempts: 2, nextPollAt: &overdue})
	defer cleanupJobB()
	defer rcCleanupPollJobsFor(h, jobB)
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM audit_log WHERE tenant_id = $1`, tenantB)
	}()

	errSentinel := errors.New("reconciliation: poison-tenant probe")
	r := rcReconciler(h)
	r.afterHeal = func(tenantID string) error {
		if tenantID == tenantA {
			return errSentinel
		}
		return nil
	}

	if err := r.SweepOnce(ctx); err != nil {
		t.Logf("SweepOnce returned: %v (a per-tenant failure need not fail the whole sweep)", err)
	}

	// Tenant A: poisoned — must roll back completely.
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobA); n != 0 {
		t.Errorf("tenant A (poisoned) submission_poll river_job rows = %d, want 0 (rolled back)", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantA); n != 0 {
		t.Errorf("tenant A (poisoned) reconciliation.auto_fixed audit rows = %d, want 0 (rolled back)", n)
	}

	// Tenant B: healthy sibling in the SAME sweep — must commit in full, proving A's
	// rollback did not abort or otherwise corrupt B's own tenant tx.
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM river_job WHERE kind = 'submission_poll' AND args @> jsonb_build_object('submission_job_id', $1::text)`,
		jobB); n != 1 {
		t.Errorf("tenant B (healthy sibling) submission_poll river_job rows = %d, want exactly 1 — "+
			"a poisoned tenant A must not starve tenant B's own heal in the same SweepOnce call", n)
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'reconciliation.auto_fixed'`, tenantB); n != 1 {
		t.Errorf("tenant B (healthy sibling) reconciliation.auto_fixed audit rows = %d, want exactly 1", n)
	}
}

// Stop called with no prior Start (s.cancel/s.done both nil) must be a safe no-op — not a
// nil-pointer panic, not a deadlock waiting on a channel that was never created. Sweeper.Stop
// already special-cases this (sweeper.go:72-74); this proves the contract, not just reads it.
func TestSweeperStopBeforeStartIsNoop(t *testing.T) {
	s := &Sweeper{Interval: time.Hour, sweepFn: func(context.Context) error { return nil }}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Errorf("Stop before Start = %v, want nil (nothing to drain)", err)
	}
}

// A second Stop call, after the first has already returned, must also be a safe no-op —
// re-cancelling an already-cancelled context and re-selecting on an already-closed s.done
// channel must neither panic nor block.
func TestSweeperDoubleStopIsNoop(t *testing.T) {
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
			t.Fatalf("sweepFn was never called within 500ms — Start must be driving the ticker " +
				"before double-Stop's safety can be proven")
		}
		time.Sleep(2 * time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop (1st): %v", err)
	}

	// The 2nd Stop must return promptly (not block on a closed channel) and without panic —
	// a bounded timeout ctx proves "promptly" rather than merely "eventually".
	stopCtx2, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel2()
	if err := s.Stop(stopCtx2); err != nil {
		t.Errorf("Stop (2nd, after the 1st already returned) = %v, want nil", err)
	}
}

// Start must return promptly regardless of Interval — it must never block the caller for a
// whole tick period (or longer) waiting on the ticker itself. A 1-hour Interval makes any
// accidental synchronous first-tick wait obvious: Start returning in under 100ms proves it
// launches the ticker loop in a goroutine rather than blocking inline.
func TestSweeperStartReturnsPromptly(t *testing.T) {
	s := &Sweeper{Interval: time.Hour, sweepFn: func(context.Context) error { return nil }}

	started := time.Now()
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Errorf("Start took %v to return at a 1h interval, want << the interval (it must launch "+
			"the ticker loop asynchronously, never block the caller for a whole tick period)", elapsed)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// Extends TestSweeperStopHalts's 30ms post-Stop quiescence window to a much longer one (~40
// tick periods at a 5ms interval) so a race that only manifests over a longer window — e.g. a
// ticker goroutine that keeps running one extra iteration past cancellation under load — has
// many more chances to show up than the RED suite's single short sleep allows. -race-clean by
// construction (atomic counter, no shared mutable state read without synchronization).
func TestSweeperPostStopExtendedQuiescence(t *testing.T) {
	var calls atomic.Int64
	s := &Sweeper{
		Interval: 5 * time.Millisecond,
		sweepFn: func(context.Context) error {
			calls.Add(1)
			return nil
		},
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Positive control: let several ticks land before stopping.
	deadline := time.Now().Add(500 * time.Millisecond)
	for calls.Load() < 5 {
		if time.Now().After(deadline) {
			t.Fatalf("sweepFn call count = %d after 500ms at a 5ms tick interval, want >= 5 before "+
				"the quiescence half can mean anything", calls.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	frozen := calls.Load()
	time.Sleep(200 * time.Millisecond) // ~40 tick periods worth, once Stop has returned
	if got := calls.Load(); got != frozen {
		t.Errorf("sweepFn call count grew from %d to %d in the 200ms after Stop returned "+
			"(~40 tick periods at this Interval), want frozen — no further ticks may start a new "+
			"sweepFn call once Stop's contract says the loop has halted", frozen, got)
	}
}

// AC-3: every one of the four RECONCILE_* vars is independently parsed+validated, not just
// RECONCILE_INTERVAL (the only one sweeper_test.go's own TestConfigFromEnvMalformed
// exercises) — a malformed value on ANY of the four must be a non-nil error and the zero
// Config, never a silently-applied default.
func TestConfigFromEnvMalformedAllFourVars(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"interval", envReconcileInterval},
		{"poll overdue grace", envPollOverdueGrace},
		{"hop ceiling", envHopCeiling},
		{"max pending age", envMaxPendingAge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, "not-a-valid-value")

			cfg, err := ConfigFromEnv()
			if err == nil {
				t.Fatalf("ConfigFromEnv() with %s=%q = %+v, <nil> error — want a non-nil error, "+
					"not a silently-applied default", tc.key, "not-a-valid-value", cfg)
			}
			if cfg != (Config{}) {
				t.Errorf("ConfigFromEnv() error path (bad %s) returned %+v, want the zero Config "+
					"— never a partially-populated one alongside the error", tc.key, cfg)
			}
		})
	}
}

// TestConfigFromEnvDefaults (sweeper_test.go) builds its `want` literal from the SAME
// defaultReconcileInterval/defaultPollOverdueGrace/defaultHopCeiling/defaultMaxPendingAge
// constants ConfigFromEnv itself reads — so a typo'd constant (e.g. 5m silently changed to
// 7m) would move both sides of that comparison together and the RED suite's own defaults
// test would never catch it (confirmed: mutating defaultReconcileInterval alone left
// TestConfigFromEnvDefaults GREEN during this QA pass's meaningfulness probe). This test
// pins the documented defaults (M5-06 story, [M5-06-06] AC-3: interval 5m, grace 15m,
// ceiling 20, maxAge 24h) as LITERAL values, independent of the package's own constants, so
// a future constant-value regression is actually caught.
func TestConfigFromEnvDefaultsLiteralValues(t *testing.T) {
	for _, key := range []string{envReconcileInterval, envPollOverdueGrace, envHopCeiling, envMaxPendingAge} {
		t.Setenv(key, "sentinel")
		os.Unsetenv(key)
	}

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() with every RECONCILE_* unset returned unexpected error: %v", err)
	}
	want := Config{
		Interval:         5 * time.Minute,
		PollOverdueGrace: 15 * time.Minute,
		MaxPendingAge:    24 * time.Hour,
		HopCeiling:       20,
	}
	if cfg != want {
		t.Errorf("ConfigFromEnv() with every RECONCILE_* unset = %+v, want the LITERAL documented "+
			"defaults %+v (M5-06 story [M5-06-06] AC-3) — pinned independently of this package's "+
			"own default* constants", cfg, want)
	}
}
