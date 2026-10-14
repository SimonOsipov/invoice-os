package staffjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func serveList(t *testing.T, target string, list func(context.Context, *string, *uuid.UUID) ([]Job, error)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ListHandler(list, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func serveGet(t *testing.T, get func(context.Context, string) (Detail, error)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/staff/jobs/abc", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	GetHandler(get, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))).ServeHTTP(rec, req)
	return rec
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || strings.TrimSpace(rec.Body.String()) != body {
		t.Errorf("got %d %q, want %d %q", rec.Code, rec.Body.String(), status, body)
	}
}

func TestListHandler_PassesStateAndTenantFilters(t *testing.T) {
	tenant := uuid.New()
	var gotState *string
	var gotTenant *uuid.UUID
	calls := 0
	list := func(_ context.Context, s *string, tn *uuid.UUID) ([]Job, error) {
		calls++
		gotState, gotTenant = s, tn
		return nil, nil
	}
	serveList(t, "/v1/staff/jobs?state=dead_lettered&tenant="+tenant.String(), list)
	if gotState == nil || *gotState != "dead_lettered" || gotTenant == nil || *gotTenant != tenant {
		t.Errorf("filters = %v, %v; want dead_lettered and %s", gotState, gotTenant, tenant)
	}
	serveList(t, "/v1/staff/jobs", list)
	if gotState != nil || gotTenant != nil || calls != 2 {
		t.Errorf("no query: filters = %v, %v after %d calls; want both nil after 2", gotState, gotTenant, calls)
	}
}

func TestListHandler_RefusesAnUnknownState(t *testing.T) {
	called := false
	rec := serveList(t, "/v1/staff/jobs?state=canceled", func(context.Context, *string, *uuid.UUID) ([]Job, error) {
		called = true
		return nil, nil
	})
	wantBody(t, rec, http.StatusBadRequest, `{"error":"unknown state"}`)
	if called {
		t.Error("list was called for an unknown state")
	}
}

func TestListHandler_RefusesAMalformedTenant(t *testing.T) {
	called := false
	rec := serveList(t, "/v1/staff/jobs?tenant=nope", func(context.Context, *string, *uuid.UUID) ([]Job, error) {
		called = true
		return nil, nil
	})
	wantBody(t, rec, http.StatusBadRequest, `{"error":"invalid tenant"}`)
	if called {
		t.Error("list was called for a malformed tenant")
	}
}

func TestListHandler_EmptyListIsAnArray(t *testing.T) {
	rec := serveList(t, "/v1/staff/jobs", func(context.Context, *string, *uuid.UUID) ([]Job, error) { return nil, nil })
	wantBody(t, rec, http.StatusOK, `{"jobs":[]}`)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestListHandler_NotStaffIs403(t *testing.T) {
	rec := serveList(t, "/v1/staff/jobs", func(context.Context, *string, *uuid.UUID) ([]Job, error) { return nil, db.ErrNotStaff })
	wantBody(t, rec, http.StatusForbidden, `{"error":"forbidden"}`)
}

func TestListHandler_ServerFaultIs500AndLogged(t *testing.T) {
	var logs bytes.Buffer
	rec := httptest.NewRecorder()
	ListHandler(func(context.Context, *string, *uuid.UUID) ([]Job, error) { return nil, errors.New("boom") },
		slog.New(slog.NewTextHandler(&logs, nil))).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/jobs", nil))
	wantBody(t, rec, http.StatusInternalServerError, `{"error":"internal server error"}`)
	if !strings.Contains(logs.String(), "staffjobs: list response") {
		t.Errorf("log = %q, want the list response op", logs.String())
	}
}

func TestGetHandler_NotFoundIs404(t *testing.T) {
	rec := serveGet(t, func(context.Context, string) (Detail, error) { return Detail{}, ErrJobNotFound })
	wantBody(t, rec, http.StatusNotFound, `{"error":"job not found"}`)
}

func TestGetHandler_NotStaffIs403(t *testing.T) {
	rec := serveGet(t, func(context.Context, string) (Detail, error) { return Detail{}, db.ErrNotStaff })
	wantBody(t, rec, http.StatusForbidden, `{"error":"forbidden"}`)
}

func TestGetHandler_CarriesMaxAttemptsAndHistory(t *testing.T) {
	rec := serveGet(t, func(context.Context, string) (Detail, error) {
		return Detail{Job: Job{ID: "abc", State: "dead_lettered", Attempts: 8}}, nil
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	var body struct {
		Job     map[string]json.RawMessage `json:"job"`
		History []json.RawMessage          `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	want := submission.SubmitArgs{}.InsertOpts().MaxAttempts
	if got := string(body.Job["max_attempts"]); got != strconv.Itoa(want) {
		t.Errorf("max_attempts = %s, want %d", got, want)
	}
	if string(body.Job["last_error"]) != "null" {
		t.Errorf("last_error = %s, want null", body.Job["last_error"])
	}
	if body.History == nil || len(body.History) != 0 {
		t.Errorf("history = %v, want an empty array", body.History)
	}
}
