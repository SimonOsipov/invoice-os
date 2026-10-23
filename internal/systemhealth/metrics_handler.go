package systemhealth

import (
	"context"
	"log/slog"
	"net/http"
)

// MetricsHandler validates the query, runs the database staff check, then serves the cached metrics.
// Red-commit stub: writes 200 {} and calls neither check nor metrics.
func MetricsHandler(check func(context.Context) error, metrics func(ctx context.Context, rangeName, service string) (Metrics, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct{}{})
	}
}
