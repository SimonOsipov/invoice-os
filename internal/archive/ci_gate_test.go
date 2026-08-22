package archive_test

// Static guards (no DB, so they also run in the `go` job) over the config file that
// decides whether this package's DB-backed tests execute at all. Modelled on
// internal/actor/ci_gate_test.go.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// rlsJobBlock returns the lines of ci.yml's `rls:` job, or fails.
func rlsJobBlock(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var inJobs, inRLS bool
	var block []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "jobs:") {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.TrimSpace(line) != "" {
			inRLS = strings.TrimSpace(line) == "rls:"
			continue
		}
		if inRLS {
			block = append(block, line)
		}
	}
	if len(block) == 0 {
		t.Fatal("no `rls:` job in .github/workflows/ci.yml — every assertion against the block would pass vacuously")
	}
	return block
}

// TestArchive_CIJobBlockExtractionIsNotVacuous is the control needle: a sibling step
// this package does not own proves the scan captured real content rather than an
// empty or truncated block.
func TestArchive_CIJobBlockExtractionIsNotVacuous(t *testing.T) {
	block := rlsJobBlock(t)
	if !strings.Contains(strings.Join(block, "\n"), "./internal/importer/...") {
		t.Fatalf("the extracted rls job does not run ./internal/importer/... — the block scan is broken, so every "+
			"assertion against it is vacuous:\n%s", strings.Join(block, "\n"))
	}
}

// Without the rls-job step this package's DB-backed tests SKIP into a green build, and
// rls-test-gate.sh is the only thing that fails a build on a silent skip.
func TestArchive_CIJobRunsThisPackage(t *testing.T) {
	block := rlsJobBlock(t)

	gate, seed := -1, -1
	for i, line := range block {
		if !strings.Contains(line, "scripts/ci/rls-test-gate.sh") {
			continue
		}
		switch {
		case strings.Contains(line, "./internal/archive/..."):
			gate = i
			// A filter would re-open the silent-skip hole for whatever it does not match.
			if strings.Contains(line, "-run") {
				t.Errorf("the archive gate step carries a -run filter, so any test it does not match would "+
					"still SKIP into a green build: %s", strings.TrimSpace(line))
			}
		case strings.Contains(line, "TestSeed|TestCIRunFilters"):
			seed = i
		}
	}

	if gate == -1 {
		t.Fatal("the rls job has no `scripts/ci/rls-test-gate.sh ... ./internal/archive/...` step — this " +
			"package's DB-backed tests would either not run at all or SKIP into a green build")
	}
	if seed == -1 {
		t.Fatal("the rls job has no `TestSeed|TestCIRunFilters` step — the ordering assertion below would pass vacuously")
	}
	if gate > seed {
		t.Errorf("the archive step is at rls-block line %d, after the seed step at %d; db.Seed re-anchors "+
			"created_at and re-enables every rule, so the seed step must stay last", gate, seed)
	}
}

// A bare `go test` would let a t.Skip pass the build; only rls-test-gate.sh fails on
// a skip, a zero-ran package, or a real failure.
func TestArchive_CIGateScriptIsTheRunner(t *testing.T) {
	block := rlsJobBlock(t)

	var line string
	for _, l := range block {
		if strings.Contains(l, "./internal/archive/...") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("no step in the rls block runs ./internal/archive/... — cannot check its runner")
	}
	if !strings.Contains(line, "scripts/ci/rls-test-gate.sh") {
		t.Errorf("the ./internal/archive/... step does not go through scripts/ci/rls-test-gate.sh: %s", strings.TrimSpace(line))
	}
}

// Subtask 05's plan-shape tests must ANALYZE tables; invoice_app cannot — Postgres
// answers with a WARNING, not an error, so the test would pass having proved nothing.
func TestArchive_CIJobExportsTheSuperuserDSN(t *testing.T) {
	block := rlsJobBlock(t)
	if !strings.Contains(strings.Join(block, "\n"), "DATABASE_SUPERUSER_URL") {
		t.Error("the rls job env does not export DATABASE_SUPERUSER_URL")
	}
}
