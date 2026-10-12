package ai

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

type sinkSpy struct {
	mu  sync.Mutex
	got []airecord.Call
}

func (s *sinkSpy) sink(c airecord.Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, c)
}

func (s *sinkSpy) calls() []airecord.Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]airecord.Call(nil), s.got...)
}

func t1Ctx(ctx context.Context) context.Context {
	return auth.WithIdentity(ctx, auth.Identity{Subject: "u1", TenantID: "T1"})
}

// loggedClient is a fake-clock client against url with a JSON logger and a sink spy.
func loggedClient(t *testing.T, url string, withSink bool) (*Client, *bytes.Buffer, *sinkSpy) {
	t.Helper()
	buf := &bytes.Buffer{}
	fc := &fakeClock{t: time.Unix(0, 0)}
	c := newClient(config{key: "k", endpoint: url, budget: 15 * time.Second, now: fc.now, sleep: fc.sleep},
		slog.New(slog.NewJSONHandler(buf, nil)))
	spy := &sinkSpy{}
	if withSink {
		c.SetRecorder(spy.sink)
	}
	return c, buf, spy
}

func wantOneCall(t *testing.T, spy *sinkSpy) airecord.Call {
	t.Helper()
	got := spy.calls()
	if len(got) != 1 {
		t.Fatalf("sink got %d calls, want 1: %+v", len(got), got)
	}
	return got[0]
}

func statusServer(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCall_RecordsTheSummedUsageOfAnOKCall(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":2,"cost":0.001}}`))
			return
		}
		_, _ = w.Write([]byte(envelope(validContent, `{"prompt_tokens":20,"completion_tokens":5,"cost":0.002}`)))
	}))
	t.Cleanup(srv.Close)

	c, buf, spy := loggedClient(t, srv.URL, true)
	if _, err := c.Call(t1Ctx(t.Context()), baseReq()); err != nil {
		t.Fatal(err)
	}
	got := wantOneCall(t, spy)
	line := only(t, buf)

	want := airecord.Call{TenantID: "T1", Client: "ai", Purpose: "document", Model: Model,
		InputTokens: 30, OutputTokens: 7, Cost: 0.003, LatencyMS: got.LatencyMS, Attempts: 2, Outcome: "ok"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.LatencyMS <= 0 {
		t.Errorf("LatencyMS = %d, want > 0 (one backoff on the fake clock)", got.LatencyMS)
	}
	if line["latency_ms"] != float64(got.LatencyMS) {
		t.Errorf("log latency_ms = %v, recorded %d", line["latency_ms"], got.LatencyMS)
	}
	if line["attempts"] != float64(got.Attempts) || line["outcome"] != got.Outcome {
		t.Errorf("log %v disagrees with recorded %+v", line, got)
	}
}

func TestCall_RecordsAnUnavailableCall(t *testing.T) {
	srv, _ := statusServer(t, http.StatusServiceUnavailable)
	c, _, spy := loggedClient(t, srv.URL, true)
	_, err := c.Call(t1Ctx(t.Context()), baseReq())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	got := wantOneCall(t, spy)
	if got.Outcome != "unavailable" || got.Attempts < 2 {
		t.Errorf("got %+v, want unavailable with attempts >= 2", got)
	}
}

func TestCall_RecordsAWireRefusal(t *testing.T) {
	srv, hits := statusServer(t, http.StatusBadRequest)
	c, _, spy := loggedClient(t, srv.URL, true)
	if _, err := c.Call(t1Ctx(t.Context()), baseReq()); err == nil {
		t.Fatal("err = nil")
	}
	got := wantOneCall(t, spy)
	if got.Outcome != "refused" || got.Attempts != 1 || hits.Load() != 1 {
		t.Errorf("got %+v hits %d, want refused after one request", got, hits.Load())
	}
}

func fakeWithSink(t *testing.T) (*Client, *sinkSpy) {
	t.Helper()
	c := fakeModeClient(t)
	spy := &sinkSpy{}
	c.SetRecorder(spy.sink)
	return c, spy
}

func TestCall_RecordsAFakeCall(t *testing.T) {
	c, spy := fakeWithSink(t)
	req := baseReq()
	req.Text = "invoice 123"
	noDials(t, func() { _, _ = c.Call(t1Ctx(t.Context()), req) })
	got := wantOneCall(t, spy)
	if got.Outcome != "fake" || got.Attempts != 1 || got.InputTokens != 0 || got.OutputTokens != 0 || got.Cost != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestCall_RecordsAFakeUnavailableCall(t *testing.T) {
	c, spy := fakeWithSink(t)
	req := baseReq()
	req.Text = markerUnavailable
	var err error
	noDials(t, func() { _, err = c.Call(t1Ctx(t.Context()), req) })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if got := wantOneCall(t, spy); got.Outcome != "fake" {
		t.Errorf("outcome = %q, want fake", got.Outcome)
	}
}

func TestCall_RecordsEachPurposeAsItsString(t *testing.T) {
	c, spy := fakeWithSink(t)
	purposes := []Purpose{PurposeDocument, PurposeSpreadsheet, PurposeLineItems}
	noDials(t, func() {
		for _, p := range purposes {
			req := baseReq()
			req.Purpose = p
			if _, err := c.Call(t1Ctx(t.Context()), req); err != nil {
				t.Fatal(err)
			}
		}
	})
	var got []string
	for _, c := range spy.calls() {
		got = append(got, c.Purpose)
	}
	if !reflect.DeepEqual(got, []string{"document", "spreadsheet", "line_items"}) {
		t.Errorf("purposes = %v", got)
	}
}

func TestCall_AnOffClientRecordsNothing(t *testing.T) {
	srv, hits := statusServer(t, http.StatusOK)
	spy := &sinkSpy{}
	c := newClient(config{endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep}, nil)
	c.SetRecorder(spy.sink)
	if _, err := c.Call(t1Ctx(t.Context()), baseReq()); !errors.Is(err, ErrOff) {
		t.Fatalf("err = %v, want ErrOff", err)
	}
	if n := len(spy.calls()); n != 0 || hits.Load() != 0 {
		t.Errorf("sink calls %d, requests %d, want 0 and 0", n, hits.Load())
	}
}

func TestCall_AnInvalidRequestRecordsNothing(t *testing.T) {
	srv, hits := statusServer(t, http.StatusOK)
	c, _, spy := loggedClient(t, srv.URL, true)
	req := baseReq()
	req.SchemaName = ""
	if _, err := c.Call(t1Ctx(t.Context()), req); err == nil {
		t.Fatal("err = nil")
	}
	if n := len(spy.calls()); n != 0 || hits.Load() != 0 {
		t.Errorf("sink calls %d, requests %d, want 0 and 0", n, hits.Load())
	}
}

func TestCall_TheSinkChangesNeitherAnswerNorError(t *testing.T) {
	okAfterRetry := func(w http.ResponseWriter, n int32) {
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(envelope(validContent, `{"prompt_tokens":3,"completion_tokens":1,"cost":0.5}`)))
	}
	cases := map[string]func(w http.ResponseWriter, n int32){
		"ok":          okAfterRetry,
		"refused":     func(w http.ResponseWriter, _ int32) { w.WriteHeader(http.StatusBadRequest) },
		"unavailable": func(w http.ResponseWriter, _ int32) { w.WriteHeader(http.StatusServiceUnavailable) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			run := func(withSink bool) (map[string]any, error, map[string]any) {
				var n atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h(w, n.Add(1)) }))
				t.Cleanup(srv.Close)
				c, buf, _ := loggedClient(t, srv.URL, withSink)
				ans, err := c.Call(t1Ctx(t.Context()), baseReq())
				line := only(t, buf)
				delete(line, "latency_ms")
				delete(line, "time")
				return ans, err, line
			}
			a1, e1, l1 := run(true)
			a2, e2, l2 := run(false)
			if !reflect.DeepEqual(a1, a2) {
				t.Errorf("answers differ: %v vs %v", a1, a2)
			}
			if (e1 == nil) != (e2 == nil) || (e1 != nil && (e1.Error() != e2.Error() ||
				errors.Is(e1, ErrUnavailable) != errors.Is(e2, ErrUnavailable))) {
				t.Errorf("errors differ: %v vs %v", e1, e2)
			}
			if !reflect.DeepEqual(l1, l2) {
				t.Errorf("log lines differ: %v vs %v", l1, l2)
			}
		})
	}
}

func TestCall_NoRecorderIsTodaysBehaviour(t *testing.T) {
	srv, _ := okServer(t, validContent)
	c, buf, _ := loggedClient(t, srv.URL, false)
	if _, err := c.Call(t1Ctx(t.Context()), baseReq()); err != nil {
		t.Fatal(err)
	}
	if line := only(t, buf); line["msg"] != "ai call" {
		t.Errorf("log line = %v", line)
	}
}

func TestCall_RecordsACallerCancelledCall(t *testing.T) {
	run := func(withSink bool) (error, *sinkSpy) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cancel()
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)
		c, _, spy := loggedClient(t, srv.URL, withSink)
		_, err := c.Call(t1Ctx(ctx), baseReq())
		return err, spy
	}
	err, spy := run(true)
	plain, _ := run(false)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want Canceled and not ErrUnavailable", err)
	}
	if plain == nil || err.Error() != plain.Error() {
		t.Errorf("err %q differs from sink-less %v", err, plain)
	}
	got := wantOneCall(t, spy)
	if got.Outcome != "unavailable" || got.Attempts != 1 || got.TenantID != "T1" {
		t.Errorf("got %+v", got)
	}
}
