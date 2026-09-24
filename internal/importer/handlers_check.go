// handlers_check.go: POST /v1/imports/check-mapping.
package importer

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/SimonOsipov/invoice-os/internal/document"
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

// CheckMappingHandler serves POST /v1/imports/check-mapping.
func CheckMappingHandler(
	open func(ctx context.Context, id, rangeHeader string) (document.Document, document.Object, error),
	checker MappingChecker,
	log *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, checkMappingResponse{Doubted: []string{}})
	}
}
