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
type invoiceBody struct {
	ID            string         `json:"id"`
	EntityID      string         `json:"entity_id"`
	InvoiceNumber string         `json:"invoice_number"`
	Status        string         `json:"status"`
	LineItems     []lineItemWire `json:"line_items"`
	Error         string         `json:"error"`
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

// TestCreateHandler_201_WireShape (QA Mode B adversarial, Surface-Conflict
// verification): a created invoice's RAW response body must be the
// snake_case wire shape the story's System Design specifies -- entity_id,
// invoice_number, status, violations, and line_items all present -- AND must
// NOT contain rule_set_version_id at all. Invoice.Violations carries
// `json:"violations"` (surfaced, always "[]" in M4-02) while
// Invoice.RuleSetVersionID carries `json:"-"` (hidden, M4-04's field); this
// test sets RuleSetVersionID to a non-nil value specifically so a regression
// that dropped the `json:"-"` tag (reverting to a normal `json:"rule_set_version_id"`
// tag) would leak the value into the body and fail this test -- checking the
// raw bytes rather than a decoded struct (whose Go type simply lacks the
// field) is what makes this non-vacuous.
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
	if bytes.Contains(raw, []byte(`rule_set_version_id`)) {
		t.Errorf("body = %s, must NOT contain rule_set_version_id (json:\"-\", hidden per M4-02 System Design)", raw)
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
func TestTransitionHandler_200(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusValidated}
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		if gotID != invoiceID || target != StatusValidated {
			t.Fatalf("transition called with id=%q target=%q, want id=%q target=%q", gotID, target, invoiceID, StatusValidated)
		}
		return want, nil
	}
	body, err := json.Marshal(transitionRequest{Target: "validated"})
	if err != nil {
		t.Fatalf("marshal transition request: %v", err)
	}
	rec, resp := doInvoiceTransition(t, transition, &id, invoiceID, string(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusValidated) {
		t.Errorf("status = %q, want %q", resp.Status, StatusValidated)
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
func TestTransitionHandler_NotFound404(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		return Invoice{}, ErrNotFound
	}
	body, err := json.Marshal(transitionRequest{Target: "validated"})
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
func TestTransitionHandler_NoIdentity401(t *testing.T) {
	invoiceID := uuid.NewString()
	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) {
		t.Fatal("transition must not run without an identity")
		return Invoice{}, nil
	}
	body, err := json.Marshal(transitionRequest{Target: "validated"})
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
