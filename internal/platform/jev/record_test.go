package jev

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
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

func wantOneCall(t *testing.T, spy *sinkSpy) airecord.Call {
	t.Helper()
	got := spy.calls()
	if len(got) != 1 {
		t.Fatalf("sink got %d calls, want 1: %+v", len(got), got)
	}
	return got[0]
}

// recordClient is a fake-clock client against ts with a JSON logger and, when asked, a sink spy.
func recordClient(ts *testServer, withSink bool) (*Client, *bytes.Buffer, *sinkSpy) {
	buf := &bytes.Buffer{}
	c := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf))
	spy := &sinkSpy{}
	if withSink {
		c.SetRecorder(spy.sink)
	}
	return c, buf, spy
}

func fakeRecordClient(t *testing.T) (*Client, *sinkSpy) {
	t.Helper()
	c, _ := fakeClient(t)
	spy := &sinkSpy{}
	c.SetRecorder(spy.sink)
	return c, spy
}

func statusOnly(code int) func(int32, http.ResponseWriter, *http.Request) {
	return func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, code, "") }
}

func TestAsk_RecordsTheSummedCostOfAnOKAsk(t *testing.T) {
	ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			reply(w, http.StatusServiceUnavailable, `{"usage":{"input_tokens":5,"output_tokens":1,"cost":0.0004}}`)
			return
		}
		reply(w, http.StatusOK, `{"model":"typesafe/jev-2","answers":{"q1":{"type":"noul","noul":0.95}},"usage":{"input_tokens":7,"output_tokens":2,"cost":0.0006}}`)
	})
	c, buf, spy := recordClient(ts, true)
	if _, err := c.Ask(t1Ctx(t.Context()), noulReq("s")); err != nil {
		t.Fatal(err)
	}
	got := wantOneCall(t, spy)
	line := oneLine(t, buf)

	want := airecord.Call{TenantID: "T1", Client: "jev", Purpose: "value_check", Model: "typesafe/jev-2",
		InputTokens: 12, OutputTokens: 3, Cost: got.Cost, LatencyMS: got.LatencyMS, Attempts: 2, Outcome: "ok"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if d := got.Cost - 0.001; d > 1e-12 || d < -1e-12 {
		t.Errorf("Cost = %v, want 0.001", got.Cost)
	}
	if got.LatencyMS <= 0 {
		t.Errorf("LatencyMS = %d, want > 0 (one backoff on the fake clock)", got.LatencyMS)
	}
	wantNum(t, line, "latency_ms", float64(got.LatencyMS))
	wantNum(t, line, "attempts", float64(got.Attempts))
	wantStr(t, line, "outcome", got.Outcome)
}

func TestAsk_TheRecordedCostEqualsTheLoggedCost(t *testing.T) {
	ts := newServer(t, replyWith(http.StatusOK,
		`{"model":"m","answers":{"q1":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1,"cost":0.000123456}}`))
	c, buf, spy := recordClient(ts, true)
	if _, err := c.Ask(t1Ctx(t.Context()), noulReq("s")); err != nil {
		t.Fatal(err)
	}
	got := wantOneCall(t, spy)
	wantNum(t, oneLine(t, buf), "cost", got.Cost)
	if got.Cost != 0.000123456 {
		t.Errorf("Cost = %v, want 0.000123456", got.Cost)
	}
}

func TestAsk_RecordsAnUnavailableAsk(t *testing.T) {
	ts := newServer(t, statusOnly(http.StatusServiceUnavailable))
	c, _, spy := recordClient(ts, true)
	_, err := c.Ask(t1Ctx(t.Context()), noulReq("s"))
	if !errors.Is(err, ErrCheckSkipped) {
		t.Fatalf("err = %v, want ErrCheckSkipped", err)
	}
	if got := wantOneCall(t, spy); got.Outcome != "skipped_unavailable" || got.Attempts != 2 {
		t.Errorf("got %+v, want skipped_unavailable after 2 attempts", got)
	}
}

func TestAsk_RecordsAWireRefusal(t *testing.T) {
	ts := newServer(t, statusOnly(http.StatusBadRequest))
	c, _, spy := recordClient(ts, true)
	if _, err := c.Ask(t1Ctx(t.Context()), noulReq("s")); !errors.Is(err, ErrCheckSkipped) {
		t.Fatalf("err = %v, want ErrCheckSkipped", err)
	}
	if got := wantOneCall(t, spy); got.Outcome != "skipped_refused" || got.Attempts != 1 {
		t.Errorf("got %+v, want skipped_refused after 1 attempt", got)
	}
}

func TestAsk_RecordsEveryFakeOutcome(t *testing.T) {
	c, spy := fakeRecordClient(t)
	for _, state := range []string{"s", "JEVFAKE-UNAVAILABLE s", "JEVFAKE-REFUSED s"} {
		_, _ = c.Ask(t1Ctx(t.Context()), fakeReq(state))
	}
	got := spy.calls()
	if len(got) != 3 {
		t.Fatalf("sink got %d calls, want 3: %+v", len(got), got)
	}
	for i, g := range got {
		if g.Outcome != "fake" || g.Model != "" || g.Cost != 0 || g.Attempts != 1 || g.Client != "jev" {
			t.Errorf("call %d = %+v", i, g)
		}
	}
}

func TestAsk_RecordsEachPurposeAsItsString(t *testing.T) {
	c, spy := fakeRecordClient(t)
	for _, p := range []Purpose{PurposeValueCheck, PurposeDocumentType, PurposeMappingCheck} {
		req := fakeReq("s")
		req.Purpose = p
		if _, err := c.Ask(t1Ctx(t.Context()), req); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, c := range spy.calls() {
		got = append(got, c.Purpose)
	}
	if !reflect.DeepEqual(got, []string{"value_check", "document_type", "mapping_check"}) {
		t.Errorf("purposes = %v", got)
	}
}

func TestAsk_AnOffClientRecordsNothing(t *testing.T) {
	ts := newServer(t, statusOnly(http.StatusOK))
	c := clockLogClient(ts, "", newFakeClock(), nil)
	spy := &sinkSpy{}
	c.SetRecorder(spy.sink)
	if _, err := c.Ask(t1Ctx(t.Context()), noulReq("s")); !errors.Is(err, ErrCheckSkipped) {
		t.Fatalf("err = %v, want ErrCheckSkipped", err)
	}
	if n := len(spy.calls()); n != 0 || ts.hits.Load() != 0 {
		t.Errorf("sink calls %d, requests %d, want 0 and 0", n, ts.hits.Load())
	}
}

func TestAsk_AnInvalidRequestRecordsNothing(t *testing.T) {
	ts := newServer(t, statusOnly(http.StatusOK))
	c, _, spy := recordClient(ts, true)
	if _, err := c.Ask(t1Ctx(t.Context()), Request{}); !errors.Is(err, ErrCheckSkipped) {
		t.Fatalf("err = %v, want ErrCheckSkipped", err)
	}
	if n := len(spy.calls()); n != 0 || ts.hits.Load() != 0 {
		t.Errorf("sink calls %d, requests %d, want 0 and 0", n, ts.hits.Load())
	}
}

func TestAsk_TheSinkChangesNeitherResponseNorError(t *testing.T) {
	handler := func(n int32, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			reply(w, http.StatusServiceUnavailable, "")
			return
		}
		reply(w, http.StatusOK, noulOK)
	}
	run := func(withSink bool, h func(int32, http.ResponseWriter, *http.Request)) (Response, error, map[string]any) {
		ts := newServer(t, h)
		c, buf, _ := recordClient(ts, withSink)
		resp, err := c.Ask(t1Ctx(t.Context()), noulReq("s"))
		line := oneLine(t, buf)
		delete(line, "time")
		return resp, err, line
	}
	cases := map[string]func(int32, http.ResponseWriter, *http.Request){
		"ok": handler, "refused": statusOnly(http.StatusBadRequest), "unavailable": statusOnly(http.StatusServiceUnavailable),
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			r1, e1, l1 := run(true, h)
			r2, e2, l2 := run(false, h)
			if !reflect.DeepEqual(r1, r2) {
				t.Errorf("responses differ: %+v vs %+v", r1, r2)
			}
			if (e1 == nil) != (e2 == nil) || (e1 != nil && e1.Error() != e2.Error()) {
				t.Errorf("errors differ: %v vs %v", e1, e2)
			}
			if !reflect.DeepEqual(l1, l2) {
				t.Errorf("log lines differ: %v vs %v", l1, l2)
			}
		})
	}
	if r, _, _ := run(true, handler); r.Usage != (Usage{InputTokens: 318, OutputTokens: 34}) {
		t.Errorf("Usage = %+v", r.Usage)
	}
}

func TestAsk_RecordsACallerCancelledAsk(t *testing.T) {
	run := func(withSink bool) (error, *sinkSpy) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
			cancel()
			reply(w, http.StatusServiceUnavailable, "")
		})
		c, _, spy := recordClient(ts, withSink)
		_, err := c.Ask(t1Ctx(ctx), noulReq("s"))
		return err, spy
	}
	err, spy := run(true)
	plain, _ := run(false)
	if !errors.Is(err, ErrCheckSkipped) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrCheckSkipped and Canceled", err)
	}
	if plain == nil || err.Error() != plain.Error() {
		t.Errorf("err %q differs from sink-less %v", err, plain)
	}
	if got := wantOneCall(t, spy); got.Outcome != "skipped_unavailable" || got.Attempts != 1 || got.TenantID != "T1" {
		t.Errorf("got %+v", got)
	}
}

// A clock that moves on every read exposes a second now() between log and record.
func TestAsk_RecordedLatencyIsTheLoggedLatencyOnAMovingClock(t *testing.T) {
	ts := newServer(t, replyWith(http.StatusOK, noulOK))
	buf := &bytes.Buffer{}
	var tick atomic.Int64
	now := func() time.Time { return time.Unix(0, 0).Add(time.Duration(tick.Add(7)) * time.Millisecond) }
	c := newClient(config{key: "k", endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: now, sleep: testSleep}, jsonLogger(buf))
	spy := &sinkSpy{}
	c.SetRecorder(spy.sink)
	if _, err := c.Ask(t1Ctx(t.Context()), noulReq("s")); err != nil {
		t.Fatal(err)
	}
	got := wantOneCall(t, spy)
	if got.LatencyMS <= 0 {
		t.Fatalf("LatencyMS = %d, want > 0", got.LatencyMS)
	}
	wantNum(t, oneLine(t, buf), "latency_ms", float64(got.LatencyMS))
}

func TestUsage_HasExactlyItsTwoFields(t *testing.T) {
	typ := reflect.TypeOf(Usage{})
	var got []string
	for i := range typ.NumField() {
		got = append(got, typ.Field(i).Name)
	}
	if want := []string{"InputTokens", "OutputTokens"}; !slices.Equal(got, want) {
		t.Errorf("fields %v, want %v", got, want)
	}
}

func TestAsk_ACtxDoneBeforeTheFirstRequestRecordsNothing(t *testing.T) {
	run := func(withSink bool) (error, map[string]any, *sinkSpy, int32) {
		ts := newServer(t, statusOnly(http.StatusOK))
		c, buf, spy := recordClient(ts, withSink)
		ctx, cancel := context.WithCancel(t1Ctx(t.Context()))
		cancel()
		_, err := c.Ask(ctx, noulReq("s"))
		line := oneLine(t, buf)
		delete(line, "time")
		return err, line, spy, ts.hits.Load()
	}
	err, l1, spy, hits := run(true)
	plain, l2, _, _ := run(false)
	if hits != 0 {
		t.Errorf("stub saw %d requests, want 0", hits)
	}
	if n := len(spy.calls()); n != 0 {
		t.Errorf("sink got %d calls, want 0: %+v", n, spy.calls())
	}
	if !errors.Is(err, context.Canceled) || plain == nil || err.Error() != plain.Error() {
		t.Errorf("err %v differs from sink-less %v", err, plain)
	}
	if !reflect.DeepEqual(l1, l2) {
		t.Errorf("log lines differ: %v vs %v", l1, l2)
	}
}
