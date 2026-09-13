// handlers_document.go: EXTR-06-06 (task-766) -- POST /v1/imports/document, the document-import
// route's HTTP layer over Service.ImportDocument. JSON body, not multipart: the bytes are
// already stored (mirrors internal/invoice/handlers.go:156-178's CreateHandler shape, not the
// spreadsheet CreateHandler's multipart/mapping/dry_run one -- this route carries neither, D-3/
// D-4). No dry_run means no 200 branch: a quarantine (imp returns err == nil) is still 201.
// Also carries GET .../document/reading and POST .../document/invoice.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// maxCreateDocumentBodyBytes bounds the request body BEFORE it is decoded (CodeRabbit,
// CWE-400): the body carries only entity_id and document_id, two uuids.
const maxCreateDocumentBodyBytes = 4 * 1024

// CarriedReading is a no-number document's stored reading, carried into manual entry.
type CarriedReading struct {
	DocumentID      string        `json:"document_id"`
	ExtractionJobID string        `json:"extraction_job_id"`
	IssueDate       *string       `json:"issue_date"` // YYYY-MM-DD
	BuyerTIN        *string       `json:"buyer_tin"`
	BuyerName       *string       `json:"buyer_name"`
	Currency        *string       `json:"currency"`
	Subtotal        *string       `json:"subtotal"`
	VAT             *string       `json:"vat"`
	Total           *string       `json:"total"`
	LineItems       []CarriedLine `json:"line_items"` // never nil
}

type CarriedLine struct {
	Description *string `json:"description"`
	Quantity    *string `json:"quantity"`
	UnitPrice   *string `json:"unit_price"`
	LineTotal   *string `json:"line_total"`
	LineTax     *string `json:"line_tax"`
}

type readingResponse struct {
	Reading *CarriedReading `json:"reading"`
}

type supplyRequest struct {
	EntityID      string `json:"entity_id"`
	DocumentID    string `json:"document_id"`
	InvoiceNumber string `json:"invoice_number"`
}

const (
	readingNotCarriedReason    = "This document's reading cannot be carried into an invoice. Enter this invoice by hand."
	documentAlreadyFiledReason = "An invoice has already been filed from this document."
)

// ReadingHandler answers GET /v1/imports/document/reading?document_id=<uuid>. It never answers
// 404 for "nothing to carry" -- the hand-off serves dead-lettered and poor-scan documents too,
// and a uniform null keeps the route from becoming an existence oracle.
func ReadingHandler(read func(ctx context.Context, documentID string) (*CarriedReading, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		documentID := r.URL.Query().Get("document_id")
		if documentID == "" {
			writeError(w, http.StatusBadRequest, "document_id is required")
			return
		}
		parsed, err := uuid.Parse(documentID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			return
		}

		reading, err := read(r.Context(), parsed.String())
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "importer: read carried reading", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, readingResponse{Reading: reading})
	}
}

// SupplyNumberHandler answers POST /v1/imports/document/invoice: an operator-typed number files
// the carried reading. Errors map inline rather than through a new *StatusForErr func --
// TestHandlerMappingEveryRefusalSiteNamesNotActiveMember requires that shape to name
// db.ErrNotActiveMember in its own body, which only statusForErr's default arm does.
func SupplyNumberHandler(supply func(ctx context.Context, entityID, documentID, invoiceNumber string) (invoice.Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxCreateDocumentBodyBytes)
		var req supplyRequest
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
		entity, err := uuid.Parse(req.EntityID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "entity_id must be a well-formed uuid")
			return
		}
		if req.DocumentID == "" {
			writeError(w, http.StatusBadRequest, "document_id is required")
			return
		}
		doc, err := uuid.Parse(req.DocumentID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "document_id must be a well-formed uuid")
			return
		}
		number := strings.TrimSpace(req.InvoiceNumber)
		if number == "" {
			writeError(w, http.StatusBadRequest, "invoice_number is required")
			return
		}

		inv, err := supply(r.Context(), entity.String(), doc.String(), number)
		if err != nil {
			var status int
			var msg string
			switch {
			case errors.Is(err, invoice.ErrDuplicateNumber):
				status, msg = http.StatusConflict, invoice.NumberTakenReason
			case errors.Is(err, ErrReadingNotCarried):
				status, msg = http.StatusConflict, readingNotCarriedReason
			case errors.Is(err, ErrDocumentAlreadyFiled):
				status, msg = http.StatusConflict, documentAlreadyFiledReason
			case errors.Is(err, invoice.ErrValidation):
				status = http.StatusBadRequest
				msg, _ = domainCreateErrorMessage(err)
			default:
				status, msg = statusForErr(err) // 403 suspended, 404 no reading, 400 importer.ErrValidation, 500
			}
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "importer: supply invoice number", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusCreated, inv)
	}
}

// createDocumentRequest is the POST /v1/imports/document JSON body.
type createDocumentRequest struct {
	EntityID   string `json:"entity_id"`
	DocumentID string `json:"document_id"`
}

// CreateDocumentHandler returns POST /v1/imports/document: identity-first-401 -> body cap
// (413) -> json.Decode (400) -> entity_id/document_id presence+uuid guards (400) -> imp ->
// statusForErr -> 201 importResponse (format "document", delimiter/encoding null).
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

		r.Body = http.MaxBytesReader(w, r.Body, maxCreateDocumentBodyBytes)
		var req createDocumentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// The 413 check MUST precede the generic 400: MaxBytesReader surfaces the cap
			// as a *http.MaxBytesError from Decode, otherwise indistinguishable from
			// malformed JSON (internal/validation/handlers.go:217-225).
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
