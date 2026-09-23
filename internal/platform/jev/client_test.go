package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// -- helpers --

type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(0, 0)} }

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// sleep advances the clock by d, as a real sleep would.
func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
	f.sleeps = append(f.sleeps, d)
	return nil
}

func (f *fakeClock) sleepCalls() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sleeps)
}

func testSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type testServer struct {
	*httptest.Server
	hits    atomic.Int32
	release chan struct{}
}

// newServer calls h with the 1-based hit number.
func newServer(t *testing.T, h func(n int32, w http.ResponseWriter, r *http.Request)) *testServer {
	t.Helper()
	ts := &testServer{release: make(chan struct{})}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(ts.hits.Add(1), w, r)
	}))
	t.Cleanup(ts.Close)
	// Cleanups run last-first: a handler still blocked in hang is freed before Close waits on it.
	t.Cleanup(func() { close(ts.release) })
	return ts
}

// hang drains the body, without which the server never sees the client
// disconnect, then blocks until the attempt or the test ends.
func (ts *testServer) hang(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-ts.release:
	}
}

func (ts *testServer) endpoint() string { return ts.URL + "/v1/systemone" }

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// hijackClose drops the connection with no response: a transport error.
func hijackClose(t *testing.T, w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Error("ResponseWriter does not implement http.Hijacker")
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Errorf("Hijack: %v", err)
		return
	}
	_ = conn.Close()
}

type captured struct {
	method, path string
	header       http.Header
	raw          []byte
}

type capture struct {
	mu   sync.Mutex
	reqs []captured
}

func (c *capture) record(r *http.Request) []byte {
	raw, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, captured{r.Method, r.URL.Path, r.Header.Clone(), raw})
	return raw
}

func (c *capture) only(t *testing.T) captured {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) != 1 {
		t.Fatalf("captured %d request(s), want exactly 1", len(c.reqs))
	}
	return c.reqs[0]
}

// clockClient uses the production budget and wait on fc.
func clockClient(ts *testServer, fc *fakeClock) *Client {
	return newClient(config{key: "k", endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)
}

func realClient(ts *testServer, b, wait time.Duration) *Client {
	return newClient(config{key: "k", endpoint: ts.endpoint(), budget: b, retryWait: wait, now: time.Now, sleep: testSleep}, nil)
}

// askWithin fails the test if Ask has not returned after d, so a missing
// timeout reds in seconds instead of hanging.
func askWithin(t *testing.T, c *Client, ctx context.Context, req Request, d time.Duration) (Response, error) {
	t.Helper()
	type outcome struct {
		resp Response
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		resp, err := c.Ask(ctx, req)
		ch <- outcome{resp, err}
	}()
	select {
	case got := <-ch:
		return got.resp, got.err
	case <-time.After(d):
		t.Fatalf("Ask did not return within %v", d)
		return Response{}, nil
	}
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

const (
	textOff         = "jev: check skipped: off"
	textRefused     = "jev: check skipped: refused"
	textUnavailable = "jev: check skipped: unavailable"
)

// c21Texts is every error text the client may return.
var c21Texts = []string{
	textOff,
	textRefused,
	textUnavailable,
	textRefused + " (fake)",
	textUnavailable + " (fake)",
	textUnavailable + ": context canceled",
	textUnavailable + ": context deadline exceeded",
}

// wantSkipped asserts err wraps ErrCheckSkipped with exactly text.
func wantSkipped(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Ask() err = nil, want %q", text)
	}
	if !errors.Is(err, ErrCheckSkipped) {
		t.Errorf("Ask() err = %v, want errors.Is ErrCheckSkipped", err)
	}
	if got := err.Error(); got != text {
		t.Errorf("Ask() err text = %q, want %q", got, text)
	}
}

func wantHits(t *testing.T, ts *testServer, want int32) {
	t.Helper()
	if got := ts.hits.Load(); got != want {
		t.Fatalf("hits = %d, want %d", got, want)
	}
}

func noulReq(state string) Request {
	return Request{
		Purpose:   PurposeValueCheck,
		State:     state,
		Questions: map[string]Question{"q1": {Type: TypeNoul, Instructions: "Is the total 1,935.00?"}},
	}
}

const noulOK = `{"model":"jev-1.13.0","answers":{"q1":{"type":"noul","noul":0.95}},"usage":{"input_tokens":318,"output_tokens":34}}`

func wantNoul(t *testing.T, resp Response, id string, want float64) {
	t.Helper()
	a, ok := resp.Answers[id]
	if !ok {
		t.Fatalf("answers = %v, want an answer for %q", resp.Answers, id)
	}
	if a.Type != TypeNoul || a.Noul != want {
		t.Errorf("answers[%q] = %+v, want noul %v", id, a, want)
	}
}

// threeTypeReq asks one question of each type.
func threeTypeReq() Request {
	return Request{
		Purpose: PurposeDocumentType,
		State:   "s",
		Questions: map[string]Question{
			"n": {Type: TypeNoul, Instructions: "Is it an invoice?"},
			"c": {Type: TypeChoice, Instructions: "Which document type?", Options: []Option{
				{"tax invoice", "A tax invoice"}, {"receipt", ""}, {"credit note", "A credit note"},
			}, Default: "receipt"},
			"s": {Type: TypeScore, Instructions: "How legible is it?", Options: []Option{
				{"l0", "illegible"}, {"l1", "partly legible"}, {"l2", "legible"},
			}, Default: "l2"},
		},
	}
}

const threeTypeOK = `{"answers":{"n":{"type":"noul","noul":0.9},"c":{"type":"choice","choice":"receipt","confidence":0.9},"s":{"type":"score","score":2,"confidence":0.9}},"usage":{"input_tokens":5,"output_tokens":0}}`

// -- AC-1 --

func TestAsk_SendsOnePostForTheModel(t *testing.T) {
	var rec capture
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		reply(w, http.StatusOK, noulOK)
	})
	c := clockClient(ts, newFakeClock())

	_, err := c.Ask(t.Context(), noulReq("s"))
	wantHits(t, ts, 1)
	if err != nil {
		t.Errorf("Ask() err = %v, want nil", err)
	}

	got := rec.only(t)
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.path != "/v1/systemone" {
		t.Errorf("path = %q, want /v1/systemone", got.path)
	}
	if h := got.header.Get("Authorization"); h != "Bearer k" {
		t.Errorf("Authorization = %q, want %q", h, "Bearer k")
	}
	if h := got.header.Get("Content-Type"); h != "application/json" {
		t.Errorf("Content-Type = %q, want %q", h, "application/json")
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(got.raw, &body); err != nil {
		t.Fatalf("body is not a JSON object: %v", err)
	}
	if keys := slices.Sorted(maps.Keys(body)); !slices.Equal(keys, []string{"model", "questions", "state"}) {
		t.Errorf("body keys = %v, want exactly [model questions state]", keys)
	}
	var model string
	if err := json.Unmarshal(body["model"], &model); err != nil || model != "jev-latest" {
		t.Errorf("model = %q (err %v), want %q", model, err, "jev-latest")
	}
}

func TestAsk_StateIsSentVerbatim(t *testing.T) {
	const state = " line one\nline \"two\"\tcol\n₦1,935.00\r\nend\n"
	var rec capture
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		reply(w, http.StatusOK, noulOK)
	})
	c := clockClient(ts, newFakeClock())

	_, _ = c.Ask(t.Context(), noulReq(state))
	wantHits(t, ts, 1)

	var body struct {
		State *string `json:"state"`
	}
	if err := json.Unmarshal(rec.only(t).raw, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.State == nil {
		t.Fatal("body has no state")
	}
	if *body.State != state {
		t.Errorf("state = %q, want %q byte for byte", *body.State, state)
	}
}

// -- AC-2 --

// jsonKeys returns every object key at any depth.
func jsonKeys(v any) []string {
	var keys []string
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			keys = append(keys, k)
			keys = append(keys, jsonKeys(child)...)
		}
	case []any:
		for _, child := range x {
			keys = append(keys, jsonKeys(child)...)
		}
	}
	return keys
}

func TestAsk_EachQuestionTypeHasItsWireShape(t *testing.T) {
	var rec capture
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		reply(w, http.StatusOK, `{"answers":{"bare":{"type":"noul","noul":0.9},"tf":{"type":"noul","noul":0.8},`+
			`"kind":{"type":"choice","choice":"receipt","confidence":0.9},"level":{"type":"score","score":1,"confidence":0.9}}}`)
	})
	c := clockClient(ts, newFakeClock())

	req := Request{
		Purpose: PurposeDocumentType,
		State:   "s",
		Questions: map[string]Question{
			"bare": {Type: TypeNoul, Instructions: "Is it signed?"},
			"tf":   {Type: TypeNoul, Instructions: "Is the TIN right?", True: "the TIN matches", False: "the TIN differs"},
			"kind": {Type: TypeChoice, Instructions: "Which type?", Options: []Option{
				{"tax invoice", "A tax invoice"}, {"receipt", ""}, {"credit note", "A credit note"},
			}, Default: "receipt"},
			"level": {Type: TypeScore, Instructions: "How legible?", Options: []Option{
				{"SCORENAME-a", "low"}, {"SCORENAME-b", "mid"}, {"SCORENAME-c", "high"},
			}, Default: "SCORENAME-b"},
		},
	}
	_, _ = c.Ask(t.Context(), req)
	wantHits(t, ts, 1)
	raw := rec.only(t).raw

	var body struct {
		Questions map[string]map[string]any `json:"questions"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	want := map[string]map[string]any{
		"bare": {"type": "noul", "instructions": "Is it signed?"},
		"tf": {"type": "noul", "instructions": "Is the TIN right?",
			"criteria": map[string]any{"true": "the TIN matches", "false": "the TIN differs"}},
		"kind": {"type": "choice", "instructions": "Which type?",
			"criteria": map[string]any{"tax invoice": "A tax invoice", "receipt": nil, "credit note": "A credit note"}},
		"level": {"type": "score", "instructions": "How legible?",
			"criteria": []any{"low", "mid", "high"}},
	}
	if len(body.Questions) == 0 {
		t.Fatal("questions is empty on the wire")
	}
	for id, wantQ := range want {
		gotQ, ok := body.Questions[id]
		if !ok {
			t.Errorf("questions[%q] missing", id)
			continue
		}
		if gk, wk := slices.Sorted(maps.Keys(gotQ)), slices.Sorted(maps.Keys(wantQ)); !slices.Equal(gk, wk) {
			t.Errorf("questions[%q] keys = %v, want exactly %v", id, gk, wk)
		}
		if !reflect.DeepEqual(gotQ, wantQ) {
			t.Errorf("questions[%q] = %v, want %v", id, gotQ, wantQ)
		}
	}
	if len(body.Questions) != len(want) {
		t.Errorf("questions carries %d id(s), want %d", len(body.Questions), len(want))
	}

	// encoding/json sorts map keys; the fixture's order is not sorted.
	i1, i2, i3 := strings.Index(string(raw), `"credit note"`), strings.Index(string(raw), `"receipt"`), strings.Index(string(raw), `"tax invoice"`)
	if i1 < 0 || i2 < 0 || i3 < 0 || i1 >= i2 || i2 >= i3 {
		t.Errorf("choice criteria keys at offsets %d, %d, %d, want sorted: credit note, receipt, tax invoice", i1, i2, i3)
	}

	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	keys := jsonKeys(decoded)
	if !slices.Contains(keys, "instructions") || !slices.Contains(keys, "criteria") {
		t.Fatalf("key walk found %v, missing the control keys instructions/criteria", keys)
	}
	for _, k := range keys {
		if strings.EqualFold(k, "default") {
			t.Errorf("wire body carries a key %q, want no default key at any depth", k)
		}
	}
	if strings.Contains(string(raw), "SCORENAME-") {
		t.Errorf("wire body carries a score option Name: %s", raw)
	}
}

// -- AC-3 --

func TestAsk_DecodesTheVendorsDocumentedAnswers(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, `{"model":"jev-1.13.0","answers":{`+
			`"is_invoice":{"type":"noul","noul":0.95},`+
			`"department":{"type":"choice","choice":"billing","probabilities":{"billing":0.81,"technical":0.12,"sales":0.07},"confidence":0.81},`+
			`"urgency":{"type":"score","score":1.05,"legend":{"0":"Not urgent","1":"Somewhat urgent","2":"Very urgent"},"probabilities":{"0":0.05,"1":0.85,"2":0.1},"confidence":0.92}},`+
			`"usage":{"input_tokens":318,"output_tokens":34}}`)
	})
	c := clockClient(ts, newFakeClock())

	req := Request{
		Purpose: PurposeValueCheck,
		State:   "s",
		Questions: map[string]Question{
			"is_invoice": {Type: TypeNoul, Instructions: "Is this an invoice?"},
			"department": {Type: TypeChoice, Instructions: "Which department?", Options: []Option{
				{"billing", "Payments"}, {"technical", "Bugs"}, {"sales", "Pricing"},
			}, Default: "billing"},
			"urgency": {Type: TypeScore, Instructions: "How urgent?", Options: []Option{
				{"low", "Not urgent"}, {"mid", "Somewhat urgent"}, {"high", "Very urgent"},
			}, Default: "low"},
		},
	}
	resp, err := c.Ask(t.Context(), req)
	if err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	wantHits(t, ts, 1)
	want := map[string]Answer{
		"is_invoice": {Type: TypeNoul, Noul: 0.95},
		"department": {Type: TypeChoice, Choice: "billing", Confidence: 0.81},
		"urgency":    {Type: TypeScore, Score: 1.05, Confidence: 0.92},
	}
	if len(resp.Answers) == 0 {
		t.Fatal("Ask() returned no answers")
	}
	if !reflect.DeepEqual(resp.Answers, want) {
		t.Errorf("answers = %+v, want %+v", resp.Answers, want)
	}
	if resp.Usage != (Usage{InputTokens: 318, OutputTokens: 34}) {
		t.Errorf("usage = %+v, want {318 34}", resp.Usage)
	}
}

func TestAsk_AMissingUsageIsZeroNotAFailure(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, `{"answers":{"q1":{"type":"noul","noul":0.95}}}`)
	})
	c := clockClient(ts, newFakeClock())

	resp, err := c.Ask(t.Context(), noulReq("s"))
	if err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	wantHits(t, ts, 1)
	wantNoul(t, resp, "q1", 0.95)
	if resp.Usage != (Usage{}) {
		t.Errorf("usage = %+v, want {0 0}", resp.Usage)
	}
}

func TestAsk_UsageIsSummedOverAttempts(t *testing.T) {
	ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			reply(w, http.StatusInternalServerError, `{"usage":{"input_tokens":7,"output_tokens":1}}`)
			return
		}
		reply(w, http.StatusOK, noulOK)
	})
	c := clockClient(ts, newFakeClock())

	resp, err := c.Ask(t.Context(), noulReq("s"))
	wantHits(t, ts, 2)
	if err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	wantNoul(t, resp, "q1", 0.95)
	if resp.Usage != (Usage{InputTokens: 325, OutputTokens: 35}) {
		t.Errorf("usage = %+v, want {325 35}: attempt 1's 7/1 plus attempt 2's 318/34", resp.Usage)
	}
}

// -- AC-4 --

func TestAsk_AnAnswerMissingAQuestionKeyFails(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, `{"answers":{"q1":{"type":"noul","noul":0.95}}}`)
	})
	c := clockClient(ts, newFakeClock())

	req := noulReq("s")
	req.Questions["q2"] = Question{Type: TypeNoul, Instructions: "Is the date right?"}
	_, err := c.Ask(t.Context(), req)
	wantHits(t, ts, 1)
	wantSkipped(t, err, textUnavailable)
}

func TestAsk_AnAnswerOfTheWrongTypeFails(t *testing.T) {
	noulQ := Question{Type: TypeNoul, Instructions: "Is the total right?"}
	choiceQ := Question{Type: TypeChoice, Instructions: "Which type?", Options: []Option{
		{"tax invoice", ""}, {"credit note", ""},
	}, Default: "tax invoice"}
	scoreQ := Question{Type: TypeScore, Instructions: "How legible?", Options: []Option{
		{"l0", "low"}, {"l1", "mid"}, {"l2", "high"},
	}, Default: "l0"}

	cases := []struct {
		name   string
		q      Question
		answer string
		want   *Answer // nil: the call must fail
	}{
		// Every field valid for either type, so only the type clause can refuse it.
		{"noul_answered_as_choice", noulQ, `{"type":"choice","noul":0.9,"choice":"tax invoice","confidence":0.9}`, nil},
		{"noul_as_a_string", noulQ, `{"type":"noul","noul":"0.9"}`, nil},
		{"noul_above_one", noulQ, `{"type":"noul","noul":1.5}`, nil},
		{"noul_below_zero", noulQ, `{"type":"noul","noul":-0.01}`, nil},
		{"noul_value_missing", noulQ, `{"type":"noul"}`, nil},
		{"noul_null", noulQ, `{"type":"noul","noul":null}`, nil},
		{"choice_not_asked", choiceQ, `{"type":"choice","choice":"receipt","confidence":0.9}`, nil},
		{"choice_without_confidence", choiceQ, `{"type":"choice","choice":"tax invoice"}`, nil},
		{"choice_null_confidence", choiceQ, `{"type":"choice","choice":"tax invoice","confidence":null}`, nil},
		{"choice_confidence_above_one", choiceQ, `{"type":"choice","choice":"tax invoice","confidence":1.01}`, nil},
		{"choice_confidence_below_zero", choiceQ, `{"type":"choice","choice":"tax invoice","confidence":-0.01}`, nil},
		{"score_without_score", scoreQ, `{"type":"score","confidence":0.9}`, nil},
		{"score_null", scoreQ, `{"type":"score","score":null,"confidence":0.9}`, nil},
		{"score_below_zero", scoreQ, `{"type":"score","score":-0.01,"confidence":0.9}`, nil},
		{"score_above_the_top_level", scoreQ, `{"type":"score","score":2.01,"confidence":0.9}`, nil},
		{"score_confidence_above_one", scoreQ, `{"type":"score","score":1,"confidence":1.01}`, nil},

		{"control_noul_one", noulQ, `{"type":"noul","noul":1.0}`, &Answer{Type: TypeNoul, Noul: 1}},
		{"control_noul_zero", noulQ, `{"type":"noul","noul":0}`, &Answer{Type: TypeNoul, Noul: 0}},
		{"control_score_zero", scoreQ, `{"type":"score","score":0,"confidence":0.9}`, &Answer{Type: TypeScore, Score: 0, Confidence: 0.9}},
		{"control_score_top_level", scoreQ, `{"type":"score","score":2.0,"confidence":0.9}`, &Answer{Type: TypeScore, Score: 2, Confidence: 0.9}},
		{"control_choice_confidence_zero", choiceQ, `{"type":"choice","choice":"tax invoice","confidence":0}`, &Answer{Type: TypeChoice, Choice: "tax invoice", Confidence: 0}},
		{"control_choice_confidence_one", choiceQ, `{"type":"choice","choice":"credit note","confidence":1}`, &Answer{Type: TypeChoice, Choice: "credit note", Confidence: 1}},
		{"control_score_confidence_zero", scoreQ, `{"type":"score","score":1,"confidence":0}`, &Answer{Type: TypeScore, Score: 1, Confidence: 0}},
		{"control_score_confidence_one", scoreQ, `{"type":"score","score":1,"confidence":1}`, &Answer{Type: TypeScore, Score: 1, Confidence: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
				reply(w, http.StatusOK, `{"answers":{"q":`+tc.answer+`},"usage":{"input_tokens":1,"output_tokens":0}}`)
			})
			c := clockClient(ts, newFakeClock())
			req := Request{Purpose: PurposeValueCheck, State: "s", Questions: map[string]Question{"q": tc.q}}

			resp, err := c.Ask(t.Context(), req)
			wantHits(t, ts, 1)
			if tc.want == nil {
				wantSkipped(t, err, textUnavailable)
				return
			}
			if err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			got, ok := resp.Answers["q"]
			if !ok {
				t.Fatalf("answers = %v, want an answer for q", resp.Answers)
			}
			if got != *tc.want {
				t.Errorf("answers[q] = %+v, want %+v", got, *tc.want)
			}
		})
	}
}

func TestAsk_MalformedBodyFailsWithoutARetry(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, "not json")
	})
	c := clockClient(ts, newFakeClock())

	_, err := c.Ask(t.Context(), noulReq("s"))
	wantHits(t, ts, 1)
	wantSkipped(t, err, textUnavailable)
}

// -- AC-5 --

func TestAsk_OneFailureThenSuccess(t *testing.T) {
	ts := newServer(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			reply(w, http.StatusServiceUnavailable, "")
			return
		}
		reply(w, http.StatusOK, noulOK)
	})
	c := clockClient(ts, newFakeClock())

	resp, err := c.Ask(t.Context(), noulReq("s"))
	wantHits(t, ts, 2)
	if err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	wantNoul(t, resp, "q1", 0.95)
}

func TestAsk_RetriesEachRetryableStatusOnce(t *testing.T) {
	for _, status := range []int{408, 429, 500, 503, 529} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			ts := newServer(t, func(n int32, w http.ResponseWriter, r *http.Request) {
				if n == 1 {
					reply(w, status, "")
					return
				}
				reply(w, http.StatusOK, noulOK)
			})
			c := clockClient(ts, newFakeClock())

			resp, err := c.Ask(t.Context(), noulReq("s"))
			wantHits(t, ts, 2)
			if err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			wantNoul(t, resp, "q1", 0.95)
		})
	}
}

func TestAsk_TwoRetryableFailuresSendExactlyTwoRequests(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		reply(w, 529, "")
	})
	c := clockClient(ts, newFakeClock())

	_, err := c.Ask(t.Context(), noulReq("s"))
	wantHits(t, ts, 2)
	wantSkipped(t, err, textUnavailable)
}

func TestAsk_NetworkErrorIsRetried(t *testing.T) {
	ts := newServer(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			hijackClose(t, w)
			return
		}
		reply(w, http.StatusOK, noulOK)
	})
	c := clockClient(ts, newFakeClock())

	resp, err := c.Ask(t.Context(), noulReq("s"))
	wantHits(t, ts, 2)
	if err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	wantNoul(t, resp, "q1", 0.95)
}

// Nil error with 2 hits proves the per-attempt cap: uncapped, attempt 1
// would spend the budget and no retry would fit.
func TestAsk_ASlowAttemptTimesOutAndIsRetried(t *testing.T) {
	var ts *testServer
	ts = newServer(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			ts.hang(w, r)
			return
		}
		reply(w, http.StatusOK, noulOK)
	})
	c := realClient(ts, 400*time.Millisecond, 50*time.Millisecond)

	start := time.Now()
	resp, err := askWithin(t, c, t.Context(), noulReq("s"), 2*time.Second)
	wall := time.Since(start)

	wantHits(t, ts, 2)
	if err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	wantNoul(t, resp, "q1", 0.95)
	if wall >= time.Second {
		t.Errorf("Ask() took %v, want < 1s", wall)
	}
}

func TestAsk_EveryAttemptHangingEndsWithinTheBudget(t *testing.T) {
	var ts *testServer
	ts = newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		ts.hang(w, r)
	})
	c := realClient(ts, 400*time.Millisecond, 50*time.Millisecond)

	start := time.Now()
	_, err := askWithin(t, c, t.Context(), noulReq("s"), 2*time.Second)
	wall := time.Since(start)

	wantHits(t, ts, 2)
	wantSkipped(t, err, textUnavailable)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v wraps context.DeadlineExceeded, want an attempt timeout kept apart from a caller deadline", err)
	}
	if wall >= time.Second {
		t.Errorf("Ask() took %v, want < 1s", wall)
	}
}

func TestAsk_BudgetSpentSkipsWithoutARetry(t *testing.T) {
	fc := newFakeClock()
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		fc.advance(2900 * time.Millisecond)
		reply(w, http.StatusServiceUnavailable, "")
	})
	c := clockClient(ts, fc)

	_, err := c.Ask(t.Context(), noulReq("s"))
	wantHits(t, ts, 1)
	wantSkipped(t, err, textUnavailable)
}

// Asserts on the sleep, not on hit 2: after a 2.749s advance the second
// attempt's real-time cap is ~1ms and may never reach the server.
func TestAsk_TheRetryNeedsMoreThanTheWaitLeft(t *testing.T) {
	cases := []struct {
		name       string
		advance    time.Duration
		wantSleeps []time.Duration
	}{
		{"exactly_the_wait_left", budget - retryWait, nil},
		{"one_ms_more_than_the_wait", budget - retryWait - time.Millisecond, []time.Duration{retryWait}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClock()
			ts := newServer(t, func(n int32, w http.ResponseWriter, r *http.Request) {
				if n == 1 {
					fc.advance(tc.advance)
				}
				reply(w, http.StatusServiceUnavailable, "")
			})
			c := clockClient(ts, fc)

			_, err := c.Ask(t.Context(), noulReq("s"))
			if got := fc.sleepCalls(); !slices.Equal(got, tc.wantSleeps) {
				t.Errorf("sleep calls = %v, want %v", got, tc.wantSleeps)
			}
			if tc.wantSleeps == nil {
				wantHits(t, ts, 1)
			} else if got := ts.hits.Load(); got < 1 {
				t.Errorf("hits = %d, want at least 1", got)
			}
			if !errors.Is(err, ErrCheckSkipped) {
				t.Errorf("Ask() err = %v, want errors.Is ErrCheckSkipped", err)
			}
		})
	}
}

// -- AC-6 --

func TestAsk_RefusedStatusIsNotRetried(t *testing.T) {
	for _, status := range []int{401, 422, 400, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
				reply(w, status, `{"detail":"no"}`)
			})
			c := clockClient(ts, newFakeClock())

			_, err := c.Ask(t.Context(), noulReq("s"))
			wantHits(t, ts, 1)
			wantSkipped(t, err, textRefused)
		})
	}
}

func TestAsk_ARedirectWithoutALocationIsRefused(t *testing.T) {
	// No Location header, so net/http hands the 302 back instead of following it.
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	})
	c := clockClient(ts, newFakeClock())

	_, err := c.Ask(t.Context(), noulReq("s"))
	wantHits(t, ts, 1)
	wantSkipped(t, err, textRefused)
}

// -- AC-7 --

func TestAsk_EveryFailureIsTheOneSentinel(t *testing.T) {
	cases := []struct {
		name    string
		handler func(n int32, w http.ResponseWriter, r *http.Request)
		key     string
		req     Request
		cancel  bool
		want    string
	}{
		{name: "off", key: "", req: noulReq("s"), want: textOff},
		{name: "validation_refusal", key: "k", req: Request{State: "s", Questions: noulReq("s").Questions}, want: textRefused},
		{name: "status_401", key: "k", req: noulReq("s"), want: textRefused,
			handler: func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 401, "") }},
		{name: "status_503_twice", key: "k", req: noulReq("s"), want: textUnavailable,
			handler: func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 503, "") }},
		{name: "malformed_200", key: "k", req: noulReq("s"), want: textUnavailable,
			handler: func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 200, "not json") }},
		{name: "missing_answer_key", key: "k", req: noulReq("s"), want: textUnavailable,
			handler: func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 200, `{"answers":{}}`) }},
		{name: "cancelled_ctx", key: "k", req: noulReq("s"), cancel: true, want: textUnavailable + ": context canceled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.handler
			if h == nil {
				h = func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 200, noulOK) }
			}
			ts := newServer(t, h)
			fc := newFakeClock()
			c := newClient(config{key: tc.key, endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)
			ctx := t.Context()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			_, err := c.Ask(ctx, tc.req)
			if !errors.Is(err, ErrCheckSkipped) {
				t.Errorf("Ask() err = %v, want errors.Is ErrCheckSkipped", err)
			}
			if !slices.Contains(c21Texts, errText(err)) {
				t.Errorf("Ask() err text = %q, want one of %q", errText(err), c21Texts)
			}
			if got := errText(err); got != tc.want {
				t.Errorf("Ask() err text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAsk_AnErrorNeverCarriesTheBodyStateOrKey(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"status_422", 422, `{"detail":"SECRETBODY422"}`},
		{"status_500_twice", 500, `SECRETBODY500`},
		{"200_not_json", 200, `SECRETBODYJSON`},
		{"200_failed_check", 200, `{"answers":{"q1":{"type":"SECRETBODYJSON","noul":0.5}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
				reply(w, tc.status, tc.body)
			})
			fc := newFakeClock()
			c := newClient(config{key: "SECRETKEY", endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)
			req := Request{Purpose: PurposeValueCheck, State: "SECRETSTATE", Questions: map[string]Question{
				"q1": {Type: TypeNoul, Instructions: "SECRETQUESTION"},
			}}

			_, err := c.Ask(t.Context(), req)
			if err == nil {
				t.Fatal("Ask() err = nil, want a skipped error")
			}
			if ts.hits.Load() == 0 {
				t.Fatal("hits = 0, want the request to reach the server")
			}
			text := err.Error()
			for _, needle := range []string{"SECRETKEY", "SECRETSTATE", "SECRETQUESTION", "SECRETBODY422", "SECRETBODY500", "SECRETBODYJSON", ts.URL, "http"} {
				if strings.Contains(text, needle) {
					t.Errorf("err text %q contains %q", text, needle)
				}
			}
		})
	}
}

// -- AC-8 --

func TestAsk_CancelledContextStopsAtOnce(t *testing.T) {
	var ts *testServer
	ts = newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		ts.hang(w, r)
	})
	c := realClient(ts, budget, retryWait)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)

	before := dials.Load()
	start := time.Now()
	_, err := askWithin(t, c, ctx, noulReq("s"), 2*time.Second)
	wall := time.Since(start)

	if wall >= 500*time.Millisecond {
		t.Errorf("Ask() took %v, want < 500ms", wall)
	}
	wantHits(t, ts, 1)
	if got := dials.Load() - before; got != 1 {
		t.Errorf("round trips = %d, want 1: no attempt may follow a cancelled caller", got)
	}
	if !errors.Is(err, ErrCheckSkipped) {
		t.Errorf("Ask() err = %v, want errors.Is ErrCheckSkipped", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Ask() err = %v, want errors.Is context.Canceled", err)
	}
}

func TestAsk_CancelDuringTheRetryWaitStopsAtOnce(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusServiceUnavailable, "")
	})
	ctx, cancel := context.WithCancel(t.Context())
	var sleeps atomic.Int32
	fc := newFakeClock()
	c := newClient(config{key: "k", endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now,
		sleep: func(sctx context.Context, _ time.Duration) error {
			sleeps.Add(1)
			cancel()
			return sctx.Err()
		}}, nil)

	before := dials.Load()
	_, err := askWithin(t, c, ctx, noulReq("s"), 2*time.Second)
	wantHits(t, ts, 1)
	// A cancelled attempt never reaches the server; only the round-trip count sees it.
	if got := dials.Load() - before; got != 1 {
		t.Errorf("round trips = %d, want 1: no attempt may follow a cancelled wait", got)
	}
	if got := sleeps.Load(); got != 1 {
		t.Errorf("sleep calls = %d, want 1", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Ask() err = %v, want errors.Is context.Canceled", err)
	}
	if !errors.Is(err, ErrCheckSkipped) {
		t.Errorf("Ask() err = %v, want errors.Is ErrCheckSkipped", err)
	}
}

func TestAsk_ACallerDeadlineShorterThanTheBudgetWins(t *testing.T) {
	var ts *testServer
	ts = newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		ts.hang(w, r)
	})
	c := realClient(ts, budget, retryWait)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	before := dials.Load()
	start := time.Now()
	_, err := askWithin(t, c, ctx, noulReq("s"), 2*time.Second)
	wall := time.Since(start)

	if wall >= 500*time.Millisecond {
		t.Errorf("Ask() took %v, want < 500ms", wall)
	}
	if got := dials.Load() - before; got != 1 {
		t.Errorf("round trips = %d, want 1: no attempt may follow an expired caller deadline", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Ask() err = %v, want errors.Is context.DeadlineExceeded", err)
	}
	if !errors.Is(err, ErrCheckSkipped) {
		t.Errorf("Ask() err = %v, want errors.Is ErrCheckSkipped", err)
	}
}

// -- AC-9 --

func TestAsk_AnInvalidRequestSendsNothing(t *testing.T) {
	setQ := func(r *Request, id string, f func(*Question)) {
		q := r.Questions[id]
		f(&q)
		r.Questions[id] = q
	}
	cases := []struct {
		clause string
		mutate func(r *Request)
	}{
		{"purpose_not_one_of_the_three", func(r *Request) { r.Purpose = "bogus" }},
		{"state_empty", func(r *Request) { r.State = "" }},
		{"zero_questions", func(r *Request) { r.Questions = map[string]Question{} }},
		{"question_id_empty", func(r *Request) { r.Questions[""] = r.Questions["n"]; delete(r.Questions, "n") }},
		{"question_type_unknown", func(r *Request) { setQ(r, "n", func(q *Question) { q.Type = "yesno" }) }},
		{"instructions_empty", func(r *Request) { setQ(r, "n", func(q *Question) { q.Instructions = "" }) }},
		{"choice_fewer_than_two_options", func(r *Request) {
			setQ(r, "c", func(q *Question) { q.Options = []Option{{"receipt", ""}}; q.Default = "receipt" })
		}},
		{"option_name_duplicated", func(r *Request) {
			setQ(r, "c", func(q *Question) { q.Options = []Option{{"receipt", ""}, {"receipt", "again"}, {"credit note", ""}} })
		}},
		{"option_name_empty", func(r *Request) {
			setQ(r, "c", func(q *Question) { q.Options = []Option{{"", "blank"}, {"receipt", ""}, {"credit note", ""}} })
		}},
		{"choice_default_not_an_option", func(r *Request) { setQ(r, "c", func(q *Question) { q.Default = "purchase order" }) }},
		{"score_default_missing", func(r *Request) { setQ(r, "s", func(q *Question) { q.Default = "" }) }},
		{"score_level_description_empty", func(r *Request) { setQ(r, "s", func(q *Question) { q.Options[1].Description = "" }) }},
	}
	for _, tc := range cases {
		t.Run(tc.clause, func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 200, threeTypeOK) })
			c := clockClient(ts, newFakeClock())
			req := threeTypeReq()
			tc.mutate(&req)

			_, err := c.Ask(t.Context(), req)
			wantHits(t, ts, 0)
			wantSkipped(t, err, textRefused)
		})
	}

	// The first row's request with a valid purpose: the refusal came from that clause alone.
	t.Run("control_valid_purpose", func(t *testing.T) {
		ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 200, threeTypeOK) })
		c := clockClient(ts, newFakeClock())
		req := threeTypeReq()
		req.Purpose = PurposeValueCheck

		resp, err := c.Ask(t.Context(), req)
		wantHits(t, ts, 1)
		if err != nil {
			t.Fatalf("Ask() err = %v, want nil", err)
		}
		if len(resp.Answers) != 3 {
			t.Errorf("answers = %v, want 3", resp.Answers)
		}
	})
}

// -- AC-15 --

func TestAsk_AKeyWithAControlByteSendsNothing(t *testing.T) {
	for _, key := range []string{"k\n", "k\r\n", "\x00k", "k\x7f"} {
		t.Run(strconv.Quote(key), func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 200, noulOK) })
			fc := newFakeClock()
			c := newClient(config{key: key, endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)

			var err error
			noDials(t, func() { _, err = c.Ask(t.Context(), noulReq("s")) })
			wantHits(t, ts, 0)
			wantSkipped(t, err, textRefused)
			if strings.Contains(errText(err), key) {
				t.Errorf("err text %q contains the key", errText(err))
			}
		})
	}

	for _, key := range []string{"k\t", " k "} {
		t.Run("control_"+strconv.Quote(key), func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, 200, noulOK) })
			fc := newFakeClock()
			c := newClient(config{key: key, endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)

			_, err := c.Ask(t.Context(), noulReq("s"))
			wantHits(t, ts, 1)
			if err != nil {
				t.Errorf("Ask() err = %v, want nil", err)
			}
		})
	}
}

// -- AC-16 --

func TestAsk_ConcurrentCallsKeepTheirOwnAnswers(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		var body struct {
			State string `json:"state"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil || !strings.HasPrefix(body.State, "S") {
			reply(w, http.StatusBadRequest, "")
			return
		}
		time.Sleep(10 * time.Millisecond) // overlap the calls
		reply(w, http.StatusOK, fmt.Sprintf(`{"answers":{"q1":{"type":"noul","noul":0.%s}}}`, body.State[1:]))
	})
	c := realClient(ts, budget, retryWait)

	const n = 8
	var wg sync.WaitGroup
	resps := make([]Response, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			resps[i], errs[i] = c.Ask(t.Context(), noulReq("S"+strconv.Itoa(i)))
		})
	}
	wg.Wait()

	wantHits(t, ts, n)
	for i := range n {
		if errs[i] != nil {
			t.Errorf("call %d: err = %v, want nil", i, errs[i])
			continue
		}
		wantNoul(t, resps[i], "q1", float64(i)/10)
	}
}
