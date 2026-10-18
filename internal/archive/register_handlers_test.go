package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type registerSpy struct {
	calls int
	fn    func(w io.Writer, onStart func(string)) error
}

func (s *registerSpy) Register(_ context.Context, _ Request, w io.Writer, onStart func(string)) error {
	s.calls++
	if s.fn == nil {
		return nil
	}
	return s.fn(w, onStart)
}

func registerQuery(from, to string) url.Values {
	return url.Values{"entity_id": {validEntityID}, "from": {from}, "to": {to}}
}

func validRegisterQuery() url.Values { return registerQuery("2026-09-01", "2026-09-30") }

func TestRegisterHandler_UnauthenticatedIs401BeforeParsing(t *testing.T) {
	spy := &registerSpy{}
	rec := httptest.NewRecorder()
	RegisterHandler(spy.Register, slog.Default()).ServeHTTP(rec, newTestRequest(t, url.Values{"entity_id": {"not-a-uuid"}}, false))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := decodeErrorBody(t, rec.Body); got != "unauthorized" {
		t.Errorf("error = %q, want unauthorized", got)
	}
	if spy.calls != 0 {
		t.Errorf("store called %d times, want 0", spy.calls)
	}
}

func TestRegisterHandler_BadParamIs400AndSkipsTheStore(t *testing.T) {
	cases := []struct {
		name  string
		query url.Values
		want  string
	}{
		{"no entity_id", url.Values{"from": {"2026-09-01"}, "to": {"2026-09-30"}}, "entity_id is required"},
		{"bad uuid", url.Values{"entity_id": {"x"}, "from": {"2026-09-01"}, "to": {"2026-09-30"}}, "entity_id must be a well-formed uuid"},
		{"no from", url.Values{"entity_id": {validEntityID}, "to": {"2026-09-30"}}, "from is required"},
		{"no to", url.Values{"entity_id": {validEntityID}, "from": {"2026-09-01"}}, "to is required"},
		{"RFC3339 from", registerQuery("2026-09-01T00:00:00Z", "2026-09-30"), "from must be a YYYY-MM-DD date"},
		{"month 13 to", registerQuery("2026-09-01", "2026-13-01"), "to must be a YYYY-MM-DD date"},
		{"year 0 from", registerQuery("0000-01-01", "2026-09-30"), "from must be a YYYY-MM-DD date"},
		{"year 0 to", registerQuery("2026-09-01", "0000-12-31"), "to must be a YYYY-MM-DD date"},
		{"from after to", registerQuery("2026-10-01", "2026-09-30"), "from must not be after to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &registerSpy{}
			rec := httptest.NewRecorder()
			RegisterHandler(spy.Register, slog.Default()).ServeHTTP(rec, newTestRequest(t, tc.query, true))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if got := decodeErrorBody(t, rec.Body); got != tc.want {
				t.Errorf("error = %q, want %q", got, tc.want)
			}
			if spy.calls != 0 {
				t.Errorf("store called %d times, want 0", spy.calls)
			}
		})
	}
}

func TestParseRegisterRequest_AcceptsEqualDates(t *testing.T) {
	r, msg := parseRegisterRequest(registerQuery("2026-09-15", "2026-09-15"))
	if msg != "" {
		t.Fatalf("msg = %q, want none", msg)
	}
	if !r.From.Equal(r.To) || r.From.Format("2006-01-02") != "2026-09-15" {
		t.Errorf("From, To = %v, %v, want both 2026-09-15", r.From, r.To)
	}
}

func TestParseRegisterRequest_AcceptsYearOne(t *testing.T) {
	if _, msg := parseRegisterRequest(registerQuery("0001-01-01", "9999-12-31")); msg != "" {
		t.Errorf("msg = %q, want none", msg)
	}
}

func TestRegisterHandler_ErrorTable(t *testing.T) {
	raw := errors.New("pq: secret internals")
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"not found", ErrEntityNotFound, 404, "not found"},
		{"suspended", db.ErrNotActiveMember, 403, db.NotActiveMemberMessage},
		{"suspended wrapped", fmt.Errorf("tx: %w", db.ErrNotActiveMember), 403, db.NotActiveMemberMessage},
		{"no tenant", db.ErrNoTenant, 401, "unauthorized"},
		{"over cap", &TooManyInvoicesError{Count: 10001, Limit: 10000}, 400, "10001 invoices exceeds the register limit of 10000"},
		{"cell too long", &CellTooLongError{InvoiceNumber: "INV-1", Column: "rejection_reasons"}, 400, "invoice INV-1: rejection_reasons is longer than the 32767 characters an XLSX cell holds"},
		{"raw", raw, 500, "internal server error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &registerSpy{fn: func(io.Writer, func(string)) error { return tc.err }}
			rec := httptest.NewRecorder()
			RegisterHandler(spy.Register, slog.Default()).ServeHTTP(rec, newTestRequest(t, validRegisterQuery(), true))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if strings.Contains(rec.Body.String(), "secret internals") {
				t.Errorf("body leaks the raw error: %s", rec.Body.String())
			}
			if got := decodeErrorBody(t, rec.Body); got != tc.wantBody {
				t.Errorf("error = %q, want %q", got, tc.wantBody)
			}
		})
	}
}

func TestRegisterHandler_SetsXLSXHeaders(t *testing.T) {
	const name = "ASComply_invoice-register_X_20260901_20260930.xlsx"
	spy := &registerSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart(name)
		_, err := w.Write([]byte("PK\x03\x04stub"))
		return err
	}}
	rec := httptest.NewRecorder()
	RegisterHandler(spy.Register, slog.Default()).ServeHTTP(rec, newTestRequest(t, validRegisterQuery(), true))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	h := rec.Header()
	if got := h.Get("Content-Type"); got != xlsxContentType {
		t.Errorf("Content-Type = %q, want %q", got, xlsxContentType)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got, want := h.Get("Content-Disposition"), "attachment; filename="+name; got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if _, ok := h["Content-Length"]; ok {
		t.Errorf("Content-Length = %q, want none", h.Get("Content-Length"))
	}
}

func TestRegisterHandler_ErrorAfterFirstByteKeeps200(t *testing.T) {
	streamed := []byte("PK\x03\x04-partial")
	spy := &registerSpy{fn: func(w io.Writer, onStart func(string)) error {
		onStart("x.xlsx")
		if _, err := w.Write(streamed); err != nil {
			return err
		}
		return errors.New("boom")
	}}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	rec := httptest.NewRecorder()
	RegisterHandler(spy.Register, log).ServeHTTP(rec, newTestRequest(t, validRegisterQuery(), true))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), streamed) {
		t.Errorf("body = %q, want only the streamed bytes", rec.Body.Bytes())
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
		t.Errorf("error log lines = %d, want 1 (%s)", n, logs.String())
	}
}
