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

	"github.com/google/uuid"

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

// JobsHandler returns GET /v1/extraction-jobs. Identity is checked FIRST, before any
// parameter is read, so an unauthenticated caller cannot learn which parameters exist by
// watching 400s (TestExtractionJobsHandler_UnauthenticatedIs401BeforeParsing).
//
// The state column passes through untouched: no stage is named here and no number is
// derived from one (TestExtractionHandlers_NamesNoStateLiteral).
func JobsHandler(list func(ctx context.Context, documentID string) (JobsResponse, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Empty is ABSENT for the optional filters of internal/audit/handlers.go:70-74;
		// document_id is required, so both mean the caller named no document
		// (internal/importer/handlers.go:231-235). This check stays above uuid.Parse,
		// which errors on "" too (TestExtractionJobsHandler_MissingDocumentIDIs400).
		documentID := r.URL.Query().Get("document_id")
		if documentID == "" {
			writeError(w, http.StatusBadRequest, "document_id is required")
			return
		}
		if _, err := uuid.Parse(documentID); err != nil {
			writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			return
		}

		out, err := list(r.Context(), documentID)
		if err != nil {
			status, body := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "extraction: jobs for document", slog.Any("err", err))
			}
			writeError(w, status, body)
			return
		}

		writeJSON(w, http.StatusOK, out)
	}
}
