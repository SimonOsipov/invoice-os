// handlers.go: GET /v1/extractions and GET /v1/extractions/{id}. Reached through the gateway
// as /api/submission/v1/… — the gateway routes on the first segment under /api/ and forwards
// the subpath, so the mux patterns carry no prefix.
//
// writeJSON and writeError mirror internal/audit/handlers.go verbatim; statusForErr mirrors it
// plus the 404 arm this package owns. Per-package duplicates are the convention here, not a
// shared library.
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
	// Safe for the collection route, which never raises it
	// (TestExtractionJobsForDocument_NeverReturnsErrNotFound), and narrow: everything else is
	// still a 500 (TestStatusForErr_UnknownErrorIsStill500).
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// JobsHandler returns GET /v1/extractions. Identity is checked FIRST, before any
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
		parsed, err := uuid.Parse(documentID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			return
		}

		// Forward the canonical spelling, not the raw value: uuid.Parse accepts a "urn:uuid:"
		// prefix that Postgres rejects with 22P02, which would surface as a 500
		// (TestExtractionJobsHandler_UrnPrefixedUuidReachesTheReaderCanonicalised).
		out, err := list(r.Context(), parsed.String())
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

// DetailHandler returns GET /v1/extractions/{id}. Identity is checked FIRST, before the path
// value is read, so an unauthenticated caller cannot tell a malformed id from a well-formed one
// (TestExtractionDetailHandler_UnauthenticatedIs401BeforeParsing). An absent job and another
// tenant's are one answer: the reader raises ErrNotFound for both and statusForErr maps it to
// 404.
func DetailHandler(detail func(ctx context.Context, jobID string) (ExtractionDetail, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// The message names this route's own parameter: the collection route's would tell a
		// caller the wrong field (TestExtractionDetailHandler_MalformedIdIs400).
		parsed, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "id must be a well-formed uuid")
			return
		}

		// Forward the canonical spelling, not the raw path value: uuid.Parse accepts a
		// "urn:uuid:" prefix that Postgres rejects with 22P02, which would surface as a 500
		// (TestExtractionDetailHandler_UrnPrefixedUuidReachesTheReaderCanonicalised).
		out, err := detail(r.Context(), parsed.String())
		if err != nil {
			status, body := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "extraction: detail for job", slog.Any("err", err))
			}
			writeError(w, status, body)
			return
		}

		writeJSON(w, http.StatusOK, out)
	}
}
