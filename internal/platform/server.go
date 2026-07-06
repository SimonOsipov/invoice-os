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

// App is a service built on the platform kit: configuration, a logger, and a
// mux for routes, with the standard middleware chain, health endpoints, and
// Sentry wiring applied for free. A consumer registers routes on Mux and calls
// Run:
//
//	app, err := platform.New("tenancy")
//	if err != nil {
//		log.Fatal(err)
//	}
//	app.Mux.HandleFunc("GET /v1/ping", pingHandler)
//	if err := app.Run(context.Background()); err != nil {
//		log.Fatal(err)
//	}
type App struct {
	Config Config
	Logger *slog.Logger
	Mux    *http.ServeMux

	readiness readiness
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

// handler wraps the mux with the standard middleware chain (outermost first):
// request-id and tenant-id run before recovery so a recovered panic is logged
// and reported with both ids.
func (a *App) handler() http.Handler {
	return chain(a.Mux,
		requestIDMiddleware,
		tenantIDMiddleware,
		recoveryMiddleware(a.Logger),
		requestLogMiddleware(a.Logger),
	)
}

// Run serves HTTP until the process receives SIGINT/SIGTERM (or ctx is
// cancelled), then shuts down gracefully within ShutdownTimeout and flushes
// Sentry.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(a.Config.Port),
		Handler:           a.handler(),
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
		return fmt.Errorf("platform: server error: %w", err)
	case <-ctx.Done():
		a.Logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.Config.ShutdownTimeout)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	flushSentry(2 * time.Second)
	if err != nil {
		return fmt.Errorf("platform: graceful shutdown: %w", err)
	}
	a.Logger.Info("shutdown complete")
	return nil
}
