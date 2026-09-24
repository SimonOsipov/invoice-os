package importer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("object storage reset") }
func (errReader) Close() error             { return nil }

// chkAskedHeader returns the header the one asked question names for field.
func chkAskedHeader(t *testing.T, s *mcStub, field string) string {
	t.Helper()
	if len(s.calls) != 1 {
		t.Fatalf("Ask calls = %d, want 1", len(s.calls))
	}
	q, ok := s.calls[0].Questions[field]
	if !ok {
		t.Fatalf("no question for %q in %v", field, s.calls[0].Questions)
	}
	_, header, ok := strings.Cut(q.Instructions, " Column header: ")
	if !ok {
		t.Fatalf("question for %q names no header: %q", field, q.Instructions)
	}
	return strings.TrimSuffix(header, ".")
}

func TestCheckMappingHandler_OpensTheRequestedDocumentWholeAndClosesIt(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}

	rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, chkOnePlacement))
	if rec.Code != http.StatusOK || len(stub.calls) != 1 {
		t.Fatalf("status %d, Ask %d; want 200, 1 (body=%s)", rec.Code, len(stub.calls), raw)
	}
	if len(open.ids) != 1 || open.ids[0] != open.doc.ID {
		t.Errorf("opened %v, want [%s]", open.ids, open.doc.ID)
	}
	if len(open.ranges) != 1 || open.ranges[0] != "" {
		t.Errorf("ranges %q, want one unranged read", open.ranges)
	}
	if open.body.closes != 1 {
		t.Errorf("body closes = %d, want 1", open.body.closes)
	}
}

func TestCheckMappingHandler_AReadFailureIs500(t *testing.T) {
	id := testIdentity()
	fn, ct := "data.csv", "text/csv"
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 0})}
	open := func(context.Context, string, string) (document.Document, document.Object, error) {
		return document.Document{Filename: &fn, DeclaredContentType: &ct}, document.Object{Body: errReader{}}, nil
	}
	rec, raw := doCheckRequest(t, open, stub, nil, &id, checkReqBody(t, "8f7e9a4c-2b1d-4c3e-9f0a-1b2c3d4e5f60", chkOnePlacement))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, raw)
	}
	if got := chkErrorText(t, raw); got != "internal server error" {
		t.Errorf("error = %q, want %q", got, "internal server error")
	}
	if len(stub.calls) != 0 {
		t.Errorf("Ask calls = %d, want 0", len(stub.calls))
	}
}

func TestCheckMappingHandler_AnEmptyFileAnswersEmptyWithoutAsk(t *testing.T) {
	id := testIdentity()
	t.Run("control: a one-row file asks", func(t *testing.T) {
		open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 1))
		stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}
		doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, chkOnePlacement))
		if len(stub.calls) != 1 {
			t.Errorf("Ask calls = %d, want 1", len(stub.calls))
		}
	})
	open := newFakeDocOpen("empty.csv", "text/csv", nil)
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 0})}
	rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, chkOnePlacement))
	if rec.Code != http.StatusOK || string(raw) != chkEmpty {
		t.Errorf("status %d body %q; want 200 %q", rec.Code, raw, chkEmpty)
	}
	if len(open.ids) != 1 || len(stub.calls) != 0 {
		t.Errorf("opens %d, Ask %d; want 1, 0", len(open.ids), len(stub.calls))
	}
}

func TestCheckMappingHandler_AHeaderOnlyCSVAsksOverTheHeaderAlone(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", []byte("Invoice No,VAT\n"))
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 0})}
	rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, chkOnePlacement))
	if rec.Code != http.StatusOK || string(raw) != "{\"doubted\":[\"invoice_number\"]}\n" {
		t.Fatalf("status %d body %q; want 200 doubting invoice_number", rec.Code, raw)
	}
	if want := mappingPromptText([][]string{{"Invoice No", "VAT"}}); stub.calls[0].State != want {
		t.Errorf("State = %q, want %q", stub.calls[0].State, want)
	}
	if strings.Contains(stub.calls[0].State, "Row 2:") {
		t.Errorf("State shows a row past the header: %q", stub.calls[0].State)
	}
}

func TestCheckMappingHandler_AnXLSXDocumentIsChecked(t *testing.T) {
	id := testIdentity()
	header := []string{"Invoice No", "VAT"}
	rows := [][]string{{"INV-1", "75"}, {"INV-2", "150"}}
	open := newFakeDocOpen("data.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", xlsxBody(t, header, rows))
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1, "vat": 0.05})}
	rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, map[string]string{"invoice_number": "Invoice No", "vat": "VAT"}))
	if rec.Code != http.StatusOK || string(raw) != "{\"doubted\":[\"vat\"]}\n" {
		t.Fatalf("status %d body %q; want 200 doubting vat", rec.Code, raw)
	}
	if want := mappingPromptText(suggestWindow(header, rows)); stub.calls[0].State != want {
		t.Errorf("State = %q, want %q", stub.calls[0].State, want)
	}
}

func TestCheckMappingHandler_TheBodyLimitIsInclusive(t *testing.T) {
	id := testIdentity()
	sized := func(docID string, n int) string {
		head := `{"document_id":"` + docID + `","mapping":{"invoice_number":"Invoice No"},"pad":"`
		return head + strings.Repeat("x", n-len(head)-2) + `"}`
	}
	for _, tc := range []struct {
		name       string
		size       int
		wantStatus int
		wantAsks   int
	}{
		{"exactly the limit", maxCreateDocumentBodyBytes, http.StatusOK, 1},
		{"one byte over", maxCreateDocumentBodyBytes + 1, http.StatusRequestEntityTooLarge, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
			stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}
			body := sized(open.doc.ID, tc.size)
			if len(body) != tc.size {
				t.Fatalf("fixture body is %d bytes, want %d", len(body), tc.size)
			}
			rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, body)
			if rec.Code != tc.wantStatus || len(stub.calls) != tc.wantAsks {
				t.Errorf("status %d, Ask %d; want %d, %d (body=%s)", rec.Code, len(stub.calls), tc.wantStatus, tc.wantAsks, raw)
			}
		})
	}
}

func TestCheckMappingHandler_MappingIsValidatedEvenWhenOff(t *testing.T) {
	id := testIdentity()
	for _, tc := range []struct {
		name    string
		checker MappingChecker
	}{
		{"nil checker", nil},
		{"off checker", &mcStub{enabled: false}},
	} {
		for _, bad := range []struct {
			name    string
			mapping string
			wantMsg string
		}{
			{"bad key", `{"supplier_tin":"Invoice No"}`, `mapping key "supplier_tin" is not a recognized canonical field`},
			{"empty value", `{"vat":""}`, `mapping value for "vat" is empty`},
		} {
			t.Run(tc.name+", "+bad.name, func(t *testing.T) {
				open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
				rec, raw := doCheckRequest(t, open.fn(), tc.checker, nil, &id, `{"document_id":"`+open.doc.ID+`","mapping":`+bad.mapping+`}`)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, raw)
				}
				if got := chkErrorText(t, raw); got != bad.wantMsg {
					t.Errorf("error = %q, want %q", got, bad.wantMsg)
				}
				if len(open.ids) != 0 {
					t.Errorf("opens = %d, want 0", len(open.ids))
				}
			})
		}
	}
}

func TestCheckMappingHandler_TheFirstBadEntryInKeyOrderIsNamed(t *testing.T) {
	id := testIdentity()
	for _, tc := range []struct {
		name    string
		mapping string
		wantMsg string
	}{
		{"one bad key among good ones", `{"invoice_number":"A","total":"B","vat_rate":"C","buyer_tin":"D"}`, `mapping key "vat_rate" is not a recognized canonical field`},
		{"two bad keys", `{"zz_bad":"x","total":"T","aa_bad":"y"}`, `mapping key "aa_bad" is not a recognized canonical field`},
		{"a bad key before an empty value", `{"buyer_tin":"","aa_bad":"x"}`, `mapping key "aa_bad" is not a recognized canonical field`},
		{"an empty value before a bad key", `{"zz_bad":"x","buyer_tin":""}`, `mapping value for "buyer_tin" is empty`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Map order is random per request; 64 requests pin one answer.
			for i := 0; i < 64; i++ {
				open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
				stub := &mcStub{enabled: true}
				rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, `{"document_id":"`+open.doc.ID+`","mapping":`+tc.mapping+`}`)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("request %d: status = %d, want 400 (body=%s)", i, rec.Code, raw)
				}
				if got := chkErrorText(t, raw); got != tc.wantMsg {
					t.Fatalf("request %d: error = %q, want %q", i, got, tc.wantMsg)
				}
			}
		})
	}
}

func TestCheckMappingHandler_KeysMatchExactly(t *testing.T) {
	id := testIdentity()
	for _, key := range []string{"Invoice_Number", "VAT", " vat", "vat ", "invoice-number"} {
		t.Run(key, func(t *testing.T) {
			open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
			stub := &mcStub{enabled: true}
			rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, map[string]string{key: "Invoice No"}))
			want := `mapping key "` + key + `" is not a recognized canonical field`
			if rec.Code != http.StatusBadRequest || chkErrorText(t, raw) != want {
				t.Errorf("status %d body %s; want 400 %q", rec.Code, raw, want)
			}
			if len(open.ids) != 0 || len(stub.calls) != 0 {
				t.Errorf("opens %d, Ask %d; want 0, 0", len(open.ids), len(stub.calls))
			}
		})
	}
}

func TestCheckMappingHandler_DuplicateAndNullJSON(t *testing.T) {
	id := testIdentity()
	for _, tc := range []struct {
		name       string
		body       func(docID string) string
		wantStatus int
		wantBody   string // exact 200 body, or the error text
		wantAsks   int
	}{
		{"a duplicate key takes the last value", func(d string) string {
			return `{"document_id":"` + d + `","mapping":{"vat":"","vat":"VAT"}}`
		}, http.StatusOK, "{\"doubted\":[\"vat\"]}\n", 1},
		{"an empty last duplicate is empty", func(d string) string {
			return `{"document_id":"` + d + `","mapping":{"vat":"VAT","vat":""}}`
		}, http.StatusBadRequest, `mapping value for "vat" is empty`, 0},
		{"a duplicate document_id takes the last", func(d string) string {
			return `{"document_id":"nope","document_id":"` + d + `","mapping":{"vat":"VAT"}}`
		}, http.StatusOK, "{\"doubted\":[\"vat\"]}\n", 1},
		{"a null value is empty", func(d string) string {
			return `{"document_id":"` + d + `","mapping":{"vat":null}}`
		}, http.StatusBadRequest, `mapping value for "vat" is empty`, 0},
		{"a null mapping is absent", func(d string) string {
			return `{"document_id":"` + d + `","mapping":null}`
		}, http.StatusOK, chkEmpty, 0},
		{"a mapping that is not an object", func(d string) string {
			return `{"document_id":"` + d + `","mapping":["vat"]}`
		}, http.StatusBadRequest, "invalid request body", 0},
		{"a non-string value", func(d string) string {
			return `{"document_id":"` + d + `","mapping":{"vat":7}}`
		}, http.StatusBadRequest, "invalid request body", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No", "VAT"}, 2))
			stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"vat": 0})}
			rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, tc.body(open.doc.ID))
			if rec.Code != tc.wantStatus || len(stub.calls) != tc.wantAsks {
				t.Fatalf("status %d, Ask %d; want %d, %d (body=%s)", rec.Code, len(stub.calls), tc.wantStatus, tc.wantAsks, raw)
			}
			got := string(raw)
			if tc.wantStatus != http.StatusOK {
				got = chkErrorText(t, raw)
			}
			if got != tc.wantBody {
				t.Errorf("body = %q, want %q", got, tc.wantBody)
			}
			if tc.wantAsks == 1 {
				if h := chkAskedHeader(t, stub, "vat"); h != "VAT" {
					t.Errorf("asked header = %q, want %q", h, "VAT")
				}
			}
		})
	}
}

// The handler does not check a placed header against the file: it asks as sent.
func TestCheckMappingHandler_AHeaderNotInTheFileIsAskedVerbatim(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 0})}
	rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, map[string]string{"invoice_number": "Not In File"}))
	if rec.Code != http.StatusOK || string(raw) != "{\"doubted\":[\"invoice_number\"]}\n" {
		t.Fatalf("status %d body %q; want 200 doubting invoice_number", rec.Code, raw)
	}
	if h := chkAskedHeader(t, stub, "invoice_number"); h != "Not In File" {
		t.Errorf("asked header = %q, want %q", h, "Not In File")
	}
}

func TestCheckMappingHandler_NoDoubtIsAnEmptyArrayNotNull(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No", "VAT"}, 2))
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1, "vat": 0.5})}
	rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, map[string]string{"invoice_number": "Invoice No", "vat": "VAT"}))
	if len(stub.calls) != 1 {
		t.Fatalf("Ask calls = %d, want 1", len(stub.calls))
	}
	if rec.Code != http.StatusOK || string(raw) != chkEmpty {
		t.Errorf("status %d body %q; want 200 %q", rec.Code, raw, chkEmpty)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestCheckMappingHandler_TheCallerContextReachesAsk(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
	var got context.Context
	spy := &ctxSpy{mcStub: mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}, ctx: &got}
	r := httptest.NewRequest("POST", "/v1/imports/check-mapping", strings.NewReader(checkReqBody(t, open.doc.ID, chkOnePlacement)))
	r = r.WithContext(context.WithValue(auth.WithIdentity(r.Context(), id), chkCtxKey{}, "request"))
	rec := httptest.NewRecorder()
	CheckMappingHandler(open.fn(), spy, nil).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || len(spy.calls) != 1 {
		t.Fatalf("status %d, Ask %d; want 200, 1 (body=%s)", rec.Code, len(spy.calls), rec.Body.String())
	}
	if got == nil || got.Value(chkCtxKey{}) != "request" {
		t.Errorf("Ask did not get the request's context")
	}
}

type chkCtxKey struct{}

type ctxSpy struct {
	mcStub
	ctx *context.Context
}

func (s *ctxSpy) Ask(ctx context.Context, req jev.Request) (jev.Response, error) {
	*s.ctx = ctx
	return s.mcStub.Ask(ctx, req)
}
