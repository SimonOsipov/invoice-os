package main

import (
	"strings"
	"testing"
	"time"
)

func TestRunExec_TimeoutKillsTheProcessGroup(t *testing.T) {
	start := time.Now()
	_, _, err := runExec(200*time.Millisecond, t.TempDir(), "sh", "-c", "sleep 30 & wait")
	if err == nil || !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Fatalf("err = %v, want timed out after 200ms", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("returned after %v, want under 5s", d)
	}

	out, code, err := runExec(0, t.TempDir(), "sh", "-c", "echo hi; exit 3")
	if out != "hi\n" || code != 3 || err != nil {
		t.Errorf("runExec = %q, %d, %v; want \"hi\\n\", 3, nil", out, code, err)
	}
}
