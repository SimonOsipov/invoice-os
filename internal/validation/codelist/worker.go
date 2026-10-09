package codelist

import (
	"context"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

// SyncInterval is the period between syncs; NRS enforces a change months after it publishes it.
const SyncInterval = 24 * time.Hour

// Worker runs fn at start and on every interval tick, one run at a time.
type Worker struct {
	interval time.Duration
	fn       func(context.Context) error

	// set by Start, read only by Stop (called after Start returns)
	cancel context.CancelFunc
	done   chan struct{}
}

var _ platform.BackgroundWorker = (*Worker)(nil)

func NewWorker(interval time.Duration, fn func(context.Context) error) *Worker {
	return &Worker{interval: interval, fn: fn}
}

// Start returns at once. Runs are serial in one goroutine, so a slow run coalesces the ticks it overlaps.
func (w *Worker) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})

	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			w.run(runCtx)
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

// run reports every failed run; a run ended by shutdown is not a failure.
func (w *Worker) run(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			platform.CapturePanic(ctx, rec)
		}
	}()
	if err := w.fn(ctx); err != nil && ctx.Err() == nil {
		platform.CaptureError(ctx, err)
	}
}

// Stop cancels the loop and waits for the in-flight run, or for ctx to expire.
func (w *Worker) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
