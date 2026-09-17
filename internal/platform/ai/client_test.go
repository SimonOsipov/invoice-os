package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// -- helpers --

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
	return nil
}

// capture records the last request's header and decoded body under a mutex.
type capture struct {
	mu     sync.Mutex
	header http.Header
	body   map[string]any
	raw    []byte
}

func (c *capture) record(r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.header = r.Header.Clone()
	c.body = body
	c.raw = raw
}

func (c *capture) snapshot() (http.Header, map[string]any, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.header, c.body, c.raw
}

var testSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["total","vat","currency","n"],"properties":{"total":{"type":["string","null"]},"vat":{"type":["string","null"]},"currency":{"type":"string","enum":["NGN","USD"]},"n":{"type":"integer"}}}`)

const validContent = `{"total":"1935.00","vat":null,"currency":"NGN","n":3}`

// envelope wraps content as the single choice's message content, optionally
// attaching a usage object.
func envelope(content, usageJSON string) string {
	b, _ := json.Marshal(content)
	s := `{"choices":[{"message":{"content":` + string(b) + `}}]`
	if usageJSON != "" {
		s += `,"usage":` + usageJSON
	}
	return s + `}`
}

func baseReq() Request {
	return Request{
		Purpose:    PurposeDocument,
		System:     "sys",
		Text:       "rows",
		SchemaName: "invoice_fields",
		Schema:     testSchema,
	}
}

func fakeClient(t *testing.T, url string, b time.Duration) (*Client, *fakeClock) {
	t.Helper()
	fc := &fakeClock{t: time.Unix(0, 0)}
	c := newClient(config{key: "k", endpoint: url, budget: b, now: fc.now, sleep: fc.sleep}, nil)
	return c, fc
}

func realClient(t *testing.T, url string, b time.Duration) *Client {
	t.Helper()
	return newClient(config{key: "k", endpoint: url, budget: b, now: time.Now, sleep: realSleep}, nil)
}

// callWithin fails the test if Call has not returned after d, so a loop that
// never ends reds in seconds instead of at the package timeout.
func callWithin(t *testing.T, c *Client, ctx context.Context, req Request, d time.Duration) (map[string]any, error) {
	t.Helper()
	type outcome struct {
		answer map[string]any
		err    error
	}
	ch := make(chan outcome, 1)
	go func() {
		answer, err := c.Call(ctx, req)
		ch <- outcome{answer, err}
	}()
	select {
	case got := <-ch:
		return got.answer, got.err
	case <-time.After(d):
		t.Fatalf("Call did not return within %v", d)
		return nil, nil
	}
}

// okServer always answers 200 with content wrapped in a valid envelope.
func okServer(t *testing.T, content string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(content, "")))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// retryServer answers hit 1 with hit1Body and every later hit with okContent.
func retryServer(t *testing.T, hit1Body, okContent string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			_, _ = w.Write([]byte(hit1Body))
			return
		}
		_, _ = w.Write([]byte(envelope(okContent, "")))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// assertRefusedBeforeTheWire runs req against a healthy server and requires
// that nothing was sent.
func assertRefusedBeforeTheWire(t *testing.T, req Request) {
	t.Helper()
	srv, hits := okServer(t, validContent)
	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	_, err := c.Call(t.Context(), req)
	if hits.Load() != 0 {
		t.Fatalf("hits = %d, want 0", hits.Load())
	}
	if err == nil {
		t.Fatal("err = nil, want non-nil")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Errorf("err wraps ErrUnavailable, want a validation error")
	}
}

// -- T01 --

func TestCall_SendsOneChatCompletionForTheModel(t *testing.T) {
	var hits atomic.Int32
	var rec capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		rec.record(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(validContent, "")))
	}))
	t.Cleanup(srv.Close)

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	if _, err := c.Call(t.Context(), baseReq()); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}

	header, body, _ := rec.snapshot()
	if got := header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer k")
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got, _ := body["model"].(string); got != Model {
		t.Errorf("model = %q, want %q", got, Model)
	}

	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	sys, _ := messages[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "sys" {
		t.Errorf("messages[0] = %v, want {role: system, content: sys}", sys)
	}
	user, _ := messages[1].(map[string]any)
	if user["role"] != "user" {
		t.Errorf("messages[1].role = %v, want user", user["role"])
	}
	parts, _ := user["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("len(user content parts) = %d, want 1", len(parts))
	}
	part, _ := parts[0].(map[string]any)
	if part["type"] != "text" || part["text"] != "rows" {
		t.Errorf("user part = %v, want {type: text, text: rows}", part)
	}

	rf, _ := body["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, want json_schema", rf["type"])
	}
	js, _ := rf["json_schema"].(map[string]any)
	if js["name"] != "invoice_fields" {
		t.Errorf("json_schema.name = %v, want invoice_fields", js["name"])
	}
	if js["strict"] != true {
		t.Errorf("json_schema.strict = %v, want true", js["strict"])
	}

	var gotSchema, wantSchema map[string]any
	if b, err := json.Marshal(js["schema"]); err == nil {
		_ = json.Unmarshal(b, &gotSchema)
	}
	_ = json.Unmarshal(testSchema, &wantSchema)
	if !reflect.DeepEqual(gotSchema, wantSchema) {
		t.Errorf("json_schema.schema = %v, want %v", gotSchema, wantSchema)
	}

	provider, _ := body["provider"].(map[string]any)
	if provider["require_parameters"] != true {
		t.Errorf("provider.require_parameters = %v, want true", provider["require_parameters"])
	}
	if body["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", body["temperature"])
	}
	if _, ok := body["usage"]; ok {
		t.Errorf(`body has key "usage", want absent`)
	}
}

// -- T02 --

func TestCall_SendsPagesAsPNGDataURLsAfterTheText(t *testing.T) {
	var rec capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(validContent, "")))
	}))
	t.Cleanup(srv.Close)

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	req := baseReq()
	req.Text = "t"
	p1 := []byte{0x89, 'P', 'N', 'G', 1}
	p2 := []byte{0x89, 'P', 'N', 'G', 2}
	req.Pages = [][]byte{p1, p2}

	if _, err := c.Call(t.Context(), req); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	_, body, _ := rec.snapshot()
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	user, _ := messages[1].(map[string]any)
	parts, _ := user["content"].([]any)
	if len(parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(parts))
	}

	textPart, _ := parts[0].(map[string]any)
	if textPart["type"] != "text" || textPart["text"] != "t" {
		t.Errorf("parts[0] = %v, want text part %q", textPart, "t")
	}

	wantURLs := []string{
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(p1),
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(p2),
	}
	for i, want := range wantURLs {
		part, _ := parts[i+1].(map[string]any)
		if part["type"] != "image_url" {
			t.Errorf("parts[%d].type = %v, want image_url", i+1, part["type"])
			continue
		}
		imgURL, _ := part["image_url"].(map[string]any)
		if imgURL["url"] != want {
			t.Errorf("parts[%d].image_url.url = %v, want %v", i+1, imgURL["url"], want)
		}
	}
}

// -- T03 --

func TestCall_ImagesOnlyOmitsTheTextPart(t *testing.T) {
	var rec capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(validContent, "")))
	}))
	t.Cleanup(srv.Close)

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	req := baseReq()
	req.Text = ""
	req.Pages = [][]byte{{0x89, 'P', 'N', 'G'}}

	if _, err := c.Call(t.Context(), req); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	_, body, _ := rec.snapshot()
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	user, _ := messages[1].(map[string]any)
	parts, _ := user["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(parts))
	}
	part, _ := parts[0].(map[string]any)
	if part["type"] != "image_url" {
		t.Errorf("parts[0].type = %v, want image_url", part["type"])
	}
}

// -- T04 --

func TestCall_ReturnsTheParsedAnswerObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(validContent, "")))
	}))
	t.Cleanup(srv.Close)

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	got, err := c.Call(t.Context(), baseReq())
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	want := map[string]any{
		"total":    "1935.00",
		"vat":      nil,
		"currency": "NGN",
		"n":        json.Number("3"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Call() = %#v, want %#v", got, want)
	}
}

// -- T05 --

func TestCall_OneFailureThenSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(`{"total":"2.00","vat":null,"currency":"USD","n":1}`, "")))
	}))
	t.Cleanup(srv.Close)

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	got, err := c.Call(t.Context(), baseReq())
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
	if got["total"] != "2.00" {
		t.Errorf(`total = %v, want "2.00"`, got["total"])
	}
}

// -- T06 --

func TestCall_RetriesEachRetryableStatus(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				if n == 1 {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(envelope(validContent, "")))
			}))
			t.Cleanup(srv.Close)

			c, _ := fakeClient(t, srv.URL, 15*time.Second)
			_, err := c.Call(t.Context(), baseReq())
			if err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			if hits.Load() != 2 {
				t.Fatalf("hits = %d, want 2", hits.Load())
			}
		})
	}
}

// -- T07 --

func TestCall_RefusedStatusIsNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 402, 403, 404, 408} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte("BODY-7f3a"))
			}))
			t.Cleanup(srv.Close)

			c, _ := fakeClient(t, srv.URL, 15*time.Second)
			_, err := c.Call(t.Context(), baseReq())
			if hits.Load() != 1 {
				t.Fatalf("hits = %d, want 1", hits.Load())
			}
			if err == nil {
				t.Fatal("err = nil, want non-nil")
			}
			if errors.Is(err, ErrUnavailable) {
				t.Errorf("err wraps ErrUnavailable, want a distinct refusal error")
			}
			if strings.Contains(err.Error(), "BODY-7f3a") {
				t.Errorf("err.Error() = %q, must not leak the response body", err.Error())
			}
		})
	}
}

// -- T08 --

func TestCall_MalformedBodyIsRetried(t *testing.T) {
	cases := map[string]string{
		"error_object":     `{"error":{"code":502,"message":"x"}}`,
		"no_choices":       `{"choices":[]}`,
		"content_not_json": envelope("not json", ""),
		"missing_field":    envelope(`{"total":"1","currency":"NGN","n":1}`, ""),
		"extra_field":      envelope(`{"total":"1935.00","vat":null,"currency":"NGN","n":3,"x":1}`, ""),
		"wrong_type":       envelope(`{"total":"1","vat":null,"currency":"NGN","n":"3"}`, ""),
		"enum_violation":   envelope(`{"total":"1","vat":null,"currency":"EUR","n":1}`, ""),
		"trailing_value":   envelope(`{"total":"1","vat":null,"currency":"NGN","n":1} {}`, ""),
	}
	for name, hit1Body := range cases {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				w.WriteHeader(http.StatusOK)
				if n == 1 {
					_, _ = w.Write([]byte(hit1Body))
					return
				}
				_, _ = w.Write([]byte(envelope(validContent, "")))
			}))
			t.Cleanup(srv.Close)

			c, _ := fakeClient(t, srv.URL, 15*time.Second)
			_, err := c.Call(t.Context(), baseReq())
			if err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			if hits.Load() != 2 {
				t.Fatalf("hits = %d, want 2", hits.Load())
			}
		})
	}
}

// -- T09 --

func TestCall_NetworkErrorIsRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not implement http.Hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("Hijack: %v", err)
				return
			}
			conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(validContent, "")))
	}))
	t.Cleanup(srv.Close)

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	_, err := c.Call(t.Context(), baseReq())
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

// -- T10 --

func TestCall_BudgetSpentIsUnavailable(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	fc := &fakeClock{t: time.Unix(0, 0)}
	t0 := fc.now()
	c := newClient(config{key: "k", endpoint: srv.URL, budget: 15 * time.Second, now: fc.now, sleep: fc.sleep}, nil)

	wallStart := time.Now()
	_, err := c.Call(t.Context(), baseReq())
	wall := time.Since(wallStart)

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if hits.Load() < 2 {
		t.Errorf("hits = %d, want >= 2", hits.Load())
	}
	if fc.now().Sub(t0) > 15*time.Second {
		t.Errorf("fake clock advanced %v, want <= 15s", fc.now().Sub(t0))
	}
	if wall >= 2*time.Second {
		t.Errorf("wall elapsed %v, want < 2s", wall)
	}
}

// -- T11 --

func TestCall_BudgetNotAttemptCountEndsTheLoop(t *testing.T) {
	run := func(t *testing.T, b time.Duration) int32 {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		fc := &fakeClock{t: time.Unix(0, 0)}
		c := newClient(config{key: "k", endpoint: srv.URL, budget: b, now: fc.now, sleep: fc.sleep}, nil)
		_, err := c.Call(t.Context(), baseReq())
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("budget %v: err = %v, want ErrUnavailable", b, err)
		}
		return hits.Load()
	}

	got15 := run(t, 15*time.Second)
	got30 := run(t, 30*time.Second)
	if got30 <= got15 {
		t.Errorf("hits(30s) = %d, hits(15s) = %d, want hits(30s) > hits(15s)", got30, got15)
	}
}

// -- T12 --

func TestCall_SlowAttemptTimesOutWithinTheBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain so Go's server arms the background read that notices the
		// client disconnect; an unread body leaves r.Context() un-cancelable.
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := realClient(t, srv.URL, 300*time.Millisecond)

	start := time.Now()
	// Bounded: an attempt with no deadline of its own never returns here.
	_, err := callWithin(t, c, t.Context(), baseReq(), 2*time.Second)
	wall := time.Since(start)

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err wraps context.DeadlineExceeded, want it indistinguishable from a caller deadline")
	}
	if wall >= time.Second {
		t.Errorf("wall = %v, want < 1s", wall)
	}
}

// -- T13 --

func TestCall_CancelledContextStopsAtOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain so Go's server arms the background read that notices the
		// client disconnect; an unread body leaves r.Context() un-cancelable.
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := realClient(t, srv.URL, 15*time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := c.Call(ctx, baseReq())
	wall := time.Since(start)

	if wall >= 500*time.Millisecond {
		t.Errorf("wall = %v, want < 500ms", wall)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Errorf("err wraps ErrUnavailable, want it distinct from a caller cancellation")
	}
}

// -- T14 (corrected: hit 1 writes 503, then cancel fires during the backoff) --

func TestCall_CancelDuringBackoffStopsAtOnce(t *testing.T) {
	var hits atomic.Int32
	var cancel context.CancelFunc
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		time.AfterFunc(50*time.Millisecond, func() { cancel() })
	}))
	t.Cleanup(srv.Close)

	c := realClient(t, srv.URL, 15*time.Second)

	ctx, cancelFn := context.WithCancel(t.Context())
	cancel = cancelFn

	start := time.Now()
	_, err := c.Call(ctx, baseReq())
	wall := time.Since(start)

	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Errorf("err wraps ErrUnavailable, want it distinct from a caller cancellation")
	}
	if wall >= 200*time.Millisecond {
		t.Errorf("wall = %v, want < 200ms", wall)
	}
}

// -- T15 --

func TestCall_InvalidRequestSendsNothing(t *testing.T) {
	disallowedKeyword := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["total","vat","currency","n"],"properties":{"total":{"type":["string","null"],"pattern":"^N"},"vat":{"type":["string","null"]},"currency":{"type":"string","enum":["NGN","USD"]},"n":{"type":"integer"}}}`)
	additionalPropsTrue := json.RawMessage(`{"type":"object","additionalProperties":true,"properties":{"n":{"type":"integer"}}}`)

	cases := map[string]func() Request{
		"bad_purpose": func() Request {
			r := baseReq()
			r.Purpose = "x"
			return r
		},
		"no_input": func() Request {
			r := baseReq()
			r.Text = ""
			r.FakeHint = "AIFAKE-UNAVAILABLE"
			return r
		},
		"no_schema_name": func() Request {
			r := baseReq()
			r.SchemaName = ""
			return r
		},
		"invalid_schema_json": func() Request {
			r := baseReq()
			r.Schema = json.RawMessage(`{`)
			return r
		},
		"non_object_schema": func() Request {
			r := baseReq()
			r.Schema = json.RawMessage(`{"type":"array"}`)
			return r
		},
		"disallowed_keyword": func() Request {
			r := baseReq()
			r.Schema = disallowedKeyword
			return r
		},
		"additional_properties_true": func() Request {
			r := baseReq()
			r.Schema = additionalPropsTrue
			return r
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(envelope(validContent, "")))
			}))
			t.Cleanup(srv.Close)

			c, _ := fakeClient(t, srv.URL, 15*time.Second)
			_, err := c.Call(t.Context(), build())

			if hits.Load() != 0 {
				t.Fatalf("hits = %d, want 0", hits.Load())
			}
			if err == nil {
				t.Fatal("err = nil, want non-nil")
			}
			if errors.Is(err, ErrUnavailable) {
				t.Errorf("err wraps ErrUnavailable, want a validation error")
			}
		})
	}
}

// -- T18 --

func TestCall_FakeHintIsNeverSent(t *testing.T) {
	var rec capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(validContent, "")))
	}))
	t.Cleanup(srv.Close)

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	req := baseReq()
	req.FakeHint = "HINT-7f3a"

	if _, err := c.Call(t.Context(), req); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	_, _, raw := rec.snapshot()
	if bytes.Contains(raw, []byte("HINT-7f3a")) {
		t.Errorf("request body contains FakeHint text %q, want absent", "HINT-7f3a")
	}
	if !bytes.Contains(raw, []byte("rows")) {
		t.Errorf("request body missing control text %q", "rows")
	}
}

// -- T19 --

func TestCall_CallerDeadlineShorterThanBudgetWins(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Drain so Go's server arms the background read that notices the
		// client disconnect; an unread body leaves r.Context() un-cancelable.
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	c := realClient(t, srv.URL, 15*time.Second)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Call(ctx, baseReq())
	wall := time.Since(start)

	if wall >= 500*time.Millisecond {
		t.Errorf("wall = %v, want < 500ms", wall)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Errorf("err wraps ErrUnavailable, want it distinct from a caller deadline")
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", hits.Load())
	}
}

// -- adversarial: schema refusals --

func TestCall_RefusesABannedKeywordAnywhereInTheSchema(t *testing.T) {
	cases := map[string]string{
		"on_items":               `{"type":"object","additionalProperties":false,"required":["rows"],"properties":{"rows":{"type":"array","items":{"type":"string","minLength":1}}}}`,
		"under_items_properties": `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string","pattern":"^N"}}}}}}`,
		"two_levels_down":        `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"object","properties":{"c":{"type":"string","format":"date"}}}}}}}`,
		"nested_bad_type":        `{"type":"object","properties":{"a":{"type":"date"}}}`,
		"nested_additional_true": `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"object","additionalProperties":true}}}`,
		"items_not_an_object":    `{"type":"object","properties":{"rows":{"type":"array","items":[{"type":"string"}]}}}`,
		"type_is_a_number":       `{"type":"object","properties":{"a":{"type":1}}}`,
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			req := baseReq()
			req.Schema = json.RawMessage(schema)
			assertRefusedBeforeTheWire(t, req)
		})
	}
}

// -- adversarial: deep answer checking --

var nestedSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["buyer"],"properties":{"buyer":{"type":"object","additionalProperties":false,"required":["tin"],"properties":{"tin":{"type":"string"}}}}}`)

const nestedContent = `{"buyer":{"tin":"12345"}}`

func TestCall_AcceptsAnAnswerMatchingADeepSchema(t *testing.T) {
	srv, hits := okServer(t, nestedContent)
	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	req := baseReq()
	req.Schema = nestedSchema

	got, err := c.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	want := map[string]any{"buyer": map[string]any{"tin": "12345"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Call() = %#v, want %#v", got, want)
	}
}

func TestCall_RetriesAnAnswerThatFailsDeepInsideANestedObject(t *testing.T) {
	cases := map[string]string{
		"deep_wrong_type":   `{"buyer":{"tin":7}}`,
		"deep_missing_key":  `{"buyer":{}}`,
		"deep_extra_key":    `{"buyer":{"tin":"1","x":1}}`,
		"branch_not_object": `{"buyer":"1"}`,
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			srv, hits := retryServer(t, envelope(bad, ""), nestedContent)
			c, _ := fakeClient(t, srv.URL, 15*time.Second)
			req := baseReq()
			req.Schema = nestedSchema

			if _, err := c.Call(t.Context(), req); err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			if hits.Load() != 2 {
				t.Fatalf("hits = %d, want 2", hits.Load())
			}
		})
	}
}

func TestCall_ChecksEveryArrayElementAgainstItems(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["rows"],"properties":{"rows":{"type":"array","items":{"type":"string"}}}}`)

	t.Run("last_element_wrong", func(t *testing.T) {
		srv, hits := retryServer(t, envelope(`{"rows":["a","b",3]}`, ""), `{"rows":["a","b"]}`)
		c, _ := fakeClient(t, srv.URL, 15*time.Second)
		req := baseReq()
		req.Schema = schema

		if _, err := c.Call(t.Context(), req); err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		if hits.Load() != 2 {
			t.Fatalf("hits = %d, want 2", hits.Load())
		}
	})

	t.Run("empty_array_is_accepted", func(t *testing.T) {
		srv, hits := okServer(t, `{"rows":[]}`)
		c, _ := fakeClient(t, srv.URL, 15*time.Second)
		req := baseReq()
		req.Schema = schema

		if _, err := c.Call(t.Context(), req); err != nil {
			t.Fatalf("Call() err = %v, want nil", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("hits = %d, want 1", hits.Load())
		}
	})
}

func TestCall_EnumOfNumbersComparesByJSONValue(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["n"],"properties":{"n":{"type":"integer","enum":[1,2,3]}}}`)
	srv, hits := retryServer(t, envelope(`{"n":5}`, ""), `{"n":2}`)
	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	req := baseReq()
	req.Schema = schema

	got, err := c.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
	if got["n"] != json.Number("2") {
		t.Errorf("n = %#v, want json.Number(2)", got["n"])
	}
}

func TestCall_RequiredKeyMissingFromPropertiesIsStillRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["n","ghost"],"properties":{"n":{"type":"integer"}}}`)
	srv, hits := retryServer(t, envelope(`{"n":1}`, ""), `{"n":1,"ghost":"x"}`)
	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	req := baseReq()
	req.Schema = schema

	got, err := c.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
	if got["ghost"] != "x" {
		t.Errorf("ghost = %#v, want x", got["ghost"])
	}
}

func TestCall_RetriesANonIntegerWhereIntegerIsRequired(t *testing.T) {
	srv, hits := retryServer(t, envelope(`{"total":"1","vat":null,"currency":"NGN","n":3.5}`, ""), validContent)
	c, _ := fakeClient(t, srv.URL, 15*time.Second)

	if _, err := c.Call(t.Context(), baseReq()); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestCall_AcceptsAFractionWhereNumberIsAllowed(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["n"],"properties":{"n":{"type":"number"}}}`)
	srv, hits := okServer(t, `{"n":3.5}`)
	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	req := baseReq()
	req.Schema = schema

	got, err := c.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if got["n"] != json.Number("3.5") {
		t.Errorf("n = %#v, want json.Number(3.5)", got["n"])
	}
}

// -- adversarial: response envelope --

func TestCall_RetriesContentThatIsNotOneJSONObject(t *testing.T) {
	cases := map[string]string{
		"array":       `[{"total":"1","vat":null,"currency":"NGN","n":1}]`,
		"bare_string": `"1935.00"`,
		"bare_number": `5`,
		"json_null":   `null`,
		"empty":       ``,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			srv, hits := retryServer(t, envelope(content, ""), validContent)
			c, _ := fakeClient(t, srv.URL, 15*time.Second)

			if _, err := c.Call(t.Context(), baseReq()); err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			if hits.Load() != 2 {
				t.Fatalf("hits = %d, want 2", hits.Load())
			}
		})
	}
}

func TestCall_ANullErrorKeyIsNotAnError(t *testing.T) {
	content, _ := json.Marshal(validContent)
	body := `{"choices":[{"message":{"content":` + string(content) + `}}],"error":null}`

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	got, err := c.Call(t.Context(), baseReq())
	if err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if got["currency"] != "NGN" {
		t.Errorf("currency = %#v, want NGN", got["currency"])
	}
}

func TestCall_AnOddUsageObjectDoesNotFailTheCall(t *testing.T) {
	cases := map[string]string{
		"partially_null":  `{"prompt_tokens":5,"completion_tokens":null,"cost":null}`,
		"null_usage":      `null`,
		"empty_object":    `{}`,
		"fractional_cost": `{"prompt_tokens":5,"completion_tokens":7,"cost":0.00042}`,
	}
	for name, usageJSON := range cases {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(envelope(validContent, usageJSON)))
			}))
			t.Cleanup(srv.Close)

			c, _ := fakeClient(t, srv.URL, 15*time.Second)
			got, err := c.Call(t.Context(), baseReq())
			if err != nil {
				t.Fatalf("Call() err = %v, want nil", err)
			}
			if hits.Load() != 1 {
				t.Fatalf("hits = %d, want 1", hits.Load())
			}
			if got["currency"] != "NGN" {
				t.Errorf("currency = %#v, want NGN", got["currency"])
			}
		})
	}
}

func TestCall_RedirectStatusIsRefused(t *testing.T) {
	// No Location header, so net/http hands the 3xx back instead of following it.
	for _, status := range []int{301, 303, 304} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(status)
			}))
			t.Cleanup(srv.Close)

			c, _ := fakeClient(t, srv.URL, 15*time.Second)
			_, err := c.Call(t.Context(), baseReq())
			if hits.Load() != 1 {
				t.Fatalf("hits = %d, want 1", hits.Load())
			}
			if err == nil {
				t.Fatal("err = nil, want non-nil")
			}
			if errors.Is(err, ErrUnavailable) {
				t.Errorf("err wraps ErrUnavailable, want a distinct refusal error")
			}
		})
	}
}

// -- adversarial: wire shape --

func TestCall_SendsManyPagesInOrderIncludingAnEmptyOne(t *testing.T) {
	var rec capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(envelope(validContent, "")))
	}))
	t.Cleanup(srv.Close)

	pages := make([][]byte, 12)
	for i := range pages {
		pages[i] = []byte{0x89, 'P', 'N', 'G', byte(i)}
	}
	pages[3] = []byte{}

	c, _ := fakeClient(t, srv.URL, 15*time.Second)
	req := baseReq()
	req.Pages = pages
	if _, err := c.Call(t.Context(), req); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}

	_, body, _ := rec.snapshot()
	messages, _ := body["messages"].([]any)
	user, _ := messages[1].(map[string]any)
	parts, _ := user["content"].([]any)
	if len(parts) != len(pages)+1 {
		t.Fatalf("len(parts) = %d, want %d", len(parts), len(pages)+1)
	}
	for i, page := range pages {
		part, _ := parts[i+1].(map[string]any)
		imgURL, _ := part["image_url"].(map[string]any)
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(page)
		if imgURL["url"] != want {
			t.Errorf("parts[%d].image_url.url = %v, want %v", i+1, imgURL["url"], want)
		}
	}
}

func TestCall_FakeHintAloneIsNotInput(t *testing.T) {
	req := baseReq()
	req.Text = ""
	req.Pages = nil
	req.FakeHint = "HINT-7f3a"
	assertRefusedBeforeTheWire(t, req)
}

// -- adversarial: budget and clock --

func TestCall_ASpentBudgetRefusesBeforeTheFirstAnswer(t *testing.T) {
	for name, b := range map[string]time.Duration{"zero": 0, "negative": -time.Second} {
		t.Run(name, func(t *testing.T) {
			srv, hits := okServer(t, validContent)
			c, _ := fakeClient(t, srv.URL, b)

			_, err := callWithin(t, c, t.Context(), baseReq(), 2*time.Second)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err wraps context.DeadlineExceeded, want it distinct from a caller deadline")
			}
			if hits.Load() > 1 {
				t.Errorf("hits = %d, want <= 1", hits.Load())
			}
		})
	}
}

// backwardClock hands out one reading earlier than the previous one, once,
// right after the HTTP attempt the caller arms it on — independent of how
// many times production code happens to read the clock before that.
type backwardClock struct {
	mu     sync.Mutex
	t      time.Time
	armed  bool
	jumped bool
}

func (b *backwardClock) now() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.armed && !b.jumped {
		b.jumped = true
		return b.t.Add(-time.Second)
	}
	return b.t
}

// arm makes the clock's next reading jump backward.
func (b *backwardClock) arm() {
	b.mu.Lock()
	b.armed = true
	b.mu.Unlock()
}

// didJump reports whether the backward reading was actually handed out, so a
// run that armed nothing cannot pass as a run that absorbed a jump.
func (b *backwardClock) didJump() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.jumped
}

func (b *backwardClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	b.t = b.t.Add(d)
	b.mu.Unlock()
	return nil
}

// backwardClockRun is one 503-only Call under a backward clock.
type backwardClockRun struct {
	attempts int
	hits     int32
	jumped   bool
	err      error
}

// runUnderBackwardClock retries a 503 server until the budget ends the loop,
// arming the backward jump right after hit armAfter (0 never arms). It reads
// the attempt count off the log line: the round the jump buys dials nothing,
// because the widened budget hands that attempt an already-expired context.
func runUnderBackwardClock(t *testing.T, armAfter int32) backwardClockRun {
	t.Helper()
	var hits atomic.Int32
	bc := &backwardClock{t: time.Unix(0, 0)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := hits.Add(1); n == armAfter {
			bc.arm()
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c, buf := logClient(t, config{key: "k", endpoint: srv.URL, budget: time.Second, now: bc.now, sleep: bc.sleep})
	_, err := callWithin(t, c, t.Context(), baseReq(), 5*time.Second)
	attempts, _ := only(t, buf)["attempts"].(float64)
	return backwardClockRun{attempts: int(attempts), hits: hits.Load(), jumped: bc.didJump(), err: err}
}

func TestCall_AClockThatGoesBackwardsStillEndsTheLoop(t *testing.T) {
	// The control run fixes where the jump goes: the last attempt's deadline
	// check is the one whose decision a one-second backward reading flips.
	// Arm it anywhere earlier and the jump changes nothing, which is how this
	// test used to pass with no backward reading at all.
	control := runUnderBackwardClock(t, 0)
	if !errors.Is(control.err, ErrUnavailable) {
		t.Fatalf("control err = %v, want ErrUnavailable", control.err)
	}
	if control.jumped {
		t.Fatal("the control clock jumped backward, want it forward-only")
	}
	if control.hits < 2 {
		t.Fatalf("control hits = %d, want >= 2", control.hits)
	}

	jumped := runUnderBackwardClock(t, int32(control.attempts))
	if !errors.Is(jumped.err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", jumped.err)
	}
	if !jumped.jumped {
		t.Fatal("the clock never handed out its backward reading; the run proves nothing")
	}
	if jumped.attempts != control.attempts+1 {
		t.Errorf("attempts = %d, want %d: the loop absorbs the backward second and ends one round later",
			jumped.attempts, control.attempts+1)
	}
}

// -- adversarial: the caller's context --

// errCtx reports an error but has no Done channel, so the HTTP attempt itself
// is never aborted and only the post-attempt caller-ctx check can see it.
type errCtx struct{ context.Context }

func (errCtx) Done() <-chan struct{} { return nil }

func (errCtx) Err() error { return context.Canceled }

func TestCall_ACancelledCallerBeatsASuccessfulAnswer(t *testing.T) {
	srv, hits := okServer(t, validContent)
	c, _ := fakeClient(t, srv.URL, 15*time.Second)

	got, err := callWithin(t, c, errCtx{t.Context()}, baseReq(), 2*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Errorf("err wraps ErrUnavailable, want it distinct from a caller cancellation")
	}
	if got != nil {
		t.Errorf("answer = %#v, want nil", got)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1 (the attempt is sent before the check)", hits.Load())
	}
}

func TestCall_AnErrorObjectBesideAValidChoiceIsRetried(t *testing.T) {
	content, _ := json.Marshal(validContent)
	hit1 := `{"choices":[{"message":{"content":` + string(content) + `}}],"error":{"code":502,"message":"x"}}`
	srv, hits := retryServer(t, hit1, validContent)
	c, _ := fakeClient(t, srv.URL, 15*time.Second)

	if _, err := c.Call(t.Context(), baseReq()); err != nil {
		t.Fatalf("Call() err = %v, want nil", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

// The documented schedule for a 15s budget: 0.25, 0.5, 1, then 2 capped,
// which leaves 1.25s before the 10th wait and ends the loop at 10 hits.
func TestCall_BackoffIsCappedSoTheBudgetFitsTenAttempts(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c, fc := fakeClient(t, srv.URL, 15*time.Second)
	if _, err := callWithin(t, c, t.Context(), baseReq(), 5*time.Second); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if got := hits.Load(); got != 10 {
		t.Errorf("hits = %d, want 10", got)
	}
	if got := fc.now().Sub(time.Unix(0, 0)); got != 13750*time.Millisecond {
		t.Errorf("slept %v in total, want 13.75s", got)
	}
}
