package platform

import "context"

// BackgroundWorker is a long-running process component — e.g. a River worker pool —
// that runs for the life of the service alongside the HTTP server. App.Run starts
// each registered worker before it begins serving and, on SIGINT/SIGTERM, drains it
// via Stop within the same ShutdownTimeout window srv.Shutdown uses.
//
// The contract matches River's *river.Client exactly (Start/Stop with these
// signatures), so a River client — or the queue.Client wrapper around it — registers
// with no adapter. This is the reusable lifecycle seam the M3 submission worker rides
// on (docs/migrations.md §8): it keeps the signal handling and graceful-drain logic in
// the platform kit instead of a bespoke per-service loop.
type BackgroundWorker interface {
	// Start launches the worker and returns promptly; the worker runs until Stop. It
	// is handed the process-lifetime context (NOT the signal context), so a shutdown
	// signal does not abruptly cancel in-flight work — draining is explicit, via Stop.
	Start(ctx context.Context) error
	// Stop drains the worker, blocking until in-flight work finishes or ctx (the
	// shutdown-window deadline) expires.
	Stop(ctx context.Context) error
}

// AddBackgroundWorker registers a worker to run alongside the HTTP server for the life
// of the process. Call it before Run; registration order is start order.
func (a *App) AddBackgroundWorker(w BackgroundWorker) {
	a.bgWorkers = append(a.bgWorkers, w)
}
