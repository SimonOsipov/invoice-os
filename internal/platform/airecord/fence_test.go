package airecord

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAirecordPackage_ImportsNoPgxSentryOrAI(t *testing.T) {
	modOut, err := exec.CommandContext(t.Context(), "go", "list", "-m").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	mod := strings.TrimSpace(string(modOut))

	depsOut, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps .: %v", err)
	}
	var sawContext bool
	for _, line := range strings.Split(strings.TrimSpace(string(depsOut)), "\n") {
		dep := strings.TrimSpace(line)
		if dep == "context" {
			sawContext = true
		}
		if strings.HasPrefix(dep, "github.com/jackc/pgx") ||
			strings.HasPrefix(dep, "github.com/getsentry/sentry-go") ||
			dep == mod+"/internal/platform/ai" || strings.HasPrefix(dep, mod+"/internal/platform/ai/") {
			t.Errorf("airecord depends on %s -- banned", dep)
		}
	}
	if !sawContext {
		t.Fatal("control needle context missing -- the fence would pass vacuously")
	}
}
