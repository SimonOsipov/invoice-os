package sentrytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/getsentry/sentry-go"
)

// Call is one request a HeaderStub received.
type Call struct {
	Path, Query string
	Header      http.Header
}

// HeaderStub is a server that records every request's headers and answers 200 unless told otherwise.
type HeaderStub struct {
	URL   string
	mu    sync.Mutex
	calls []Call
}

// NewHeaderStub starts a stub; h, when set, writes the response.
func NewHeaderStub(t testing.TB, h http.HandlerFunc) *HeaderStub {
	t.Helper()
	s := &HeaderStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls = append(s.calls, Call{Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()})
		s.mu.Unlock()
		if h != nil {
			h(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// Calls returns a snapshot of the requests received so far.
func (s *HeaderStub) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Only fails unless the stub received exactly want requests, and returns the last.
func (s *HeaderStub) Only(t testing.TB, want int) Call {
	t.Helper()
	calls := s.Calls()
	if len(calls) != want {
		t.Fatalf("stub received %d requests, want %d", len(calls), want)
	}
	return calls[len(calls)-1]
}

// Trace fails unless the request carries exactly one well-formed sentry-trace; it returns the trace and parent span ids.
func (c Call) Trace(t testing.TB) (traceID, parentID string) {
	t.Helper()
	vals := c.Header.Values("sentry-trace")
	if len(vals) != 1 {
		t.Fatalf("request carries %d sentry-trace headers %q, want exactly 1", len(vals), vals)
	}
	parts := strings.Split(vals[0], "-")
	if len(parts) < 2 || len(parts[0]) != 32 || len(parts[1]) != 16 {
		t.Fatalf("sentry-trace %q is not <32 hex>-<16 hex>[-flag]", vals[0])
	}
	return parts[0], parts[1]
}

// AssertUntraced fails if the request carries a sentry-trace or baggage header.
func (c Call) AssertUntraced(t testing.TB) {
	t.Helper()
	for _, h := range []string{"sentry-trace", "baggage"} {
		if vals := c.Header.Values(h); len(vals) != 0 {
			t.Errorf("request carries %s %q, want none", h, vals)
		}
	}
}

// TraceID returns the trace id of a transaction event, as Sentry would receive it.
func TraceID(t testing.TB, e *sentry.Event) string {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	var wire struct {
		Contexts struct {
			Trace struct {
				TraceID string `json:"trace_id"`
			} `json:"trace"`
		} `json:"contexts"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Contexts.Trace.TraceID == "" {
		t.Fatalf("transaction %q has no contexts.trace.trace_id (err %v)", e.Transaction, err)
	}
	return wire.Contexts.Trace.TraceID
}

// ClientSpans returns the http.client spans of a transaction event.
func ClientSpans(e *sentry.Event) []*sentry.Span {
	var out []*sentry.Span
	for _, s := range e.Spans {
		if s.Op == "http.client" {
			out = append(out, s)
		}
	}
	return out
}
