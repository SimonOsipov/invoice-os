package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// errShutdown marks a Run error from a graceful shutdown that overran its window.
var errShutdown = errors.New("platform: graceful shutdown")

// App is a service built on the platform kit: configuration, a logger, and a
// mux for routes, with the standard middleware chain, health endpoints, and
// Sentry wiring applied for free. A consumer registers routes on Mux and calls
// Run:
//
//	app, err := platform.New("tenancy")
//	if err != nil {
//		platform.Fatal(slog.Default(), "tenancy: %v", err)
//	}
//	app.Mux.HandleFunc("GET /v1/ping", pingHandler)
//	if err := app.Run(context.Background()); err != nil {
//		platform.Fatal(app.Logger, "tenancy: %v", err)
//	}
type App struct {
	Config Config
	Logger *slog.Logger
	Mux    *http.ServeMux

	gatewayToken string
	openPatterns []string
	readiness    readiness
	bgWorkers    []BackgroundWorker
}

// New builds an App for the named service: it loads config from the
// environment, initializes Sentry and the logger, and registers /healthz and
// /readyz. Register routes on App.Mux, then call Run.
func New(service string) (*App, error) {
	cfg, err := LoadConfig(service)
	if err != nil {
		return nil, err
	}
	if err := initSentry(cfg); err != nil {
		return nil, err
	}
	if cfg.SentryTestEvent {
		captureTestEvent(cfg.Service)
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	app := &App{
		Config: cfg,
		Logger: logger,
		Mux:    http.NewServeMux(),
	}
	app.Mux.HandleFunc("GET /healthz", healthzHandler)
	app.Mux.HandleFunc("GET /readyz", app.readiness.readyzHandler)
	return app, nil
}

// Ready registers a readiness check surfaced by /readyz (e.g. a database ping).
func (a *App) Ready(name string, check ReadyCheck) {
	a.readiness.add(name, check)
}

// Handler wraps the mux with the standard middleware chain (outermost first):
// request-id, tenant-id and identity run before tracing and recovery so a recovered
// panic is logged and reported with the request and tenant ids, and so tenant-scoped
// handlers see the verified caller the gateway injected. Tracing runs before recovery
// so a recovered 500 reaches the transaction status. The gateway guard (RequireGateway) runs right after request-id. staffMiddleware (the /v1/staff rules-role check) runs inside recovery and the request log. serverError is innermost so it
// reads the mux's matched pattern.
func (a *App) Handler() http.Handler {
	return chain(a.Mux,
		requestIDMiddleware,
		a.gatewayGuard,
		tenantIDMiddleware,
		identityMiddleware,
		tracingMiddleware(a.Mux),
		recoveryMiddleware(a.Logger),
		requestLogMiddleware(a.Logger),
		a.staffMiddleware,
		serverErrorMiddleware,
	)
}

// Run serves HTTP until the process receives SIGINT/SIGTERM (or ctx is
// cancelled), then shuts down gracefully within ShutdownTimeout and flushes
// Sentry. Any registered background workers (AddBackgroundWorker) start alongside
// the server and drain within the same shutdown window.
func (a *App) Run(ctx context.Context) error {
	// Keep the process-lifetime context (runCtx) distinct from the signal context:
	// background workers start on runCtx, so a shutdown signal does NOT hard-cancel
	// their in-flight jobs — those are drained explicitly (Stop) within the window.
	runCtx := ctx
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start background workers (e.g. the River pool) before serving, so /healthz and
	// the workers come up together. Track the ones already up so any error path can drain
	// them: a worker that fails to start must not leave its predecessors running past Run.
	started := make([]BackgroundWorker, 0, len(a.bgWorkers))
	for _, w := range a.bgWorkers {
		if err := w.Start(runCtx); err != nil {
			a.drainWorkers(started)
			return fmt.Errorf("platform: start background worker: %w", err)
		}
		started = append(started, w)
	}

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(a.Config.Port),
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.Logger.Info("http server listening", slog.Int("port", a.Config.Port))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		// The listener died; drain the workers we started before bailing out.
		a.drainWorkers(started)
		return fmt.Errorf("platform: server error: %w", err)
	case <-sigCtx.Done():
		a.Logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.Config.ShutdownTimeout)
	defer cancel()

	// Drain the HTTP server and every background worker within the same window.
	// Errors are joined so one worker's failure can't mask the server's or another's.
	shutdownErr := srv.Shutdown(shutdownCtx)
	for _, w := range started {
		if err := w.Stop(shutdownCtx); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	flushSentry(2 * time.Second)
	if shutdownErr != nil {
		return fmt.Errorf("%w: %w", errShutdown, shutdownErr)
	}
	a.Logger.Info("shutdown complete")
	return nil
}

// drainWorkers stops the given workers within a fresh ShutdownTimeout window. It is used on
// the error exit paths where Run bails out before its normal shutdown sequence, so an
// already-started worker never outlives the call. Stop errors are logged, not returned —
// Run is already returning the primary failure.
func (a *App) drainWorkers(workers []BackgroundWorker) {
	if len(workers) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.Config.ShutdownTimeout)
	defer cancel()
	for _, w := range workers {
		if err := w.Stop(ctx); err != nil {
			a.Logger.Error("draining background worker on error exit", slog.Any("err", err))
		}
	}
}
