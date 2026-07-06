package platform

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// ctxKey is the private type for request-scoped context values so keys never
// collide with other packages.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyTenantID
)

// WithRequestID returns a context carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestIDFromContext returns the request id, or "" if unset.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// WithTenantID returns a context carrying the tenant id.
func WithTenantID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyTenantID, id)
}

// TenantIDFromContext returns the tenant id, or "" if unset.
func TenantIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyTenantID).(string)
	return id
}

// newLogger builds the process logger: JSON to stdout at the configured level,
// with request_id and tenant_id automatically added from the context on every
// *Context log call, plus service and environment as base fields.
func newLogger(cfg Config) *slog.Logger {
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)})
	return slog.New(&contextHandler{Handler: base}).With(
		slog.String("service", cfg.Service),
		slog.String("environment", cfg.Environment),
	)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// contextHandler enriches every record with request-scoped fields pulled from
// the context, so handlers never have to thread ids through each log call.
type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestIDFromContext(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if id := TenantIDFromContext(ctx); id != "" {
		r.AddAttrs(slog.String("tenant_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}
