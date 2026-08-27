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
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// maxUploadBytes is the whole-request upload cap ([upload-cap]): a request
// body over this size 413s before ParseMultipartForm ever finishes reading it.
// Binary MiB, not 15,000,000 decimal: documents.size_bytes CHECKs <= 15728640.
const maxUploadBytes = 15 << 20 // 15 MiB

// maxMultipartMemory is ParseMultipartForm's in-memory threshold: parts
// under this combined size are held in memory, anything larger spills to a
// temp file on disk. This is an implementation-detail knob, unrelated to
// maxUploadBytes (which bounds the whole request, on-disk or not).
const maxMultipartMemory = 8 << 20 // 8 MiB

// importResponse is the POST /v1/imports success body: BatchResult's fields
// plus the DecodeFacts merged in (format/delimiter/encoding).
//
// The four rule-outcome fields are ADDITIVE (M4-04-07, [import-report-shape]):
// the five M4-03 counters above them keep their EXACT shipped meaning -- M4-08
// is being built against them -- and `errors` stays STRUCTURAL ONLY, M4-03's
// unchanged RowError set. The two outcomes NEVER MIX (Core AC#5): `errors`
// means "couldn't read this row"; `invoice_violations` means "read fine, but
// the rule failed". A rule failure is not a structural error and must never
// appear in `errors`. Since M4-06-01, a structural `errors[]` entry MAY
// itself carry a `rule_key`/`severity` (e.g. a store-level duplicate,
// RuleKey "no-duplicate-invoice-number") when the reason for quarantining
// the row happens to be a NAMED rule -- this is still structural, not a
// content violation: the row was never grouped into an evaluated invoice, so
// it does NOT and must NOT ever appear in `invoice_violations` too. Core
// AC#5's NEVER MIX invariant holds exactly as before. Such an entry still
// lands in the wire's `errors[]` array, but the browser routes it to its own
// already-imported tab rather than the Unreadable rows one.
//
// RuleSetVersion is a *int with NO omitempty on purpose: it must render an
// explicit `null` when nothing was evaluated, never be absent and never be a
// false `0` stamp ([Stage-1 F2] / IMPV-16). RuleSetVersion/InvoicesClean/
// InvoicesWithViolations/InvoiceViolations are populated on BOTH the real and
// dry-run paths -- `invoices_clean` is named for that: on a real import clean
// means promoted to `validated`, on a dry-run it is the count that WOULD pass. ID/Status carry
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

	RuleSetVersion         *int                `json:"rule_set_version"`
	InvoicesClean          int                 `json:"invoices_clean"`
	InvoicesWithViolations int                 `json:"invoices_with_violations"`
	InvoiceViolations      []InvoiceViolations `json:"invoice_violations"`
}

// nilIfEmpty returns nil for "" and &s otherwise -- used for Delimiter/
// Encoding, which must be JSON null (not "") for an xlsx upload.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// derefOr is nilIfEmpty's inverse, for the document row's two nullable
// columns: a NULL filename or content type is simply no signal, not an error.
func derefOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

// maxSampleRows caps the number of data rows previewResponse.SampleRows
// echoes back to the caller (M4-08-01, task-170 Implementation Plan §B).
const maxSampleRows = 5

// previewResponse is the POST /v1/imports/preview success body: the stored
// document's id plus DecodeFacts merged with Decode's header/rows, capped and
// reshaped for a preview. Field order = JSON key order = the story's example.
// Delimiter/Encoding mirror importResponse exactly (nilIfEmpty, JSON null for
// an xlsx upload). Columns/SampleRows must always render as a JSON array,
// never null -- see PreviewHandler's nil-slice guard.
type previewResponse struct {
	DocumentID string     `json:"document_id"`
	Format     string     `json:"format"`
	Delimiter  *string    `json:"delimiter"`
	Encoding   *string    `json:"encoding"`
	Columns    []string   `json:"columns"`
	SampleRows [][]string `json:"sample_rows"`
	RowsTotal  int        `json:"rows_total"`
}

// previewError is the envelope for preview's two POST-STORE failures
// ([error-body-carries-document-id]): the bytes are already stored, so the
// response must name them or a file that failed to parse is unretrievable.
// Every PRE-store failure keeps writeError's bare envelope.
type previewError struct {
	Error      string `json:"error"`
	DocumentID string `json:"document_id"`
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
// -> 413, IMP-API-04; any other parse error -> 400) -> entity_id/mapping/
// document_id form values (blank/malformed -> 400, IMP-API-05) -> open (the
// document's bytes) -> format detection (unrecognized -> 400) -> Decode
// (undecodable -> 400) -> imp (Service.Import) -> statusForErr -> the shared
// {"error":"..."} envelope on failure, or a 200 (dry run) / 201 (real)
// importResponse on success.
//
// [upload-once] The file itself no longer crosses this wire: it was stored by
// POST /v1/imports/preview, and the caller sends that document's id. The read
// happens BEFORE any database write, so an import cannot half-succeed while
// object storage is down ([fail-closed]). A dry run needs the bytes too --
// it decodes them, it just persists nothing.
func CreateHandler(
	imp func(ctx context.Context, entityID, filename, documentID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error),
	open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error),
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

		// The retired part fails loudly rather than being silently ignored: a
		// client still uploading here would otherwise import someone else's
		// document id against its own bytes.
		if r.MultipartForm != nil && len(r.MultipartForm.File["file"]) > 0 {
			writeError(w, http.StatusBadRequest, "file is no longer accepted here: upload it to /v1/imports/preview and send the document_id it returns")
			return
		}

		documentID := r.FormValue("document_id")
		if documentID == "" {
			writeError(w, http.StatusBadRequest, "document_id is required")
			return
		}
		// Ahead of open, mirroring GetHandler: a malformed id must never reach
		// the document store's own 22P02 mapping.
		if _, err := uuid.Parse(documentID); err != nil {
			writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			return
		}

		doc, obj, err := open(r.Context(), documentID, "")
		if err != nil {
			// Mapped here, not through statusForErr: errors.Is(document.ErrNotFound,
			// ErrNotFound) is false, so a cross-tenant id would 500.
			switch {
			case errors.Is(err, document.ErrNotFound):
				writeError(w, http.StatusNotFound, "not found")
			case errors.Is(err, document.ErrValidation):
				writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			default:
				// Everything else, including a suspended caller's
				// db.ErrNotActiveMember, goes through statusForErr.
				status, msg := statusForErr(err)
				if status == http.StatusInternalServerError {
					log.ErrorContext(r.Context(), "importer: open source document", slog.Any("err", err))
				}
				writeError(w, status, msg)
			}
			return
		}
		if obj.Body != nil {
			defer func() { _ = obj.Body.Close() }()
		}

		// The stored row is what both paths resolve the format from, so preview
		// and import cannot disagree about the same upload.
		filename := derefOr(doc.Filename, "")
		format := detectFormat(filename, derefOr(doc.DeclaredContentType, ""))
		if format == "" {
			writeError(w, http.StatusBadRequest, "unrecognized file format")
			return
		}

		// Decode nil-dereferences inside io.ReadAll, so the read is guarded
		// like the Close above. TestImport_NilObjectBodyIs500.
		if obj.Body == nil {
			log.ErrorContext(r.Context(), "importer: source document opened with no body")
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		header, rows, facts, err := Decode(obj.Body, format)
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not decode uploaded file")
			return
		}

		// dry_run must not fail open ([review-authority]/[quarantine]: nothing
		// is written until the caller consents to a real import): only an
		// ABSENT query param, "true", or "false" are recognized -- a typo
		// like "1"/"TRUE"/"treu" 400s rather than silently falling through to
		// a REAL (persisting) import.
		var dryRun bool
		switch r.URL.Query().Get("dry_run") {
		case "", "false":
			dryRun = false
		case "true":
			dryRun = true
		default:
			writeError(w, http.StatusBadRequest, "dry_run must be true or false")
			return
		}

		res, err := imp(r.Context(), entityID, filename, documentID, mapping, header, rows, dryRun)
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

			RuleSetVersion:         res.RuleSetVersion,
			InvoicesClean:          res.InvoicesClean,
			InvoicesWithViolations: res.InvoicesWithViolations,
			InvoiceViolations:      res.InvoiceViolations,
		})
	}
}

// PreviewHandler returns POST /v1/imports/preview: the [upload-once] entry
// point. It is the ONLY route by which a source document reaches storage --
// it writes the bytes to object storage and a row to documents, then previews
// them ([preview-auth] still holds; [preview-stateless] does not, which is why
// it now takes a store and a logger). Flow: identity-first-401 -> upload-cap
// via http.MaxBytesReader ([upload-cap]) -> ParseMultipartForm (MaxBytesError
// -> 413, any other parse error -> 400) -> the "file" part -> store
// ([store-before-decode], so an unparseable file is still retrievable) ->
// format detection (unrecognized -> 400) -> Decode (undecodable -> 400) -> a
// 200 previewResponse. See handlers_preview_test.go for the PRV-01..PRV-16 map.
//
// The store call sits after r.FormFile, so an oversized body 413s before any
// object is written. The two 4xx paths BELOW it carry the document id
// (previewError); the four above it, and the new 500, do not.
//
// It reuses detectFormat/Decode/maxUploadBytes/maxMultipartMemory/nilIfEmpty/
// writeJSON/writeError and adds NO second parsing path on purpose
// ([preview-reuses-decode]): the columns this endpoint shows the user must be
// the same bytes the import path will later read, or client and server
// disagree about where the header is -- the exact failure [column-source]
// exists to prevent. PRV-16 pins that by comparing against a direct Decode.
func PreviewHandler(
	store func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (document.Document, error),
	log *slog.Logger,
) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Ordering is load-bearing: the identity check precedes the upload
		// cap, so an oversized body from an unauthenticated caller is 401,
		// not 413 -- we never read a stranger's bytes (PRV-01).
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

		file, fh, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "file is required")
			return
		}
		defer file.Close()

		// The RAW part filename: Service.Store owns the sanitization, and two
		// copies of a security coercion drift apart.
		doc, err := store(r.Context(), fh.Filename, fh.Header.Get("Content-Type"), fh.Size, file)
		if err != nil {
			// Same statusForErr idiom as GetHandler: a suspended caller's
			// db.ErrNotActiveMember must not fall to the 500 default.
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "importer: store source document", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		// Store leaves the reader at EOF; without this Decode reads zero bytes.
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			log.ErrorContext(r.Context(), "importer: rewind upload", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		format := detectFormat(fh.Filename, fh.Header.Get("Content-Type"))
		if format == "" {
			writeJSON(w, http.StatusBadRequest, previewError{Error: "unrecognized file format", DocumentID: doc.ID})
			return
		}

		header, rows, facts, err := Decode(file, format)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, previewError{Error: "could not decode uploaded file", DocumentID: doc.ID})
			return
		}

		// Decode returns nil (not an empty slice) for both header and rows on
		// an empty file (decode.go:102-104), and re-slicing a nil slice keeps
		// it nil -- encoding/json renders that as `null`. Columns/SampleRows
		// are contracted to ALWAYS be arrays (PRV-08), so coerce explicitly
		// rather than slicing.
		columns := header
		if columns == nil {
			columns = []string{}
		}
		sample := make([][]string, 0, maxSampleRows)
		for i, row := range rows {
			if i == maxSampleRows {
				break
			}
			// Verbatim: no copy, no padding, no trimming. Decode tolerates
			// ragged rows (FieldsPerRecord = -1) and the import path reads
			// cells by bounds-checked index, so padding here would show the
			// user data the importer will never see ([preview-samples]).
			sample = append(sample, row)
		}

		writeJSON(w, http.StatusOK, previewResponse{
			DocumentID: doc.ID,
			Format:     facts.Format,
			Delimiter:  nilIfEmpty(facts.Delimiter),
			Encoding:   nilIfEmpty(facts.Encoding),
			Columns:    columns,
			SampleRows: sample,
			// Every data row, not just the previewed ones.
			RowsTotal: len(rows),
		})
	}
}

// batchResponse is the GET /v1/imports/{id} success body (task-283):
// id/entity_id/status/rows_total/rows_valid/rows_invalid/errors/created_at
// are FROZEN at Finalize; rule_set_version is DERIVED (min() over the
// batch's invoices, task-283 R4). Errors is []RowError (never nil from
// Store.GetBatch), so a clean batch serializes "errors":[] -- NOT the same
// as POST /v1/imports's own contract, which renders "errors":null for a
// clean import (a flagged, deliberately-unfixed divergence between the two
// endpoints, task-283 trap 4).
//
// RuleSetVersion is a *int with NO omitempty: it must render an explicit
// JSON null when nothing under this batch was ever validated -- never
// omitted, never a false 0 ([Stage-1 F2]).
type batchResponse struct {
	ID          string     `json:"id"`
	EntityID    string     `json:"entity_id"`
	Filename    *string    `json:"filename"` // NEW (BULK-01-01). NO omitempty -- an unrecorded filename must serialise as an explicit null, never an absent key.
	Status      string     `json:"status"`
	RowsTotal   int        `json:"rows_total"`
	RowsValid   int        `json:"rows_valid"`
	RowsInvalid int        `json:"rows_invalid"`
	Errors      []RowError `json:"errors"`

	RuleSetVersion *int      `json:"rule_set_version"`
	CreatedAt      time.Time `json:"created_at"`
}

// GetHandler is GET /v1/imports/{id} (task-283): identity-first-401 -> a
// handler-level uuid.Parse guard (400 BEFORE the store is ever called -- a
// malformed id must never reach Store.GetBatch's own 22P02 mapping, which
// would put the package-internal "importer: validation" string on the wire;
// the wording here mirrors internal/invoice's ListHandler instead) -> get ->
// statusForErr -> 200 + batchResponse on success.
//
// A cross-tenant id and an id that exists nowhere BOTH resolve to
// ErrNotFound in the store, so both render the SAME 404 body -- that
// byte-equality, not the 404 status itself, is what proves there is no
// existence oracle (task-283 R5).
func GetHandler(get func(ctx context.Context, id string) (Batch, error), log *slog.Logger) http.HandlerFunc {
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

		batch, err := get(r.Context(), id)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "importer: get batch", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		// Second nil guard, after Store.GetBatch's own: this handler is a
		// factory over an arbitrary get func, and a nil []RowError marshals
		// to JSON null, not [] (spec 4).
		rowErrors := batch.Errors
		if rowErrors == nil {
			rowErrors = []RowError{}
		}

		writeJSON(w, http.StatusOK, batchResponse{
			ID:          batch.ID,
			EntityID:    batch.EntityID,
			Filename:    batch.Filename,
			Status:      batch.Status,
			RowsTotal:   batch.RowsTotal,
			RowsValid:   batch.RowsValid,
			RowsInvalid: batch.RowsInvalid,
			Errors:      rowErrors,

			RuleSetVersion: batch.RuleSetVersion,
			CreatedAt:      batch.CreatedAt,
		})
	}
}

// maxSheetRows caps the data rows the previewer receives. rows_total still
// reports the true count, so a truncated response never understates the file.
const maxSheetRows = 5000

// sheetResponse is the GET /v1/documents/{id}/sheet success body.
// Delimiter/Encoding mirror importResponse (nilIfEmpty -> JSON null for xlsx).
// Columns/Rows are contracted to always be arrays, never null.
type sheetResponse struct {
	Format       string     `json:"format"`
	Delimiter    *string    `json:"delimiter"`
	Encoding     *string    `json:"encoding"`
	Columns      []string   `json:"columns"`
	Rows         [][]string `json:"rows"`
	RowsTotal    int        `json:"rows_total"`
	RowsReturned int        `json:"rows_returned"`
	Truncated    bool       `json:"truncated"`
}

// SheetHandler is GET /v1/documents/{id}/sheet: a stored CSV/XLSX decoded
// through the SAME Decode the import path reads, so the evidence surface
// cannot disagree with the invoice it is evidence for. Adds no second parsing
// path, in Go or JS. Flow: identity-first-401 -> uuid guard -> open -> nil-body
// guard -> format detection (unrecognized -> 400) -> Decode (undecodable ->
// 400) -> row cap -> 200.
//
// The returned window is ALWAYS the first rows_returned data rows in Decode
// order, so rows[i] stays sheet row sheetRow(i) even when truncated.
// Numbering from physical lines would skew: encoding/csv drops blank lines and
// a quoted cell can span several (TestSheetHandler_RowNumberingMatchesImporterSheetRow).
func SheetHandler(
	open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error),
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

		doc, obj, err := open(r.Context(), id, "")
		if err != nil {
			// Mapped here, not through statusForErr: errors.Is(document.ErrNotFound,
			// ErrNotFound) is false, so a cross-tenant id would 500 -- an
			// existence oracle by status code.
			switch {
			case errors.Is(err, document.ErrNotFound):
				writeError(w, http.StatusNotFound, "not found")
			case errors.Is(err, document.ErrValidation):
				writeError(w, http.StatusBadRequest, "id must be a well-formed uuid")
			default:
				// Everything else, including a suspended caller's
				// db.ErrNotActiveMember, goes through statusForErr.
				status, msg := statusForErr(err)
				if status == http.StatusInternalServerError {
					log.ErrorContext(r.Context(), "importer: open source document", slog.Any("err", err))
				}
				writeError(w, status, msg)
			}
			return
		}
		if obj.Body != nil {
			defer func() { _ = obj.Body.Close() }()
		}

		// Guarded BEFORE detectFormat, unlike CreateHandler: a document that is
		// both unrecognized AND missing its bytes is a server fault, not a bad
		// file. Decode nil-dereferences inside io.ReadAll.
		if obj.Body == nil {
			log.ErrorContext(r.Context(), "importer: source document opened with no body")
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		format := detectFormat(derefOr(doc.Filename, ""), derefOr(doc.DeclaredContentType, ""))
		if format == "" {
			writeError(w, http.StatusBadRequest, "unrecognized file format")
			return
		}

		header, rows, facts, err := Decode(obj.Body, format)
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not decode uploaded file")
			return
		}

		// Decode returns nil for both on an empty file; a nil slice marshals to
		// `null`, and columns/rows are contracted to be arrays (PRV-08).
		columns := header
		if columns == nil {
			columns = []string{}
		}

		total := len(rows)
		n := total
		if n > maxSheetRows {
			n = maxSheetRows
		}
		out := make([][]string, 0, n)
		for _, row := range rows[:n] {
			// excelize materializes a gap row as a nil []string, which would
			// marshal to a `null` ELEMENT inside rows and crash any client doing
			// row.map(...). Coercion, not padding: zero cells before, zero after.
			if row == nil {
				row = []string{}
			}
			// Otherwise verbatim: ragged rows stay ragged, no copy, no trimming.
			out = append(out, row)
		}

		writeJSON(w, http.StatusOK, sheetResponse{
			Format:    facts.Format,
			Delimiter: nilIfEmpty(facts.Delimiter),
			Encoding:  nilIfEmpty(facts.Encoding),
			Columns:   columns,
			Rows:      out,
			// The TRUE total, even when truncated; rows_returned is read off the
			// slice itself so it can never report a count it did not send.
			RowsTotal:    total,
			RowsReturned: len(out),
			Truncated:    total > maxSheetRows,
		})
	}
}

// statusForErr maps a service error to the HTTP status + message this
// handler writes to the response, mirroring internal/invoice's own
// statusForErr: db.ErrNotActiveMember is 403, ErrValidation is 400 with the
// wrapped message, ErrNotFound is 404, anything else is 500 with a generic
// body (never leaking internals).
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
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
