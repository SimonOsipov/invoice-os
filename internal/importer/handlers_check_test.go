package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

const chkEmpty = "{\"doubted\":[]}\n"

func checkReqBody(t *testing.T, documentID string, mapping map[string]string) string {
	t.Helper()
	b, err := json.Marshal(checkMappingRequest{DocumentID: documentID, Mapping: mapping})
	if err != nil {
		t.Fatalf("marshal check request: %v", err)
	}
	return string(b)
}

func doCheckRequest(t *testing.T, open openSpec, checker MappingChecker, log *slog.Logger, id *auth.Identity, body string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/imports/check-mapping", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if id != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), *id))
	}
	rec := httptest.NewRecorder()
	CheckMappingHandler(open, checker, log).ServeHTTP(rec, r)
	return rec, rec.Body.Bytes()
}

func chkErrorText(t *testing.T, raw []byte) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("decode error body %s: %v", raw, err)
	}
	return e.Error
}

// chkCSV is a header plus n numbered data rows.
func chkCSV(t *testing.T, header []string, n int) []byte {
	t.Helper()
	rows := make([][]string, n)
	for i := range rows {
		row := make([]string, len(header))
		for j := range row {
			row[j] = fmt.Sprintf("r%dc%d", i+1, j+1)
		}
		rows[i] = row
	}
	return csvBody(t, header, rows)
}

var chkOnePlacement = map[string]string{"invoice_number": "Invoice No"}

func TestCheckMappingHandler_StateIsTheSuggestionWindow(t *testing.T) {
	id := testIdentity()
	content := chkCSV(t, []string{"Invoice No", "VAT %", "Notes"}, 13)
	// Two openers over the same bytes: each handler drains its body.
	sOpen := newFakeDocOpen("data.csv", "text/csv", content)
	cOpen := newFakeDocOpen("data.csv", "text/csv", content)
	cOpen.doc = sOpen.doc

	suggester := &fakeSuggester{enabled: true}
	_, sRaw := doSuggestRequest(t, sOpen.fn(), (&lookupSpy{}).fn(), suggester, nil, &id, suggestReqBody(uuid.NewString(), sOpen.doc.ID))
	if len(suggester.calls) != 1 {
		t.Fatalf("control: suggester calls = %d, want 1 (body=%s)", len(suggester.calls), sRaw)
	}

	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}
	rec, raw := doCheckRequest(t, cOpen.fn(), stub, nil, &id, checkReqBody(t, sOpen.doc.ID, chkOnePlacement))
	if len(stub.calls) != 1 {
		t.Fatalf("Ask calls = %d, want 1 (status=%d body=%s)", len(stub.calls), rec.Code, raw)
	}
	state := stub.calls[0].State
	if state != suggester.calls[0].Text {
		t.Errorf("State differs from the suggestion's Text\nState: %q\n Text: %q", state, suggester.calls[0].Text)
	}
	if !strings.Contains(state, "Row 10:") || strings.Contains(state, "Row 11:") {
		t.Errorf("State should end at Row 10:\n%s", state)
	}
}

func TestCheckMappingHandler_NamesOnlyTheDoubtedField(t *testing.T) {
	id := testIdentity()
	open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No", "Buyer TIN", "VAT"}, 3))
	stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"buyer_tin": 0.05, "invoice_number": 0.9, "vat": 0.9})}
	mapping := map[string]string{"invoice_number": "Invoice No", "buyer_tin": "Buyer TIN", "vat": "VAT"}

	rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, mapping))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, raw)
	}
	if want := "{\"doubted\":[\"buyer_tin\"]}\n"; string(raw) != want {
		t.Errorf("body = %q, want %q", raw, want)
	}
}

func TestCheckMappingHandler_OffAnswersEmptyAndOpensNothing(t *testing.T) {
	id := testIdentity()
	content := chkCSV(t, []string{"Invoice No"}, 2)

	t.Run("control: enabled with one placement", func(t *testing.T) {
		open := newFakeDocOpen("data.csv", "text/csv", content)
		stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 1})}
		rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, chkOnePlacement))
		if rec.Code != http.StatusOK || len(open.ids) != 1 || len(stub.calls) != 1 {
			t.Errorf("status %d, opens %d, Ask %d; want 200, 1, 1 (body=%s)", rec.Code, len(open.ids), len(stub.calls), raw)
		}
	})

	for _, tc := range []struct {
		name string
		stub *mcStub // nil means a nil checker
		body func(docID string) string
	}{
		{"nil checker", nil, func(d string) string { return checkReqBody(t, d, chkOnePlacement) }},
		{"off checker", &mcStub{enabled: false}, func(d string) string { return checkReqBody(t, d, chkOnePlacement) }},
		{"empty mapping", &mcStub{enabled: true}, func(d string) string { return checkReqBody(t, d, map[string]string{}) }},
		{"absent mapping", &mcStub{enabled: true}, func(d string) string { return `{"document_id":"` + d + `"}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := newFakeDocOpen("data.csv", "text/csv", content)
			var checker MappingChecker
			if tc.stub != nil {
				tc.stub.resp = mcNouls(map[string]float64{"invoice_number": 0})
				checker = tc.stub
			}
			rec, raw := doCheckRequest(t, open.fn(), checker, nil, &id, tc.body(open.doc.ID))
			if rec.Code != http.StatusOK || string(raw) != chkEmpty {
				t.Errorf("status %d body %q; want 200 %q", rec.Code, raw, chkEmpty)
			}
			if len(open.ids) != 0 {
				t.Errorf("opens = %d, want 0", len(open.ids))
			}
			if tc.stub != nil && len(tc.stub.calls) != 0 {
				t.Errorf("Ask calls = %d, want 0", len(tc.stub.calls))
			}
		})
	}
}

func TestCheckMappingHandler_AnySkipAnswersEmpty(t *testing.T) {
	id := testIdentity()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"refused", fmt.Errorf("%w: refused", jev.ErrCheckSkipped)},
		{"unavailable", fmt.Errorf("%w: unavailable", jev.ErrCheckSkipped)},
		{"deadline", fmt.Errorf("jev: %w", context.DeadlineExceeded)},
		{"plain error", errors.New("boom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
			// A doubting answer beside the error: the handler must not trust it.
			stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 0}), err: tc.err}
			rec, raw := doCheckRequest(t, open.fn(), stub, nil, &id, checkReqBody(t, open.doc.ID, chkOnePlacement))
			if len(stub.calls) != 1 {
				t.Errorf("Ask calls = %d, want 1", len(stub.calls))
			}
			if rec.Code != http.StatusOK || string(raw) != chkEmpty {
				t.Errorf("status %d body %q; want 200 %q", rec.Code, raw, chkEmpty)
			}
		})
	}
}

func TestCheckMappingHandler_TheRequestLadder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		noIdentity bool
		body       func(docID string) string
		wantStatus int
		wantMsg    string
	}{
		{"no identity, before a malformed body", true, func(string) string { return "not json" }, http.StatusUnauthorized, "unauthorized"},
		{"body over the limit", false, func(d string) string {
			return `{"document_id":"` + d + `","mapping":{"invoice_number":"Invoice No"},"pad":"` + strings.Repeat("x", maxCreateDocumentBodyBytes+1024) + `"}`
		}, http.StatusRequestEntityTooLarge, "request body exceeds the size limit"},
		{"non-JSON body", false, func(string) string { return "{not json" }, http.StatusBadRequest, "invalid request body"},
		{"document_id missing", false, func(string) string { return `{"mapping":{"invoice_number":"Invoice No"}}` }, http.StatusBadRequest, "document_id is required"},
		{"document_id malformed", false, func(string) string { return `{"document_id":"nope","mapping":{"invoice_number":"Invoice No"}}` }, http.StatusBadRequest, "document_id must be a well-formed uuid"},
		{"non-canonical key", false, func(d string) string {
			return `{"document_id":"` + d + `","mapping":{"invoice_number":"Invoice No","supplier_tin":"Invoice No"}}`
		}, http.StatusBadRequest, `mapping key "supplier_tin" is not a recognized canonical field`},
		{"empty value", false, func(d string) string {
			return `{"document_id":"` + d + `","mapping":{"invoice_number":"Invoice No","vat":""}}`
		}, http.StatusBadRequest, `mapping value for "vat" is empty`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, []string{"Invoice No"}, 2))
			stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 0, "vat": 0, "supplier_tin": 0})}
			var id *auth.Identity
			if !tc.noIdentity {
				i := testIdentity()
				id = &i
			}
			rec, raw := doCheckRequest(t, open.fn(), stub, nil, id, tc.body(open.doc.ID))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, raw)
			}
			if got := chkErrorText(t, raw); got != tc.wantMsg {
				t.Errorf("error = %q, want %q", got, tc.wantMsg)
			}
			if len(open.ids) != 0 || len(stub.calls) != 0 {
				t.Errorf("opens %d, Ask %d; want 0, 0", len(open.ids), len(stub.calls))
			}
		})
	}
}

func TestCheckMappingHandler_TheOpenLadder(t *testing.T) {
	for _, tc := range []struct {
		name        string
		openErr     error
		nilBody     bool
		filename    string
		contentType string
		content     []byte
		wantStatus  int
		wantMsg     string
	}{
		{name: "not found", openErr: document.ErrNotFound, wantStatus: http.StatusNotFound, wantMsg: "not found"},
		{name: "validation", openErr: document.ErrValidation, wantStatus: http.StatusBadRequest, wantMsg: "document_id must be a well-formed uuid"},
		{name: "not an active member", openErr: fmt.Errorf("wrap: %w", db.ErrNotActiveMember), wantStatus: http.StatusForbidden, wantMsg: db.NotActiveMemberMessage},
		{name: "plain open error", openErr: errors.New("object storage unreachable: get"), wantStatus: http.StatusInternalServerError, wantMsg: "internal server error"},
		{name: "nil body", nilBody: true, wantStatus: http.StatusInternalServerError, wantMsg: "internal server error"},
		{name: "unrecognised format", filename: "scan.pdf", contentType: "application/pdf", content: []byte("%PDF-1.7\n"), wantStatus: http.StatusBadRequest, wantMsg: "unrecognized file format"},
		{name: "undecodable file", content: bytes.Repeat([]byte{0x00, 0x01, 0x02}, 64), wantStatus: http.StatusBadRequest, wantMsg: "could not decode uploaded file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filename, contentType, content := tc.filename, tc.contentType, tc.content
			if filename == "" {
				filename, contentType = "data.csv", "text/csv"
			}
			if content == nil {
				content = chkCSV(t, []string{"Invoice No"}, 2)
			}
			open := newFakeDocOpen(filename, contentType, content)
			open.err = tc.openErr
			openFn := open.fn()
			if tc.nilBody {
				fn, ct := "data.csv", "text/csv"
				openFn = func(context.Context, string, string) (document.Document, document.Object, error) {
					return document.Document{ID: open.doc.ID, Filename: &fn, DeclaredContentType: &ct}, document.Object{}, nil
				}
			}
			stub := &mcStub{enabled: true, resp: mcNouls(map[string]float64{"invoice_number": 0})}
			id := testIdentity()

			rec, raw := doCheckRequest(t, openFn, stub, nil, &id, checkReqBody(t, open.doc.ID, chkOnePlacement))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, raw)
			}
			if got := chkErrorText(t, raw); got != tc.wantMsg {
				t.Errorf("error = %q, want %q", got, tc.wantMsg)
			}
			if len(stub.calls) != 0 {
				t.Errorf("Ask calls = %d, want 0", len(stub.calls))
			}
		})
	}
}

func TestCheckMappingHandler_TheFakeDoubtsUnderItsMarkerAndLogsOneLine(t *testing.T) {
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	client, err := jev.FromEnv(logger)
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	id := testIdentity()
	header := []string{"Invoice No", "Issue Date", "Buyer TIN", "Buyer Name", "Currency", "Subtotal", "VAT", "Total", "Description", "Qty", "Unit Price", ""}
	mapping := map[string]string{
		"invoice_number": "Invoice No", "issue_date": "Issue Date", "buyer_tin": "Buyer TIN", "currency": "Currency",
		"vat": "VAT", "total": "Total", "line_quantity": "Qty", "line_unit_price": "Unit Price",
	}

	for _, tc := range []struct {
		notes string
		want  string
	}{
		{"Notes JEVFAKE-DOUBT", "{\"doubted\":[\"buyer_tin\",\"currency\",\"invoice_number\",\"issue_date\",\"line_quantity\",\"line_unit_price\",\"total\",\"vat\"]}\n"},
		{"Notes", chkEmpty},
	} {
		header[11] = tc.notes
		open := newFakeDocOpen("data.csv", "text/csv", chkCSV(t, header, 3))
		rec, raw := doCheckRequest(t, open.fn(), client, logger, &id, checkReqBody(t, open.doc.ID, mapping))
		if rec.Code != http.StatusOK || string(raw) != tc.want {
			t.Errorf("header %q: status %d body %q; want 200 %q", tc.notes, rec.Code, raw, tc.want)
		}
	}

	var calls []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		if m["msg"] == "jev call" {
			calls = append(calls, m)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("%d %q lines, want 2 (one per request): %s", len(calls), "jev call", buf.String())
	}
	for i, c := range calls {
		if c["purpose"] != "mapping_check" || c["question_count"] != float64(8) || c["outcome"] != "fake" {
			t.Errorf("line %d: purpose %v, question_count %v, outcome %v; want mapping_check, 8, fake", i, c["purpose"], c["question_count"], c["outcome"])
		}
	}
	if strings.Contains(buf.String(), "Invoice No") {
		t.Errorf("the log carries a header: %s", buf.String())
	}
}
