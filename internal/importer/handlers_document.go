// handlers_document.go: EXTR-06-06 (task-766) Mode A stub. CreateDocumentHandler exists only
// so handlers_document_test.go compiles -- no decode, no auth check, no imp call yet. The real
// POST /v1/imports/document flow (identity-first-401 -> json.Decode -> entity_id/document_id
// uuid guards -> imp -> statusForErr -> 201 importResponse) is EXTR-06-06's implementation,
// not this RED pass.
package importer

import (
	"context"
	"log/slog"
	"net/http"
)

// CreateDocumentHandler returns POST /v1/imports/document. Stub: always 501, imp untouched.
func CreateDocumentHandler(
	imp func(ctx context.Context, entityID, documentID string) (BatchResult, error),
	log *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}
}
