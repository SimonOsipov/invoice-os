package platform

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
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
}
