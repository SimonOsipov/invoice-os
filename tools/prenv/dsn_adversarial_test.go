// dsn_adversarial_test.go closes two coverage holes found by MUTATING the
// M4-22-FU implementation rather than by reading it (QA Stage 4, task-178).
//
// Both holes have the same shape -- the shape this whole story exists to
// prevent: a guard that is green, is wired into CI, and cannot fail for the
// reason it was built. Each was proven live before the spec below was written:
// the mutation was applied to the real source, `go test -count=1
// ./tools/prenv/...` stayed GREEN, and the mutation was reverted.
//
//	M-prefix    add {"invoice", "PGBOUNCER_DSN", IfPresent} to DSNRequirements
//	            -> suite green. The shell filters the rendered map to
//	            DATABASE* (railway-env.sh:803,874-875), so that row is dropped
//	            before it ever reaches CheckDSNs and is never checked.
//	M-buildmsg  collapse run_dsn_check's build-failure message into the
//	            defects-found message (railway-env.sh:818)
//	            -> suite green. Nothing drives that branch at all.
//
// Neither spec restates behaviour an existing test already pins; each fails
// today if and only if its named mutation is reapplied.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// T3-1. Every row of the severity table must name a variable the shell will
// actually SHIP, and the shell ships only variables starting with
// DSN_VAR_PREFIX.
//
// WHY THIS IS NOT A STYLE RULE. cmd_assert_db_dsns enumerates every service
// instance in the environment -- including landing/app/ops-console, which hold
// no DSN -- so it filters values to DATABASE* before they leave the script
// (railway-env.sh:874-875). That filter is correct and deliberate: the
// rendered map is the most credential-dense object the script ever holds and
// narrowing it is a hygiene win. But it silently couples the Go table to a
// shell string. A future row named PGBOUNCER_DSN, DB_DSN, or POSTGRES_URL
// would be filtered out upstream, arrive absent, and -- if its severity is
// IfPresent -- be skipped in total silence. The row would look like coverage
// and be none.
//
// WHY THE PREFIX IS READ FROM THE SCRIPT AND NOT HARDCODED HERE. A copy of
// "DATABASE" in this file would drift the moment someone narrows the filter
// (say to DATABASE_URL, which would silently drop gateway's
// DATABASE_MIGRATION_URL). Parsing the live assignment makes the assertion
// bidirectional: it fails on a bad table row AND on a bad filter change.
//
// KILLS: M-prefix, and any narrowing of DSN_VAR_PREFIX.
func TestEverySeverityTableRowSurvivesTheShellPrefixFilter(t *testing.T) {
	path := railwayEnvScript(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	m := regexp.MustCompile(`(?m)^DSN_VAR_PREFIX="([^"]*)"`).FindStringSubmatch(string(content))
	if m == nil {
		t.Fatalf("could not find the DSN_VAR_PREFIX assignment in %s. If the prefix filter was removed, delete this test deliberately; if it was renamed, update this pattern -- do not let the coupling go unasserted.", path)
	}
	prefix := m[1]

	// Non-vacuity: an empty prefix makes startswith() universally true, so the
	// assertion below would pass against a filter that filters nothing.
	if prefix == "" {
		t.Fatalf("DSN_VAR_PREFIX is empty -- every check below would pass vacuously")
	}

	if len(DSNRequirements) == 0 {
		t.Fatalf("DSNRequirements is empty -- the loop below would pass vacuously")
	}

	for _, req := range DSNRequirements {
		if !strings.HasPrefix(req.Variable, prefix) {
			t.Errorf("severity-table row {%s, %s, %v} names a variable that does NOT start with DSN_VAR_PREFIX %q. scripts/ci/railway-env.sh:874-875 filters the rendered map to %s*, so this variable is dropped before it reaches CheckDSNs: the row is never checked, and for an IfPresent row that failure is SILENT. Either rename the variable, or widen the filter and this assertion together.",
				req.Service, req.Variable, req.Severity, prefix, prefix)
		}
	}
}

// T3-2. When tools/prenv cannot be BUILT, the check did not run -- and that
// must not read like the check ran and found defects. The two have opposite
// remedies (fix the toolchain / fix named variables) and, critically, opposite
// epistemic status: "defects found" is knowledge about the fleet, "did not
// build" is the absence of it.
//
// WHY THIS BRANCH EXISTS AT ALL. run_dsn_check execs a BUILT binary rather
// than `go run` precisely because `go run` collapses a COMPILE failure into
// exit 1 with empty stdout -- byte-identical to a clean run of a checker that
// found nothing (railway-env.sh:814-815, and main_test.go:168-175 on the same
// `go run` collapse). Splitting the build out only helps if the split is
// actually reported, which is what this pins.
//
// HOW THE FAILURE IS INDUCED. A `go` shim that exits non-zero is prepended to
// PATH, so `go build` fails exactly as a compile error would -- non-zero, no
// usable binary. This drives the REAL branch in the REAL script; it does not
// assert on a copy of the message.
//
// NON-VACUITY: the same healthy map is run WITHOUT the shim first and must
// exit 0. Without that, this test would pass against a script that fails on
// every input for any reason at all.
//
// KILLS: M-buildmsg, and any change that swallows a build failure.
func TestAssertDBDSNsReportsABuildFailureAsCheckDidNotRun(t *testing.T) {
	// Non-vacuity precondition: this map is clean on a working toolchain.
	if _, _, code := runSelfTest(t, healthyMap(), nil); code != 0 {
		t.Fatalf("the healthy map does not pass on an unmodified toolchain (exit %d) -- the build-failure assertions below would be meaningless", code)
	}

	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "go")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\necho 'simulated compile failure' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("writing the go shim: %v", err)
	}

	// PATH is dropped from the inherited environment and reassembled, rather
	// than appended to: duplicate PATH entries in execve are resolved by libc
	// to the FIRST occurrence, so an appended override would be ignored.
	brokenPath := "PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH")
	stdout, stderr, code := runSelfTest(t, healthyMap(), []string{brokenPath}, "PATH")

	// Guard the induction itself: if the shim were not reached, the run would
	// have exited 0 like the precondition above, and everything below would be
	// asserting about the wrong branch.
	if code == 0 {
		t.Fatalf("exit code = 0 with a failing `go` on PATH: the build-failure branch was never reached, so this test proves nothing. stdout = %q, stderr = %q", stdout, stderr)
	}

	if !strings.Contains(stdout, "::error::") {
		t.Errorf("a failed build emits no ::error:: annotation -- the failure is invisible in the Actions UI; stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "did NOT run") {
		t.Errorf("a failed build does not say the check did NOT run. An operator reading this must not conclude the DSNs were inspected and found healthy; stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "NOT evidence") {
		t.Errorf("a failed build does not state that this is NOT evidence of healthy DSNs; stdout = %q", stdout)
	}

	// Structural distinguishability -- same technique and same reason as T1-2
	// and T1-11: assert the two reports DIFFER rather than matching prose, so
	// the property survives rewording.
	offendersOut, _, offendersCode := runSelfTest(t, badSelfTestMap(), nil)
	if offendersCode == 0 {
		t.Fatalf("the bad map passed (exit 0) -- no offenders report to compare against")
	}
	if stdout == offendersOut {
		t.Errorf("the build-failure report is IDENTICAL to the offenders-found report %q. An operator cannot tell 'the check never ran' from 'the check ran and these variables are broken' -- the first means the fleet's DSN health is UNKNOWN.", stdout)
	}

	// The healthy map carries the sentinel in every DSN, and a build-failure
	// path that dumps its input while bailing out leaks all nine at once.
	if strings.Contains(stdout, sentinelPW) || strings.Contains(stderr, sentinelPW) {
		t.Errorf("the build-failure path leaked the password sentinel %q; stdout = %q, stderr = %q", sentinelPW, stdout, stderr)
	}
	if strings.Contains(stdout, "postgresql://") || strings.Contains(stderr, "postgresql://") {
		t.Errorf("the build-failure path echoed a raw DSN; stdout = %q, stderr = %q", stdout, stderr)
	}
}
