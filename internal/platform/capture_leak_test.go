package platform

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
)

// leakApp serves the real chain with Sentry bound to initSentry's filtered client.
func leakApp(t *testing.T) (*App, *mockTransport) {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	return app, filteredClient(t, false)
}

// leakRequest carries every marker in its query, fragment, body and non-allowlisted headers.
func leakRequest() *http.Request {
	body := `{"buyer_tin":"` + markerTIN + `","total":"` + markerAmt + `","irn":"` + markerIRN + `","token":"` + markerCred + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/things/3f2b8c1e-6d4a-4b7e-9a1c-2e5f7d9b0a64?q="+markerTIN+"#"+markerIRN, strings.NewReader(body))
	for k, v := range map[string]string{
		"X-S2S-Token":     markerCred + "-s2s",
		"X-Gateway-Token": markerCred + "-gw",
		"Authorization":   "Bearer " + markerCred + "-authz",
		"Cookie":          "s=" + markerCred + "-cookie",
		"X-User-Id":       markerCred + "-user",
		"Referer":         "https://app.example/things?q=" + markerTIN,
		"Content-Type":    "application/json",
		"X-Request-Id":    "req-leak-1",
	} {
		r.Header.Set(k, v)
	}
	return r
}

// assertAllowlistedHeaders checks the request headers against the six-name sentryHeaders allowlist.
func assertAllowlistedHeaders(t *testing.T, req *sentry.Request) {
	t.Helper()
	allowed := map[string]bool{"Accept": true, "Content-Length": true, "Content-Type": true, "Host": true, "User-Agent": true, "X-Request-Id": true}
	for k := range req.Headers {
		if !allowed[k] {
			t.Errorf("request header %q is not on the allowlist", k)
		}
	}
	if req.Headers["X-Request-Id"] == "" {
		t.Errorf("request headers %v lack X-Request-Id", req.Headers)
	}
}

func TestPanic_RequestIsScrubbed(t *testing.T) {
	app, mt := leakApp(t)
	app.Mux.HandleFunc("POST /v1/things/{id}", func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		panic("boom")
	})
	app.Handler().ServeHTTP(httptest.NewRecorder(), leakRequest())

	ev := oneEvent(t, mt, "")
	assertNoLeak(t, mt.captured())
	if ev.Request == nil {
		t.Fatal("panic event has no request")
	}
	if ev.Request.Method != http.MethodPost {
		t.Errorf("request.method = %q, want POST", ev.Request.Method)
	}
	assertAllowlistedHeaders(t, ev.Request)
}

func TestServerError_RequestIsScrubbed(t *testing.T) {
	app, mt := leakApp(t)
	app.Mux.HandleFunc("POST /v1/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		app.Logger.ErrorContext(r.Context(), "thing: lookup", slog.Any("err", fmt.Errorf("lookup %q: not found", markerTIN)))
		w.WriteHeader(http.StatusInternalServerError)
	})
	app.Handler().ServeHTTP(httptest.NewRecorder(), leakRequest())

	ev := oneEvent(t, mt, "")
	// The cause and fingerprint must be present, or a clean result proves nothing.
	if v := exceptionValue(t, ev); !strings.Contains(v, "lookup") {
		t.Errorf("exception value = %q, want the logged cause", v)
	}
	if len(ev.Fingerprint) == 0 {
		t.Error("event has no fingerprint")
	}
	assertNoLeak(t, []*sentry.Event{ev})
	if ev.Request == nil {
		t.Fatal("5xx event has no request")
	}
	if ev.Request.Method != http.MethodPost {
		t.Errorf("request.method = %q, want POST", ev.Request.Method)
	}
	assertAllowlistedHeaders(t, ev.Request)
}
