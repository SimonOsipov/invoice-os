// railway_env_ai_fake_test.go pins the RED contract of the not-yet-implemented
// `scripts/ci/railway-env.sh set-ai-fake` subcommand, `ai_key_verdict` and
// `ai_fake_self_test` (AIR-02-04, task-1077). T01-T08 are expected to FAIL
// today; T09 is the no-regression oracle for the shared-helper label
// argument and must stay green throughout.
//
// NOT COVERED HERE: the live GraphQL write, verify_variable's mismatch
// branch on OPENROUTER_API_KEY (deliberately never called — an absent and an
// empty key both read back as "" through it, so a want="" compare would pass
// vacuously; ai_key_verdict's has() read is the discriminating check), and
// whether Railway's variableUpsert mutation accepts an empty string value.
// The only oracle for those is a green prepare-env run on a ready PR.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// runAIFakeCmd execs `railway-env.sh set-ai-fake <args...>`. Deliberate copy
// of runApprovalsCmd (same env-filter loop) rather than a shared helper — the
// two differ by subcommand name and refactoring a green file is out of scope
// here.
func runAIFakeCmd(t *testing.T, args []string, extraEnv []string, unsetVars ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmdArgs := append([]string{railwayEnvScript(t), "set-ai-fake"}, args...)
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

var aiFakeFixtureCountPattern = regexp.MustCompile(`AI fake self-test: (\d+) fixtures passed`)

// T01: --self-test must short-circuit before require_env/require_source_env
// and state its fixture count (AC #1).
//
// KILLS: require_env/require_source_env moved ahead of the self-test
// short-circuit; an empty fixture list reading as success.
func TestSetAIFakeSelfTestPassesWithoutAToken(t *testing.T) {
	stdout, stderr, code := runAIFakeCmd(t, []string{"--self-test"}, nil,
		"RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID", "RAILWAY_DEV_ENVIRONMENT_ID")

	if code != 0 {
		t.Errorf("exit code = %d, want 0 with no token, project id or dev environment id set; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	m := aiFakeFixtureCountPattern.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("stdout does not state a fixture count in the form %q; stdout = %q", "AI fake self-test: N fixtures passed", stdout)
	}
	count, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("could not parse fixture count %q: %v", m[1], err)
	}
	if count < 7 {
		t.Errorf("fixture count = %d, want >= 7 (F1-F8, F10 reusing a shape)", count)
	}
	if strings.Contains(stdout, "RAILWAY_API_TOKEN") || strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("output mentions RAILWAY_API_TOKEN — the self-test reached require_env; stdout = %q, stderr = %q", stdout, stderr)
	}
	if strings.Contains(stdout, "RAILWAY_DEV_ENVIRONMENT_ID is not set") || strings.Contains(stderr, "RAILWAY_DEV_ENVIRONMENT_ID is not set") {
		t.Errorf("output mentions require_source_env's message — the self-test reached require_source_env; stdout = %q, stderr = %q", stdout, stderr)
	}
}

// T02: credential hygiene at the self-test's own reporting layer (AC #1: a
// fixture's key value must never be printed).
//
// KILLS: a diagnostic echo of a fixture map; a refusal message that
// interpolates the value.
func TestSetAIFakeSelfTestNeverPrintsAKeyValue(t *testing.T) {
	const sentinel = "sentinel-token-do-not-leak-7c1e"
	stdout, stderr, code := runAIFakeCmd(t, []string{"--self-test"}, []string{
		"RAILWAY_API_TOKEN=" + sentinel,
	})

	if code != 0 {
		t.Errorf("exit code = %d, want 0: a token being present must not change --self-test's outcome; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	for _, leak := range []string{sentinel, "sk-or-v1-fixture-not-a-real-key", "pw-fixture"} {
		if strings.Contains(stdout, leak) {
			t.Errorf("stdout leaked %q; stdout = %q", leak, stdout)
		}
		if strings.Contains(stderr, leak) {
			t.Errorf("stderr leaked %q; stderr = %q", leak, stderr)
		}
	}
}

// T03: an empty argument must exit 2 with a usage message naming
// set-ai-fake specifically (AC #3). The dispatch catch-all's generic usage
// line also exits 2 today, so the exit code alone would pass vacuously
// before the subcommand is wired — the usage-text assertion is what makes
// this RED now.
//
// KILLS: the usage guard omitted, or placed after the persistent-environment
// compare where an empty argument compares false and PASSES.
func TestSetAIFakeEmptyArgumentExitsBeforeAnyCompare(t *testing.T) {
	stdout, stderr, code := runAIFakeCmd(t, nil, []string{
		"RAILWAY_DEV_ENVIRONMENT_ID=" + persistentEnvironmentID,
	}, "RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID")

	if code != 2 {
		t.Errorf("exit code = %d, want 2; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "usage: railway-env.sh set-ai-fake") {
		t.Errorf("stdout does not carry a usage message naming set-ai-fake specifically (the dispatch catch-all's generic usage line does not satisfy this); stdout = %q", stdout)
	}
	if strings.Contains(stdout, "RAILWAY_API_TOKEN") || strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("output mentions RAILWAY_API_TOKEN — the run fell through past the usage guard into require_env; stdout = %q, stderr = %q", stdout, stderr)
	}
	if strings.Contains(stdout, "RAILWAY_DEV_ENVIRONMENT_ID is not set") || strings.Contains(stderr, "RAILWAY_DEV_ENVIRONMENT_ID is not set") {
		t.Errorf("output mentions require_source_env's message — the run fell through past the usage guard; stdout = %q, stderr = %q", stdout, stderr)
	}
}

// T04: the persistent environment must be refused with ::error:: before
// require_env (AC #4).
//
// KILLS: the refusal omitted; the refusal placed after require_env, which
// would make "no" need a live token to say.
func TestSetAIFakeRefusesThePersistentEnvironment(t *testing.T) {
	stdout, stderr, code := runAIFakeCmd(t, []string{persistentEnvironmentID}, []string{
		"RAILWAY_DEV_ENVIRONMENT_ID=" + persistentEnvironmentID,
	}, "RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID")

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero: forcing AI fake mode in the persistent environment must be refused; stdout = %q, stderr = %q", stdout, stderr)
	}
	if !strings.Contains(stdout, "::error::") {
		t.Errorf("stdout carries no ::error:: annotation; stdout = %q", stdout)
	}
	if !strings.Contains(stdout, persistentEnvironmentID) {
		t.Errorf("stdout does not name the refused id %q; stdout = %q", persistentEnvironmentID, stdout)
	}
	if strings.Contains(stdout, "RAILWAY_API_TOKEN") || strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("output mentions RAILWAY_API_TOKEN — the refusal did not fire before require_env; stdout = %q, stderr = %q", stdout, stderr)
	}
}

// T05: cmd_set_ai_fake must set both services, verify AI_FAKE with a fresh
// read, and run the key verdict on a fresh read too (AC #5).
//
// KILLS: one service only; the verdict run on the mutation's own response
// instead of a fresh read; verify before upsert; a SETTLE_QUERY per service.
func TestSetAIFakeBodySetsAndChecksBothServices(t *testing.T) {
	body := strings.Join(shellFunctionBody(t, "cmd_set_ai_fake"), "\n")

	required := []string{
		"for svc in submission invoice",
		"service_id_by_name",
		`upsert_variable "$env_id" "$svc_id" "$svc" AI_FAKE true`,
		`upsert_variable "$env_id" "$svc_id" "$svc" OPENROUTER_API_KEY ""`,
		"verify_variable",
		"ai_key_verdict",
	}
	for _, r := range required {
		if !strings.Contains(body, r) {
			t.Errorf("cmd_set_ai_fake does not contain %q", r)
		}
	}

	upsertAt := strings.Index(body, "upsert_variable")
	verifyAt := strings.Index(body, "verify_variable")
	if upsertAt >= 0 && verifyAt >= 0 && verifyAt < upsertAt {
		t.Errorf("verify_variable is called BEFORE upsert_variable — it would verify the pre-write value")
	}

	if count := strings.Count(body, "SETTLE_QUERY"); count != 1 {
		t.Errorf("cmd_set_ai_fake references SETTLE_QUERY %d times, want exactly 1 — one settle call shared by both services in the loop", count)
	}

	serviceVarsAt := strings.Index(body, "SERVICE_VARIABLES_QUERY")
	verdictAt := strings.Index(body, "ai_key_verdict")
	if serviceVarsAt >= 0 && verdictAt >= 0 && verdictAt < serviceVarsAt {
		t.Errorf("ai_key_verdict is called BEFORE SERVICE_VARIABLES_QUERY — the verdict must run on a FRESH read, not the mutation's own response")
	}
}

// T06: ai_fake_self_test must cover every verdict shape ai_key_verdict can
// produce (AC #2).
//
// KILLS: a fixture silently dropped while the printed count is left
// unchanged.
func TestAIFakeSelfTestCoversEveryVerdictShape(t *testing.T) {
	body := strings.Join(shellFunctionBody(t, "ai_fake_self_test"), "\n")

	needles := []string{
		`"AI_FAKE":"true"}}}`,        // F1: absent key passes
		`"OPENROUTER_API_KEY":""`,    // F2/F10: empty key passes
		"sk-or-v1-fixture",           // F3: a present key refuses
		`\t`,                         // F4: a whitespace-only key refuses
		"secrets.OPENROUTER_API_KEY", // F5: an unrendered reference refuses
		`"variables":null`,           // F6: a null variables map refuses
		`"errors"`,                   // F7: a GraphQL error refuses
		"'[]'",                       // F8: a bare array refuses
	}
	for _, n := range needles {
		if !strings.Contains(body, n) {
			t.Errorf("ai_fake_self_test does not cover the fixture shape %q", n)
		}
	}

	calls := regexp.MustCompile(`(?m)^\s*ai_expect_(pass|refusal)\s`).FindAllString(body, -1)
	if len(calls) == 0 {
		t.Fatalf("ai_fake_self_test has no ai_expect_pass/ai_expect_refusal call line — a dropped fixture would leave the printed count unchanged with nothing to notice it")
	}
	if len(calls) < 7 {
		t.Errorf("ai_fake_self_test has %d ai_expect_pass/ai_expect_refusal call lines, want at least 7 (F1-F8, F10 reusing a shape)", len(calls))
	}
}

// T07: dev-env.yml must run set-ai-fake exactly once, inside prepare-env,
// PR-only, with no continue-on-error (AC #6).
//
// WEAK BY DESIGN, same as the approvals wiring test: proves the wiring, not
// the write.
//
// KILLS: the step omitted; the step gated on the wrong event; a silent
// continue-on-error swallowing a failed write.
func TestDevEnvYmlWiresSetAIFakeIntoPrepareEnv(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "dev-env.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(raw)

	callSites := regexp.MustCompile(`set-ai-fake`).FindAllStringIndex(content, -1)
	if len(callSites) == 0 {
		t.Fatalf("%s never mentions set-ai-fake — the prepare-env step is not wired up yet", path)
	}
	if len(callSites) > 1 {
		t.Fatalf("%s mentions set-ai-fake %d times, want exactly 1 — two call sites is two places to keep in sync", path, len(callSites))
	}
	callAt := callSites[0][0]

	prepareEnvAt := strings.Index(content, "\n  prepare-env:")
	deployGatewayAt := strings.Index(content, "\n  deploy-gateway:")
	if prepareEnvAt == -1 || deployGatewayAt == -1 {
		t.Fatalf("could not locate the prepare-env: or deploy-gateway: job headers in %s — this test's anchors have drifted", path)
	}
	if callAt < prepareEnvAt || callAt > deployGatewayAt {
		t.Errorf("set-ai-fake is called at byte offset %d, outside the prepare-env job (%d-%d)", callAt, prepareEnvAt, deployGatewayAt)
	}

	// Scoped to the CURRENT step only, same reasoning as the approvals wiring
	// test: a fixed byte window risks bleeding into the PRECEDING step's own
	// if: line.
	nameLines := regexp.MustCompile(`(?m)^\s*- name:`).FindAllStringIndex(content[:callAt], -1)
	if len(nameLines) == 0 {
		t.Fatalf("no '- name:' step header found before the set-ai-fake call site in %s", path)
	}
	windowStart := nameLines[len(nameLines)-1][0]
	windowEnd := callAt + 200
	if windowEnd > len(content) {
		windowEnd = len(content)
	}
	window := content[windowStart:windowEnd]
	if !strings.Contains(window, "if: github.event_name == 'pull_request'") {
		t.Errorf("the set-ai-fake step is not gated `if: github.event_name == 'pull_request'`; window = %q", window)
	}
	if strings.Contains(window, "continue-on-error") {
		t.Errorf("the set-ai-fake step carries continue-on-error — a silent failure would leave a PR environment holding a usable key; window = %q", window)
	}
}

// T08: railway-invariants.yml must run set-ai-fake --self-test exactly once,
// in job ai-fake-self-test (AC #7).
//
// KILLS: the job renamed away; the job removed; the run line pointing at a
// different subcommand.
func TestRailwayInvariantsYmlWiresSetAIFakeSelfTest(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "railway-invariants.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(raw)

	callSites := regexp.MustCompile(`run: bash scripts/ci/railway-env\.sh set-ai-fake --self-test`).FindAllStringIndex(content, -1)
	if len(callSites) == 0 {
		t.Fatalf("%s never runs `set-ai-fake --self-test` — the self-test job is not wired up", path)
	}
	if len(callSites) > 1 {
		t.Fatalf("%s runs `set-ai-fake --self-test` %d times, want exactly 1 — two call sites is two places to keep in sync", path, len(callSites))
	}

	jobAt := strings.Index(content, "\n  ai-fake-self-test:")
	if jobAt == -1 {
		t.Fatalf("%s has no `ai-fake-self-test:` job header", path)
	}
	if callSites[0][0] < jobAt {
		t.Errorf("set-ai-fake --self-test is called at byte %d, before its own job header at byte %d — it belongs to a different job", callSites[0][0], jobAt)
	}
}

// T09: no-regression oracle for the shared-helper label argument added to
// service_id_by_name and assert_environment_is_ephemeral — the label
// default must keep every approvals message and the approvals fixture count
// byte-identical. Reuses runApprovalsCmd, already declared in
// railway_env_approvals_test.go.
func TestApprovalsSelfTestUnchangedByTheLabelArgument(t *testing.T) {
	stdout, _, code := runApprovalsCmd(t, []string{"--self-test"}, nil, "RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout = %q", code, stdout)
	}
	const want = "Approvals enforcement self-test: 11 fixtures passed, no token read, no network call."
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout does not contain %q byte-for-byte; stdout = %q", want, stdout)
	}
}
