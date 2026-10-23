package systemhealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
}

func wantServed(t *testing.T, rec *httptest.ResponseRecorder, want Metrics) {
	t.Helper()
	raw, _ := json.Marshal(want)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != string(raw) {
		t.Errorf("got %d %q, want 200 %s", rec.Code, rec.Body.String(), raw)
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
