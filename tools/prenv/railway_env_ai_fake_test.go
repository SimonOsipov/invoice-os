// railway_env_ai_fake_test.go pins the contract of
// `scripts/ci/railway-env.sh set-ai-fake`, `ai_key_verdict`, `ai_fake_self_test`,
// and the shared service selector it resolves services through (R6, R7).
//
// NOT COVERED HERE: the live GraphQL write, and whether Railway's
// variableCollectionUpsert mutation accepts an empty string value.
// The only oracle for those is a green prepare-env run on a ready PR.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// persistentEnvironmentID is the id dev-env.yml, dev-env-teardown.yml and dev-env-sweeper.yml pin as RAILWAY_DEV_ENVIRONMENT_ID, so the refusal tests compare against CI's exact string.
const persistentEnvironmentID = "6c864094-6a06-452f-8495-be77d8a94fe7"

// runAIFakeCmd execs `railway-env.sh set-ai-fake <args...>` with unsetVars removed from the environment and extraEnv appended.
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

	code := strings.Join(codeStatements(shellFunctionBody(t, "ai_fake_self_test")), "\n")
	for _, n := range []string{
		`"OPENROUTER_API_KEY":"a-present-key-fixture"`,                          // control: F3
		`"DATABASE_URL":"postgres://u:pw-fixture@h/db","OPENROUTER_API_KEY":""`, // F10
	} {
		if !strings.Contains(code, n) {
			t.Errorf("ai_fake_self_test's statements (comment lines excluded) do not contain the fixture %q", n)
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

// R6: set-ai-fake --self-test runs the shared service selector's 11 fixtures, so the ai-fake-self-test job keeps service_id_by_name under CI.
func TestSetAIFakeSelfTestRunsTheServiceSelectorFixtures(t *testing.T) {
	stdout, stderr, code := runAIFakeCmd(t, []string{"--self-test"}, nil,
		"RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID", "RAILWAY_DEV_ENVIRONMENT_ID")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Service selector self-test: 11 fixtures passed, no token read, no network call.") {
		t.Errorf("stdout does not contain the selector self-test's closing line; stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "AI fake self-test: 10 fixtures passed") {
		t.Errorf("stdout does not contain the ai-fake self-test's closing line; stdout = %q", stdout)
	}
}

// The selector's closing line is a literal that still says 11 after a fixture
// is deleted; this counts the fixtures that actually reported.
func TestServiceSelectorSelfTestReportsEveryFixture(t *testing.T) {
	stdout, stderr, code := runAIFakeCmd(t, []string{"--self-test"}, nil,
		"RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID", "RAILWAY_DEV_ENVIRONMENT_ID")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	end := strings.Index(stdout, "Service selector self-test: 11 fixtures passed")
	if end < 0 {
		t.Fatalf("no selector closing line; stdout = %q", stdout)
	}
	reported := regexp.MustCompile(`(?m)^  (A\d+) ok -> `).FindAllStringSubmatch(stdout[:end], -1)
	if len(reported) == 0 {
		t.Fatalf("no selector fixture reported ok before the closing line; stdout = %q", stdout)
	}
	seen := map[string]int{}
	for _, m := range reported {
		seen[m[1]]++
	}
	for i := 1; i <= 11; i++ {
		if id := "A" + strconv.Itoa(i); seen[id] != 1 {
			t.Errorf("fixture %s reported ok %d time(s), want 1", id, seen[id])
		}
	}
	if len(reported) != 11 {
		t.Errorf("%d selector fixtures reported ok, the closing line claims 11", len(reported))
	}
}

// T12 counts call lines in the source, so a fixture that never runs still counts;
// this counts the AI fixtures that actually reported.
func TestAIFakeSelfTestReportsEveryFixture(t *testing.T) {
	stdout, stderr, code := runAIFakeCmd(t, []string{"--self-test"}, nil,
		"RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID", "RAILWAY_DEV_ENVIRONMENT_ID")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	start := strings.Index(stdout, "Service selector self-test: 11 fixtures passed")
	end := strings.Index(stdout, "AI fake self-test: 10 fixtures passed")
	if start < 0 || end < start {
		t.Fatalf("no selector closing line followed by the AI closing line; stdout = %q", stdout)
	}
	reported := regexp.MustCompile(`(?m)^  (F\d+) ok -> `).FindAllStringSubmatch(stdout[start:end], -1)
	if len(reported) == 0 {
		t.Fatalf("no AI fixture reported ok before the closing line; stdout = %q", stdout)
	}
	seen := map[string]int{}
	for _, m := range reported {
		seen[m[1]]++
	}
	for i := 1; i <= 10; i++ {
		if id := "F" + strconv.Itoa(i); seen[id] != 1 {
			t.Errorf("fixture %s reported ok %d time(s), want 1", id, seen[id])
		}
	}
	if len(reported) != 10 {
		t.Errorf("%d AI fixtures reported ok, the closing line claims 10", len(reported))
	}
}

// --self-test's "no network call", observed: curl is shimmed to record each
// call. The live path is the control that proves the shim is on the script's PATH.
func TestSetAIFakeSelfTestCallsNoNetwork(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "curl-calls")
	shim := "#!/bin/sh\necho called >> '" + calls + "'\necho '{\"data\":{\"environments\":{\"edges\":[]}}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(shim), 0o755); err != nil {
		t.Fatalf("writing the curl shim: %v", err)
	}
	path := "PATH=" + dir + ":" + os.Getenv("PATH")
	railway := []string{"RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID", "RAILWAY_DEV_ENVIRONMENT_ID"}

	stdout, stderr, code := runAIFakeCmd(t, []string{"--self-test"}, []string{path}, railway...)
	if code != 0 {
		t.Fatalf("--self-test exit code = %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Errorf("--self-test called curl; stdout = %q", stdout)
	}

	live := []string{path, "RAILWAY_API_TOKEN=not-a-token", "RAILWAY_PROJECT_ID=p", "RAILWAY_DEV_ENVIRONMENT_ID=" + persistentEnvironmentID}
	if out, _, code := runAIFakeCmd(t, []string{"env-x"}, live, railway...); code == 0 {
		t.Errorf("control: the live path exited 0 against an empty environment list; stdout = %q", out)
	}
	if _, err := os.Stat(calls); err != nil {
		t.Fatalf("control: the live path never reached the curl shim, so --self-test's silence proves nothing: %v", err)
	}
}

var shellTrailingComment = regexp.MustCompile(`(^|[ \t;])#.*$`)

// codeStatements is statements with trailing `# …` comments cut too. A ` #`
// inside quotes is cut as well; that fails a needle, it cannot pass one.
func codeStatements(body []string) []string {
	var out []string
	for _, s := range statements(body) {
		if s = strings.TrimSpace(shellTrailingComment.ReplaceAllString(s, "${1}")); s != "" {
			out = append(out, s)
		}
	}
	return out
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
// RUNNER_TEMP is stripped too, so a CI run never appends to the runner's call log.
func runBashScript(t *testing.T, script string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.Command("bash", append([]string{"-c", script, "bash"}, args...)...)
	cmd.Dir = repoRoot(t)

	var filtered []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "RAILWAY_") || strings.HasPrefix(kv, "RUNNER_TEMP=") {
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
// self-test fixture covers, plus the no-leak property on EVERY branch.
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
		{"boolean true key", `{"data":{"variables":{"OPENROUTER_API_KEY":true}}}`, false, ""},
		{"unrendered reference", `{"data":{"variables":{"OPENROUTER_API_KEY":"${{ secrets.OPENROUTER_API_KEY }}"}}}`, false, "secrets.OPENROUTER_API_KEY"},
		// The retired key is no longer audited: production deleted it, so no fork copies it.
		{"retired typesafe key beside an empty key", `{"data":{"variables":{"OPENROUTER_API_KEY":"","TYPESAFE_API_KEY":"ts-9f2a-ignored"}}}`, true, "ts-9f2a-ignored"},
		// Only the errors branch can refuse this: the map itself is readable and clean.
		{"populated errors beside an empty key", `{"errors":[{"message":"x"}],"data":{"variables":{"OPENROUTER_API_KEY":""}}}`, false, ""},
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

// runAIKeyVerdict drives ai_key_verdict through the truth table's source path.
func runAIKeyVerdict(t *testing.T, json string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return runBashScript(t, "set -uo pipefail\n"+shellFunctionSource(t, "ai_key_verdict")+verdictDriver, json)
}

func TestAIKeyVerdictNamesTheOffendingVariable(t *testing.T) {
	const value = "mk-9f2a-named"
	stdout, stderr, code := runAIKeyVerdict(t, `{"data":{"variables":{"OPENROUTER_API_KEY":"`+value+`"}}}`)
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero; output = %q", stdout)
	}
	if want := "submission.OPENROUTER_API_KEY is SET"; !strings.Contains(stdout, want) {
		t.Errorf("output does not contain %q; output = %q", want, stdout)
	}
	if strings.Contains(stdout, value) || strings.Contains(stderr, value) {
		t.Errorf("the verdict printed %q; stdout = %q, stderr = %q", value, stdout, stderr)
	}
}

// Line shape is the prepare-env evidence: "submission.OPENROUTER_API_KEY is empty".
func TestAIKeyVerdictReportsTheKeyOnAPass(t *testing.T) {
	cases := []struct{ name, json, kind string }{
		{"empty", `{"data":{"variables":{"OPENROUTER_API_KEY":""}}}`, "empty"},
		{"absent", `{"data":{"variables":{"AI_FAKE":"true","JEV_FAKE":"true"}}}`, "absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, code := runAIKeyVerdict(t, tc.json)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; output = %q", code, stdout)
			}
			if strings.Contains(stdout, "::error::") {
				t.Errorf("a pass carries an ::error:: line; output = %q", stdout)
			}
			if want := "submission.OPENROUTER_API_KEY is " + tc.kind; !strings.Contains(stdout, want) {
				t.Errorf("output does not contain %q; output = %q", want, stdout)
			}
			if n := strings.Count(stdout, "submission.OPENROUTER_API_KEY is "); n != 1 {
				t.Errorf("output reports submission.OPENROUTER_API_KEY %d times, want 1; output = %q", n, stdout)
			}
		})
	}
}

func TestAIKeyVerdictUnreadableNamesTheKey(t *testing.T) {
	cases := []struct{ name, json string }{
		{"variables null", `{"data":{"variables":null}}`},
		{"graphql error", `{"errors":[{"message":"x"}],"data":null}`},
		// Readable map: only the errors message can name the key here.
		{"graphql error beside a readable map", `{"errors":[{"message":"x"}],"data":{"variables":{"OPENROUTER_API_KEY":""}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, code := runAIKeyVerdict(t, tc.json)
			if code == 0 {
				t.Errorf("exit code = 0, want non-zero; output = %q", stdout)
			}
			var errLines []string
			for _, l := range strings.Split(stdout, "\n") {
				if strings.Contains(l, "::error::") {
					errLines = append(errLines, l)
				}
			}
			if len(errLines) == 0 {
				t.Fatalf("no ::error:: line; output = %q", stdout)
			}
			if !slices.ContainsFunc(errLines, func(l string) bool { return strings.Contains(l, "OPENROUTER_API_KEY") }) {
				t.Errorf("no ::error:: line names OPENROUTER_API_KEY; lines = %q", errLines)
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

// R7: neither shared helper has a default label; a call without one refuses before it reads anything.
func TestServiceSelectorLabelIsRequired(t *testing.T) {
	// A1's 11-instance fleet (service_selector_self_test), copied verbatim: selects svc-inv.
	const fleetJSON = `{"data":{"environment":{"serviceInstances":{"edges":[{"node":{"serviceId":"svc-gw","serviceName":"gateway"}},{"node":{"serviceId":"svc-pg","serviceName":"postgres"}},{"node":{"serviceId":"svc-ten","serviceName":"tenancy"}},{"node":{"serviceId":"svc-port","serviceName":"portfolio"}},{"node":{"serviceId":"svc-inv","serviceName":"invoice"}},{"node":{"serviceId":"svc-val","serviceName":"validation"}},{"node":{"serviceId":"svc-sub","serviceName":"submission"}},{"node":{"serviceId":"svc-dash","serviceName":"dashboard"}},{"node":{"serviceId":"svc-notif","serviceName":"notifications"}},{"node":{"serviceId":"svc-land","serviceName":"landing"}},{"node":{"serviceId":"svc-app","serviceName":"app"}}]}}}}`

	t.Run("service_id_by_name without a label refuses", func(t *testing.T) {
		script := "set -uo pipefail\n" + shellFunctionSource(t, "service_id_by_name") +
			`rc=0; out=$(service_id_by_name "$1" invoice "self-test R7" 2>/dev/null) || rc=$?; printf '%s' "$out"; exit "$rc"`
		stdout, _, code := runBashScript(t, script, fleetJSON)
		if code == 0 {
			t.Errorf("exit code = 0, want non-zero: no label given, must refuse; stdout = %q", stdout)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty: a leak here would upsert against a garbage serviceId", stdout)
		}
	})

	t.Run("service_id_by_name with a label selects", func(t *testing.T) {
		script := "set -uo pipefail\n" + shellFunctionSource(t, "service_id_by_name") +
			`rc=0; out=$(service_id_by_name "$1" invoice "self-test R7" AI_FAKE 2>/dev/null) || rc=$?; printf '%s' "$out"; exit "$rc"`
		stdout, _, code := runBashScript(t, script, fleetJSON)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 with a label given; stdout = %q", code, stdout)
		}
		if stdout != "svc-inv" {
			t.Errorf("stdout = %q, want %q", stdout, "svc-inv")
		}
	})

	t.Run("assert_environment_is_ephemeral without a label exits before the read", func(t *testing.T) {
		script := "set -uo pipefail\n" + shellFunctionSource(t, "assert_environment_is_ephemeral") + `
fetch_environment_list() { echo FETCHED; GQL_RESPONSE='{"data":{"environments":{"edges":[{"node":{"id":"env-x","isEphemeral":true}}]}}}'; }
assert_environment_is_ephemeral env-x
`
		stdout, _, code := runBashScript(t, script)
		if code == 0 {
			t.Errorf("exit code = 0, want non-zero: no label given, must refuse before reading; stdout = %q", stdout)
		}
		if strings.Contains(stdout, "FETCHED") {
			t.Errorf("stdout contains FETCHED — fetch_environment_list ran before the label was checked; stdout = %q", stdout)
		}
	})

	t.Run("assert_environment_is_ephemeral with a label passes an ephemeral env", func(t *testing.T) {
		script := "set -uo pipefail\n" + shellFunctionSource(t, "assert_environment_is_ephemeral") + `
fetch_environment_list() { echo FETCHED; GQL_RESPONSE='{"data":{"environments":{"edges":[{"node":{"id":"env-x","isEphemeral":true}}]}}}'; }
assert_environment_is_ephemeral env-x AI_FAKE
`
		stdout, _, code := runBashScript(t, script)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 with a label given; stdout = %q", code, stdout)
		}
		if !strings.Contains(stdout, "FETCHED") {
			t.Errorf("stdout does not contain FETCHED; stdout = %q", stdout)
		}
	})
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
