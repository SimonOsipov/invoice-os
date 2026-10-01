package platform

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/getsentry/sentry-go"
)

// initSentry initializes the global Sentry hub. An empty DSN disables Sentry
// (the SDK becomes a no-op), which is the intended state for local dev and CI.
func initSentry(cfg Config) error {
	if cfg.SentryDSN == "" {
		return nil
	}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:         cfg.SentryDSN,
		Environment: cfg.SentryEnvironment,
		Release:     cfg.Release,
		ServerName:  cfg.Service,
		// The telemetry scheduler polls an event off its buffer before queueing it, so Flush can
		// return first and os.Exit drops a boot failure. The transport path queues synchronously.
		DisableTelemetryBuffer: true,
		EnableTracing:          true,
		// ceiling: 100% of requests ≈ 13k–32k spans/month (0.3–0.6% of 5M); sample below 1.0 above ~2.5M spans/month
		TracesSampleRate: 1.0,
		// Keep 404s: the SDK drops them by default.
		TraceIgnoreStatusCodes: [][]int{},
		// Client hooks cover every capture path; initSentry is the only sentry.Init.
		BeforeSend:            scrubEvent,
		BeforeSendTransaction: scrubEvent,
		BeforeSendLog:         scrubLog,
	}); err != nil {
		return fmt.Errorf("platform: sentry init: %w", err)
	}
	return nil
}

// SentryState is "on" or "off" for /healthz. sentry-go binds a no-op client
// for an empty DSN, so a bound client alone does not mean events are sent.
func SentryState() string {
	if c := sentry.CurrentHub().Client(); c != nil && c.Options().Dsn != "" {
		return "on"
	}
	return "off"
}

// flushSentry flushes buffered events. A no-op when Sentry is disabled.
func flushSentry(timeout time.Duration) {
	sentry.Flush(timeout)
}

// taggedHub returns a cloned hub with request/tenant ids from the context set
// as tags, or nil when Sentry is disabled. It clones the request's hub when
// tracingMiddleware installed one, so issues carry the request's trace.
func taggedHub(ctx context.Context) *sentry.Hub {
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		hub = sentry.CurrentHub()
	}
	if hub.Client() == nil {
		return nil
	}
	hub = hub.Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		if id := RequestIDFromContext(ctx); id != "" {
			scope.SetTag("request_id", id)
		}
		if id := TenantIDFromContext(ctx); id != "" {
			scope.SetTag("tenant_id", id)
		}
	})
	return hub
}

// capturePanic reports a recovered handler panic with its request; scrubEvent
// strips the query, body, cookies and non-allowlisted headers. A no-op when disabled.
func capturePanic(r *http.Request, rec any) {
	if hub := taggedHub(r.Context()); hub != nil {
		hub.Scope().SetRequest(r)
		hub.RecoverWithContext(r.Context(), rec)
	}
}

// CaptureError reports a non-fatal error to Sentry, tagged with the request and
// tenant ids from the context. Safe to call when Sentry is disabled.
func CaptureError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if hub := taggedHub(ctx); hub != nil {
		hub.CaptureException(err)
	}
}
