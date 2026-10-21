// advisories_workflow_test.go pins the advisories job of ci.yml, its place in the CI rollup and the two jobs of advisories-nightly.yml.
package main

import (
	"slices"
	"strings"
	"testing"
)

const (
	advisoriesCmd      = "bash scripts/ci/advisories.sh"
	advisoriesGuard    = `if [ "${{ needs.advisories.result }}" != "success" ]; then` + "\n" + `echo "::error::advisories job failed"; exit 1` + "\n" + "fi"
	advisoriesEcho     = `echo "advisories:    ${{ needs.advisories.result }}"`
	nightlyScheduleYML = "  schedule:\n    - cron: '23 5 * * *'\n"
	nightlyPullReqYML  = "  pull_request:\n    paths:\n" +
		"      - '.github/workflows/advisories-nightly.yml'\n" +
		"      - 'scripts/ci/advisories.sh'\n" +
		"      - 'internal/tools/advisorygate/**'\n"
)

// advisoriesJobProblems checks the parts the ci.yml job and the nightly job share.
func advisoriesJobProblems(label string, job workflowJob) []string {
	var problems []string
	if strings.Contains(strings.Join(job.lines, "\n"), "continue-on-error") {
		problems = append(problems, label+" has continue-on-error")
	}
	ran := false
	for _, s := range job.steps() {
		if _, ok := s.keys["if"]; ok {
			problems = append(problems, label+" has a step-level if:")
		}
		run := strings.TrimSpace(s.keys["run"])
		if strings.HasPrefix(run, advisoriesCmd) {
			ran = true
			if strings.Contains(run, "||") {
				problems = append(problems, label+" run swallows the exit code (||)")
			}
		}
	}
	if !ran {
		problems = append(problems, label+" does not run scripts/ci/advisories.sh")
	}
	return problems
}

func advisoriesProblems(ciYAML, nightlyYAML string) []string {
	var job, ci *workflowJob
	jobs := workflowJobsOf(ciYAML)
	for i := range jobs {
		switch jobs[i].name {
		case "advisories":
			job = &jobs[i]
		case "ci":
			ci = &jobs[i]
		}
	}
	if job == nil {
		return []string{"no advisories job"}
	}
	var problems []string
	if _, ok := jobKey(*job, "if"); ok {
		problems = append(problems, "advisories has an if:")
	}
	if _, ok := jobKey(*job, "needs"); ok {
		problems = append(problems, "advisories has needs:")
	}
	problems = append(problems, advisoriesJobProblems("advisories", *job)...)
	if ci == nil {
		return append(problems, "no ci job")
	}
	if !slices.Contains(jobList(*ci, "needs"), "advisories") {
		problems = append(problems, "ci needs lacks advisories")
	}
	var rollup string
	for _, s := range ci.steps() {
		rollup += s.keys["run"] + "\n"
	}
	if !strings.Contains(rollup, advisoriesEcho) {
		problems = append(problems, "ci rollup lacks the advisories result echo")
	}
	if !strings.Contains(rollup, advisoriesGuard) {
		problems = append(problems, `ci rollup lacks the != "success" exit for advisories`)
	}
	problems = append(problems, nightlyProblems(nightlyYAML)...)
	return append(problems, govulncheckDriftProblems(jobs, workflowJobsOf(nightlyYAML))...)
}

// govulncheckDriftProblems requires the nightly scan line to equal the ci.yml one.
func govulncheckDriftProblems(ciJobs, nightlyJobs []workflowJob) []string {
	find := func(jobs []workflowJob) *workflowJob {
		for i := range jobs {
			if jobs[i].name == "govulncheck" {
				return &jobs[i]
			}
		}
		return nil
	}
	ci, nightly := find(ciJobs), find(nightlyJobs)
	if ci != nil && nightly != nil && govulncheckCommand(*ci) != govulncheckCommand(*nightly) {
		return []string{"nightly govulncheck command differs from ci.yml"}
	}
	return nil
}

func nightlyProblems(yml string) []string {
	lines := stripHashComments(strings.Split(yml, "\n"))
	text := strings.Join(lines, "\n")
	var problems []string
	if !strings.Contains(text, "\n"+nightlyScheduleYML) {
		problems = append(problems, "nightly has no schedule with the cron")
	}
	if !strings.Contains(text, "\n  workflow_dispatch:\n") {
		problems = append(problems, "nightly has no workflow_dispatch")
	}
	if !strings.Contains(text, "\n  pull_request:\n") {
		problems = append(problems, "nightly has no pull_request trigger")
	} else if !strings.Contains(text, "\n"+nightlyPullReqYML) {
		problems = append(problems, "nightly pull_request paths are not the workflow, the script and the gate")
	}
	var perms []string
	for i, l := range lines {
		if l != "permissions:" {
			continue
		}
		for _, n := range lines[i+1:] {
			if !strings.HasPrefix(n, "  ") {
				break
			}
			perms = append(perms, strings.TrimSpace(n))
		}
	}
	if !slices.Equal(perms, []string{"contents: read"}) {
		problems = append(problems, "nightly permissions are not only contents: read; no issues permission allowed")
	}
	if strings.Contains(text, "gh issue") {
		problems = append(problems, "nightly opens an issue (gh issue)")
	}
	var advisories, vuln *workflowJob
	jobs := workflowJobsOf(yml)
	for i := range jobs {
		switch jobs[i].name {
		case "advisories":
			advisories = &jobs[i]
		case "govulncheck":
			vuln = &jobs[i]
		}
	}
	if advisories == nil {
		problems = append(problems, "nightly has no advisories job")
	} else {
		if _, ok := jobKey(*advisories, "if"); ok {
			problems = append(problems, "nightly has an if:")
		}
		if _, ok := jobKey(*advisories, "needs"); ok {
			problems = append(problems, "nightly has needs:")
		}
		if _, ok := jobKey(*advisories, "permissions"); ok {
			problems = append(problems, "nightly job overrides permissions; no issues permission allowed")
		}
		problems = append(problems, advisoriesJobProblems("nightly", *advisories)...)
	}
	if vuln == nil {
		return append(problems, "nightly has no govulncheck job")
	}
	if _, ok := jobKey(*vuln, "permissions"); ok {
		problems = append(problems, "nightly govulncheck job overrides permissions; no issues permission allowed")
	}
	return append(problems, govulncheckJobProblems("nightly govulncheck", *vuln)...)
}

func requireAdvisoriesProblem(t *testing.T, ciYML, nightlyYML, want string) {
	t.Helper()
	got := advisoriesProblems(ciYML, nightlyYML)
	if !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, want) }) {
		t.Errorf("problems %q lack one containing %q", got, want)
	}
}

func mutateCI(t *testing.T, old, repl string) (ci, nightly string) {
	t.Helper()
	return mutateOnce(t, readWorkflow(t, "ci.yml"), old, repl), readWorkflow(t, "advisories-nightly.yml")
}

func mutateNightly(t *testing.T, old, repl string) (ci, nightly string) {
	t.Helper()
	return readWorkflow(t, "ci.yml"), mutateOnce(t, readWorkflow(t, "advisories-nightly.yml"), old, repl)
}

func nightlyJob(t *testing.T, id string) workflowJob {
	t.Helper()
	for _, j := range workflowJobsOf(readWorkflow(t, "advisories-nightly.yml")) {
		if j.name == id {
			return j
		}
	}
	t.Fatalf("advisories-nightly.yml has no job %q", id)
	return workflowJob{}
}

func TestCIAdvisories_RepoIsWired(t *testing.T) {
	t.Parallel()
	if got := advisoriesProblems(readWorkflow(t, "ci.yml"), readWorkflow(t, "advisories-nightly.yml")); len(got) != 0 {
		t.Errorf("advisories wiring problems: %q", got)
	}
	if n := len(ciJob(t, "advisories").steps()); n < 3 {
		t.Errorf("control: advisories has %d steps, want at least 3", n)
	}
	if n := len(nightlyJob(t, "govulncheck").steps()); n < 3 {
		t.Errorf("control: nightly govulncheck has %d steps, want at least 3", n)
	}
}

func TestCIAdvisories_IfConditionFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, "    name: advisories\n", "    name: advisories\n    if: needs.changes.outputs.go == 'true'\n")
	requireAdvisoriesProblem(t, ci, n, "has an if:")
}

func TestCIAdvisories_NeedsFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, "    name: advisories\n", "    name: advisories\n    needs: changes\n")
	requireAdvisoriesProblem(t, ci, n, "has needs:")
}

func TestCIAdvisories_ContinueOnErrorFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, "    name: advisories\n", "    name: advisories\n    continue-on-error: true\n")
	requireAdvisoriesProblem(t, ci, n, "continue-on-error")
}

func TestCIAdvisories_StepIfFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, "      - name: Scan for high or critical advisories\n",
		"      - name: Scan for high or critical advisories\n        if: github.event_name == 'push'\n")
	requireAdvisoriesProblem(t, ci, n, "step-level if:")
}

func TestCIAdvisories_SwallowedExitFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, "        run: "+advisoriesCmd+"\n", "        run: "+advisoriesCmd+" || true\n")
	requireAdvisoriesProblem(t, ci, n, "swallows the exit code")
}

func TestCIAdvisories_ScanStepMissingFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, "        run: "+advisoriesCmd+"\n", "        run: echo ok\n")
	requireAdvisoriesProblem(t, ci, n, "does not run scripts/ci/advisories.sh")
}

func TestCIAdvisories_MissingJobFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, "  advisories:\n    name:", "  advisoriez:\n    name:")
	if got := advisoriesProblems(ci, n); !slices.Equal(got, []string{"no advisories job"}) {
		t.Errorf("problems = %q, want exactly [no advisories job]", got)
	}
}

func TestCIAdvisories_RollupNeedsMissingFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, "history-rewrite, govulncheck, advisories]", "history-rewrite, govulncheck]")
	requireAdvisoriesProblem(t, ci, n, "ci needs lacks advisories")
}

func TestCIAdvisories_RollupEchoOnlyFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, `echo "::error::advisories job failed"; exit 1`, `echo "::error::advisories job failed"`)
	requireAdvisoriesProblem(t, ci, n, "rollup lacks")
}

// A skipped or cancelled scan must fail CI: other guards in ci.yml hold != "success", so the needle is the full line.
func TestCIAdvisories_RollupFailureOnlyFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, `if [ "${{ needs.advisories.result }}" != "success" ]; then`,
		`if [ "${{ needs.advisories.result }}" = "failure" ]; then`)
	requireAdvisoriesProblem(t, ci, n, "rollup lacks")
}

func TestAdvisoriesNightly_NoScheduleFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, nightlyScheduleYML, "")
	requireAdvisoriesProblem(t, ci, n, "no schedule")
}

func TestAdvisoriesNightly_NoPullRequestFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, nightlyPullReqYML, "")
	requireAdvisoriesProblem(t, ci, n, "no pull_request")
}

func TestAdvisoriesNightly_PullRequestPathsFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "      - 'scripts/ci/advisories.sh'\n", "")
	requireAdvisoriesProblem(t, ci, n, "pull_request paths")
}

func TestAdvisoriesNightly_IssuesPermissionFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "  contents: read\n", "  contents: read\n  issues: write\n")
	requireAdvisoriesProblem(t, ci, n, "issues")
}

func TestAdvisoriesNightly_OpensIssueFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "        run: "+advisoriesCmd+"\n",
		"        run: "+advisoriesCmd+"\n      - run: gh issue create --title x --body y\n")
	requireAdvisoriesProblem(t, ci, n, "issue")
}

func TestAdvisoriesNightly_DifferentCommandFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "        run: "+advisoriesCmd+"\n", "        run: echo ok\n")
	requireAdvisoriesProblem(t, ci, n, "does not run scripts/ci/advisories.sh")
}

func TestAdvisoriesNightly_ContinueOnErrorFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "    name: advisories nightly\n", "    name: advisories nightly\n    continue-on-error: true\n")
	requireAdvisoriesProblem(t, ci, n, "continue-on-error")
}

func TestAdvisoriesNightly_NoWorkflowDispatchFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "  workflow_dispatch:\n", "")
	requireAdvisoriesProblem(t, ci, n, "no workflow_dispatch")
}

func TestAdvisoriesNightly_CommentedScheduleFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, nightlyScheduleYML, "  # schedule:\n  #   - cron: '23 5 * * *'\n")
	requireAdvisoriesProblem(t, ci, n, "no schedule")
}

func TestAdvisoriesNightly_JobIssuesPermissionFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "    name: advisories nightly\n", "    name: advisories nightly\n    permissions:\n      issues: write\n")
	requireAdvisoriesProblem(t, ci, n, "issues")
}

func TestAdvisoriesNightly_JobIfFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "    name: advisories nightly\n", "    name: advisories nightly\n    if: false\n")
	requireAdvisoriesProblem(t, ci, n, "has an if:")
}

func TestCIAdvisories_RollupEchoMissingFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateCI(t, advisoriesEcho+"\n", "")
	requireAdvisoriesProblem(t, ci, n, "result echo")
}

func TestAdvisoriesNightly_MissingGovulncheckJobFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "  govulncheck:\n    name:", "  govulnscan:\n    name:")
	requireAdvisoriesProblem(t, ci, n, "nightly has no govulncheck job")
}

func TestAdvisoriesNightly_GovulncheckIfFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "    name: govulncheck nightly\n", "    name: govulncheck nightly\n    if: github.event_name == 'schedule'\n")
	requireAdvisoriesProblem(t, ci, n, "nightly govulncheck has an if:")
}

func TestAdvisoriesNightly_GovulncheckStepIfFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "      - name: Scan for reachable vulnerabilities\n", "      - name: Scan for reachable vulnerabilities\n        if: github.event_name == 'schedule'\n")
	requireAdvisoriesProblem(t, ci, n, "nightly govulncheck has a step-level if:")
}

func TestAdvisoriesNightly_GovulncheckOrTrueFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, govulncheckCmd, govulncheckCmd+" || true")
	requireAdvisoriesProblem(t, ci, n, "swallows the exit code")
}

func TestAdvisoriesNightly_GovulncheckJSONFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, govulncheckCmd, "go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -json ./...")
	requireAdvisoriesProblem(t, ci, n, "-json or -format")
}

func TestAdvisoriesNightly_GovulncheckContinueOnErrorFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "    name: govulncheck nightly\n", "    name: govulncheck nightly\n    continue-on-error: true\n")
	requireAdvisoriesProblem(t, ci, n, "nightly govulncheck has continue-on-error")
}

func TestAdvisoriesNightly_GovulncheckVersionDriftFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "govulncheck@v1.9.0", "govulncheck@v1.8.0")
	requireAdvisoriesProblem(t, ci, n, "differs from ci.yml")
}

func TestAdvisoriesNightly_GovulncheckCgoFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, `CGO_ENABLED: "0"`, `CGO_ENABLED: "1"`)
	requireAdvisoriesProblem(t, ci, n, "CGO_ENABLED")
}

func TestAdvisoriesNightly_GovulncheckNoGoVersionLineFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "          go version\n          "+govulncheckCmd, "          "+govulncheckCmd)
	requireAdvisoriesProblem(t, ci, n, "lacks go version")
}

func TestAdvisoriesNightly_GovulncheckGoVersionFileFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "          go-version-file: go.mod\n      - name: Scan for reachable vulnerabilities", "          go-version: stable\n      - name: Scan for reachable vulnerabilities")
	requireAdvisoriesProblem(t, ci, n, "go-version-file")
}

func TestAdvisoriesNightly_GovulncheckNeedsFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "    name: govulncheck nightly\n", "    name: govulncheck nightly\n    needs: advisories\n")
	requireAdvisoriesProblem(t, ci, n, "nightly govulncheck has needs:")
}

func TestAdvisoriesNightly_AdvisoriesNeedsFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "    name: advisories nightly\n", "    name: advisories nightly\n    needs: govulncheck\n")
	requireAdvisoriesProblem(t, ci, n, "nightly has needs:")
}

func TestAdvisoriesNightly_GovulncheckJobPermissionsFails(t *testing.T) {
	t.Parallel()
	ci, n := mutateNightly(t, "    name: govulncheck nightly\n", "    name: govulncheck nightly\n    permissions:\n      issues: write\n")
	requireAdvisoriesProblem(t, ci, n, "overrides permissions")
}
