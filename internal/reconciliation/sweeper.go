// M5-06-06: Sweeper is the platform.BackgroundWorker cmd/reconciliation registers via
// app.AddBackgroundWorker (M5-06-07) — it drives sweepFn on a time.Ticker, single-flight
// (a slow sweep is never overlapped by the next tick), and stops within the platform
// shutdown window (M5-06 System Design).
package reconciliation

import (
	"context"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// Sweeper implements platform.BackgroundWorker (Start/Stop). sweepFn and Interval are the
// test seams TestSweeper* use to run with no DB and a short tick period — production
// wiring (cmd/reconciliation, M5-06-07) points sweepFn at a built Reconciler's SweepOnce
// and Interval at Cfg.Interval.
type Sweeper struct {
	// Interval is the tick period between sweeps.
	Interval time.Duration

	// sweepFn is invoked once per tick. Unexported: only this package's own production
	// wiring and its in-package TestSweeper* set it directly — every test in this suite is
	// `package reconciliation`, not an external `_test` package (fixture_test.go:30).
	sweepFn func(context.Context) error
}

var _ platform.BackgroundWorker = (*Sweeper)(nil)

// Start launches the ticker loop and returns promptly (BackgroundWorker contract): every
// Interval, sweepFn runs; a sweep still in flight when the next tick fires is NOT started
// again (single-flight) — the following tick's sweep runs only once the current one
// finishes.
//
// TODO(M5-06-06): executor — time.Ticker(s.Interval) goroutine calling s.sweepFn,
// single-flight guard, stop the loop when Stop cancels it.
func (s *Sweeper) Start(ctx context.Context) error {
	return nil
}

// Stop halts the ticker loop: no further tick starts a new sweepFn call once Stop
// returns. Blocks until any in-flight sweep finishes or ctx (the shutdown-window
// deadline) expires (BackgroundWorker contract).
//
// TODO(M5-06-06): executor — cancel the ticker loop, wait for an in-flight sweepFn call
// to finish or ctx to expire.
func (s *Sweeper) Stop(ctx context.Context) error {
	return nil
}
