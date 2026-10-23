package systemhealth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// MetricsHandler serves GET /v1/staff/metrics: validate the query, run the database staff check, then read the metrics.
func MetricsHandler(check func(context.Context) error, metrics func(ctx context.Context, rangeName, service string) (Metrics, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		rangeName, service := q.Get("range"), q.Get("service")
		if _, ok := Ranges[rangeName]; !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown range"})
			return
		}
		if service != "" && !slices.Contains(MetricServices, service) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown service"})
			return
		}
		if err := check(r.Context()); err != nil {
			if errors.Is(err, db.ErrNotStaff) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
			log.Error("systemhealth: staff metrics check", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}
		m, err := metrics(r.Context(), rangeName, service)
		if err != nil {
			if r.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			log.Error("systemhealth: staff metrics", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}
		writeJSON(w, http.StatusOK, m)
	}
}
