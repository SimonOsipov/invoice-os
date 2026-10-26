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
	"net/url"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
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

		sink := &bundleSink{w: w, contentType: "application/zip"}
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
	w           http.ResponseWriter
	contentType string
	filename    string
	wrote       bool
}

func (s *bundleSink) setFilename(name string) { s.filename = name }

func (s *bundleSink) Write(p []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
		h := s.w.Header()
		h.Set("Content-Type", s.contentType)
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
	case errors.Is(err, db.ErrWorkspaceLocked):
		return http.StatusForbidden, db.WorkspaceLockedMessage
	case errors.Is(err, ErrExportNotPermitted):
		return http.StatusForbidden, "only an admin can download the workspace export"
	case errors.Is(err, ErrExportRunning):
		return http.StatusConflict, "an export of this workspace is already running"
	case errors.Is(err, ErrExportBusy):
		return http.StatusServiceUnavailable, "another workspace export is running; try again in a few minutes"
	case errors.Is(err, ErrEntityNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrTenantNotFound):
		return http.StatusNotFound, "tenant not found"
	case errors.Is(err, db.ErrNotStaff):
		return http.StatusForbidden, "forbidden"
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

// staffEntitiesFn, staffPreviewFn and staffAssembleFn are the Store methods the staff handlers consume.
type (
	staffEntitiesFn func(ctx context.Context, tenantID string) ([]StaffEntity, error)
	staffPreviewFn  func(ctx context.Context, tenantID string, r Request) (Preview, error)
	staffAssembleFn func(ctx context.Context, tenantID string, r Request, w io.Writer, onStart func(filename string)) error
)

// parseStaffRequest parses the query as the tenant routes do, but fails a malformed escape instead of dropping it.
func parseStaffRequest(r *http.Request) (Request, string) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return Request{}, "invalid query string"
	}
	return parseRequest(query)
}

// writeStaffErr answers a store error; auth.RequireStaff runs before the staff handlers.
func writeStaffErr(w http.ResponseWriter, r *http.Request, log *slog.Logger, op string, err error) {
	status, body := statusForErr(err)
	if status == http.StatusInternalServerError {
		log.ErrorContext(r.Context(), op, slog.Any("err", err))
	}
	writeError(w, status, body)
}

// StaffEntitiesHandler returns GET /v1/staff/tenants/{id}/entities.
func StaffEntitiesHandler(list staffEntitiesFn, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		entities, err := list(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStaffErr(w, r, log, "archive: staff entities", err)
			return
		}
		if entities == nil {
			entities = []StaffEntity{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"entities": entities})
	}
}

// StaffPreviewHandler returns GET /v1/staff/tenants/{id}/evidence-bundle/preview.
func StaffPreviewHandler(preview staffPreviewFn, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		req, msg := parseStaffRequest(r)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		p, err := preview(r.Context(), r.PathValue("id"), req)
		if err != nil {
			writeStaffErr(w, r, log, "archive: staff preview", err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	}
}

// StaffDownloadHandler returns GET /v1/staff/tenants/{id}/evidence-bundle.
func StaffDownloadHandler(assemble staffAssembleFn, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		req, msg := parseStaffRequest(r)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		sink := &bundleSink{w: w, contentType: "application/zip"}
		err := assemble(r.Context(), r.PathValue("id"), req, sink, sink.setFilename)
		if err == nil {
			return
		}
		if sink.wrote {
			log.ErrorContext(r.Context(), "archive: bundle abandoned mid-stream", slog.Any("err", err))
			return
		}
		writeStaffErr(w, r, log, "archive: staff assemble", err)
	}
}

// exportWriteWindow is how long the export may keep writing; the server's own write timeout is shorter.
const exportWriteWindow = time.Hour

type workspaceAssembleFn func(ctx context.Context, w io.Writer, onStart func(filename string)) error

// WorkspaceExportHandler returns GET /v1/workspace/export. After the first byte an error aborts
// the connection, so the client sees a broken download and not a short ZIP.
func WorkspaceExportHandler(assemble workspaceAssembleFn, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(exportWriteWindow))

		sink := &bundleSink{w: w, contentType: "application/zip"}
		err := assemble(r.Context(), sink, sink.setFilename)
		if err == nil {
			return
		}
		if sink.wrote {
			if !errors.Is(r.Context().Err(), context.Canceled) {
				platform.CaptureError(r.Context(), err)
			}
			log.ErrorContext(r.Context(), "archive: workspace export aborted mid-stream", slog.Any("err", err))
			panic(http.ErrAbortHandler)
		}
		status, body := statusForErr(err)
		if status == http.StatusInternalServerError {
			log.ErrorContext(r.Context(), "archive: workspace export", slog.Any("err", err))
		}
		if errors.Is(err, ErrExportBusy) {
			w.Header().Set("Retry-After", "60")
		}
		writeError(w, status, body)
	}
}

// WorkspaceExportPreflightHandler returns GET /v1/workspace/export/preflight: 204 when the export can start.
func WorkspaceExportPreflightHandler(check func(context.Context) error, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := check(r.Context()); err != nil {
			writeStaffErr(w, r, log, "archive: workspace export preflight", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
