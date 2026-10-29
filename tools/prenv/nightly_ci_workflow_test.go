// nightly_ci_workflow_test.go pins the full mode of ci.yml: a dispatch runs every job and each results job writes and uploads its files.
package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	ciDispatch   = "github.event_name == 'workflow_dispatch'"
	ciGroupLine  = "group: ci-${{ github.workflow }}-${{ " + ciDispatch + " && 'nightly-' || '' }}${{ github.ref }}"
	ciNameLine   = "${{ " + ciDispatch + " && 'CI (nightly)' || 'CI' }}"
	ciLaneOutput = "lane: ${{ steps.lane.outputs.lane }}"
)

var ciForcedOutputs = []string{"go", "frontend", "migrations", "sidecar", "idp", "hooks", "caddy"}

func ciForcedOutputLine(name string) string {
	return name + ": ${{ " + ciDispatch + " && 'true' || steps.filter.outputs." + name + " }}"
}

func ciJobOf(yaml, id string) (workflowJob, bool) {
	for _, j := range workflowJobsOf(yaml) {
		if j.name == id {
			return j, true
		}
	}
	return workflowJob{}, false
}

// ciFullModeProblems names each rule of ci.yml full mode that the file breaks.
func ciFullModeProblems(yaml string) []string {
	lines := strings.Split(yaml, "\n")
	var p []string

	on := onBlockLines(lines)
	if !slices.Contains(on, "workflow_dispatch:") {
		p = append(p, "on: lacks workflow_dispatch")
	}
	for _, l := range on {
		if !slices.Contains([]string{"pull_request:", "push:", "workflow_dispatch:"}, l) {
			p = append(p, "on: names "+l)
		}
	}
	if !slices.Contains(on, "pull_request:") {
		p = append(p, "on: lacks pull_request")
	}
	if len(blockLines(lines, 2, "workflow_dispatch")) > 0 {
		p = append(p, "workflow_dispatch has inputs")
	}
	if !slices.Equal(blockLines(lines, 2, "push"), []string{"branches: [main]"}) {
		p = append(p, "on: push is not branches [main]")
	}

	conc := blockLines(lines, 0, "concurrency")
	if !slices.Contains(conc, ciGroupLine) {
		p = append(p, "concurrency group has no dispatch part")
	}
	if !slices.Contains(conc, "cancel-in-progress: true") {
		p = append(p, "concurrency cancel-in-progress is not true")
	}

	changes, ok := ciJobOf(yaml, "changes")
	if !ok {
		return append(p, "no changes job")
	}
	outs := blockLines(changes.lines, 4, "outputs")
	for _, name := range ciForcedOutputs {
		if !slices.Contains(outs, ciForcedOutputLine(name)) {
			p = append(p, "changes output \""+name+"\" is not forced on dispatch")
		}
	}
	if !slices.Contains(outs, ciLaneOutput) {
		p = append(p, "changes output \"lane\" changed")
	}
	filterSkipped := false
	for i, l := range changes.lines {
		if strings.TrimSpace(l) != "- uses: dorny/paths-filter@v3" {
			continue
		}
		for _, n := range changes.lines[i+1:] {
			if strings.HasPrefix(n, "      - ") {
				break
			}
			if strings.TrimSpace(n) == "if: github.event_name != 'workflow_dispatch'" {
				filterSkipped = true
			}
		}
	}
	if !filterSkipped {
		p = append(p, "the paths-filter step runs on a dispatch")
	}

	for _, lane := range []string{"library", "blog"} {
		j, ok := ciJobOf(yaml, lane)
		if !ok {
			p = append(p, "no "+lane+" job")
			continue
		}
		if v, _ := jobKey(j, "if"); v != "needs.changes.outputs.lane == '"+lane+"' || "+ciDispatch {
			p = append(p, lane+" job does not run on a dispatch")
		}
	}

	if j, ok := ciJobOf(yaml, "ci"); !ok {
		p = append(p, "no ci job")
	} else if v, _ := jobKey(j, "name"); v != ciNameLine {
		p = append(p, "the ci roll-up name is not gated on a dispatch")
	}
	return p
}

func requireCIFullModeProblems(t *testing.T, yml string, want ...string) {
	t.Helper()
	got := ciFullModeProblems(yml)
	if len(got) != len(want) {
		t.Fatalf("problems = %q, want %d naming %q", got, len(want), want)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("problem %d = %q, want it to name %q", i, got[i], w)
		}
	}
}

func TestCIFullMode_RealFileHasNoProblems(t *testing.T) {
	t.Parallel()
	if got := ciFullModeProblems(readWorkflow(t, "ci.yml")); len(got) != 0 {
		t.Fatalf("ci.yml full mode problems: %q", got)
	}
}

func TestCIFullMode_DispatchTriggerRemovedFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  workflow_dispatch:\n", "")
	requireCIFullModeProblems(t, yml, "on: lacks workflow_dispatch")
}

func TestCIFullMode_PullRequestTriggerRemovedFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  pull_request:\n", "")
	requireCIFullModeProblems(t, yml, "on: lacks pull_request")
}

func TestCIFullMode_DispatchInputsFail(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  workflow_dispatch:\n",
		"  workflow_dispatch:\n    inputs:\n      x:\n        type: string\n")
	requireCIFullModeProblems(t, yml, "workflow_dispatch has inputs")
}

func TestCIFullMode_PushBranchWidenedFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "    branches: [main]\n", "    branches: [main, epic/*]\n")
	requireCIFullModeProblems(t, yml, "push")
}

func TestCIFullMode_ExtraTriggerFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  workflow_dispatch:\n", "  workflow_dispatch:\n  schedule:\n")
	requireCIFullModeProblems(t, yml, "on: names schedule:")
}

func TestCIFullMode_ConcurrencyWithoutDispatchPartFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), ciGroupLine, "group: ci-${{ github.workflow }}-${{ github.ref }}")
	requireCIFullModeProblems(t, yml, "concurrency group has no dispatch part")
}

func TestCIFullMode_ConcurrencyCancelFalseFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "cancel-in-progress: true", "cancel-in-progress: false")
	requireCIFullModeProblems(t, yml, "cancel-in-progress")
}

func TestCIFullMode_EachOutputForced(t *testing.T) {
	t.Parallel()
	for _, name := range ciForcedOutputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "      "+ciForcedOutputLine(name)+"\n",
				"      "+name+": ${{ steps.filter.outputs."+name+" }}\n")
			requireCIFullModeProblems(t, yml, "changes output \""+name+"\"")
		})
	}
}

func TestCIFullMode_LaneOutputChangedFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "      "+ciLaneOutput+"\n",
		"      lane: ${{ "+ciDispatch+" && 'true' || steps.lane.outputs.lane }}\n")
	requireCIFullModeProblems(t, yml, "\"lane\"")
}

func TestCIFullMode_FilterStepRunsOnDispatchFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"),
		"        id: filter\n        if: github.event_name != 'workflow_dispatch'\n", "        id: filter\n")
	requireCIFullModeProblems(t, yml, "paths-filter")
}

func TestCIFullMode_LibraryAndBlogRunOnDispatch(t *testing.T) {
	t.Parallel()
	for _, lane := range []string{"library", "blog"} {
		t.Run(lane, func(t *testing.T) {
			t.Parallel()
			yml := mutateOnce(t, readWorkflow(t, "ci.yml"),
				"    if: needs.changes.outputs.lane == '"+lane+"' || "+ciDispatch+"\n",
				"    if: needs.changes.outputs.lane == '"+lane+"'\n")
			requireCIFullModeProblems(t, yml, lane+" job")
		})
	}
}

func TestCIFullMode_RollupNameNotDispatchGatedFails(t *testing.T) {
	t.Parallel()
	ci := readWorkflow(t, "ci.yml")
	requireCIFullModeProblems(t, mutateOnce(t, ci, "    name: "+ciNameLine+"\n", "    name: CI\n"), "roll-up")
	swapped := "${{ " + ciDispatch + " && 'CI' || 'CI (nightly)' }}"
	requireCIFullModeProblems(t, mutateOnce(t, ci, ciNameLine, swapped), "roll-up")
}

const (
	ciUploadStepName = "Upload the nightly results"
	ciEnvLine        = "NIGHTLY_RESULTS: ${{ " + ciDispatch + " && 'nightly-results' || '' }}"
	ciGoRetryLine    = `go run ./internal/tools/gotestretry -out "$NIGHTLY_RESULTS/gotest-go.json" -- ./...`
	ciDoclingRetry   = "|| pytest -v --lf --junitxml=/results/junit-docling-retry.xml"
	ciDoclingToday   = `docker run --rm -v "$GITHUB_WORKSPACE:/repo:ro" -e REPO_ROOT=/repo docling:test`
	ciGoFullBlock    = "if [ -n \"$NIGHTLY_RESULTS\" ]; then\nmkdir -p \"$NIGHTLY_RESULTS\"\n" + ciGoRetryLine + "\nelse\ngo test ./...\nfi"
	ciDoclingFull    = "if [ -n \"$NIGHTLY_RESULTS\" ]; then\nmkdir -p \"$NIGHTLY_RESULTS\" && chmod 777 \"$NIGHTLY_RESULTS\"\n" +
		`docker run --rm -v "$GITHUB_WORKSPACE:/repo:ro" -v "$GITHUB_WORKSPACE/$NIGHTLY_RESULTS:/results" -e REPO_ROOT=/repo docling:test sh -c ` +
		`'J="-o junit_family=xunit1 -o junit_suite_name=sidecar/docling"; pytest -v --junitxml=/results/junit-docling.xml $J ` + ciDoclingRetry + ` $J'` + "\nelse\n"
)

var (
	ciResultsJobs  = []string{"go", "migrations", "rls", "idp", "queue", "audit", "frontend", "library", "blog", "hooks", "docling-test"}
	ciVitestJobs   = []string{"frontend", "library", "blog", "hooks"}
	ciUploadLines  = []string{"if: always() && env.NIGHTLY_RESULTS != ''", "uses: actions/upload-artifact@v7", "with:", "name: results-${{ github.job }}", "path: nightly-results/", "if-no-files-found: warn", "retention-days: 14"}
	ciVitestRun    = regexp.MustCompile(`^pnpm --filter @invoice-os/([\w-]+) (test(?::unit|:hooks)?)( .*)?$`)
	ciVitestSuffix = regexp.MustCompile(`^ \$\{NIGHTLY_RESULTS:\+--retry=1 --reporter=default --reporter=json --outputFile\.json=\$GITHUB_WORKSPACE/\$NIGHTLY_RESULTS/(vitest-[\w-]+\.json)\}$`)
)

func ciVitestSuffixFor(name string) string {
	return " ${NIGHTLY_RESULTS:+--retry=1 --reporter=default --reporter=json --outputFile.json=$GITHUB_WORKSPACE/$NIGHTLY_RESULTS/vitest-" + name + ".json}"
}

func ciStepNamed(j workflowJob, name string) (workflowStep, bool) {
	for _, s := range j.steps() {
		if s.keys["name"] == name {
			return s, true
		}
	}
	return workflowStep{}, false
}

// ciUploadStepProblems checks the upload step of one job: present, last, exact fields.
func ciUploadStepProblems(j workflowJob) []string {
	steps := j.steps()
	if len(steps) == 0 || steps[len(steps)-1].keys["name"] != ciUploadStepName {
		return []string{j.name + " job does not end with the upload step"}
	}
	var got []string
	in := false
	for _, l := range j.lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(l, "      - ") {
			in = t == "- name: "+ciUploadStepName
			continue
		}
		if in && t != "" {
			got = append(got, t)
		}
	}
	if !slices.Equal(got, ciUploadLines) {
		return []string{j.name + " job upload step differs: " + strings.Join(got, " / ")}
	}
	return nil
}

// ciMinGoTestLines counts the bare `go test` lines of the results jobs: go Test, JWKS,
// mockissuer, Tier-1 accuracy, wired corpus, end-to-end corpus.
const ciMinGoTestLines = 6

// ciGoTestProblems checks every `go test` line of a job's run blocks: it must sit in the else
// branch of a NIGHTLY_RESULTS guard whose gotestretry line carries the same arguments.
func ciGoTestProblems(j workflowJob) ([]string, int) {
	var p []string
	n := 0
	for _, s := range j.steps() {
		retry, guard, els := "", false, false
		holdsRC, checksRC := false, false
		for _, l := range strings.Split(s.keys["run"], "\n") {
			switch {
			case strings.HasPrefix(l, "#"):
			case l == `[ "$rc" -eq 0 ]`:
				checksRC = true
			case l == `if [ -n "$NIGHTLY_RESULTS" ]; then`:
				guard, retry, els = true, "", false
			case guard && strings.HasPrefix(l, `go run ./internal/tools/gotestretry -out "$NIGHTLY_RESULTS/gotest-`):
				_, args, _ := strings.Cut(l, " -- ")
				var tail string
				retry, tail, _ = strings.Cut(args, " || ")
				holdsRC = tail == "rc=$?"
				if tail != "" && !holdsRC {
					p = append(p, j.name+" job step "+fmt.Sprint(s.keys["name"])+": go test line lacks the full-mode gotestretry branch: the exit of gotestretry is swallowed by "+tail)
				}
			case l == "else":
				els = els || retry != ""
			case strings.Contains(l, "go test "):
				n++
				_, args, _ := strings.Cut(l, "go test ")
				args, _, _ = strings.Cut(args, " | ")
				if !els || retry == "" || retry != strings.TrimSpace(args) {
					p = append(p, j.name+" job step "+fmt.Sprint(s.keys["name"])+": go test line lacks the full-mode gotestretry branch: "+l)
				}
			}
		}
		if holdsRC && !checksRC {
			p = append(p, j.name+" job step "+fmt.Sprint(s.keys["name"])+": go test line lacks the full-mode gotestretry branch: the rc captured from gotestretry is never tested")
		}
	}
	return p, n
}

// ciResultsProblems names each rule of the full-mode results wiring that ci.yml breaks.
func ciResultsProblems(yaml string) []string {
	lines := strings.Split(yaml, "\n")
	var p []string

	if env := blockLines(lines, 0, "env"); !slices.Equal(env, []string{ciEnvLine}) {
		p = append(p, "workflow env is not the single NIGHTLY_RESULTS line: "+strings.Join(env, " / "))
	}
	if n := strings.Count(yaml, "\n      NIGHTLY_RESULTS:") + strings.Count(yaml, "\n        NIGHTLY_RESULTS:"); n != 0 {
		p = append(p, "NIGHTLY_RESULTS is defined below the workflow level")
	}

	if j, ok := ciJobOf(yaml, "go"); !ok {
		p = append(p, "no go job")
	} else if s, ok := ciStepNamed(j, "Test"); !ok {
		p = append(p, "go job has no Test step")
	} else {
		run := s.keys["run"]
		guard, retry, els, plain := strings.Index(run, `if [ -n "$NIGHTLY_RESULTS" ]; then`), strings.Index(run, ciGoRetryLine),
			strings.Index(run, "else\ngo test ./...\nfi"), strings.Index(run, "go test ./...")
		if guard < 0 || retry < guard || els < retry || plain < 0 || !strings.Contains(run, ciGoFullBlock) {
			p = append(p, "go job Test step does not run gotestretry in full mode and go test ./... otherwise")
		}
	}

	for _, id := range ciVitestJobs {
		j, ok := ciJobOf(yaml, id)
		if !ok {
			p = append(p, "no "+id+" job")
			continue
		}
		seen := map[string]bool{}
		for _, s := range j.steps() {
			m := ciVitestRun.FindStringSubmatch(s.keys["run"])
			if m == nil {
				continue
			}
			pkg, script, rest := m[1], m[2], m[3]
			if strings.Contains(s.keys["run"], "NIGHTLY_RESULTS") && !ciVitestSuffix.MatchString(rest) {
				p = append(p, id+" job "+pkg+" step has a malformed results suffix")
				continue
			}
			sm := ciVitestSuffix.FindStringSubmatch(rest)
			if sm == nil {
				p = append(p, id+" job "+pkg+" "+script+" step lacks the results suffix")
				continue
			}
			if seen[sm[1]] {
				p = append(p, id+" job writes "+sm[1]+" twice")
			}
			seen[sm[1]] = true
		}
		for _, s := range j.steps() {
			if r := s.keys["run"]; strings.Contains(r, " build") && strings.Contains(r, "NIGHTLY_RESULTS") {
				p = append(p, id+" job build step carries the results suffix")
			}
		}
	}

	if j, ok := ciJobOf(yaml, "docling-test"); !ok {
		p = append(p, "no docling-test job")
	} else if s, ok := ciStepNamed(j, "Run the pytest suite"); !ok {
		p = append(p, "docling-test has no pytest step")
	} else {
		run := s.keys["run"]
		for _, c := range []struct{ need, why string }{
			{"--junitxml=/results/junit-docling.xml", "no junit-docling.xml output"},
			{ciDoclingRetry, "no --lf re-run into junit-docling-retry.xml on failure"},
			{"junit_family=xunit1", "no junit_family=xunit1"},
			{"junit_suite_name=sidecar/docling", "no junit_suite_name=sidecar/docling"},
			{"chmod 777", "no chmod 777 on the results directory"},
			{"else\n" + ciDoclingToday + "\nfi", "today's command is not the else branch"},
			{ciDoclingFull, "full-mode branch differs from the pinned command (guard, mkdir, volume, options, exit code)"},
		} {
			if !strings.Contains(run, c.need) {
				p = append(p, "docling pytest step: "+c.why)
			}
		}
	}

	goTests := 0
	for _, id := range ciResultsJobs {
		if j, ok := ciJobOf(yaml, id); !ok {
			p = append(p, "no "+id+" job")
		} else {
			p = append(p, ciUploadStepProblems(j)...)
			gp, n := ciGoTestProblems(j)
			p, goTests = append(p, gp...), goTests+n
		}
	}
	if goTests < ciMinGoTestLines {
		p = append(p, fmt.Sprintf("inspected %d go test lines in the results jobs, want at least %d", goTests, ciMinGoTestLines))
	}
	for _, j := range workflowJobsOf(yaml) {
		if _, has := ciStepNamed(j, ciUploadStepName); has && !slices.Contains(ciResultsJobs, j.name) {
			p = append(p, j.name+" job has the upload step but no results")
		}
	}
	return p
}

// mutateInJob replaces old once inside the named job's block.
func mutateInJob(t *testing.T, yml, job, old, repl string) string {
	t.Helper()
	loc := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(job) + `:$`).FindStringIndex(yml)
	if loc == nil {
		t.Fatalf("no job %q", job)
	}
	end := len(yml)
	if next := regexp.MustCompile(`(?m)^  [a-z-]+:$`).FindStringIndex(yml[loc[1]:]); next != nil {
		end = loc[1] + next[0]
	}
	return yml[:loc[0]] + mutateOnce(t, yml[loc[0]:end], old, repl) + yml[end:]
}

func requireCIResultsProblem(t *testing.T, yml string, want ...string) {
	t.Helper()
	got := ciResultsProblems(yml)
	if len(got) == 0 {
		t.Fatalf("no problems, want one naming %q", want)
	}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, w) }) {
			t.Errorf("problems %q lack one naming %q", got, w)
		}
	}
}

func ciUploadStepText() string {
	return "      - name: " + ciUploadStepName + "\n        " + strings.Join(ciUploadLines[:3], "\n        ") +
		"\n          " + strings.Join(ciUploadLines[3:], "\n          ") + "\n"
}

func TestCIResults_RealFileHasNoProblems(t *testing.T) {
	t.Parallel()
	if got := ciResultsProblems(readWorkflow(t, "ci.yml")); len(got) != 0 {
		t.Fatalf("ci.yml results problems: %q", got)
	}
}

func TestCIResults_EnvNotDispatchGatedFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  "+ciEnvLine+"\n", "  NIGHTLY_RESULTS: nightly-results\n")
	requireCIResultsProblem(t, yml, "NIGHTLY_RESULTS")
}

func TestCIResults_EnvRedefinedInAJobFails(t *testing.T) {
	t.Parallel()
	yml := mutateInJob(t, readWorkflow(t, "ci.yml"), "audit", "    runs-on: ubuntu-latest\n",
		"    runs-on: ubuntu-latest\n    env:\n      NIGHTLY_RESULTS: x\n")
	requireCIResultsProblem(t, yml, "below the workflow level")
}

func TestCIResults_GoJobWritesJSONOnlyInFullMode(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), ciGoRetryLine, "go test -json ./... > x")
	requireCIResultsProblem(t, yml, "go job Test step")
	yml = mutateOnce(t, readWorkflow(t, "ci.yml"), "              go test ./...\n            fi", "              echo skipped\n            fi")
	requireCIResultsProblem(t, yml, "go job Test step")
}

func TestCIResults_EveryVitestStepHasTheSuffix(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "ci.yml")
	count := 0
	for _, id := range ciVitestJobs {
		j, _ := ciJobOf(yml, id)
		for _, s := range j.steps() {
			if ciVitestRun.MatchString(s.keys["run"]) {
				count++
			}
		}
	}
	if count != 17 {
		t.Fatalf("found %d vitest steps in the four jobs, want 17", count)
	}
	drop := mutateInJob(t, yml, "frontend", ciVitestSuffixFor("app"), "")
	requireCIResultsProblem(t, drop, "frontend job app")
	noRetry := mutateInJob(t, yml, "blog", "--retry=1 --reporter=default --reporter=json --outputFile.json=$GITHUB_WORKSPACE/$NIGHTLY_RESULTS/vitest-blog.json",
		"--reporter=default --reporter=json --outputFile.json=$GITHUB_WORKSPACE/$NIGHTLY_RESULTS/vitest-blog.json")
	requireCIResultsProblem(t, noRetry, "blog job blog")
	hooks := mutateInJob(t, yml, "hooks", ciVitestSuffixFor("e2e-hooks"), "")
	requireCIResultsProblem(t, hooks, "hooks job e2e")
}

func TestCIResults_VitestFileNamesUniquePerJob(t *testing.T) {
	t.Parallel()
	yml := mutateInJob(t, readWorkflow(t, "ci.yml"), "frontend", ciVitestSuffixFor("app"), ciVitestSuffixFor("library"))
	requireCIResultsProblem(t, yml, "writes vitest-library.json twice")
}

func TestCIResults_BuildStepsUntouched(t *testing.T) {
	t.Parallel()
	yml := mutateInJob(t, readWorkflow(t, "ci.yml"), "library", "pnpm --filter @invoice-os/library build", "pnpm --filter @invoice-os/library build"+ciVitestSuffixFor("library"))
	requireCIResultsProblem(t, yml, "library job build step")
}

func TestCIResults_DoclingStepWritesJUnitInFullMode(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "ci.yml")
	requireCIResultsProblem(t, mutateOnce(t, yml, " -o junit_suite_name=sidecar/docling", ""), "junit_suite_name")
	requireCIResultsProblem(t, mutateOnce(t, yml, " "+ciDoclingRetry+" $J", ""), "--lf re-run")
	requireCIResultsProblem(t, mutateOnce(t, yml, "junit_family=xunit1", "junit_family=legacy"), "junit_family")
	requireCIResultsProblem(t, mutateOnce(t, yml, "chmod 777", "chmod 755"), "chmod 777")
	requireCIResultsProblem(t, mutateOnce(t, yml, "            "+ciDoclingToday+"\n          fi", "            true\n          fi"), "today's command")
}

func TestCIResults_UploadStepOnExactlyTheResultsJobs(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "ci.yml")
	requireCIResultsProblem(t, mutateInJob(t, yml, "audit", ciUploadStepText(), ""), "audit job does not end")
	requireCIResultsProblem(t, mutateInJob(t, yml, "audit", "        if: always() && env.NIGHTLY_RESULTS != ''\n", "        if: always()\n"), "audit job upload step differs")
	requireCIResultsProblem(t, mutateInJob(t, yml, "queue", "retention-days: 14", "retention-days: 90"), "queue job upload step differs")
	requireCIResultsProblem(t, mutateInJob(t, yml, "queue", "name: results-${{ github.job }}", "name: results"), "queue job upload step differs")
	requireCIResultsProblem(t, mutateInJob(t, yml, "idp", "if-no-files-found: warn", "if-no-files-found: error"), "idp job upload step differs")
	only := mutateInJob(t, yml, "stale-refs", "      - uses: actions/checkout@v4\n", "      - uses: actions/checkout@v4\n"+ciUploadStepText())
	requireCIResultsProblem(t, only, "stale-refs job has the upload step")
}

func TestCIResults_EveryGoTestLineHasTheRetryBranch(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "ci.yml")
	total := 0
	for _, id := range ciResultsJobs {
		j, _ := ciJobOf(yml, id)
		_, n := ciGoTestProblems(j)
		total += n
	}
	if total < ciMinGoTestLines {
		t.Fatalf("inspected %d go test lines, want at least %d", total, ciMinGoTestLines)
	}
	if got := ciResultsProblems(yml); len(got) != 0 {
		t.Fatalf("ci.yml results problems: %q", got)
	}
}

func TestCIResults_GoTestStepsRouteThroughGotestretry(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "ci.yml")
	for _, c := range []struct{ name, job, old, repl string }{
		{"jwks retry dropped", "go", `-out "$NIGHTLY_RESULTS/gotest-jwks.json" -- -race`, `-out "$NIGHTLY_RESULTS/gotest-jwks.json" -- -x -race`},
		{"jwks guard dropped", "go", "if [ -n \"$NIGHTLY_RESULTS\" ]; then\n            mkdir -p \"$NIGHTLY_RESULTS\"\n            go run ./internal/tools/gotestretry -out \"$NIGHTLY_RESULTS/gotest-jwks.json\"", "if true; then\n            go run ./internal/tools/gotestretry -out \"$NIGHTLY_RESULTS/gotest-jwks.json\""},
		{"mockissuer retry dropped", "go", `gotest-mockissuer.json" -- -tags mockissuer`, `gotest-mockissuer.json" -- -tags other`},
		{"accuracy retry dropped", "go", `gotest-accuracy.json" -- -count=1 -v`, `gotest-accuracy.json" -- -count=1`},
		{"wired retry dropped", "queue", `gotest-wired.json" -- -count=1 -v`, `gotest-wired.json" -- -count=1`},
		{"endtoend retry dropped", "queue", `gotest-endtoend.json" -- -count=1 -v`, `gotest-endtoend.json" -- -count=1`},
	} {
		t.Run(c.name, func(t *testing.T) {
			requireCIResultsProblem(t, mutateInJob(t, yml, c.job, c.old, c.repl), "go test line lacks")
		})
	}
	for _, c := range []struct{ name, job, old, repl string }{
		{"accuracy exit swallowed", "go", "-run 'TestTier1Accuracy' ./internal/extraction/ || rc=$?", "-run 'TestTier1Accuracy' ./internal/extraction/ || true"},
		{"accuracy rc test dropped", "go", "tee accuracy.txt\n            [ \"$rc\" -eq 0 ]\n", "tee accuracy.txt\n"},
		{"wired rc test dropped", "queue", "tee wired-corpus.txt\n            [ \"$rc\" -eq 0 ]\n", "tee wired-corpus.txt\n"},
		{"endtoend rc test dropped", "queue", "tee endtoend-corpus.txt\n            [ \"$rc\" -eq 0 ]\n", "tee endtoend-corpus.txt\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			requireCIResultsProblem(t, mutateInJob(t, yml, c.job, c.old, c.repl), "go test line lacks")
		})
	}
	// A bare go test line outside any guard fails the same check.
	requireCIResultsProblem(t, mutateInJob(t, yml, "queue", "      - name: Upload the nightly results\n", "      - name: Bare\n        run: go test ./x/\n      - name: Upload the nightly results\n"), "go test line lacks")
}
