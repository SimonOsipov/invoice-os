package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// -- helpers --

// contractKeys is the line's attributes in emission order, after time, level and msg.
var contractKeys = []string{"tenant_id", "purpose", "question_count", "input_tokens", "latency_ms", "attempts", "outcome"}

func jsonLogger(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewJSONHandler(buf, nil)) }

func logLines(buf *bytes.Buffer) []string {
	raw := strings.TrimRight(buf.String(), "\n")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// rawLine returns the single raw line in buf, failing unless there is exactly one.
func rawLine(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	ls := logLines(buf)
	if len(ls) != 1 {
		t.Fatalf("got %d log line(s), want 1: %q", len(ls), ls)
	}
	return ls[0]
}

func oneLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	raw := rawLine(t, buf)
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("malformed log line %q: %v", raw, err)
	}
	return m
}

// attrKeys reads the line's top-level keys in written order, minus time, level
// and msg. A decoded map would lose the order.
func attrKeys(t *testing.T, raw string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("log line %q is not a JSON object", raw)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		if k := tok.(string); k != "time" && k != "level" && k != "msg" {
			keys = append(keys, k)
		}
	}
	return keys
}

func wantStr(t *testing.T, line map[string]any, key, want string) {
	t.Helper()
	if got, ok := line[key].(string); !ok || got != want {
		t.Errorf("%s = %v, want %q", key, line[key], want)
	}
}

func wantNum(t *testing.T, line map[string]any, key string, want float64) {
	t.Helper()
	if got, ok := line[key].(float64); !ok || got != want {
		t.Errorf("%s = %v, want %v", key, line[key], want)
	}
}

func replyWith(code int, body string) func(int32, http.ResponseWriter, *http.Request) {
	return func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, code, body) }
}

func clockLogClient(ts *testServer, key string, fc *fakeClock, logger *slog.Logger) *Client {
	return newClient(config{key: key, endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, logger)
}

// captureHandler records the ctx each Handle receives.
type captureHandler struct {
	slog.Handler
	mu   sync.Mutex
	ctxs []context.Context
}

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	h.ctxs = append(h.ctxs, ctx)
	h.mu.Unlock()
	return h.Handler.Handle(ctx, r)
}

// -- AC-10 --

func TestLog_OneLinePerCallAcrossRetries(t *testing.T) {
	ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			reply(w, http.StatusServiceUnavailable, "")
			return
		}
		reply(w, http.StatusOK, noulOK)
	})
	buf := &bytes.Buffer{}
	c := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf))

	if _, err := c.Ask(t.Context(), noulReq("s")); err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	wantHits(t, ts, 2)
	line := oneLine(t, buf)
	wantNum(t, line, "attempts", 2)
}

// -- AC-11 --

func TestLog_KeysAreExactlyTheContract(t *testing.T) {
	ts := newServer(t, replyWith(http.StatusOK, threeTypeOK))
	buf := &bytes.Buffer{}
	c := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf))
	ctx := auth.WithIdentity(t.Context(), auth.Identity{Subject: "u", Role: "authenticated", TenantID: "t-1"})

	if _, err := c.Ask(ctx, threeTypeReq()); err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	raw := rawLine(t, buf)
	if got := attrKeys(t, raw); !slices.Equal(got, contractKeys) {
		t.Errorf("keys = %v, want %v in that order", got, contractKeys)
	}
	line := oneLine(t, bytes.NewBufferString(raw))
	wantStr(t, line, "msg", "jev call")
	wantStr(t, line, "level", "INFO")
	if _, ok := line["time"]; !ok {
		t.Error("time is missing")
	}
	wantStr(t, line, "purpose", "document_type")
	wantNum(t, line, "question_count", 3)
}

func TestLog_TenantFromIdentity(t *testing.T) {
	ts := newServer(t, replyWith(http.StatusOK, noulOK))
	buf := &bytes.Buffer{}
	c := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf))
	ctx := auth.WithIdentity(t.Context(), auth.Identity{TenantID: "t-9"})

	if _, err := c.Ask(ctx, noulReq("s")); err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	raw := rawLine(t, buf)
	wantStr(t, oneLine(t, bytes.NewBufferString(raw)), "tenant_id", "t-9")
	if n := strings.Count(raw, `"tenant_id"`); n != 1 {
		t.Errorf(`line holds %d "tenant_id" keys, want 1`, n)
	}
}

func TestLog_NoIdentityOmitsTenant(t *testing.T) {
	cases := []struct {
		name string
		ctx  func(t *testing.T) context.Context
	}{
		{"no_identity", func(t *testing.T) context.Context { return t.Context() }},
		{"identity_empty_tenant", func(t *testing.T) context.Context {
			return auth.WithIdentity(t.Context(), auth.Identity{Subject: "u", TenantID: ""})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newServer(t, replyWith(http.StatusOK, noulOK))
			buf := &bytes.Buffer{}
			c := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf))

			if _, err := c.Ask(tc.ctx(t), noulReq("s")); err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			raw := rawLine(t, buf)
			if got := attrKeys(t, raw); !slices.Equal(got, contractKeys[1:]) {
				t.Errorf("keys = %v, want %v: tenant_id omitted, never blank", got, contractKeys[1:])
			}
		})
	}
}

// -- AC-12 --

func TestLog_OutcomePerPath(t *testing.T) {
	wire := func(key string, h func(int32, http.ResponseWriter, *http.Request), req func() Request) func(*testing.T, *slog.Logger) {
		return func(t *testing.T, l *slog.Logger) {
			ts := newServer(t, h)
			_, _ = askWithin(t, clockLogClient(ts, key, newFakeClock(), l), t.Context(), req(), 2*time.Second)
		}
	}
	fake := func(req func() Request) func(*testing.T, *slog.Logger) {
		return func(t *testing.T, l *slog.Logger) {
			ts := noSendServer(t)
			_, _ = askFake(t, newClient(fakeConfig(t, ts), l), ts, req())
		}
	}
	valid := func() Request { return noulReq("s") }
	invalid := func() Request {
		req := threeTypeReq()
		req.Purpose = "bogus"
		return req
	}
	fakeState := func(state string) func() Request { return func() Request { return fakeReq(state) } }

	cases := []struct {
		name         string
		run          func(*testing.T, *slog.Logger)
		wantOutcome  string
		wantAttempts float64
	}{
		{"ok", wire("k", replyWith(200, noulOK), valid), "ok", 1},
		{"503_twice", wire("k", replyWith(503, ""), valid), "skipped_unavailable", 2},
		{"malformed_200", wire("k", replyWith(200, "not json"), valid), "skipped_unavailable", 1},
		{"401", wire("k", replyWith(401, ""), valid), "skipped_refused", 1},
		{"302_without_location", wire("k", func(_ int32, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusFound)
		}, valid), "skipped_refused", 1},
		{"invalid_request", wire("k", replyWith(200, threeTypeOK), invalid), "skipped_refused", 0},
		{"invalid_request_fake", fake(invalid), "skipped_refused", 0},
		{"key_with_a_control_byte", wire("k\n", replyWith(200, noulOK), valid), "skipped_refused", 0},
		{"off", wire("", replyWith(200, noulOK), valid), "off", 0},
		{"fake", fake(fakeState("s")), "fake", 1},
		{"fake_unavailable_marker", fake(fakeState(markerUnavailable)), "fake", 1},
		{"fake_refused_marker", fake(fakeState(markerRefused)), "fake", 1},
		{"cancelled_during_the_call", func(t *testing.T, l *slog.Logger) {
			var ts *testServer
			ts = newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) { ts.hang(w, r) })
			c := newClient(config{key: "k", endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: time.Now, sleep: testSleep}, l)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			time.AfterFunc(50*time.Millisecond, cancel)
			_, _ = askWithin(t, c, ctx, valid(), 2*time.Second)
		}, "skipped_unavailable", 1},
		// One RoundTrip enters the transport before the cancelled ctx stops it; nothing reaches the server.
		{"cancelled_before_ask", func(t *testing.T, l *slog.Logger) {
			ts := newServer(t, replyWith(200, noulOK))
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, _ = askWithin(t, clockLogClient(ts, "k", newFakeClock(), l), ctx, valid(), 2*time.Second)
			wantHits(t, ts, 0)
		}, "skipped_unavailable", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			tc.run(t, jsonLogger(buf))

			line := oneLine(t, buf)
			wantStr(t, line, "outcome", tc.wantOutcome)
			wantNum(t, line, "attempts", tc.wantAttempts)
		})
	}
}

// -- AC-13 --

func TestLog_QuestionCountAndTokensAreTheCallsOwn(t *testing.T) {
	t.Run("retried_call", func(t *testing.T) {
		ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
			if n == 1 {
				reply(w, http.StatusInternalServerError, `{"usage":{"input_tokens":7,"output_tokens":1}}`)
				return
			}
			reply(w, http.StatusOK, `{"answers":{"n":{"type":"noul","noul":0.9},"c":{"type":"choice","choice":"receipt","confidence":0.9},"s":{"type":"score","score":2,"confidence":0.9}},"usage":{"input_tokens":318,"output_tokens":34}}`)
		})
		buf := &bytes.Buffer{}
		c := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf))

		if _, err := c.Ask(t.Context(), threeTypeReq()); err != nil {
			t.Fatalf("Ask() err = %v, want nil", err)
		}
		wantHits(t, ts, 2)
		line := oneLine(t, buf)
		wantNum(t, line, "question_count", 3)
		wantNum(t, line, "input_tokens", 325)
	})

	// No answers come back, so a count of answers instead of questions reads 0.
	t.Run("refused_call", func(t *testing.T) {
		ts := newServer(t, replyWith(http.StatusUnprocessableEntity, ""))
		buf := &bytes.Buffer{}
		c := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf))

		if _, err := c.Ask(t.Context(), threeTypeReq()); err == nil {
			t.Fatal("Ask() err = nil, want refused")
		}
		line := oneLine(t, buf)
		wantNum(t, line, "question_count", 3)
		wantNum(t, line, "input_tokens", 0)
	})
}

func TestLog_LatencyCoversTheRetryWait(t *testing.T) {
	fc := newFakeClock()
	ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
		fc.advance(100 * time.Millisecond)
		if n == 1 {
			reply(w, http.StatusServiceUnavailable, "")
			return
		}
		reply(w, http.StatusOK, noulOK)
	})
	buf := &bytes.Buffer{}
	c := clockLogClient(ts, "k", fc, jsonLogger(buf))

	if _, err := c.Ask(t.Context(), noulReq("s")); err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	if got := fc.sleepCalls(); !slices.Equal(got, []time.Duration{retryWait}) {
		t.Fatalf("sleeps = %v, want [%v]", got, retryWait)
	}
	line := oneLine(t, buf)
	wantNum(t, line, "latency_ms", 450)
}

// -- AC-14 --

func TestLog_CarriesNoContent(t *testing.T) {
	req := func() Request {
		return Request{Purpose: PurposeDocumentType, State: "STATESENTINEL", Questions: map[string]Question{
			"n": {Type: TypeNoul, Instructions: "INSTRSENTINELN", True: "TRUESENTINEL", False: "FALSESENTINEL"},
			"c": {Type: TypeChoice, Instructions: "INSTRSENTINELC", Options: []Option{
				{"OPTNAMESENTINEL", "OPTDESCSENTINELA"}, {"ANSWERSENTINEL", "OPTDESCSENTINELB"},
			}, Default: "OPTNAMESENTINEL"},
			"s": {Type: TypeScore, Instructions: "INSTRSENTINELS", Options: []Option{
				{"LEVELNAMESENTINEL0", "LEVELDESCSENTINEL0"}, {"LEVELNAMESENTINEL1", "LEVELDESCSENTINEL1"},
			}, Default: "LEVELNAMESENTINEL1"},
		}}
	}
	buf := &bytes.Buffer{}
	logger := jsonLogger(buf)

	refusing := newServer(t, replyWith(http.StatusUnprocessableEntity, `{"detail":"BODYSENTINEL"}`))
	if _, err := clockLogClient(refusing, "KEYSENTINEL", newFakeClock(), logger).Ask(t.Context(), req()); err == nil {
		t.Fatal("422 call: err = nil, want refused")
	}

	// Numbers with more digits than a timestamp's fraction, so a time value cannot match.
	answering := newServer(t, replyWith(http.StatusOK, `{"answers":{"n":{"type":"noul","noul":0.61803398875},"c":{"type":"choice","choice":"ANSWERSENTINEL","confidence":0.70710678118},"s":{"type":"score","score":0.41421356237,"confidence":0.73205080756}},"usage":{"input_tokens":3,"output_tokens":4}}`))
	resp, err := clockLogClient(answering, "KEYSENTINEL", newFakeClock(), logger).Ask(t.Context(), req())
	if err != nil {
		t.Fatalf("200 call: err = %v, want nil", err)
	}
	if got := resp.Answers["c"].Choice; got != "ANSWERSENTINEL" {
		t.Fatalf("fixture: choice = %q, want ANSWERSENTINEL", got)
	}

	raw := buf.String()
	// Controls first, so the needle checks cannot pass on an empty buffer.
	for _, want := range []string{`"msg":"jev call"`, `"outcome":"ok"`, `"outcome":"skipped_refused"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("log buffer is missing %s; got %q", want, raw)
		}
	}
	for _, needle := range []string{
		"SENTINEL", "STATESENTINEL", "INSTRSENTINEL", "TRUESENTINEL", "FALSESENTINEL", "OPTNAMESENTINEL",
		"OPTDESCSENTINEL", "LEVELNAMESENTINEL", "LEVELDESCSENTINEL", "ANSWERSENTINEL", "KEYSENTINEL", "BODYSENTINEL",
		"Bearer", "61803398875", "70710678118", "41421356237", "73205080756",
	} {
		if strings.Contains(raw, needle) {
			t.Errorf("log buffer contains %q, want absent", needle)
		}
	}
}

// -- AC-15 --

func TestLog_WrittenWithABackgroundContext(t *testing.T) {
	type requestScoped struct{}
	ts := newServer(t, replyWith(http.StatusOK, noulOK))
	ch := &captureHandler{Handler: slog.NewJSONHandler(&bytes.Buffer{}, nil)}
	c := clockLogClient(ts, "k", newFakeClock(), slog.New(ch))
	ctx := context.WithValue(auth.WithIdentity(t.Context(), auth.Identity{TenantID: "t-1"}), requestScoped{}, "req-1")

	if _, err := c.Ask(ctx, noulReq("s")); err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	ch.mu.Lock()
	ctxs := slices.Clone(ch.ctxs)
	ch.mu.Unlock()
	if len(ctxs) != 1 {
		t.Fatalf("Handle called %d time(s), want 1", len(ctxs))
	}
	if v := ctxs[0].Value(requestScoped{}); v != nil {
		t.Errorf("handler ctx carries the caller's value %v, want a background context", v)
	}
	if _, ok := auth.IdentityFromContext(ctxs[0]); ok {
		t.Error("handler ctx carries the caller's identity, want a background context")
	}
}

func TestLog_ANilLoggerWritesNothing(t *testing.T) {
	ts := newServer(t, replyWith(http.StatusOK, noulOK))
	c := clockLogClient(ts, "k", newFakeClock(), nil)

	resp, err := c.Ask(t.Context(), noulReq("s"))
	if err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	wantNoul(t, resp, "q1", 0.95)
}
