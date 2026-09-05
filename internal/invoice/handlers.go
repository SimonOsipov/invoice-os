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
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/approval"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/qrcode"
)

// --- wire request/response types --------------------------------------------
//
// These are named distinctly from handlers_test.go's own test-local wire
// types (createInvoiceRequest/lineItemWire/transitionRequest/
// listPaginationWire/listInvoicesResponse) to avoid a same-package type
// clash -- the JSON shapes match (same tags), only the Go type names differ.
// Create/Transition write the domain Invoice directly (portfolio precedent:
// writeJSON(w, status, entity)), so neither needs a response DTO. Get and List
// do wrap: getResponse adds qr_png_base64 and the action flags, listItem adds
// approval -- both EMBED Invoice, so its keys keep their names and order.

// lineItemReq is one entry of createRequest.LineItems AND of editReq.LineItems
// (INVED-01-05): the create and edit line shapes are deliberately ONE type, not
// two byte-identical twins that could drift apart. LineNo is deliberately
// absent -- it is system-assigned 1..N by array position (Store.Create /
// replaceLinesTx, [D10], [line-no-by-position]), never caller-supplied, so a
// client that sends one has it silently ignored rather than rejected.
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
	// Appended last so the mirrored SPA/e2e types keep every existing key's
	// position; absent and explicit null are both "no document"
	// (TestCreateHandler_SourceDocumentIDAbsentOrNullIsNoDocument).
	SourceDocumentID *string `json:"source_document_id"`
}

// transitionReq is the POST /v1/invoices/{id}/transitions wire body ([D12]:
// a single endpoint, {"target":...}, not per-target sub-paths).
type transitionReq struct {
	Target string `json:"target"`
}

// editReq is the PATCH /v1/invoices/{id} wire body (M4-05-03, [A1]): the 9
// optional header MBS-content fields, snake_case tags IDENTICAL to
// createRequest's own (above) minus entity_id/invoice_number -- identity and
// lifecycle are not the edit's job ([D9]).
//
// LineItems (INVED-01-05) is a POINTER to a slice, mirroring
// EditInput.LineItems, because three states must stay distinguishable
// ([line-items-optional]): an ABSENT key and an explicit NULL both decode to a
// nil pointer ("leave the stored lines exactly as they are"), while `[]`
// decodes to a NON-nil pointer at a zero-length slice ("remove every line") and
// a populated array replaces the whole set. A plain []lineItemReq could not
// tell the first case from the second, so `[]` could never delete a line.
type editReq struct {
	IssueDate    *time.Time     `json:"issue_date"`
	SupplierTIN  *string        `json:"supplier_tin"`
	SupplierName *string        `json:"supplier_name"`
	BuyerTIN     *string        `json:"buyer_tin"`
	BuyerName    *string        `json:"buyer_name"`
	Currency     *string        `json:"currency"`
	Subtotal     *string        `json:"subtotal"`
	VAT          *string        `json:"vat"`
	Total        *string        `json:"total"`
	LineItems    *[]lineItemReq `json:"line_items"`
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
// {"invoices":[...],"pagination":{...}}. Invoices is []listItem, and the
// never-a-nil-slice guarantee is ListHandler's own make(), not Store.List's --
// an empty result serializes "invoices":[] rather than "invoices":null
// (TestListHandler_EmptyState).
type listResponse struct {
	Invoices   []listItem     `json:"invoices"`
	Pagination listPagination `json:"pagination"`
}

// listItem is one GET /v1/invoices row: Invoice embedded, plus five additive
// siblings. Invoice itself is NOT widened -- it is shared by Get/Create/Edit,
// and RuleSetVersion is json:"-" for exactly that reason.
//
// No omitempty on any of the five: a nil Approval emits explicit null, the
// honest answer for an invoice with no approval run, and an omitted permission
// flag would read undefined in the SPA and fail OPEN
// (TestListItem_InvoiceKeysUnmovedAndUnrenamed,
// TestListItem_ApproveFlagsCarryNoOmitempty).
//
// CanApprove/ApproveBlockedReason are approvalGate's answer per row (APPR-12-09,
// U5). The REJECT pair stays detail-only (U5a) -- the queue has no reject action.
//
// CanSubmit/SubmitBlockedReason are submitGate's answer per row, from the same
// call GetHandler makes (TestListAndDetail_SubmitGateCannotDisagree).
type listItem struct {
	Invoice
	Approval             *approval.RowFacts `json:"approval"`
	CanApprove           bool               `json:"can_approve"`
	ApproveBlockedReason *string            `json:"approve_blocked_reason"`
	CanSubmit            bool               `json:"can_submit"`
	SubmitBlockedReason  *string            `json:"submit_blocked_reason"`
}

// --- handlers ----------------------------------------------------------------

// CreateHandler returns POST /v1/invoices. Identity-first-401, then decodes
// the snake_case wire body (400 on decode error), 400 if entity_id or
// invoice_number is blank (before create ever runs -- Store.Create's own
// pre-tx guard is the defense-in-depth backstop for the importer-reuse path,
// [D3]), calls create, maps errors via statusForErr, 201 + Invoice (with
// line_items) on success.
//
// req.SupplierTIN/req.SupplierName (INVCR-01-17, C7 fix): passed through to
// CreateInput unchanged here, but Store.Create OVERRIDES them with the
// resolved entity's own tin (MBS-hyphen-restored)/name before the INSERT --
// this handler does not reject a caller-supplied value with a 400, it is
// silently superseded. Architect ruling (task-293 AC #8): supplier identity
// is the firm's own data, never the caller's wire body, and a 400 here would
// break e2e/api/client.ts's InvoiceCreateInput, which has sent these two
// fields since M4-07-05 -- override keeps that harness green while closing
// the false supplier-tin-format violation for API-created entities.
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
		// Named 400 rather than the store's 22P02 -> ErrValidation, which names
		// nothing (TestCreateHandler_MalformedSourceDocumentID400). A document
		// belonging to another tenant is NOT checked here: the composite FK
		// invoices_tenant_source_document_fk refuses it inside the tx, so the
		// answer cannot become an existence oracle
		// (TestRLS_CreateSourceDocumentRefusalIsTheFKNotAbsence).
		if req.SourceDocumentID != nil {
			if _, err := uuid.Parse(*req.SourceDocumentID); err != nil {
				writeError(w, http.StatusBadRequest, "source_document_id must be a well-formed uuid")
				return
			}
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
			// SourceRows stays nil: a hand-typed invoice has no sheet rows.
			SourceDocumentID: req.SourceDocumentID,
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
// (keeping every existing field's name/type/position), plus two additive
// sibling keys, rule_set_version and qr_png_base64 -- mirrors validateResponse
// below (M4-09-01, [read-shape-getresponse-wrapper]). Neither is added to the
// Invoice domain struct itself: Invoice is shared by List, which must NOT
// gain either key.
//
// RuleSetVersion is a *int with NO omitempty: it must render an explicit
// JSON null when the invoice was never validated (Store.Get's zero-value
// convention) -- never omitted, never a false 0
// (TestGetHandler_RuleSetVersionMarshalsNull).
//
// QRPNGBase64 (M5-09-01, task-250) is likewise a *string with NO omitempty --
// explicit null when absent (TestGetHandler_QRPNGBase64MarshalsNull).
// GetHandler calls qrcode.RenderBase64(*inv.QRPayload) when inv.QRPayload is
// non-nil, logging (never 5xxing) a render failure, per Core AC #3/#5.
//
// CanEdit/CanRevalidate/RevalidateBlockedReason (INVED-01-05,
// [gates-on-the-wire]) ship the DERIVED per-invoice availability of the two
// actions, so the SPA holds no status set of its own at all. They are declared
// LAST deliberately: writeJSON marshals with json.NewEncoder, so declaration
// order IS wire key order, and appending keeps every pre-existing key's name,
// type AND position untouched.
//
// NONE of the three carries omitempty, for the same reason the two siblings
// above do not: a `false` bool tagged omitempty marshals to a MISSING key,
// which would make "the server says this invoice is not editable" impossible to
// tell apart from "an older server that has never heard of this key". All three
// are present on every status -- explicit null, never omitted.
//
// CanViewUBL/UBLBlockedReason (BUG-04-03) follow both rules above -- appended
// last, no omitempty -- and come from ublGate (ubl.go), which derives them from
// invoice CONTENT via ubl.Missing, never from status.
//
// CanResolveOutside/ResolveOutsideBlockedReason follow the same two rules,
// appended last of all, and come from resolveOutsideGate below.
//
// CanApprove/ApproveBlockedReason/CanReject/RejectBlockedReason (APPR-08-06)
// follow the same two rules, appended last of all, and come from ONE
// approvalGate call -- approve and reject availability are identical.
type getResponse struct {
	Invoice
	RuleSetVersion              *int    `json:"rule_set_version"`
	QRPNGBase64                 *string `json:"qr_png_base64"`
	CanEdit                     bool    `json:"can_edit"`
	CanRevalidate               bool    `json:"can_revalidate"`
	RevalidateBlockedReason     *string `json:"revalidate_blocked_reason"`
	CanSubmit                   bool    `json:"can_submit"`
	SubmitBlockedReason         *string `json:"submit_blocked_reason"`
	CanViewUBL                  bool    `json:"can_view_ubl"`
	UBLBlockedReason            *string `json:"ubl_blocked_reason"`
	CanResolveOutside           bool    `json:"can_resolve_outside"`
	ResolveOutsideBlockedReason *string `json:"resolve_outside_blocked_reason"`
	CanApprove                  bool    `json:"can_approve"`
	ApproveBlockedReason        *string `json:"approve_blocked_reason"`
	CanReject                   bool    `json:"can_reject"`
	RejectBlockedReason         *string `json:"reject_blocked_reason"`
}

// revalidateBlockedReason is the SINGLE, status-independent copy for a disabled
// Re-validate button ([revalidate-reason-from-backend]). Deliberately NOT a
// switch over validated/rejected: that would be a fourth hand-maintained status
// list, reopening Core AC 4. Separator is an em dash (U+2014) with single
// spaces, matching the copy already on the invoice-detail screen.
const revalidateBlockedReason = "Only draft invoices can be re-validated — edit this invoice to return it to draft."

// notApproverTransmitReason is the ONE refusal sentence both transmit doors
// emit, so a blocked caller's 403 never varies by request shape
// (TestTransmitGate_NoExistenceOracle). Deliberately not routed through
// ErrNotPermitted: that sentinel's statusForErr arm answers with the
// resolve-outside copy, which would be a wire lie here.
const notApproverTransmitReason = "Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team."

// awaitingApprovalReason is the ONE refusal sentence every approval-blocked door
// emits, so a blocked caller's message never varies by door FOR THE SAME WIRE
// FIELD — the notApproverTransmitReason precedent above. Those fields are the 409
// body (statusForErr) and submit_blocked_reason (submitGate). The batch door has
// no sentence field at all: BatchSubmitResultItem.Reason carries the machine token
// awaiting_approval, which the SPA labels itself (docs/approvals.md §11).
// The SPA maps that token straight back to these bytes, pinned both ways by
// TestAwaitingApprovalReason_MatchesTheSPASkipLabel.
// Distinguishable on purpose from internal/approval's "no longer awaiting
// approval" 409, which means the inverse
// (TestAwaitingApprovalReason_DistinctFromTheApprovalPackageRefusal).
const awaitingApprovalReason = "This invoice is waiting on approval — it can be submitted once an approver approves it."

// submitBlockedReason mirrors revalidateBlockedReason's canEdit && !canX gate,
// but the copy FORKS by status: draft points at a live, enabled Re-validate,
// rejected points at a disabled one, so one shared sentence would lie on
// whichever status it doesn't describe. The blocked SET stays derived, not a
// hardcoded switch -- the default arm must return a real string (never nil)
// so a future legalTransitions edge that widens canEdit still gets a reason.
func submitBlockedReason(s Status) *string {
	if !canEdit(s) || canSubmit(s) {
		return nil
	}
	var reason string
	switch s {
	case StatusDraft:
		reason = "Only validated invoices can be submitted — re-validate this invoice first."
	case StatusRejected:
		reason = "Only validated invoices can be submitted — edit this invoice and re-validate it first."
	default:
		reason = "Only validated invoices can be submitted."
	}
	return &reason
}

// resolveOutsideGate checks status BEFORE role, deliberately the reverse of
// Store.ResolveOutside's permission-first order (store.go): the store's
// order exists only to stop a non-approver probing row existence via a
// 409-vs-404 distinction, but this gate runs on a row already fetched, so
// status-first gives the more relevant message instead.
func resolveOutsideGate(s Status, role string) (bool, *string) {
	if s != StatusFailed {
		r := "Only a failed invoice can be marked resolved outside the system."
		return false, &r
	}
	if !isApprover(role) {
		r := "Only an approver can mark an invoice resolved outside the system — ask an admin or a reviewer on your team."
		return false, &r
	}
	return true, nil
}

// submitGate checks role BEFORE status, deliberately the reverse of
// resolveOutsideGate above: a preparer told "re-validate this invoice first"
// would run a re-validation that still cannot end in a submit.
//
// approvalClear is the LAST rung, after status: an invoice that is not validated
// has no run to wait on, so the awaiting-approval sentence would be a lie there.
// It arrives already folded against APPROVALS_ENFORCED (Store.ApprovalFacts), so
// a flag-off deployment reads true and this arm is inert
// (TestSubmitGate_AdminAndReviewerUnchanged).
func submitGate(s Status, role string, approvalClear bool) (bool, *string) {
	if !isApprover(role) {
		r := notApproverTransmitReason // a const is not addressable; copy to a local
		return false, &r
	}
	if !canSubmit(s) {
		return false, submitBlockedReason(s)
	}
	if !approvalClear {
		r := awaitingApprovalReason
		return false, &r
	}
	return true, nil
}

// approvalGate is the detail page's approve/reject availability, mirroring
// decideTx's refusal ladder rung for rung so the wire never offers a button the
// endpoint would refuse. ONE gate feeds both pairs: decideTx branches on the
// decision only after every rung, and reject's extra reason rule is
// DecideHandler's body check, not an availability rung.
//
// Rung 4 is a conjunction, and rung 2 precedes the run rungs, because a dead run
// keeps its later steps pending and a reject demotes the invoice back to draft:
// TestApprovalGate_DeadRunWithALaterPendingStep and
// TestApprovalGate_RejectedRunDemotedToDraft catch either half going missing.
// Both halves of rung 4 answer decideTx's one ErrRunClosed, hence one sentence.
func approvalGate(s Status, role string, f ApprovalFacts) (bool, *string) {
	if !isApprover(role) {
		r := "Only an admin or a reviewer can approve or reject an invoice — ask an approver on your team."
		return false, &r
	}
	if s != StatusValidated {
		r := "Only a validated invoice can be approved or rejected."
		return false, &r
	}
	if f.RunState == "" {
		r := "This invoice has no approval run to decide on."
		return false, &r
	}
	if f.RunState != "open" || f.PendingStepOrd == nil {
		r := "This invoice's approval run is already closed."
		return false, &r
	}
	if !f.CallerHoldsRole {
		r := "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."
		return false, &r
	}
	return true, nil
}

// GetHandler returns GET /v1/invoices/{id}. Same identity-first-401 order as
// CreateHandler, reading r.PathValue("id"); 404 via ErrNotFound (covers both
// a genuinely unknown id and a cross-tenant one, RLS-scoped 0-rows), 200 +
// Invoice (with line_items, [D7]) on success.
//
// callerRole feeds both submitGate and resolveOutsideGate alongside the
// fetched invoice's status. It is not assumed to honor Store.CallerRole's own
// "never errors" contract -- an error from it fails closed (not-an-approver),
// never a 5xx.
//
// approvalFacts feeds submitGate's third rung. An error from it fails closed --
// the zero ApprovalFacts reads TransmitClear false -- and is logged while the
// response stays 200 (TestGetHandler_ApprovalFactsErrorFailsClosedNot500). It is
// logged, unlike callerRole, because a failed approval read has no legitimate
// reading: "" is a real answer for a non-member, a read fault never is.
func GetHandler(
	get func(ctx context.Context, id string) (Invoice, error),
	callerRole func(ctx context.Context) (string, error),
	approvalFacts func(ctx context.Context, id string) (ApprovalFacts, error),
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

		inv, err := get(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: get", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		// QRPNGBase64 stays nil (-> explicit JSON null via getResponse's
		// no-omitempty tag) when the invoice has no qr_payload, or when
		// rendering it fails. A render failure is logged, never turned into a
		// non-200: a corrupt/oversized qr_payload must not make an otherwise
		// viewable invoice inaccessible (Core AC #3/#5, M5-09-01, task-250).
		var qrPNGBase64 *string
		if inv.QRPayload != nil {
			rendered, err := qrcode.RenderBase64(*inv.QRPayload)
			if err != nil {
				log.ErrorContext(r.Context(), "invoice: render qr", slog.Any("err", err))
			} else {
				qrPNGBase64 = &rendered
			}
		}

		// ublGate is ONE return, so ubl_blocked_reason != null IFF
		// can_view_ubl == false, and both agree with GET /{id}/ubl's 409
		// ([ubl-gate-is-content-not-status]).
		canViewUBL, ublReason := ublGate(SubmissionCanonical(inv))

		// ONE resolution shared by both gates: submitGate consults role on every
		// status, resolveOutsideGate only on failed
		// (TestGetHandler_CallerRoleResolvedOnEveryStatus).
		role, err := callerRole(r.Context())
		if err != nil {
			role = ""
		}
		// Keyed on the FETCHED row's id, never r.PathValue: Store.Get returns
		// Postgres's canonical uuid text, so a braced or uppercase spelling in the
		// URL cannot reach the approval read (Store.Transition's lockedID trap,
		// TestGetHandler_ApprovalSeamKeyedOnTheFetchedRowId).
		facts, err := approvalFacts(r.Context(), inv.ID)
		if err != nil {
			log.ErrorContext(r.Context(), "invoice: approval facts", slog.Any("err", err))
			facts = ApprovalFacts{}
		}

		canResolveOutside, resolveOutsideReason := resolveOutsideGate(inv.Status, role)
		canSubmitInv, submitReason := submitGate(inv.Status, role, facts.TransmitClear)
		// ONE call feeds both pairs, so can_approve and can_reject cannot diverge
		// (TestApprovalGate_ApproveAndRejectNeverDiverge).
		canDecide, decideReason := approvalGate(inv.Status, role, facts)

		// Both action flags are read off the DERIVED predicates canEdit/
		// canRevalidate (store.go), never a status switch here: a switch would be
		// a fourth hand-maintained status list and re-open Core AC 4.
		//
		// The reason is non-null EXACTLY when canEdit && !canRevalidate (i.e.
		// validated/rejected). Not merely !canRevalidate: on queued/submitted/
		// accepted/failed the copy would be a lie, since those statuses cannot be
		// edited back to draft either. The SPA renders Re-validate at every status,
		// disabled off !can_revalidate ([actions-visibility]), so a null reason means
		// either the action is permitted (draft) or there is no honest copy for it.
		resp := getResponse{
			Invoice:                     inv,
			RuleSetVersion:              inv.RuleSetVersion,
			QRPNGBase64:                 qrPNGBase64,
			CanEdit:                     canEdit(inv.Status),
			CanRevalidate:               canRevalidate(inv.Status),
			CanSubmit:                   canSubmitInv,
			SubmitBlockedReason:         submitReason,
			CanViewUBL:                  canViewUBL,
			UBLBlockedReason:            ublReason,
			CanResolveOutside:           canResolveOutside,
			ResolveOutsideBlockedReason: resolveOutsideReason,
			CanApprove:                  canDecide,
			ApproveBlockedReason:        decideReason,
			CanReject:                   canDecide,
			RejectBlockedReason:         decideReason,
		}
		if resp.CanEdit && !resp.CanRevalidate {
			reason := revalidateBlockedReason // a const is not addressable; copy to a local
			resp.RevalidateBlockedReason = &reason
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// maxFilterTextLen bounds ListHandler's two free-text filter params, rule_key
// and q (INVCR-01-06 AC-6). 200 is an invented abuse bound with a wide margin:
// the longest shipped rule key is ~24 chars, and no honest review-screen search
// is longer. Over the cap is a 400, deliberately NOT a silent truncation --
// truncating turns "find X" into "find some prefix of X" and returns rows the
// caller never asked for. (Contrast limit>200, which DOES silently clamp: a
// clamp there still answers the question asked, just less of it, and
// pagination.limit reports the truncation on the wire.)
//
// This is a byte length, not a rune count: the cap exists to bound the input,
// and bytes are what an abusive caller actually spends.
const maxFilterTextLen = 200

// maxImportBatchIDs bounds the number of repeated import_batch_id values
// ListHandler/ViolationSummaryHandler will accept in one request (BULK-01-02,
// [one-review-screen]). An invented abuse bound, same shape as
// maxFilterTextLen/maxKeepAsIsReasonLen: set well above the product's 5-file
// run cap so raising that cap needs no server change. Checked BEFORE
// uuid.Parse (a resource guard must not be payable in unbounded parse work)
// and counts only USABLE (non-empty) values -- an empty value is not an id,
// so it can never itself trip the cap.
const maxImportBatchIDs = 25

// ListHandler returns GET /v1/invoices. Same identity-first-401 order as
// Create/GetHandler. Query params (portfolio's exact defaulting/clamping
// rules, [D8]): limit (default 50, non-integer -> 400, <1 -> 400, >200
// clamps down to 200), offset (default 0, non-integer or negative -> 400),
// needs_attention (M4-09-02, AC #5, [needs-attention-param-strictness]:
// absent defaults to false/unfiltered; parsed via strconv.ParseBool, so
// "true"/"false"/"1"/"0"/etc. all work; an unparseable value 400s BEFORE the
// store is ever called, mirroring the limit/offset 400 contract), entity_id
// ([entity-id-restored], regression fix reversing [entity-id-cut]): absent
// applies no filter, tenant-wide, exactly as before this param existed;
// present is validated with uuid.Parse (same pre-store guard BatchSubmitHandler
// already uses for invoice_ids, ~L610) and 400s "entity_id must be a
// well-formed uuid" BEFORE the store is ever called if malformed -- never a
// silent ignore, never a bare Postgres 500. [entity-id-cut] had reasoned the
// SPA could narrow to one entity by filtering an already-fetched, still
// tenant-wide LIMIT-50 page in the browser instead (lib/invoices.ts) -- wrong:
// that's filter-AFTER-paginate, so an entity's own invoices silently vanished
// from Invoices/Reports/Customers whenever they weren't inside the newest 50
// tenant-wide (the CI-caught regression this param fixes).
//
// ELEVEN params AND together today, and the SPA's ListInvoicesOptions mirrors
// all eleven (lib/invoices.ts): limit, offset, needs_attention, entity_id,
// import_batch_id, status, needs_fix, rule_key, q, kept_as_is, awaiting_approval.
//
// INVCR-01-06 ([D4], Core AC 7) added the review screen's five of them:
// import_batch_id (uuid.Parse, same shape as entity_id), status (validated by
// the existing Status.valid(), reusing TransitionHandler's byte-identical
// "unknown status" 400 rather than writing a second copy of the 7-value list),
// needs_fix (strconv.ParseBool, same shape as needs_attention), and the two
// free-text params rule_key and q, capped at maxFilterTextLen.
//
// Two contract rules hold across all five. First, EMPTY IS ABSENT, not a 400
// (`if raw != ""`), matching all four params that shipped before them -- so
// `?import_batch_id=` applies no filter and returns the tenant-wide list. That
// is safe only because the review route is `#review/<uuid>` and parseReviewHash
// returns null for a non-uuid, so no request is ever issued without a batch id;
// a caller must never render a review table from a response fetched without
// one. Second, a MALFORMED value is always a 400 raised BEFORE the store is
// called, never a silent ignore -- silently dropping a narrowing filter renders
// a wrong page (too many rows, plausible total) instead of an honest error,
// which is exactly the [entity-id-cut] failure mode.
//
// The response envelope is unchanged: exactly two top-level keys, "invoices"
// and "pagination". Applied filters are deliberately NOT echoed back
// (TestListHandler_EnvelopeExactKeysAndEffectiveClampedValues pins len == 2).
//
// import_batch_id WIDENS to a repeated param (BULK-01-02, [one-review-screen]):
// the review screen can supply several, e.g.
// `?import_batch_id=A&import_batch_id=B`, so every invoice a multi-file run
// produced across all its batches shows on one query. Read via the raw
// url.Values (query["import_batch_id"]), never query.Get (which returns only
// the first), skipping each "" value individually (empty-is-absent PER
// VALUE, unchanged) -- capped at maxImportBatchIDs USABLE (non-empty) ids,
// checked BEFORE uuid.Parse so the cap is never payable in unbounded parse
// work. One value still produces a one-element ListFilter.ImportBatchIDs and
// byte-identical semantics to today.
//
// rowFacts (APPR-08-08) reads the page's approval standing in ONE call, keyed
// on the store-returned inv.ID, and is skipped entirely on an empty page. It now
// also returns the page's gate inputs (ListGateFacts, store.go), which feed
// approvalGate per row (APPR-12-09).
//
// Its error stays a 500, deliberately the OPPOSITE of GetHandler's approvalFacts
// arm, which logs and stays 200. That arm can degrade because a zero gate value
// means "deny" -- a real and safe answer. Here the same call ALSO carries the
// DISPLAYED approval facts, whose only available default is approval:null: a
// positive claim ("this invoice has no approval run"), and serving it on a read
// fault is a silent lie on a compliance surface. The 500 refuses both halves at
// once, so no row is ever served with a fabricated standing OR a guessed gate.
// Do not harmonise the two. TestListHandler_RowFactsErrorIs500.
func ListHandler(
	list func(ctx context.Context, f ListFilter) ([]Invoice, int, error),
	rowFacts func(ctx context.Context, ids []string) (map[string]approval.RowFacts, ListGateFacts, error),
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

		entityID := query.Get("entity_id")
		if entityID != "" {
			if _, err := uuid.Parse(entityID); err != nil {
				writeError(w, http.StatusBadRequest, "entity_id must be a well-formed uuid")
				return
			}
		}

		// query["import_batch_id"] reads EVERY value of the repeated param
		// (query.Get would return only the first). "" is skipped PER VALUE
		// (empty-is-absent, unchanged: ?import_batch_id= alone still applies
		// no filter). The cap is checked BEFORE uuid.Parse, over USABLE
		// (non-empty) ids only, so it is never payable in unbounded parse
		// work and 30 empty values never trip it (Decision 3).
		var importBatchIDs []string
		for _, raw := range query["import_batch_id"] {
			if raw != "" {
				importBatchIDs = append(importBatchIDs, raw)
			}
		}
		if len(importBatchIDs) > maxImportBatchIDs {
			writeError(w, http.StatusBadRequest, "import_batch_id exceeds the 25 cap")
			return
		}
		for _, batchID := range importBatchIDs {
			if _, err := uuid.Parse(batchID); err != nil {
				writeError(w, http.StatusBadRequest, "import_batch_id must be a well-formed uuid")
				return
			}
		}

		// statusFilter, not `status`: the error path below binds its own
		// `status` from statusForErr, and shadowing it here reads as a bug.
		statusFilter := Status(query.Get("status"))
		if statusFilter != "" && !statusFilter.valid() {
			writeError(w, http.StatusBadRequest, "unknown status")
			return
		}

		needsFix := false
		if raw := query.Get("needs_fix"); raw != "" {
			b, err := strconv.ParseBool(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "needs_fix must be a boolean")
				return
			}
			needsFix = b
		}

		ruleKey := query.Get("rule_key")
		if len(ruleKey) > maxFilterTextLen {
			writeError(w, http.StatusBadRequest, "rule_key is too long")
			return
		}

		q := query.Get("q")
		if len(q) > maxFilterTextLen {
			writeError(w, http.StatusBadRequest, "q is too long")
			return
		}

		// kept_as_is (INVCR-01-15, D6): the review shell's "N kept as-is" footer
		// counter query -- same empty-is-absent/strconv.ParseBool shape as
		// needs_fix/needs_attention above, deliberately NOT one of the four
		// toolbar pills (System Design §7).
		keptAsIs := false
		if raw := query.Get("kept_as_is"); raw != "" {
			b, err := strconv.ParseBool(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "kept_as_is must be a boolean")
				return
			}
			keptAsIs = b
		}

		// awaiting_approval (APPR-08-07): invoices an active approval policy still holds.
		// Never gated by APPROVALS_ENFORCED -- the flag gates enforcement, not visibility
		// (docs/approvals.md §11).
		awaitingApproval := false
		if raw := query.Get("awaiting_approval"); raw != "" {
			b, err := strconv.ParseBool(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "awaiting_approval must be a boolean")
				return
			}
			awaitingApproval = b
		}

		filter := ListFilter{
			Limit: limit, Offset: offset, EntityID: entityID, NeedsAttention: needsAttention,
			ImportBatchIDs: importBatchIDs, Status: statusFilter, NeedsFix: needsFix,
			RuleKey: ruleKey, Query: q, KeptAsIs: keptAsIs,
			AwaitingApproval: awaitingApproval,
		}

		items, total, err := list(r.Context(), filter)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: list", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		// inv.ID, never a caller-supplied spelling: APPR-08-01's DEFECT QA-1 (a
		// non-canonical uuid is absent from RowFactsTx's map, so it silently reads
		// "no run") is unreachable only because these ids came from the store.
		// TestListHandler_ApprovalKeyedOnTheStoreReturnedRowID.
		ids := make([]string, 0, len(items))
		for _, inv := range items {
			ids = append(ids, inv.ID)
		}
		facts := map[string]approval.RowFacts{}
		var gate ListGateFacts
		if len(ids) > 0 {
			facts, gate, err = rowFacts(r.Context(), ids)
			if err != nil {
				log.ErrorContext(r.Context(), "invoice: list row facts", slog.Any("err", err))
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
		}

		// make, not var: a nil []listItem marshals "invoices":null
		// (TestListHandler_EmptyState).
		rows := make([]listItem, 0, len(items))
		for _, inv := range items {
			row := listItem{Invoice: inv}
			f, armed := facts[inv.ID]
			if armed {
				row.Approval = &f
			}
			// The SAME approvalGate GetHandler calls, so the two wires cannot
			// disagree (TestListAndDetail_ApproveGateCannotDisagree). A pure
			// call and two map lookups: no query enters this loop.
			row.CanApprove, row.ApproveBlockedReason = approvalGate(inv.Status, gate.CallerRole, ApprovalFacts{
				RunState:        f.RunState,
				PendingStepOrd:  f.PendingOrd,
				CallerHoldsRole: gate.HoldsPendingRole[inv.ID],
			})
			// The SAME submitGate GetHandler calls, over the same three inputs.
			// One map lookup: no query enters this loop.
			row.CanSubmit, row.SubmitBlockedReason = submitGate(inv.Status, gate.CallerRole, gate.TransmitClear[inv.ID])
			rows = append(rows, row)
		}

		writeJSON(w, http.StatusOK, listResponse{
			Invoices:   rows,
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
//
// callerRole gates transmission: only an admin or a reviewer may drive a
// transition, matching the shipped capability matrix. It is not assumed to
// honor Store.CallerRole's "never errors" contract -- an error fails closed to
// 403, never a 5xx.
func TransitionHandler(transition func(ctx context.Context, id string, target Status) (Invoice, error), callerRole func(ctx context.Context) (string, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Refusing before any row is read keeps a non-approver from probing
		// whether an id exists. callerRole opens its own transaction, separate
		// from the write below, so a suspension racing the write is a known,
		// accepted TOCTOU.
		role, err := callerRole(r.Context())
		if err != nil || !isApprover(role) {
			writeError(w, http.StatusForbidden, notApproverTransmitReason)
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
// snake_case wire body (400 on decode error -- including a line_items whose
// JSON SHAPE is wrong, which is a decode-time 400, never a 500) into the 9
// optional header MBS-content fields plus the optional line_items array,
// builds EditInput 1:1 from the decoded request (identity/lifecycle are not
// the edit's job, [D9]), and calls edit.
// Errors map via statusForErr -- including the new ErrNotFixable->409 case
// (Core AC #1) and the existing ErrValidation->400 case for the all-nil
// guard ([A7]) -- 200 + updated Invoice on success (Core AC #2/#3).
func EditHandler(edit func(ctx context.Context, id string, in EditInput) (Invoice, error), log *slog.Logger) http.HandlerFunc {
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

		// Carry the three line_items states across into EditInput unchanged
		// ([line-items-optional]). make(..., len) rather than a var+append loop is
		// load-bearing: append to a nil slice leaves a NIL slice when the array is
		// empty, whereas make(..., 0) is non-nil, which is what lets a `[]` body
		// reach Store.Edit as "remove every line" instead of "leave them alone".
		//
		// No all-nil guard here: a body carrying ONLY "line_items":[] must still
		// reach the store. Store.Edit owns that check and needs to see the
		// non-nil-but-empty pointer.
		var lines *[]LineItemInput
		if req.LineItems != nil {
			mapped := make([]LineItemInput, len(*req.LineItems))
			for i, li := range *req.LineItems {
				mapped[i] = LineItemInput{
					Description: li.Description,
					Quantity:    li.Quantity,
					UnitPrice:   li.UnitPrice,
					LineTotal:   li.LineTotal,
					LineTax:     li.LineTax,
				}
			}
			lines = &mapped
		}

		inv, err := edit(r.Context(), id, EditInput{UpdateInput: UpdateInput{
			IssueDate:    req.IssueDate,
			SupplierTIN:  req.SupplierTIN,
			SupplierName: req.SupplierName,
			BuyerTIN:     req.BuyerTIN,
			BuyerName:    req.BuyerName,
			Currency:     req.Currency,
			Subtotal:     req.Subtotal,
			VAT:          req.VAT,
			Total:        req.Total,
		}, LineItems: lines})
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

// keepAsIsRequest is the POST /v1/invoices/{id}/keep-as-is wire body (INVCR-01-15, D6):
// a single free-text reason. Deliberately has NO `by`/`actor` field -- the actor is
// ALWAYS auth.Identity.Subject (KeepAsIsHandler never reads one off the body), so a
// client-supplied "by" is silently ignored by json.Decode rather than accepted
// (TestKeepAsIsHandler_ActorIsIdentityNotBody).
type keepAsIsRequest struct {
	Reason string `json:"reason"`
}

// maxKeepAsIsReasonLen bounds the free-text reason (task-291 AC #3's "400 on an
// empty/oversized reason"). An invented abuse bound, mirroring maxFilterTextLen's own
// "wide margin" reasoning above: a genuine compliance note explaining why a failing
// invoice was kept can run to a couple of sentences, so 1000 bytes leaves ample room
// without opening the door to an unbounded audit-log payload.
const maxKeepAsIsReasonLen = 1000

// KeepAsIsHandler returns POST /v1/invoices/{id}/keep-as-is (INVCR-01-15, D6,
// task-291). Same identity-first-401 order as every handler above, then decodes the
// body (400 on decode error), trims the reason and 400s on empty/oversized BEFORE
// keep is ever called ([must-not-allow-empty-reason], mirrors BatchSubmitHandler's own
// pre-tx idempotency_key guard) -- keep itself never sees an empty or over-length
// reason. Errors map via statusForErr, including the new ErrNotKeepable->409 case
// (AC #3's "409 unless draft AND carries a blocking violation") -- 200 + the updated
// Invoice (kept_as_is_at/by/reason now stamped) on success.
func KeepAsIsHandler(keep func(ctx context.Context, id, reason string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req keepAsIsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			writeError(w, http.StatusBadRequest, "reason is required")
			return
		}
		if len(reason) > maxKeepAsIsReasonLen {
			writeError(w, http.StatusBadRequest, "reason exceeds the 1000-char bound")
			return
		}

		inv, err := keep(r.Context(), r.PathValue("id"), reason)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: keep as-is", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, inv)
	}
}

// UnkeepAsIsHandler returns DELETE /v1/invoices/{id}/keep-as-is (INVCR-01-15, D6,
// task-291) -- KeepAsIsHandler's un-do. Same identity-first-401 order; no body to
// decode, matching HistoryHandler/ValidateHandler's own no-body GET/POST shape. 200 +
// the updated Invoice (kept_as_is_at/by/reason now NULL) on success, idempotent when
// the invoice was not kept to begin with (Store.UnkeepAsIs's own no-op branch).
func UnkeepAsIsHandler(unkeep func(ctx context.Context, id string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		inv, err := unkeep(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: unkeep as-is", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, inv)
	}
}

// ResolveOutsideHandler returns POST /v1/invoices/{id}/resolved-outside.
// Mirrors KeepAsIsHandler's exact validation order (identity, decode, trim,
// bound), reusing keepAsIsRequest/maxKeepAsIsReasonLen -- both endpoints
// bound the same free-text reason the same way. Errors map via statusForErr,
// including the ErrNotPermitted/ErrNotResolvable arms.
func ResolveOutsideHandler(resolve func(ctx context.Context, id, reason string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req keepAsIsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			writeError(w, http.StatusBadRequest, "reason is required")
			return
		}
		if len(reason) > maxKeepAsIsReasonLen {
			writeError(w, http.StatusBadRequest, "reason exceeds the 1000-char bound")
			return
		}

		inv, err := resolve(r.Context(), r.PathValue("id"), reason)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: resolve outside", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, inv)
	}
}

// UnresolveOutsideHandler returns DELETE /v1/invoices/{id}/resolved-outside,
// ResolveOutsideHandler's un-do sibling -- see its doc comment. No body to
// decode, matching UnkeepAsIsHandler's own no-body shape.
func UnresolveOutsideHandler(unresolve func(ctx context.Context, id string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		inv, err := unresolve(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: unresolve outside", slog.Any("err", err))
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

// SourceDocumentHandler returns GET /v1/invoices/{id}/source-document. Shaped
// exactly like HistoryHandler, including the absent uuid.Parse: the path id
// goes straight to the store, whose 22P02 -> ErrValidation mapping is what
// makes a malformed id a 400. An invoice with no document is a 200 with null
// fields, never a 404.
func SourceDocumentHandler(get func(ctx context.Context, id string) (SourceDocument, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		out, err := get(r.Context(), r.PathValue("id"))
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: source document", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		writeJSON(w, http.StatusOK, out)
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
//
// callerRole gates transmission the same way TransitionHandler does: admin and
// reviewer only, errors failing closed to 403.
func BatchSubmitHandler(submit func(ctx context.Context, in BatchSubmitInput) (BatchSubmitResult, error), callerRole func(ctx context.Context) (string, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Ahead of the decode, for TransitionHandler's reasons: no existence
		// oracle, and the same accepted TOCTOU on suspension.
		role, err := callerRole(r.Context())
		if err != nil || !isApprover(role) {
			writeError(w, http.StatusForbidden, notApproverTransmitReason)
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
		// uuid.Parse accepts uppercase, {braced}, urn:uuid: and 32-hex-no-dash, so keep
		// the parse result: the approval gate keys on Postgres's canonical text and a
		// non-canonical id would read absent there
		// (TestBatchSubmitHandler_NormalisesInvoiceIdsToCanonicalForm).
		for i, id := range req.InvoiceIDs {
			parsed, err := uuid.Parse(id)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invoice_ids must be well-formed UUIDs")
				return
			}
			req.InvoiceIDs[i] = parsed.String()
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

// violationSummaryResponse is the GET /v1/invoices/violation-summary success
// body: {"rules":[...]}. Rules is []RuleCount (never a nil slice from
// Store.ViolationSummary), so a batch with no violations serializes
// "rules":[] rather than "rules":null.
type violationSummaryResponse struct {
	Rules []RuleCount `json:"rules"`
}

// ViolationSummaryHandler is GET
// /v1/invoices/violation-summary?import_batch_id=X (task-283 R6) -- a
// SEPARATE route, not a listResponse key:
// TestListHandler_EnvelopeExactKeysAndEffectiveClampedValues pins
// listResponse at exactly 2 keys. It coexists with GET /v1/invoices/{id}
// without any registration-order requirement: Go 1.22+ net/http.ServeMux
// resolves by pattern specificity, and the literal "violation-summary"
// segment always beats the {id} wildcard.
//
// import_batch_id is REQUIRED to carry at least one usable id -- zero usable
// ids (absent OR every value "") is a 400 raised by a pre-store guard,
// because an unbounded tenant-wide aggregation is not a supported query.
// Same identity-first-401 order as every handler above, then summary ->
// statusForErr -> 200 + violationSummaryResponse.
//
// WIDENS to a repeated param (BULK-01-02, mirroring ListHandler's own
// import_batch_id widening above): several ids let the rail span every batch
// a multi-file run produced.
func ViolationSummaryHandler(summary func(ctx context.Context, importBatchIDs []string) ([]RuleCount, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Same repeated-param read as ListHandler's own (query["import_batch_id"],
		// not query.Get, "" skipped per value) -- see its doc comment above.
		// REQUIRED stays required: zero usable ids (absent OR every value "")
		// is a 400 BEFORE the cap/uuid.Parse guards, because an unbounded
		// tenant-wide aggregation is not a supported query.
		var importBatchIDs []string
		for _, raw := range r.URL.Query()["import_batch_id"] {
			if raw != "" {
				importBatchIDs = append(importBatchIDs, raw)
			}
		}
		if len(importBatchIDs) == 0 {
			writeError(w, http.StatusBadRequest, "import_batch_id is required")
			return
		}
		if len(importBatchIDs) > maxImportBatchIDs {
			writeError(w, http.StatusBadRequest, "import_batch_id exceeds the 25 cap")
			return
		}
		for _, batchID := range importBatchIDs {
			if _, err := uuid.Parse(batchID); err != nil {
				writeError(w, http.StatusBadRequest, "import_batch_id must be a well-formed uuid")
				return
			}
		}

		rules, err := summary(r.Context(), importBatchIDs)
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "invoice: violation summary", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}

		// Second nil guard, after Store.ViolationSummary's own: this handler
		// is a factory over an arbitrary summary func, and a nil []RuleCount
		// marshals to JSON null, not [].
		if rules == nil {
			rules = []RuleCount{}
		}

		writeJSON(w, http.StatusOK, violationSummaryResponse{Rules: rules})
	}
}

// statusForErr maps a store/domain error to the HTTP status + message the
// handlers above write to the response ([D4]/[D12] error-map table).
// db.ErrNoTenant is 401 (fail-closed, missing identity never reaches here in
// practice since every handler checks identity first, but this is the
// defense-in-depth mirror of portfolio's own statusForErr);
// db.ErrNotActiveMember is 403; ErrValidation is 400 with the wrapped
// message; ErrNotFound is 404; ErrDuplicateNumber/
// ErrRedundantTransition/ErrIllegalTransition/ErrAwaitingApproval are 409;
// the last answers with awaitingApprovalReason, the shared refusal sentence
// (TestStatusForErr_AwaitingApprovalIs409); anything else
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
	case errors.Is(err, db.ErrNotActiveMember):
		return http.StatusForbidden, db.NotActiveMemberMessage
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
	case errors.Is(err, ErrAwaitingApproval):
		return http.StatusConflict, awaitingApprovalReason
	case errors.Is(err, ErrNotDraft):
		return http.StatusConflict, "invoice is not a draft"
	case errors.Is(err, ErrStaleValidation):
		return http.StatusConflict, "invoice changed during validation"
	case errors.Is(err, ErrNotFixable):
		return http.StatusConflict, "invoice is not in a fixable state"
	case errors.Is(err, ErrNotKeepable):
		return http.StatusConflict, "invoice must be a draft with a blocking violation to be kept as-is"
	case errors.Is(err, ErrNotPermitted):
		return http.StatusForbidden, "approver rights are required to mark an invoice resolved outside the system"
	case errors.Is(err, ErrNotResolvable):
		return http.StatusConflict, "only a failed invoice can be marked resolved outside the system"
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
