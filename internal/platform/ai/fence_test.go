// fence_test.go: T16 -- internal/platform/ai depends only on internal/platform/*
// and never crosses out of the platform fence.
package ai

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAIPackage_StaysInsideTheExtractionFence(t *testing.T) {
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
		t.Fatalf("go list -deps returned %d lines, want >= 20 (a truncated scan)", len(lines))
	}

	self := mod + "/internal/platform/ai"
	platformPfx := mod + "/internal/platform/"

	var sawContext, sawSelf bool
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "context":
			sawContext = true
		case self:
			sawSelf = true
		}
	}
	if !sawContext {
		t.Fatalf("dep list missing %q; the fence below would pass vacuously", "context")
	}
	if !sawSelf {
		t.Fatalf("dep list missing %q; the fence below would pass vacuously", self)
	}

	for _, line := range lines {
		dep := strings.TrimSpace(line)
		if dep != mod && !strings.HasPrefix(dep, mod+"/") {
			continue
		}
		if dep == self {
			continue
		}
		if !strings.HasPrefix(dep, platformPfx) {
			t.Errorf("internal/platform/ai depends on %s -- only internal/platform/* is allowed", dep)
		}
	}
}
