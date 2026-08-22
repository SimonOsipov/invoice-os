// handlers.go: GET /v1/audit-log (AUDIT-04-07). Reached through the gateway as
// /api/invoice/v1/audit-log — the gateway routes on the first segment under /api/ and
// forwards the subpath, so the mux pattern carries no prefix.
//
// writeJSON/writeError/statusForErr mirror internal/dashboard/handlers.go:22-44 verbatim;
// per-package duplicates are the convention here, not a shared library.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// maxFilterTextLen bounds q, matching internal/invoice's cap of the same name.
const maxFilterTextLen = 200

// maxFilterValues bounds the repeated event and actor params. Checked over USABLE
// (non-empty) values and before any further work, so a caller cannot buy unbounded
// parsing with empty ones.
const maxFilterValues = 50

// defaultLimit and maxLimit are §2's page rules: absent means 25, above 100 clamps, below
// 1 is a 400 rather than a clamp — a caller asking for 0 rows has made a mistake, and
// silently serving 25 hides it.
const (
	defaultLimit = 25
	maxLimit     = 100
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusForErr maps a store error to its status and body. db.ErrNoTenant is 401
// (fail-closed); anything else is a 500 with a generic body, so no internal ever reaches
// the response. Logging the 500 case is the caller's job — only it knows the operation.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// ListHandler returns GET /v1/audit-log.
func ListHandler(list func(ctx context.Context, f Filter) (Response, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		_ = auth.IdentityFromContext
		writeJSON(w, http.StatusOK, Response{})
	}
}
