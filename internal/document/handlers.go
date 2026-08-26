package document

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// DownloadHandler is GET /v1/documents/{id}: identity-first 401 -> a
// handler-level uuid.Parse guard -> open -> stream. The request carries only the
// opaque id; the storage key comes off the RLS-visible row.
//
// It takes the open func rather than the *Service so the failure modes stay
// injectable.
func DownloadHandler(
	open func(ctx context.Context, id, rangeHeader string) (Document, Object, error),
	log *slog.Logger,
) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		id := r.PathValue("id")
		if _, err := uuid.Parse(id); err != nil {
			writeError(w, http.StatusBadRequest, "id must be a well-formed uuid")
			return
		}

		// Range is forwarded verbatim rather than parsed: RFC 7233 parsing belongs to
		// the object store, and a byte offset cannot select a different object.
		doc, obj, err := open(r.Context(), id, r.Header.Get("Range"))
		if obj.Body != nil {
			defer func() { _ = obj.Body.Close() }()
		}
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "document: download", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		// A partial body with no Content-Range is an upstream fault, not a 200: a 200
		// declares the complete representation, so it would ship a silent truncation.
		if obj.Partial && obj.ContentRange == "" {
			log.ErrorContext(r.Context(), "document: upstream partial response carries no Content-Range")
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		// Fixed, never the row's declared_content_type: with nosniff and an
		// attachment disposition this is the stated virus-scanning mitigation.
		h := w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Disposition", contentDisposition(doc.Filename))
		h.Set("Accept-Ranges", "bytes")
		// Object.Size is the BODY length, so it is right on a 206 too. A nil upstream
		// ContentLength arrives as 0 and must stay undeclared, not truncate the body.
		if obj.Size > 0 {
			h.Set("Content-Length", strconv.FormatInt(obj.Size, 10))
		}

		// The fault guard above already rejected a blank ContentRange, so a 206 can
		// never go out without the Content-Range it is only meaningful with.
		status := http.StatusOK
		if obj.Partial {
			status = http.StatusPartialContent
			h.Set("Content-Range", obj.ContentRange)
		}
		w.WriteHeader(status)

		if obj.Body != nil {
			if _, err := io.Copy(w, obj.Body); err != nil {
				// The status is already on the wire; this can only be logged.
				log.ErrorContext(r.Context(), "document: stream object", slog.Any("err", err))
			}
		}
	}
}

// contentDisposition renders the header rather than concatenating it:
// SanitizeFilename leaves `"` intact and passes non-ASCII through. A nil name
// omits the param entirely — an empty value would render filename="".
func contentDisposition(name *string) string {
	if name == nil || *name == "" {
		return "attachment"
	}
	return mime.FormatMediaType("attachment", map[string]string{"filename": *name})
}

// statusForErr maps a service error to the status and the message that goes on
// the wire. ErrValidation carries a fixed message, never err.Error(): the
// package-internal "document: validation" string must not leak.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrRangeNotSatisfiable):
		return http.StatusRequestedRangeNotSatisfiable, "requested range not satisfiable"
	case errors.Is(err, ErrValidation):
		return http.StatusBadRequest, "id must be a well-formed uuid"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// writeError mirrors internal/invoice's helper of the same name: the shared
// {"error":"..."} envelope, copied per-package rather than imported.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
