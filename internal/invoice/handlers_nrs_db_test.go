package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// nrsHTTP drives the real handlers over the real store, so the sentinel identity and the
// store's refusals cross the handler as they do in production.
type nrsHTTP struct {
	f nrsFixture
}

func (h nrsHTTP) serve(t *testing.T, ctx context.Context, method, path, id, body string, handler http.HandlerFunc) (int, map[string]json.RawMessage) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if id != "" {
		r.SetPathValue("id", id)
	}
	ident, _ := auth.IdentityFromContext(ctx)
	r = r.WithContext(auth.WithIdentity(r.Context(), ident))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("%s %s: decode %q: %v", method, path, rec.Body.String(), err)
	}
	return rec.Code, m
}

func (h nrsHTTP) post(t *testing.T, body string) (int, map[string]json.RawMessage) {
	return h.serve(t, h.f.c, http.MethodPost, "/v1/invoices", "", body, CreateHandler(h.f.store.Create, nil))
}

func (h nrsHTTP) patch(t *testing.T, ctx context.Context, id, body string) (int, map[string]json.RawMessage) {
	return h.serve(t, ctx, http.MethodPatch, "/v1/invoices/"+id, id, body, EditHandler(h.f.store.Edit, nil))
}

func (h nrsHTTP) get(t *testing.T, id string) map[string]json.RawMessage {
	t.Helper()
	code, m := h.serve(t, h.f.c, http.MethodGet, "/v1/invoices/"+id, id, "", GetHandler(h.f.store.Get, h.f.store.CallerRole, h.f.store.ApprovalFacts, nil))
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d", id, code)
	}
	return m
}

func str(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if string(raw) == "null" {
		return "<null>"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("not a string: %s", raw)
	}
	return s
}

func lineIDsOf(t *testing.T, inv map[string]json.RawMessage) []string {
	t.Helper()
	var lines []map[string]json.RawMessage
	if err := json.Unmarshal(inv["line_items"], &lines); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range lines {
		ids = append(ids, str(t, l["id"]))
	}
	return ids
}

// The wire to the store and back: POST sets the 31 keys, GET reads them with the derived
// subtotals, PATCH null clears only a new key, a line id carries the NRS line fields, and every
// foreign or malformed line id answers the same 400 and writes nothing.
func TestNRSInvoiceHTTP_CreateGetPatchRoundTripThroughTheStore(t *testing.T) {
	f := newNRSFixture(t, "NRS-HTTP")
	h := nrsHTTP{f}

	line := map[string]any{
		"description": "Widget", "quantity": "1", "unit_price": "100.00", "line_total": "100.00", "line_tax": "7.50",
		"id": uuid.NewString(), "tax_category": "STANDARD_VAT", "tax_percent": "7.5", "hsn_code": "8471.30", "isic_code": "6201",
		"product_category": "Electronics", "service_category": "Software", "sellers_item_identification": "SKU-1",
		"price_unit": "KGM", "base_quantity": "1",
	}
	body := nrsHeaderBody()
	body["entity_id"], body["invoice_number"], body["buyer_tin"] = f.entityID, "NRS-HTTP-1", "BUYER-TIN"
	body["line_items"] = []any{line}
	code, created := h.post(t, mustJSON(t, body))
	if code != http.StatusCreated {
		t.Fatalf("POST = %d %s", code, created["error"])
	}
	id := str(t, created["id"])
	if ids := lineIDsOf(t, created); len(ids) != 1 || ids[0] == line["id"] {
		t.Fatalf("created line ids = %v, want one minted id that is not the body's", ids)
	}

	got := h.get(t, id)
	for _, k := range nrsHeaderKeys {
		switch k {
		case "due_date", "tax_point_date":
			if !strings.HasPrefix(str(t, got[k]), "2026-03-0") {
				t.Errorf("%s = %s, want a 2026-03 date", k, got[k])
			}
		default:
			if want := str(t, json.RawMessage(mustJSON(t, body[k]))); str(t, got[k]) != want {
				t.Errorf("%s = %s, want %q", k, got[k], want)
			}
		}
	}
	var subtotals []map[string]string
	if err := json.Unmarshal(got["tax_subtotals"], &subtotals); err != nil || len(subtotals) != 1 ||
		subtotals[0]["tax_category"] != "STANDARD_VAT" || subtotals[0]["tax_percent"] != "7.50" ||
		subtotals[0]["taxable_amount"] != "100.00" || subtotals[0]["tax_amount"] != "7.50" {
		t.Fatalf("tax_subtotals = %s (%v), want one STANDARD_VAT group 7.50 / 100.00 / 7.50", got["tax_subtotals"], err)
	}
	lineID := lineIDsOf(t, got)[0]

	// null clears a new key and leaves an old key and every omitted key alone.
	code, patched := h.patch(t, f.c, id, `{"buyer_email":null,"due_date":null,"issue_time":null,"buyer_tin":null}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH null = %d %s", code, patched["error"])
	}
	got = h.get(t, id)
	for _, k := range []string{"buyer_email", "due_date", "issue_time"} {
		if string(got[k]) != "null" {
			t.Errorf("%s = %s after null, want null", k, got[k])
		}
	}
	if str(t, got["buyer_tin"]) != "BUYER-TIN" {
		t.Errorf("buyer_tin = %s, want it unchanged by null", got["buyer_tin"])
	}
	if str(t, got["buyer_state"]) != "v-buyer_state" || str(t, got["tax_point_date"]) == "<null>" {
		t.Errorf("an omitted key changed: buyer_state=%s tax_point_date=%s", got["buyer_state"], got["tax_point_date"])
	}

	// A new key alone, then a time round trip.
	if code, m := h.patch(t, f.c, id, `{"issue_time":"09:05:03"}`); code != http.StatusOK {
		t.Fatalf("PATCH issue_time = %d %s", code, m["error"])
	}
	if got = h.get(t, id); str(t, got["issue_time"]) != "09:05:03" {
		t.Errorf("issue_time = %s, want 09:05:03", got["issue_time"])
	}

	// A line id with a changed description keeps the omitted NRS fields; the line id is re-minted.
	edit := mustJSON(t, map[string]any{"line_items": []any{map[string]any{
		"id": strings.ToUpper(lineID), "description": "Widget v2", "quantity": "1", "unit_price": "100.00", "line_total": "100.00", "line_tax": "7.50",
	}}})
	if code, m := h.patch(t, f.c, id, edit); code != http.StatusOK {
		t.Fatalf("PATCH line = %d %s", code, m["error"])
	}
	got = h.get(t, id)
	var lines []map[string]json.RawMessage
	_ = json.Unmarshal(got["line_items"], &lines)
	if len(lines) != 1 || str(t, lines[0]["description"]) != "Widget v2" || str(t, lines[0]["tax_category"]) != "STANDARD_VAT" ||
		str(t, lines[0]["hsn_code"]) != "8471.30" || str(t, lines[0]["tax_percent"]) != "7.50" || str(t, lines[0]["id"]) == lineID {
		t.Fatalf("line after an id edit = %v, want the new description, the carried NRS fields and a new id", lines)
	}
	lineID = str(t, lines[0]["id"])

	// A foreign line id: another invoice of this tenant, another tenant, a random uuid and a
	// non-uuid all get the same 400 and the same body, and the header change in the body is not applied.
	other := h.f.create(t, "NRS-HTTP-OTHER", CreateInput{LineItems: []LineItemInput{fullNRSLine()}})
	otherLine := h.f.get(t, other.ID).LineItems[0].ID
	tenantB := seedTenant(t, f.super, "NRS-HTTP B")
	entityB := seedEntity(t, f.super, tenantB, "B Co")
	cB := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB})
	invB, err := f.store.Create(cB, CreateInput{EntityID: entityB, InvoiceNumber: "NRS-HTTP-B", LineItems: []LineItemInput{fullNRSLine()}})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	foreignLine := f.get2(t, cB, invB.ID).LineItems[0].ID

	var first []byte
	for name, bad := range map[string]string{
		"another invoice": otherLine, "another tenant": foreignLine, "random uuid": uuid.NewString(), "not a uuid": "not-a-uuid", "empty": "",
	} {
		b := mustJSON(t, map[string]any{"buyer_city": "Changed", "line_items": []any{map[string]any{"id": bad, "description": "x"}}})
		r := httptest.NewRequest(http.MethodPatch, "/v1/invoices/"+id, strings.NewReader(b))
		r.SetPathValue("id", id)
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: f.tenantID}))
		rec := httptest.NewRecorder()
		EditHandler(f.store.Edit, nil).ServeHTTP(rec, r)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"line_items id must name a line of this invoice"`) {
			t.Errorf("%s: %d %s, want the 400 sentence", name, rec.Code, rec.Body.String())
		}
		if first == nil {
			first = rec.Body.Bytes()
		} else if string(first) != rec.Body.String() {
			t.Errorf("%s: body %s differs from the others %s: an existence oracle", name, rec.Body.String(), first)
		}
	}
	got = h.get(t, id)
	if str(t, got["buyer_city"]) != "v-buyer_city" || lineIDsOf(t, got)[0] != lineID {
		t.Errorf("a refused edit changed the invoice: buyer_city=%s lines=%v", got["buyer_city"], lineIDsOf(t, got))
	}

	// The other tenant's invoice by id is a 404, with or without a line id; nothing of B's changes.
	code, _ = h.patch(t, f.c, invB.ID, mustJSON(t, map[string]any{"buyer_city": "Pwned", "line_items": []any{map[string]any{"id": foreignLine}}}))
	if code != http.StatusNotFound {
		t.Errorf("PATCH of another tenant's invoice = %d, want 404", code)
	}
	if now := f.get2(t, cB, invB.ID); now.BuyerCity != nil || now.LineItems[0].ID != foreignLine {
		t.Errorf("tenant B's invoice changed: %+v", now)
	}

	// A malformed issue_time never reaches the store.
	if code, m := h.patch(t, f.c, id, `{"issue_time":"9:05:03","buyer_city":"Changed"}`); code != http.StatusBadRequest ||
		str(t, m["error"]) != "issue_time must be HH:MM:SS" {
		t.Errorf("PATCH bad time = %d %s", code, m["error"])
	}
	if got = h.get(t, id); str(t, got["buyer_city"]) != "v-buyer_city" {
		t.Errorf("buyer_city = %s, want it untouched by a refused edit", got["buyer_city"])
	}
}

func (f nrsFixture) get2(t *testing.T, ctx context.Context, id string) Invoice {
	t.Helper()
	inv, err := f.store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return inv
}

func TestNRSInvoiceHTTP_NULInTextIs400AndWritesNothing(t *testing.T) {
	f := newNRSFixture(t, "NRS-NUL")
	h := nrsHTTP{f}
	const wantMsg = "invoice: validation"
	line := map[string]any{"description": "Widget", "quantity": "1", "unit_price": "100.00", "line_total": "100.00", "line_tax": "7.50"}

	for _, key := range []string{"buyer_city", "buyer_name"} {
		body := nrsHeaderBody()
		body["entity_id"], body["invoice_number"], body["line_items"] = f.entityID, "NRS-NUL-"+key, []any{line}
		body[key] = "a\u0000b"
		code, m := h.post(t, mustJSON(t, body))
		if code != http.StatusBadRequest || str(t, m["error"]) != wantMsg {
			t.Errorf("POST %s NUL = %d %s, want 400 %q", key, code, m["error"], wantMsg)
		}
		var n int
		if err := f.super.QueryRow(context.Background(), `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, f.tenantID, "NRS-NUL-"+key).Scan(&n); err != nil || n != 0 {
			t.Errorf("POST %s NUL left %d rows (err %v), want 0", key, n, err)
		}
	}

	for _, key := range []string{"description", "hsn_code"} {
		bad := map[string]any{}
		for k, v := range line {
			bad[k] = v
		}
		bad[key] = "a\u0000b"
		num := "NRS-NUL-LINE-" + key
		body := nrsHeaderBody()
		body["entity_id"], body["invoice_number"], body["line_items"] = f.entityID, num, []any{bad}
		code, m := h.post(t, mustJSON(t, body))
		if code != http.StatusBadRequest || str(t, m["error"]) != wantMsg {
			t.Errorf("POST line %s NUL = %d %s, want 400 %q", key, code, m["error"], wantMsg)
		}
		var n int
		if err := f.super.QueryRow(context.Background(), `SELECT count(*) FROM invoices WHERE tenant_id = $1 AND invoice_number = $2`, f.tenantID, num).Scan(&n); err != nil || n != 0 {
			t.Errorf("POST line %s NUL left %d invoice rows (err %v), want 0", key, n, err)
		}
	}

	body := nrsHeaderBody()
	body["entity_id"], body["invoice_number"], body["line_items"] = f.entityID, "NRS-NUL-OK", []any{line}
	code, created := h.post(t, mustJSON(t, body))
	if code != http.StatusCreated {
		t.Fatalf("POST = %d %s", code, created["error"])
	}
	id := str(t, created["id"])
	before := h.get(t, id)

	patches := map[string]string{
		"buyer_city":  `{"buyer_city":"a\u0000b"}`,
		"buyer_name":  `{"buyer_name":"a\u0000b"}`,
		"line text":   `{"line_items":[{"description":"a\u0000b","quantity":"1","unit_price":"100.00","line_total":"100.00","line_tax":"7.50"}]}`,
		"line + head": `{"buyer_city":"Changed","line_items":[{"description":"a\u0000b","quantity":"1","unit_price":"100.00","line_total":"100.00","line_tax":"7.50"}]}`,
	}
	for name, p := range patches {
		code, m := h.patch(t, f.c, id, p)
		if code != http.StatusBadRequest || str(t, m["error"]) != wantMsg {
			t.Errorf("PATCH %s NUL = %d %s, want 400 %q", name, code, m["error"], wantMsg)
		}
	}
	after := h.get(t, id)
	for _, k := range []string{"buyer_city", "buyer_name", "line_items"} {
		if string(after[k]) != string(before[k]) {
			t.Errorf("%s changed by a refused edit: %s -> %s", k, before[k], after[k])
		}
	}
}
