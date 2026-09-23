// railway_env_environment_adversarial_test.go drives set-fork-environment end to end against a
// scripted Railway, and environment_verdict and its self-test past the shapes the AC tests leave open.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	forkEnvID     = "env-fork-1"
	forkGatewayID = "svc-gw-fork"
	forkProjectID = "proj-test"
	forkToken     = "tok-sentinel-not-real"
)

// railwayShim is a curl on PATH that answers each GraphQL operation from <op>.json and logs every request body.
type railwayShim struct{ dir, prelude, log string }

func newRailwayShim(t *testing.T, responses map[string]string) railwayShim {
	t.Helper()
	dir := t.TempDir()
	s := railwayShim{dir: dir, prelude: "export PATH='" + dir + "':\"$PATH\"\n", log: filepath.Join(dir, "calls.jsonl")}
	for op, body := range responses {
		if err := os.WriteFile(filepath.Join(dir, op+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shim := `#!/bin/sh
data=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--data" ]; then data="$2"; shift; fi
  shift
done
printf '%s' "$data" | jq -c . >> '` + s.log + `'
op=$(printf '%s' "$data" | jq -r '.query | capture("^\\s*(query|mutation)\\s+(?<n>\\w+)").n')
f='` + dir + `'/"$op".json
if [ -f "$f" ]; then cat "$f"; else echo '{"errors":[{"message":"unrouted"}]}'; fi
`
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

type railwayCall struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

var gqlOperation = regexp.MustCompile(`^\s*(?:query|mutation)\s+(\w+)`)

func (s railwayShim) calls(t *testing.T) []railwayCall {
	t.Helper()
	raw, err := os.ReadFile(s.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []railwayCall
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var c railwayCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("the shim logged a body that is not JSON: %q", line)
		}
		out = append(out, c)
	}
	return out
}

func operations(calls []railwayCall) []string {
	var ops []string
	for _, c := range calls {
		m := gqlOperation.FindStringSubmatch(c.Query)
		if m == nil {
			ops = append(ops, "?")
			continue
		}
		ops = append(ops, m[1])
	}
	return ops
}

// requireLogs is the control for an empty log: under the same prelude, curl is the shim and it logs.
func (s railwayShim) requireLogs(t *testing.T) {
	t.Helper()
	before := len(s.calls(t))
	_, errOut, code := runBashScript(t, s.prelude+`curl --data '{"query":"query shimControl { x }"}' >/dev/null`+"\n")
	if code != 0 || len(s.calls(t)) != before+1 {
		t.Fatalf("control: curl under the prelude is not the logging shim (exit %d, stderr %q), so an empty log proves nothing", code, errOut)
	}
}

func forkExports(token, project, devEnv bool) string {
	var b strings.Builder
	if token {
		b.WriteString("export RAILWAY_API_TOKEN=" + forkToken + "\n")
	}
	if project {
		b.WriteString("export RAILWAY_PROJECT_ID=" + forkProjectID + "\n")
	}
	if devEnv {
		b.WriteString("export RAILWAY_DEV_ENVIRONMENT_ID=" + persistentEnvironmentID + "\n")
	}
	return b.String()
}

func (s railwayShim) run(t *testing.T, exports string, args ...string) (out string, code int) {
	t.Helper()
	stdout, stderr, code := runBashScript(t, s.prelude+exports+"bash '"+railwayEnvScript(t)+"' set-fork-environment \"$@\"\n", args...)
	return stdout + stderr, code
}

func forkVariables(environment string) string {
	return `{"data":{"variables":{` + forkEnvSecretSibling + `,"RAILWAY_ENVIRONMENT_NAME":"pr-900"` + environment + `}}}`
}

func forkRailway() map[string]string {
	vars := forkVariables(`,"ENVIRONMENT":"development"`)
	return map[string]string{
		"envList": `{"data":{"environments":{"edges":[` +
			`{"node":{"id":"` + persistentEnvironmentID + `","name":"production","isEphemeral":false}},` +
			`{"node":{"id":"` + forkEnvID + `","name":"pr-900","isEphemeral":true}},` +
			`{"node":{"id":"env-other","name":"pr-901","isEphemeral":true}}]}}}`,
		"settle":    forkSettle(`{"node":{"serviceId":"` + forkGatewayID + `","serviceName":"gateway"}}`),
		"varUpsert": `{"data":{"variableUpsert":true}}`,
		"svcVars":   vars,
		"vars":      vars,
	}
}

func forkSettle(extra ...string) string {
	edges := append([]string{
		`{"node":{"serviceId":"svc-sub","serviceName":"submission"}}`,
		`{"node":{"serviceId":"svc-inv","serviceName":"invoice"}}`,
	}, extra...)
	return `{"data":{"environment":{"serviceInstances":{"edges":[` + strings.Join(edges, ",") + `]}}}}`
}

var upsertEcho = regexp.MustCompile(`(?m)^[ \t]+\S+\.\S+ = .*$`)

func TestSetForkEnvironmentAgainstAScriptedRailway(t *testing.T) {
	full := []string{"envList", "settle", "varUpsert", "svcVars"}
	cases := []struct {
		name     string
		override map[string]string
		code     int
		ops      []string
		says     string
	}{
		{"re-read development", nil, 0, full, "gateway ENVIRONMENT=development confirmed in environment " + forkEnvID},
		{"re-read empty", map[string]string{"svcVars": forkVariables(`,"ENVIRONMENT":""`)}, 1, full, "is empty"},
		{"re-read absent", map[string]string{"svcVars": forkVariables("")}, 1, full, "is absent"},
		{"re-read production", map[string]string{"svcVars": forkVariables(`,"ENVIRONMENT":"production"`)}, 1, full, "reads 'production'"},
		{"re-read GraphQL error", map[string]string{"svcVars": `{"errors":[{"message":"Not Authorized"}],"data":{"variables":{` + forkEnvSecretSibling + `}}}`}, 1, full, "Not Authorized"},
		{"no gateway in the fork", map[string]string{"settle": forkSettle()}, 1, []string{"envList", "settle"}, "is named 'gateway'"},
		{"two gateways", map[string]string{"settle": forkSettle(`{"node":{"serviceId":"svc-gw-a","serviceName":"gateway"}}`, `{"node":{"serviceId":"svc-gw-b","serviceName":"gateway"}}`)}, 1, []string{"envList", "settle"}, "are named 'gateway'"},
		{"the target is not ephemeral", map[string]string{"envList": `{"data":{"environments":{"edges":[{"node":{"id":"` + forkEnvID + `","name":"pr-900","isEphemeral":false}}]}}}`}, 1, []string{"envList"}, "is NOT ephemeral"},
		{"the project does not own the id", map[string]string{"envList": `{"data":{"environments":{"edges":[{"node":{"id":"env-other","name":"pr-901","isEphemeral":true}}]}}}`}, 1, []string{"envList"}, "No environment with id " + forkEnvID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			responses := forkRailway()
			for op, body := range c.override {
				responses[op] = body
			}
			if c.override != nil && reflect.DeepEqual(responses, forkRailway()) {
				t.Fatalf("the override did not change the scripted Railway")
			}
			shim := newRailwayShim(t, responses)
			out, code := shim.run(t, forkExports(true, true, true), forkEnvID)
			calls := shim.calls(t)

			if code != c.code {
				t.Errorf("exit code = %d, want %d; output = %q", code, c.code, out)
			}
			if !strings.Contains(out, c.says) {
				t.Errorf("output does not carry %q; output = %q", c.says, out)
			}
			if c.code != 0 && strings.Contains(out, "confirmed") {
				t.Errorf("a failed run printed the confirmation line; output = %q", out)
			}
			if got := operations(calls); !slices.Equal(got, c.ops) {
				t.Errorf("Railway calls = %v, want %v", got, c.ops)
			}
			for _, leak := range []string{forkEnvSecret, forkToken} {
				if strings.Contains(out, leak) {
					t.Errorf("the output carries %q; output = %q", leak, out)
				}
			}

			wantEcho := []string{}
			if slices.Contains(c.ops, "varUpsert") {
				wantEcho = []string{"  gateway.ENVIRONMENT = development"}
			}
			if got := upsertEcho.FindAllString(out, -1); !slices.Equal(got, wantEcho) {
				t.Errorf("upsert echoes = %q, want %q", got, wantEcho)
			}

			for i, call := range calls {
				switch operations(calls)[i] {
				case "settle":
					if want := map[string]any{"e": forkEnvID}; !reflect.DeepEqual(call.Variables, want) {
						t.Errorf("settle variables = %v, want %v", call.Variables, want)
					}
				case "varUpsert":
					want := map[string]any{"input": map[string]any{
						"projectId": forkProjectID, "environmentId": forkEnvID, "serviceId": forkGatewayID,
						"name": "ENVIRONMENT", "value": "development", "skipDeploys": true,
					}}
					if !reflect.DeepEqual(call.Variables, want) {
						t.Errorf("upsert variables = %v, want %v", call.Variables, want)
					}
				case "svcVars":
					if want := map[string]any{"p": forkProjectID, "e": forkEnvID, "s": forkGatewayID}; !reflect.DeepEqual(call.Variables, want) {
						t.Errorf("re-read variables = %v, want %v", call.Variables, want)
					}
				}
			}
		})
	}
}

func TestSetForkEnvironmentRefusesBeforeAnyRailwayCall(t *testing.T) {
	cases := []struct {
		name, exports, arg string
		code               int
		says               string
	}{
		{"the persistent id with a token present", forkExports(true, true, true), persistentEnvironmentID, 1, "Refusing to set ENVIRONMENT in the persistent environment (" + persistentEnvironmentID + ")"},
		{"no source environment id", forkExports(true, true, false), forkEnvID, 1, "RAILWAY_DEV_ENVIRONMENT_ID is not set"},
		{"no token", forkExports(false, true, true), forkEnvID, 1, "RAILWAY_API_TOKEN is not set"},
		{"no project id", forkExports(true, false, true), forkEnvID, 1, "RAILWAY_PROJECT_ID is not set"},
		{"no argument with every variable set", forkExports(true, true, true), "", 2, forkEnvironmentUsage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shim := newRailwayShim(t, forkRailway())
			out, code := shim.run(t, c.exports, c.arg)
			if code != c.code {
				t.Errorf("exit code = %d, want %d; output = %q", code, c.code, out)
			}
			if !strings.Contains(out, c.says) {
				t.Errorf("output does not carry %q; output = %q", c.says, out)
			}
			if ops := operations(shim.calls(t)); len(ops) != 0 {
				t.Errorf("the refusal reached Railway: %v", ops)
			}
			shim.requireLogs(t)
		})
	}
}

func TestEnvironmentVerdictTruthTableIncludingShapesNoFixtureCovers(t *testing.T) {
	script := "set -euo pipefail\n" + shellFunctionSource(t, "environment_verdict") +
		"rc=0\nenvironment_verdict \"$1\" \"$2\" || rc=$?\nexit \"$rc\"\n"
	vars := func(fields string) string { return `{"data":{"variables":{` + forkEnvSecretSibling + fields + `}}}` }

	const (
		absent     = `is absent`
		empty      = `is empty`
		unreadable = `unreadable`
		gqlError   = `GraphQL error`
	)
	cases := []struct {
		name, json, want, says string // says "" means the verdict passes
	}{
		{"match beside an empty errors array", `{"errors":[],"data":{"variables":{` + forkEnvSecretSibling + `,"ENVIRONMENT":"development"}}}`, "development", ""},
		{"want production, reads production", vars(`,"ENVIRONMENT":"production"`), "production", ""},
		{"want production, reads development", vars(`,"ENVIRONMENT":"development"`), "production", `reads 'development'`},
		{"leading space", vars(`,"ENVIRONMENT":" development"`), "development", `reads ' development'`},
		{"trailing space", vars(`,"ENVIRONMENT":"development "`), "development", `reads 'development '`},
		{"capitalised", vars(`,"ENVIRONMENT":"Development"`), "development", `reads 'Development'`},
		{"the fork's own name", vars(`,"ENVIRONMENT":"pr-900"`), "development", `reads 'pr-900'`},
		{"json null", vars(`,"ENVIRONMENT":null`), "development", `reads 'null'`},
		{"number", vars(`,"ENVIRONMENT":7`), "development", `reads '7'`},
		// JSON \n is a real newline; $(...) strips it before a shell compare.
		{"trailing newline", vars(`,"ENVIRONMENT":"development\n"`), "development", `reads`},
		{"trailing newline, want production", vars(`,"ENVIRONMENT":"production\n"`), "production", `reads`},
		{"lower-case key only", vars(`,"environment":"development"`), "development", absent},
		{"empty beside an empty errors array", `{"errors":[],"data":{"variables":{` + forkEnvSecretSibling + `,"ENVIRONMENT":""}}}`, "development", empty},
		{"errors carrying a secret", `{"errors":[{"message":"` + forkEnvSecret + `"}],"data":{"variables":{` + forkEnvSecretSibling + `,"ENVIRONMENT":"development"}}}`, "development", gqlError},
		{"errors beside a matching map", `{"errors":[{"message":"Not Authorized"}],"data":{"variables":{"ENVIRONMENT":"development"}}}`, "development", gqlError},
		{"top-level null", `null`, "development", unreadable},
		{"top-level array", `[` + vars(`,"ENVIRONMENT":"development"`) + `]`, "development", unreadable},
		{"top-level string", `"` + forkEnvSecret + `"`, "development", unreadable},
		{"empty object", `{}`, "development", unreadable},
		{"data null", `{"data":null}`, "development", unreadable},
		{"variables null", `{"data":{"variables":null}}`, "development", unreadable},
		{"variables a string", `{"data":{"variables":"` + forkEnvSecret + `"}}`, "development", unreadable},
		{"variables an array", `{"data":{"variables":["` + forkEnvSecret + `"]}}`, "development", unreadable},
		{"empty input", ``, "development", unreadable},
		{"truncated json", `{"data":{"variables":{` + forkEnvSecretSibling + `,"ENVIRONMENT":"development"`, "development", unreadable},
		{"two json documents", vars(`,"ENVIRONMENT":"development"`) + " " + vars(`,"ENVIRONMENT":"development"`), "development", unreadable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runBashScript(t, script, c.json, c.want)
			out := stdout + stderr
			if c.says == "" && code != 0 {
				t.Errorf("exit code = %d, want 0; output = %q", code, out)
			}
			if c.says != "" {
				if code == 0 {
					t.Errorf("exit code = 0, want non-zero; output = %q", out)
				}
				if !strings.Contains(out, c.says) || !strings.Contains(out, "::error::") {
					t.Errorf("output does not carry ::error:: and %q; output = %q", c.says, out)
				}
			}
			if strings.Contains(out, forkEnvSecret) {
				t.Errorf("the verdict printed %q; output = %q", forkEnvSecret, out)
			}
		})
	}
}

// environmentSelfTestWith runs environment_self_test with override appended after the real functions.
func environmentSelfTestWith(t *testing.T, override string) (string, int) {
	t.Helper()
	real := shellFunctionBody(t, "environment_verdict")
	script := "set -euo pipefail\n" + shellFunctionSource(t, "environment_verdict", "env_expect", "environment_self_test") +
		"real_environment_verdict() {\n" + strings.Join(real, "\n") + "\n}\n" + override + "\nenvironment_self_test\n"
	stdout, stderr, code := runBashScript(t, script)
	return stdout + stderr, code
}

func TestEnvironmentSelfTestNegativeControlsAreLive(t *testing.T) {
	t.Run("the real verdict passes", func(t *testing.T) {
		out, code := environmentSelfTestWith(t, "")
		if code != 0 || !strings.Contains(out, "7 fixtures passed") || strings.Contains(out, "FAILED") {
			t.Fatalf("exit %d; output = %q", code, out)
		}
	})

	cases := []struct {
		name, verdict string
		failed        []string
		passed        []string
	}{
		{"a verdict that accepts everything", `environment_verdict() { echo "  ok"; return 0; }`,
			[]string{"E2", "E3", "E4", "E5", "E6", "E7"}, []string{"E1"}},
		{"a verdict that refuses everything", `environment_verdict() { echo "::error::is absent is empty reads 'production' unreadable GraphQL error"; return 1; }`,
			[]string{"E1", "E7"}, []string{"E2", "E3", "E4", "E5", "E6"}},
		{"a verdict that leaks the map", `environment_verdict() { local rc=0; real_environment_verdict "$@" || rc=$?; printf '%s\n' "$1"; return "$rc"; }`,
			[]string{"E1", "E2", "E3", "E4", "E5", "E6"}, []string{"E7"}},
		{"a verdict that refuses with the wrong words", `environment_verdict() {
  local rc=0 out
  out=$(real_environment_verdict "$@" 2>&1) || rc=$?
  if [ "$rc" = "0" ]; then printf '%s\n' "$out"; else echo "::error::refused ${#1}"; fi
  return "$rc"
}`, []string{"E2", "E3", "E4", "E5", "E6"}, []string{"E1", "E7"}},
		{"a verdict that cannot tell absent from empty", `environment_verdict() {
  local rc=0 out
  out=$(real_environment_verdict "$@" 2>&1) || rc=$?
  case "$out" in *"is absent"*|*"is empty"*) echo "::error::gateway ENVIRONMENT is absent or is empty"; return 1 ;; esac
  printf '%s\n' "$out"; return "$rc"
}`, []string{"E7"}, []string{"E1", "E2", "E3", "E4", "E5", "E6"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := environmentSelfTestWith(t, c.verdict)
			if code == 0 {
				t.Errorf("environment_self_test exited 0 under a broken verdict; output = %q", out)
			}
			if !strings.Contains(out, "fixture(s) FAILED") {
				t.Errorf("no failure summary; output = %q", out)
			}
			for _, id := range c.failed {
				if !strings.Contains(out, "self-test "+id+" FAILED") {
					t.Errorf("%s did not report FAILED; output = %q", id, out)
				}
			}
			for _, id := range c.passed {
				if !regexp.MustCompile(`(?m)^  ` + id + ` ok\b`).MatchString(out) {
					t.Errorf("%s did not report ok, so the control is not isolated; output = %q", id, out)
				}
			}
		})
	}
}

func TestEnvironmentSelfTestPrintedCountMatchesItsFixtures(t *testing.T) {
	body := strings.Join(stripHashComments(shellFunctionBody(t, "environment_self_test")), "\n")
	calls := len(regexp.MustCompile(`(?m)^\s*env_expect\s+E\d+\s`).FindAllString(body, -1))
	inline := len(regexp.MustCompile(`(?m)^\s*if \[ "\$msg_`).FindAllString(body, -1))
	if calls < 6 || inline < 1 {
		t.Fatalf("environment_self_test runs %d env_expect calls and %d inline comparisons; the counting patterns have drifted", calls, inline)
	}

	stdout, stderr, code := runBashScript(t, "bash '"+railwayEnvScript(t)+"' set-fork-environment --self-test\n")
	if code != 0 {
		t.Fatalf("--self-test exit %d; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	m := regexp.MustCompile(`Fork ENVIRONMENT self-test: (\d+) fixtures passed`).FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no numeric fixture count in %q", stdout)
	}
	printed, _ := strconv.Atoi(m[1])
	var ids []string
	for _, ok := range regexp.MustCompile(`(?m)^  (E\d+) ok\b`).FindAllStringSubmatch(stdout, -1) {
		if slices.Contains(ids, ok[1]) {
			t.Errorf("%s reports ok twice", ok[1])
		}
		ids = append(ids, ok[1])
	}
	if printed != calls+inline || printed != len(ids) {
		t.Errorf("the summary claims %d fixtures; the body runs %d (%d env_expect + %d inline) and %d reported ok (%v)", printed, calls+inline, calls, inline, len(ids), ids)
	}
}

func TestRailwayEnvGenericUsageNamesSetForkEnvironment(t *testing.T) {
	stdout, stderr, code := runBashScript(t, "bash '"+railwayEnvScript(t)+"' no-such-subcommand\n")
	out := stdout + stderr
	if code != 2 || !strings.Contains(out, "set-ai-fake <environment-id|--self-test>") {
		t.Fatalf("control: the generic usage did not print (exit %d); output = %q", code, out)
	}
	if !strings.Contains(out, "set-fork-environment <environment-id|--self-test>") {
		t.Errorf("the generic usage does not name set-fork-environment; output = %q", out)
	}
}
