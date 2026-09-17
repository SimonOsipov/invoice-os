// railway_env_ai_fake_test.go pins the contract of
// `scripts/ci/railway-env.sh set-ai-fake`, `ai_key_verdict` and
// `ai_fake_self_test`. T09 is the no-regression oracle for the shared-helper
// label argument added to service_id_by_name and
// assert_environment_is_ephemeral.
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
	for _, leak := range []string{sentinel, "a-present-key-fixture", "pw-fixture"} {
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
		// The second guard, independent of the literal id compare: it survives a
		// drifted RAILWAY_DEV_ENVIRONMENT_ID.
		`assert_environment_is_ephemeral "$env_id" AI_FAKE`,
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
		"a-present-key-fixture",      // F3: a present key refuses
		`\t`,                         // F4: a whitespace-only key refuses
		"secrets.OPENROUTER_API_KEY", // F5: an unrendered reference refuses
		`"variables":null`,           // F6: a null variables map refuses
		`"errors"`,                   // F7: a GraphQL error refuses
		"'[]'",                       // F8: a bare array refuses
		`[ "$msg_f6" = "$msg_f3" ]`,  // F9: the two refusals must differ
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

// shellFunctionSource returns bash text redefining the named railway-env.sh
// functions, so a test can drive one directly without reaching the script's
// own `case` dispatch. Built on shellFunctionBody, which t.Fatalf's when a
// name is missing, so a rename cannot leave the caller asserting on nothing.
func shellFunctionSource(t *testing.T, names ...string) string {
	t.Helper()
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + "() {\n")
		b.WriteString(strings.Join(shellFunctionBody(t, n), "\n"))
		b.WriteString("\n}\n")
	}
	return b.String()
}

// runBashScript runs a bash snippet with every RAILWAY_* variable stripped, so
// no snippet below can reach Railway even if a guard is mutated away.
func runBashScript(t *testing.T, script string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.Command("bash", append([]string{"-c", script, "bash"}, args...)...)
	cmd.Dir = repoRoot(t)

	var filtered []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "RAILWAY_") {
			continue
		}
		filtered = append(filtered, kv)
	}
	cmd.Env = filtered

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running a bash snippet: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

const verdictDriver = `
rc=0
out=$(ai_key_verdict "$1" "submission" 2>&1) || rc=$?
printf '%s' "$out"
exit "$rc"
`

// T10: ai_key_verdict's full truth table, including response shapes no
// self-test fixture covers, plus the no-leak property on EVERY branch —
// ai_fake_self_test only carries a leak needle on three of its fixtures.
func TestAIKeyVerdictTruthTableIncludingShapesNoFixtureCovers(t *testing.T) {
	prelude := "set -uo pipefail\n" + shellFunctionSource(t, "ai_key_verdict")

	cases := []struct {
		name     string
		json     string
		wantPass bool
		// marker, when non-empty, appears in the fixture and must NOT appear
		// in the verdict's output.
		marker string
	}{
		{"absent key", `{"data":{"variables":{"AI_FAKE":"true"}}}`, true, ""},
		{"empty key", `{"data":{"variables":{"OPENROUTER_API_KEY":""}}}`, true, ""},
		{"non-empty key", `{"data":{"variables":{"OPENROUTER_API_KEY":"mk-9f2a-plain"}}}`, false, "mk-9f2a-plain"},
		{"whitespace key", `{"data":{"variables":{"OPENROUTER_API_KEY":" \t "}}}`, false, ""},
		// A JSON null is not the empty string, so it must NOT read as absent: a
		// `// ""` style read would silently accept it.
		{"json null key", `{"data":{"variables":{"OPENROUTER_API_KEY":null}}}`, false, ""},
		{"numeric key", `{"data":{"variables":{"OPENROUTER_API_KEY":91827364}}}`, false, "91827364"},
		{"object key", `{"data":{"variables":{"OPENROUTER_API_KEY":{"v":"mk-9f2a-nested"}}}}`, false, "mk-9f2a-nested"},
		{"array key", `{"data":{"variables":{"OPENROUTER_API_KEY":["mk-9f2a-array"]}}}`, false, "mk-9f2a-array"},
		{"boolean key", `{"data":{"variables":{"OPENROUTER_API_KEY":false}}}`, false, ""},
		// The Go client reads the exact name, so a differently-cased variable is
		// genuinely not a usable key. Asserted so the decision is visible.
		{"lower-case key name", `{"data":{"variables":{"openrouter_api_key":"mk-9f2a-lower"}}}`, true, "mk-9f2a-lower"},
		{"top-level null", `null`, false, ""},
		{"top-level string", `"mk-9f2a-string"`, false, "mk-9f2a-string"},
		{"top-level number", `42`, false, ""},
		{"top-level array", `[]`, false, ""},
		{"empty object", `{}`, false, ""},
		{"data without variables", `{"data":{"other":"mk-9f2a-data"}}`, false, "mk-9f2a-data"},
		{"data null", `{"data":null}`, false, ""},
		{"variables null", `{"data":{"variables":null}}`, false, ""},
		{"variables is a string", `{"data":{"variables":"mk-9f2a-str"}}`, false, "mk-9f2a-str"},
		{"variables is an array", `{"data":{"variables":[]}}`, false, ""},
		// An EMPTY errors array is not an error, so it must not short-circuit
		// the variables read — but it must not pass with a key set either.
		{"empty errors array only", `{"errors":[]}`, false, ""},
		{"empty errors array with a live key", `{"errors":[],"data":{"variables":{"OPENROUTER_API_KEY":"mk-9f2a-both"}}}`, false, "mk-9f2a-both"},
		{"populated errors", `{"errors":[{"message":"mk-9f2a-err"}],"data":null}`, false, "mk-9f2a-err"},
		{"not json at all", `mk-9f2a-garbage{`, false, "mk-9f2a-garbage"},
		{"empty input", ``, false, ""},
		{"two json documents", `{"data":{"variables":{}}} {"data":{"variables":{"OPENROUTER_API_KEY":"mk-9f2a-two"}}}`, false, "mk-9f2a-two"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runBashScript(t, prelude+verdictDriver, tc.json)
			if tc.wantPass && code != 0 {
				t.Errorf("exit code = %d, want 0 (no usable key); output = %q", code, stdout)
			}
			if !tc.wantPass && code == 0 {
				t.Errorf("exit code = 0, want non-zero: this shape is not evidence that OPENROUTER_API_KEY is unset; output = %q", stdout)
			}
			if !tc.wantPass && !strings.Contains(stdout, "::error::") {
				t.Errorf("a refusal carries no ::error:: annotation, so the job log would not surface it; output = %q", stdout)
			}
			if tc.marker != "" {
				if strings.Contains(stdout, tc.marker) {
					t.Errorf("the verdict printed %q out of the rendered variable map, which carries live credentials; output = %q", tc.marker, stdout)
				}
				if strings.Contains(stderr, tc.marker) {
					t.Errorf("stderr printed %q out of the rendered variable map; stderr = %q", tc.marker, stderr)
				}
			}
		})
	}
}

// T11: negative controls for ai_fake_self_test's own machinery. Without these
// the failures counter and both leak needles can be removed with every test
// still green — the self-test would print "10 fixtures passed" on a suite that
// failed every fixture.
func TestAIFakeSelfTestNegativeControlsAreLive(t *testing.T) {
	funcs := shellFunctionSource(t,
		"ai_key_verdict", "ai_expect_pass", "ai_expect_refusal", "ai_fake_self_test")
	prelude := "set -uo pipefail\n" + funcs

	t.Run("failures counter gates the exit", func(t *testing.T) {
		stdout, stderr, code := runBashScript(t, prelude+`
ai_key_verdict() { return 0; }
ai_fake_self_test
`)
		if code == 0 {
			t.Errorf("ai_fake_self_test exited 0 with a verdict that never refuses — the failures counter does not gate the exit; stdout = %q, stderr = %q", stdout, stderr)
		}
	})

	t.Run("refusal leak needle counts a failure", func(t *testing.T) {
		stdout, _, _ := runBashScript(t, prelude+`
ai_key_verdict() { echo "mk-9f2a-leak"; return 1; }
failures=0
ai_expect_refusal FX '{}' "mk-9f2a-leak" >/dev/null 2>&1
printf '%s' "$failures"
`)
		if stdout != "1" {
			t.Errorf("failures = %q, want \"1\": ai_expect_refusal did not notice the leaked needle", stdout)
		}
	})

	t.Run("pass leak needle counts a failure", func(t *testing.T) {
		stdout, _, _ := runBashScript(t, prelude+`
ai_key_verdict() { echo "mk-9f2a-leak"; return 0; }
failures=0
ai_expect_pass FX '{}' "mk-9f2a-leak" >/dev/null 2>&1
printf '%s' "$failures"
`)
		if stdout != "1" {
			t.Errorf("failures = %q, want \"1\": ai_expect_pass did not notice the leaked needle", stdout)
		}
	})

	// Must STAY green: a needle check that fires on clean output would make
	// every fixture fail and the suite useless in the other direction.
	t.Run("a clean refusal counts nothing", func(t *testing.T) {
		stdout, _, _ := runBashScript(t, prelude+`
ai_key_verdict() { echo "no needle here"; return 1; }
failures=0
ai_expect_refusal FX '{}' "mk-9f2a-leak" >/dev/null 2>&1
printf '%s' "$failures"
`)
		if stdout != "0" {
			t.Errorf("failures = %q, want \"0\": the needle check fired on output that did not contain it", stdout)
		}
	})
}

// T12: the printed fixture count must track the fixtures actually run. T01's
// floor and T06's call-line floor both accept a count that lies upward.
func TestAIFakeSelfTestPrintedCountMatchesItsFixtures(t *testing.T) {
	body := strings.Join(shellFunctionBody(t, "ai_fake_self_test"), "\n")

	calls := len(regexp.MustCompile(`(?m)^\s*ai_expect_(pass|refusal)\s`).FindAllString(body, -1))
	inline := len(regexp.MustCompile(`(?m)^\s*if \[ "\$msg_`).FindAllString(body, -1))
	if calls+inline == 0 {
		t.Fatalf("ai_fake_self_test runs no fixtures at all — this test's counting patterns have drifted")
	}

	m := regexp.MustCompile(`AI fake self-test: (\d+) fixtures passed`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("ai_fake_self_test's closing line does not state a numeric fixture count")
	}
	printed, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("could not parse the printed fixture count %q: %v", m[1], err)
	}
	if want := calls + inline; printed != want {
		t.Errorf("the closing line claims %d fixtures but the body runs %d (%d ai_expect_* calls + %d inline comparisons) — adding or dropping a fixture must move the printed number", printed, want, calls, inline)
	}
}

// T13: the label argument added to service_id_by_name and
// assert_environment_is_ephemeral must DEFAULT to APPROVALS_ENFORCED, and the
// six refusal messages must still render the pre-existing wording. T09 cannot
// see this: the approvals self-test discards service_id_by_name's stderr.
func TestSharedHelperLabelDefaultsToApprovalsEnforced(t *testing.T) {
	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatalf("reading railway-env.sh: %v", err)
	}
	content := string(raw)

	for _, decl := range []string{
		`local resp="$1" name="$2" ctx="$3" label="${4:-APPROVALS_ENFORCED}" total count`,
		`local env_id="$1" label="${2:-APPROVALS_ENFORCED}" count ephemeral`,
	} {
		if n := strings.Count(content, decl); n != 1 {
			t.Errorf("found %d occurrence(s) of %q, want exactly 1 — every existing 3-arg and 1-arg call site depends on this default", n, decl)
		}
	}
	if n := strings.Count(content, "APPROVALS_ENFORCED was NOT set."); n != 0 {
		t.Errorf("%d refusal message(s) still hardcode APPROVALS_ENFORCED — set-ai-fake would name the wrong variable", n)
	}
	if n := strings.Count(content, `$label was NOT set.`); n != 6 {
		t.Errorf("%d `$label was NOT set.` message(s), want 6: four in service_id_by_name and two in assert_environment_is_ephemeral", n)
	}

	const zeroInstances = `{"data":{"environment":{"serviceInstances":{"edges":[]}}}}`
	prelude := "set -uo pipefail\n" + shellFunctionSource(t, "service_id_by_name")
	for _, tc := range []struct {
		name     string
		call     string
		wantTail string
	}{
		{"three args keep the approvals wording", `service_id_by_name "$1" invoice "environment env-x"`, "APPROVALS_ENFORCED was NOT set."},
		{"a fourth argument replaces it", `service_id_by_name "$1" invoice "environment env-x" AI_FAKE`, "AI_FAKE was NOT set."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := prelude + "\nrc=0\nout=$(" + tc.call + " 2>&1 >/dev/null) || rc=$?\nprintf '%s' \"$out\"\nexit \"$rc\"\n"
			stdout, _, code := runBashScript(t, script, zeroInstances)
			if code == 0 {
				t.Fatalf("service_id_by_name exited 0 on a zero-instance response; stdout = %q", stdout)
			}
			if !strings.HasSuffix(strings.TrimSpace(stdout), tc.wantTail) {
				t.Errorf("refusal does not end with %q; got %q", tc.wantTail, strings.TrimSpace(stdout))
			}
		})
	}
}

// T14: with every Railway variable unset, an empty argument must still exit 2
// with the usage message. T03 sets RAILWAY_DEV_ENVIRONMENT_ID, so it cannot
// tell a usage guard that runs FIRST from one that runs after
// require_source_env and the persistent-environment compare.
func TestSetAIFakeUsageGuardPrecedesTheSourceEnvRead(t *testing.T) {
	stdout, stderr, code := runAIFakeCmd(t, nil, nil,
		"RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID", "RAILWAY_DEV_ENVIRONMENT_ID")

	if code != 2 {
		t.Errorf("exit code = %d, want 2 with every Railway variable unset; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "usage: railway-env.sh set-ai-fake") {
		t.Errorf("stdout carries no set-ai-fake usage message, so the usage guard did not run first; stdout = %q, stderr = %q", stdout, stderr)
	}
}
