package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// -- T01 --

func TestFromEnv_NoKeyIsOff(t *testing.T) {
	cases := map[string]struct{ key, fake string }{
		"unset_key_unset_fake": {"", ""},
		"unset_key_false_fake": {"", "false"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(EnvKey, tc.key)
			t.Setenv(EnvFake, tc.fake)

			c, err := FromEnv(nil)
			if err != nil {
				t.Fatalf("FromEnv() err = %v, want nil", err)
			}
			if c == nil {
				t.Fatal("FromEnv() client = nil, want non-nil")
			}
			if c.Enabled() {
				t.Error("Enabled() = true, want false")
			}

			noDials(t, func() {
				// Empty Request: an off client must answer before it looks
				// at the request at all.
				answer, err := c.Call(t.Context(), Request{})
				if !errors.Is(err, ErrOff) {
					t.Errorf("err = %v, want ErrOff", err)
				}
				if errors.Is(err, ErrUnavailable) {
					t.Error("err wraps ErrUnavailable, want ErrOff only")
				}
				if answer != nil {
					t.Errorf("answer = %#v, want nil", answer)
				}
			})
		})
	}
}

// -- T02 --

func TestFromEnv_KeySetIsEnabled(t *testing.T) {
	cases := map[string]string{
		"plain_key":       "k",
		"realistic_shape": "sk-or-v1-x",
		"whitespace_key":  " ", // set, even though a real call would 401
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(EnvKey, key)
			t.Setenv(EnvFake, "")

			c, err := FromEnv(nil)
			if err != nil {
				t.Fatalf("FromEnv() err = %v, want nil", err)
			}
			if c == nil {
				t.Fatal("FromEnv() client = nil, want non-nil")
			}
			if !c.Enabled() {
				t.Error("Enabled() = false, want true")
			}
		})
	}
}

// -- T03 --

func TestFromEnv_FakeWinsOverAKey(t *testing.T) {
	cases := map[string]string{
		"no_key":   "",
		"with_key": "k",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(EnvKey, key)
			t.Setenv(EnvFake, "true")

			c, err := FromEnv(nil)
			if err != nil {
				t.Fatalf("FromEnv() err = %v, want nil", err)
			}
			if c == nil {
				t.Fatal("FromEnv() client = nil, want non-nil")
			}
			if !c.Enabled() {
				t.Error("Enabled() = false, want true")
			}

			noDials(t, func() {
				answer, err := c.Call(t.Context(), baseReq())
				if err != nil {
					t.Fatalf("Call() err = %v, want nil", err)
				}
				want := map[string]any{"total": nil, "vat": nil, "currency": nil, "n": nil}
				if !reflect.DeepEqual(answer, want) {
					t.Errorf("Call() = %#v, want %#v", answer, want)
				}
			})
		})
	}
}

// -- T04 --

func TestFromEnv_FakeParsesLikeParseBool(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantFake bool
	}{
		{"empty", "", false},
		{"false", "false", false},
		{"zero", "0", false},
		{"FALSE_upper", "FALSE", false},
		{"true", "true", true},
		{"one", "1", true},
		{"TRUE_upper", "TRUE", true},
		{"True_title", "True", true},
	}

	// Direct table: Enabled() alone cannot tell "fake" from "key set", so
	// parseFake is asserted on its own too.
	for _, tc := range cases {
		t.Run("parseFake_"+tc.name, func(t *testing.T) {
			if got, err := parseFake(tc.raw); err != nil || got != tc.wantFake {
				t.Errorf("parseFake(%q) = (%v, %v), want (%v, nil)", tc.raw, got, err, tc.wantFake)
			}
		})
	}

	for _, tc := range cases {
		t.Run("Enabled_"+tc.name, func(t *testing.T) {
			t.Setenv(EnvKey, "")
			t.Setenv(EnvFake, tc.raw)

			c, err := FromEnv(nil)
			if err != nil {
				t.Fatalf("FromEnv() err = %v, want nil", err)
			}
			if c == nil {
				t.Fatal("FromEnv() client = nil, want non-nil")
			}
			if got := c.Enabled(); got != tc.wantFake {
				t.Errorf("Enabled() = %v, want %v", got, tc.wantFake)
			}
		})
	}
}

// -- T05 --

func TestFromEnv_UnparseableFakeIsAnErrorNotAnExit(t *testing.T) {
	cases := []string{"yes", "no", "ture", "2x", " true"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(EnvKey, "")
			t.Setenv(EnvFake, raw)

			c, err := FromEnv(nil)
			if c != nil {
				t.Errorf("FromEnv() client = %v, want nil", c)
			}
			if err == nil {
				t.Fatal("FromEnv() err = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), EnvFake) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), EnvFake)
			}
		})
	}

	// Only reached if no case above exited the process.
	t.Setenv(EnvKey, "")
	t.Setenv(EnvFake, "")
	if _, err := FromEnv(nil); err != nil {
		t.Errorf("FromEnv() err = %v, want nil (process still running)", err)
	}
}

// -- QA adversarial coverage --

// TestFromEnv_DoesNotExitTheProcess re-execs this binary in FromEnv-only mode
// (see envSubprocess). An in-process test cannot tell "returned an error" from
// "called os.Exit", because the exit would end the run instead of failing it.
func TestFromEnv_DoesNotExitTheProcess(t *testing.T) {
	cases := map[string]struct {
		fake    string
		wantErr bool
	}{
		"unparseable_fake": {"ture", true},
		"fake_true":        {"true", false},
		"fake_unset":       {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0])
			// Appended last, so exec's dedup keeps these over the parent's.
			cmd.Env = append(os.Environ(), envSubprocess+"=1", EnvKey+"=", EnvFake+"="+tc.fake)

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("subprocess err = %v, want nil: FromEnv must return, not exit. output: %s", err, out)
			}
			if !strings.Contains(string(out), subprocessDone) {
				t.Fatalf("output = %q, want it to contain %q: FromEnv never returned", out, subprocessDone)
			}
			gotErr := !strings.Contains(string(out), "err=<nil>")
			if gotErr != tc.wantErr {
				t.Errorf("subprocess reported err = %v, want an error: %v. output: %s", gotErr, tc.wantErr, out)
			}
			wantClient := "client=" + map[bool]string{true: "false", false: "true"}[tc.wantErr]
			if !strings.Contains(string(out), wantClient) {
				t.Errorf("output = %q, want it to contain %q", out, wantClient)
			}
		})
	}
}

// TestFromEnv_FakeWithSurroundingWhitespaceIsAnError pins that AI_FAKE is not
// trimmed: a stray space is a typo, and the typo must not read as "not fake".
func TestFromEnv_FakeWithSurroundingWhitespaceIsAnError(t *testing.T) {
	// " true" is T05's row; these are the other shapes of the same typo.
	for _, raw := range []string{"true ", "\ttrue", "true\n", " 1", "false ", " "} {
		t.Run(envCaseName(raw), func(t *testing.T) {
			if _, err := parseFake(raw); err == nil {
				t.Errorf("parseFake(%q) err = nil, want non-nil", raw)
			}

			t.Setenv(EnvKey, "")
			t.Setenv(EnvFake, raw)
			c, err := FromEnv(nil)
			if c != nil {
				t.Errorf("FromEnv() client = %v, want nil", c)
			}
			if err == nil {
				t.Fatal("FromEnv() err = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), EnvFake) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), EnvFake)
			}
		})
	}
}

// envCaseName names a subtest after a value whose whitespace matters.
func envCaseName(s string) string {
	return strings.NewReplacer(" ", "_space_", "\t", "_tab_", "\n", "_newline_").Replace(s)
}

// TestFromEnv_WiresTheRealEndpointBudgetAndClock reads cfg directly: a FromEnv
// client that dropped the endpoint, the budget or the clock would still pass
// every off/fake test, because neither of those paths reaches the wire.
func TestFromEnv_WiresTheRealEndpointBudgetAndClock(t *testing.T) {
	t.Setenv(EnvKey, "k")
	t.Setenv(EnvFake, "true")

	c, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv() err = %v, want nil", err)
	}
	if c.cfg.endpoint != endpoint {
		t.Errorf("cfg.endpoint = %q, want %q", c.cfg.endpoint, endpoint)
	}
	if c.cfg.budget != budget {
		t.Errorf("cfg.budget = %v, want %v", c.cfg.budget, budget)
	}
	if c.cfg.key != "k" {
		t.Errorf("cfg.key = %q, want %q", c.cfg.key, "k")
	}
	if !c.cfg.fake {
		t.Error("cfg.fake = false, want true")
	}
	if c.http == nil {
		t.Error("http = nil, want a client")
	}

	if c.cfg.now == nil {
		t.Fatal("cfg.now = nil, want the real clock")
	}
	if drift := time.Since(c.cfg.now()); drift < 0 || drift > time.Minute {
		t.Errorf("cfg.now() is %v off the real clock, want the real clock", drift)
	}

	if c.cfg.sleep == nil {
		t.Fatal("cfg.sleep = nil, want the real sleep")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.cfg.sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("cfg.sleep(cancelled ctx) = %v, want context.Canceled (a real sleep, not a no-op)", err)
	}
}

// TestFromEnv_AWhitespaceKeyIsSetNotOff is the consequence of T02's
// whitespace_key row: the call is not ErrOff, it goes out with that key, and
// upstream is what refuses it.
func TestFromEnv_AWhitespaceKeyIsSetNotOff(t *testing.T) {
	var hits atomic.Int32
	var rec capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		rec.record(r)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := newClient(config{key: " ", endpoint: srv.URL, budget: budget, now: time.Now, sleep: realSleep}, nil)
	if !c.Enabled() {
		t.Fatal("Enabled() = false, want true for a whitespace key")
	}

	answer, err := callWithin(t, c, t.Context(), baseReq(), 2*time.Second)
	if errors.Is(err, ErrOff) {
		t.Error("err wraps ErrOff, want the upstream refusal")
	}
	if err == nil {
		t.Fatal("err = nil, want the upstream 401 refusal")
	}
	if answer != nil {
		t.Errorf("answer = %#v, want nil", answer)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1: a whitespace key must still reach the wire", got)
	}
	// Go trims a header value's surrounding space on read, so only the
	// prefix is assertable here.
	h, _, _ := rec.snapshot()
	if got := h.Get("Authorization"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("Authorization = %q, want it to start with %q", got, "Bearer")
	}
}
