package approval

// Static guards (no DB, so they also run in the `go` job) over the two config files that
// decide whether this package's DB-backed tests execute at all.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the module root from this package's directory.
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
		// A new two-space key ends the rls job's block.
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
	// A sibling step this package does not own: proves the scan captured the job's steps.
	if !strings.Contains(strings.Join(block, "\n"), "./internal/importer/...") {
		t.Fatalf("the extracted rls job does not run ./internal/importer/... — the block scan is broken, so every "+
			"assertion against it is vacuous:\n%s", strings.Join(block, "\n"))
	}
	return block
}

// Without the rls-job step this package's 64 DB-backed tests SKIP into a green build, and
// rls-test-gate.sh is the only thing that fails a build on a silent skip.
func TestApproval_CIRLSJobRunsThisPackage(t *testing.T) {
	block := rlsJobBlock(t)

	gate, seed := -1, -1
	for i, line := range block {
		if !strings.Contains(line, "scripts/ci/rls-test-gate.sh") {
			continue
		}
		switch {
		case strings.Contains(line, "./internal/approval/..."):
			gate = i
			// A filter would re-open the silent-skip hole for whatever it fails to match.
			if strings.Contains(line, "-run") {
				t.Errorf("the approval gate step carries a -run filter, so any test it does not match would "+
					"still SKIP into a green build: %s", strings.TrimSpace(line))
			}
		case strings.Contains(line, "TestSeed|TestCIRunFilters"):
			seed = i
		}
	}

	if gate == -1 {
		t.Fatal("the rls job has no `scripts/ci/rls-test-gate.sh ... ./internal/approval/...` step — this " +
			"package's DB-backed tests would either not run at all or SKIP into a green build")
	}
	if seed == -1 {
		t.Fatal("the rls job has no `TestSeed|TestCIRunFilters` step — the ordering assertion below would pass vacuously")
	}
	if gate > seed {
		t.Errorf("the approval step is at rls-block line %d, after the seed step at %d; db.Seed re-anchors "+
			"created_at and re-enables every rule, so the seed step must stay last", gate, seed)
	}
}

// `go test` exits 0 on a skip, so a short export set makes `make test-approvals` report `ok`
// having run none of the DB-backed tests. All three DSNs the package reads are pinned —
// DATABASE_MIGRATION_URL feeds policy_immutability_test.go's migratorPool (the owner-proof
// attacks on the seal lock).
func TestApproval_MakeTargetExportsTheDSNsThePackageReads(t *testing.T) {
	path := filepath.Join(repoRoot(t), "Makefile")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	idx := strings.Index(string(raw), "\ntest-approvals:")
	if idx == -1 {
		t.Fatal("the Makefile has no `test-approvals` target — the only local route to this package's DB-backed tests")
	}
	recipe := string(raw)[idx+1:]
	if end := strings.Index(recipe, "\n\n"); end != -1 {
		recipe = recipe[:end]
	}

	for _, dsn := range []string{"DATABASE_URL", "DATABASE_SUPERUSER_URL", "DATABASE_MIGRATION_URL"} {
		if !strings.Contains(recipe, dsn+`="`) {
			t.Errorf("test-approvals does not export %s — the suite would silently skip and still report ok:\n%s", dsn, recipe)
		}
	}
	if !strings.Contains(recipe, "./internal/approval/...") {
		t.Errorf("test-approvals no longer selects ./internal/approval/...:\n%s", recipe)
	}
}
