// handlers.go: GET /v1/evidence-bundle (AUDIT-05-08). Identity-first, then
// parameters, then the store. writeJSON/writeError/statusForErr mirror
// internal/audit/handlers.go:42-61 -- per-package duplicates are this repo's
// convention. Subtask 09's PreviewHandler calls the same three functions (D-40).
package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// assembleFn is Store.Assemble as DownloadHandler consumes it. Unexported: a
// method value's type is unnamed, so archive.NewStore(pool).Assemble is
// assignable from cmd/invoice without naming it.
type assembleFn func(ctx context.Context, r Request, w io.Writer, onStart func(filename string)) error

// previewFn is Store.Preview as PreviewHandler consumes it.
type previewFn func(ctx context.Context, r Request) (Preview, error)

// DownloadHandler returns GET /v1/evidence-bundle. Identity is checked FIRST,
// before any parameter is read (AC-1); parsing runs before the store is ever
// touched (AC-2).
func DownloadHandler(assemble assembleFn, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		req, msg := parseRequest(r.URL.Query())
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		sink := &bundleSink{w: w}
		err := assemble(r.Context(), req, sink, sink.setFilename)
		if err == nil {
			return
		}
		if sink.wrote {
			// The 200 is already on the wire; assemble skipped bw.Close(), so the
			// archive has no central directory (D-15). Log only, append no JSON.
			log.ErrorContext(r.Context(), "archive: bundle abandoned mid-stream", slog.Any("err", err))
			return
		}
		status, body := statusForErr(err)
		if status == http.StatusInternalServerError {
			log.ErrorContext(r.Context(), "archive: assemble", slog.Any("err", err))
		}
		writeError(w, status, body)
	}
}

// PreviewHandler returns GET /v1/evidence-bundle/preview. Same identity-then-parse-
// then-store order as DownloadHandler (D-52), and the same statusForErr/writeJSON/
// writeError seam -- no second error-mapping copy.
func PreviewHandler(preview previewFn, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		req, msg := parseRequest(r.URL.Query())
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}

		p, err := preview(r.Context(), req)
		if err != nil {
			status, body := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "archive: preview", slog.Any("err", err))
			}
			writeError(w, status, body)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// bundleSink defers the 200 and its headers to the first byte, so any failure
// before then is still an honest JSON status (D-41).
type bundleSink struct {
	w        http.ResponseWriter
	filename string
	wrote    bool
}

func (s *bundleSink) setFilename(name string) { s.filename = name }

func (s *bundleSink) Write(p []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
		h := s.w.Header()
		h.Set("Content-Type", "application/zip")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Disposition", contentDisposition(s.filename))
		s.w.WriteHeader(http.StatusOK)
	}
	return s.w.Write(p)
}

// contentDisposition mirrors internal/document/handlers.go:98-105, taking a
// string rather than a *string: DownloadHandler always has a filename by the
// time bundleSink's first Write fires.
func contentDisposition(name string) string {
	if name == "" {
		return "attachment"
	}
	return mime.FormatMediaType("attachment", map[string]string{"filename": name})
}

// statusForErr maps a store error to its status and body (D-40, D-45).
// TooManyInvoicesError's fields, never its Error() string, build the 400 body --
// (*TooManyInvoicesError).Error() carries the "archive: " package prefix, which
// internal/document/handlers.go:110-118 forbids leaking onto the wire.
func statusForErr(err error) (status int, msg string) {
	var tooMany *TooManyInvoicesError
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
	case errors.Is(err, ErrEntityNotFound):
		return http.StatusNotFound, "not found"
	case errors.As(err, &tooMany):
		return http.StatusBadRequest,
			fmt.Sprintf("%d invoices exceeds the bundle limit of %d", tooMany.Count, tooMany.Limit)
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
