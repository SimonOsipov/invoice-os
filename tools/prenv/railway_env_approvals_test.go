// railway_env_approvals_test.go pins the RED contract of the not-yet-implemented
// `scripts/ci/railway-env.sh set-approvals-enforced <environment-id>` subcommand
// (APPR-14-03, task-569). Nothing in this file requires the subcommand to exist;
// every test here is expected to FAIL today and pass once Stage 3 implements it.
//
// CORRECTS task-569's implementation plan section 4, which proposed a `curl`
// PATH-shim harness modelled on dsn_adversarial_test.go:167-177. That shim is for
// `go` (forcing a build failure), not `curl` — no `curl` shim exists anywhere in
// this repo. Every shipped test of railway-env.sh instead drives a `--self-test`
// mode that short-circuits BEFORE any network call (railway_env_dsn_test.go's
// package comment: "TOKEN-FREE AND NETWORK-FREE, BY CONSTRUCTION"). This file
// follows that precedent: it drives `set-approvals-enforced --self-test` and the
// guards that fire before require_env, and invents no mocking harness.
//
// NOT COVERED HERE, stated plainly: AC #3 ("zero or multiple invoice instances is
// a hard failure") and AC #4 ("a verify mismatch exits non-zero") against REAL
// Railway data both need a live GraphQL round-trip this file refuses to fake.
// verify_variable in particular cannot be driven token-free — it re-reads via
// graphql_post, which exits the process on any transport or `.errors` response,
// so there is no way to observe "mismatch" without a real (or mocked) server. The
// self-test mode (approvals_self_test, per the plan) is expected to exercise the
// ambiguous/absent-service shapes with INTERNAL fixtures; this file can only
// observe that self-test's aggregate exit code and fixture count, not which
// individual fixtures it covers. The only oracle for the live behaviour is a
// green `prepare-env` run on a real PR (the step's own log names the resolved
// serviceId) plus, per task-569's own VERIFICATION DEPENDENCY section,
// APPR-14-06's contract-invoice.spec.ts 409-while-undecided assertion — the first
// spec in this repo that cannot pass unless this subcommand actually took effect.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The real persistent environment id, hardcoded identically in
// .github/workflows/dev-env.yml:135, dev-env-teardown.yml:56 and
// dev-env-sweeper.yml:42. Used here, not a placeholder, so the refusal test
// exercises the exact string the refusal compares against in CI.
const persistentEnvironmentID = "6c864094-6a06-452f-8495-be77d8a94fe7"

var fixtureCountPattern = regexp.MustCompile(`\d+ fixtures? `)

// runApprovalsCmd execs `railway-env.sh set-approvals-enforced <args...>`.
// extraEnv entries are appended to the inherited environment; unsetVars are
// removed from it first. Returns combined stdout, stderr, exit code. Mirrors
// runSelfTest's env-filtering (railway_env_dsn_test.go) but drives positional
// args instead of a stdin fixture, since set-approvals-enforced takes an
// environment id on argv, not a JSON map on stdin.
func runApprovalsCmd(t *testing.T, args []string, extraEnv []string, unsetVars ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmdArgs := append([]string{railwayEnvScript(t), "set-approvals-enforced"}, args...)
	cmd := exec.Command("bash", cmdArgs...)
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

// TestSetApprovalsEnforcedEmptyArgumentExitsBeforeAnyCompare pins Correction 2
// from task-569: require_source_env checks ONLY RAILWAY_DEV_ENVIRONMENT_ID, never
// the subcommand's own argument. With RAILWAY_DEV_ENVIRONMENT_ID set (the normal
// CI state — dev-env.yml:135) and no argument, the persistent-environment compare
// `"$env_id" = "$RAILWAY_DEV_ENVIRONMENT_ID"` is `"" = "<real-id>"` — FALSE — so a
// script with no explicit empty-argument guard PROCEEDS instead of refusing. That
// is the dangerous direction: CI would write APPROVALS_ENFORCED against whatever
// the caller's bug pointed the argument at.
//
// The fix, copied from cmd_reconcile_fork (scripts/ci/railway-env.sh:1747-1752):
// an explicit `[ -z "$env_id" ]` guard BEFORE the compare, exiting 2 with a usage
// message. This drives that guard directly and checks the run never falls through
// into require_env or require_source_env.
//
// KILLS: an empty-argument guard omitted, or placed after the persistent-env
// compare instead of before it.
func TestSetApprovalsEnforcedEmptyArgumentExitsBeforeAnyCompare(t *testing.T) {
	stdout, stderr, code := runApprovalsCmd(t, nil, []string{
		"RAILWAY_DEV_ENVIRONMENT_ID=" + persistentEnvironmentID,
	}, "RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID")

	if code != 2 {
		t.Errorf("exit code = %d, want 2 (the usage-guard exit code cmd_reconcile_fork and cmd_ensure_environment both use); stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "usage: railway-env.sh set-approvals-enforced") {
		t.Errorf("stdout does not carry a usage message for the new subcommand; stdout = %q", stdout)
	}
	if strings.Contains(stdout, "RAILWAY_API_TOKEN") || strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("output mentions RAILWAY_API_TOKEN — the run fell through past the usage guard into require_env; stdout = %q, stderr = %q", stdout, stderr)
	}
	if strings.Contains(stdout, "RAILWAY_DEV_ENVIRONMENT_ID is not set") || strings.Contains(stderr, "RAILWAY_DEV_ENVIRONMENT_ID is not set") {
		t.Errorf("output mentions require_source_env's message — the run fell through past the usage guard into require_source_env; stdout = %q, stderr = %q", stdout, stderr)
	}
}

// TestSetApprovalsEnforcedRefusesThePersistentEnvironment drives the subcommand
// with the REAL persistent environment id and asserts the refusal fires before
// any network call — RAILWAY_API_TOKEN and RAILWAY_PROJECT_ID are both unset, so
// a run that fell through to require_env would fail on THAT message instead of
// the refusal's.
//
// KILLS: the refusal omitted; the refusal placed after require_env, which would
// make "no" require a live token to say.
func TestSetApprovalsEnforcedRefusesThePersistentEnvironment(t *testing.T) {
	stdout, stderr, code := runApprovalsCmd(t, []string{persistentEnvironmentID}, []string{
		"RAILWAY_DEV_ENVIRONMENT_ID=" + persistentEnvironmentID,
	}, "RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID")

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero: turning enforcement on in the persistent environment is an OPERATOR action (APPR-14-10) — no CI path may take it; stdout = %q, stderr = %q", stdout, stderr)
	}
	if !strings.Contains(stdout, "::error::") {
		t.Errorf("stdout carries no ::error:: annotation — the refusal would be invisible in the Actions UI; stdout = %q", stdout)
	}
	if !strings.Contains(stdout, persistentEnvironmentID) {
		t.Errorf("stdout does not name the refused id %q; stdout = %q", persistentEnvironmentID, stdout)
	}
	if strings.Contains(stdout, "RAILWAY_API_TOKEN") || strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("output mentions RAILWAY_API_TOKEN — the refusal did not fire before require_env; stdout = %q, stderr = %q", stdout, stderr)
	}
}

// TestSetApprovalsEnforcedSelfTestNeedsNoToken mirrors
// TestAssertDBDSNsSelfTestNeedsNoToken (railway_env_dsn_test.go T2-4): --self-test
// must short-circuit before require_env, so it needs no token and no network, and
// must run on a fork PR (which receives no secrets by design).
//
// The fixture-count assertion operationalises the story's AC #6 ("states its
// fixture count") without pinning a specific number — the fixture set is Stage
// 3's decision, not this spec's.
//
// KILLS: require_env moved ahead of the --self-test short-circuit; an empty
// fixture list reading as success (domain_self_test's closing-line convention,
// scripts/ci/railway-env.sh:1042).
func TestSetApprovalsEnforcedSelfTestNeedsNoToken(t *testing.T) {
	stdout, stderr, code := runApprovalsCmd(t, []string{"--self-test"}, nil, "RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID")

	if code != 0 {
		t.Errorf("exit code = %d, want 0 with RAILWAY_API_TOKEN and RAILWAY_PROJECT_ID unset: --self-test must short-circuit before require_env; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "RAILWAY_API_TOKEN") || strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("the self-test reached require_env — it must short-circuit before it; stdout = %q, stderr = %q", stdout, stderr)
	}
	if !fixtureCountPattern.MatchString(stdout) {
		t.Errorf("stdout does not state a fixture count (AC #6); stdout = %q", stdout)
	}
}

// TestSetApprovalsEnforcedSelfTestNeverEchoesTheToken sets RAILWAY_API_TOKEN to a
// sentinel the self-test has no legitimate reason to touch (AC #6: it needs no
// token at all) and checks the sentinel never reaches stdout or stderr — the same
// credential-hygiene-at-the-wiring-layer property T2-2 pins for assert-db-dsns.
//
// KILLS: a debug echo of the inherited environment; a self-test that reads the
// token "just in case" and reports it in a diagnostic line.
func TestSetApprovalsEnforcedSelfTestNeverEchoesTheToken(t *testing.T) {
	const sentinel = "sentinel-token-do-not-leak-3f9a"
	stdout, stderr, code := runApprovalsCmd(t, []string{"--self-test"}, []string{
		"RAILWAY_API_TOKEN=" + sentinel,
	})

	if code != 0 {
		t.Errorf("exit code = %d, want 0: a token being present must not change --self-test's outcome; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, sentinel) {
		t.Errorf("stdout leaked the token sentinel %q; stdout = %q", sentinel, stdout)
	}
	if strings.Contains(stderr, sentinel) {
		t.Errorf("stderr leaked the token sentinel %q; stderr = %q", sentinel, stderr)
	}
}

// cmdSetApprovalsEnforcedBody extracts the body of cmd_set_approvals_enforced
// from scripts/ci/railway-env.sh: everything between its opening brace and the
// next line that is exactly `}` at column 0 — the convention every function in
// this file follows. Fatal, not a silent miss, when the function is absent: the
// two source-text tests below would otherwise pass vacuously against a function
// that does not exist.
func cmdSetApprovalsEnforcedBody(t *testing.T) string {
	t.Helper()
	path := railwayEnvScript(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	start := regexp.MustCompile(`(?m)^cmd_set_approvals_enforced\(\)\s*\{`).FindStringIndex(string(content))
	if start == nil {
		t.Fatalf("cmd_set_approvals_enforced() not found in %s — not implemented yet", path)
	}

	rest := string(content)[start[1]:]
	end := regexp.MustCompile(`(?m)^\}$`).FindStringIndex(rest)
	if end == nil {
		t.Fatalf("could not find the closing brace of cmd_set_approvals_enforced in %s", path)
	}
	return rest[:end[0]]
}

// TestSetApprovalsEnforcedSourceResolvesInvoiceByNameNotUUID is a SOURCE-TEXT
// assertion, not a behavioural one — it proves the function body mentions
// SETTLE_QUERY (the shared by-name service resolver, :648-650) and the literal
// service name "invoice"; it proves nothing about what the function does with a
// live response. TestRailwayEnvScriptHardcodesNoServiceUUIDs
// (railway_env_dsn_test.go) is the whole-file regression guard against a
// hardcoded UUID; this pins the positive half — that the function reaches for the
// by-name query at all — which a whole-file scan cannot.
//
// KILLS: a hand-rolled GraphQL query that bypasses SETTLE_QUERY; a hardcoded
// service name that is not "invoice".
func TestSetApprovalsEnforcedSourceResolvesInvoiceByNameNotUUID(t *testing.T) {
	body := cmdSetApprovalsEnforcedBody(t)

	if !strings.Contains(body, "SETTLE_QUERY") {
		t.Errorf("cmd_set_approvals_enforced does not reference SETTLE_QUERY — AC #2 requires resolving the invoice service by NAME, from the same query assert-db-dsns already uses")
	}
	if !strings.Contains(body, `"invoice"`) {
		t.Errorf(`cmd_set_approvals_enforced does not reference the literal service name "invoice"`)
	}
}

// TestSetApprovalsEnforcedSourceCallsVerifyAfterUpsert is a SOURCE-TEXT
// assertion. It cannot observe whether verify_variable's independent re-read
// actually catches a real mismatch — that needs a live GraphQL round-trip, which
// this file refuses to mock (see the package comment). It can only prove the call
// sites exist, in the right order.
//
// KILLS: upsert_variable called with no matching verify_variable; verify_variable
// called before upsert_variable, which would verify the pre-write value.
func TestSetApprovalsEnforcedSourceCallsVerifyAfterUpsert(t *testing.T) {
	body := cmdSetApprovalsEnforcedBody(t)

	upsertAt := strings.Index(body, "upsert_variable")
	verifyAt := strings.Index(body, "verify_variable")
	if upsertAt == -1 {
		t.Errorf("cmd_set_approvals_enforced never calls upsert_variable")
	}
	if verifyAt == -1 {
		t.Errorf("cmd_set_approvals_enforced never calls verify_variable — \"the mutation's own response is never the evidence\" (scripts/ci/railway-env.sh:1203)")
	}
	if upsertAt != -1 && verifyAt != -1 && verifyAt < upsertAt {
		t.Errorf("verify_variable is called BEFORE upsert_variable — it would verify the pre-write value")
	}
	if !strings.Contains(body, "APPROVALS_ENFORCED") {
		t.Errorf(`cmd_set_approvals_enforced never mentions the literal variable name "APPROVALS_ENFORCED"`)
	}
}

// TestDevEnvYmlWiresApprovalsEnforcedIntoPrepareEnv is a SOURCE-TEXT assertion
// over the raw YAML, not a parse — this repo has no direct YAML dependency
// (gopkg.in/yaml.v3 is indirect only), and main_test.go:104 already establishes
// the precedent of grepping repo source from a Go test. It proves the WIRING is
// present — one call site, PR-gated, inside prepare-env, before deploy-gateway —
// not that the step WORKS. WEAK BY DESIGN: the only oracle for "works" is a green
// prepare-env run on a real PR (see the package comment's NOT COVERED HERE
// section).
//
// KILLS: the step omitted; the step gated on the wrong event; the step placed
// outside prepare-env (e.g. merged into a neighbouring job, or added after
// deploy-gateway where it would run with no environment left to target).
func TestDevEnvYmlWiresApprovalsEnforcedIntoPrepareEnv(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "dev-env.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(raw)

	callSites := regexp.MustCompile(`set-approvals-enforced`).FindAllStringIndex(content, -1)
	if len(callSites) == 0 {
		t.Fatalf("%s never mentions set-approvals-enforced — the prepare-env step is not wired up yet", path)
	}
	if len(callSites) > 1 {
		t.Fatalf("%s mentions set-approvals-enforced %d times, want exactly 1 — two call sites is two places to keep in sync", path, len(callSites))
	}
	callAt := callSites[0][0]

	prepareEnvAt := strings.Index(content, "\n  prepare-env:")
	deployGatewayAt := strings.Index(content, "\n  deploy-gateway:")
	if prepareEnvAt == -1 || deployGatewayAt == -1 {
		t.Fatalf("could not locate the prepare-env: or deploy-gateway: job headers in %s — this test's anchors have drifted", path)
	}
	if callAt < prepareEnvAt || callAt > deployGatewayAt {
		t.Errorf("set-approvals-enforced is called at byte offset %d, outside the prepare-env job (%d-%d) — it must run as a step of prepare-env, not a neighbouring job", callAt, prepareEnvAt, deployGatewayAt)
	}

	// Scoped to the CURRENT step only: from its own nearest preceding `- name:`
	// line up to the call site. A fixed byte window risks bleeding into the
	// PRECEDING step's own `if:` line (reconcile-urls, :502, carries the exact
	// same gate) and passing vacuously against the wrong step.
	nameLines := regexp.MustCompile(`(?m)^\s*- name:`).FindAllStringIndex(content[:callAt], -1)
	if len(nameLines) == 0 {
		t.Fatalf("no '- name:' step header found before the set-approvals-enforced call site in %s", path)
	}
	windowStart := nameLines[len(nameLines)-1][0]
	windowEnd := callAt + 200
	if windowEnd > len(content) {
		windowEnd = len(content)
	}
	window := content[windowStart:windowEnd]
	if !strings.Contains(window, "if: github.event_name == 'pull_request'") {
		t.Errorf("the set-approvals-enforced step is not gated `if: github.event_name == 'pull_request'` — on push/dispatch the target is the persistent environment, and this is the seam's second line of defence alongside the subcommand's own refusal; window = %q", window)
	}

	needsMatch := regexp.MustCompile(`(?m)^  deploy-gateway:[\s\S]{0,400}?needs:\s*\[([^\]]*)\]`).FindStringSubmatch(content)
	if needsMatch == nil {
		t.Fatalf("could not find deploy-gateway's needs: list in %s", path)
	}
	if !strings.Contains(needsMatch[1], "prepare-env") {
		t.Errorf("deploy-gateway's needs: list %q does not name prepare-env — the gate that stops the whole deploy on a failed prepare-env step (needs.prepare-env.result == 'success') would not apply", needsMatch[1])
	}
}

// TestRailwayInvariantsYmlWiresApprovalsEnforcedSelfTest is a SOURCE-TEXT
// assertion, same technique as TestDevEnvYmlWiresApprovalsEnforcedIntoPrepareEnv.
// railway-invariants.yml is where this belongs, not dev-env.yml's `go` job or
// ci.yml: both are paths-filtered on a list without scripts/**, so a PR that
// only edits railway-env.sh would run neither — the comment at
// railway-invariants.yml:63-65 states this for domain-selection-self-test and
// it applies identically here. This workflow has no paths filter.
//
// WEAK BY DESIGN, same as the dev-env.yml wiring test: it proves the job is
// wired to the right subcommand, not that it runs or passes in Actions.
//
// Checked first: nothing in this package pins domain-selection-self-test's
// own wiring today (grepped this file for "domain-selection-self-test" and
// "railway-invariants" before writing this test — no hit). That sibling job
// could be renamed or deleted with no test noticing. This test is added
// anyway for the NEW job rather than left unpinned to match: an unpinned CI
// job is exactly the silent-deletion risk this subtask exists to close, and
// matching an existing gap is not a reason to add a second one.
//
// KILLS: the job renamed away from `set-approvals-enforced --self-test`; the
// job removed; the run line pointing at a different subcommand.
//
// Matches the `run:` line only, not the step `name:` — that line echoes the
// same text on purpose, following domain-selection-self-test's own step-name
// convention ("Run select-domain --self-test"), so both lines legitimately
// carry the string and only the run line is the thing that actually executes.
func TestRailwayInvariantsYmlWiresApprovalsEnforcedSelfTest(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "railway-invariants.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(raw)

	callSites := regexp.MustCompile(`run: bash scripts/ci/railway-env\.sh set-approvals-enforced --self-test`).FindAllStringIndex(content, -1)
	if len(callSites) == 0 {
		t.Fatalf("%s never runs `set-approvals-enforced --self-test` — the self-test job is not wired up", path)
	}
	if len(callSites) > 1 {
		t.Fatalf("%s runs `set-approvals-enforced --self-test` %d times, want exactly 1 — two call sites is two places to keep in sync", path, len(callSites))
	}

	jobAt := strings.Index(content, "\n  approvals-enforced-self-test:")
	if jobAt == -1 {
		t.Fatalf("%s has no `approvals-enforced-self-test:` job header", path)
	}
	if callSites[0][0] < jobAt {
		t.Errorf("set-approvals-enforced --self-test is called at byte %d, before its own job header at byte %d — it belongs to a different job", callSites[0][0], jobAt)
	}
}
