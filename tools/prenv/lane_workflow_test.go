// lane_workflow_test.go pins the small-lane wiring of .github/workflows/ci.yml and dev-env.yml.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func ciJob(t *testing.T, id string) workflowJob {
	t.Helper()
	for _, j := range workflowJobsOf(readWorkflow(t, "ci.yml")) {
		if j.name == id {
			return j
		}
	}
	t.Fatalf("ci.yml has no job %q", id)
	return workflowJob{}
}

func TestCIChangesPublishesThePRLane(t *testing.T) {
	j := ciJob(t, "changes")
	if !slices.Contains(blockLines(j.lines, 4, "outputs"), "lane: ${{ steps.lane.outputs.lane }}") {
		t.Errorf("changes outputs lack lane: ${{ steps.lane.outputs.lane }}")
	}
	laneSteps := 0
	for _, s := range j.steps() {
		if !strings.Contains(s.keys["run"], "scripts/ci/lane.sh") {
			continue
		}
		laneSteps++
		if s.keys["run"] != "bash scripts/ci/lane.sh pr" || s.keys["id"] != "lane" {
			t.Errorf("lane step run=%q id=%q, want bash scripts/ci/lane.sh pr / lane", s.keys["run"], s.keys["id"])
		}
		if s.keys["if"] != "github.event_name == 'pull_request'" {
			t.Errorf("lane step if = %q, want the pull_request gate", s.keys["if"])
		}
	}
	if laneSteps != 1 {
		t.Fatalf("changes has %d lane.sh steps, want 1", laneSteps)
	}
	checkout, depth, lane := -1, -1, -1
	for i, l := range j.lines {
		tl := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(tl, "- uses: actions/checkout@") && checkout < 0:
			checkout = i
		case tl == "fetch-depth: 2" && depth < 0:
			depth = i
		case strings.Contains(tl, "scripts/ci/lane.sh"):
			lane = i
		}
	}
	if checkout < 0 || depth != checkout+2 || lane < depth {
		t.Errorf("want checkout with fetch-depth: 2 before the lane step; checkout=%d depth=%d lane=%d", checkout, depth, lane)
	}
}

func TestCILibraryJobReplacesFrontendOnTheSmallLane(t *testing.T) {
	fe := ciJob(t, "frontend")
	const feWant = "needs.changes.outputs.frontend == 'true' && needs.changes.outputs.lane != 'library'"
	if feIf, _ := jobKey(fe, "if"); feIf != feWant {
		t.Errorf("frontend if = %q, want %q", feIf, feWant)
	}
	lib := ciJob(t, "library")
	if v, _ := jobKey(lib, "name"); v != "Library" {
		t.Errorf("library name = %q, want Library", v)
	}
	if got := jobList(lib, "needs"); got != nil {
		t.Errorf("library needs list = %v, want the scalar form", got)
	}
	if v, _ := jobKey(lib, "needs"); v != "changes" {
		t.Errorf("library needs = %q, want changes", v)
	}
	if v, _ := jobKey(lib, "if"); v != "needs.changes.outputs.lane == 'library'" {
		t.Errorf("library if = %q", v)
	}
	var runs []string
	libSteps := lib.steps()
	if len(libSteps) == 0 {
		t.Fatal("library job has no steps")
	}
	for _, s := range libSteps {
		for _, k := range []string{"if", "continue-on-error"} {
			if v, ok := s.keys[k]; ok {
				t.Errorf("library step %v has %s: %q, want none", s.keys, k, v)
			}
		}
		if r := s.keys["run"]; r != "" {
			runs = append(runs, r)
		}
	}
	for _, want := range []string{
		"pnpm --filter @invoice-os/library test",
		"pnpm --filter @invoice-os/library build",
		"pnpm --filter @invoice-os/monitoring test",
		"pnpm --filter @invoice-os/e2e test:unit",
	} {
		if !slices.Contains(runs, want) {
			t.Errorf("library steps lack %q; runs = %v", want, runs)
		}
	}
}

func TestCIRollupRequiresTheLibraryJob(t *testing.T) {
	ci := ciJob(t, "ci")
	if !slices.Contains(jobList(ci, "needs"), "library") {
		t.Errorf("ci needs = %v, want library in it", jobList(ci, "needs"))
	}
	var run string
	for _, s := range ci.steps() {
		run += s.keys["run"] + "\n"
	}
	const guard = `if [ "${{ needs.library.result }}" = "failure" ] || [ "${{ needs.library.result }}" = "cancelled" ]; then` +
		"\n" + `echo "::error::Library job failed"; exit 1` + "\n" + "fi"
	if !strings.Contains(run, guard) {
		t.Errorf("ci run lacks the failure-or-cancelled exit for needs.library.result:\n%s", run)
	}
}

func TestCIStaticChecksStayUnconditional(t *testing.T) {
	for _, id := range []string{"clean-clone", "stale-refs", "migration-order", "cite-refs"} {
		if v, ok := jobKey(ciJob(t, id), "if"); ok {
			t.Errorf("%s has if: %q, want none", id, v)
		}
	}
	if _, ok := jobKey(ciJob(t, "frontend"), "if"); !ok {
		t.Error("control: frontend has no if:, so the scan cannot tell a conditional job")
	}
}

// rawSteps splits a job's steps into their trimmed lines.
func rawSteps(j workflowJob) [][]string {
	var out [][]string
	in := false
	for _, l := range stripHashComments(j.lines) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !in {
			in = l == "    steps:"
			continue
		}
		t := strings.TrimSpace(l)
		if strings.HasPrefix(l, "      - ") {
			out = append(out, nil)
			t = strings.TrimPrefix(t, "- ")
		}
		if len(out) > 0 {
			out[len(out)-1] = append(out[len(out)-1], t)
		}
	}
	return out
}

// checkoutsWhen returns the checkout steps of j whose if: equals cond.
func checkoutsWhen(j workflowJob, cond string) [][]string {
	var out [][]string
	for _, s := range rawSteps(j) {
		if strings.HasPrefix(s[0], "uses: actions/checkout@") && slices.Contains(s, "if: "+cond) {
			out = append(out, s)
		}
	}
	return out
}

func TestDevEnvLibraryPRSkipsE2E(t *testing.T) {
	j := devEnvJob(t, "changes")
	if !slices.Contains(blockLines(j.lines, 4, "outputs"), "e2e: ${{ github.event_name != 'pull_request' && 'true' || steps.lane.outputs.e2e }}") {
		t.Errorf("changes outputs lack the e2e expression from steps.lane: %v", blockLines(j.lines, 4, "outputs"))
	}
	laneSteps := 0
	for _, s := range j.steps() {
		if !strings.Contains(s.keys["run"], "scripts/ci/lane.sh") || s.keys["id"] != "lane" {
			continue
		}
		laneSteps++
		if s.keys["run"] != "bash scripts/ci/lane.sh pr-e2e" {
			t.Errorf("lane step run = %q, want bash scripts/ci/lane.sh pr-e2e", s.keys["run"])
		}
		if s.keys["if"] != "github.event_name == 'pull_request'" {
			t.Errorf("lane step if = %q, want the pull_request gate", s.keys["if"])
		}
		if got := s.env["FILTER_E2E"]; got != "${{ steps.filter.outputs.e2e }}" {
			t.Errorf("lane step FILTER_E2E = %q", got)
		}
	}
	if laneSteps != 1 {
		t.Fatalf("changes has %d lane steps, want 1", laneSteps)
	}
	prCheckouts := checkoutsWhen(j, "github.event_name == 'pull_request'")
	if len(prCheckouts) != 1 || !slices.Contains(prCheckouts[0], "fetch-depth: 2") {
		t.Errorf("want one pull_request checkout with fetch-depth: 2, got %v", prCheckouts)
	}
}

func TestDevEnvE2EFilterKeepsEveryFrontendPackage(t *testing.T) {
	var filter []string
	for _, s := range rawSteps(devEnvJob(t, "changes")) {
		if strings.HasPrefix(s[0], "uses: dorny/paths-filter@") {
			filter = s
		}
	}
	if filter == nil {
		t.Fatal("changes has no paths-filter step")
	}
	for _, want := range []string{"- 'frontend/**'", "- 'internal/**'"} {
		if !slices.Contains(filter, want) {
			t.Errorf("e2e filter lacks %s", want)
		}
	}
}

func TestDevEnvPushPublishesTheScope(t *testing.T) {
	j := devEnvJob(t, "changes")
	if !slices.Contains(blockLines(j.lines, 4, "outputs"), "scope: ${{ github.event_name == 'push' && steps.scope.outputs.scope || 'full' }}") {
		t.Errorf("changes outputs lack the scope expression: %v", blockLines(j.lines, 4, "outputs"))
	}
	if !slices.Contains(blockLines(j.lines, 4, "permissions"), "actions: read") {
		t.Errorf("changes permissions = %v, want actions: read", blockLines(j.lines, 4, "permissions"))
	}
	scopeSteps := 0
	for _, s := range j.steps() {
		if s.keys["id"] != "scope" {
			continue
		}
		scopeSteps++
		if s.keys["run"] != `bash scripts/ci/lane.sh push "$SHA"` {
			t.Errorf("scope step run = %q", s.keys["run"])
		}
		if s.keys["if"] != "github.event_name == 'push'" || s.keys["continue-on-error"] != "true" {
			t.Errorf("scope step if = %q, continue-on-error = %q", s.keys["if"], s.keys["continue-on-error"])
		}
		for k, want := range map[string]string{
			"REPO": "${{ github.repository }}", "RUN_ID": "${{ github.run_id }}",
			"GH_TOKEN": "${{ github.token }}", "SHA": "${{ github.sha }}",
		} {
			if s.env[k] != want {
				t.Errorf("scope step env %s = %q, want %q", k, s.env[k], want)
			}
		}
	}
	if scopeSteps != 1 {
		t.Fatalf("changes has %d scope steps, want 1", scopeSteps)
	}
	pushCheckouts := checkoutsWhen(j, "github.event_name == 'push'")
	if len(pushCheckouts) != 1 {
		t.Fatalf("want one push checkout, got %v", pushCheckouts)
	}
	for _, want := range []string{"fetch-depth: 0", "continue-on-error: true"} {
		if !slices.Contains(pushCheckouts[0], want) {
			t.Errorf("push checkout lacks %q: %v", want, pushCheckouts[0])
		}
	}
}

func TestDevEnvLibraryScopeSkipsTheFleetChain(t *testing.T) {
	gw := devEnvJob(t, "deploy-gateway")
	if !slices.Contains(jobList(gw, "needs"), "changes") {
		t.Errorf("deploy-gateway needs = %v, want changes in it", jobList(gw, "needs"))
	}
	if cond, _ := jobKey(gw, "if"); !strings.Contains(cond, "needs.changes.outputs.scope != 'library'") {
		t.Errorf("deploy-gateway if: %q lacks the library-scope skip", cond)
	}
	for job, dep := range map[string]string{
		"health-gate": "deploy-gateway", "deploy-context": "health-gate", "deploy-spas": "health-gate",
		"fleet-gate": "deploy-context", "spa-build-gate": "deploy-spas",
	} {
		if !slices.Contains(jobList(devEnvJob(t, job), "needs"), dep) {
			t.Errorf("%s needs = %v, want %s: the library scope would not skip it", job, jobList(devEnvJob(t, job), "needs"), dep)
		}
	}
}

func TestDevEnvDeployLibraryDeploysOnlyTheLibrary(t *testing.T) {
	j := devEnvJob(t, "deploy-library")
	cond, _ := jobKey(j, "if")
	for _, want := range []string{
		"github.event_name == 'push'", "needs.changes.outputs.scope == 'library'",
		"needs.await-ci.result == 'success'", "needs.prepare-env.result == 'success'",
	} {
		if !strings.Contains(cond, want) {
			t.Errorf("deploy-library if: %q lacks %q", cond, want)
		}
	}
	if !slices.Contains(jobList(j, "needs"), "prepare-env") {
		t.Errorf("deploy-library needs = %v, want prepare-env", jobList(j, "needs"))
	}
	if name, _ := jobKey(j, "name"); !strings.HasPrefix(name, "Deploy ") || !strings.Contains(name, " → ") {
		t.Errorf("deploy-library name = %q, want \"Deploy <x> → <env>\"", name)
	}
	var ups []string
	for _, s := range j.steps() {
		if strings.Contains(s.keys["run"], "railway-up-ci.sh") {
			ups = append(ups, s.keys["run"])
		}
	}
	if len(ups) != 1 || ups[0] != "sh scripts/ci/railway-up-ci.sh library" {
		t.Errorf("deploy-library railway-up-ci.sh calls = %v, want exactly one with library", ups)
	}
	for _, dep := range []string{"changes", "await-ci"} {
		if !slices.Contains(jobList(j, "needs"), dep) {
			t.Errorf("deploy-library needs = %v, want %s", jobList(j, "needs"), dep)
		}
	}
	var upEnv map[string]string
	for _, s := range j.steps() {
		if strings.Contains(s.keys["run"], "railway-up-ci.sh") {
			upEnv = s.env
		}
	}
	if upEnv["RAILWAY_API_TOKEN"] != "${{ secrets.RAILWAY_API_TOKEN }}" || upEnv["RAILWAY_ENVIRONMENT"] != "${{ needs.prepare-env.outputs.environment }}" {
		t.Errorf("deploy-library railway up env = %v", upEnv)
	}
}

func TestDevEnvLibraryBuildGateChecksTheLibraryBuild(t *testing.T) {
	j := devEnvJob(t, "library-build-gate")
	if !slices.Contains(jobList(j, "needs"), "deploy-library") {
		t.Errorf("library-build-gate needs = %v, want deploy-library", jobList(j, "needs"))
	}
	var waits []string
	for _, s := range j.steps() {
		if strings.Contains(s.keys["run"], "wait-spa-builds.sh") {
			waits = append(waits, s.keys["run"])
		}
	}
	if len(waits) != 1 || waits[0] != `bash scripts/ci/wait-spa-builds.sh "$EXPECTED_BUILD" "$LIBRARY_URL"` {
		t.Errorf("library-build-gate waits = %v", waits)
	}
	if cond, _ := jobKey(j, "if"); cond != "needs.deploy-library.result == 'success'" {
		t.Errorf("library-build-gate if: %q, want it to run only after a successful deploy-library", cond)
	}
	env := jobEnv(j)
	if env["EXPECTED_BUILD"] != "${{ github.sha }}" || env["LIBRARY_URL"] != "${{ needs.prepare-env.outputs.library_url }}" {
		t.Errorf("library-build-gate env = %v", env)
	}
}

// laneConst reads a single-quoted shell constant from scripts/ci/lane.sh.
func laneConst(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "ci", "lane.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^` + name + `='([^']*)'$`).FindStringSubmatch(string(raw))
	if m == nil || m[1] == "" {
		t.Fatalf("lane.sh has no non-empty %s", name)
	}
	return m[1]
}

func TestDevEnvBaseJobNamesMatchTheLaneLookup(t *testing.T) {
	want := map[string]string{
		"deploy-gateway": laneConst(t, "GATEWAY_JOB_PREFIX"),
		"fleet-gate":     laneConst(t, "FLEET_GATE_JOB_PREFIX"),
		"spa-build-gate": laneConst(t, "SPA_GATE_JOB_PREFIX"),
	}
	for _, j := range workflowJobsOf(readWorkflow(t, "dev-env.yml")) {
		name, _ := jobKey(j, "name")
		for id, prefix := range want {
			if got := strings.HasPrefix(name, prefix); got != (j.name == id) {
				t.Errorf("job %s name %q: starts with %q = %v, want %v", j.name, name, prefix, got, j.name == id)
			}
		}
	}
}
