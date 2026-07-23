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

	"github.com/google/uuid"

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

// editReq is the PATCH /v1/invoices/{id} wire body (M4-05-03, [A1]): the 9
// optional header MBS-content fields, snake_case tags IDENTICAL to
// createRequest's own (above) minus entity_id/invoice_number/line_items --
// identity and lifecycle are not the edit's job ([D9]).
type editReq struct {
	IssueDate    *time.Time `json:"issue_date"`
	SupplierTIN  *string    `json:"supplier_tin"`
	SupplierName *string    `json:"supplier_name"`
	BuyerTIN     *string    `json:"buyer_tin"`
	BuyerName    *string    `json:"buyer_name"`
	Currency     *string    `json:"currency"`
	Subtotal     *string    `json:"subtotal"`
	VAT          *string    `json:"vat"`
	Total        *string    `json:"total"`
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

// getResponse is the GET /v1/invoices/{id} response body: Invoice embedded
// (keeping every existing field's name/type/position), plus one additive
// sibling key, rule_set_version -- mirrors validateResponse below (M4-09-01,
// [read-shape-getresponse-wrapper]). Not added to the Invoice domain struct
// itself: Invoice is shared by List, which must NOT gain this key.
//
// RuleSetVersion is a *int with NO omitempty: it must render an explicit
// JSON null when the invoice was never validated (Store.Get's zero-value
// convention) -- never omitted, never a false 0
// (TestGetHandler_RuleSetVersionMarshalsNull).
type getResponse struct {
	Invoice
	RuleSetVersion *int `json:"rule_set_version"`
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

		writeJSON(w, http.StatusOK, getResponse{Invoice: inv, RuleSetVersion: inv.RuleSetVersion})
	}
}

// ListHandler returns GET /v1/invoices. Same identity-first-401 order as
// Create/GetHandler. Query params (portfolio's exact defaulting/clamping
// rules, [D8]): limit (default 50, non-integer -> 400, <1 -> 400, >200
// clamps down to 200), offset (default 0, non-integer or negative -> 400),
// needs_attention (M4-09-02, AC #5, [needs-attention-param-strictness]:
// absent defaults to false/unfiltered; parsed via strconv.ParseBool, so
// "true"/"false"/"1"/"0"/etc. all work; an unparseable value 400s BEFORE the
// store is ever called, mirroring the limit/offset 400 contract). No
// status/entity filters beyond that ([D8], [entity-id-cut]) -- unlike
// portfolio's ListHandler, there is no q/status parsing here.
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

		needsAttention := false
		if raw := query.Get("needs_attention"); raw != "" {
			b, err := strconv.ParseBool(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "needs_attention must be a boolean")
				return
			}
			needsAttention = b
		}

		filter := ListFilter{Limit: limit, Offset: offset, NeedsAttention: needsAttention}

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
// store's own legality/redundancy checks are a distinct, later 409) -- then
// 409 if target is validated ([validated-is-earned] [R1]: that status is
// reachable ONLY through POST /v1/invoices/{id}/validate, the gate) -- else
// calls transition, maps ErrNotFound/ErrRedundantTransition/
// ErrIllegalTransition (and anything else) via statusForErr, 200 + updated
// Invoice on success.
//
// The validated guard lives HERE and not in Store.Transition on purpose. The
// two placements have IDENTICAL production reach -- cmd/invoice/main.go is
// Store.Transition's sole production consumer, and it passes the method value
// straight into this factory -- but the store placement additionally destroys
// the state-machine suite: legalTransitions[StatusDraft] = {StatusValidated}
// makes validated the only reachable second state, so the transition tests
// route through it. Trading that proof for a guard with no additional reach is
// a bad trade. Residual risk, named: a future IN-PROCESS caller of
// Store.Transition could promote without validating -- there is none today.
// Hand-off: if M4-05 adds a second production consumer of Store.Transition,
// re-evaluate this placement.
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

		// validated is EARNED, never asserted ([validated-is-earned] [R1]). A
		// pre-call refusal, written exactly like the !target.valid() check above
		// it: no sentinel, no statusForErr case, no store change. Without it any
		// caller could mark an invoice validated having never run a rule --
		// status='validated' with violations='[]' and rule_set_version_id=NULL,
		// precisely the lie this story exists to make impossible. Narrow on
		// purpose: only this one target is refused, every other target still
		// reaches the store untouched (GAPI-16), and a garbage target still 400s
		// at the check above before ever reaching here (GAPI-17).
		if target == StatusValidated {
			writeError(w, http.StatusConflict, "validated is earned via POST /v1/invoices/{id}/validate, not via this endpoint")
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

// validateResponse is the POST /v1/invoices/{id}/validate response body:
// Invoice embedded (keeping every existing field's name/type/position),
// plus one additive sibling key, rule_set_version. Not added to the Invoice
// domain struct itself: Invoice is shared by Get/List, which never call the
// gate and would start emitting a misleading always-null key.
//
// RuleSetVersion is a *int with NO omitempty: it must render an explicit
// JSON null when nothing was evaluated (Gate.Evaluate's zero-value
// convention) -- never omitted, never a false 0
// (TestValidateHandler_NilVersionMarshalsNull).
type validateResponse struct {
	Invoice
	RuleSetVersion *int `json:"rule_set_version"`
}

// ValidateHandler returns POST /v1/invoices/{id}/validate -- THE gate
// ([gate-endpoint]): the only route by which an invoice reaches validated, and
// therefore also the on-demand re-validate endpoint (Core AC #6). It is
// re-callable at any time on a stored invoice, and re-calling it IS
// re-validation; it is named /validate rather than /revalidate because the
// first call is not a RE-validation.
//
// Same identity-first-401 order as every other handler here, then the injected
// gate closure, then statusForErr. There is no body to decode -- the id is the
// whole request.
//
// A blocking violation is a 200 carrying the violations as ordinary data
// ([error semantics]), never an HTTP error: "this invoice has errors" is a
// legitimate OUTCOME of the gate, and the fix loop and the violations panel
// read it as data. The HTTP error codes are reserved for the cases where no
// verdict was reached at all -- 502 (04 unreachable/broken) and 503 (04 healthy
// but with no published rule-set), never laundered into a clean 200.
func ValidateHandler(validate func(ctx context.Context, id string) (Invoice, int, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		inv, version, err := validate(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: validate", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		// nil -> JSON null when nothing was evaluated (see validateResponse's doc).
		resp := validateResponse{Invoice: inv}
		if version != 0 {
			resp.RuleSetVersion = &version
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// EditHandler returns PATCH /v1/invoices/{id} (M4-05-03). Same
// identity-first-401 order as every other handler here, then decodes the
// snake_case wire body (400 on decode error) into the 9 optional
// header MBS-content fields, builds UpdateInput 1:1 from the decoded
// request (identity/lifecycle are not the edit's job, [D9]), and calls edit.
// Errors map via statusForErr -- including the new ErrNotFixable->409 case
// (Core AC #1) and the existing ErrValidation->400 case for the all-nil
// guard ([A7]) -- 200 + updated Invoice on success (Core AC #2/#3).
func EditHandler(edit func(ctx context.Context, id string, in UpdateInput) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req editReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		id := r.PathValue("id")

		inv, err := edit(r.Context(), id, UpdateInput{
			IssueDate:    req.IssueDate,
			SupplierTIN:  req.SupplierTIN,
			SupplierName: req.SupplierName,
			BuyerTIN:     req.BuyerTIN,
			BuyerName:    req.BuyerName,
			Currency:     req.Currency,
			Subtotal:     req.Subtotal,
			VAT:          req.VAT,
			Total:        req.Total,
		})
		if err != nil {
			status, msg := statusForErr(err)
			if status >= http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: edit", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, inv)
	}
}

// HistoryHandler returns GET /v1/invoices/{id}/history (task-160/M4-22-01).
// ErrNotFound covers both an unknown id and a cross-tenant one (RLS-scoped
// zero rows) -- 404, same as GetHandler. Malformed id maps ErrValidation ->
// 400, mirroring Get/Update/Transition, not 404. Success body is a BARE
// JSON array of StatusChange -- no pagination, no envelope
// ([history-endpoint-scope]) -- unlike every other handler here.
func HistoryHandler(history func(ctx context.Context, id string) ([]StatusChange, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		changes, err := history(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: history", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, changes)
	}
}

// batchSubmitReq is the POST /v1/invoices/submissions wire body ([batch-key-in-the-body],
// task-231): idempotency_key is a JSON body field, not a header.
type batchSubmitReq struct {
	InvoiceIDs     []string `json:"invoice_ids"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// maxBatchSubmitInvoiceIDs is the invented per-request cap ([batch-route-and-cap], task-231
// System Design): an unbounded batch is an unbounded transaction.
const maxBatchSubmitInvoiceIDs = 200

// maxBatchSubmitIdempotencyKeyLen is 255 (idempotency_keys.key's CHECK char_length<=255) minus
// 1 (the ":" deriveBatchSubmitKey inserts) minus 36 (a uuid) = 218 -- the precise bound that
// keeps every derived "<request key>:<invoice id>" key within the shared idempotency_keys /
// submission_jobs CHECK, superseding the story's earlier "1..200 chars" language (task-231
// Implementation Notes; T07-7's bound half pins 218 accepted, 219 rejected).
const maxBatchSubmitIdempotencyKeyLen = 218

// maxBatchSubmitBodyBytes bounds the request body BEFORE it is decoded (CodeRabbit,
// PR #92, handlers.go:547): the platform server applies no request body limit of its own,
// so an oversized invoice_ids array would be fully materialized by json.Decode before the
// 200-id cap (maxBatchSubmitInvoiceIDs) ever gets a chance to reject it. A legitimate body
// tops out at ~8.1 KB -- 200 UUIDs, quoted + comma-separated (200 * 39 = 7800 bytes) plus a
// <=218-char idempotency_key, quoted (~220 bytes) plus field names/braces (~100 bytes).
// 64 KiB leaves ~8x headroom without opening the door to unbounded allocation.
const maxBatchSubmitBodyBytes = 64 * 1024

// BatchSubmitHandler returns POST /v1/invoices/submissions (task-231, [trigger-surface]).
// Identity-first-401 (same order as every handler above) -> decode (400 on malformed JSON)
// -> pre-tx validation, ALL before submit is ever called ([T07-8 non-uuid handling]): empty
// invoice_ids -> 400; >200 ids -> 400; blank or >218-char idempotency_key -> 400; any
// non-uuid id -> 400 -> submit(ctx, BatchSubmitInput{...}) -> statusForErr (ErrNotFound ->
// 404, ErrValidation -> 400, the existing map) -> 200 + BatchSubmitResult.
func BatchSubmitHandler(submit func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBatchSubmitBodyBytes)
		var req batchSubmitReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if len(req.InvoiceIDs) == 0 {
			writeError(w, http.StatusBadRequest, "invoice_ids is required")
			return
		}
		if len(req.InvoiceIDs) > maxBatchSubmitInvoiceIDs {
			writeError(w, http.StatusBadRequest, "invoice_ids exceeds the 200 cap")
			return
		}
		if req.IdempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "idempotency_key is required")
			return
		}
		if len(req.IdempotencyKey) > maxBatchSubmitIdempotencyKeyLen {
			writeError(w, http.StatusBadRequest, "idempotency_key exceeds the 218-char bound")
			return
		}
		for _, id := range req.InvoiceIDs {
			if _, err := uuid.Parse(id); err != nil {
				writeError(w, http.StatusBadRequest, "invoice_ids must be well-formed UUIDs")
				return
			}
		}

		result, err := submit(r.Context(), BatchSubmitInput{
			InvoiceIDs:     req.InvoiceIDs,
			IdempotencyKey: req.IdempotencyKey,
		})
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: batch submit", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, result)
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
// into the response.
//
// The gate's four rows ([error-mapping], M4-04-06). ErrNotDraft and
// ErrStaleValidation are 409: the caller asked for something that is no longer
// true, not something malformed (400) or missing (404). ErrUpstream and
// ErrNoActiveRuleSet are DELIBERATELY distinguishable -- 502 means 04 is broken
// or unreachable so 03 could not get a verdict; 503 means 04 is healthy but has
// nothing published to evaluate against. Both are outages; NEITHER ever means
// "the invoice is clean". All four are independent sentinels (none wraps
// ErrValidation), so their order among these cases carries no meaning. Logging the unrecognized (500) case via slog is the
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
	case errors.Is(err, ErrNotDraft):
		return http.StatusConflict, "invoice is not a draft"
	case errors.Is(err, ErrStaleValidation):
		return http.StatusConflict, "invoice changed during validation"
	case errors.Is(err, ErrNotFixable):
		return http.StatusConflict, "invoice is not in a fixable state"
	case errors.Is(err, ErrUpstream):
		return http.StatusBadGateway, "validation service unavailable"
	case errors.Is(err, ErrNoActiveRuleSet):
		return http.StatusServiceUnavailable, "no active rule-set"
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
