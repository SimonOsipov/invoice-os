package jev

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// unsetEnv removes key for this test; t.Setenv first so it is restored.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

func TestFromEnv_NoKeyIsOff(t *testing.T) {
	for _, name := range []string{"unset", "empty"} {
		t.Run(name, func(t *testing.T) {
			if name == "unset" {
				unsetEnv(t, EnvKey)
			} else {
				t.Setenv(EnvKey, "")
			}
			unsetEnv(t, EnvFake)

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

			// Request{} too: off answers before validation looks at the request.
			for _, req := range []Request{noulReq("s"), {}} {
				noDials(t, func() {
					resp, err := c.Ask(t.Context(), req)
					if got := errText(err); got != "jev: check skipped: off" {
						t.Errorf("Ask() err = %q, want %q", got, "jev: check skipped: off")
					}
					if !errors.Is(err, ErrCheckSkipped) {
						t.Errorf("Ask() err = %v, want errors.Is ErrCheckSkipped", err)
					}
					if len(resp.Answers) != 0 {
						t.Errorf("Ask() answers = %v, want none", resp.Answers)
					}
				})
			}
		})
	}
}

func TestFromEnv_KeySetIsEnabled(t *testing.T) {
	t.Setenv(EnvKey, "k")
	unsetEnv(t, EnvFake)

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
	if c.cfg.key != "k" {
		t.Errorf("cfg.key = %q, want %q", c.cfg.key, "k")
	}
}

func TestFromEnv_AWhitespaceKeyIsSetNotOff(t *testing.T) {
	t.Setenv(EnvKey, " ")
	unsetEnv(t, EnvFake)

	c, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv() err = %v, want nil", err)
	}
	if c == nil {
		t.Fatal("FromEnv() client = nil, want non-nil")
	}
	if !c.Enabled() {
		t.Error("Enabled() = false, want true: a whitespace key is set, never trimmed to off")
	}
}

func TestFromEnv_WiresTheRealEndpointAndBudget(t *testing.T) {
	t.Setenv(EnvKey, "k")
	unsetEnv(t, EnvFake)

	c, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv() err = %v, want nil", err)
	}
	if c.cfg.endpoint != "https://api.typesafe.ai/v1/systemone" {
		t.Errorf("cfg.endpoint = %q, want the vendor URL", c.cfg.endpoint)
	}
	if c.cfg.budget != 3*time.Second {
		t.Errorf("cfg.budget = %v, want 3s", c.cfg.budget)
	}
	if c.cfg.retryWait != 250*time.Millisecond {
		t.Errorf("cfg.retryWait = %v, want 250ms", c.cfg.retryWait)
	}
	if c.cfg.now == nil {
		t.Fatal("cfg.now = nil, want the real clock")
	}
	if drift := time.Since(c.cfg.now()); drift < 0 || drift > time.Minute {
		t.Errorf("cfg.now() is %v off the real clock", drift)
	}
	if c.cfg.sleep == nil {
		t.Fatal("cfg.sleep = nil, want a real sleep")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.cfg.sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("cfg.sleep(cancelled ctx) = %v, want context.Canceled (a sleep that honours ctx)", err)
	}
}

// An in-process test cannot tell "returned" from "called os.Exit"; the
// subprocess mode in main_test.go can.
func TestFromEnv_DoesNotExitTheProcess(t *testing.T) {
	cases := []struct {
		name    string
		key     *string
		fake    *string
		wantErr bool
	}{
		{name: "key_unset"},
		{name: "key_empty", key: ptr("")},
		{name: "key_set", key: ptr("k")},
		{name: "fake_without_a_key", key: ptr(""), fake: ptr("true")},
		{name: "fake_unparseable", fake: ptr("ture"), wantErr: true},
		{name: "fake_with_a_key", key: ptr("k"), fake: ptr("true"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env []string
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, EnvKey+"=") && !strings.HasPrefix(kv, EnvFake+"=") {
					env = append(env, kv)
				}
			}
			env = append(env, envSubprocess+"=1")
			if tc.key != nil {
				env = append(env, EnvKey+"="+*tc.key)
			}
			if tc.fake != nil {
				env = append(env, EnvFake+"="+*tc.fake)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0])
			cmd.Env = env

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("subprocess err = %v: FromEnv must return, not exit. output: %s", err, out)
			}
			want := subprocessDone + " client=true err=<nil>"
			if tc.wantErr {
				want = subprocessDone + " client=false err="
			}
			if !strings.Contains(string(out), want) {
				t.Errorf("output = %q, want it to contain %q", out, want)
			}
			if tc.wantErr && strings.Contains(string(out), "err=<nil>") {
				t.Errorf("output = %q, want a non-nil error", out)
			}
		})
	}
}

func TestFromEnv_FakeParsesLikeParseBool(t *testing.T) {
	cases := []struct {
		name     string
		raw      *string
		wantFake bool
	}{
		{"unset", nil, false},
		{"empty", ptr(""), false},
		{"false", ptr("false"), false},
		{"zero", ptr("0"), false},
		{"true", ptr("true"), true},
		{"one", ptr("1"), true},
		{"TRUE", ptr("TRUE"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvKey, "")
			if tc.raw == nil {
				unsetEnv(t, EnvFake)
			} else {
				t.Setenv(EnvFake, *tc.raw)
			}

			c, err := FromEnv(nil)
			if err != nil {
				t.Fatalf("FromEnv() err = %v, want nil", err)
			}
			if c == nil {
				t.Fatal("FromEnv() client = nil, want non-nil")
			}
			if c.cfg.fake != tc.wantFake {
				t.Errorf("cfg.fake = %v, want %v", c.cfg.fake, tc.wantFake)
			}
			if got := c.Enabled(); got != tc.wantFake {
				t.Errorf("Enabled() = %v, want %v with no key", got, tc.wantFake)
			}
		})
	}

	// Never trimmed: a padded value is a typo, not "not fake".
	for name, raw := range map[string]string{"ture": "ture", "leading_space": " true", "trailing_space": "true "} {
		t.Run("unparseable_"+name, func(t *testing.T) {
			t.Setenv(EnvKey, "")
			t.Setenv(EnvFake, raw)

			c, err := FromEnv(nil)
			if !strings.Contains(errText(err), "JEV_FAKE") {
				t.Errorf("FromEnv() err = %q, want an error naming JEV_FAKE", errText(err))
			}
			if c != nil {
				t.Error("FromEnv() client = non-nil, want nil")
			}
		})
	}
}

func TestFromEnv_FakeWithAKeyRefusesToStart(t *testing.T) {
	t.Setenv(EnvFake, "true")
	t.Setenv(EnvKey, "sk-SECRET-7f")

	c, err := FromEnv(nil)
	if c != nil {
		t.Error("FromEnv() client = non-nil, want nil")
	}
	text := errText(err)
	for _, name := range []string{"JEV_FAKE", "TYPESAFE_API_KEY"} {
		if !strings.Contains(text, name) {
			t.Errorf("FromEnv() err = %q, want it to name %s", text, name)
		}
	}
	if strings.Contains(text, "sk-SECRET-7f") || strings.Contains(text, "SECRET") {
		t.Errorf("FromEnv() err = %q contains the key's value", text)
	}

	t.Run("control_empty_key", func(t *testing.T) {
		t.Setenv(EnvFake, "true")
		t.Setenv(EnvKey, "")

		c, err := FromEnv(nil)
		if err != nil {
			t.Fatalf("FromEnv() err = %v, want nil", err)
		}
		if c == nil {
			t.Fatal("FromEnv() client = nil, want non-nil")
		}
		if !c.Enabled() {
			t.Error("Enabled() = false, want true in fake mode")
		}
	})
}

func TestFromEnv_FakeWithAWhitespaceKeyRefusesToStart(t *testing.T) {
	t.Setenv(EnvFake, "true")
	t.Setenv(EnvKey, " ")

	c, err := FromEnv(nil)
	if err == nil {
		t.Error("FromEnv() err = nil, want an error: a whitespace key is set")
	}
	if c != nil {
		t.Error("FromEnv() client = non-nil, want nil")
	}
	for _, name := range []string{"JEV_FAKE", "TYPESAFE_API_KEY"} {
		if !strings.Contains(errText(err), name) {
			t.Errorf("FromEnv() err = %q, want it to name %s", errText(err), name)
		}
	}
}

func TestFromEnv_AControlByteKeyStillBoots(t *testing.T) {
	t.Setenv(EnvKey, "k\n")
	unsetEnv(t, EnvFake)

	c, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv() err = %v, want nil: a bad key refuses calls, never boot", err)
	}
	if c == nil {
		t.Fatal("FromEnv() client = nil, want non-nil")
	}
	if !c.Enabled() {
		t.Error("Enabled() = false, want true")
	}
}

func ptr(s string) *string { return &s }
