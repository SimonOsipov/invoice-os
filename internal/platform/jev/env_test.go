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
		name string
		key  *string
	}{
		{"key_unset", nil},
		{"key_empty", ptr("")},
		{"key_set", ptr("k")},
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
			cmd := exec.CommandContext(t.Context(), os.Args[0])
			cmd.Env = env

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("subprocess err = %v: FromEnv must return, not exit. output: %s", err, out)
			}
			want := subprocessDone + " client=true err=<nil>"
			if !strings.Contains(string(out), want) {
				t.Errorf("output = %q, want it to contain %q", out, want)
			}
		})
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
