// M4-02-03 (task-98): the four HTTP handlers over internal/invoice's Store --
// POST /v1/invoices, GET /v1/invoices/{id}, GET /v1/invoices, POST
// /v1/invoices/{id}/transitions -- following internal/portfolio/portfolio.go's
// CreateHandler/GetHandler/ListHandler idiom (identity-first-401 -> decode/
// validate -> call store -> statusForErr -> JSON, shared {"error":"..."}
// envelope) plus a new TransitionHandler ([D12]: a single POST .../transitions
// with a {"target":...} body, not per-target sub-paths).
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- wire request/response types --------------------------------------------
//
// These are named distinctly from handlers_test.go's own test-local wire
// types (createInvoiceRequest/lineItemWire/transitionRequest/
// listPaginationWire/listInvoicesResponse) to avoid a same-package type
// clash -- the JSON shapes match (same tags), only the Go type names differ.
// Response bodies are the domain Invoice/[]Invoice types directly (portfolio
// precedent: writeJSON(w, status, entity)), so no separate response DTO is
// needed for Create/Get/Transition.

// lineItemReq is one entry of createRequest.LineItems. LineNo is deliberately
// absent -- it is system-assigned 1..N by array position (Store.Create,
// [D10]), never caller-supplied.
type lineItemReq struct {
	Description *string `json:"description"`
	Quantity    *string `json:"quantity"`
	UnitPrice   *string `json:"unit_price"`
	LineTotal   *string `json:"line_total"`
	LineTax     *string `json:"line_tax"`
}

// createRequest is the POST /v1/invoices wire body (snake_case JSON tags).
type createRequest struct {
	EntityID      string        `json:"entity_id"`
	InvoiceNumber string        `json:"invoice_number"`
	IssueDate     *time.Time    `json:"issue_date"`
	SupplierTIN   *string       `json:"supplier_tin"`
	SupplierName  *string       `json:"supplier_name"`
	BuyerTIN      *string       `json:"buyer_tin"`
	BuyerName     *string       `json:"buyer_name"`
	Currency      *string       `json:"currency"`
	Subtotal      *string       `json:"subtotal"`
	VAT           *string       `json:"vat"`
	Total         *string       `json:"total"`
	LineItems     []lineItemReq `json:"line_items"`
}

// transitionReq is the POST /v1/invoices/{id}/transitions wire body ([D12]:
// a single endpoint, {"target":...}, not per-target sub-paths).
type transitionReq struct {
	Target string `json:"target"`
}

// listPagination is the "pagination" object in ListHandler's response
// envelope: the effective limit/offset applied (after defaulting/clamping)
// plus the total filtered count across all pages ([D8]).
type listPagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total"`
}

// listResponse is the GET /v1/invoices response body:
// {"invoices":[...],"pagination":{...}}. Invoices is []Invoice (never a nil
// slice from Store.List), so an empty result serializes "invoices":[] rather
// than "invoices":null.
type listResponse struct {
	Invoices   []Invoice      `json:"invoices"`
	Pagination listPagination `json:"pagination"`
}

// --- handlers ----------------------------------------------------------------

// CreateHandler returns POST /v1/invoices. Identity-first-401, then decodes
// the snake_case wire body (400 on decode error), 400 if entity_id or
// invoice_number is blank (before create ever runs -- Store.Create's own
// pre-tx guard is the defense-in-depth backstop for the importer-reuse path,
// [D3]), calls create, maps errors via statusForErr, 201 + Invoice (with
// line_items) on success.
func CreateHandler(create func(ctx context.Context, in CreateInput) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req createRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.EntityID == "" {
			writeError(w, http.StatusBadRequest, "entity_id is required")
			return
		}
		if req.InvoiceNumber == "" {
			writeError(w, http.StatusBadRequest, "invoice_number is required")
			return
		}

		lineItems := make([]LineItemInput, len(req.LineItems))
		for i, li := range req.LineItems {
			lineItems[i] = LineItemInput{
				Description: li.Description,
				Quantity:    li.Quantity,
				UnitPrice:   li.UnitPrice,
				LineTotal:   li.LineTotal,
				LineTax:     li.LineTax,
			}
		}

		inv, err := create(r.Context(), CreateInput{
			EntityID:      req.EntityID,
			InvoiceNumber: req.InvoiceNumber,
			IssueDate:     req.IssueDate,
			SupplierTIN:   req.SupplierTIN,
			SupplierName:  req.SupplierName,
			BuyerTIN:      req.BuyerTIN,
			BuyerName:     req.BuyerName,
			Currency:      req.Currency,
			Subtotal:      req.Subtotal,
			VAT:           req.VAT,
			Total:         req.Total,
			LineItems:     lineItems,
		})
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: create", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusCreated, inv)
	}
}

// GetHandler returns GET /v1/invoices/{id}. Same identity-first-401 order as
// CreateHandler, reading r.PathValue("id"); 404 via ErrNotFound (covers both
// a genuinely unknown id and a cross-tenant one, RLS-scoped 0-rows), 200 +
// Invoice (with line_items, [D7]) on success.
func GetHandler(get func(ctx context.Context, id string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		inv, err := get(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: get", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, inv)
	}
}

// ListHandler returns GET /v1/invoices. Same identity-first-401 order as
// Create/GetHandler. Query params (portfolio's exact defaulting/clamping
// rules, [D8]): limit (default 50, non-integer -> 400, <1 -> 400, >200
// clamps down to 200), offset (default 0, non-integer or negative -> 400).
// No status/entity filters ([D8]) -- unlike portfolio's ListHandler, there is
// no q/status parsing here.
func ListHandler(list func(ctx context.Context, f ListFilter) ([]Invoice, int, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		query := r.URL.Query()

		limit := 50
		if raw := query.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "limit must be an integer")
				return
			}
			limit = n
		}
		if limit > 200 {
			limit = 200
		} else if limit < 1 {
			writeError(w, http.StatusBadRequest, "limit must be >= 1")
			return
		}

		offset := 0
		if raw := query.Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "offset must be an integer")
				return
			}
			offset = n
		}
		if offset < 0 {
			writeError(w, http.StatusBadRequest, "offset must be >= 0")
			return
		}

		filter := ListFilter{Limit: limit, Offset: offset}

		items, total, err := list(r.Context(), filter)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: list", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, listResponse{
			Invoices:   items,
			Pagination: listPagination{Limit: filter.Limit, Offset: filter.Offset, Total: total},
		})
	}
}

// TransitionHandler returns POST /v1/invoices/{id}/transitions ([D12]: a
// single endpoint, {"target":...}). Same identity-first-401 order, decodes
// the body (400 on decode error), 400 "unknown status" if target is not one
// of the 7 canonical Status values -- WITHOUT ever calling transition (the
// store's own legality/redundancy checks are a distinct, later 409) -- else
// calls transition, maps ErrNotFound/ErrRedundantTransition/
// ErrIllegalTransition (and anything else) via statusForErr, 200 + updated
// Invoice on success.
func TransitionHandler(transition func(ctx context.Context, id string, target Status) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req transitionReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		target := Status(req.Target)
		if !target.valid() {
			writeError(w, http.StatusBadRequest, "unknown status")
			return
		}

		inv, err := transition(r.Context(), r.PathValue("id"), target)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: transition", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, inv)
	}
}

// statusForErr maps a store/domain error to the HTTP status + message the
// handlers above write to the response ([D4]/[D12] error-map table).
// db.ErrNoTenant is 401 (fail-closed, missing identity never reaches here in
// practice since every handler checks identity first, but this is the
// defense-in-depth mirror of portfolio's own statusForErr); ErrValidation is
// 400 with the wrapped message; ErrNotFound is 404; ErrDuplicateNumber/
// ErrRedundantTransition/ErrIllegalTransition are 409; anything else
// (including a 22P02 malformed-numeric-input pgconn error, [D15] accepted
// residual) is 500 with a generic body -- this helper never leaks internals
// into the response. Logging the unrecognized (500) case via slog is the
// caller's responsibility, since only the caller knows the operation name to
// log.
func statusForErr(err error) (status int, msg string) {
	switch {
	case errors.Is(err, db.ErrNoTenant):
		return http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, ErrValidation):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrDuplicateNumber):
		return http.StatusConflict, "duplicate invoice number"
	case errors.Is(err, ErrRedundantTransition):
		return http.StatusConflict, "redundant transition"
	case errors.Is(err, ErrIllegalTransition):
		return http.StatusConflict, "illegal transition"
	default:
		return http.StatusInternalServerError, "internal server error"
	}
}

// writeJSON and writeError mirror internal/portfolio/portfolio.go's helpers
// of the same name verbatim (the shared {"error":"..."} envelope convention).
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
