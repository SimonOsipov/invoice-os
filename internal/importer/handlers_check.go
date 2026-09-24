// handlers_check.go: POST /v1/imports/check-mapping.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// checkMappingRequest is POST /v1/imports/check-mapping's JSON body.
type checkMappingRequest struct {
	DocumentID string            `json:"document_id"`
	Mapping    map[string]string `json:"mapping"`
}

// checkMappingResponse is POST /v1/imports/check-mapping's 200 body; Doubted is never null.
type checkMappingResponse struct {
	Doubted []string `json:"doubted"`
}

// CheckMappingHandler serves POST /v1/imports/check-mapping: SuggestMappingHandler's
// open/decode ladders, then one Ask over the same ten-row window.
func CheckMappingHandler(
	open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error),
	checker MappingChecker,
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

		r.Body = http.MaxBytesReader(w, r.Body, maxCreateDocumentBodyBytes)
		var req checkMappingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.DocumentID == "" {
			writeError(w, http.StatusBadRequest, "document_id is required")
			return
		}
		if _, err := uuid.Parse(req.DocumentID); err != nil {
			writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			return
		}
		// Sorted so a request with two bad keys always names the same one.
		fields := make([]string, 0, len(req.Mapping))
		for field := range req.Mapping {
			fields = append(fields, field)
		}
		slices.Sort(fields)
		for _, field := range fields {
			if !canonicalFields[field] {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("mapping key %q is not a recognized canonical field", field))
				return
			}
			if req.Mapping[field] == "" {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("mapping value for %q is empty", field))
				return
			}
		}

		// Off answers before the open, so production never reads a document for this route.
		if checker == nil || !checker.Enabled() || len(req.Mapping) == 0 {
			writeJSON(w, http.StatusOK, checkMappingResponse{Doubted: []string{}})
			return
		}

		doc, obj, err := open(r.Context(), req.DocumentID, "")
		if err != nil {
			// errors.Is(document.ErrNotFound, ErrNotFound) is false, so statusForErr would 500 a cross-tenant id.
			switch {
			case errors.Is(err, document.ErrNotFound):
				writeError(w, http.StatusNotFound, "not found")
			case errors.Is(err, document.ErrValidation):
				writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			default:
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

		// Read apart from Decode so a storage failure is a 500, not a 400.
		raw, err := io.ReadAll(obj.Body)
		if err != nil {
			log.ErrorContext(r.Context(), "importer: read source document", slog.Any("err", err))
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		hdr1, rows1, _, err := Decode(bytes.NewReader(raw), format)
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not decode uploaded file")
			return
		}

		// An empty header gives a nil window, which checkPlacements answers without an Ask.
		doubted := checkPlacements(r.Context(), checker, suggestWindow(hdr1, rows1), req.Mapping)
		writeJSON(w, http.StatusOK, checkMappingResponse{Doubted: doubted})
	}
}
