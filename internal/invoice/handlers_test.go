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
//	M4-09-02 AC#5 TestListHandler_NeedsAttentionParse
//	[entity-id-restored] TestListHandler_EntityIDParam
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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
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
// Violations/RuleSetVersionID/RuleSetVersion are additive: no pre-existing
// test references any of the three, so they decode as zero values there.
//
// RuleSetVersion can't distinguish JSON null from an absent key (both
// decode to nil *int) -- TestValidateHandler_NilVersionMarshalsNull checks
// raw bytes instead, for that reason.
//
// IRN/CSID/QRPayload/RejectionReasons (M5-05-02, task-238, Stage 1 Part 3
// recommendation) are likewise additive, ahead of the production Invoice
// type actually carrying them -- they decode as zero values (nil/nil) until
// then, same as the trio above.
//
// QRPNGBase64 (M5-09-01, task-250) mirrors RuleSetVersion's own addition
// exactly: additive, ahead of GetHandler actually populating it, decodes as
// nil until then.
type invoiceBody struct {
	ID               string          `json:"id"`
	EntityID         string          `json:"entity_id"`
	InvoiceNumber    string          `json:"invoice_number"`
	Status           string          `json:"status"`
	Violations       json.RawMessage `json:"violations"`
	RuleSetVersionID *string         `json:"rule_set_version_id"`
	RuleSetVersion   *int            `json:"rule_set_version"`
	LineItems        []lineItemWire  `json:"line_items"`
	IRN              *string         `json:"irn"`
	CSID             *string         `json:"csid"`
	QRPayload        *string         `json:"qr_payload"`
	QRPNGBase64      *string         `json:"qr_png_base64"`
	RejectionReasons json.RawMessage `json:"rejection_reasons"`
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
func doInvoiceEdit(t *testing.T, edit func(ctx context.Context, id string, in EditInput) (Invoice, error), id *auth.Identity, invoiceID, rawBody string) (*httptest.ResponseRecorder, invoiceBody) {
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
func doInvoiceValidate(t *testing.T, validate func(ctx context.Context, id string) (Invoice, int, error), id *auth.Identity, invoiceID string) (*httptest.ResponseRecorder, invoiceBody) {
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

// TestListHandler_NeedsAttentionParse (M4-09-02, AC #5,
// [needs-attention-param-strictness]): ListHandler parses ?needs_attention
// via strconv.ParseBool -- absent defaults the captured ListFilter.
// NeedsAttention to false (the zero value, unchanged/unfiltered list);
// "true" sets it true; an unparseable value ("maybe") 400s BEFORE the store
// is ever called, mirroring TestListHandler_NonIntegerLimit400's shape
// exactly, just for a different query param.
//
// RED today: ListHandler does not parse needs_attention at all -- the
// "true" sub-test fails because captured.NeedsAttention stays false (the
// param is silently ignored, not applied), and the "malformed" sub-test
// fails because there is no 400 (t.Fatal fires because store.List runs
// anyway, the guard that would refuse it doesn't exist yet).
func TestListHandler_NeedsAttentionParse(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "")
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.NeedsAttention {
			t.Errorf("captured ListFilter.NeedsAttention = true, want false when ?needs_attention is absent")
		}
	})

	t.Run("true sets the filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "?needs_attention=true")
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if !captured.NeedsAttention {
			t.Errorf("captured ListFilter.NeedsAttention = false, want true for ?needs_attention=true")
		}
	})

	t.Run("unparseable value 400s, store not called", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when needs_attention is not a bool")
			return nil, 0, nil
		}
		rec, resp := doInvoiceList(t, list, &id, "?needs_attention=maybe")

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Error("expected a non-empty error message in the body")
		}
	})
}

// TestListHandler_EntityIDParam ([entity-id-restored], persona-handoff-fix
// regression fix): ListHandler parses ?entity_id with uuid.Parse -- absent
// leaves the captured ListFilter.EntityID at "" (the zero value, unfiltered);
// a well-formed uuid passes through verbatim; a malformed value 400s BEFORE
// the store is ever called, mirroring TestListHandler_NeedsAttentionParse's
// exact shape, just for this param.
func TestListHandler_EntityIDParam(t *testing.T) {
	t.Run("absent applies no filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "")
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.EntityID != "" {
			t.Errorf("captured ListFilter.EntityID = %q, want \"\" when ?entity_id is absent", captured.EntityID)
		}
	})

	t.Run("well-formed uuid sets the filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		entityID := uuid.NewString()
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "?entity_id="+entityID)
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.EntityID != entityID {
			t.Errorf("captured ListFilter.EntityID = %q, want %q", captured.EntityID, entityID)
		}
	})

	t.Run("malformed value 400s, store not called", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when entity_id is not a well-formed uuid")
			return nil, 0, nil
		}
		rec, resp := doInvoiceList(t, list, &id, "?entity_id=not-a-uuid")

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Error("expected a non-empty error message in the body")
		}
	})
}

// TestListHandler_MalformedImportBatchID400 (INVCR-01-06 spec 7, AC-6/AC-1
// fork 1, task-282): ListHandler parses ?import_batch_id with uuid.Parse --
// absent OR EMPTY leaves the captured ListFilter.ImportBatchID at "" (the
// zero value, unfiltered -- [Fork 1: empty param = absent, not 400]); a
// well-formed uuid passes through verbatim; a malformed value 400s BEFORE
// the store is ever called. Mirrors TestListHandler_EntityIDParam's exact
// shape.
//
// RED today: ListHandler does not parse import_batch_id at all, so the
// malformed sub-test fails (200 + store called, not 400) and the
// well-formed-uuid sub-test fails (captured.ImportBatchID stays "", not the
// uuid) -- both value mismatches, not compile errors.
func TestListHandler_MalformedImportBatchID400(t *testing.T) {
	t.Run("absent applies no filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "")
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.ImportBatchID != "" {
			t.Errorf("captured ListFilter.ImportBatchID = %q, want \"\" when ?import_batch_id is absent", captured.ImportBatchID)
		}
	})

	t.Run("empty string applies no filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "?import_batch_id=")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.ImportBatchID != "" {
			t.Errorf("captured ListFilter.ImportBatchID = %q, want \"\" for ?import_batch_id= (empty is absent, not a 400 -- [Fork 1])", captured.ImportBatchID)
		}
	})

	t.Run("well-formed uuid sets the filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		batchID := uuid.NewString()
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "?import_batch_id="+batchID)
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.ImportBatchID != batchID {
			t.Errorf("captured ListFilter.ImportBatchID = %q, want %q", captured.ImportBatchID, batchID)
		}
	})

	t.Run("malformed value 400s, store not called", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when import_batch_id is not a well-formed uuid")
			return nil, 0, nil
		}
		rec, resp := doInvoiceList(t, list, &id, "?import_batch_id=nope")

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Error("expected a non-empty error message in the body")
		}
	})
}

// TestListHandler_UnknownStatus400 (INVCR-01-06 spec 8, AC-6, task-282):
// ListHandler parses ?status via the existing Status.valid() (the same
// "unknown status" 400 TransitionHandler already uses, handlers.go:434) --
// absent leaves the captured ListFilter.Status at "" (zero value,
// unfiltered); one of the 7 canonical values passes through; anything else
// 400s BEFORE the store is ever called. "approved" is D2's forbidden
// vocabulary (not one of the 7 canonical Status values), so this doubles as
// a D2 guard.
//
// RED today: ListHandler does not parse status at all, so "approved" 200s
// with the store called (not 400), and "validated" leaves captured.Status
// == "" (not StatusValidated) -- both value mismatches.
func TestListHandler_UnknownStatus400(t *testing.T) {
	t.Run("absent applies no filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "")
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.Status != "" {
			t.Errorf("captured ListFilter.Status = %q, want \"\" when ?status is absent", captured.Status)
		}
	})

	t.Run("validated sets the filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "?status=validated")
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.Status != StatusValidated {
			t.Errorf("captured ListFilter.Status = %q, want %q", captured.Status, StatusValidated)
		}
	})

	t.Run("approved is unknown, 400s store not called", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when status is not a canonical value")
			return nil, 0, nil
		}
		rec, resp := doInvoiceList(t, list, &id, "?status=approved")

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Error("expected a non-empty error message in the body")
		}
	})
}

// TestListHandler_NonBoolNeedsFix400 (INVCR-01-06 spec 9, AC-6, task-282):
// ListHandler parses ?needs_fix via strconv.ParseBool, mirroring
// TestListHandler_NeedsAttentionParse's exact shape -- absent defaults the
// captured ListFilter.NeedsFix to false; "1" sets it true; an unparseable
// value 400s BEFORE the store is ever called.
//
// RED today: ListHandler does not parse needs_fix at all, so the "1"
// sub-test fails (captured.NeedsFix stays false) and the "malformed"
// sub-test fails (no 400, store runs anyway) -- both value mismatches.
func TestListHandler_NonBoolNeedsFix400(t *testing.T) {
	t.Run("absent defaults to false", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "")
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if captured.NeedsFix {
			t.Errorf("captured ListFilter.NeedsFix = true, want false when ?needs_fix is absent")
		}
	})

	t.Run("1 sets the filter", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		rec, _ := doInvoiceList(t, list, &id, "?needs_fix=1")
		if !called {
			t.Fatalf("store.List was not called (status=%d, body=%s)", rec.Code, rec.Body.String())
		}
		if !captured.NeedsFix {
			t.Errorf("captured ListFilter.NeedsFix = false, want true for ?needs_fix=1")
		}
	})

	t.Run("unparseable value 400s, store not called", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when needs_fix is not a bool")
			return nil, 0, nil
		}
		rec, resp := doInvoiceList(t, list, &id, "?needs_fix=maybe")

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Error("expected a non-empty error message in the body")
		}
	})
}

// TestListHandler_RuleKeyAndQLengthCap (QA Mode B, AC-6, task-282): the
// implementation plan's own §1 param-contract table requires rule_key/q to
// 400 with "rule_key is too long" / "q is too long" above a 200-char cap --
// Stage 2.5 (RED) flagged that no test in the 11-row spec table pinned it,
// and Stage 3 (execution) implemented the cap without adding one. Closes
// that gap. Covers: the boundary (exactly 200 bytes accepted, 201 bytes
// 400s), that the cap is a BYTE length and not a rune count (200 multi-byte
// CJK runes is 600 bytes and must still 400 even though it is "200
// characters" by a rune count), and that malformed input is REJECTED rather
// than silently truncated -- every 400 sub-case here uses a store closure
// that calls t.Fatal if invoked, so there is no code path where a truncated
// value could reach Store.List.
func TestListHandler_RuleKeyAndQLengthCap(t *testing.T) {
	t.Run("rule_key exactly 200 bytes is accepted", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		key := strings.Repeat("a", 200)
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		v := url.Values{}
		v.Set("rule_key", key)
		rec, _ := doInvoiceList(t, list, &id, "?"+v.Encode())
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 for a 200-byte rule_key (body=%s)", rec.Code, rec.Body.String())
		}
		if !called {
			t.Fatal("store.List was not called for a 200-byte rule_key")
		}
		if captured.RuleKey != key {
			t.Errorf("captured ListFilter.RuleKey len = %d, want 200 (value must pass through unmutated, not truncated)", len(captured.RuleKey))
		}
	})

	t.Run("rule_key 201 bytes 400s, store not called", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		key := strings.Repeat("a", 201)
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when rule_key exceeds the 200-byte cap")
			return nil, 0, nil
		}
		v := url.Values{}
		v.Set("rule_key", key)
		rec, resp := doInvoiceList(t, list, &id, "?"+v.Encode())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a 201-byte rule_key (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error != "rule_key is too long" {
			t.Errorf("error = %q, want \"rule_key is too long\"", resp.Error)
		}
	})

	t.Run("q exactly 200 bytes is accepted", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		q := strings.Repeat("b", 200)
		var captured ListFilter
		called := false
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			called = true
			captured = f
			return []Invoice{}, 0, nil
		}
		v := url.Values{}
		v.Set("q", q)
		rec, _ := doInvoiceList(t, list, &id, "?"+v.Encode())
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 for a 200-byte q (body=%s)", rec.Code, rec.Body.String())
		}
		if !called {
			t.Fatal("store.List was not called for a 200-byte q")
		}
		if captured.Query != q {
			t.Errorf("captured ListFilter.Query len = %d, want 200 (value must pass through unmutated, not truncated)", len(captured.Query))
		}
	})

	t.Run("q 201 bytes 400s, store not called", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		q := strings.Repeat("b", 201)
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when q exceeds the 200-byte cap")
			return nil, 0, nil
		}
		v := url.Values{}
		v.Set("q", q)
		rec, resp := doInvoiceList(t, list, &id, "?"+v.Encode())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a 201-byte q (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error != "q is too long" {
			t.Errorf("error = %q, want \"q is too long\"", resp.Error)
		}
	})

	// The cap is a BYTE length, not a rune count: 200 CJK runes is 200
	// characters by any human count but 600 UTF-8 bytes (3 bytes/rune for
	// U+6D4B), so it must still 400 -- a rune-counting implementation would
	// wrongly accept this.
	t.Run("rule_key 200 multi-byte runes (600 bytes) 400s -- byte cap, not rune cap", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		key := strings.Repeat("测", 200)
		if n := len([]rune(key)); n != 200 {
			t.Fatalf("test fixture bug: fixture has %d runes, want 200", n)
		}
		if n := len(key); n != 600 {
			t.Fatalf("test fixture bug: fixture has %d bytes, want 600 (3 bytes/rune for U+6D4B)", n)
		}
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when rule_key exceeds the 200-BYTE cap, even at exactly 200 runes")
			return nil, 0, nil
		}
		v := url.Values{}
		v.Set("rule_key", key)
		rec, resp := doInvoiceList(t, list, &id, "?"+v.Encode())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a 200-rune/600-byte rule_key (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error != "rule_key is too long" {
			t.Errorf("error = %q, want \"rule_key is too long\"", resp.Error)
		}
	})

	// Same byte-vs-rune proof for q, confirming the cap applies uniformly to
	// both free-text params rather than only rule_key.
	t.Run("q 200 multi-byte runes (600 bytes) 400s -- byte cap, not rune cap", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		q := strings.Repeat("测", 200)
		list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
			t.Fatal("store.List must not run when q exceeds the 200-BYTE cap, even at exactly 200 runes")
			return nil, 0, nil
		}
		v := url.Values{}
		v.Set("q", q)
		rec, resp := doInvoiceList(t, list, &id, "?"+v.Encode())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a 200-rune/600-byte q (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error != "q is too long" {
			t.Errorf("error = %q, want \"q is too long\"", resp.Error)
		}
	})
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
	validate := func(ctx context.Context, id string) (Invoice, int, error) {
		t.Fatal("validate must not run without an identity")
		return Invoice{}, 0, nil
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
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		if gotID != invoiceID {
			t.Fatalf("validate called with id=%q, want %q", gotID, invoiceID)
		}
		return want, 0, nil
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
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return want, 0, nil
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
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return Invoice{}, 0, ErrNotFound
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
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return Invoice{}, 0, ErrNotDraft
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
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return Invoice{}, 0, ErrStaleValidation
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
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return Invoice{}, 0, ErrUpstream
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
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return Invoice{}, 0, ErrNoActiveRuleSet
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
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return Invoice{}, 0, fmt.Errorf("%w: malformed invoice id", ErrValidation)
	}
	rec, resp := doInvoiceValidate(t, validate, &id, "not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// --- Rule-set version on the validate response (task-161/M4-22-02) --------

// TestValidateHandler_ExposesRuleSetVersion (#1): a 200 response must
// carry rule_set_version as the stubbed gate's evaluated version,
// alongside an unchanged rule_set_version_id.
func TestValidateHandler_ExposesRuleSetVersion(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	versionID := uuid.NewString()
	want := Invoice{
		ID:               invoiceID,
		Status:           StatusValidated,
		Violations:       json.RawMessage(`[]`),
		RuleSetVersionID: &versionID,
	}
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return want, 2, nil
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.RuleSetVersion == nil || *resp.RuleSetVersion != 2 {
		t.Errorf("rule_set_version = %v, want 2 (body=%s)", resp.RuleSetVersion, rec.Body.String())
	}
	if resp.RuleSetVersionID == nil || *resp.RuleSetVersionID != versionID {
		t.Errorf("rule_set_version_id = %v, want %q -- unchanged by this story (body=%s)",
			resp.RuleSetVersionID, versionID, rec.Body.String())
	}
}

// TestValidateHandler_ResponseIsAdditive (#2): decoding into the existing
// Invoice type must still succeed with every field matching exactly -- the
// new rule_set_version sibling key must not rename or move anything.
//
// M5-05-02 (task-238), Test-Inversion Register #8: `want` below sets
// RejectionReasons: json.RawMessage(`[]`) -- else the zero-value nil
// marshals a real "null", decodes back into a non-nil RawMessage("null")
// (json.RawMessage's UnmarshalJSON always runs, even on a null literal),
// and reflect.DeepEqual(got, want) at :1361 breaks. See
// TestValidateHandler_ResponseCarriesRejectionReasonsEmptyArray
// (fiscal_outcome_projection_test.go) for the compile-safe RED proof of the
// underlying wire behavior this fixture depends on.
func TestValidateHandler_ResponseIsAdditive(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	versionID := uuid.NewString()
	entityID := uuid.NewString()
	desc := "widget"
	want := Invoice{
		ID:               invoiceID,
		EntityID:         entityID,
		InvoiceNumber:    "INV-RSV-01",
		Status:           StatusValidated,
		Violations:       json.RawMessage(`[]`),
		RuleSetVersionID: &versionID,
		RejectionReasons: json.RawMessage(`[]`),
		LineItems:        []LineItem{{ID: uuid.NewString(), LineNo: 1, Description: &desc}},
	}
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return want, 2, nil
	}
	rec, _ := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var got Invoice
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response into the existing Invoice type: %v (body=%s)", err, rec.Body.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded Invoice = %+v, want %+v -- every pre-existing field must match the stub's invoice "+
			"exactly, unaffected by the new rule_set_version sibling key", got, want)
	}
}

// TestValidateHandler_NilVersionMarshalsNull (#3): gate version 0 ("nothing
// evaluated") must marshal to the literal `"rule_set_version":null` --
// checked on raw bytes, since json.Unmarshal can't distinguish null from an
// absent key.
func TestValidateHandler_NilVersionMarshalsNull(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft, Violations: json.RawMessage(`[]`)}
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return want, 0, nil
	}
	rec, _ := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"rule_set_version":null`) {
		t.Errorf("body = %s, want it to contain the literal \"rule_set_version\":null", body)
	}
	// fmt.Sprintf, not a literal: a literal ":0" here would trip
	// internal/validation's TestRuleSetV2_JSONQuotedVersionPinNotPresent grep guard.
	forbiddenZeroPin := fmt.Sprintf(`"rule_set_version":%d`, 0)
	if strings.Contains(body, forbiddenZeroPin) {
		t.Errorf("body = %s, must NEVER stamp a run with the literal :0 for rule_set_version -- 0 means "+
			"\"nothing evaluated\", never a real version", body)
	}
}

// TestValidateHandler_ViolationsStillCarryVersion (#4, AC #4): a blocking
// violation still 200s with violations AND a populated rule_set_version --
// [error semantics] is not weakened by this story.
func TestValidateHandler_ViolationsStillCarryVersion(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	violations := json.RawMessage(`[{"rule_key":"vat-standard-rate","severity":"error","message":"VAT must equal 7.5% of the subtotal."}]`)
	want := Invoice{ID: invoiceID, Status: StatusDraft, Violations: violations}
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return want, 2, nil
	}
	rec, resp := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a blocking violation is normal success-payload data, never an HTTP "+
			"error [error semantics] (body=%s)", rec.Code, rec.Body.String())
	}
	if len(resp.Violations) == 0 || string(resp.Violations) == "[]" || string(resp.Violations) == "null" {
		t.Errorf("violations = %s, want a non-empty violation set carried in the body", resp.Violations)
	}
	if resp.RuleSetVersion == nil || *resp.RuleSetVersion != 2 {
		t.Errorf("rule_set_version = %v, want 2 -- a blocked verdict is still a real evaluated verdict against a "+
			"real rule-set version (AC #4) (body=%s)", resp.RuleSetVersion, rec.Body.String())
	}
}

// TestValidateHandler_UpstreamErrorsUnchanged (#5, AC #5): 502/503 error
// paths behave identically to before and must never carry a
// rule_set_version key -- no verdict was reached.
func TestValidateHandler_UpstreamErrorsUnchanged(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	upstream := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return Invoice{}, 0, ErrUpstream
	}
	rec, resp := doInvoiceValidate(t, upstream, &id, uuid.NewString())
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
	if strings.Contains(rec.Body.String(), "rule_set_version") {
		t.Errorf("body = %s, an outage response must carry NO rule_set_version key at all -- no verdict was "+
			"reached", rec.Body.String())
	}

	noActive := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return Invoice{}, 0, ErrNoActiveRuleSet
	}
	rec2, resp2 := doInvoiceValidate(t, noActive, &id, uuid.NewString())
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", rec2.Code, rec2.Body.String())
	}
	if resp2.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
	if strings.Contains(rec2.Body.String(), "rule_set_version") {
		t.Errorf("body = %s, an outage response must carry NO rule_set_version key at all -- no verdict was "+
			"reached", rec2.Body.String())
	}
}

// --- QA Mode B adversarial coverage: raw JSON keys, GET/List pollution ----

// TestValidateHandler_TopLevelKeysNotNested: validateResponse embeds
// Invoice, which encoding/json flattens onto one top-level object -- checks
// raw keys directly: no "invoice"/"result"/"data" wrapper, and the key set
// is exactly the known Invoice fields plus one sibling.
func TestValidateHandler_TopLevelKeysNotNested(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	versionID := uuid.NewString()
	entityID := uuid.NewString()
	desc := "widget"
	want := Invoice{
		ID:               invoiceID,
		EntityID:         entityID,
		InvoiceNumber:    "INV-RSV-KEYS",
		Status:           StatusValidated,
		Violations:       json.RawMessage(`[]`),
		RuleSetVersionID: &versionID,
		LineItems:        []LineItem{{ID: uuid.NewString(), LineNo: 1, Description: &desc}},
	}
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return want, 2, nil
	}
	rec, _ := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response into a raw top-level key map: %v (body=%s)", err, rec.Body.String())
	}

	wantKeys := []string{
		"id", "entity_id", "import_batch_id", "invoice_number", "status", "issue_date",
		"supplier_tin", "supplier_name", "buyer_tin", "buyer_name", "currency", "subtotal",
		"vat", "total", "violations", "rule_set_version_id", "created_at", "line_items",
		"rule_set_version",
		// M5-05-02 (task-238), Test-Inversion Register #9: +4 -- irn/csid/
		// qr_payload/rejection_reasons join Invoice as direct top-level
		// siblings once Stage 3 lands (AC#6), same flattened-embed shape as
		// every other Invoice field here. FAILS today (19 raw keys vs. this
		// slice's 23) since none of the four exist on Invoice yet.
		"irn", "csid", "qr_payload", "rejection_reasons",
	}
	for _, k := range wantKeys {
		if _, ok := raw[k]; !ok {
			t.Errorf("raw JSON keys missing %q (body=%s) -- every Invoice field must stay a direct top-level "+
				"sibling of rule_set_version; embedding must flatten, not nest", k, rec.Body.String())
		}
	}
	if len(raw) != len(wantKeys) {
		t.Errorf("raw JSON has %d top-level keys, want exactly %d %v (body=%s) -- an extra key (e.g. a wrapper "+
			"like \"invoice\") would mean the embedding nested Invoice instead of flattening it",
			len(raw), len(wantKeys), wantKeys, rec.Body.String())
	}
	for _, wrapper := range []string{"invoice", "result", "data"} {
		if _, ok := raw[wrapper]; ok {
			t.Errorf("raw JSON has a %q wrapper key (body=%s) -- Invoice must be embedded flat, never nested "+
				"under a sub-object", wrapper, rec.Body.String())
		}
	}
}

// TestGetHandler_CarriesRuleSetVersionKey (M4-09-01, task-182): inverted
// from the former TestGetHandler_NoRuleSetVersionKey -- that guard asserted
// the literal INVERSE of this story's Core AC #1. GET must now CARRY
// "rule_set_version": (a getResponse sibling mirroring validateResponse,
// [read-shape-getresponse-wrapper]); rule_set_version_id stays present,
// unaffected. Checked on raw bytes for the exact key `"rule_set_version":`
// so it can't false-match rule_set_version_id.
func TestGetHandler_CarriesRuleSetVersionKey(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	versionID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusValidated, RuleSetVersionID: &versionID}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"rule_set_version_id":`) {
		t.Errorf("body = %s, want rule_set_version_id to stay present (unaffected by this story)", body)
	}
	if !strings.Contains(body, `"rule_set_version":`) {
		t.Errorf("body = %s, GET must now CARRY a rule_set_version key -- M4-09-01's read-shape addition mirrors "+
			"the validate response wrapper ([read-shape-getresponse-wrapper]), Core AC #1", body)
	}
}

// TestGetHandler_RuleSetVersionMarshalsNull (M4-09-01, task-182, Core AC #2):
// mirrors TestValidateHandler_NilVersionMarshalsNull's explicit-null check
// on the GET path -- a never-validated invoice (the stub's transient
// RuleSetVersion left nil) must render "rule_set_version":null, present and
// explicit, never omitted and never a false 0.
func TestGetHandler_RuleSetVersionMarshalsNull(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft, RuleSetVersion: nil}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"rule_set_version":null`) {
		t.Errorf("body = %s, want it to contain the literal \"rule_set_version\":null (explicit null, not "+
			"omitted, not a false 0) -- GetHandler must wrap the result in a getResponse sibling like "+
			"validateResponse", body)
	}
}

// TestListHandler_NoRuleSetVersionKey: List must stay clean of
// rule_set_version, unlike GET (TestGetHandler_CarriesRuleSetVersionKey,
// M4-09-01) -- the domain Invoice's new RuleSetVersion field is json:"-",
// so List (which marshals the domain type directly, no wrapper) never gains
// the key. Unaffected by M4-09-01; kept GREEN, unchanged.
func TestListHandler_NoRuleSetVersionKey(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invID := uuid.NewString()
	versionID := uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: invID, Status: StatusDraft, RuleSetVersionID: &versionID}}, 1, nil
	}
	rec, _ := doInvoiceList(t, list, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"rule_set_version_id":`) {
		t.Errorf("body = %s, want rule_set_version_id to stay present on each list item (unaffected by this "+
			"story)", body)
	}
	if strings.Contains(body, `"rule_set_version":`) {
		t.Errorf("body = %s, List must NOT gain a rule_set_version key on any item -- that field belongs only "+
			"to the validate response wrapper, never the domain Invoice struct shared by Get/List", body)
	}
}

// --- QR PNG tests (M5-09-01, task-250) --------------------------------------
//
// RED against the current state: getResponse/invoiceBody already carry
// qr_png_base64 (Stage 2, QA Mode A widening -- mirrors the rule_set_version
// precedent exactly), but GetHandler does NOT yet populate it -- that wiring
// (qrcode.RenderBase64(*inv.QRPayload) when QRPayload != nil) is Stage 3
// (executor) work. AC-4 (marshals null when absent) and AC-6 (List stays
// clean) are expected to already be green at this stage, same as this
// story's own precedent TestListHandler_NoRuleSetVersionKey above -- both are
// regression guards on behavior the additive-only widening already delivers,
// not signals of missing executor work. AC-3 (populated when present) and
// the log half of AC-5 are the two specs that are genuinely RED right now.

// TestGetHandler_QRPNGBase64PopulatedWhenPayloadPresent (AC-3, Core AC #3):
// an accepted invoice with a non-nil QRPayload must decode-to a valid PNG
// under qr_png_base64. RED today -- GetHandler never calls
// qrcode.RenderBase64, so QRPNGBase64 stays nil.
func TestGetHandler_QRPNGBase64PopulatedWhenPayloadPresent(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	payload := "irn-0001-2026.csid-abc123.mbs-qr-payload-sample-fixture"
	want := Invoice{ID: invoiceID, Status: StatusAccepted, QRPayload: &payload}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, resp := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.QRPNGBase64 == nil {
		t.Fatalf("qr_png_base64 = null (body=%s), want a populated base64 PNG -- GetHandler must call "+
			"qrcode.RenderBase64(*inv.QRPayload) when QRPayload is non-nil", rec.Body.String())
	}
	raw, err := base64.StdEncoding.DecodeString(*resp.QRPNGBase64)
	if err != nil {
		t.Fatalf("qr_png_base64 = %q does not base64-decode (StdEncoding): %v", *resp.QRPNGBase64, err)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("qr_png_base64 decodes to bytes that are not a valid PNG: %v", err)
	}
}

// TestGetHandler_QRPNGBase64MarshalsNull (AC-4): a never-submitted invoice
// (QRPayload == nil) must render "qr_png_base64":null -- present and
// explicit, never omitted, never an empty string. Mirrors
// TestGetHandler_RuleSetVersionMarshalsNull's raw-byte technique exactly.
// Already GREEN at this stage (see the section doc comment above) -- kept as
// a permanent regression guard, not evidence of Stage 3 work being done.
func TestGetHandler_QRPNGBase64MarshalsNull(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft, QRPayload: nil}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"qr_png_base64":null`) {
		t.Errorf("body = %s, want the literal \"qr_png_base64\":null (explicit null, not omitted, not an "+
			"empty string) when the invoice has no qr_payload", body)
	}
}

// TestGetHandler_UnrenderableQRPayloadStillReturns200 (AC-5): a QRPayload
// past QR capacity must still yield HTTP 200 with qr_png_base64: null --
// never a non-200 response (a corrupt/oversized payload must not make the
// invoice unviewable). Uses overlongBytePayload's exact fixture shape
// (4000 lowercase byte-mode chars, NOT all-digits) -- see
// internal/platform/qrcode/qrcode_test.go's doc comment and Stage 1
// validation addenda #1 for why an all-digit 4000-char payload would fit
// comfortably inside QR's numeric-mode capacity and NOT reproduce this case.
// The 200+null half is already GREEN at this stage (see the section doc
// comment above); it becomes a real regression guard once GetHandler wires
// the render call. The "and a logged error" half of AC-5 is NOT asserted
// here -- see TestGetHandler_UnrenderableQRPayloadIsLogged below, which
// bypasses doInvoiceGet (it always passes a nil logger, falling back to
// slog.Default(), unobservable through that helper).
func TestGetHandler_UnrenderableQRPayloadStillReturns200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	overlong := strings.Repeat("a", 4000)
	want := Invoice{ID: invoiceID, Status: StatusAccepted, QRPayload: &overlong}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, resp := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when qr_payload is unrenderable (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.QRPNGBase64 != nil {
		t.Errorf("qr_png_base64 = %q, want null when the payload cannot be rendered as a QR code", *resp.QRPNGBase64)
	}
}

// TestGetHandler_UnrenderableQRPayloadIsLogged (AC-5's logging clause,
// Stage 1 validation addenda #4 -- optional 10th spec): constructs
// GetHandler directly with a buffer-backed logger (the repo's buffer-logger
// idiom, internal/platform/log_test.go:13) rather than going through
// doInvoiceGet, which always passes a nil logger (falling back to
// slog.Default(), where a real emission would be unobservable here). RED
// today -- nothing calls qrcode.RenderBase64 yet, so nothing is logged.
func TestGetHandler_UnrenderableQRPayloadIsLogged(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	overlong := strings.Repeat("a", 4000)
	want := Invoice{ID: invoiceID, Status: StatusAccepted, QRPayload: &overlong}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	r = r.WithContext(auth.WithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	GetHandler(get, logger).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if buf.Len() == 0 {
		t.Error("expected the render failure to be logged via the injected *slog.Logger, but the log buffer is empty")
	}
}

// TestListHandler_HasNoQRKey (AC-6): List must stay byte-shape-identical to
// before this subtask -- no qr_png_base64 key on any item. Clones
// TestListHandler_NoRuleSetVersionKey's approach exactly (same hazard: every
// list item already carries "qr_payload":null, so a substring check on "qr"
// or "qr_p" would pass vacuously -- this asserts the exact literal
// qr_png_base64). Already GREEN at this stage, same as its precedent; kept
// as a permanent regression guard.
func TestListHandler_HasNoQRKey(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invID := uuid.NewString()
	payload := "irn-0001-2026.csid-abc123.mbs-qr-payload-sample-fixture"
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: invID, Status: StatusAccepted, QRPayload: &payload}}, 1, nil
	}
	rec, _ := doInvoiceList(t, list, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"qr_payload":`) {
		t.Errorf("body = %s, want qr_payload to stay present on each list item (unaffected by this story)", body)
	}
	if strings.Contains(body, `"qr_png_base64"`) {
		t.Errorf("body = %s, List must NOT gain a qr_png_base64 key on any item -- that field belongs only "+
			"to the GET response wrapper, never the domain Invoice struct shared by Get/List", body)
	}
}

// TestValidateHandler_ErrorBodiesCarryNoRuleSetVersionKey (AC #5): raw-byte
// check that the remaining error paths (401/404/409x2/400) carry no
// rule_set_version key either.
func TestValidateHandler_ErrorBodiesCarryNoRuleSetVersionKey(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()

	cases := []struct {
		name       string
		identity   *auth.Identity
		validate   func(ctx context.Context, gotID string) (Invoice, int, error)
		wantStatus int
	}{
		{"401-no-identity", nil, func(ctx context.Context, gotID string) (Invoice, int, error) {
			t.Fatal("validate must not run without an identity")
			return Invoice{}, 0, nil
		}, http.StatusUnauthorized},
		{"404-not-found", &id, func(ctx context.Context, gotID string) (Invoice, int, error) {
			return Invoice{}, 0, ErrNotFound
		}, http.StatusNotFound},
		{"409-not-draft", &id, func(ctx context.Context, gotID string) (Invoice, int, error) {
			return Invoice{}, 0, ErrNotDraft
		}, http.StatusConflict},
		{"409-stale-validation", &id, func(ctx context.Context, gotID string) (Invoice, int, error) {
			return Invoice{}, 0, ErrStaleValidation
		}, http.StatusConflict},
		{"400-malformed-id", &id, func(ctx context.Context, gotID string) (Invoice, int, error) {
			return Invoice{}, 0, fmt.Errorf("%w: malformed invoice id", ErrValidation)
		}, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, resp := doInvoiceValidate(t, tc.validate, tc.identity, invoiceID)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if resp.Error == "" {
				t.Error("expected a non-empty error message in the body")
			}
			if strings.Contains(rec.Body.String(), "rule_set_version") {
				t.Errorf("body = %s, an error response must carry NO rule_set_version key at all -- no verdict "+
					"was reached", rec.Body.String())
			}
		})
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
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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
// ErrNotFixable must map to 409 -- the edit surface accepts three fixable
// statuses (draft, validated, rejected -- M5-05-01 (task-237)), and this is the
// HTTP-layer proof of that guard's error mapping. The test body itself is
// unaffected by the widening (it stubs the store), so this is a
// comment-only inversion.
func TestEditHandler_NotFixable409(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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

	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
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

// --- GET /v1/invoices/{id}/history (HistoryHandler, task-160/M4-22-01) ----
//
// A malformed id maps to 400 (ErrValidation), not 404 -- matching
// Get/Update/Transition. Store.History's own 22P02 mapping is pinned
// separately by malformed_id_test.go's "History" subtest.
//
// DB-backed ordering/cross-tenant/unset-GUC coverage lives in
// cross_tenant_integration_test.go as TestRLS_InvoiceHistory_*.

// historyChangeWire mirrors the GET /v1/invoices/{id}/history response
// element shape (task-160) -- json tags identical to the (future)
// production StatusChange type.
type historyChangeWire struct {
	FromStatus *string   `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Actor      string    `json:"actor"`
	ChangedAt  time.Time `json:"changed_at"`
}

// doInvoiceHistory drives GET /v1/invoices/{id}/history. Returns the raw
// recorder, not a decoded body: success is a bare JSON array, error is the
// {"error":...} object -- two shapes that can't share one decode target.
func doInvoiceHistory(t *testing.T, history func(ctx context.Context, id string) ([]StatusChange, error), id *auth.Identity, invoiceID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID+"/history", nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	HistoryHandler(history, nil).ServeHTTP(rec, r)
	return rec
}

// decodeInvoiceErrorBody decodes rec's body as the shared {"error":"..."}
// envelope -- History's success shape is a bare array, not an object, so
// it can't reuse doInvoiceGet's decoder.
func decodeInvoiceErrorBody(t *testing.T, rec *httptest.ResponseRecorder) invoiceBody {
	t.Helper()
	var resp invoiceBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response %q: %v", rec.Body.String(), err)
	}
	return resp
}

// TestHistoryHandler_Unauthenticated401 (#1): no identity in the request
// context must 401 before history ever runs -- same identity-first-401
// order as every other handler in this file.
func TestHistoryHandler_Unauthenticated401(t *testing.T) {
	invoiceID := uuid.NewString()
	history := func(ctx context.Context, id string) ([]StatusChange, error) {
		t.Fatal("history must not run without an identity")
		return nil, nil
	}
	rec := doInvoiceHistory(t, history, nil, invoiceID)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp := decodeInvoiceErrorBody(t, rec); resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestHistoryHandler_GenesisOnly (#2): a store returning exactly one
// genesis StatusChange (from_status null, to_status "draft") must produce
// 200 with a 1-element JSON array; from_status renders JSON null, to_status
// "draft" -- AND history must be called with the exact path id.
func TestHistoryHandler_GenesisOnly(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	history := func(ctx context.Context, gotID string) ([]StatusChange, error) {
		if gotID != invoiceID {
			t.Fatalf("history called with id = %q, want %q", gotID, invoiceID)
		}
		return []StatusChange{{FromStatus: nil, ToStatus: StatusDraft, Actor: "user-1", ChangedAt: time.Now()}}, nil
	}
	rec := doInvoiceHistory(t, history, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp []historyChangeWire
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if len(resp) != 1 {
		t.Fatalf("body array length = %d, want 1", len(resp))
	}
	if resp[0].FromStatus != nil {
		t.Errorf("resp[0].from_status = %q, want JSON null", *resp[0].FromStatus)
	}
	if resp[0].ToStatus != string(StatusDraft) {
		t.Errorf("resp[0].to_status = %q, want %q", resp[0].ToStatus, StatusDraft)
	}
}

// TestHistoryHandler_OmitsInternalColumns (#3, AC #7): the RAW response
// body for a populated change must contain no tenant_id, invoice_id, or
// (row) id key -- StatusChange deliberately surfaces only
// from_status/to_status/actor/changed_at.
func TestHistoryHandler_OmitsInternalColumns(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	from := StatusDraft
	history := func(ctx context.Context, gotID string) ([]StatusChange, error) {
		return []StatusChange{{FromStatus: &from, ToStatus: StatusValidated, Actor: "user-1", ChangedAt: time.Now()}}, nil
	}
	rec := doInvoiceHistory(t, history, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	for _, forbidden := range []string{`"id":`, `"tenant_id":`, `"invoice_id":`} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("body = %s, must NOT contain %s (AC #7)", raw, forbidden)
		}
	}
}

// TestHistoryHandler_NotFoundMapsTo404 (#4): the store returning ErrNotFound
// must map to 404 with a non-empty error message -- the shape both a
// genuinely unknown id and a cross-tenant one resolve to (AC #5).
func TestHistoryHandler_NotFoundMapsTo404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	history := func(ctx context.Context, gotID string) ([]StatusChange, error) {
		return nil, ErrNotFound
	}
	rec := doInvoiceHistory(t, history, &id, uuid.NewString())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp := decodeInvoiceErrorBody(t, rec); resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestHistoryHandler_MalformedIDIs400 (#5): ErrValidation maps to 400, not
// 404 -- matching Get/Update/Transition.
func TestHistoryHandler_MalformedIDIs400(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	history := func(ctx context.Context, gotID string) ([]StatusChange, error) {
		return nil, fmt.Errorf("%w: malformed id", ErrValidation)
	}
	rec := doInvoiceHistory(t, history, &id, "not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp := decodeInvoiceErrorBody(t, rec); resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
}

// TestHistoryHandler_GenesisOnly_RawWireShape: raw bytes, not a decoded
// struct -- checks the top-level shape is a JSON array and from_status
// carries literal null, not "" or an omitted key (an accidental omitempty
// on StatusChange.FromStatus would decode identically to #2 above).
func TestHistoryHandler_GenesisOnly_RawWireShape(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	history := func(ctx context.Context, gotID string) ([]StatusChange, error) {
		return []StatusChange{{FromStatus: nil, ToStatus: StatusDraft, Actor: "user-1", ChangedAt: time.Now()}}, nil
	}
	rec := doInvoiceHistory(t, history, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := bytes.TrimSpace(rec.Body.Bytes())
	if len(raw) == 0 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		t.Fatalf("body = %s, want the top-level JSON to be an array (starts with '[', ends with ']')", raw)
	}
	if !bytes.Contains(raw, []byte(`"from_status":null`)) {
		t.Errorf("body = %s, want raw JSON to contain the literal \"from_status\":null (not omitted, not empty string)", raw)
	}
	if bytes.Contains(raw, []byte(`"from_status":""`)) {
		t.Errorf("body = %s, from_status must never serialize as an empty string", raw)
	}
}

// ---------------------------------------------------------------------------
// INVED-01-05 (task-266) -- RED specs (Mode A) for two additive wire changes:
// (1) editReq gains line_items (a *[]lineItemReq, absent/null/empty/populated
// all distinguishable), (2) getResponse gains three action-flag siblings
// (can_edit, can_revalidate, revalidate_blocked_reason). Neither change
// exists yet in handlers.go: today editReq has no line_items field at all
// (any request body whose ONLY content is a line_items key decodes to an
// all-zero editReq, so EditInput.LineItems stays nil, exactly the same as
// before this field existed), and getResponse carries none of the three new
// keys. Every assertion below therefore fails on an actual value (a missing
// JSON key, a wrong status code, a nil pointer where a populated one is
// wanted) -- never a compile error. A handful of specs describe behavior
// that is ALREADY correct today (raw-byte/store-still-called guards) and so
// PASS at RED; each is called out inline rather than weakened to force a
// failure.
//
// Spec-to-test map (Corrected Test Specs table, task-266):
//
//	T1   TestEditHandler_LineItemsAllFieldsMapOneToOne
//	T2   TestEditHandler_LineItemsAbsentLeavesLinesUntouched          (guard, already green)
//	T2b  TestEditHandler_LineItemsNullLeavesLinesUntouched            (guard, already green)
//	T3   TestEditHandler_EmptyLineItemsArrayIsNonNilZeroLen
//	T3b  TestEditHandler_EmptyLineItemsOnlyStoreStillCalled           (guard, already green)
//	T6   TestEditHandler_ClientLineNoIgnored
//	T11  TestEditHandler_LineItemsNoIdentity401                       (guard, already green)
//	T12  TestEditHandler_LineItemsMalformedShape400
//	T13  TestEditHandler_LineItemsMalformedNumericIs400NotFrom500     (guard, already green)
//	T15  TestEditHandler_ResponseCarriesLineItemsOnlyWhenHydrated     (guard, already green)
//	T7   TestGetHandler_ActionFlagsAllStatuses
//	T7b  TestGetHandler_ActionFlagsFalseNotOmitted
//	T8   TestGetHandler_RevalidateBlockedReasonNullOnDraft
//	T9   TestGetHandler_RevalidateBlockedReasonSameForValidatedAndRejected
//	T9b  TestGetHandler_RevalidateBlockedReasonExactCopy
//	T10  TestGetHandler_ActionFlagsAdditiveKeepAllExistingKeys
//	T14  TestListHandler_NoActionFlagKeys                             (guard, already green)
//	T-E2E-GET   TestGetHandler_RealStore_DraftActionFlags
//	T-E2E-PATCH TestEditHandler_RealStore_LineItemsThreeStates
// ---------------------------------------------------------------------------

// exactRevalidateBlockedReason is the test-local copy of the exact string
// GetHandler must emit under revalidate_blocked_reason. Hardcoded here
// (NEVER imported from production -- the const does not exist until GREEN)
// so this pins the copy non-tautologically. Em dash (U+2014), single
// spaces, matching task-266's Implementation Plan byte-for-byte.
const exactRevalidateBlockedReason = "Only draft invoices can be re-validated — edit this invoice to return it to draft."

// TestEditHandler_LineItemsAllFieldsMapOneToOne (T1): a raw-JSON PATCH body
// with 2 line objects, ALL FIVE fields set on each, must produce an
// EditInput whose LineItems is non-nil, len 2, with every one of the 10
// values mapped 1:1 -- the line-item mirror of
// TestEditHandler_AllFieldsMapOneToOne. Raw string literal, not a marshalled
// Go struct (a []lineItemWire field would round-trip fine here since every
// field is populated, but the raw form is used throughout this section for
// consistency with the three-state specs where it is load-bearing). RED
// today: EditHandler never decodes line_items, so gotIn.LineItems stays nil.
func TestEditHandler_LineItemsAllFieldsMapOneToOne(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	body := `{"line_items":[` +
		`{"description":"Widget A","quantity":"2","unit_price":"10.00","line_total":"20.00","line_tax":"1.50"},` +
		`{"description":"Widget B","quantity":"3","unit_price":"5.00","line_total":"15.00","line_tax":"1.00"}` +
		`]}`
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.LineItems == nil {
		t.Fatalf("EditInput.LineItems = nil, want a non-nil pointer to 2 mapped line items -- EditHandler must decode editReq.LineItems and map each entry into a LineItemInput")
	}
	lines := *gotIn.LineItems
	if len(lines) != 2 {
		t.Fatalf("len(*EditInput.LineItems) = %d, want 2", len(lines))
	}
	want0 := LineItemInput{Description: strPtr("Widget A"), Quantity: strPtr("2"), UnitPrice: strPtr("10.00"), LineTotal: strPtr("20.00"), LineTax: strPtr("1.50")}
	want1 := LineItemInput{Description: strPtr("Widget B"), Quantity: strPtr("3"), UnitPrice: strPtr("5.00"), LineTotal: strPtr("15.00"), LineTax: strPtr("1.00")}
	for i, want := range []LineItemInput{want0, want1} {
		got := lines[i]
		if got.Description == nil || *got.Description != *want.Description {
			t.Errorf("line[%d].Description = %v, want %q", i, got.Description, *want.Description)
		}
		if got.Quantity == nil || *got.Quantity != *want.Quantity {
			t.Errorf("line[%d].Quantity = %v, want %q", i, got.Quantity, *want.Quantity)
		}
		if got.UnitPrice == nil || *got.UnitPrice != *want.UnitPrice {
			t.Errorf("line[%d].UnitPrice = %v, want %q", i, got.UnitPrice, *want.UnitPrice)
		}
		if got.LineTotal == nil || *got.LineTotal != *want.LineTotal {
			t.Errorf("line[%d].LineTotal = %v, want %q", i, got.LineTotal, *want.LineTotal)
		}
		if got.LineTax == nil || *got.LineTax != *want.LineTax {
			t.Errorf("line[%d].LineTax = %v, want %q", i, got.LineTax, *want.LineTax)
		}
	}
}

// TestEditHandler_LineItemsAbsentLeavesLinesUntouched (T2): a body with only
// a header field set (no line_items key at all) must leave EditInput.
// LineItems nil, and still map the header field. ALREADY GREEN at RED: with
// no line_items field on editReq today, gotIn.LineItems is unconditionally
// nil -- this guards that absence stays absence once the field is added.
func TestEditHandler_LineItemsAbsentLeavesLinesUntouched(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"supplier_name":"X"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.LineItems != nil {
		t.Errorf("EditInput.LineItems = %v, want nil when the key is absent", gotIn.LineItems)
	}
	if gotIn.SupplierName == nil || *gotIn.SupplierName != "X" {
		t.Errorf("EditInput.SupplierName = %v, want a non-nil pointer to %q", gotIn.SupplierName, "X")
	}
}

// TestEditHandler_LineItemsNullLeavesLinesUntouched (T2b): explicit JSON
// null for line_items must decode identically to the key being absent
// ([line-items-optional]) -- the SPA can legitimately emit null. ALREADY
// GREEN at RED for the same reason as T2 above.
func TestEditHandler_LineItemsNullLeavesLinesUntouched(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"supplier_name":"X","line_items":null}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.LineItems != nil {
		t.Errorf("EditInput.LineItems = %v, want nil -- an explicit JSON null must decode the same as an absent key", gotIn.LineItems)
	}
	if gotIn.SupplierName == nil || *gotIn.SupplierName != "X" {
		t.Errorf("EditInput.SupplierName = %v, want a non-nil pointer to %q", gotIn.SupplierName, "X")
	}
}

// TestEditHandler_EmptyLineItemsArrayIsNonNilZeroLen (T3): a raw `[]` for
// line_items must decode to a NON-NIL pointer to a zero-length slice, never
// a nil pointer -- the pointer-vs-content distinction Store.Edit's
// [line-items-optional] guard depends on to tell "remove every line" apart
// from "leave them alone". Raw string literal is load-bearing here: a Go
// struct field tagged `[]lineItemWire json:"line_items,omitempty"` would
// marshal an empty slice to an ABSENT key, silently turning this into a T2
// spec (proven trap, task-266 Implementation Plan). RED today: EditHandler
// never decodes line_items at all, so gotIn.LineItems stays nil, not a
// non-nil pointer to len 0.
func TestEditHandler_EmptyLineItemsArrayIsNonNilZeroLen(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"line_items":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.LineItems == nil {
		t.Fatalf("EditInput.LineItems = nil, want a non-nil pointer to a zero-length slice for a raw `[]` body -- nil and empty must stay distinguishable")
	}
	if len(*gotIn.LineItems) != 0 {
		t.Errorf("len(*EditInput.LineItems) = %d, want 0", len(*gotIn.LineItems))
	}
}

// TestEditHandler_EmptyLineItemsOnlyStoreStillCalled (T3b): a body carrying
// ONLY `"line_items":[]` (no header field at all) must still reach the
// store closure -- EditHandler itself must grow NO all-nil guard of its own
// (that guard belongs to Store.Edit, store.go:753-756, and needs to SEE the
// non-nil-but-empty pointer to ever delete a line). ALREADY GREEN at RED:
// EditHandler has no pre-store all-nil check today, so the stub is always
// invoked regardless of what editReq decodes to.
func TestEditHandler_EmptyLineItemsOnlyStoreStillCalled(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	called := false
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		called = true
		return want, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"line_items":[]}`)

	if !called {
		t.Error("the store closure was not called -- EditHandler must not grow its own all-nil guard; Store.Edit owns that check and must see the non-nil empty pointer")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestEditHandler_ClientLineNoIgnored (T6, [line-no-by-position]): a
// client-supplied line_no must be silently ignored, never persisted and
// never a validation error -- lineItemReq structurally has no LineNo field.
// RED today: gotIn.LineItems stays nil (see T1's rationale), so this fails
// on the nil check before it can even inspect the mapped description.
func TestEditHandler_ClientLineNoIgnored(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"line_items":[{"description":"a","line_no":99}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, NOT 400 -- an unknown line_no key must be silently ignored (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.LineItems == nil {
		t.Fatalf("EditInput.LineItems = nil, want a non-nil pointer to 1 mapped line item")
	}
	lines := *gotIn.LineItems
	if len(lines) != 1 {
		t.Fatalf("len(*EditInput.LineItems) = %d, want 1", len(lines))
	}
	if lines[0].Description == nil || *lines[0].Description != "a" {
		t.Errorf("line[0].Description = %v, want %q", lines[0].Description, "a")
	}
}

// TestEditHandler_LineItemsNoIdentity401 (T11): identity-first-401 applies
// to a line_items-only body exactly as it does to every other PATCH body --
// the store closure must never run. ALREADY GREEN at RED: this is the
// existing, unaffected identity check.
func TestEditHandler_LineItemsNoIdentity401(t *testing.T) {
	invoiceID := uuid.NewString()
	called := false
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		called = true
		return Invoice{}, nil
	}
	rec, resp := doInvoiceEdit(t, edit, nil, invoiceID, `{"line_items":[{"description":"a"}]}`)

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

// TestEditHandler_LineItemsMalformedShape400 (T12): a line_items value that
// is not an array-of-objects (a bare string, an array of numbers) must 400
// "invalid request body" -- a shape error is a decode-time 400, never a
// 500 -- and the store closure must never run. RED today: with no
// line_items field on editReq, BOTH bodies decode trivially (the whole key
// is skipped, whatever its value), so today's actual behavior is 200 with
// the store called -- the exact inverse of what this pins for once the
// field exists.
func TestEditHandler_LineItemsMalformedShape400(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"bare_string", `{"line_items":"nope"}`},
		{"array_of_numbers", `{"line_items":[1,2]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
			invoiceID := uuid.NewString()
			called := false
			edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
				called = true
				return Invoice{}, nil
			}
			rec, resp := doInvoiceEdit(t, edit, &id, invoiceID, tc.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			if called {
				t.Error("edit must not run when line_items has the wrong JSON shape")
			}
			if resp.Error != "invalid request body" {
				t.Errorf("error = %q, want %q", resp.Error, "invalid request body")
			}
		})
	}
}

// TestEditHandler_LineItemsMalformedNumericIs400NotFrom500 (T13): a line
// with a malformed numeric decodes cleanly (every lineItemReq field is
// *string, so no decode error is possible), reaches the store, and a
// wrapped ErrValidation coming back from the store must map to 400, never
// 500 -- statusForErr's ErrValidation case is unchanged by this story.
// ALREADY GREEN at RED: this is the existing, unmodified error-map contract;
// included as a permanent regression guard for the store.go 22P02 -> 400
// chain once line_items is wired.
func TestEditHandler_LineItemsMalformedNumericIs400NotFrom500(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	called := false
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		called = true
		return Invoice{}, fmt.Errorf("%w: invalid line numeric", ErrValidation)
	}
	rec, resp := doInvoiceEdit(t, edit, &id, invoiceID, `{"line_items":[{"unit_price":"abc"}]}`)

	if !called {
		t.Fatal("the store closure was not called")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, NOT 500 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(resp.Error, "invalid line numeric") {
		t.Errorf("error = %q, want it to carry the wrapped ErrValidation message", resp.Error)
	}
}

// TestEditHandler_ResponseCarriesLineItemsOnlyWhenHydrated (T15,
// [edit-response-carries-lines]): EditHandler writes the store's returned
// Invoice directly (writeJSON(w, http.StatusOK, inv)), so this is a
// pre-existing, UNCHANGED-by-this-story contract: a hydrated LineItems
// slice renders "line_items":[...], while a nil one (Invoice.LineItems is
// json:"line_items,omitempty", invoice.go:105) drops the key ENTIRELY --
// never "line_items":[]. Both arms are ALREADY GREEN at RED; documented here
// (not changed) because subtasks 06/07 depend on knowing the zero-lines arm
// omits the key rather than emitting an empty array.
func TestEditHandler_ResponseCarriesLineItemsOnlyWhenHydrated(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	t.Run("hydrated_lines_present", func(t *testing.T) {
		invoiceID := uuid.NewString()
		desc := "widget"
		want := Invoice{ID: invoiceID, Status: StatusDraft, LineItems: []LineItem{
			{ID: uuid.NewString(), LineNo: 1, Description: &desc},
			{ID: uuid.NewString(), LineNo: 2, Description: &desc},
		}}
		edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) { return want, nil }
		rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"vat":"7.50"}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"line_items":[`) {
			t.Errorf("body = %s, want the literal \"line_items\":[ when the store returns hydrated lines", rec.Body.String())
		}
	})

	t.Run("zero_lines_key_omitted_not_empty_array", func(t *testing.T) {
		invoiceID := uuid.NewString()
		want := Invoice{ID: invoiceID, Status: StatusDraft, LineItems: nil}
		edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) { return want, nil }
		rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"line_items":[]}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if strings.Contains(body, `"line_items":`) {
			t.Errorf("body = %s, want NO line_items key at all when the store returns zero lines -- it must be omitted (omitempty), never rendered as \"line_items\":[]", body)
		}
	})
}

// TestGetHandler_ActionFlagsAllStatuses (T7): all three action-flag keys
// must be present with the correct value on EVERY status, per the truth
// table hard-coded here (task-266 Implementation Plan) -- NEVER derived by
// calling canEdit/canRevalidate (that would be tautological, the mistake
// task-265's QA caught). Decodes into map[string]json.RawMessage so
// presence and value are discriminated TOGETHER: decoding into a Go bool
// could not tell "false" from "absent". RED today across every status: none
// of the three keys exist on the wire yet, so every raw[...] lookup is an
// empty json.RawMessage.
func TestGetHandler_ActionFlagsAllStatuses(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	tests := []struct {
		status            Status
		wantCanEdit       string
		wantCanRevalidate string
		wantReasonNull    bool
	}{
		{StatusDraft, "true", "true", true},
		{StatusValidated, "true", "false", false},
		{StatusRejected, "true", "false", false},
		{StatusQueued, "false", "false", true},
		{StatusSubmitted, "false", "false", true},
		{StatusAccepted, "false", "false", true},
		{StatusFailed, "false", "false", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			invoiceID := uuid.NewString()
			want := Invoice{ID: invoiceID, Status: tt.status}
			get := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }
			rec, _ := doInvoiceGet(t, get, &id, invoiceID)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode raw body %q: %v", rec.Body.String(), err)
			}
			if got := string(raw["can_edit"]); got != tt.wantCanEdit {
				t.Errorf("status %q: can_edit raw = %q, want %q (body=%s)", tt.status, got, tt.wantCanEdit, rec.Body.String())
			}
			if got := string(raw["can_revalidate"]); got != tt.wantCanRevalidate {
				t.Errorf("status %q: can_revalidate raw = %q, want %q (body=%s)", tt.status, got, tt.wantCanRevalidate, rec.Body.String())
			}
			reasonRaw := string(raw["revalidate_blocked_reason"])
			if tt.wantReasonNull {
				if reasonRaw != "null" {
					t.Errorf("status %q: revalidate_blocked_reason raw = %q, want null (body=%s)", tt.status, reasonRaw, rec.Body.String())
				}
			} else if reasonRaw == "" || reasonRaw == "null" {
				t.Errorf("status %q: revalidate_blocked_reason raw = %q, want a non-null quoted string (body=%s)", tt.status, reasonRaw, rec.Body.String())
			}
		})
	}
}

// TestGetHandler_ActionFlagsFalseNotOmitted (T7b): on a status where both
// booleans are false (accepted), the raw bytes must contain the literal
// "can_edit":false and "can_revalidate":false -- an omitempty tag on either
// bool would drop the key entirely rather than render false, making "not
// editable" indistinguishable from "an older server that doesn't know this
// key exists" (AC #4's "all three keys on every status"). Mirrors
// TestGetHandler_RuleSetVersionMarshalsNull's raw-byte technique. RED today:
// neither key exists at all.
func TestGetHandler_ActionFlagsFalseNotOmitted(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusAccepted}
	get := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"can_edit":false`) {
		t.Errorf("body = %s, want the literal \"can_edit\":false (not omitted) on a non-editable status", body)
	}
	if !strings.Contains(body, `"can_revalidate":false`) {
		t.Errorf("body = %s, want the literal \"can_revalidate\":false (not omitted) on a non-editable status", body)
	}
}

// TestGetHandler_RevalidateBlockedReasonNullOnDraft (T8): a draft invoice
// (can_edit && can_revalidate both true) must render an explicit
// "revalidate_blocked_reason":null -- the gate is canEdit(s) &&
// !canRevalidate(s), which is false on draft. RED today: the key does not
// exist on the wire at all.
func TestGetHandler_RevalidateBlockedReasonNullOnDraft(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	get := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"revalidate_blocked_reason":null`) {
		t.Errorf("body = %s, want the literal \"revalidate_blocked_reason\":null on a draft invoice", body)
	}
}

// TestGetHandler_RevalidateBlockedReasonSameForValidatedAndRejected (T9):
// validated and rejected are the two statuses where canEdit && !canRevalidate
// -- both must carry the SAME non-empty reason string (a single,
// status-independent copy, [revalidate-reason-from-backend] -- deliberately
// NOT a switch over status, which would reopen Core AC 4). RED today: the
// key does not exist, so both decode to nil.
func TestGetHandler_RevalidateBlockedReasonSameForValidatedAndRejected(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

	reasonFor := func(t *testing.T, status Status) *string {
		t.Helper()
		invoiceID := uuid.NewString()
		want := Invoice{ID: invoiceID, Status: status}
		get := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }
		rec, _ := doInvoiceGet(t, get, &id, invoiceID)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %q: http status = %d, want 200 (body=%s)", status, rec.Code, rec.Body.String())
		}
		var raw struct {
			RevalidateBlockedReason *string `json:"revalidate_blocked_reason"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode raw body %q: %v", rec.Body.String(), err)
		}
		return raw.RevalidateBlockedReason
	}

	validatedReason := reasonFor(t, StatusValidated)
	rejectedReason := reasonFor(t, StatusRejected)

	if validatedReason == nil || *validatedReason == "" {
		t.Fatalf("validated: revalidate_blocked_reason = %v, want a non-empty string", validatedReason)
	}
	if rejectedReason == nil || *rejectedReason == "" {
		t.Fatalf("rejected: revalidate_blocked_reason = %v, want a non-empty string", rejectedReason)
	}
	if *validatedReason != *rejectedReason {
		t.Errorf("validated reason %q != rejected reason %q, want the identical copy on both statuses", *validatedReason, *rejectedReason)
	}
}

// TestGetHandler_RevalidateBlockedReasonExactCopy (T9b): pins the exact
// byte-for-byte copy 07 renders verbatim into the disabled Re-validate
// button. Compared against a test-local literal (exactRevalidateBlockedReason,
// above), NEVER the production const -- that const does not exist until
// GREEN, so referencing it here would be a compile error, not a RED
// assertion failure.
func TestGetHandler_RevalidateBlockedReasonExactCopy(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusValidated}
	get := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var raw struct {
		RevalidateBlockedReason *string `json:"revalidate_blocked_reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw body %q: %v", rec.Body.String(), err)
	}
	if raw.RevalidateBlockedReason == nil {
		t.Fatalf("revalidate_blocked_reason = null, want the exact literal %q", exactRevalidateBlockedReason)
	}
	if *raw.RevalidateBlockedReason != exactRevalidateBlockedReason {
		t.Errorf("revalidate_blocked_reason = %q, want the exact literal %q", *raw.RevalidateBlockedReason, exactRevalidateBlockedReason)
	}
}

// TestGetHandler_ActionFlagsAdditiveKeepAllExistingKeys (T10; STRENGTHENED by
// QA/Mode B, task-266 Part B #1): every pre-existing getResponse key must
// keep its name/position (AC #5), and the three new action-flag keys must be
// purely ADDITIVE -- exactly len(preExisting)+3 top-level keys total,
// modeled on TestValidateHandler_TopLevelKeysNotNested's exact-key-set
// technique.
//
// QA finding: the original single-fixture version of this test (status
// validated only) asserted PRESENCE of the three new keys but never their
// VALUE. validated is a status where can_edit is TRUE (a non-zero bool), so
// a regression that tagged can_edit `,omitempty` left the key present in
// THIS fixture regardless -- a `false`-only bug hides behind a `true`-valued
// sample. Confirmed empirically by mutation testing: adding `,omitempty` to
// can_edit turns TestGetHandler_ActionFlagsAllStatuses and
// TestGetHandler_ActionFlagsFalseNotOmitted red but left the original
// single-case version of THIS test green. Relying on those two separate
// tests to catch that one regression made this test's own "additive keys"
// claim fragile: a future edit could delete either sibling believing this
// test already covers the contract, and the regression would then go
// permanently undetected.
//
// Strengthened two ways, both applied via a 2-case table:
//  1. VALUE assertions on all three new keys, not just presence -- also
//     catches a value-computation bug (e.g. a status switch that always
//     emits can_edit:true) that presence-only checks cannot see.
//  2. A SECOND case at a status where both booleans are FALSE (queued), so
//     this test is self-sufficient against a `,omitempty` regression on
//     EITHER bool, independent of whether TestGetHandler_ActionFlagsAllStatuses/
//     ActionFlagsFalseNotOmitted continue to exist.
func TestGetHandler_ActionFlagsAdditiveKeepAllExistingKeys(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	versionID := uuid.NewString()
	version := 3
	desc := "widget"
	irn := "IRN-T10-001"
	csid := "CSID-T10-001"
	// Known-good short fixture (reused verbatim from
	// TestGetHandler_QRPNGBase64PopulatedWhenPayloadPresent) that renders as
	// a valid PNG without tripping QR capacity.
	payload := "irn-0001-2026.csid-abc123.mbs-qr-payload-sample-fixture"

	preExisting := []string{
		"id", "entity_id", "import_batch_id", "invoice_number", "status", "issue_date",
		"supplier_tin", "supplier_name", "buyer_tin", "buyer_name", "currency", "subtotal",
		"vat", "total", "violations", "rule_set_version_id", "created_at", "line_items",
		"irn", "csid", "qr_payload", "rejection_reasons",
		"rule_set_version", "qr_png_base64",
	}
	newKeys := []string{"can_edit", "can_revalidate", "revalidate_blocked_reason"}

	tests := []struct {
		name              string
		status            Status
		wantCanEdit       string
		wantCanRevalidate string
		wantReasonNull    bool
	}{
		{"validated_true_can_edit", StatusValidated, "true", "false", false},
		{"queued_false_both_flags", StatusQueued, "false", "false", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invoiceID := uuid.NewString()
			want := Invoice{
				ID:               invoiceID,
				EntityID:         uuid.NewString(),
				InvoiceNumber:    "INV-T10",
				Status:           tt.status,
				Violations:       json.RawMessage(`[]`),
				RuleSetVersionID: &versionID,
				RuleSetVersion:   &version,
				LineItems:        []LineItem{{ID: uuid.NewString(), LineNo: 1, Description: &desc}},
				IRN:              &irn,
				CSID:             &csid,
				QRPayload:        &payload,
				RejectionReasons: json.RawMessage(`["reason1"]`),
			}
			get := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }
			rec, _ := doInvoiceGet(t, get, &id, invoiceID)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}

			var raw map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode raw body %q: %v", rec.Body.String(), err)
			}

			for _, k := range preExisting {
				if _, ok := raw[k]; !ok {
					t.Errorf("raw JSON keys missing pre-existing key %q (body=%s) -- the three new action-flag keys must be purely additive", k, rec.Body.String())
				}
			}
			for _, k := range newKeys {
				if _, ok := raw[k]; !ok {
					t.Errorf("raw JSON keys missing new action-flag key %q (body=%s)", k, rec.Body.String())
				}
			}
			wantTotal := len(preExisting) + len(newKeys)
			if len(raw) != wantTotal {
				t.Errorf("raw JSON has %d top-level keys, want exactly %d (%d pre-existing + %d new) (body=%s)",
					len(raw), wantTotal, len(preExisting), len(newKeys), rec.Body.String())
			}

			if got := string(raw["can_edit"]); got != tt.wantCanEdit {
				t.Errorf("%s: can_edit raw = %q, want %q (body=%s)", tt.status, got, tt.wantCanEdit, rec.Body.String())
			}
			if got := string(raw["can_revalidate"]); got != tt.wantCanRevalidate {
				t.Errorf("%s: can_revalidate raw = %q, want %q (body=%s)", tt.status, got, tt.wantCanRevalidate, rec.Body.String())
			}
			reasonRaw := string(raw["revalidate_blocked_reason"])
			if tt.wantReasonNull {
				if reasonRaw != "null" {
					t.Errorf("%s: revalidate_blocked_reason raw = %q, want null (body=%s)", tt.status, reasonRaw, rec.Body.String())
				}
			} else if reasonRaw == "" || reasonRaw == "null" {
				t.Errorf("%s: revalidate_blocked_reason raw = %q, want a non-null quoted string (body=%s)", tt.status, reasonRaw, rec.Body.String())
			}
		})
	}
}

// TestGetHandler_ActionFlagKeysOrderedLast (task-266 Part B #2): a permanent
// gate on wire key ORDER, not just the key SET. The exact-key-set test above
// decodes into a map[string]json.RawMessage, which discards order -- someone
// reordering getResponse's fields (e.g. moving the three action-flag keys
// before rule_set_version/qr_png_base64, or interleaving them among the
// embedded Invoice fields) would violate AC #5's "position" clause with
// every other test in this file still green. Confirmed empirically:
// reordering getResponse's struct fields (can_edit/can_revalidate/
// revalidate_blocked_reason moved BEFORE rule_set_version/qr_png_base64)
// left the full ./internal/invoice/... suite green before this test existed.
//
// Walks the raw response with json.Decoder.Token(), which -- unlike
// json.Unmarshal into a map -- preserves the ORDER keys appear on the wire,
// and asserts the exact ordered key list: every embedded Invoice field in
// its OWN struct declaration order (invoice.go), followed by
// rule_set_version, qr_png_base64, then the three action-flag keys LAST, in
// that order -- exactly what json.NewEncoder produces for an embedded
// anonymous struct field followed by the outer struct's own fields, per
// encoding/json's byIndex field ordering.
func TestGetHandler_ActionFlagKeysOrderedLast(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	versionID := uuid.NewString()
	version := 3
	desc := "widget"
	irn := "IRN-ORDER-001"
	csid := "CSID-ORDER-001"
	payload := "irn-0001-2026.csid-abc123.mbs-qr-payload-sample-fixture"
	want := Invoice{
		ID:               invoiceID,
		EntityID:         uuid.NewString(),
		InvoiceNumber:    "INV-ORDER",
		Status:           StatusValidated,
		Violations:       json.RawMessage(`[]`),
		RuleSetVersionID: &versionID,
		RuleSetVersion:   &version,
		LineItems:        []LineItem{{ID: uuid.NewString(), LineNo: 1, Description: &desc}},
		IRN:              &irn,
		CSID:             &csid,
		QRPayload:        &payload,
		RejectionReasons: json.RawMessage(`["reason1"]`),
	}
	get := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	got := topLevelKeyOrder(t, rec.Body.Bytes())
	want2 := []string{
		// Invoice's own declared field order (invoice.go) -- LineItems is
		// declared LAST on Invoice, after RejectionReasons, NOT immediately
		// after created_at (unlike the presence-only list above, which never
		// asserted order and so never needed to get this right).
		"id", "entity_id", "import_batch_id", "invoice_number", "status", "issue_date",
		"supplier_tin", "supplier_name", "buyer_tin", "buyer_name", "currency", "subtotal",
		"vat", "total", "violations", "rule_set_version_id", "created_at",
		"irn", "csid", "qr_payload", "rejection_reasons", "line_items",
		// getResponse's own fields, in declaration order -- the three
		// action-flag keys MUST be last (AC #5's additive/position clause).
		"rule_set_version", "qr_png_base64",
		"can_edit", "can_revalidate", "revalidate_blocked_reason",
	}
	if !reflect.DeepEqual(got, want2) {
		t.Errorf("top-level key order =\n%v\nwant\n%v\n(body=%s)", got, want2, rec.Body.String())
	}
}

// topLevelKeyOrder walks a JSON object's top-level keys in WIRE ORDER using
// json.Decoder.Token() (interleaved with Decode into a throwaway
// json.RawMessage to consume each value without needing to know its shape)
// -- unlike json.Unmarshal into a map, which discards order entirely.
func topLevelKeyOrder(t *testing.T, body []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("read opening token: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("first token = %v, want object start '{'", tok)
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatalf("read key token: %v", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			t.Fatalf("key token = %v (%T), want a string", keyTok, keyTok)
		}
		keys = append(keys, key)
		var discard json.RawMessage
		if err := dec.Decode(&discard); err != nil {
			t.Fatalf("consume value for key %q: %v", key, err)
		}
	}
	return keys
}

// TestGetHandler_ActionFlagsAreDerivedNotHardcoded (task-266 Part C mutation
// finding): perturbs legalTransitions at runtime -- same technique as
// TestCanEdit_TracksLegalTransitions (transition_test.go) -- and confirms
// GetHandler's can_edit wire value tracks the change. This is the ONLY spec
// in the package that can tell "GetHandler calls canEdit(inv.Status)" apart
// from "GetHandler restates a hardcoded {draft,validated,rejected} switch":
// on TODAY's legalTransitions table the two produce byte-identical output,
// so a hardcoded switch mutation was confirmed (empirically, via mutation
// testing) to leave the ENTIRE existing ./internal/invoice/... suite green,
// including every other action-flag test in this file. That silent
// equivalence is exactly the Core AC 4 risk this test exists to close: a
// future edge added to legalTransitions would widen canEdit but leave a
// hardcoded switch stale, and nothing else would catch it.
func TestGetHandler_ActionFlagsAreDerivedNotHardcoded(t *testing.T) {
	orig := legalTransitions
	t.Cleanup(func() { legalTransitions = orig })
	legalTransitions = edgeTableWith(orig, StatusFailed, StatusDraft)

	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusFailed}
	get := func(ctx context.Context, gotID string) (Invoice, error) { return want, nil }
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"can_edit":true`) {
		t.Errorf("body = %s, want the literal \"can_edit\":true for failed once failed->draft is a legal edge -- GetHandler must call canEdit(inv.Status), not restate a hardcoded per-status switch (Core AC 4)", body)
	}
}

// TestListHandler_NoActionFlagKeys (T14): List must stay clean of all three
// action-flag keys, mirroring TestListHandler_NoRuleSetVersionKey -- they
// live only on GetHandler's getResponse wrapper, never on the domain
// Invoice struct List marshals directly. ALREADY GREEN at RED (neither key
// exists anywhere yet); kept as a permanent regression guard.
func TestListHandler_NoActionFlagKeys(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invID := uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{{ID: invID, Status: StatusDraft}}, 1, nil
	}
	rec, _ := doInvoiceList(t, list, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, k := range []string{`"can_edit":`, `"can_revalidate":`, `"revalidate_blocked_reason":`} {
		if strings.Contains(body, k) {
			t.Errorf("body = %s, List must NOT gain %s -- these keys belong only to GetHandler's getResponse wrapper", body, k)
		}
	}
}

// TestGetHandler_RealStore_DraftActionFlags (T-E2E-GET): wires the REAL
// Store.Get into the REAL GetHandler (same method-value wiring
// cmd/invoice/main.go uses) against a freshly seeded draft row -- pins the
// action-flag contract end to end, DB row through to wire byte, mirroring
// TestGetHandler_RealStore_NeverValidatedEmitsExplicitNull. RED today: none
// of the three keys exist on the wire.
func TestGetHandler_RealStore_DraftActionFlags(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INVED-01-05-e2e-get tenant")
	entityID := seedEntity(t, super, tenantID, "INVED-01-05-e2e-get entity")
	store := NewStore(app)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "INVED-01-05-E2E-GET")

	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	r = r.WithContext(auth.WithIdentity(ctx, identity))
	rec := httptest.NewRecorder()

	GetHandler(store.Get, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"can_edit":true`) {
		t.Errorf("body = %s, want the literal \"can_edit\":true for a freshly seeded draft row", body)
	}
	if !strings.Contains(body, `"can_revalidate":true`) {
		t.Errorf("body = %s, want the literal \"can_revalidate\":true for a freshly seeded draft row", body)
	}
	if !strings.Contains(body, `"revalidate_blocked_reason":null`) {
		t.Errorf("body = %s, want the literal \"revalidate_blocked_reason\":null for a freshly seeded draft row", body)
	}
}

// TestEditHandler_RealStore_LineItemsThreeStates (T-E2E-PATCH): wires the
// REAL Store.Edit into the REAL EditHandler against real seeded, lined
// invoices (seedLinedInvoiceAtStatus, edit_test.go) -- the DB-e2e
// composition of the wire->store mapping the unit specs above prove in
// isolation. Four independent subtests, one fresh invoice each:
//
//   - populated_replaces_and_ignores_line_no: a populated line_items array
//     (including a client-supplied line_no:99) replaces the stored set,
//     renumbered 1..N by position, and the response carries the new lines.
//   - absent_leaves_lines_untouched: a header-only PATCH leaves the stored
//     rows byte-identical, including ids.
//   - empty_array_deletes_all_lines: `"line_items":[]` deletes every row.
//   - non_editable_status_409_rows_unchanged: editing an accepted invoice's
//     lines refuses with 409, nothing written.
//
// RED today for (a)/(c)/(d): with editReq.LineItems undecoded,
// EditInput.LineItems is always nil, so a body whose ONLY content is
// line_items hits Store.Edit's all-nil guard (hasHeader==false &&
// LineItems==nil) and 400s, rather than the 200/200/409 targeted here.
// (b) is a pre-existing, correct guard (a header-only edit already leaves
// nil-LineItems lines untouched) and is expected to already be green.
func TestEditHandler_RealStore_LineItemsThreeStates(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "INVED-01-05-e2e-patch tenant")
	entityID := seedEntity(t, super, tenantID, "INVED-01-05-e2e-patch entity")
	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	// seedLinedInvoiceAtStatus seeds via Store.Create, which -- like every
	// Store method -- reads the caller's tenant off the context (db.
	// WithinRequestTenantTx); it needs the SAME identity injected here, not
	// the bare ctx (edit_test.go's own seedLinedInvoiceAtStatus callers all
	// do this).
	c := auth.WithIdentity(ctx, identity)

	seedLines := []LineItemInput{
		{Description: strPtr("Seed A"), Quantity: strPtr("1"), UnitPrice: strPtr("10.00"), LineTotal: strPtr("10.00"), LineTax: strPtr("0.00")},
		{Description: strPtr("Seed B"), Quantity: strPtr("2"), UnitPrice: strPtr("5.00"), LineTotal: strPtr("10.00"), LineTax: strPtr("0.00")},
	}

	t.Run("populated_replaces_and_ignores_line_no", func(t *testing.T) {
		inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "INVED-01-05-E2E-PATCH-A", StatusDraft, seedLines)
		body := `{"line_items":[{"description":"New A","quantity":"3","unit_price":"7.00","line_total":"21.00","line_tax":"1.00","line_no":99}]}`
		rec, resp := doInvoiceEdit(t, store.Edit, &identity, inv.ID, body)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		stored := readLineItemsForTest(t, super, inv.ID)
		if len(stored) != 1 {
			t.Fatalf("stored line_items = %d rows, want 1 (replaced)", len(stored))
		}
		if stored[0].LineNo != 1 {
			t.Errorf("stored line_no = %d, want 1 -- system-assigned by array position; client-supplied line_no:99 must be ignored", stored[0].LineNo)
		}
		if len(resp.LineItems) != 1 || resp.LineItems[0].Description == nil || *resp.LineItems[0].Description != "New A" {
			t.Errorf("response line_items = %+v, want exactly 1 entry with description %q", resp.LineItems, "New A")
		}
	})

	t.Run("absent_leaves_lines_untouched", func(t *testing.T) {
		inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "INVED-01-05-E2E-PATCH-B", StatusDraft, seedLines)
		before := readLineItemsForTest(t, super, inv.ID)

		rec, _ := doInvoiceEdit(t, store.Edit, &identity, inv.ID, `{"vat":"9.99"}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		after := readLineItemsForTest(t, super, inv.ID)
		if !reflect.DeepEqual(before, after) {
			t.Errorf("line_items changed by a header-only PATCH: before %+v, after %+v -- absent line_items must leave lines byte-identical, including ids", before, after)
		}
	})

	t.Run("empty_array_deletes_all_lines", func(t *testing.T) {
		inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "INVED-01-05-E2E-PATCH-C", StatusDraft, seedLines)

		rec, _ := doInvoiceEdit(t, store.Edit, &identity, inv.ID, `{"line_items":[]}`)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
		after := readLineItemsForTest(t, super, inv.ID)
		if len(after) != 0 {
			t.Errorf("stored line_items = %d rows after an empty-array PATCH, want 0 (all deleted)", len(after))
		}
	})

	t.Run("non_editable_status_409_rows_unchanged", func(t *testing.T) {
		inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "INVED-01-05-E2E-PATCH-D", StatusAccepted, seedLines)
		before := readLineItemsForTest(t, super, inv.ID)

		rec, resp := doInvoiceEdit(t, store.Edit, &identity, inv.ID, `{"line_items":[{"description":"nope"}]}`)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Error("expected a non-empty error message in the body")
		}
		after := readLineItemsForTest(t, super, inv.ID)
		if !reflect.DeepEqual(before, after) {
			t.Errorf("line_items changed despite a 409-refused edit: before %+v, after %+v", before, after)
		}
	})
}

// --- INVED-01-05 QA (Mode B): Part D (DB e2e) + Part F (adversarial) -------
// ---------------------------------------------------------------------------
//
// Beyond the RED specs and the two Part B gap-closure tests above: DB e2e
// composition gaps the RED specs deliberately left to store-level/unit-level
// coverage (malformed line numeric through the REAL HTTP handler, a
// lines-only PATCH demoting a REAL validated invoice through the REAL HTTP
// handler), plus adversarial wire-shape coverage (mixed header+line_items in
// one call, an unknown per-line field, a large array, duplicate content
// lines, and a null array entry).

// TestEditHandler_RealStore_MalformedLineNumericIs400NotFrom500 (task-266
// Part D): T13 (TestEditHandler_LineItemsMalformedNumericIs400NotFrom500,
// above) only proves the statusForErr mapping via a STUBBED store that
// fabricates a wrapped ErrValidation -- it never exercises the real
// replaceLinesTx 22P02 path through the real HTTP handler. This wires the
// REAL Store.Edit into the REAL EditHandler (mirrors
// TestEditHandler_RealStore_LineItemsThreeStates) so a malformed line
// numeric's actual 22P02 -> ErrValidation -> 400 chain is proven end to end,
// DB error through to wire status code, and the original line survives the
// rolled-back replace-all.
func TestEditHandler_RealStore_MalformedLineNumericIs400NotFrom500(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "INVED-01-05-e2e-malformed tenant")
	entityID := seedEntity(t, super, tenantID, "INVED-01-05-e2e-malformed entity")
	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, identity)

	seedLines := []LineItemInput{
		{Description: strPtr("Seed A"), Quantity: strPtr("1"), UnitPrice: strPtr("10.00"), LineTotal: strPtr("10.00"), LineTax: strPtr("0.00")},
	}
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "INVED-01-05-E2E-MALFORMED", StatusDraft, seedLines)

	body := `{"line_items":[{"description":"a","unit_price":"not-a-number"}]}`
	rec, resp := doInvoiceEdit(t, store.Edit, &identity, inv.ID, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, NOT 500 (body=%s) -- the real store's 22P02 must map through statusForErr's ErrValidation case", rec.Code, rec.Body.String())
	}
	if resp.Error == "" {
		t.Error("expected a non-empty error message in the body")
	}
	after := readLineItemsForTest(t, super, inv.ID)
	if len(after) != 1 || after[0].Description == nil || *after[0].Description != "Seed A" {
		t.Errorf("line_items after a 400-refused edit = %+v, want the original 1 row unchanged (the malformed replace-all rolled back)", after)
	}
}

// TestEditHandler_RealStore_LinesOnlyPatchDemotesValidatedInvoice (task-266
// Part D): the RED specs' DB e2e four-way table
// (TestEditHandler_RealStore_LineItemsThreeStates) exercises all four
// line_items states only against a DRAFT (or, for the 409 case, an
// ACCEPTED) invoice -- it never proves a lines-only PATCH through the REAL
// HTTP handler demotes a VALIDATED invoice. Store-level demotion is already
// proven (TestStoreEdit_LineChangeOnValidatedDemotesAndAuditsLineItemsField,
// INVED-01-04) by calling store.Edit directly; this is the composition test
// that proves the SAME contract survives the wire decode this subtask adds:
// a body carrying ONLY line_items (no header field at all) reaches
// EditHandler, decodes to a non-nil EditInput.LineItems, and Store.Edit's
// demotion step still fires -- response status draft, one invoice.updated
// audit row whose fields include "line_items", one invoice.transitioned
// audit row, and one (validated,draft) invoice_status_history row.
func TestEditHandler_RealStore_LinesOnlyPatchDemotesValidatedInvoice(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "INVED-01-05-e2e-demote tenant")
	entityID := seedEntity(t, super, tenantID, "INVED-01-05-e2e-demote entity")
	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, identity)

	seedLines := []LineItemInput{
		{Description: strPtr("Seed A"), Quantity: strPtr("1"), UnitPrice: strPtr("10.00"), LineTotal: strPtr("10.00"), LineTax: strPtr("0.00")},
	}
	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "INVED-01-05-E2E-DEMOTE", StatusValidated, seedLines)

	beforeUpdated := auditCount(t, app, tenantID, "invoice.updated")
	beforeTransitioned := auditCount(t, app, tenantID, "invoice.transitioned")

	body := `{"line_items":[{"description":"New A","quantity":"2","unit_price":"5.00","line_total":"10.00","line_tax":"0.00"}]}`
	rec, resp := doInvoiceEdit(t, store.Edit, &identity, inv.ID, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusDraft) {
		t.Errorf("response status = %q, want %q -- a lines-only PATCH (no header fields) on a validated invoice must still demote it", resp.Status, StatusDraft)
	}
	if n := auditCount(t, app, tenantID, "invoice.updated"); n != beforeUpdated+1 {
		t.Errorf("invoice.updated audit rows = %d, want %d", n, beforeUpdated+1)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeTransitioned+1 {
		t.Errorf("invoice.transitioned audit rows = %d, want %d", n, beforeTransitioned+1)
	}
	fields := auditFields(t, app, tenantID, "invoice.updated")
	found := false
	for _, f := range fields {
		if f == "line_items" {
			found = true
		}
	}
	if !found {
		t.Errorf("invoice.updated audit fields = %v, want it to contain %q ([audit-fields-includes-line-items])", fields, "line_items")
	}
	if n := mustCount(t, super,
		`SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND from_status = 'validated' AND to_status = 'draft'`, inv.ID,
	); n != 1 {
		t.Errorf("invoice_status_history (validated,draft) rows = %d, want exactly 1", n)
	}
}

// TestEditHandler_MixedHeaderAndLineItemsBothMapped (task-266 Part F): a
// single PATCH carrying BOTH a header field and line_items must map BOTH
// into EditInput -- neither the header decode nor the line_items decode
// path in EditHandler is exclusive of the other. None of the RED unit specs
// combine the two in one body (T1's fixture is line_items-only; the header
// mapping specs predate this story).
func TestEditHandler_MixedHeaderAndLineItemsBothMapped(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"vat":"9.99","supplier_name":"Acme","line_items":[{"description":"a"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.VAT == nil || *gotIn.VAT != "9.99" {
		t.Errorf("EditInput.VAT = %v, want a non-nil pointer to %q", gotIn.VAT, "9.99")
	}
	if gotIn.SupplierName == nil || *gotIn.SupplierName != "Acme" {
		t.Errorf("EditInput.SupplierName = %v, want a non-nil pointer to %q", gotIn.SupplierName, "Acme")
	}
	if gotIn.LineItems == nil || len(*gotIn.LineItems) != 1 {
		t.Fatalf("EditInput.LineItems = %v, want a non-nil pointer to 1 mapped line item", gotIn.LineItems)
	}
	if got := (*gotIn.LineItems)[0].Description; got == nil || *got != "a" {
		t.Errorf("line[0].Description = %v, want %q", got, "a")
	}
}

// TestEditHandler_LineItemsUnknownFieldIgnored200 (task-266 Part F): an
// unknown key WITHIN a line_items entry (not just at the top level, already
// pinned by TestEditHandler_UnknownFieldIgnored200) must be silently
// ignored -- EditHandler never calls DisallowUnknownFields, and lineItemReq
// has no such field, so this is the line-item mirror of that existing
// top-level guard.
func TestEditHandler_LineItemsUnknownFieldIgnored200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"line_items":[{"description":"a","not_a_real_field":"whatever"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an unknown per-line JSON field (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.LineItems == nil || len(*gotIn.LineItems) != 1 {
		t.Fatalf("EditInput.LineItems = %v, want a non-nil pointer to 1 mapped line item", gotIn.LineItems)
	}
	if got := (*gotIn.LineItems)[0].Description; got == nil || *got != "a" {
		t.Errorf("line[0].Description = %v, want %q -- the unknown field must not have interfered with decoding the known ones", got, "a")
	}
}

// TestEditHandler_LineItemsLargeArrayMapped (task-266 Part F): no
// http.MaxBytesReader/line-count cap applies to PATCH (documented,
// traceable-to-no-AC non-goal in the Implementation Plan, mirroring
// CreateHandler which has none either) -- a large line_items array must
// still decode and map completely, not truncate or error partway.
func TestEditHandler_LineItemsLargeArrayMapped(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}

	const n = 500
	var b strings.Builder
	b.WriteString(`{"line_items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"description":"line-%d"}`, i)
	}
	b.WriteString(`]}`)

	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, b.String())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a %d-entry line_items array (body len=%d)", rec.Code, n, rec.Body.Len())
	}
	if gotIn.LineItems == nil || len(*gotIn.LineItems) != n {
		t.Fatalf("len(*EditInput.LineItems) = %v, want %d", gotIn.LineItems, n)
	}
	first, last := (*gotIn.LineItems)[0], (*gotIn.LineItems)[n-1]
	if first.Description == nil || *first.Description != "line-0" {
		t.Errorf("line[0].Description = %v, want %q", first.Description, "line-0")
	}
	if last.Description == nil || *last.Description != fmt.Sprintf("line-%d", n-1) {
		t.Errorf("line[%d].Description = %v, want %q", n-1, last.Description, fmt.Sprintf("line-%d", n-1))
	}
}

// TestEditHandler_RealStore_DuplicateContentLinesBothSurvive (task-266 Part
// F): two line_items entries with byte-identical MBS content (same
// description/quantity/price/total/tax) must BOTH survive the replace-all --
// line_no is system-assigned by array POSITION ([line-no-by-position]), not
// derived from content, so there is no content-based uniqueness constraint
// to collide with. Confirms replaceLinesTx's per-line_no unique index
// (line_items_invoice_line_no_uq) is scoped to (invoice_id, line_no), never
// to content.
func TestEditHandler_RealStore_DuplicateContentLinesBothSurvive(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "INVED-01-05-e2e-dup tenant")
	entityID := seedEntity(t, super, tenantID, "INVED-01-05-e2e-dup entity")
	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	c := auth.WithIdentity(ctx, identity)

	inv := seedLinedInvoiceAtStatus(t, super, store, c, entityID, "INVED-01-05-E2E-DUP", StatusDraft, nil)

	body := `{"line_items":[` +
		`{"description":"Same","quantity":"1","unit_price":"10.00","line_total":"10.00","line_tax":"0.00"},` +
		`{"description":"Same","quantity":"1","unit_price":"10.00","line_total":"10.00","line_tax":"0.00"}` +
		`]}`
	rec, _ := doInvoiceEdit(t, store.Edit, &identity, inv.ID, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for two content-identical lines (body=%s)", rec.Code, rec.Body.String())
	}
	stored := readLineItemsForTest(t, super, inv.ID)
	if len(stored) != 2 {
		t.Fatalf("stored line_items = %d rows, want 2 -- duplicate CONTENT must not be deduplicated or rejected", len(stored))
	}
	if stored[0].LineNo != 1 || stored[1].LineNo != 2 {
		t.Errorf("stored line_no = [%d, %d], want [1, 2] -- system-assigned by position", stored[0].LineNo, stored[1].LineNo)
	}
	for i, li := range stored {
		if li.Description == nil || *li.Description != "Same" {
			t.Errorf("stored[%d].Description = %v, want %q", i, li.Description, "Same")
		}
	}
}

// TestEditHandler_LineItemsNullEntryDecodesToZeroValueNotError (task-266
// Part F): a line_items array containing a `null` ELEMENT (not the whole
// key, already covered by T2b) alongside a real object must decode without
// error -- encoding/json's null handling for a struct-kind slice element
// leaves it at its zero value (every lineItemReq field is a nil pointer)
// rather than raising a decode error, since lineItemReq is a plain struct,
// not a pointer/map/slice/interface. Pins that this does not panic or 500
// when the mapping loop dereferences its (all-nil) fields.
func TestEditHandler_LineItemsNullEntryDecodesToZeroValueNotError(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft}
	var gotIn EditInput
	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) {
		gotIn = in
		return want, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &id, invoiceID, `{"line_items":[null,{"description":"a"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a null array entry, NOT a decode error (body=%s)", rec.Code, rec.Body.String())
	}
	if gotIn.LineItems == nil || len(*gotIn.LineItems) != 2 {
		t.Fatalf("EditInput.LineItems = %v, want a non-nil pointer to 2 entries", gotIn.LineItems)
	}
	lines := *gotIn.LineItems
	if lines[0].Description != nil {
		t.Errorf("line[0] (from a JSON null entry).Description = %v, want nil (zero-value, not an error)", lines[0].Description)
	}
	if lines[1].Description == nil || *lines[1].Description != "a" {
		t.Errorf("line[1].Description = %v, want %q", lines[1].Description, "a")
	}
}

// --- GET /v1/invoices/violation-summary (task-283 specs 12-14) -------------

// violationSummaryBody mirrors the (future) GET
// /v1/invoices/violation-summary response wire shape (violationSummaryResponse,
// handlers.go), plus an Error field for the shared {"error":"..."} envelope
// -- same convention as invoiceBody/listInvoicesResponse.
type violationSummaryBody struct {
	Rules []RuleCount `json:"rules"`
	Error string      `json:"error"`
}

// doViolationSummary builds the GET /v1/invoices/violation-summary request
// (query appended verbatim, e.g. "?import_batch_id=..."), injects id into
// the context when non-nil, runs it through ViolationSummaryHandler(summary,
// nil), and decodes the JSON response body.
func doViolationSummary(t *testing.T, summary func(ctx context.Context, importBatchID string) ([]RuleCount, error), id *auth.Identity, query string) (*httptest.ResponseRecorder, violationSummaryBody) {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/invoices/violation-summary"+query, nil)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	ViolationSummaryHandler(summary, nil).ServeHTTP(rec, r)
	var resp violationSummaryBody
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, resp
}

// TestViolationSummaryHandler_MissingOrMalformedBatchID400StoreNeverCalled
// (spec 12, task-283 AC-6): import_batch_id is REQUIRED on this route --
// absent OR malformed must 400 BEFORE store.ViolationSummary is ever
// called (an unbounded tenant-wide aggregation is not a supported query).
// The status alone is VACUOUS: a malformed uuid reaching the store would
// ALSO plausibly 400 -- the spy (store must not run) is the only half of
// this test that actually discriminates a handler-level uuid.Parse
// pre-check from a store-level rejection.
func TestViolationSummaryHandler_MissingOrMalformedBatchID400StoreNeverCalled(t *testing.T) {
	t.Run("missing import_batch_id", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		summary := func(ctx context.Context, importBatchID string) ([]RuleCount, error) {
			t.Fatal("store.ViolationSummary must not run when import_batch_id is absent")
			return nil, nil
		}
		rec, resp := doViolationSummary(t, summary, &id, "")

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Error("expected a non-empty error message in the body")
		}
	})

	t.Run("malformed import_batch_id", func(t *testing.T) {
		id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
		summary := func(ctx context.Context, importBatchID string) ([]RuleCount, error) {
			t.Fatal("store.ViolationSummary must not run when import_batch_id is not a well-formed uuid")
			return nil, nil
		}
		rec, resp := doViolationSummary(t, summary, &id, "?import_batch_id=not-a-uuid")

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
		}
		if resp.Error == "" {
			t.Error("expected a non-empty error message in the body")
		}
	})
}

// TestViolationSummaryHandler_RulesIsEmptyArrayNotNull (spec 13): a store
// returning a nil []RuleCount must still render "rules":[], never null.
func TestViolationSummaryHandler_RulesIsEmptyArrayNotNull(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	summary := func(ctx context.Context, importBatchID string) ([]RuleCount, error) {
		return nil, nil
	}
	rec, _ := doViolationSummary(t, summary, &id, "?import_batch_id="+uuid.NewString())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if !bytes.Contains(raw, []byte(`"rules":[]`)) {
		t.Errorf("body = %s, want raw JSON to contain \"rules\":[] (never null, even when the store returns a nil slice)", raw)
	}
}

// TestRoutes_BothResolveInBothDirections (spec 14, task-283 R6): Go 1.22+
// net/http.ServeMux resolves by PATTERN SPECIFICITY, not registration order
// -- a literal segment ("violation-summary") always beats a wildcard
// ({id}), in EITHER registration order (go.mod is go 1.26.4; empirically
// verified live both ways -- the older "register the literal BEFORE {id}"
// wording is obsolete). This test drives a real *http.ServeMux, built in
// BOTH registration orders, for BOTH request directions each time, and
// asserts the CORRECT closure fires (and the wrong one does not) every
// time.
func TestRoutes_BothResolveInBothDirections(t *testing.T) {
	build := func(literalFirst bool) (mux *http.ServeMux, getCalled, summaryCalled *bool) {
		getCalled = new(bool)
		summaryCalled = new(bool)
		get := func(ctx context.Context, id string) (Invoice, error) {
			*getCalled = true
			return Invoice{}, nil
		}
		summary := func(ctx context.Context, importBatchID string) ([]RuleCount, error) {
			*summaryCalled = true
			return nil, nil
		}
		mux = http.NewServeMux()
		if literalFirst {
			mux.HandleFunc("GET /v1/invoices/violation-summary", ViolationSummaryHandler(summary, nil))
			mux.HandleFunc("GET /v1/invoices/{id}", GetHandler(get, nil))
		} else {
			mux.HandleFunc("GET /v1/invoices/{id}", GetHandler(get, nil))
			mux.HandleFunc("GET /v1/invoices/violation-summary", ViolationSummaryHandler(summary, nil))
		}
		return mux, getCalled, summaryCalled
	}

	callerID := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	doReq := func(mux *http.ServeMux, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", target, nil)
		r = r.WithContext(auth.WithIdentity(r.Context(), callerID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}

	for _, literalFirst := range []bool{true, false} {
		name := "wildcard registered first"
		if literalFirst {
			name = "literal registered first"
		}
		t.Run(name, func(t *testing.T) {
			mux, getCalled, summaryCalled := build(literalFirst)
			doReq(mux, "/v1/invoices/violation-summary?import_batch_id="+uuid.NewString())
			if !*summaryCalled {
				t.Error("GET /v1/invoices/violation-summary did not resolve to ViolationSummaryHandler's closure")
			}
			if *getCalled {
				t.Error(`GET /v1/invoices/violation-summary incorrectly resolved to GetHandler's closure ("violation-summary" read as a path {id})`)
			}

			mux, getCalled, summaryCalled = build(literalFirst)
			doReq(mux, "/v1/invoices/"+uuid.NewString())
			if !*getCalled {
				t.Error("GET /v1/invoices/<uuid> did not resolve to GetHandler's closure")
			}
			if *summaryCalled {
				t.Error("GET /v1/invoices/<uuid> incorrectly resolved to ViolationSummaryHandler's closure")
			}
		})
	}
}
