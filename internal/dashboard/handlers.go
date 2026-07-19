// M4-07-03 (task-157): RollupHandler is GET /v1/rollup -- identity-first
// 401 -> call rollup -> statusForErr -> JSON, same order and shape as
// internal/portfolio/portfolio.go's GetHandler and
// internal/invoice/handlers.go's GetHandler (the read-no-params analogues;
// rollup takes no path/query/body params, unlike List/Create). writeJSON/
// writeError mirror those packages' helpers of the same name verbatim (the
// shared {"error":"..."} envelope convention, per-package duplicate, not a
// shared library).
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusForErr maps a store/domain error to the HTTP status + message the
// response body carries. db.ErrNoTenant is 401 (fail-closed); anything else
// is 500 with a generic body -- this helper never leaks internals into the
// response. Logging the unrecognized (500) case via slog is the caller's
// responsibility, since only the caller knows the operation name to log.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// RollupHandler returns GET /v1/rollup. No path/query/body params -- the
// endpoint is read-only and tenant-scoped entirely by ctx identity.
func RollupHandler(rollup func(ctx context.Context) (Rollup, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		result, err := rollup(r.Context())
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "dashboard: rollup", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, result)
	}
}
