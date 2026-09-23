package jev

import (
	"os/exec"
	"strings"
	"testing"
)

// The package sits under internal/platform so internal/extraction and
// internal/importer may both import it; nothing it depends on may break that.
func TestJevPackage_StaysInsideThePlatformFence(t *testing.T) {
	modOut, err := exec.CommandContext(t.Context(), "go", "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	mod := strings.TrimSpace(string(modOut))

	depsOut, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps .: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(depsOut)), "\n")
	if len(lines) < 20 {
		t.Fatalf("go list -deps returned %d line(s), want >= 20 (a truncated scan)", len(lines))
	}

	self := mod + "/internal/platform/jev"
	platformPfx := mod + "/internal/platform/"
	banned := map[string]bool{
		mod + "/internal/platform/ai":    true,
		mod + "/internal/jevmeasure":     true,
		"github.com/getsentry/sentry-go": true,
	}

	var sawContext, sawSelf bool
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "context":
			sawContext = true
		case self:
			sawSelf = true
		}
	}
	if !sawContext || !sawSelf {
		t.Fatalf("control needles: context=%v %s=%v -- the fence below would pass vacuously", sawContext, self, sawSelf)
	}

	for _, line := range lines {
		dep := strings.TrimSpace(line)
		if banned[dep] || strings.HasPrefix(dep, "github.com/getsentry/sentry-go/") {
			t.Errorf("internal/platform/jev depends on %s -- banned", dep)
			continue
		}
		if dep == self || (dep != mod && !strings.HasPrefix(dep, mod+"/")) {
			continue
		}
		if !strings.HasPrefix(dep, platformPfx) {
			t.Errorf("internal/platform/jev depends on %s -- only internal/platform/* is allowed", dep)
		}
	}
}
