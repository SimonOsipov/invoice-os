// M4-03-05 (task-106): POST /v1/imports -- the HTTP handler over Service.Import.
// Mirrors internal/invoice/handlers.go's CreateHandler factory idiom
// (identity-first-401, local statusForErr, shared {"error":"..."} envelope
// copied per-package rather than imported cross-package) plus the
// multipart-specific steps this endpoint needs: an upload cap
// ([upload-cap]), multipart form parsing, mapping JSON decode, and
// CSV/XLSX format detection ([mapping-transport]) ahead of the package-level
// Decode -> Service.Import handoff. See handlers_test.go's doc comment for
// the full IMP-API-01..07 Test Specs map.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// maxUploadBytes is the whole-request upload cap ([upload-cap]): a request
// body (multipart preamble + entity_id/mapping fields + the file part) over
// this size 413s before ParseMultipartForm ever finishes reading it.
const maxUploadBytes = 10 << 20 // 10 MiB

// maxMultipartMemory is ParseMultipartForm's in-memory threshold: parts
// under this combined size are held in memory, anything larger spills to a
// temp file on disk. This is an implementation-detail knob, unrelated to
// maxUploadBytes (which bounds the whole request, on-disk or not).
const maxMultipartMemory = 8 << 20 // 8 MiB

// importResponse is the POST /v1/imports success body: BatchResult's fields
// plus the DecodeFacts merged in (format/delimiter/encoding). ID/Status carry
// their zero value ("") for a dry run -- BatchResult never sets them in that
// case -- and are marked omitempty so a dry-run body omits both rather than
// emitting empty strings. Delimiter/Encoding are pointers so an xlsx upload
// (DecodeFacts leaves both "") serializes as JSON null, not "".
type importResponse struct {
	ID                  string     `json:"id,omitempty"`
	Status              string     `json:"status,omitempty"`
	Format              string     `json:"format"`
	Delimiter           *string    `json:"delimiter"`
	Encoding            *string    `json:"encoding"`
	RowsTotal           int        `json:"rows_total"`
	RowsValid           int        `json:"rows_valid"`
	RowsInvalid         int        `json:"rows_invalid"`
	ReadyInvoices       int        `json:"ready_invoices"`
	QuarantinedInvoices int        `json:"quarantined_invoices"`
	Errors              []RowError `json:"errors"`
}

// nilIfEmpty returns nil for "" and &s otherwise -- used for Delimiter/
// Encoding, which must be JSON null (not "") for an xlsx upload.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// detectFormat resolves the uploaded file's format ("csv" | "xlsx") from its
// filename extension first, falling back to its declared Content-Type when
// the extension is missing or unrecognized ([mapping-transport] leaves both
// available to the handler). Returns "" when neither signals a supported
// format.
func detectFormat(filename, contentType string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".xlsx":
		return "xlsx"
	case ".csv":
		return "csv"
	}

	// mime.ParseMediaType strips any "; charset=..." parameters a client
	// might send alongside the base type.
	base := contentType
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		base = parsed
	}
	switch base {
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "xlsx"
	case "text/csv", "text/plain":
		return "csv"
	}
	return ""
}

// CreateHandler returns POST /v1/imports (mirrors internal/invoice's
// CreateHandler factory: a closure over the injected Service.Import method ->
// http.HandlerFunc). Flow: identity-first-401 (IMP-API-01) -> upload-cap via
// http.MaxBytesReader ([upload-cap]) -> ParseMultipartForm (a MaxBytesError
// -> 413, IMP-API-04; any other parse error -> 400) -> entity_id/mapping form
// values (blank/malformed -> 400, IMP-API-05) -> the file part + format
// detection (unrecognized -> 400) -> Decode (undecodable -> 400) -> imp
// (Service.Import) -> statusForErr -> the shared {"error":"..."} envelope on
// failure, or a 200 (dry run) / 201 (real) importResponse on success.
func CreateHandler(imp func(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the upload size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid multipart form")
			return
		}

		entityID := r.FormValue("entity_id")
		if entityID == "" {
			writeError(w, http.StatusBadRequest, "entity_id is required")
			return
		}

		rawMapping := r.FormValue("mapping")
		if rawMapping == "" {
			writeError(w, http.StatusBadRequest, "mapping is required")
			return
		}
		var mapping map[string]string
		if err := json.Unmarshal([]byte(rawMapping), &mapping); err != nil {
			writeError(w, http.StatusBadRequest, "mapping is not valid JSON")
			return
		}

		file, fh, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "file is required")
			return
		}
		defer file.Close()

		format := detectFormat(fh.Filename, fh.Header.Get("Content-Type"))
		if format == "" {
			writeError(w, http.StatusBadRequest, "unrecognized file format")
			return
		}

		header, rows, facts, err := Decode(file, format)
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not decode uploaded file")
			return
		}

		dryRun := r.URL.Query().Get("dry_run") == "true"

		res, err := imp(r.Context(), entityID, mapping, header, rows, dryRun)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "importer: create", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		status := http.StatusCreated
		if dryRun {
			status = http.StatusOK
		}
		writeJSON(w, status, importResponse{
			ID:                  res.ID,
			Status:              res.Status,
			Format:              facts.Format,
			Delimiter:           nilIfEmpty(facts.Delimiter),
			Encoding:            nilIfEmpty(facts.Encoding),
			RowsTotal:           res.RowsTotal,
			RowsValid:           res.RowsValid,
			RowsInvalid:         res.RowsInvalid,
			ReadyInvoices:       res.ReadyInvoices,
			QuarantinedInvoices: res.QuarantinedInvoices,
			Errors:              res.Errors,
		})
	}
}

// statusForErr maps a service error to the HTTP status + message this
// handler writes to the response, mirroring internal/invoice's own
// statusForErr: ErrValidation is 400 with the wrapped message, ErrNotFound is
// 404, anything else is 500 with a generic body (never leaking internals).
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, ErrValidation):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// writeJSON and writeError mirror internal/invoice/handlers.go's helpers of
// the same name verbatim (the shared {"error":"..."} envelope convention,
// copied per-package rather than imported cross-package).
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
