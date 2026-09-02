// handlers.go: GET /v1/extractions, GET /v1/extractions/{id} and
// GET /v1/extractions/{id}/pages/{n}. Reached through the gateway as /api/submission/v1/… — the
// gateway routes on the first segment under /api/ and forwards the subpath, so the mux patterns
// carry no prefix.
//
// writeJSON and writeError mirror internal/audit/handlers.go verbatim; statusForErr mirrors it
// plus the 404 arm this package owns. Per-package duplicates are the convention here, not a
// shared library.
package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

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
	// The correction route's three domain outcomes (TestStatusForErr_MapsTheThreeInvoiceSentinels).
	case errors.Is(err, ErrNoInvoiceForDocument):
		return http.StatusConflict, "no invoice has been filed from this document"
	case errors.Is(err, ErrInvoiceNotEditable):
		return http.StatusConflict, "this invoice can no longer be corrected"
	case errors.Is(err, ErrValueRefused):
		return http.StatusBadRequest, "the invoice refused this value"
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

// PageObject fetches one stored object by key, handing the body back UNREAD. A func value, not
// internal/document's ObjectStore: deps_test.go fences this package off that one, so
// cmd/submission supplies the adapter (TestExtractionPackage_DoesNotImportDocumentPackage).
type PageObject func(ctx context.Context, key string) (io.ReadCloser, int64, error)

// PageImageHandler returns GET /v1/extractions/{id}/pages/{n}. Identity is checked FIRST, before
// either path value is read (TestExtractionPageImageHandler_UnauthenticatedIs401BeforeParsing),
// and the object key comes off the RLS-visible row rather than the request, so no
// caller-supplied text reaches object storage.
//
// The three headers and the ABSENT Content-Disposition are the deliverable: an attachment is
// never painted as an image (TestExtractionPageImageHandler_ServesImagePngWithoutADisposition).
func PageImageHandler(key func(ctx context.Context, jobID string, page int) (string, error), object PageObject, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// The detail route's message: both routes bind the same {id}, and a second spelling
		// would tell a caller the wrong field (TestExtractionPageImageHandler_MalformedJobIdIs400).
		parsed, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "id must be a well-formed uuid")
			return
		}
		// page_number is 1-based. Atoi refuses "", " 1", "1.5" and anything past int64; the sign
		// check refuses the rest (TestExtractionPageImageHandler_NonPositivePageIs400).
		page, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || page < 1 {
			writeError(w, http.StatusBadRequest, "page must be a positive integer")
			return
		}

		// Canonical spelling, not the raw path value: uuid.Parse accepts a "urn:uuid:" prefix
		// that Postgres rejects with 22P02.
		storageKey, err := key(r.Context(), parsed.String(), page)
		if err != nil {
			status, body := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "extraction: page image key", slog.Any("err", err))
			}
			writeError(w, status, body)
			return
		}

		body, size, err := object(r.Context(), storageKey)
		// Registered before the error branch: a store that hands back both a body and an error
		// still owes a close (TestExtractionPageImageHandler_ClosesBodyWhenTheStoreErrors).
		if body != nil {
			defer func() { _ = body.Close() }()
		}
		if err == nil && body == nil {
			err = errors.New("the object store returned no body")
		}
		if err != nil {
			log.ErrorContext(r.Context(), "extraction: page image object",
				slog.String("key", storageKey), slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		// Refused before the 200, so Content-Length below can be unconditional. That header is
		// what turns a mid-stream object failure into a broken response rather than a short,
		// well-formed PNG the browser accepts
		// (TestExtractionPageImageHandler_ATruncatedObjectDoesNotArriveAsASuccessfulShortPng).
		if size <= 0 {
			log.ErrorContext(r.Context(), "extraction: page image object reports no length",
				slog.String("key", storageKey), slog.Int64("size", size))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		h := w.Header()
		h.Set("Content-Type", "image/png")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "private, no-store")
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)

		// io.Copy, never io.ReadAll: a page is ~113 KiB and a document may hold 800
		// (TestExtractionPageImageHandler_StreamsRatherThanBuffers).
		if _, err := io.Copy(w, body); err != nil {
			log.ErrorContext(r.Context(), "extraction: stream page image",
				slog.String("key", storageKey), slog.Any("err", err))
		}
	}
}
