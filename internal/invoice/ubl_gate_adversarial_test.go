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

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
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

			// The endpoint must agree, or the SPA's tooltip and the download
			// would disagree on whitespace exactly where trimming happens.
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
