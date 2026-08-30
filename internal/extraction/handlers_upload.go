// handlers_upload.go: POST /v1/documents. Reached through the gateway as
// /api/submission/v1/documents.
//
// Stage-2.5 stub: UploadHandler answers 501 on every path. Stage 3 owns the ordering
// identity 401 -> MaxBytesReader -> ParseMultipartForm -> FormFile -> classify -> refuse ->
// store -> enqueue -> 201.
package extraction

import (
	"context"
	"io"
	"log/slog"
	"net/http"
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
// here may be one the server cannot populate at response time (AC-7).
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
func UploadHandler(
	store func(ctx context.Context, filename, contentType string, size int64, body io.ReadSeeker) (StoredDocument, error),
	enqueue func(ctx context.Context, documentID string) (skipped bool, err error),
	log *slog.Logger,
) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	_, _ = store, enqueue
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
