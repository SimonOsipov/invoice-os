// handlers_suggest.go: POST /v1/imports/suggest-mapping (§2/§4). Declarations only -- every
// body returns a zero value. The executor fills in the ladder, the guard wiring and the AI call.
package importer

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/ai"
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

// askMapping answers nil on off, a failed call, and a refused envelope.
func askMapping(ctx context.Context, s MappingSuggester, window [][]string) map[string]any {
	return nil
}

// SuggestMappingHandler serves POST /v1/imports/suggest-mapping (§4).
func SuggestMappingHandler(
	open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error),
	lookup func(ctx context.Context, entityID string, header []string) (*SavedMapping, error),
	suggester MappingSuggester,
	log *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, suggestMappingResponse{})
	}
}
