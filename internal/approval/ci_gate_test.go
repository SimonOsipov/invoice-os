package approval

// Static guards (no DB, so they also run in the `go` job) over the two config files that
// decide whether this package's DB-backed tests execute at all.

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// TestApproval_DoesNotImportInvoicePackage (task-482 AC-5): internal/approval must never
// import internal/invoice -- task-482 created the internal/invoice -> internal/approval
// edge and it must not reverse. Modelled on internal/invoice/validator_test.go's VC-14
// guard (TestValidatorClient_DoesNotImportValidationPackage): `go list -deps` WITHOUT
// -test, so test files never enter the graph. The edge exists now (since subtask 06);
// this polices it from ever reversing.
func TestApproval_DoesNotImportInvoicePackage(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "./internal/approval")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/approval: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "github.com/SimonOsipov/invoice-os/internal/invoice" {
			t.Fatalf("internal/approval imports internal/invoice -- forbidden: the arming edge runs " +
				"internal/invoice -> internal/approval only and must never reverse")
		}
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

// TestMain_WiresTheFingerprinter (task-484 AC-6): cmd/invoice/main.go's approval.NewStore
// call must pass invoice.FingerprintTx as its second argument, never nil. The required
// parameter makes an OMITTED argument a compile error, but nil still compiles and would
// only fail closed at runtime -- this is the static guard that catches that at review
// time instead. An AST walk, not a byte scan, mirrors
// TestWorkflowRoleHandlers_RoutesRegisteredInCmdInvoiceMain (handlers_test.go), so
// reformatting main.go cannot break it. Fails today: main.go:176 reads
// approval.NewStore(pool), one argument.
func TestMain_WiresTheFingerprinter(t *testing.T) {
	path := filepath.Join(repoRoot(t), "cmd", "invoice", "main.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var found bool
	ast.Inspect(f, func(node ast.Node) bool {
		if found {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewStore" {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "approval" {
			return true
		}
		found = true

		if len(call.Args) != 2 {
			t.Fatalf("approval.NewStore call in %s has %d argument(s), want 2 (pool, fingerprinter)",
				path, len(call.Args))
			return false
		}
		arg1, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok {
			t.Fatalf("approval.NewStore's second argument in %s is not invoice.FingerprintTx (got %T) -- "+
				"nil still compiles here but only fails closed at runtime", path, call.Args[1])
			return false
		}
		pkg1, ok := arg1.X.(*ast.Ident)
		if !ok || pkg1.Name != "invoice" || arg1.Sel.Name != "FingerprintTx" {
			t.Fatalf("approval.NewStore's second argument in %s is not invoice.FingerprintTx", path)
		}
		return false
	})
	if !found {
		t.Fatal("no approval.NewStore( call found in cmd/invoice/main.go")
	}
}
