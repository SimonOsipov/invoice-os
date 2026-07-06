package platform

import (
	"context"
	"fmt"
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
		Environment: cfg.Environment,
		ServerName:  cfg.Service,
	}); err != nil {
		return fmt.Errorf("platform: sentry init: %w", err)
	}
	return nil
}

// flushSentry flushes buffered events; called during graceful shutdown. A
// no-op when Sentry is disabled.
func flushSentry(timeout time.Duration) {
	sentry.Flush(timeout)
}

// taggedHub returns a cloned hub with request/tenant ids from the context set
// as tags, or nil when Sentry is disabled.
func taggedHub(ctx context.Context) *sentry.Hub {
	hub := sentry.CurrentHub()
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

// capturePanic reports a recovered panic to Sentry. A no-op when disabled.
func capturePanic(ctx context.Context, rec any) {
	if hub := taggedHub(ctx); hub != nil {
		hub.RecoverWithContext(ctx, rec)
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
