// nightly_workflow_test.go pins nightly.yml and scripts/ci/nightly-suites.txt against ci.yml.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	nightlyGuardRun   = `[ "$GITHUB_REF" = refs/heads/main ] || { echo "::error::Nightly runs on main only, not on $GITHUB_REF"; exit 1; }`
	nightlyCron       = "- cron: '17 1 * * *'"
	nightlySuitesFlag = "-suites scripts/ci/nightly-suites.txt"
)

var (
	nightlyNotReported = []string{"changes", "ci", "history-rewrite", "advisories", "govulncheck"}
	nightlyUploadLines = []string{"if: always()", "uses: actions/upload-artifact@v7", "name: nightly-summary", "path: ${{ runner.temp }}/summary.json", "if-no-files-found: error", "retention-days: 14"}
)

// nightlyStepLines returns the trimmed lines of the first step of j that has a line containing marker.
func nightlyStepLines(j workflowJob, marker string) []string {
	var cur []string
	flush := func() []string {
		if slices.ContainsFunc(cur, func(l string) bool { return strings.Contains(l, marker) }) {
			return cur
		}
		return nil
	}
	for _, l := range j.lines {
		if strings.HasPrefix(l, "      - ") {
			if got := flush(); got != nil {
				return got
			}
			cur = nil
		}
		if strings.HasPrefix(l, "      ") && strings.TrimSpace(l) != "" {
			cur = append(cur, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")))
		}
	}
	return flush()
}

// nightlyRunProblems names each rule of nightly.yml and nightly-suites.txt that the files break.
func nightlyRunProblems(nightlyYAML, ciYAML, suitesTxt string) []string {
	lines := strings.Split(nightlyYAML, "\n")
	var p []string

	for _, l := range stripHashComments(lines) {
		if strings.HasPrefix(l, "name:") && strings.TrimSpace(strings.TrimPrefix(l, "name:")) != "Nightly" {
			p = append(p, "name is not Nightly: "+l)
		}
	}

	on := onBlockLines(lines)
	for _, want := range []string{"schedule:", "workflow_dispatch:"} {
		if !slices.Contains(on, want) {
			p = append(p, "on: lacks "+want)
		}
	}
	for _, l := range on {
		if !slices.Contains([]string{"schedule:", "workflow_dispatch:"}, l) {
			p = append(p, "on: names the trigger "+l)
		}
	}
	if !slices.Equal(blockLines(lines, 2, "schedule"), []string{nightlyCron}) {
		p = append(p, "on: schedule is not the single "+nightlyCron)
	}

	conc := blockLines(lines, 0, "concurrency")
	if !slices.Contains(conc, "group: nightly") {
		p = append(p, "concurrency group is not the fixed nightly")
	}
	if !slices.Contains(conc, "cancel-in-progress: false") {
		p = append(p, "concurrency cancel-in-progress is not false")
	}
	if !slices.Equal(blockLines(lines, 0, "permissions"), []string{"contents: read"}) {
		p = append(p, "workflow permissions are not contents: read only")
	}
	for _, l := range stripHashComments(lines) {
		if strings.Contains(l, "issues") || strings.Contains(l, "gh issue") {
			p = append(p, "nightly.yml names issues: "+strings.TrimSpace(l))
		}
	}

	var mainOnly, summary workflowJob
	var haveMain, haveSummary bool
	for _, j := range workflowJobsOf(nightlyYAML) {
		switch j.name {
		case "main-only":
			mainOnly, haveMain = j, true
		case "summary":
			summary, haveSummary = j, true
		}
	}
	if !haveMain {
		p = append(p, "no main-only guard job")
	} else {
		if _, ok := jobKey(mainOnly, "if"); ok {
			p = append(p, "main-only guard has an if: a skipped guard fails nothing")
		}
		steps := mainOnly.steps()
		if len(steps) != 1 || steps[0].keys["run"] != "'"+nightlyGuardRun+"'" {
			p = append(p, "main-only guard step differs from the pinned run line")
		}
	}
	if !haveSummary {
		return append(p, "no summary job")
	}
	if v, _ := jobKey(summary, "needs"); v != "main-only" {
		p = append(p, "summary does not need main-only")
	}
	if _, ok := jobKey(summary, "if"); ok {
		p = append(p, "summary has an if")
	}
	perms := blockLines(summary.lines, 4, "permissions")
	slices.Sort(perms)
	if !slices.Equal(perms, []string{"actions: write", "contents: read"}) {
		p = append(p, "summary permissions are not contents: read and actions: write")
	}

	asm := nightlyStepLines(summary, "internal/tools/nightlysummary")
	switch {
	case asm == nil:
		p = append(p, "summary has no assembler step")
	default:
		if !slices.Contains(asm, "if: always()") {
			p = append(p, "assembler step does not run always()")
		}
		if !strings.Contains(strings.Join(asm, "\n"), nightlySuitesFlag+" ") {
			p = append(p, "assembler step lacks "+nightlySuitesFlag)
		}
	}
	up := nightlyStepLines(summary, "uses: actions/upload-artifact@")
	if up == nil {
		p = append(p, "summary has no upload step")
	}
	for _, want := range nightlyUploadLines {
		if up != nil && !slices.Contains(up, want) {
			p = append(p, "upload step lacks "+want)
		}
	}

	return append(p, nightlySuitesProblems(ciYAML, suitesTxt)...)
}

// nightlySuitesProblems checks nightly-suites.txt against the jobs of ci.yml.
func nightlySuitesProblems(ciYAML, suitesTxt string) []string {
	var p []string
	type suite struct {
		name    string
		results bool
	}
	suites := map[string]suite{}
	for _, l := range strings.Split(suitesTxt, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Split(l, "=")
		if len(f) < 2 || len(f) > 3 || (len(f) == 3 && f[2] != "results") {
			p = append(p, "suites line is malformed: "+l)
			continue
		}
		if _, dup := suites[f[0]]; dup {
			p = append(p, "suites lists "+f[0]+" twice")
		}
		suites[f[0]] = suite{name: f[1], results: len(f) == 3}
	}
	ciJobs := map[string]workflowJob{}
	for _, j := range workflowJobsOf(ciYAML) {
		ciJobs[j.name] = j
		if slices.Contains(nightlyNotReported, j.name) {
			if _, ok := suites[j.name]; ok {
				p = append(p, "suites lists "+j.name+", which the summary does not report")
			}
			continue
		}
		s, ok := suites[j.name]
		if !ok {
			p = append(p, "suites lacks the ci.yml job "+j.name)
			continue
		}
		if name, _ := jobKey(j, "name"); name != s.name {
			p = append(p, "suites name of "+j.name+" is \""+s.name+"\", ci.yml has \""+name+"\"")
		}
		if _, up := ciStepNamed(j, ciUploadStepName); up != s.results {
			p = append(p, "suites results marker of "+j.name+" does not match its upload step")
		}
	}
	for key := range suites {
		if _, ok := ciJobs[key]; !ok {
			p = append(p, "suites lists "+key+", which is no ci.yml job")
		}
	}
	slices.Sort(p)
	return p
}

func nightlyFiles(t *testing.T) (nightly, ci, suites string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "ci", "nightly-suites.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return readWorkflow(t, "nightly.yml"), readWorkflow(t, "ci.yml"), string(raw)
}

// requireNightlyProblems fails unless every want is a substring of some problem.
func requireNightlyProblems(t *testing.T, nightly, ci, suites string, want ...string) {
	t.Helper()
	got := nightlyRunProblems(nightly, ci, suites)
	if len(got) == 0 {
		t.Fatalf("no problems, want ones naming %q", want)
	}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, w) }) {
			t.Errorf("problems %q lack one naming %q", got, w)
		}
	}
}

func requireNightlyMutation(t *testing.T, old, repl, want string) {
	t.Helper()
	nightly, ci, suites := nightlyFiles(t)
	requireNightlyProblems(t, mutateOnce(t, nightly, old, repl), ci, suites, want)
}

func TestNightly_RealFilesHaveNoProblems(t *testing.T) {
	t.Parallel()
	nightly, ci, suites := nightlyFiles(t)
	if got := nightlyRunProblems(nightly, ci, suites); len(got) > 0 {
		t.Errorf("problems: %q", got)
	}
}

func TestNightly_TriggerMutations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, old, repl, want string }{
		{"cron changed", nightlyCron, "- cron: '0 2 * * *'", "schedule"},
		{"push added", "  workflow_dispatch:\n", "  workflow_dispatch:\n  push:\n", "push:"},
		{"pull_request added", "  workflow_dispatch:\n", "  workflow_dispatch:\n  pull_request:\n", "pull_request:"},
		{"dispatch dropped", "  workflow_dispatch:\n", "", "workflow_dispatch:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireNightlyMutation(t, tc.old, tc.repl, tc.want)
		})
	}
}

func TestNightly_ConcurrencyMutations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, old, repl, want string }{
		{"cancel true", "cancel-in-progress: false", "cancel-in-progress: true", "cancel-in-progress"},
		{"dropped", "concurrency:\n  group: nightly\n  cancel-in-progress: false\n", "", "concurrency"},
		{"per-run group", "group: nightly", "group: nightly-${{ github.run_id }}", "fixed nightly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireNightlyMutation(t, tc.old, tc.repl, tc.want)
		})
	}
}

func TestNightly_NameMustBeNightly(t *testing.T) {
	t.Parallel()
	requireNightlyMutation(t, "name: Nightly\n", "name: Nightly run\n", "name is not Nightly")
}

func TestNightly_MainOnlyGuard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, old, repl, want string }{
		{"job dropped", "  main-only:\n", "  mainonly:\n", "no main-only guard job"},
		{"needs dropped", "    needs: main-only\n", "", "summary does not need main-only"},
		{"test inverted", "= refs/heads/main", "!= refs/heads/main", "main-only guard"},
		{"exit dropped", "; exit 1; }", "; }", "main-only guard"},
		{"ref dropped from the error", "not on $GITHUB_REF\"", "not on main\"", "main-only guard"},
		{"job if added", "    timeout-minutes: 2\n", "    timeout-minutes: 2\n    if: github.ref == 'refs/heads/main'\n", "main-only guard has an if"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireNightlyMutation(t, tc.old, tc.repl, tc.want)
		})
	}
}

func TestNightly_MainOnlyGuardRuns(t *testing.T) {
	t.Parallel()
	nightly, _, _ := nightlyFiles(t)
	var guard string
	for _, j := range workflowJobsOf(nightly) {
		if j.name == "main-only" {
			guard = strings.Trim(j.steps()[0].keys["run"], "'")
		}
	}
	if guard == "" {
		t.Fatal("no main-only run")
	}
	for _, tc := range []struct {
		ref, out string
		code     int
	}{
		{"refs/heads/main", "", 0},
		{"refs/heads/epic/nightly", "::error::Nightly runs on main only, not on refs/heads/epic/nightly\n", 1},
	} {
		cmd := exec.Command("bash", "-c", guard)
		cmd.Env = append(os.Environ(), "GITHUB_REF="+tc.ref)
		stdout, _, err := runToFiles(t, cmd, false)
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.code || stdout != tc.out {
			t.Errorf("GITHUB_REF=%s: exit %d stdout %q, want exit %d stdout %q", tc.ref, code, stdout, tc.code, tc.out)
		}
	}
}

func TestNightly_ResultsMarkerMatchesUploadSteps(t *testing.T) {
	t.Parallel()
	nightly, ci, suites := nightlyFiles(t)
	requireNightlyProblems(t, nightly, ci, mutateOnce(t, suites, "audit=Audit suite (append-only + atomicity)=results", "audit=Audit suite (append-only + atomicity)"), "audit")
	requireNightlyProblems(t, nightly, ci, mutateOnce(t, suites, "clean-clone=Clean-clone build", "clean-clone=Clean-clone build=results"), "clean-clone")
}

func TestNightly_SuitesMissingCIJobFails(t *testing.T) {
	t.Parallel()
	nightly, ci, suites := nightlyFiles(t)
	requireNightlyProblems(t, nightly, ci, mutateOnce(t, suites, "caddy-headers=Caddyfile frame headers\n", ""), "caddy-headers")
}

func TestNightly_SuitesWrongDisplayNameFails(t *testing.T) {
	t.Parallel()
	nightly, ci, suites := nightlyFiles(t)
	requireNightlyProblems(t, nightly, ci, mutateOnce(t, suites, "rls=RLS isolation (adversarial)=results", "rls=RLS isolation=results"), "rls", "RLS isolation (adversarial)")
}

func TestNightly_SuitesListsExcludedJobFails(t *testing.T) {
	t.Parallel()
	nightly, ci, suites := nightlyFiles(t)
	got := nightlyRunProblems(nightly, ci, suites+"govulncheck=govulncheck\nhistory-rewrite=History rewrite\n")
	for _, want := range []string{"govulncheck", "history-rewrite"} {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, want) }) {
			t.Errorf("problems %q lack one naming %q", got, want)
		}
	}
}

func TestNightly_NewCIJobMustBeListed(t *testing.T) {
	t.Parallel()
	nightly, ci, suites := nightlyFiles(t)
	ci = mutateOnce(t, ci, "  history-rewrite:\n", "  newjob:\n    name: New job\n    runs-on: ubuntu-latest\n\n  history-rewrite:\n")
	requireNightlyProblems(t, nightly, ci, suites, "newjob")
}

func TestNightly_AssemblerStepMutations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, old, repl, want string }{
		{"suites flag dropped", nightlySuitesFlag + " ", "", "lacks " + nightlySuitesFlag},
		{"always dropped", "      - name: Write summary.json\n        if: always()\n", "      - name: Write summary.json\n", "does not run always()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireNightlyMutation(t, tc.old, tc.repl, tc.want)
		})
	}
}

func TestNightly_SummaryUploadMutations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, old, repl, want string }{
		{"artifact renamed", "          name: nightly-summary\n", "          name: summary\n", "upload step lacks name: nightly-summary"},
		{"always dropped", "        if: always()\n        uses: actions/upload-artifact@v7\n", "        uses: actions/upload-artifact@v7\n", "upload step lacks if: always()"},
		{"retention changed", "retention-days: 14", "retention-days: 30", "upload step lacks retention-days: 14"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireNightlyMutation(t, tc.old, tc.repl, tc.want)
		})
	}
}

func TestNightly_NoIssuePermissionOrCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, old, repl string }{
		{"issues permission", "      actions: write\n", "      actions: write\n      issues: write\n"},
		{"gh issue step", "      - name: Upload nightly-summary\n", "      - run: gh issue create --title x --body y\n      - name: Upload nightly-summary\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			requireNightlyMutation(t, tc.old, tc.repl, "names issues")
		})
	}
}

func TestNightly_ActionsWriteOnlyOnTheJob(t *testing.T) {
	t.Parallel()
	nightly, ci, suites := nightlyFiles(t)
	n := mutateOnce(t, nightly, "      actions: write\n", "")
	n = mutateOnce(t, n, "permissions:\n  contents: read\n", "permissions:\n  contents: read\n  actions: write\n")
	got := nightlyRunProblems(n, ci, suites)
	for _, want := range []string{"workflow permissions", "summary permissions"} {
		if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, want) }) {
			t.Errorf("problems %q lack one naming %q", got, want)
		}
	}
}
