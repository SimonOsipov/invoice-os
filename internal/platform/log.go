package platform

import (
	"context"
	"log/slog"
	"os"
	"strings"

	sentryslog "github.com/getsentry/sentry-go/slog"
)

// ctxKey is the private type for request-scoped context values so keys never
// collide with other packages.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyTenantID
	ctxKeyOutcome
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
// with request_id and tenant_id added from the context on every *Context call.
// With a Sentry DSN, every standard-level record also goes to Sentry Logs.
func newLogger(cfg Config) *slog.Logger {
	level := parseLevel(cfg.LogLevel)
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	if cfg.SentryDSN != "" {
		h = slog.NewMultiHandler(h, escapePercent{sentryLogHandler(level)})
	}
	return slog.New(&contextHandler{Handler: h}).With(
		slog.String("service", cfg.Service),
		slog.String("environment", cfg.Environment),
	)
}

// sentryLogHandler sends logs only: an empty non-nil EventLevel stops the bridge opening
// issues, and LevelFatal stays out because the bridge ends the process on it.
// ceiling: every logged level goes to Sentry (~5 MB/month of 5 GB); add a level floor above ~1 GB/month
// ceiling: a []byte or RawMessage attribute reaches Sentry as unscrubbed %+v text; log it as a string first
func sentryLogHandler(min slog.Level) slog.Handler {
	var levels []slog.Level
	for _, l := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if l >= min {
			levels = append(levels, l)
		}
	}
	return sentryslog.Option{
		EventLevel: []slog.Level{},
		LogLevel:   levels,
		// initSentry's sentry.environment is the only environment label.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == "environment" {
				return slog.Attr{}
			}
			return a
		},
	}.NewSentryHandler(context.Background())
}

// escapePercent doubles "%" in the message: the bridge uses it as a format string.
// ceiling: only the four standard levels reach Sentry; round a custom level down before the bridge if one is ever used
type escapePercent struct{ slog.Handler }

func (h escapePercent) Handle(ctx context.Context, r slog.Record) error {
	r.Message = strings.ReplaceAll(r.Message, "%", "%%")
	return h.Handler.Handle(ctx, r)
}

func (h escapePercent) WithAttrs(attrs []slog.Attr) slog.Handler {
	return escapePercent{h.Handler.WithAttrs(attrs)}
}

func (h escapePercent) WithGroup(name string) slog.Handler {
	return escapePercent{h.Handler.WithGroup(name)}
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
	// ceiling: cause only from an error-valued attribute; a string "error" attribute gives a message-only event
	if r.Level >= slog.LevelError {
		if out := outcomeFromContext(ctx); out != nil {
			var cause error
			r.Attrs(func(a slog.Attr) bool {
				if err, ok := a.Value.Any().(error); ok {
					cause = err
				}
				return true
			})
			if cause != nil {
				out.setCause(cause)
			}
		}
	}
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
