package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	healthzHandler(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("body = %q, want status ok", rec.Body.String())
	}
}

func TestReadyzNoChecks(t *testing.T) {
	rd := &readiness{}
	rec := httptest.NewRecorder()
	rd.readyzHandler(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with no checks", rec.Code)
	}
}

func TestReadyzPassing(t *testing.T) {
	rd := &readiness{}
	rd.add("db", func(context.Context) error { return nil })
	rec := httptest.NewRecorder()
	rd.readyzHandler(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when checks pass", rec.Code)
	}
}

func TestReadyzFailing(t *testing.T) {
	rd := &readiness{}
	rd.add("db", func(context.Context) error { return errors.New("connection refused") })
	rd.add("queue", func(context.Context) error { return nil })
	rec := httptest.NewRecorder()
	rd.readyzHandler(rec, httptest.NewRequest("GET", "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when a check fails", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "db") || !strings.Contains(body, "connection refused") {
		t.Errorf("body = %q, want the failing dependency + reason", body)
	}
}
