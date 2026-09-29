package platform

import (
	"fmt"
	"log/slog"
	"os"
)

// Fatal logs a boot failure at ERROR and exits 1.
func Fatal(logger *slog.Logger, format string, args ...any) {
	logger.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}

// ReportBootPanic is deferred first in main; it re-panics a recovered value.
func ReportBootPanic() {
	if rec := recover(); rec != nil {
		panic(rec)
	}
}
