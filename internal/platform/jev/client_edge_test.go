package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// roundTripFunc stands in for one attempt's transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// net/http's own header check is the oracle for every byte value.
func TestAsk_TheKeyRuleIsNetHTTPsHeaderRule(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, noulOK) })

	var refused, sent int
	for b := range 256 {
		key := "k" + string([]byte{byte(b)})

		probe, err := http.NewRequest(http.MethodGet, ts.URL, nil)
		if err != nil {
			t.Fatalf("byte %#02x: new probe: %v", b, err)
		}
		probe.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultTransport.RoundTrip(probe)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err != nil && !strings.Contains(err.Error(), "invalid header field value") {
			t.Fatalf("byte %#02x: probe err = %v, want nil or net/http's header refusal", b, err)
		}
		netRefuses := err != nil

		before := ts.hits.Load()
		fc := newFakeClock()
		c := newClient(config{key: key, endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)
		_, askErr := c.Ask(t.Context(), noulReq("s"))
		hits := ts.hits.Load() - before

		if netRefuses {
			refused++
			if hits != 0 || errText(askErr) != textRefused {
				t.Errorf("byte %#02x: net/http refuses it; Ask hits = %d, err = %q, want 0 and %q", b, hits, errText(askErr), textRefused)
			}
			continue
		}
		sent++
		if hits != 1 || askErr != nil {
			t.Errorf("byte %#02x: net/http accepts it; Ask hits = %d, err = %v, want 1 and nil", b, hits, askErr)
		}
	}
	if refused == 0 || sent == 0 {
		t.Fatalf("net/http refused %d and accepted %d byte values, want both non-zero", refused, sent)
	}
}

func TestAsk_EachAttemptIsCappedAtHalfTheBudgetAfterTheWait(t *testing.T) {
	const slack = 100 * time.Millisecond
	cases := []struct {
		name    string
		advance time.Duration // fake time spent inside attempt 1
		want    []time.Duration
	}{
		{"both_attempts_get_the_cap", 0, []time.Duration{1375 * time.Millisecond, 1375 * time.Millisecond}},
		{"the_second_gets_only_the_budget_left", 2 * time.Second, []time.Duration{1375 * time.Millisecond, 750 * time.Millisecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClock()
			ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
				if n == 1 {
					fc.advance(tc.advance)
					reply(w, http.StatusServiceUnavailable, "")
					return
				}
				reply(w, http.StatusOK, noulOK)
			})
			c := clockClient(ts, fc)
			var mu sync.Mutex
			var left []time.Duration
			c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				dl, ok := r.Context().Deadline()
				mu.Lock()
				if ok {
					left = append(left, time.Until(dl))
				} else {
					left = append(left, -1)
				}
				mu.Unlock()
				return http.DefaultTransport.RoundTrip(r)
			})}

			_, err := c.Ask(t.Context(), noulReq("s"))
			if err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			wantHits(t, ts, 2)
			mu.Lock()
			defer mu.Unlock()
			if len(left) != len(tc.want) {
				t.Fatalf("recorded %d attempt deadline(s), want %d", len(left), len(tc.want))
			}
			for i, want := range tc.want {
				if got := left[i]; got > want || got < want-slack {
					t.Errorf("attempt %d had %v left on its deadline, want %v (within %v)", i+1, got, want, slack)
				}
			}
		})
	}
}

func TestAsk_ACallerContextCancelledBeforeAskSendsNothing(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, noulOK) })
	c := clockClient(ts, newFakeClock())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	_, err := askWithin(t, c, ctx, noulReq("s"), 2*time.Second)
	if wall := time.Since(start); wall >= 500*time.Millisecond {
		t.Errorf("Ask() took %v, want < 500ms", wall)
	}
	wantHits(t, ts, 0)
	wantSkipped(t, err, textUnavailable+": context canceled")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Ask() err = %v, want errors.Is context.Canceled", err)
	}
}

// Attempt 2 has no retry after it, so only the caller-context check can wrap ctx.Err().
func TestAsk_CancelDuringTheSecondAttemptStopsAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var ts *testServer
	ts = newServer(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			reply(w, http.StatusServiceUnavailable, "")
			return
		}
		cancel()
		ts.hang(w, r)
	})
	c := clockClient(ts, newFakeClock())

	start := time.Now()
	_, err := askWithin(t, c, ctx, noulReq("s"), 2*time.Second)
	if wall := time.Since(start); wall >= 500*time.Millisecond {
		t.Errorf("Ask() took %v, want < 500ms", wall)
	}
	wantHits(t, ts, 2)
	wantSkipped(t, err, textUnavailable+": context canceled")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Ask() err = %v, want errors.Is context.Canceled", err)
	}
}

func TestAsk_TheCallersCancelWinsOverALateSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	fc := newFakeClock()
	c := newClient(config{key: "k", endpoint: "http://127.0.0.1:1/v1/systemone", budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)
	var calls int
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(noulOK)), Request: r}, nil
	})}

	resp, err := c.Ask(ctx, noulReq("s"))
	if calls != 1 {
		t.Errorf("attempts = %d, want 1", calls)
	}
	wantSkipped(t, err, textUnavailable+": context canceled")
	if len(resp.Answers) != 0 {
		t.Errorf("answers = %v, want none once the caller cancelled", resp.Answers)
	}
}

func TestAsk_ANonJSONFailedAttemptAddsNoUsage(t *testing.T) {
	ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			reply(w, http.StatusServiceUnavailable, "<html>503 Service Unavailable</html>")
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
	if resp.Usage != (Usage{InputTokens: 318, OutputTokens: 34}) {
		t.Errorf("usage = %+v, want {318 34}: the non-JSON attempt reports none", resp.Usage)
	}
}

func TestAsk_Any2xxIsASuccess(t *testing.T) {
	for _, status := range []int{201, 203, 299} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, status, noulOK) })
			c := clockClient(ts, newFakeClock())

			resp, err := c.Ask(t.Context(), noulReq("s"))
			wantHits(t, ts, 1)
			if err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			wantNoul(t, resp, "q1", 0.95)
		})
	}
}

func TestAsk_TheRetryableRangeEndsAt599(t *testing.T) {
	for _, status := range []int{504, 599} {
		t.Run("retried_"+strconv.Itoa(status), func(t *testing.T) {
			ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
				if n == 1 {
					reply(w, status, "")
					return
				}
				reply(w, http.StatusOK, noulOK)
			})
			c := clockClient(ts, newFakeClock())

			_, err := c.Ask(t.Context(), noulReq("s"))
			wantHits(t, ts, 2)
			if err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
		})
	}
	for _, status := range []int{404, 499, 600} {
		t.Run("refused_"+strconv.Itoa(status), func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, status, "") })
			c := clockClient(ts, newFakeClock())

			_, err := c.Ask(t.Context(), noulReq("s"))
			wantHits(t, ts, 1)
			wantSkipped(t, err, textRefused)
		})
	}
}

// askOne asks q alone against a server answering answer; it returns the hits.
func askOne(t *testing.T, q Question, answer string) (Response, int32, error) {
	t.Helper()
	ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, `{"answers":{"q":`+answer+`}}`)
	})
	c := clockClient(ts, newFakeClock())
	resp, err := c.Ask(t.Context(), Request{Purpose: PurposeValueCheck, State: "s", Questions: map[string]Question{"q": q}})
	return resp, ts.hits.Load(), err
}

func TestAsk_TheScoreRangeFollowsTheLevelCount(t *testing.T) {
	levels := func(n int) Question {
		q := Question{Type: TypeScore, Instructions: "How legible?", Default: "l0"}
		for i := range n {
			q.Options = append(q.Options, Option{"l" + strconv.Itoa(i), "level " + strconv.Itoa(i)})
		}
		return q
	}
	cases := []struct {
		name   string
		levels int
		score  string
		conf   string
		ok     bool
	}{
		{"two_levels_zero", 2, "0", "0.9", true},
		{"two_levels_top", 2, "1", "0.9", true},
		{"two_levels_above_the_top", 2, "1.01", "0.9", false},
		{"four_levels_top", 4, "3", "0.9", true},
		{"four_levels_above_the_top", 4, "3.01", "0.9", false},
		{"confidence_below_zero", 3, "1", "-0.01", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, hits, err := askOne(t, levels(tc.levels), `{"type":"score","score":`+tc.score+`,"confidence":`+tc.conf+`}`)
			if hits != 1 {
				t.Fatalf("hits = %d, want 1", hits)
			}
			if !tc.ok {
				wantSkipped(t, err, textUnavailable)
				return
			}
			if err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			want, _ := strconv.ParseFloat(tc.score, 64)
			if got := resp.Answers["q"]; got.Score != want {
				t.Errorf("score = %v, want %v", got.Score, want)
			}
		})
	}
}

func TestAsk_AnAnswerEntryThatIsNotAnAnswerFails(t *testing.T) {
	noulQ := Question{Type: TypeNoul, Instructions: "Is the total right?"}
	for name, answer := range map[string]string{
		"null":        `null`,
		"a_number":    `0.5`,
		"a_string":    `"noul"`,
		"an_array":    `[]`,
		"type_cased":  `{"type":"NOUL","noul":0.5}`,
		"type_absent": `{"noul":0.5}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, hits, err := askOne(t, noulQ, answer)
			if hits != 1 {
				t.Fatalf("hits = %d, want 1", hits)
			}
			wantSkipped(t, err, textUnavailable)
		})
	}
	t.Run("control", func(t *testing.T) {
		resp, _, err := askOne(t, noulQ, `{"type":"noul","noul":0.5}`)
		if err != nil {
			t.Fatalf("Ask() err = %v, want nil", err)
		}
		wantNoul(t, resp, "q", 0.5)
	})
}

func TestAsk_AnAnswerNobodyAskedIsDropped(t *testing.T) {
	ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, `{"answers":{"q1":{"type":"noul","noul":0.95},"zzz":{"type":"bogus","noul":"x"}}}`)
	})
	c := clockClient(ts, newFakeClock())

	resp, err := c.Ask(t.Context(), noulReq("s"))
	if err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	if ids := slices.Sorted(maps.Keys(resp.Answers)); !slices.Equal(ids, []string{"q1"}) {
		t.Errorf("answer ids = %v, want exactly [q1]", ids)
	}
}

func TestAsk_ANoulWithOneCriterionSendsOnlyThatKey(t *testing.T) {
	cases := []struct {
		name    string
		yes, no string
		want    map[string]any
	}{
		{"true_only", "the TIN matches", "", map[string]any{"true": "the TIN matches"}},
		{"false_only", "", "the TIN differs", map[string]any{"false": "the TIN differs"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec capture
			ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				reply(w, http.StatusOK, noulOK)
			})
			c := clockClient(ts, newFakeClock())
			req := noulReq("s")
			req.Questions["q1"] = Question{Type: TypeNoul, Instructions: "Is the TIN right?", True: tc.yes, False: tc.no}

			_, _ = c.Ask(t.Context(), req)
			var body struct {
				Questions map[string]map[string]any `json:"questions"`
			}
			if err := json.Unmarshal(rec.only(t).raw, &body); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			q, ok := body.Questions["q1"]
			if !ok {
				t.Fatal("questions[q1] missing on the wire")
			}
			got, ok := q["criteria"].(map[string]any)
			if !ok {
				t.Fatalf("criteria = %#v, want an object", q["criteria"])
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("criteria = %v, want exactly %v", got, tc.want)
			}
		})
	}
}

// Needles are compared lowercased, so a re-cased leak still counts.
func TestAsk_NoFailurePathNamesTheEndpointOrTheRequest(t *testing.T) {
	secretReq := func() Request {
		return Request{Purpose: PurposeValueCheck, State: "SECRETSTATE", Questions: map[string]Question{
			"q1": {Type: TypeNoul, Instructions: "SECRETQUESTION"},
		}}
	}
	cases := []struct {
		name    string
		key     string
		real    bool // real clock with a 400ms budget
		mutate  func(*Request)
		handler func(ts *testServer, n int32, w http.ResponseWriter, r *http.Request)
		ctx     func(context.Context) (context.Context, context.CancelFunc)
	}{
		{name: "transport_error_twice", key: "SECRETKEY",
			handler: func(_ *testServer, _ int32, w http.ResponseWriter, _ *http.Request) { hijackClose(t, w) }},
		{name: "attempt_timeout_twice", key: "SECRETKEY", real: true,
			handler: func(ts *testServer, _ int32, w http.ResponseWriter, r *http.Request) { ts.hang(w, r) }},
		{name: "caller_cancelled_mid_attempt", key: "SECRETKEY", real: true,
			handler: func(ts *testServer, _ int32, w http.ResponseWriter, r *http.Request) { ts.hang(w, r) },
			ctx: func(p context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(p)
				time.AfterFunc(50*time.Millisecond, cancel)
				return ctx, cancel
			}},
		{name: "caller_deadline", key: "SECRETKEY", real: true,
			handler: func(ts *testServer, _ int32, w http.ResponseWriter, r *http.Request) { ts.hang(w, r) },
			ctx: func(p context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(p, 100*time.Millisecond)
			}},
		{name: "control_byte_key", key: "SECRETKEY\n"},
		{name: "invalid_request", key: "SECRETKEY", mutate: func(r *Request) { r.Purpose = "SECRETPURPOSE" }},
		{name: "off", key: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ts *testServer
			ts = newServer(t, func(n int32, w http.ResponseWriter, r *http.Request) {
				if tc.handler == nil {
					reply(w, http.StatusOK, noulOK)
					return
				}
				tc.handler(ts, n, w, r)
			})
			fc := newFakeClock()
			cfg := config{key: tc.key, endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}
			if tc.real {
				cfg.budget, cfg.retryWait, cfg.now, cfg.sleep = 400*time.Millisecond, 50*time.Millisecond, time.Now, testSleep
			}
			c := newClient(cfg, nil)
			req := secretReq()
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			ctx := t.Context()
			if tc.ctx != nil {
				var cancel context.CancelFunc
				ctx, cancel = tc.ctx(ctx)
				defer cancel()
			}

			_, err := askWithin(t, c, ctx, req, 2*time.Second)
			text := errText(err)
			if !slices.Contains(c21Texts, text) {
				t.Fatalf("err text = %q, want one of %q", text, c21Texts)
			}
			lower := strings.ToLower(text)
			for _, needle := range []string{"secretkey", "secretstate", "secretquestion", "secretpurpose",
				strings.ToLower(ts.URL), "http", "/v1/", "typesafe", "127.0.0.1", "post", "dial", "eof"} {
				if strings.Contains(lower, needle) {
					t.Errorf("err text %q contains %q", text, needle)
				}
			}
		})
	}
}

func TestAsk_TheKeyTravelsOnlyInTheAuthorizationHeader(t *testing.T) {
	var rec capture
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		reply(w, http.StatusOK, noulOK)
	})
	fc := newFakeClock()
	c := newClient(config{key: "SECRETKEY", endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)

	if _, err := c.Ask(t.Context(), noulReq("s")); err != nil {
		t.Fatalf("Ask() err = %v, want nil", err)
	}
	got := rec.only(t)
	if strings.Contains(string(got.raw), "SECRETKEY") {
		t.Errorf("body carries the key: %s", got.raw)
	}
	var carriers []string
	for name, vals := range got.header {
		for _, v := range vals {
			if strings.Contains(v, "SECRETKEY") {
				carriers = append(carriers, name)
			}
		}
	}
	if !slices.Equal(carriers, []string{"Authorization"}) {
		t.Errorf("headers carrying the key = %v, want exactly [Authorization]", carriers)
	}
}

func TestAsk_AnInvalidScoreSendsNothing(t *testing.T) {
	cases := []struct {
		clause  string
		options []Option
		def     string
	}{
		{"one_level", []Option{{"l0", "low"}}, "l0"},
		{"level_name_duplicated", []Option{{"l0", "low"}, {"l0", "high"}}, "l0"},
		{"level_name_empty", []Option{{"", "low"}, {"l1", "high"}}, "l1"},
		{"default_not_a_level", []Option{{"l0", "low"}, {"l1", "high"}}, "l2"},
	}
	for _, tc := range cases {
		t.Run(tc.clause, func(t *testing.T) {
			ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusOK, threeTypeOK) })
			c := clockClient(ts, newFakeClock())
			req := threeTypeReq()
			req.Questions["s"] = Question{Type: TypeScore, Instructions: "How legible?", Options: tc.options, Default: tc.def}

			_, err := c.Ask(t.Context(), req)
			wantHits(t, ts, 0)
			wantSkipped(t, err, textRefused)
		})
	}
	t.Run("control_two_levels", func(t *testing.T) {
		ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
			reply(w, http.StatusOK, `{"answers":{"n":{"type":"noul","noul":0.9},"c":{"type":"choice","choice":"receipt","confidence":0.9},"s":{"type":"score","score":1,"confidence":0.9}}}`)
		})
		c := clockClient(ts, newFakeClock())
		req := threeTypeReq()
		req.Questions["s"] = Question{Type: TypeScore, Instructions: "How legible?", Options: []Option{{"l0", "low"}, {"l1", "high"}}, Default: "l1"}

		_, err := c.Ask(t.Context(), req)
		wantHits(t, ts, 1)
		if err != nil {
			t.Fatalf("Ask() err = %v, want nil", err)
		}
	})
}

// .invalid never resolves, so a client that bypassed the guard still reaches no real host.
func TestAsk_TheClientSendsThroughTheGuardedTransport(t *testing.T) {
	fc := newFakeClock()
	c := newClient(config{key: "k", endpoint: "https://guard-probe.invalid/v1/systemone", budget: budget, retryWait: retryWait, now: fc.now, sleep: fc.sleep}, nil)

	before := dials.Load()
	_, err := askWithin(t, c, t.Context(), noulReq("s"), 2*time.Second)
	if got := dials.Load() - before; got != 2 {
		t.Errorf("guarded round trips = %d, want 2: the client must send through http.DefaultTransport", got)
	}
	wantSkipped(t, err, textUnavailable)
}

func TestAsk_ConcurrentRetriesKeepTheirOwnUsage(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	ts := newServer(t, func(_ int32, w http.ResponseWriter, r *http.Request) {
		var body struct {
			State string `json:"state"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		i, err := strconv.Atoi(strings.TrimPrefix(body.State, "S"))
		if err != nil {
			reply(w, http.StatusBadRequest, "")
			return
		}
		mu.Lock()
		seen[body.State]++
		first := seen[body.State] == 1
		mu.Unlock()
		if first {
			reply(w, http.StatusServiceUnavailable, `{"usage":{"input_tokens":`+strconv.Itoa(i)+`,"output_tokens":0}}`)
			return
		}
		reply(w, http.StatusOK, `{"answers":{"q1":{"type":"noul","noul":0.5}},"usage":{"input_tokens":`+strconv.Itoa(10*i)+`,"output_tokens":0}}`)
	})
	c := realClient(ts, budget, 20*time.Millisecond)

	const n = 8
	var wg sync.WaitGroup
	resps := make([]Response, n)
	errs := make([]error, n)
	for i := 1; i <= n; i++ {
		wg.Go(func() {
			resps[i-1], errs[i-1] = c.Ask(t.Context(), noulReq("S"+strconv.Itoa(i)))
		})
	}
	wg.Wait()

	wantHits(t, ts, 2*n)
	for i := 1; i <= n; i++ {
		if errs[i-1] != nil {
			t.Errorf("call %d: err = %v, want nil", i, errs[i-1])
			continue
		}
		if got := resps[i-1].Usage.InputTokens; got != 11*i {
			t.Errorf("call %d: input tokens = %d, want %d (its own two attempts)", i, got, 11*i)
		}
	}
}

// Retyped on purpose: the vendor SDK and the measurement harness read this exact name.
func TestFromEnv_ReadsTheTypesafeAPIKeyVariable(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "k")
	unsetEnv(t, EnvFake)

	c, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv() err = %v, want nil", err)
	}
	if c.cfg.key != "k" {
		t.Errorf("cfg.key = %q, want %q read from TYPESAFE_API_KEY", c.cfg.key, "k")
	}
}

func TestFromEnv_TheRealWaitLastsItsDuration(t *testing.T) {
	t.Setenv(EnvKey, "k")
	unsetEnv(t, EnvFake)

	c, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv() err = %v, want nil", err)
	}
	start := time.Now()
	if err := c.cfg.sleep(t.Context(), 50*time.Millisecond); err != nil {
		t.Fatalf("cfg.sleep() = %v, want nil", err)
	}
	if wall := time.Since(start); wall < 50*time.Millisecond {
		t.Errorf("cfg.sleep(50ms) returned after %v, want at least 50ms", wall)
	}
}

// Only a missing usage is harmless; a malformed one fails the decode and the call.
func TestAsk_AMalformedUsageFailsTheCall(t *testing.T) {
	const answers = `{"answers":{"q1":{"type":"noul","noul":0.95}},"usage":{"input_tokens":`
	t.Run("integer_usage_control", func(t *testing.T) {
		ts := newServer(t, replyWith(http.StatusOK, answers+`1,"output_tokens":0}}`))
		if _, err := clockClient(ts, newFakeClock()).Ask(t.Context(), noulReq("s")); err != nil {
			t.Fatalf("Ask() err = %v, want nil: the fixture's answers must pass on their own", err)
		}
	})
	t.Run("fractional_input_tokens", func(t *testing.T) {
		ts := newServer(t, replyWith(http.StatusOK, answers+`1.5,"output_tokens":0}}`))
		resp, err := clockClient(ts, newFakeClock()).Ask(t.Context(), noulReq("s"))
		wantHits(t, ts, 1)
		wantSkipped(t, err, textUnavailable)
		wantNoAnswers(t, resp)
	})
}
