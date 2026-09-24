package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// goJobRunsJWKSUnderRace reports whether one command line of one `go` job step
// runs the TestJWKS_ tests under -race; needles split across steps do not count.
func goJobRunsJWKSUnderRace(ciYAML string) bool {
	for _, step := range jobSteps(jobBlock(yamlCode(ciYAML), "go")) {
		for _, line := range strings.Split(runText(step), "\n") {
			if strings.Contains(line, "go test") && strings.Contains(line, "-race") &&
				strings.Contains(line, "TestJWKS_") && strings.Contains(line, "./internal/platform/auth/") {
				return true
			}
		}
	}
	return false
}

func TestCIGoJobRunsJWKSTestsUnderRace(t *testing.T) {
	const head = "on: push\njobs:\n  go:\n    runs-on: ubuntu-latest\n    steps:\n      - name: Test\n        run: go test ./...\n"
	const step = "      - name: JWKS under race\n        run: go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const blockStep = "      - name: JWKS under race\n        run: |\n          go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const noRace = "      - name: JWKS\n        run: go test -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const split = "      - run: go test -race ./...\n      - run: go test -run 'TestJWKS_' ./internal/platform/auth/\n"
	const commented = "      # - run: go test -race -count=1 -run 'TestJWKS_' ./internal/platform/auth/\n"
	const tail = "  docker-canary:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo canary\n"

	for _, c := range []struct {
		name string
		yaml string
		want bool
	}{
		{"with the step", head + step + tail, true},
		{"with the step as a block scalar", head + blockStep + tail, true},
		{"without the step", head + tail, false},
		{"without -race", head + noRace + tail, false},
		{"needles split across steps", head + split + tail, false},
		{"step commented out", head + commented + tail, false},
		{"step in another job", head + tail + step, false},
	} {
		if got := goJobRunsJWKSUnderRace(c.yaml); got != c.want {
			t.Errorf("fixture %q: runs JWKS under race = %v, want %v", c.name, got, c.want)
		}
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Control: the scan finds the real go job's steps before judging them.
	steps := jobSteps(jobBlock(yamlCode(string(raw)), "go"))
	if len(steps) == 0 {
		t.Fatal("found no steps in the ci.yml go job; the scan is broken")
	}
	if !goJobRunsJWKSUnderRace(string(raw)) {
		t.Errorf(".github/workflows/ci.yml: no `go` job step runs `go test -race -run 'TestJWKS_' ./internal/platform/auth/`")
	}
}

const idpJobIf = "needs.changes.outputs.idp == 'true'"

// jobIfExpr returns a job's own if: expression without the ${{ }} wrapper.
func jobIfExpr(block []string) string {
	for _, l := range block {
		if v, ok := strings.CutPrefix(l, "    if:"); ok {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "${{"), "}}"))
		}
	}
	return ""
}

// changesOutputs returns the job's outputs: entries, one trimmed line each.
func changesOutputs(block []string) []string {
	var out []string
	in := false
	for _, l := range block {
		indent := len(l) - len(strings.TrimLeft(l, " "))
		switch {
		case strings.TrimSpace(l) == "":
		case indent == 4:
			in = strings.TrimSpace(l) == "outputs:"
		case in && indent == 6:
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

// filterPaths returns the paths-filter entries listed under name: in the changes job.
func filterPaths(block []string, name string) []string {
	var paths []string
	keyIndent := -1
	for _, l := range block {
		trimmed := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if trimmed == "" {
			continue
		}
		if keyIndent >= 0 {
			if indent <= keyIndent {
				keyIndent = -1
			} else if item, ok := strings.CutPrefix(trimmed, "- "); ok {
				paths = append(paths, strings.Trim(strings.TrimSpace(item), `"'`))
				continue
			}
		}
		if trimmed == name+":" && indent > 6 {
			keyIndent = indent
		}
	}
	return paths
}

// aggregateFailsOnFailureAndCancel is aggregateFailsOn that also requires a cancelled
// job to fail the roll-up; `!= "success"` covers both.
func aggregateFailsOnFailureAndCancel(run, job string) bool {
	lines := strings.Split(run, "\n")
	ref := "${{ needs." + job + ".result }}"
	for i, l := range lines {
		if !strings.HasPrefix(l, "if ") || !strings.Contains(l, ref) {
			continue
		}
		both := strings.Contains(l, `= "failure"`) && strings.Contains(l, `= "cancelled"`)
		if !both && !strings.Contains(l, `!= "success"`) {
			continue
		}
		for _, next := range lines[i:] {
			if strings.Contains(next, "exit 1") {
				return true
			}
			if next == "fi" {
				break
			}
		}
	}
	return false
}

// idpGateStep reports whether one command runs TestIdP on the auth package through the skip gate.
func idpGateStep(block []string) bool {
	for _, line := range strings.Split(runText(block), "\n") {
		if strings.Contains(line, "scripts/ci/rls-test-gate.sh") &&
			(strings.Contains(line, "-run TestIdP") || strings.Contains(line, "-run 'TestIdP")) &&
			strings.Contains(line, "./internal/platform/auth/") {
			return true
		}
	}
	return false
}

func idpJobProblems(ciYAML string) []string {
	lines := yamlCode(ciYAML)
	var problems []string

	changes := jobBlock(lines, "changes")
	if len(changes) == 0 {
		return []string{"no changes job"}
	}
	if !slices.Contains(changesOutputs(changes), "idp: ${{ steps.filter.outputs.idp }}") {
		problems = append(problems, "changes.outputs has no `idp: ${{ steps.filter.outputs.idp }}`; the idp job's if: would read an empty output and never run")
	}
	paths := filterPaths(changes, "idp")
	for _, want := range []string{"sidecar/auth/**", "internal/platform/auth/**"} {
		if !slices.Contains(paths, want) {
			problems = append(problems, fmt.Sprintf("the idp paths filter %v does not list %q", paths, want))
		}
	}

	if idp := jobBlock(lines, "idp"); len(idp) == 0 {
		problems = append(problems, "no idp job")
	} else {
		if got := jobIfExpr(idp); got != idpJobIf {
			problems = append(problems, fmt.Sprintf("the idp job if: reads %q, want %q", got, idpJobIf))
		}
		if !idpGateStep(idp) {
			problems = append(problems, "no idp job step runs `scripts/ci/rls-test-gate.sh … -run TestIdP ./internal/platform/auth/...`; a skipped TestIdP_ test would pass")
		}
	}

	ci := jobBlock(lines, "ci")
	if len(ci) == 0 {
		return append(problems, "no ci job")
	}
	if !slices.Contains(jobNeeds(ci), "idp") {
		problems = append(problems, fmt.Sprintf("the ci job's needs: %v does not list idp", jobNeeds(ci)))
	}
	if !aggregateFailsOnFailureAndCancel(runText(ci), "idp") {
		problems = append(problems, "the ci job's shell block does not fail on a failed or cancelled needs.idp.result")
	}
	return problems
}

func readCIYAML(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCIAggregateAssertsIdPJob(t *testing.T) {
	const changes = "jobs:\n  changes:\n    runs-on: ubuntu-latest\n    outputs:\n      go: ${{ steps.filter.outputs.go }}\n      idp: ${{ steps.filter.outputs.idp }}\n" +
		"    steps:\n      - uses: dorny/paths-filter@v3\n        id: filter\n        with:\n          filters: |\n" +
		"            go:\n              - 'cmd/**'\n            idp:\n              - 'sidecar/auth/**'\n              - 'internal/platform/auth/**'\n              - 'Makefile'\n"
	const idp = "  idp:\n    needs: changes\n    if: needs.changes.outputs.idp == 'true'\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - run: scripts/ci/rls-test-gate.sh -count=1 -run TestIdP ./internal/platform/auth/...\n"
	const ci = "  ci:\n    needs: [changes, go, idp]\n    if: always()\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n" +
		"          echo \"idp: ${{ needs.idp.result }}\"\n" +
		"          if [ \"${{ needs.idp.result }}\" = \"failure\" ] || [ \"${{ needs.idp.result }}\" = \"cancelled\" ]; then\n" +
		"            echo \"::error::IdP suite failed\"; exit 1\n          fi\n"
	good := changes + idp + ci

	if p := idpJobProblems(good); len(p) != 0 {
		t.Fatalf("the good fixture reports %v; the scan is broken", p)
	}
	for _, c := range []struct{ name, yaml, want string }{
		{"no idp output", strings.Replace(good, "      idp: ${{ steps.filter.outputs.idp }}\n", "", 1), "changes.outputs"},
		{"filter misses auth", strings.Replace(good, "              - 'internal/platform/auth/**'\n", "", 1), "internal/platform/auth/**"},
		{"job reads the go output", strings.Replace(good, "outputs.idp == 'true'", "outputs.go == 'true'", 1), "if: reads"},
		{"tests run without the gate", strings.Replace(good, "scripts/ci/rls-test-gate.sh -count=1", "go test -count=1", 1), "rls-test-gate.sh"},
		{"idp missing from needs", strings.Replace(good, "[changes, go, idp]", "[changes, go]", 1), "needs:"},
		{"echo only", strings.Replace(good, "            echo \"::error::IdP suite failed\"; exit 1\n", "            echo \"::error::IdP suite failed\"\n", 1), "shell block"},
		{"failure only", strings.Replace(good, " || [ \"${{ needs.idp.result }}\" = \"cancelled\" ]", "", 1), "shell block"},
		{"no idp job", strings.Replace(good, idp, "", 1), "no idp job"},
	} {
		if c.yaml == good {
			t.Fatalf("fixture %q: the edit did not apply", c.name)
		}
		p := idpJobProblems(c.yaml)
		if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, c.want) }) {
			t.Errorf("fixture %q: problems %v, want one naming %q", c.name, p, c.want)
		}
	}

	raw := readCIYAML(t)
	if len(jobBlock(yamlCode(raw), "ci")) == 0 || len(changesOutputs(jobBlock(yamlCode(raw), "changes"))) == 0 {
		t.Fatal("found no ci job or no changes outputs in ci.yml; the scan is broken")
	}
	for _, p := range idpJobProblems(raw) {
		t.Errorf(".github/workflows/ci.yml: %s", p)
	}
}

var shellVarRE = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$|^\$\{\{\s*env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)

// idpUpDSN returns the first idp-up.sh argument in the idp job, and that argument
// resolved one level through the step's or the job's env:.
func idpUpDSN(ciYAML string) (arg, resolved string, found bool) {
	block := jobBlock(yamlCode(ciYAML), "idp")
	for _, step := range jobSteps(block) {
		run := strings.ReplaceAll(runText(step), "\\\n", " ")
		for _, line := range strings.Split(run, "\n") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if !strings.HasSuffix(f, "idp-up.sh") || i+1 >= len(fields) {
					continue
				}
				arg = strings.Trim(fields[i+1], `"'`)
				resolved = arg
				if m := shellVarRE.FindStringSubmatch(arg); m != nil {
					name := m[1] + m[2]
					for _, l := range append(append([]string{}, step...), block...) {
						if v, ok := strings.CutPrefix(strings.TrimSpace(l), name+":"); ok {
							resolved = strings.TrimSpace(v)
							break
						}
					}
				}
				return arg, resolved, true
			}
		}
	}
	return "", "", false
}

func idpHarnessDSNProblem(ciYAML string) string {
	arg, resolved, found := idpUpDSN(ciYAML)
	switch {
	case !found:
		return "no idp job step calls idp-up.sh with an argument"
	case strings.Contains(arg+resolved, "SUPERUSER") || strings.Contains(resolved, "//postgres:"):
		return fmt.Sprintf("idp-up.sh's DSN argument %q (resolves to %q) is the superuser; it bypasses RLS, grants and search_path", arg, resolved)
	case !strings.Contains(arg+resolved, "AUTH_ADMIN") && !strings.Contains(resolved, "supabase_auth_admin"):
		return fmt.Sprintf("idp-up.sh's DSN argument %q (resolves to %q) is not the supabase_auth_admin DSN", arg, resolved)
	}
	return ""
}

func TestIdPHarnessNeverUsesTheSuperuserDSN(t *testing.T) {
	const head = "jobs:\n  idp:\n    runs-on: ubuntu-latest\n    env:\n" +
		"      DATABASE_SUPERUSER_URL: postgres://postgres:postgres@localhost:5432/invoice_os\n" +
		"      DATABASE_AUTH_ADMIN_URL: postgres://supabase_auth_admin:auth_admin@localhost:5432/invoice_os\n    steps:\n"
	const good = head + "      - name: Start the IdP\n        run: scripts/ci/idp-up.sh \"$DATABASE_AUTH_ADMIN_URL\" 5432\n"

	if p := idpHarnessDSNProblem(good); p != "" {
		t.Fatalf("the good fixture reports %q; the scan is broken", p)
	}
	for _, c := range []struct{ name, yaml string }{
		{"superuser DSN", strings.Replace(good, "$DATABASE_AUTH_ADMIN_URL", "$DATABASE_SUPERUSER_URL", 1)},
		{"superuser through a step alias", head + "      - env:\n          DSN: ${{ env.DATABASE_SUPERUSER_URL }}\n        run: scripts/ci/idp-up.sh \"$DSN\" 5432\n"},
		{"no idp-up step", head + "      - run: echo hi\n"},
	} {
		if idpHarnessDSNProblem(c.yaml) == "" {
			t.Errorf("fixture %q: no problem reported", c.name)
		}
	}

	if p := idpHarnessDSNProblem(readCIYAML(t)); p != "" {
		t.Errorf(".github/workflows/ci.yml: %s", p)
	}
}

// --- dev-env.yml deploy gate for `auth` ---

const prOnlyIf = "github.event_name == 'pull_request'"

var (
	setForkAuthRE     = regexp.MustCompile(`railway-env\.sh set-fork-auth(\s|$)`)
	setForkAuthSiteRE = regexp.MustCompile(`railway-env\.sh set-fork-auth-site(\s|$)`)
	auditSealedRE     = regexp.MustCompile(`railway-env\.sh audit-sealed-variables`)
	assertDSNsRE      = regexp.MustCompile(`railway-env\.sh assert-db-dsns`)
	expectedJSONLitRE = regexp.MustCompile(`expected_json='\[([^\]]*)\]'`)
	matrixServiceRE   = regexp.MustCompile(`^\s+service:\s*\[([^\]]*)\]\s*$`)
	quotedNameRE      = regexp.MustCompile(`"([^"]*)"`)
)

// devEnvCode returns dev-env.yml with comments stripped.
func devEnvCode(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "dev-env.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return yamlCode(string(raw))
}

// stepIf returns a step's own if: expression without the ${{ }} wrapper.
func stepIf(step []string) string {
	v, _ := stepKey(step, "if")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(v, "${{"), "}}"))
}

func stepWithID(steps [][]string, id string) int {
	for i, s := range steps {
		if v, ok := stepKey(s, "id"); ok && v == id {
			return i
		}
	}
	return -1
}

// stepsRunning returns the indexes of the steps whose run text matches re.
func stepsRunning(steps [][]string, re *regexp.Regexp) []int {
	var out []int
	for i, s := range steps {
		if re.MatchString(runText(s)) {
			out = append(out, i)
		}
	}
	return out
}

func prepareEnvSteps(t *testing.T) [][]string {
	t.Helper()
	steps := jobSteps(jobBlock(devEnvCode(t), "prepare-env"))
	if stepWithID(steps, "resolve") < 0 || stepWithID(steps, "urls") < 0 {
		t.Fatalf("dev-env.yml prepare-env parsed to %d step(s) with no `resolve` or `urls` id; the scan is broken", len(steps))
	}
	return steps
}

func TestSetForkAuthRunsFirstAfterResolveOnPullRequestOnly(t *testing.T) {
	steps := prepareEnvSteps(t)
	resolve := stepWithID(steps, "resolve")
	later := append(stepsRunning(steps, auditSealedRE), stepsRunning(steps, assertDSNsRE)...)
	if len(later) == 0 {
		t.Fatal("prepare-env runs neither audit-sealed-variables nor assert-db-dsns; the ordering has nothing to compare")
	}

	got := stepsRunning(steps, setForkAuthRE)
	if len(got) != 1 {
		t.Fatalf("dev-env.yml prepare-env has %d step(s) running `railway-env.sh set-fork-auth`, want 1", len(got))
	}
	i := got[0]
	if g := stepIf(steps[i]); g != prOnlyIf {
		t.Errorf("the set-fork-auth step's if: reads %q, want %q; push and dispatch target the persistent environment", g, prOnlyIf)
	}
	if i != resolve+1 {
		t.Errorf("set-fork-auth is prepare-env step %d, want %d (the first step after `resolve`)", i, resolve+1)
	}
	for _, j := range later {
		if i > j {
			t.Errorf("set-fork-auth (step %d) runs after %q (step %d); it must precede the audit and every DSN assert", i, runText(steps[j]), j)
		}
	}
	text := strings.Join(steps[i], "\n")
	if strings.Contains(strings.ToLower(text), "landing") {
		t.Errorf("the set-fork-auth step references a landing URL; it runs before `urls` and set-fork-auth-site owns GOTRUE_SITE_URL:\n%s", text)
	}
	if !strings.Contains(text, "steps.resolve.outputs.environment_id") {
		t.Errorf("the set-fork-auth step does not read steps.resolve.outputs.environment_id; it would write to no fork:\n%s", text)
	}
}

func TestSetForkAuthSiteRunsRightAfterURLs(t *testing.T) {
	lines := devEnvCode(t)
	if needs := jobNeeds(jobBlock(lines, "deploy-gateway")); !slices.Contains(needs, "prepare-env") {
		t.Fatalf("deploy-gateway needs %v, not prepare-env; no prepare-env step is guaranteed to precede it", needs)
	}
	steps := prepareEnvSteps(t)
	urls := stepWithID(steps, "urls")

	got := stepsRunning(steps, setForkAuthSiteRE)
	if len(got) != 1 {
		t.Fatalf("dev-env.yml prepare-env has %d step(s) running `railway-env.sh set-fork-auth-site`, want 1", len(got))
	}
	i := got[0]
	if g := stepIf(steps[i]); g != prOnlyIf {
		t.Errorf("the set-fork-auth-site step's if: reads %q, want %q", g, prOnlyIf)
	}
	if i != urls+1 {
		t.Errorf("set-fork-auth-site is prepare-env step %d, want %d (immediately after `urls`)", i, urls+1)
	}
	if text := strings.Join(steps[i], "\n"); !strings.Contains(text, "steps.urls.outputs.landing_url") {
		t.Errorf("the set-fork-auth-site step does not read steps.urls.outputs.landing_url:\n%s", text)
	}
}

// jobMatrix returns a job's `service: [...]` matrix.
func jobMatrix(block []string) []string {
	for _, l := range block {
		if m := matrixServiceRE.FindStringSubmatch(l); m != nil {
			var out []string
			for _, f := range strings.Split(m[1], ",") {
				if f = strings.TrimSpace(f); f != "" {
					out = append(out, f)
				}
			}
			return out
		}
	}
	return nil
}

func devEnvExpectedJSON(lines []string) []string {
	m := expectedJSONLitRE.FindStringSubmatch(strings.Join(lines, "\n"))
	if m == nil {
		return nil
	}
	var out []string
	for _, q := range quotedNameRE.FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out
}

func TestAuthIsDeployedAfterTheGatewayGate(t *testing.T) {
	lines := devEnvCode(t)
	expected := devEnvExpectedJSON(lines)
	ctxJob := jobBlock(lines, "deploy-context")
	ctx, spas := jobMatrix(ctxJob), jobMatrix(jobBlock(lines, "deploy-spas"))
	// Control: the parse reaches lists every version of the file carries.
	if !slices.Contains(expected, "docling") || !slices.Contains(ctx, "docling") || !slices.Contains(spas, "landing") {
		t.Fatalf("parsed expected_json=%v deploy-context=%v deploy-spas=%v; the scan is broken", expected, ctx, spas)
	}
	if !slices.Contains(jobNeeds(ctxJob), "health-gate") {
		t.Errorf("deploy-context needs %v, not health-gate; auth would deploy before the gateway migrates", jobNeeds(ctxJob))
	}

	if !slices.Contains(expected, "auth") {
		t.Errorf("dev-env.yml expected_json %v does not name `auth`; the Watch-Paths assertion never sees it", expected)
	}
	if !slices.Contains(ctx, "auth") {
		t.Errorf("dev-env.yml deploy-context matrix %v does not name `auth`; nothing ships it after health-gate", ctx)
	}
	if slices.Contains(spas, "auth") {
		t.Errorf("dev-env.yml deploy-spas matrix %v names `auth`; it is a backend, not a static front end", spas)
	}
}

// healthGateWaitRun returns the run text of the health-gate step that reads .build.
func healthGateWaitRun(t *testing.T) string {
	t.Helper()
	for _, s := range jobSteps(devEnvHealthGate(t)) {
		if r := runText(s); strings.Contains(r, ".build // empty") {
			return r
		}
	}
	t.Fatal("no health-gate step reads .build // empty")
	return ""
}

// gateShim is a curl that serves $SHIM_DIR/healthz.json and fleet.json, answers
// 404 to any other URL, honours -o and -w, and logs each URL.
const gateShim = `#!/bin/sh
out=""; w=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -w) w="$2"; shift ;;
    http*) url="$1" ;;
  esac
  shift
done
echo "$url" >> "$SHIM_DIR/curl.log"
case "$url" in
  */healthz/fleet) body="$SHIM_DIR/fleet.json" ;;
  */healthz) body="$SHIM_DIR/healthz.json" ;;
  *) body="" ;;
esac
code=404
if [ -n "$body" ]; then
  code=200
  if [ -n "$out" ]; then cp "$body" "$out"; else cat "$body"; fi
fi
if [ -n "$w" ]; then printf '%s' "$code"; fi
exit 0
`

// runWithGateShim runs block with curl and sleep shimmed and <name>.json holding body.
// Paths under /tmp/ move into the test's own directory.
func runWithGateShim(t *testing.T, block, name string, body any, env ...string) (code int, out string, urls []string) {
	t.Helper()
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Fatalf("jq is not on PATH: %v", err)
	}
	dir := t.TempDir()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for f, b := range map[string][]byte{"curl": []byte(gateShim), "sleep": []byte("#!/bin/sh\n"), name + ".json": raw} {
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	block = strings.ReplaceAll(block, "/tmp/", dir+"/")
	base := []string{"PATH=" + dir + ":" + filepath.Dir(jq) + ":/usr/bin:/bin", "SHIM_DIR=" + dir, "GATEWAY_URL=" + probeGatewayURL}
	code, out = runGate(t, block, append(base, env...)...)
	return code, out, readLog(t, filepath.Join(dir, "curl.log"))
}

func errorLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "::error::") {
			lines = append(lines, l)
		}
	}
	return lines
}

// healthzBody is a /healthz body that passes every existing barrier for its target.
func healthzBody(isPR bool, issuers string) map[string]string {
	b := map[string]string{"build": "deadbeef", "db_reset": "false", "demo_purge": "false", "mock_issuer": "absent"}
	if isPR {
		b["db_reset"], b["demo_purge"], b["mock_issuer"] = "true", "true", "on"
	}
	if issuers != "" {
		b["auth_issuers"] = issuers
	}
	return b
}

func TestAuthIssuersGateIsDirectional(t *testing.T) {
	run := healthGateWaitRun(t)

	t.Run("control", func(t *testing.T) {
		body := healthzBody(true, "2")
		body["mock_issuer"] = "off"
		code, out, urls := runWithGateShim(t, run, "healthz", body, "IS_PR=true")
		if code == 0 || !slices.ContainsFunc(errorLines(out), func(l string) bool { return strings.Contains(l, "mock_issuer='off'") }) {
			t.Fatalf("a PR body with mock_issuer=off exits %d without naming it; the runner is not executing the real step (output %q)", code, out)
		}
		if !slices.ContainsFunc(urls, func(u string) bool { return strings.HasSuffix(u, "/healthz") }) {
			t.Fatalf("the curl shim saw %v, no /healthz fetch", urls)
		}
	})

	for _, c := range []struct {
		isPR     bool
		issuers  string
		wantExit int
	}{
		{true, "2", 0}, {true, "1", 1}, {true, "3", 1}, {true, "", 1},
		{false, "1", 0}, {false, "2", 1}, {false, "", 1},
	} {
		code, out, _ := runWithGateShim(t, run, "healthz", healthzBody(c.isPR, c.issuers), "IS_PR="+strconv.FormatBool(c.isPR))
		errs := errorLines(out)
		if code != c.wantExit {
			t.Errorf("IS_PR=%v auth_issuers=%q: health-gate exits %d, want %d (errors %q)", c.isPR, c.issuers, code, c.wantExit, errs)
			continue
		}
		if c.wantExit == 0 {
			if len(errs) != 0 {
				t.Errorf("IS_PR=%v auth_issuers=%q: a passing gate printed %q", c.isPR, c.issuers, errs)
			}
			continue
		}
		named := slices.ContainsFunc(errs, func(l string) bool {
			return strings.Contains(l, "auth_issuers") && strings.Contains(l, c.issuers)
		})
		if !named {
			t.Errorf("IS_PR=%v auth_issuers=%q: no ::error:: line names auth_issuers and the value seen: %q", c.isPR, c.issuers, errs)
		}
	}
}

var printfBodyRE = regexp.MustCompile(`printf '%s' "\$([A-Za-z_][A-Za-z0-9_]*)"`)

// authIssuersReadFaults reports each way run departs from reading auth_issuers
// out of the body whose .build matched.
func authIssuersReadFaults(run string) []string {
	lines := strings.Split(run, "\n")
	find := func(needle string) int {
		return slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, needle) })
	}
	build, mock, auth := find(".build // empty"), find(".mock_issuer // empty"), find(".auth_issuers")
	brk := -1
	for i := max(build, 0); i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "break" {
			brk = i
			break
		}
	}
	if build < 0 || brk < 0 || mock < 0 || printfBodyRE.FindStringSubmatch(lines[build]) == nil {
		return []string{"the .build read, its break or the mock_issuer read is not where the scan expects; the scan is broken"}
	}
	if auth < 0 {
		return []string{"the health-gate never reads .auth_issuers"}
	}
	bodyRef := `"$` + printfBodyRE.FindStringSubmatch(lines[build])[1] + `"`

	var faults []string
	if auth < build || auth > brk {
		faults = append(faults, "the auth_issuers read sits outside the block that matched .build")
	}
	if strings.Contains(lines[auth], "curl") || !strings.Contains(lines[auth], bodyRef) {
		faults = append(faults, "the auth_issuers read does not parse "+bodyRef+", the body the .build match read")
	}
	if !strings.Contains(lines[mock], bodyRef) {
		faults = append(faults, "the mock_issuer read does not parse "+bodyRef)
	}
	fetches := 0
	for _, l := range lines {
		if strings.Contains(l, "curl") && strings.Contains(l, `/healthz"`) {
			fetches++
		}
	}
	if fetches != 1 {
		faults = append(faults, fmt.Sprintf("the health-gate fetches /healthz %d time(s), want 1", fetches))
	}
	return faults
}

func TestAuthIssuersGateReadsTheBuildMatchedBody(t *testing.T) {
	const read = `    issuers=$(printf '%s' "$body" | jq -r '.auth_issuers // empty' 2>/dev/null || echo '')` + "\n"
	good := fxWaitLoopA + fxMockRead + read + fxWaitLoopB
	if f := authIssuersReadFaults(good); len(f) != 0 {
		t.Fatalf("the planned read reports %v; the scan is broken", f)
	}
	for _, c := range []struct{ name, src, want string }{
		{"its own curl", strings.Replace(good, `printf '%s' "$body" | jq -r '.auth_issuers`, `curl -fsS "$GATEWAY_URL/healthz" | jq -r '.auth_issuers`, 1), "does not parse"},
		{"read after the break", fxWaitLoopA + fxMockRead + fxWaitLoopB + read, "outside the block"},
		{"another body", strings.Replace(good, `"$body" | jq -r '.auth_issuers`, `"$old" | jq -r '.auth_issuers`, 1), "does not parse"},
		{"never read", fxWaitLoopA + fxMockRead + fxWaitLoopB, "never reads"},
	} {
		if c.src == good {
			t.Fatalf("fixture %q: the edit did not apply", c.name)
		}
		if f := authIssuersReadFaults(c.src); !slices.ContainsFunc(f, func(s string) bool { return strings.Contains(s, c.want) }) {
			t.Errorf("fixture %q: faults %v, want one naming %q", c.name, f, c.want)
		}
	}

	for _, f := range authIssuersReadFaults(healthGateWaitRun(t)) {
		t.Errorf(".github/workflows/dev-env.yml health-gate: %s", f)
	}
}

// fleetGateStaleRun returns the run text of the fleet-gate step that computes stale.
func fleetGateStaleRun(t *testing.T) string {
	t.Helper()
	for _, s := range jobSteps(jobBlock(devEnvCode(t), "fleet-gate")) {
		if r := runText(s); strings.Contains(r, "stale=") && strings.Contains(r, "/healthz/fleet") {
			return r
		}
	}
	t.Fatal("no fleet-gate step computes stale from /healthz/fleet; the scan is broken")
	return ""
}

type fleetEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Build  string `json:"build,omitempty"`
}

// fleetBody is a roll-up with every deploy-context backend up on deadbeef; extra replaces or adds.
func fleetBody(extra ...fleetEntry) map[string][]fleetEntry {
	var s []fleetEntry
	for _, n := range []string{"tenancy", "portfolio", "invoice", "validation", "submission", "dashboard", "notifications", "reconciliation", "docling"} {
		s = append(s, fleetEntry{n, "up", "deadbeef"})
	}
	for _, e := range extra {
		if i := slices.IndexFunc(s, func(x fleetEntry) bool { return x.Name == e.Name }); i >= 0 {
			s[i] = e
		} else {
			s = append(s, e)
		}
	}
	return map[string][]fleetEntry{"services": s}
}

type fleetRow struct {
	name        string
	body        map[string][]fleetEntry
	wantExit    int
	names, omit string
}

func checkFleetRow(t *testing.T, run string, r fleetRow) {
	t.Helper()
	code, out, urls := runWithGateShim(t, run, "fleet", r.body)
	if len(urls) == 0 {
		t.Fatalf("%s: the curl shim saw no fetch", r.name)
	}
	errs := strings.Join(errorLines(out), "\n")
	if code != r.wantExit {
		t.Errorf("%s: fleet-gate exits %d, want %d (errors %q)", r.name, code, r.wantExit, errs)
	}
	if r.names != "" && !strings.Contains(errs, r.names) {
		t.Errorf("%s: the stale report %q does not name %s", r.name, errs, r.names)
	}
	if r.omit != "" && strings.Contains(errs, r.omit) {
		t.Errorf("%s: the stale report %q names %s; auth carries no build and is exempt by name", r.name, errs, r.omit)
	}
}

func TestFleetGateExemptsOnlyAuthFromBuildSHA(t *testing.T) {
	run := fleetGateStaleRun(t)
	t.Run("control", func(t *testing.T) {
		checkFleetRow(t, run, fleetRow{"every backend on the build", fleetBody(), 0, "", ""})
		checkFleetRow(t, run, fleetRow{"tenancy on an old build", fleetBody(fleetEntry{"tenancy", "up", "0ld"}), 1, "tenancy=0ld", ""})
	})
	for _, r := range []fleetRow{
		{"auth with no build", fleetBody(fleetEntry{"auth", "up", ""}), 0, "", ""},
		{"auth on a wrong build", fleetBody(fleetEntry{"auth", "up", "0ld"}), 0, "", ""},
		{"docling with no build beside auth", fleetBody(fleetEntry{"auth", "up", ""}, fleetEntry{"docling", "up", ""}), 1, "docling=none", "auth=none"},
		{"authz is not auth", fleetBody(fleetEntry{"auth", "up", ""}, fleetEntry{"authz", "up", ""}), 1, "authz=none", "auth=none"},
	} {
		checkFleetRow(t, run, r)
	}
}
