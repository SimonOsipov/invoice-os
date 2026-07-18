// M4-02-03 (task-98): HTTP acceptance tests for internal/invoice's four
// handlers -- written BEFORE the real handler logic exists (RED against
// handlers.go's not-implemented stub: every handler currently always answers
// 501 "not implemented" without decoding the request, checking identity, or
// calling the injected store closure, so every assertion below fails on its
// status/body value, not on a compile error). httptest + fake store
// closures, no DB -- mirrors internal/portfolio/portfolio_test.go's
// doCreate/doGet/doList idiom (net/http/httptest, auth.WithIdentity for
// identity injection, r.SetPathValue for path params).
//
// Spec-to-test map (Test Specs table, M4-02-03 story / task-98):
//
//	INV-HTTP-01 TestCreateHandler_201                        (also asserts store called with decoded input)
//	INV-HTTP-02 TestCreateHandler_MissingEntityID400
//	INV-HTTP-02 TestCreateHandler_MissingInvoiceNumber400
//	INV-HTTP-02 TestCreateHandler_StoreValidationError400     (ErrValidation passthrough, error-map table)
//	INV-HTTP-03 TestCreateHandler_NoIdentity401
//	INV-HTTP-04 TestCreateHandler_DuplicateNumber409
//	INV-HTTP-05 TestGetHandler_200                            (also asserts path-id passthrough + line_items hydrated)
//	INV-HTTP-05 TestGetHandler_NotFound404
//	INV-HTTP-06 TestListHandler_200Envelope
//	INV-HTTP-06 TestListHandler_EmptyState
//	INV-HTTP-06 TestListHandler_LimitDefaultAndClamp
//	INV-HTTP-06 TestListHandler_LimitLessThan1_400
//	INV-HTTP-06 TestListHandler_OffsetNegative400
//	INV-HTTP-06 TestListHandler_NonIntegerLimit400
//	INV-HTTP-07 TestTransitionHandler_200
//	INV-HTTP-08 TestTransitionHandler_Illegal409
//	INV-HTTP-09 TestTransitionHandler_Redundant409
//	INV-HTTP-10 TestTransitionHandler_UnknownStatus400_StoreNotCalled
//	INV-HTTP-11 TestTransitionHandler_NotFound404             (error-map table; not separately numbered in the
//	                                                            13-row table but required by the story's error
//	                                                            model and the [D4] map)
//	INV-HTTP-11 TestTransitionHandler_NoIdentity401
//	INV-HTTP-12 -- distributed, not a standalone test: every 400/404/409/401 test
//	              above already asserts body.Error != "" (or, for List, the raw
//	              {"error":...} shape), covering "every failure path returns
//	              {"error":"..."}" across representative statuses.
//	(pattern)   TestGetHandler_NoIdentity401, TestListHandler_NoIdentity401 --
//	              identity-first-401 on every route, same pattern as INV-HTTP-03/11
//	              (Get/List don't have their own numbered row in the table).
//
// INV-HTTP-13 (ping stub preserved) is intentionally NOT covered here: the
// /v1/ping stub lives in cmd/invoice/main.go (main package), not
// internal/invoice, and this subtask's scaffold does not touch cmd/invoice/
// main.go at all ("Keep the /v1/ping stub untested-change (it stays)").
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- test-local wire types --------------------------------------------------
//
// These mirror the (future, Stage 3) snake_case JSON wire shapes described in
// task-98's System Design -- they do NOT exist on the production Invoice/
// LineItem types yet (Stage 3 adds the json tags), so they are declared here,
// test-local, purely to marshal request bodies and decode whatever JSON the
// handler under test actually writes.

// createInvoiceRequest mirrors the POST /v1/invoices wire body.
type createInvoiceRequest struct {
	EntityID      string         `json:"entity_id"`
	InvoiceNumber string         `json:"invoice_number"`
	IssueDate     *time.Time     `json:"issue_date,omitempty"`
	SupplierTIN   *string        `json:"supplier_tin,omitempty"`
	SupplierName  *string        `json:"supplier_name,omitempty"`
	BuyerTIN      *string        `json:"buyer_tin,omitempty"`
	BuyerName     *string        `json:"buyer_name,omitempty"`
	Currency      *string        `json:"currency,omitempty"`
	Subtotal      *string        `json:"subtotal,omitempty"`
	VAT           *string        `json:"vat,omitempty"`
	Total         *string        `json:"total,omitempty"`
	LineItems     []lineItemWire `json:"line_items,omitempty"`
}

// editInvoiceRequest mirrors the PATCH /v1/invoices/{id} wire body
// (M4-05-03, [A1]) -- the 9 optional header fields, no entity_id/
// invoice_number/line_items.
type editInvoiceRequest struct {
	IssueDate    *time.Time `json:"issue_date,omitempty"`
	SupplierTIN  *string    `json:"supplier_tin,omitempty"`
	SupplierName *string    `json:"supplier_name,omitempty"`
	BuyerTIN     *string    `json:"buyer_tin,omitempty"`
	BuyerName    *string    `json:"buyer_name,omitempty"`
	Currency     *string    `json:"currency,omitempty"`
	Subtotal     *string    `json:"subtotal,omitempty"`
	VAT          *string    `json:"vat,omitempty"`
	Total        *string    `json:"total,omitempty"`
}

// lineItemWire mirrors one line_items entry in the create wire body / the
// Invoice response body's line_items array.
type lineItemWire struct {
	Description *string `json:"description,omitempty"`
	Quantity    *string `json:"quantity,omitempty"`
	UnitPrice   *string `json:"unit_price,omitempty"`
	LineTotal   *string `json:"line_total,omitempty"`
	LineTax     *string `json:"line_tax,omitempty"`
}

// invoiceBody mirrors the (future) Invoice JSON response shape, plus an Error
// field for the shared {"error":"..."} envelope -- same convention as
// portfolio_test.go's entityBody.
//
// Violations/RuleSetVersionID (task-113/M4-04-06, GAPI-02/03) are additive:
// no existing Create/Get/List/Transition test above references either
// field, so decoding their responses into this wider struct leaves those
// two simply zero-valued, unchanged behaviour for every test already in
// this file.
type invoiceBody struct {
	ID               string          `json:"id"`
	EntityID         string          `json:"entity_id"`
	InvoiceNumber    string          `json:"invoice_number"`
	Status           string          `json:"status"`
	Violations       json.RawMessage `json:"violations"`
	RuleSetVersionID *string         `json:"rule_set_version_id"`
	LineItems        []lineItemWire  `json:"line_items"`
	Error            string          `json:"error"`
}

// transitionRequest mirrors the POST /v1/invoices/{id}/transitions wire body
// ([D12]: a single endpoint, {"target":...}, not per-target sub-paths).
type transitionRequest struct {
	Target string `json:"target"`
}

// listPaginationWire mirrors the "pagination" object in ListHandler's
// envelope.
type listPaginationWire struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total"`
}

// listInvoicesResponse mirrors the GET /v1/invoices response envelope, plus
// an Error field for the shared error envelope.
type listInvoicesResponse struct {
	Invoices   []invoiceBody      `json:"invoices"`
	Pagination listPaginationWire `json:"pagination"`
	Error      string             `json:"error"`
}

// --- request helpers ---------------------------------------------------------

func marshalCreate(t *testing.T, body createInvoiceRequest) string {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	return string(b)
}

func doInvoiceCreate(t *testing.T, create func(ctx context.Context, in CreateInput) (Invoice, error), id *auth.Identity, rawBody string) (*httptest.ResponseRecorder, invoiceBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/invoices", strings.NewReader(rawBody))
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CreateHandler(create, nil).ServeHTTP(rec, r)
	var resp invoiceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

func marshalEdit(t *testing.T, body editInvoiceRequest) string {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal edit request: %v", err)
	}
	return string(b)
}

func doInvoiceGet(t *testing.T, get func(ctx context.Context, id string) (Invoice, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, invoiceBody) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	GetHandler(get, nil).ServeHTTP(rec, r)
	var resp invoiceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

func doInvoiceList(t *testing.T, list func(ctx context.Context, f ListFilter) ([]Invoice, int, error), id *auth.Identity, query string) (*httptest.ResponseRecorder, listInvoicesResponse) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/invoices"+query, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ListHandler(list, nil).ServeHTTP(rec, r)
	var resp listInvoicesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

func doInvoiceTransition(t *testing.T, transition func(ctx context.Context, id string, target Status) (Invoice, error), id *auth.Identity, invoiceID, rawBody string) (*httptest.ResponseRecorder, invoiceBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/invoices/"+invoiceID+"/transitions", strings.NewReader(rawBody))
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	TransitionHandler(transition, nil).ServeHTTP(rec, r)
	var resp invoiceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// doInvoiceEdit drives PATCH /v1/invoices/{id} (M4-05-03) -- cloned from
// doInvoiceTransition: same identity-injection/path-value shape, PATCH
// method, request body optional (an empty rawBody is a valid, if
// content-length-zero, PATCH).
func doInvoiceEdit(t *testing.T, edit func(ctx context.Context, id string, in UpdateInput) (Invoice, error), id *auth.Identity, invoiceID, rawBody string) (*httptest.ResponseRecorder, invoiceBody) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPatch, "/v1/invoices/"+invoiceID, strings.NewReader(rawBody))
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	EditHandler(edit, nil).ServeHTTP(rec, r)
	var resp invoiceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// doInvoiceValidate drives POST /v1/invoices/{id}/validate (task-113/
// M4-04-06's ValidateHandler) -- no request body (unlike Transition), same
// identity-injection/path-value/decode shape as doInvoiceGet.
func doInvoiceValidate(t *testing.T, validate func(ctx context.Context, id string) (Invoice, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, invoiceBody) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/invoices/"+invoiceID+"/validate", nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ValidateHandler(validate, nil).ServeHTTP(rec, r)
	var resp invoiceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// --- Create handler tests (INV-HTTP-01..04) --------------------------------

// TestCreateHandler_201 (INV-HTTP-01): a valid body with identity present
// must produce 201, with the response body reflecting the created Invoice
// (id, status:"draft"), AND create must be called with the decoded input
// (entity_id/invoice_number passed through unchanged).
func TestCreateHandler_201(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	want := Invoice{ID: uuid.NewString(), EntityID: entityID, InvoiceNumber: "INV-0001", Status: StatusDraft}
	var gotIn CreateInput
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	body := marshalCreate(t, createInvoiceRequest{EntityID: entityID, InvoiceNumber: "INV-0001"})
	rec, resp := doInvoiceCreate(t, create, &id, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.ID != want.ID {
		t.Errorf("id = %q, want %q", resp.ID, want.ID)
	}
	if resp.Status != string(StatusDraft) {
		t.Errorf("status = %q, want %q", resp.Status, StatusDraft)
	}
	if gotIn.EntityID != entityID || gotIn.InvoiceNumber != "INV-0001" {
		t.Errorf("create called with unexpected input: %+v, want entity_id=%q invoice_number=%q", gotIn, entityID, "INV-0001")
	}
}

// TestCreateHandler_MalformedJSON400: an unparseable request body must 400
// before create ever runs -- asserted by failing the test if create is
// called. (portfolio parity; not separately numbered in the Test Specs
// table, required by the Stage 2.5 prompt's minimum-coverage list.)
func TestCreateHandler_MalformedJSON400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		t.Fatal("create must not run when the request body is malformed JSON")
		return Invoice{}, nil
	}
	rec, resp := doInvoiceCreate(t, create, &id, `{"entity_id":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_MissingEntityID400 (INV-HTTP-02): a body with a blank
// entity_id must 400 before create ever runs.
func TestCreateHandler_MissingEntityID400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		t.Fatal("create must not run when entity_id is blank")
		return Invoice{}, nil
	}
	body := marshalCreate(t, createInvoiceRequest{InvoiceNumber: "INV-0001"})
	rec, resp := doInvoiceCreate(t, create, &id, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_MissingInvoiceNumber400 (INV-HTTP-02): a body with a
// blank invoice_number must 400 before create ever runs.
func TestCreateHandler_MissingInvoiceNumber400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		t.Fatal("create must not run when invoice_number is blank")
		return Invoice{}, nil
	}
	body := marshalCreate(t, createInvoiceRequest{EntityID: uuid.NewString()})
	rec, resp := doInvoiceCreate(t, create, &id, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_StoreValidationError400 (INV-HTTP-02, error-map table):
// the store returning ErrValidation must map to 400 with a non-empty error
// message.
func TestCreateHandler_StoreValidationError400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		return Invoice{}, fmt.Errorf("%w: entity_id and invoice_number are required", ErrValidation)
	}
	body := marshalCreate(t, createInvoiceRequest{EntityID: uuid.NewString(), InvoiceNumber: "INV-0001"})
	rec, resp := doInvoiceCreate(t, create, &id, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_NoIdentity401 (INV-HTTP-03): no identity in the request
// context must 401 before create ever runs.
func TestCreateHandler_NoIdentity401(t *testing.T) {
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		t.Fatal("create must not run without an identity")
		return Invoice{}, nil
	}
	body := marshalCreate(t, createInvoiceRequest{EntityID: uuid.NewString(), InvoiceNumber: "INV-0001"})
	rec, resp := doInvoiceCreate(t, create, nil, body)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_DuplicateNumber409 (INV-HTTP-04): the store returning
// ErrDuplicateNumber must map to 409 with a non-empty error message.
func TestCreateHandler_DuplicateNumber409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		return Invoice{}, ErrDuplicateNumber
	}
	body := marshalCreate(t, createInvoiceRequest{EntityID: uuid.NewString(), InvoiceNumber: "INV-0001"})
	rec, resp := doInvoiceCreate(t, create, &id, body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestCreateHandler_DuplicateNumber409_ExactWireContract (PAR-04, M4-06-02):
// tightens TestCreateHandler_DuplicateNumber409 (INV-HTTP-04, directly
// above), which only asserts resp.Error is non-empty. This locks the EXACT
// status code AND EXACT error string (Core AC#3, M4-06 Store-Level
// Duplicate Rule: "Manual POST /v1/invoices continues to reject an
// against-store duplicate with a friendly 409 'duplicate invoice
// number'") -- byte for byte, not merely "some 4xx with some message" --
// so a future statusForErr edit cannot silently reword or re-code the
// manual duplicate response out from under any client parsing it.
func TestCreateHandler_DuplicateNumber409_ExactWireContract(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		return Invoice{}, ErrDuplicateNumber
	}
	body := marshalCreate(t, createInvoiceRequest{EntityID: uuid.NewString(), InvoiceNumber: "INV-0001"})
	rec, resp := doInvoiceCreate(t, create, &id, body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if resp.Error != "duplicate invoice number" {
		t.Errorf("body error = %q, want exact %q (PAR-04, Core AC#3)", resp.Error, "duplicate invoice number")
	}
}

// TestCreateHandler_201_WireShape (QA Mode B adversarial, Surface-Conflict
// verification): a created invoice's RAW response body must be the
// snake_case wire shape the story's System Design specifies -- entity_id,
// invoice_number, status, violations, line_items and rule_set_version_id all
// present.
//
// HISTORY: through M4-02 this test asserted the exact OPPOSITE for
// rule_set_version_id -- that it must NOT appear at all. Invoice.RuleSetVersionID
// carried `json:"-"` because M4-02 never wrote the column (it was always null)
// and M4-02 explicitly DEFERRED the field's wire shape to M4-04; the assertion
// was a deliberate tripwire, set so that dropping the `json:"-"` tag could not
// happen silently -- it had to be a considered decision by whoever defined that
// shape. M4-04-05 §c IS that decision: the validate gate now writes the column,
// so the tag became `json:"rule_set_version_id"` and the tripwire is flipped to
// assert PRESENCE. The tripwire did its job; this comment is its record.
//
// The test still sets RuleSetVersionID to a non-nil value and still checks the
// RAW bytes rather than a decoded struct -- that is what keeps it non-vacuous
// in EITHER direction: a decoded struct would prove nothing about the tag, and
// a nil value would render `null` and pass a naive presence check without
// proving the value is carried.
func TestCreateHandler_201_WireShape(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	entityID := uuid.NewString()
	rsv := "some-rule-set-version-id"
	want := Invoice{
		ID: uuid.NewString(), EntityID: entityID, InvoiceNumber: "INV-0001", Status: StatusDraft,
		Violations:       json.RawMessage(`[]`),
		RuleSetVersionID: &rsv,
		LineItems:        []LineItem{{ID: uuid.NewString(), LineNo: 1}},
	}
	create := func(ctx context.Context, in CreateInput) (Invoice, error) {
		return want, nil
	}
	body := marshalCreate(t, createInvoiceRequest{EntityID: entityID, InvoiceNumber: "INV-0001"})
	rec, _ := doInvoiceCreate(t, create, &id, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	for _, want := range []string{`"entity_id":`, `"invoice_number":"INV-0001"`, `"status":"draft"`, `"line_items":[`, `"violations":[]`} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("body = %s, want raw JSON to contain %s", raw, want)
		}
	}
	if !bytes.Contains(raw, []byte(`"rule_set_version_id":"some-rule-set-version-id"`)) {
		t.Errorf("body = %s, want raw JSON to carry the stamped rule_set_version_id "+
			"(json:\"rule_set_version_id\" -- M4-04-05 §c defines the shape M4-02 deferred)", raw)
	}
}

// --- Get handler tests (INV-HTTP-05) ----------------------------------------

// TestGetHandler_200 (INV-HTTP-05): a get resolving an invoice must produce
// 200 with the invoice's id + hydrated line_items in the body, AND get must
// be called with the exact path id (passthrough).
func TestGetHandler_200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{
		ID: invoiceID, EntityID: uuid.NewString(), InvoiceNumber: "INV-0001", Status: StatusDraft,
		LineItems: []LineItem{{ID: uuid.NewString(), LineNo: 1}},
	}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		if gotID != invoiceID {
			t.Fatalf("get called with id = %q, want %q", gotID, invoiceID)
		}
		return want, nil
	}
	rec, resp := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.ID != invoiceID {
		t.Errorf("id = %q, want %q", resp.ID, invoiceID)
	}
	if len(resp.LineItems) != 1 {
		t.Errorf("line_items length = %d, want 1 (line items hydrated)", len(resp.LineItems))
	}
}

// TestGetHandler_NotFound404 (INV-HTTP-05): the store returning ErrNotFound
// must map to 404 with a non-empty error message -- the shape a
// unknown/cross-tenant id resolves to.
func TestGetHandler_NotFound404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{}, ErrNotFound
	}
	rec, resp := doInvoiceGet(t, get, &id, uuid.NewString())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestGetHandler_NoIdentity401 (identity-first pattern, same as
// INV-HTTP-03/11): no identity in the request context must 401 before get
// ever runs.
func TestGetHandler_NoIdentity401(t *testing.T) {
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		t.Fatal("get must not run without an identity")
		return Invoice{}, nil
	}
	rec, resp := doInvoiceGet(t, get, nil, uuid.NewString())

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- List handler tests (INV-HTTP-06) ---------------------------------------

// TestListHandler_200Envelope (INV-HTTP-06): a non-empty result must produce
// 200 with the {"invoices":[...],"pagination":{...}} envelope.
func TestListHandler_200Envelope(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invID := uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: invID, Status: StatusDraft}}, 1, nil
	}
	rec, resp := doInvoiceList(t, list, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(resp.Invoices) != 1 || resp.Invoices[0].ID != invID {
		t.Errorf("invoices = %+v, want one invoice with id %q", resp.Invoices, invID)
	}
	if resp.Pagination.Total != 1 {
		t.Errorf("pagination.total = %d, want 1", resp.Pagination.Total)
	}
}

// TestListHandler_EmptyState (INV-HTTP-06): the store returning ([]Invoice{},
// 0, nil) must produce 200 with the RAW response body containing
// "invoices":[] (never "invoices":null).
func TestListHandler_EmptyState(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{}, 0, nil
	}
	rec, _ := doInvoiceList(t, list, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte(`"invoices":[]`)) {
		t.Errorf("body = %s, want raw JSON to contain \"invoices\":[] (not null)", body)
	}
}

// TestListHandler_LimitDefaultAndClamp (INV-HTTP-06): the ListFilter the
// handler passes to the store must default an omitted ?limit= to 50, and
// clamp an over-large ?limit=500 down to 200 (portfolio's exact clamping
// rule -- Store.List does not clamp itself).
func TestListHandler_LimitDefaultAndClamp(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantLimit int
	}{
		{"omitted defaults to 50", "", 50},
		{"500 clamps to 200", "?limit=500", 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			var captured ListFilter
			called := false
			list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
				called = true
				captured = f
				return []Invoice{}, 0, nil
			}
			rec, _ := doInvoiceList(t, list, &id, tc.query)
			if !called {
				t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
			}
			if captured.Limit != tc.wantLimit {
				t.Errorf("captured ListFilter.Limit = %d, want %d", captured.Limit, tc.wantLimit)
			}
		})
	}
}

// TestListHandler_LimitLessThan1_400 (INV-HTTP-06): ?limit=0 must 400 before
// the store is ever called.
func TestListHandler_LimitLessThan1_400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		t.Fatal("store.List must not run when limit < 1")
		return nil, 0, nil
	}
	rec, resp := doInvoiceList(t, list, &id, "?limit=0")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestListHandler_OffsetNegative400 (INV-HTTP-06): ?offset=-1 must 400 before
// the store is ever called.
func TestListHandler_OffsetNegative400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		t.Fatal("store.List must not run when offset < 0")
		return nil, 0, nil
	}
	rec, resp := doInvoiceList(t, list, &id, "?offset=-1")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestListHandler_NonIntegerLimit400 (INV-HTTP-06): a non-integer ?limit=
// must 400 before the store is ever called.
func TestListHandler_NonIntegerLimit400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		t.Fatal("store.List must not run when limit is not an integer")
		return nil, 0, nil
	}
	rec, resp := doInvoiceList(t, list, &id, "?limit=abc")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestListHandler_EnvelopeExactKeysAndEffectiveClampedValues (QA Mode B
// adversarial): the RAW response body's top-level envelope must have EXACTLY
// two keys, "invoices" and "pagination" (no extra keys, no drift from the
// {invoices,pagination} shape the story specifies), and pagination.limit/
// offset in the body must reflect the EFFECTIVE post-clamp values (?limit=500
// clamped to 200) -- not merely the ListFilter captured by the fake store
// (TestListHandler_LimitDefaultAndClamp already covers that half; this closes
// the gap by asserting on what the client actually receives).
func TestListHandler_EnvelopeExactKeysAndEffectiveClampedValues(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{}, 0, nil
	}
	rec, _ := doInvoiceList(t, list, &id, "?limit=500&offset=3")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw envelope: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("envelope has %d top-level keys, want exactly 2 (invoices, pagination): %s", len(raw), rec.Body.String())
	}
	if _, ok := raw["invoices"]; !ok {
		t.Error("envelope missing \"invoices\" key")
	}
	if _, ok := raw["pagination"]; !ok {
		t.Error("envelope missing \"pagination\" key")
	}

	var pag listPaginationWire
	if err := json.Unmarshal(raw["pagination"], &pag); err != nil {
		t.Fatalf("decode pagination: %v", err)
	}
	if pag.Limit != 200 {
		t.Errorf("response body pagination.limit = %d, want 200 (post-clamp effective value, not the raw ?limit=500)", pag.Limit)
	}
	if pag.Offset != 3 {
		t.Errorf("response body pagination.offset = %d, want 3", pag.Offset)
	}
}

// TestListHandler_NoIdentity401 (identity-first pattern, same as
// INV-HTTP-03/11): no identity in the request context must 401 before list
// ever runs.
func TestListHandler_NoIdentity401(t *testing.T) {
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		t.Fatal("store.List must not run without an identity")
		return nil, 0, nil
	}
	rec, resp := doInvoiceList(t, list, nil, "")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- Transition handler tests (INV-HTTP-07..11) -----------------------------

// TestTransitionHandler_200 (INV-HTTP-07): a legal target must produce 200
// with the updated Invoice's status in the body, AND transition must be
// called with the exact path id + decoded target.
//
// RETARGETED from "validated" to "queued" by M4-04-06/task-113. This test's
// SUBJECT is unchanged and was never the target's identity: it is the 200, the
// body's status, and the exact id/target passthrough. "validated" is no longer
// expressible here -- TransitionHandler now refuses it with a 409 pre-call
// guard ([validated-is-earned] [R1]: that status is earned only via POST
// /v1/invoices/{id}/validate), which would turn this test into an assertion
// about the guard rather than about its own subject. "queued" is canonical
// (invoice.go:32) and clears !target.valid() identically, so every original
// assertion still runs unchanged. The refused target's own coverage is GAPI-15.
func TestTransitionHandler_200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusQueued}
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		if gotID != invoiceID || target != StatusQueued {
			t.Fatalf("transition called with id=%q target=%q, want id=%q target=%q", gotID, target, invoiceID, StatusQueued)
		}
		return want, nil
	}
	body, err := json.Marshal(transitionRequest{Target: "queued"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusQueued) {
		t.Errorf("status = %q, want %q", resp.Status, StatusQueued)
	}
}

// TestTransitionHandler_Illegal409 (INV-HTTP-08): the store returning
// ErrIllegalTransition must map to 409 with a non-empty error message.
func TestTransitionHandler_Illegal409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		return Invoice{}, ErrIllegalTransition
	}
	body, err := json.Marshal(transitionRequest{Target: "accepted"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestTransitionHandler_Redundant409 (INV-HTTP-09): the store returning
// ErrRedundantTransition (a no-op) must map to 409 with a non-empty error
// message.
func TestTransitionHandler_Redundant409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		return Invoice{}, ErrRedundantTransition
	}
	body, err := json.Marshal(transitionRequest{Target: "draft"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestTransitionHandler_UnknownStatus400_StoreNotCalled (INV-HTTP-10): a
// target string that is not one of the 7 canonical Status values must 400
// "unknown status" WITHOUT ever calling transition.
func TestTransitionHandler_UnknownStatus400_StoreNotCalled(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		t.Fatal("transition must not run when target is not one of the 7 canonical statuses")
		return Invoice{}, nil
	}
	body, err := json.Marshal(transitionRequest{Target: "foo"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestTransitionHandler_MalformedOrEmptyBody400_StoreNotCalled (QA Mode B
// adversarial, portfolio parity with TestCreateHandler_MalformedJSON400): an
// unparseable or entirely empty request body must 400 before transition ever
// runs -- asserted by failing the test if transition is called. Covers both
// "path id but bad body" and "path id but no body" from the QA prompt's
// optional coverage list.
func TestTransitionHandler_MalformedOrEmptyBody400_StoreNotCalled(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"target":`},
		{"empty body", ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			invoiceID := uuid.NewString()
			transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
				t.Fatal("transition must not run when the request body is malformed or empty")
				return Invoice{}, nil
			}
			rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, tc.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			if resp.Error == "" {
				t.Error("expected a non-empty error message in the body")
			}
		})
	}
}

// TestTransitionHandler_MissingOrEmptyTarget400_StoreNotCalled (QA Mode B
// adversarial): a well-formed JSON body whose target is an empty string, or
// which omits the target key entirely, must 400 "unknown status" WITHOUT
// ever calling transition -- the empty-string edge of Status.valid()'s
// membership check, distinct from INV-HTTP-10's garbage-string ("foo") case.
func TestTransitionHandler_MissingOrEmptyTarget400_StoreNotCalled(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty string target", `{"target":""}`},
		{"target key absent", `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			invoiceID := uuid.NewString()
			transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
				t.Fatal("transition must not run when target is empty/absent")
				return Invoice{}, nil
			}
			rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, tc.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			if resp.Error == "" {
				t.Error("expected a non-empty error message in the body")
			}
		})
	}
}

// TestTransitionHandler_NotFound404 (error-map table; not separately
// numbered in the 13-row Test Specs table, but required by the story's error
// model): the store returning ErrNotFound must map to 404.
//
// RETARGETED to "queued" by M4-04-06/task-113 (subject unchanged -- the
// ErrNotFound -> 404 mapping). Under "validated" the new pre-call guard would
// 409 before the store ran at all, so the stub's ErrNotFound would never be
// reached and this test would silently stop testing its own mapping.
func TestTransitionHandler_NotFound404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		return Invoice{}, ErrNotFound
	}
	body, err := json.Marshal(transitionRequest{Target: "queued"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestTransitionHandler_NoIdentity401 (INV-HTTP-11): no identity in the
// request context must 401 before transition ever runs.
//
// RETARGETED to "queued" by M4-04-06/task-113 for CONSISTENCY, not necessity:
// the identity check runs first, so this test 401s before reaching the new
// validated-target guard either way and was never at risk. Retargeted so that
// no test in this file posts a target the endpoint now refuses.
func TestTransitionHandler_NoIdentity401(t *testing.T) {
	invoiceID := uuid.NewString()
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		t.Fatal("transition must not run without an identity")
		return Invoice{}, nil
	}
	body, err := json.Marshal(transitionRequest{Target: "queued"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, nil, invoiceID, string(body))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// ---------------------------------------------------------------------------
// task-113 / M4-04-06 -- Mode A RED specs for the HTTP half of GAPI-01..17:
// the new ValidateHandler (GAPI-01..09) and the transitions guard
// (GAPI-15..17). GAPI-10..14 (Gate.Evaluate/Validate, no HTTP layer) live in
// gate_test.go instead.
//
// GAPI-01..09 run against ValidateHandler, currently gate_qa_scaffold.go's
// blanket-501 stub (see that file's header) -- every test below fails on
// the real status-code assertion it names, not a compile error.
//
// GAPI-15..17 run against the REAL, shipped TransitionHandler above
// (handlers.go) -- this section adds NO scaffold and touches NO existing
// test. The `target == StatusValidated` -> 409 pre-call guard
// ([validated-is-earned] [R1]) is simply absent from TransitionHandler
// today, so GAPI-15 (POST .../transitions {"target":"validated"}) currently
// falls through to the stub `transition` closure and returns whatever it
// returns (200, called=true) -- the OPPOSITE of the 409/not-called this
// spec demands, which is exactly what makes it discriminate the guard's
// absence. GAPI-16/17 assert properties that are ALREADY true of the
// shipped handler (a non-"validated" target is unaffected by a guard that
// only checks target==validated; the pre-existing !target.valid() 400
// check is untouched) -- they are boundary/regression coverage for the
// guard's NARROWNESS, proving it does not overreach, not new RED specs; they
// are expected to already pass and to keep passing once the guard lands
// (see this story's task-113 return-to-orchestrator notes for the explicit
// call-out).
//
// Spec-to-test map (task-113's Test Specs table):
//
//	GAPI-01 TestValidateHandler_NoIdentity401
//	GAPI-02 TestValidateHandler_CleanDraft200
//	GAPI-03 TestValidateHandler_BlockingViolation200StaysDraft
//	GAPI-04 TestValidateHandler_NotFound404
//	GAPI-05 TestValidateHandler_NotDraft409
//	GAPI-06 TestValidateHandler_StaleValidation409
//	GAPI-07 TestValidateHandler_Upstream502
//	GAPI-08 TestValidateHandler_NoActiveRuleSet503
//	GAPI-09 TestValidateHandler_MalformedID400
//	GAPI-15 TestTransitionHandler_ValidatedTarget409GuardPreCall
//	GAPI-16 TestTransitionHandler_QueuedTargetStillReachesStub200
//	GAPI-17 TestTransitionHandler_NonsenseTargetStill400UnknownStatus
// ---------------------------------------------------------------------------

// --- Validate handler tests (GAPI-01..09) -----------------------------------

// TestValidateHandler_NoIdentity401 (GAPI-01): no identity in the request
// context must 401 before validate ever runs -- same identity-first-401
// order as every other handler in this file.
func TestValidateHandler_NoIdentity401(t *testing.T) {
	invoiceID := uuid.NewString()
	validate := func(ctx context.Context, id string) (Invoice, error) {
		t.Fatal("validate must not run without an identity")
		return Invoice{}, nil
	}
	rec, resp := doInvoiceValidate(t, validate, nil, invoiceID)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestValidateHandler_CleanDraft200 (GAPI-02, Core AC #6): a draft that
// passes must 200 with status:"validated", violations:[], and a non-null
// rule_set_version_id -- and validate must be called with the exact path
// id.
func TestValidateHandler_CleanDraft200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	versionID := uuid.NewString()
	want := Invoice{
		ID:               invoiceID,
		Status:           StatusValidated,
		Violations:       json.RawMessage(`[]`),
		RuleSetVersionID: &versionID,
	}
	validate := func(ctx context.Context, gotID string) (Invoice, error) {
		if gotID != invoiceID {
			t.Fatalf("validate called with id=%q, want %q", gotID, invoiceID)
		}
		return want, nil
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusValidated) {
		t.Errorf("status = %q, want %q", resp.Status, StatusValidated)
	}
	if string(resp.Violations) != "[]" {
		t.Errorf("violations = %s, want []", resp.Violations)
	}
	if resp.RuleSetVersionID == nil || *resp.RuleSetVersionID != versionID {
		t.Errorf("rule_set_version_id = %v, want %q", resp.RuleSetVersionID, versionID)
	}
}

// TestValidateHandler_BlockingViolation200StaysDraft (GAPI-03, [error
// semantics], Core AC #3): a draft that fails must still 200 -- NEVER an
// HTTP error -- with status staying "draft" and the violation present in
// the body as ordinary data.
func TestValidateHandler_BlockingViolation200StaysDraft(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	violations := json.RawMessage(`[{"rule_key":"vat-standard-rate","severity":"error","message":"VAT must equal 7.5% of the subtotal."}]`)
	want := Invoice{ID: invoiceID, Status: StatusDraft, Violations: violations}
	validate := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a blocking violation is normal success-payload data, never an HTTP "+
			"error [error semantics] (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusDraft) {
		t.Errorf("status = %q, want %q", resp.Status, StatusDraft)
	}
	if len(resp.Violations) == 0 || string(resp.Violations) == "[]" || string(resp.Violations) == "null" {
		t.Errorf("violations = %s, want a non-empty violation set carried in the body", resp.Violations)
	}
}

// TestValidateHandler_NotFound404 (GAPI-04): the gate returning ErrNotFound
// must map to 404.
func TestValidateHandler_NotFound404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	validate := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{}, ErrNotFound
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestValidateHandler_NotDraft409 (GAPI-05): the gate returning ErrNotDraft
// must map to 409 ([gate-scope-draft-only]: the gate is draft-only).
func TestValidateHandler_NotDraft409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	validate := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{}, ErrNotDraft
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestValidateHandler_StaleValidation409 (GAPI-06): the gate returning
// ErrStaleValidation must map to 409 ([toctou-staleness]).
func TestValidateHandler_StaleValidation409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	validate := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{}, ErrStaleValidation
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestValidateHandler_Upstream502 (GAPI-07): the gate returning ErrUpstream
// (04 down/broken) must map to 502 -- and MUST NOT be a 200 with no
// violations, which would launder an outage into "clean".
func TestValidateHandler_Upstream502(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	validate := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{}, ErrUpstream
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 -- an unreachable/broken 04 must never be laundered into a clean 200 "+
			"(body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestValidateHandler_NoActiveRuleSet503 (GAPI-08): the gate returning
// ErrNoActiveRuleSet must map to 503 -- 04 is healthy but has nothing
// published to evaluate against, distinguishable from ErrUpstream's 502.
func TestValidateHandler_NoActiveRuleSet503(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	validate := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{}, ErrNoActiveRuleSet
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestValidateHandler_MalformedID400 (GAPI-09): a malformed (non-uuid) path
// id traces to Gate.Validate's Store.Get raising 22P02 -> ErrValidation,
// which the EXISTING statusForErr case (unchanged by this story) already
// maps to 400 -- exercised here at the HTTP layer via the injected closure,
// same as every other error-map row above.
func TestValidateHandler_MalformedID400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	validate := func(ctx context.Context, gotID string) (Invoice, error) {
		return Invoice{}, fmt.Errorf("%w: malformed invoice id", ErrValidation)
	}
	rec, resp := doInvoiceValidate(t, validate, &id, "not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- Transitions guard tests (GAPI-15..17, [validated-is-earned] [R1]) -----

// TestTransitionHandler_ValidatedTarget409GuardPreCall (GAPI-15): POST
// .../transitions {"target":"validated"} must 409 BEFORE the store is
// called -- the stub transition func must never run. This is the guard's
// own discriminating test: TransitionHandler today has no guard, so
// target=="validated" clears the shipped !target.valid() check and falls
// straight through to the stub, which returns success -- yielding 200 with
// called==true, the OPPOSITE of what this test demands. It fails on BOTH
// the status-code assertion and the not-called assertion until the guard
// is added.
func TestTransitionHandler_ValidatedTarget409GuardPreCall(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	called := false
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		called = true
		return Invoice{ID: gotID, Status: target}, nil
	}
	body, err := json.Marshal(transitionRequest{Target: "validated"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 -- validated is earned only via POST .../validate, never via the "+
			"transitions endpoint [validated-is-earned] [R1] (body=%s)", rec.Code, rec.Body.String())
	}
	if called {
		t.Error("the stub transition func WAS called -- the guard must be a PRE-CALL check that refuses " +
			"target=validated before Store.Transition ever runs")
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestTransitionHandler_QueuedTargetStillReachesStub200 (GAPI-16): a
// non-"validated" legal target (queued) must be UNAFFECTED by the guard --
// still reaches the stub and still 200s. Proves the guard is narrow
// (target==validated only), not a blanket refusal of the whole endpoint.
// Already true of the shipped handler (there is no guard yet to overreach);
// stays true once the guard lands, since it only special-cases
// target==StatusValidated.
func TestTransitionHandler_QueuedTargetStillReachesStub200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusQueued}
	called := false
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		called = true
		if gotID != invoiceID || target != StatusQueued {
			t.Fatalf("transition called with id=%q target=%q, want id=%q target=%q", gotID, target, invoiceID, StatusQueued)
		}
		return want, nil
	}
	body, err := json.Marshal(transitionRequest{Target: "queued"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- the guard is narrow (target==validated only), not a blanket refusal "+
			"[validated-is-earned] [R1] (body=%s)", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("the stub transition func was NOT called -- a non-validated target must still reach the store")
	}
	if resp.Status != string(StatusQueued) {
		t.Errorf("status = %q, want %q", resp.Status, StatusQueued)
	}
}

// TestTransitionHandler_NonsenseTargetStill400UnknownStatus (GAPI-17): an
// unknown target string must still 400 "unknown status" via the shipped
// !target.valid() check, unchanged by the new guard -- the guard sits AFTER
// that check (handlers.go's order: !target.valid() -> 400, THEN the new
// target==validated -> 409, THEN transition(...)), so a garbage target
// never reaches the guard at all. Already true of the shipped handler;
// stays true once the guard lands, since !target.valid() rejects "nonsense"
// before the guard's own comparison ever runs.
func TestTransitionHandler_NonsenseTargetStill400UnknownStatus(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		t.Fatal("transition must not run for an unknown target status")
		return Invoice{}, nil
	}
	body, err := json.Marshal(transitionRequest{Target: "nonsense"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 -- the shipped !target.valid() guard fires first, unchanged by the new "+
			"validated-target guard [validated-is-earned] [R1] (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// ---------------------------------------------------------------------------
// M4-05-03 (task-122) -- Mode A RED specs for PATCH /v1/invoices/{id}
// (EditHandler) and the new ErrNotFixable->409 statusForErr row.
//
// EditHandler is currently handlers.go's blanket-501 STUB (see its own
// "STUB — replaced by M4-05-03 executor" marker): it always answers 501
// "not implemented [M4-05-03]" without decoding the request, checking
// identity, or calling the injected edit closure -- every status-code
// assertion below fails on that mismatch, never on a compile error.
// statusForErr has NO ErrNotFixable case yet, so it falls through to the
// default (500, "internal server error") -- TestStatusForErr_NotFixableIs409
// fails on that value, also not a compile error.
//
// Spec-to-test map (Test Specs table, M4-05-03 story / task-122):
//
//	identity   TestEditHandler_Unauthenticated401
//	decode     TestEditHandler_MalformedBody400
//	Core AC #1 TestEditHandler_NotFixable409
//	[A7]       TestEditHandler_AllNil400
//	not-found  TestEditHandler_NotFound404
//	Core AC #2 TestEditHandler_DemotionReturns200Draft
//	Core AC #3 TestEditHandler_NoOpReturns200Validated
//	Core AC #1 TestStatusForErr_NotFixableIs409
// ---------------------------------------------------------------------------

// TestEditHandler_Unauthenticated401: no identity in the request context
// must 401 before edit ever runs -- same identity-first-401 order as every
// other handler in this file.
func TestEditHandler_Unauthenticated401(t *testing.T) {
	invoiceID := uuid.NewString()
	called := false
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		called = true
		return Invoice{}, nil
	}
	body := marshalEdit(t, editInvoiceRequest{VAT: strPtr("7.50")})
	rec, resp := doInvoiceEdit(t, edit, nil, invoiceID, body)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity in context (body=%s)", rec.Code, rec.Body.String())
	}
	if called {
		t.Error("edit must not run without an identity")
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestEditHandler_MalformedBody400: an unparseable request body (with
// identity present) must 400 "invalid request body" before edit ever runs --
// portfolio/Create/Transition parity.
func TestEditHandler_MalformedBody400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	called := false
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		called = true
		return Invoice{}, nil
	}
	rec, resp := doInvoiceEdit(t, edit, &id, invoiceID, `{"vat":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if called {
		t.Error("edit must not run when the request body is malformed JSON")
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestEditHandler_NotFixable409 (Core AC #1): the store returning
// ErrNotFixable must map to 409 -- the edit surface is restricted to the two
// fixable states (draft, validated), and this is the HTTP-layer proof of
// that guard's error mapping.
func TestEditHandler_NotFixable409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		return Invoice{}, ErrNotFixable
	}
	body := marshalEdit(t, editInvoiceRequest{VAT: strPtr("7.50")})
	rec, resp := doInvoiceEdit(t, edit, &id, invoiceID, body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestEditHandler_AllNil400 ([A7]): the store returning ErrValidation (the
// all-nil UpdateInput guard) must map to 400 with the wrapped message --
// matching the EXISTING statusForErr ErrValidation case, unchanged by this
// story.
func TestEditHandler_AllNil400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		return Invoice{}, fmt.Errorf("%w: no fields to update", ErrValidation)
	}
	body := marshalEdit(t, editInvoiceRequest{})
	rec, resp := doInvoiceEdit(t, edit, &id, invoiceID, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
	if !strings.Contains(resp.Error, "no fields to update") {
		t.Errorf("error = %q, want it to carry the wrapped ErrValidation message", resp.Error)
	}
}

// TestEditHandler_NotFound404: the store returning ErrNotFound must map to
// 404 -- covers both a genuinely unknown id and a cross-tenant one.
func TestEditHandler_NotFound404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		return Invoice{}, ErrNotFound
	}
	body := marshalEdit(t, editInvoiceRequest{VAT: strPtr("7.50")})
	rec, resp := doInvoiceEdit(t, edit, &id, uuid.NewString(), body)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestEditHandler_DemotionReturns200Draft (Core AC #2): a content-changing
// edit to a validated invoice must 200 with body status "draft" -- AND edit
// must be called with an UpdateInput whose fields map 1:1 from the decoded
// request body (VAT passthrough, the same passthrough-assertion pattern as
// TestCreateHandler_201/TestGetHandler_200).
func TestEditHandler_DemotionReturns200Draft(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn UpdateInput
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		if gotID != invoiceID {
			t.Fatalf("edit called with id = %q, want %q", gotID, invoiceID)
		}
		gotIn = in
		return want, nil
	}
	body := marshalEdit(t, editInvoiceRequest{VAT: strPtr("9.99")})
	rec, resp := doInvoiceEdit(t, edit, &id, invoiceID, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusDraft) {
		t.Errorf("status = %q, want %q", resp.Status, StatusDraft)
	}
	if gotIn.VAT == nil || *gotIn.VAT != "9.99" {
		t.Errorf("edit called with UpdateInput.VAT = %v, want a non-nil pointer to %q", gotIn.VAT, "9.99")
	}
}

// TestEditHandler_NoOpReturns200Validated (Core AC #3): a no-op edit on a
// validated invoice must 200 with body status "validated" -- no demotion.
func TestEditHandler_NoOpReturns200Validated(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusValidated}
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		return want, nil
	}
	body := marshalEdit(t, editInvoiceRequest{VAT: strPtr("7.50")})
	rec, resp := doInvoiceEdit(t, edit, &id, invoiceID, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusValidated) {
		t.Errorf("status = %q, want %q", resp.Status, StatusValidated)
	}
}

// TestStatusForErr_NotFixableIs409 (Core AC #1): statusForErr(ErrNotFixable)
// must return (409, non-empty msg) -- unit-level, no HTTP round-trip. This
// is the discriminating test for the new statusForErr case itself: today
// ErrNotFixable falls through to the unmapped default (500, "internal
// server error"), so this fails on BOTH the status code and, incidentally,
// the message-emptiness check would still pass (the default message is
// non-empty) -- the status-code assertion alone is the RED signal.
func TestStatusForErr_NotFixableIs409(t *testing.T) {
	status, msg := statusForErr(ErrNotFixable)
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
	if msg == "" {
		t.Error("expected a non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// M4-05-03 (task-122) -- QA Mode B adversarial coverage for EditHandler,
// added post-implementation (commit 7bd2a8c). All 8 Mode A specs above are
// green; the two tests below close the one genuine gap found on top of them.
// ---------------------------------------------------------------------------

// TestEditHandler_AllFieldsMapOneToOne (Mode B adversarial, highest-value
// gap): a PATCH body carrying values for ALL 9 header MBS-content fields
// must produce an UpdateInput with every corresponding field non-nil and
// equal to what was sent. TestEditHandler_DemotionReturns200Draft above only
// asserts VAT passthrough -- EditHandler's editReq->UpdateInput mapping is
// hand-written field-by-field (not a loop or reflection-based copy), so a
// typo or omission on any ONE of the other 8 lines (e.g. dropping
// BuyerName, or transposing SupplierTIN/BuyerTIN) would slip past every
// other Edit test in this file undetected.
func TestEditHandler_AllFieldsMapOneToOne(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	issueDate := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	req := editInvoiceRequest{
		IssueDate:    &issueDate,
		SupplierTIN:  strPtr("TIN-SUP-1"),
		SupplierName: strPtr("Supplier Co"),
		BuyerTIN:     strPtr("TIN-BUY-1"),
		BuyerName:    strPtr("Buyer Co"),
		Currency:     strPtr("NGN"),
		Subtotal:     strPtr("100.00"),
		VAT:          strPtr("7.50"),
		Total:        strPtr("107.50"),
	}

	var gotIn UpdateInput
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	body := marshalEdit(t, req)
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.IssueDate == nil || !gotIn.IssueDate.Equal(issueDate) {
		t.Errorf("UpdateInput.IssueDate = %v, want %v", gotIn.IssueDate, issueDate)
	}
	if gotIn.SupplierTIN == nil || *gotIn.SupplierTIN != "TIN-SUP-1" {
		t.Errorf("UpdateInput.SupplierTIN = %v, want a non-nil pointer to %q", gotIn.SupplierTIN, "TIN-SUP-1")
	}
	if gotIn.SupplierName == nil || *gotIn.SupplierName != "Supplier Co" {
		t.Errorf("UpdateInput.SupplierName = %v, want a non-nil pointer to %q", gotIn.SupplierName, "Supplier Co")
	}
	if gotIn.BuyerTIN == nil || *gotIn.BuyerTIN != "TIN-BUY-1" {
		t.Errorf("UpdateInput.BuyerTIN = %v, want a non-nil pointer to %q", gotIn.BuyerTIN, "TIN-BUY-1")
	}
	if gotIn.BuyerName == nil || *gotIn.BuyerName != "Buyer Co" {
		t.Errorf("UpdateInput.BuyerName = %v, want a non-nil pointer to %q", gotIn.BuyerName, "Buyer Co")
	}
	if gotIn.Currency == nil || *gotIn.Currency != "NGN" {
		t.Errorf("UpdateInput.Currency = %v, want a non-nil pointer to %q", gotIn.Currency, "NGN")
	}
	if gotIn.Subtotal == nil || *gotIn.Subtotal != "100.00" {
		t.Errorf("UpdateInput.Subtotal = %v, want a non-nil pointer to %q", gotIn.Subtotal, "100.00")
	}
	if gotIn.VAT == nil || *gotIn.VAT != "7.50" {
		t.Errorf("UpdateInput.VAT = %v, want a non-nil pointer to %q", gotIn.VAT, "7.50")
	}
	if gotIn.Total == nil || *gotIn.Total != "107.50" {
		t.Errorf("UpdateInput.Total = %v, want a non-nil pointer to %q", gotIn.Total, "107.50")
	}
}

// TestEditHandler_UnknownFieldIgnored200 (Mode B adversarial): an unknown/
// extra JSON key in the PATCH body -- including entity_id, which [D9] says
// is deliberately NOT part of editReq -- must be silently ignored (standard
// encoding/json Decoder behavior; EditHandler never calls
// .DisallowUnknownFields(), same as every other decode path in this file)
// rather than 400, and must not interfere with decoding the known fields
// alongside it.
func TestEditHandler_UnknownFieldIgnored200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusValidated}
	var gotIn UpdateInput
	edit := func(ctx context.Context, gotID string, in UpdateInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	rec, resp := doInvoiceEdit(t, edit, &id, invoiceID,
		`{"vat":"7.50","not_a_real_field":"whatever","entity_id":"should-be-ignored"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an unknown JSON field (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusValidated) {
		t.Errorf("status = %q, want %q", resp.Status, StatusValidated)
	}
	if gotIn.VAT == nil || *gotIn.VAT != "7.50" {
		t.Errorf("UpdateInput.VAT = %v, want a non-nil pointer to %q -- the unknown fields must not have "+
			"interfered with decoding the known one", gotIn.VAT, "7.50")
	}
}
