// M5-05-02 (task-238), RALPH Stage 2.5 Mode A: RED tests for the fiscal-
// outcome read projection -- irn/csid/qr_payload/rejection_reasons readable
// over Store.Get/Store.List and the GET/List HTTP surfaces (Core AC#6).
// Written BEFORE Invoice gains the four fields (that IS this subtask, Stage
// 3) -- so every assertion below goes through the WIRE (json.Marshal of
// whatever Store.Get/Store.List actually returns, or the raw HTTP response
// bytes) rather than a typed inv.IRN/inv.RejectionReasons access, which
// would not compile yet. Each test fails TODAY because the key is simply
// absent from the marshalled output; none fail on a compile or setup error,
// and each flips green for the right reason once Stage 3 adds the fields
// (invoiceColumns/scanInvoice/Invoice struct -- no other code change is
// needed for these tests to pass).
//
// Migration 20260722083015_invoices_fiscal_outcome.sql (M5-01-02) already
// shipped the four DB columns -- this file exercises them directly via the
// superuser pool for seeding, the same idiom edit_test.go's task-237 tests
// use for rejection_reasons.
//
// Spec-to-test map (Test Specs table, M5-05-02 story / task-238):
//
//	TestStoreGet_ProjectsFiscalOutcomeColumns
//	TestStoreList_ProjectsFiscalOutcomeColumns
//	TestGetHandler_FiscalOutcomeKeysAreTopLevelSiblings
//	TestGetHandler_EmptyRejectionReasonsMarshalsEmptyArrayNotNull
//	TestGetHandler_AbsentIRNMarshalsExplicitNull
//	TestListHandler_FiscalOutcomeOnEveryItem
//
// QA Mode A additions beyond the table -- AC#4/#5's DB-to-wire proof end to
// end (not just at a fake-store handler layer), and the register #8 forward
// guard for handlers_test.go:1340's TestValidateHandler_ResponseIsAdditive:
//
//	TestGetHandler_RealStore_NeverSubmittedRendersOutcomeDefaults (AC#4)
//	TestGetHandler_RealStore_SeededOutcomeRendersVerbatim         (AC#5)
//	TestListHandler_RealStore_SeededOutcomeRendersVerbatim        (AC#3/#5, list surface)
//	TestValidateHandler_ResponseCarriesRejectionReasonsEmptyArray (register #8 forward guard)
package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- Test Specs table -------------------------------------------------------

// TestStoreGet_ProjectsFiscalOutcomeColumns (Test Specs table): Store.Get
// must hydrate irn/csid/qr_payload/rejection_reasons from a row seeded with
// a real outcome. Invoice has no Go-side field for any of the four yet
// (that IS this subtask), so the proof goes through the wire instead:
// json.Marshal the Invoice Store.Get actually returns and check the raw
// bytes for the seeded values -- a compile-safe assertion that needs no new
// symbol, and one that keeps proving the same thing once the fields land
// (the marshalled bytes gain the keys for free). FAILS today: none of the
// four keys are in the marshalled output at all.
func TestStoreGet_ProjectsFiscalOutcomeColumns(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M5-05-02 tenant")
	entityID := seedEntity(t, super, tenantID, "M5-05-02 entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "M5-05-02-GET")

	reasonsJSON := `[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"},{"code":"VAT_RATE","message":"VAT must equal 7.5%","path":"vat"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"IRN-GET-0001", "CSID-GET-0001", "QR-PAYLOAD-GET-0001", reasonsJSON, invoiceID,
	); err != nil {
		t.Fatalf("seed fiscal outcome columns (is the M5-01-02 migration applied?): %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.Get(c, invoiceID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal Store.Get's returned Invoice: %v", err)
	}
	body := string(b)
	for _, want := range []string{
		`"irn":"IRN-GET-0001"`,
		`"csid":"CSID-GET-0001"`,
		`"qr_payload":"QR-PAYLOAD-GET-0001"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Store.Get's marshalled Invoice = %s, want it to contain %s -- AC#6 (Store.Get must hydrate the seeded outcome, not just the DB row)", body, want)
		}
	}
	if !strings.Contains(body, `"code":"TIN_MISMATCH"`) || !strings.Contains(body, `"code":"VAT_RATE"`) {
		t.Errorf("Store.Get's marshalled Invoice = %s, want both seeded rejection_reasons elements to survive verbatim", body)
	}
}

// TestStoreList_ProjectsFiscalOutcomeColumns (Test Specs table): the list
// header shape must carry the outcome too, not just Store.Get's detail
// hydration -- Store.List shares invoiceColumns/scanInvoice with Store.Get
// (store.go's single-source-of-truth, Stage 1's re-grepped claim), so this
// pins that sharing at the List call site specifically. Same wire-marshal
// technique as TestStoreGet_ProjectsFiscalOutcomeColumns, for the same
// compile-safety reason. FAILS today: the four keys are absent from the
// marshalled list item.
func TestStoreList_ProjectsFiscalOutcomeColumns(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M5-05-02 list tenant")
	entityID := seedEntity(t, super, tenantID, "M5-05-02 list entity")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "M5-05-02-LIST")

	reasonsJSON := `[{"code":"APP-ERR-0417","message":"Supplier TIN not registered","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"IRN-LIST-0001", "CSID-LIST-0001", "QR-PAYLOAD-LIST-0001", reasonsJSON, invoiceID,
	); err != nil {
		t.Fatalf("seed fiscal outcome columns: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, _, err := store.List(c, ListFilter{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, inv := range items {
		if inv.ID != invoiceID {
			continue
		}
		found = true
		b, err := json.Marshal(inv)
		if err != nil {
			t.Fatalf("marshal List's returned Invoice item: %v", err)
		}
		body := string(b)
		for _, want := range []string{
			`"irn":"IRN-LIST-0001"`,
			`"csid":"CSID-LIST-0001"`,
			`"qr_payload":"QR-PAYLOAD-LIST-0001"`,
			`"code":"APP-ERR-0417"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("List item's marshalled Invoice = %s, want it to contain %s -- the list surface must project the outcome too, not just Get", body, want)
			}
		}
	}
	if !found {
		t.Fatalf("List did not return the seeded invoice %q", invoiceID)
	}
}

// TestGetHandler_FiscalOutcomeKeysAreTopLevelSiblings (Test Specs table):
// modeled on TestValidateHandler_TopLevelKeysNotNested's raw top-level-key
// decode -- irn/csid/qr_payload/rejection_reasons must be direct top-level
// siblings of rule_set_version_id (getResponse embeds Invoice, encoding/json
// flattens the embed), never nested under a sub-object. FAILS today: none
// of the four keys exist in the raw map at all.
func TestGetHandler_FiscalOutcomeKeysAreTopLevelSiblings(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusValidated, Violations: json.RawMessage(`[]`)}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response into a raw top-level key map: %v (body=%s)", err, rec.Body.String())
	}
	for _, k := range []string{"irn", "csid", "qr_payload", "rejection_reasons"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("raw JSON keys missing %q (body=%s) -- Core AC#6: the fiscal outcome must be a direct top-level sibling of rule_set_version_id, not nested", k, rec.Body.String())
		}
	}
}

// TestGetHandler_EmptyRejectionReasonsMarshalsEmptyArrayNotNull (Test Specs
// table): the M4-16 read-side trap this subtask must not repeat -- a
// never-rejected invoice's rejection_reasons must render as the literal
// "[]", never "null" (AC#4). Checked on the raw marshalled bytes, the same
// convention TestBatchSubmitHandler_EmptyResultsMarshalsEmptyArrayNotNull
// (batch_submit_handler_test.go) and TestListHandler_EmptyState use. FAILS
// today: the key is absent altogether, so the "contains []" assertion fails
// outright (it would ALSO fail post-implementation if RejectionReasons were
// typed []Reason instead of json.RawMessage -- exactly the hazard this
// guards against).
func TestGetHandler_EmptyRejectionReasonsMarshalsEmptyArrayNotNull(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft, Violations: json.RawMessage(`[]`), RejectionReasons: json.RawMessage(`[]`)}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"rejection_reasons":[]`) {
		t.Errorf("body = %s, want the literal \"rejection_reasons\":[] -- AC#4, never null", body)
	}
	if strings.Contains(body, `"rejection_reasons":null`) {
		t.Errorf("body = %s, must NEVER contain \"rejection_reasons\":null (the M4-16 hazard this subtask must not repeat)", body)
	}
}

// TestGetHandler_AbsentIRNMarshalsExplicitNull (Test Specs table, AC#4):
// irn/csid/qr_payload are *string with NO omitempty (Stage 1's confirmed
// struct decl, matching the SupplierTIN/BuyerTIN sibling convention) -- a
// never-submitted invoice must render an explicit "irn":null rather than
// dropping the key, and likewise for csid/qr_payload (same mechanism, same
// test). FAILS today: none of the three keys are present at all -- a
// missing key is not the same claim as an explicit null, which is what pins
// the "no omitempty" contract once the fields land.
func TestGetHandler_AbsentIRNMarshalsExplicitNull(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusDraft, Violations: json.RawMessage(`[]`)}
	get := func(ctx context.Context, gotID string) (Invoice, error) {
		return want, nil
	}
	rec, _ := doInvoiceGet(t, get, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"irn":null`, `"csid":null`, `"qr_payload":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s, want it to contain the literal %s", body, want)
		}
	}
}

// TestListHandler_FiscalOutcomeOnEveryItem (Test Specs table, AC#3): the
// list envelope's items must carry the four keys too -- not just the detail
// (Get) surface. Two fake items (neither can set the not-yet-existing
// IRN/CSID/QRPayload/RejectionReasons fields on the Invoice literal itself
// -- that would fail to compile today; the verbatim-seeded-value claim is
// instead pinned by TestListHandler_RealStore_SeededOutcomeRendersVerbatim
// below, which writes the values through raw SQL rather than a Go literal).
// Decoded via the raw "invoices" array. FAILS today: absent on both items.
func TestListHandler_FiscalOutcomeOnEveryItem(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	idA := uuid.NewString()
	idB := uuid.NewString()
	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) {
		return []Invoice{
			{ID: idA, Status: StatusDraft, Violations: json.RawMessage(`[]`)},
			{ID: idB, Status: StatusRejected, Violations: json.RawMessage(`[]`)},
		}, 2, nil
	}
	rec, _ := doInvoiceList(t, list, &id, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var raw struct {
		Invoices []map[string]json.RawMessage `json:"invoices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response into raw items: %v (body=%s)", err, rec.Body.String())
	}
	if len(raw.Invoices) != 2 {
		t.Fatalf("invoices length = %d, want 2 (body=%s)", len(raw.Invoices), rec.Body.String())
	}
	for i, item := range raw.Invoices {
		for _, k := range []string{"irn", "csid", "qr_payload", "rejection_reasons"} {
			if _, ok := item[k]; !ok {
				t.Errorf("invoices[%d] missing key %q (body=%s) -- AC#3: every list item must carry the fiscal outcome, not just the detail (Get) surface", i, k, rec.Body.String())
			}
		}
	}
}

// --- QA Mode A additions: DB-to-wire proof end to end ----------------------

// TestGetHandler_RealStore_NeverSubmittedRendersOutcomeDefaults (AC#4):
// wires the REAL Store.Get into the REAL GetHandler (same method-value
// wiring cmd/invoice/main.go uses in production, mirrors
// TestGetHandler_RealStore_NeverValidatedEmitsExplicitNull in
// rule_set_version_adversarial_test.go) against a freshly seeded row that
// has never been touched by a submission (irn/csid/qr_payload NULL,
// rejection_reasons at its DB DEFAULT '[]') -- pins Core AC#4 end to end, DB
// row -> Store.Get -> wire byte, not just at a fake-store handler layer.
// FAILS today: none of the four keys are in the response at all.
func TestGetHandler_RealStore_NeverSubmittedRendersOutcomeDefaults(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M5-05-02-e2e-defaults tenant")
	entityID := seedEntity(t, super, tenantID, "M5-05-02-e2e-defaults entity")
	store := NewStore(app)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "M5-05-02-E2E-DEFAULTS")

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
	for _, want := range []string{`"irn":null`, `"csid":null`, `"qr_payload":null`, `"rejection_reasons":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s, want it to contain the literal %s -- AC#4, end to end from a real, freshly seeded never-submitted row", body, want)
		}
	}
	if strings.Contains(body, `"rejection_reasons":null`) {
		t.Errorf("body = %s, must NEVER contain \"rejection_reasons\":null", body)
	}
}

// TestGetHandler_RealStore_SeededOutcomeRendersVerbatim (AC#5): a real row
// carrying a stored outcome (superuser-set irn/csid/qr_payload plus a real
// 2-element rejection_reasons array) must render every value verbatim
// through the real Store.Get -> real GetHandler stack. FAILS today: none of
// the four keys are in the response at all.
func TestGetHandler_RealStore_SeededOutcomeRendersVerbatim(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M5-05-02-e2e-seeded tenant")
	entityID := seedEntity(t, super, tenantID, "M5-05-02-e2e-seeded entity")
	store := NewStore(app)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "M5-05-02-E2E-SEEDED")
	reasonsJSON := `[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"},{"code":"VAT_RATE","message":"VAT must equal 7.5%","path":"vat"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"IRN-E2E-0001", "CSID-E2E-0001", "QR-PAYLOAD-E2E-0001", reasonsJSON, invoiceID,
	); err != nil {
		t.Fatalf("seed fiscal outcome columns: %v", err)
	}

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
	for _, want := range []string{
		`"irn":"IRN-E2E-0001"`,
		`"csid":"CSID-E2E-0001"`,
		`"qr_payload":"QR-PAYLOAD-E2E-0001"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s, want it to contain the literal %s -- AC#5, verbatim seeded values end to end", body, want)
		}
	}
	if !strings.Contains(body, `"code":"TIN_MISMATCH"`) || !strings.Contains(body, `"code":"VAT_RATE"`) {
		t.Errorf("body = %s, want both seeded rejection_reasons elements to survive verbatim -- AC#5", body)
	}
}

// TestListHandler_RealStore_SeededOutcomeRendersVerbatim (AC#3/#5): the same
// DB-to-wire proof as TestGetHandler_RealStore_SeededOutcomeRendersVerbatim,
// but through the list surface (real Store.List -> real ListHandler) -- AC#6
// names "GET /v1/invoices" explicitly, not just the detail route. FAILS
// today: none of the four keys are in the response at all.
func TestListHandler_RealStore_SeededOutcomeRendersVerbatim(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M5-05-02-e2e-list tenant")
	entityID := seedEntity(t, super, tenantID, "M5-05-02-e2e-list entity")
	store := NewStore(app)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "M5-05-02-E2E-LIST")
	reasonsJSON := `[{"code":"APP-ERR-0417","message":"Supplier TIN not registered","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"IRN-E2E-LIST-0001", "CSID-E2E-LIST-0001", "QR-PAYLOAD-E2E-LIST-0001", reasonsJSON, invoiceID,
	); err != nil {
		t.Fatalf("seed fiscal outcome columns: %v", err)
	}

	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	r := httptest.NewRequest("GET", "/v1/invoices", nil)
	r = r.WithContext(auth.WithIdentity(ctx, identity))
	rec := httptest.NewRecorder()

	ListHandler(store.List, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"irn":"IRN-E2E-LIST-0001"`,
		`"csid":"CSID-E2E-LIST-0001"`,
		`"qr_payload":"QR-PAYLOAD-E2E-LIST-0001"`,
		`"code":"APP-ERR-0417"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s, want it to contain the literal %s -- the list surface must project the seeded outcome verbatim too", body, want)
		}
	}
}

// TestValidateHandler_ResponseCarriesRejectionReasonsEmptyArray (forward
// guard for Test-Inversion Register #8, handlers_test.go:1340/:1361's
// TestValidateHandler_ResponseIsAdditive): once RejectionReasons lands on
// Invoice with no omitempty, a stubbed store that leaves it unset (Go
// zero-value nil json.RawMessage) marshals a real "null" over the validate
// route too -- reintroducing the M4-16 hazard there specifically, not just
// on GET (see the forward note above TestValidateHandler_ResponseIsAdditive
// itself). This pins the SAME wire contract as
// TestGetHandler_EmptyRejectionReasonsMarshalsEmptyArrayNotNull, on the
// validate route. Compiles against the CURRENT Invoice (no field
// reference); FAILS today because the key is absent altogether.
func TestValidateHandler_ResponseCarriesRejectionReasonsEmptyArray(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	want := Invoice{ID: invoiceID, Status: StatusValidated, Violations: json.RawMessage(`[]`), RejectionReasons: json.RawMessage(`[]`)}
	validate := func(ctx context.Context, gotID string) (Invoice, int, error) {
		return want, 2, nil
	}
	rec, _ := doInvoiceValidate(t, validate, &id, invoiceID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"rejection_reasons":[]`) {
		t.Errorf("body = %s, want the literal \"rejection_reasons\":[] on the validate route too", body)
	}
}

// --- QA Mode B additions (M5-05-02, task-238 Stage 4): cross-tenant
// isolation of the new columns, no-bleed across List rows, and an
// order+content deep-equality guard on rejection_reasons (the existing Test
// Specs coverage above only asserts a Contains substring per field/element,
// which cannot distinguish "verbatim" from "same set, different order or
// with extra/missing sub-fields") ------------------------------------------

// TestStoreGet_CrossTenantFiscalOutcomeIsolated: the four new columns live
// on the same invoices row as every other tenant-scoped column, so RLS
// (already proven generically by TestStoreGet_CrossTenantNotFound,
// store_test.go) should refuse them identically -- this pins that specific
// claim with distinguishable sentinel values per tenant rather than
// asserting it by inference. Tenant A must never see tenant B's outcome,
// either by reading tenant B's own invoice id (RLS's lock+read 0-rows ->
// ErrNotFound, same mechanism as every other cross-tenant Get refusal) or
// by any other means.
func TestStoreGet_CrossTenantFiscalOutcomeIsolated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "M5-05-02 cross-tenant A")
	entityA := seedEntity(t, super, tenantA, "M5-05-02 cross-tenant A entity")
	invA := seedInvoice(t, super, tenantA, entityA, "M5-05-02-XTEN-A")

	tenantB := seedTenant(t, super, "M5-05-02 cross-tenant B")
	entityB := seedEntity(t, super, tenantB, "M5-05-02 cross-tenant B entity")
	invB := seedInvoice(t, super, tenantB, entityB, "M5-05-02-XTEN-B")

	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"TENANT-A-IRN", "TENANT-A-CSID", "TENANT-A-QR", `[{"code":"TENANT-A-CODE","message":"a","path":"x"}]`, invA,
	); err != nil {
		t.Fatalf("seed tenant A outcome: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"TENANT-B-IRN", "TENANT-B-CSID", "TENANT-B-QR", `[{"code":"TENANT-B-CODE","message":"b","path":"y"}]`, invB,
	); err != nil {
		t.Fatalf("seed tenant B outcome: %v", err)
	}

	store := NewStore(app)
	asTenantA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	// Tenant A reading its OWN invoice sees only its own outcome.
	got, err := store.Get(asTenantA, invA)
	if err != nil {
		t.Fatalf("Get(own invoice): %v", err)
	}
	b, _ := json.Marshal(got)
	body := string(b)
	if !strings.Contains(body, `"irn":"TENANT-A-IRN"`) {
		t.Errorf("tenant A's own Get = %s, want it to contain its own irn", body)
	}
	if strings.Contains(body, "TENANT-B") {
		t.Errorf("tenant A's own Get = %s, must NEVER contain any tenant B sentinel value", body)
	}

	// Tenant A reading tenant B's invoice id must be refused entirely --
	// not merely have the outcome fields blanked out.
	if _, err := store.Get(asTenantA, invB); err != ErrNotFound {
		t.Errorf("Get(tenant B's invoice id) err = %v, want ErrNotFound -- RLS must refuse the whole row, the new outcome columns must not create a side channel", err)
	}
}

// TestStoreList_CrossTenantFiscalOutcomeIsolated: the List surface variant
// of the Get test above -- tenant A's List must never surface tenant B's
// row or any of tenant B's outcome values, over the same RLS mechanism
// List shares with Get (store.go's WithinRequestTenantTx).
func TestStoreList_CrossTenantFiscalOutcomeIsolated(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "M5-05-02 list cross-tenant A")
	entityA := seedEntity(t, super, tenantA, "M5-05-02 list cross-tenant A entity")
	invA := seedInvoice(t, super, tenantA, entityA, "M5-05-02-XTEN-LIST-A")

	tenantB := seedTenant(t, super, "M5-05-02 list cross-tenant B")
	entityB := seedEntity(t, super, tenantB, "M5-05-02 list cross-tenant B entity")
	invB := seedInvoice(t, super, tenantB, entityB, "M5-05-02-XTEN-LIST-B")

	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"LIST-TENANT-A-IRN", "LIST-TENANT-A-CSID", "LIST-TENANT-A-QR", `[{"code":"LIST-TENANT-A-CODE","message":"a","path":"x"}]`, invA,
	); err != nil {
		t.Fatalf("seed tenant A outcome: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"LIST-TENANT-B-IRN", "LIST-TENANT-B-CSID", "LIST-TENANT-B-QR", `[{"code":"LIST-TENANT-B-CODE","message":"b","path":"y"}]`, invB,
	); err != nil {
		t.Fatalf("seed tenant B outcome: %v", err)
	}

	store := NewStore(app)
	asTenantA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	items, _, err := store.List(asTenantA, ListFilter{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, inv := range items {
		if inv.ID == invB {
			t.Fatalf("tenant A's List returned tenant B's invoice %q -- RLS leak", invB)
		}
	}
	b, _ := json.Marshal(items)
	if strings.Contains(string(b), "LIST-TENANT-B") {
		t.Errorf("tenant A's marshalled List = %s, must NEVER contain any tenant B sentinel value", string(b))
	}
}

// TestStoreList_MixedOutcomesNoBleedBetweenRows (AC#3/#5): three invoices in
// the SAME tenant -- two with distinct, distinguishable outcomes and one
// with none -- must each carry exactly their own values on the List
// surface, with zero cross-contamination between rows (a scan-buffer reuse
// bug, or a query missing a WHERE/ORDER BY that mis-paired rows, would show
// up here as one row's value appearing on a sibling).
func TestStoreList_MixedOutcomesNoBleedBetweenRows(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M5-05-02 mixed-outcomes tenant")
	entityID := seedEntity(t, super, tenantID, "M5-05-02 mixed-outcomes entity")

	invA := seedInvoice(t, super, tenantID, entityID, "M5-05-02-MIX-A")
	invB := seedInvoice(t, super, tenantID, entityID, "M5-05-02-MIX-B")
	invC := seedInvoice(t, super, tenantID, entityID, "M5-05-02-MIX-C") // deliberately left with defaults

	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"MIX-A-IRN", "MIX-A-CSID", "MIX-A-QR", `[{"code":"MIX-A-CODE","message":"a","path":"x"}]`, invA,
	); err != nil {
		t.Fatalf("seed invoice A outcome: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET irn = $1, csid = $2, qr_payload = $3, rejection_reasons = $4::jsonb WHERE id = $5`,
		"MIX-B-IRN", "MIX-B-CSID", "MIX-B-QR", `[{"code":"MIX-B-CODE","message":"b","path":"y"}]`, invB,
	); err != nil {
		t.Fatalf("seed invoice B outcome: %v", err)
	}

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	items, _, err := store.List(c, ListFilter{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := map[string]Invoice{}
	for _, inv := range items {
		byID[inv.ID] = inv
	}
	for _, id := range []string{invA, invB, invC} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("List did not return seeded invoice %q", id)
		}
	}

	gotA, gotB, gotC := byID[invA], byID[invB], byID[invC]

	if gotA.IRN == nil || *gotA.IRN != "MIX-A-IRN" || gotA.CSID == nil || *gotA.CSID != "MIX-A-CSID" || gotA.QRPayload == nil || *gotA.QRPayload != "MIX-A-QR" {
		t.Errorf("invoice A's own fields = irn=%v csid=%v qr=%v, want MIX-A-IRN/MIX-A-CSID/MIX-A-QR exactly", derefOrNil(gotA.IRN), derefOrNil(gotA.CSID), derefOrNil(gotA.QRPayload))
	}
	if !strings.Contains(string(gotA.RejectionReasons), "MIX-A-CODE") || strings.Contains(string(gotA.RejectionReasons), "MIX-B-CODE") {
		t.Errorf("invoice A's rejection_reasons = %s, want only MIX-A-CODE, never MIX-B-CODE", string(gotA.RejectionReasons))
	}

	if gotB.IRN == nil || *gotB.IRN != "MIX-B-IRN" || gotB.CSID == nil || *gotB.CSID != "MIX-B-CSID" || gotB.QRPayload == nil || *gotB.QRPayload != "MIX-B-QR" {
		t.Errorf("invoice B's own fields = irn=%v csid=%v qr=%v, want MIX-B-IRN/MIX-B-CSID/MIX-B-QR exactly", derefOrNil(gotB.IRN), derefOrNil(gotB.CSID), derefOrNil(gotB.QRPayload))
	}
	if !strings.Contains(string(gotB.RejectionReasons), "MIX-B-CODE") || strings.Contains(string(gotB.RejectionReasons), "MIX-A-CODE") {
		t.Errorf("invoice B's rejection_reasons = %s, want only MIX-B-CODE, never MIX-A-CODE", string(gotB.RejectionReasons))
	}

	if gotC.IRN != nil || gotC.CSID != nil || gotC.QRPayload != nil {
		t.Errorf("invoice C (never touched) fields = irn=%v csid=%v qr=%v, want all three nil -- it must not inherit A's or B's values", derefOrNil(gotC.IRN), derefOrNil(gotC.CSID), derefOrNil(gotC.QRPayload))
	}
	if string(gotC.RejectionReasons) != "[]" {
		t.Errorf("invoice C (never touched) rejection_reasons = %s, want the literal default \"[]\", no bleed from A or B", string(gotC.RejectionReasons))
	}
}

// derefOrNil renders a *string for a t.Errorf %v without a nil-pointer dodge
// -- "<nil>" for nil, the pointed-to value otherwise. Test-only helper local
// to this file's no-bleed assertions above (named to avoid colliding with
// store_test.go's strPtr(string) *string, the opposite direction).
func derefOrNil(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// reasonWire mirrors internal/submission.Reason's wire shape (code/message/
// path) for decode-and-compare in
// TestGetHandler_RealStore_RejectionReasonsOrderAndContentPreservedVerbatim
// below -- a package-local copy rather than importing internal/submission,
// since this file only needs the JSON shape, not the type identity.
type reasonWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
}

// TestGetHandler_RealStore_RejectionReasonsOrderAndContentPreservedVerbatim
// (AC#5, QA Mode B): the Test Specs table's own verbatim assertions
// (TestGetHandler_RealStore_SeededOutcomeRendersVerbatim et al.) only
// strings.Contains for each element's "code" substring -- that cannot
// distinguish true verbatim round-tripping from "the same codes present in
// some other order" or "message/path silently dropped". This test seeds a
// THREE-element array in deliberately non-alphabetical code order (so an
// accidental ORDER-BY-code reshuffle would be caught) and decodes the real
// HTTP response's rejection_reasons key into a typed slice, asserting exact
// index-by-index equality of code+message+path -- the strongest verbatim
// guard the DB-to-wire path has.
func TestGetHandler_RealStore_RejectionReasonsOrderAndContentPreservedVerbatim(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "M5-05-02-order tenant")
	entityID := seedEntity(t, super, tenantID, "M5-05-02-order entity")
	store := NewStore(app)

	invoiceID := seedInvoice(t, super, tenantID, entityID, "M5-05-02-ORDER")
	want := []reasonWire{
		{Code: "ZULU", Message: "third alphabetically, first in the array", Path: "z_field"},
		{Code: "ALPHA", Message: "first alphabetically, second in the array", Path: "a_field"},
		{Code: "MIKE", Message: "middle alphabetically, third in the array", Path: "m_field"},
	}
	reasonsJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal seed reasons: %v", err)
	}
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`,
		string(reasonsJSON), invoiceID,
	); err != nil {
		t.Fatalf("seed rejection_reasons: %v", err)
	}

	identity := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID}
	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID, nil)
	r.SetPathValue("id", invoiceID)
	r = r.WithContext(auth.WithIdentity(ctx, identity))
	rec := httptest.NewRecorder()

	GetHandler(store.Get, nil).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response into raw top-level key map: %v (body=%s)", err, rec.Body.String())
	}
	rrBytes, ok := raw["rejection_reasons"]
	if !ok {
		t.Fatalf("response missing rejection_reasons key entirely (body=%s)", rec.Body.String())
	}
	var got []reasonWire
	if err := json.Unmarshal(rrBytes, &got); err != nil {
		t.Fatalf("decode rejection_reasons into []reasonWire: %v (raw=%s)", err, string(rrBytes))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rejection_reasons round-tripped as %+v, want exactly %+v (order and every field, verbatim) -- AC#5", got, want)
	}
}
