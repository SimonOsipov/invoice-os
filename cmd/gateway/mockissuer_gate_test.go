package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// isPRFromEvent is the health-gate's only source of IS_PR.
const isPRFromEvent = "IS_PR: ${{ github.event_name == 'pull_request' }}"

// devEnvHealthGate returns dev-env.yml's comment-stripped health-gate job. A job
// that never reads .build is not the gate, and every scan below would read nothing.
func devEnvHealthGate(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "dev-env.yml"))
	if err != nil {
		t.Fatal(err)
	}
	job := jobBlock(yamlCode(string(raw)), "health-gate")
	if len(job) < 20 || !strings.Contains(runText(job), ".build // empty") {
		t.Fatalf("dev-env.yml's health-gate job is %d line(s) and never reads .build; the scan would read nothing", len(job))
	}
	return job
}

// gateStmt is one shell statement and the 0-based line it starts on.
type gateStmt struct {
	text string
	line int
}

// gateStmts splits src at unquoted `;` and newlines, drops `#` comments, and
// splits a leading then/else/do off a command that shares its line.
func gateStmts(src string) []gateStmt {
	var out []gateStmt
	for i, line := range strings.Split(src, "\n") {
		var cur strings.Builder
		flush := func() {
			s := strings.TrimSpace(cur.String())
			cur.Reset()
			for s != "" {
				kw, rest, _ := strings.Cut(s, " ")
				rest = strings.TrimSpace(rest)
				if (kw != "then" && kw != "else" && kw != "do") || rest == "" {
					out = append(out, gateStmt{s, i})
					return
				}
				out = append(out, gateStmt{kw, i})
				s = rest
			}
		}
		var inS, inD bool
	scan:
		for j := 0; j < len(line); j++ {
			c := line[j]
			switch {
			case c == '\\' && !inS && j+1 < len(line):
				cur.WriteByte(c)
				j++
				c = line[j]
			case c == '\'' && !inD:
				inS = !inS
			case c == '"' && !inS:
				inD = !inD
			case inS || inD:
			case c == ';':
				flush()
				continue
			case c == '#' && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t'):
				break scan
			}
			cur.WriteByte(c)
		}
		flush()
	}
	return out
}

func stmtWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// topLevelIfs returns, as runnable shell in source order, each if-block of src
// that no other if-block encloses and that keep accepts.
func topLevelIfs(src string, keep func(b []gateStmt) bool) string {
	lines := strings.Split(src, "\n")
	stmts := gateStmts(src)
	var parts []string
	depth, start := 0, 0
	for j, s := range stmts {
		switch stmtWord(s.text) {
		case "if":
			if depth == 0 {
				start = j
			}
			depth++
		case "fi":
			if depth == 0 {
				continue
			}
			if depth--; depth == 0 && keep(stmts[start:j+1]) {
				for _, l := range lines[stmts[start].line : s.line+1] {
					parts = append(parts, strings.TrimSpace(l))
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// mockGateScript returns the top-level blocks that assign want_mock or test "$mock".
func mockGateScript(run string) string {
	return topLevelIfs(run, func(b []gateStmt) bool {
		return slices.ContainsFunc(b, func(s gateStmt) bool {
			w := stmtWord(s.text)
			return strings.HasPrefix(s.text, "want_mock=") || ((w == "if" || w == "elif") && strings.Contains(s.text, `"$mock"`))
		})
	})
}

// probeScript returns the top-level `if [ "$IS_PR" != "true" ]` blocks that run curl.
func probeScript(run string) string {
	return topLevelIfs(run, func(b []gateStmt) bool {
		return strings.HasPrefix(b[0].text, `if [ "$IS_PR" != "true" ]`) &&
			slices.ContainsFunc(b, func(s gateStmt) bool { return strings.Contains(s.text, "curl ") })
	})
}

// isPRScopeFaults reports each step whose run carries needle but where IS_PR is not set
// from the event, at step or job level.
func isPRScopeFaults(job []string, needle string) []string {
	jobLevel := false
	for _, l := range job {
		if strings.TrimSpace(l) == "steps:" {
			break
		}
		jobLevel = jobLevel || strings.TrimSpace(l) == isPRFromEvent
	}
	var faults []string
	found := false
	for _, step := range jobSteps(job) {
		if !strings.Contains(runText(step), needle) {
			continue
		}
		found = true
		if !jobLevel && !slices.ContainsFunc(step, func(l string) bool { return strings.TrimSpace(l) == isPRFromEvent }) {
			name, _ := stepKey(step, "name")
			faults = append(faults, "step "+strconv.Quote(name)+" runs "+needle+" without "+isPRFromEvent)
		}
	}
	if !found {
		faults = append(faults, "no health-gate step runs "+needle)
	}
	return faults
}

var mockGateFragments = []string{`want_mock=on`, `want_mock=absent`, `[ "$mock" != "$want_mock" ]`}

// mockGateFaults reports each way a workflow's health-gate departs from reading
// mock_issuer out of the build-matched body and asserting it per target.
func mockGateFaults(workflow string) []string {
	job := jobBlock(yamlCode(workflow), "health-gate")
	if len(job) == 0 {
		return []string{"no health-gate job"}
	}
	run := runText(job)
	lines := strings.Split(run, "\n")
	find := func(pred func(string) bool, from int) int {
		for i := max(from, 0); i < len(lines); i++ {
			if pred(lines[i]) {
				return i
			}
		}
		return -1
	}
	build := find(func(l string) bool { return strings.Contains(l, ".build // empty") }, 0)
	mock := find(func(l string) bool { return strings.Contains(l, ".mock_issuer // empty") }, 0)
	brk := find(func(l string) bool { return l == "break" }, build)

	var faults []string
	switch {
	case mock < 0:
		faults = append(faults, "the health-gate never reads .mock_issuer // empty")
	case build < 0 || brk < 0:
		faults = append(faults, "the health-gate has no .build read and break to place the mock_issuer read between")
	default:
		if mock < build || mock > brk {
			faults = append(faults, "the mock_issuer read sits outside the block that matched .build, so it can observe the old container mid-rollout")
		}
		if !strings.HasPrefix(lines[mock], "mock=") || !strings.Contains(lines[mock], `"$body"`) || strings.Contains(lines[mock], "curl") {
			faults = append(faults, "the mock_issuer read is not `mock=$(… \"$body\" …)`: it must parse the build-matched body, not fetch its own")
		}
	}
	for _, f := range mockGateFragments {
		if !strings.Contains(run, f) {
			faults = append(faults, "the health-gate carries no "+f)
		}
	}
	return append(faults, isPRScopeFaults(job, `[ "$mock" != "$want_mock" ]`)...)
}

// fxHealthGate wraps script as the run of a one-step health-gate job.
func fxHealthGate(stepEnv, script string) string {
	var b strings.Builder
	b.WriteString("on: push\njobs:\n  health-gate:\n    runs-on: ubuntu-latest\n    steps:\n      - name: Wait for gateway /healthz\n        env:\n          EXPECTED_BUILD: ${{ github.sha }}\n")
	b.WriteString(stepEnv)
	b.WriteString("        run: |\n")
	for _, l := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		b.WriteString("          " + l + "\n")
	}
	b.WriteString("  deploy-context:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo next\n")
	return b.String()
}

const (
	fxIsPREnv   = "          " + isPRFromEvent + "\n"
	fxMockRead  = `    mock=$(printf '%s' "$body" | jq -r '.mock_issuer // empty' 2>/dev/null || echo '')` + "\n"
	fxWaitLoopA = `for _ in $(seq 1 180); do
  body=$(curl -fsS --connect-timeout 5 --max-time 10 "$GATEWAY_URL/healthz" 2>/dev/null || echo '')
  seen=$(printf '%s' "$body" | jq -r '.build // empty' 2>/dev/null || echo '')
  if [ -n "$seen" ] && [ "$seen" = "$EXPECTED_BUILD" ]; then
    purge=$(printf '%s' "$body" | jq -r '.demo_purge // empty' 2>/dev/null || echo '')
`
	fxWaitLoopB = `    break
  fi
  sleep 5
done
`
	fxExpect = `if [ "$IS_PR" = "true" ]; then
  want_purge=true;  want_mock=on
else
  want_purge=false; want_mock=absent
fi
`
	fxMockCheck = `if [ "$mock" != "$want_mock" ]; then
  echo "::error::gateway reports mock_issuer='${mock:-none}', want '$want_mock'"
  exit 1
fi
`
	fxMockGate = fxWaitLoopA + fxMockRead + fxWaitLoopB + fxExpect + fxMockCheck
)

func TestMockIssuerIsAssertedByTheDevEnvGate(t *testing.T) {
	good := fxHealthGate(fxIsPREnv, fxMockGate)
	t.Run("fixtures", func(t *testing.T) {
		if faults := mockGateFaults(good); len(faults) != 0 {
			t.Fatalf("the planned gate reports %v", faults)
		}
		for _, bad := range []struct{ name, src string }{
			{"its own curl", strings.Replace(good, `printf '%s' "$body" | jq -r '.mock_issuer`, `curl -fsS "$GATEWAY_URL/healthz" | jq -r '.mock_issuer`, 1)},
			{"read after the break", fxHealthGate(fxIsPREnv, fxWaitLoopA+fxWaitLoopB+strings.TrimPrefix(fxMockRead, "    ")+fxExpect+fxMockCheck)},
			{"no want_mock=absent", strings.Replace(good, "want_mock=absent", "want_mock=off", 1)},
			{"no IS_PR in the step", fxHealthGate("", fxMockGate)},
			{"assertion commented out", fxHealthGate(fxIsPREnv, fxWaitLoopA+fxMockRead+fxWaitLoopB+fxExpect+"# "+strings.ReplaceAll(strings.TrimSuffix(fxMockCheck, "\n"), "\n", "\n# ")+"\n")},
		} {
			if bad.src == good {
				t.Fatalf("fixture %q: the edit did not apply", bad.name)
			}
			if faults := mockGateFaults(bad.src); len(faults) == 0 {
				t.Errorf("fixture %q: no fault reported", bad.name)
			}
		}
	})

	job := devEnvHealthGate(t)
	// Control: the step that reads the build today sets IS_PR from the event.
	if faults := isPRScopeFaults(job, ".build // empty"); len(faults) != 0 {
		t.Fatalf("control: %v; the step parse is broken", faults)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "dev-env.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range mockGateFaults(string(raw)) {
		t.Errorf(".github/workflows/dev-env.yml: %s", f)
	}
}

// runGate runs block under bash -e, as a workflow `run:` step does, with only env set.
func runGate(t *testing.T, block string, env ...string) (code int, out string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-e", "-c", block)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "EXPECTED_BUILD=deadbeef"}, env...)
	b, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(b)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running a gate block with %v: %v (output %q)", env, err, b)
	}
	return exitErr.ExitCode(), string(b)
}

// mockOutcomes: a PR build is tagged and ENVIRONMENT=development, so only "on"
// passes there; production's binary is untagged, so only "absent" passes there.
var mockOutcomes = []struct {
	isPR, mock string
	wantExit   int
}{
	{"true", "on", 0}, {"true", "off", 1}, {"true", "absent", 1}, {"true", "", 1},
	{"false", "absent", 0}, {"false", "on", 1}, {"false", "off", 1}, {"false", "", 1},
}

func TestMockIssuerGateFailsOffItsDirection(t *testing.T) {
	t.Run("control needle", func(t *testing.T) {
		for _, c := range mockOutcomes {
			if got, out := runGate(t, fxExpect+fxMockCheck, "IS_PR="+c.isPR, "mock="+c.mock); got != c.wantExit {
				t.Fatalf("a hand-written gate exits %d for IS_PR=%s mock=%q, want %d; the runner cannot observe the outcome (output %q)", got, c.isPR, c.mock, c.wantExit, out)
			}
			inverted := strings.Replace(fxMockCheck, `"$mock" != "$want_mock"`, `"$mock" = "$want_mock"`, 1)
			if got, _ := runGate(t, fxExpect+inverted, "IS_PR="+c.isPR, "mock="+c.mock); got == c.wantExit {
				t.Fatalf("an INVERTED gate also exits %d for IS_PR=%s mock=%q; the runner is not reading the block it was given", got, c.isPR, c.mock)
			}
		}
		if got := mockGateScript(fxMockGate); got != trimLines(strings.TrimSuffix(fxExpect+fxMockCheck, "\n")) {
			t.Fatalf("the extractor returns %q from the planned run text, want the expectation and assertion blocks", got)
		}
	})

	block := mockGateScript(runText(devEnvHealthGate(t)))
	if block == "" {
		t.Errorf(`dev-env.yml's health-gate carries no if-block on "$mock"; running an empty gate below`)
	}
	for _, c := range mockOutcomes {
		if got, out := runGate(t, block, "IS_PR="+c.isPR, "mock="+c.mock); got != c.wantExit {
			t.Errorf("IS_PR=%s mock_issuer=%q exits %d, want %d (output %q)", c.isPR, c.mock, got, c.wantExit, out)
		}
	}
}

const probeGatewayURL = "http://gateway.invalid"

// probeRoutes are the two mint routes and the method each is probed with.
var probeRoutes = []struct{ name, method, path string }{
	{"jwks", "GET", "/.well-known/jwks.json"},
	{"login", "POST", "/auth/login"},
}

// probeShim is a curl that answers each route from a scripted code list, the last
// code repeating, and a sleep that returns at once. Both log their arguments.
const probeShim = `#!/bin/sh
route=unrouted
fail=0
for a in "$@"; do
  case "$a" in
    */.well-known/jwks.json) route=jwks ;;
    */auth/login) route=login ;;
    --fail|--fail-with-body|-f*|-[!-]*f*) fail=1 ;;
  esac
done
echo "$route $*" >> "$SHIM_DIR/curl.log"
if [ "$route" = unrouted ]; then printf 000; exit 7; fi
n=$(( $(cat "$SHIM_DIR/$route.n" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$SHIM_DIR/$route.n"
code=$(awk -v n="$n" '{ print ((n > NF) ? $NF : $n) }' "$SHIM_DIR/$route.codes")
printf '%s' "$code"
if [ "$code" = 000 ]; then exit 7; fi
if [ "$fail" = 1 ] && [ "$code" -ge 400 ]; then exit 22; fi
exit 0
`

type probeRun struct {
	code   int
	out    string
	calls  map[string][][]string
	sleeps []string
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// runProbe runs block with the shims first on PATH and codes scripted per route.
func runProbe(t *testing.T, block, isPR string, codes map[string][]string) probeRun {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"curl": probeShim, "sleep": "#!/bin/sh\necho \"$*\" >> \"$SHIM_DIR/sleep.log\"\n"}
	for route, c := range codes {
		files[route+".codes"] = strings.Join(c, " ") + "\n"
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	code, out := runGate(t, block, "PATH="+dir+":/usr/bin:/bin", "SHIM_DIR="+dir, "IS_PR="+isPR, "GATEWAY_URL="+probeGatewayURL)
	r := probeRun{code: code, out: out, calls: map[string][][]string{}, sleeps: readLog(t, filepath.Join(dir, "sleep.log"))}
	for _, l := range readLog(t, filepath.Join(dir, "curl.log")) {
		f := strings.Fields(l)
		r.calls[f[0]] = append(r.calls[f[0]], f[1:])
	}
	return r
}

func hasPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

type probeRow struct {
	name        string
	jwks, login []string
	wantExit    int
	calls       map[string][2]int // route -> [min, max] attempts
	exhausted   string            // the route that must use the whole window; "any" for either
	lastCode    string            // the code a failure must print
}

func repeat(code string) []string { return []string{code} }

var probeRows = []probeRow{
	{"both answer 404 at once", repeat("404"), repeat("404"), 0, map[string][2]int{"jwks": {1, 1}, "login": {1, 1}}, "", ""},
	{"jwks drains 200,200,404", []string{"200", "200", "404"}, repeat("404"), 0, map[string][2]int{"jwks": {3, 3}, "login": {1, 1}}, "", ""},
	{"login drains 403,404", repeat("404"), []string{"403", "404"}, 0, map[string][2]int{"jwks": {1, 1}, "login": {2, 2}}, "", ""},
	{"unreachable, then 404", []string{"000", "404"}, []string{"000", "404"}, 0, map[string][2]int{"jwks": {2, 2}, "login": {2, 2}}, "", ""},
	{"jwks answers 200 for 36 attempts", repeat("200"), repeat("404"), 1, map[string][2]int{"jwks": {36, 36}, "login": {0, 1}}, "jwks", "200"},
	{"login answers 405 for 36 attempts", repeat("404"), repeat("405"), 1, map[string][2]int{"jwks": {0, 1}, "login": {36, 36}}, "login", "405"},
	{"unreachable for 36 attempts", repeat("000"), repeat("000"), 1, map[string][2]int{"jwks": {0, 36}, "login": {0, 36}}, "any", "000"},
}

// probeRowFaults runs one scripted row against block and reports every departure
// from the 404-only, 36 x 5 s, bounded-curl contract.
func probeRowFaults(t *testing.T, block string, row probeRow) []string {
	t.Helper()
	r := runProbe(t, block, "false", map[string][]string{"jwks": row.jwks, "login": row.login})
	var faults []string
	add := func(format string, a ...any) { faults = append(faults, row.name+": "+fmt.Sprintf(format, a...)) }

	if r.code != row.wantExit {
		add("exit %d, want %d (output %q)", r.code, row.wantExit, r.out)
	}
	if n := len(r.calls["unrouted"]); n != 0 {
		add("%d curl call(s) to neither mint route", n)
	}
	most := 0
	for _, rt := range probeRoutes {
		calls := r.calls[rt.name]
		most = max(most, len(calls))
		if want := row.calls[rt.name]; len(calls) < want[0] || len(calls) > want[1] {
			add("%s %s probed %d time(s), want %d..%d", rt.method, rt.path, len(calls), want[0], want[1])
		}
		for _, args := range calls {
			for _, p := range [][2]string{{"-X", rt.method}, {"--connect-timeout", "5"}, {"--max-time", "10"}, {"-o", "/dev/null"}, {"-w", "%{http_code}"}} {
				if !hasPair(args, p[0], p[1]) {
					add("a %s call lacks `%s %s`: %v", rt.path, p[0], p[1], args)
				}
			}
			if !slices.Contains(args, probeGatewayURL+rt.path) {
				add("a %s call does not target %s: %v", rt.path, probeGatewayURL+rt.path, args)
			}
		}
	}
	for _, s := range r.sleeps {
		if s != "5" {
			add("sleep %q between attempts, want 5", s)
		}
	}
	var errLines []string
	for _, l := range strings.Split(r.out, "\n") {
		if strings.Contains(l, "::error::") {
			errLines = append(errLines, l)
		}
	}
	if row.exhausted == "" {
		if len(errLines) != 0 {
			add("a passing probe printed %q", errLines)
		}
		return faults
	}
	if most != 36 {
		add("the busiest route was probed %d time(s), want the full 36", most)
	}
	if len(r.sleeps) < 35 {
		add("%d sleep(s) across an exhausted window, want at least 35 x 5 s", len(r.sleeps))
	}
	named := slices.ContainsFunc(errLines, func(l string) bool {
		if !strings.Contains(l, row.lastCode) {
			return false
		}
		for _, rt := range probeRoutes {
			if (row.exhausted == rt.name || row.exhausted == "any") && strings.Contains(l, rt.path) {
				return true
			}
		}
		return false
	})
	if !named {
		add("no ::error:: line names the route and its last code %s: %q", row.lastCode, errLines)
	}
	return faults
}

const referenceProbe = `if [ "$IS_PR" != "true" ]; then
  for probe in "GET /.well-known/jwks.json" "POST /auth/login"; do
    method=${probe%% *}
    path=${probe#* }
    code=""
    for _ in $(seq 1 36); do
      code=$(curl -s --connect-timeout 5 --max-time 10 -o /dev/null -w '%{http_code}' -X "$method" "$GATEWAY_URL$path" || true)
      if [ "$code" = "404" ]; then
        break
      fi
      sleep 5
    done
    if [ "$code" != "404" ]; then
      echo "::error::$method $path answered $code, not 404, for 36 attempts"
      exit 1
    fi
  done
fi`

func TestMintRoutesAreProbedOnThePersistentEnvironment(t *testing.T) {
	t.Run("control needle", func(t *testing.T) {
		wrapped := fxExpect + fxMockCheck + referenceProbe + "\necho done\n"
		if got := probeScript(wrapped); got != trimLines(referenceProbe) {
			t.Fatalf("the extractor returns %q from a run text holding the reference probe", got)
		}
		for _, row := range probeRows {
			if faults := probeRowFaults(t, referenceProbe, row); len(faults) != 0 {
				t.Fatalf("the reference probe reports %v; the harness cannot see a correct block", faults)
			}
		}
		acceptsAny := strings.Replace(referenceProbe, `if [ "$code" = "404" ]; then`, `if [ -n "$code" ]; then`, 1)
		if faults := probeRowFaults(t, acceptsAny, probeRows[4]); len(faults) == 0 {
			t.Fatal("a probe that accepts any code reports clean on the 200 x 36 row; the harness cannot find a planted violation")
		}
		if r := runProbe(t, "curl -X GET \"$GATEWAY_URL/.well-known/jwks.json\"; sleep 5", "true", map[string][]string{"jwks": repeat("404")}); len(r.calls["jwks"]) != 1 || !slices.Equal(r.sleeps, []string{"5"}) {
			t.Fatalf("control: the curl and sleep shims are not first on PATH (calls %v, sleeps %v)", r.calls, r.sleeps)
		}
	})

	job := devEnvHealthGate(t)
	block := probeScript(runText(job))
	if block == "" {
		t.Errorf(`dev-env.yml's health-gate carries no if [ "$IS_PR" != "true" ] block that runs curl; running an empty probe below`)
	} else {
		for _, f := range isPRScopeFaults(job, "/.well-known/jwks.json") {
			t.Errorf(".github/workflows/dev-env.yml: %s", f)
		}
	}

	for _, row := range probeRows {
		for _, f := range probeRowFaults(t, block, row) {
			t.Errorf("IS_PR=false, %s", f)
		}
	}

	r := runProbe(t, block, "true", map[string][]string{"jwks": repeat("404"), "login": repeat("404")})
	if r.code != 0 || len(r.calls) != 0 || len(r.sleeps) != 0 {
		t.Errorf("IS_PR=true: exit %d, curl calls %v, sleeps %v; a PR fork serves the mint routes by design, so the probe must not run there", r.code, r.calls, r.sleeps)
	}
}

func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(lines, "\n")
}
