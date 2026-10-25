package systemhealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type srcCall struct{ rangeName, service string }

// mhRig counts what the handler reaches; the source returns want, or srcErr when set.
type mhRig struct {
	checks   int
	checkErr error
	calls    []srcCall
	srcErr   error
	want     Metrics
	logs     bytes.Buffer
}

func newMhRig() *mhRig {
	return &mhRig{want: Metrics{Range: "fixed", Service: "fixed", StepSeconds: 7, ConnectionBudget: ConnectionBudget, Panels: []PanelData{{Key: "fixed", Unit: "u", Series: []Series{}}}}}
}

func (h *mhRig) handler() http.HandlerFunc {
	return MetricsHandler(
		func(context.Context) error { h.checks++; return h.checkErr },
		func(_ context.Context, rangeName, service string) (Metrics, error) {
			h.calls = append(h.calls, srcCall{rangeName, service})
			return h.want, h.srcErr
		},
		slog.New(slog.NewTextHandler(&h.logs, nil)))
}

func (h *mhRig) get(query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.handler()(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/metrics?"+query, nil))
	return rec
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status || strings.TrimSpace(rec.Body.String()) != body {
		t.Errorf("got %d %q, want %d %s", rec.Code, rec.Body.String(), status, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func wantServed(t *testing.T, rec *httptest.ResponseRecorder, want Metrics) {
	t.Helper()
	raw, _ := json.Marshal(want)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != string(raw) {
		t.Errorf("got %d %q, want 200 %s", rec.Code, rec.Body.String(), raw)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestMetricsHandler_RefusesABadQueryBeforeAnyRead(t *testing.T) {
	const badRange, badService = `{"error":"unknown range"}`, `{"error":"unknown service"}`
	cases := []struct{ name, query, body string }{
		{"no range", "", badRange},
		{"range outside Ranges", "range=2h", badRange},
		{"range is case-sensitive", "range=1H", badRange},
		{"unknown service", "range=1h&service=nope", badService},
		{"service is case-sensitive", "range=1h&service=APP", badService},
		{"service with a capital", "range=1h&service=Submission", badService},
		{"service with a trailing space", "range=1h&service=app%20", badService},
		{"empty range", "range=", badRange},
		{"range wins over service", "range=2h&service=nope", badRange},
		{"the first range value counts", "range=2h&range=1h", badRange},
		{"the first service value counts", "range=1h&service=nope&service=app", badService},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newMhRig()
			wantBody(t, h.get(c.query), http.StatusBadRequest, c.body)
			if h.checks != 0 || len(h.calls) != 0 {
				t.Errorf("check ran %d time(s), source %d time(s), want 0 and 0 before a 400", h.checks, len(h.calls))
			}
		})
	}
	// Control: the same handler reaches both on a valid query, so the zeros above are refusals, not a dead handler.
	h := newMhRig()
	wantServed(t, h.get("range=1h"), h.want)
	if h.checks != 1 || len(h.calls) != 1 {
		t.Errorf("valid query: check ran %d time(s), source %d time(s), want 1 and 1", h.checks, len(h.calls))
	}
}

func TestMetricsHandler_AnEmptyServiceMeansAll(t *testing.T) {
	h := newMhRig()
	wantServed(t, h.get("range=1h&service="), h.want)
	if want := []srcCall{{"1h", ""}}; !slices.Equal(h.calls, want) {
		t.Errorf("source calls = %v, want %v", h.calls, want)
	}
	if h.checks != 1 {
		t.Errorf("check ran %d time(s), want 1", h.checks)
	}
}

func TestMetricsHandler_PassesEveryRangeAndKnownService(t *testing.T) {
	if len(Ranges) == 0 || len(MetricServices) == 0 {
		t.Fatalf("Ranges has %d key(s), MetricServices %d name(s): the table below would be empty", len(Ranges), len(MetricServices))
	}
	var want []srcCall
	for name := range Ranges {
		want = append(want, srcCall{name, ""})
	}
	for _, s := range MetricServices {
		want = append(want, srcCall{"1h", s})
	}
	for _, c := range want {
		t.Run(c.rangeName+"/"+c.service, func(t *testing.T) {
			h := newMhRig()
			q := "range=" + c.rangeName
			if c.service != "" {
				q += "&service=" + c.service
			}
			wantServed(t, h.get(q), h.want)
			if len(h.calls) != 1 || h.calls[0] != c {
				t.Errorf("source calls = %v, want exactly [%v]", h.calls, c)
			}
			if h.checks != 1 {
				t.Errorf("check ran %d time(s), want 1", h.checks)
			}
		})
	}
}

func TestMetricsHandler_ACheckFaultIs500AndSkipsTheSource(t *testing.T) {
	h := newMhRig()
	h.checkErr = errors.New("db down")
	wantBody(t, h.get("range=1h"), http.StatusInternalServerError, `{"error":"internal server error"}`)
	if len(h.calls) != 0 {
		t.Errorf("source ran %d time(s) after a check fault, want 0", len(h.calls))
	}
	if h.checks != 1 {
		t.Errorf("check ran %d time(s), want 1", h.checks)
	}
	if got := h.logs.String(); !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "db down") {
		t.Errorf("log = %q, want an Error line carrying the cause", got)
	}

	// Control: the refusal sentinel is a 403, not a 500.
	f := newMhRig()
	f.checkErr = db.ErrNotStaff
	wantBody(t, f.get("range=1h"), http.StatusForbidden, `{"error":"forbidden"}`)
	if len(f.calls) != 0 || f.checks != 1 {
		t.Errorf("not-staff: check ran %d time(s), source %d, want 1 and 0", f.checks, len(f.calls))
	}

	// A wrapped sentinel is still a refusal.
	w := newMhRig()
	w.checkErr = fmt.Errorf("staff check: %w", db.ErrNotStaff)
	wantBody(t, w.get("range=1h"), http.StatusForbidden, `{"error":"forbidden"}`)
	if len(w.calls) != 0 {
		t.Errorf("wrapped not-staff: source ran %d time(s), want 0", len(w.calls))
	}
}

// A fault answers 500 with a fixed body: the cause (a Google error text, a project id, a SQL message) goes to the log only.
func TestMetricsHandler_AFaultBodyCarriesNoCause(t *testing.T) {
	const google = `googleapi: Error 403: Permission monitoring.timeSeries.list denied on projects/ascomply-prod`
	cases := []struct {
		name     string
		checkErr error
		srcErr   error
		wantLog  string
	}{
		{"source fault with Google text", nil, errors.New(google), "Permission monitoring"},
		{"source fault wrapped", nil, fmt.Errorf("fetch: %w", errors.New(google)), "Permission monitoring"},
		{"joiner of an aborted fetch", nil, errFetchAborted, "fetch aborted"},
		{"check fault with SQL text", errors.New(`ERROR: permission denied for table health_samples (SQLSTATE 42501)`), nil, "SQLSTATE 42501"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newMhRig()
			h.checkErr, h.srcErr = c.checkErr, c.srcErr
			rec := h.get("range=1h")
			wantBody(t, rec, http.StatusInternalServerError, `{"error":"internal server error"}`)
			if !strings.Contains(h.logs.String(), c.wantLog) || !strings.Contains(h.logs.String(), "level=ERROR") {
				t.Errorf("log = %q, want an Error line carrying %q", h.logs.String(), c.wantLog)
			}
			if c.checkErr != nil && len(h.calls) != 0 {
				t.Errorf("source ran %d time(s) after a check fault, want 0", len(h.calls))
			}
		})
	}
}

// writeSpy records whether the handler wrote anything at all: a recorder reports 200 by default.
type writeSpy struct {
	header http.Header
	wrote  bool
}

func (w *writeSpy) Header() http.Header         { return w.header }
func (w *writeSpy) WriteHeader(int)             { w.wrote = true }
func (w *writeSpy) Write(b []byte) (int, error) { w.wrote = true; return len(b), nil }

func TestMetricsHandler_ACallerThatLeftGetsNothingWritten(t *testing.T) {
	h := newMhRig()
	h.srcErr = context.Canceled
	w := &writeSpy{header: http.Header{}}
	h.handler()(w, httptest.NewRequest(http.MethodGet, "/v1/staff/metrics?range=1h", nil))
	if h.checks != 1 || len(h.calls) != 1 {
		t.Errorf("check ran %d time(s), source %d time(s), want 1 and 1: the caller must have been served up to the source", h.checks, len(h.calls))
	}
	if w.wrote {
		t.Error("the handler wrote a status or body for a caller whose source call ended in context.Canceled, want nothing")
	}
}

// The request's own context ending is the departure, whatever error the source reports.
func TestMetricsHandler_ARequestThatEndedGetsNothingWrittenForAnyError(t *testing.T) {
	h := newMhRig()
	h.srcErr = errors.New("transport closed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &writeSpy{header: http.Header{}}
	h.handler()(w, httptest.NewRequest(http.MethodGet, "/v1/staff/metrics?range=1h", nil).WithContext(ctx))
	if h.checks != 1 || len(h.calls) != 1 {
		t.Errorf("check ran %d time(s), source %d time(s), want 1 and 1", h.checks, len(h.calls))
	}
	if w.wrote {
		t.Error("the handler wrote for a request whose context had ended, want nothing")
	}
}
