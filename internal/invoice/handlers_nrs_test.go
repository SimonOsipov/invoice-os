package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// nrsHeaderKeys are the 22 NRS header and party wire keys, in wire order.
var nrsHeaderKeys = []string{
	"invoice_kind", "tax_currency_code", "due_date", "issue_time", "tax_point_date", "payment_status",
	"supplier_email", "supplier_telephone", "supplier_street", "supplier_city", "supplier_postal_zone", "supplier_country",
	"supplier_state", "supplier_lga", "buyer_email", "buyer_telephone", "buyer_street", "buyer_city",
	"buyer_postal_zone", "buyer_country", "buyer_state", "buyer_lga",
}

var nrsLineKeys = []string{
	"tax_category", "hsn_code", "isic_code", "product_category", "service_category",
	"sellers_item_identification", "price_unit", "tax_percent", "base_quantity",
}

// nrsHeaderBody is a request body with a distinct value per NRS header key.
func nrsHeaderBody() map[string]any {
	b := map[string]any{}
	for _, k := range nrsHeaderKeys {
		switch k {
		case "due_date", "tax_point_date":
			b[k] = "2026-03-0" + string(rune('1'+len(b)%9)) + "T00:00:00Z" // distinct per key
		case "issue_time":
			b[k] = "14:30:05"
		default:
			b[k] = "v-" + k
		}
	}
	return b
}

func nrsLineBody() map[string]any {
	l := map[string]any{"description": "d"}
	for _, k := range nrsLineKeys {
		l[k] = "l-" + k
	}
	return l
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var nrsIdentity = auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}

// nrsHeaderGot reads the 22 NRS header fields of a CreateInput or UpdateInput by Go field name.
func nrsHeaderGot(v reflect.Value) map[string]any {
	got := map[string]any{}
	for _, name := range nrsHeaderGoNames {
		f := v.FieldByName(name)
		switch p := f.Interface().(type) {
		case *string:
			if p != nil {
				got[name] = *p
			}
		case *time.Time:
			if p != nil {
				got[name] = p.UTC().Format(time.RFC3339)
			}
		}
	}
	return got
}

var nrsHeaderGoNames = []string{
	"InvoiceKind", "TaxCurrencyCode", "DueDate", "IssueTime", "TaxPointDate", "PaymentStatus",
	"SupplierEmail", "SupplierTelephone", "SupplierStreet", "SupplierCity", "SupplierPostalZone", "SupplierCountry",
	"SupplierState", "SupplierLGA", "BuyerEmail", "BuyerTelephone", "BuyerStreet", "BuyerCity",
	"BuyerPostalZone", "BuyerCountry", "BuyerState", "BuyerLGA",
}

func nrsHeaderWant(body map[string]any) map[string]any {
	want := map[string]any{}
	for i, k := range nrsHeaderKeys {
		want[nrsHeaderGoNames[i]] = body[k]
	}
	return want
}

func nrsLineGot(li LineItemInput) map[string]string {
	return map[string]string{
		"tax_category": *li.TaxCategory, "hsn_code": *li.HSNCode, "isic_code": *li.ISICCode,
		"product_category": *li.ProductCategory, "service_category": *li.ServiceCategory,
		"sellers_item_identification": *li.SellersItemIdentification, "price_unit": *li.PriceUnit,
		"tax_percent": *li.TaxPercent, "base_quantity": *li.BaseQuantity,
	}
}

func TestCreateHandler_NRSFieldsMapOneToOne(t *testing.T) {
	body := nrsHeaderBody()
	body["entity_id"] = uuid.NewString()
	body["invoice_number"] = "INV-1"
	body["line_items"] = []any{nrsLineBody()}

	var got CreateInput
	create := func(_ context.Context, in CreateInput) (Invoice, error) { got = in; return Invoice{ID: "x"}, nil }
	rec, _ := doInvoiceCreate(t, create, &nrsIdentity, mustJSON(t, body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}

	if g, w := nrsHeaderGot(reflect.ValueOf(got)), nrsHeaderWant(body); !reflect.DeepEqual(g, w) {
		t.Errorf("CreateInput NRS header =\n%v\nwant\n%v", g, w)
	}
	if len(got.LineItems) != 1 {
		t.Fatalf("lines = %d, want 1", len(got.LineItems))
	}
	for _, k := range nrsLineKeys {
		if g := nrsLineGot(got.LineItems[0])[k]; g != "l-"+k {
			t.Errorf("line %s = %q, want %q", k, g, "l-"+k)
		}
	}
}

func TestEditHandler_NRSFieldsMapOneToOne(t *testing.T) {
	body := nrsHeaderBody()
	lineID := uuid.NewString()
	l := nrsLineBody()
	l["id"] = lineID
	body["line_items"] = []any{l}

	var got EditInput
	edit := func(_ context.Context, _ string, in EditInput) (Invoice, error) {
		got = in
		return Invoice{ID: "x"}, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), mustJSON(t, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	if g, w := nrsHeaderGot(reflect.ValueOf(got.UpdateInput)), nrsHeaderWant(body); !reflect.DeepEqual(g, w) {
		t.Errorf("EditInput NRS header =\n%v\nwant\n%v", g, w)
	}
	if got.LineItems == nil || len(*got.LineItems) != 1 {
		t.Fatalf("lines = %v, want 1", got.LineItems)
	}
	li := (*got.LineItems)[0]
	for _, k := range nrsLineKeys {
		if g := nrsLineGot(li)[k]; g != "l-"+k {
			t.Errorf("line %s = %q, want %q", k, g, "l-"+k)
		}
	}
}

func TestEditHandler_OnlyAnNRSKeyReachesTheStore(t *testing.T) {
	called := false
	var got EditInput
	edit := func(_ context.Context, _ string, in EditInput) (Invoice, error) {
		called, got = true, in
		return Invoice{ID: "x"}, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), `{"buyer_state":"NG-LA"}`)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("status = %d, called = %v, want 200 and true (body=%s)", rec.Code, called, rec.Body.String())
	}
	if got.BuyerState == nil || *got.BuyerState != "NG-LA" {
		t.Errorf("BuyerState = %v, want NG-LA", got.BuyerState)
	}
}

func TestCreateAndEditHandler_RefuseAMalformedIssueTime(t *testing.T) {
	bads := []string{
		"25:61", "14:30", "2:30 PM", "9:05:03", "14:30:00.5", "24:00:00", "",
		" 14:30:00", "14:30:00 ", "14:30:00\n", "١٤:٣٠:٠٠", "23:59:60", "14:60:00", "99:00:00",
		"14:30:00Z", "T14:30:00", "14-30-00", "20:00", "\x00invoice.clear",
	}
	for _, bad := range bads {
		t.Run(bad, func(t *testing.T) {
			called := false
			create := func(context.Context, CreateInput) (Invoice, error) { called = true; return Invoice{}, nil }
			edit := func(context.Context, string, EditInput) (Invoice, error) { called = true; return Invoice{}, nil }

			cb := mustJSON(t, map[string]any{"entity_id": uuid.NewString(), "invoice_number": "N", "issue_time": bad})
			eb := mustJSON(t, map[string]any{"issue_time": bad})
			for name, rec := range map[string]*httptest.ResponseRecorder{
				"create": firstRec(doInvoiceCreate(t, create, &nrsIdentity, cb)),
				"edit":   firstRec(doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), eb)),
			} {
				if rec.Code != http.StatusBadRequest {
					t.Errorf("%s: status = %d, want 400 (body=%s)", name, rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), `"issue_time must be HH:MM:SS"`) {
					t.Errorf("%s: body = %s, want the exact message", name, rec.Body.String())
				}
			}
			if called {
				t.Error("store called, want it never reached")
			}
		})
	}
}

func firstRec(rec *httptest.ResponseRecorder, _ invoiceBody) *httptest.ResponseRecorder { return rec }

func TestCreateHandler_AcceptsAWellFormedIssueTime(t *testing.T) {
	for _, ok := range []string{"23:59:59", "00:00:00", "19:59:59", "20:00:00", "09:05:03", "12:00:00"} {
		t.Run(ok, func(t *testing.T) {
			var got CreateInput
			create := func(_ context.Context, in CreateInput) (Invoice, error) { got = in; return Invoice{ID: "x"}, nil }
			b := mustJSON(t, map[string]any{"entity_id": uuid.NewString(), "invoice_number": "N", "issue_time": ok})
			rec, _ := doInvoiceCreate(t, create, &nrsIdentity, b)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
			}
			if got.IssueTime == nil || *got.IssueTime != ok {
				t.Errorf("IssueTime = %v, want %q", got.IssueTime, ok)
			}

			// The PATCH leg of the same boundary.
			var edited EditInput
			edit := func(_ context.Context, _ string, in EditInput) (Invoice, error) {
				edited = in
				return Invoice{ID: "x"}, nil
			}
			rec, _ = doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), mustJSON(t, map[string]any{"issue_time": ok}))
			if rec.Code != http.StatusOK {
				t.Fatalf("edit status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			if edited.IssueTime == nil || edited.IssueTime == ClearText || *edited.IssueTime != ok {
				t.Errorf("edit IssueTime = %v, want %q", edited.IssueTime, ok)
			}
		})
	}
}

func TestEditHandler_NullClearsANewKeyButNotAnOldOne(t *testing.T) {
	var got EditInput
	edit := func(_ context.Context, _ string, in EditInput) (Invoice, error) {
		got = in
		return Invoice{ID: "x"}, nil
	}
	rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(),
		`{"buyer_email":null,"due_date":null,"issue_time":null,"buyer_tin":null,"supplier_name":"S"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got.BuyerEmail != ClearText {
		t.Errorf("BuyerEmail = %v, want ClearText", got.BuyerEmail)
	}
	if got.DueDate != ClearDate {
		t.Errorf("DueDate = %v, want ClearDate", got.DueDate)
	}
	if got.IssueTime != ClearText {
		t.Errorf("IssueTime = %v, want ClearText", got.IssueTime)
	}
	if got.BuyerTIN != nil {
		t.Errorf("BuyerTIN = %v, want nil: null on an old key leaves it unchanged", got.BuyerTIN)
	}
	if got.BuyerState != nil {
		t.Errorf("BuyerState = %v, want nil for an absent key", got.BuyerState)
	}

	// Each of the 22 keys alone: that field is the sentinel, and every other member stays nil.
	for i, key := range nrsHeaderKeys {
		t.Run(key, func(t *testing.T) {
			var one EditInput
			edit := func(_ context.Context, _ string, in EditInput) (Invoice, error) {
				one = in
				return Invoice{ID: "x"}, nil
			}
			rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), `{"`+key+`":null}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			v := reflect.ValueOf(one.UpdateInput)
			for j := 0; j < v.NumField(); j++ {
				f, name := v.Field(j), v.Type().Field(j).Name
				if name != nrsHeaderGoNames[i] {
					if !f.IsNil() {
						t.Errorf("%s = %v, want nil", name, f.Interface())
					}
					continue
				}
				want := reflect.ValueOf(ClearText)
				if f.Type() == reflect.TypeOf((*time.Time)(nil)) {
					want = reflect.ValueOf(ClearDate)
				}
				if f.IsNil() || f.Pointer() != want.Pointer() {
					t.Errorf("%s = %v, want the clear sentinel", name, f.Interface())
				}
			}
		})
	}
}

func TestInvoiceWire_NRSKeysSitBetweenFailureKindAndLineItems(t *testing.T) {
	b, err := json.Marshal(Invoice{ID: "x", Violations: json.RawMessage(`[]`), LineItems: []LineItem{{ID: "l"}}})
	if err != nil {
		t.Fatal(err)
	}
	keys := topLevelKeyOrder(t, b)
	at := -1
	for i, k := range keys {
		if k == "failure_kind" {
			at = i
		}
	}
	if at < 0 || !reflect.DeepEqual(keys[at+1:at+1+len(nrsHeaderKeys)], nrsHeaderKeys) {
		t.Fatalf("keys after failure_kind = %v, want %v", keys, nrsHeaderKeys)
	}
	if keys[at+1+len(nrsHeaderKeys)] != "line_items" {
		t.Errorf("key after the NRS keys = %q, want line_items", keys[at+1+len(nrsHeaderKeys)])
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range nrsHeaderKeys {
		if string(raw[k]) != "null" {
			t.Errorf("%s = %s, want null", k, raw[k])
		}
	}
	var lines []map[string]json.RawMessage
	if err := json.Unmarshal(raw["line_items"], &lines); err != nil {
		t.Fatal(err)
	}
	for _, k := range nrsLineKeys {
		if string(lines[0][k]) != "null" {
			t.Errorf("line %s = %s, want null", k, lines[0][k])
		}
	}
}

func TestGetHandler_TaxSubtotalsDerivedAfterTheInvoiceKeys(t *testing.T) {
	cat, pct := "STANDARD_VAT", "7.50"
	tot, tax := "100.00", "7.50"
	get := func(context.Context, string) (Invoice, error) {
		return Invoice{ID: "x", Violations: json.RawMessage(`[]`), LineItems: []LineItem{
			{ID: "a", LineNo: 1, TaxCategory: &cat, TaxPercent: &pct, LineTotal: &tot, LineTax: &tax},
			{ID: "b", LineNo: 2, TaxCategory: &cat, TaxPercent: &pct, LineTotal: &tot, LineTax: &tax},
		}}, nil
	}
	rec, _ := doInvoiceGet(t, get, &nrsIdentity, "x")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	keys := topLevelKeyOrder(t, rec.Body.Bytes())
	for i, k := range keys {
		if k == "tax_subtotals" {
			if keys[i-1] != "line_items" || keys[i+1] != "rule_set_version" {
				t.Errorf("tax_subtotals sits between %q and %q, want line_items and rule_set_version", keys[i-1], keys[i+1])
			}
		}
	}
	if n := len(keys); keys[n-1] != "verdict_stale" || keys[n-3] != "can_correct_invoice_number" || keys[n-2] != "invoice_number_blocked_reason" {
		t.Errorf("last three keys = %v, want the invoice-number pair then verdict_stale", keys[n-3:])
	}
	var resp struct {
		TaxSubtotals []map[string]any `json:"tax_subtotals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"tax_category": cat, "tax_percent": "7.50", "taxable_amount": "200.00", "tax_amount": "15.00"}}
	if !reflect.DeepEqual(resp.TaxSubtotals, want) {
		t.Errorf("tax_subtotals = %v, want %v", resp.TaxSubtotals, want)
	}

	// No category: [] not null, and the tail keys stay.
	none := func(context.Context, string) (Invoice, error) {
		return Invoice{ID: "x", Violations: json.RawMessage(`[]`), LineItems: []LineItem{{ID: "a", LineNo: 1}}}, nil
	}
	rec, _ = doInvoiceGet(t, none, &nrsIdentity, "x")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["tax_subtotals"]) != "[]" {
		t.Errorf("tax_subtotals = %s, want []", raw["tax_subtotals"])
	}
}

func TestEditHandler_LineIDMapsAndUnknownIDIs400(t *testing.T) {
	lineID := uuid.NewString()
	var got EditInput
	edit := func(_ context.Context, _ string, in EditInput) (Invoice, error) {
		got = in
		return Invoice{ID: "x"}, nil
	}
	body := mustJSON(t, map[string]any{"line_items": []any{map[string]any{"id": lineID, "description": "d"}}})
	rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got.LineItems == nil || (*got.LineItems)[0].ID == nil || *(*got.LineItems)[0].ID != lineID {
		t.Errorf("LineItemInput.ID = %v, want %q", got.LineItems, lineID)
	}

	unknown := func(context.Context, string, EditInput) (Invoice, error) { return Invoice{}, ErrUnknownLineID }
	rec, _ = doInvoiceEdit(t, unknown, &nrsIdentity, uuid.NewString(), body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"line_items id must name a line of this invoice"`) {
		t.Errorf("status = %d body = %s, want 400 with the exact message", rec.Code, rec.Body.String())
	}
}

func TestCreateHandler_LineIDIsIgnored(t *testing.T) {
	var got CreateInput
	create := func(_ context.Context, in CreateInput) (Invoice, error) { got = in; return Invoice{ID: "x"}, nil }
	b := mustJSON(t, map[string]any{
		"entity_id": uuid.NewString(), "invoice_number": "N",
		"line_items": []any{map[string]any{"id": uuid.NewString(), "description": "d"}},
	})
	rec, _ := doInvoiceCreate(t, create, &nrsIdentity, b)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if got.LineItems[0].ID != nil {
		t.Errorf("CreateInput.LineItems[0].ID = %v, want nil", got.LineItems[0].ID)
	}
}

// A wrongly typed value on a new key is a 400, never a silent clear and never a 500.
func TestCreateAndEditHandler_WrongJSONTypeOnANewKeyIs400(t *testing.T) {
	values := map[string][]string{
		"text": {`5`, `true`, `{}`, `{"a":1}`, `[]`, `["a"]`},
		"date": {`5`, `true`, `{}`, `[]`, `""`, `"2026-03-01"`, `"yesterday"`},
	}
	called := false
	create := func(context.Context, CreateInput) (Invoice, error) { called = true; return Invoice{}, nil }
	edit := func(context.Context, string, EditInput) (Invoice, error) { called = true; return Invoice{}, nil }
	var n int
	for i, key := range nrsHeaderKeys {
		kind := "text"
		if nrsHeaderGoNames[i] == "DueDate" || nrsHeaderGoNames[i] == "TaxPointDate" {
			kind = "date"
		}
		for _, v := range values[kind] {
			n++
			cb := `{"entity_id":"` + uuid.NewString() + `","invoice_number":"N","` + key + `":` + v + `}`
			if rec, _ := doInvoiceCreate(t, create, &nrsIdentity, cb); rec.Code != http.StatusBadRequest {
				t.Errorf("create %s=%s: status = %d, want 400 (body=%s)", key, v, rec.Code, rec.Body.String())
			}
			if rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), `{"`+key+`":`+v+`}`); rec.Code != http.StatusBadRequest {
				t.Errorf("edit %s=%s: status = %d, want 400 (body=%s)", key, v, rec.Code, rec.Body.String())
			}
		}
	}
	if n < 100 {
		t.Fatalf("cases = %d, want at least 100", n)
	}
	if called {
		t.Error("store called, want it never reached")
	}
}

// Null on a line field is "omitted" (the line's stored value carries); only the
// header keys clear. A wrongly typed line key or id is a 400, and a malformed id is the store's to refuse.
func TestEditHandler_LineNRSKeysNullAreOmittedAndIDStaysAsSent(t *testing.T) {
	var got EditInput
	edit := func(_ context.Context, _ string, in EditInput) (Invoice, error) {
		got = in
		return Invoice{ID: "x"}, nil
	}

	allNull := map[string]any{"id": uuid.NewString(), "description": "d"}
	for _, k := range nrsLineKeys {
		allNull[k] = nil
	}
	body := mustJSON(t, map[string]any{"line_items": []any{allNull, map[string]any{"id": nil, "description": "e"}, map[string]any{"id": "not-a-uuid"}}})
	rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), body)
	if rec.Code != http.StatusOK || got.LineItems == nil || len(*got.LineItems) != 3 {
		t.Fatalf("status = %d lines = %v, want 200 and 3 lines (body=%s)", rec.Code, got.LineItems, rec.Body.String())
	}
	lines := *got.LineItems
	for i, li := range lines {
		for _, p := range []*string{li.TaxCategory, li.HSNCode, li.ISICCode, li.ProductCategory, li.ServiceCategory,
			li.SellersItemIdentification, li.PriceUnit, li.TaxPercent, li.BaseQuantity} {
			if p != nil {
				t.Errorf("line %d carries %q, want every null NRS line key to stay nil", i, *p)
			}
		}
	}
	if lines[0].ID == nil || *lines[0].ID != allNull["id"] {
		t.Errorf("line 0 ID = %v, want the sent id", lines[0].ID)
	}
	if lines[1].ID != nil {
		t.Errorf("line 1 ID = %v, want nil for an explicit null id", lines[1].ID)
	}
	if lines[2].ID == nil || *lines[2].ID != "not-a-uuid" {
		t.Errorf("line 2 ID = %v, want the malformed id passed to the store untouched", lines[2].ID)
	}

	for _, bad := range []string{`{"id":5}`, `{"id":{}}`, `{"tax_percent":7.5}`, `{"base_quantity":1}`, `{"tax_category":["a"]}`} {
		rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), `{"line_items":[`+bad+`]}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("line %s: status = %d, want 400 (body=%s)", bad, rec.Code, rec.Body.String())
		}
	}
}

// A key added to Invoice or LineItem but not to the request bodies, or typed wrongly there, fails here.
func TestRequestStructs_CarryEveryContentKeyAndOnlyTheNewEditKeysAreNullable(t *testing.T) {
	tagsOf := func(typ reflect.Type) map[string]reflect.Type {
		m := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			m[tag] = typ.Field(i).Type
		}
		return m
	}
	_, invTags := contentFields(reflect.TypeOf(Invoice{}))
	_, lineTags := contentFields(reflect.TypeOf(LineItem{}))
	if len(invTags) != 32 || len(lineTags) != 15 {
		t.Fatalf("content tags = %d header, %d line; want 32 and 15", len(invTags), len(lineTags))
	}

	create, edit, line := tagsOf(reflect.TypeOf(createRequest{})), tagsOf(reflect.TypeOf(editReq{})), tagsOf(reflect.TypeOf(lineItemReq{}))
	for _, k := range invTags {
		if _, ok := create[k]; !ok {
			t.Errorf("createRequest lacks %q", k)
		}
		ft, ok := edit[k]
		if !ok {
			t.Errorf("editReq lacks %q", k)
			continue
		}
		isNew := false
		for _, n := range nrsHeaderKeys {
			isNew = isNew || n == k
		}
		if nullableType := strings.HasPrefix(ft.Name(), "nullable["); nullableType != isNew {
			t.Errorf("editReq %q nullable = %v, want %v (null clears only the 22 new keys)", k, nullableType, isNew)
		}
	}
	for _, k := range lineTags {
		if _, ok := line[k]; !ok && k != "line_no" {
			t.Errorf("lineItemReq lacks %q", k)
		}
	}
	if _, ok := line["id"]; !ok {
		t.Error("lineItemReq lacks id")
	}
	if _, ok := line["line_no"]; ok {
		t.Error("lineItemReq carries line_no, which a client never sends")
	}
}

// Nothing else ties the field list's keys to the wire: a typo there regenerates its SPA copy and passes.
func TestInvoiceFieldsList_NamesTheWireContentKeys(t *testing.T) {
	_, invTags := contentFields(reflect.TypeOf(Invoice{}))
	_, lineTags := contentFields(reflect.TypeOf(LineItem{}))
	var header, line []string
	for _, f := range invoicefields.All {
		if f.Line {
			line = append(line, f.Key)
		} else {
			header = append(header, f.Key)
		}
	}
	sortedCopy := func(in []string) []string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return out
	}
	var wantLine []string
	for _, k := range lineTags {
		if k != "line_no" {
			wantLine = append(wantLine, k)
		}
	}
	if !reflect.DeepEqual(sortedCopy(header), sortedCopy(invTags)) {
		t.Errorf("header field keys = %v, want the Invoice content tags %v", sortedCopy(header), sortedCopy(invTags))
	}
	if !reflect.DeepEqual(sortedCopy(line), sortedCopy(wantLine)) {
		t.Errorf("line field keys = %v, want the LineItem content tags %v", sortedCopy(line), sortedCopy(wantLine))
	}
	got := map[string]bool{}
	for _, f := range invoicefields.All {
		got[f.Key] = true
	}
	for _, k := range append(append([]string(nil), nrsHeaderKeys...), nrsLineKeys...) {
		if !got[k] {
			t.Errorf("the field list lacks the NRS key %q", k)
		}
	}
}

func TestEditHandler_LastDuplicateKeyWinsOnANullableKey(t *testing.T) {
	cases := []struct {
		name, body string
		check      func(t *testing.T, in EditInput)
	}{
		{"null then value", `{"buyer_email":null,"buyer_email":"x"}`, func(t *testing.T, in EditInput) {
			if in.BuyerEmail == nil || in.BuyerEmail == ClearText || *in.BuyerEmail != "x" {
				t.Errorf("BuyerEmail = %v, want \"x\"", in.BuyerEmail)
			}
		}},
		{"null then valid time", `{"issue_time":null,"issue_time":"10:00:00"}`, func(t *testing.T, in EditInput) {
			if in.IssueTime == nil || in.IssueTime == ClearText || *in.IssueTime != "10:00:00" {
				t.Errorf("IssueTime = %v, want \"10:00:00\"", in.IssueTime)
			}
		}},
		{"bad time then null", `{"issue_time":"bad","issue_time":null}`, func(t *testing.T, in EditInput) {
			if in.IssueTime != ClearText {
				t.Errorf("IssueTime = %v, want ClearText", in.IssueTime)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got EditInput
			edit := func(_ context.Context, _ string, in EditInput) (Invoice, error) {
				got = in
				return Invoice{ID: "x"}, nil
			}
			rec, _ := doInvoiceEdit(t, edit, &nrsIdentity, uuid.NewString(), tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}
			tc.check(t, got)
		})
	}
}
