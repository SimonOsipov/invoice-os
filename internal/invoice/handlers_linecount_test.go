package invoice

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/jsonbody"
)

const tooManyLinesBody = `{"error":"line_items must not exceed 999"}`

func linesJSON(n int, line string) string {
	return "[" + strings.TrimSuffix(strings.Repeat(line+",", n), ",") + "]"
}

// serveLineCount serves a create or edit body and returns the recorder.
func serveLineCount(h http.Handler, method, body string) *httptest.ResponseRecorder {
	id := uuid.NewString()
	r := httptest.NewRequest(method, "/v1/invoices/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func createSpy(calls *int, got *CreateInput) http.Handler {
	return CreateHandler(func(_ context.Context, in CreateInput) (Invoice, error) {
		*calls++
		*got = in
		return Invoice{}, nil
	}, nil)
}

func editSpy(calls *int, got *EditInput, ret Invoice) http.Handler {
	return EditHandler(func(_ context.Context, _ string, in EditInput) (Invoice, error) {
		*calls++
		*got = in
		return ret, nil
	}, nil)
}

func createBody(lines string) string {
	return `{"entity_id":"e","invoice_number":"n","line_items":` + lines + `}`
}

func TestCreateHandler_ThousandLineItemsIs400StatingTheMaximum(t *testing.T) {
	var calls int
	var in CreateInput
	rec := serveLineCount(createSpy(&calls, &in), http.MethodPost, createBody(linesJSON(1000, `{"description":"x"}`)))
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != tooManyLinesBody {
		t.Fatalf("got %d %q, want 400 %s", rec.Code, rec.Body.String(), tooManyLinesBody)
	}
	if calls != 0 {
		t.Fatalf("create called %d times, want 0", calls)
	}
}

func TestCreateHandler_NineHundredNinetyNineLineItemsPass(t *testing.T) {
	var calls int
	var in CreateInput
	rec := serveLineCount(createSpy(&calls, &in), http.MethodPost, createBody(linesJSON(999, `{"description":"x"}`)))
	if calls != 1 || len(in.LineItems) != 999 {
		t.Fatalf("status %d, create calls %d, lines %d; want one call with 999 lines (%s)", rec.Code, calls, len(in.LineItems), rec.Body.String())
	}
}

func TestEditHandler_ThousandLineItemsIs400StatingTheMaximum(t *testing.T) {
	var calls int
	var in EditInput
	rec := serveLineCount(editSpy(&calls, &in, Invoice{}), http.MethodPatch, `{"line_items":`+linesJSON(1000, `{"description":"x"}`)+`}`)
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != tooManyLinesBody {
		t.Fatalf("got %d %q, want 400 %s", rec.Code, rec.Body.String(), tooManyLinesBody)
	}
	if calls != 0 {
		t.Fatalf("edit called %d times, want 0", calls)
	}
}

func TestEditHandler_NineHundredNinetyNineLineItemsPass(t *testing.T) {
	var calls int
	var in EditInput
	rec := serveLineCount(editSpy(&calls, &in, Invoice{}), http.MethodPatch, `{"line_items":`+linesJSON(999, `{"description":"x"}`)+`}`)
	if calls != 1 || in.LineItems == nil || len(*in.LineItems) != 999 {
		t.Fatalf("status %d, edit calls %d; want one call with 999 lines (%s)", rec.Code, calls, rec.Body.String())
	}
}

func TestEditHandler_AbsentAndEmptyLineItemsUnchanged(t *testing.T) {
	var calls int
	var in EditInput
	h := editSpy(&calls, &in, Invoice{})
	serveLineCount(h, http.MethodPatch, `{"buyer_name":"A"}`)
	if calls != 1 || in.LineItems != nil {
		t.Fatalf("absent line_items: calls %d, LineItems %v; want one call with nil", calls, in.LineItems)
	}
	serveLineCount(h, http.MethodPatch, `{"line_items":[]}`)
	if calls != 2 || in.LineItems == nil || len(*in.LineItems) != 0 {
		t.Fatalf("empty line_items: calls %d, LineItems %v; want a non-nil empty slice", calls, in.LineItems)
	}
}

func TestEditHandler_HeaderOnlyPatchIgnoresTheStoredLineCount(t *testing.T) {
	var calls int
	var in EditInput
	stored := Invoice{LineItems: make([]LineItem, 1500)}
	rec := serveLineCount(editSpy(&calls, &in, stored), http.MethodPatch, `{"buyer_name":"A"}`)
	if rec.Code != http.StatusOK || calls != 1 || in.LineItems != nil {
		t.Fatalf("status %d, calls %d, LineItems %v; want 200, one call, nil", rec.Code, calls, in.LineItems)
	}
}

func TestCreateHandler_NineHundredNinetyNineFullWidthLinesFit(t *testing.T) {
	desc := strings.Repeat("d", 3000)
	line := fmt.Sprintf(`{"id":%q,"description":%q,"quantity":"123456789012345","unit_price":"123456789012345","line_total":"123456789012345","line_tax":"123456789012345",`+
		`"tax_category":"STANDARD_VAT","hsn_code":"1234.56.78","isic_code":"1234","product_category":"Goods","service_category":"Services",`+
		`"sellers_item_identification":"SKU-0000000001","price_unit":"123456789012345","tax_percent":"7.5","base_quantity":"123456789012345"}`,
		uuid.NewString(), desc)
	body := createBody(linesJSON(999, line))
	if len(body) < 3<<20 || int64(len(body)) > jsonbody.LineItemsLimit {
		t.Fatalf("body is %d bytes, want between 3 MiB and %d", len(body), jsonbody.LineItemsLimit)
	}
	var calls int
	var in CreateInput
	rec := serveLineCount(createSpy(&calls, &in), http.MethodPost, body)
	if calls != 1 || len(in.LineItems) != 999 {
		t.Fatalf("status %d, calls %d, lines %d; want one call with 999 lines (%s)", rec.Code, calls, len(in.LineItems), rec.Body.String())
	}
}
