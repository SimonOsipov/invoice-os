// handlers_suggest.go: POST /v1/imports/suggest-mapping.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// suggestMappingRequest is POST /v1/imports/suggest-mapping's JSON body.
type suggestMappingRequest struct {
	EntityID   string `json:"entity_id"`
	DocumentID string `json:"document_id"`
}

// suggestMappingResponse is POST /v1/imports/suggest-mapping's 200 body (§2).
type suggestMappingResponse struct {
	Source     string            `json:"source"`
	HeaderRow  int               `json:"header_row"`
	Columns    []string          `json:"columns"`
	SampleRows [][]string        `json:"sample_rows"`
	RowsTotal  int               `json:"rows_total"`
	Mapping    map[string]string `json:"mapping"`
	SavedAt    *time.Time        `json:"saved_at"`
}

// MappingSuggester is the slice of *ai.Client this package uses; nil is off. Mirrors
// internal/extraction's AIReader.
type MappingSuggester interface {
	Enabled() bool
	Call(ctx context.Context, req ai.Request) (map[string]any, error)
}

// askMapping answers nil on off, a failed call, and a refused envelope: every failure has the
// same consequence, so there is no second return value.
func askMapping(ctx context.Context, s MappingSuggester, window [][]string) map[string]any {
	if s == nil || !s.Enabled() {
		return nil
	}
	ans, err := s.Call(ctx, ai.Request{
		Purpose:    ai.PurposeSpreadsheet,
		System:     mappingSystem,
		Text:       mappingPromptText(window),
		SchemaName: mappingSchemaName,
		Schema:     mappingSchema,
	})
	if err != nil {
		return nil
	}
	return ans
}

// coerceColumns and coerceSampleRows mirror PreviewHandler's own coercions: §2 forbids
// a null columns/sample_rows array, and excelize's gap row comes back nil
// (TestSuggestHandler_EmptyFileAnswersEmptyArraysNotNull, TestSuggestHandler_AGapRowIsAnEmptyArrayNotNull).
func coerceColumns(header []string) []string {
	if header == nil {
		return []string{}
	}
	return header
}

func coerceSampleRows(rows [][]string) [][]string {
	sample := make([][]string, 0, maxSampleRows)
	for i, row := range rows {
		if i == maxSampleRows {
			break
		}
		if row == nil {
			row = []string{}
		}
		sample = append(sample, row)
	}
	return sample
}

// respondSuggestion writes the 200 body, coercing header/rows/mapping to §2's never-null shapes.
func respondSuggestion(w http.ResponseWriter, source string, headerRow int, header []string, rows [][]string, mapping map[string]string, savedAt *time.Time) {
	if mapping == nil {
		mapping = map[string]string{}
	}
	writeJSON(w, http.StatusOK, suggestMappingResponse{
		Source:     source,
		HeaderRow:  headerRow,
		Columns:    coerceColumns(header),
		SampleRows: coerceSampleRows(rows),
		RowsTotal:  len(rows),
		Mapping:    mapping,
		SavedAt:    savedAt,
	})
}

// SuggestMappingHandler serves POST /v1/imports/suggest-mapping: the request ladder is
// CreateDocumentHandler's (POST + JSON body, two uuids), the open/decode ladder is
// SavedMappingHandler's.
func SuggestMappingHandler(
	open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error),
	lookup func(ctx context.Context, entityID string, header []string) (*SavedMapping, error),
	suggester MappingSuggester,
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
		var req suggestMappingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds the size limit")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.EntityID == "" {
			writeError(w, http.StatusBadRequest, "entity_id is required")
			return
		}
		if _, err := uuid.Parse(req.EntityID); err != nil {
			writeError(w, http.StatusBadRequest, "entity_id must be a well-formed uuid")
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

		doc, obj, err := open(r.Context(), req.DocumentID, "")
		if err != nil {
			// Mapped here, not through statusForErr: errors.Is(document.ErrNotFound,
			// ErrNotFound) is false, so a cross-tenant id would 500.
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

		// obj.Body is handed back unread and not seekable; buffer once, decode from memory
		// since this endpoint decodes up to twice (TestSuggestHandler_ReDecodesAtTheAnsweredHeaderRow).
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

		window := suggestWindow(hdr1, rows1)
		if len(window) == 0 {
			// Nothing to show the model; only a blank answer is possible
			// (TestSuggestHandler_EmptyFileAnswersEmptyArraysNotNull).
			respondSuggestion(w, "none", defaultHeaderRow, hdr1, rows1, nil, nil)
			return
		}

		ans := askMapping(r.Context(), suggester, window)
		headerRow := guardHeaderRow(ans, len(window))

		header, rows := hdr1, rows1
		if headerRow != defaultHeaderRow {
			h, rs, _, derr := DecodeFrom(bytes.NewReader(raw), format, headerRow)
			// The AI's row choice is never the caller's fault: a re-parse failure or a
			// blank header at that row falls back to row 1 rather than 400ing
			// (TestSuggestHandler_AnUndecodableTailAtTheAnsweredRowFallsBackToRowOne,
			// TestSuggestHandler_ABlankHeaderAtTheAnsweredRowFallsBackToRowOne).
			if derr != nil || len(h) == 0 {
				headerRow = defaultHeaderRow
				header, rows = hdr1, rows1
			} else {
				header, rows = h, rs
			}
		}

		mapping := guardPlacements(ans, header)

		saved, err := lookup(r.Context(), req.EntityID, header)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "importer: lookup saved mapping", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		if saved != nil {
			respondSuggestion(w, "saved", headerRow, header, rows, saved.Mapping, &saved.SavedAt)
			return
		}

		source := "ai"
		if len(mapping) == 0 && headerRow == defaultHeaderRow {
			source = "none"
		}
		respondSuggestion(w, source, headerRow, header, rows, mapping, nil)
	}
}
