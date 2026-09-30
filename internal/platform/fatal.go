package platform

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
)

const fatalFlushTimeout = 2 * time.Second

// Fatal logs a boot failure at ERROR, reports it, flushes and exits 1. The
// fingerprint holds the format string, never the arguments.
//
// ceiling: no report before initSentry; parse the DSN first if a boot-config typo ever goes unseen
// ceiling: a crash loop sends one event per restart, bounded by Railway's restart cap
func Fatal(logger *slog.Logger, format string, args ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	msg := fmt.Sprintf(format, args...)
	logger.Error(msg)
	if hub := sentry.CurrentHub(); hub.Client() != nil && !isShutdownErr(args) {
		hub = hub.Clone()
		hub.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetLevel(sentry.LevelFatal)
			scope.SetFingerprint([]string{"boot-failure", format})
		})
		hub.CaptureMessage(msg)
	}
	flushSentry(fatalFlushTimeout)
	os.Exit(1)
}

// isShutdownErr reports a graceful-shutdown overrun, which every deploy can cause.
func isShutdownErr(args []any) bool {
	for _, a := range args {
		if err, ok := a.(error); ok && errors.Is(err, errShutdown) {
			return true
		}
	}
	return false
}

// ReportBootPanic is deferred first in main; it reports a recovered panic, then re-panics it.
func ReportBootPanic() {
	rec := recover()
	if rec == nil {
		return
	}
	if hub := sentry.CurrentHub(); hub.Client() != nil {
		hub = hub.Clone()
		hub.ConfigureScope(func(scope *sentry.Scope) { scope.SetLevel(sentry.LevelFatal) })
		hub.Recover(rec)
	}
	flushSentry(fatalFlushTimeout)
	panic(rec)
}
