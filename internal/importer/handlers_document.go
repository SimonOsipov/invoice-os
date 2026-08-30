// handlers_document.go: EXTR-06-06 (task-766) -- POST /v1/imports/document, the document-import
// route's HTTP layer over Service.ImportDocument. JSON body, not multipart: the bytes are
// already stored (mirrors internal/invoice/handlers.go:156-178's CreateHandler shape, not the
// spreadsheet CreateHandler's multipart/mapping/dry_run one -- this route carries neither, D-3/
// D-4). No dry_run means no 200 branch: a quarantine (imp returns err == nil) is still 201.
package importer

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// createDocumentRequest is the POST /v1/imports/document JSON body.
type createDocumentRequest struct {
	EntityID   string `json:"entity_id"`
	DocumentID string `json:"document_id"`
}

// CreateDocumentHandler returns POST /v1/imports/document: identity-first-401 -> json.Decode
// (400) -> entity_id/document_id presence+uuid guards (400) -> imp -> statusForErr -> 201
// importResponse (format "document", delimiter/encoding null).
func CreateDocumentHandler(
	imp func(ctx context.Context, entityID, documentID string) (BatchResult, error),
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

		var req createDocumentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

		res, err := imp(r.Context(), req.EntityID, req.DocumentID)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "importer: import document", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusCreated, importResponse{
			ID:                  res.ID,
			Status:              res.Status,
			Format:              "document",
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
