package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMailSubjectCheck(t *testing.T) {
	cases := map[string]struct {
		subject  string
		wantCode int
	}{
		"plain":                       {"Confirm your ASComply account", 0},
		"uses a data field":           {"Welcome {{ .Data.registration.display_name }}", 0},
		"unclosed action":             {"Confirm {{ ", 1},
		"errors on execute":           {"Confirm {{ index .Email 99 }}", 1},
		"unknown function":            {"Confirm {{ nope }}", 1},
		"html context left open":      {"Hi <b {{ .Email }}", 1},
		"errors only without answers": {"Hi {{ len .Data.registration }}", 1},
		"empty subject is not a call": {"", 2},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			code := RunMailSubjectCheck([]string{c.subject}, &out)
			if code != c.wantCode {
				t.Errorf("exit %d, want %d; output = %q", code, c.wantCode, out.String())
			}
			if c.wantCode != 0 && !strings.Contains(out.String(), "::error::") {
				t.Errorf("a failure printed no ::error:: line: %q", out.String())
			}
			if c.wantCode == 0 && !strings.HasPrefix(out.String(), "ok ") {
				t.Errorf("a pass printed no ok line: %q", out.String())
			}
		})
	}
	t.Run("two arguments exit 2", func(t *testing.T) {
		var out bytes.Buffer
		if code := RunMailSubjectCheck([]string{"a", "b"}, &out); code != 2 {
			t.Errorf("exit %d, want 2", code)
		}
	})
	t.Run("no argument exits 2", func(t *testing.T) {
		var out bytes.Buffer
		if code := RunMailSubjectCheck(nil, &out); code != 2 {
			t.Errorf("exit %d, want 2", code)
		}
	})
}

func TestMailSubjectCheck_CLI(t *testing.T) {
	_, stderr, _ := runCLI(t)
	if !strings.Contains(stderr, "mail-subject-check") {
		t.Errorf("usage lacks mail-subject-check; stderr = %q", stderr)
	}
	if _, _, code := runCLI(t, "mail-subject-check", "Confirm {{ "); code != 1 {
		t.Errorf("a bad subject exited %d, want 1", code)
	}
	stdout, _, code := runCLI(t, "mail-subject-check", "Confirm your ASComply account")
	if code != 0 || stdout != "ok Confirm your ASComply account\n" {
		t.Errorf("a good subject exited %d with stdout %q, want 0 and its ok line", code, stdout)
	}
}
