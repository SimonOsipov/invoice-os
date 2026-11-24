package erp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFakeConsentPage_FormAndHeaders(t *testing.T) {
	state, _ := NewState(uuid.NewString())
	rec := httptest.NewRecorder()
	FakeConsentPage().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/erp/fake/consent?state="+url.QueryEscape(state), nil))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	for k, v := range map[string]string{
		"Cache-Control":           "no-store",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
		"Content-Type":            "text/html; charset=utf-8",
	} {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	for _, want := range []string{
		`<form method="get" action="/erp/callback">`,
		`<input type="hidden" name="state" value="` + state + `">`,
		`name="error" value="access_denied"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %s", want)
		}
	}
	opts := regexp.MustCompile(`<option value="([^"]+)">`).FindAllStringSubmatch(body, -1)
	if len(opts) != 9 {
		t.Fatalf("%d options, want 9", len(opts))
	}
	for i, c := range fakeCodes() {
		if opts[i][1] != c {
			t.Errorf("option %d = %q, want %q", i, opts[i][1], c)
		}
	}
}

func TestFakeConsentPage_StateIsEscaped(t *testing.T) {
	for _, bad := range []string{`"><script>alert(1)</script>`, "", "x.y"} {
		rec := httptest.NewRecorder()
		FakeConsentPage().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/erp/fake/consent?state="+url.QueryEscape(bad), nil))
		if rec.Code != 400 || strings.Contains(rec.Body.String(), "<script>") {
			t.Errorf("%q: %d %q", bad, rec.Code, rec.Body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%q: headers missing on the 400", bad)
		}
	}
	rec := httptest.NewRecorder()
	FakeConsentPage().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/erp/fake/consent", nil))
	if rec.Code != 405 {
		t.Errorf("POST = %d", rec.Code)
	}
}
