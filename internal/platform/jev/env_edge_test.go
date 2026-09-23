package jev

import (
	"strings"
	"testing"
)

// Retyped on purpose: CI's PR-environment step sets this exact name.
func TestFromEnv_ReadsTheJevFakeVariable(t *testing.T) {
	t.Setenv(EnvKey, "")
	t.Setenv("JEV_FAKE", "true")

	c, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv() err = %v, want nil", err)
	}
	if !c.cfg.fake {
		t.Error("cfg.fake = false, want true read from JEV_FAKE")
	}
}

// Only a true JEV_FAKE refuses a key; a set-but-false one is the real client.
func TestFromEnv_AFalseFakeWithAKeyIsTheRealClient(t *testing.T) {
	for _, raw := range []string{"false", "0", "FALSE"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(EnvFake, raw)
			t.Setenv(EnvKey, "k")

			c, err := FromEnv(nil)
			if err != nil {
				t.Fatalf("FromEnv() err = %v, want nil", err)
			}
			if c == nil {
				t.Fatal("FromEnv() client = nil, want non-nil")
			}
			if c.cfg.fake || c.cfg.key != "k" || !c.Enabled() {
				t.Errorf("cfg.fake = %v, cfg.key = %q, Enabled() = %v; want false, %q, true", c.cfg.fake, c.cfg.key, c.Enabled(), "k")
			}
		})
	}
}

func TestFromEnv_AnUnparseableFakeNeverCarriesTheKey(t *testing.T) {
	t.Setenv(EnvFake, "ture")
	t.Setenv(EnvKey, "sk-SECRET-7f")

	c, err := FromEnv(nil)
	if c != nil {
		t.Error("FromEnv() client = non-nil, want nil")
	}
	text := errText(err)
	if !strings.Contains(text, "JEV_FAKE") {
		t.Errorf("FromEnv() err = %q, want it to name JEV_FAKE", text)
	}
	if strings.Contains(text, "SECRET") {
		t.Errorf("FromEnv() err = %q contains the key's value", text)
	}
}
