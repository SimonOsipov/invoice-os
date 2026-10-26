package platform

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestRequestIDGenerated(t *testing.T) {
	var gotID string
	h := requestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotID = RequestIDFromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if gotID == "" {
		t.Fatal("expected a generated request id in context")
	}
	if h := rec.Header().Get("X-Request-ID"); h != gotID {
		t.Errorf("response X-Request-ID = %q, want %q", h, gotID)
	}
}

func TestRequestIDHonorsInbound(t *testing.T) {
	var gotID string
	h := requestIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotID = RequestIDFromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "abc123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if gotID != "abc123" {
		t.Errorf("context request id = %q, want abc123", gotID)
	}
	if rec.Header().Get("X-Request-ID") != "abc123" {
		t.Error("did not echo the inbound request id")
	}
}

func TestTenantIDMiddleware(t *testing.T) {
	var got string
	h := tenantIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = TenantIDFromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Tenant-ID", "tenant-9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "tenant-9" {
		t.Errorf("tenant id in context = %q, want tenant-9", got)
	}
}

func TestTenantIDMiddlewareAbsent(t *testing.T) {
	got := "sentinel"
	h := tenantIDMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = TenantIDFromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	if got != "" {
		t.Errorf("tenant id = %q, want empty when header absent", got)
	}
}

func TestRecoveryReturns500(t *testing.T) {
	h := recoveryMiddleware(discardLogger())(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestRecoveryMiddleware_AbortHandlerLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	panicWith := func(v any) (got any) {
		h := recoveryMiddleware(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(v) }))
		defer func() { got = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		return nil
	}
	const record = `"msg":"panic recovered"`

	if got := panicWith(http.ErrAbortHandler); got != http.ErrAbortHandler {
		t.Errorf("recovered %v, want http.ErrAbortHandler re-panicked", got)
	}
	if n := strings.Count(buf.String(), record); n != 0 {
		t.Errorf("abort logged %d panic recovered records, want 0", n)
	}

	panicWith("boom")
	if n := strings.Count(buf.String(), record); n != 1 {
		t.Errorf("boom logged %d panic recovered records, want 1", n)
	}
}

func TestStatusRecorder(t *testing.T) {
	// Implicit 200 on first Write.
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	if _, err := sr.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if sr.status != http.StatusOK {
		t.Errorf("status = %d, want 200", sr.status)
	}

	// Explicit code is captured, and the first write wins.
	rec2 := httptest.NewRecorder()
	sr2 := &statusRecorder{ResponseWriter: rec2, status: http.StatusOK}
	sr2.WriteHeader(http.StatusTeapot)
	sr2.WriteHeader(http.StatusOK)
	if sr2.status != http.StatusTeapot {
		t.Errorf("status = %d, want 418 (first WriteHeader wins)", sr2.status)
	}

	// An informational 1xx is not the final status.
	sr3 := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	sr3.WriteHeader(http.StatusContinue)
	sr3.WriteHeader(http.StatusBadGateway)
	if sr3.status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 after a 100 Continue", sr3.status)
	}

	sr4 := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	sr4.WriteHeader(http.StatusContinue)
	sr4.WriteHeader(http.StatusEarlyHints)
	sr4.WriteHeader(http.StatusServiceUnavailable)
	if sr4.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 after two 1xx", sr4.status)
	}

	// A body after a 1xx is an implicit 200.
	sr5 := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	sr5.WriteHeader(http.StatusEarlyHints)
	// httptest.ResponseRecorder took the 103 as final and refuses the body; only sr5.status matters here.
	_, _ = sr5.Write([]byte("ok"))
	sr5.WriteHeader(http.StatusInternalServerError)
	if sr5.status != http.StatusOK {
		t.Errorf("status = %d, want 200 from the Write after a 103", sr5.status)
	}

	// The 1xx range ends at 199: 199 is informational, 200 is final.
	for _, tc := range []struct{ first, want int }{{199, http.StatusBadGateway}, {http.StatusOK, http.StatusOK}} {
		sr := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
		sr.WriteHeader(tc.first)
		sr.WriteHeader(http.StatusBadGateway)
		if sr.status != tc.want {
			t.Errorf("WriteHeader(%d) then 502: status = %d, want %d", tc.first, sr.status, tc.want)
		}
	}
}

func TestApp_AHandlerCanExtendItsWriteDeadline(t *testing.T) {
	app, _ := leakApp(t)
	var got error
	called := false
	app.Mux.HandleFunc("GET /v1/long", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		got = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Hour))
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	res, err := srv.Client().Get(srv.URL + "/v1/long")
	if err != nil {
		t.Fatalf("GET /v1/long: %v", err)
	}
	res.Body.Close()
	if !called {
		t.Fatal("the route never ran, so its SetWriteDeadline result proves nothing")
	}
	if got != nil {
		t.Errorf("SetWriteDeadline through the middleware chain = %v, want nil", got)
	}
}

func TestApp_AFlushedResponseKeepsItsRecordedStatus(t *testing.T) {
	app, mt := leakApp(t)
	var logs bytes.Buffer
	app.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	var flushErr error
	app.Mux.HandleFunc("GET /v1/flushed", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("partial"))
		flushErr = http.NewResponseController(w).Flush()
	})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	res, err := srv.Client().Get(srv.URL + "/v1/flushed")
	if err != nil {
		t.Fatalf("GET /v1/flushed: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if flushErr != nil {
		t.Errorf("Flush through the middleware chain = %v, want nil", flushErr)
	}
	if res.StatusCode != http.StatusBadGateway || string(body) != "partial" {
		t.Errorf("client read %d %q, want 502 %q", res.StatusCode, body, "partial")
	}
	if !strings.Contains(logs.String(), "status=502") {
		t.Errorf("request log = %q, want status=502", logs.String())
	}
	oneEvent(t, mt, "")
}
