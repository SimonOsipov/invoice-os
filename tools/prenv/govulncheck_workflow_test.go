// govulncheck_workflow_test.go pins the govulncheck job of ci.yml and its place in the CI rollup; the nightly reuses its job checks.
package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

const rollupGuard = `if [ "${{ needs.govulncheck.result }}" != "success" ]; then` + "\n" +
	`echo "::error::govulncheck job failed"; exit 1` + "\n" + "fi"

const govulncheckCmd = "go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./..."

var (
	govulncheckPinRE  = regexp.MustCompile(`golang\.org/x/vuln/cmd/govulncheck@v\d+\.\d+\.\d+`)
	govulncheckJSONRE = regexp.MustCompile(`(^|\s)--?(json\b|format(\s+|=)(json|sarif|openvex|sbom)\b)`)
)

var unquote = strings.NewReplacer(`"`, "", "'", "")

// govulncheckCommand returns the pinned govulncheck line of the job's run steps, or "".
func govulncheckCommand(job workflowJob) string {
	cmd := ""
	for _, s := range job.steps() {
		for _, l := range strings.Split(s.keys["run"], "\n") {
			if govulncheckPinRE.MatchString(l) {
				cmd = l
			}
		}
	}
	return cmd
}

// govulncheckJobProblems checks the job shape that ci.yml and the nightly share.
func govulncheckJobProblems(label string, job workflowJob) []string {
	var problems []string
	if _, ok := jobKey(job, "if"); ok {
		problems = append(problems, label+" has an if:")
	}
	if _, ok := jobKey(job, "needs"); ok {
		problems = append(problems, label+" has needs:")
	}
	if strings.Contains(strings.Join(job.lines, "\n"), "continue-on-error") {
		problems = append(problems, label+" has continue-on-error")
	}
	if !slices.Contains(strings.Split(strings.Join(job.lines, "\n"), "\n"), "          go-version-file: go.mod") {
		problems = append(problems, label+" does not use go-version-file: go.mod")
	}
	if !slices.ContainsFunc(job.lines, func(l string) bool { return unquote.Replace(strings.TrimSpace(l)) == "CGO_ENABLED: 0" }) {
		problems = append(problems, label+` lacks CGO_ENABLED: "0" (the production build)`)
	}
	var run string
	for _, s := range job.steps() {
		if _, ok := s.keys["if"]; ok {
			problems = append(problems, label+" has a step-level if:")
		}
		run += s.keys["run"] + "\n"
	}
	if slices.ContainsFunc(job.lines, func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "go-version:") }) {
		problems = append(problems, label+" sets go-version beside the version file")
	}
	if first, _, _ := strings.Cut(strings.TrimSpace(run), "\n"); first != "go version" {
		problems = append(problems, label+" run lacks go version as its first line")
	}
	cmd := govulncheckCommand(job)
	if cmd == "" {
		return append(problems, label+" is not pinned to @vX.Y.Z")
	}
	if govulncheckJSONRE.MatchString(unquote.Replace(cmd)) {
		problems = append(problems, label+" uses -json or -format json|sarif|openvex|sbom (exit 0 on a finding)")
	}
	scan, _, swallowed := strings.Cut(cmd, "||")
	if swallowed {
		problems = append(problems, label+" run swallows the exit code (||)")
	}
	if f := strings.Fields(scan); len(f) == 0 {
		problems = append(problems, label+" run has no command before ||")
	} else if f[len(f)-1] != "./..." {
		problems = append(problems, label+" does not scan ./...")
	}
	return problems
}

func govulncheckProblems(ciYAML string) []string {
	var job, ci *workflowJob
	jobs := workflowJobsOf(ciYAML)
	for i := range jobs {
		switch jobs[i].name {
		case "govulncheck":
			job = &jobs[i]
		case "ci":
			ci = &jobs[i]
		}
	}
	if job == nil {
		return []string{"no govulncheck job"}
	}
	problems := govulncheckJobProblems("govulncheck", *job)
	if ci == nil {
		return append(problems, "no ci job")
	}
	if !slices.Contains(jobList(*ci, "needs"), "govulncheck") {
		problems = append(problems, "ci needs lacks govulncheck")
	}
	var rollup string
	for _, s := range ci.steps() {
		rollup += s.keys["run"] + "\n"
	}
	if !strings.Contains(rollup, rollupGuard) {
		problems = append(problems, `ci rollup lacks the != "success" exit for govulncheck`)
	}
	return problems
}

// mutateOnce replaces the one occurrence of old in yml; it fails the test on any other count.
func mutateOnce(t *testing.T, yml, old, repl string) string {
	t.Helper()
	if n := strings.Count(yml, old); n != 1 {
		t.Fatalf("mutation needle %q occurs %d times in the workflow, want 1", old, n)
	}
	return strings.Replace(yml, old, repl, 1)
}

func requireProblem(t *testing.T, yml, want string) {
	t.Helper()
	for _, p := range govulncheckProblems(yml) {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Errorf("problems %q lack one containing %q", govulncheckProblems(yml), want)
}

func TestCIGovulncheck_RepoIsWired(t *testing.T) {
	yml := readWorkflow(t, "ci.yml")
	if got := govulncheckProblems(yml); len(got) != 0 {
		t.Errorf("govulncheck wiring problems: %q", got)
	}
	if n := len(ciJob(t, "govulncheck").steps()); n < 3 {
		t.Errorf("control: govulncheck has %d steps, want at least 3", n)
	}
}

func TestCIGovulncheck_IfConditionFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "    name: govulncheck\n",
		"    name: govulncheck\n    if: needs.changes.outputs.go == 'true'\n")
	requireProblem(t, yml, "has an if:")
}

func TestCIGovulncheck_NeedsFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "    name: govulncheck\n",
		"    name: govulncheck\n    needs: changes\n")
	requireProblem(t, yml, "has needs:")
}

func TestCIGovulncheck_ContinueOnErrorFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "    name: govulncheck\n",
		"    name: govulncheck\n    continue-on-error: true\n")
	requireProblem(t, yml, "continue-on-error")
}

func TestCIGovulncheck_FormatJSONFails(t *testing.T) {
	for _, format := range []string{"json", "sarif"} {
		yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd,
			"go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -format "+format+" ./...")
		requireProblem(t, yml, "-json or -format")
	}
}

func TestCIGovulncheck_FormatEqualsFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd,
		"go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -format=json ./...")
	requireProblem(t, yml, "-json or -format")
}

func TestCIGovulncheck_JSONFlagFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd,
		"go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -json ./...")
	requireProblem(t, yml, "-json or -format")
}

func TestCIGovulncheck_NarrowTargetFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd,
		"go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./cmd/gateway")
	requireProblem(t, yml, "does not scan ./...")
}

func TestCIGovulncheck_NoGoVersionLineFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "          go version\n          "+govulncheckCmd, "          "+govulncheckCmd)
	requireProblem(t, yml, "lacks go version")
}

func TestCIGovulncheck_FormatTextPasses(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd,
		"go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -format text ./...")
	if got := govulncheckProblems(yml); len(got) != 0 {
		t.Errorf("-format text is the exit-3 mode and must pass, got %q", got)
	}
}

func TestCIGovulncheck_OrTrueFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd, govulncheckCmd+" || true")
	requireProblem(t, yml, "swallows the exit code")
}

func TestCIGovulncheck_NoSetupGoFileFails(t *testing.T) {
	yml := readWorkflow(t, "ci.yml")
	start := strings.Index(yml, "  govulncheck:\n")
	if start < 0 {
		t.Fatal("govulncheck job block not found")
	}
	end := start + strings.Index(yml[start:], "\n  ci:\n")
	if end < start {
		t.Fatal("ci job block not found after govulncheck")
	}
	job := mutateOnce(t, yml[start:end], "go-version-file: go.mod", "go-version: stable")
	requireProblem(t, yml[:start]+job+yml[end:], "go-version-file")
}

func TestCIGovulncheck_CgoEnabledFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "        env:\n          CGO_ENABLED: \"0\"\n", "")
	requireProblem(t, yml, "CGO_ENABLED")
}

func TestCIGovulncheck_CgoEnabledOneFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), `CGO_ENABLED: "0"`, `CGO_ENABLED: "1"`)
	requireProblem(t, yml, "CGO_ENABLED")
}

func TestCIGovulncheck_LeadingOrFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd, "|| "+govulncheckCmd)
	requireProblem(t, yml, "no command before ||")
}

func TestCIGovulncheck_LatestFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "govulncheck@v1.9.0", "govulncheck@latest")
	requireProblem(t, yml, "not pinned")
}

func TestCIGovulncheck_UnversionedFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "govulncheck@v1.9.0", "govulncheck")
	requireProblem(t, yml, "not pinned")
}

func TestCIGovulncheck_RollupNeedsMissingFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"),
		"rls, idp, queue, audit, stale-refs, migration-order, cite-refs, history-rewrite, govulncheck, advisories]",
		"rls, idp, queue, audit, stale-refs, migration-order, cite-refs, history-rewrite, advisories]")
	requireProblem(t, yml, "ci needs lacks")
}

func TestCIGovulncheck_RollupEchoOnlyFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"),
		`echo "::error::govulncheck job failed"; exit 1`, `echo "::error::govulncheck job failed"`)
	requireProblem(t, yml, "rollup lacks")
}

// A cancelled scan must fail CI: four other guards in ci.yml hold != "success", so the needle is the full line.
func TestCIGovulncheck_RollupFailureOnlyFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"),
		`if [ "${{ needs.govulncheck.result }}" != "success" ]; then`,
		`if [ "${{ needs.govulncheck.result }}" = "failure" ]; then`)
	requireProblem(t, yml, "rollup lacks")
}

func TestCIGovulncheck_MissingJobFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  govulncheck:\n    name:", "  govulnscan:\n    name:")
	if got := govulncheckProblems(yml); !slices.Equal(got, []string{"no govulncheck job"}) {
		t.Errorf("problems = %q, want exactly [no govulncheck job]", got)
	}
}

func TestCIGovulncheck_CommentedCommandFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd, "# "+govulncheckCmd)
	requireProblem(t, yml, "not pinned")
}

func TestCIGovulncheck_StepIfFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "      - name: Scan for reachable vulnerabilities\n",
		"      - name: Scan for reachable vulnerabilities\n        if: github.event_name == 'push'\n")
	requireProblem(t, yml, "step-level if:")
}

// setup-go ignores go-version-file when go-version is also set.
func TestCIGovulncheck_GoVersionBesideFileFails(t *testing.T) {
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "          go-version-file: go.mod\n      # Pinned: a scanner",
		"          go-version-file: go.mod\n          go-version: stable\n      # Pinned: a scanner")
	requireProblem(t, yml, "sets go-version")
}

func TestCIGovulncheck_QuotedFormatFails(t *testing.T) {
	for _, format := range []string{`-format "json"`, `-format 'sarif'`, `-format="json"`} {
		yml := mutateOnce(t, readWorkflow(t, "ci.yml"), govulncheckCmd,
			"go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 "+format+" ./...")
		requireProblem(t, yml, "-json or -format")
	}
}

func TestCIGovulncheck_RollupGuardInWrongJobFails(t *testing.T) {
	const moved = "          if [ \"${{ needs.govulncheck.result }}\" != \"success\" ]; then\n" +
		"            echo \"::error::govulncheck job failed\"; exit 1\n          fi\n"
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "          # Unconditional, so a skip would read like a clean scan.\n"+moved, "")
	yml = mutateOnce(t, yml, "          "+govulncheckCmd+"\n", "          "+govulncheckCmd+"\n"+moved)
	requireProblem(t, yml, "rollup lacks")
}
