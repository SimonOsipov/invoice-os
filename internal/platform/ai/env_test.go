package ai

import (
	"errors"
	"reflect"
	"strings"
	"testing"
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
