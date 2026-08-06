// ubl_test.go: the acceptance-criteria specs for GET /v1/invoices/{id}/ubl.
// package invoice (internal): ublBlockedReason is unexported and
// dbTestPools/seedTenant/seedEntity live in store_test.go. Adversarial and
// edge coverage lives in ubl_adversarial_test.go.
package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/ubl"
)

// doUBL mirrors doSourceDocument's shape for the new route.
func doUBL(t *testing.T, get func(ctx context.Context, id string) (Invoice, error), id *auth.Identity, invoiceID string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/invoices/"+invoiceID+"/ubl", nil)
	r.SetPathValue("id", invoiceID)
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	UBLHandler(get, nil).ServeHTTP(rec, r)
	return rec
}

func ublTestIdentity() auth.Identity {
	return auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: uuid.NewString()}
}

func ublStr(v string) *string { return &v }

// completeUBLInvoice is a renderable fixture. The ubl.Missing floor is
// load-bearing: an incomplete fixture would make every 200 row assert against
// a 409 instead.
func completeUBLInvoice(t *testing.T, number string) Invoice {
	t.Helper()
	issued := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	inv := Invoice{
		ID:            uuid.NewString(),
		EntityID:      uuid.NewString(),
		InvoiceNumber: number,
		Status:        StatusValidated,
		IssueDate:     &issued,
		SupplierTIN:   ublStr("12345678-0001"),
		SupplierName:  ublStr("Acme Supplies Ltd"),
		BuyerTIN:      ublStr("87654321-0001"),
		BuyerName:     ublStr("Beta Buyers Ltd"),
		Currency:      ublStr("NGN"),
		Subtotal:      ublStr("100.00"),
		VAT:           ublStr("7.50"),
		Total:         ublStr("107.50"),
		LineItems: []LineItem{{
			ID:          uuid.NewString(),
			LineNo:      1,
			Description: ublStr("Widget"),
			Quantity:    ublStr("2"),
			UnitPrice:   ublStr("50.00"),
			LineTotal:   ublStr("100.00"),
			LineTax:     ublStr("7.50"),
		}},
	}
	if m := ubl.Missing(SubmissionCanonical(inv)); len(m) > 0 {
		t.Fatalf("fixture is not renderable: ubl.Missing = %v, want empty", m)
	}
	return inv
}

// ublInvoiceMissingLines is complete except for its line items -- exactly one gap.
func ublInvoiceMissingLines(t *testing.T, number string) Invoice {
	t.Helper()
	inv := completeUBLInvoice(t, number)
	inv.LineItems = nil
	got := ubl.Missing(SubmissionCanonical(inv))
	if len(got) != 1 || got[0] != "at least one line item" {
		t.Fatalf("fixture gaps = %v, want exactly [at least one line item]", got)
	}
	return inv
}

func ublGetOK(inv Invoice) func(ctx context.Context, id string) (Invoice, error) {
	return func(ctx context.Context, id string) (Invoice, error) { return inv, nil }
}

func ublGetErr(err error) func(ctx context.Context, id string) (Invoice, error) {
	return func(ctx context.Context, id string) (Invoice, error) { return Invoice{}, err }
}

// ublErrorValue decodes the shared {"error":"..."} envelope and asserts the
// key set is exactly {"error"}.
func ublErrorValue(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if len(body) != 1 {
		t.Fatalf("body = %s, want exactly one top-level key", rec.Body.String())
	}
	raw, ok := body["error"]
	if !ok {
		t.Fatalf("body = %s, want the sole key to be \"error\"", rec.Body.String())
	}
	var msg string
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decode error value: %v", err)
	}
	return msg
}

// --- AC #1: the 200 body and its headers -------------------------------------

// The response body is ubl.Render's bytes verbatim -- no re-marshal, no
// re-indent, no trailing newline.
func TestUBLHandler_200ServesRenderBytesUnmodified(t *testing.T) {
	inv := completeUBLInvoice(t, "INV-0001")
	want, err := ubl.Render(SubmissionCanonical(inv))
	if err != nil {
		t.Fatalf("ubl.Render (fixture): %v", err)
	}
	if len(want) == 0 || !bytes.Contains(want, []byte("<Invoice")) {
		t.Fatalf("fixture render = %q, want a non-empty UBL document (vacuity floor)", want)
	}

	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("body = %q, want ubl.Render's bytes unmodified (%q)", rec.Body.Bytes(), want)
	}
}

func TestUBLHandler_200SetsTheXMLContentType(t *testing.T) {
	inv := completeUBLInvoice(t, "INV-0001")
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "application/xml; charset=utf-8")
	}
}

func TestUBLHandler_200SetsNosniffAndAttachmentFilename(t *testing.T) {
	inv := completeUBLInvoice(t, "INV-0001")
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
	cd := rec.Header().Get("Content-Disposition")
	mediatype, params, err := mime.ParseMediaType(cd)
	if err != nil {
		t.Fatalf("ParseMediaType(%q) = %v, want a parseable header", cd, err)
	}
	if mediatype != "attachment" {
		t.Errorf("Content-Disposition type = %q, want %q", mediatype, "attachment")
	}
	if got := params["filename"]; got != "INV-0001.xml" {
		t.Errorf("filename = %q, want %q", got, "INV-0001.xml")
	}
}

// Both clauses are load-bearing: naive concatenation yields
// `attachment; filename="A"B.xml"`, which ParseMediaType rejects with
// `mime: invalid media parameter` AND an empty filename -- so asserting only
// the value would pass vacuously on the bug. mime.FormatMediaType escapes it.
func TestUBLHandler_FilenameIsQuotedNotConcatenated(t *testing.T) {
	inv := completeUBLInvoice(t, `A"B`)
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		t.Fatalf("ParseMediaType(%q) = %v, want err == nil", cd, err)
	}
	if got := params["filename"]; got != `A"B.xml` {
		t.Errorf("filename = %q, want %q", got, `A"B.xml`)
	}
}

// --- AC #2: 401 / 400 / 404 ---------------------------------------------------

func TestUBLHandler_NoIdentityIs401BeforeTheStoreCall(t *testing.T) {
	get := func(ctx context.Context, id string) (Invoice, error) {
		t.Fatal("the store closure must not run without an identity")
		return Invoice{}, nil
	}
	rec := doUBL(t, get, nil, uuid.NewString())

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if msg := ublErrorValue(t, rec); msg == "" {
		t.Error("error message is empty, want non-empty")
	}
}

// The message is the pre-existing "invoice: validation" that statusForErr
// leaks for ErrValidation (handlers.go:1116-1117) -- assert the status and the
// key set only, never a friendlier string.
func TestUBLHandler_MalformedIDIs400(t *testing.T) {
	id := ublTestIdentity()
	rec := doUBL(t, ublGetErr(ErrValidation), &id, "not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if msg := ublErrorValue(t, rec); msg == "" {
		t.Error("error message is empty, want non-empty")
	}
}

func TestUBLHandler_UnknownIDIs404(t *testing.T) {
	id := ublTestIdentity()
	rec := doUBL(t, ublGetErr(ErrNotFound), &id, uuid.NewString())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"not found"}` {
		t.Errorf("body = %q, want %q", got, `{"error":"not found"}`)
	}
}

// Cross-tenant and unknown-id are byte-identical 404s through the real
// Store.Get + handler -- no existence oracle. Gated on DATABASE_URL +
// DATABASE_SUPERUSER_URL via dbTestPools.
func TestRLS_UBLHandlerCrossTenantIs404AndIdenticalToUnknown(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "BUG-04-02 UBL tenant A")
	tenantB := seedTenant(t, super, "BUG-04-02 UBL tenant B")
	entityA := seedEntity(t, super, tenantA, "BUG-04-02 UBL entity A")

	store := NewStore(app)
	issued := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})
	invA, err := store.Create(cA, CreateInput{
		EntityID:      entityA,
		InvoiceNumber: "BUG-04-02-RLS",
		IssueDate:     &issued,
		BuyerName:     ublStr("Beta Buyers Ltd"),
		Currency:      ublStr("NGN"),
		Subtotal:      ublStr("100.00"),
		VAT:           ublStr("7.50"),
		Total:         ublStr("107.50"),
		LineItems: []LineItemInput{{
			Description: ublStr("Widget"),
			Quantity:    ublStr("2"),
			UnitPrice:   ublStr("50.00"),
			LineTotal:   ublStr("100.00"),
			LineTax:     ublStr("7.50"),
		}},
	})
	if err != nil {
		t.Fatalf("Create (as tenant A): %v", err)
	}

	// Non-vacuity floor: the row A owns is RENDERABLE, so B's 404 can only be
	// RLS -- not the 409 an incomplete fixture would have produced anyway.
	hydrated, err := store.Get(cA, invA.ID)
	if err != nil {
		t.Fatalf("Get (as tenant A): %v", err)
	}
	if m := ubl.Missing(SubmissionCanonical(hydrated)); len(m) > 0 {
		t.Fatalf("seeded invoice is not renderable: ubl.Missing = %v, want empty", m)
	}

	identityB := auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantB}
	recCross := doUBL(t, store.Get, &identityB, invA.ID)
	recUnknown := doUBL(t, store.Get, &identityB, uuid.NewString())

	if recCross.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant status = %d, want 404 (body=%s)", recCross.Code, recCross.Body.String())
	}
	if recUnknown.Code != http.StatusNotFound {
		t.Fatalf("unknown-id status = %d, want 404 (body=%s)", recUnknown.Code, recUnknown.Body.String())
	}
	bodyCross := recCross.Body.Bytes()
	bodyUnknown := recUnknown.Body.Bytes()
	if len(bodyCross) == 0 || len(bodyUnknown) == 0 {
		t.Fatalf("bodies must be non-empty: cross=%q unknown=%q", bodyCross, bodyUnknown)
	}
	if !bytes.Equal(bodyCross, bodyUnknown) {
		t.Errorf("cross-tenant body = %s, unknown-id body = %s, want byte-identical", bodyCross, bodyUnknown)
	}
}

// --- AC #3: the 409 and its reason copy ---------------------------------------

func TestUBLHandler_IncompleteInvoiceIs409WithTheReasonCopy(t *testing.T) {
	inv := ublInvoiceMissingLines(t, "INV-0409")
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	const want = "This invoice cannot be rendered as a UBL document — it is missing at least one line item."
	if got := ublErrorValue(t, rec); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// The wire copy and ublBlockedReason are the same string BY CONSTRUCTION --
// BUG-04-03 populates ubl_blocked_reason from this same function, so the
// cross-endpoint equality test lives there, not here.
func TestUBLHandler_409ReasonIsTheUblBlockedReasonString(t *testing.T) {
	inv := ublInvoiceMissingLines(t, "INV-0409")
	want := ublBlockedReason(ubl.Missing(SubmissionCanonical(inv)))
	if want == nil {
		t.Fatal("ublBlockedReason returned nil for an invoice with one gap, want a reason string")
	}

	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := ublErrorValue(t, rec); got != *want {
		t.Errorf("error = %q, want ublBlockedReason's own string %q", got, *want)
	}
}

func TestUBLBlockedReason_AllSixGapsMatchTheStoryCopy(t *testing.T) {
	missing := ubl.Missing(submission.Canonical{})
	if len(missing) != 6 {
		t.Fatalf("ubl.Missing(zero Canonical) = %v, want all six gaps", missing)
	}
	got := ublBlockedReason(missing)
	if got == nil {
		t.Fatal("ublBlockedReason = nil for six gaps, want a reason string")
	}
	const want = "This invoice cannot be rendered as a UBL document — it is missing an invoice number, an issue date, a currency, a supplier name, a buyer name and at least one line item."
	if *got != want {
		t.Errorf("reason = %q, want %q", *got, want)
	}
}

func TestUBLBlockedReason_JoinsThreeGapsWithCommasAndAnd(t *testing.T) {
	got := ublBlockedReason([]string{"an issue date", "a currency", "a buyer name"})
	if got == nil {
		t.Fatal("ublBlockedReason = nil for three gaps, want a reason string")
	}
	const want = "This invoice cannot be rendered as a UBL document — it is missing an issue date, a currency and a buyer name."
	if *got != want {
		t.Errorf("reason = %q, want %q", *got, want)
	}
}

// Two gaps is the boundary neither the one-gap nor the three-gap row covers:
// a naive strings.Join(all, ", ") passes those two and fails only here.
func TestUBLBlockedReason_TwoGapsJoinWithAndAndNoComma(t *testing.T) {
	got := ublBlockedReason([]string{"a currency", "a buyer name"})
	if got == nil {
		t.Fatal("ublBlockedReason = nil for two gaps, want a reason string")
	}
	const want = "This invoice cannot be rendered as a UBL document — it is missing a currency and a buyer name."
	if *got != want {
		t.Errorf("reason = %q, want %q", *got, want)
	}
	if strings.Contains(*got, ",") {
		t.Errorf("reason = %q, must contain no comma for two gaps", *got)
	}
}

func TestUBLBlockedReason_SingleGapHasNoConjunction(t *testing.T) {
	got := ublBlockedReason([]string{"at least one line item"})
	if got == nil {
		t.Fatal("ublBlockedReason = nil for one gap, want a reason string")
	}
	const want = "This invoice cannot be rendered as a UBL document — it is missing at least one line item."
	if *got != want {
		t.Errorf("reason = %q, want %q", *got, want)
	}
	if strings.Contains(*got, " and ") {
		t.Errorf("reason = %q, must contain no \" and \" for one gap", *got)
	}
	if strings.Contains(*got, ",") {
		t.Errorf("reason = %q, must contain no comma for one gap", *got)
	}
}

// --- AC #4: a refusal never ships a partial XML body --------------------------

// AC #4's only reachable half (ubl.Render is a package function, so no test can
// force its 500 arm): the XML headers are written strictly AFTER the render
// gate, so a 409 carries the JSON envelope and nothing XML.
func TestUBLHandler_409EmitsJSONHeadersAndNoXML(t *testing.T) {
	inv := ublInvoiceMissingLines(t, "INV-0409")
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q, want it absent on a refusal", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "" {
		t.Errorf("X-Content-Type-Options = %q, want it absent on a refusal", got)
	}
	body := rec.Body.Bytes()
	if bytes.Contains(body, []byte("<?xml")) || bytes.Contains(body, []byte("<Invoice")) {
		t.Errorf("body = %s, must contain no XML", body)
	}
}

// --- AC #5: the nil contract ---------------------------------------------------

func TestUBLBlockedReason_NilWhenNothingIsMissing(t *testing.T) {
	if got := ublBlockedReason(nil); got != nil {
		t.Errorf("ublBlockedReason(nil) = %q, want nil (never a pointer to \"\")", *got)
	}
	if got := ublBlockedReason([]string{}); got != nil {
		t.Errorf("ublBlockedReason([]string{}) = %q, want nil (never a pointer to \"\")", *got)
	}
}
