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
	"strconv"
	"time"

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
// response body carries. db.ErrNoTenant is 401 (fail-closed),
// db.ErrNotActiveMember and db.ErrWorkspaceLocked are 403 (TestHandlerMappingEveryRefusalSiteNamesWorkspaceLocked); anything else is 500 with a generic body --
// this helper never leaks internals into the response. Logging the unrecognized (500) case via slog is the caller's
// responsibility, since only the caller knows the operation name to log.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, db.ErrWorkspaceLocked):
		return http.StatusForbidden, db.WorkspaceLockedMessage
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

// HistoryWindowDays is the default and the maximum `days` of GET /v1/rollup/history.
const HistoryWindowDays = 84

// HistoryHandler returns GET /v1/rollup/history. `days` follows the audit-log `limit` rules:
// empty is the default, above the cap clamps, below 1 or non-integer is 400.
func HistoryHandler(history func(ctx context.Context, since string) ([]HistoryRow, error), now func() time.Time, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		days := HistoryWindowDays
		if raw := r.URL.Query().Get("days"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "days must be an integer")
				return
			}
			if n < 1 {
				writeError(w, http.StatusBadRequest, "days must be >= 1")
				return
			}
			days = min(n, HistoryWindowDays)
		}
		since := WATDay(now().Add(-time.Duration(days-1) * 24 * time.Hour))

		rows, err := history(r.Context(), since)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "dashboard: rollup history", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		if rows == nil {
			rows = []HistoryRow{}
		}
		writeJSON(w, http.StatusOK, History{History: rows})
	}
}
