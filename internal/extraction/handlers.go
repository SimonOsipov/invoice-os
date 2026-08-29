// handlers.go: GET /v1/extraction-jobs. Reached through the gateway as
// /api/submission/v1/extraction-jobs — the gateway routes on the first segment under /api/
// and forwards the subpath, so the mux pattern carries no prefix.
//
// writeJSON/writeError/statusForErr mirror internal/audit/handlers.go:42-64 verbatim;
// per-package duplicates are the convention here, not a shared library.
package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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

// statusForErr maps a reader error to its status and body; no internal ever reaches the
// response. Logging the 500 case is the caller's job — only it knows the operation.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// JobsHandler returns GET /v1/extraction-jobs.
//
// STUB. Every arm of the status table is unbuilt, so handlers_test.go fails on its own
// assertions rather than on a compile error — a compile error proves nothing about them.
func JobsHandler(list func(ctx context.Context, documentID string) (JobsResponse, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
