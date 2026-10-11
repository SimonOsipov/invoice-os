package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
)

type codeSearchCall struct {
	list, q string
	limit   int
}

type fakeCodeSearch struct {
	calls []codeSearchCall
	out   []CodeEntry
	err   error
}

func (f *fakeCodeSearch) search(_ context.Context, list, q string, limit int) ([]CodeEntry, error) {
	f.calls = append(f.calls, codeSearchCall{list, q, limit})
	return f.out, f.err
}

// codeSearchGet drives the handler through the route pattern so {list} resolves.
func codeSearchGet(t *testing.T, f *fakeCodeSearch, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /v1/code-lists/{list}", CodeSearchHandler(f.search, nil))
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(auth.WithIdentity(req.Context(),
		auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: "00000000-0000-4000-8000-000000000001"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func codeSearchErrorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return got.Error
}

func TestCodeSearchHandler_UnknownListIs404(t *testing.T) {
	f := &fakeCodeSearch{}
	rec := codeSearchGet(t, f, "/v1/code-lists/bogus?q=a")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := codeSearchErrorOf(t, rec); got != "unknown code list" {
		t.Errorf("error = %q, want %q", got, "unknown code list")
	}
	if len(f.calls) != 0 {
		t.Errorf("search called %d times for an unknown list, want 0", len(f.calls))
	}

	// A registry list the search does not serve is unknown too.
	if rec := codeSearchGet(t, f, "/v1/code-lists/currencies?q=a"); rec.Code != http.StatusNotFound {
		t.Errorf("currencies: status = %d, want 404", rec.Code)
	}
	// Control: an allowed list reaches the search.
	if rec := codeSearchGet(t, f, "/v1/code-lists/states?q=a"); rec.Code != http.StatusOK || len(f.calls) != 1 || f.calls[0].list != "states" {
		t.Errorf("states control: status %d, calls %+v, want 200 and one call for states", rec.Code, f.calls)
	}
}

func TestCodeSearchHandler_LimitRules(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		status      int
		msg         string
		wantLimit   int
	}{
		{"absent defaults to 20", "q=a", http.StatusOK, "", 20},
		{"explicit 7", "q=a&limit=7", http.StatusOK, "", 7},
		{"50 is allowed", "q=a&limit=50", http.StatusOK, "", 50},
		{"500 clamps to 50", "q=a&limit=500", http.StatusOK, "", 50},
		{"not an integer", "q=a&limit=abc", http.StatusBadRequest, "limit must be an integer", 0},
		{"zero", "q=a&limit=0", http.StatusBadRequest, "limit must be >= 1", 0},
		{"negative", "q=a&limit=-3", http.StatusBadRequest, "limit must be >= 1", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeCodeSearch{}
			rec := codeSearchGet(t, f, "/v1/code-lists/states?"+tc.query)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status != http.StatusOK {
				if got := codeSearchErrorOf(t, rec); got != tc.msg {
					t.Errorf("error = %q, want %q", got, tc.msg)
				}
				if len(f.calls) != 0 {
					t.Errorf("search called %d times on a 400, want 0", len(f.calls))
				}
				return
			}
			if len(f.calls) != 1 || f.calls[0].limit != tc.wantLimit {
				t.Errorf("search calls = %+v, want one call with limit %d", f.calls, tc.wantLimit)
			}
		})
	}
}

func TestCodeSearchHandler_LongQueryIs400(t *testing.T) {
	f := &fakeCodeSearch{}
	rec := codeSearchGet(t, f, "/v1/code-lists/states?q="+strings.Repeat("a", 101))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("101 chars: status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := codeSearchErrorOf(t, rec); got != "q must be at most 100 characters" {
		t.Errorf("error = %q, want %q", got, "q must be at most 100 characters")
	}
	if len(f.calls) != 0 {
		t.Errorf("search called %d times on a 400, want 0", len(f.calls))
	}

	q := strings.Repeat("a", 100)
	rec = codeSearchGet(t, f, "/v1/code-lists/states?q="+q)
	if rec.Code != http.StatusOK {
		t.Fatalf("100 chars: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.calls) != 1 || f.calls[0].q != q {
		t.Errorf("search calls = %+v, want one call with the 100-char q", f.calls)
	}
	// An empty result is [] on the wire, never null.
	if got := strings.TrimSpace(rec.Body.String()); got != `{"codes":[]}` {
		t.Errorf("body = %s, want {\"codes\":[]}", got)
	}

	// The cap counts characters, not bytes, after trimming.
	multi := strings.Repeat("\u00e9", 100)
	for name, tc := range map[string]struct {
		target string
		status int
		wantQ  string
	}{
		"100 two-byte characters":        {"q=" + url.QueryEscape(multi), http.StatusOK, multi},
		"101 two-byte characters":        {"q=" + url.QueryEscape(multi+"\u00e9"), http.StatusBadRequest, ""},
		"100 characters in padding":      {"q=" + url.QueryEscape("  "+q+"  "), http.StatusOK, q},
		"whitespace only searches blank": {"q=" + url.QueryEscape(" \t "), http.StatusOK, ""},
		"inner space is kept":            {"q=" + url.QueryEscape(" a b "), http.StatusOK, "a b"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeCodeSearch{}
			rec := codeSearchGet(t, f, "/v1/code-lists/states?"+tc.target)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status != http.StatusOK {
				if len(f.calls) != 0 {
					t.Errorf("search called %d times on a 400, want 0", len(f.calls))
				}
				return
			}
			if len(f.calls) != 1 || f.calls[0].q != tc.wantQ {
				t.Errorf("search calls = %+v, want one call with q %q", f.calls, tc.wantQ)
			}
		})
	}
}

// A malformed q is the caller's mistake: 400, never a 500 that pages Sentry.
func TestCodeSearchHandler_UnstorableQueryIs400(t *testing.T) {
	for name, q := range map[string]string{"NUL byte": "a%00b", "invalid UTF-8": "%ff", "truncated UTF-8": "%C3"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeCodeSearch{}
			rec := codeSearchGet(t, f, "/v1/code-lists/states?q="+q)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			if len(f.calls) != 0 {
				t.Errorf("search called %d times on a 400, want 0", len(f.calls))
			}
		})
	}
	// Control: a well-formed multi-byte q reaches the search.
	f := &fakeCodeSearch{}
	if rec := codeSearchGet(t, f, "/v1/code-lists/states?q=%C3%A9"); rec.Code != http.StatusOK || len(f.calls) != 1 {
		t.Errorf("control: status %d, calls %+v, want 200 and one call", rec.Code, f.calls)
	}
}

func TestCodeSearchHandler_500LogsTheOperationNotTheQuery(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeCodeSearch{err: errors.New("boom")}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/code-lists/{list}", CodeSearchHandler(f.search, slog.New(slog.NewTextHandler(&buf, nil))))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/code-lists/lgas?q=SECRETNEEDLE", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	logged := buf.String()
	if !strings.Contains(logged, "code_search") || !strings.Contains(logged, "lgas") {
		t.Errorf("log %q lacks the operation name and list", logged)
	}
	if strings.Contains(logged, "SECRETNEEDLE") {
		t.Errorf("log carries q: %s", logged)
	}

	// A 4xx domain refusal is not logged as an error.
	buf.Reset()
	f.err = db.ErrNotActiveMember
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/code-lists/lgas?q=a", nil))
	if buf.Len() != 0 {
		t.Errorf("a 403 logged %q, want nothing", buf.String())
	}
}

func TestCodeSearchHandler_StoreErrorIs500(t *testing.T) {
	f := &fakeCodeSearch{err: errors.New("boom: connection reset")}
	rec := codeSearchGet(t, f, "/v1/code-lists/states?q=a")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := codeSearchErrorOf(t, rec); got != "internal server error" {
		t.Errorf("error = %q, want %q", got, "internal server error")
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("body leaks the store error: %s", rec.Body.String())
	}
	// Control: the same request succeeds when the store does.
	ok := &fakeCodeSearch{out: []CodeEntry{{Code: "NG-LA", Label: "Lagos"}}}
	rec = codeSearchGet(t, ok, "/v1/code-lists/states?q=a")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":"NG-LA"`) {
		t.Errorf("control: status %d body %s, want 200 with NG-LA", rec.Code, rec.Body.String())
	}
}

func TestCodeSearchLists_AreRegistryLists(t *testing.T) {
	want := []string{"states", "lgas", "hs-codes", "services-codes", "invoice-quantity-codes", "tax-categories"}
	if len(codeSearchLists) != len(want) {
		t.Fatalf("codeSearchLists has %d names %v, want the %d of D2: %v", len(codeSearchLists), codeSearchLists, len(want), want)
	}
	registry := map[string]bool{}
	for _, l := range codelist.Lists {
		registry[l.Name] = true
	}
	for _, name := range codeSearchLists {
		if !registry[name] {
			t.Errorf("%q is not a registry list in codelist.Lists", name)
		}
	}
	for _, name := range want {
		if !slices.Contains(codeSearchLists, name) {
			t.Errorf("codeSearchLists lacks %q", name)
		}
	}
}
