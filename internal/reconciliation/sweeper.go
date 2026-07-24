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

	// cancel stops the ticker loop's context; done closes once that loop has returned.
	// Both are set by Start and read only by Stop, which is only ever called after Start
	// has returned (the BackgroundWorker contract) — no mutex needed for that
	// happens-before ordering.
	cancel context.CancelFunc
	done   chan struct{}
}

var _ platform.BackgroundWorker = (*Sweeper)(nil)

// Start launches the ticker loop and returns promptly (BackgroundWorker contract): every
// Interval, sweepFn runs; a sweep still in flight when the next tick fires is NOT started
// again (single-flight) — the following tick's sweep runs only once the current one
// finishes.
//
// single-flight falls out of the loop shape itself rather than a separate busy flag: the
// ticker's channel is buffered at 1 and the runtime drops a tick that arrives while nothing
// is receiving, so a tick that fires while sweepFn is still running (synchronously, in this
// same goroutine) is simply coalesced away — the next tick after sweepFn returns is the
// earliest it can run again.
func (s *Sweeper) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})

	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_ = s.sweepFn(runCtx)
			}
		}
	}()
	return nil
}

// Stop halts the ticker loop: no further tick starts a new sweepFn call once Stop
// returns. Blocks until any in-flight sweep finishes or ctx (the shutdown-window
// deadline) expires (BackgroundWorker contract).
func (s *Sweeper) Stop(ctx context.Context) error {
	if s.cancel == nil || s.done == nil {
		return nil // Stop called without a prior Start — nothing to drain.
	}
	s.cancel()

	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
