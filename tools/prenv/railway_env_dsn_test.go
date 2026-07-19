// railway_env_dsn_test.go pins the WIRING contract of the not-yet-implemented
// `scripts/ci/railway-env.sh assert-db-dsns` subcommand (M4-22-FU-02,
// task-178). dsn_check_test.go proves the checker is correct; this file
// proves the checker is actually REACHED, actually fails the job, and does
// not leak on the way.
//
// WHY A GO TEST FOR A SHELL SCRIPT. Precedent: cmd/gateway/main_test.go
// :130-181 already asserts on repo source text from a Go test, for the same
// reason -- `go test ./...` is the one gate that always runs, so a guard
// living anywhere else is a guard that can be skipped.
//
// TOKEN-FREE AND NETWORK-FREE, BY CONSTRUCTION. Every test here drives
// `--self-test`, which must short-circuit BEFORE require_env
// (scripts/ci/railway-env.sh:~127). Nothing below calls Railway, and T2-4
// asserts the short-circuit ordering directly by unsetting the token. No test
// in this file skips: a test that silently skips in CI is a decorative test.
//
// ASSUMED CONTRACT -- FLAGGED FOR THE EXECUTOR. The plan fixes that
// `--self-test` needs no token and no network, but does not say where its map
// comes from, and T2-1/T2-3 need to drive a BAD map and a GOOD map
// separately. These tests therefore assume the map arrives on STDIN, the same
// channel `dsn-check` uses and for the same reason: argv is visible in `ps`
// and this map carries live credentials. If the executor picks a different
// input channel, this file must be updated deliberately -- not silently
// loosened.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot resolves the repository this test is running in. Uses git rather
// than a relative path so the test is correct under a worktree checkout --
// which is where this story is being developed.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func railwayEnvScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "scripts", "ci", "railway-env.sh")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("railway-env.sh not found at %s: %v", path, err)
	}
	return path
}

// runSelfTest execs `railway-env.sh assert-db-dsns --self-test` with m on
// stdin. extraEnv entries are appended to the inherited environment;
// unsetVars are removed from it. Returns combined stdout, stderr, exit code.
func runSelfTest(t *testing.T, m dsnMap, extraEnv []string, unsetVars ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshalling fixture map: %v", err)
	}

	cmd := exec.Command("bash", railwayEnvScript(t), "assert-db-dsns", "--self-test")
	cmd.Stdin = strings.NewReader(string(raw))
	cmd.Dir = repoRoot(t)

	env := os.Environ()
	if len(unsetVars) > 0 {
		var filtered []string
		for _, kv := range env {
			drop := false
			for _, name := range unsetVars {
				if strings.HasPrefix(kv, name+"=") {
					drop = true
					break
				}
			}
			if !drop {
				filtered = append(filtered, kv)
			}
		}
		env = filtered
	}
	cmd.Env = append(env, extraEnv...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("failed to run railway-env.sh: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// badSelfTestMap is the shared bad fixture: the verbatim M4-22 incident DSN
// in the slot it actually occupied.
func badSelfTestMap() dsnMap {
	m := healthyMap()
	m["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN
	return m
}

// T2-1. The self-test must actually FAIL the job on a bad map, and must
// surface the offender through GitHub Actions' `::error::` annotation channel
// naming both the service and the variable. A self-test that always exits 0
// is the exact decorative-guard shape this whole story exists to prevent: it
// is green, it is wired into CI, and it cannot fail.
//
// Asserts exit != 0 only, per the two-way exit contract (see
// dsn_check_test.go's package comment).
//
// KILLS: M-selftest0.
func TestAssertDBDSNsSelfTestFailsOnBadMap(t *testing.T) {
	stdout, stderr, code := runSelfTest(t, badSelfTestMap(), nil)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero: the self-test fed a DSN with an empty password and must fail; stdout = %q, stderr = %q", stdout, stderr)
	}
	if !strings.Contains(stdout, "::error::") {
		t.Errorf("stdout carries no ::error:: annotation -- without it the failure is invisible in the Actions UI; stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "gateway") {
		t.Errorf("stdout does not name the offending service %q; stdout = %q", "gateway", stdout)
	}
	if !strings.Contains(stdout, "DATABASE_MIGRATION_URL") {
		t.Errorf("stdout does not name the offending variable %q; stdout = %q", "DATABASE_MIGRATION_URL", stdout)
	}
}

// T2-2. Credential hygiene at the WIRING layer, not just inside the checker.
// The shell is where a stray `set -x`, an `echo "$map"`, or a `::error::` that
// interpolates the whole payload would leak -- dsn_check_test.go's T1-9 cannot
// see any of those.
//
// KILLS: M-mask / credential echo at the wiring layer.
func TestAssertDBDSNsSelfTestNeverEchoesACredential(t *testing.T) {
	stdout, stderr, code := runSelfTest(t, badSelfTestMap(), nil)

	if code == 0 {
		t.Errorf("exit code = 0 -- this run must FAIL for the leak assertions below to be meaningful rather than vacuous")
	}
	// NON-VACUITY PRECONDITION -- see the same guard in dsn_check_test.go's
	// TestDSNCheckNeverEchoesACredential. A script that never reaches the
	// check leaks nothing, so the leak assertions must be gated on the check
	// having actually reported the offender.
	if !strings.Contains(stdout, "gateway") || !strings.Contains(stdout, "DATABASE_MIGRATION_URL") {
		t.Errorf("the self-test did not report the offender -- 'it leaked no credential' is vacuous until it produces a real report; stdout = %q", stdout)
	}
	if strings.Contains(stdout, sentinelPW) {
		t.Errorf("stdout leaked the password sentinel %q into the CI log; stdout = %q", sentinelPW, stdout)
	}
	if strings.Contains(stderr, sentinelPW) {
		t.Errorf("stderr leaked the password sentinel %q into the CI log; stderr = %q", sentinelPW, stderr)
	}
}

// T2-3. The inverted-self-test guard. A self-test that fails on a healthy map
// is worse than none: it blocks every deploy until someone disables it, and
// then nothing is checked at all.
//
// KILLS: inverted self-test.
func TestAssertDBDSNsSelfTestPassesOnGoodMap(t *testing.T) {
	stdout, stderr, code := runSelfTest(t, healthyMap(), nil)

	if code != 0 {
		t.Errorf("exit code = %d, want 0: the fleet map is entirely healthy; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "::error::") {
		t.Errorf("stdout emits an ::error:: annotation for a healthy map; stdout = %q", stdout)
	}
}

// T2-4. Pins the ORDERING that makes the self-test runnable at all: the
// `--self-test` short-circuit must come BEFORE require_env
// (scripts/ci/railway-env.sh:~127). With RAILWAY_API_TOKEN unset, a self-test
// placed after require_env exits 1 with "RAILWAY_API_TOKEN is not set" --
// which is a FAILING self-test that never ran the check, and which on a fork
// PR (no secrets, by design) would fail every build.
//
// RAILWAY_PROJECT_ID is unset too: require_env checks both, so leaving one set
// would let the test pass against a half-moved short-circuit.
//
// KILLS: require_env moved ahead of the short-circuit.
func TestAssertDBDSNsSelfTestNeedsNoToken(t *testing.T) {
	stdout, stderr, code := runSelfTest(t, healthyMap(), nil, "RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID")

	if code != 0 {
		t.Errorf("exit code = %d, want 0 with RAILWAY_API_TOKEN and RAILWAY_PROJECT_ID unset: --self-test must short-circuit before require_env, so it needs no token and no network; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "RAILWAY_API_TOKEN") || strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("the self-test reached require_env -- it must short-circuit before it; stdout = %q, stderr = %q", stdout, stderr)
	}
}

// T2-5. The severity table must be derived from service NAMES, never from
// hardcoded Railway service UUIDs. A UUID is environment-scoped: hardcoding
// the `development` fleet's ids makes the check silently inert in every
// per-PR environment, where the same services carry different ids -- a guard
// that is green precisely where it is needed least.
//
// Precedent for grepping repo source from a Go test:
// cmd/gateway/main_test.go:149,181.
//
// HONESTY NOTE -- THIS TEST IS GREEN TODAY. railway-env.sh currently contains
// ZERO UUIDs (measured), so unlike every other test in this file it does not
// start RED. It is a REGRESSION GUARD, not a specification of unbuilt
// behaviour: it fails the moment the executor reaches for a UUID while
// implementing cmd_assert_db_dsns. Recording this rather than dressing it up
// as a RED spec.
//
// KILLS: M-hardcode.
func TestRailwayEnvScriptHardcodesNoServiceUUIDs(t *testing.T) {
	path := railwayEnvScript(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	uuidPattern := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	hits := uuidPattern.FindAllString(string(content), -1)
	if len(hits) > 0 {
		t.Errorf("railway-env.sh hardcodes %d UUID(s): %v. Service ids are environment-scoped -- resolve services by NAME (as SETTLE_QUERY at :637 already does) so the check works in per-PR environments too.", len(hits), hits)
	}
}

// T2-6. The debug-dump guard. The rendered map is the single most
// credential-dense object this script ever holds, and GITHUB_OUTPUT /
// GITHUB_ENV are the two sinks whose contents outlive the step and flow into
// later steps -- a leak there is durable, not just a scrollback line.
//
// Runs the BAD map: the failure path is where a debug dump is most likely to
// have been added, and it is the path a hurried operator adds one to.
//
// HONESTY NOTE: like T2-5 this cannot fail today for the reason it exists
// (the subcommand does not exist, so nothing is written to either file). Its
// exit-code assertion IS red today; the leak assertions become load-bearing
// only once the executor implements the subcommand. Kept because a leak guard
// added after the leak is a guard added too late.
//
// KILLS: M-echo.
func TestAssertDBDSNsSelfTestDoesNotLeakIntoGitHubFiles(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "github_output")
	envPath := filepath.Join(dir, "github_env")
	for _, p := range []string{outPath, envPath} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatalf("creating %s: %v", p, err)
		}
	}

	stdout, _, code := runSelfTest(t, badSelfTestMap(), []string{
		"GITHUB_OUTPUT=" + outPath,
		"GITHUB_ENV=" + envPath,
	})

	if code == 0 {
		t.Errorf("exit code = 0 on the bad map -- this run must FAIL for the leak assertions below to be meaningful rather than vacuous")
	}
	// NON-VACUITY PRECONDITION -- see T2-2. Without this, a script that never
	// reaches the check writes nothing to either file and passes.
	if !strings.Contains(stdout, "gateway") || !strings.Contains(stdout, "DATABASE_MIGRATION_URL") {
		t.Errorf("the self-test did not report the offender -- the no-leak assertions below are vacuous until it does; stdout = %q", stdout)
	}

	for _, p := range []string{outPath, envPath} {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		text := string(body)
		if strings.Contains(text, sentinelPW) {
			t.Errorf("%s contains the password sentinel %q -- GITHUB_OUTPUT/GITHUB_ENV outlive the step and flow into later ones, so a leak here is durable; contents = %q", filepath.Base(p), sentinelPW, text)
		}
		if strings.Contains(text, "postgresql://") {
			t.Errorf("%s contains a raw DSN; contents = %q", filepath.Base(p), text)
		}
	}

	if strings.Contains(stdout, "postgresql://") {
		t.Errorf("stdout dumped raw DSNs from the rendered map; report service and variable NAMES only. stdout = %q", stdout)
	}
	// A whole-map dump is recognisable by its JSON envelope even if the
	// values were somehow scrubbed.
	if strings.Contains(stdout, `{"gateway":`) || strings.Contains(stdout, `"DATABASE_MIGRATION_URL":`) {
		t.Errorf("stdout dumped the whole rendered map as JSON; stdout = %q", stdout)
	}
}
