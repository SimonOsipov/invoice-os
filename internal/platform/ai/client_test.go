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
	_, err := c.Call(t.Context(), baseReq())
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
