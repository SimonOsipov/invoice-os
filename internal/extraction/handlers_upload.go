// handlers_upload.go: POST /v1/documents. Reached through the gateway as
// /api/submission/v1/documents.
package extraction

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// maxUploadBytes bounds the whole request body; documents.size_bytes CHECKs <= 15728640.
// maxMultipartMemory is ParseMultipartForm's in-memory threshold, an unrelated knob.
// Both mirror internal/importer/handlers.go:34,40 — per-package copies, per convention.
const (
	maxUploadBytes     = 15 << 20 // 15 MiB
	maxMultipartMemory = 8 << 20  // 8 MiB
)

// uploadRefusalMessage is the 400 body for a file type this route will not read.
const uploadRefusalMessage = "this file type cannot be read here"

// StoredDocument is what the store seam reports back. internal/extraction never imports
// internal/document (deps_test.go's fence), so cmd/submission adapts document.Service.Store
// into this shape, mirroring newDocumentOpener.
type StoredDocument struct {
	ID          string
	Filename    string
	ContentType string
	SizeBytes   int64
	Reused      bool
}

// uploadResponse is the 201 body. Every field is a projection of the stored row: no field
// here may be one the server cannot populate at response time (AC-7). No omitempty on
// Reused — a false verdict is an answer, and dropping it reads as "unknown".
type uploadResponse struct {
	DocumentID  string `json:"document_id"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
	Reused      bool   `json:"reused"`
}

// UploadHandler returns POST /v1/documents. store and enqueue are injected closures, so the
// handler is drivable with no pool and the enqueue reaches EnqueueExtraction as a bare call
// rather than a second seam (TestExtractionExposesExactlyOneEnqueueSeam).
//
// The ordering is the contract: identity 401 -> MaxBytesReader -> ParseMultipartForm ->
// FormFile -> classify -> refuse -> store -> enqueue -> 201. Classification sits ABOVE the
// store, inverting POST /v1/imports/preview (handlers.go:397,416), which stores bytes it then
// refuses to read (TestUploadHandler_ClassifyPrecedesStore).
func UploadHandler(
	store func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (StoredDocument, error),
	enqueue func(ctx context.Context, documentID string) (skipped bool, err error),
	log *slog.Logger,
) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// Above the cap on purpose: a stranger's bytes are never read, so an oversized
		// unauthenticated body is 401, not 413 (PRV-01's shape).
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

		contentType := classifyDocumentType(fh.Filename, fh.Header.Get("Content-Type"))
		if contentType == "" {
			writeError(w, http.StatusBadRequest, uploadRefusalMessage)
			return
		}

		// The RAW part filename: Service.Store owns the sanitization, and two copies of a
		// security coercion drift apart. The CANONICAL content type, though — the row must
		// not record whatever header the client chose.
		doc, err := store(r.Context(), fh.Filename, contentType, fh.Size, file)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "extraction: store document", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		// EnqueueExtraction accepts a blank id and burns the tenant's permanent "extract:"
		// key on nothing, so this is the only place that can refuse one.
		if doc.ID == "" {
			log.ErrorContext(r.Context(), "extraction: store returned no document id")
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		// The STORED id, never one read off the request: the enqueue seam does no ownership
		// check (TestUploadHandler_EnqueuesTheStoredIdNotACallerSuppliedOne).
		if _, err := enqueue(r.Context(), doc.ID); err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "extraction: enqueue extraction", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusCreated, uploadResponse{
			DocumentID:  doc.ID,
			Filename:    doc.Filename,
			SizeBytes:   doc.SizeBytes,
			ContentType: doc.ContentType,
			Reused:      doc.Reused,
		})
	}
}
