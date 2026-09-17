// log_test.go: T01-T08 for AIR-02-03. Red until Stage 3 wires logCall into
// Call -- every test below fails at "0 log lines, want 1" or its equivalent.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
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

	for _, needle := range []string{"SYS-7f3a", "TXT-7f3a", "HINT-7f3a", "SCH-7f3a", "ANS-7f3a", "BODY-7f3a"} {
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
