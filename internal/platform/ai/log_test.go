// log_test.go: the one log line per call. T01-T08 are the acceptance specs;
// the adversarial rows below them cover the edges those specs do not reach.
package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// -- helpers --

// logClient returns a client logging JSON into buf.
func logClient(t *testing.T, cfg config) (*Client, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return newClient(cfg, slog.New(slog.NewJSONHandler(buf, nil))), buf
}

// lines splits buf into decoded JSON objects and fails on malformed output.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	dec := json.NewDecoder(buf)
	var out []map[string]any
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("malformed log output: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// only returns the single line in buf, failing when the count is not 1.
func only(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	ls := lines(t, buf)
	if len(ls) != 1 {
		t.Fatalf("got %d log lines, want 1: %v", len(ls), ls)
	}
	return ls[0]
}

// offClientWithLogger is offClient (fake_test.go) with a logger attached.
func offClientWithLogger(t *testing.T, logger *slog.Logger) *Client {
	t.Helper()
	return newClient(config{endpoint: noDialServer(t).URL, budget: budget, now: time.Now, sleep: realSleep}, logger)
}

// fakeModeClientWithLogger is fakeModeClient (fake_test.go) with a logger attached.
func fakeModeClientWithLogger(t *testing.T, logger *slog.Logger) *Client {
	t.Helper()
	return newClient(config{key: "k", endpoint: noDialServer(t).URL, fake: true, budget: budget, now: time.Now, sleep: realSleep}, logger)
}

// captureHandler records the ctx Handle receives, then forwards to Handler.
type captureHandler struct {
	slog.Handler
	ctx context.Context
}

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.ctx = ctx
	return h.Handler.Handle(ctx, r)
}

// -- T01 --

func TestLog_OneLinePerCallAcrossRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			_, _ = w.Write([]byte(envelope(`{"total":"1"}`, `{"prompt_tokens":10,"completion_tokens":2,"cost":0.001}`)))
			return
		}
		_, _ = w.Write([]byte(envelope(validContent, `{"prompt_tokens":20,"completion_tokens":3,"cost":0.002}`)))
	}))
	t.Cleanup(srv.Close)

	fc := &fakeClock{t: time.Unix(0, 0)}
	// Always advances 1500ms regardless of the backoff schedule call() asks for.
	sleep := func(ctx context.Context, d time.Duration) error { return fc.sleep(ctx, 1500*time.Millisecond) }
	c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: fc.now, sleep: sleep})

	if _, err := c.Call(t.Context(), baseReq()); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	line := only(t, buf)
	if got, _ := line["attempts"].(float64); got != 2 {
		t.Errorf("attempts = %v, want 2", line["attempts"])
	}
	if got, _ := line["input_tokens"].(float64); got != 30 {
		t.Errorf("input_tokens = %v, want 30", line["input_tokens"])
	}
	if got, _ := line["output_tokens"].(float64); got != 5 {
		t.Errorf("output_tokens = %v, want 5", line["output_tokens"])
	}
	if got, _ := line["cost"].(float64); math.Abs(got-0.003) > 1e-9 {
		t.Errorf("cost = %v, want 0.003", line["cost"])
	}
	if line["outcome"] != "ok" {
		t.Errorf("outcome = %v, want ok", line["outcome"])
	}
	if got, _ := line["latency_ms"].(float64); got != 1500 {
		t.Errorf("latency_ms = %v, want 1500", line["latency_ms"])
	}
}

// -- T02 --

func TestLog_KeysAreExactlyTheContract(t *testing.T) {
	srv, _ := okServer(t, validContent)
	c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep})

	ctx := auth.WithIdentity(t.Context(), auth.Identity{Subject: "u", Role: "authenticated", TenantID: "t-1"})
	if _, err := c.Call(ctx, baseReq()); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	line := only(t, buf)
	wantKeys := map[string]bool{
		"time": true, "level": true, "msg": true, "tenant_id": true,
		"model": true, "purpose": true, "input_tokens": true, "output_tokens": true,
		"cost": true, "latency_ms": true, "attempts": true, "outcome": true,
	}
	for k := range wantKeys {
		if _, ok := line[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	for k := range line {
		if !wantKeys[k] {
			t.Errorf("unexpected key %q", k)
		}
	}
	if line["msg"] != "ai call" {
		t.Errorf("msg = %v, want %q", line["msg"], "ai call")
	}
	if line["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", line["level"])
	}
	if line["model"] != Model {
		t.Errorf("model = %v, want %q", line["model"], Model)
	}
	if line["purpose"] != "document" {
		t.Errorf("purpose = %v, want document", line["purpose"])
	}
}

// -- T03 --

func TestLog_OutcomePerPath(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		srv, _ := okServer(t, validContent)
		fc := &fakeClock{t: time.Unix(0, 0)}
		c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: fc.now, sleep: fc.sleep})

		if _, err := c.Call(t.Context(), baseReq()); err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		line := only(t, buf)
		if line["outcome"] != "ok" {
			t.Errorf("outcome = %v, want ok", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got != 1 {
			t.Errorf("attempts = %v, want 1", line["attempts"])
		}
	})

	t.Run("refused_by_status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
		t.Cleanup(srv.Close)
		c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep})

		_, _ = c.Call(t.Context(), baseReq())
		line := only(t, buf)
		if line["outcome"] != "refused" {
			t.Errorf("outcome = %v, want refused", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got != 1 {
			t.Errorf("attempts = %v, want 1", line["attempts"])
		}
	})

	t.Run("refused_by_request", func(t *testing.T) {
		srv, _ := okServer(t, validContent)
		c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep})
		req := baseReq()
		req.Purpose = "x"

		_, _ = c.Call(t.Context(), req)
		line := only(t, buf)
		if line["outcome"] != "refused" {
			t.Errorf("outcome = %v, want refused", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got != 0 {
			t.Errorf("attempts = %v, want 0", line["attempts"])
		}
		if got, _ := line["latency_ms"].(float64); got != 0 {
			t.Errorf("latency_ms = %v, want 0", line["latency_ms"])
		}
	})

	t.Run("off", func(t *testing.T) {
		buf := &bytes.Buffer{}
		c := offClientWithLogger(t, slog.New(slog.NewJSONHandler(buf, nil)))

		noDials(t, func() { _, _ = c.Call(t.Context(), baseReq()) })
		line := only(t, buf)
		if line["outcome"] != "off" {
			t.Errorf("outcome = %v, want off", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got != 0 {
			t.Errorf("attempts = %v, want 0", line["attempts"])
		}
		if got, _ := line["latency_ms"].(float64); got != 0 {
			t.Errorf("latency_ms = %v, want 0", line["latency_ms"])
		}
	})

	t.Run("fake_blank", func(t *testing.T) {
		buf := &bytes.Buffer{}
		c := fakeModeClientWithLogger(t, slog.New(slog.NewJSONHandler(buf, nil)))
		req := baseReq()
		req.Text = "invoice 123"

		noDials(t, func() { _, _ = c.Call(t.Context(), req) })
		line := only(t, buf)
		if line["outcome"] != "fake" {
			t.Errorf("outcome = %v, want fake", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got != 1 {
			t.Errorf("attempts = %v, want 1", line["attempts"])
		}
	})

	t.Run("fake_unavailable", func(t *testing.T) {
		buf := &bytes.Buffer{}
		c := fakeModeClientWithLogger(t, slog.New(slog.NewJSONHandler(buf, nil)))
		req := baseReq()
		req.Text = markerUnavailable

		noDials(t, func() { _, _ = c.Call(t.Context(), req) })
		line := only(t, buf)
		if line["outcome"] != "fake" {
			t.Errorf("outcome = %v, want fake", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got != 1 {
			t.Errorf("attempts = %v, want 1", line["attempts"])
		}
	})

	t.Run("unavailable_by_budget", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)
		fc := &fakeClock{t: time.Unix(0, 0)}
		c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: fc.now, sleep: fc.sleep})

		_, _ = c.Call(t.Context(), baseReq())
		line := only(t, buf)
		if line["outcome"] != "unavailable" {
			t.Errorf("outcome = %v, want unavailable", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got <= 1 {
			t.Errorf("attempts = %v, want > 1", line["attempts"])
		}
	})

	t.Run("unavailable_by_cancelled_caller", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		}))
		t.Cleanup(srv.Close)
		c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: time.Now, sleep: realSleep})

		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(50*time.Millisecond, cancel)
		_, _ = c.Call(ctx, baseReq())

		line := only(t, buf)
		if line["outcome"] != "unavailable" {
			t.Errorf("outcome = %v, want unavailable", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got != 1 {
			t.Errorf("attempts = %v, want 1", line["attempts"])
		}
	})

	t.Run("unavailable_by_expired_deadline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		}))
		t.Cleanup(srv.Close)
		c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: time.Now, sleep: realSleep})

		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		_, _ = c.Call(ctx, baseReq())

		line := only(t, buf)
		if line["outcome"] != "unavailable" {
			t.Errorf("outcome = %v, want unavailable", line["outcome"])
		}
		if got, _ := line["attempts"].(float64); got != 1 {
			t.Errorf("attempts = %v, want 1", line["attempts"])
		}
	})
}

// -- T04 --

func TestLog_CarriesNoContent(t *testing.T) {
	oneStringSchema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":{"type":"string"}}}`)

	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	req := baseReq()
	req.System = "SYS-7f3a"
	req.Text = "TXT-7f3a"
	req.FakeHint = "HINT-7f3a"
	req.FakeScope = "ZZZSCOPENEEDLE"
	req.SchemaName = "SCH-7f3a"
	req.Schema = oneStringSchema

	srv1, _ := okServer(t, `{"value":"ANS-7f3a"}`)
	c1 := newClient(config{key: "k", endpoint: srv1.URL, budget: budget, now: time.Now, sleep: realSleep}, logger)
	if _, err := c1.Call(t.Context(), req); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("BODY-7f3a"))
	}))
	t.Cleanup(srv2.Close)
	c2 := newClient(config{key: "k", endpoint: srv2.URL, budget: budget, now: time.Now, sleep: realSleep}, logger)
	if _, err := c2.Call(t.Context(), req); err == nil {
		t.Fatal("err = nil, want non-nil")
	}

	raw := buf.String()
	// Control first: proves the buffer actually has content, so the needle
	// checks below cannot pass vacuously on an empty buffer.
	if !strings.Contains(raw, "ai call") {
		t.Fatalf("log buffer missing %q; got %q", "ai call", raw)
	}
	if !strings.Contains(raw, `"outcome":"ok"`) {
		t.Errorf("log buffer missing an %q outcome; got %q", "ok", raw)
	}
	if !strings.Contains(raw, `"outcome":"refused"`) {
		t.Errorf("log buffer missing a %q outcome; got %q", "refused", raw)
	}

	for _, needle := range []string{"SYS-7f3a", "TXT-7f3a", "HINT-7f3a", "ZZZSCOPENEEDLE", "SCH-7f3a", "ANS-7f3a", "BODY-7f3a"} {
		if strings.Contains(raw, needle) {
			t.Errorf("log buffer contains %q, want absent", needle)
		}
	}
}

// -- T05 --

func TestLog_TenantFromIdentity(t *testing.T) {
	srv, _ := okServer(t, validContent)
	c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep})

	ctx := auth.WithIdentity(t.Context(), auth.Identity{TenantID: "t-1"})
	if _, err := c.Call(ctx, baseReq()); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	raw := buf.String()
	line := only(t, buf)
	if line["tenant_id"] != "t-1" {
		t.Errorf("tenant_id = %v, want t-1", line["tenant_id"])
	}
	if n := strings.Count(raw, `"tenant_id"`); n != 1 {
		t.Errorf(`buffer contains %d occurrences of "tenant_id", want 1`, n)
	}
}

// -- T06 --

func TestLog_TenantNotDuplicatedByTheContextHandler(t *testing.T) {
	srv, _ := okServer(t, validContent)
	buf := &bytes.Buffer{}
	ch := &captureHandler{Handler: slog.NewJSONHandler(buf, nil)}
	c := newClient(config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep}, slog.New(ch))

	ctx := platform.WithTenantID(auth.WithIdentity(t.Context(), auth.Identity{TenantID: "t-1"}), "t-1")
	if _, err := c.Call(ctx, baseReq()); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	if ch.ctx == nil {
		t.Fatal("Handle was never called")
	}
	if got := platform.TenantIDFromContext(ch.ctx); got != "" {
		t.Errorf("TenantIDFromContext(recorded ctx) = %q, want empty", got)
	}

	raw := buf.String()
	line := only(t, buf)
	if line["tenant_id"] != "t-1" {
		t.Errorf("tenant_id = %v, want t-1", line["tenant_id"])
	}
	if n := strings.Count(raw, `"tenant_id"`); n != 1 {
		t.Errorf(`buffer contains %d occurrences of "tenant_id", want 1`, n)
	}
}

// -- T07 --

func TestLog_NoIdentityOmitsTenant(t *testing.T) {
	cases := map[string]context.Context{
		"no_identity":           t.Context(),
		"identity_empty_tenant": auth.WithIdentity(t.Context(), auth.Identity{TenantID: ""}),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := okServer(t, validContent)
			c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep})

			if _, err := c.Call(ctx, baseReq()); err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			line := only(t, buf)
			if _, ok := line["tenant_id"]; ok {
				t.Errorf("tenant_id = %v, want absent", line["tenant_id"])
			}
			for _, key := range []string{"time", "level", "msg", "model", "purpose", "input_tokens", "output_tokens", "cost", "latency_ms", "attempts", "outcome"} {
				if _, ok := line[key]; !ok {
					t.Errorf("missing key %q", key)
				}
			}
		})
	}
}

// -- T08 --

func TestLog_MissingUsageLogsZero(t *testing.T) {
	cases := map[string]struct {
		usageJSON                                   string
		wantInputTokens, wantOutputTokens, wantCost float64
	}{
		"no_usage_key": {"", 0, 0, 0},
		"cost_only":    {`{"cost":0.5}`, 0, 0, 0.5},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(envelope(validContent, tc.usageJSON)))
			}))
			t.Cleanup(srv.Close)
			c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep})

			if _, err := c.Call(t.Context(), baseReq()); err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			line := only(t, buf)
			if got, _ := line["input_tokens"].(float64); got != tc.wantInputTokens {
				t.Errorf("input_tokens = %v, want %v", line["input_tokens"], tc.wantInputTokens)
			}
			if got, _ := line["output_tokens"].(float64); got != tc.wantOutputTokens {
				t.Errorf("output_tokens = %v, want %v", line["output_tokens"], tc.wantOutputTokens)
			}
			if got, _ := line["cost"].(float64); math.Abs(got-tc.wantCost) > 1e-9 {
				t.Errorf("cost = %v, want %v", line["cost"], tc.wantCost)
			}
		})
	}
}

// -- adversarial: the logger itself --

// errHandler forwards to Handler and then reports a failure, which slog
// discards (logger.go does `_ = Handle(...)`).
type errHandler struct {
	slog.Handler
	calls atomic.Int32
}

func (h *errHandler) Handle(ctx context.Context, r slog.Record) error {
	h.calls.Add(1)
	_ = h.Handler.Handle(ctx, r)
	return errors.New("handler refused the record")
}

func TestLog_AFailingHandlerLeavesTheAnswerAndErrorAlone(t *testing.T) {
	buf := &bytes.Buffer{}
	eh := &errHandler{Handler: slog.NewJSONHandler(buf, nil)}
	logger := slog.New(eh)

	okSrv, _ := okServer(t, validContent)
	answer, err := newClient(config{key: "k", endpoint: okSrv.URL, budget: budget, now: time.Now, sleep: realSleep}, logger).
		Call(t.Context(), baseReq())
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if got := answer["currency"]; got != "NGN" {
		t.Errorf("answer[currency] = %v, want NGN", got)
	}

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(badSrv.Close)
	if _, err := newClient(config{key: "k", endpoint: badSrv.URL, budget: budget, now: time.Now, sleep: realSleep}, logger).
		Call(t.Context(), baseReq()); err == nil {
		t.Fatal("err = nil, want the refusal")
	}

	if got := eh.calls.Load(); got != 2 {
		t.Errorf("handler calls = %d, want 2", got)
	}
	if got := len(lines(t, buf)); got != 2 {
		t.Errorf("log lines = %d, want 2", got)
	}
}

// -- adversarial: one client, two calls --

func TestLog_TwoCallsOnOneClientSumIndependently(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			_, _ = w.Write([]byte(envelope(validContent, `{"prompt_tokens":10,"completion_tokens":2,"cost":0.001}`)))
			return
		}
		_, _ = w.Write([]byte(envelope(validContent, `{"prompt_tokens":20,"completion_tokens":3,"cost":0.002}`)))
	}))
	t.Cleanup(srv.Close)
	c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep})

	for i := range 2 {
		if _, err := c.Call(t.Context(), baseReq()); err != nil {
			t.Fatalf("Call() %d err = %v, want nil", i+1, err)
		}
	}

	ls := lines(t, buf)
	if len(ls) != 2 {
		t.Fatalf("got %d log lines, want 2: %v", len(ls), ls)
	}
	want := []struct{ in, out, cost float64 }{{10, 2, 0.001}, {20, 3, 0.002}}
	for i, w := range want {
		if got, _ := ls[i]["input_tokens"].(float64); got != w.in {
			t.Errorf("line %d input_tokens = %v, want %v (no carry-over from the other call)", i+1, ls[i]["input_tokens"], w.in)
		}
		if got, _ := ls[i]["output_tokens"].(float64); got != w.out {
			t.Errorf("line %d output_tokens = %v, want %v", i+1, ls[i]["output_tokens"], w.out)
		}
		if got, _ := ls[i]["cost"].(float64); math.Abs(got-w.cost) > 1e-9 {
			t.Errorf("line %d cost = %v, want %v", i+1, ls[i]["cost"], w.cost)
		}
		if got, _ := ls[i]["attempts"].(float64); got != 1 {
			t.Errorf("line %d attempts = %v, want 1", i+1, ls[i]["attempts"])
		}
	}
}

// -- adversarial: what the usage object says --

func TestLog_UsageIsLoggedVerbatim(t *testing.T) {
	cases := map[string]struct {
		usageJSON                 string
		wantOutcome               string
		wantIn, wantOut, wantCost float64
	}{
		"integer_cost":        {`{"prompt_tokens":1,"completion_tokens":1,"cost":2}`, "ok", 1, 1, 2},
		"negative_and_absurd": {`{"prompt_tokens":-5,"completion_tokens":2147483647,"cost":-1.5}`, "ok", -5, 2147483647, -1.5},
		// A usage field of the wrong JSON type fails the whole envelope decode,
		// so the attempt is retried and the line reports no usage at all.
		"cost_as_string": {`{"prompt_tokens":1,"completion_tokens":1,"cost":"free"}`, "unavailable", 0, 0, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(envelope(validContent, tc.usageJSON)))
			}))
			t.Cleanup(srv.Close)
			fc := &fakeClock{t: time.Unix(0, 0)}
			c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: fc.now, sleep: fc.sleep})

			_, _ = callWithin(t, c, t.Context(), baseReq(), 5*time.Second)

			line := only(t, buf)
			if line["outcome"] != tc.wantOutcome {
				t.Errorf("outcome = %v, want %v", line["outcome"], tc.wantOutcome)
			}
			if got, _ := line["input_tokens"].(float64); got != tc.wantIn {
				t.Errorf("input_tokens = %v, want %v (nothing clamps it)", line["input_tokens"], tc.wantIn)
			}
			if got, _ := line["output_tokens"].(float64); got != tc.wantOut {
				t.Errorf("output_tokens = %v, want %v", line["output_tokens"], tc.wantOut)
			}
			if got, _ := line["cost"].(float64); math.Abs(got-tc.wantCost) > 1e-9 {
				t.Errorf("cost = %v, want %v", line["cost"], tc.wantCost)
			}
		})
	}
}

// -- adversarial: a clock that runs backwards across the whole call --

// scriptedClock hands out readings in order and then repeats the last one.
type scriptedClock struct {
	mu    sync.Mutex
	at    []time.Time
	reads int
}

func (s *scriptedClock) now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return s.at[min(s.reads-1, len(s.at)-1)]
}

func (s *scriptedClock) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func TestLog_ABackwardClockLogsANegativeLatency(t *testing.T) {
	// The off path reads no clock, so Call's own two readings are the only
	// ones: the second lands as the end of the call. Nothing clamps the
	// subtraction, so latency_ms goes negative. Deployed code reads time.Now,
	// whose monotonic reading cannot go backwards; a test clock can.
	t0 := time.Unix(0, 0)
	sc := &scriptedClock{at: []time.Time{t0, t0.Add(-2 * time.Second)}}
	buf := &bytes.Buffer{}
	c := newClient(config{endpoint: noDialServer(t).URL, budget: budget, now: sc.now, sleep: realSleep},
		slog.New(slog.NewJSONHandler(buf, nil)))

	noDials(t, func() { _, _ = c.Call(t.Context(), baseReq()) })

	line := only(t, buf)
	if line["outcome"] != "off" {
		t.Fatalf("outcome = %v, want off", line["outcome"])
	}
	if got, _ := line["latency_ms"].(float64); got != -2000 {
		t.Errorf("latency_ms = %v, want -2000", line["latency_ms"])
	}
	if got := sc.count(); got != 2 {
		t.Errorf("clock reads = %d, want 2: the off path adds none of its own", got)
	}
}

// -- adversarial: an identity on a call that is cancelled --

func TestLog_ACancelledCallWithAnIdentityStillCarriesTheTenant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: time.Now, sleep: realSleep})

	ctx, cancel := context.WithCancel(auth.WithIdentity(t.Context(), auth.Identity{TenantID: "t-9"}))
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := c.Call(ctx, baseReq()); err == nil {
		t.Fatal("err = nil, want the cancelled caller's error")
	}

	raw := buf.String()
	line := only(t, buf)
	if line["tenant_id"] != "t-9" {
		t.Errorf("tenant_id = %v, want t-9", line["tenant_id"])
	}
	if line["outcome"] != "unavailable" {
		t.Errorf("outcome = %v, want unavailable", line["outcome"])
	}
	if n := strings.Count(raw, `"tenant_id"`); n != 1 {
		t.Errorf(`buffer contains %d occurrences of "tenant_id", want 1`, n)
	}
}

// -- adversarial: what purpose carries --

func TestLog_PurposeIsLoggedVerbatim(t *testing.T) {
	cases := map[string]string{
		"long":              strings.Repeat("p", 300),
		"non_ascii":         "фактура-ọdún-发票",
		"quote_and_newline": "doc\"ument\nsecond line",
	}
	for name, purpose := range cases {
		t.Run(name, func(t *testing.T) {
			srv, hits := okServer(t, validContent)
			c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep})
			req := baseReq()
			req.Purpose = Purpose(purpose)

			if _, err := c.Call(t.Context(), req); err == nil {
				t.Fatal("err = nil, want the refusal an unsupported purpose earns")
			}

			line := only(t, buf)
			if line["purpose"] != purpose {
				t.Errorf("purpose = %q, want %q", line["purpose"], purpose)
			}
			if line["outcome"] != "refused" {
				t.Errorf("outcome = %v, want refused", line["outcome"])
			}
			if got := hits.Load(); got != 0 {
				t.Errorf("hits = %d, want 0: an unsupported purpose is refused before the wire", got)
			}
		})
	}
}

// -- adversarial: an off client asked for nothing --

func TestLog_AnOffCallWithAZeroRequestStillCarriesTheContract(t *testing.T) {
	buf := &bytes.Buffer{}
	c := offClientWithLogger(t, slog.New(slog.NewJSONHandler(buf, nil)))

	noDials(t, func() { _, _ = c.Call(t.Context(), Request{}) })

	line := only(t, buf)
	if line["model"] != Model {
		t.Errorf("model = %v, want %q", line["model"], Model)
	}
	if line["outcome"] != "off" {
		t.Errorf("outcome = %v, want off", line["outcome"])
	}
	if got, ok := line["purpose"]; !ok || got != "" {
		t.Errorf("purpose = %v (present = %v), want an empty string", got, ok)
	}
	for _, key := range []string{"time", "level", "msg", "model", "purpose", "input_tokens", "output_tokens", "cost", "latency_ms", "attempts", "outcome"} {
		if _, ok := line[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	if _, ok := line["tenant_id"]; ok {
		t.Errorf("tenant_id = %v, want absent", line["tenant_id"])
	}
}

// -- adversarial: the fake path --

func TestLog_AFakeAnsweredCallLogsFakeWithNoUsage(t *testing.T) {
	buf := &bytes.Buffer{}
	c := fakeModeClientWithLogger(t, slog.New(slog.NewJSONHandler(buf, nil)))
	req := baseReq()
	req.Text = answerMarker(validContent)

	var answer map[string]any
	noDials(t, func() {
		var err error
		if answer, err = c.Call(t.Context(), req); err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
	})
	if got := answer["currency"]; got != "NGN" {
		t.Errorf("answer[currency] = %v, want NGN", got)
	}

	line := only(t, buf)
	if line["outcome"] != "fake" {
		t.Errorf("outcome = %v, want fake", line["outcome"])
	}
	if got, _ := line["attempts"].(float64); got != 1 {
		t.Errorf("attempts = %v, want 1", line["attempts"])
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cost"} {
		if got, _ := line[key].(float64); got != 0 {
			t.Errorf("%s = %v, want 0: a fake call spends nothing", key, line[key])
		}
	}
}

func TestLog_CarriesNeitherPageBytesNorAFakeAnswer(t *testing.T) {
	// The two content needles T04 cannot reach: the page bytes, and the fake
	// path's answer, which never passes through a server.
	page := []byte("PAGE-4c19")
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	req := baseReq()
	req.Text = "TXT-4c19 " + answerMarker(`{"total":"ANS-4c19","vat":null,"currency":"NGN","n":1}`)
	req.FakeHint = "HINT-4c19"
	req.Pages = [][]byte{page}

	noDials(t, func() {
		if _, err := fakeModeClientWithLogger(t, logger).Call(t.Context(), req); err != nil {
			t.Fatalf("fake Call() err = %v, want nil", err)
		}
	})

	wireSrv, hits := okServer(t, `{"total":"ANS2-4c19","vat":null,"currency":"NGN","n":2}`)
	wireReq := baseReq()
	wireReq.Text = "TXT2-4c19"
	wireReq.Pages = [][]byte{page}
	if _, err := newClient(config{key: "k", endpoint: wireSrv.URL, budget: budget, now: time.Now, sleep: realSleep}, logger).
		Call(t.Context(), wireReq); err != nil {
		t.Fatalf("wire Call() err = %v, want nil", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}

	raw := buf.String()
	// Control first, so the needle checks cannot pass on an empty buffer.
	for _, want := range []string{`"outcome":"fake"`, `"outcome":"ok"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("log buffer missing %s; got %q", want, raw)
		}
	}
	needles := []string{"PAGE-4c19", base64.StdEncoding.EncodeToString(page), "TXT-4c19", "TXT2-4c19", "ANS-4c19", "ANS2-4c19", "HINT-4c19"}
	for _, needle := range needles {
		if strings.Contains(raw, needle) {
			t.Errorf("log buffer contains %q, want absent", needle)
		}
	}
}
