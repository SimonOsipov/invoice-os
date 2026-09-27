package jev

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Production clients come from FromEnv, so a logger it drops silences every line.
func TestLog_FromEnvWiresTheLogger(t *testing.T) {
	cases := []struct {
		name, fake, wantOutcome string
		req                     Request
	}{
		{"off", "", "off", noulReq("s")},
		{"fake", "true", "fake", fakeReq("s")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvKey, "")
			t.Setenv(EnvFake, tc.fake)
			buf := &bytes.Buffer{}
			c, err := FromEnv(jsonLogger(buf))
			if err != nil {
				t.Fatalf("FromEnv() err = %v, want nil", err)
			}

			noDials(t, func() { _, _ = askWithin(t, c, t.Context(), tc.req, 2*time.Second) })
			wantStr(t, oneLine(t, buf), "outcome", tc.wantOutcome)
		})
	}
}

// A nil logger must not fall back to the process default, which a test logger cannot see.
func TestLog_ANilLoggerWritesNothingToTheDefaultLogger(t *testing.T) {
	buf := &bytes.Buffer{}
	prevSlog, prevOut, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(jsonLogger(buf))
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	// Control: the captured default does see a write.
	slog.Info("control")
	if !strings.Contains(buf.String(), `"msg":"control"`) {
		t.Fatalf("default logger capture saw nothing: %q", buf.String())
	}
	buf.Reset()

	ts := newServer(t, replyWith(http.StatusOK, noulOK))
	fakeTS := noSendServer(t)
	invalid := noulReq("s")
	invalid.Purpose = "bogus"
	for name, ask := range map[string]func(){
		"ok":      func() { _, _ = clockLogClient(ts, "k", newFakeClock(), nil).Ask(t.Context(), noulReq("s")) },
		"off":     func() { _, _ = clockLogClient(ts, "", newFakeClock(), nil).Ask(t.Context(), noulReq("s")) },
		"refused": func() { _, _ = clockLogClient(ts, "k", newFakeClock(), nil).Ask(t.Context(), invalid) },
		"fake":    func() { _, _ = askFake(t, newClient(fakeConfig(t, fakeTS), nil), fakeTS, fakeReq("s")) },
	} {
		ask()
		if buf.Len() != 0 {
			t.Errorf("%s: a nil-logger client wrote to the default logger: %q", name, buf.String())
			buf.Reset()
		}
	}
	wantHits(t, ts, 1)
}

// Paths TestLog_OutcomePerPath leaves out: retries, 422, a bad 200, the spent budget, a cancelled
// retry wait, an empty off request and the fake doubt and choice markers. Each logs one INFO "jev call".
func TestLog_OutcomeOnTheRemainingPaths(t *testing.T) {
	wire := func(h func(int32, http.ResponseWriter, *http.Request)) func(*testing.T, *slog.Logger) {
		return func(t *testing.T, l *slog.Logger) {
			ts := newServer(t, h)
			_, _ = askWithin(t, clockLogClient(ts, "k", newFakeClock(), l), t.Context(), noulReq("s"), 2*time.Second)
		}
	}
	fake := func(state string) func(*testing.T, *slog.Logger) {
		return func(t *testing.T, l *slog.Logger) {
			ts := noSendServer(t)
			_, _ = askFake(t, newClient(fakeConfig(t, ts), l), ts, fakeReq(state))
		}
	}

	cases := []struct {
		name         string
		run          func(*testing.T, *slog.Logger)
		wantOutcome  string
		wantAttempts float64
	}{
		{"503_then_200", wire(func(n int32, w http.ResponseWriter, _ *http.Request) {
			if n == 1 {
				reply(w, http.StatusServiceUnavailable, "")
				return
			}
			reply(w, http.StatusOK, noulOK)
		}), "ok", 2},
		{"transport_error_twice", func(t *testing.T, l *slog.Logger) {
			wire(func(_ int32, w http.ResponseWriter, _ *http.Request) { hijackClose(t, w) })(t, l)
		}, "skipped_unavailable", 2},
		{"408_twice", wire(replyWith(http.StatusRequestTimeout, "")), "skipped_unavailable", 2},
		{"429_twice", wire(replyWith(http.StatusTooManyRequests, "")), "skipped_unavailable", 2},
		{"529_twice", wire(replyWith(529, "")), "skipped_unavailable", 2},
		{"422", wire(replyWith(http.StatusUnprocessableEntity, `{"detail":"no"}`)), "skipped_refused", 1},
		{"200_failing_the_answer_check", wire(replyWith(http.StatusOK, `{"answers":{"q1":{"type":"noul","noul":1.5}}}`)), "skipped_unavailable", 1},
		{"budget_spent", func(t *testing.T, l *slog.Logger) {
			fc := newFakeClock()
			ts := newServer(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
				fc.advance(2900 * time.Millisecond)
				reply(w, http.StatusServiceUnavailable, "")
			})
			_, _ = askWithin(t, clockLogClient(ts, "k", fc, l), t.Context(), noulReq("s"), 2*time.Second)
		}, "skipped_unavailable", 1},
		{"cancelled_in_the_retry_wait", func(t *testing.T, l *slog.Logger) {
			ts := newServer(t, replyWith(http.StatusServiceUnavailable, ""))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := newClient(config{key: "k", endpoint: ts.endpoint(), budget: budget, retryWait: retryWait, now: newFakeClock().now,
				sleep: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }}, l)
			_, _ = askWithin(t, c, ctx, noulReq("s"), 2*time.Second)
			wantHits(t, ts, 1)
		}, "skipped_unavailable", 1},
		{"off_with_an_empty_request", func(t *testing.T, l *slog.Logger) {
			_, _ = newClient(config{now: newFakeClock().now}, l).Ask(t.Context(), Request{})
		}, "off", 0},
		{"fake_doubt_marker", fake(markerDoubt), "fake", 1},
		{"fake_choice_marker", fake(choiceMarker("credit note") + " end"), "fake", 1},
		{"fake_choice_naming_an_absent_option", fake(choiceMarker("purchase order") + " end"), "fake", 1},
		{"fake_choice_undecodable", fake(markerChoicePrefix + "A end"), "fake", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			tc.run(t, jsonLogger(buf))

			line := oneLine(t, buf)
			wantStr(t, line, "outcome", tc.wantOutcome)
			wantNum(t, line, "attempts", tc.wantAttempts)
			wantStr(t, line, "level", "INFO")
			wantStr(t, line, "msg", "jev call")
		})
	}
}

// A failed call still bills its attempts and spends its wait.
func TestLog_AFailedCallLogsItsTokensAndLatency(t *testing.T) {
	fc := newFakeClock()
	ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
		fc.advance(100 * time.Millisecond)
		if n == 1 {
			reply(w, http.StatusServiceUnavailable, `{"usage":{"input_tokens":7,"output_tokens":1}}`)
			return
		}
		reply(w, http.StatusServiceUnavailable, `{"usage":{"input_tokens":11,"output_tokens":1}}`)
	})
	buf := &bytes.Buffer{}

	if _, err := clockLogClient(ts, "k", fc, jsonLogger(buf)).Ask(t.Context(), noulReq("s")); err == nil {
		t.Fatal("Ask() err = nil, want unavailable")
	}
	wantHits(t, ts, 2)
	line := oneLine(t, buf)
	wantStr(t, line, "outcome", "skipped_unavailable")
	wantNum(t, line, "input_tokens", 18)
	wantNum(t, line, "latency_ms", 450)
}

func TestLog_PurposeIsTheCallersOwn(t *testing.T) {
	for _, p := range []Purpose{PurposeValueCheck, PurposeDocumentType, PurposeMappingCheck} {
		t.Run(string(p), func(t *testing.T) {
			ts := newServer(t, replyWith(http.StatusOK, noulOK))
			buf := &bytes.Buffer{}
			req := noulReq("s")
			req.Purpose = p

			if _, err := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf)).Ask(t.Context(), req); err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			wantStr(t, oneLine(t, buf), "purpose", string(p))
		})
	}
}

// -- model and cost edges --

func TestLog_TheModelCutBoundary(t *testing.T) {
	for _, n := range []int{127, 128, 129} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			model := "typesafe/" + strings.Repeat("x", n-len("typesafe/"))
			if len(model) != n {
				t.Fatalf("fixture model is %d bytes, want %d", len(model), n)
			}
			ts := newServer(t, replyWith(http.StatusOK, `{"model":"`+model+`","answers":{"q1":{"type":"noul","noul":0.95}}}`))
			buf := &bytes.Buffer{}
			if _, err := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf)).Ask(t.Context(), noulReq("s")); err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			wantStr(t, oneLine(t, buf), "model", model[:min(n, 128)])
		})
	}
}

// The cut is by byte, so it can split a rune; slog's JSON handler then writes U+FFFD for the stray byte.
func TestLog_ACutThatSplitsARuneLogsAReplacementChar(t *testing.T) {
	prefix := "typesafe/" + strings.Repeat("x", 118) // 127 bytes
	cases := []struct {
		name, model, want string
	}{
		{"rune_ends_at_128", prefix[:126] + "é", prefix[:126] + "é"},
		{"rune_split_at_128", prefix + "é", prefix + "�"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newServer(t, replyWith(http.StatusOK, `{"model":"`+tc.model+`","answers":{"q1":{"type":"noul","noul":0.95}}}`))
			buf := &bytes.Buffer{}
			if _, err := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf)).Ask(t.Context(), noulReq("s")); err != nil {
				t.Fatalf("Ask() err = %v, want nil", err)
			}
			wantStr(t, oneLine(t, buf), "model", tc.want)
		})
	}
}

// The model is vendor text: a quote or newline in it must stay inside the one value.
func TestLog_AModelCannotForgeALineOrAKey(t *testing.T) {
	const forged = `typesafe/x\n\",\"outcome\":\"ok`
	ts := newServer(t, replyWith(http.StatusPaymentRequired, `{"model":"`+forged+`"}`))
	buf := &bytes.Buffer{}

	if _, err := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf)).Ask(t.Context(), noulReq("s")); err == nil {
		t.Fatal("Ask() err = nil, want refused")
	}
	raw := rawLine(t, buf)
	if got, want := attrKeys(t, raw), contractKeys[1:]; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	line := oneLine(t, buf)
	wantStr(t, line, "outcome", "skipped_refused")
	wantStr(t, line, "model", "typesafe/x\n\",\"outcome\":\"ok")
}

// The final attempt is the one Ask returns, a refusal included.
func TestLog_ARefusedReplyLogsItsModelAndCost(t *testing.T) {
	ts := newServer(t, replyWith(http.StatusPaymentRequired, `{"model":"typesafe/jev-1.13-20260917","usage":{"input_tokens":5,"output_tokens":2,"cost":0.0007}}`))
	buf := &bytes.Buffer{}

	if _, err := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf)).Ask(t.Context(), noulReq("s")); err == nil {
		t.Fatal("Ask() err = nil, want refused")
	}
	wantHits(t, ts, 1)
	line := oneLine(t, buf)
	wantStr(t, line, "outcome", "skipped_refused")
	wantNum(t, line, "attempts", 1)
	wantStr(t, line, "model", "typesafe/jev-1.13-20260917")
	wantNum(t, line, "output_tokens", 2)
	wantNum(t, line, "cost", 0.0007)
}

// An unreadable final attempt carries no model, so the earlier attempt's model is dropped; its cost is not.
func TestLog_AnUnreadableFinalAttemptDropsTheEarlierModel(t *testing.T) {
	const first = `{"model":"typesafe/jev-X","usage":{"input_tokens":1,"output_tokens":1,"cost":0.001}}`
	for name, second := range map[string]func(*testing.T, http.ResponseWriter){
		"transport_failure": func(t *testing.T, w http.ResponseWriter) { hijackClose(t, w) },
		"malformed_503":     func(_ *testing.T, w http.ResponseWriter) { reply(w, http.StatusServiceUnavailable, "not json") },
		"malformed_200":     func(_ *testing.T, w http.ResponseWriter) { reply(w, http.StatusOK, "not json") },
	} {
		t.Run(name, func(t *testing.T) {
			ts := newServer(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
				if n == 1 {
					reply(w, http.StatusServiceUnavailable, first)
					return
				}
				second(t, w)
			})
			buf := &bytes.Buffer{}
			if _, err := clockLogClient(ts, "k", newFakeClock(), jsonLogger(buf)).Ask(t.Context(), noulReq("s")); err == nil {
				t.Fatal("Ask() err = nil, want unavailable")
			}
			wantHits(t, ts, 2)
			line := oneLine(t, buf)
			wantStr(t, line, "outcome", "skipped_unavailable")
			wantNum(t, line, "attempts", 2)
			wantNum(t, line, "cost", 0.001)
			wantNum(t, line, "output_tokens", 1)
			wantStr(t, line, "model", "")
		})
	}
}
