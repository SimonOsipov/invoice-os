package aicalls

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type spy struct {
	got   Filter
	calls int
	err   error
}

func (s *spy) list(_ context.Context, f Filter) (Page, error) {
	s.got, s.calls = f, s.calls+1
	return Page{}, s.err
}

func (s *spy) totals(_ context.Context, f Filter) (TotalsPage, error) {
	s.got, s.calls = f, s.calls+1
	return TotalsPage{}, s.err
}

func get(h http.Handler, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/ai-calls"+query, nil))
	return rec
}

func errMsg(rec *httptest.ResponseRecorder) string {
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body["error"]
}

func TestStaffCallsHandler_EmptyPageIsAnArray(t *testing.T) {
	rec := get(StaffCallsHandler((&spy{}).list, nil), "")
	want := `{"calls":[],"page":{"limit":25,"has_more":false,"next_cursor":null}}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("got %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
}

func TestStaffCallsHandler_PassesEveryFilter(t *testing.T) {
	tenant := uuid.NewString()
	t1, t2 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	id := uuid.NewString()
	cur := EncodeCursor(Cursor{CreatedAt: t1, ID: id})
	s := &spy{}
	h := StaffCallsHandler(s.list, nil)
	rec := get(h, "?tenant="+tenant+"&purpose=p&outcome=o&failed=true&from="+t1.Format(time.RFC3339)+"&to="+t2.Format(time.RFC3339)+"&limit=50&cursor="+cur)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	g := s.got
	if g.Tenant != tenant || g.Purpose != "p" || g.Outcome != "o" || !g.Failed || !g.From.Equal(t1) || !g.To.Equal(t2) ||
		g.Limit != 50 || g.Cursor == nil || g.Cursor.ID != id || !g.Cursor.CreatedAt.Equal(t1) {
		t.Errorf("store received %+v, want every filter", g)
	}
	get(h, "?tenant=&purpose=&outcome=&failed=&from=&to=&limit=&cursor=")
	if g := s.got; g.Tenant != "" || g.Purpose != "" || g.Outcome != "" || g.Failed || !g.From.IsZero() || !g.To.IsZero() || g.Cursor != nil || g.Limit != 25 {
		t.Errorf("empty values reached the store as %+v, want the zero filter and limit 25", g)
	}
}

func malformed(t *testing.T, h func(*spy) http.Handler, cases []struct{ query, msg string }) {
	t.Helper()
	for _, tc := range cases {
		s := &spy{}
		rec := get(h(s), "?"+tc.query)
		if rec.Code != http.StatusBadRequest || errMsg(rec) != tc.msg {
			t.Errorf("%s: got %d %s, want 400 %q", tc.query, rec.Code, rec.Body, tc.msg)
		}
		if s.calls != 0 {
			t.Errorf("%s: the store ran", tc.query)
		}
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestStaffCallsHandler_RefusesMalformedValues(t *testing.T) {
	long := strings.Repeat("x", 65)
	malformed(t, func(s *spy) http.Handler { return StaffCallsHandler(s.list, nil) }, []struct{ query, msg string }{
		{"tenant=nope", "invalid tenant"},
		{"purpose=" + long, "purpose is too long"},
		{"outcome=" + long, "outcome is too long"},
		{"failed=maybe", "failed must be true or false"},
		{"from=yesterday", "from must be an RFC3339 timestamp"},
		{"to=x", "to must be an RFC3339 timestamp"},
		{"from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z", "from must not be after to"},
		{"limit=0", "limit must be >= 1"},
		{"limit=x", "limit must be an integer"},
		{"cursor=%25%25%25", "cursor is malformed"},
		{"cursor=" + b64("2026-10-01T00:00:00Z|x"), "cursor is malformed"},
		{"cursor=" + b64("2026-10-01T00:00:00Z"), "cursor is malformed"},
		{"cursor=" + b64("yesterday|"+uuid.NewString()), "cursor is malformed"},
		{"a=%zz", "invalid query string"},
	})
}

func TestStaffCallsHandler_LimitBoundary(t *testing.T) {
	for query, want := range map[string]int{"limit=100": 100, "limit=101": 100, "limit=1": 1} {
		s := &spy{}
		get(StaffCallsHandler(s.list, nil), "?"+query)
		if s.got.Limit != want {
			t.Errorf("%s: store limit = %d, want %d", query, s.got.Limit, want)
		}
	}
}

func TestStaffCallsHandler_NotStaffIs403(t *testing.T) {
	s := &spy{err: db.ErrNotStaff}
	rec := get(StaffCallsHandler(s.list, nil), "")
	if rec.Code != http.StatusForbidden || errMsg(rec) != "forbidden" {
		t.Errorf("got %d %s, want 403 forbidden", rec.Code, rec.Body)
	}
	s.err = errors.New("boom")
	if rec := get(StaffCallsHandler(s.list, nil), ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("other error = %d, want 500", rec.Code)
	}
}

func TestCursor_RoundTripsAndRefusesGarbage(t *testing.T) {
	c := Cursor{CreatedAt: time.Date(2026, 10, 1, 12, 30, 45, 123456789, time.UTC), ID: uuid.NewString()}
	got, err := DecodeCursor(EncodeCursor(c))
	if err != nil || !got.CreatedAt.Equal(c.CreatedAt) || got.ID != c.ID {
		t.Errorf("DecodeCursor = %+v, %v, want %+v", got, err, c)
	}
	for name, raw := range map[string]string{
		"no separator": b64("2026-10-01T00:00:00Z"),
		"bad time":     b64("yesterday|" + c.ID),
		"bad uuid":     b64("2026-10-01T00:00:00Z|x"),
		"bad base64":   "%%%",
	} {
		if _, err := DecodeCursor(raw); err == nil {
			t.Errorf("DecodeCursor(%s) accepted", name)
		}
	}

	tc := TotalsCursor{Day: "2026-10-01", Name: `A|"B" Ünï`, Tenant: uuid.NewString()}
	gotT, err := DecodeTotalsCursor(EncodeTotalsCursor(tc))
	if err != nil || gotT != tc {
		t.Errorf("DecodeTotalsCursor = %+v, %v, want %+v", gotT, err, tc)
	}
	for name, raw := range map[string]string{
		"not json":   b64("nope"),
		"bad day":    b64(`{"day":"2026-13-40","name":"n","tenant":"` + tc.Tenant + `"}`),
		"bad tenant": b64(`{"day":"2026-10-01","name":"n","tenant":"x"}`),
		"bad base64": "%%%",
	} {
		if _, err := DecodeTotalsCursor(raw); err == nil {
			t.Errorf("DecodeTotalsCursor(%s) accepted", name)
		}
	}
}

func TestStaffTotalsHandler_StatusMapping(t *testing.T) {
	rec := get(StaffTotalsHandler((&spy{}).totals, nil), "")
	want := `{"totals":[],"page":{"limit":25,"has_more":false,"next_cursor":null}}`
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("got %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
	for _, tc := range []struct {
		err  error
		code int
		msg  string
	}{{db.ErrNotStaff, http.StatusForbidden, "forbidden"}, {errors.New("boom"), http.StatusInternalServerError, "internal server error"}} {
		rec := get(StaffTotalsHandler((&spy{err: tc.err}).totals, nil), "")
		if rec.Code != tc.code || errMsg(rec) != tc.msg {
			t.Errorf("%v: got %d %s, want %d %q", tc.err, rec.Code, rec.Body, tc.code, tc.msg)
		}
	}
	s := &spy{}
	rec = get(StaffTotalsHandler(s.totals, nil), "?from=2026-10-02T00:00:00Z&to=2026-10-01T00:00:00Z")
	if rec.Code != http.StatusBadRequest || errMsg(rec) != "from must not be after to" || s.calls != 0 {
		t.Errorf("inverted range: got %d %s, calls %d", rec.Code, rec.Body, s.calls)
	}
}

func TestStaffTotalsHandler_PassesEveryFilter(t *testing.T) {
	tenant := uuid.NewString()
	t1, t2 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	tc := TotalsCursor{Day: "2026-10-01", Name: "Acme", Tenant: uuid.NewString()}
	s := &spy{}
	h := StaffTotalsHandler(s.totals, nil)
	rec := get(h, "?tenant="+tenant+"&purpose=p&outcome=o&failed=true&from="+t1.Format(time.RFC3339)+"&to="+t2.Format(time.RFC3339)+"&limit=50&cursor="+EncodeTotalsCursor(tc))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	g := s.got
	if g.Tenant != tenant || g.Purpose != "p" || g.Outcome != "o" || !g.Failed || !g.From.Equal(t1) || !g.To.Equal(t2) ||
		g.Limit != 50 || g.Cursor != nil || g.TotalsCursor == nil || *g.TotalsCursor != tc {
		t.Errorf("store received %+v, want every filter and the totals cursor", g)
	}
	get(h, "")
	if g := s.got; g.Tenant != "" || g.Purpose != "" || g.Outcome != "" || g.Failed || !g.From.IsZero() || !g.To.IsZero() || g.TotalsCursor != nil || g.Limit != 25 {
		t.Errorf("no query reached the store as %+v, want the zero filter and limit 25", g)
	}
}

func TestStaffTotalsHandler_RefusesMalformedValues(t *testing.T) {
	listCursor := EncodeCursor(Cursor{CreatedAt: time.Now(), ID: uuid.NewString()})
	malformed(t, func(s *spy) http.Handler { return StaffTotalsHandler(s.totals, nil) }, []struct{ query, msg string }{
		{"failed=maybe", "failed must be true or false"},
		{"limit=0", "limit must be >= 1"},
		{"cursor=" + listCursor, "cursor is malformed"},
		{"cursor=" + b64(`{"day":"nope","name":"n","tenant":"`+uuid.NewString()+`"}`), "cursor is malformed"},
		{"cursor=" + b64(`{"day":"2026-10-01","name":"n","tenant":"x"}`), "cursor is malformed"},
		{"tenant=nope", "invalid tenant"},
	})
}

func TestStaffTotalsHandler_LimitBoundary(t *testing.T) {
	for query, want := range map[string]int{"limit=100": 100, "limit=101": 100, "limit=1": 1, "limit=9999999999": 100} {
		s := &spy{}
		rec := get(StaffTotalsHandler(s.totals, nil), "?"+query)
		if s.got.Limit != want || !strings.Contains(rec.Body.String(), `"limit":`+strconv.Itoa(want)) {
			t.Errorf("%s: store limit = %d, body %s, want %d", query, s.got.Limit, rec.Body, want)
		}
	}
}

// Postgres refuses a NUL byte and invalid UTF-8 in text, so they must be a 400 before the store.
func TestStaffHandlers_RefuseTextThatPostgresRejects(t *testing.T) {
	nulName := EncodeTotalsCursor(TotalsCursor{Day: "2026-10-01", Name: "a\x00b", Tenant: uuid.NewString()})
	cases := []struct{ query string }{{"purpose=a%00b"}, {"outcome=a%00b"}, {"purpose=%ff%fe"}, {"outcome=%ff"}}
	for _, tc := range cases {
		for name, h := range map[string]func(*spy) http.Handler{
			"calls":  func(s *spy) http.Handler { return StaffCallsHandler(s.list, nil) },
			"totals": func(s *spy) http.Handler { return StaffTotalsHandler(s.totals, nil) },
		} {
			s := &spy{}
			if rec := get(h(s), "?"+tc.query); rec.Code != http.StatusBadRequest || s.calls != 0 {
				t.Errorf("%s %s: got %d, store calls %d, want 400 and no store call", name, tc.query, rec.Code, s.calls)
			}
		}
	}
	s := &spy{}
	if rec := get(StaffTotalsHandler(s.totals, nil), "?cursor="+nulName); rec.Code != http.StatusBadRequest || s.calls != 0 {
		t.Errorf("totals cursor with a NUL name: got %d, store calls %d, want 400 and no store call", rec.Code, s.calls)
	}
}

func TestStaffHandlers_FilterLimitCountsCharactersNotBytes(t *testing.T) {
	atLimit := strings.Repeat("é", maxFilterLen)
	over := atLimit + "é"
	for _, p := range []string{"purpose", "outcome"} {
		s := &spy{}
		if rec := get(StaffCallsHandler(s.list, nil), "?"+p+"="+atLimit); rec.Code != http.StatusOK {
			t.Errorf("%s of %d characters: status %d, want 200", p, maxFilterLen, rec.Code)
		}
		rec := get(StaffCallsHandler(s.list, nil), "?"+p+"="+over)
		if rec.Code != http.StatusBadRequest || errMsg(rec) != p+" is too long" {
			t.Errorf("%s of %d characters: %d %q, want 400 too long", p, maxFilterLen+1, rec.Code, errMsg(rec))
		}
	}
}
