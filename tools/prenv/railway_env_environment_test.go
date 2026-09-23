// railway_env_environment_test.go pins `railway-env.sh set-fork-environment`,
// `environment_verdict`, and the PR-only dev-env.yml steps that keep the mock issuer on forks.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	forkEnvironmentUsage  = "usage: railway-env.sh set-fork-environment <environment-id>"
	forkEnvSecret         = "sentinel-secret-dsn"
	forkEnvSecretSibling  = `"DATABASE_URL":"sentinel-secret-dsn"`
	prOnlyCondition       = "github.event_name == 'pull_request'"
	forkEnvironmentRunCmd = `bash scripts/ci/railway-env.sh set-fork-environment "$ENV_ID"`
	forkSelfTestRunCmd    = "bash scripts/ci/railway-env.sh set-fork-environment --self-test"
)

// curlShim prepends a curl to PATH that logs each call instead of reaching the network.
type curlShim struct{ dir, prelude, log string }

func newCurlShim(t *testing.T) curlShim {
	t.Helper()
	dir := t.TempDir()
	s := curlShim{dir: dir, prelude: "export PATH='" + dir + "':\"$PATH\"\n", log: filepath.Join(dir, "curl-calls")}
	body := "#!/bin/sh\necho \"$*\" >> '" + s.log + "'\necho '{\"data\":{}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(body), 0o755); err != nil {
		t.Fatalf("writing the curl shim: %v", err)
	}
	return s
}

// calls returns every logged curl call, "" when curl never ran.
func (s curlShim) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(s.log)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("reading the curl log: %v", err)
	}
	return string(b)
}

// requireOnPath is the control for an empty log: under the same prelude, curl is the shim and it logs.
func (s curlShim) requireOnPath(t *testing.T) {
	t.Helper()
	out, errOut, code := runBashScript(t, s.prelude+"command -v curl\ncurl shim-control >/dev/null\n")
	if code != 0 || strings.TrimSpace(out) != filepath.Join(s.dir, "curl") {
		t.Fatalf("control: curl resolves to %q (exit %d, stderr %q), not the shim, so an empty log proves nothing", strings.TrimSpace(out), code, errOut)
	}
	if !strings.Contains(s.calls(t), "shim-control") {
		t.Fatalf("control: the shim logged no call, so an empty log proves nothing")
	}
}

// runSetForkEnvironment runs the subcommand through runBashScript, which strips every RAILWAY_* variable.
func runSetForkEnvironment(t *testing.T, shim curlShim, exports string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runBashScript(t, shim.prelude+exports+"bash '"+railwayEnvScript(t)+"' set-fork-environment \"$@\"\n", args...)
}

// stripHashComments drops shell and YAML `#` comments outside quotes.
func stripHashComments(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = line
		var inS, inD bool
		for j := 0; j < len(line); j++ {
			switch c := line[j]; {
			case c == '\\' && inD:
				j++
			case c == '\'' && !inD:
				inS = !inS
			case c == '"' && !inS:
				inD = !inD
			case c == '#' && !inS && !inD && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t'):
				out[i] = strings.TrimRight(line[:j], " \t")
				j = len(line)
			}
		}
	}
	return out
}

func TestSetForkEnvironmentRefusesThePersistentEnvironment(t *testing.T) {
	shim := newCurlShim(t)
	stdout, stderr, code := runSetForkEnvironment(t, shim,
		"export RAILWAY_DEV_ENVIRONMENT_ID="+persistentEnvironmentID+"\n", persistentEnvironmentID)
	out := stdout + stderr

	if code != 1 {
		t.Errorf("exit code = %d, want 1: the persistent environment must be refused; output = %q", code, out)
	}
	if !strings.Contains(out, persistentEnvironmentID) {
		t.Errorf("output does not name the refused persistent environment %s; output = %q", persistentEnvironmentID, out)
	}
	if strings.Contains(out, "RAILWAY_API_TOKEN is not set") {
		t.Errorf("the run reached require_env before refusing, so the refusal needs a token to say no; output = %q", out)
	}
	if calls := shim.calls(t); calls != "" {
		t.Errorf("the refusal called curl:\n%s", calls)
	}
	shim.requireOnPath(t)
}

func TestSetForkEnvironmentUsageExitsTwo(t *testing.T) {
	shim := newCurlShim(t)
	stdout, stderr, code := runSetForkEnvironment(t, shim, "")
	out := stdout + stderr

	if code != 2 {
		t.Errorf("exit code = %d, want 2 with no argument; output = %q", code, out)
	}
	if !strings.Contains(out, forkEnvironmentUsage) {
		t.Errorf("output does not carry %q; output = %q", forkEnvironmentUsage, out)
	}
	if strings.Contains(out, "RAILWAY_DEV_ENVIRONMENT_ID is not set") || strings.Contains(out, "RAILWAY_API_TOKEN is not set") {
		t.Errorf("the run passed the usage guard into require_source_env or require_env; output = %q", out)
	}

	// Control: the catch-all also exits 2, so only the phrase proves the subcommand's own guard ran.
	gout, gerr, gcode := runBashScript(t, shim.prelude+"bash '"+railwayEnvScript(t)+"'\n")
	generic := gout + gerr
	if gcode != 2 || !strings.Contains(generic, "usage: railway-env.sh <") {
		t.Fatalf("control: the dispatcher's generic usage did not print (exit %d); output = %q", gcode, generic)
	}
	if strings.Contains(generic, forkEnvironmentUsage) {
		t.Errorf("the generic usage carries %q, so it cannot tell the subcommand's guard from the catch-all; output = %q", forkEnvironmentUsage, generic)
	}
}

var (
	forkEnvironmentGuards = []struct {
		name string
		re   *regexp.Regexp
	}{
		{"the --self-test branch", regexp.MustCompile(`--self-test`)},
		{"the usage guard", regexp.MustCompile(regexp.QuoteMeta(forkEnvironmentUsage))},
		{"require_source_env", regexp.MustCompile(`\brequire_source_env\b`)},
		{"the persistent-id compare", regexp.MustCompile(`"\$\{?env_id\}?"\s*==?\s*"\$\{?RAILWAY_DEV_ENVIRONMENT_ID\}?"|"\$\{?RAILWAY_DEV_ENVIRONMENT_ID\}?"\s*==?\s*"\$\{?env_id\}?"`)},
		{"require_env", regexp.MustCompile(`\brequire_env\b`)},
		{"assert_environment_is_ephemeral", regexp.MustCompile(`\bassert_environment_is_ephemeral\s+"\$\{?env_id\}?"\s+\S`)},
		{"service_id_by_name", regexp.MustCompile(`\bservice_id_by_name\b`)},
		{"upsert_variable", regexp.MustCompile(`\bupsert_variable\b`)},
	}
	gatewayIDByName    = regexp.MustCompile(`\b(\w+)=\$\(\s*service_id_by_name\s+\S+\s+"?gateway"?\s`)
	variablesReRead    = regexp.MustCompile(`\b(SERVICE_)?VARIABLES_QUERY\b`)
	developmentVerdict = regexp.MustCompile(`\benvironment_verdict\s+\S+\s+"?development"?(\s|$)`)
)

// forkEnvironmentBodyFaults reports each way comment-stripped cmd_set_fork_environment code
// departs from the guard order and the gateway-only write.
func forkEnvironmentBodyFaults(code string) []string {
	var faults []string
	prev, prevName := -1, ""
	for _, g := range forkEnvironmentGuards {
		loc := g.re.FindStringIndex(code)
		if loc == nil {
			faults = append(faults, "no "+g.name)
			continue
		}
		if loc[0] < prev {
			faults = append(faults, fmt.Sprintf("%s comes before %s", g.name, prevName))
		}
		prev, prevName = loc[0], g.name
	}

	upserts := regexp.MustCompile(`\bupsert_variable\b`).FindAllStringIndex(code, -1)
	if len(upserts) != 1 {
		faults = append(faults, fmt.Sprintf("%d upsert_variable calls, want exactly 1", len(upserts)))
	}
	if strings.Contains(code, "RAILWAY_SVC_GATEWAY_ID") {
		faults = append(faults, "names RAILWAY_SVC_GATEWAY_ID; the gateway must be resolved by name")
	}
	if !strings.Contains(code, "SETTLE_QUERY") {
		faults = append(faults, "no SETTLE_QUERY read for service_id_by_name")
	}
	if m := gatewayIDByName.FindStringSubmatch(code); m == nil {
		faults = append(faults, "no `<id>=$(service_id_by_name … gateway …)`")
	} else {
		write := regexp.MustCompile(`(?m)\bupsert_variable\s+"\$\{?env_id\}?"\s+"\$\{?` + regexp.QuoteMeta(m[1]) + `\}?"\s+"?gateway"?\s+"?ENVIRONMENT"?\s+"?development"?\s*$`)
		if !write.MatchString(code) {
			faults = append(faults, fmt.Sprintf(`no upsert_variable "$env_id" "$%s" gateway ENVIRONMENT development`, m[1]))
		}
	}

	var upsertAt, reReadAt, verdictAt = -1, -1, -1
	if len(upserts) > 0 {
		upsertAt = upserts[len(upserts)-1][0]
	}
	if all := variablesReRead.FindAllStringIndex(code, -1); len(all) > 0 {
		reReadAt = all[len(all)-1][0]
	}
	if all := developmentVerdict.FindAllStringIndex(code, -1); len(all) > 0 {
		verdictAt = all[len(all)-1][0]
	}
	switch {
	case reReadAt < 0 || reReadAt < upsertAt:
		faults = append(faults, "no fresh variables re-read after the upsert")
	case verdictAt < 0:
		faults = append(faults, "no `environment_verdict … development`")
	case verdictAt < reReadAt:
		faults = append(faults, "environment_verdict runs before the fresh re-read")
	}
	return faults
}

const (
	forkBodySelfTest = `  local env_id="${1:-}"
  if [ "$env_id" = "--self-test" ]; then
    environment_self_test
    return
  fi
`
	forkBodyUsage = `  if [ -z "$env_id" ]; then
    echo "::error::usage: railway-env.sh set-fork-environment <environment-id>"
    exit 2
  fi
`
	forkBodyGuards = `  require_source_env
  if [ "$env_id" = "$RAILWAY_DEV_ENVIRONMENT_ID" ]; then
    echo "::error::Refusing to set ENVIRONMENT in the persistent environment ($env_id)."
    exit 1
  fi
  require_env
  assert_environment_is_ephemeral "$env_id" ENVIRONMENT
  graphql_post "$(gql_body "$SETTLE_QUERY" "$(jq -n --arg e "$env_id" '{e: $e}')")" \
    "listing service instances in environment $env_id"
  local svc_id
`
	forkBodyResolve = `  svc_id=$(service_id_by_name "$GQL_RESPONSE" gateway "environment $env_id" ENVIRONMENT)
`
	forkBodyUpsert = `  upsert_variable "$env_id" "$svc_id" gateway ENVIRONMENT development
`
	forkBodyReRead = `  graphql_post "$(gql_body "$SERVICE_VARIABLES_QUERY" \
    "$(jq -n --arg p "$RAILWAY_PROJECT_ID" --arg e "$env_id" --arg s "$svc_id" '{p: $p, e: $e, s: $s}')")" \
    "re-reading gateway variables in environment $env_id"
`
	forkBodyVerdict = `  environment_verdict "$GQL_RESPONSE" development || exit 1
  echo "gateway ENVIRONMENT=development confirmed in environment $env_id."
`
)

// swapOnce exchanges the first occurrences of a and b.
func swapOnce(s, a, b string) string {
	const mark = "\x00"
	return strings.Replace(strings.Replace(strings.Replace(s, a, mark, 1), b, a, 1), mark, b, 1)
}

func shellCode(lines []string) string {
	return strings.Join(stripHashComments(lines), "\n")
}

func TestSetForkEnvironmentWritesOnlyTheGatewayEnvironment(t *testing.T) {
	good := forkBodySelfTest + forkBodyUsage + forkBodyGuards + forkBodyResolve + forkBodyUpsert + forkBodyReRead + forkBodyVerdict
	fixtures := []struct{ name, body string }{
		{"require_env swapped with require_source_env", swapOnce(good, "  require_source_env\n", "  require_env\n")},
		{"usage after require_source_env", swapOnce(good, forkBodyUsage, "  require_source_env\n")},
		{"service_id_by_name before assert_environment_is_ephemeral", swapOnce(good, "  assert_environment_is_ephemeral \"$env_id\" ENVIRONMENT\n", forkBodyResolve)},
		{"the constant gateway id", strings.Replace(good, forkBodyResolve, "  svc_id=\"$RAILWAY_SVC_GATEWAY_ID\"\n", 1)},
		{"the constant id in the upsert", strings.Replace(good, `"$svc_id" gateway ENVIRONMENT`, `"$RAILWAY_SVC_GATEWAY_ID" gateway ENVIRONMENT`, 1)},
		{"a second variable written", strings.Replace(good, forkBodyUpsert, forkBodyUpsert+"  upsert_variable \"$env_id\" \"$svc_id\" gateway GATEWAY_MOCK_ISSUER true\n", 1)},
		{"the value production", strings.Replace(good, "ENVIRONMENT development\n", "ENVIRONMENT production\n", 1)},
		{"a service other than gateway", strings.Replace(good, `"$GQL_RESPONSE" gateway "environment`, `"$GQL_RESPONSE" submission "environment`, 1)},
		{"require_env commented out", strings.Replace(good, "  require_env\n", "  # require_env\n", 1)},
		{"no fresh re-read", strings.Replace(good, forkBodyReRead, "", 1)},
		{"the verdict before the re-read", swapOnce(good, forkBodyReRead, forkBodyVerdict)},
	}
	t.Run("fixtures", func(t *testing.T) {
		if faults := forkEnvironmentBodyFaults(shellCode(strings.Split(good, "\n"))); len(faults) != 0 {
			t.Fatalf("the planned body reports %v", faults)
		}
		for _, f := range fixtures {
			if f.body == good {
				t.Fatalf("fixture %q: the edit did not apply", f.name)
			}
			if faults := forkEnvironmentBodyFaults(shellCode(strings.Split(f.body, "\n"))); len(faults) == 0 {
				t.Errorf("fixture %q: no fault reported", f.name)
			}
		}
	})

	for _, fault := range forkEnvironmentBodyFaults(shellCode(shellFunctionBody(t, "cmd_set_fork_environment"))) {
		t.Errorf("cmd_set_fork_environment: %s", fault)
	}
}

func TestEnvironmentVerdictTellsAbsentFromEmpty(t *testing.T) {
	script := "set -euo pipefail\n" + shellFunctionSource(t, "environment_verdict") +
		"rc=0\nenvironment_verdict \"$1\" development || rc=$?\nexit \"$rc\"\n"

	cases := []struct {
		name, json string
		pass       bool
		says       *regexp.Regexp
	}{
		{"match", `{"data":{"variables":{` + forkEnvSecretSibling + `,"ENVIRONMENT":"development"}}}`, true, nil},
		{"absent", `{"data":{"variables":{` + forkEnvSecretSibling + `}}}`, false, regexp.MustCompile(`is absent`)},
		{"empty", `{"data":{"variables":{` + forkEnvSecretSibling + `,"ENVIRONMENT":""}}}`, false, regexp.MustCompile(`is empty`)},
		{"other", `{"data":{"variables":{` + forkEnvSecretSibling + `,"ENVIRONMENT":"production"}}}`, false, regexp.MustCompile(`reads 'production'`)},
		{"unreadable", `not json {` + forkEnvSecretSibling + `}`, false, regexp.MustCompile(`(?i)unreadable`)},
		{"errors", `{"errors":[{"message":"Not Authorized"}],"data":{"variables":{` + forkEnvSecretSibling + `}}}`, false, regexp.MustCompile(`(?i)graphql|\bapi\b|not authorized`)},
	}

	failures := map[string]string{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runBashScript(t, script, c.json)
			out := stdout + stderr
			if c.pass && code != 0 {
				t.Errorf("exit code = %d, want 0; output = %q", code, out)
			}
			if !c.pass {
				if code == 0 {
					t.Errorf("exit code = 0, want non-zero; output = %q", out)
				}
				if !c.says.MatchString(out) {
					t.Errorf("output does not match %q; output = %q", c.says, out)
				}
				failures[c.name] = out
			}
			if strings.Contains(stdout, forkEnvSecret) || strings.Contains(stderr, forkEnvSecret) {
				t.Errorf("the verdict printed %q from the variable map; stdout = %q, stderr = %q", forkEnvSecret, stdout, stderr)
			}
		})
	}

	if len(failures) != 5 {
		t.Fatalf("%d failing fixtures ran, want 5", len(failures))
	}
	seen := map[string]string{}
	for _, name := range []string{"absent", "empty", "other", "unreadable", "errors"} {
		if other, dup := seen[failures[name]]; dup {
			t.Errorf("%s and %s print the same message %q", other, name, failures[name])
		}
		seen[failures[name]] = name
	}
}

// singleQuoted returns the single-quoted shell words on each line, outside double quotes.
func singleQuoted(lines []string) []string {
	var out []string
	for _, line := range lines {
		inD := false
		for i := 0; i < len(line); i++ {
			switch c := line[i]; {
			case c == '\\' && inD:
				i++
			case c == '"':
				inD = !inD
			case c == '\'' && !inD:
				end := strings.IndexByte(line[i+1:], '\'')
				if end < 0 {
					i = len(line)
					continue
				}
				out = append(out, line[i+1:i+1+end])
				i += end + 1
			}
		}
	}
	return out
}

// selfTestFixtureFaults reports JSON fixtures in a comment-stripped self-test body that lack the secret sibling.
func selfTestFixtureFaults(lines []string) []string {
	var fixtures []string
	for _, w := range singleQuoted(stripHashComments(lines)) {
		if strings.ContainsAny(w, `{"`) {
			fixtures = append(fixtures, w)
		}
	}
	var faults []string
	if len(fixtures) < 6 {
		faults = append(faults, fmt.Sprintf("%d single-quoted JSON fixtures, want at least 6 (match, absent, empty, other, unreadable, errors)", len(fixtures)))
	}
	for _, f := range fixtures {
		if !strings.Contains(f, forkEnvSecretSibling) {
			faults = append(faults, fmt.Sprintf("fixture %q does not carry %s", f, forkEnvSecretSibling))
		}
	}
	return faults
}

func TestSetForkEnvironmentSelfTestNeverPrintsASecretValue(t *testing.T) {
	shim := newCurlShim(t)
	stdout, stderr, code := runSetForkEnvironment(t, shim, "", "--self-test")
	if code != 0 {
		t.Errorf("exit code = %d, want 0; the no-leak check needs a self-test that ran; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, forkEnvSecret) || strings.Contains(stderr, forkEnvSecret) {
		t.Errorf("the self-test printed %q; stdout = %q, stderr = %q", forkEnvSecret, stdout, stderr)
	}

	t.Run("planted fixture", func(t *testing.T) {
		row := func(json string) string { return "  env_expect E1 '" + json + "'" }
		withSecret := `{"data":{"variables":{` + forkEnvSecretSibling + `}}}`
		good := []string{row(withSecret), row(withSecret), row(withSecret), row(withSecret), row(withSecret), row(withSecret)}
		if faults := selfTestFixtureFaults(good); len(faults) != 0 {
			t.Fatalf("six fixtures with the sibling report %v", faults)
		}
		bare := append(slices.Clone(good[:5]), row(`{"data":{"variables":{"ENVIRONMENT":""}}}`))
		if faults := selfTestFixtureFaults(bare); len(faults) != 1 {
			t.Errorf("a fixture without the sibling reports %v, want 1 fault", faults)
		}
		if faults := selfTestFixtureFaults(good[:5]); len(faults) != 1 {
			t.Errorf("five fixtures report %v, want 1 fault", faults)
		}
	})

	for _, fault := range selfTestFixtureFaults(shellFunctionBody(t, "environment_self_test")) {
		t.Errorf("environment_self_test: %s", fault)
	}
}

func TestSetForkEnvironmentSelfTestCallsNoNetwork(t *testing.T) {
	shim := newCurlShim(t)
	stdout, stderr, code := runSetForkEnvironment(t, shim, "", "--self-test")
	out := stdout + stderr
	if code != 0 {
		t.Errorf("exit code = %d, want 0 with no token and no Railway variable; output = %q", code, out)
	}
	if strings.Contains(out, "RAILWAY_API_TOKEN is not set") || strings.Contains(out, "RAILWAY_DEV_ENVIRONMENT_ID is not set") {
		t.Errorf("--self-test reached require_env or require_source_env; output = %q", out)
	}
	if calls := shim.calls(t); calls != "" {
		t.Errorf("--self-test called curl:\n%s", calls)
	}
	shim.requireOnPath(t)
}

// codeAndCommentCounts counts needle in comment-stripped src and in its comments.
func codeAndCommentCounts(src, needle string) (inCode, inComments int) {
	inCode = strings.Count(shellCode(strings.Split(src, "\n")), needle)
	return inCode, strings.Count(src, needle) - inCode
}

func TestReconcileForkRecordsNoEnvironment(t *testing.T) {
	needle := "record_environment" + "_variable"

	if c, k := codeAndCommentCounts("  ensure_bucket \"$env_id\"\n  "+needle+" \"$env_id\"\n", needle); c != 1 || k != 0 {
		t.Fatalf("planted call: counted %d in code and %d in comments, want 1 and 0", c, k)
	}
	if c, k := codeAndCommentCounts("# "+needle+" measures it\n", needle); c != 0 || k != 1 {
		t.Fatalf("planted comment: counted %d in code and %d in comments, want 0 and 1", c, k)
	}

	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	// Floor and control: the read is the whole script, reconcile-fork included.
	if n := strings.Count(src, "\n"); n < 1000 || !strings.Contains(shellCode(strings.Split(src, "\n")), "cmd_reconcile_fork() {") {
		t.Fatalf("read %d lines without a `cmd_reconcile_fork() {` definition; this is not railway-env.sh", n)
	}
	inCode, inComments := codeAndCommentCounts(src, needle)
	if inCode != 0 {
		t.Errorf("railway-env.sh names %s %d time(s) in code; it contradicts the fork ENVIRONMENT write", needle, inCode)
	}
	if inComments != 0 {
		t.Errorf("railway-env.sh names %s %d time(s) in comments", needle, inComments)
	}

	t.Run("no file in the repo names it", func(t *testing.T) {
		assertBreaklistFindsNothing(t, regexp.QuoteMeta(needle), needle+"() {\n", "cmd_reconcile_fork", "scripts/")
	})
}

var breaklistTotal = regexp.MustCompile(`^TOTAL (\d+) hits in \d+ files \(\d+ files scanned\)$`)

// breaklistHits runs the repo's breaklist and returns its hit lines. A self-check failure is fatal.
func breaklistHits(t *testing.T, pattern string, paths ...string) []string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "./internal/tools/breaklist", pattern}, paths...)...)
	cmd.Dir = repoRoot(t)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("breaklist %q: %v\n%s", pattern, err, errOut.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	m := breaklistTotal.FindStringSubmatch(lines[len(lines)-1])
	if m == nil {
		t.Fatalf("breaklist %q printed no TOTAL line:\n%s", pattern, out.String())
	}
	hits := lines[:len(lines)-1]
	if fmt.Sprint(len(hits)) != m[1] {
		t.Fatalf("breaklist %q printed %d hit lines for %s", pattern, len(hits), lines[len(lines)-1])
	}
	return hits
}

// assertBreaklistFindsNothing proves pattern finds a planted line, proves the whole-repo walk
// reaches each tree through control, then asserts the whole repo has no hit.
func assertBreaklistFindsNothing(t *testing.T, pattern, planted, control string, trees ...string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planted.txt"), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	if hits := breaklistHits(t, pattern, dir); len(hits) != 1 {
		t.Fatalf("planted: %q finds %d line(s) in %q, want 1", pattern, len(hits), planted)
	}
	controlHits := breaklistHits(t, control)
	for _, tree := range trees {
		if !slices.ContainsFunc(controlHits, func(h string) bool { return strings.HasPrefix(h, tree) }) {
			t.Fatalf("control: the whole-repo walk finds no %q under %s", control, tree)
		}
	}
	if hits := breaklistHits(t, pattern); len(hits) != 0 {
		t.Errorf("breaklist %q reports %d hit(s), want TOTAL 0:\n%s", pattern, len(hits), strings.Join(hits, "\n"))
	}
}

func TestNoFileCallsTheForkENVIRONMENTDecorative(t *testing.T) {
	pattern := `(?i)decorative` + ` in a`
	planted := "### `ENVIRONMENT` is " + "decorative" + " in a fork\n"
	assertBreaklistFindsNothing(t, pattern, planted, "RAILWAY_ENVIRONMENT_NAME", "docs/", "cmd/", "internal/")
}

// workflowJob is one jobs.<name> block of comment-stripped workflow lines.
type workflowJob struct {
	name  string
	lines []string
}

func workflowJobsOf(yaml string) []workflowJob {
	var jobs []workflowJob
	inJobs := false
	for _, line := range stripHashComments(strings.Split(yaml, "\n")) {
		if strings.HasPrefix(line, "jobs:") {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(trimmed, ":") {
			jobs = append(jobs, workflowJob{name: strings.TrimSuffix(trimmed, ":")})
			continue
		}
		if len(jobs) > 0 {
			jobs[len(jobs)-1].lines = append(jobs[len(jobs)-1].lines, line)
		}
	}
	return jobs
}

// workflowStep holds one step's scalar keys (a block run: joined by newlines) and its env map.
type workflowStep struct {
	index int
	keys  map[string]string
	env   map[string]string
}

func (j workflowJob) steps() []workflowStep {
	var raw [][]string
	inSteps := false
	for _, line := range j.lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		if !inSteps {
			inSteps = indent == 4 && trimmed == "steps:"
			continue
		}
		if trimmed == "" {
			continue
		}
		if indent <= 4 {
			break
		}
		if indent == 6 && strings.HasPrefix(trimmed, "- ") {
			raw = append(raw, nil)
		}
		if len(raw) > 0 {
			raw[len(raw)-1] = append(raw[len(raw)-1], line)
		}
	}
	steps := make([]workflowStep, len(raw))
	for i, lines := range raw {
		s := workflowStep{index: i, keys: map[string]string{}, env: map[string]string{}}
		lines[0] = strings.Replace(lines[0], "- ", "  ", 1)
		block := ""
		var run []string
		for _, line := range lines {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			trimmed := strings.TrimSpace(line)
			if indent > 8 {
				switch block {
				case "env":
					if k, v, ok := strings.Cut(trimmed, ":"); ok {
						s.env[strings.TrimSpace(k)] = strings.TrimSpace(v)
					}
				case "run":
					run = append(run, trimmed)
				}
				continue
			}
			k, v, _ := strings.Cut(trimmed, ":")
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch {
			case k == "env" && v == "":
				block = "env"
			case k == "run" && (strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">")):
				block = "run"
			default:
				block = ""
				s.keys[k] = v
			}
		}
		if len(run) > 0 {
			s.keys["run"] = strings.Join(run, "\n")
		}
		steps[i] = s
	}
	return steps
}

var (
	shellLeadWords  = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true, "!": true, "{": true, "(": true, "exec": true, "time": true}
	shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// invocations returns each simple command in run that names needle and is not an echo or printf.
func invocations(run, needle string) []string {
	run = strings.ReplaceAll(run, "\\\n", " ")
	var segs []string
	var cur strings.Builder
	var inS, inD bool
	flush := func() {
		segs = append(segs, strings.Join(strings.Fields(cur.String()), " "))
		cur.Reset()
	}
	for i := 0; i < len(run); i++ {
		c := run[i]
		switch {
		case c == '\\' && !inS && i+1 < len(run):
			cur.WriteByte(c)
			i++
			c = run[i]
		case c == '\'' && !inD:
			inS = !inS
		case c == '"' && !inS:
			inD = !inD
		case !inS && !inD && (c == ';' || c == '&' || c == '|' || c == '\n'):
			flush()
			continue
		}
		cur.WriteByte(c)
	}
	flush()
	var out []string
	for _, seg := range segs {
		if !strings.Contains(seg, needle) {
			continue
		}
		f := strings.Fields(seg)
		for len(f) > 0 && (shellLeadWords[f[0]] || shellAssignment.MatchString(f[0])) {
			f = f[1:]
		}
		if len(f) > 0 && (f[0] == "echo" || f[0] == "printf") {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// forkEnvironmentStepFaults reports each way dev-env.yml departs from one PR-only prepare-env step running set-fork-environment.
func forkEnvironmentStepFaults(devEnv string) []string {
	type site struct {
		job  string
		step workflowStep
		cmd  string
	}
	var sites []site
	for _, job := range workflowJobsOf(devEnv) {
		for _, s := range job.steps() {
			for _, cmd := range invocations(s.keys["run"], "set-fork-environment") {
				sites = append(sites, site{job.name, s, cmd})
			}
		}
	}
	if len(sites) != 1 {
		return []string{fmt.Sprintf("%d commands run set-fork-environment, want exactly 1", len(sites))}
	}
	s := sites[0]
	var faults []string
	if s.job != "prepare-env" {
		faults = append(faults, "set-fork-environment runs in job "+s.job+", not prepare-env")
	}
	if s.cmd != forkEnvironmentRunCmd {
		faults = append(faults, fmt.Sprintf("the command is %q, want %q", s.cmd, forkEnvironmentRunCmd))
	}
	if got := s.step.keys["if"]; got != prOnlyCondition {
		faults = append(faults, fmt.Sprintf("the step's if: is %q, want %q", got, prOnlyCondition))
	}
	if got := s.step.env["RAILWAY_API_TOKEN"]; got != "${{ secrets.RAILWAY_API_TOKEN }}" {
		faults = append(faults, fmt.Sprintf("the step's env RAILWAY_API_TOKEN is %q", got))
	}
	if got := s.step.env["ENV_ID"]; got != "${{ steps.resolve.outputs.environment_id }}" {
		faults = append(faults, fmt.Sprintf("the step's env ENV_ID is %q", got))
	}
	if _, ok := s.step.keys["continue-on-error"]; ok {
		faults = append(faults, "the step carries continue-on-error")
	}
	return faults
}

var gatewayUpload = regexp.MustCompile(`(^|\s)(\./)?scripts/ci/railway-up-ci\.sh\s+gateway$`)

// mockIssuerStampFaults reports each way dev-env.yml departs from one PR-only deploy-gateway
// stamp that precedes both gateway uploads.
func mockIssuerStampFaults(devEnv string) []string {
	var faults []string
	found := false
	for _, job := range workflowJobsOf(devEnv) {
		var stamps []workflowStep
		var uploads []int
		for _, s := range job.steps() {
			if len(invocations(s.keys["run"], "stamp-mock-issuer.sh")) > 0 {
				stamps = append(stamps, s)
			}
			for _, cmd := range invocations(s.keys["run"], "railway-up-ci.sh") {
				if gatewayUpload.MatchString(cmd) {
					uploads = append(uploads, s.index)
				}
			}
		}
		if job.name != "deploy-gateway" {
			if len(stamps) > 0 {
				faults = append(faults, "job "+job.name+" runs stamp-mock-issuer.sh")
			}
			continue
		}
		found = true
		if len(uploads) != 2 {
			faults = append(faults, fmt.Sprintf("deploy-gateway has %d `railway-up-ci.sh gateway` steps, want 2 (PR/push and dispatch)", len(uploads)))
		}
		if len(stamps) != 1 {
			faults = append(faults, fmt.Sprintf("deploy-gateway has %d stamp-mock-issuer.sh steps, want 1", len(stamps)))
			continue
		}
		if got := stamps[0].keys["if"]; got != prOnlyCondition {
			faults = append(faults, fmt.Sprintf("the stamp step's if: is %q, want %q", got, prOnlyCondition))
		}
		for _, u := range uploads {
			if stamps[0].index >= u {
				faults = append(faults, fmt.Sprintf("the stamp is step %d, not before the gateway upload at step %d", stamps[0].index, u))
			}
		}
	}
	if !found {
		faults = append(faults, "no deploy-gateway job")
	}
	return faults
}

const (
	fxAIFakeStep = "      - name: Force AI fake mode\n        if: github.event_name == 'pull_request'\n        env:\n          RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n          ENV_ID: ${{ steps.resolve.outputs.environment_id }}\n        run: bash scripts/ci/railway-env.sh set-ai-fake \"$ENV_ID\"\n"
	fxForkIf     = "        if: github.event_name == 'pull_request'\n"
	fxForkToken  = "          RAILWAY_API_TOKEN: ${{ secrets.RAILWAY_API_TOKEN }}\n"
	fxForkStep   = "      - name: Set the fork gateway ENVIRONMENT to development\n" + fxForkIf + "        env:\n" + fxForkToken + "          ENV_ID: ${{ steps.resolve.outputs.environment_id }}\n        run: bash scripts/ci/railway-env.sh set-fork-environment \"$ENV_ID\"\n"
	fxStampIf    = "        if: github.event_name == 'pull_request'\n"
	fxStampStep  = "      - name: Stamp the mock issuer into the PR build\n" + fxStampIf + "        run: sh scripts/ci/stamp-mock-issuer.sh\n"
	fxUpPush     = "      - name: railway up gateway (PR/push)\n        if: github.event_name != 'workflow_dispatch'\n        run: sh scripts/ci/railway-up-ci.sh gateway\n"
	fxUpDispatch = "      - name: railway up gateway (dispatch)\n        if: github.event_name == 'workflow_dispatch'\n        run: sh scripts/ci/railway-up-ci.sh gateway\n"
	fxHealthGate = "  health-gate:\n    needs: [deploy-gateway, prepare-env]\n    runs-on: ubuntu-latest\n    steps:\n      - name: Wait for gateway /healthz\n        run: |\n          if [ \"$mock\" != \"$want_mock\" ]; then\n            echo \"::error::mock_issuer is '$mock'; see scripts/ci/stamp-mock-issuer.sh and set-fork-environment\"\n            exit 1\n          fi\n"
	fxDevEnv     = "on: pull_request\njobs:\n  prepare-env:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n" + fxAIFakeStep + fxForkStep +
		"  deploy-gateway:\n    needs: [await-ci, prepare-env]\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n      - name: Stamp the build sha into the upload\n        run: sh scripts/ci/stamp-build-sha.sh \"${{ github.sha }}\"\n" +
		fxStampStep + fxUpPush + fxUpDispatch + fxHealthGate
)

// commentOut prefixes every line of block with `# `.
func commentOut(block string) string {
	return regexp.MustCompile(`(?m)^( *)(\S)`).ReplaceAllString(block, "$1# $2")
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDevEnvYmlWiresSetForkEnvironmentIntoPrepareEnv(t *testing.T) {
	for _, c := range []struct {
		name, yaml string
		bad        bool
	}{
		{"the planned step", fxDevEnv, false},
		{"no if:", strings.Replace(fxDevEnv, fxForkStep, strings.Replace(fxForkStep, fxForkIf, "", 1), 1), true},
		{"a push-and-PR if:", strings.Replace(fxDevEnv, fxForkStep, strings.Replace(fxForkStep, fxForkIf, "        if: github.event_name != 'workflow_dispatch'\n", 1), 1), true},
		{"no RAILWAY_API_TOKEN", strings.Replace(fxDevEnv, fxForkStep, strings.Replace(fxForkStep, fxForkToken, "", 1), 1), true},
		{"continue-on-error", strings.Replace(fxDevEnv, fxForkStep, fxForkStep+"        continue-on-error: true\n", 1), true},
		{"the step in deploy-gateway", strings.Replace(strings.Replace(fxDevEnv, fxForkStep, "", 1), fxStampStep, fxForkStep+fxStampStep, 1), true},
		{"a second run in health-gate", strings.Replace(fxDevEnv, "            exit 1\n", "            bash scripts/ci/railway-env.sh set-fork-environment \"$ENV_ID\"\n            exit 1\n", 1), true},
		{"the step commented out", strings.Replace(fxDevEnv, fxForkStep, commentOut(fxForkStep), 1), true},
	} {
		if c.bad && c.yaml == fxDevEnv {
			t.Fatalf("fixture %q: the edit did not apply", c.name)
		}
		if faults := forkEnvironmentStepFaults(c.yaml); (len(faults) > 0) != c.bad {
			t.Errorf("fixture %q: faults %v, want faults = %v", c.name, faults, c.bad)
		}
	}

	devEnv := readWorkflow(t, "dev-env.yml")
	// Control: the parser reads the set-ai-fake step's if: and env in the real prepare-env job.
	var control []workflowStep
	for _, job := range workflowJobsOf(devEnv) {
		for _, s := range job.steps() {
			if len(invocations(s.keys["run"], "set-ai-fake")) > 0 && job.name == "prepare-env" {
				control = append(control, s)
			}
		}
	}
	if len(control) != 1 || control[0].keys["if"] != prOnlyCondition || control[0].env["RAILWAY_API_TOKEN"] == "" {
		t.Fatalf("control: the parser does not read the set-ai-fake step in prepare-env (%d found); the scan is broken", len(control))
	}
	for _, f := range forkEnvironmentStepFaults(devEnv) {
		t.Errorf(".github/workflows/dev-env.yml: %s", f)
	}
}

func TestDevEnvYmlStampsTheMockIssuerOnPullRequestsOnly(t *testing.T) {
	for _, c := range []struct {
		name, yaml string
		bad        bool
	}{
		{"the planned step", fxDevEnv, false},
		{"the stamp after both uploads", strings.Replace(strings.Replace(fxDevEnv, fxStampStep, "", 1), fxUpDispatch, fxUpDispatch+fxStampStep, 1), true},
		{"the stamp between the uploads", strings.Replace(strings.Replace(fxDevEnv, fxStampStep, "", 1), fxUpDispatch, fxStampStep+fxUpDispatch, 1), true},
		{"no if:", strings.Replace(fxDevEnv, fxStampStep, strings.Replace(fxStampStep, fxStampIf, "", 1), 1), true},
		{"a push-and-PR if:", strings.Replace(fxDevEnv, fxStampStep, strings.Replace(fxStampStep, fxStampIf, "        if: github.event_name != 'workflow_dispatch'\n", 1), 1), true},
		{"a stamp in deploy-context", fxDevEnv + "  deploy-context:\n    runs-on: ubuntu-latest\n    steps:\n" + fxStampStep, true},
		{"the stamp commented out", strings.Replace(fxDevEnv, fxStampStep, commentOut(fxStampStep), 1), true},
	} {
		if c.bad && c.yaml == fxDevEnv {
			t.Fatalf("fixture %q: the edit did not apply", c.name)
		}
		if faults := mockIssuerStampFaults(c.yaml); (len(faults) > 0) != c.bad {
			t.Errorf("fixture %q: faults %v, want faults = %v", c.name, faults, c.bad)
		}
	}

	for _, f := range mockIssuerStampFaults(readWorkflow(t, "dev-env.yml")) {
		t.Errorf(".github/workflows/dev-env.yml: %s", f)
	}
}

// forkSelfTestJobFaults reports each way railway-invariants.yml departs from one fork-environment-self-test job running the self-test.
func forkSelfTestJobFaults(yaml string) []string {
	var jobs, faults []string
	for _, job := range workflowJobsOf(yaml) {
		for _, s := range job.steps() {
			for _, cmd := range invocations(s.keys["run"], "set-fork-environment") {
				if cmd != forkSelfTestRunCmd {
					faults = append(faults, fmt.Sprintf("job %s runs %q", job.name, cmd))
					continue
				}
				jobs = append(jobs, job.name)
			}
		}
	}
	switch {
	case len(jobs) != 1:
		faults = append(faults, fmt.Sprintf("%d steps run %q, want exactly 1", len(jobs), forkSelfTestRunCmd))
	case jobs[0] != "fork-environment-self-test":
		faults = append(faults, fmt.Sprintf("%q runs in job %s, not fork-environment-self-test", forkSelfTestRunCmd, jobs[0]))
	}
	return faults
}

func TestRailwayInvariantsYmlWiresSetForkEnvironmentSelfTest(t *testing.T) {
	const aiJob = "  ai-fake-self-test:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n      - name: Run set-ai-fake --self-test\n        run: bash scripts/ci/railway-env.sh set-ai-fake --self-test\n"
	const forkRun = "        run: " + forkSelfTestRunCmd + "\n"
	const forkJob = "  fork-environment-self-test:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n      - name: Run set-fork-environment --self-test\n" + forkRun
	good := "on:\n  pull_request:\njobs:\n" + aiJob + forkJob
	for _, c := range []struct {
		name, yaml string
		bad        bool
	}{
		{"the planned job", good, false},
		{"the run commented out", strings.Replace(good, forkRun, commentOut(forkRun), 1), true},
		{"the job renamed", strings.Replace(good, "  fork-environment-self-test:\n", "  fork-env-check:\n", 1), true},
		{"a second run in ai-fake-self-test", strings.Replace(good, aiJob, aiJob+"      - "+strings.TrimSpace(forkRun)+"\n", 1), true},
		{"no job", "on:\n  pull_request:\njobs:\n" + aiJob, true},
	} {
		if c.bad && c.yaml == good {
			t.Fatalf("fixture %q: the edit did not apply", c.name)
		}
		if faults := forkSelfTestJobFaults(c.yaml); (len(faults) > 0) != c.bad {
			t.Errorf("fixture %q: faults %v, want faults = %v", c.name, faults, c.bad)
		}
	}

	inv := readWorkflow(t, "railway-invariants.yml")
	// Control: the parser finds the ai-fake self-test in its own job.
	var control []string
	for _, job := range workflowJobsOf(inv) {
		for _, s := range job.steps() {
			if slices.Contains(invocations(s.keys["run"], "set-ai-fake"), "bash scripts/ci/railway-env.sh set-ai-fake --self-test") {
				control = append(control, job.name)
			}
		}
	}
	if !slices.Equal(control, []string{"ai-fake-self-test"}) {
		t.Fatalf("control: the parser finds the set-ai-fake self-test in jobs %v; the scan is broken", control)
	}
	for _, f := range forkSelfTestJobFaults(inv) {
		t.Errorf(".github/workflows/railway-invariants.yml: %s", f)
	}
}

// goPathsFilter returns the `go:` list of the changes job's paths-filter block.
func goPathsFilter(ciYAML string) []string {
	var job []string
	for _, j := range workflowJobsOf(ciYAML) {
		if j.name == "changes" {
			job = j.lines
		}
	}
	inFilters, goIndent := false, -1
	var items []string
	for _, line := range job {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case !inFilters:
			inFilters = strings.HasPrefix(trimmed, "filters:")
		case goIndent < 0:
			if trimmed == "go:" {
				goIndent = indent
			}
		case trimmed == "":
		case indent <= goIndent:
			return items
		default:
			if item, ok := strings.CutPrefix(trimmed, "- "); ok {
				items = append(items, strings.Trim(strings.TrimSpace(item), `'"`))
			}
		}
	}
	return items
}

var deployWorkflows = []string{".github/workflows/dev-env.yml", ".github/workflows/railway-invariants.yml"}

func TestCIGoFilterCoversTheDeployWorkflows(t *testing.T) {
	missing := func(yaml string) []string {
		items := goPathsFilter(yaml)
		var out []string
		for _, w := range deployWorkflows {
			if !slices.Contains(items, w) {
				out = append(out, w)
			}
		}
		return out
	}
	const devEnvItem = "              - '.github/workflows/dev-env.yml'\n"
	const invItem = "              - '.github/workflows/railway-invariants.yml'\n"
	const migrations = "            migrations:\n              - 'migrations/**'\n"
	good := "on: push\njobs:\n  changes:\n    runs-on: ubuntu-latest\n    outputs:\n      go: ${{ steps.filter.outputs.go }}\n    steps:\n      - uses: dorny/paths-filter@v3\n        id: filter\n        with:\n          filters: |\n            go:\n              - 'cmd/**'\n              - '.github/workflows/ci.yml'\n" +
		devEnvItem + invItem + migrations
	for _, c := range []struct {
		name, yaml string
		want       int
	}{
		{"both listed", good, 0},
		{"dev-env.yml missing", strings.Replace(good, devEnvItem, "", 1), 1},
		{"railway-invariants.yml missing", strings.Replace(good, invItem, "", 1), 1},
		{"both commented out", strings.Replace(good, devEnvItem+invItem, commentOut(devEnvItem+invItem), 1), 2},
		{"both under migrations", strings.Replace(strings.Replace(good, devEnvItem+invItem, "", 1), migrations, migrations+devEnvItem+invItem, 1), 2},
	} {
		if c.want > 0 && c.yaml == good {
			t.Fatalf("fixture %q: the edit did not apply", c.name)
		}
		if got := missing(c.yaml); len(got) != c.want {
			t.Errorf("fixture %q: missing %v, want %d", c.name, got, c.want)
		}
	}

	ci := readWorkflow(t, "ci.yml")
	// Control: the parser reads the real go filter.
	if items := goPathsFilter(ci); !slices.Contains(items, "cmd/**") || !slices.Contains(items, ".github/workflows/ci.yml") {
		t.Fatalf("control: the go filter parses as %v, without cmd/** or ci.yml; the scan is broken", items)
	}
	for _, w := range missing(ci) {
		t.Errorf("ci.yml's go paths filter does not list %s; an edit to it alone skips the go job that holds its pins", w)
	}
}
