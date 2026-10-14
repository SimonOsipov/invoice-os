package staffjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
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

func TestListHandler_EveryJobCarriesMaxAttempts(t *testing.T) {
	rec := serveList(t, "/v1/staff/jobs?state=cancelled&state=queued", func(_ context.Context, s *string, _ *uuid.UUID) ([]Job, error) {
		if s == nil || *s != "cancelled" {
			t.Errorf("state = %v, want cancelled", s)
		}
		return []Job{{ID: "a"}, {ID: "b"}}, nil
	})
	var body struct {
		Jobs []struct {
			MaxAttempts int `json:"max_attempts"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("got %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	if len(body.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(body.Jobs))
	}
	want := submission.SubmitArgs{}.InsertOpts().MaxAttempts
	for i, j := range body.Jobs {
		if j.MaxAttempts != want || want == 0 {
			t.Errorf("job %d max_attempts = %d, want %d", i, j.MaxAttempts, want)
		}
	}
}

func TestListHandler_EmptyFiltersAreAbsent(t *testing.T) {
	var s *string
	var tn *uuid.UUID
	serveList(t, "/v1/staff/jobs?state=&tenant=", func(_ context.Context, st *string, t2 *uuid.UUID) ([]Job, error) {
		s, tn = st, t2
		return nil, nil
	})
	if s != nil || tn != nil {
		t.Errorf("empty filters = %v, %v, want nil, nil", s, tn)
	}
}

func TestGetHandler_ServerFaultIs500AndLogged(t *testing.T) {
	var logs bytes.Buffer
	req := httptest.NewRequest(http.MethodGet, "/v1/staff/jobs/abc", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	GetHandler(func(context.Context, string) (Detail, error) { return Detail{}, errors.New("boom") },
		slog.New(slog.NewTextHandler(&logs, nil))).ServeHTTP(rec, req)
	wantBody(t, rec, http.StatusInternalServerError, `{"error":"internal server error"}`)
	if !strings.Contains(logs.String(), "staffjobs: get response") {
		t.Errorf("log = %q", logs.String())
	}
}

type redriveCall struct {
	n      int
	id     string
	reason string
}

func serveRedrive(t *testing.T, body string, result Detail, err error) (*httptest.ResponseRecorder, *redriveCall) {
	t.Helper()
	call := &redriveCall{}
	h := RedriveHandler(func(_ context.Context, id, reason string) (Detail, error) {
		call.n++
		call.id, call.reason = id, reason
		return result, err
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	req := httptest.NewRequest(http.MethodPost, "/v1/staff/jobs/abc/redrive", strings.NewReader(body))
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, call
}

func TestRedriveHandler_SuccessReturnsTheDetail(t *testing.T) {
	rec, call := serveRedrive(t, `{"reason":"  APP back up "}`, Detail{Job: Job{ID: "abc", State: "queued"}}, nil)
	var body struct {
		Job     map[string]json.RawMessage `json:"job"`
		History []json.RawMessage          `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("got %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	if call.n != 1 || call.id != "abc" || call.reason != "APP back up" {
		t.Errorf("redrive call = %+v, want one call with id abc and the trimmed reason", call)
	}
	if string(body.Job["state"]) != `"queued"` || string(body.Job["max_attempts"]) != strconv.Itoa(maxAttempts) || body.History == nil {
		t.Errorf("body = %s, want the job with max_attempts and a history array", rec.Body.String())
	}
}

func TestRedriveHandler_ConflictsAre409(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		msg  string
	}{
		{"job state", ErrJobState, "only a dead_lettered job can be re-driven"},
		{"acknowledged", ErrAcknowledged, "the Access Point acknowledged this submission; re-sending may file it twice"},
		{"river duplicate", ErrRiverDuplicate, "another River job already holds this submission"},
		{"river running", ErrRiverRunning, "the previous River job is still running; retry shortly"},
		{"resolved outside", invoice.ErrResolvedOutside, "the invoice was resolved outside the system"},
		{"invoice status", invoice.StatusError{Have: invoice.StatusAccepted}, "the invoice is accepted, not failed"},
		{"wrapped", fmt.Errorf("redrive: %w", ErrJobState), "only a dead_lettered job can be re-driven"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, call := serveRedrive(t, `{"reason":"r"}`, Detail{}, tc.err)
			wantBody(t, rec, http.StatusConflict, `{"error":"`+tc.msg+`"}`)
			if call.n != 1 {
				t.Errorf("redrive calls = %d, want 1", call.n)
			}
		})
	}
}

func TestRedriveHandler_ReasonIsRequired(t *testing.T) {
	for _, reason := range []string{"", "   "} {
		rec, call := serveRedrive(t, `{"reason":"`+reason+`"}`, Detail{}, nil)
		wantBody(t, rec, http.StatusBadRequest, `{"error":"reason must be 1 to 500 characters"}`)
		if call.n != 0 {
			t.Errorf("reason %q: redrive was called", reason)
		}
	}
	rec, call := serveRedrive(t, `{}`, Detail{}, nil)
	wantBody(t, rec, http.StatusBadRequest, `{"error":"reason must be 1 to 500 characters"}`)
	if call.n != 0 {
		t.Error("a missing reason reached redrive")
	}
}

func TestRedriveHandler_ReasonBoundary(t *testing.T) {
	// "é" is two bytes: 500 runes is 1000 bytes, so a byte count would refuse it.
	ok := strings.Repeat("é", 500)
	rec, call := serveRedrive(t, `{"reason":" `+ok+` "}`, Detail{}, nil)
	if rec.Code != http.StatusOK || call.n != 1 || call.reason != ok || utf8.RuneCountInString(call.reason) != 500 {
		t.Errorf("500 runes: got %d, calls %d, reason of %d runes; want 200, 1 call with the trimmed reason", rec.Code, call.n, utf8.RuneCountInString(call.reason))
	}
	rec, call = serveRedrive(t, `{"reason":"`+ok+`é"}`, Detail{}, nil)
	wantBody(t, rec, http.StatusBadRequest, `{"error":"reason must be 1 to 500 characters"}`)
	if call.n != 0 {
		t.Error("501 runes reached redrive")
	}
}

func TestRedriveHandler_UnknownFieldIs400(t *testing.T) {
	for _, body := range []string{`{"reason":"x","force":true}`, `{"reason":"x"} {"reason":"y"}`, `{"reason":"x"} 1`, `{"reason":"x"} {}`, `{"reason":"x"} null`, `not json`, ``, `[]`, `{"reason":5}`} {
		rec, call := serveRedrive(t, body, Detail{}, nil)
		wantBody(t, rec, http.StatusBadRequest, `{"error":"invalid request body"}`)
		if call.n != 0 {
			t.Errorf("body %q reached redrive", body)
		}
	}
}

func TestRedriveHandler_OversizeBodyIs413(t *testing.T) {
	for _, body := range []string{
		`{"reason":"` + strings.Repeat("a", 9<<10) + `"}`,
		`{"reason":"r"}` + strings.Repeat(" ", 9<<10) + `{}`,
	} {
		rec, call := serveRedrive(t, body, Detail{}, nil)
		wantBody(t, rec, http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`)
		if call.n != 0 {
			t.Error("an oversize body reached redrive")
		}
	}
	// The limit is 8 KiB exactly: padding to 8192 bytes passes, one more byte is refused.
	const body = `{"reason":"r"}`
	rec, call := serveRedrive(t, body+strings.Repeat(" ", 8<<10-len(body)), Detail{}, nil)
	if rec.Code != http.StatusOK || call.n != 1 {
		t.Errorf("8192-byte body: got %d, %d calls; want 200 and 1 call", rec.Code, call.n)
	}
	rec, _ = serveRedrive(t, body+strings.Repeat(" ", 8<<10-len(body)+1), Detail{}, nil)
	wantBody(t, rec, http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`)
}

func TestRedriveHandler_NotStaffIs403AndNotFoundIs404(t *testing.T) {
	rec, _ := serveRedrive(t, `{"reason":"r"}`, Detail{}, db.ErrNotStaff)
	wantBody(t, rec, http.StatusForbidden, `{"error":"forbidden"}`)
	rec, _ = serveRedrive(t, `{"reason":"r"}`, Detail{}, ErrJobNotFound)
	wantBody(t, rec, http.StatusNotFound, `{"error":"job not found"}`)
}

func TestRedriveHandler_ServerFaultIs500AndLogged(t *testing.T) {
	var logs bytes.Buffer
	req := httptest.NewRequest(http.MethodPost, "/v1/staff/jobs/abc/redrive", strings.NewReader(`{"reason":"r"}`))
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	RedriveHandler(func(context.Context, string, string) (Detail, error) { return Detail{}, errors.New("boom") },
		slog.New(slog.NewTextHandler(&logs, nil))).ServeHTTP(rec, req)
	wantBody(t, rec, http.StatusInternalServerError, `{"error":"internal server error"}`)
	if !strings.Contains(logs.String(), "staffjobs: redrive response") {
		t.Errorf("log = %q, want the redrive response op", logs.String())
	}
}

func serveCancel(t *testing.T, body string, result Detail, err error) (*httptest.ResponseRecorder, *redriveCall) {
	t.Helper()
	call := &redriveCall{}
	h := CancelHandler(func(_ context.Context, id, reason string) (Detail, error) {
		call.n++
		call.id, call.reason = id, reason
		return result, err
	}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	req := httptest.NewRequest(http.MethodPost, "/v1/staff/jobs/abc/cancel", strings.NewReader(body))
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, call
}

func TestCancelHandler_BodyAndStatusMapping(t *testing.T) {
	const badReason = `{"error":"reason must be 1 to 500 characters"}`
	// Success first: the refusals below only mean something if a valid request reaches the store.
	rec, call := serveCancel(t, `{"reason":"  APP is down "}`, Detail{Job: Job{ID: "abc", State: "cancelled"}}, nil)
	var ok struct {
		Job     map[string]json.RawMessage `json:"job"`
		History []json.RawMessage          `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("success: got %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	if call.n != 1 || call.id != "abc" || call.reason != "APP is down" {
		t.Errorf("cancel call = %+v, want one call with id abc and the trimmed reason", call)
	}
	if string(ok.Job["state"]) != `"cancelled"` || string(ok.Job["max_attempts"]) != strconv.Itoa(maxAttempts) || ok.History == nil {
		t.Errorf("body = %s, want the job with max_attempts and a history array", rec.Body.String())
	}

	for _, tc := range []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"empty reason", `{"reason":""}`, 400, badReason},
		{"blank reason", `{"reason":"   "}`, 400, badReason},
		{"501 runes", `{"reason":"` + strings.Repeat("é", 501) + `"}`, 400, badReason},
		{"unknown field", `{"reason":"x","force":true}`, 400, `{"error":"invalid request body"}`},
		{"9 KiB", `{"reason":"` + strings.Repeat("a", 9<<10) + `"}`, 413, `{"error":"request body too large"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, call := serveCancel(t, tc.body, Detail{}, nil)
			wantBody(t, rec, tc.status, tc.want)
			if call.n != 0 {
				t.Error("a refused body reached cancel")
			}
		})
	}

	for _, tc := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"job state", ErrJobState, 409, "only a queued job can be cancelled"},
		{"wrapped job state", fmt.Errorf("cancel: %w", ErrJobState), 409, "only a queued job can be cancelled"},
		{"invoice status", invoice.StatusError{Have: invoice.StatusAccepted}, 409, "the invoice is accepted, not queued"},
		{"not found", ErrJobNotFound, 404, "job not found"},
		{"not staff", db.ErrNotStaff, 403, "forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, call := serveCancel(t, `{"reason":"r"}`, Detail{}, tc.err)
			wantBody(t, rec, tc.status, `{"error":"`+tc.want+`"}`)
			if call.n != 1 {
				t.Errorf("cancel calls = %d, want 1", call.n)
			}
		})
	}
}

// The jsonb audit payload cannot hold U+0000: a reason with one passed the length check and became a 500.
func TestReasonRoutes_RefuseAnEmbeddedNulBeforeTheStore(t *testing.T) {
	routes := map[string]func(t *testing.T, body string) (*httptest.ResponseRecorder, *redriveCall){
		"redrive": func(t *testing.T, body string) (*httptest.ResponseRecorder, *redriveCall) {
			return serveRedrive(t, body, Detail{}, nil)
		},
		"cancel": func(t *testing.T, body string) (*httptest.ResponseRecorder, *redriveCall) {
			return serveCancel(t, body, Detail{}, nil)
		},
	}
	for name, serve := range routes {
		t.Run(name, func(t *testing.T) {
			if rec, call := serve(t, `{"reason":"a b"}`); rec.Code != http.StatusOK || call.n != 1 {
				t.Errorf("a plain reason: got %d, %d calls; want 200 and 1 call", rec.Code, call.n)
			}
			for _, body := range []string{`{"reason":"a\u0000b"}`, `{"reason":"\u0000"}`, `{"reason":"\u0000x"}`, `{"reason":"x\u0000"}`} {
				rec, call := serve(t, body)
				wantBody(t, rec, http.StatusBadRequest, `{"error":"reason must be 1 to 500 characters"}`)
				if call.n != 0 {
					t.Errorf("body %s reached the store", body)
				}
			}
		})
	}
}
