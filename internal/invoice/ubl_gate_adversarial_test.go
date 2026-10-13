package invoice

// BUG-04-03 QA (task-399): adversarial coverage the AC rows do not reach --
// wire-byte identity across the two endpoints, whitespace-only content, the
// no-extra-query claim, staleness after an edit, and cross-tenant leakage.
// Fixtures and helpers come from ubl_test.go / handlers_test.go (same package).

import (
	"context"
	"encoding/json"
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

// ublRawReason returns a top-level key's RAW JSON bytes -- escaping intact.
// json.RawMessage, never a decoded string: two endpoints can decode to the
// same Go string while emitting different bytes (— vs literal U+2014).
func ublRawReason(t *testing.T, rec *httptest.ResponseRecorder, key string) string {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	v, ok := raw[key]
	if !ok {
		t.Fatalf("body = %s, want a %q key", rec.Body.String(), key)
	}
	return string(v)
}

// TestUBLGate_ReasonBytesAreIdenticalOnBothEndpoints: the /ubl 409's error
// value and the detail payload's ubl_blocked_reason must be the same WIRE
// BYTES, not merely the same decoded string, and must carry a literal
// unescaped em dash. Guards a future encoder split between writeError and
// writeJSON that decoded-string equality cannot see.
func TestUBLGate_ReasonBytesAreIdenticalOnBothEndpoints(t *testing.T) {
	noCurrency := completeUBLInvoice(t, "INV-QA-BYTES-NOCCY")
	noCurrency.Currency = nil

	tests := []struct {
		name string
		inv  Invoice
	}{
		{"missing_lines", ublInvoiceMissingLines(t, "INV-QA-BYTES-NOLINES")},
		{"missing_currency", noCurrency},
		{"all_six_gaps", ublInvoiceAllSixGaps(t, StatusDraft)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := ublTestIdentity()

			getRec, _ := doInvoiceGet(t, ublGetOK(tt.inv), &id, tt.inv.ID)
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET status = %d, want 200 (body=%s)", getRec.Code, getRec.Body.String())
			}
			payload := ublRawReason(t, getRec, "ubl_blocked_reason")

			ublRec := doUBL(t, ublGetOK(tt.inv), &id, tt.inv.ID)
			if ublRec.Code != http.StatusConflict {
				t.Fatalf("GET /ubl status = %d, want 409 (body=%s)", ublRec.Code, ublRec.Body.String())
			}
			route := ublRawReason(t, ublRec, "error")

			if payload == "null" {
				t.Fatalf("ubl_blocked_reason = null on an invoice the /ubl route refuses with 409")
			}
			if payload != route {
				t.Errorf("raw ubl_blocked_reason = %s but the /ubl 409 error = %s -- the two must be byte-identical on the wire", payload, route)
			}
			if !strings.Contains(payload, "—") {
				t.Errorf("raw ubl_blocked_reason = %s, want a literal unescaped em dash (U+2014), not an escape or a hyphen", payload)
			}
		})
	}
}

// TestGetHandler_UBLGateTreatsWhitespaceOnlyFieldsAsMissing: ubl.Missing trims
// (blank/TrimSpace), so a field holding only spaces or tabs is a GAP. Every
// fixture in the AC rows is either fully populated or nil, so none of them can
// see a gate that switched to a bare == "" or a nil check.
func TestGetHandler_UBLGateTreatsWhitespaceOnlyFieldsAsMissing(t *testing.T) {
	tests := []struct {
		name    string
		blank   func(*Invoice)
		wantGap string
	}{
		{"invoice_number_spaces", func(i *Invoice) { i.InvoiceNumber = "   " }, "an invoice number"},
		{"currency_tab", func(i *Invoice) { i.Currency = ublStr("\t") }, "a currency"},
		{"supplier_name_spaces", func(i *Invoice) { i.SupplierName = ublStr("   ") }, "a supplier name"},
		{"buyer_name_newline", func(i *Invoice) { i.BuyerName = ublStr(" \n ") }, "a buyer name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := completeUBLInvoice(t, "INV-QA-WS")
			tt.blank(&inv)

			// Floor: exactly the one gap under test, so the assertions below
			// cannot pass off a different gap as this one.
			if got := ubl.Missing(SubmissionCanonical(inv)); len(got) != 1 || got[0] != tt.wantGap {
				t.Fatalf("fixture gaps = %v, want exactly [%s]", got, tt.wantGap)
			}
			wantReason := "This invoice cannot be rendered as a UBL document — it is missing " + tt.wantGap + "."

			id := ublTestIdentity()
			getRec, _ := doInvoiceGet(t, ublGetOK(inv), &id, inv.ID)
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET status = %d, want 200 (body=%s)", getRec.Code, getRec.Body.String())
			}
			canView, reasonRaw := ublWireKeys(t, getRec)
			if canView != "false" {
				t.Errorf("can_view_ubl raw = %q, want false -- whitespace-only content is not renderable", canView)
			}
			got, ok := ublReasonValue(t, reasonRaw)
			if !ok || got != wantReason {
				t.Errorf("ubl_blocked_reason = %q (raw %s), want %q", got, reasonRaw, wantReason)
			}

			// The endpoint must agree, or the UBL card's printed refusal and
			// the download would disagree on whitespace exactly where trimming happens.
			ublRec := doUBL(t, ublGetOK(inv), &id, inv.ID)
			if ublRec.Code != http.StatusConflict {
				t.Errorf("GET /ubl status = %d, want 409 (body=%s)", ublRec.Code, ublRec.Body.String())
			}
		})
	}
}

// TestGetHandler_UBLGateAddsNoStoreCall: the gate is pure in-memory work over
// the invoice already fetched. Exactly ONE fetch per detail request, on both
// the renderable and the blocked path.
func TestGetHandler_UBLGateAddsNoStoreCall(t *testing.T) {
	tests := []struct {
		name        string
		inv         Invoice
		wantCanView string
	}{
		{"renderable", completeUBLInvoice(t, "INV-QA-ONECALL-OK"), "true"},
		{"blocked", ublInvoiceMissingLines(t, "INV-QA-ONECALL-BAD"), "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			get := func(ctx context.Context, id string) (Invoice, error) {
				calls++
				return tt.inv, nil
			}
			id := ublTestIdentity()
			rec, _ := doInvoiceGet(t, get, &id, tt.inv.ID)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			// Non-vacuity: the gate must actually have run.
			if canView, _ := ublWireKeys(t, rec); canView != tt.wantCanView {
				t.Fatalf("can_view_ubl raw = %q, want %q", canView, tt.wantCanView)
			}
			if calls != 1 {
				t.Errorf("store fetched %d times, want exactly 1 -- the UBL gate reads the already-fetched invoice, it must not re-query", calls)
			}
		})
	}
}

// TestRLS_GetHandlerUBLGateAddsNoDatabaseRoundTrip: the in-memory claim,
// MEASURED rather than asserted. A full GetHandler request must acquire the
// same number of pool connections as a bare Store.Get -- so the gate adds no
// round trip of its own.
func TestRLS_GetHandlerUBLGateAddsNoDatabaseRoundTrip(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-04-03 QA cost tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-04-03 QA cost entity")
	store := NewStore(app)
	identity := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	tenantCtx := auth.WithIdentity(ctx, identity)

	issued := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	inv, err := store.Create(tenantCtx, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "BUG-04-03-QA-COST",
		IssueDate:     &issued,
		BuyerName:     ublStr("Beta Buyers Ltd"),
		Currency:      ublStr("NGN"),
		LineItems: []LineItemInput{{
			Description: ublStr("Widget"), Quantity: ublStr("2"),
			UnitPrice: ublStr("50.00"), LineTotal: ublStr("100.00"), LineTax: ublStr("7.50"),
		}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	before := app.Stat().AcquireCount()
	if _, err := store.Get(tenantCtx, inv.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	bare := app.Stat().AcquireCount() - before
	if bare == 0 {
		t.Fatalf("a bare Store.Get acquired 0 connections -- the measurement is not observing anything")
	}

	before = app.Stat().AcquireCount()
	rec, _ := doInvoiceGet(t, store.Get, &identity, inv.ID)
	viaHandler := app.Stat().AcquireCount() - before

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if canView, _ := ublWireKeys(t, rec); canView != "true" {
		t.Fatalf("can_view_ubl raw = %q, want true -- the gate must have run for this measurement to mean anything", canView)
	}
	if viaHandler != bare {
		t.Errorf("GetHandler acquired %d connections vs %d for a bare Store.Get -- the UBL gate must add no database round trip", viaHandler, bare)
	}
}

// TestRLS_GetHandlerUBLGateFlipsAfterAnEdit: nothing caches the detail
// payload, so an edit that removes required content flips can_view_ubl on the
// very next read -- and restoring it flips back. This is what makes the SPA's
// discard-the-action-response-and-re-read pattern (InvoiceDetail.tsx) safe.
func TestRLS_GetHandlerUBLGateFlipsAfterAnEdit(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "BUG-04-03 QA edit tenant")
	entityID := seedEntity(t, super, tenantID, "BUG-04-03 QA edit entity")
	store := NewStore(app)
	identity := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}
	tenantCtx := auth.WithIdentity(ctx, identity)

	line := LineItemInput{
		Description: ublStr("Widget"), Quantity: ublStr("2"),
		UnitPrice: ublStr("50.00"), LineTotal: ublStr("100.00"), LineTax: ublStr("7.50"),
	}
	issued := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	inv, err := store.Create(tenantCtx, CreateInput{
		EntityID:      entityID,
		InvoiceNumber: "BUG-04-03-QA-EDIT",
		IssueDate:     &issued,
		BuyerName:     ublStr("Beta Buyers Ltd"),
		Currency:      ublStr("NGN"),
		LineItems:     []LineItemInput{line},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	read := func(t *testing.T, when string) (canView, reason string) {
		t.Helper()
		rec, _ := doInvoiceGet(t, store.Get, &identity, inv.ID)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body=%s)", when, rec.Code, rec.Body.String())
		}
		return ublWireKeys(t, rec)
	}

	if canView, reason := read(t, "before the edit"); canView != "true" || reason != "null" {
		t.Fatalf("before the edit: can_view_ubl = %s, ubl_blocked_reason = %s, want true/null", canView, reason)
	}

	// A present-but-empty slice removes every line ([line-items-optional]).
	if _, err := store.Edit(tenantCtx, inv.ID, EditInput{LineItems: &[]LineItemInput{}}); err != nil {
		t.Fatalf("Edit(remove lines): %v", err)
	}
	canView, reasonRaw := read(t, "after removing the lines")
	if canView != "false" {
		t.Errorf("after removing the lines: can_view_ubl = %s, want false -- a stale gate would still say true", canView)
	}
	if got, ok := ublReasonValue(t, reasonRaw); !ok || got != ublReasonMissingLines {
		t.Errorf("after removing the lines: ubl_blocked_reason = %q (raw %s), want %q", got, reasonRaw, ublReasonMissingLines)
	}

	if _, err := store.Edit(tenantCtx, inv.ID, EditInput{LineItems: &[]LineItemInput{line}}); err != nil {
		t.Fatalf("Edit(restore lines): %v", err)
	}
	if canView, reason := read(t, "after restoring the lines"); canView != "true" || reason != "null" {
		t.Errorf("after restoring the lines: can_view_ubl = %s, ubl_blocked_reason = %s, want true/null", canView, reason)
	}
}

// TestRLS_GetHandlerCrossTenantUBLKeysNotLeaked: tenant B holds a RENDERABLE
// invoice (can_view_ubl:true for B). Tenant A asking for the same id gets a
// 404 whose body carries neither key -- the gate's verdict must not be
// observable across tenants, true or false. Mirrors
// TestGetHandler_RealStore_CrossTenantCanSubmitNotLeaked.
func TestRLS_GetHandlerCrossTenantUBLKeysNotLeaked(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "BUG-04-03 QA cross tenant A")
	tenantB := seedTenant(t, super, "BUG-04-03 QA cross tenant B")
	entityB := seedEntity(t, super, tenantB, "BUG-04-03 QA cross B entity")
	store := NewStore(app)

	identityB := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB}
	ctxB := auth.WithIdentity(ctx, identityB)
	issued := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	invB, err := store.Create(ctxB, CreateInput{
		EntityID:      entityB,
		InvoiceNumber: "BUG-04-03-QA-CROSS-B",
		IssueDate:     &issued,
		BuyerName:     ublStr("Beta Buyers Ltd"),
		Currency:      ublStr("NGN"),
		LineItems: []LineItemInput{{
			Description: ublStr("Widget"), Quantity: ublStr("2"),
			UnitPrice: ublStr("50.00"), LineTotal: ublStr("100.00"), LineTax: ublStr("7.50"),
		}},
	})
	if err != nil {
		t.Fatalf("Create(tenant B): %v", err)
	}

	// Non-vacuity: the invoice really is renderable for its OWNER, so a 404
	// for tenant A is RLS, not an unrenderable fixture.
	recB, _ := doInvoiceGet(t, store.Get, &identityB, invB.ID)
	if canView, _ := ublWireKeys(t, recB); canView != "true" {
		t.Fatalf("tenant B's own can_view_ubl raw = %q, want true (body=%s)", canView, recB.Body.String())
	}

	identityA := auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA}
	r := httptest.NewRequest("GET", "/v1/invoices/"+invB.ID, nil)
	r.SetPathValue("id", invB.ID)
	r = r.WithContext(auth.WithIdentity(ctx, identityA))
	recA := httptest.NewRecorder()
	GetHandler(store.Get, store.CallerRole, clearApprovalStub, nil).ServeHTTP(recA, r)

	if recA.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (tenant A must not see tenant B's invoice) (body=%s)", recA.Code, recA.Body.String())
	}
	for _, k := range []string{"can_view_ubl", "ubl_blocked_reason"} {
		if strings.Contains(recA.Body.String(), k) {
			t.Errorf("body = %s, %s must never leak across tenants, in any form", recA.Body.String(), k)
		}
	}
}

const (
	gateNoVAT    = "The invoice has tax categories but no VAT total."
	gateMismatch = "The VAT total does not equal the sum of the tax categories."
)

func gateWithSubtotals(t *testing.T, vat *string, taxAmounts ...*string) submission.Canonical {
	t.Helper()
	c := SubmissionCanonical(completeUBLInvoice(t, "INV-GATE-CAT"))
	c.VAT = vat
	for _, a := range taxAmounts {
		c.TaxSubtotals = append(c.TaxSubtotals, submission.TaxSubtotal{Category: "STANDARD_VAT", TaxAmount: a})
	}
	return c
}

func TestUBLGate_RefusesCategoriesWithoutVAT(t *testing.T) {
	ok, reason := ublGate(gateWithSubtotals(t, nil, ublStr("7.50")))
	if ok || reason == nil || *reason != gateNoVAT {
		t.Errorf("ublGate = %v, %v; want false, %q", ok, reason, gateNoVAT)
	}
}

func TestUBLGate_RefusesVATThatDiffersFromTheCategories(t *testing.T) {
	cases := []struct {
		name string
		c    submission.Canonical
		ok   bool
	}{
		{"differs", gateWithSubtotals(t, ublStr("75.00"), ublStr("40.00"), ublStr("30.00")), false},
		{"equal by value not text", gateWithSubtotals(t, ublStr("75.0"), ublStr("45.00"), ublStr("30.00")), true},
		{"nil tax amount", gateWithSubtotals(t, ublStr("75.00"), ublStr("75.00"), nil), false},
		{"non-decimal subtotal", gateWithSubtotals(t, ublStr("75.00"), ublStr("abc")), false},
		{"non-decimal vat", gateWithSubtotals(t, ublStr("abc"), ublStr("75.00")), false},
		{"exponent text compares by value", gateWithSubtotals(t, ublStr("75.00"), ublStr("7.5e1")), true},
		{"non-decimal subtotal among valid ones", gateWithSubtotals(t, ublStr("75.00"), ublStr("75.00"), ublStr("abc")), false},
		{"non-decimal vat with zero subtotal", gateWithSubtotals(t, ublStr("abc"), ublStr("0.00")), false},
		{"empty vat", gateWithSubtotals(t, ublStr(""), ublStr("0.00")), false},
		{"empty subtotal amount", gateWithSubtotals(t, ublStr("75.00"), ublStr("75.00"), ublStr("")), false},
		{"padded vat is not a decimal", gateWithSubtotals(t, ublStr(" 75.00"), ublStr("75.00")), false},
		{"nan vat", gateWithSubtotals(t, ublStr("NaN"), ublStr("0.00")), false},
		{"negative equal", gateWithSubtotals(t, ublStr("-5.00"), ublStr("-2.00"), ublStr("-3.00")), true},
		{"sign differs", gateWithSubtotals(t, ublStr("-5.00"), ublStr("5.00")), false},
		{"negative zero equals zero", gateWithSubtotals(t, ublStr("0"), ublStr("-0.00")), true},
		{"decimal not float", gateWithSubtotals(t, ublStr("0.30"), ublStr("0.10"), ublStr("0.20")), true},
		{"one cent apart", gateWithSubtotals(t, ublStr("75.00"), ublStr("74.99")), false},
		{"sub-cent difference", gateWithSubtotals(t, ublStr("75.004"), ublStr("75.00")), false},
		{"nil first then matching sum", gateWithSubtotals(t, ublStr("75.00"), nil, ublStr("75.00")), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := ublGate(tc.c)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (reason %v)", ok, tc.ok, reason)
			}
			if !ok && (reason == nil || *reason != gateMismatch) {
				t.Errorf("reason = %v, want %q", reason, gateMismatch)
			}
			if ok && reason != nil {
				t.Errorf("reason = %q on an open gate", *reason)
			}
		})
	}
}

func TestUBLGate_InvoiceWithoutCategoriesIsUnaffected(t *testing.T) {
	c := SubmissionCanonical(completeUBLInvoice(t, "INV-GATE-NOCAT"))
	c.VAT = nil
	if ok, reason := ublGate(c); !ok || reason != nil {
		t.Errorf("ublGate = %v, %v; want true, nil", ok, reason)
	}
}

func TestUBLGate_MissingContentOutranksTheCategoryRefusals(t *testing.T) {
	c := gateWithSubtotals(t, nil, ublStr("7.50"))
	c.Currency = nil
	ok, reason := ublGate(c)
	want := "This invoice cannot be rendered as a UBL document — it is missing a currency."
	if ok || reason == nil || *reason != want {
		t.Errorf("ublGate = %v, %v; want false, %q", ok, reason, want)
	}
}

// categorisedInvoice: two categorised lines, tax 5.00 + 2.50, so the derived subtotals sum to 7.50.
func categorisedInvoice(t *testing.T, number string, vat *string) Invoice {
	t.Helper()
	inv := completeUBLInvoice(t, number)
	inv.VAT = vat
	inv.LineItems = []LineItem{
		{ID: uuid.NewString(), LineNo: 1, Description: ublStr("A"), Quantity: ublStr("1"), UnitPrice: ublStr("50.00"),
			LineTotal: ublStr("50.00"), LineTax: ublStr("5.00"), TaxCategory: ublStr("STANDARD_VAT"), TaxPercent: ublStr("10.00")},
		{ID: uuid.NewString(), LineNo: 2, Description: ublStr("B"), Quantity: ublStr("1"), UnitPrice: ublStr("50.00"),
			LineTotal: ublStr("50.00"), LineTax: ublStr("2.50"), TaxCategory: ublStr("ZERO_VAT")},
	}
	return inv
}

func TestUBLHandler_CategoryRefusalsAre409AndTheDetailPayloadAgrees(t *testing.T) {
	noLineTax := categorisedInvoice(t, "INV-CAT-NOTAX", ublStr("7.50"))
	noLineTax.LineItems[1].LineTax = nil
	uncategorised := categorisedInvoice(t, "INV-CAT-PARTIAL", ublStr("8.50"))
	uncategorised.LineItems = append(uncategorised.LineItems, LineItem{
		ID: uuid.NewString(), LineNo: 3, Description: ublStr("C"), LineTax: ublStr("1.00")})

	tests := []struct {
		name string
		inv  Invoice
		want string
	}{
		{"vat_null", categorisedInvoice(t, "INV-CAT-NOVAT", nil), gateNoVAT},
		{"vat_differs", categorisedInvoice(t, "INV-CAT-DIFF", ublStr("8.00")), gateMismatch},
		{"line_tax_null", noLineTax, gateMismatch},
		{"vat_counts_an_uncategorised_line_the_subtotals_omit", uncategorised, gateMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := ublTestIdentity()
			rec := doUBL(t, ublGetOK(tt.inv), &id, tt.inv.ID)
			if rec.Code != http.StatusConflict {
				t.Fatalf("/ubl status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
			}
			if got := ublErrorValue(t, rec); got != tt.want {
				t.Errorf("/ubl error = %q, want %q", got, tt.want)
			}
			if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "xml") || rec.Header().Get("Content-Disposition") != "" {
				t.Errorf("a refusal carried XML headers: %q", ct)
			}

			getRec, _ := doInvoiceGet(t, ublGetOK(tt.inv), &id, tt.inv.ID)
			canView, reasonRaw := ublWireKeys(t, getRec)
			if canView != "false" {
				t.Errorf("can_view_ubl = %s, want false", canView)
			}
			if got, ok := ublReasonValue(t, reasonRaw); !ok || got != tt.want {
				t.Errorf("ubl_blocked_reason = %q (present %v), want %q", got, ok, tt.want)
			}
			if payload, route := reasonRaw, ublRawReason(t, rec, "error"); payload != route {
				t.Errorf("wire bytes differ: payload %s, route %s", payload, route)
			}
		})
	}
}

func TestUBLHandler_AgreeingCategoriesServeTheSubtotalsAndOpenTheGate(t *testing.T) {
	inv := categorisedInvoice(t, "INV-CAT-OK", ublStr("7.5"))
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ubl status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if n := strings.Count(body, "<cac:TaxSubtotal>"); n != 2 {
		t.Errorf("%d cac:TaxSubtotal, want 2", n)
	}
	for _, s := range []string{"<cbc:ID>STANDARD_VAT</cbc:ID>", "<cbc:ID>ZERO_VAT</cbc:ID>", `<cbc:TaxableAmount currencyID="NGN">50.00</cbc:TaxableAmount>`} {
		if !strings.Contains(body, s) {
			t.Errorf("document lacks %s", s)
		}
	}
	getRec, _ := doInvoiceGet(t, ublGetOK(inv), &id, inv.ID)
	if canView, reason := ublWireKeys(t, getRec); canView != "true" || reason != "null" {
		t.Errorf("can_view_ubl = %s, ubl_blocked_reason = %s; want true, null", canView, reason)
	}
}

func TestUBLHandler_NoCategoriesAndNullVATStillServes(t *testing.T) {
	inv := completeUBLInvoice(t, "INV-NOCAT-NOVAT")
	inv.VAT = nil
	id := ublTestIdentity()
	rec := doUBL(t, ublGetOK(inv), &id, inv.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ubl status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<cac:TaxSubtotal>") {
		t.Error("a TaxSubtotal rendered for an uncategorised invoice")
	}
}
