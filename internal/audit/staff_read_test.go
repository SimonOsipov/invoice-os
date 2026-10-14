package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type staffSpy struct {
	got   audit.StaffFilter
	calls int
	page  audit.StaffPage
	err   error
}

func (s *staffSpy) search(_ context.Context, f audit.StaffFilter) (audit.StaffPage, error) {
	s.calls++
	s.got = f
	return s.page, s.err
}

func staffGet(h http.Handler, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/audit-log"+query, nil))
	return rec
}

func TestStaffSearchHandler_EmptyPageIsAnArray(t *testing.T) {
	spy := &staffSpy{}
	rec := staffGet(audit.StaffSearchHandler(spy.search, nil), "")
	want := `{"events":[],"page":{"limit":25,"has_more":false,"next_cursor":null}}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("got %d %s, want 200 %s", rec.Code, rec.Body.String(), want)
	}
}

func TestStaffSearchHandler_PassesEveryFilter(t *testing.T) {
	tenant := uuid.NewString()
	t1, t2 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	cursor := audit.EncodeCursor(t1, 7)
	spy := &staffSpy{}
	h := audit.StaffSearchHandler(spy.search, nil)

	q := url.Values{"tenant": {tenant}, "event": {"e"}, "actor": {"a"}, "from": {t1.Format(time.RFC3339)},
		"to": {t2.Format(time.RFC3339)}, "limit": {"50"}, "cursor": {cursor}}
	if rec := staffGet(h, "?"+q.Encode()); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	g := spy.got
	if g.Tenant != tenant || g.Event != "e" || g.Actor != "a" || !g.From.Equal(t1) || !g.To.Equal(t2) || g.Limit != 50 ||
		g.Cursor == nil || g.Cursor.ID != 7 || !g.Cursor.CreatedAt.Equal(t1) {
		t.Errorf("store received %+v, want every filter", g)
	}

	// An empty value is absent.
	staffGet(h, "?tenant=&event=&actor=&from=&to=&limit=&cursor=")
	if g := spy.got; g.Tenant != "" || g.Event != "" || g.Actor != "" || !g.From.IsZero() || !g.To.IsZero() || g.Cursor != nil || g.Limit != 25 {
		t.Errorf("empty values reached the store as %+v, want the zero filter and limit 25", g)
	}
}

func TestStaffSearchHandler_RefusesMalformedValues(t *testing.T) {
	long := strings.Repeat("x", 201)
	for _, tc := range []struct{ query, msg string }{
		{"tenant=nope", "invalid tenant"},
		{"event=" + long, "event is too long"},
		{"actor=" + long, "actor is too long"},
		{"from=yesterday", "from must be an RFC3339 timestamp"},
		{"to=x", "to must be an RFC3339 timestamp"},
		{"from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z", "from must not be after to"},
		{"limit=0", "limit must be >= 1"},
		{"limit=-3", "limit must be >= 1"},
		{"limit=x", "limit must be an integer"},
		{"cursor=%25%25%25", "cursor is malformed"},
	} {
		spy := &staffSpy{}
		rec := staffGet(audit.StaffSearchHandler(spy.search, nil), "?"+tc.query)
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusBadRequest || body["error"] != tc.msg {
			t.Errorf("%s: got %d %s, want 400 %q", tc.query, rec.Code, rec.Body, tc.msg)
		}
		if spy.calls != 0 {
			t.Errorf("%s: the store ran", tc.query)
		}
	}
}

func TestStaffSearchHandler_LimitBoundary(t *testing.T) {
	for query, want := range map[string]int{"limit=100": 100, "limit=101": 100, "limit=1": 1} {
		spy := &staffSpy{}
		staffGet(audit.StaffSearchHandler(spy.search, nil), "?"+query)
		if spy.got.Limit != want {
			t.Errorf("%s: store limit = %d, want %d", query, spy.got.Limit, want)
		}
	}
}

func TestStaffSearchHandler_NotStaffIs403(t *testing.T) {
	spy := &staffSpy{err: db.ErrNotStaff}
	rec := staffGet(audit.StaffSearchHandler(spy.search, nil), "")
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != `{"error":"forbidden"}` {
		t.Errorf("got %d %s, want 403 forbidden", rec.Code, rec.Body)
	}
	spy.err = errors.New("boom")
	if rec := staffGet(audit.StaffSearchHandler(spy.search, nil), ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("other error = %d, want 500", rec.Code)
	}
}

func TestStaffChangesHandler_StatusMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		body string
	}{
		{nil, http.StatusOK, `{"changes":[]}`},
		{db.ErrNotStaff, http.StatusForbidden, `{"error":"forbidden"}`},
		{errors.New("boom"), http.StatusInternalServerError, `{"error":"internal server error"}`},
	} {
		h := audit.StaffChangesHandler(func(context.Context) ([]audit.StaffChange, error) { return nil, tc.err }, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/staff-audit-log", nil))
		if rec.Code != tc.code || strings.TrimSpace(rec.Body.String()) != tc.body {
			t.Errorf("err %v: got %d %s, want %d %s", tc.err, rec.Code, rec.Body, tc.code, tc.body)
		}
	}
}
